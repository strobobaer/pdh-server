package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
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

// Die Karte der Zertifikate hängt nicht mehr an der Cloudflare-Karte (eigener Ladeweg, I4).
func TestCloudflarePanelWithoutCerts(t *testing.T) {
	tmpl := loadTestTemplates(t)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "cf-panel", cfPanel{Error: "Cloudflare lehnt den API-Token ab"}); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "lehnt den API-Token ab") || strings.Contains(out, `id="cert-panel"`) {
		t.Errorf("Cloudflare-Karte:\n%s", out)
	}
}

// Die Seite der Server-Einstellungen lädt die Karte nicht selbst (kein Agent, keine CA); mit
// system.certs gibt es den Knopf „Zertifikate laden“ – auch ohne API-Token und ohne system.cloudflare.
func TestServerConfigDoesNotLoadCerts(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Groups: []envGroupView{{Key: "cloudflare"}}, CF: &CloudflareView{CanAPI: true, APITokenSet: true}}
	d.Tab = "cloudflare"
	out := renderPage(t, tmpl, "server_config", d)
	if strings.Contains(out, "certs/panel") || strings.Contains(out, `id="cert-panel"`) {
		t.Error("ohne Recht kein Knopf für die Zertifikate")
	}
	d.CF = &CloudflareView{CanCerts: true} // kein API-Token, kein system.cloudflare
	out = renderPage(t, tmpl, "server_config", d)
	btn := regexp.MustCompile(`<button[^>]*certs/panel[^>]*>`).FindString(out)
	if btn == "" || !strings.Contains(out, "Zertifikate laden") || !strings.Contains(out, `id="cert-panel"`) {
		t.Fatalf("Knopf „Zertifikate laden“ fehlt:\n%s", out)
	}
	if strings.Count(out, "certs/panel") != 1 || strings.Contains(btn, "hx-trigger") {
		t.Error("die Karte darf nur auf Klick laden")
	}
}

// Ohne API-Token: Hinweis, keine Formulare zum Holen – aber vorhandene Namen mit „Entfernen“ (I4).
func TestCertPanelWithoutTokenShowsRemove(t *testing.T) {
	p := certPanel{CanManage: true, Hint: "Zuerst oben den API-Token eintragen und speichern.",
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig, ValidUntil: "09.02.2027", CanRemove: true,
			Note: "Zertifikat liegt unter /etc/ssl/pdh/nc.firma.de/. Bitte von Hand eintragen."}}}
	out := renderCertPanel(t, p)
	if !strings.Contains(out, "nc.firma.de") || strings.Count(out, `value="remove"`) != 1 || strings.Contains(out, `value="issue"`) {
		t.Errorf("Zeile mit Entfernen erwartet:\n%s", out)
	}
	if strings.Contains(out, "certs/add") || strings.Contains(out, "certs/settings") || strings.Contains(out, "certs/strict") {
		t.Errorf("ohne Token keine Formulare zum Eintragen:\n%s", out)
	}
	if !strings.Contains(out, "Bitte von Hand eintragen.") || !strings.Contains(out, "cert-note") {
		t.Errorf("Hinweis des Agenten fehlt:\n%s", out)
	}
	p.Lines[0].CanRemove = false
	if out := renderCertPanel(t, p); strings.Contains(out, "<form") {
		t.Errorf("ohne Bereichsadministrator keine Formulare:\n%s", out)
	}
}

// I8: Die Rückfrage zu Full (strict) sagt, dass die Verschlüsselung für alle Namen der Domain gilt.
func TestCertStrictConfirmMentionsWholeZone(t *testing.T) {
	out := renderCertPanel(t, certPanel{CanManage: true, CanStrict: true, Zone: "firma.de", AgentOK: true, NginxOK: true,
		Lines: []certLine{{Name: "nc.firma.de", State: certStateGueltig}}})
	if !strings.Contains(out, "ALLE Namen der Domain firma.de") || !strings.Contains(out, "nicht über den Tunnel") {
		t.Errorf("Hinweis auf die ganze Domain fehlt:\n%s", out)
	}
}

