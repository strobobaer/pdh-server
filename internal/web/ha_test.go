package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh"
	"pdh/internal/core/users"
)

// fakeHA: pdh-ha-Socket-API auf einem Unix-Listener in t.TempDir().
type fakeHA struct {
	mu     sync.Mutex
	status string // JSON von /v1/status
	events string // JSON-Liste der Ereignisse
	calls  []string
	bodies []string
}

func startFakeHA(t *testing.T, f *fakeHA) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("Unix-Socket nicht verfügbar: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/status":
			_, _ = w.Write([]byte(f.status))
		case "/v1/events":
			var all, out []haEvent
			_ = json.Unmarshal([]byte(f.events), &all)
			since := parseSeq(r.URL.Query().Get("since"))
			limit := int(parseSeq(r.URL.Query().Get("limit")))
			for _, e := range all {
				if e.Seq > since && (limit == 0 || len(out) < limit) {
					out = append(out, e)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "events": out})
		case "/v1/join-code":
			_, _ = w.Write([]byte(`{"ok":true,"code":"CODE123","expires":"2026-10-25T10:30:00Z"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	t.Setenv("PDH_HA_SOCKET", sock)
	return sock
}

func (f *fakeHA) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func renderDR(t *testing.T, v drView) string {
	t.Helper()
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	var b strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&b, "dr-admin", v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestHAViewNoSocket(t *testing.T) {
	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
	h := &Handler{}
	v := h.haLoadView(context.Background())
	if v.Installed {
		t.Fatal("ohne Socket als installiert erkannt")
	}
	v.SuggestAddrs = []string{"192.168.1.10", "10.66.0.1"}
	out := renderDR(t, drView{HA: v, AgentErr: "Update-Agent nicht erreichbar"})
	for _, want := range []string{"Betriebsart: Einzel", `action="/admin/server-config/ha/setup"`, "Ausfallschutz einrichten", "192.168.1.10, 10.66.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter ohne %q", want)
		}
	}
	// Fortschritt der Installation
	v.Install = haInstall{Running: true, Log: "baue pdh-ha …"}
	out = renderDR(t, drView{HA: v})
	if !strings.Contains(out, "baue pdh-ha …") || !strings.Contains(out, "Installation läuft") {
		t.Error("Fortschritt der Installation fehlt")
	}
}

const haTrioStatus = `{"ok":true,"configured":true,"mode":"trio",
 "self":{"id":"pdh1","kind":"node","role":"aktiv","term":3,"version":"0.75.0","gateway":true,"seen":true,"last_seen_ago":0,"clock_skew":0,"paths":[]},
 "peers":[{"id":"pdh2","kind":"node","role":"passiv","term":3,"version":"0.75.0","gateway":true,"seen":true,"last_seen_ago":0.4,"clock_skew":-0.1,
   "paths":[{"addr":"10.0.0.2:7480","alive":true,"last_recv_ago":0.4,"err":""},{"addr":"10.66.0.2:7480","alive":false,"last_recv_ago":-1,"err":"connection refused"}]},
  {"id":"zeuge1","kind":"witness","role":"zeuge","term":0,"version":"0.74.0","gateway":false,"seen":false,"last_seen_ago":-1,"clock_skew":7,
   "paths":[{"addr":"10.0.0.9:7480","alive":false,"last_recv_ago":95,"err":""}]}],
 "maint":true,"settings":{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3},
 "gateway_targets":["192.168.1.1"],"blocked":"Wartungsstand beim Partner noch nicht angekommen","observe":true,
 "warnings":["Versionen unterschiedlich: zeuge1 hat 0.74.0"],"ca_holder":true,"error":""}`

func TestHAViewTrio(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[{"seq":1,"at":"2026-10-25T09:00:00Z","kind":"info","text":"pdh-ha 0.75.0 gestartet"},
		{"seq":2,"at":"2026-10-25T09:01:00Z","kind":"alarm","text":"Zeuge nicht erreichbar (Beobachtung)"}]`}
	startFakeHA(t, f)
	h := &Handler{}
	v := h.haLoadView(context.Background())
	if !v.Installed || v.Err != "" {
		t.Fatalf("Installed=%v Err=%q", v.Installed, v.Err)
	}
	if len(v.Events) != 2 || v.Events[0].Seq != 2 {
		t.Fatalf("Ereignisse (neueste zuerst): %+v", v.Events)
	}
	out := renderDR(t, drView{HA: v})
	for _, want := range []string{"Beobachtungsmodus – es wird nichts umgeschaltet", "Betriebsart: Trio", "pdh1", "pdh2", "zeuge1",
		">aktiv<", ">passiv<", ">zeuge<", "10.0.0.2:7480", "10.66.0.2:7480", "connection refused", "Versionen unterschiedlich: zeuge1 hat 0.74.0",
		"Übernahme gesperrt: Wartungsstand beim Partner noch nicht angekommen", "Übernahme erzwingen", "Uhrzeit prüfen",
		"Zeuge nicht erreichbar (Beobachtung)", `action="/admin/server-config/ha/maintenance"`, `action="/admin/server-config/ha/command"`,
		`action="/admin/server-config/ha/remove"`, `action="/admin/server-config/ha/settings"`, `name="takeover_trio_s" min="10" max="600" value="30"`,
		"Rollen tauschen", "Automatik an", "192.168.1.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Übersicht ohne %q", want)
		}
	}
	// Einstellungen nur am ersten Server
	v.Status.CAHolder = false
	out = renderDR(t, drView{HA: v})
	if strings.Contains(out, `action="/admin/server-config/ha/settings"`) || !strings.Contains(out, "Einstellungen bitte am ersten Server ändern") {
		t.Error("Zeiten-Formular auf einem Nicht-CA-Knoten")
	}
	// Ladefehler des Dienstes deutlich zeigen
	v = haView{Installed: true, Status: haStatus{Configured: false, Error: "Konfiguration nicht lesbar: node.json kaputt"}}
	v.Err = haStatusErr(v.Status)
	out = renderDR(t, drView{HA: v})
	if !strings.Contains(out, "pdh-ha meldet: Konfiguration nicht lesbar: node.json kaputt") {
		t.Error("Fehler des Dienstes fehlt")
	}
	// Beitrittscode einmalig mit fertigem Befehl
	v = haView{Installed: true, Status: haStatus{Configured: true, Mode: "einzel", CAHolder: true}, JoinCode: "CODE123", JoinCmd: "sudo bash pdh-ha-join.sh --witness CODE123",
		JoinExpires: time.Date(2026, 10, 25, 10, 30, 0, 0, time.UTC)}
	out = renderDR(t, drView{HA: v})
	for _, want := range []string{"sudo bash pdh-ha-join.sh --witness CODE123", `href="/admin/server-config/ha/join-script"`, "gültig bis"} {
		if !strings.Contains(out, want) {
			t.Errorf("Beitrittscode ohne %q", want)
		}
	}
}

