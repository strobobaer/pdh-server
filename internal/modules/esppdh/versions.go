package esppdh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// FreeMBAt: freier Platz in MB für path (-1 = unbekannt), siehe freeMB. Fehlt path noch, zählt
// der nächste vorhandene Elternordner.
func FreeMBAt(path string) int64 { return freeMB(path) }

// WorkDirPath: der tatsächlich geltende Arbeitsordner (Bauten, Zustand je Server).
func WorkDirPath() string { return workDir() }

// ── Zustand je Server ─────────────────────────────────────────

// maxStateEntries: so viele gescheiterte und gemeldete Versionen merkt sich der Zustand höchstens;
// darüber fallen die ältesten weg.
const maxStateEntries = 50

// FailedEntry: eine Version, die sich nicht einrichten ließ, an der Probe scheiterte oder von
// Hand verlassen wurde – mit der PDH-Version, unter der das geschah.
type FailedEntry struct {
	Version string
	PDH     string
}

// UpdateState: was der tägliche Lauf dieses Servers zuletzt festgestellt hat
// (<WorkDir>/update-state.json).
//
// Failed sind alle gescheiterten Versionen; eine Version wird nur unter derselben PDH-Version
// nicht erneut versucht (SkipFor). Notified sind alle Versionen, über die schon benachrichtigt
// wurde. FailedVersion, FailedPDH, FailedLog und NotifiedVersion beschreiben die zuletzt
// gescheiterte bzw. gemeldete Version für die Anzeige. Eine ältere Datei, die nur diese
// Einzelfelder kennt, wird beim Lesen in die Listen übernommen.
type UpdateState struct {
	CheckedAt                           time.Time
	Result                              string
	Installed                           string
	FailedVersion, FailedPDH, FailedLog string
	NotifiedVersion                     string
	Failed                              []FailedEntry `json:",omitempty"`
	Notified                            []string      `json:",omitempty"`
}

// migrate übernimmt die Einzelfelder einer älteren Zustandsdatei in die Listen.
func (st *UpdateState) migrate() {
	if st.FailedVersion != "" && !slices.ContainsFunc(st.Failed, func(e FailedEntry) bool { return e.Version == st.FailedVersion }) {
		st.Failed = append(st.Failed, FailedEntry{Version: st.FailedVersion, PDH: st.FailedPDH})
	}
	if st.NotifiedVersion != "" && !slices.Contains(st.Notified, st.NotifiedVersion) {
		st.Notified = append(st.Notified, st.NotifiedVersion)
	}
}

// AddFailed vermerkt eine gescheiterte Version, auch als zuletzt gescheiterte für die Anzeige.
// Eine schon vermerkte Version bekommt die neue PDH-Version und rückt ans Ende.
func (st *UpdateState) AddFailed(version, pdhVersion, log string) {
	st.FailedVersion, st.FailedPDH, st.FailedLog = version, pdhVersion, log
	st.Failed = slices.DeleteFunc(st.Failed, func(e FailedEntry) bool { return e.Version == version })
	st.Failed = append(st.Failed, FailedEntry{Version: version, PDH: pdhVersion})
	if n := len(st.Failed) - maxStateEntries; n > 0 {
		st.Failed = slices.Clone(st.Failed[n:])
	}
}

// SkipFor: die Versionen, die unter dieser PDH-Version gescheitert sind – sie werden ohne
// „Jetzt prüfen“ nicht erneut versucht.
func (st UpdateState) SkipFor(pdhVersion string) []string {
	var out []string
	for _, e := range st.Failed {
		if e.PDH == pdhVersion {
			out = append(out, e.Version)
		}
	}
	return out
}

// WasNotified: Wurde über diese Version schon benachrichtigt?
func (st UpdateState) WasNotified(version string) bool { return slices.Contains(st.Notified, version) }

// AddNotified vermerkt, dass über die Version benachrichtigt wurde.
func (st *UpdateState) AddNotified(version string) {
	st.NotifiedVersion = version
	if !slices.Contains(st.Notified, version) {
		st.Notified = append(st.Notified, version)
	}
	if n := len(st.Notified) - maxStateEntries; n > 0 {
		st.Notified = slices.Clone(st.Notified[n:])
	}
}

// Activated räumt nach dem Umschalten auf version auf: Gescheiterte und gemeldete Versionen bis
// einschließlich version entfallen – als Kandidat kommen nur neuere in Frage.
func (st *UpdateState) Activated(version string) {
	upTo := func(v string) bool { return CompareVersions(v, version) <= 0 }
	st.Failed = slices.DeleteFunc(st.Failed, func(e FailedEntry) bool { return upTo(e.Version) })
	st.Notified = slices.DeleteFunc(st.Notified, upTo)
	if st.FailedVersion != "" && upTo(st.FailedVersion) {
		st.FailedVersion, st.FailedPDH, st.FailedLog = "", "", ""
	}
	if st.NotifiedVersion != "" && upTo(st.NotifiedVersion) {
		st.NotifiedVersion = ""
	}
}

func updateStatePath() string { return filepath.Join(workDir(), updateStateName) }

// LoadUpdateState liest den Zustand; fehlt die Datei oder ist sie unlesbar, ist er leer.
func LoadUpdateState() UpdateState {
	var st UpdateState
	b, err := os.ReadFile(updateStatePath())
	if err != nil || json.Unmarshal(b, &st) != nil {
		return UpdateState{}
	}
	st.migrate()
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
