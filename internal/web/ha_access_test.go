package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func accessView(mut func(v *haView)) haView {
	v := haView{Installed: true, Status: haStatus{Configured: true,
		Self:  haNode{ID: "pdh1", Kind: "node", Role: "aktiv"},
		Peers: []haNode{{ID: "pdh2", Kind: "node", Role: "passiv"}},
	}}
	v.Status.Access.Hooks.Same = true
	if mut != nil {
		mut(&v)
	}
	return v
}

func rowOf(t *testing.T, v haView, title string) haAccessRow {
	t.Helper()
	for _, r := range v.AccessRows() {
		if r.Title == title {
			return r
		}
	}
	t.Fatalf("Zeile %q fehlt: %+v", title, v.AccessRows())
	return haAccessRow{}
}

func TestHAAccessRows(t *testing.T) {
	// Tunnel
	v := accessView(func(v *haView) { v.Status.Access.Tunnel.Installed = true; v.Status.Access.Tunnel.Running = true })
	if r := rowOf(t, v, "Tunnel"); r.Level != "ok" || r.State != "läuft auf pdh1" {
		t.Errorf("Tunnel auf aktivem: %+v", r)
	}
	v = accessView(func(v *haView) {
		v.Status.Self.Role = "passiv"
		v.Status.Access.Tunnel.Installed = true
		v.Status.Access.Tunnel.Running = true
	})
	if r := rowOf(t, v, "Tunnel"); r.Level != "err" || !strings.Contains(r.State, "nicht aktiv") {
		t.Errorf("Tunnel auf passivem: %+v", r)
	}
	v = accessView(func(v *haView) {
		v.Status.Access.Tunnel.Installed = true
		v.Status.Access.Tunnel.Running = true
		v.Status.Access.Tunnel.Sum = "aaa"
		v.Status.Peers[0].TunnelSum = "bbb"
	})
	if r := rowOf(t, v, "Tunnel"); r.Level != "warn" || !strings.Contains(r.Detail, "Token auf den Servern verschieden") {
		t.Errorf("Prüfsummen verschieden: %+v", r)
	}
	v = accessView(func(v *haView) {
		v.Origin = "http://192.168.1.5:8090"
		v.Status.Access.Tunnel.Installed = true
		v.Status.Access.Tunnel.Running = true
		v.Status.Access.Tunnel.Sum = "aaa"
		v.Status.Peers[0].TunnelSum = "aaa"
	})
	if r := rowOf(t, v, "Tunnel"); r.Level != "warn" || !strings.Contains(r.Detail, "Ziel im Cloudflare-Dashboard muss localhost:8090 sein") {
		t.Errorf("fremdes Ziel: %+v", r)
	}
	v.Origin = "http://127.0.0.1:8090"
	if r := rowOf(t, v, "Tunnel"); r.Level != "ok" || r.Detail != "" {
		t.Errorf("Ziel 127.0.0.1: %+v", r)
	}
	if r := rowOf(t, accessView(nil), "Tunnel"); r.Level != "off" {
		t.Errorf("Tunnel nicht eingerichtet: %+v", r)
	}

	// Adresse
	if r := rowOf(t, accessView(nil), "Mitwandernde Adresse"); r.Level != "off" || r.State != "nicht eingerichtet" {
		t.Errorf("keine Adresse: %+v", r)
	}
	v = accessView(func(v *haView) {
		a := &v.Status.Access
		a.VIP, a.VIPOn, a.Holder = "10.77.1.50/24", true, "pdh1"
		a.Tested = &haVIPTested{Net: "10.77.1.0/24", Ifaces: map[string]string{"pdh1": "eth0"}, At: time.Date(2026, 10, 28, 9, 5, 0, 0, time.UTC)}
	})
	r := rowOf(t, v, "Mitwandernde Adresse")
	if r.Level != "ok" || !strings.Contains(r.State, "10.77.1.50/24") || !strings.Contains(r.State, "pdh1") || !strings.Contains(r.Detail, "Letzter Test") {
		t.Errorf("Adresse aktiv: %+v", r)
	}
	v.Status.Access.Holder = ""
	if r := rowOf(t, v, "Mitwandernde Adresse"); r.Level != "warn" || !strings.Contains(r.State, "keinem Server") {
		t.Errorf("ohne Halter: %+v", r)
	}
	v.Status.Access.Holder = "pdh1"
	v.Status.Access.IfaceErr = "Karte nicht eindeutig"
	if r := rowOf(t, v, "Mitwandernde Adresse"); r.Level != "warn" || !strings.Contains(r.Detail, "Karte nicht eindeutig") {
		t.Errorf("Kartenfehler: %+v", r)
	}
	v.Status.Access.IfaceErr, v.Status.Access.VIPOn = "", false
	if r := rowOf(t, v, "Mitwandernde Adresse"); r.Level != "off" || !strings.Contains(r.State, "ausgeschaltet") {
		t.Errorf("Adresse aus: %+v", r)
	}

	// Skripte
	if r := rowOf(t, accessView(nil), "Skripte"); r.Level != "off" || r.State != "keine Skripte" {
		t.Errorf("keine Skripte: %+v", r)
	}
	v = accessView(func(v *haView) {
		h := &v.Status.Access.Hooks
		h.Names = []string{"10-dns.sh", "20-mail.sh"}
		v.Status.Access.HooksOn = true
		h.Last = &haHooksRun{At: time.Date(2026, 10, 28, 9, 5, 0, 0, time.UTC), Role: "aktiv",
			Results: []haHookResult{{Name: "10-dns.sh", OK: true}, {Name: "20-mail.sh", OK: false, Out: "boom"}}}
	})
	r = rowOf(t, v, "Skripte")
	if r.Level != "warn" || !strings.Contains(r.State, "gleich auf beiden Servern") || !strings.Contains(r.Detail, "1 von 2") {
		t.Errorf("Skripte, letzter Lauf mit Fehler: %+v", r)
	}
	v.Status.Access.Hooks.Last.Results[1].OK = true
	if r := rowOf(t, v, "Skripte"); r.Level != "ok" {
		t.Errorf("Skripte ok: %+v", r)
	}
	v.Status.Access.Hooks.Same = false
	if r := rowOf(t, v, "Skripte"); r.Level != "warn" || !strings.Contains(r.State, "verschieden") {
		t.Errorf("Skripte verschieden: %+v", r)
	}
	v.Status.Access.Hooks.Same = true
	v.Status.Access.HooksOn = false
	if r := rowOf(t, v, "Skripte"); r.Level != "off" {
		t.Errorf("Skripte aus: %+v", r)
	}
}

