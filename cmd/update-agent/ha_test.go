package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// haFakeSocket: Fake-pdh-ha auf einem Unix-Socket, PDH_HA_SOCKET zeigt darauf.
func haFakeSocket(t *testing.T, body string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("Unix-Socket nicht möglich: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("PDH_HA_SOCKET", sock)
	return sock
}

func TestDRIsActive(t *testing.T) {
	a := &updateAgent{mode: "systemd"}
	ctx := context.Background()

	t.Run("Datei fehlt", func(t *testing.T) {
		t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
		if ok, err := a.drIsActive(ctx); !ok || err != nil {
			t.Fatalf("ohne pdh-ha: %v %v", ok, err)
		}
	})
	t.Run("aktiv", func(t *testing.T) {
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv"}}`)
		if ok, err := a.drIsActive(ctx); !ok || err != nil {
			t.Fatalf("aktiv: %v %v", ok, err)
		}
	})
	t.Run("passiv", func(t *testing.T) {
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
		if ok, err := a.drIsActive(ctx); ok || err != nil {
			t.Fatalf("passiv: %v %v", ok, err)
		}
	})
	t.Run("nie eingerichtet", func(t *testing.T) {
		haFakeSocket(t, `{"ok":true,"configured":false,"self":{"role":""}}`)
		if ok, err := a.drIsActive(ctx); !ok || err != nil {
			t.Fatalf("uneingerichtet: %v %v", ok, err)
		}
	})
	t.Run("Ladefehler", func(t *testing.T) {
		haFakeSocket(t, `{"ok":true,"configured":false,"error":"Konfiguration nicht lesbar: x"}`)
		if ok, err := a.drIsActive(ctx); ok || err == nil {
			t.Fatalf("Ladefehler: %v %v", ok, err)
		}
	})
	t.Run("kein Server", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "api.sock")
		if err := os.WriteFile(sock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PDH_HA_SOCKET", sock)
		if ok, err := a.drIsActive(ctx); ok || err == nil {
			t.Fatalf("ohne Dienst: %v %v", ok, err)
		}
	})
}

// Auf dem passiven Knoten pausiert die Schleife; ist pdh-ha nicht erreichbar,
// steht das im Fehler.
func TestDRTickPausedOnPassive(t *testing.T) {
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	target := t.TempDir()
	a := &updateAgent{mode: "docker"} // Pfade ohne /proc/1/root
	a.dr.Config = drDefaults()
	a.dr.Config.Target = target

	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); st.Paused != "passiver Knoten" || st.Error != "" {
		t.Fatalf("passiv: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(target, "wal")); err == nil {
		t.Fatal("passiver Knoten hat das Ziel angefasst")
	}

	sock := filepath.Join(t.TempDir(), "api.sock")
	_ = os.WriteFile(sock, nil, 0o600)
	t.Setenv("PDH_HA_SOCKET", sock)
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); !strings.Contains(st.Error, "pdh-ha nicht erreichbar – Sicherung pausiert") || st.Paused != "" {
		t.Fatalf("nicht erreichbar: %+v", st)
	}

	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv"}}`)
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); st.Paused != "" || strings.Contains(st.Error, "pdh-ha") {
		t.Fatalf("aktiv: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(target, "wal")); err != nil {
		t.Fatal("aktiver Knoten verschiebt nicht")
	}
}

func TestHAInstallDockerRefused(t *testing.T) {
	a := &updateAgent{mode: "docker"}
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/ha/install", strings.NewReader(`{"addrs":["10.0.0.1"]}`)),
		httptest.NewRequest(http.MethodGet, "/v1/ha/install", nil),
	} {
		rec := httptest.NewRecorder()
		if r.Method == http.MethodPost {
			a.haInstallHandler(rec, r)
		} else {
			a.haInstallStatusHandler(rec, r)
		}
		var res map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if res["ok"] != false || res["error"] != "Ausfallschutz mit zwei Servern nur bei systemd-Installationen (Docker ist das Testsystem)" {
			t.Fatalf("%s im Docker-Modus: %s", r.Method, rec.Body.String())
		}
	}
}

