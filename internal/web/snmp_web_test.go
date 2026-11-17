package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/internal/modules/it"
)

// SNMP v3 – Oberfläche (snmp_web.go): Rechte an jeder Route, Kennwörter nie in einer
// Antwort, „leer = unverändert“, Klartext-Fehler bei „Prüfen“, Darstellung leer und mit Daten.

const snmpWebID = "22222222-2222-4222-8222-222222222222"

// snmpWebRoutes: jede neue Route mit ihrem Recht. Der Test unten hält Routen, Gate-Regeln
// und Handler zusammen – eine neue Route ohne Eintrag hier fällt in TestSNMPRoutesRegistered auf.
func snmpWebRoutes(h *Handler) []struct {
	path, perm string
	fn         http.HandlerFunc
} {
	return []struct {
		path, perm string
		fn         http.HandlerFunc
	}{
		{"/admin/server-config/snmp/profiles", permSNMP, h.SNMPProfileCreateWeb},
		{"/admin/server-config/snmp/profiles/" + snmpWebID, permSNMP, h.SNMPProfileUpdateWeb},
		{"/admin/server-config/snmp/profiles/" + snmpWebID + "/delete", permSNMPAdmin, h.SNMPProfileDeleteWeb},
		{"/it/net/snmp/targets", permITNetsync, h.ITNetSNMPTargetCreateWeb},
		{"/it/net/snmp/targets/" + snmpWebID, permITNetsync, h.ITNetSNMPTargetUpdateWeb},
		{"/it/net/snmp/targets/" + snmpWebID + "/enabled", permITNetsync, h.ITNetSNMPTargetEnabledWeb},
		{"/it/net/snmp/targets/" + snmpWebID + "/check", permITNetsync, h.ITNetSNMPTargetCheckWeb},
		{"/it/net/snmp/targets/" + snmpWebID + "/remove", itNetPermAdmin, h.ITNetSNMPTargetRemoveWeb},
	}
}

var snmpWebForm = url.Values{"name": {"Switches"}, "username": {"pdh"}, "auth_proto": {"sha256"}, "auth_pass": {snmpTestAuth},
	"priv_proto": {"aes128"}, "priv_pass": {snmpTestPriv}, "host": {"10.79.0.1"}, "port": {"161"}, "profile": {snmpWebID}, "on": {"1"}}

// Jede neue Route hat eine Gate-Regel mit genau ihrem Recht, und der Handler lehnt ohne
// dieses Recht mit 403 ab – auch wenn alle anderen Rechte da sind.
func TestSNMPRoutesNeedTheirPermission(t *testing.T) {
	for _, c := range snmpWebRoutes(&Handler{}) {
		_, perm, ref, _, hit := matchGate("POST", c.path)
		if !hit || perm != c.perm || ref != "" {
			t.Errorf("Gate %s: Treffer %v, Recht %q, erwartet %q", c.path, hit, perm, c.perm)
		}
	}
	for _, path := range []string{"/admin/server-config/snmp", "/admin/server-config/snmp/profiles/x", "/admin/server-config/snmp/profiles/" + snmpWebID + "/anderes",
		"/it/net/snmp/targets/" + snmpWebID + "/anderes", "/it/net/snmp"} {
		if _, perm, _, _, ok := matchGate("POST", path); ok && (perm == permSNMP || perm == permSNMPAdmin) {
			t.Errorf("%s trifft eine SNMP-Regel (%s)", path, perm)
		}
	}
	// Der Bereichsadministrator darf auch anlegen und ändern; UniFi- und Omada-Recht ersetzen nichts.
	if !gatePermOK(permSNMP, func(p string) bool { return p == permSNMPAdmin }) {
		t.Error("system.snmp_admin muss für die Profil-Routen genügen")
	}
	if gatePermOK(permSNMP, func(p string) bool { return p == permUnifi || p == permOmada || p == permCertsAdmin }) {
		t.Error("ein fremdes Recht ersetzt system.snmp")
	}
	if gatePermOK(permSNMPAdmin, func(p string) bool { return p != permSNMPAdmin }) {
		t.Error("Profil löschen: nur system.snmp_admin")
	}
	if areaAdminPerm["snmp"] != permSNMPAdmin {
		t.Errorf("Bereichsadministrator SNMP: %q", areaAdminPerm["snmp"])
	}

	for _, c := range snmpWebRoutes(&Handler{}) {
		var asked []string
		h := &Handler{hasPermHook: func(_ *http.Request, p string) bool {
			asked = append(asked, p)
			// alles außer dem Recht der Route – und außer dem Bereichsadministrator, der es ersetzt
			return p != c.perm && !(c.perm == permSNMP && p == permSNMPAdmin)
		}}
		var fn http.HandlerFunc
		for _, x := range snmpWebRoutes(h) {
			if x.path == c.path {
				fn = x.fn
			}
		}
		w := itNetDo(fn, "POST", c.path, snmpWebID, "", snmpWebForm)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne %s: Status %d", c.path, c.perm, w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, snmpTestAuth) || strings.Contains(body, snmpTestPriv) || strings.Contains(body, "<form") {
			t.Errorf("%s: Antwort ohne Recht zeigt zu viel: %s", c.path, body)
		}
		found := false
		for _, p := range asked {
			found = found || p == c.perm
		}
		if !found {
			t.Errorf("%s: Recht %s wurde nicht geprüft (geprüft: %v)", c.path, c.perm, asked)
		}
	}
}

// Die Routen sind im Router eingetragen – jede aus snmpWebRoutes, und keine weitere unter
// den beiden Pfaden, die hier fehlt.
func TestSNMPRoutesRegistered(t *testing.T) {
	b, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	want := map[string]bool{}
	for _, c := range snmpWebRoutes(&Handler{}) {
		p := strings.ReplaceAll(c.path, snmpWebID, "{id}")
		want[p] = true
		if !strings.Contains(src, `r.Post("`+p+`", h.`) {
			t.Errorf("Route %s fehlt in handler.go", p)
		}
	}
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"/admin/server-config/snmp", "/it/net/snmp"} {
			i := strings.Index(line, `("`+prefix)
			if i < 0 || !strings.HasPrefix(line, "r.") {
				continue
			}
			p := line[i+2:]
			p = p[:strings.Index(p, `"`)]
			if !want[p] {
				t.Errorf("Route %s steht in handler.go, aber nicht in snmpWebRoutes – Recht und Gate-Regel prüfen", p)
			}
		}
	}
}

