package main

// ESPPDH – mehrere ESPHome-Umgebungen nebeneinander.
//
// Neben der Grundumgebung (/opt/pdh-esphome) liegen neuere Versionen unter
// <VersionsDir>/<Version>/ (VersionsDir = /opt/pdh-esphome-versions). Zwei Zeigerdateien im
// Versionsordner legen fest, was gilt – dieselben Dateien liest das PDH (internal/modules/esppdh,
// versions.go), geschrieben werden sie nur hier:
//
//	active    eine Zeile „<Version>“; fehlt die Datei, gilt die Grundumgebung
//	previous  eine Zeile „<Version>“ oder „base“ (Grundumgebung), zum Zurückschalten
//
// Der Agent arbeitet mit dem rohen Inhalt der Zeiger (geprüft gegen das Versionsmuster); die
// gefilterte Sicht des PDH (Mindestversion, vorhandener Ordner) gilt hier nicht.
// Gelöscht wird ausschließlich in removeVersionDir.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"pdh/internal/modules/esppdh"
)

const (
	pointerActive   = "active"
	pointerPrevious = "previous"
	pointerBase     = "base" // Wert von „previous“: Grundumgebung
)

// versionsDir: Ordner der ESPHome-Versionen. Der Agent läuft als root und liest dafür keine
// Umgebungsvariable (anders als das PDH, dessen Tests PDH_ESPHOME_VERSIONS_DIR setzen) – nur die
// Tests des Agenten setzen diese Variable direkt.
var versionsDir = esppdh.VersionsDir

// versionBin: Programm ESPHome einer Version im Versionen-Ordner.
func versionBin(version string) string { return filepath.Join(versionsDir, version, "bin", "esphome") }

// versionsAnswer: Antwort von GET und POST /v1/esppdh/versions.
type versionsAnswer struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	Active    string   `json:"active"`   // leer = Grundumgebung
	Previous  string   `json:"previous"` // „base“ = Grundumgebung, leer = keine
	Installed []string `json:"installed"`
}

// readRawPointer: Inhalt der Zeigerdatei – nur wenn er dem Versionsmuster entspricht (bei
// „previous“ auch „base“); sonst leer.
func readRawPointer(name string) string {
	b, err := os.ReadFile(filepath.Join(versionsDir, name))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if esppdh.StableVersionRe.MatchString(v) || (name == pointerPrevious && v == pointerBase) {
		return v
	}
	return ""
}

