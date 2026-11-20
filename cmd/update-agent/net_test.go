package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/hostnet"
)

// Netzwerk dieses Servers: Aufträge im Update-Agenten (net.go).

type netAnswer struct {
	OK      bool             `json:"ok"`
	Error   string           `json:"error"`
	Running bool             `json:"running"`
	Net     *hostnet.Summary `json:"net"`
	Pending *netPending      `json:"pending"`
	JobErr  string           `json:"job_error"`
}

func netCall(t *testing.T, a *updateAgent, method, path, body string) netAnswer {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	switch path {
	case "/v1/net/status":
		a.netStatusHandler(w, r)
	case "/v1/net/apply":
		a.netApplyHandler(w, r)
	case "/v1/net/confirm":
		a.netConfirmHandler(w, r)
	default:
		t.Fatalf("Weg %s", path)
	}
	var ans netAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil || w.Code != http.StatusOK {
		t.Fatalf("Antwort %d %q: %v", w.Code, w.Body.String(), err)
	}
	return ans
}

// netRec: aufgezeichnete Aufrufe von netRun.
type netRec struct {
	mu    sync.Mutex
	calls []netCallRec
}

type netCallRec struct {
	script string
	env    map[string]string
}

func (r *netRec) add(script string, env map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := map[string]string{}
	for k, v := range env {
		cp[k] = v
	}
	r.calls = append(r.calls, netCallRec{script, cp})
}

func (r *netRec) all() []netCallRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]netCallRec(nil), r.calls...)
}

var netTestSummary = hostnet.Summary{Iface: "eth0", Addr: "10.10.0.17/24", Mode: hostnet.ModeDHCP, Gateway: "10.10.0.1",
	DNS: []string{"10.10.0.1"}, Netplan: true}

// netTestAgent: systemd-Betrieb, Zustand im Temp-Ordner, pdh-ha nicht eingerichtet, alle
// Befehle ersetzt.
func netTestAgent(t *testing.T, rec *netRec, run func(context.Context, string, map[string]string) (string, error)) *updateAgent {
	t.Helper()
	t.Setenv("PDH_HA_ETC", t.TempDir())
	sum := netTestSummary
	return &updateAgent{mode: "systemd", repoDir: "/srv/pdh", netStateDir: t.TempDir(),
		netRun: func(ctx context.Context, script string, env map[string]string) (string, error) {
			rec.add(script, env)
			if run != nil {
				return run(ctx, script, env)
			}
			return "ok\n", nil
		},
		netCollect: func(context.Context) hostnet.Summary { return sum },
		netProbe:   func(context.Context, string) error { return nil },
	}
}

