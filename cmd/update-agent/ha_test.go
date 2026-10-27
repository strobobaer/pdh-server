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

// haInstalled: node.json in PDH_HA_ETC – pdh-ha gilt als eingerichtet.
func haInstalled(t *testing.T) {
	t.Helper()
	etc := t.TempDir()
	if err := os.WriteFile(filepath.Join(etc, "node.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDH_HA_ETC", etc)
}

func TestDRIsActive(t *testing.T) {
	a := &updateAgent{mode: "systemd"}
	ctx := context.Background()

	t.Run("ohne node.json", func(t *testing.T) {
		t.Setenv("PDH_HA_ETC", t.TempDir())
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
		if ok, err := a.drIsActive(ctx); !ok || err != nil {
			t.Fatalf("ohne pdh-ha: %v %v", ok, err)
		}
	})
	t.Run("Socket fehlt", func(t *testing.T) {
		haInstalled(t)
		t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
		if ok, err := a.drIsActive(ctx); ok || err == nil {
			t.Fatalf("eingerichtet, Socket fehlt: %v %v", ok, err)
		}
	})
	t.Run("aktiv", func(t *testing.T) {
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv"}}`)
		if ok, err := a.drIsActive(ctx); !ok || err != nil {
			t.Fatalf("aktiv: %v %v", ok, err)
		}
	})
	t.Run("passiv", func(t *testing.T) {
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
		if ok, err := a.drIsActive(ctx); ok || err != nil {
			t.Fatalf("passiv: %v %v", ok, err)
		}
	})
	t.Run("nie eingerichtet", func(t *testing.T) {
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":false,"self":{"role":""}}`)
		if ok, err := a.drIsActive(ctx); !ok || err != nil {
			t.Fatalf("uneingerichtet: %v %v", ok, err)
		}
	})
	t.Run("Ladefehler", func(t *testing.T) {
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":false,"error":"Konfiguration nicht lesbar: x"}`)
		if ok, err := a.drIsActive(ctx); ok || err == nil {
			t.Fatalf("Ladefehler: %v %v", ok, err)
		}
	})
	t.Run("kein Server", func(t *testing.T) {
		haInstalled(t)
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

// haEnforce: Sperre wie ab Teil 3 (Standard, Testhook).
func haEnforce(t *testing.T) {
	t.Helper()
	old := haGateEnforced
	haGateEnforced = true
	t.Cleanup(func() { haGateEnforced = old })
}

// haObserve: Sperre aus wie in Teil 2 (Beobachtung, Testhook).
func haObserve(t *testing.T) {
	t.Helper()
	old := haGateEnforced
	haGateEnforced = false
	t.Cleanup(func() { haGateEnforced = old })
}

// Beobachtung (Teil 1): manuelle Sicherung auf dem passiven Knoten läuft, Hinweis steht im Status.
func TestDRRunObservedOnPassive(t *testing.T) {
	haObserve(t)
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	haInstalled(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
	a := &updateAgent{mode: "systemd"}
	a.dr.Config = drDefaults()
	a.dr.Config.Enabled = true
	a.dr.Running = "files" // Lauf kommt bis drStartRun, ohne wirklich zu sichern
	rec := httptest.NewRecorder()
	a.drRunHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/dr/run", strings.NewReader(`{"what":"base"}`)))
	if strings.Contains(rec.Body.String(), "pausiert") || !strings.Contains(rec.Body.String(), "läuft bereits") {
		t.Fatalf("Beobachtung darf nicht sperren: %s", rec.Body.String())
	}
	if st := a.drStatus(); st.Paused != "" || st.Advice != "Beobachtung: würde pausieren – passiver Knoten" {
		t.Fatalf("Status: %+v", st)
	}
}

// Manuelle Sicherung auf dem passiven Knoten abgelehnt (Sperre aktiv).
func TestDRRunRefusedOnPassive(t *testing.T) {
	haEnforce(t)
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	haInstalled(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
	a := &updateAgent{mode: "systemd"}
	a.dr.Config = drDefaults()
	a.dr.Config.Enabled = true
	rec := httptest.NewRecorder()
	a.drRunHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/dr/run", strings.NewReader(`{"what":"base"}`)))
	if !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "Sicherung pausiert: passiver Knoten") {
		t.Fatalf("passiv: %s", rec.Body.String())
	}
	if a.drStatus().Running != "" {
		t.Fatal("Lauf trotzdem gestartet")
	}
}

// Beobachtung (Teil 1): die Schleife sichert auch auf dem passiven Knoten
// und bei nicht erreichbarem pdh-ha; nur der Hinweis zeigt, was künftig passieren würde.
func TestDRTickObservedOnPassive(t *testing.T) {
	haObserve(t)
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	target := t.TempDir()
	a := &updateAgent{mode: "docker", drSysID: drTestSysID}
	a.dr.Config = drDefaults()
	a.dr.Config.Target = target
	haInstalled(t)

	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); st.Paused != "" || st.Error != "" || st.Advice != "Beobachtung: würde pausieren – passiver Knoten" {
		t.Fatalf("passiv: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(target, "wal")); err != nil {
		t.Fatal("Beobachtung hat die Sicherung gestoppt")
	}

	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); st.Paused != "" || st.Error != "" || st.Advice != "Beobachtung: würde pausieren – pdh-ha nicht erreichbar" {
		t.Fatalf("nicht erreichbar: %+v", st)
	}

	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv"}}`)
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); st.Advice != "" || st.Paused != "" {
		t.Fatalf("aktiv: %+v", st)
	}
}

// Sperre aktiv: auf dem passiven Knoten pausiert die Schleife; ist pdh-ha nicht
// erreichbar, steht das im Fehler.
func TestDRTickPausedOnPassive(t *testing.T) {
	haEnforce(t)
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	target := t.TempDir()
	a := &updateAgent{mode: "docker", drSysID: drTestSysID} // Pfade ohne /proc/1/root
	a.dr.Config = drDefaults()
	a.dr.Config.Target = target
	haInstalled(t)

	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
	a.drTick(context.Background(), time.Now())
	if st := a.drStatus(); st.Paused != "passiver Knoten" || st.Error != "" {
		t.Fatalf("passiv: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(target, "wal")); err == nil {
		t.Fatal("passiver Knoten hat das Ziel angefasst")
	}

	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
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
	// außerhalb des Agenten (systemd-run), Werte nur über die Umgebung
	for _, want := range []string{"systemd-run --wait --pipe --collect --quiet", "--setenv=PDH_HA_REPO", "--setenv=PDH_HA_ADDRS", `"$PDH_HA_SCRIPT"`} {
		if !strings.Contains(gotScript, want) {
			t.Fatalf("Skript ohne %q: %s", want, gotScript)
		}
	}
	inner := gotEnv["PDH_HA_SCRIPT"]
	if strings.Contains(gotScript+inner, "192.168") || !strings.Contains(inner, `"$PDH_HA_ADDRS"`) || !strings.Contains(inner, `"configured":true`) {
		t.Fatalf("Skript: %s / %s", gotScript, inner)
	}
	drBashSyntax(t, "ha-install-inner", inner)
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
	// erst Unit (kann an schreibgeschütztem /etc scheitern), dann Binary atomar tauschen
	if i, j := strings.Index(s, "daemon-reload"), strings.Index(s, "mv -f"); i < 0 || j < 0 || i > j || !strings.Contains(s, "/usr/local/bin/pdh-ha.new") {
		t.Error("install-pdh-ha.sh: Binary nicht atomar nach der Unit getauscht")
	}
	if strings.Contains(s, "ufw allow") {
		t.Error("install-pdh-ha.sh öffnet ufw selbst (macht pdh-ha)")
	}
	// Benutzer des PDH in die Gruppe pdh (Socket), PDH-Neustart nur beim Erst-Install
	for _, want := range []string{"systemctl show -p User --value pdh", `usermod -aG pdh "$pdh_user"`, "PDH muss neu gestartet werden"} {
		if !strings.Contains(s, want) {
			t.Errorf("install-pdh-ha.sh ohne %q", want)
		}
	}
	if i, k := strings.Index(s, "if (( first_install ))"), strings.Index(s, "systemctl try-restart pdh"); i < 0 || k < i || strings.Count(s, "try-restart pdh") != 1 {
		t.Error("install-pdh-ha.sh: PDH-Neustart nicht auf den Erst-Install beschränkt")
	}
	j, err := os.ReadFile("../../scripts/pdh-ha-join.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://github.com/strobobaer/pdh-server.git", "/opt/pdh-ha-src", "tr '_-' '/+'", "--update", "--witness", "iputils-ping",
		`PDH_HA_JOIN="$CODE" /usr/local/bin/pdh-ha`, "sudo snap install go --classic", "read -r"} {
		if !strings.Contains(string(j), want) {
			t.Errorf("pdh-ha-join.sh ohne %q", want)
		}
	}
	u, err := os.ReadFile("../../install_update.sh")
	if err != nil {
		t.Fatal(err)
	}
	us := string(u)
	if strings.Contains(string(j), `"$CODE"; then`) || strings.Contains(string(j), `join "$CODE"`) {
		t.Error("pdh-ha-join.sh gibt den Code in der Befehlszeile weiter")
	}
	// vor dem Agent-Neustart und außerhalb seiner cgroup
	i := strings.Index(us, "systemd-run --wait --pipe --collect --quiet --unit=pdh-ha-update-")
	k := strings.Index(us, "--unit=pdh-update-agent-restart")
	if !strings.Contains(us, "if [[ -x /usr/local/bin/pdh-ha ]]") || i < 0 || k < 0 || i > k {
		t.Error("install_update.sh baut pdh-ha nicht vor dem Agent-Neustart per systemd-run")
	}
	// systemd-run --pipe zeigt die Ausgabe direkt, ein Journal der Einmal-Unit gibt es nicht
	if !strings.Contains(us, "pdh-ha-Update fehlgeschlagen – siehe Ausgabe oben") || strings.Contains(us, "journalctl -u pdh-ha-update") {
		t.Error("install_update.sh: Warnung fehlt")
	}
}

// Datenbank-Spiegel (Teil 3): --voll richtet Knoten 2 ein, pdh startet nur
// auf dem aktiven Server.
func TestFullNodeScripts(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	j := read("../../scripts/pdh-ha-join.sh")
	for _, want := range []string{"--voll) VOLL=1", "pdh-ha peer-pg-major", "install-pdh-program.sh", `replica-init --confirm-host "$confirm"`,
		"apt.postgresql.org", "read -r -p \"Zum Bestätigen den Hostnamen", "--voll nicht zusammen mit --witness"} {
		if !strings.Contains(j, want) {
			t.Errorf("pdh-ha-join.sh ohne %q", want)
		}
	}
	// replica-init bringt die .env – erst danach Agent und PDH bauen
	if i, k := strings.Index(j, "pdh-ha replica-init"), strings.Index(j, "install-update-agent.sh"); i < 0 || k < i {
		t.Error("pdh-ha-join.sh: install-update-agent.sh muss nach replica-init laufen")
	}
	p := read("../../scripts/install-pdh-program.sh")
	if strings.Contains(p, "systemctl restart pdh") || strings.Contains(p, "systemctl start pdh") || strings.Contains(p, "createdb") ||
		!strings.Contains(p, "systemctl disable pdh") {
		t.Error("install-pdh-program.sh darf pdh nicht starten und keine Datenbank anlegen")
	}
	for _, f := range []string{"../../install-update-agent.sh", "../../install_update.sh"} {
		s := strings.ReplaceAll(read(f), "\r\n", "\n")
		if !strings.Contains(s, "pdh-ha should-run-pdh") || strings.Count(s, "systemctl restart pdh\n") != 1 {
			t.Errorf("%s: Neustart von pdh nicht an should-run-pdh gebunden", f)
		}
		if i, k := strings.Index(s, "should-run-pdh"), strings.Index(s, "systemctl restart pdh\n"); k < i {
			t.Errorf("%s: Neustart vor der Prüfung", f)
		}
	}
}
