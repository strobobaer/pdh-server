package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ESPPDH: Python-Umgebung mit ESPHome im Hintergrund einrichten (esppdh.go).

type esppdhAnswer struct {
	OK      bool   `json:"ok"`
	Running bool   `json:"running"`
	Done    bool   `json:"done"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

func esppdhCall(t *testing.T, a *updateAgent, method string) (int, esppdhAnswer) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/v1/esppdh/install", strings.NewReader("{}"))
	if method == http.MethodGet {
		a.esppdhStatusHandler(w, r)
	} else {
		a.esppdhInstallHandler(w, r)
	}
	var ans esppdhAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
		t.Fatalf("Antwort ist kein JSON: %q", w.Body.String())
	}
	return w.Code, ans
}

// esppdhWaitDone: Status abfragen, bis der Lauf fertig ist.
func esppdhWaitDone(t *testing.T, a *updateAgent) esppdhAnswer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, ans := esppdhCall(t, a, http.MethodGet)
		if ans.Done || time.Now().After(deadline) {
			return ans
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Im Docker-Betrieb: nichts ausführen, nur der Hinweis – auch beim Status.
func TestEsppdhInstallDocker(t *testing.T) {
	ran := false
	a := &updateAgent{mode: "docker", esppdhRun: func(context.Context, string, map[string]string) (string, error) {
		ran = true
		return "", nil
	}}
	for _, m := range []string{http.MethodPost, http.MethodGet} {
		if _, ans := esppdhCall(t, a, m); ans.OK || ans.Error != "ESPPDH gibt es im Docker-Betrieb nicht" {
			t.Errorf("Docker %s: %+v", m, ans)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if ran {
		t.Error("im Docker-Betrieb ausgeführt")
	}
}

// Start kehrt sofort zurück; ein zweiter Start während des Laufs startet nichts;
// der Status zeigt „läuft“ und danach „fertig“ mit Version.
func TestEsppdhInstallBackground(t *testing.T) {
	release := make(chan struct{})
	var runs atomic.Int32
	var gotScript string
	var gotEnv map[string]string
	a := &updateAgent{mode: "systemd", repoDir: "/srv/pdh", esppdhRun: func(_ context.Context, script string, env map[string]string) (string, error) {
		runs.Add(1)
		gotScript, gotEnv = script, env
		<-release
		return "Collecting esphome==2026.10.2\nSuccessfully installed esphome-2026.10.2\nVersion: 2026.10.2\n", nil
	}}
	if _, ans := esppdhCall(t, a, http.MethodGet); !ans.OK || ans.Running || ans.Done {
		t.Fatalf("vor dem Start: %+v", ans)
	}
	start := time.Now()
	code, ans := esppdhCall(t, a, http.MethodPost)
	if code != http.StatusOK || !ans.OK || !ans.Running || time.Since(start) > 2*time.Second {
		t.Fatalf("Start: %d %+v nach %s", code, ans, time.Since(start))
	}
	if code, ans := esppdhCall(t, a, http.MethodPost); code != http.StatusOK || !ans.OK || !ans.Running {
		t.Fatalf("zweiter Start: %d %+v", code, ans)
	}
	if _, ans := esppdhCall(t, a, http.MethodGet); !ans.Running || ans.Done {
		t.Fatalf("Status während des Laufs: %+v", ans)
	}
	close(release)
	ans = esppdhWaitDone(t, a)
	if !ans.OK || ans.Running || !ans.Done || ans.Version != "2026.10.2" || ans.Error != "" {
		t.Fatalf("fertig: %+v", ans)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("%d Läufe gestartet, erwartet 1", n)
	}
	if gotEnv["PDH_ESPHOME_REPO"] != "/srv/pdh" || !strings.Contains(gotScript, `"$PDH_ESPHOME_REPO/scripts/install-esphome.sh"`) {
		t.Errorf("Skript %q Umgebung %v", gotScript, gotEnv)
	}
}

// Fehler: Status „fertig“ mit der Meldung des Skripts vorn und dem Ende der Ausgabe.
func TestEsppdhInstallFails(t *testing.T) {
	msg := "ESPHome 2026.10.2 braucht Python 3.12 oder neuer. Vorhanden: Python 3.10."
	a := &updateAgent{mode: "systemd", repoDir: "/srv/pdh", esppdhRun: func(context.Context, string, map[string]string) (string, error) {
		return strings.Repeat("Zeile\n", 50) + msg + "\n", errors.New("exit status 1")
	}}
	if _, ans := esppdhCall(t, a, http.MethodPost); !ans.Running {
		t.Fatalf("Start: %+v", ans)
	}
	ans := esppdhWaitDone(t, a)
	if !ans.OK || ans.Running || !ans.Done || ans.Version != "" || !strings.HasPrefix(ans.Error, "Einrichtung fehlgeschlagen: "+msg) ||
		strings.Count(ans.Error, "Zeile") > 20 {
		t.Errorf("Fehler: %+v", ans)
	}
	// ohne Versionszeile gilt es nicht als eingerichtet; ein neuer Start setzt den alten Fehler zurück
	a.esppdhRun = func(context.Context, string, map[string]string) (string, error) { return "fertig\n", nil }
	if _, ans := esppdhCall(t, a, http.MethodPost); !ans.Running || ans.Done || ans.Error != "" {
		t.Fatalf("neuer Start: %+v", ans)
	}
	if ans := esppdhWaitDone(t, a); !ans.Done || ans.Version != "" || !strings.Contains(ans.Error, "keine Version") {
		t.Errorf("ohne Version: %+v", ans)
	}
}

// ── Schutz in scripts/install-esphome.sh (läuft als root): echte Aufrufe mit bash ──

// esppdhScript: absoluter Pfad des Skripts (Schrägstriche, auch für Git Bash unter Windows).
func esppdhScript(t *testing.T) (bash, script string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash nicht verfügbar")
	}
	p, err := filepath.Abs("../../scripts/install-esphome.sh")
	if err != nil {
		t.Fatal(err)
	}
	return bash, filepath.ToSlash(p)
}

// esppdhGuard ruft eine Prüffunktion des Skripts auf (source ohne main) – Ausgabe und Fehler.
func esppdhGuard(t *testing.T, fn string, args ...string) (string, error) {
	t.Helper()
	bash, script := esppdhScript(t)
	out, err := exec.Command(bash, append([]string{"-c", `source "$1"; shift; "$@"`, "esppdh-test", script, fn}, args...)...).CombinedOutput()
	return string(out), err
}

func esppdhExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestEsppdhScriptPathGuard(t *testing.T) {
	for _, p := range []string{"/opt/pdh-esphome", "/var/lib/pdh-esphome", "/srv/esphome/venv"} {
		if out, err := esppdhGuard(t, "esphome_path_ok", "PDH_ESPHOME_DIR", p); err != nil {
			t.Errorf("%s abgelehnt: %v %s", p, err, out)
		}
	}
	for p, why := range map[string]string{
		"/opt/../var": "darf weder", "/opt/pdh-esphome/..": "darf weder", "/opt/./x": "darf weder", "/opt/x/.": "darf weder",
		"/opt//x": "darf weder", "/opt/x/": "darf weder",
		"/var": "zwei Ordnerebenen", "/": "zwei Ordnerebenen", "opt/pdh-esphome": "absoluter Pfad", "": "absoluter Pfad",
	} {
		out, err := esppdhGuard(t, "esphome_path_ok", "PDH_ESPHOME_DIR", p)
		if err == nil || !strings.Contains(out, why) || !strings.Contains(out, "PDH_ESPHOME_DIR") {
			t.Errorf("%q: Fehler %v, Meldung %q (erwartet %s)", p, err, out, why)
		}
	}
}

// Löschen nur einer Python-Umgebung (pyvenv.cfg) oder eines leeren Ordners.
func TestEsppdhScriptRemoveGuard(t *testing.T) {
	dir := t.TempDir()
	fremd := filepath.Join(dir, "fremd")
	_ = os.MkdirAll(filepath.Join(fremd, "postgresql"), 0o755)
	_ = os.WriteFile(filepath.Join(fremd, "postgresql", "PG_VERSION"), []byte("16\n"), 0o644)
	out, err := esppdhGuard(t, "esphome_remove_venv", filepath.ToSlash(fremd))
	if err == nil || !strings.Contains(out, "keine Python-Umgebung (pyvenv.cfg fehlt)") || !esppdhExists(filepath.Join(fremd, "postgresql", "PG_VERSION")) {
		t.Errorf("fremder Ordner: Fehler %v, Meldung %q, noch da: %v", err, out, esppdhExists(fremd))
	}
	datei := filepath.Join(dir, "datei")
	_ = os.WriteFile(datei, []byte("x"), 0o644)
	if _, err := esppdhGuard(t, "esphome_remove_venv", filepath.ToSlash(datei)); err == nil || !esppdhExists(datei) {
		t.Errorf("Datei gelöscht oder angenommen: %v", err)
	}
	venv := filepath.Join(dir, "venv")
	_ = os.MkdirAll(filepath.Join(venv, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644)
	_ = os.WriteFile(filepath.Join(venv, "bin", "python3"), []byte("x"), 0o755)
	if out, err := esppdhGuard(t, "esphome_remove_venv", filepath.ToSlash(venv)); err != nil || esppdhExists(venv) {
		t.Errorf("Python-Umgebung nicht entfernt: %v %s", err, out)
	}
	leer := filepath.Join(dir, "leer")
	_ = os.MkdirAll(leer, 0o755)
	if out, err := esppdhGuard(t, "esphome_remove_venv", filepath.ToSlash(leer)); err != nil || esppdhExists(leer) {
		t.Errorf("leerer Ordner nicht entfernt: %v %s", err, out)
	}
}

// Arbeitsordner: neu oder schon dem Dienstbenutzer gehörend – einen fremden nie übernehmen.
func TestEsppdhScriptWorkdirGuard(t *testing.T) {
	bash, _ := esppdhScript(t)
	dir := t.TempDir()
	vorhanden := filepath.Join(dir, "arbeit")
	_ = os.MkdirAll(vorhanden, 0o755)
	o, err := exec.Command(bash, "-c", `stat -c %U "$1"`, "x", filepath.ToSlash(vorhanden)).Output()
	owner := strings.TrimSpace(string(o))
	if err != nil || owner == "" {
		t.Fatalf("Eigentümer nicht lesbar: %v", err)
	}
	if out, err := esppdhGuard(t, "esphome_workdir_ok", filepath.ToSlash(filepath.Join(dir, "neu")), "pdh"); err != nil {
		t.Errorf("neuer Ordner abgelehnt: %v %s", err, out)
	}
	if out, err := esppdhGuard(t, "esphome_workdir_ok", filepath.ToSlash(vorhanden), owner); err != nil {
		t.Errorf("eigener Ordner abgelehnt: %v %s", err, out)
	}
	out, err := esppdhGuard(t, "esphome_workdir_ok", filepath.ToSlash(vorhanden), "pdh-gibt-es-nicht")
	if err == nil || !strings.Contains(out, "gehört "+owner+" und wird nicht übernommen") {
		t.Errorf("fremder Ordner angenommen: %v %q", err, out)
	}
	datei := filepath.Join(dir, "datei")
	_ = os.WriteFile(datei, []byte("x"), 0o644)
	if out, err := esppdhGuard(t, "esphome_workdir_ok", filepath.ToSlash(datei), owner); err == nil || !strings.Contains(out, "kein Ordner") {
		t.Errorf("Datei als Arbeitsordner angenommen: %v %q", err, out)
	}
}

// Das ganze Skript: ein falscher Pfad hält es an, bevor es irgendetwas tut (auch vor der root-Prüfung).
func TestEsppdhScriptStopsOnBadPath(t *testing.T) {
	bash, script := esppdhScript(t)
	for env, want := range map[string]string{
		"PDH_ESPHOME_DIR=/opt/../var":         "PDH_ESPHOME_DIR: '/opt/../var' darf weder",
		"PDH_ESPHOME_WORKDIR=/var":            "PDH_ESPHOME_WORKDIR: '/var' muss ein absoluter Pfad mit mindestens zwei Ordnerebenen sein.",
		"PDH_ESPHOME_WORKDIR=/var/lib/../etc": "PDH_ESPHOME_WORKDIR: '/var/lib/../etc' darf weder",
	} {
		cmd := exec.Command(bash, script)
		cmd.Env = append(os.Environ(), env)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(string(out), want) || strings.Contains(string(out), "root") {
			t.Errorf("%s: %v %q", env, err, out)
		}
	}
}

// Kandidat: nur gültige, stabile Versionen (JJJJ.M.P) – nichts anderes landet in einem Pfad.
func TestEsppdhVersionGuard(t *testing.T) {
	for _, v := range []string{"2026.11.1", "2026.1.12", "2027.12.100"} {
		if out, err := esppdhGuard(t, "esphome_version_ok", v); err != nil {
			t.Errorf("%s abgelehnt: %v %s", v, err, out)
		}
	}
	for _, v := range []string{"2026.11.0b1", "1.2", "2026.11.1; rm", "", "../x", "2026.11.1/..", "2026.11.1 "} {
		out, err := esppdhGuard(t, "esphome_version_ok", v)
		if err == nil || !strings.Contains(out, "Ungültige ESPHome-Version") {
			t.Errorf("%q: Fehler %v, Meldung %q", v, err, out)
		}
	}
	// Zeilenumbruch mitten in der Version: in Bash selbst gebildet (ein Argument aus Go verliert ihn unter Windows)
	bash, script := esppdhScript(t)
	out, err := exec.Command(bash, "-c", `source "$1"; esphome_version_ok $'2026.11.1\n2026.11.2'`, "x", script).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Ungültige ESPHome-Version") {
		t.Errorf("Version mit Zeilenumbruch: Fehler %v, Meldung %q", err, out)
	}
}

// Mit PDH_ESPHOME_VERSION ist der Zielordner immer <Versionsordner>/<Version> – ein mitgegebenes
// PDH_ESPHOME_DIR wird ignoriert; ohne bleibt es bei PDH_ESPHOME_DIR bzw. der Grundumgebung.
func TestEsppdhCandidateIgnoresDir(t *testing.T) {
	bash, script := esppdhScript(t)
	run := func(env ...string) string {
		t.Helper()
		cmd := exec.Command(bash, "-c", `source "$1"; esphome_venv_dir`, "x", script)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v", env, err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run("PDH_ESPHOME_VERSION=2026.11.1", "PDH_ESPHOME_DIR=/opt/pdh-esphome"); got != "/opt/pdh-esphome-versions/2026.11.1" {
		t.Errorf("Kandidat: %q", got)
	}
	if got := run("PDH_ESPHOME_VERSION=2026.11.1", "PDH_ESPHOME_DIR=/etc/boese"); got != "/opt/pdh-esphome-versions/2026.11.1" {
		t.Errorf("Kandidat mit fremdem Ordner: %q", got)
	}
	if got := run("PDH_ESPHOME_VERSION=", "PDH_ESPHOME_DIR=/srv/esphome/venv"); got != "/srv/esphome/venv" {
		t.Errorf("mit PDH_ESPHOME_DIR: %q", got)
	}
	if got := run("PDH_ESPHOME_VERSION=", "PDH_ESPHOME_DIR="); got != "/opt/pdh-esphome" {
		t.Errorf("Grundumgebung: %q", got)
	}
	// das ganze Skript bricht bei ungültiger Version ab, bevor etwas geschieht
	cmd := exec.Command(bash, script)
	cmd.Env = append(os.Environ(), "PDH_ESPHOME_VERSION=2026.11.0b1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(string(out), "Ungültige ESPHome-Version") || strings.Contains(string(out), "root") {
		t.Errorf("ungültige Version: %v %q", err, out)
	}
}

// install_update.sh: ESPHome nur, wenn eingerichtet; vor dem Agent-Neustart und außerhalb
// seiner cgroup (sonst beendet der Neustart pip); ein Fehler ist nur eine Warnung.
func TestEsppdhUpdateScripts(t *testing.T) {
	u, err := os.ReadFile("../../install_update.sh")
	if err != nil {
		t.Fatal(err)
	}
	us := string(u)
	i := strings.Index(us, "--unit=pdh-esphome-update-")
	k := strings.Index(us, "--unit=pdh-update-agent-restart")
	if !strings.Contains(us, `if [[ -x /opt/pdh-esphome/bin/esphome ]]; then`) || i < 0 || k < 0 || i > k ||
		!strings.Contains(us, `"$repo_dir/scripts/install-esphome.sh"`) ||
		!strings.Contains(us, "WARNING: ESPHome konnte nicht aktualisiert werden – Update trotzdem fertig.") {
		t.Error("install_update.sh: ESPHome-Block fehlt, steht nach dem Agent-Neustart oder bricht das Update ab")
	}
	b, err := os.ReadFile("../../scripts/install-esphome.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`/opt/pdh-esphome`, `/var/lib/pdh-esphome`, `"$script_dir/esphome.version"`, `python3 -m venv "$venv"`,
		"python3-venv", `--upgrade "esphome==$version"`, "flock", `install -d -m 0750 -o "$svc_user"`, `"$venv/bin/esphome" version`,
		"braucht Python $py_min oder neuer"} {
		if !strings.Contains(s, want) {
			t.Errorf("install-esphome.sh ohne %q", want)
		}
	}
	// Python-Prüfung vor dem Anlegen der Umgebung
	if p, v := strings.Index(s, "py_ok python3 ||"), strings.Index(s, `python3 -m venv "$venv"`); p < 0 || v < 0 || p > v {
		t.Error("install-esphome.sh prüft die Python-Version nicht vor dem Anlegen der Umgebung")
	}
	if strings.Contains(s, "\r") {
		t.Error("install-esphome.sh mit CRLF – läuft unter Linux nicht")
	}
}