func netWriteState(t *testing.T, a *updateAgent, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.netStateDir, "state"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func netWaitIdle(t *testing.T, a *updateAgent) netAnswer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ans := netCall(t, a, http.MethodGet, "/v1/net/status", "")
		if !ans.Running || time.Now().After(deadline) {
			return ans
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Docker: alle drei Wege lehnen ab, nichts wird ausgeführt – auch keine Wiederaufnahme.
func TestNetAgentDockerRefuses(t *testing.T) {
	rec := &netRec{}
	a := netTestAgent(t, rec, nil)
	a.mode = "docker"
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/net/status", ""},
		{http.MethodPost, "/v1/net/apply", `{"action":"fix","by":"Anna"}`},
		{http.MethodPost, "/v1/net/confirm", `{"by":"Anna"}`},
	} {
		if ans := netCall(t, a, c.method, c.path, c.body); ans.OK || ans.Error != netDockerMsg || ans.Net != nil {
			t.Errorf("%s %s: %+v", c.method, c.path, ans)
		}
	}
	netWriteState(t, a, "state=pending\ndeadline=1\n")
	done := make(chan struct{})
	go func() { a.netResume(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("netResume kehrt im Docker-Betrieb nicht zurück")
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("%d Aufrufe im Docker-Betrieb", n)
	}
}

func TestNetApplyValidation(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"Aktion":            {`{"action":"x"}`, "Unbekannte Aktion"},
		"ohne Aktion":       {`{}`, "Unbekannte Aktion"},
		"kaputt":            {`{"action":`, "ungültiger Rumpf"},
		"vier DNS":          {`{"action":"dns","dns":["1.1.1.1","8.8.8.8","9.9.9.9","10.10.0.1"]}`, "ein bis drei"},
		"kein DNS":          {`{"action":"dns","dns":[]}`, "ein bis drei"},
		"Befehl":            {`{"action":"dns","dns":["1.1.1.1;reboot"]}`, "Ungültiger DNS-Server"},
		"Loopback":          {`{"action":"dns","dns":["127.0.0.1"]}`, "Ungültiger DNS-Server"},
		"IPv6":              {`{"action":"dns","dns":["2606:4700:4700::1111"]}`, "Ungültiger DNS-Server"},
		"doppelt":           {`{"action":"dns","dns":["1.1.1.1","1.1.1.1"]}`, "doppelt"},
		"fix mit DNS":       {`{"action":"fix","dns":["1.1.1.1"]}`, "nur bei „DNS-Server ändern“"},
		"Komma im Eintrag":  {`{"action":"dns","dns":["1.1.1.1,8.8.8.8"]}`, "Ungültiger DNS-Server"},
		"Leerzeichen innen": {`{"action":"dns","dns":["1.1.1.1 8.8.8.8"]}`, "Ungültiger DNS-Server"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &netRec{}
			a := netTestAgent(t, rec, nil)
			if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", c.body); ans.OK || !strings.Contains(ans.Error, c.want) {
				t.Errorf("%+v (erwartet %q)", ans, c.want)
			}
			time.Sleep(20 * time.Millisecond)
			if n := len(rec.all()); n != 0 {
				t.Errorf("Skript %d-mal gestartet", n)
			}
		})
	}
}

func TestNetApplyBusy(t *testing.T) {
	t.Run("Update läuft", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		a.status.Running = true
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || ans.Error != "Ein PDH-Update läuft – bitte danach erneut versuchen" {
			t.Errorf("%+v", ans)
		}
		if len(rec.all()) != 0 {
			t.Error("Skript gestartet")
		}
	})
	t.Run("Änderung wartet", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		netWriteState(t, a, fmt.Sprintf("state=pending\ndeadline=%d\n", time.Now().Unix()+100))
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || ans.Error != "Eine Änderung wartet noch auf Bestätigung" {
			t.Errorf("%+v", ans)
		}
		if len(rec.all()) != 0 {
			t.Error("Skript gestartet")
		}
	})
	t.Run("Wartung der vorigen Änderung noch an", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		netWriteState(t, a, "id=x1\nstate=confirmed\nmaint=1\nmaint_off=pending\n")
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || !strings.Contains(ans.Error, "wird noch ausgeschaltet") {
			t.Errorf("%+v", ans)
		}
		if len(rec.all()) != 0 {
			t.Error("Skript gestartet")
		}
	})
	t.Run("ohne netplan", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		a.netCollect = func(context.Context) hostnet.Summary {
			s := netTestSummary
			s.Netplan, s.Mode = false, hostnet.ModeUnknown
			return s
		}
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || ans.Error != "Fest einstellen nur auf Ubuntu-Servern mit netplan" {
			t.Errorf("%+v", ans)
		}
		if len(rec.all()) != 0 {
			t.Error("Skript gestartet")
		}
	})
	t.Run("Adresse unbekannt", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		a.netCollect = func(context.Context) hostnet.Summary {
			s := netTestSummary
			s.Gateway, s.Mode = "", hostnet.ModeUnknown
			return s
		}
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || !strings.Contains(ans.Error, "es wurde nichts geändert") {
			t.Errorf("%+v", ans)
		}
		if len(rec.all()) != 0 {
			t.Error("Skript gestartet")
		}
	})
}

