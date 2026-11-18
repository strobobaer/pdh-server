package main

// ESPPDH – Python-Umgebung mit ESPHome einrichten (im Auftrag des PDH, Server-Einstellungen →
// Reiter „ESPPDH“ → „Umgebung einrichten“).
//
// Das PDH läuft ohne Root-Rechte. Der Agent führt scripts/install-esphome.sh auf dem Host aus:
// venv unter /opt/pdh-esphome, ESPHome in der Version aus scripts/esphome.version,
// Arbeitsordner /var/lib/pdh-esphome für den Dienstbenutzer. Das Skript ist mehrfach
// ausführbar; install_update.sh ruft es bei jedem Update auf, wenn die Umgebung schon da ist.
// Im Docker-Betrieb gibt es ESPPDH nicht (das Programm-Abbild enthält kein Python).

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	esppdhDockerMsg = "ESPPDH gibt es im Docker-Betrieb nicht"
	// esppdhInstallTimeout: pip lädt ESPHome samt Abhängigkeiten aus dem Internet.
	esppdhInstallTimeout = 20 * time.Minute
	esppdhTailLines      = 15
)

// esppdhBusy: nur eine Einrichtung zur Zeit (das Skript sperrt zusätzlich per flock gegen
// ein gleichzeitiges Update).
var esppdhBusy sync.Mutex

// esppdhInstallScript läuft im Host-Namensraum, per systemd-run außerhalb von Agent-cgroup
// und -Sandbox: Ein Neustart des Agenten bricht die Einrichtung nicht ab, und apt darf nach
// /usr schreiben. Der Pfad zum Repository kommt nur über die Umgebung.
const esppdhInstallScript = `set -euo pipefail
if command -v systemd-run >/dev/null 2>&1; then
    exec systemd-run --wait --pipe --collect --quiet --unit="pdh-esphome-install-$(date +%s)" \
        --setenv=PDH_ESPHOME_REPO bash "$PDH_ESPHOME_REPO/scripts/install-esphome.sh"
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
func esppdhTail(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > esppdhTailLines {
		lines = lines[len(lines)-esppdhTailLines:]
	}
	return strings.Join(lines, "\n")
}

// esppdhInstallHandler: POST /v1/esppdh/install {} → {"ok":true,"version":"…"}.
// Wartet, bis die Einrichtung fertig ist (einige Minuten).
func (a *updateAgent) esppdhInstallHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": esppdhDockerMsg})
		return
	}
	if !esppdhBusy.TryLock() {
		writeMountJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "Die Einrichtung läuft bereits"})
		return
	}
	defer esppdhBusy.Unlock()
	// Die Antwort kommt erst nach der Einrichtung – länger als die übliche Schreibfrist.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(esppdhInstallTimeout + time.Minute))
	// Bricht das PDH die Anfrage ab, läuft die Einrichtung trotzdem zu Ende.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), esppdhInstallTimeout)
	defer cancel()
	out, err := a.esppdhRunFn()(ctx, esppdhInstallScript, map[string]string{"PDH_ESPHOME_REPO": a.repoDir})
	if err != nil {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Einrichtung fehlgeschlagen (" + err.Error() + "):\n" + esppdhTail(out)})
		return
	}
	v := esppdhVersion(out)
	if v == "" {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Einrichtung ohne Ergebnis – ESPHome nennt keine Version:\n" + esppdhTail(out)})
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v})
}
