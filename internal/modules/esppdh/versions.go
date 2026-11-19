package esppdh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mehrere ESPHome-Umgebungen nebeneinander. Neben der Grundumgebung (VenvDir, Version aus
// scripts/esphome.version) kann der Update-Agent neuere Versionen unter
// <VersionsDir>/<version>/ einrichten. Zwei Zeigerdateien (je eine Zeile mit der Version,
// Zeilenende erlaubt) legen fest, was gilt – dieselben Dateien liest und schreibt der
// Update-Agent (root):
//
//	active    die Version, mit der gebaut wird; fehlt die Datei, gilt die Grundumgebung
//	previous  die Version davor, zum Zurückschalten; „base“ heißt Grundumgebung
//
// Ein Zeigerwert gilt nur, wenn er StableVersionRe entspricht (das weist „../x“ ab), sein
// Ordner da ist und die Version nicht älter als PinnedVersion ist. Sonst: Grundumgebung.

// VersionsDir: Ordner der zusätzlichen Umgebungen (überschreibbar mit PDH_ESPHOME_VERSIONS_DIR,
// nur für Tests).
const VersionsDir = "/opt/pdh-esphome-versions"

// updateStateName: Zustandsdatei je Server im Arbeitsordner.
const updateStateName = "update-state.json"

// VersionsDirPath: der tatsächlich geltende Ordner der zusätzlichen Umgebungen.
func VersionsDirPath() string { return envOr("PDH_ESPHOME_VERSIONS_DIR", VersionsDir) }

// VersionBin: Programm ESPHome der Version – „<VersionsDir>/<version>/bin/esphome“; leer bei
// ungültiger Version.
func VersionBin(version string) string {
	if !StableVersionRe.MatchString(version) {
		return ""
	}
	return filepath.Join(VersionsDirPath(), version, "bin", "esphome")
}

// readPointer: Wert der Zeigerdatei name, wenn er gültig ist, sein Ordner da ist und die Version
// nicht älter als PinnedVersion ist; sonst leer.
func readPointer(name string) string {
	b, err := os.ReadFile(filepath.Join(VersionsDirPath(), name))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !StableVersionRe.MatchString(v) || CompareVersions(v, PinnedVersion()) < 0 {
		return ""
	}
	if fi, err := os.Stat(filepath.Join(VersionsDirPath(), v)); err != nil || !fi.IsDir() {
		return ""
	}
	return v
}

// ActiveVersionPointer: die Version in „active“, wenn gültig; sonst leer (= Grundumgebung).
func ActiveVersionPointer() string { return readPointer("active") }

// PreviousVersionPointer: „base“ (Grundumgebung), die Version in „previous“, wenn gültig; sonst leer.
func PreviousVersionPointer() string {
	if b, err := os.ReadFile(filepath.Join(VersionsDirPath(), "previous")); err == nil && strings.TrimSpace(string(b)) == "base" {
		return "base"
	}
	return readPointer("previous")
}

// ActiveBin: das Programm ESPHome, mit dem gebaut wird – die aktive Version, sonst die Grundumgebung.
func ActiveBin() string {
	if bin := VersionBin(ActiveVersionPointer()); bin != "" {
		return bin
	}
	return filepath.Join(venvDir(), "bin", "esphome")
}

// FreeMBAt: freier Platz in MB für path (-1 = unbekannt), siehe freeMB.
func FreeMBAt(path string) int64 { return freeMB(path) }

// ── Zustand je Server ─────────────────────────────────────────

// UpdateState: was der tägliche Lauf dieses Servers zuletzt festgestellt hat
// (<WorkDir>/update-state.json). FailedVersion ist eine Version, die sich nicht einrichten ließ
// oder beim Prüfen scheiterte – mit der PDH-Version (FailedPDH) und dem Verlauf; NotifiedVersion
// die Version, über die schon benachrichtigt wurde.
type UpdateState struct {
	CheckedAt                           time.Time
	Result                              string
	Installed                           string
	FailedVersion, FailedPDH, FailedLog string
	NotifiedVersion                     string
}

func updateStatePath() string { return filepath.Join(workDir(), updateStateName) }

// LoadUpdateState liest den Zustand; fehlt die Datei oder ist sie unlesbar, ist er leer.
func LoadUpdateState() UpdateState {
	var st UpdateState
	b, err := os.ReadFile(updateStatePath())
	if err != nil || json.Unmarshal(b, &st) != nil {
		return UpdateState{}
	}
	return st
}

// SaveUpdateState schreibt den Zustand atomar (Temp-Datei, dann Rename), Rechte 0600.
func SaveUpdateState(st UpdateState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := updateStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), updateStateName+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := firstErr(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
