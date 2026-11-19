package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ESPPDH: ESPHome-Versionen nebeneinander (esppdh_versions.go).

// versionsAgent: Agent im systemd-Betrieb mit Temp-Ordner als versionsDir; ESPHome „nennt“ die
// Version seines Ordners, laufende Einrichtungen gibt es nicht.
func versionsAgent(t *testing.T) (*updateAgent, string) {
	t.Helper()
	dir := t.TempDir()
	old := versionsDir
	versionsDir = dir
	t.Cleanup(func() { versionsDir = old })
	a := &updateAgent{mode: "systemd", repoDir: "/srv/pdh"}
	a.esphomeVersion = func(_ context.Context, bin string) (string, error) {
		return filepath.Base(filepath.Dir(filepath.Dir(bin))), nil
	}
	a.esppdhBusy = func(context.Context) bool { return false }
	return a, dir
}

// mkVersion legt eine Umgebung <dir>/<version> mit pyvenv.cfg an.
func mkVersion(t *testing.T, dir, version string) string {
	t.Helper()
	p := filepath.Join(dir, version)
	if err := os.MkdirAll(filepath.Join(p, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeRaw(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRaw(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<fehlt>"
	}
	return string(b)
}

type versionsReply struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error"`
	Active    string   `json:"active"`
	Previous  string   `json:"previous"`
	Installed []string `json:"installed"`
}

func versionsPost(t *testing.T, a *updateAgent, action, version string) versionsReply {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action, "version": version})
	w := httptest.NewRecorder()
	a.versionsPostHandler(w, httptest.NewRequest(http.MethodPost, "/v1/esppdh/versions", strings.NewReader(string(body))))
	var ans versionsReply
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
		t.Fatalf("Antwort ist kein JSON: %q", w.Body.String())
	}
	return ans
}

func versionsGet(t *testing.T, a *updateAgent) versionsReply {
	t.Helper()
	w := httptest.NewRecorder()
	a.versionsGetHandler(w, httptest.NewRequest(http.MethodGet, "/v1/esppdh/versions", nil))
	var ans versionsReply
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
		t.Fatalf("Antwort ist kein JSON: %q", w.Body.String())
	}
	return ans
}

func TestVersionsActivate(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.11.1")
	mkVersion(t, dir, "2026.10.5") // fremder Kandidat: weg
	ans := versionsPost(t, a, "activate", "2026.11.1")
	if !ans.OK || ans.Active != "2026.11.1" || ans.Previous != "base" {
		t.Fatalf("Antwort: %+v", ans)
	}
	if got := readRaw(dir, "active"); got != "2026.11.1\n" {
		t.Errorf("active = %q", got)
	}
	if got := readRaw(dir, "previous"); got != "base\n" {
		t.Errorf("previous = %q", got)
	}
	if esppdhExists(filepath.Join(dir, "2026.10.5")) || !esppdhExists(filepath.Join(dir, "2026.11.1")) {
		t.Error("fremder Ordner nicht entfernt oder neuer Ordner weg")
	}
	// zweite Umschaltung: bisherige aktive Version bleibt als „previous“ erhalten, alles andere geht
	mkVersion(t, dir, "2026.11.2")
	mkVersion(t, dir, "2026.9.9")
	if ans := versionsPost(t, a, "activate", "2026.11.2"); !ans.OK || ans.Active != "2026.11.2" || ans.Previous != "2026.11.1" {
		t.Fatalf("zweite Umschaltung: %+v", ans)
	}
	if !esppdhExists(filepath.Join(dir, "2026.11.1")) || esppdhExists(filepath.Join(dir, "2026.9.9")) {
		t.Error("vorige Version muss bleiben, andere Ordner müssen weg")
	}
	if got := readRaw(dir, "previous"); got != "2026.11.1\n" {
		t.Errorf("previous = %q", got)
	}
}

