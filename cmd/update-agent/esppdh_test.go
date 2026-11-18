package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
