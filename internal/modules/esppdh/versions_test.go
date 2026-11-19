package esppdh

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Mehrere Umgebungen nebeneinander (versions.go), Konfigurationsprüfung (Runner.Config) und
// Zustand je Server.

// versionsEnv richtet einen eigenen Versionen-Ordner und eine Grundumgebung ein.
func versionsEnv(t *testing.T) (versions, base string) {
	t.Helper()
	versions, base = t.TempDir(), t.TempDir()
	t.Setenv("PDH_ESPHOME_VERSIONS_DIR", versions)
	t.Setenv("PDH_ESPHOME_DIR", base)
	return versions, base
}

func setPointer(t *testing.T, versions, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(versions, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkVersion(t *testing.T, versions, v string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(versions, v, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestVersionsDirOverridable(t *testing.T) {
	t.Setenv("PDH_ESPHOME_VERSIONS_DIR", "")
	if VersionsDirPath() != VersionsDir {
		t.Errorf("Standard: %s", VersionsDirPath())
	}
	t.Setenv("PDH_ESPHOME_VERSIONS_DIR", " /srv/versionen ")
	if VersionsDirPath() != "/srv/versionen" {
		t.Errorf("überschrieben: %s", VersionsDirPath())
	}
}

func TestVersionBin(t *testing.T) {
	versions, _ := versionsEnv(t)
	if got, want := VersionBin("2099.1.0"), filepath.Join(versions, "2099.1.0", "bin", "esphome"); got != want {
		t.Errorf("VersionBin = %q, erwartet %q", got, want)
	}
	for _, bad := range []string{"", "../x", "2099.1", "2099.1.0b1", "latest", "2099.1.0/../.."} {
		if got := VersionBin(bad); got != "" {
			t.Errorf("VersionBin(%q) = %q, erwartet leer", bad, got)
		}
	}
}

func TestActiveBinFallback(t *testing.T) {
	versions, base := versionsEnv(t)
	baseBin := filepath.Join(base, "bin", "esphome")
	newer := "2099.1.0"
	if CompareVersions(newer, PinnedVersion()) <= 0 {
		t.Fatalf("Testversion %s nicht neuer als %s", newer, PinnedVersion())
	}

	// ohne Zeiger: Grundumgebung
	if ActiveBin() != baseBin || esphomeBin() != baseBin || ActiveVersionPointer() != "" {
		t.Errorf("ohne Zeiger: %q / %q", ActiveBin(), ActiveVersionPointer())
	}

	// Zeiger auf vorhandene neuere Version: deren Programm (Zeilenende erlaubt)
	mkVersion(t, versions, newer)
	setPointer(t, versions, "active", newer+"\n")
	if want := filepath.Join(versions, newer, "bin", "esphome"); ActiveBin() != want || esphomeBin() != want || ActiveVersionPointer() != newer {
		t.Errorf("neuere Version: %q / %q, erwartet %q", ActiveBin(), ActiveVersionPointer(), want)
	}

	// Zeiger auf fehlenden Ordner
	setPointer(t, versions, "active", "2099.2.0\n")
	if ActiveBin() != baseBin || ActiveVersionPointer() != "" {
		t.Errorf("fehlender Ordner: %q", ActiveBin())
	}

	// Zeiger älter als die Mindestversion (Ordner da)
	mkVersion(t, versions, "2000.1.0")
	setPointer(t, versions, "active", "2000.1.0\n")
	if ActiveBin() != baseBin || ActiveVersionPointer() != "" {
		t.Errorf("zu alt: %q", ActiveBin())
	}

	// Zeiger mit ../x (auch wenn der Ordner existiert)
	if err := os.MkdirAll(filepath.Join(versions, "..", filepath.Base(versions)+"-x", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(versions, "..", filepath.Base(versions)+"-x")) })
	setPointer(t, versions, "active", "../"+filepath.Base(versions)+"-x\n")
	if ActiveBin() != baseBin || ActiveVersionPointer() != "" {
		t.Errorf("../x: %q", ActiveBin())
	}
}

func TestPreviousVersionPointer(t *testing.T) {
	versions, _ := versionsEnv(t)
	if got := PreviousVersionPointer(); got != "" {
		t.Errorf("ohne Datei: %q", got)
	}
	setPointer(t, versions, "previous", "base\n")
	if got := PreviousVersionPointer(); got != "base" {
		t.Errorf("base: %q", got)
	}
	mkVersion(t, versions, "2099.1.0")
	setPointer(t, versions, "previous", "2099.1.0")
	if got := PreviousVersionPointer(); got != "2099.1.0" {
		t.Errorf("vorhandene Version: %q", got)
	}
	setPointer(t, versions, "previous", "2099.5.0\n") // Ordner fehlt
	if got := PreviousVersionPointer(); got != "" {
		t.Errorf("fehlender Ordner: %q", got)
	}
	mkVersion(t, versions, "2000.1.0")
	setPointer(t, versions, "previous", "2000.1.0\n") // älter als Mindestversion
	if got := PreviousVersionPointer(); got != "" {
		t.Errorf("zu alt: %q", got)
	}
	setPointer(t, versions, "previous", "../x\n")
	if got := PreviousVersionPointer(); got != "" {
		t.Errorf("../x: %q", got)
	}
}

// Veraltet heißt: älter als die Mindestversion – gleich und neuer sind nicht veraltet.
func TestOutdatedOnlyWhenOlder(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{{"2000.1.0", true}, {PinnedVersion(), false}, {"2099.1.0", false}}
	for _, c := range cases {
		fakeEnv(t, "version", c.version)
		st := CheckEnv(context.Background())
		if !st.Installed || st.Outdated != c.want {
			t.Errorf("Version %s: %+v, Outdated erwartet %v", c.version, st, c.want)
		}
		if !st.Base {
			t.Errorf("Version %s: Grundumgebung nicht erkannt: %+v", c.version, st)
		}
	}
}

// Läuft eine Version aus dem Versionen-Ordner, ist Base falsch und deren Programm wird gefragt.
func TestCheckEnvActiveVersion(t *testing.T) {
	_, calls := fakeEnv(t, "version", "2099.1.0")
	versions := t.TempDir()
	t.Setenv("PDH_ESPHOME_VERSIONS_DIR", versions)
	mkVersion(t, versions, "2099.1.0")
	bin := VersionBin("2099.1.0")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setPointer(t, versions, "active", "2099.1.0\n")
	st := CheckEnv(context.Background())
	if !st.Installed || st.Base || st.Outdated {
		t.Fatalf("Zustand: %+v", st)
	}
	if len(*calls) != 1 || (*calls)[0] != bin {
		t.Errorf("Aufruf %v, erwartet %s", *calls, bin)
	}
}

func TestFreeMBAt(t *testing.T) {
	if FreeMBAt(t.TempDir()) != freeMB(t.TempDir()) {
		t.Error("FreeMBAt weicht von freeMB ab")
	}
}

// ── Konfigurationsprüfung ─────────────────────────────────────

// fakeConfigRunner: Attrappe für „esphome config ./esppdh.yaml“; sie hält Argumente, Ordner und
// den Inhalt von secrets.yaml fest.
type configProbe struct {
	mu          sync.Mutex
	args        []string
	dir         string
	hadSecrets  bool
	hadTmp      bool
	runnerCalls int
}

func fakeConfigRunner(t *testing.T, mode string) (*Runner, string, *configProbe) {
	t.Helper()
	work, out := t.TempDir(), t.TempDir()
	t.Setenv("PDH_ESPHOME_WORKDIR", work)
	t.Setenv("PDH_TEST_SECRET_ENV", "datenbank-kennwort")
	p := &configProbe{}
	r := &Runner{Timeout: time.Minute, Command: func(ctx context.Context, dir string, args ...string) *exec.Cmd {
		p.mu.Lock()
		p.runnerCalls++
		p.args, p.dir = append([]string(nil), args...), dir
		_, serr := os.Stat(filepath.Join(dir, secretsName))
		_, terr := os.Stat(filepath.Join(dir, "tmp"))
		p.hadSecrets, p.hadTmp = serr == nil, terr == nil
		p.mu.Unlock()
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestHelperEsphomeBuild$", "--", helperMark, mode, out, testSecret}, args...)...)
		cmd.Dir = dir
		return cmd
	}}
	return r, work, p
}

func TestRunnerConfig(t *testing.T) {
	r, work, p := fakeConfigRunner(t, "fail")
	job := testJob()
	log, err := r.Config(context.Background(), job)
	if err == nil {
		t.Fatalf("Fehler (Exit-Code 2) nicht gemeldet:\n%s", log)
	}
	if strings.Join(p.args, " ") != "config ./esppdh.yaml" {
		t.Errorf("Argumente: %v", p.args)
	}
	if !p.hadSecrets || !p.hadTmp {
		t.Errorf("Laufordner ohne secrets.yaml/tmp: %+v", p)
	}
	if !strings.HasPrefix(p.dir, filepath.Join(work, "build")) {
		t.Errorf("Laufordner %q nicht unter %s", p.dir, work)
	}
	for _, s := range []string{testOddSecret, testOddCpp} {
		if strings.Contains(log, s) {
			t.Errorf("Kennwort %q im Verlauf:\n%s", s, log)
		}
	}
	if !strings.Contains(log, "mqtt password ••• abc") || !strings.Contains(err.Error(), "2") {
		t.Errorf("Verlauf/Fehler: %v\n%s", err, log)
	}
	assertGone(t, work)
	if _, serr := os.Stat(p.dir); !errors.Is(serr, fs.ErrNotExist) {
		t.Errorf("Laufordner noch da: %v", serr)
	}
}

func TestRunnerConfigOK(t *testing.T) {
	r, work, _ := fakeConfigRunner(t, "nofirmware") // beendet mit 0 und nur einer Zeile
	log, err := r.Config(context.Background(), testJob())
	if err != nil || !strings.Contains(log, "INFO ESPHome 2099.1.0") {
		t.Fatalf("Prüfung: %v\n%s", err, log)
	}
	assertGone(t, work)
}

func TestRunnerConfigRejectsBadJob(t *testing.T) {
	r, _, p := fakeConfigRunner(t, "ok")
	job := testJob()
	job.ID = "../x"
	if _, err := r.Config(context.Background(), job); err == nil || p.runnerCalls != 0 {
		t.Errorf("ungültiger Auftrag: err=%v, Aufrufe=%d", err, p.runnerCalls)
	}
}

func TestRunnerConfigTimeout(t *testing.T) {
	r, work, _ := fakeConfigRunner(t, "sleep")
	old := configTimeout
	t.Cleanup(func() { configTimeout = old })
	configTimeout = 300 * time.Millisecond
	start := time.Now()
	log, err := r.Config(context.Background(), testJob())
	if err == nil || !strings.Contains(log, "Zeitlimit") {
		t.Fatalf("Zeitlimit: err=%v\n%s", err, log)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("Zeitlimit wirkt nicht: %s", took)
	}
	assertGone(t, work)
}

// Runner.Bin: leer = aktive Umgebung, sonst genau dieses Programm.
func TestRunnerBin(t *testing.T) {
	versions, base := versionsEnv(t)
	r := NewRunner()
	last := func() string {
		args := strings.Fields(strings.Join(r.Command(context.Background(), "/arbeit", "config", "x.yaml").Args, " "))
		for _, a := range args {
			if strings.HasSuffix(a, "esphome") {
				return a
			}
		}
		return ""
	}
	if got, want := last(), filepath.Join(base, "bin", "esphome"); got != want {
		t.Errorf("ohne Bin: %q, erwartet %q", got, want)
	}
	r.Bin = filepath.Join(versions, "2099.1.0", "bin", "esphome")
	if got := last(); got != r.Bin {
		t.Errorf("mit Bin: %q, erwartet %q", got, r.Bin)
	}
}

// ── Zustand je Server ─────────────────────────────────────────

func TestUpdateStateRoundTrip(t *testing.T) {
	work := t.TempDir()
	t.Setenv("PDH_ESPHOME_WORKDIR", filepath.Join(work, "neu")) // Ordner gibt es noch nicht
	if st := LoadUpdateState(); !reflect.DeepEqual(st, UpdateState{}) {
		t.Fatalf("ohne Datei: %+v", st)
	}
	in := UpdateState{CheckedAt: time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC), Result: "ok", Installed: "2099.1.0"}
	in.AddFailed("2099.3.0", "0.82.1", "")
	in.AddFailed("2099.2.0", "0.82.1", "Fehler\n")
	in.AddNotified("2099.3.0")
	in.AddNotified("2099.2.0")
	if err := SaveUpdateState(in); err != nil {
		t.Fatal(err)
	}
	out := LoadUpdateState()
	if !out.CheckedAt.Equal(in.CheckedAt) {
		t.Errorf("CheckedAt: %v", out.CheckedAt)
	}
	out.CheckedAt = in.CheckedAt
	if !reflect.DeepEqual(out, in) {
		t.Errorf("Zustand: %+v, erwartet %+v", out, in)
	}
	path := filepath.Join(work, "neu", "update-state.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("Rechte %o, erwartet 600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("Temp-Datei übrig: %v", entries)
	}
}

func TestUpdateStateCorrupt(t *testing.T) {
	work := t.TempDir()
	t.Setenv("PDH_ESPHOME_WORKDIR", work)
	if err := os.WriteFile(filepath.Join(work, "update-state.json"), []byte("{kaputt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := LoadUpdateState(); !reflect.DeepEqual(st, UpdateState{}) {
		t.Errorf("kaputte Datei: %+v", st)
	}
	// überschreiben geht trotzdem
	if err := SaveUpdateState(UpdateState{Installed: "2099.1.0"}); err != nil {
		t.Fatal(err)
	}
	if st := LoadUpdateState(); st.Installed != "2099.1.0" {
		t.Errorf("nach Speichern: %+v", st)
	}
}

// Eine Zustandsdatei aus der Zeit vor den Listen (nur Einzelfelder) gilt weiter: Die gescheiterte
// Version wird nicht erneut versucht, die gemeldete nicht noch einmal gemeldet.
func TestUpdateStateOldFile(t *testing.T) {
	work := t.TempDir()
	t.Setenv("PDH_ESPHOME_WORKDIR", work)
	old := `{"CheckedAt":"2026-11-19T08:30:00Z","Result":"x","Installed":"2026.10.2",` +
		`"FailedVersion":"2026.11.1","FailedPDH":"0.82.1","FailedLog":"Fehler","NotifiedVersion":"2026.11.1"}`
	if err := os.WriteFile(filepath.Join(work, "update-state.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st := LoadUpdateState()
	if !reflect.DeepEqual(st.Failed, []FailedEntry{{Version: "2026.11.1", PDH: "0.82.1"}}) || !reflect.DeepEqual(st.Notified, []string{"2026.11.1"}) {
		t.Fatalf("Einzelfelder nicht übernommen: %+v", st)
	}
	if !reflect.DeepEqual(st.SkipFor("0.82.1"), []string{"2026.11.1"}) || len(st.SkipFor("0.82.2")) != 0 || !st.WasNotified("2026.11.1") {
		t.Errorf("SkipFor/WasNotified: %v %v %v", st.SkipFor("0.82.1"), st.SkipFor("0.82.2"), st.WasNotified("2026.11.1"))
	}
	// zweimal laden und speichern verdoppelt nichts
	if err := SaveUpdateState(st); err != nil {
		t.Fatal(err)
	}
	if st := LoadUpdateState(); len(st.Failed) != 1 || len(st.Notified) != 1 {
		t.Errorf("verdoppelt: %+v", st)
	}
}

// Gescheiterte und gemeldete Versionen: Menge ohne Doppel, höchstens 50, ältere fallen weg;
// nach dem Umschalten entfällt alles bis einschließlich der neuen Version.
func TestUpdateStateLists(t *testing.T) {
	var st UpdateState
	st.AddFailed("2026.11.1", "0.82.1", "a")
	st.AddFailed("2026.11.0", "0.82.1", "b")
	st.AddFailed("2026.11.1", "0.82.2", "c") // erneut gescheitert unter neuer PDH-Version: rückt ans Ende
	want := []FailedEntry{{"2026.11.0", "0.82.1"}, {"2026.11.1", "0.82.2"}}
	if !reflect.DeepEqual(st.Failed, want) || st.FailedVersion != "2026.11.1" || st.FailedPDH != "0.82.2" || st.FailedLog != "c" {
		t.Errorf("AddFailed: %+v", st)
	}
	if got := st.SkipFor("0.82.2"); !reflect.DeepEqual(got, []string{"2026.11.1"}) {
		t.Errorf("SkipFor: %v", got)
	}
	st.AddNotified("2026.11.1")
	st.AddNotified("2026.11.1")
	if len(st.Notified) != 1 || st.NotifiedVersion != "2026.11.1" {
		t.Errorf("AddNotified doppelt: %+v", st.Notified)
	}
	for i := range 60 {
		v := "2027.1." + strconv.Itoa(i)
		st.AddFailed(v, "0.82.2", "")
		st.AddNotified(v)
	}
	if len(st.Failed) != 50 || len(st.Notified) != 50 || st.Failed[0].Version != "2027.1.10" || st.Notified[49] != "2027.1.59" {
		t.Errorf("Obergrenze: %d %d %v %v", len(st.Failed), len(st.Notified), st.Failed[0], st.Notified[49])
	}

	var a UpdateState
	a.AddFailed("2026.11.2", "0.82.1", "")
	a.AddFailed("2026.11.0", "0.82.1", "x")
	a.AddNotified("2026.11.2")
	a.AddNotified("2026.11.0")
	a.Activated("2026.11.1")
	if !reflect.DeepEqual(a.Failed, []FailedEntry{{"2026.11.2", "0.82.1"}}) || !reflect.DeepEqual(a.Notified, []string{"2026.11.2"}) ||
		a.FailedVersion != "" || a.FailedLog != "" || a.NotifiedVersion != "" {
		t.Errorf("Activated: %+v", a)
	}
}