// Der Agent stuft nie herab: nur neuer als die aktive Version und nicht unter der Mindestversion.
func TestVersionsActivateNoDowngrade(t *testing.T) {
	a, dir := versionsAgent(t)
	for _, v := range []string{"2026.11.5", "2026.11.2", "2026.11.9", "2026.10.1"} {
		mkVersion(t, dir, v)
	}
	writeRaw(t, dir, "active", "2026.11.5\n")
	writeRaw(t, dir, "previous", "base\n")
	for v, want := range map[string]string{"2026.11.2": "älter", "2026.11.5": "schon aktiv", "2026.10.1": "älter"} {
		ans := versionsPost(t, a, "activate", v)
		if ans.OK || !strings.Contains(ans.Error, want) {
			t.Errorf("activate %s: %+v (erwartet Fehler mit %q)", v, ans, want)
		}
		if readRaw(dir, "active") != "2026.11.5\n" || readRaw(dir, "previous") != "base\n" {
			t.Errorf("activate %s: Zeiger verändert: active=%q previous=%q", v, readRaw(dir, "active"), readRaw(dir, "previous"))
		}
	}
	// neuer: geht
	if ans := versionsPost(t, a, "activate", "2026.11.9"); !ans.OK || ans.Active != "2026.11.9" {
		t.Errorf("neuere Version: %+v", ans)
	}
	// unter der Mindestversion, auch ohne aktive Version (Grundumgebung); eigener Ordner
	b, bdir := versionsAgent(t)
	mkVersion(t, bdir, "2026.9.9")
	ans := versionsPost(t, b, "activate", "2026.9.9")
	if ans.OK || !strings.Contains(ans.Error, "Mindestversion") || readRaw(bdir, "active") != "<fehlt>" {
		t.Errorf("unter Mindestversion: %+v", ans)
	}
}

// Ein Zeiger „active“ unter der Mindestversion (z. B. nach einem PDH-Update mit höherer
// Mindestversion) gilt nicht: Beim Umschalten wird „previous“ die Grundumgebung, nicht die alte Version.
func TestVersionsActivateStaleActive(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.9.9") // älter als die Mindestversion
	mkVersion(t, dir, "2026.11.1")
	writeRaw(t, dir, "active", "2026.9.9\n")
	ans := versionsPost(t, a, "activate", "2026.11.1")
	if !ans.OK || ans.Active != "2026.11.1" || ans.Previous != "base" {
		t.Fatalf("Antwort: %+v", ans)
	}
	if readRaw(dir, "previous") != "base\n" || esppdhExists(filepath.Join(dir, "2026.9.9")) {
		t.Errorf("previous = %q, alte Version noch da: %v", readRaw(dir, "previous"), esppdhExists(filepath.Join(dir, "2026.9.9")))
	}
}

// Einen Kandidaten, der sich nie einschalten ließe (nicht neuer als die aktive Version oder unter
// der Mindestversion), richtet der Agent gar nicht erst ein – gleiche Regel und Meldung wie beim Umschalten.
func TestInstallCandidateNoDowngrade(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.11.5")
	writeRaw(t, dir, "active", "2026.11.5\n")
	ran := false
	a.esppdhRun = func(context.Context, string, map[string]string) (string, error) {
		ran = true
		return "Version: 2026.11.6\n", nil
	}
	call := func(version string) esppdhAnswer {
		w := httptest.NewRecorder()
		a.esppdhInstallHandler(w, httptest.NewRequest(http.MethodPost, "/v1/esppdh/install", strings.NewReader(`{"version":"`+version+`"}`)))
		var ans esppdhAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
			t.Fatalf("Antwort ist kein JSON: %q", w.Body.String())
		}
		return ans
	}
	// Feste Reihenfolge: Der letzte Fall entfernt den Zeiger „active“ – stünde er
	// früher, gälte für die Fälle danach nur noch die Mindestversion.
	cases := []struct{ v, want string }{
		{"2026.11.5", "schon aktiv"},
		{"2026.11.2", "nicht herabgestuft"},
		{"2026.9.9", "Mindestversion"},
	}
	for _, c := range cases {
		if c.v == "2026.9.9" { // unter der Mindestversion – auch ohne aktive Version
			_ = os.Remove(filepath.Join(dir, "active"))
		}
		ans := call(c.v)
		wantErr := versionsPost(t, a, "activate", c.v).Error // Meldung beim Umschalten
		if ans.OK || !strings.Contains(ans.Error, c.want) || ans.Error != wantErr {
			t.Errorf("Einrichtung %s: %+v (erwartet %q wie beim Umschalten)", c.v, ans, wantErr)
		}
	}
	if ran || a.esppdh.status().Running {
		t.Fatal("Einrichtung trotzdem gestartet")
	}
	// neuer als die aktive Version: geht
	writeRaw(t, dir, "active", "2026.11.5\n")
	if ans := call("2026.11.6"); !ans.OK || !ans.Running {
		t.Fatalf("neuere Version: %+v", ans)
	}
	esppdhWaitDone(t, a)
}