// Ausfallschutz: Wartung, Rollentausch, Update im Verbund oder nicht erreichbar → gesperrt.
// Am aktiven Server schaltet der Agent die Wartung auch ohne ha_maint ein.
func TestNetApplyHA(t *testing.T) {
	for name, c := range map[string]struct{ status, want string }{
		"Wartung":      {`{"ok":true,"configured":true,"maint":true,"self":{"id":"a","role":"aktiv"}}`, "Die Wartung des Ausfallschutzes ist an – bitte danach erneut versuchen"},
		"Aktion läuft": {`{"ok":true,"configured":true,"busy":true,"self":{"id":"a","role":"aktiv"}}`, "Ein Rollentausch läuft – bitte danach erneut versuchen"},
		"Rückkehr":     {`{"ok":true,"configured":true,"self":{"id":"a","role":"wiedereingliederung"}}`, "Ein Rollentausch läuft – bitte danach erneut versuchen"},
		"Update":       {`{"ok":true,"configured":true,"update":{"active":true},"self":{"id":"a","role":"passiv"}}`, "Ein PDH-Update läuft – bitte danach erneut versuchen"},
		"Fehler":       {`{"ok":true,"configured":false,"error":"node.json kaputt"}`, "Ausfallschutz nicht erreichbar – bitte später erneut versuchen"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &netRec{}
			a := netTestAgent(t, rec, nil)
			haInstalled(t)
			haFakeSocket(t, c.status)
			if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || ans.Error != c.want {
				t.Errorf("%+v", ans)
			}
			if len(rec.all()) != 0 {
				t.Error("Skript gestartet")
			}
		})
	}
	t.Run("nicht erreichbar", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		haInstalled(t)
		t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || ans.Error != "Ausfallschutz nicht erreichbar – bitte später erneut versuchen" {
			t.Errorf("%+v", ans)
		}
	})
	t.Run("aktiver Server", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv"}}`)
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix","ha_maint":false}`); !ans.OK {
			t.Fatalf("%+v", ans)
		}
		netWaitIdle(t, a)
		if c := rec.all(); len(c) != 1 || c[0].env["PDH_NET_HA_MAINT"] != "1" {
			t.Errorf("Wartung am aktiven Server nicht verlangt: %+v", c)
		}
	})
	t.Run("Spiegel", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv"}}`)
		if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); !ans.OK {
			t.Fatalf("%+v", ans)
		}
		netWaitIdle(t, a)
		if c := rec.all(); len(c) != 1 || c[0].env["PDH_NET_HA_MAINT"] != "0" {
			t.Errorf("Spiegel: %+v", c)
		}
	})
}

// Die gemeinsame Adresse (IPv4 aus der Einstellung, IPv6 hier gehalten) zählt nie als eigene.
func TestNetHAExclude(t *testing.T) {
	a := &updateAgent{mode: "systemd"}
	t.Setenv("PDH_HA_ETC", t.TempDir())
	if info, err := a.netHAInfo(context.Background()); err != nil || info.Configured || len(info.Exclude) != 0 {
		t.Fatalf("ohne pdh-ha: %+v %v", info, err)
	}
	haInstalled(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","vip6_held":"2a02:aaaa::15"},
		"access":{"vip":"10.10.0.15/24","vip6":"2a02:aaaa::15","holder":"a"}}`)
	info, err := a.netHAInfo(context.Background())
	want := []netip.Addr{netip.MustParseAddr("10.10.0.15"), netip.MustParseAddr("2a02:aaaa::15")}
	if err != nil || !info.Configured || !info.Active || !reflect.DeepEqual(info.Exclude, want) {
		t.Errorf("%+v %v", info, err)
	}
}

func TestNetApplyDNSProbe(t *testing.T) {
	rec := &netRec{}
	a := netTestAgent(t, rec, nil)
	var asked []string
	a.netProbe = func(ctx context.Context, s string) error {
		asked = append(asked, s)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("Probe ohne Frist")
		}
		if s == "9.9.9.9" {
			return errors.New("Der DNS-Server 9.9.9.9 antwortet nicht")
		}
		return nil
	}
	ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"dns","dns":["10.10.0.1","9.9.9.9"]}`)
	if ans.OK || ans.Error != "Der DNS-Server 9.9.9.9 antwortet nicht – es wurde nichts geändert" {
		t.Errorf("%+v", ans)
	}
	if !reflect.DeepEqual(asked, []string{"10.10.0.1", "9.9.9.9"}) {
		t.Errorf("gefragt: %v", asked)
	}
	time.Sleep(20 * time.Millisecond)
	if len(rec.all()) != 0 {
		t.Error("Skript gestartet")
	}
}