// Ohne Bereichsadministrator: Profil löschen 403, auch mit system.snmp. Ohne it.admin:
// Entfernen 403, auch mit it.netsync.
func TestSNMPDeleteOnlyAreaAdmin(t *testing.T) {
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == permSNMP || p == permITNetsync || p == permITView }}
	if w := itNetDo(h.SNMPProfileDeleteWeb, "POST", "/admin/server-config/snmp/profiles/"+snmpWebID+"/delete", snmpWebID, "", nil); w.Code != http.StatusForbidden {
		t.Errorf("Profil löschen mit system.snmp allein: %d", w.Code)
	}
	if w := itNetDo(h.ITNetSNMPTargetRemoveWeb, "POST", "/it/net/snmp/targets/"+snmpWebID+"/remove", snmpWebID, "", nil); w.Code != http.StatusForbidden {
		t.Errorf("Entfernen mit it.netsync allein: %d", w.Code)
	}
	if h.snmpMayDelete(httptest.NewRequest("GET", "/", nil)) || !h.snmpCanManage(httptest.NewRequest("GET", "/", nil)) {
		t.Error("system.snmp: verwalten ja, löschen nein")
	}
	admin := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == permSNMPAdmin }}
	if !admin.snmpMayDelete(httptest.NewRequest("GET", "/", nil)) || !admin.snmpCanManage(httptest.NewRequest("GET", "/", nil)) {
		t.Error("Bereichsadministrator: verwalten und löschen")
	}
	if (&Handler{}).snmpCanManage(httptest.NewRequest("GET", "/", nil)) {
		t.Error("ohne Rechtedienst darf niemand")
	}
}

func renderSNMPTemplate(t *testing.T, name string, v any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := loadTestTemplates(t).ExecuteTemplate(&buf, name, v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Der Reiter: Einleitung, ehrliche Hinweise, Verfahren, Platzhalter; ohne Recht nur der Satz.
func TestSNMPPanelRender(t *testing.T) {
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == permSNMP }}
	tab := h.snmpTabFor(context.Background(), httptest.NewRequest("GET", "/admin/server-config", nil))
	if !tab.Can || tab.CanDelete || tab.New.Auth != "sha256" || tab.New.Priv != "aes128" || len(tab.AuthProtos) != 5 || len(tab.PrivProtos) != 3 {
		t.Fatalf("Reiter: %+v", tab)
	}
	out := renderSNMPTemplate(t, "snmp-panel", tab)
	for _, want := range []string{`id="snmp-panel"`, "Zusatz – ohne läuft alles wie bisher", "noch nicht erprobt", "beide Server",
		`hx-post="/admin/server-config/snmp/profiles"`, `hx-target="#snmp-panel"`, `type="password" name="auth_pass"`, `type="password" name="priv_pass"`,
		`<option value="sha256" selected>SHA-256</option>`, `<option value="aes128" selected>AES-128</option>`, `<option value="sha1" >SHA-1 (veraltet)</option>`,
		"MD5, DES und SNMP v1/v2c bietet das PDH nicht an", "Noch kein Zugangsprofil", "system.snmp_admin", `href="/it/net"`} {
		if !strings.Contains(out, want) {
			t.Errorf("leerer Reiter enthält %q nicht", want)
		}
	}
	for _, bad := range []string{"md5", "MD5-", ">DES<", "hinterlegt – nur zum Ändern eingeben", "/delete"} {
		if strings.Contains(out, bad) {
			t.Errorf("leerer Reiter enthält %q", bad)
		}
	}

	// mit Profilen: Liste, Ändern mit Platzhalter, Löschen nur für den Bereichsadministrator
	tab.Profiles = []snmpProfileRow{
		{SNMPProfile: SNMPProfile{ID: snmpWebID, Name: "Alt-Switches", User: "pdh", AuthProto: "sha1", PrivProto: "aes128", Targets: 2},
			AuthName: "SHA-1", PrivName: "AES-128", AuthOld: true, Form: snmpProfileForm{Name: "Alt-Switches", User: "pdh", Auth: "sha1", Priv: "aes128"}},
		{SNMPProfile: SNMPProfile{ID: itNetTestID, Name: "Drucker", User: "druck", AuthProto: "sha512", PrivProto: "aes256"},
			AuthName: "SHA-512", PrivName: "AES-256", Form: snmpProfileForm{Name: "Drucker", User: "druck", Auth: "sha512", Priv: "aes256"}},
	}
	out = renderSNMPTemplate(t, "snmp-panel", tab)
	for _, want := range []string{"Alt-Switches", "SHA-1 / AES-128", `>veraltet</span>`, "2 Geräte", "0 Geräte", "SHA-512 / AES-256",
		`hx-post="/admin/server-config/snmp/profiles/` + snmpWebID + `"`, `placeholder="hinterlegt – nur zum Ändern eingeben"`,
		`<option value="sha1" selected>SHA-1 (veraltet)</option>`, `value="druck"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter mit Profilen enthält %q nicht", want)
		}
	}
	if strings.Contains(out, "/delete") || strings.Contains(out, "ti-trash") {
		t.Error("ohne Bereichsadministrator ist „Löschen“ zu sehen")
	}
	tab.CanDelete = true
	out = renderSNMPTemplate(t, "snmp-panel", tab)
	if !strings.Contains(out, `hx-post="/admin/server-config/snmp/profiles/`+itNetTestID+`/delete"`) || !strings.Contains(out, "endgültig löschen?") {
		t.Error("Bereichsadministrator: „Löschen“ am unbenutzten Profil fehlt")
	}
	if strings.Contains(out, `profiles/`+snmpWebID+`/delete`) || !strings.Contains(out, "Wird noch von 2 Geräten genutzt") {
		t.Error("benutztes Profil: Knopf muss gesperrt sein und den Grund nennen")
	}

	// ohne Recht: nur der Satz mit dem Recht
	none := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	tab = none.snmpTabFor(context.Background(), httptest.NewRequest("GET", "/admin/server-config", nil))
	out = renderSNMPTemplate(t, "snmp-panel", tab)
	if tab.Can || !strings.Contains(out, "system.snmp") || strings.Contains(out, "<form") || strings.Contains(out, "<select") {
		t.Errorf("ohne Recht kein Formular:\n%s", out)
	}
}

// Server-Einstellungen: Reiter und Karte „SNMP“ nur mit system.snmp.
func TestServerConfigSNMPTab(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{Unifi: unifiTab{Can: true}, Omada: omadaTab{Can: true}}
	out := renderPage(t, tmpl, "server_config", d)
	if strings.Contains(out, "tab=snmp") || strings.Contains(out, `data-pane="snmp"`) || strings.Contains(out, "snmp-panel") ||
		strings.Contains(out, "server-config/snmp") {
		t.Error("ohne system.snmp sind Reiter oder Karte „SNMP“ zu sehen")
	}
	d = ServerConfigData{SNMP: snmpTab{Can: true}}
	out = renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{`data-tab="snmp" href="?tab=snmp"`, `data-pane="snmp"`, `id="snmp-panel"`, ".snmp-form{"} {
		if !strings.Contains(out, want) {
			t.Errorf("mit system.snmp fehlt %q", want)
		}
	}
	if strings.Contains(out, `data-pane="unifi"`) || strings.Contains(out, `data-pane="omada"`) || strings.Contains(out, `SNMP<span class="sc-dot`) {
		t.Error("der Reiter „SNMP“ darf nicht an anderen Rechten hängen; ohne Profil kein Punkt")
	}
	d.SNMP.Profiles = []snmpProfileRow{{SNMPProfile: SNMPProfile{ID: snmpWebID, Name: "A"}}}
	if out = renderPage(t, tmpl, "server_config", d); !strings.Contains(out, `SNMP<span class="sc-dot ok"`) {
		t.Error("mit Profil: grüner Punkt am Reiter fehlt")
	}
}

