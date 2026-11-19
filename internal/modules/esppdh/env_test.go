package esppdh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Zustand der Python-Umgebung (env.go). Statt eines echten ESPHome läuft eine Attrappe:
// Das Testprogramm ruft sich selbst auf (TestHelperEsphome) – das geht auch unter Windows,
// wo sich ein Shell-Skript bin/esphome nicht starten lässt.

// TestHelperEsphome ist kein Test, sondern die Attrappe von „esphome version“.
func TestHelperEsphome(t *testing.T) {
	mode := os.Getenv("PDH_ESPHOME_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "version":
		fmt.Println("INFO ESPHome startet")
		fmt.Println("Version: " + os.Getenv("PDH_ESPHOME_HELPER_VERSION"))
	case "fail":
		fmt.Fprintln(os.Stderr, "Traceback (most recent call last):")
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'esphome'")
		os.Exit(1)
	case "noversion":
		fmt.Println("Hallo")
	case "hang":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// fakeEnv legt eine Python-Umgebung mit Attrappe bin/esphome an (Shell-Skript, gibt
// „Version: 2099.1.0“ aus) und leitet den Aufruf auf TestHelperEsphome um. Rückgabe: Ordner
// und die Programme, mit denen CheckEnv „version“ aufgerufen hat.
func fakeEnv(t *testing.T, mode, version string) (string, *[]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "esphome"), []byte("#!/bin/sh\necho 'Version: 2099.1.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_ESPHOME_DIR", dir)
	t.Setenv("PDH_ESPHOME_WORKDIR", filepath.Join(dir, "arbeit")) // gibt es nicht: freier Platz vom Elternordner
	calls := &[]string{}
	old := versionCmd
	t.Cleanup(func() { versionCmd = old })
	versionCmd = func(ctx context.Context, bin string) *exec.Cmd {
		*calls = append(*calls, bin)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperEsphome$")
		cmd.Env = append(os.Environ(), "PDH_ESPHOME_HELPER="+mode, "PDH_ESPHOME_HELPER_VERSION="+version)
		return cmd
	}
	return dir, calls
}

func TestCheckEnvInstalledOutdated(t *testing.T) {
	dir, calls := fakeEnv(t, "version", "2099.1.0")
	st := CheckEnv(context.Background())
	if !st.Installed || st.Version != "2099.1.0" || !st.Outdated || st.Problem != "" {
		t.Fatalf("Zustand: %+v", st)
	}
	if want := filepath.Join(dir, "bin", "esphome"); len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("Aufruf %v, erwartet %s", *calls, want)
	}
	switch {
	case runtime.GOOS == "linux" && st.FreeMB <= 0:
		t.Errorf("freier Platz unter Linux unbekannt: %d", st.FreeMB)
	case runtime.GOOS != "linux" && st.FreeMB != -1:
		t.Errorf("freier Platz außerhalb von Linux: %d, erwartet -1 (unbekannt)", st.FreeMB)
	}
}

func TestCheckEnvCurrent(t *testing.T) {
	fakeEnv(t, "version", PinnedVersion())
	if st := CheckEnv(context.Background()); !st.Installed || st.Version != PinnedVersion() || st.Outdated || st.Problem != "" {
		t.Fatalf("Zustand: %+v", st)
	}
}

// Ohne Umgebung: nicht eingerichtet, aber kein Problem – und kein Aufruf.
func TestCheckEnvMissing(t *testing.T) {
	_, calls := fakeEnv(t, "version", "2099.1.0")
	t.Setenv("PDH_ESPHOME_DIR", filepath.Join(t.TempDir(), "gibt-es-nicht"))
	st := CheckEnv(context.Background())
	if st.Installed || st.Version != "" || st.Outdated || st.Problem != "" {
		t.Fatalf("Zustand: %+v", st)
	}
	if len(*calls) != 0 {
		t.Errorf("ESPHome aufgerufen, obwohl es fehlt: %v", *calls)
	}
}

// EnvPresent prüft nur, ob das Programm da ist – ohne ESPHome aufzurufen (für Seiten).
func TestEnvPresent(t *testing.T) {
	_, calls := fakeEnv(t, "version", "2099.1.0")
	if !EnvPresent() {
		t.Error("Umgebung da: EnvPresent falsch")
	}
	t.Setenv("PDH_ESPHOME_DIR", filepath.Join(t.TempDir(), "gibt-es-nicht"))
	if EnvPresent() {
		t.Error("Umgebung fehlt: EnvPresent wahr")
	}
	if len(*calls) != 0 {
		t.Errorf("ESPHome aufgerufen: %v", *calls)
	}
}

// Umgebung da, ESPHome startet aber nicht oder nennt keine Version: Problem im Klartext.
func TestCheckEnvBroken(t *testing.T) {
	fakeEnv(t, "fail", "")
	st := CheckEnv(context.Background())
	if st.Installed || st.Outdated || !strings.Contains(st.Problem, "No module named 'esphome'") {
		t.Fatalf("Zustand: %+v", st)
	}
	fakeEnv(t, "noversion", "")
	if st := CheckEnv(context.Background()); st.Installed || !strings.Contains(st.Problem, "keine Version") {
		t.Fatalf("Zustand ohne Version: %+v", st)
	}
}

// Ein hängendes ESPHome hält die Seite nicht auf: Abbruch nach dem Zeitlimit.
func TestCheckEnvTimeout(t *testing.T) {
	fakeEnv(t, "hang", "")
	old := envTimeout
	t.Cleanup(func() { envTimeout = old })
	envTimeout = 300 * time.Millisecond
	start := time.Now()
	st := CheckEnv(context.Background())
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("Zeitlimit wirkt nicht: %s", took)
	}
	if st.Installed || !strings.Contains(st.Problem, "Zeitlimit") {
		t.Fatalf("Zustand: %+v", st)
	}
}

// Unter Linux: das echte „<venv>/bin/esphome version“ mit dem Shell-Skript als Attrappe.
func TestCheckEnvRealCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Shell-Skript lässt sich unter Windows nicht starten (TestCheckEnvInstalledOutdated deckt CheckEnv ab)")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "esphome"), []byte("#!/bin/sh\n[ \"$1\" = version ] || exit 2\necho 'Version: 2099.1.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_ESPHOME_DIR", dir)
	if st := CheckEnv(context.Background()); !st.Installed || st.Version != "2099.1.0" || !st.Outdated {
		t.Fatalf("Zustand: %+v", st)
	}
}

func TestPinnedVersion(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "esphome.version"))
	if err != nil {
		t.Fatal(err)
	}
	v := PinnedVersion()
	if v != strings.TrimSpace(string(b)) {
		t.Errorf("eingebettet %q, Datei %q", v, strings.TrimSpace(string(b)))
	}
	if !regexp.MustCompile(`^\d{4}\.\d{1,2}\.\d+$`).MatchString(v) {
		t.Errorf("keine stabile ESPHome-Version: %q", v)
	}
}

func TestDirsOverridable(t *testing.T) {
	t.Setenv("PDH_ESPHOME_DIR", "")
	t.Setenv("PDH_ESPHOME_WORKDIR", "")
	if venvDir() != VenvDir || workDir() != WorkDir {
		t.Errorf("Standard: %s %s", venvDir(), workDir())
	}
	t.Setenv("PDH_ESPHOME_DIR", " /srv/esphome ")
	t.Setenv("PDH_ESPHOME_WORKDIR", "/srv/arbeit")
	if venvDir() != "/srv/esphome" || workDir() != "/srv/arbeit" {
		t.Errorf("überschrieben: %s %s", venvDir(), workDir())
	}
}