// Erfolg: Antwort sofort, Werte nur über die Umgebung; zweiter Start während des Laufs abgelehnt.
func TestNetApplyRunsScript(t *testing.T) {
	release := make(chan struct{})
	rec := &netRec{}
	a := netTestAgent(t, rec, func(context.Context, string, map[string]string) (string, error) {
		<-release
		return "Feste Adresse eingestellt\n", nil
	})
	start := time.Now()
	ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"dns","dns":[" 10.10.0.1 ","9.9.9.9"],"by":"Anna\nAdmin=x","ha_maint":true}`)
	if !ans.OK || !ans.Running || time.Since(start) > 2*time.Second {
		t.Fatalf("Start: %+v", ans)
	}
	if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); ans.OK || ans.Error != "Eine Änderung wartet noch auf Bestätigung" {
		t.Errorf("zweiter Start: %+v", ans)
	}
	if st := netCall(t, a, http.MethodGet, "/v1/net/status", ""); !st.OK || !st.Running {
		t.Errorf("Status während des Laufs: %+v", st)
	}
	close(release)
	if st := netWaitIdle(t, a); st.Running || st.JobErr != "" {
		t.Errorf("fertig: %+v", st)
	}
	calls := rec.all()
	if len(calls) != 1 {
		t.Fatalf("%d Läufe", len(calls))
	}
	want := map[string]string{
		"PDH_NET_ACTION": "dns", "PDH_NET_DNS": "10.10.0.1,9.9.9.9", "PDH_NET_BY": "AnnaAdminx", "PDH_NET_HA_MAINT": "1",
		"PDH_NET_ROLLBACK": "120", "PDH_NET_SCRIPT": "/srv/pdh/scripts/pdh-static-ip.sh",
		"PDH_NET_IF": "eth0", "PDH_NET_IP": "10.10.0.17/24", "PDH_NET_GW": "10.10.0.1",
	}
	if !reflect.DeepEqual(calls[0].env, want) {
		t.Errorf("Umgebung:\n%v\nerwartet:\n%v", calls[0].env, want)
	}
	for _, v := range []string{"10.10.0.1", "9.9.9.9", "10.10.0.17", "eth0", "Anna", "/srv/pdh"} {
		if strings.Contains(calls[0].script, v) {
			t.Errorf("Skripttext enthält %q", v)
		}
	}
	if !strings.Contains(calls[0].script, "--auto") || !strings.Contains(calls[0].script, "--unit=\"pdh-static-ip-apply-") {
		t.Errorf("Skript: %s", calls[0].script)
	}
	// Aktion „fix“: kein DNS
	rec2 := &netRec{}
	a2 := netTestAgent(t, rec2, nil)
	if ans := netCall(t, a2, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); !ans.OK {
		t.Fatalf("fix: %+v", ans)
	}
	netWaitIdle(t, a2)
	if c := rec2.all(); len(c) != 1 || c[0].env["PDH_NET_ACTION"] != "fix" || c[0].env["PDH_NET_DNS"] != "" || c[0].env["PDH_NET_HA_MAINT"] != "0" {
		t.Errorf("fix: %+v", c)
	}
}

// Der Aufruf im Host: Werte als einzelne Argumente, nie als Shell-Text (echte bash, Attrappe
// für systemd-run im PATH).
func TestNetApplyWrapperArgs(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash nicht verfügbar")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(script string, env ...string) []string {
		t.Helper()
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append(os.Environ(), append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	}
	got := run(netApplyScript, "PDH_NET_ACTION=dns", "PDH_NET_DNS=10.10.0.1,9.9.9.9", "PDH_NET_BY=Anna; rm -rf / $(reboot)",
		"PDH_NET_HA_MAINT=1", "PDH_NET_ROLLBACK=120", "PDH_NET_SCRIPT=/srv/pdh/scripts/pdh-static-ip.sh",
		"PDH_NET_IF=eth0", "PDH_NET_IP=10.10.0.17/24", "PDH_NET_GW=10.10.0.1")
	if len(got) < 5 || got[0] != "--wait" || !strings.HasPrefix(got[4], "--unit=pdh-static-ip-apply-") {
		t.Fatalf("systemd-run: %q", got)
	}
	want := []string{"bash", "/srv/pdh/scripts/pdh-static-ip.sh", "--auto", "--rollback", "120", "--by", "Anna; rm -rf / $(reboot)",
		"--if", "eth0", "--ip", "10.10.0.17/24", "--gw", "10.10.0.1", "--dns", "10.10.0.1,9.9.9.9", "--ha-maint"}
	if !reflect.DeepEqual(got[5:], want) {
		t.Errorf("Argumente:\n%q\nerwartet:\n%q", got[5:], want)
	}
	got = run(netApplyScript, "PDH_NET_ACTION=fix", "PDH_NET_DNS=", "PDH_NET_BY=Anna", "PDH_NET_HA_MAINT=0", "PDH_NET_ROLLBACK=120",
		"PDH_NET_SCRIPT=/s.sh", "PDH_NET_IF=eth0", "PDH_NET_IP=10.10.0.17/24", "PDH_NET_GW=10.10.0.1")
	if strings.Contains(strings.Join(got, " "), "--dns") || strings.Contains(strings.Join(got, " "), "--ha-maint") {
		t.Errorf("fix: %q", got)
	}
	if got := run(netMaintOffScript, "PDH_NET_SCRIPT=/s.sh"); !reflect.DeepEqual(got[5:], []string{"bash", "/s.sh", "--maint-off"}) {
		t.Errorf("maint-off: %q", got)
	}
	if got := run(netConfirmScript, "PDH_NET_SCRIPT=/s.sh", "PDH_NET_BY=Bert", "PDH_NET_CONFIRM_ID=20261120-1"); !reflect.DeepEqual(got[5:], []string{"bash", "/s.sh", "--confirm", "--id", "20261120-1", "--by", "Bert"}) ||
		!strings.HasPrefix(got[4], "--unit=pdh-static-ip-confirm-") {
		t.Errorf("confirm: %q", got)
	}
	if got := run(netResumeScript, "PDH_NET_SCRIPT=/s.sh"); !reflect.DeepEqual(got[5:], []string{"bash", "/s.sh", "--resume"}) {
		t.Errorf("resume: %q", got)
	}
}

func TestNetStatusReadsState(t *testing.T) {
	rec := &netRec{}
	a := netTestAgent(t, rec, func(context.Context, string, map[string]string) (string, error) {
		return "Rückfall-Wecker\n\x1b[1;31m✘ Rückfall-Wecker ließ sich nicht stellen – es wurde nichts geändert\x1b[0m\n\n", errors.New("exit status 1")
	})
	ans := netCall(t, a, http.MethodGet, "/v1/net/status", "")
	if !ans.OK || ans.Pending != nil || ans.Running || ans.JobErr != "" || ans.Net == nil || !reflect.DeepEqual(*ans.Net, netTestSummary) {
		t.Fatalf("ohne Datei: %+v", ans)
	}
	netWriteState(t, a, "id=20261120-1\nstate=restored\naction=dns\nby=Anna\nstarted=1763600000\ndeadline=1763600120\napplied=1\nmaint=1\nmaint_seq=8\n"+
		"maint_off=pending\nmaint_msg=Wartung des Ausfallschutzes noch an – wird automatisch ausgeschaltet, sobald möglich\n"+
		"msg=Nicht bestätigt – vorige Einstellung wiederhergestellt\n")
	ans = netCall(t, a, http.MethodGet, "/v1/net/status", "")
	p := ans.Pending
	if p == nil || p.ID != "20261120-1" || p.State != "restored" || p.Action != "dns" || p.By != "Anna" || !p.Maint || !p.Applied ||
		!p.MaintOffPending || !strings.Contains(p.MaintMsg, "sobald möglich") ||
		p.Deadline.Unix() != 1763600120 || p.Started.Unix() != 1763600000 || p.Msg != "Nicht bestätigt – vorige Einstellung wiederhergestellt" {
		t.Fatalf("Zustand: %+v", p)
	}
	// Zeitpunkte ohne Wert fehlen im JSON (keine „0001-01-01“)
	netWriteState(t, a, "id=x1\nstate=failed\nmsg=Abgebrochen\n")
	w := httptest.NewRecorder()
	a.netStatusHandler(w, httptest.NewRequest(http.MethodGet, "/v1/net/status", nil))
	if s := w.Body.String(); strings.Contains(s, `"started"`) || strings.Contains(s, `"deadline"`) || !strings.Contains(s, `"maint_off_pending":false`) {
		t.Errorf("JSON: %s", s)
	}
	if ans := netCall(t, a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`); !ans.OK {
		t.Fatalf("Start: %+v", ans)
	}
	if st := netWaitIdle(t, a); st.JobErr != "✘ Rückfall-Wecker ließ sich nicht stellen – es wurde nichts geändert" {
		t.Errorf("Fehler des Laufs: %q", st.JobErr)
	}
}