// Ergebnis von „Prüfen“: Angaben des Geräts oder der Fehler.
func TestSNMPCheckRender(t *testing.T) {
	d := snmpSwitch("sw-halle", "02:79:00:00:00:01")
	out := renderSNMPTemplate(t, "itn-snmp-check", ITNetSNMPCheckView{Detail: d, Ports: 2, Up: 1, Switch: true})
	for _, want := range []string{"Das Gerät antwortet (Switch).", "sw-halle", "XS-24", "1.2.3", "SN-sw-halle", "02:79:00:00:00:01", "2 (1 aktiv)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Ergebnis enthält %q nicht", want)
		}
	}
	out = renderSNMPTemplate(t, "itn-snmp-check", ITNetSNMPCheckView{Err: "Zugang abgelehnt – Profil prüfen"})
	if !strings.Contains(out, "Zugang abgelehnt – Profil prüfen") || !strings.Contains(out, `role="alert"`) || strings.Contains(out, "antwortet") {
		t.Errorf("Fehler:\n%s", out)
	}
}

// ── mit Datenbank ────────────────────────────────────────────

// snmpWebEnv: snmpEnv mit Vorlagen und echtem Rechtedienst; Meldungen an die
// Administratoren landen in notes statt im Chat.
func snmpWebEnv(t *testing.T) (*snmpEnv, *[]string) {
	t.Helper()
	e := newSNMPEnv(t)
	rb := rbac.NewService(rbac.NewRepository(e.pool))
	if err := rb.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.h.rbac = rb
	e.h.tmpl = loadTestTemplates(t)
	e.h.it = it.NewService(it.NewRepository(e.pool))
	cwd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	notes := &[]string{}
	e.h.itNet.snmpNotify = func(_ context.Context, msg string) { *notes = append(*notes, msg) }
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	return e, notes
}

func snmpNoSecrets(t *testing.T, where, body string, secrets ...string) {
	t.Helper()
	for _, s := range append(secrets, snmpTestAuth, snmpTestPriv) {
		if s != "" && strings.Contains(body, s) {
			t.Errorf("%s: die Antwort enthält ein Kennwort", where)
		}
	}
}