// writePointer schreibt eine Zeigerdatei atomar (Temp-Datei im selben Ordner, dann Rename), 0644.
func writePointer(name, value string) error {
	dir := versionsDir
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.WriteString(value + "\n")
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmpName, 0o644)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// removePointer entfernt eine Zeigerdatei (fehlt sie schon, ist das gut).
func removePointer(name string) error {
	err := os.Remove(filepath.Join(versionsDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// installedVersions: Namen der Versionsordner (echte Ordner mit gültigem Namen), aufsteigend.
func installedVersions() []string {
	out := []string{}
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.Type().IsDir() && esppdh.StableVersionRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return esppdh.CompareVersions(out[i], out[j]) < 0 })
	return out
}

// removeVersionDir löscht <VersionsDir>/<version> – die einzige Löschstelle für Versionsordner.
// Streng: gültige Versionsform, Pfad genau <VersionsDir>/<version>, kein Verweis, ein Ordner, der
// pyvenv.cfg enthält oder leer ist. Ein fehlender Ordner ist kein Fehler.
func removeVersionDir(version string) error {
	if !esppdh.StableVersionRe.MatchString(version) {
		return fmt.Errorf("ungültige ESPHome-Version %q", version)
	}
	base := filepath.Clean(versionsDir)
	if !filepath.IsAbs(base) || filepath.Dir(base) == base {
		return fmt.Errorf("ungültiger Versionsordner %q", base)
	}
	p := filepath.Join(base, version)
	if filepath.Dir(p) != base || filepath.Base(p) != version {
		return fmt.Errorf("Pfad %q liegt nicht direkt im Versionsordner", p)
	}
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() { // Datei oder Verweis (Lstat folgt nicht)
		return fmt.Errorf("%s ist kein Ordner (Datei oder Verweis) und wird nicht gelöscht", p)
	}
	if _, err := os.Stat(filepath.Join(p, "pyvenv.cfg")); err != nil {
		entries, rerr := os.ReadDir(p)
		if rerr != nil {
			return rerr
		}
		if len(entries) > 0 {
			return fmt.Errorf("%s ist keine Python-Umgebung (pyvenv.cfg fehlt) und wird nicht gelöscht", p)
		}
	}
	return os.RemoveAll(p)
}

// ── Aufräumen ────────────────────────────────────────────────────

// esppdhInstallRunning: läuft außerhalb dieses Agenten eine Einrichtung (z. B. nach einem
// Neustart des Agenten, systemd-run läuft weiter)? Im Zweifel ja – dann wird nichts gelöscht.
func (a *updateAgent) esppdhInstallRunning(ctx context.Context) bool {
	if a.esppdhBusy != nil {
		return a.esppdhBusy(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := a.hostCommand(ctx, false,
		`systemctl list-units --plain --no-legend --state=active,activating,deactivating 'pdh-esphome-install-*'`).Output()
	return err != nil || strings.TrimSpace(string(out)) != ""
}

// pruneVersionOrphans löscht Versionsordner, die weder „active“ noch „previous“ noch das Ziel
// eines laufenden Auftrags sind (verwaiste Kandidaten, z. B. nach einem Neustart). Läuft sonst
// irgendwo eine Einrichtung, bleibt alles stehen.
func (a *updateAgent) pruneVersionOrphans(ctx context.Context) {
	if a.mode != "systemd" {
		return
	}
	a.vmu.Lock()
	defer a.vmu.Unlock()
	a.sweepVersions(ctx, readRawPointer(pointerActive), readRawPointer(pointerPrevious))
}

// installingTarget: wird genau diese Version gerade vom Auftrag dieses Agenten eingerichtet?
func (a *updateAgent) installingTarget(version string) bool {
	a.esppdh.mu.Lock()
	defer a.esppdh.mu.Unlock()
	return a.esppdh.running && a.esppdh.target == version
}

// sweepVersions löscht alle Versionsordner außer den genannten (vmu gehalten). Die einzige
// Stelle, die mehrere Ordner auf einmal löscht: Läuft irgendwo eine Einrichtung, wird nichts
// gelöscht (pip schreibt noch); der Ordner des eigenen laufenden Auftrags bleibt immer stehen.
func (a *updateAgent) sweepVersions(ctx context.Context, keep ...string) {
	if a.esppdhInstallRunning(ctx) {
		return
	}
	for _, v := range installedVersions() {
		if a.installingTarget(v) || slices.Contains(keep, v) {
			continue
		}
		if err := removeVersionDir(v); err != nil {
			log.Printf("esppdh: Version %s nicht gelöscht: %v", v, err)
			continue
		}
		log.Printf("esppdh: Version %s gelöscht", v)
	}
}

// ── Prüfung des Programms ────────────────────────────────────────

// esphomeVersionOf: die Version, die „<bin> version“ nennt.
func (a *updateAgent) esphomeVersionOf(ctx context.Context, bin string) (string, error) {
	if a.esphomeVersion != nil {
		return a.esphomeVersion(ctx, bin)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, firstLine(string(out)))
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	v := ""
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "Version:"); ok {
			v = strings.TrimSpace(rest)
		}
	}
	return v, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ── Umschalten ───────────────────────────────────────────────────

// versionDirOK: der Ordner der Version ist ein echter Ordner (kein Verweis).
func versionDirOK(version string) bool {
	fi, err := os.Lstat(filepath.Join(versionsDir, version))
	return err == nil && fi.IsDir()
}

func (a *updateAgent) versionsState(errMsg string) versionsAnswer {
	return versionsAnswer{
		OK: errMsg == "", Error: errMsg,
		Active: readRawPointer(pointerActive), Previous: readRawPointer(pointerPrevious), Installed: installedVersions(),
	}
}

// effectiveActive: der Zeiger „active“, wie er gilt – ist er älter als die Mindestversion, nimmt
// das PDH die Grundumgebung (esppdh.ActiveVersionPointer), dann hier "" (Grundumgebung).
func effectiveActive() string {
	cur := readRawPointer(pointerActive)
	if cur != "" && esppdh.CompareVersions(cur, esppdh.PinnedVersion()) < 0 {
		return ""
	}
	return cur
}

// upgradeAllowed: Darf version aktiv werden? Nie herabstufen – nur eine neuere Version als die
// aktive und nicht unter der Mindestversion; zurück auf die vorige Umgebung geht allein mit
// „rollback“. Gilt beim Umschalten und schon beim Einrichten eines Kandidaten.
func upgradeAllowed(version string) error {
	if cur := effectiveActive(); cur != "" && esppdh.CompareVersions(version, cur) <= 0 {
		if version == cur {
			return fmt.Errorf("ESPHome %s ist schon aktiv", version)
		}
		return fmt.Errorf("ESPHome %s ist älter als die aktive Version %s – es wird nicht herabgestuft", version, cur)
	}
	if esppdh.CompareVersions(version, esppdh.PinnedVersion()) < 0 {
		return fmt.Errorf("ESPHome %s ist älter als die Mindestversion %s", version, esppdh.PinnedVersion())
	}
	return nil
}

// versionsActivate: die Version wird aktiv; die bisherige aktive (oder die Grundumgebung) wird
// „previous“; alle anderen Versionsordner werden gelöscht (eine Generation bleibt erhalten).
func (a *updateAgent) versionsActivate(ctx context.Context, version string) error {
	if !esppdh.StableVersionRe.MatchString(version) {
		return errors.New("ungültige ESPHome-Version")
	}
	if err := upgradeAllowed(version); err != nil {
		return err
	}
	// eine aktive Version unter der Mindestversion gilt nicht – „previous“ wird dann die Grundumgebung
	cur := effectiveActive()
	if !versionDirOK(version) {
		return fmt.Errorf("ESPHome %s ist nicht eingerichtet", version)
	}
	got, err := a.esphomeVersionOf(ctx, versionBin(version))
	if err != nil {
		return fmt.Errorf("ESPHome %s lässt sich nicht starten: %w", version, err)
	}
	if got != version {
		return fmt.Errorf("Der Ordner für ESPHome %s enthält ESPHome %q – nicht umgeschaltet", version, got)
	}
	prev := cur
	if prev == "" {
		prev = pointerBase
	}
	if err := writePointer(pointerPrevious, prev); err != nil {
		return fmt.Errorf("Zeiger „previous“ nicht schreibbar: %w", err)
	}
	if err := writePointer(pointerActive, version); err != nil {
		return fmt.Errorf("Zeiger „active“ nicht schreibbar: %w", err)
	}
	// Alles andere aufräumen – nicht, solange eine Einrichtung läuft (das macht der nächste Lauf).
	a.sweepVersions(ctx, version, prev)
	return nil
}

// versionsDiscard: Ordner eines Kandidaten löschen, wenn er weder aktiv noch „previous“ noch
// das Ziel eines laufenden Auftrags ist.
func (a *updateAgent) versionsDiscard(ctx context.Context, version string) error {
	if !esppdh.StableVersionRe.MatchString(version) {
		return errors.New("ungültige ESPHome-Version")
	}
	if version == readRawPointer(pointerActive) || version == readRawPointer(pointerPrevious) {
		return fmt.Errorf("ESPHome %s ist aktiv oder die vorige Version und bleibt erhalten", version)
	}
	// Läuft eine Einrichtung (auch eine, die der Agent nach einem Neustart nicht mehr kennt),
	// kann es genau diese Version sein – dann nichts löschen.
	if a.installingTarget(version) || a.esppdhInstallRunning(ctx) {
		return fmt.Errorf("ESPHome %s kann jetzt nicht verworfen werden: Es wird gerade eine ESPHome-Version eingerichtet. Bitte danach erneut versuchen", version)
	}
	return removeVersionDir(version)
}

// versionsRollback: „active“ und „previous“ tauschen („base“ = Zeiger „active“ entfernen).
func (a *updateAgent) versionsRollback() error {
	prev := readRawPointer(pointerPrevious)
	cur := readRawPointer(pointerActive)
	if prev == "" || (prev == pointerBase && cur == "") {
		return errors.New("Es gibt keine vorige Version")
	}
	if prev == pointerBase {
		// zurück auf die Grundumgebung; die bisherige Version bleibt als „previous“ (Tausch)
		if err := writePointer(pointerPrevious, cur); err != nil {
			return fmt.Errorf("Zeiger „previous“ nicht schreibbar: %w", err)
		}
		if err := removePointer(pointerActive); err != nil {
			return fmt.Errorf("Zeiger „active“ nicht entfernbar: %w", err)
		}
		return nil
	}
	if !versionDirOK(prev) {
		return fmt.Errorf("Die vorige Version %s ist nicht mehr eingerichtet", prev)
	}
	newPrev := cur
	if newPrev == "" {
		newPrev = pointerBase
	}
	if err := writePointer(pointerPrevious, newPrev); err != nil {
		return fmt.Errorf("Zeiger „previous“ nicht schreibbar: %w", err)
	}
	if err := writePointer(pointerActive, prev); err != nil {
		return fmt.Errorf("Zeiger „active“ nicht schreibbar: %w", err)
	}
	return nil
}

// ── HTTP ─────────────────────────────────────────────────────────

// versionsGetHandler: GET /v1/esppdh/versions → aktive und vorige Version, eingerichtete Ordner.
func (a *updateAgent) versionsGetHandler(w http.ResponseWriter, _ *http.Request) {
	if a.mode != "systemd" {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": esppdhDockerMsg})
		return
	}
	a.vmu.Lock()
	ans := a.versionsState("")
	a.vmu.Unlock()
	writeMountJSON(w, http.StatusOK, ans)
}

// versionsPostHandler: POST /v1/esppdh/versions {"action":"activate|discard|rollback","version":"…"}.
func (a *updateAgent) versionsPostHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": esppdhDockerMsg})
		return
	}
	var req struct {
		Action  string `json:"action"`
		Version string `json:"version"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err := json.Unmarshal(body, &req); err != nil {
		writeMountJSON(w, http.StatusOK, a.versionsState("ungültiger Rumpf"))
		return
	}
	a.vmu.Lock()
	defer a.vmu.Unlock()
	var err error
	switch req.Action {
	case "activate":
		err = a.versionsActivate(r.Context(), req.Version)
	case "discard":
		err = a.versionsDiscard(r.Context(), req.Version)
	case "rollback":
		err = a.versionsRollback()
	default:
		err = errors.New("unbekannte Aktion")
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	writeMountJSON(w, http.StatusOK, a.versionsState(msg))
}
