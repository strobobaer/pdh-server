package main

// ESPPDH – Python-Umgebung mit ESPHome einrichten (im Auftrag des PDH, Server-Einstellungen →
// Reiter „ESPPDH“ → „Umgebung einrichten“).
//
// Das PDH läuft ohne Root-Rechte. Der Agent führt scripts/install-esphome.sh auf dem Host aus:
// venv unter /opt/pdh-esphome, ESPHome in der Version aus scripts/esphome.version,
// Arbeitsordner /var/lib/pdh-esphome für den Dienstbenutzer. Das Skript ist mehrfach
// ausführbar; install_update.sh ruft es bei jedem Update auf, wenn die Umgebung schon da ist.
// Im Docker-Betrieb gibt es ESPPDH nicht (das Programm-Abbild enthält kein Python).
//
// Die Einrichtung dauert einige Minuten und läuft deshalb im Hintergrund (Muster
// /v1/ha/install): POST startet, GET liefert den Stand; das PDH fragt alle 2 s nach.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"pdh/internal/modules/esppdh"
)

const (
	esppdhDockerMsg = "ESPPDH gibt es im Docker-Betrieb nicht"
	// esppdhInstallTimeout: pip lädt ESPHome samt Abhängigkeiten aus dem Internet.
	esppdhInstallTimeout = 20 * time.Minute
	esppdhTailLines      = 15
)

// esppdhStatus: Stand der Einrichtung (GET /v1/esppdh/install).
type esppdhStatus struct {
	OK      bool   `json:"ok"` // für agentJSON im PDH
	Running bool   `json:"running"`
	Done    bool   `json:"done"`              // ein Lauf ist fertig (seit dem Start des Agenten)
	Version string `json:"version,omitempty"` // eingerichtete Version, wenn der Lauf geklappt hat
	Error   string `json:"error,omitempty"`   // Meldung des fehlgeschlagenen Laufs samt Ende der Ausgabe
	Target  string `json:"target,omitempty"`  // Version des Kandidaten, leer = Grundumgebung
}

// esppdhJob: Einrichtung nur im Speicher, immer nur eine (das Skript sperrt zusätzlich per
// flock gegen ein gleichzeitiges Update).
type esppdhJob struct {
	mu      sync.Mutex
	running bool
	done    bool
	version string
	err     string
	target  string // Kandidat dieses Laufs (leer = Grundumgebung)
}

func (j *esppdhJob) status() esppdhStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return esppdhStatus{OK: true, Running: j.running, Done: j.done, Version: j.version, Error: j.err, Target: j.target}
}

// esppdhInstallScript läuft im Host-Namensraum, per systemd-run außerhalb von Agent-cgroup
// und -Sandbox: Ein Neustart des Agenten bricht die Einrichtung nicht ab, und apt darf nach
// /usr schreiben. Der Pfad zum Repository kommt nur über die Umgebung.
const esppdhInstallScript = `set -euo pipefail
if command -v systemd-run >/dev/null 2>&1; then
    exec systemd-run --wait --pipe --collect --quiet --unit="pdh-esphome-install-$(date +%s)" \
        --setenv=PDH_ESPHOME_REPO --setenv=PDH_ESPHOME_VERSION bash "$PDH_ESPHOME_REPO/scripts/install-esphome.sh"
fi
exec bash "$PDH_ESPHOME_REPO/scripts/install-esphome.sh"
`

// esppdhRunFn: Ausführung des Skripts – Ausgabe (stdout und stderr) und Fehler.
func (a *updateAgent) esppdhRunFn() func(context.Context, string, map[string]string) (string, error) {
	if a.esppdhRun != nil {
		return a.esppdhRun
	}
	return func(ctx context.Context, script string, env map[string]string) (string, error) {
		out, err := a.hostCommandEnv(ctx, false, script, env).CombinedOutput()
		return string(out), err
	}
}

// esppdhVersion: letzte Zeile „Version: …“ der Ausgabe (das Skript endet mit „esphome version“).
func esppdhVersion(out string) string {
	v := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "Version:"); ok {
			v = strings.TrimSpace(rest)
		}
	}
	return v
}

// esppdhTail: die letzten Zeilen der Ausgabe für die Fehlermeldung.
func esppdhTail(out string) []string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > esppdhTailLines {
		lines = lines[len(lines)-esppdhTailLines:]
	}
	return lines
}

// esppdhResult: Ergebnis eines Laufs – Version oder Fehlermeldung. Die letzte Zeile der
// Ausgabe (die Meldung des Skripts) steht vorn, darunter das Ende der Ausgabe.
func esppdhResult(out string, err error) (version, msg string) {
	tail := esppdhTail(out)
	if err == nil {
		if v := esppdhVersion(out); v != "" {
			return v, ""
		}
		return "", "Einrichtung ohne Ergebnis – ESPHome nennt keine Version.\n\nLetzte Ausgabe:\n" + strings.Join(tail, "\n")
	}
	last := strings.TrimSpace(tail[len(tail)-1])
	if last == "" {
		last = err.Error()
	}
	return "", "Einrichtung fehlgeschlagen: " + last + "\n\nLetzte Ausgabe (" + err.Error() + "):\n" + strings.Join(tail, "\n")
}

// esppdhInstallHandler: POST /v1/esppdh/install {} oder {"version":"2026.11.1"} →
// {"ok":true,"running":true}, sofort. Ohne Version wird die Grundumgebung eingerichtet, mit
// Version ein Kandidat unter <VersionsDir>/<Version>. Läuft schon eine Einrichtung, startet
// keine zweite.
func (a *updateAgent) esppdhInstallHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": esppdhDockerMsg})
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "ungültiger Rumpf"})
				return
			}
		}
	}
	if req.Version != "" {
		if !esppdh.StableVersionRe.MatchString(req.Version) {
			writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "ungültige ESPHome-Version (erwartet JJJJ.M.P)"})
			return
		}
		// nichts einrichten, was sich nie einschalten ließe (gleiche Regel wie beim Umschalten)
		a.vmu.Lock()
		err := upgradeAllowed(req.Version)
		a.vmu.Unlock()
		if err != nil {
			writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	j := &a.esppdh
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
		return
	}
	j.running, j.done, j.version, j.err, j.target = true, false, "", "", req.Version
	j.mu.Unlock()
	run := a.esppdhRunFn()
	env := map[string]string{"PDH_ESPHOME_REPO": a.repoDir}
	if req.Version != "" {
		env["PDH_ESPHOME_VERSION"] = req.Version
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), esppdhInstallTimeout)
		defer cancel()
		a.pruneVersionOrphans(ctx) // verwaiste Kandidaten weg, der eigene Auftrag ist geschützt
		out, err := run(ctx, esppdhInstallScript, env)
		v, msg := esppdhResult(out, err)
		j.mu.Lock()
		j.running, j.done, j.version, j.err = false, true, v, msg
		j.mu.Unlock()
	}()
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
}

// esppdhStatusHandler: GET /v1/esppdh/install → Stand der Einrichtung.
func (a *updateAgent) esppdhStatusHandler(w http.ResponseWriter, _ *http.Request) {
	if a.mode != "systemd" {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": esppdhDockerMsg})
		return
	}
	writeMountJSON(w, http.StatusOK, a.esppdh.status())
}