// Profile über den ganzen Weg: anlegen, ändern mit leeren Kennwörtern (bleiben), ändern mit
// neuem Kennwort, Fehler mit behaltenen Eingaben, löschen. Kein Kennwort in Antwort oder Meldung.
func TestSNMPProfileWeb(t *testing.T) {
	e, notes := snmpWebEnv(t)
	ctx := context.Background()
	const base = "/admin/server-config/snmp/profiles"
	post := func(fn http.HandlerFunc, path, id string, f url.Values) string {
		t.Helper()
		w := itNetDo(fn, "POST", path, id, "", f)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	form := func(name, auth, priv string) url.Values {
		return url.Values{"name": {name}, "username": {"leser"}, "auth_proto": {"sha256"}, "auth_pass": {auth}, "priv_proto": {"aes128"}, "priv_pass": {priv}}
	}
	const auth1, priv1, auth2 = "Anmelde-Kennwort-1", "Schluessel-Kennwort-1", "Anmelde-Kennwort-2"

	// anlegen
	body := post(e.h.SNMPProfileCreateWeb, base, "", form("Drucker", auth1, priv1))
	snmpNoSecrets(t, "anlegen", body, auth1, priv1)
	if !strings.Contains(body, "Zugangsprofil „Drucker“ angelegt") || !strings.Contains(body, `id="snmp-panel"`) || !strings.Contains(body, "hinterlegt – nur zum Ändern eingeben") {
		t.Fatalf("anlegen:\n%s", body)
	}
	var id string
	list, err := e.h.snmpProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Name == "Drucker" {
			id = p.ID
		}
	}
	if id == "" {
		t.Fatal("Profil nicht gespeichert")
	}
	secrets := func() (string, string) {
		t.Helper()
		cfg, err := e.h.snmpProfileConfig(ctx, id, "10.79.0.1", 161)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.AuthPass, cfg.PrivPass
	}
	if a, p := secrets(); a != auth1 || p != priv1 {
		t.Fatal("Kennwörter nicht gespeichert")
	}

	// ändern, Kennwörter leer = unverändert
	f := form("Drucker Halle", "", "")
	f.Set("auth_proto", "sha512")
	body = post(e.h.SNMPProfileUpdateWeb, base+"/"+id, id, f)
	snmpNoSecrets(t, "ändern", body, auth1, priv1)
	if !strings.Contains(body, "Die Kennwörter bleiben unverändert") || !strings.Contains(body, "Drucker Halle") || !strings.Contains(body, "SHA-512 / AES-128") {
		t.Errorf("ändern ohne Kennwort:\n%s", body)
	}
	if a, p := secrets(); a != auth1 || p != priv1 {
		t.Error("leere Kennwort-Felder haben die Kennwörter verändert")
	}
	// nur ein Kennwort neu
	body = post(e.h.SNMPProfileUpdateWeb, base+"/"+id, id, form("Drucker Halle", auth2, ""))
	snmpNoSecrets(t, "Kennwort ändern", body, auth1, priv1, auth2)
	if a, p := secrets(); a != auth2 || p != priv1 || !strings.Contains(body, "Kennwort der Anmeldung neu hinterlegt") {
		t.Errorf("ein Kennwort neu: %v", strings.Contains(body, "neu hinterlegt"))
	}

	// Fehler: zu kurzes Kennwort – Klartext, Eingaben bleiben, nichts gespeichert, Formular offen
	body = post(e.h.SNMPProfileUpdateWeb, base+"/"+id, id, form("Drucker neu", "kurz", ""))
	snmpNoSecrets(t, "Fehler", body, auth1, priv1, auth2)
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="Drucker neu"`) || !strings.Contains(body, `<details class="snmp-edit" open>`) {
		t.Errorf("Fehler beim Ändern:\n%s", body)
	}
	if a, _ := secrets(); a != auth2 {
		t.Error("ein abgelehntes Kennwort wurde gespeichert")
	}
	body = post(e.h.SNMPProfileCreateWeb, base, "", form("Drucker Halle", auth1, priv1))
	if !strings.Contains(body, "Ein Profil mit diesem Namen gibt es schon") || !strings.Contains(body, `id="snmp-name-neu" name="name" value="Drucker Halle"`) {
		t.Errorf("doppelter Name:\n%s", body)
	}
	snmpNoSecrets(t, "doppelter Name", body, auth1, priv1)

	// löschen: mit Gerät verständliche Meldung, ohne Bereichsadministrator 403
	target, err := e.h.snmpTargetCreate(ctx, "10.79.0.9", 0, id, "")
	if err != nil {
		t.Fatal(err)
	}
	body = post(e.h.SNMPProfileDeleteWeb, base+"/"+id+"/delete", id, nil)
	if !strings.Contains(body, "wird noch von 1 Gerät genutzt") || !strings.Contains(body, "Drucker Halle") {
		t.Errorf("benutztes Profil löschen:\n%s", body)
	}
	if err := e.h.snmpTargetRemove(ctx, target); err != nil {
		t.Fatal(err)
	}
	e.h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSNMPAdmin }
	if w := itNetDo(e.h.SNMPProfileDeleteWeb, "POST", base+"/"+id+"/delete", id, "", nil); w.Code != http.StatusForbidden {
		t.Errorf("löschen ohne Bereichsadministrator: %d", w.Code)
	}
	if _, err := e.h.snmpProfileConfig(ctx, id, "10.79.0.1", 161); err != nil {
		t.Errorf("Profil darf ohne Bereichsadministrator nicht weg sein: %v", err)
	}
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	body = post(e.h.SNMPProfileDeleteWeb, base+"/"+id+"/delete", id, nil)
	if !strings.Contains(body, "Zugangsprofil „Drucker Halle“ gelöscht") {
		t.Errorf("löschen:\n%s", body)
	}
	if _, err := e.h.snmpProfileConfig(ctx, id, "10.79.0.1", 161); !errors.Is(err, errSNMPNoProfile) {
		t.Errorf("Profil nicht gelöscht: %v", err)
	}
	// unbekanntes Profil: Klartext, kein Absturz
	if body = post(e.h.SNMPProfileDeleteWeb, base+"/"+snmpWebID+"/delete", snmpWebID, nil); !strings.Contains(body, "Eintrag nicht gefunden") {
		t.Errorf("unbekanntes Profil:\n%s", body)
	}

	// Meldungen an die Administratoren: nur bei Erfolg, nie mit Kennwort
	if len(*notes) != 4 {
		t.Errorf("Meldungen: %d %v", len(*notes), *notes)
	}
	snmpNoSecrets(t, "Meldungen", strings.Join(*notes, "\n"), auth1, priv1, auth2)

	// Am Spiegelserver und in der Wartung: nichts ändern
	e.h.mlock.locked = true
	body = post(e.h.SNMPProfileCreateWeb, base, "", form("Wartung", auth1, priv1))
	e.h.mlock.locked = false
	if !strings.Contains(body, "während der Wartung") {
		t.Errorf("Wartungssperre:\n%s", body)
	}
	if list, _ := e.h.snmpProfiles(ctx); len(list) != 1 { // nur das Profil aus newSNMPEnv
		t.Errorf("in der Wartung angelegt: %d Profile", len(list))
	}
}

// Seite /it/net: rendert ohne Profil und ohne Gerät und mit Daten; Texte der Karte SNMP;
// Formulare und Knöpfe nur mit dem passenden Recht.
func TestITNetPageSNMP(t *testing.T) {
	e, _ := snmpWebEnv(t)
	ctx := context.Background()
	page := func() string {
		t.Helper()
		w := itNetDo(e.h.ITNetPage, "GET", "/it/net", "", "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("Seite: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	// die Karte der Quelle SNMP (bis zur Einstellungs-Karte)
	srcCard := func(body string) string {
		t.Helper()
		i := strings.Index(body, `data-source="snmp"`)
		j := strings.Index(body, `id="itn-snmp"`)
		if i < 0 || j < i {
			t.Fatalf("Karten fehlen (%d, %d)", i, j)
		}
		return body[i:j]
	}

	// 0 Profile, 0 Geräte
	if err := e.h.snmpProfileDelete(ctx, e.profile); err != nil {
		t.Fatal(err)
	}
	body := page()
	card := srcCard(body)
	for _, want := range []string{"Quelle SNMP v3", "nicht verbunden", "Für SNMP gibt es noch kein Zugangsprofil.", `href="/admin/server-config?tab=snmp"`,
		"Reiter „SNMP“</a> anlegen.", "noch nicht an echten Switches und Druckern erprobt", "<dt>Geräte</dt>"} {
		if !strings.Contains(card, want) {
			t.Errorf("Karte SNMP ohne Profil enthält %q nicht", want)
		}
	}
	for _, bad := range []string{"Fingerabdruck", "<dt>Clients</dt>", "SNMP ist noch nicht verbunden"} {
		if strings.Contains(card, bad) {
			t.Errorf("Karte SNMP ohne Profil enthält %q", bad)
		}
	}
	for _, want := range []string{"SNMP-Geräte", "0 von 200", "Noch kein Gerät in der Liste", "Zuerst ein Zugangsprofil anlegen.", "beide Server eintragen",
		"SNMP liefert keine Vorschläge", "UniFi, TP-Link Omada und per SNMP v3", "gelten für alle Quellen", `name="snmp_enabled"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Seite ohne Profil enthält %q nicht", want)
		}
	}
	if strings.Contains(body, `hx-post="/it/net/snmp/targets"`) {
		t.Error("ohne Profil gibt es kein Formular „Gerät hinzufügen“")
	}
	// ohne system.snmp: kein Link zum Reiter, dafür der Hinweis, wer es einrichtet
	e.h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSNMP && p != permSNMPAdmin }
	body = page()
	if strings.Contains(body, "tab=snmp") || !strings.Contains(srcCard(body), "SNMP-Zugangsprofile verwalten") {
		t.Error("ohne system.snmp: Link zum Reiter sichtbar oder Hinweis fehlt")
	}
	e.h.hasPermHook = func(*http.Request, string) bool { return true }

	// mit Daten
	e.profile = e.mkProfile("Switches")
	okID, _ := e.dev("10.79.1.1", snmpSwitch("sw-web", "02:79:01:00:00:01"))
	badID, bad := e.dev("10.79.1.2", snmpSwitch("sw-zu", "02:79:01:00:00:02"))
	e.set(bad, func(f *fakeSNMPDev) { f.pingErr = snmpErrAuth })
	e.mustRun()
	asset, _ := e.asset(okID)
	if asset == "" {
		t.Fatal("kein Asset aus dem Abgleich")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE it_snmp_targets SET note = 'schon über UniFi bekannt' WHERE id = $1::uuid`, badID); err != nil {
		t.Fatal(err)
	}
	body = page()
	card = srcCard(body)
	if !strings.Contains(card, `<span class="badge b-green">verbunden</span>`) || !strings.Contains(card, "Zugangsprofile in den Server-Einstellungen, Reiter „SNMP“") ||
		strings.Contains(card, "noch kein Zugangsprofil") {
		t.Errorf("Karte SNMP mit Profil:\n%s", card)
	}
	for _, want := range []string{"2 von 200", "10.79.1.1", "10.79.1.2", `<span class="badge b-green">online</span>`, `<span class="badge b-gray">noch kein Asset</span>`,
		"Zugang abgelehnt – Profil prüfen", "schon über UniFi bekannt", `href="/it/` + asset + `"`, "sw-web", "zuletzt geantwortet:",
		`hx-post="/it/net/snmp/targets"`, `hx-post="/it/net/snmp/targets/` + okID + `/check"`, `hx-post="/it/net/snmp/targets/` + okID + `/enabled"`,
		`hx-post="/it/net/snmp/targets/` + okID + `"`, `hx-post="/it/net/snmp/targets/` + okID + `/remove"`,
		"Das IT-Asset bleibt bestehen und wird nicht mehr abgeglichen.", `id="itn-snmp-check-` + okID + `"`, `name="port" value="161"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Seite mit Daten enthält %q nicht", want)
		}
	}
	snmpNoSecrets(t, "Seite", body)

	// it.netsync ohne it.admin: kein „Entfernen“, dafür der Hinweis
	e.h.hasPermHook = func(_ *http.Request, p string) bool { return p != itNetPermAdmin }
	body = page()
	if strings.Contains(body, "/remove") || !strings.Contains(body, "Aus der Liste entfernen darf nur der Bereichsadministrator IT") ||
		!strings.Contains(body, `/check"`) {
		t.Error("ohne it.admin: „Entfernen“ sichtbar oder Hinweis fehlt")
	}
	// nur it.view: Liste ja, keine Formulare und Knöpfe
	e.h.hasPermHook = func(_ *http.Request, p string) bool { return p == permITView }
	body = page()
	if !strings.Contains(body, "10.79.1.1") || strings.Contains(body, "/it/net/snmp/targets") || strings.Contains(body, "<th>Aktion</th>") {
		t.Error("nur it.view: Liste fehlt oder Aktionen sichtbar")
	}
}