func TestHAViewUnreachable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("Unix-Socket nicht verfügbar: %v", err)
	}
	t.Setenv("PDH_HA_SOCKET", sock)
	ln.Close() // Datei bleibt (Windows) bzw. fehlt (Linux) – beides darf nicht hängen
	v := (&Handler{}).haLoadView(context.Background())
	if v.Installed && v.Err == "" {
		t.Fatal("unerreichbarer Dienst ohne Fehler")
	}
}

func TestHAForbidden(t *testing.T) {
	h := &Handler{}
	for _, fn := range []http.HandlerFunc{h.HASetupWeb, h.HAJoinCodeWeb, h.HAMaintenanceWeb, h.HACommandWeb, h.HARemoveWeb, h.HASettingsWeb, h.HAJoinScriptWeb} {
		req := httptest.NewRequest(http.MethodPost, "/admin/server-config/ha/x", strings.NewReader("cmd=swap"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "u1", Role: users.Role("user")}))
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("ohne Recht: Status %d", rec.Code)
		}
	}
}

func TestHAForceNeedsConfirm(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[]`}
	startFakeHA(t, f)
	h := &Handler{}
	ctx := context.Background()
	for _, confirm := range []string{"", "uebernehmen", "JA"} {
		if _, err := h.haDoCommand(ctx, url.Values{"cmd": {"force_takeover"}, "confirm": {confirm}}); err == nil || err.Error() != "Bitte ÜBERNEHMEN eintippen" {
			t.Fatalf("confirm %q: %v", confirm, err)
		}
	}
	if c := f.callList(); len(c) != 0 {
		t.Fatalf("Socket ohne Bestätigung aufgerufen: %v", c)
	}
	if _, err := h.haDoCommand(ctx, url.Values{"cmd": {"force_takeover"}, "confirm": {" ÜBERNEHMEN "}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.haDoCommand(ctx, url.Values{"cmd": {"swap"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.haDoCommand(ctx, url.Values{"cmd": {"reboot"}}); err == nil {
		t.Fatal("unbekannter Befehl angenommen")
	}
	c := f.callList()
	if len(c) != 2 || c[0] != "POST /v1/command" || !strings.Contains(f.bodies[0], `"force_takeover"`) || !strings.Contains(f.bodies[1], `"swap"`) {
		t.Fatalf("Aufrufe: %v %v", c, f.bodies)
	}
}

func TestHASettingsInvalid(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[]`}
	startFakeHA(t, f)
	h := &Handler{}
	form := url.Values{"tick_s": {"1"}, "fence_s": {"30"}, "takeover_trio_s": {"30"}, "takeover_duo_s": {"60"}, "cooldown_min": {"15"}, "max_per_day": {"3"}}
	_, err := h.haDoSettings(context.Background(), form)
	if err == nil || err.Error() != "Übernahmezeit muss mindestens Selbst-Abschaltung + 2 × Takt + 5 s sein" {
		t.Fatalf("Fehler: %v", err)
	}
	rec := httptest.NewRecorder()
	drRedirect(rec, httptest.NewRequest(http.MethodPost, "/admin/server-config/ha/settings", nil), "", err)
	if loc, _ := url.Parse(rec.Header().Get("Location")); loc.Query().Get("err") != err.Error() || loc.Query().Get("tab") != "dr" {
		t.Fatalf("Rückleitung: %s", rec.Header().Get("Location"))
	}
	if c := f.callList(); len(c) != 0 {
		t.Fatalf("Socket trotz ungültiger Zeiten aufgerufen: %v", c)
	}
	form.Set("fence_s", "10")
	form.Set("gateway_targets", "192.168.1.1, 10.0.0.1")
	if _, err := h.haDoSettings(context.Background(), form); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Settings       map[string]int `json:"settings"`
		GatewayTargets []string       `json:"gateway_targets"`
	}
	_ = json.Unmarshal([]byte(f.bodies[0]), &body)
	if f.calls[0] != "PUT /v1/settings" || body.Settings["fence_s"] != 10 || body.Settings["takeover_trio_s"] != 30 || len(body.GatewayTargets) != 2 {
		t.Fatalf("gesendet: %v %s", f.calls, f.bodies[0])
	}
}