// I4: Entfernen braucht keinen Cloudflare-Zugang – nur einen eingetragenen Namen.
func TestCertRemoveWithoutCloudflare(t *testing.T) {
	ctx := context.Background()
	store := newMemCertStore(certRow{Name: "nc.firma.de", State: certStateGueltig, ChainPEM: "-----BEGIN CERTIFICATE-----", InstalledFP: "ab"})
	l := &certActLog{revokeErr: errors.New("kein Zugang"), settings: map[string]string{keyCertNotePrefix + "nc.firma.de": "x", keyCertPendingPrefix + "nc.firma.de": "y"}}
	d := testCertAct(store, l)
	d.zone = func(context.Context) (string, error) { return "", errors.New("Zuerst oben den API-Token eintragen und speichern") }
	if _, err := certDoRemove(ctx, d, "unbekannt.firma.de"); err == nil || !strings.Contains(err.Error(), "nicht eingetragen") {
		t.Fatalf("nicht eingetragen: %v", err)
	}
	msg, err := certDoRemove(ctx, d, " NC.firma.de ")
	if err != nil || store.get("nc.firma.de").Name != "" || !strings.Contains(msg, "Widerruf") || len(l.agent) != 1 {
		t.Fatalf("ohne Cloudflare: %q %v", msg, err)
	}
	if l.settings[keyCertNotePrefix+"nc.firma.de"] != "" || l.settings[keyCertPendingPrefix+"nc.firma.de"] != "" {
		t.Error("Hinweis und Merker bleiben nach dem Entfernen stehen")
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
		{"issue ohne system.certs und ohne Bereichsadministrator", deny(permCerts, permCertsAdmin), func(h *Handler) http.HandlerFunc { return h.CertIssueWeb }, "/admin/server-config/certs/issue"},
		{"add ohne system.certs und ohne Bereichsadministrator", deny(permCerts, permCertsAdmin), func(h *Handler) http.HandlerFunc { return h.CertAddWeb }, "/admin/server-config/certs/add"},
		{"settings ohne system.certs und ohne Bereichsadministrator", deny(permCerts, permCertsAdmin), func(h *Handler) http.HandlerFunc { return h.CertSettingsWeb }, "/admin/server-config/certs/settings"},
		{"strict ohne system.certs und ohne Bereichsadministrator", deny(permCerts, permCertsAdmin), func(h *Handler) http.HandlerFunc { return h.CertStrictWeb }, "/admin/server-config/certs/strict"},
	}
	for _, c := range cases {
		if code := post(c.h, c.fn, c.path); code != http.StatusForbidden {
			t.Errorf("%s: %d, erwartet 403", c.name, code)
		}
	}
	rec := httptest.NewRecorder()
	deny(permCerts, permCertsAdmin).CertPanelWeb(rec, httptest.NewRequest("GET", "/admin/server-config/certs/panel", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("panel ohne system.certs: %d", rec.Code)
	}
}

// system.certs_admin allein genügt für Karte, Holen und Entfernen; system.certs allein nicht fürs Entfernen.
func TestCertAdminAloneSuffices(t *testing.T) {
	tmpl := loadTestTemplates(t)
	only := func(perms ...string) *Handler {
		return &Handler{tmpl: tmpl, hasPermHook: func(_ *http.Request, p string) bool {
			for _, x := range perms {
				if x == p {
					return true
				}
			}
			return false
		}}
	}
	post := func(fn http.HandlerFunc, path string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader("name=nc.firma.de"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec.Code
	}
	admin := only(permCertsAdmin)
	req := httptest.NewRequest("GET", "/admin/server-config/certs/panel", nil)
	if !admin.certPanelFor(context.Background(), req, "", nil).CanManage {
		t.Error("Bereichsadministrator allein muss die Karte sehen")
	}
	// Karte mit seinen Rechten: Formulare und Entfernen-Knopf
	p := certPanel{CanManage: admin.certCanManage(req), Zone: "firma.de", AgentOK: true, NginxOK: true,
		Lines: certLines([]certRow{{Name: "nc.firma.de", State: certStateGueltig}}, nil, admin.certMayRemove(req))}
	if out := renderCertPanel(t, p); !strings.Contains(out, "<form") || !strings.Contains(out, `value="remove"`) {
		t.Errorf("Formulare und Entfernen fehlen:\n%s", out)
	}
	rec := httptest.NewRecorder()
	admin.CertPanelWeb(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("panel: %d", rec.Code)
	}
	for path, fn := range map[string]http.HandlerFunc{
		"/admin/server-config/certs/remove":   admin.CertRemoveWeb,
		"/admin/server-config/certs/issue":    admin.CertIssueWeb,
		"/admin/server-config/certs/add":      admin.CertAddWeb,
		"/admin/server-config/certs/settings": admin.CertSettingsWeb,
	} {
		if code := post(fn, path); code != http.StatusOK {
			t.Errorf("%s mit system.certs_admin allein: %d", path, code)
		}
	}
	if code := post(only(permCerts).CertRemoveWeb, "/admin/server-config/certs/remove"); code != http.StatusForbidden {
		t.Errorf("remove nur mit system.certs: %d, erwartet 403", code)
	}

	// Das Gate lässt eines der beiden Rechte gelten – aber nur für system.certs.
	has := func(perms ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range perms {
				if x == p {
					return true
				}
			}
			return false
		}
	}
	if !gatePermOK(permCerts, has(permCertsAdmin)) || !gatePermOK(permCerts, has(permCerts)) || gatePermOK(permCerts, has()) {
		t.Error("gatePermOK für system.certs")
	}
	if gatePermOK(permCertsAdmin, has(permCerts)) || gatePermOK(permCloudflare, has(permCertsAdmin)) {
		t.Error("system.certs ersetzt nie den Bereichsadministrator, und nichts ersetzt system.cloudflare")
	}
}

// Durch die echte PermissionGateMiddleware und isAreaAdmin (braucht eine Test-Datenbank).
func TestCertGateMiddlewareIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	roleAdmin, roleCerts := "zertadm"+sfx, "zert"+sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'ZA', 40), ($2, 'Z', 40)`, roleAdmin, roleCerts)
	must(err)
	var uidAdmin, uidCerts string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Z', 'A', $2, '', '') RETURNING id::text`, "za"+sfx, roleAdmin).Scan(&uidAdmin))
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Z', 'C', $2, '', '') RETURNING id::text`, "zc"+sfx, roleCerts).Scan(&uidCerts))
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1::uuid, $2::uuid)`, uidAdmin, uidCerts)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE key IN ($1, $2)`, roleAdmin, roleCerts)
	}()
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p WHERE (r.key = $1 AND p.key = 'system.certs_admin') OR (r.key = $2 AND p.key = 'system.certs')`, roleAdmin, roleCerts)
	must(err)
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	const secret = "test-secret-zertifikate-0123456789abcdef"
	h := &Handler{db: pool, rbac: rb, users: users.NewService(users.NewRepository(pool), secret, 1), jwtSecret: secret}
	reached := false
	gate := h.PermissionGateMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
	through := func(uid, path string) bool {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid}).SignedString([]byte(secret))
		must(err)
		req := httptest.NewRequest("POST", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		reached = false
		gate.ServeHTTP(httptest.NewRecorder(), req)
		return reached
	}
	for _, path := range []string{"remove", "issue", "add", "settings"} {
		if !through(uidAdmin, "/admin/server-config/certs/"+path) {
			t.Errorf("%s: Bereichsadministrator allein wird abgewiesen", path)
		}
	}
	if through(uidAdmin, "/admin/server-config/certs/strict") {
		t.Error("strict ohne system.cloudflare durchgelassen")
	}
	if through(uidCerts, "/admin/server-config/certs/remove") {
		t.Error("remove nur mit system.certs durchgelassen")
	}
	if !through(uidCerts, "/admin/server-config/certs/issue") {
		t.Error("issue mit system.certs abgewiesen")
	}
	// certMayRemove über isAreaAdmin/hasPermRec (rbac gesetzt)
	asUser := func(uid, role string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: uid, Role: users.Role(role)}))
	}
	if !h.certMayRemove(asUser(uidAdmin, roleAdmin)) || !h.certCanManage(asUser(uidAdmin, roleAdmin)) {
		t.Error("Bereichsadministrator: entfernen und Karte")
	}
	if h.certMayRemove(asUser(uidCerts, roleCerts)) || !h.certCanManage(asUser(uidCerts, roleCerts)) {
		t.Error("system.certs: Karte ja, entfernen nein")
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