// Geräteliste über den ganzen Weg: hinzufügen (mit Klartext-Fehlern), prüfen, aus/ein,
// bearbeiten, entfernen nur mit it.admin – das Asset bleibt.
func TestITNetSNMPTargetsWeb(t *testing.T) {
	e, _ := snmpWebEnv(t)
	ctx := context.Background()
	const base = "/it/net/snmp/targets"
	post := func(fn http.HandlerFunc, path, id string, f url.Values) string {
		t.Helper()
		w := itNetDo(fn, "POST", path, id, "", f)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		snmpNoSecrets(t, path, w.Body.String())
		return w.Body.String()
	}
	count := func() int {
		t.Helper()
		list, err := e.h.snmpTargets(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}

	// hinzufügen: Adresse außer Haus und falscher Port → Klartext, Eingaben bleiben, nichts gespeichert
	body := post(e.h.ITNetSNMPTargetCreateWeb, base, "", url.Values{"host": {"8.8.8.8"}, "port": {"161"}, "profile": {e.profile}})
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `name="host" value="8.8.8.8"`) || !strings.Contains(body, `id="itn-snmp"`) || count() != 0 {
		t.Errorf("Adresse außer Haus:\n%s", body)
	}
	body = post(e.h.ITNetSNMPTargetCreateWeb, base, "", url.Values{"host": {"10.79.2.1"}, "port": {"99999"}, "profile": {e.profile}})
	if !strings.Contains(body, "Port: bitte eine Zahl von 1 bis 65535") || count() != 0 {
		t.Errorf("falscher Port:\n%s", body)
	}
	// Port leer = 161
	body = post(e.h.ITNetSNMPTargetCreateWeb, base, "", url.Values{"host": {" 10.79.2.1 "}, "port": {""}, "profile": {e.profile}})
	if !strings.Contains(body, "Gerät 10.79.2.1 hinzugefügt") || count() != 1 {
		t.Fatalf("hinzufügen:\n%s", body)
	}
	list, _ := e.h.snmpTargets(ctx)
	id := list[0].ID
	if list[0].Port != 161 || list[0].Host != "10.79.2.1" || !list[0].Enabled {
		t.Errorf("gespeichert: %+v", list[0])
	}
	body = post(e.h.ITNetSNMPTargetCreateWeb, base, "", url.Values{"host": {"10.79.2.1"}, "port": {"161"}, "profile": {e.profile}})
	if !strings.Contains(body, "steht schon in der Liste") || count() != 1 {
		t.Errorf("doppelt:\n%s", body)
	}

	// prüfen: keine Antwort → Klartext; Antwort → Angaben; Programmfehler → neutral, nichts Internes
	chk := func() string { return post(e.h.ITNetSNMPTargetCheckWeb, base+"/"+id+"/check", id, nil) }
	if body = chk(); !strings.Contains(body, "Keine Antwort") || !strings.Contains(body, `role="alert"`) || strings.Contains(body, `id="itn-snmp"`) {
		t.Errorf("prüfen ohne Antwort:\n%s", body)
	}
	fd := &fakeSNMPDev{detail: snmpSwitch("sw-pruef", "02:79:02:00:00:01")}
	e.net.mu.Lock()
	e.net.devs["10.79.2.1"] = fd
	e.net.mu.Unlock()
	if body = chk(); !strings.Contains(body, "Das Gerät antwortet") || !strings.Contains(body, "sw-pruef") || !strings.Contains(body, "XS-24") ||
		!strings.Contains(body, "SN-sw-pruef") || !strings.Contains(body, "2 (1 aktiv)") {
		t.Errorf("prüfen mit Antwort:\n%s", body)
	}
	if tg := e.target(id); tg.LastOK != nil || tg.AssetID != "" {
		t.Errorf("prüfen hat etwas gespeichert: %+v", tg)
	}
	e.set(fd, func(f *fakeSNMPDev) { f.detailErr = snmpErrAuth })
	if body = chk(); !strings.Contains(body, "Zugang abgelehnt – Profil prüfen") {
		t.Errorf("prüfen abgelehnt:\n%s", body)
	}
	e.set(fd, func(f *fakeSNMPDev) { f.detailErr = errors.New("pq: interner fehler in tabelle xyz") })
	if body = chk(); !strings.Contains(body, snmpNeutralErr) || strings.Contains(body, "xyz") || strings.Contains(body, "pq:") {
		t.Errorf("unbekannter Fehler muss neutral gemeldet werden:\n%s", body)
	}
	e.set(fd, func(f *fakeSNMPDev) { f.detailErr = nil })
	// unbekanntes Gerät: 200 mit Klartext; kaputte ID: 404
	if body = post(e.h.ITNetSNMPTargetCheckWeb, base+"/"+snmpWebID+"/check", snmpWebID, nil); !strings.Contains(body, "Eintrag nicht gefunden") {
		t.Errorf("unbekanntes Gerät:\n%s", body)
	}
	if w := itNetDo(e.h.ITNetSNMPTargetCheckWeb, "POST", base+"/x/check", "x", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("kaputte ID: %d", w.Code)
	}

	// Asset entsteht beim Abgleich; aus- und einschalten
	e.mustRun()
	asset, state := e.asset(id)
	if asset == "" || state != "online" {
		t.Fatalf("Asset: %q %q", asset, state)
	}
	body = post(e.h.ITNetSNMPTargetEnabledWeb, base+"/"+id+"/enabled", id, url.Values{"on": {"0"}})
	if e.target(id).Enabled || !strings.Contains(body, "Abfrage ausgeschaltet") || !strings.Contains(body, `<span class="badge b-gray">aus</span>`) ||
		!strings.Contains(body, "Einschalten") {
		t.Errorf("ausschalten:\n%s", body)
	}
	body = post(e.h.ITNetSNMPTargetEnabledWeb, base+"/"+id+"/enabled", id, url.Values{"on": {"1"}})
	if !e.target(id).Enabled || !strings.Contains(body, "Abfrage eingeschaltet") {
		t.Errorf("einschalten:\n%s", body)
	}

	// bearbeiten: Fehler lässt das Formular offen; Erfolg ändert Adresse, Port, Profil – Asset bleibt
	body = post(e.h.ITNetSNMPTargetUpdateWeb, base+"/"+id, id, url.Values{"host": {"8.8.4.4"}, "port": {"161"}, "profile": {e.profile}})
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `<details class="itn-snmp-edit" open>`) || e.target(id).Host != "10.79.2.1" {
		t.Errorf("bearbeiten mit Fehler:\n%s", body)
	}
	p2 := e.mkProfile("Drucker")
	body = post(e.h.ITNetSNMPTargetUpdateWeb, base+"/"+id, id, url.Values{"host": {"10.79.2.7"}, "port": {"1161"}, "profile": {p2}})
	if tg := e.target(id); tg.Host != "10.79.2.7" || tg.Port != 1161 || tg.ProfileID != p2 || tg.AssetID != asset || !strings.Contains(body, "gespeichert") {
		t.Errorf("bearbeiten: %+v\n%s", tg, body)
	}

	// entfernen: ohne it.admin 403 und nichts weg; mit it.admin weg, Asset bleibt getrennt
	e.h.hasPermHook = func(_ *http.Request, p string) bool { return p != itNetPermAdmin }
	if w := itNetDo(e.h.ITNetSNMPTargetRemoveWeb, "POST", base+"/"+id+"/remove", id, "", nil); w.Code != http.StatusForbidden || count() != 1 {
		t.Errorf("entfernen ohne it.admin: %d, %d Geräte", w.Code, count())
	}
	// ohne it.netsync: alles andere 403, nichts geändert
	e.h.hasPermHook = func(_ *http.Request, p string) bool { return p != permITNetsync }
	for path, fn := range map[string]http.HandlerFunc{
		base: e.h.ITNetSNMPTargetCreateWeb, base + "/" + id: e.h.ITNetSNMPTargetUpdateWeb,
		base + "/" + id + "/enabled": e.h.ITNetSNMPTargetEnabledWeb, base + "/" + id + "/check": e.h.ITNetSNMPTargetCheckWeb,
	} {
		if w := itNetDo(fn, "POST", path, id, "", url.Values{"host": {"10.79.2.8"}, "port": {"161"}, "profile": {e.profile}, "on": {"0"}}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne it.netsync: %d", path, w.Code)
		}
	}
	if tg := e.target(id); count() != 1 || !tg.Enabled || tg.Host != "10.79.2.7" {
		t.Errorf("ohne it.netsync wurde etwas geändert: %+v", tg)
	}
	e.h.hasPermHook = func(*http.Request, string) bool { return true }
	body = post(e.h.ITNetSNMPTargetRemoveWeb, base+"/"+id+"/remove", id, nil)
	if count() != 0 || !strings.Contains(body, "Das IT-Asset bleibt bestehen und wird nicht mehr abgeglichen.") || !strings.Contains(body, "Noch kein Gerät in der Liste") {
		t.Errorf("entfernen:\n%s", body)
	}
	var detached, monitored bool
	if err := e.pool.QueryRow(ctx, `SELECT net_detached, net_monitored FROM it_assets WHERE id = $1::uuid`, asset).Scan(&detached, &monitored); err != nil || !detached || monitored {
		t.Errorf("Asset nach dem Entfernen: getrennt %v, überwacht %v, %v", detached, monitored, err)
	}
}