// Der Agent (root) liest den Versionen-Ordner nicht aus der Umgebung – nur aus versionsDir.
func TestVersionsIgnoresEnv(t *testing.T) {
	a, dir := versionsAgent(t)
	other := t.TempDir()
	t.Setenv("PDH_ESPHOME_VERSIONS_DIR", other)
	mkVersion(t, other, "2026.11.1")
	writeRaw(t, other, "active", "2026.11.1\n")
	if ans := versionsGet(t, a); ans.Active != "" || len(ans.Installed) != 0 {
		t.Errorf("Umgebungsvariable gelesen: %+v", ans)
	}
	if ans := versionsPost(t, a, "activate", "2026.11.1"); ans.OK || readRaw(dir, "active") != "<fehlt>" {
		t.Errorf("Ordner aus der Umgebung umgeschaltet: %+v", ans)
	}
	if got := versionBin("2026.11.1"); got != filepath.Join(dir, "2026.11.1", "bin", "esphome") {
		t.Errorf("versionBin: %s", got)
	}
}

// Läuft eine Einrichtung (im Speicher oder nach einem Neustart noch als Dienst), wird nichts gelöscht.
func TestVersionsNoDeleteWhileInstalling(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.11.1")
	mkVersion(t, dir, "2026.11.2") // Kandidat, der gerade eingerichtet wird
	mkVersion(t, dir, "2026.11.3") // anderer Ordner
	// 1) Auftrag dieses Agenten läuft für 2026.11.2: Umschalten klappt, 2026.11.2 bleibt, 2026.11.3 geht
	a.esppdh.running, a.esppdh.target = true, "2026.11.2"
	if ans := versionsPost(t, a, "activate", "2026.11.1"); !ans.OK {
		t.Fatalf("activate: %+v", ans)
	}
	if !esppdhExists(filepath.Join(dir, "2026.11.2")) || esppdhExists(filepath.Join(dir, "2026.11.3")) {
		t.Error("Ziel des laufenden Auftrags gelöscht oder fremder Ordner stehen geblieben")
	}
	// 2) Einrichtung läuft außerhalb (Agent neu gestartet): Umschalten klappt, es wird nichts gelöscht,
	//    verwerfen wird verweigert
	a.esppdh.running = false
	a.esppdhBusy = func(context.Context) bool { return true }
	mkVersion(t, dir, "2026.11.4")
	mkVersion(t, dir, "2026.11.5")
	if ans := versionsPost(t, a, "activate", "2026.11.5"); !ans.OK || ans.Active != "2026.11.5" {
		t.Fatalf("activate bei laufender Einrichtung: %+v", ans)
	}
	for _, v := range []string{"2026.11.1", "2026.11.2", "2026.11.4", "2026.11.5"} {
		if !esppdhExists(filepath.Join(dir, v)) {
			t.Errorf("%s bei laufender Einrichtung gelöscht", v)
		}
	}
	if ans := versionsPost(t, a, "discard", "2026.11.4"); ans.OK || !strings.Contains(ans.Error, "eingerichtet") || !esppdhExists(filepath.Join(dir, "2026.11.4")) {
		t.Errorf("discard bei laufender Einrichtung: %+v", ans)
	}
	// 3) nichts läuft mehr: verwerfen geht, das nächste Aufräumen holt den Rest
	a.esppdhBusy = func(context.Context) bool { return false }
	if ans := versionsPost(t, a, "discard", "2026.11.4"); !ans.OK || esppdhExists(filepath.Join(dir, "2026.11.4")) {
		t.Errorf("discard danach: %+v", ans)
	}
	a.pruneVersionOrphans(context.Background())
	if esppdhExists(filepath.Join(dir, "2026.11.2")) || !esppdhExists(filepath.Join(dir, "2026.11.1")) {
		t.Error("Aufräumen nach Ende der Einrichtung")
	}
}

