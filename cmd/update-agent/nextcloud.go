package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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
	return a.hostCommandEnv(ctx, detached, script, nil)
}

// hostCommandEnv: wie hostCommand, mit Umgebungsvariablen für das Skript
// (z. B. Geheimnisse – so stehen sie weder im Skripttext noch in den
// Argumenten von docker run).
func (a *updateAgent) hostCommandEnv(ctx context.Context, detached bool, script string, env map[string]string) *exec.Cmd {
	var extra []string
	for k, v := range env {
		extra = append(extra, k+"="+v)
	}
	if a.mode == "systemd" {
		cmd := exec.CommandContext(ctx, "nsenter", "-t", "1", "-m", "-u", "-i", "-n", "--", "bash", "-c", script)
		if len(extra) > 0 {
			cmd.Env = append(os.Environ(), extra...)
		}
		return cmd
	}
	args := []string{"run", "--privileged", "--pid=host", "--net=host", "--entrypoint", "nsenter"}
	if detached {
		args = append(args, "--detach", "--name", ncJobName)
	} else {
		args = append(args, "--rm")
	}
	for k := range env {
		args = append(args, "-e", k) // Wert kommt aus cmd.Env, nicht aus der Befehlszeile
	}
	args = append(args, "pdh-updater:local", "-t", "1", "-m", "-u", "-i", "-n", "--", "bash", "-c", script)
	cmd := exec.CommandContext(ctx, "docker", args...)
	if len(extra) > 0 {
		cmd.Env = append(os.Environ(), extra...)
	}
	return cmd
}

// ── Nextcloud-Anmeldung mit Microsoft (user_oidc) ─────────────────────────
// Das PDH schickt Tenant, Client-ID und Secret aus seiner Datenbank; der
// Agent richtet damit die App user_oidc ein (wie scripts/configure-nextcloud-microsoft-login.sh).

var (
	ncGUIDRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	ncProviderRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,40}$`)
)

type ncOIDCRequest struct {
	Dir           string `json:"dir"`
	TenantID      string `json:"tenant_id"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	Provider      string `json:"provider"`
	AutoProvision bool   `json:"auto_provision"`
	OnlyMicrosoft bool   `json:"only_microsoft"`
}

// ncOIDCScript: occ-Befehle für user_oidc. Das Secret kommt aus $PDH_OIDC_SECRET.
func ncOIDCScript(req ncOIDCRequest) (string, error) {
	if !ncDirRe.MatchString(req.Dir) || strings.Contains(req.Dir, "..") {
		return "", errors.New("ungültiges Nextcloud-Verzeichnis")
	}
	if !ncGUIDRe.MatchString(req.TenantID) || !ncGUIDRe.MatchString(req.ClientID) {
		return "", errors.New("Tenant-ID und Client-ID müssen GUIDs sein (Server-Einstellungen → Microsoft 365)")
	}
	if req.Provider == "" {
		req.Provider = "Microsoft"
	}
	if !ncProviderRe.MatchString(req.Provider) {
		return "", errors.New("ungültiger Name für den Anmeldeknopf")
	}
	multi := "1"
	if req.OnlyMicrosoft {
		multi = "0"
	}
	return `set -e
cd '` + req.Dir + `' 2>/dev/null || { echo "Nextcloud-Verzeichnis ` + req.Dir + ` nicht gefunden"; exit 3; }
[ -f occ ] || { echo "occ nicht gefunden in ` + req.Dir + `"; exit 3; }
[ -n "$PDH_OIDC_SECRET" ] || { echo "Client-Secret fehlt"; exit 4; }
U="$(stat -c %U occ)"
occ() { runuser -u "$U" -- php occ "$@"; }
echo "### App user_oidc"
occ app:list --output=json | grep -q '"user_oidc"' || occ app:install user_oidc
occ app:enable user_oidc
echo "### Anbieter ` + req.Provider + `"
occ user_oidc:provider '` + req.Provider + `' \
  --clientid='` + req.ClientID + `' \
  --clientsecret="$PDH_OIDC_SECRET" \
  --discoveryuri='https://login.microsoftonline.com/` + req.TenantID + `/v2.0/.well-known/openid-configuration' \
  --scope='openid email profile' \
  --unique-uid=0 --mapping-uid=email --mapping-display-name=name --mapping-email=email >/dev/null
echo "### Konto-Regeln"
occ config:system:set user_oidc auto_provision --value=` + fmt.Sprint(req.AutoProvision) + ` --type=boolean >/dev/null
occ config:system:set user_oidc soft_auto_provision --value=true --type=boolean >/dev/null
occ config:app:set user_oidc allow_multiple_user_backends --value=` + multi + ` >/dev/null
echo "### Adresse"
echo "URL=$(occ config:system:get overwrite.cli.url 2>/dev/null || true)"
echo "### fertig"
`, nil
}