func TestHAAlarmThrottle(t *testing.T) {
	kv := map[string]string{}
	var sent []string
	get := func(k string) string { return kv[k] }
	set := func(k, v string) { kv[k] = v }
	notify := func(s string) { sent = append(sent, s) }
	t0 := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	ev := func(seq uint64, kind, text string, at time.Time) haEvent {
		return haEvent{Seq: seq, Kind: kind, Text: text, At: at}
	}
	haNotifyAlarms([]haEvent{ev(1, "alarm", "Zeuge nicht erreichbar (Beobachtung)", t0), ev(2, "info", "Befehl: Rollen tauschen", t0)}, t0, false, get, set, notify)
	haNotifyAlarms([]haEvent{ev(3, "alarm", "Zeuge nicht erreichbar (Beobachtung)", t0.Add(10*time.Minute))}, t0.Add(10*time.Minute), false, get, set, notify)
	if len(sent) != 1 {
		t.Fatalf("zwei gleiche Alarme in 15 min: %d Meldungen", len(sent))
	}
	if sent[0] != "⚠️ **Ausfallschutz (Beobachtung)**\nZeuge nicht erreichbar (Beobachtung)\nServer-Einstellungen → Ausfallschutz" {
		t.Fatalf("Text: %q", sent[0])
	}
	haNotifyAlarms([]haEvent{ev(4, "alarm", "Selbst-Abschaltung (Amtszeit 4) (Beobachtung)", t0.Add(11*time.Minute))}, t0.Add(11*time.Minute), false, get, set, notify)
	if len(sent) != 2 {
		t.Fatalf("anderer Text ohne eigene Meldung: %d", len(sent))
	}
	haNotifyAlarms([]haEvent{ev(5, "alarm", "Zeuge nicht erreichbar (Beobachtung)", t0.Add(16*time.Minute))}, t0.Add(16*time.Minute), false, get, set, notify)
	if len(sent) != 3 {
		t.Fatalf("nach 15 min erneut: %d", len(sent))
	}
	// ohne Prüfpunkt (erster Lauf): alte Alarme nicht nachmelden
	sent, kv = nil, map[string]string{}
	haNotifyAlarms([]haEvent{ev(1, "alarm", "Uralt (Beobachtung)", t0.Add(-2*time.Hour)), ev(2, "alarm", "Frisch (Beobachtung)", t0.Add(-time.Minute))}, t0, true, get, set, notify)
	if len(sent) != 1 || !strings.Contains(sent[0], "Frisch") {
		t.Fatalf("erster Lauf: %v", sent)
	}
	if k := haAlarmKey(strings.Repeat("x", 40) + "A"); k != haAlarmKey(strings.Repeat("x", 40)+"B") {
		t.Error("Schlüssel soll nur die ersten 40 Zeichen nutzen")
	}
}