func TestVersionsActivateWrongBinary(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.11.1")
	writeRaw(t, dir, "active", "2026.10.2\n")
	mkVersion(t, dir, "2026.10.2")
	a.esphomeVersion = func(context.Context, string) (string, error) { return "2026.11.0", nil }
	ans := versionsPost(t, a, "activate", "2026.11.1")
	if ans.OK || !strings.Contains(ans.Error, "2026.11.0") {
		t.Fatalf("Antwort: %+v", ans)
	}
	if readRaw(dir, "active") != "2026.10.2\n" || readRaw(dir, "previous") != "<fehlt>" {
		t.Errorf("Zeiger verändert: active=%q previous=%q", readRaw(dir, "active"), readRaw(dir, "previous"))
	}
	if !esppdhExists(filepath.Join(dir, "2026.11.1")) || !esppdhExists(filepath.Join(dir, "2026.10.2")) {
		t.Error("Ordner nach Fehler verändert")
	}
	// fehlender Ordner
	if ans := versionsPost(t, a, "activate", "2026.12.0"); ans.OK {
		t.Errorf("fehlender Ordner angenommen: %+v", ans)
	}
}

func TestVersionsDiscardKeepsActive(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.11.1")
	mkVersion(t, dir, "2026.11.2")
	mkVersion(t, dir, "2026.11.3")
	writeRaw(t, dir, "active", "2026.11.2\n")
	writeRaw(t, dir, "previous", "2026.11.1\n")
	for _, v := range []string{"2026.11.2", "2026.11.1"} {
		if ans := versionsPost(t, a, "discard", v); ans.OK || ans.Error == "" {
			t.Errorf("%s verworfen: %+v", v, ans)
		}
		if !esppdhExists(filepath.Join(dir, v)) {
			t.Errorf("%s gelöscht", v)
		}
	}
	if ans := versionsPost(t, a, "discard", "2026.11.3"); !ans.OK {
		t.Fatalf("Kandidat nicht verworfen: %+v", ans)
	}
	if esppdhExists(filepath.Join(dir, "2026.11.3")) {
		t.Error("Kandidat noch da")
	}
	// Kandidat einer laufenden Einrichtung wird nicht verworfen
	mkVersion(t, dir, "2026.11.4")
	a.esppdh.running, a.esppdh.target = true, "2026.11.4"
	if ans := versionsPost(t, a, "discard", "2026.11.4"); ans.OK || !esppdhExists(filepath.Join(dir, "2026.11.4")) {
		t.Errorf("laufende Einrichtung verworfen: %+v", ans)
	}
}

