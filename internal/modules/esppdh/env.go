// Package esppdh: ESP-Geräte im PDH (ESPPDH). Reine Logik ohne Datenbank und ohne HTTP –
// Seiten, Rechte und Speicherung liegen in internal/web/esppdh*.go.
//
// Die Firmware baut ESPHome aus einer eigenen Python-Umgebung (venv), die der Update-Agent
// mit scripts/install-esphome.sh einrichtet. Sie ist ein Zusatz: Fehlt sie, läuft das PDH
// unverändert; nur ESPPDH zeigt einen Hinweis.
package esppdh

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pdh"
)

const (
	// VenvDir: Python-Umgebung mit ESPHome (überschreibbar mit PDH_ESPHOME_DIR).
	VenvDir = "/opt/pdh-esphome"
	// WorkDir: Arbeitsordner der Bauläufe samt Zwischenspeicher von PlatformIO
	// (überschreibbar mit PDH_ESPHOME_WORKDIR); gehört dem Dienstbenutzer pdh.
	WorkDir = "/var/lib/pdh-esphome"
)

// envTimeout: so lange darf „esphome version“ höchstens dauern (Tests verkürzen es).
var envTimeout = 10 * time.Second

// versionCmd baut den Aufruf „<venv>/bin/esphome version“. Tests ersetzen ihn durch eine
// Attrappe, die auch unter Windows läuft.
var versionCmd = func(ctx context.Context, bin string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, "version")
}

// EnvState: Zustand der Python-Umgebung mit ESPHome.
type EnvState struct {
	Installed bool   // ESPHome startet und nennt seine Version
	Version   string // installierte Version, z. B. "2026.10.2"
	Outdated  bool   // installierte Version weicht von PinnedVersion ab
	Docker    bool   // das PDH läuft in einem Container – dort gibt es ESPPDH nicht
	FreeMB    int64  // freier Platz für den Arbeitsordner in MB; -1 = unbekannt
	Problem   string // Umgebung ist da, aber ESPHome startet nicht (Klartext); leer, wenn sie fehlt
}

// PinnedVersion: die ESPHome-Version, mit der ESPPDH baut (scripts/esphome.version, eingebettet).
func PinnedVersion() string { return strings.TrimSpace(pdh.ESPHomeVersion) }

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// venvDir: Ordner der Python-Umgebung.
func venvDir() string { return envOr("PDH_ESPHOME_DIR", VenvDir) }

// workDir: Arbeitsordner der Bauläufe.
func workDir() string { return envOr("PDH_ESPHOME_WORKDIR", WorkDir) }

// esphomeBin: Programm ESPHome in der Python-Umgebung.
func esphomeBin() string { return filepath.Join(venvDir(), "bin", "esphome") }

// InContainer: läuft das Programm in einem Container (Docker, Podman)?
func InContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// freeMB: freier Platz in MB für path – fehlt der Ordner noch, zählt der nächste
// vorhandene Elternordner. -1 = unbekannt.
func freeMB(path string) int64 {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			if b, ok := freeBytes(p); ok {
				return b >> 20
			}
			return -1
		}
		if filepath.Dir(p) == p {
			return -1
		}
	}
}

// parseVersion: Version aus der Ausgabe von „esphome version“ („Version: 2026.10.2“).
func parseVersion(out string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "Version:"); ok {
			if v = strings.TrimSpace(v); v != "" && !strings.ContainsAny(v, " \t") {
				return v, true
			}
		}
	}
	return "", false
}

// lastLine: letzte nicht leere Zeile (bei Python meist die eigentliche Fehlermeldung).
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := []rune(strings.TrimSpace(lines[len(lines)-1]))
	if len(l) > 300 {
		return string(l[:300]) + " …"
	}
	return string(l)
}

// EnvPresent: liegt ESPHome in der Python-Umgebung? Prüft nur, ob das Programm da ist, ohne es
// aufzurufen – schnell genug für jede Seite. Ob es auch startet, sagt CheckEnv.
func EnvPresent() bool {
	_, err := os.Stat(esphomeBin())
	return err == nil
}

// CheckEnv prüft die Python-Umgebung: ruft „<venv>/bin/esphome version“ mit Zeitlimit 10 s.
// Fehlt die Umgebung, ist Installed falsch und Problem leer – das ist kein Fehler.
func CheckEnv(ctx context.Context) EnvState {
	st := EnvState{Docker: InContainer(), FreeMB: freeMB(workDir())}
	bin := esphomeBin()
	if _, err := os.Stat(bin); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			st.Problem = "ESPHome ist nicht lesbar: " + err.Error()
		}
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, envTimeout)
	defer cancel()
	cmd := versionCmd(ctx, bin)
	cmd.WaitDelay = time.Second // hängende Kindprozesse halten den Aufruf nicht auf
	out, err := cmd.Output()
	switch {
	case ctx.Err() != nil:
		st.Problem = "ESPHome antwortet nicht (Zeitlimit " + strconv.FormatFloat(envTimeout.Seconds(), 'f', -1, 64) + " s)"
	case err != nil:
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(strings.TrimSpace(string(ee.Stderr))) > 0 {
			msg = lastLine(string(ee.Stderr))
		}
		st.Problem = "ESPHome startet nicht: " + msg
	default:
		v, ok := parseVersion(string(out))
		if !ok {
			st.Problem = "ESPHome meldet keine Version"
			return st
		}
		st.Installed, st.Version, st.Outdated = true, v, v != PinnedVersion()
	}
	return st
}