// Bestätigen nur mit Kennung, solange die Änderung wartet und angewendet ist.
func TestNetConfirm(t *testing.T) {
	rec := &netRec{}
	a := netTestAgent(t, rec, nil)
	waiting := fmt.Sprintf("id=20261120-1\nstate=pending\napplied=1\ndeadline=%d\n", time.Now().Unix()+100)
	netWriteState(t, a, waiting)
	if ans := netCall(t, a, http.MethodPost, "/v1/net/confirm", `{"id":"20261120-1","by":"Bert"}`); !ans.OK || ans.Error != "" {
		t.Fatalf("%+v", ans)
	}
	c := rec.all()
	if len(c) != 1 || !strings.Contains(c[0].script, "--confirm") || c[0].env["PDH_NET_BY"] != "Bert" || c[0].env["PDH_NET_CONFIRM_ID"] != "20261120-1" ||
		c[0].env["PDH_NET_SCRIPT"] != "/srv/pdh/scripts/pdh-static-ip.sh" || strings.Contains(c[0].script, "Bert") || strings.Contains(c[0].script, "20261120-1") {
		t.Errorf("Aufruf: %+v", c)
	}
	for name, cs := range map[string]struct{ state, body, want string }{
		"ohne Kennung":       {waiting, `{"by":"Bert"}`, "Kennung der Änderung fehlt"},
		"Kennung kaputt":     {waiting, `{"id":"x;reboot"}`, "Kennung der Änderung fehlt"},
		"alte Kennung":       {waiting, `{"id":"20261119-7"}`, "Diese Änderung wartet nicht mehr auf Bestätigung"},
		"nicht angewendet":   {"id=20261120-1\nstate=pending\n", `{"id":"20261120-1"}`, netStillApply},
		"schon bestätigt":    {"id=20261120-1\nstate=confirmed\napplied=1\n", `{"id":"20261120-1"}`, netNotWaiting},
		"ohne Zustandsdatei": {"", `{"id":"20261120-1"}`, netNotWaiting},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &netRec{}
			a := netTestAgent(t, rec, nil)
			if cs.state != "" {
				netWriteState(t, a, cs.state)
			}
			if ans := netCall(t, a, http.MethodPost, "/v1/net/confirm", cs.body); ans.OK || ans.Error != cs.want {
				t.Errorf("%+v (erwartet %q)", ans, cs.want)
			}
			if len(rec.all()) != 0 {
				t.Error("Skript aufgerufen")
			}
		})
	}
	// Skript lehnt ab (z. B. Frist abgelaufen): letzte Zeile
	a.netRun = func(context.Context, string, map[string]string) (string, error) {
		return "Frist abgelaufen – die vorige Einstellung wird wiederhergestellt\n", errors.New("exit status 3")
	}
	if ans := netCall(t, a, http.MethodPost, "/v1/net/confirm", `{"id":"20261120-1"}`); ans.OK || ans.Error != "Frist abgelaufen – die vorige Einstellung wird wiederhergestellt" {
		t.Errorf("Ausgang 3: %+v", ans)
	}
	// während die Änderung noch angewendet wird (auch nach Neustart des Agenten: applied fehlt)
	a.netJob.running = true
	if ans := netCall(t, a, http.MethodPost, "/v1/net/confirm", `{"id":"20261120-1"}`); ans.OK || ans.Error != netStillApply {
		t.Errorf("während des Laufs: %+v", ans)
	}
}

