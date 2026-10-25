package main

// Ausfallschutz mit zwei Servern (docs/superpowers/specs/2026-10-25-pdh-ha-kern-design.md):
// Der Agent installiert pdh-ha auf dem ersten Knoten (§2.4) und fragt vor jeder
// Sicherung aufs Netzlaufwerk, ob dieser Knoten aktiv ist (§4.4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	haUnsupportedMsg = "Ausfallschutz mit zwei Servern nur bei systemd-Installationen (Docker ist das Testsystem)"
	haUnreachableMsg = "pdh-ha nicht erreichbar – Sicherung pausiert"
	haPassiveMsg     = "passiver Knoten"
	haLogLines       = 40
)

func haSocketPath() string {
	if v := strings.TrimSpace(os.Getenv("PDH_HA_SOCKET")); v != "" {
		return v
	}
	return "/run/pdh-ha/api.sock"
}

func haEtcDir() string {
	if v := strings.TrimSpace(os.Getenv("PDH_HA_ETC")); v != "" {
		return v
	}
	return "/etc/pdh/ha"
}

// drIsActive: läuft die Netzlaufwerk-Sicherung auf diesem Knoten? Ohne pdh-ha
// (keine node.json) oder nie eingerichtet: ja. Eingerichtet, aber Dienst nicht
// erreichbar oder Konfiguration nicht lesbar: nein, mit Fehler.
func (a *updateAgent) drIsActive(ctx context.Context) (bool, error) {
	if _, err := os.Stat(filepath.Join(haEtcDir(), "node.json")); errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	sock := haSocketPath()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://pdh-ha/v1/status", nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("pdh-ha: HTTP %d", resp.StatusCode)
	}
	var st struct {
		Configured bool   `json:"configured"`
		Error      string `json:"error"`
		Self       struct {
			Role string `json:"role"`
		} `json:"self"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return false, err
	}
	switch {
	case st.Error != "":
		return false, errors.New(st.Error)
	case !st.Configured:
		return true, nil
	}
	return st.Self.Role == "aktiv", nil
}

// drCheckActive setzt Pause bzw. Fehler „ha“ für den Status; true = sichern.
func (a *updateAgent) drCheckActive(ctx context.Context) bool {
	active, err := a.drIsActive(ctx)
	paused := ""
	if err == nil && !active {
		paused = haPassiveMsg
	}
	a.dr.mu.Lock()
	a.dr.Paused = paused
	a.dr.mu.Unlock()
	if err != nil {
		a.drFail("ha", errors.New(haUnreachableMsg))
		return false
	}
	a.drOK("ha")
	return active
}

// ── Installation (erster Knoten) ───────────────────────────────────────────

type haInstallStatus struct {
	Running bool   `json:"running"`
	Done    bool   `json:"done"`
	Error   string `json:"error"`
	Log     string `json:"log"`
}

// haJob: Installationslauf nur im Speicher, immer nur einer.
type haJob struct {
	mu      sync.Mutex
	running bool
	done    bool
	err     string
	log     []byte // nur das Ende zählt (letzte 40 Zeilen)
}

func (j *haJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, p...)
	if len(j.log) > 64<<10 {
		j.log = append([]byte(nil), j.log[len(j.log)-32<<10:]...)
	}
	return len(p), nil
}

func (j *haJob) status() haInstallStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	lines := strings.Split(strings.TrimRight(string(j.log), "\n"), "\n")
	if len(lines) > haLogLines {
		lines = lines[len(lines)-haLogLines:]
	}
	return haInstallStatus{Running: j.running, Done: j.done, Error: j.err, Log: strings.Join(lines, "\n")}
}

// haInstallScript läuft per systemd-run außerhalb des Agenten (eigener cgroup,
// /etc beschreibbar): ein Neustart des Agenten bricht die Installation nicht ab.
// Werte nur über Umgebungsvariablen.
const haInstallScript = `set -euo pipefail
if command -v systemd-run >/dev/null 2>&1; then
    exec systemd-run --wait --pipe --collect --quiet --unit="pdh-ha-install-$(date +%s)" \
        --setenv=PDH_HA_REPO --setenv=PDH_HA_ADDRS bash -c "$PDH_HA_SCRIPT"
fi
exec bash -c "$PDH_HA_SCRIPT"
`

// haInstallInner: Installation und Einrichtung; setup nie zweimal.
const haInstallInner = `set -euo pipefail
bash "$PDH_HA_REPO/scripts/install-pdh-ha.sh" "$PDH_HA_REPO"
for _ in $(seq 1 40); do [[ -S /run/pdh-ha/api.sock ]] && break; sleep 0.25; done
if /usr/local/bin/pdh-ha status --json 2>/dev/null | grep -q '"configured":true'; then
    echo "pdh-ha ist bereits eingerichtet – Einrichtung übersprungen."
else
    /usr/local/bin/pdh-ha setup --addr "$PDH_HA_ADDRS"
fi
/usr/local/bin/pdh-ha status
`

func (a *updateAgent) haRunFn() func(context.Context, string, map[string]string, io.Writer) error {
	if a.haRun != nil {
		return a.haRun
	}
	return func(ctx context.Context, script string, env map[string]string, out io.Writer) error {
		cmd := a.hostCommandEnv(ctx, false, script, env)
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	}
}

// haInstallHandler: POST /v1/ha/install {"addrs":["192.168.1.10","10.8.0.1"]} → 202
func (a *updateAgent) haInstallHandler(w http.ResponseWriter, r *http.Request) {
	if !a.drSupported() {
		drRefuse(w, haUnsupportedMsg)
		return
	}
	var req struct {
		Addrs []string `json:"addrs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige Anfrage"})
		return
	}
	if len(req.Addrs) == 0 || len(req.Addrs) > 8 {
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "1 bis 8 Adressen angeben"})
		return
	}
	for _, s := range req.Addrs {
		if net.ParseIP(s) == nil {
			writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ungültige IP-Adresse: " + s})
			return
		}
	}
	j := &a.ha
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		writeMountJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "Die Installation läuft bereits"})
		return
	}
	j.running, j.done, j.err = true, false, ""
	j.log = nil
	j.mu.Unlock()
	env := map[string]string{"PDH_HA_REPO": a.repoDir, "PDH_HA_ADDRS": strings.Join(req.Addrs, ","), "PDH_HA_SCRIPT": haInstallInner}
	run := a.haRunFn()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := run(ctx, haInstallScript, env, j)
		j.mu.Lock()
		j.running, j.done = false, true
		if err != nil {
			j.err = "Installation fehlgeschlagen: " + err.Error()
		}
		j.mu.Unlock()
	}()
	writeMountJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// haInstallStatusHandler: GET /v1/ha/install
func (a *updateAgent) haInstallStatusHandler(w http.ResponseWriter, _ *http.Request) {
	if !a.drSupported() {
		drRefuse(w, haUnsupportedMsg)
		return
	}
	drJSON(w, a.ha.status())
}

func drPauseReason(st drStatus) string {
	if st.Paused != "" {
		return st.Paused
	}
	return haUnreachableMsg
}