func TestHAAccessWebForwards(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[]`}
	startFakeHA(t, f)
	h := &Handler{}
	form := url.Values{"vip": {" 10.77.1.50/24 "}, "vip_on": {"0", "1"}, "hooks_on": {"0"}}
	if _, err := h.haDoAccess(context.Background(), form); err != nil {
		t.Fatal(err)
	}
	if c := f.callList(); len(c) != 1 || c[0] != "PUT /v1/access" {
		t.Fatalf("Aufrufe: %v", c)
	}
	for _, want := range []string{`"vip":"10.77.1.50/24"`, `"vip_on":true`, `"hooks_on":false`} {
		if !strings.Contains(f.bodies[0], want) {
			t.Errorf("Körper ohne %s: %s", want, f.bodies[0])
		}
	}
	// nur der Skripte-Schalter: nichts anderes mitsenden
	if _, err := h.haDoAccess(context.Background(), url.Values{"hooks_on": {"0", "1"}}); err != nil {
		t.Fatal(err)
	}
	if b := f.bodies[1]; strings.Contains(b, "vip") || !strings.Contains(b, `"hooks_on":true`) {
		t.Errorf("Körper: %s", b)
	}
}

func TestHAAccessWebError(t *testing.T) {
	sock := startErrHA(t, `{"error":"Adresse liegt nicht im Netz der Karte"}`)
	_ = sock
	_, err := (&Handler{}).haDoAccess(context.Background(), url.Values{"vip": {"10.1.1.1/24"}})
	if err == nil || err.Error() != "Adresse liegt nicht im Netz der Karte" {
		t.Fatalf("Fehler: %v", err)
	}
}

func TestHAAccessForbidden(t *testing.T) {
	h := &Handler{}
	for _, fn := range []http.HandlerFunc{h.HAAccessWeb, h.HAVIPTestWeb, h.HAHooksRunWeb} {
		req := httptest.NewRequest(http.MethodPost, "/admin/server-config/ha/x", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther {
			t.Errorf("ohne Recht: Status %d", rec.Code)
		}
	}
}

func TestHAVIPTestWeb(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[]`, vipTest: `{"ok":true,"results":[
		{"holder":"pdh1","checker":"pdh2","ok":true,"err":""},
		{"holder":"pdh2","checker":"pdh1","ok":true,"err":""}]}`}
	startFakeHA(t, f)
	out := (&Handler{}).haVIPTestFragment(context.Background(), " 10.77.1.50/24 ")
	if c := f.callList(); len(c) != 1 || c[0] != "POST /v1/vip/test" || !strings.Contains(f.bodies[0], `"addr":"10.77.1.50/24"`) {
		t.Fatalf("Aufrufe: %v %v", c, f.bodies)
	}
	for _, want := range []string{"pdh1", "pdh2", "erreichbar"} {
		if !strings.Contains(out, want) {
			t.Errorf("Fragment ohne %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Proxmox") {
		t.Error("Ursachenliste trotz Erfolg")
	}
	// Fehlerfall
	f.mu.Lock()
	f.vipTest = `{"ok":true,"results":[{"holder":"pdh1","checker":"pdh2","ok":false,"err":"keine Antwort"}]}`
	f.mu.Unlock()
	out = (&Handler{}).haVIPTestFragment(context.Background(), "")
	for _, want := range []string{"keine Antwort", "Proxmox", "ARP", "Port-Security", "Skripte"} {
		if !strings.Contains(out, want) {
			t.Errorf("Fehlerfall ohne %q: %s", want, out)
		}
	}
	// Dienstfehler wird ausgegeben und maskiert
	startErrHA(t, `{"error":"<b>kaputt</b>"}`)
	out = (&Handler{}).haVIPTestFragment(context.Background(), "")
	if !strings.Contains(out, "&lt;b&gt;kaputt") || strings.Contains(out, "<b>kaputt") {
		t.Errorf("Fehler nicht maskiert: %s", out)
	}
}

func TestMirrorPeerUsesVIP(t *testing.T) {
	st := haStatus{Configured: true, Peers: []haNode{{Role: "aktiv", Seen: true, Paths: []haPath{{Addr: "192.168.5.17:7946", Alive: true}}}}}
	st.Access.VIP, st.Access.VIPOn = "10.77.1.50/24", true
	if p := mirrorPeerFrom(st); p.ActiveAddr != "10.77.1.50" || !p.ActiveSeen {
		t.Fatalf("mit Adresse: %+v", p)
	}
	st.Access.VIPOn = false
	if p := mirrorPeerFrom(st); p.ActiveAddr != "192.168.5.17" {
		t.Fatalf("Adresse aus: %+v", p)
	}
	st.Access.VIPOn, st.Access.VIP = true, ""
	if p := mirrorPeerFrom(st); p.ActiveAddr != "192.168.5.17" {
		t.Fatalf("ohne Adresse: %+v", p)
	}
}

// startErrHA: pdh-ha, das jeden Aufruf mit 400 und dem Text body beantwortet.
func startErrHA(t *testing.T, body string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "err.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("Unix-Socket nicht verfügbar: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	t.Setenv("PDH_HA_SOCKET", sock)
	return sock
}

func TestHAAccessCardRenders(t *testing.T) {
	v := accessView(func(v *haView) {
		v.Status.CAHolder = true
		v.Status.Access.VIP, v.Status.Access.VIPOn, v.Status.Access.Holder = "10.77.1.50/24", true, "pdh1"
		v.Status.Access.Hooks.Names = []string{"10-dns.sh"}
		v.Status.Access.Hooks.Last = &haHooksRun{At: time.Now(), Role: "aktiv", Results: []haHookResult{{Name: "10-dns.sh", Out: "<boom>"}}}
	})
	out := renderDR(t, drView{HA: v})
	for _, want := range []string{"Zugangsweg", "Mitwandernde Adresse", `action="/admin/server-config/ha/access"`, `hx-post="/admin/server-config/ha/vip-test"`,
		`action="/admin/server-config/ha/hooks-run"`, "Jetzt ausführen", "&lt;boom&gt;", `value="10.77.1.50/24"`, "Teil 6"} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte ohne %q", want)
		}
	}
}