// Start des Agenten (systemd): eine wartende Änderung bzw. eine noch offene Wartung wird wieder
// aufgenommen (--resume); ohne beides nichts. Die Wache holt einen ausgebliebenen Rückfall nach
// (auch wenn das Anwenden laut Agent hängt) und schaltet offene Wartung aus.
func TestNetAgentResumeOnStart(t *testing.T) {
	old := netWatchEvery
	netWatchEvery = 20 * time.Millisecond
	t.Cleanup(func() { netWatchEvery = old })
	scripts := func(rec *netRec, flag string) int {
		n := 0
		for _, c := range rec.all() {
			if strings.Contains(c.script, flag) {
				n++
			}
		}
		return n
	}
	waitFor := func(t *testing.T, rec *netRec, flag string, n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for scripts(rec, flag) < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := scripts(rec, flag); got < n {
			t.Errorf("%s: %d Aufrufe, erwartet mindestens %d", flag, got, n)
		}
	}

	t.Run("ohne Änderung", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		ctx, cancel := context.WithCancel(context.Background())
		go a.netResume(ctx)
		time.Sleep(100 * time.Millisecond)
		cancel()
		if n := len(rec.all()); n != 0 {
			t.Errorf("%d Aufrufe", n)
		}
	})
	t.Run("Änderung wartet", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		netWriteState(t, a, fmt.Sprintf("state=pending\ndeadline=%d\n", time.Now().Unix()+100))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.netResume(ctx); close(done) }()
		time.Sleep(150 * time.Millisecond)
		cancel()
		<-done
		c := rec.all()
		if len(c) != 1 || !strings.Contains(c[0].script, "--resume") || c[0].env["PDH_NET_SCRIPT"] != "/srv/pdh/scripts/pdh-static-ip.sh" {
			t.Errorf("Aufrufe (Frist offen: nur einer beim Start): %+v", c)
		}
	})
	t.Run("Frist lange vorbei", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		netWriteState(t, a, fmt.Sprintf("state=pending\ndeadline=%d\n", time.Now().Unix()-600))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.netResume(ctx)
		waitFor(t, rec, "--resume", 3)
	})
	t.Run("Anwenden hängt laut Agent", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		a.netJob.running = true
		now := time.Now()
		// knapp über der Frist: die Wache wartet auf das laufende Anwenden
		netWriteState(t, a, fmt.Sprintf("state=pending\ndeadline=%d\n", now.Unix()-60))
		a.netWatch(context.Background(), now)
		if n := len(rec.all()); n != 0 {
			t.Errorf("Rückfall trotz laufendem Anwenden innerhalb der Höchstdauer: %d", n)
		}
		// über Frist + Nachlauf + Höchstdauer des Anwendens: Rückfall trotzdem
		netWriteState(t, a, fmt.Sprintf("state=pending\ndeadline=%d\n", now.Add(-netWatchGrace-netApplyTimeout-time.Minute).Unix()))
		a.netWatch(context.Background(), now)
		if scripts(rec, "--resume") != 1 {
			t.Errorf("kein Rückfall: %+v", rec.all())
		}
	})
	t.Run("Wartung noch an", func(t *testing.T) {
		rec := &netRec{}
		fails := 1
		var mu sync.Mutex
		a := netTestAgent(t, rec, func(_ context.Context, script string, _ map[string]string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(script, "--maint-off") && fails > 0 {
				fails--
				return "Wartung des Ausfallschutzes noch an – pdh-ha antwortet nicht, wird automatisch erneut versucht\n", errors.New("exit status 1")
			}
			return "Wartung des Ausfallschutzes ausgeschaltet.\n", nil
		})
		netWriteState(t, a, "id=x1\nstate=confirmed\nmaint=1\nmaint_seq=8\nmaint_off=pending\n")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.netResume(ctx)
		// beim Start --resume (pdh-ha beim Hochfahren noch nicht da), danach --maint-off, bis es gelingt
		waitFor(t, rec, "--resume", 1)
		waitFor(t, rec, "--maint-off", 2)
	})
	t.Run("Wartung während eines Updates im Verbund", func(t *testing.T) {
		rec := &netRec{}
		a := netTestAgent(t, rec, nil)
		haInstalled(t)
		haFakeSocket(t, `{"ok":true,"configured":true,"update":{"active":true},"self":{"id":"a","role":"aktiv"}}`)
		netWriteState(t, a, "id=x1\nstate=restored\nmaint=1\nmaint_off=pending\n")
		a.netWatch(context.Background(), time.Now())
		if n := len(rec.all()); n != 0 {
			t.Errorf("Wartung aus während eines Updates versucht: %d", n)
		}
	})
	// main startet die Wiederaufnahme im systemd-Betrieb
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if i, j := strings.Index(s, `if mode == "systemd" {`), strings.Index(s, "go agent.netResume(context.Background())"); i < 0 || j < i || j > strings.Index(s, "mux := http.NewServeMux()") {
		t.Error("main.go startet netResume nicht im systemd-Block")
	}
}