func TestVersionsRollback(t *testing.T) {
	a, dir := versionsAgent(t)
	// ohne vorige Version: Fehler
	if ans := versionsPost(t, a, "rollback", ""); ans.OK || ans.Error != "Es gibt keine vorige Version" {
		t.Fatalf("ohne previous: %+v", ans)
	}
	mkVersion(t, dir, "2026.11.1")
	if ans := versionsPost(t, a, "activate", "2026.11.1"); !ans.OK {
		t.Fatal(ans.Error)
	}
	// zurück auf die Grundumgebung: active entfernt, previous = die bisherige Version
	ans := versionsPost(t, a, "rollback", "")
	if !ans.OK || ans.Active != "" || ans.Previous != "2026.11.1" {
		t.Fatalf("zurück: %+v", ans)
	}
	if readRaw(dir, "active") != "<fehlt>" || readRaw(dir, "previous") != "2026.11.1\n" || !esppdhExists(filepath.Join(dir, "2026.11.1")) {
		t.Errorf("Zeiger nach Rückschaltung: active=%q previous=%q", readRaw(dir, "active"), readRaw(dir, "previous"))
	}
	// und wieder vor
	ans = versionsPost(t, a, "rollback", "")
	if !ans.OK || ans.Active != "2026.11.1" || ans.Previous != "base" {
		t.Fatalf("vor: %+v", ans)
	}
	if readRaw(dir, "active") != "2026.11.1\n" || readRaw(dir, "previous") != "base\n" {
		t.Errorf("Zeiger: active=%q previous=%q", readRaw(dir, "active"), readRaw(dir, "previous"))
	}
	// zwei Versionen: tauschen
	mkVersion(t, dir, "2026.11.2")
	if ans := versionsPost(t, a, "activate", "2026.11.2"); !ans.OK {
		t.Fatal(ans.Error)
	}
	if ans := versionsPost(t, a, "rollback", ""); !ans.OK || ans.Active != "2026.11.1" || ans.Previous != "2026.11.2" {
		t.Fatalf("Tausch: %+v", ans)
	}
	// previous zeigt auf einen fehlenden Ordner: Fehler, nichts verändert
	writeRaw(t, dir, "previous", "2026.12.9\n")
	if ans := versionsPost(t, a, "rollback", ""); ans.OK || readRaw(dir, "active") != "2026.11.1\n" {
		t.Errorf("fehlender Ordner: %+v", ans)
	}
}

func TestVersionsRejectsBadVersion(t *testing.T) {
	a, dir := versionsAgent(t)
	mkVersion(t, dir, "2026.11.1")
	for _, v := range []string{"..", "../x", "/etc", "2026.11.1/..", "", "2026.11.0b1", "2026.11.1\n", "2026.11.1 ", "latest", "base"} {
		for _, action := range []string{"activate", "discard"} {
			if ans := versionsPost(t, a, action, v); ans.OK || ans.Error == "" {
				t.Errorf("%s %q angenommen: %+v", action, v, ans)
			}
		}
	}
	if ans := versionsPost(t, a, "unbekannt", "2026.11.1"); ans.OK {
		t.Errorf("unbekannte Aktion angenommen: %+v", ans)
	}
	if !esppdhExists(filepath.Join(dir, "2026.11.1")) || readRaw(dir, "active") != "<fehlt>" {
		t.Error("etwas verändert")
	}
	// removeVersionDir selbst: nur gültige Versionen, nur echte Umgebungen
	for _, v := range []string{"..", "", "../2026.11.1", "2026.11.1/bin"} {
		if err := removeVersionDir(v); err == nil {
			t.Errorf("removeVersionDir(%q) ohne Fehler", v)
		}
	}
	if !esppdhExists(dir) || !esppdhExists(filepath.Join(dir, "2026.11.1", "bin")) {
		t.Error("removeVersionDir hat Unerlaubtes gelöscht")
	}
}

