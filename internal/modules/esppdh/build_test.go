package esppdh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Bauen der Firmware (build.go). Statt ESPHome läuft eine Attrappe: Das Testprogramm ruft sich
// selbst auf (TestHelperEsphomeBuild) – das geht unter Windows und Linux gleich.

const (
	testBuildID = "0198c0de-0000-7000-8000-000000000001"
	testSecret  = "Geheim-WLAN-4711"
	// testOddSecret enthält Zeichen, die ESPHome im C++-Quelltext maskiert („"“, „\“, „ö“);
	// testOddCpp ist die Schreibweise in main.cpp (cpp_string_escape: oktal), wie der Compiler
	// sie in einer Fehlermeldung zitiert.
	testOddSecret = `Pa"ss\wört`
	testOddCpp    = `Pa\042ss\134w\303\266rt`
)

// helperMark: erstes Argument nach „--“ – so erkennt sich das Testprogramm als Attrappe. Die
// Einstellungen der Attrappe kommen als Argumente, weil Build ESPHome nur eine knappe Umgebung
// mitgibt (Variablen des Tests kämen nicht an).
const helperMark = "esphome-attrappe"

// helperArgs: Argumente des Aufrufs nach „--“.
func helperArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// TestHelperEsphomeBuild ist kein Test, sondern die Attrappe von „esphome compile …“. Aufruf:
// -- esphome-attrappe <modus> <ablage> <kennwort> <argumente von ESPHome …>
func TestHelperEsphomeBuild(t *testing.T) {
	all := helperArgs()
	if len(all) < 4 || all[0] != helperMark {
		return
	}
	mode, out, secret, args := all[1], all[2], all[3], all[4:]
	switch mode {
	case "ok":
		cwd, _ := os.Getwd()
		st, _ := os.Stat(cwd)
		var cfg, sec []byte
		if len(args) == 2 {
			cfg, _ = os.ReadFile(args[1])
			sec, _ = os.ReadFile("secrets.yaml")
		}
		tmp := os.Getenv("TMPDIR")
		tst, terr := os.Stat(tmp)
		if terr == nil {
			_ = os.WriteFile(filepath.Join(tmp, "ccXXXX.s"), []byte(`.string "`+secret+`"`), 0o600) // wie gcc
		}
		var keys []string
		for _, kv := range os.Environ() {
			keys = append(keys, strings.SplitN(kv, "=", 2)[0])
		}
		info, _ := json.Marshal(map[string]any{"args": args, "cwd": cwd, "mode": fmt.Sprintf("%o", st.Mode().Perm()),
			"config": string(cfg), "secrets": string(sec), "pio": os.Getenv("PLATFORMIO_CORE_DIR"), "xdg": os.Getenv("XDG_CACHE_HOME"),
			"ccache": os.Getenv("ESPHOME_CCACHE_ENABLE"), "idfccache": os.Getenv("IDF_CCACHE_ENABLE"),
			"tmp": tmp, "tmpdir": terr == nil && tst.IsDir(), "env": keys, "geheim": os.Getenv("PDH_TEST_SECRET_ENV")})
		_ = os.WriteFile(filepath.Join(out, "info.json"), info, 0o600)
		fmt.Println("INFO ESPHome 2099.1.0")
		fmt.Println("INFO Reading configuration " + args[len(args)-1] + "...")
		// wie ein Compiler, der eine Zeile aus main.cpp zitiert – mit Kennwort
		fmt.Println(`src/main.cpp:42:3: note: wifi_wifiap->set_password("` + secret + `");`)
		fmt.Fprintln(os.Stderr, "\x1b[32mINFO Compiling app\x1b[0m")
		time.Sleep(150 * time.Millisecond) // Zeit für einen Zwischenstand
		bd := filepath.Join(".esphome", "build", "esppdh-geraet", "build")
		_ = os.MkdirAll(bd, 0o755)
		_ = os.WriteFile(filepath.Join(bd, "firmware.factory.bin"), []byte("FACTORY-BYTES"), 0o644)
		_ = os.WriteFile(filepath.Join(bd, "firmware.ota.bin"), []byte("OTA-BYTES"), 0o644)
		_ = os.WriteFile(filepath.Join(bd, "main.cpp"), []byte(`set_password("`+secret+`")`), 0o644)
		fmt.Println("INFO Successfully compiled program.")
	case "fail":
		fmt.Println("INFO ESPHome 2099.1.0")
		fmt.Println(`src/main.cpp:42:3: error: expected ';' before 'mqtt_mqttclientcomponent->set_password("` + testOddCpp + `")'`)
		fmt.Println("mqtt password " + testOddSecret + " abc")
		os.Exit(2)
	case "upload", "uploadfail":
		// Attrappe von „esphome upload … --file firmware.ota.bin“: merkt Argumente und Datei
		var fw []byte
		for i, a := range args {
			if a == "--file" && i+1 < len(args) {
				fw, _ = os.ReadFile(args[i+1])
			}
		}
		info, _ := json.Marshal(map[string]any{"args": args, "firmware": string(fw)})
		_ = os.WriteFile(filepath.Join(out, "upload.json"), info, 0o600)
		fmt.Println("INFO Connecting to 192.0.2.50 port 3232... auth " + secret)
		if mode == "uploadfail" {
			fmt.Println("ERROR Connecting to 192.0.2.50 port 3232 failed: timed out")
			os.Exit(1)
		}
		fmt.Println("INFO OTA successful")
	case "nofirmware":
		fmt.Println("INFO ESPHome 2099.1.0")
	case "hang":
		// startet einen Kindprozess (wie PlatformIO den Compiler) und hängt; das Kind teilt die
		// Ausgabe und arbeitet außerhalb des Arbeitsordners (Windows könnte ihn sonst nicht löschen)
		child := exec.Command(os.Args[0], "-test.run=^TestHelperEsphomeBuild$", "--", helperMark, "sleep", out, secret)
		child.Dir = out
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Println("kind:", err)
			os.Exit(3)
		}
		_ = os.WriteFile(filepath.Join(out, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		fmt.Println("INFO Compiling …")
		time.Sleep(time.Minute)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "spam":
		line := strings.Repeat("x", 1000) + "\n"
		for i := 0; i < 3*1024; i++ { // gut 3 MiB
			fmt.Print(line)
		}
		fmt.Println("ENDE " + secret)
		os.Exit(1)
	}
	os.Exit(0)
}

// fakeRunner: Runner mit Attrappe im Modus mode; Arbeitsordner und Ablage für die Attrappe sind
// eigene Ordner des Tests. calls zählt die Aufrufe von Command.
func fakeRunner(t *testing.T, mode string) (r *Runner, work, out string, calls *atomic.Int32) {
	t.Helper()
	work, out = t.TempDir(), t.TempDir()
	t.Setenv("PDH_ESPHOME_WORKDIR", work)
	t.Setenv("PDH_TEST_SECRET_ENV", "datenbank-kennwort") // darf ESPHome nie erreichen
	calls = &atomic.Int32{}
	r = &Runner{Timeout: time.Minute, MinFreeMB: 0, Command: func(ctx context.Context, dir string, args ...string) *exec.Cmd {
		calls.Add(1)
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestHelperEsphomeBuild$", "--", helperMark, mode, out, testSecret}, args...)...)
		cmd.Dir = dir
		return cmd
	}}
	return r, work, out, calls
}

func testJob() BuildJob {
	return BuildJob{ID: testBuildID, Slug: "geraet", Config: []byte("esphome:\n  name: esppdh-geraet\n"),
		Secrets: []byte("wifi_password: !literal \"" + testSecret + "\"\n"), SecretValues: []string{testSecret, testOddSecret}}
}

// assertGone: der Arbeitsordner des Laufs samt Geheimnis-Datei ist weg.
func assertGone(t *testing.T, work string) {
	t.Helper()
	dir := filepath.Join(work, "build", testBuildID)
	if _, err := os.Stat(filepath.Join(dir, "secrets.yaml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Geheimnis-Datei noch da: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Arbeitsordner noch da: %v", err)
	}
}

func TestRunnerOK(t *testing.T) {
	r, work, out, _ := fakeRunner(t, "ok")
	old := progressEvery
	t.Cleanup(func() { progressEvery = old })
	progressEvery = 20 * time.Millisecond
	var mu sync.Mutex
	var progress []string
	var returned atomic.Bool
	res := r.Build(context.Background(), testJob(), func(log string) {
		if returned.Load() {
			t.Error("Zwischenstand nach dem Ende des Baus")
		}
		mu.Lock()
		progress = append(progress, log)
		mu.Unlock()
	})
	returned.Store(true)
	if !res.OK || string(res.Factory) != "FACTORY-BYTES" || string(res.OTA) != "OTA-BYTES" || res.Version != "2099.1.0" {
		t.Fatalf("Ergebnis: ok=%v factory=%q ota=%q version=%q\n%s", res.OK, res.Factory, res.OTA, res.Version, res.Log)
	}
	if strings.Contains(res.Log, testSecret) || !strings.Contains(res.Log, "set_password(\"•••\")") {
		t.Errorf("Kennwort nicht geschwärzt:\n%s", res.Log)
	}
	if strings.Contains(res.Log, "\x1b[") || !strings.Contains(res.Log, "INFO Compiling app") {
		t.Errorf("Farbcodes im Verlauf oder Zeile fehlt:\n%q", res.Log)
	}
	mu.Lock()
	if len(progress) == 0 {
		t.Error("kein Zwischenstand während des Baus")
	}
	for _, p := range progress {
		if strings.Contains(p, testSecret) {
			t.Errorf("Zwischenstand mit Kennwort: %q", p)
		}
	}
	mu.Unlock()

	// Aufruf: esphome compile <kennung>.yaml im eigenen Arbeitsordner, mit beiden Dateien
	var info struct {
		Args, Env                                                            []string
		Cwd, Mode, Config, Secrets, Pio, Xdg, Ccache, Idfccache, Tmp, Geheim string
		Tmpdir                                                               bool
	}
	b, err := os.ReadFile(filepath.Join(out, "info.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	// Konfiguration unter festem Namen (nie secrets.yaml, egal wie das Gerät heißt); „./“: der
	// Name kann nie als Schalter von ESPHome gelesen werden
	if strings.Join(info.Args, " ") != "compile ./esppdh.yaml" {
		t.Errorf("Aufruf: %q", info.Args)
	}
	// Zwischendateien des Compilers (mit Kennwörtern) im Arbeitsordner – sie gehen mit ihm
	if info.Tmp != filepath.Join(info.Cwd, "tmp") || !info.Tmpdir {
		t.Errorf("TMPDIR=%q (Ordner da: %v), erwartet %s", info.Tmp, info.Tmpdir, filepath.Join(info.Cwd, "tmp"))
	}
	// knappe Umgebung: nichts aus der Umgebung des PDH außer dem Nötigen
	allowed := map[string]bool{"SYSTEMROOT": true} // fügt Go unter Windows selbst an
	for _, k := range append(envKeep, "PLATFORMIO_CORE_DIR", "XDG_CACHE_HOME", "ESPHOME_DATA_DIR", "ESPHOME_BUILD_PATH",
		"ESPHOME_CCACHE_ENABLE", "IDF_CCACHE_ENABLE", "PYTHONUNBUFFERED", "TMPDIR") {
		allowed[strings.ToUpper(k)] = true
	}
	var hasPath bool
	for _, k := range info.Env {
		if !allowed[strings.ToUpper(k)] {
			t.Errorf("Variable %q erreicht ESPHome", k)
		}
		hasPath = hasPath || strings.EqualFold(k, "PATH")
	}
	if info.Geheim != "" || !hasPath {
		t.Errorf("PDH_TEST_SECRET_ENV=%q erreicht ESPHome oder PATH fehlt (%v)", info.Geheim, info.Env)
	}
	if filepath.Base(info.Cwd) != testBuildID || filepath.Base(filepath.Dir(info.Cwd)) != "build" {
		t.Errorf("Arbeitsordner: %s", info.Cwd)
	}
	if runtime.GOOS != "windows" && info.Mode != "700" {
		t.Errorf("Rechte des Arbeitsordners: %s, erwartet 700", info.Mode)
	}
	if info.Config != string(testJob().Config) || info.Secrets != string(testJob().Secrets) {
		t.Errorf("Dateien: %q / %q", info.Config, info.Secrets)
	}
	if info.Pio != filepath.Join(work, "platformio") || info.Xdg != filepath.Join(work, "cache") {
		t.Errorf("Zwischenspeicher: PLATFORMIO_CORE_DIR=%q XDG_CACHE_HOME=%q", info.Pio, info.Xdg)
	}
	if info.Ccache != "0" || info.Idfccache != "0" {
		t.Errorf("ccache nicht abgeschaltet: %q %q", info.Ccache, info.Idfccache)
	}
	assertGone(t, work)
	if st, err := os.Stat(filepath.Join(work, "platformio")); err != nil || !st.IsDir() {
		t.Errorf("Zwischenspeicher von PlatformIO fehlt: %v", err)
	}
}

// Ein Gerät mit der Kennung „secrets“: Konfiguration und Geheimnis-Datei bleiben getrennt (die
// Konfiguration hieße sonst secrets.yaml und würde von den Geheimnissen überschrieben).
func TestRunnerSlugSecrets(t *testing.T) {
	r, work, out, _ := fakeRunner(t, "ok")
	job := testJob()
	job.Slug = "secrets"
	job.Config = []byte("esphome:\n  name: esppdh-secrets\n")
	if res := r.Build(context.Background(), job, nil); !res.OK {
		t.Fatalf("Bau: %s", res.Log)
	}
	var info struct {
		Args            []string
		Config, Secrets string
	}
	b, err := os.ReadFile(filepath.Join(out, "info.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	if info.Config != string(job.Config) || info.Secrets != string(job.Secrets) || slices.Contains(info.Args, "./secrets.yaml") {
		t.Errorf("Aufruf %q, Konfiguration %q, Geheimnisse %q", info.Args, info.Config, info.Secrets)
	}
	assertGone(t, work)
}

// ESPHome meldet einen Fehler und zitiert dabei ein Kennwort – auch in der Schreibweise des
// C++-Quelltexts. Beides wird geschwärzt; der Ordner ist trotzdem weg.
func TestRunnerFailRedacted(t *testing.T) {
	r, work, _, _ := fakeRunner(t, "fail")
	res := r.Build(context.Background(), testJob(), nil)
	if res.OK || res.Factory != nil {
		t.Fatalf("Fehler nicht erkannt: %+v", res)
	}
	for _, s := range []string{testOddSecret, testOddCpp} {
		if strings.Contains(res.Log, s) {
			t.Errorf("Kennwort %q im Verlauf:\n%s", s, res.Log)
		}
	}
	if !strings.Contains(res.Log, "Fehler") || !strings.Contains(res.Log, "2") || !strings.Contains(res.Log, "mqtt password ••• abc") {
		t.Errorf("Verlauf:\n%s", res.Log)
	}
	assertGone(t, work)
}

func TestRunnerNoFirmware(t *testing.T) {
	r, work, _, _ := fakeRunner(t, "nofirmware")
	res := r.Build(context.Background(), testJob(), nil)
	if res.OK || !strings.Contains(res.Log, "firmware.factory.bin") {
		t.Fatalf("ohne Firmware-Datei: ok=%v\n%s", res.OK, res.Log)
	}
	assertGone(t, work)
}

// Ein hängender Bau endet nach dem Zeitlimit: Prozessgruppe beendet, Ordner weg.
func TestRunnerTimeoutKillsChildren(t *testing.T) {
	r, work, out, _ := fakeRunner(t, "hang")
	r.Timeout = 300 * time.Millisecond
	start := time.Now()
	res := r.Build(context.Background(), testJob(), nil)
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("Zeitlimit wirkt nicht: %s", took)
	}
	if res.OK || !strings.Contains(res.Log, "Zeitlimit") {
		t.Fatalf("Ergebnis: ok=%v\n%s", res.OK, res.Log)
	}
	assertGone(t, work)

	b, err := os.ReadFile(filepath.Join(out, "child.pid"))
	if err != nil {
		t.Fatalf("Kindprozess wurde nicht gestartet: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if runtime.GOOS == "windows" {
		// Unter Windows gibt es keine Prozessgruppe: Beendet wird nur der Prozess selbst
		// (build_other.go). Das Zielsystem ist Linux; dort prüft dieser Test das Kind mit.
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		t.Skip("Kindprozess wird nur unter Unix mit der Prozessgruppe beendet")
	}
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("Kindprozess %d läuft noch", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Abbruch von außen (Knopf „Abbrechen“, Herunterfahren): Bau endet, Ordner weg.
func TestRunnerCanceled(t *testing.T) {
	r, work, out, _ := fakeRunner(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	res := r.Build(ctx, testJob(), nil)
	if res.OK || !strings.Contains(res.Log, "abgebrochen") || strings.Contains(res.Log, "Zeitlimit") {
		t.Fatalf("Ergebnis: ok=%v\n%s", res.OK, res.Log)
	}
	assertGone(t, work)
	if b, err := os.ReadFile(filepath.Join(out, "child.pid")); err == nil && runtime.GOOS == "windows" {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill() // unter Windows bleibt das Kind sonst bis zu 30 s stehen
			}
		}
	}
}

// Zu wenig Platz: sofort Fehler, ESPHome wird gar nicht erst aufgerufen.
func TestRunnerDiskFull(t *testing.T) {
	r, work, _, calls := fakeRunner(t, "ok")
	old := freeSpaceMB
	t.Cleanup(func() { freeSpaceMB = old })
	freeSpaceMB = func(string) int64 { return 100 }
	r.MinFreeMB = 2048
	res := r.Build(context.Background(), testJob(), nil)
	if res.OK || !strings.Contains(res.Log, "Zu wenig Platz") || !strings.Contains(res.Log, "100 MB") || !strings.Contains(res.Log, "2048 MB") {
		t.Fatalf("Ergebnis: ok=%v\n%s", res.OK, res.Log)
	}
	if calls.Load() != 0 {
		t.Errorf("ESPHome trotz vollem Laufwerk aufgerufen (%d)", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(work, "build", testBuildID)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Arbeitsordner angelegt: %v", err)
	}
	// unter Linux mit dem echten freien Platz
	if runtime.GOOS == "linux" {
		freeSpaceMB = old
		r.MinFreeMB = 1 << 50
		if res := r.Build(context.Background(), testJob(), nil); res.OK || !strings.Contains(res.Log, "Zu wenig Platz") || calls.Load() != 0 {
			t.Errorf("echter Platz: ok=%v Aufrufe %d\n%s", res.OK, calls.Load(), res.Log)
		}
	}
}

// Ungültiger Auftrag (Kennung mit Pfadteilen): kein Ordner, kein Aufruf.
func TestRunnerRejectsBadJob(t *testing.T) {
	r, work, _, calls := fakeRunner(t, "ok")
	for _, j := range []BuildJob{{ID: "../x", Slug: "geraet"}, {ID: testBuildID, Slug: "../böse"}, {ID: "", Slug: "geraet"},
		{ID: testBuildID, Slug: "-ab"}, {ID: testBuildID, Slug: "--help"}, {ID: testBuildID, Slug: "ab-"}} {
		if res := r.Build(context.Background(), j, nil); res.OK {
			t.Errorf("%+v angenommen", j)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("ESPHome aufgerufen (%d)", calls.Load())
	}
	if entries, _ := os.ReadDir(filepath.Join(work, "build")); len(entries) != 0 {
		t.Errorf("Ordner angelegt: %v", entries)
	}
}

// Viel Ausgabe: der Verlauf bleibt höchstens 256 KiB groß, das Ende bleibt erhalten.
func TestRunnerLogLimit(t *testing.T) {
	r, work, _, _ := fakeRunner(t, "spam")
	res := r.Build(context.Background(), testJob(), nil)
	if res.OK || len(res.Log) > MaxLog || !strings.Contains(res.Log, "gekürzt") || !strings.Contains(res.Log, "ENDE •••") {
		t.Fatalf("ok=%v Länge %d, Ende %q", res.OK, len(res.Log), res.Log[max(0, len(res.Log)-200):])
	}
	assertGone(t, work)
}

func TestRedact(t *testing.T) {
	log := "Verbinde mit WLAN, Kennwort " + testSecret + " …\nC++: \"" + testOddCpp + "\"\nYAML: \"" + `Pa\"ss\\wört` + "\"\nkurz: abcd und xyz\n"
	got := Redact(log, []string{testSecret, testOddSecret, "abcd", "xyz", ""})
	for _, s := range []string{testSecret, testOddSecret, testOddCpp, `Pa\"ss\\wört`, "abcd"} {
		if strings.Contains(got, s) {
			t.Errorf("%q nicht geschwärzt:\n%s", s, got)
		}
	}
	if !strings.Contains(got, "Kennwort ••• …") || !strings.Contains(got, "und xyz") {
		t.Errorf("Ergebnis:\n%s", got)
	}

	// 1 MiB: höchstens 256 KiB, Anfang und Ende bleiben, mit Hinweiszeile
	var b strings.Builder
	b.WriteString("ANFANG\n")
	for b.Len() < 1<<20 {
		b.WriteString(strings.Repeat("y", 99) + "\n")
	}
	b.WriteString("Kennwort " + testSecret + " ENDE\n")
	got = Redact(b.String(), []string{testSecret})
	if len(got) > MaxLog || len(got) < MaxLog/2 {
		t.Errorf("Länge %d, erwartet höchstens %d", len(got), MaxLog)
	}
	if !strings.HasPrefix(got, "ANFANG\n") || !strings.HasSuffix(got, "Kennwort ••• ENDE\n") || !strings.Contains(got, "… gekürzt") {
		t.Errorf("Anfang, Ende oder Hinweis fehlt: %q … %q", got[:20], got[len(got)-40:])
	}
	if strings.Contains(got, testSecret) {
		t.Error("Kennwort nach dem Kürzen sichtbar")
	}
}

// Beim Start räumt das PDH Arbeitsordner eines abgestürzten Laufs weg (samt Geheimnis-Datei);
// der Zwischenspeicher der Werkzeuge bleibt.
func TestCleanupBuilds(t *testing.T) {
	work := t.TempDir()
	t.Setenv("PDH_ESPHOME_WORKDIR", work)
	old := filepath.Join(work, "build", testBuildID)
	if err := os.MkdirAll(filepath.Join(old, ".esphome"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "secrets.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pio := filepath.Join(work, "platformio", "packages")
	if err := os.MkdirAll(pio, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CleanupBuilds(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("alter Arbeitsordner noch da: %v", err)
	}
	if _, err := os.Stat(pio); err != nil {
		t.Errorf("Zwischenspeicher gelöscht: %v", err)
	}
	// ohne Arbeitsordner: kein Fehler
	t.Setenv("PDH_ESPHOME_WORKDIR", filepath.Join(work, "gibt-es-nicht"))
	if err := CleanupBuilds(); err != nil {
		t.Errorf("ohne Ordner: %v", err)
	}
}

// Die Vorgaben des echten Aufrufs: 30 min, 2 GB, nice -n 19 <venv>/bin/esphome.
func TestNewRunner(t *testing.T) {
	r := NewRunner()
	if r.Timeout != 30*time.Minute || r.MinFreeMB != 2048 || r.Command == nil {
		t.Fatalf("Vorgaben: %+v", r)
	}
	t.Setenv("PDH_ESPHOME_DIR", "/srv/esphome")
	cmd := r.Command(context.Background(), "/arbeit", "compile", "x.yaml")
	want := filepath.Join("/srv/esphome", "bin", "esphome")
	args := strings.Join(cmd.Args, " ")
	if cmd.Dir != "/arbeit" || !strings.HasSuffix(args, want+" compile x.yaml") {
		t.Errorf("Aufruf %q in %q", args, cmd.Dir)
	}
	if _, err := exec.LookPath("nice"); err == nil && !strings.Contains(args, "-n 19 "+want) {
		t.Errorf("ohne nice -n 19: %q", args)
	}
}