func TestParseNetState(t *testing.T) {
	if p := parseNetState(nil); p != nil {
		t.Errorf("leer: %+v", p)
	}
	p := parseNetState([]byte("kaputt\n=nur\nid=x1\nstate=pending\nunbekannt=1\naction=fix\nby=A=B\nstarted=12\ndeadline=abc\nmaint=0\nmsg=\r\n"))
	if p == nil || p.ID != "x1" || p.State != "pending" || p.Action != "fix" || p.By != "A=B" || p.Started.Unix() != 12 ||
		!p.Deadline.IsZero() || p.Maint || p.Msg != "" || p.Applied || p.MaintOffPending {
		t.Errorf("%+v", p)
	}
	if p := parseNetState([]byte("id=x\nmsg=nur Text\n")); p != nil {
		t.Errorf("ohne state: %+v", p)
	}
	// offene Wartung: wie maint_open im Skript
	for content, want := range map[string]bool{
		"state=pending\nmaint=1\n":                         false, // wartet: Wartung bleibt an
		"state=confirmed\nmaint=1\nmaint_off=pending\n":    true,
		"state=confirmed\nmaint=1\nmaint_off=done\n":       false,
		"state=restored\nmaint=1\nmaint_off=skipped\n":     false,
		"state=failed\nmaint=on-requested\n":               true, // Abbruch nach der Vormerkung
		"state=failed\nmaint=on-requested\nmaint_off=done": false,
		"state=confirmed\nmaint=0\n":                       false,
	} {
		if p := parseNetState([]byte(content)); p == nil || p.MaintOffPending != want {
			t.Errorf("%q: %+v, erwartet %v", content, p, want)
		}
	}
}

func TestNetBy(t *testing.T) {
	if got := netBy(" Anna\x00 Ad=min\t\n "); got != "Anna Admin" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("ä", 100)
	if got := netBy(long); got != strings.Repeat("ä", 80) {
		t.Errorf("Länge %d", len([]rune(got)))
	}
}
