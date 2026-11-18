package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ESPPDH: Python-Umgebung mit ESPHome einrichten (esppdh.go).

type esppdhAnswer struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

func esppdhCall(t *testing.T, a *updateAgent) (int, esppdhAnswer) {
	t.Helper()
	w := httptest.NewRecorder()
	a.esppdhInstallHandler(w, httptest.NewRequest("POST", "/v1/esppdh/install", strings.NewReader("{}")))
	var ans esppdhAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
		t.Fatalf("Antwort ist kein JSON: %q", w.Body.String())
	}
	return w.Code, ans
}

// Im Docker-Betrieb: nichts ausführen, nur der Hinweis.
func TestEsppdhInstallDocker(t *testing.T) {
	ran := false
	a := &updateAgent{mode: "docker", esppdhRun: func(context.Context, string, map[string]string) (string, error) {
		ran = true
		return "", nil
	}}
	_, ans := esppdhCall(t, a)
	if ans.OK || ans.Error != "ESPPDH gibt es im Docker-Betrieb nicht" {
		t.Errorf("Docker: %+v", ans)
	}
	if ran {
		t.Error("im Docker-Betrieb ausgeführt")
	}
}

// systemd: scripts/install-esphome.sh aus dem Repository, Version aus der letzten Zeile.
func TestEsppdhInstallRuns(t *testing.T) {
	var gotScript string
	var gotEnv map[string]string
	a := &updateAgent{mode: "systemd", repoDir: "/srv/pdh", esppdhRun: func(_ context.Context, script string, env map[string]string) (string, error) {
		gotScript, gotEnv = script, env
		return "Collecting esphome==2026.10.2\nSuccessfully installed esphome-2026.10.2\nVersion: 2026.10.2\n", nil
	}}
	code, ans := esppdhCall(t, a)
	if code != http.StatusOK || !ans.OK || ans.Version != "2026.10.2" || ans.Error != "" {
		t.Fatalf("Antwort %d %+v", code, ans)
	}
	if gotEnv["PDH_ESPHOME_REPO"] != "/srv/pdh" || !strings.Contains(gotScript, `"$PDH_ESPHOME_REPO/scripts/install-esphome.sh"`) {
		t.Errorf("Skript %q Umgebung %v", gotScript, gotEnv)
	}
}

// Fehler: ok=false mit dem Ende der Ausgabe, damit der Anwender die Ursache sieht.
func TestEsppdhInstallFails(t *testing.T) {
	a := &updateAgent{mode: "systemd", repoDir: "/srv/pdh", esppdhRun: func(context.Context, string, map[string]string) (string, error) {
		return strings.Repeat("Zeile\n", 50) + "ERROR: No matching distribution found for esphome==2026.10.2\n", errors.New("exit status 1")
	}}
	_, ans := esppdhCall(t, a)
	if ans.OK || !strings.Contains(ans.Error, "No matching distribution found") || strings.Count(ans.Error, "Zeile") > 20 {
		t.Errorf("Fehler: %+v", ans)
	}
	// ohne Versionszeile gilt es nicht als eingerichtet
	a.esppdhRun = func(context.Context, string, map[string]string) (string, error) { return "fertig\n", nil }
	if _, ans := esppdhCall(t, a); ans.OK || ans.Error == "" {
		t.Errorf("ohne Version: %+v", ans)
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
	s, err := os.ReadFile("../../scripts/install-esphome.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`/opt/pdh-esphome`, `/var/lib/pdh-esphome`, `"$script_dir/esphome.version"`, `python3 -m venv "$venv"`,
		"python3-venv", `--upgrade "esphome==$version"`, "flock", `install -d -m 0750 -o "$svc_user"`, `"$venv/bin/esphome" version`} {
		if !strings.Contains(string(s), want) {
			t.Errorf("install-esphome.sh ohne %q", want)
		}
	}
	if strings.Contains(string(s), "\r") {
		t.Error("install-esphome.sh mit CRLF – läuft unter Linux nicht")
	}
}

// Zwei Klicks kurz nacheinander: der zweite wartet nicht, sondern bekommt 409.
func TestEsppdhInstallBusy(t *testing.T) {
	if !esppdhBusy.TryLock() {
		t.Fatal("Sperre schon belegt")
	}
	defer esppdhBusy.Unlock()
	a := &updateAgent{mode: "systemd", esppdhRun: func(context.Context, string, map[string]string) (string, error) {
		t.Error("trotz laufender Einrichtung ausgeführt")
		return "", nil
	}}
	if code, ans := esppdhCall(t, a); code != http.StatusConflict || ans.OK || !strings.Contains(ans.Error, "läuft bereits") {
		t.Errorf("belegt: %d %+v", code, ans)
	}
}