// /it/{id} und /it: Quelle „SNMP“, Name des Anschlusses in der Tabelle, keine leere
// PoE-Spalte, kein „Update“.
func TestITDetailSNMPCard(t *testing.T) {
	e, _ := snmpWebEnv(t)
	id, _ := e.dev("10.79.3.1", snmpSwitch("sw-karte", "02:79:03:00:00:01"))
	e.mustRun()
	asset, _ := e.asset(id)
	if asset == "" {
		t.Fatal("kein Asset")
	}
	ctx := context.Background()
	a := &it.Asset{ID: asset, Name: "sw-karte", Type: "network", NetSource: itNetSourceSNMP, NetRole: "device", NetState: "online", Firmware: "1.2.3", NetMonitored: true}
	render := func() string {
		t.Helper()
		// snmpWebEnv steht im Hauptordner des Projekts (wie der laufende Server)
		c, err := e.h.tmpl.Clone()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ParseFiles(filepath.Join("web", "templates", "it_detail.gohtml")); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := c.ExecuteTemplate(&buf, "base.gohtml", ITDetailData{Asset: ITAssetDetailView{ID: asset, Name: a.Name}, CanNetEdit: true, Net: e.h.itNetDetail(ctx, a, nil)}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	body := render()
	for _, want := range []string{"SNMP · Netzgerät", "Gi0/1", "Gi0/2", "1 Gbit/s", "<th>Anschluss</th>", "1.2.3"} {
		if !strings.Contains(body, want) {
			t.Errorf("Asset-Seite enthält %q nicht", want)
		}
	}
	for _, bad := range []string{"<th>PoE</th>", "Firmware-Update verfügbar"} {
		if strings.Contains(body, bad) {
			t.Errorf("Asset-Seite enthält %q", bad)
		}
	}
	if itNetSourceName(itNetSourceSNMP) != "SNMP" {
		t.Error("Anzeigename der Quelle")
	}
	// UniFi-Anschlüsse mit PoE behalten die Spalte
	if _, err := e.pool.Exec(ctx, `UPDATE it_asset_ports SET poe_state = 'UP' WHERE asset_id = $1::uuid AND idx = 1`, asset); err != nil {
		t.Fatal(err)
	}
	if body = render(); !strings.Contains(body, "<th>PoE</th>") {
		t.Error("mit PoE-Angabe muss die Spalte bleiben")
	}
}

// ── Migration 168 ────────────────────────────────────────────

func TestMigration168Text(t *testing.T) {
	b, err := os.ReadFile("../../migrations/168_snmp_admin.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"'system.snmp_admin'", "'System & Sicherheit'", "o.key = 'system.snmp'", "o.key = 'system.certs_admin'",
		"rp.department_id IS NULL", "NOT up.granted", "ON CONFLICT (key) DO NOTHING",
		"ON CONFLICT (role_id, permission_id) DO NOTHING", "ON CONFLICT (user_id, permission_id) DO NOTHING"} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 168 enthält %q nicht", want)
		}
	}
	if up := strings.ToUpper(s); strings.Contains(up, "DELETE ") || strings.Contains(up, "DROP ") || strings.Contains(up, "DO UPDATE") {
		t.Error("Migration 168 darf nichts löschen und kein vorhandenes Recht ändern")
	}
}

