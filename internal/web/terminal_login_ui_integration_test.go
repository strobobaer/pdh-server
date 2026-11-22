//go:build integration

// Oberfläche: Anmeldeart am Terminal und Terminal-PIN in der Benutzerverwaltung.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalLoginUI ./internal/web/
package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

var uuidInHTML = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func TestTerminalLoginUI(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("uimgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("uiplain", "viewer", false)
	mgr2 := e.user("uimgr2", "viewer", false) // Benutzer verwalten, aber kein Rang über dem Ziel
	e.grant(mgr2, "system.manage_users", "", true)
	term := e.user("uiterm", "viewer", true)
	norm := e.user("uinorm", "viewer", false)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Get("/users/{id}", h.UserDetailPage)
	r.Get("/account", h.AccountPage)
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	r.Post("/users/{id}/terminal-pin", h.UserTerminalPINWeb)
	r.Post("/account/terminal-pin", h.AccountTerminalPINWeb)
	// Die Seiten laden ihre Vorlage relativ zum Arbeitsverzeichnis (Repo-Wurzel).
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	do := func(method, target, body, uid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		role := "viewer"
		if uid == mgr {
			role = "admin" // PIN setzen verlangt „Benutzer verwalten“ und Rang über dem Ziel
		}
		req = withUser(req, &users.User{ID: uid, Role: users.Role(role)})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	loginOf := func() terminalLoginSettings {
		s, err := h.terminalLoginSettings(e.ctx, term)
		e.must(err)
		return s
	}

	// Speichern der Anmeldeart: ohne Recht bzw. bei Nicht-Systembenutzern unverändert
	do("POST", "/users/"+term+"/master", "terminal_manual_login=none", plain)
	if s := loginOf(); s.ManualLogin != loginCardPIN || !s.PasswordButton {
		t.Errorf("ohne Recht gespeichert: %+v", s)
	}
	do("POST", "/users/"+norm+"/master", "terminal_manual_login=card_only", mgr)
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM terminal_settings WHERE user_id = $1::uuid`, norm).Scan(&n))
	if n != 0 {
		t.Errorf("Nicht-Systembenutzer: %d Zeilen", n)
	}
	// Mit Recht: Anmeldeart und Knopf (Checkbox fehlt = aus)
	rec := do("POST", "/users/"+term+"/master", "terminal_manual_login=card_only", mgr)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("speichern: %s", loc)
	}
	if s := loginOf(); s.ManualLogin != loginCardOnly || s.PasswordButton {
		t.Errorf("gespeichert: %+v", s)
	}
	do("POST", "/users/"+term+"/master", "terminal_manual_login=password&terminal_password_button=on", mgr)
	if s := loginOf(); s.ManualLogin != loginPassword || !s.PasswordButton {
		t.Errorf("gespeichert 2: %+v", s)
	}
	if n := mustInt(e, `SELECT COUNT(*) FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND field_name = 'Anmeldung ohne Kartenleser' AND message = 'Anmeldung ohne Kartenleser: Nur Kartennummer'`, term); n != 1 {
		t.Errorf("Verlauf Anmeldeart: %d", n)
	}
	// Ältere Formulare ohne die Felder: Werte bleiben
	do("POST", "/users/"+term+"/master", "work_location=Halle", mgr)
	if s := loginOf(); s.ManualLogin != loginPassword || !s.PasswordButton {
		t.Errorf("ohne Felder verändert: %+v", s)
	}
	// Unbekannte Art: Fehler, nichts gespeichert
	rec = do("POST", "/users/"+term+"/master", "terminal_manual_login=quatsch", mgr)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("unbekannte Art ohne Fehler: %s", loc)
	}
	if s := loginOf(); s.ManualLogin != loginPassword {
		t.Errorf("unbekannte Art gespeichert: %+v", s)
	}

	// Warntext bei „Nur Kartennummer“
	page := func(target, uid string) string {
		return do("GET", target, "", uid).Body.String()
	}
	const warn = "Wer eine Kartennummer kennt, kann sich als diese Person anmelden"
	e.must(h.saveTerminalLoginSettings(e.ctx, term, terminalLoginSettings{ManualLogin: loginCardOnly, PasswordButton: true}))
	if b := page("/users/"+term+"?tab=master", mgr); !strings.Contains(b, warn) || !strings.Contains(b, `name="terminal_manual_login"`) {
		t.Errorf("Warntext/Auswahl fehlt (%d Zeichen)", len(b))
	}
	e.must(h.saveTerminalLoginSettings(e.ctx, term, terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: true}))
	if b := page("/users/"+term+"?tab=master", mgr); !strings.Contains(b, `id="ud-card-only-warn" class="rec-msg err" style="margin-top:6px;display:none"`) {
		t.Error("Warntext nicht verborgen, obwohl nicht card_only")
	}

	// PIN über die Verwaltung
	pinPost := func(body, uid string) *httptest.ResponseRecorder {
		return do("POST", "/users/"+norm+"/terminal-pin", body, uid)
	}
	if rec := pinPost("action=set&pin=2580", plain); rec.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", rec.Code)
	}
	if has, _, _ := h.terminalPINState(e.ctx, norm, time.Now()); has {
		t.Error("PIN ohne Recht gesetzt")
	}
	rec = pinPost("action=set&pin=2580", mgr)
	if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("PIN setzen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Header().Get("Location"), "2580") {
		t.Error("PIN im Redirect")
	}
	if has, _, _ := h.terminalPINState(e.ctx, norm, time.Now()); !has {
		t.Error("PIN nicht gesetzt")
	}
	// ungültige PIN: Fehler, alte PIN bleibt
	rec = pinPost("action=set&pin=1234", mgr)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=") || strings.Contains(loc, "1234") {
		t.Errorf("ungültige PIN: %s", loc)
	}
	if ok, err := h.checkTerminalPIN(e.ctx, norm, "2580", time.Now()); err != nil || !ok {
		t.Errorf("alte PIN nicht mehr gültig: %v %v", ok, err)
	}
	// Seite: PIN nie im HTML, Sperre sichtbar
	// UUIDs (z. B. in Benutzerlisten der geteilten Test-DB) können die Ziffernfolge zufällig enthalten.
	b := uuidInHTML.ReplaceAllString(page("/users/"+norm+"?tab=master", mgr), "")
	if strings.Contains(b, "2580") || !strings.Contains(b, `name="pin"`) {
		t.Error("PIN im HTML bzw. PIN-Formular fehlt")
	}
	until := time.Now().Add(10 * time.Minute)
	e.exec(`UPDATE users SET terminal_pin_locked_until = $2 WHERE id = $1::uuid`, norm, until)
	want := "PIN gesperrt bis " + until.Local().Format("15:04")
	if b := page("/users/"+norm+"?tab=master", mgr); !strings.Contains(b, want) {
		t.Errorf("Sperre fehlt: %q", want)
	}
	// M10: „Benutzer verwalten“ ohne Rang über dem Ziel -> 403
	if rec := pinPost("action=set&pin=2580", mgr2); rec.Code != http.StatusForbidden {
		t.Errorf("ohne Rang: %d", rec.Code)
	}
	// I3: Verlauf ohne PIN-Wert
	hist := func(ref, msg string) int {
		return mustInt(e, `SELECT COUNT(*) FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND message = $2 AND created_by = $3::uuid`, ref, msg, mgr)
	}
	if hist(norm, "Terminal-PIN gesetzt") != 1 {
		t.Error("Verlaufseintrag „Terminal-PIN gesetzt“ fehlt")
	}
	if n := mustInt(e, `SELECT COUNT(*) FROM record_history WHERE ref_id = $1::uuid AND (message LIKE '%2580%' OR old_value LIKE '%2580%' OR new_value LIKE '%2580%')`, norm); n != 0 {
		t.Error("PIN im Verlauf")
	}
	// M9: Systembenutzer haben keine Terminal-PIN
	rec = do("POST", "/users/"+term+"/terminal-pin", "action=set&pin=2580", mgr)
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("Systembenutzer-PIN ohne Fehler: %s", rec.Header().Get("Location"))
	}
	if has, _, _ := h.terminalPINState(e.ctx, term, time.Now()); has {
		t.Error("PIN für Systembenutzer gesetzt")
	}
	// Entfernen hebt PIN und Sperre auf
	pinPost("action=clear", mgr)
	if hist(norm, "Terminal-PIN entfernt") != 1 {
		t.Error("Verlaufseintrag „Terminal-PIN entfernt“ fehlt")
	}
	if has, locked, _ := h.terminalPINState(e.ctx, norm, time.Now()); has || locked != nil {
		t.Errorf("entfernen: %v %v", has, locked)
	}
	if b := page("/users/"+norm+"?tab=master", mgr); strings.Contains(b, "PIN gesperrt bis") {
		t.Error("Sperre nach Entfernen sichtbar")
	}

	// Eigenes Profil
	own := func(body string) *httptest.ResponseRecorder { return do("POST", "/account/terminal-pin", body, plain) }
	own("action=set&pin=7391&id=" + norm)
	if has, _, _ := h.terminalPINState(e.ctx, plain, time.Now()); !has {
		t.Error("eigene PIN nicht gesetzt")
	}
	if has, _, _ := h.terminalPINState(e.ctx, norm, time.Now()); has {
		t.Error("fremde PIN über /account gesetzt")
	}
	rec = own("action=set&pin=0000")
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "notice=") || strings.Contains(loc, "0000") {
		t.Errorf("eigene ungültige PIN: %s", loc)
	}
	if b := page("/account", plain); strings.Contains(b, "7391") || !strings.Contains(b, `action="/account/terminal-pin"`) {
		t.Error("Konto-Seite: PIN im HTML bzw. Formular fehlt")
	}
	own("action=clear")
	if n := mustInt(e, `SELECT COUNT(*) FROM record_history WHERE ref_type = 'user' AND ref_id = $1::uuid AND message LIKE 'Terminal-PIN%' AND created_by = $1::uuid`, plain); n != 2 {
		t.Errorf("eigene PIN im Verlauf: %d Einträge", n)
	}
	if has, _, _ := h.terminalPINState(e.ctx, plain, time.Now()); has {
		t.Error("eigene PIN nicht entfernt")
	}
}