// removeVersionDir löscht nur Umgebungen (pyvenv.cfg) oder leere Ordner, nie Verweise oder Dateien.
func TestRemoveVersionDirGuards(t *testing.T) {
	_, dir := versionsAgent(t)
	fremd := filepath.Join(dir, "2026.11.1")
	_ = os.MkdirAll(filepath.Join(fremd, "daten"), 0o755)
	_ = os.WriteFile(filepath.Join(fremd, "daten", "x"), []byte("x"), 0o644)
	if err := removeVersionDir("2026.11.1"); err == nil || !esppdhExists(filepath.Join(fremd, "daten", "x")) {
		t.Errorf("fremder Ordner ohne pyvenv.cfg gelöscht: %v", err)
	}
	datei := filepath.Join(dir, "2026.11.2")
	_ = os.WriteFile(datei, []byte("x"), 0o644)
	if err := removeVersionDir("2026.11.2"); err == nil || !esppdhExists(datei) {
		t.Errorf("Datei gelöscht: %v", err)
	}
	ziel := t.TempDir()
	_ = os.WriteFile(filepath.Join(ziel, "pyvenv.cfg"), []byte("x"), 0o644)
	if err := os.Symlink(ziel, filepath.Join(dir, "2026.11.3")); err == nil {
		if err := removeVersionDir("2026.11.3"); err == nil || !esppdhExists(filepath.Join(ziel, "pyvenv.cfg")) {
			t.Errorf("Verweis gefolgt: %v", err)
		}
	}
	mkVersion(t, dir, "2026.11.4")
	if err := removeVersionDir("2026.11.4"); err != nil || esppdhExists(filepath.Join(dir, "2026.11.4")) {
		t.Errorf("Umgebung nicht gelöscht: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "2026.11.5"), 0o755)
	if err := removeVersionDir("2026.11.5"); err != nil || esppdhExists(filepath.Join(dir, "2026.11.5")) {
		t.Errorf("leerer Ordner nicht gelöscht: %v", err)
	}
	if err := removeVersionDir("2026.11.6"); err != nil {
		t.Errorf("fehlender Ordner: %v", err)
	}
}

func TestVersionsGet(t *testing.T) {
	a, dir := versionsAgent(t)
	if ans := versionsGet(t, a); !ans.OK || ans.Active != "" || ans.Previous != "" || ans.Installed == nil || len(ans.Installed) != 0 {
		t.Fatalf("leer: %+v", ans)
	}
	mkVersion(t, dir, "2026.11.1")
	mkVersion(t, dir, "2026.9.2")
	_ = os.MkdirAll(filepath.Join(dir, "fremd"), 0o755)
	writeRaw(t, dir, "active", "2026.11.1\n")
	writeRaw(t, dir, "previous", "base\n")
	ans := versionsGet(t, a)
	if !ans.OK || ans.Active != "2026.11.1" || ans.Previous != "base" || !reflect.DeepEqual(ans.Installed, []string{"2026.9.2", "2026.11.1"}) {
		t.Errorf("Stand: %+v", ans)
	}
	// ungültiger Zeigerinhalt gilt nicht
	writeRaw(t, dir, "active", "../x\n")
	if ans := versionsGet(t, a); ans.Active != "" {
		t.Errorf("ungültiger Zeiger: %+v", ans)
	}
}

func TestVersionsDiscardOrphans(t *testing.T) {
	a, dir := versionsAgent(t)
	for _, v := range []string{"2026.11.1", "2026.11.2", "2026.11.3", "2026.11.4"} {
		mkVersion(t, dir, v)
	}
	writeRaw(t, dir, "active", "2026.11.1\n")
	writeRaw(t, dir, "previous", "2026.11.2\n")
	// Einrichtung von 2026.11.3 läuft: bleibt
	a.esppdh.running, a.esppdh.target = true, "2026.11.3"
	a.pruneVersionOrphans(context.Background())
	for v, want := range map[string]bool{"2026.11.1": true, "2026.11.2": true, "2026.11.3": true, "2026.11.4": false} {
		if got := esppdhExists(filepath.Join(dir, v)); got != want {
			t.Errorf("%s vorhanden = %v, erwartet %v", v, got, want)
		}
	}
	// Lauf zu Ende: 2026.11.3 ist verwaist
	a.esppdh.running = false
	a.pruneVersionOrphans(context.Background())
	if esppdhExists(filepath.Join(dir, "2026.11.3")) || !esppdhExists(filepath.Join(dir, "2026.11.1")) || !esppdhExists(filepath.Join(dir, "2026.11.2")) {
		t.Error("Aufräumen nach Ende des Laufs")
	}
	// läuft außerhalb des Agenten eine Einrichtung (Neustart des Agenten), wird nichts gelöscht
	mkVersion(t, dir, "2026.11.5")
	a.esppdhBusy = func(context.Context) bool { return true }
	a.pruneVersionOrphans(context.Background())
	if !esppdhExists(filepath.Join(dir, "2026.11.5")) {
		t.Error("während einer laufenden Einrichtung gelöscht")
	}
}

func TestInstallCandidatePassesVersion(t *testing.T) {
	a, _ := versionsAgent(t)
	release := make(chan struct{})
	var gotScript string
	var gotEnv map[string]string
	a.esppdhRun = func(_ context.Context, script string, env map[string]string) (string, error) {
		gotScript, gotEnv = script, env
		<-release
		return "Version: 2026.11.1\n", nil
	}
	call := func(body string) esppdhAnswer {
		w := httptest.NewRecorder()
		a.esppdhInstallHandler(w, httptest.NewRequest(http.MethodPost, "/v1/esppdh/install", strings.NewReader(body)))
		var ans esppdhAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
			t.Fatalf("Antwort ist kein JSON: %q", w.Body.String())
		}
		return ans
	}
	// ungültige Version: nichts startet
	if ans := call(`{"version":"2026.11.0b1"}`); ans.OK || ans.Error == "" {
		t.Fatalf("ungültige Version angenommen: %+v", ans)
	}
	if ans := call(`{"version":"x"`); ans.OK {
		t.Fatalf("kaputtes JSON angenommen: %+v", ans)
	}
	if st := a.esppdh.status(); st.Running {
		t.Fatal("Lauf trotz ungültiger Version gestartet")
	}
	if ans := call(`{"version":"2026.11.1"}`); !ans.OK || !ans.Running {
		t.Fatalf("Start: %+v", ans)
	}
	if st := a.esppdh.status(); st.Target != "2026.11.1" {
		t.Errorf("Ziel im Status: %+v", st)
	}
	close(release)
	if ans := esppdhWaitDone(t, a); ans.Version != "2026.11.1" {
		t.Fatalf("fertig: %+v", ans)
	}
	if gotEnv["PDH_ESPHOME_VERSION"] != "2026.11.1" || gotEnv["PDH_ESPHOME_REPO"] != "/srv/pdh" || !strings.Contains(gotScript, "--setenv=PDH_ESPHOME_VERSION") {
		t.Errorf("Skript %q Umgebung %v", gotScript, gotEnv)
	}
	// Grundumgebung: keine Version in der Umgebung, Ziel leer
	a.esppdhRun = func(_ context.Context, _ string, env map[string]string) (string, error) {
		gotEnv = env
		return "Version: 2026.10.2\n", nil
	}
	if ans := call(`{}`); !ans.Running {
		t.Fatalf("Start Grundumgebung: %+v", ans)
	}
	esppdhWaitDone(t, a)
	if _, ok := gotEnv["PDH_ESPHOME_VERSION"]; ok || a.esppdh.status().Target != "" {
		t.Errorf("Grundumgebung mit Version: %v, Ziel %q", gotEnv, a.esppdh.status().Target)
	}
}

func TestVersionsDocker(t *testing.T) {
	a, _ := versionsAgent(t)
	a.mode = "docker"
	if ans := versionsGet(t, a); ans.OK || ans.Error != esppdhDockerMsg {
		t.Errorf("GET: %+v", ans)
	}
	if ans := versionsPost(t, a, "activate", "2026.11.1"); ans.OK || ans.Error != esppdhDockerMsg {
		t.Errorf("POST: %+v", ans)
	}
	w := httptest.NewRecorder()
	a.esppdhInstallHandler(w, httptest.NewRequest(http.MethodPost, "/v1/esppdh/install", strings.NewReader(`{"version":"2026.11.1"}`)))
	if !strings.Contains(w.Body.String(), esppdhDockerMsg) {
		t.Errorf("Einrichtung: %s", w.Body.String())
	}
}