// Migration 168 an der Datenbank: system.snmp_admin bekommt nur, wer system.snmp und
// system.certs_admin überall hat; ein einzeln entzogenes Recht bleibt entzogen; wiederholbar.
func TestMigration168Grants(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../migrations/168_snmp_admin.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var cat string
	if err := pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'system.snmp_admin'`).Scan(&cat); err != nil || cat != "System & Sicherheit" {
		t.Fatalf("Recht system.snmp_admin: %q %v", cat, err)
	}
	stamp := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mkRole := func(suffix string, keys ...string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'SNMP 168', 40) RETURNING id`, "sn168_"+suffix+stamp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, id) })
		exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT $1, id FROM permissions WHERE key = ANY($2)`, id, keys)
		return id
	}
	mkUser := func(suffix string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name)
			VALUES ($1::text, $1::text || '@example.invalid', 'x', 'SNMP', '168') RETURNING id`, "sn168-"+suffix+stamp).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
		return id
	}
	var dept string
	if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ($1) RETURNING id`, "SNMP168 "+stamp).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1`, dept) })

	rBoth := mkRole("b", "system.snmp", "system.certs_admin")
	rSNMP := mkRole("s", "system.snmp", "system.unifi")
	rCerts := mkRole("c", "system.certs_admin")
	rDept := mkRole("d", "system.certs_admin")
	exec(`INSERT INTO role_permissions (role_id, permission_id, department_id) SELECT $1, id, $2 FROM permissions WHERE key = 'system.snmp'`, rDept, dept)
	uBoth, uRevoked, uOne := mkUser("b"), mkUser("r"), mkUser("o")
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key IN ('system.snmp', 'system.certs_admin')`, uBoth)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, false FROM permissions WHERE key = 'system.snmp'`, uRevoked)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key = 'system.certs_admin'`, uRevoked)
	exec(`INSERT INTO user_permissions (user_id, permission_id, granted) SELECT $1, id, true FROM permissions WHERE key = 'system.snmp'`, uOne)

	exec(sql)

	roleHas := func(role string) (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id = $1 AND p.key = 'system.snmp_admin' AND rp.department_id IS NULL`, role).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if roleHas(rBoth) != 1 {
		t.Error("Rolle mit system.snmp und system.certs_admin bekommt system.snmp_admin")
	}
	for name, r := range map[string]string{"nur system.snmp": rSNMP, "nur system.certs_admin": rCerts, "system.snmp nur in einer Abteilung": rDept} {
		if roleHas(r) != 0 {
			t.Errorf("Rolle mit %s hat system.snmp_admin bekommen", name)
		}
	}
	userHas := func(user string) (n int, granted bool) {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT up.granted FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
			WHERE up.user_id = $1 AND p.key = 'system.snmp_admin'`, user)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			n++
			if err := rows.Scan(&granted); err != nil {
				t.Fatal(err)
			}
		}
		return n, granted
	}
	if n, g := userHas(uBoth); n != 1 || !g {
		t.Errorf("Benutzer mit beiden Einzelrechten: %d %v", n, g)
	}
	if n, g := userHas(uRevoked); n != 1 || g {
		t.Errorf("entzogenes system.snmp muss system.snmp_admin entziehen: %d %v", n, g)
	}
	if n, _ := userHas(uOne); n != 0 {
		t.Error("Benutzer mit nur einem Einzelrecht hat system.snmp_admin bekommen")
	}

	// Über den echten Rechtedienst (ohne Test-Haken): Löschen nur mit system.snmp_admin.
	{
		rb := rbac.NewService(rbac.NewRepository(pool))
		if err := rb.Warm(ctx); err != nil {
			t.Fatal(err)
		}
		h := &Handler{rbac: rb}
		as := func(uid string) *http.Request {
			r := httptest.NewRequest("POST", "/admin/server-config/snmp/profiles/"+snmpWebID+"/delete", nil)
			return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: uid, Role: "viewer"}))
		}
		if !h.snmpMayDelete(as(uBoth)) || !h.snmpCanManage(as(uBoth)) {
			t.Error("Bereichsadministrator darf Profile löschen und verwalten")
		}
		if h.snmpMayDelete(as(uOne)) || !h.snmpCanManage(as(uOne)) {
			t.Error("system.snmp allein: verwalten ja, löschen nein")
		}
		if h.snmpMayDelete(as(uRevoked)) || h.snmpCanManage(as(uRevoked)) {
			t.Error("entzogenes Recht: weder verwalten noch löschen")
		}
		if w := itNetDo(h.SNMPProfileDeleteWeb, "POST", "/admin/server-config/snmp/profiles/"+snmpWebID+"/delete", snmpWebID, uOne, nil); w.Code != http.StatusForbidden {
			t.Errorf("Profil löschen mit system.snmp allein (echter Rechtedienst): %d", w.Code)
		}
	}

	// wiederholbar – ein später entzogenes Recht bleibt entzogen
	exec(`UPDATE user_permissions SET granted = false WHERE user_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.snmp_admin')`, uBoth)
	exec(`DELETE FROM role_permissions WHERE role_id = $1 AND permission_id = (SELECT id FROM permissions WHERE key = 'system.snmp_admin')`, rBoth)
	exec(sql)
	exec(sql)
	if n, g := userHas(uBoth); n != 1 || g {
		t.Errorf("erneuter Lauf hat das entzogene Einzelrecht zurückgegeben: %d %v", n, g)
	}
	if roleHas(rBoth) != 1 {
		t.Error("erneuter Lauf: Rolle")
	}
	var perms int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key = 'system.snmp_admin'`).Scan(&perms); err != nil || perms != 1 {
		t.Errorf("Recht doppelt oder weg: %d %v", perms, err)
	}
	// Der Rechtedienst kennt das Recht (Matrix unter Rollen & Rechte, Handbuch).
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	if !rb.HasPermissionForUser(uBoth, "viewer", "system.snmp") || rb.HasPermissionForUser(uBoth, "viewer", permSNMPAdmin) {
		t.Error("Einzelrechte greifen nicht wie erwartet")
	}
}
