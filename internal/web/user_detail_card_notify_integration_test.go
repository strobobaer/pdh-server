//go:build integration

// Terminal-Einstellung card_notify: Speichern in der Benutzerverwaltung, Ereignis „settings“, Formular.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run CardNotify ./internal/web/
package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestTerminalCardNotify(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("cnmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("cnplain", "viewer", false)
	term := e.user("cnterm", "viewer", true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Get("/users/{id}", h.UserDetailPage)
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	do := func(method, target, body, uid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		role := "viewer"
		if uid == mgr {
			role = "admin"
		}
		req = withUser(req, &users.User{ID: uid, Role: users.Role(role)})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	// effective: die Liste, die das Ereignis „settings“ trägt.
	effective := func() []string {
		ev, err := h.terminalSettingsEvent(httptest.NewRequest("GET", "/terminal/events", nil), term)
		e.must(err)
		var d struct {
			CardNotify []string `json:"card_notify"`
		}
		for _, line := range strings.Split(ev, "\n") {
			if strings.HasPrefix(line, "data: ") {
				e.must(json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d))
			}
		}
		return d.CardNotify
	}
	stored := func() []string {
		var s []string
		e.must(e.pool.QueryRow(e.ctx, `SELECT COALESCE((SELECT card_notify FROM terminal_settings WHERE user_id = $1::uuid), '{}')`, term).Scan(&s))
		return s
	}
	const sent = "terminal_card_notify_sent=1"

	// Ohne Eintrag: Vorgabe overlay
	if got := effective(); !reflect.DeepEqual(got, []string{"overlay"}) {
		t.Errorf("ohne Eintrag: %v", got)
	}
	// banner + toast -> feste Reihenfolge
	rec := do("POST", "/users/"+term+"/master", sent+"&terminal_card_notify=banner&terminal_card_notify=toast", mgr)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("speichern: %s", loc)
	}
	if got := effective(); !reflect.DeepEqual(got, []string{"toast", "banner"}) {
		t.Errorf("banner+toast: %v", got)
	}
	// off allein
	do("POST", "/users/"+term+"/master", sent+"&terminal_card_notify=off", mgr)
	if got := effective(); !reflect.DeepEqual(got, []string{"off"}) {
		t.Errorf("off: %v", got)
	}
	// ältere Formulare ohne Marker: unverändert
	do("POST", "/users/"+term+"/master", "work_location=Halle", mgr)
	if got := effective(); !reflect.DeepEqual(got, []string{"off"}) {
		t.Errorf("ohne Feld verändert: %v", got)
	}
	// Anmeldeart speichern lässt card_notify unberührt
	do("POST", "/users/"+term+"/master", "terminal_manual_login=password", mgr)
	if got := effective(); !reflect.DeepEqual(got, []string{"off"}) {
		t.Errorf("Anmeldeart überschreibt card_notify: %v", got)
	}
	// off zusammen mit toast und unbekannter Wert: Fehler, nichts gespeichert
	for _, body := range []string{sent + "&terminal_card_notify=off&terminal_card_notify=toast", sent + "&terminal_card_notify=quatsch"} {
		rec = do("POST", "/users/"+term+"/master", body, mgr)
		if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
			t.Errorf("%s ohne Fehler: %s", body, loc)
		}
		if got := effective(); !reflect.DeepEqual(got, []string{"off"}) {
			t.Errorf("%s gespeichert: %v", body, got)
		}
	}
	// Ohne Recht: unverändert
	do("POST", "/users/"+term+"/master", sent+"&terminal_card_notify=front", plain)
	if got := effective(); !reflect.DeepEqual(got, []string{"off"}) {
		t.Errorf("ohne Recht gespeichert: %v", got)
	}
	// Nichts angehakt = Vorgabe: gespeichert wird die leere Liste
	do("POST", "/users/"+term+"/master", sent, mgr)
	if got := stored(); len(got) != 0 {
		t.Errorf("gespeichert: %v", got)
	}
	if got := effective(); !reflect.DeepEqual(got, []string{"overlay"}) {
		t.Errorf("nichts angehakt: %v", got)
	}
	// Die Datenbank lehnt off + toast selbst ab
	if _, err := e.pool.Exec(e.ctx, `UPDATE terminal_settings SET card_notify = '{off,toast}' WHERE user_id = $1::uuid`, term); err == nil {
		t.Error("Datenbank nimmt off+toast an")
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE terminal_settings SET card_notify = '{quatsch}' WHERE user_id = $1::uuid`, term); err == nil {
		t.Error("Datenbank nimmt unbekannten Wert an")
	}

	// Formular
	do("POST", "/users/"+term+"/master", sent+"&terminal_card_notify=banner", mgr)
	b := do("GET", "/users/"+term+"?tab=master", "", mgr).Body.String()
	for _, want := range []string{`name="terminal_card_notify" value="banner" checked`, `name="terminal_card_notify" value="toast"`, `name="terminal_card_notify" value="off"`,
		"Windows-Mitteilung", "Fenster in der Mitte", "Leiste oben", "Terminal nach vorn", "Keine Benachrichtigung",
		"Wirkt nur an Windows-Terminals in der Betriebsart Zusatz.", "(Vorgabe: Fenster in der Mitte)"} {
		if !strings.Contains(b, want) {
			t.Errorf("Formular ohne %q", want)
		}
	}
}