func TestHAInstallRejectsBadAddr(t *testing.T) {
	a := &updateAgent{mode: "systemd", haRun: func(context.Context, string, map[string]string, io.Writer) error {
		t.Error("Installation trotz ungültiger Adresse gestartet")
		return nil
	}}
	for _, body := range []string{`{"addrs":["10.0.0.1;x"]}`, `{"addrs":[]}`, `{"addrs":["10.0.0.1","pdh.local"]}`, `kaputt`} {
		rec := httptest.NewRecorder()
		a.haInstallHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/ha/install", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: Status %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestHAInstallRuns(t *testing.T) {
	release := make(chan struct{})
	var gotScript string
	var gotEnv map[string]string
	a := &updateAgent{mode: "systemd", repoDir: "/opt/pdh-server", haRun: func(_ context.Context, script string, env map[string]string, out io.Writer) error {
		gotScript, gotEnv = script, env
		for i := 0; i < 50; i++ {
			_, _ = io.WriteString(out, "Zeile\n")
		}
		_, _ = io.WriteString(out, "fertig\n")
		<-release
		return nil
	}}
	rec := httptest.NewRecorder()
	a.haInstallHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/ha/install", strings.NewReader(`{"addrs":["192.168.1.10","fd00::1"]}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("Start: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.haInstallHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/ha/install", strings.NewReader(`{"addrs":["192.168.1.10"]}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "läuft bereits") {
		t.Fatalf("zweiter Start: %d %s", rec.Code, rec.Body.String())
	}
	close(release)
	var st haInstallStatus
	for i := 0; i < 200; i++ {
		rec = httptest.NewRecorder()
		a.haInstallStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/v1/ha/install", nil))
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		if st.Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !st.Done || st.Running || st.Error != "" {
		t.Fatalf("Status: %+v", st)
	}
	if n := strings.Count(st.Log, "\n") + 1; n > 40 || !strings.HasSuffix(st.Log, "fertig") {
		t.Fatalf("Protokoll (%d Zeilen): %q", n, st.Log)
	}
	if gotEnv["PDH_HA_REPO"] != "/opt/pdh-server" || gotEnv["PDH_HA_ADDRS"] != "192.168.1.10,fd00::1" {
		t.Fatalf("Umgebung: %v", gotEnv)
	}
	if strings.Contains(gotScript, "192.168") || !strings.Contains(gotScript, `"$PDH_HA_ADDRS"`) || !strings.Contains(gotScript, `"configured":true`) {
		t.Fatalf("Skript: %s", gotScript)
	}
	drBashSyntax(t, "ha-install", gotScript)
}

func TestHAInstallScriptContent(t *testing.T) {
	b, err := os.ReadFile("../../scripts/install-pdh-ha.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"RuntimeDirectory=pdh-ha", "-X main.commit=", "-X main.version=", "iputils-ping",
		"ExecStart=/usr/local/bin/pdh-ha run", "Restart=always", "RestartSec=2", "RuntimeDirectoryMode=0750",
		"RuntimeDirectoryPreserve=yes", "StateDirectory=pdh-ha", "groupadd --system pdh", "GOTOOLCHAIN=auto"} {
		if !strings.Contains(s, want) {
			t.Errorf("install-pdh-ha.sh ohne %q", want)
		}
	}
	if strings.Contains(s, "ufw allow") {
		t.Error("install-pdh-ha.sh öffnet ufw selbst (macht pdh-ha)")
	}
	j, err := os.ReadFile("../../scripts/pdh-ha-join.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://github.com/strobobaer/pdh-server.git", "/opt/pdh-ha-src", "tr '_-' '/+'", "--update", "--witness", "iputils-ping"} {
		if !strings.Contains(string(j), want) {
			t.Errorf("pdh-ha-join.sh ohne %q", want)
		}
	}
	u, err := os.ReadFile("../../install_update.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(u), `if [[ -x /usr/local/bin/pdh-ha ]]; then bash "$repo_dir/scripts/install-pdh-ha.sh" "$repo_dir"`) {
		t.Error("install_update.sh baut pdh-ha nicht mit")
	}
}
