package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ── Karte ────────────────────────────────────────────────────

func renderCertPanel(t *testing.T, p certPanel) string {
	t.Helper()
	tmpl := loadTestTemplates(t)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "cert-panel", p); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Mit Recht: Überschrift, je Zeile Name, Ablauf, Weg im Haus, Knöpfe; die Fehlerzeile nennt den Grund.
func TestCertPanelRender(t *testing.T) {
	p := certPanel{CanManage: true, CanStrict: true, AgentOK: true, NginxOK: true, Zone: "firma.de", Email: "it@firma.de",
		Lines: []certLine{
			{Name: "nc.firma.de", State: certStateGueltig, ValidUntil: "09.02.2027", LAN: "direkt", CanRemove: true},
			{Name: "pdh.firma.de", State: certStateGueltig, ValidUntil: "09.02.2027", LAN: "cloudflare"},
			{Name: "alt.firma.de", State: certStateFehler, Error: "Let's Encrypt lehnt ab: <b>Grund</b>", LAN: "unbekannt"},
		},
		Suggested: []string{"neu.firma.de"},
	}
	out := renderCertPanel(t, p)
	for _, want := range []string{"Zertifikate (Let's Encrypt)", "nc.firma.de", "pdh.firma.de", "gültig bis 09.02.2027",
		"im Haus direkt", "über Cloudflare", "Let&#39;s Encrypt lehnt ab: &lt;b&gt;Grund&lt;/b&gt;", `value="it@firma.de"`,
		`hx-post="/admin/server-config/certs/add"`, `value="neu.firma.de"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "<b>Grund</b>") {
		t.Error("Fehlertext nicht maskiert")
	}
	if n := strings.Count(out, `value="issue"`); n != 3 {
		t.Errorf("Holen-Knöpfe: %d, erwartet 3", n)
	}
	if n := strings.Count(out, `value="remove"`); n != 1 {
		t.Errorf("Entfernen-Knöpfe: %d, erwartet 1 (nur CanRemove)", n)
	}
	if strings.Contains(out, "every 5s") {
		t.Error("ohne laufenden Auftrag darf die Karte nicht nachfragen")
	}

	// Ein laufender Auftrag: Schritt sichtbar, Karte fragt alle 5 Sekunden nach.
	p.Lines = []certLine{{Name: "nc.firma.de", State: certStateLaeuft, Step: "pruefen", LAN: "direkt"}}
	out = renderCertPanel(t, p)
	if !strings.Contains(out, `hx-trigger="every 5s"`) || !strings.Contains(out, "pruefen") {
		t.Errorf("laufender Auftrag:\n%s", out)
	}
}

// Ohne Recht: keine Formulare, aber der Satz mit dem Namen des Rechts.
func TestCertPanelWithoutRight(t *testing.T) {
	out := renderCertPanel(t, certPanel{Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig}}})
	if strings.Contains(out, "<form") || strings.Contains(out, "nc.firma.de") {
		t.Errorf("ohne Recht keine Formulare und keine Namen:\n%s", out)
	}
	if !strings.Contains(out, "Zertifikate verwalten (Let's Encrypt)") || !strings.Contains(out, "system.certs") {
		t.Errorf("Satz mit dem Recht fehlt:\n%s", out)
	}
}

var certStrictBtnRe = regexp.MustCompile(`<button[^>]*data-cert-strict[^>]*>`)

// Full (strict): ohne fertige Zertifikate gesperrt; Testumgebung mit Hinweis; Zurückstellen warnt.
func TestCertPanelStrictButton(t *testing.T) {
	p := certPanel{CanManage: true, CanStrict: true, AgentOK: true, NginxOK: true, Zone: "firma.de", Staging: true,
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, ValidUntil: "09.02.2027", Staging: true}}}
	out := renderCertPanel(t, p)
	btn := certStrictBtnRe.FindString(out)
	if btn == "" || !strings.Contains(btn, "disabled") {
		t.Errorf("Strict-Knopf muss gesperrt sein: %q", btn)
	}
	if !strings.Contains(out, "Testumgebung: Zertifikate sind nicht vertrauenswürdig") {
		t.Error("Hinweis auf die Testumgebung fehlt")
	}
	p.StrictReady = true
	if btn := certStrictBtnRe.FindString(renderCertPanel(t, p)); btn == "" || strings.Contains(btn, "disabled") {
		t.Errorf("bereit: Knopf muss frei sein: %q", btn)
	}
	p.StrictOn = true
	out = renderCertPanel(t, p)
	if !strings.Contains(out, "Zurückstellen") || !strings.Contains(out, "gehen verloren") {
		t.Errorf("Zurückstellen ohne Warnung:\n%s", out)
	}
	// ohne system.cloudflare kein Strict-Knopf
	p.CanStrict = false
	if certStrictBtnRe.MatchString(renderCertPanel(t, p)) {
		t.Error("ohne system.cloudflare kein Strict-Knopf")
	}
}

// Ohne Cloudflare-Zugang: kurzer Hinweis, keine Formulare, keine rote Meldung.
func TestCertPanelWithoutCloudflare(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	req := httptest.NewRequest("GET", "/admin/server-config/certs/panel", nil)
	p := h.certPanelFor(context.Background(), req, "", nil)
	if p.Hint == "" || p.Err != "" || p.AgentOK {
		t.Fatalf("Panel ohne Einrichtung: %+v", p)
	}
	out := renderCertPanel(t, p)
	if strings.Contains(out, "<form") || strings.Contains(out, "var(--red)") || !strings.Contains(out, p.Hint) {
		t.Errorf("Hinweis ohne Formulare erwartet:\n%s", out)
	}
}

// Ein Fehler bei Cloudflare lässt die Zeilen der Zertifikate stehen.
func TestCloudflarePanelKeepsCertLines(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := cfPanel{Error: "Cloudflare lehnt den API-Token ab",
		Certs: certPanel{CanManage: true, Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, ValidUntil: "09.02.2027"}}}}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "cf-panel", p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "lehnt den API-Token ab") || !strings.Contains(out, "gültig bis 09.02.2027") || !strings.Contains(out, `id="cert-panel"`) {
		t.Errorf("Zertifikate fehlen bei Cloudflare-Fehler:\n%s", out)
	}
}

// Die Seite der Server-Einstellungen lädt die Karte nicht selbst (kein Agent, keine CA).
func TestServerConfigDoesNotLoadCerts(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "cloudflare"}}, CF: &CloudflareView{CanAPI: true, APITokenSet: true}}
	d.Tab = "cloudflare"
	out := renderPage(t, tmpl, "server_config", d)
	if strings.Contains(out, "certs/panel") || strings.Contains(out, `id="cert-panel"`) {
		t.Error("Server-Einstellungen dürfen die Zertifikate nicht von selbst laden")
	}
}

// ── Rechte ───────────────────────────────────────────────────

func TestCertGateRules(t *testing.T) {
	for _, c := range []struct{ path, perm string }{
		{"/admin/server-config/certs/remove", permCertsAdmin},
		{"/admin/server-config/certs/strict", permCloudflare},
		{"/admin/server-config/certs/issue", permCerts},
		{"/admin/server-config/certs/add", permCerts},
		{"/admin/server-config/certs/settings", permCerts},
	} {
		if _, perm, _, _, ok := matchGate("POST", c.path); !ok || perm != c.perm {
			t.Errorf("%s: Recht %q, erwartet %q", c.path, perm, c.perm)
		}
	}
	if areaAdminPerm["certs"] != permCertsAdmin {
		t.Error("Bereichsadministrator der Zertifikate fehlt in areaAdminPerm")
	}

	deny := func(perms ...string) *Handler {
		return &Handler{hasPermHook: func(_ *http.Request, p string) bool {
			for _, d := range perms {
				if d == p {
					return false
				}
			}
			return true
		}}
	}
	post := func(h *Handler, fn func(*Handler) http.HandlerFunc, path string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader("name=nc.firma.de&action=on"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fn(h)(rec, req)
		return rec.Code
	}
	cases := []struct {
		name string
		h    *Handler
		fn   func(*Handler) http.HandlerFunc
		path string
	}{
		{"remove ohne Bereichsadministrator", deny(permCertsAdmin), func(h *Handler) http.HandlerFunc { return h.CertRemoveWeb }, "/admin/server-config/certs/remove"},
		{"strict ohne system.cloudflare", deny(permCloudflare), func(h *Handler) http.HandlerFunc { return h.CertStrictWeb }, "/admin/server-config/certs/strict"},
		{"issue ohne system.certs", deny(permCerts), func(h *Handler) http.HandlerFunc { return h.CertIssueWeb }, "/admin/server-config/certs/issue"},
		{"add ohne system.certs", deny(permCerts), func(h *Handler) http.HandlerFunc { return h.CertAddWeb }, "/admin/server-config/certs/add"},
		{"settings ohne system.certs", deny(permCerts), func(h *Handler) http.HandlerFunc { return h.CertSettingsWeb }, "/admin/server-config/certs/settings"},
		{"strict ohne system.certs", deny(permCerts), func(h *Handler) http.HandlerFunc { return h.CertStrictWeb }, "/admin/server-config/certs/strict"},
	}
	for _, c := range cases {
		if code := post(c.h, c.fn, c.path); code != http.StatusForbidden {
			t.Errorf("%s: %d, erwartet 403", c.name, code)
		}
	}
	rec := httptest.NewRecorder()
	deny(permCerts).CertPanelWeb(rec, httptest.NewRequest("GET", "/admin/server-config/certs/panel", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("panel ohne system.certs: %d", rec.Code)
	}
}

// ── Aktionen ─────────────────────────────────────────────────

type certActLog struct {
	agent, revoke, issue []string
	agentErr, revokeErr  error
	settings             map[string]string
}

func testCertAct(store *memCertStore, l *certActLog) certActDeps {
	if l.settings == nil {
		l.settings = map[string]string{}
	}
	return certActDeps{
		store:      store,
		zone:       func(context.Context) (string, error) { return "firma.de", nil },
		setting:    func(_ context.Context, k string) string { return l.settings[k] },
		putSetting: func(_ context.Context, k, v string) error { l.settings[k] = v; return nil },
		blocked:    func() bool { return false },
		startIssue: func(_ context.Context, name string) error { l.issue = append(l.issue, name); return nil },
		agentRemove: func(_ context.Context, r certRow) error {
			l.agent = append(l.agent, r.Name)
			return l.agentErr
		},
		revoke: func(_ context.Context, r certRow) error {
			l.revoke = append(l.revoke, r.Name)
			return l.revokeErr
		},
	}
}

func TestCertAddRejectsForeignName(t *testing.T) {
	store := newMemCertStore()
	l := &certActLog{}
	d := testCertAct(store, l)
	if _, err := certDoAdd(context.Background(), d, "nc.andere.de"); err == nil || !strings.Contains(err.Error(), "liegt nicht in der Domain") {
		t.Fatalf("fremder Name: %v", err)
	}
	if rows, _ := store.certList(context.Background()); len(rows) != 0 {
		t.Fatalf("nichts speichern: %+v", rows)
	}
	// ein Name der Domain wird mit der Testumgebung aus der Einstellung eingetragen
	l.settings[keyCertStaging] = "1"
	if _, err := certDoAdd(context.Background(), d, " NC.firma.de "); err != nil {
		t.Fatal(err)
	}
	if r := store.get("nc.firma.de"); r.Name == "" || !r.Staging {
		t.Fatalf("eingetragen: %+v", r)
	}
	if _, err := certDoAdd(context.Background(), d, "nc.firma.de"); err == nil {
		t.Error("doppelter Name muss abgelehnt werden")
	}
}

func TestCertIssueChecks(t *testing.T) {
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateNeu},
		certRow{Name: "pdh.firma.de", State: certStateLaeuft, Step: "pruefen"})
	l := &certActLog{}
	d := testCertAct(store, l)
	ctx := context.Background()
	if _, err := certDoIssue(ctx, d, "nc.firma.de"); err == nil || !strings.Contains(err.Error(), "E-Mail") || len(l.issue) != 0 {
		t.Fatalf("ohne E-Mail: %v %v", err, l.issue)
	}
	l.settings[keyCertEmail] = "it@firma.de"
	if _, err := certDoIssue(ctx, d, "unbekannt.firma.de"); err == nil || len(l.issue) != 0 {
		t.Fatalf("nicht eingetragener Name: %v", err)
	}
	if _, err := certDoIssue(ctx, d, "x.andere.de"); err == nil || !strings.Contains(err.Error(), "liegt nicht in der Domain") {
		t.Fatalf("fremder Name: %v", err)
	}
	if msg, err := certDoIssue(ctx, d, "pdh.firma.de"); err != nil || !strings.Contains(msg, errCertBusy.Error()) || len(l.issue) != 0 {
		t.Fatalf("laufender Auftrag: %q %v", msg, err)
	}
	if _, err := certDoIssue(ctx, d, "nc.firma.de"); err != nil || len(l.issue) != 1 {
		t.Fatalf("starten: %v %v", err, l.issue)
	}
	d.blocked = func() bool { return true }
	if _, err := certDoIssue(ctx, d, "nc.firma.de"); err == nil || len(l.issue) != 1 {
		t.Fatalf("Spiegel/Wartung: %v", err)
	}
}

// Entfernen: Agent, dann Widerruf, dann löschen; lehnt der Agent ab, bleibt alles.
func TestCertRemoveOrder(t *testing.T) {
	ctx := context.Background()
	withCert := certRow{Name: "nc.firma.de", State: certStateGueltig, ChainPEM: "-----BEGIN CERTIFICATE-----", InstalledFP: "ab"}

	store := newMemCertStore(withCert)
	l := &certActLog{agentErr: errors.New("Die nginx-Seite nextcloud verweist noch auf das Zertifikat – nicht geändert")}
	if _, err := certDoRemove(ctx, testCertAct(store, l), "nc.firma.de"); err == nil || !strings.Contains(err.Error(), "nicht geändert") {
		t.Fatalf("Agent lehnt ab: %v", err)
	}
	if store.get("nc.firma.de").Name == "" || len(l.revoke) != 0 {
		t.Fatal("bei Ablehnung nichts widerrufen, nichts löschen")
	}

	store = newMemCertStore(withCert)
	l = &certActLog{revokeErr: errors.New("CA nicht erreichbar"), settings: map[string]string{keyCertPrevPrefix + "nc.firma.de": `{"cert":"a","key":"b"}`}}
	msg, err := certDoRemove(ctx, testCertAct(store, l), "nc.firma.de")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "Widerruf") || len(l.agent) != 1 || len(l.revoke) != 1 {
		t.Fatalf("Widerruf gescheitert: %q %v", msg, err)
	}
	if l.settings[keyCertPrevPrefix+"nc.firma.de"] != "" {
		t.Error("gemerkte Zertifikatszeilen bleiben nach dem Entfernen stehen")
	}

	store = newMemCertStore(certRow{Name: "neu.firma.de", State: certStateFehler})
	l = &certActLog{}
	if _, err := certDoRemove(ctx, testCertAct(store, l), "neu.firma.de"); err != nil || store.get("neu.firma.de").Name != "" || len(l.agent)+len(l.revoke) != 0 {
		t.Fatalf("ohne Zertifikat nur löschen: %v %+v", err, l)
	}

	store = newMemCertStore(withCert)
	l = &certActLog{settings: map[string]string{keyCertStrictBefore: `{"ssl":"full"}`}}
	if _, err := certDoRemove(ctx, testCertAct(store, l), "nc.firma.de"); err == nil || store.get("nc.firma.de").Name == "" || len(l.agent) != 0 {
		t.Fatalf("bei Full (strict) nicht entfernen: %v", err)
	}
}

func TestCertSettingsValidate(t *testing.T) {
	l := &certActLog{}
	d := testCertAct(newMemCertStore(), l)
	if _, err := certDoSettings(context.Background(), d, "kein-mail", false); err == nil || l.settings[keyCertEmail] != "" {
		t.Fatalf("ungültige E-Mail: %v", err)
	}
	if _, err := certDoSettings(context.Background(), d, " it@firma.de ", true); err != nil || l.settings[keyCertEmail] != "it@firma.de" || l.settings[keyCertStaging] != "1" {
		t.Fatalf("speichern: %v %+v", err, l.settings)
	}
}

// certLines: Ablauf in Ortszeit, Weg im Haus, Entfernen nur für den Bereichsadministrator.
func TestCertLines(t *testing.T) {
	until := time.Date(2027, 2, 9, 12, 0, 0, 0, time.Local)
	rows := []certRow{{Name: "nc.firma.de", State: certStateGueltig, NotAfter: until, ChainPEM: "x", Staging: true, LastError: "noch nicht eingerichtet: Agent fehlt"}}
	got := certLines(rows, map[string]string{"nc.firma.de": "direkt"}, true)
	if len(got) != 1 || got[0].ValidUntil != "09.02.2027" || got[0].LAN != "direkt" || !got[0].CanRemove || !got[0].Staging || !strings.HasPrefix(got[0].Error, "noch nicht eingerichtet") {
		t.Fatalf("%+v", got)
	}
}