func TestHAWatchOnce(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[{"seq":6,"at":"2026-10-25T08:59:00Z","kind":"alarm","text":"Schon gemeldet (Beobachtung)"},{"seq":7,"at":"2026-10-25T09:00:00Z","kind":"alarm","text":"Weg 10.0.0.2:7480 seit 1 min ausgefallen (Beobachtung)"}]`}
	startFakeHA(t, f)
	kv := map[string]string{haSetEventSeq: "6"}
	var sent []string
	h := &Handler{}
	h.haWatchOnce(context.Background(), time.Date(2026, 10, 25, 9, 0, 30, 0, time.UTC),
		func(k string) string { return kv[k] }, func(k, v string) { kv[k] = v }, func(s string) { sent = append(sent, s) })
	if kv[haSetEventSeq] != "7" || len(sent) != 1 || !strings.Contains(sent[0], "Weg 10.0.0.2:7480") {
		t.Fatalf("Prüfpunkt %q, Meldungen %v", kv[haSetEventSeq], sent)
	}
	if c := f.callList(); len(c) == 0 || c[0] != "GET /v1/events" {
		t.Fatalf("Aufrufe: %v", c)
	}
}

func TestDRPausedShown(t *testing.T) {
	out := renderDR(t, drView{DBLocal: true, Status: drStatus{OK: true, Supported: true, Paused: "passiver Knoten",
		Config: drConfig{Enabled: true, Target: "/mnt/x"}}})
	if !strings.Contains(out, "Sicherung pausiert: passiver Knoten") {
		t.Error("Pause fehlt")
	}
	var st drStatus
	_ = json.Unmarshal([]byte(`{"paused":"passiver Knoten"}`), &st)
	if st.Paused != "passiver Knoten" {
		t.Error("Feld paused wird nicht gelesen")
	}
}

// Beobachtung: Hinweis statt Pause, Sicherung läuft weiter.
func TestDRAdviceShown(t *testing.T) {
	adv := "Beobachtung: würde pausieren – passiver Knoten"
	out := renderDR(t, drView{DBLocal: true, Status: drStatus{OK: true, Supported: true, Advice: adv,
		Config: drConfig{Enabled: true, Target: "/mnt/x"}}})
	if !strings.Contains(out, adv) || strings.Contains(out, "Sicherung pausiert") {
		t.Error("Hinweis fehlt oder Pause angezeigt")
	}
	var st drStatus
	_ = json.Unmarshal([]byte(`{"advice":"x"}`), &st)
	if st.Advice != "x" {
		t.Error("Feld advice wird nicht gelesen")
	}
}

func TestHAJoinScript(t *testing.T) {
	if !strings.Contains(pdh.HAJoinScript, "pdh-ha-join.sh") || !strings.Contains(pdh.HAJoinScript, "--witness") {
		t.Fatal("eingebettetes Beitrittsskript fehlt")
	}
}

func TestHASuggestAddrs(t *testing.T) {
	got := haPickAddrs([]haIfAddr{
		{Name: "lo", IP: "127.0.0.1"},
		{Name: "docker0", IP: "172.17.0.1"},
		{Name: "br-1234", IP: "172.18.0.1"},
		{Name: "eth0", IP: "fe80::1"},
		{Name: "eth0", IP: "192.168.1.10"},
		{Name: "eth1", IP: "192.168.2.10"},
		{Name: "wg0", IP: "10.66.0.1"},
	})
	if strings.Join(got, ",") != "192.168.1.10,10.66.0.1" {
		t.Fatalf("Vorschlag: %v", got)
	}
	if got := haParseAddrs(" 192.168.1.10, 10.66.0.1 ;"); strings.Join(got, ",") != "192.168.1.10,10.66.0.1" {
		t.Fatalf("Formular: %v", got)
	}
}

func TestHAJoinCodeOnce(t *testing.T) {
	f := &fakeHA{status: haTrioStatus, events: `[]`}
	startFakeHA(t, f)
	h := &Handler{}
	ctx := context.WithValue(context.Background(), "user", &users.User{ID: "admin1", Role: users.RoleAdmin})
	if _, err := h.haDoJoinCode(ctx, url.Values{"role": {"admin"}}); err == nil {
		t.Fatal("ungültige Rolle angenommen")
	}
	haJoinCodes.put("admin1", haShownCode{code: "X", cmd: "x", expires: time.Now().Add(-time.Minute)})
	if v := h.haLoadView(ctx); v.JoinCode != "" {
		t.Fatal("abgelaufener Code angezeigt")
	}
	// Fake liefert einen festen, schon abgelaufenen Zeitpunkt – für den Test verlängern
	if _, err := h.haDoJoinCode(ctx, url.Values{"role": {"witness"}}); err != nil {
		t.Fatal(err)
	}
	c := haJoinCodes.m["admin1"]
	c.expires = time.Now().Add(time.Minute)
	haJoinCodes.put("admin1", c)
	v := h.haLoadView(ctx)
	if v.JoinCode != "CODE123" || v.JoinCmd != "sudo bash pdh-ha-join.sh --witness CODE123" {
		t.Fatalf("Code %q Befehl %q", v.JoinCode, v.JoinCmd)
	}
	if v := h.haLoadView(ctx); v.JoinCode != "" {
		t.Fatal("Code zweimal angezeigt")
	}
}

// Unterschiedliche Versionen: Update-Befehl mit dem Commit des ersten Servers.
func TestHAVersionDiffUpdateCmd(t *testing.T) {
	v := haView{Installed: true, Status: haStatus{Configured: true, Mode: "duo", CAHolder: true, Observe: true,
		Self:     haNode{ID: "pdh1", Kind: "node", Role: "aktiv", Version: "0.75.0", Commit: "51c21bcabcdef"},
		Peers:    []haNode{{ID: "pdh2", Kind: "node", Role: "passiv", Version: "0.74.0", Commit: "1111111"}},
		Warnings: []string{"Versionen unterschiedlich"}}}
	out := renderDR(t, drView{HA: v})
	if !strings.Contains(out, "sudo bash /opt/pdh-ha-src/scripts/pdh-ha-join.sh --update 51c21bcabcdef") {
		t.Errorf("Update-Befehl mit Commit des ersten Servers fehlt")
	}
	if !strings.Contains(out, "51c21bc") {
		t.Error("Commit bei der Version fehlt")
	}
	// Auf dem zweiten Server: Commit vom ersten (Partner)
	v.Status.CAHolder = false
	v.Status.Self.Commit, v.Status.Peers[0].Commit = "1111111", "51c21bcabcdef"
	if out := renderDR(t, drView{HA: v}); !strings.Contains(out, "--update 51c21bcabcdef") {
		t.Error("zweiter Server: Commit des ersten fehlt")
	}
	// Commit unbekannt: Hinweis auf pdh-ha version
	v.Status.Peers[0].Commit = ""
	if out := renderDR(t, drView{HA: v}); !strings.Contains(out, "pdh-ha version") || strings.Contains(out, "--update 1111111") {
		t.Error("ohne Commit: Hinweis auf pdh-ha version fehlt")
	}
	// gleiche Versionen: kein Befehl
	v.Status.Warnings = nil
	if out := renderDR(t, drView{HA: v}); strings.Contains(out, "--update") {
		t.Error("Update-Befehl ohne Versionsunterschied")
	}
}
