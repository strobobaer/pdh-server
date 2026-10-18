package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Nextcloud-Updates (Core-Einstellungen → Kachel „Nextcloud-Updates“), wenn
// Nextcloud auf demselben Server liegt: prüfen (occ update:check), alle Apps
// aktualisieren (occ app:update --all) und System-Update (updater.phar).
// Die Befehle laufen auf dem Host als Eigentümer von occ (meist www-data);
// im Docker-Modus über einen kurzlebigen Hilfscontainer mit --pid=host.

var ncDirRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)

const ncJobName = "pdh-nextcloud-job"

// ncScript: Shell-Skript für den Host (dir ist geprüft).
func ncScript(dir, what string) (string, error) {
	if !ncDirRe.MatchString(dir) || strings.Contains(dir, "..") {
		return "", errors.New("ungültiges Nextcloud-Verzeichnis")
	}
	head := `set -e
cd '` + dir + `' 2>/dev/null || { echo "Nextcloud-Verzeichnis ` + dir + ` nicht gefunden"; exit 3; }
[ -f occ ] || { echo "occ nicht gefunden in ` + dir + `"; exit 3; }
U="$(stat -c %U occ)"
occ() { runuser -u "$U" -- php occ "$@"; }
`
	switch what {
	case "check":
		return head + `echo "### status"
occ status --output=json || true
echo "### update:check"
occ update:check || true
`, nil
	case "apps":
		return head + `echo "### Apps aktualisieren"
occ app:update --all
echo "### fertig"
`, nil
	case "system", "all":
		return head + `echo "### System-Update (updater.phar)"
runuser -u "$U" -- php updater/updater.phar --no-interaction
echo "### Datenbank-Indizes"
occ db:add-missing-indices --no-interaction || true
occ maintenance:mode --off || true
echo "### Apps aktualisieren"
occ app:update --all || true
echo "### fertig"
occ status || true
`, nil
	}
	return "", errors.New("unbekannte Aktion")
}

// hostCommand: Befehl im Host-Namensraum (Docker: Hilfscontainer).
func (a *updateAgent) hostCommand(ctx context.Context, detached bool, script string) *exec.Cmd {
	if a.mode == "systemd" {
		return exec.CommandContext(ctx, "nsenter", "-t", "1", "-m", "-u", "-i", "-n", "--", "bash", "-c", script)
	}
	args := []string{"run", "--privileged", "--pid=host", "--net=host", "--entrypoint", "nsenter"}
	if detached {
		args = append(args, "--detach", "--name", ncJobName)
	} else {
		args = append(args, "--rm")
	}
	args = append(args, "pdh-updater:local", "-t", "1", "-m", "-u", "-i", "-n", "--", "bash", "-c", script)
	return exec.CommandContext(ctx, "docker", args...)
}

var (
	ncMu     sync.Mutex
	ncStatus jobStatus // systemd-Modus (Docker: Status aus dem Job-Container)
)

type ncRequest struct {
	Dir  string `json:"dir"`
	What string `json:"what"`
}

func decodeNC(w http.ResponseWriter, r *http.Request) (ncRequest, bool) {
	var req ncRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return req, false
	}
	if req.Dir == "" {
		req.Dir = "/var/www/nextcloud"
	}
	return req, true
}

// ncCheckHandler: POST /v1/nextcloud/check {"dir": "..."} → Ausgabe von occ status/update:check
func (a *updateAgent) ncCheckHandler(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeNC(w, r)
	if !ok {
		return
	}
	script, err := ncScript(req.Dir, "check")
	if err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, err := a.hostCommand(ctx, false, script).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": lastLine(text), "output": text})
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "output": text})
}

// ncUpdateHandler: POST /v1/nextcloud/update {"dir": "...", "what": "apps|system|all"} → 202
func (a *updateAgent) ncUpdateHandler(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeNC(w, r)
	if !ok {
		return
	}
	if req.What != "apps" && req.What != "system" && req.What != "all" {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "what: apps, system oder all"})
		return
	}
	script, err := ncScript(req.Dir, req.What)
	if err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if st, _ := a.ncJobStatus(); st.Running {
		writeMountJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "Ein Nextcloud-Update läuft bereits"})
		return
	}
	if a.mode == "systemd" {
		ncMu.Lock()
		ncStatus = jobStatus{Running: true, StartedAt: time.Now().UTC()}
		ncMu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			buf := &limitedBuffer{}
			cmd := a.hostCommand(ctx, false, script)
			cmd.Stdout, cmd.Stderr = buf, buf
			err := cmd.Run()
			if err != nil {
				fmt.Fprintf(buf, "\nFehler: %v\n", err)
			}
			ncMu.Lock()
			ncStatus = jobStatus{Success: err == nil, Output: buf.String(), StartedAt: ncStatus.StartedAt, FinishedAt: time.Now().UTC()}
			ncMu.Unlock()
		}()
	} else {
		_ = exec.Command("docker", "rm", "-f", ncJobName).Run()
		if out, err := a.hostCommand(context.Background(), true, script).CombinedOutput(); err != nil {
			writeMountJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Job nicht gestartet: " + strings.TrimSpace(string(out))})
			return
		}
	}
	writeMountJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *updateAgent) ncJobStatus() (jobStatus, error) {
	if a.mode == "systemd" {
		ncMu.Lock()
		defer ncMu.Unlock()
		return ncStatus, nil
	}
	st := jobStatus{}
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Status}}|{{.State.ExitCode}}|{{.State.StartedAt}}|{{.State.FinishedAt}}", ncJobName).Output()
	if err != nil {
		return st, err
	}
	f := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(f) != 4 {
		return st, errors.New("unerwarteter Job-Status")
	}
	st.Running = f[0] == "running" || f[0] == "created"
	st.Success = f[0] == "exited" && f[1] == "0"
	st.StartedAt, _ = time.Parse(time.RFC3339Nano, f[2])
	st.FinishedAt, _ = time.Parse(time.RFC3339Nano, f[3])
	logs := &limitedBuffer{}
	cmd := exec.Command("docker", "logs", "--tail", "300", ncJobName)
	cmd.Stdout, cmd.Stderr = logs, logs
	_ = cmd.Run()
	st.Output = logs.String()
	return st, nil
}

// ncStatusHandler: GET /v1/nextcloud/status → Job-Status
func (a *updateAgent) ncStatusHandler(w http.ResponseWriter, _ *http.Request) {
	st, err := a.ncJobStatus()
	if err != nil {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "status": jobStatus{}})
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "status": st})
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