// ── Dienstkonto des PDH in Nextcloud (z. B. pdhNCadmin) ───────────────────
// Das PDH verwaltet das Passwort und hat Vorrang: Konto anlegen bzw. Passwort
// setzen, aktivieren und in die Gruppe admin aufnehmen. Das Passwort kommt
// über OC_PASS (occ --password-from-env), nicht über die Befehlszeile.

var ncUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

type ncServiceRequest struct {
	Dir      string `json:"dir"`
	User     string `json:"user"`
	Password string `json:"password"`
}

func ncServiceScript(req ncServiceRequest) (string, error) {
	if !ncDirRe.MatchString(req.Dir) || strings.Contains(req.Dir, "..") {
		return "", errors.New("ungültiges Nextcloud-Verzeichnis")
	}
	if !ncUserRe.MatchString(req.User) {
		return "", errors.New("ungültiger Benutzername (Buchstaben, Ziffern, _ . @ -)")
	}
	return `set -e
cd '` + req.Dir + `' 2>/dev/null || { echo "Nextcloud-Verzeichnis ` + req.Dir + ` nicht gefunden"; exit 3; }
[ -f occ ] || { echo "occ nicht gefunden in ` + req.Dir + `"; exit 3; }
[ -n "$OC_PASS" ] || { echo "Passwort fehlt"; exit 4; }
U="$(stat -c %U occ)"
occ() { runuser -u "$U" -- php occ "$@"; }
if occ user:info '` + req.User + `' >/dev/null 2>&1; then
  echo "### Passwort setzen"
  occ user:resetpassword --password-from-env '` + req.User + `'
  occ user:enable '` + req.User + `' >/dev/null 2>&1 || true
  echo "STATUS=updated"
else
  echo "### Konto anlegen"
  occ user:add --password-from-env --display-name='PDH (Dienstkonto)' --group=admin '` + req.User + `'
  echo "STATUS=created"
fi
occ group:adduser admin '` + req.User + `' >/dev/null 2>&1 || true
echo "### fertig"
`, nil
}

// ncServiceHandler: POST /v1/nextcloud/service-account
func (a *updateAgent) ncServiceHandler(w http.ResponseWriter, r *http.Request) {
	var req ncServiceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if req.Dir == "" {
		req.Dir = "/var/www/nextcloud"
	}
	script, err := ncServiceScript(req)
	if err == nil && len(req.Password) < 12 {
		err = errors.New("Passwort zu kurz")
	}
	if err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := a.hostCommandEnv(ctx, false, script, map[string]string{"OC_PASS": req.Password}).CombinedOutput()
	text := strings.ReplaceAll(strings.TrimSpace(string(out)), req.Password, "***")
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": lastLine(text), "output": text})
		return
	}
	status := "updated"
	if strings.Contains(text, "STATUS=created") {
		status = "created"
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status, "output": text})
}

// ncOIDCHandler: POST /v1/nextcloud/oidc → richtet die Anmeldung mit Microsoft ein.
func (a *updateAgent) ncOIDCHandler(w http.ResponseWriter, r *http.Request) {
	var req ncOIDCRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if req.Dir == "" {
		req.Dir = "/var/www/nextcloud"
	}
	script, err := ncOIDCScript(req)
	if err == nil && strings.TrimSpace(req.ClientSecret) == "" {
		err = errors.New("Client-Secret fehlt (Server-Einstellungen → Microsoft 365)")
	}
	if err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	out, err := a.hostCommandEnv(ctx, false, script, map[string]string{"PDH_OIDC_SECRET": req.ClientSecret}).CombinedOutput()
	text := strings.ReplaceAll(strings.TrimSpace(string(out)), req.ClientSecret, "***")
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": lastLine(text), "output": text})
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "output": text})
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
