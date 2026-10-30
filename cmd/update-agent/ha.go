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

// haGateEnforced: Sperre der Netzlaufwerk-Sicherung auf dem passiven Knoten –
// ab Teil 3 (Datenbank-Spiegel) scharf. Tests können sie abschalten.
var haGateEnforced = true

// haPauseAlarm: so lange darf pdh-ha unerreichbar sein, bevor die Pause als Fehler gilt.
const haPauseAlarm = 15 * time.Minute

// haSelf: was der Agent über diesen Knoten von pdh-ha wissen muss.
type haSelf struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	PG       string `json:"pg"`
	Timeline uint32 `json:"timeline"`
}

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
	_, active, err := a.drHASelf(ctx)
	return active, err
}

// drHASelf: Zustand laut pdh-ha. Aktiv heißt: Rolle aktiv und die Datenbank
// schreibbar (primary) – oder ohne Datenbank-Spiegel (unknown).
func (a *updateAgent) drHASelf(ctx context.Context) (haSelf, bool, error) {
	if _, err := os.Stat(filepath.Join(haEtcDir(), "node.json")); errors.Is(err, os.ErrNotExist) {
		return haSelf{}, true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var st struct {
		Configured bool   `json:"configured"`
		Error      string `json:"error"`
		Self       haSelf `json:"self"`
	}
	code, err := haDo(ctx, http.MethodGet, "/v1/status", nil, &st)
	if err != nil {
		return haSelf{}, false, err
	}
	if code != http.StatusOK {
		return haSelf{}, false, fmt.Errorf("pdh-ha: HTTP %d", code)
	}
	switch {
	case st.Error != "":
		return haSelf{}, false, errors.New(st.Error)
	case !st.Configured:
		return st.Self, true, nil
	}
	writable := st.Self.PG == "" || st.Self.PG == "unknown" || st.Self.PG == "primary"
	return st.Self, st.Self.Role == "aktiv" && writable, nil
}

// drCheckActive setzt Pause bzw. Fehler „ha“ für den Status; true = sichern.
// Nur der aktive Knoten mit schreibbarer Datenbank sichert. Ist pdh-ha
// eingerichtet, aber nicht erreichbar, pausiert die Sicherung; nach 15 min
// zusätzlich Fehler (Alarm). Nach einem Wechsel der Timeline (Hochstufung)
// steht sofort eine Grundsicherung an.
func (a *updateAgent) drCheckActive(ctx context.Context) bool {
	self, active, err := a.drHASelf(ctx)
	paused, advice := "", ""
	switch {
	case err != nil:
		advice = "Beobachtung: würde pausieren – pdh-ha nicht erreichbar"
	case !active:
		paused, advice = haPassiveMsg, "Beobachtung: würde pausieren – "+haPassiveMsg
	}
	if !haGateEnforced {
		a.dr.mu.Lock()
		a.dr.Paused, a.dr.Advice = "", advice
		a.dr.mu.Unlock()
		a.drOK("ha")
		return true
	}
	now := time.Now()
	a.dr.mu.Lock()
	a.dr.Paused, a.dr.Advice = paused, ""
	if err != nil {
		if a.dr.UnreachSince.IsZero() {
			a.dr.UnreachSince = now
		}
	} else {
		a.dr.UnreachSince = time.Time{}
	}
	since := a.dr.UnreachSince
	if active && self.ID != "" {
		a.dr.LastNode = self.ID
	}
	if active && self.Timeline > 0 {
		if a.dr.LastTimeline != 0 && self.Timeline != a.dr.LastTimeline {
			a.dr.BaseNeeded = true // neue Timeline: Zeitpunkt-Wiederherstellung braucht eine neue Grundsicherung
		}
		a.dr.LastTimeline = self.Timeline
	}
	a.dr.mu.Unlock()
	if err != nil {
		a.drFail("ha", errors.New(haUnreachableMsg))
		if now.Sub(since) >= haPauseAlarm {
			a.drFail("ha-pause", errors.New("Netzlaufwerk-Sicherung seit über 15 Minuten pausiert – pdh-ha prüfen"))
		}
		return false
	}
	a.drOK("ha")
	a.drOK("ha-pause")
	return active
}

// ── Installation (erster Knoten) ───────────────────────────────────────────

type haInstallStatus struct {
	OK      bool   `json:"ok"` // für agentJSON im PDH
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
	return haInstallStatus{OK: true, Running: j.running, Done: j.done, Error: j.err, Log: strings.Join(lines, "\n")}
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
