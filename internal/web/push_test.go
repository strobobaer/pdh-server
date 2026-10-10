package web

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh"
)

func TestPushDecision(t *testing.T) {
	s := pushSettings{RepeatSeconds: 60, MaxMinutes: 120}
	min := time.Minute
	for _, c := range []struct {
		name              string
		open              bool
		assignee, initial string
		urgent            bool
		age, since        time.Duration
		wantReason        string
		wantResend        bool
	}{
		{"erledigt", false, "", "", true, min, min, "Vorgang erledigt", false},
		{"anderweitig zugewiesen", true, "u2", "", true, min, min, "anderweitig zugewiesen", false},
		{"beim Anlegen zugewiesen bleibt offen", true, "u1", "u1", true, min, 10 * time.Second, "", false},
		{"wiederholen", true, "", "", true, 5 * min, 61 * time.Second, "", true},
		{"noch nicht wiederholen", true, "", "", true, 5 * min, 30 * time.Second, "", false},
		{"abgelaufen", true, "", "", true, 121 * min, 61 * time.Second, "ohne Annahme abgelaufen", false},
		{"normal ohne Wiederholung", true, "", "", false, 5 * min, 10 * min, "", false},
		{"normal nach 24 h zu", true, "", "", false, 25 * time.Hour, min, "abgelaufen", false},
	} {
		reason, resend := pushDecision(c.open, c.assignee, c.initial, c.urgent, c.age, c.since, s)
		if reason != c.wantReason || resend != c.wantResend {
			t.Errorf("%s: %q/%v, erwartet %q/%v", c.name, reason, resend, c.wantReason, c.wantResend)
		}
	}
}

func TestPushAlertTextAndHelpers(t *testing.T) {
	title, body := pushAlertText("fault", "Band steht", "Motor heiß", "high", "Halle A › Presse 3", true)
	if title != "🚨 Anlage steht – Band steht" || body != "Halle A › Presse 3 · Priorität hoch\nMotor heiß" {
		t.Errorf("dringend: %q / %q", title, body)
	}
	if title, _ := pushAlertText("ticket", "Handschuhe", "", "", "", false); title != "Neues Ticket: Handschuhe" {
		t.Errorf("Ticket: %q", title)
	}
	if _, body := pushAlertText("fault", "x", strings.Repeat("ä", 400), "", "", false); len([]rune(body)) > 242 {
		t.Errorf("Inhalt nicht gekürzt: %d", len([]rune(body)))
	}
	if got := pushTopic("3f2b1c4d-1111-4222-8333-444455556666"); len(got) != 32 || strings.Contains(got, "-") {
		t.Errorf("Topic %q", got)
	}
	if ids := splitIDs("3f2b1c4d-1111-4222-8333-444455556666, quatsch,"); len(ids) != 1 {
		t.Errorf("splitIDs: %v", ids)
	}
	for ep, want := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc": true,
		"https://web.push.apple.com/QGx":          true,
		"http://fcm.googleapis.com/x":             false,
		"https://localhost/x":                     false,
		"https://192.168.1.10/x":                  false,
		"https://10.0.0.1/x":                      false,
		"https://[::1]/x":                         false,
		"https://intern.local/x":                  false,
	} {
		if pushEndpointOK(ep) != want {
			t.Errorf("%s: erwartet %v", ep, want)
		}
	}
	if pushClamp(0, 30, 600, 60) != 60 || pushClamp(5, 30, 600, 60) != 30 || pushClamp(9999, 30, 600, 60) != 600 {
		t.Error("pushClamp")
	}
}

// Versand: Kopfzeilen nach RFC 8030/8292, 410 = Abo loeschen.
func TestSendPushHeaders(t *testing.T) {
	var got http.Header
	var body []byte
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, body = r.Header.Clone(), nil
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	old := pushHTTP
	pushHTTP = srv.Client()
	defer func() { pushHTTP = old }()

	priv, pub, _ := generateVAPIDKeys()
	v := pushVAPID{Private: priv, Public: pub, Subject: "mailto:pdh@example.com"}
	sub := pushSubscription{Endpoint: srv.URL + "/push/abc", P256dh: rfcUAPub, Auth: rfcAuth}
	if err := sendPush(context.Background(), v, sub, []byte(`{"type":"alert"}`), 5*time.Minute, "high", "a123"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"Content-Encoding": "aes128gcm", "Ttl": "300", "Urgency": "high", "Topic": "a123"} {
		if got.Get(k) != want {
			t.Errorf("%s = %q, erwartet %q", k, got.Get(k), want)
		}
	}
	if !strings.HasPrefix(got.Get("Authorization"), "vapid t=") || !strings.HasSuffix(got.Get("Authorization"), "k="+pub) {
		t.Errorf("Authorization: %s", got.Get("Authorization"))
	}
	if plain := decryptPushForTest(t, body, rfcUAPriv, rfcAuth); plain != `{"type":"alert"}` {
		t.Errorf("Inhalt: %s", plain)
	}
	status = http.StatusGone
	if err := sendPush(context.Background(), v, sub, []byte(`{}`), time.Minute, "", ""); err != errPushGone {
		t.Errorf("410 → %v", err)
	}
	status = http.StatusBadRequest
	if err := sendPush(context.Background(), v, sub, []byte(`{}`), time.Minute, "", ""); err == nil || err == errPushGone {
		t.Errorf("400 → %v", err)
	}
}

func TestPushPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	// Service Worker liegt eingebettet unter der Wurzel
	sw, err := fs.ReadFile(pdh.Static, "web/static/sw.js")
	if err != nil || !bytes.Contains(sw, []byte("notificationclick")) || !bytes.Contains(sw, []byte("'close'")) {
		t.Fatalf("sw.js: %v", err)
	}
	found := false
	for _, f := range AppIconFiles {
		found = found || f == "sw.js"
	}
	if !found {
		t.Error("sw.js wird nicht ausgeliefert")
	}

	// Alarmseite
	c, _ := tmpl.Clone()
	c = bindLang(c, "de")
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "push_alert.gohtml")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	v := pushAlertView{ID: "a1", RefType: "fault", Kind: "Störung", Title: "🚨 Anlage steht – Band steht", Body: "Halle A", Urgent: true, Recipient: true, DetailURL: "/faults/f1", Created: "09.10. 08:00"}
	if err := c.ExecuteTemplate(&buf, "push_alert.gohtml", v); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	checkScripts(t, "push-alarm", page)
	for _, want := range []string{`class="urgent live"`, `id="accept"`, `href="/faults/f1"`, "/push/alert/' + ID + '/accept", "Anlage steht"} {
		if !strings.Contains(page, want) {
			t.Errorf("Alarmseite ohne %q", want)
		}
	}

	// Core-Einstellungen, Konto, Vollbild-Alarm in der App
	cs := CoreSettingsPageData{Push: pushSettingsView{pushSettings: pushSettings{Enabled: true, RepeatSeconds: 60, MaxMinutes: 120},
		Groups: []pushGroupOption{{ID: "g1", Name: "Bereitschaft", Members: 4, ForFault: true}}}}
	set := renderPage(t, tmpl, "core_settings", cs)
	for _, want := range []string{`action="/core/settings/push"`, `name="groups_fault" value="g1" checked`, `name="groups_ticket" value="g1"`, "Bereitschaft", "nur über <b>https://</b>"} {
		if !strings.Contains(set, want) {
			t.Errorf("Einstellungen ohne %q", want)
		}
	}
	if strings.Contains(set, `name="groups_ticket" value="g1" checked`) {
		t.Error("Tickets fälschlich angehakt")
	}
	me := UserDetailData{User: UserView{ID: "u1"}, IsSelf: true}
	me.PushEnabled = true
	out := renderPage(t, tmpl, "user_detail", me)
	checkScripts(t, "konto-push", out)
	for _, want := range []string{`id="push-card"`, "pdhPush.enable", `id="push-on"`, `id="pdh-alarm"`, "/push/alerts/open", "navigator.serviceWorker.register('/sw.js'"} {
		if !strings.Contains(out, want) {
			t.Errorf("Mein Konto ohne %q", want)
		}
	}
	// ausgeschaltet: Karte mit Hinweis statt Knöpfen, kein Alarm-Vollbild
	me.PushEnabled, me.CanManageRoles = false, true
	off := renderPage(t, tmpl, "user_detail", me)
	if !strings.Contains(off, `id="push-card"`) || !strings.Contains(off, "noch ausgeschaltet") || !strings.Contains(off, `href="/core/settings#push"`) ||
		strings.Contains(off, `id="push-on"`) || strings.Contains(off, `id="pdh-alarm"`) {
		t.Error("Hinweis bei ausgeschaltetem Push falsch")
	}
	// fremder Benutzerstamm: keine Karte; eingebettete Seite /account ohne doppelte Karte
	if strings.Contains(renderPage(t, tmpl, "user_detail", UserDetailData{User: UserView{ID: "u2"}}), `id="push-card"`) {
		t.Error("Push-Karte im fremden Benutzerstamm")
	}
	acc := AccountPageData{}
	acc.PushEnabled = true
	if strings.Contains(renderPage(t, tmpl, "account", acc), `id="push-card"`) {
		t.Error("Push-Karte doppelt (eingebettete Seite /account)")
	}
}
