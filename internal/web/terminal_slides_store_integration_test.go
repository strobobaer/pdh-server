//go:build integration

// Folienspeicher des Infobildschirms und Schalter „Globales Terminal“.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TerminalSlides|TerminalGlobalScreen' ./internal/web/
package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func slideTitles(t *testing.T, e *atEnv, h *Handler, activeOnly bool) []string {
	t.Helper()
	list, err := h.listTerminalSlides(e.ctx, activeOnly)
	e.must(err)
	var out []string
	for _, s := range list {
		out = append(out, s.Title)
	}
	return out
}

func sameStrings(a, b []string) bool {
	return strings.Join(a, "|") == strings.Join(b, "|")
}

func TestTerminalSlidesStore(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	e.exec(`DELETE FROM terminal_slides`)
	t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_slides`) })

	// Speichern: neue Folien kommen ans Ende.
	for _, title := range []string{"A", "B", "C"} {
		s := terminalSlide{Kind: "text", Title: title, Body: "Text " + title, Active: true}
		e.must(h.saveTerminalSlide(e.ctx, &s))
		if s.ID == "" || s.DurationSeconds != 15 {
			t.Fatalf("nach Speichern: %+v", s)
		}
	}
	if got := slideTitles(t, e, h, false); !sameStrings(got, []string{"A", "B", "C"}) {
		t.Fatalf("Reihenfolge: %v", got)
	}
	list, _ := h.listTerminalSlides(e.ctx, false)
	a, b, c := list[0], list[1], list[2]

	ver := func() string {
		v, err := h.slidesVersion(e.ctx)
		e.must(err)
		return v
	}
	v0 := ver()
	e.must(h.moveTerminalSlide(e.ctx, a.ID, -1)) // erste nach oben: nichts
	e.must(h.moveTerminalSlide(e.ctx, c.ID, 1))  // letzte nach unten: nichts
	if got := slideTitles(t, e, h, false); !sameStrings(got, []string{"A", "B", "C"}) {
		t.Fatalf("Ränder haben etwas geändert: %v", got)
	}
	if ver() != v0 {
		t.Error("Version ändert sich ohne Änderung")
	}
	e.must(h.moveTerminalSlide(e.ctx, c.ID, -1))
	if got := slideTitles(t, e, h, false); !sameStrings(got, []string{"A", "C", "B"}) {
		t.Fatalf("hoch: %v", got)
	}
	v1 := ver()
	if v1 == v0 {
		t.Error("Version nach Verschieben gleich")
	}
	e.must(h.moveTerminalSlide(e.ctx, a.ID, 1))
	if got := slideTitles(t, e, h, false); !sameStrings(got, []string{"C", "A", "B"}) {
		t.Fatalf("runter: %v", got)
	}
	if ver() == v1 {
		t.Error("Version nach zweitem Verschieben gleich")
	}

	// Bearbeiten ändert die Version; Deaktivieren nimmt die Folie aus der aktiven Liste.
	v2 := ver()
	g, err := h.getTerminalSlide(e.ctx, b.ID)
	e.must(err)
	g.Title, g.Active = "B2", false
	e.must(h.saveTerminalSlide(e.ctx, &g))
	if ver() == v2 {
		t.Error("Version nach Bearbeiten gleich")
	}
	if got := slideTitles(t, e, h, true); !sameStrings(got, []string{"C", "A"}) {
		t.Fatalf("aktive: %v", got)
	}
	if got := slideTitles(t, e, h, false); !sameStrings(got, []string{"C", "A", "B2"}) {
		t.Fatalf("alle: %v", got)
	}

	// Abgelehnte Eingaben speichern nichts.
	rejects := map[string]terminalSlide{
		"dauer 4":         {Kind: "text", Body: "x", DurationSeconds: 4, Active: true},
		"dauer 601":       {Kind: "text", Body: "x", DurationSeconds: 601, Active: true},
		"text 2001":       {Kind: "text", Body: strings.Repeat("x", 2001), Active: true},
		"bild 5MB+1":      {Kind: "image", Image: pngBytes(5*1024*1024 - 8 + 1), Active: true},
		"png-name":        {Kind: "image", ImageType: "image/png", Image: []byte("nur Text"), Active: true},
		"neues ohne bild": {Kind: "image", Active: true},
	}
	for name, s := range rejects {
		s := s
		if err := h.saveTerminalSlide(e.ctx, &s); err == nil {
			t.Errorf("%s: kein Fehler", name)
		}
	}
	if n := mustInt(e, `SELECT count(*) FROM terminal_slides`); n != 3 {
		t.Fatalf("Anzahl nach Ablehnungen: %d", n)
	}

	// Bild speichern und lesen; Liste ohne Bilddaten; Bearbeiten ohne neues Bild behält es.
	img := terminalSlide{Kind: "image", Title: "Bild", Image: pngBytes(64), Active: true}
	e.must(h.saveTerminalSlide(e.ctx, &img))
	data, typ, err := h.terminalSlideImage(e.ctx, img.ID)
	e.must(err)
	if typ != "image/png" || len(data) != 64+8 {
		t.Fatalf("Bild: %s %d", typ, len(data))
	}
	list, _ = h.listTerminalSlides(e.ctx, false)
	for _, s := range list {
		if len(s.Image) != 0 {
			t.Fatal("Liste enthält Bilddaten")
		}
	}
	g, _ = h.getTerminalSlide(e.ctx, img.ID)
	if g.ImageType != "image/png" {
		t.Fatalf("Bildart in get: %q", g.ImageType)
	}
	g.Title = "Bild 2"
	e.must(h.saveTerminalSlide(e.ctx, &g))
	if data, _, _ = h.terminalSlideImage(e.ctx, img.ID); len(data) != 72 {
		t.Fatalf("Bild nach Bearbeiten: %d", len(data))
	}
	if _, _, err := h.terminalSlideImage(e.ctx, c.ID); err == nil {
		t.Error("Textfolie liefert ein Bild")
	}

	// Vorgänge-Folie mit Anlage.
	inf := e.infra("slide-anlage", "", "")
	vg := terminalSlide{Kind: "vorgaenge", InfraIDs: []string{inf}, ShowFaults: true, Active: true}
	e.must(h.saveTerminalSlide(e.ctx, &vg))
	if got, _ := h.getTerminalSlide(e.ctx, vg.ID); len(got.InfraIDs) != 1 || got.InfraIDs[0] != inf || !got.ShowFaults || got.ShowTickets {
		t.Fatalf("Vorgänge: %+v", got)
	}

	// Löschen.
	before := ver()
	e.must(h.deleteTerminalSlide(e.ctx, a.ID))
	if got := slideTitles(t, e, h, false); got[0] != "C" || len(got) != 4 {
		t.Fatalf("nach Löschen: %v", got)
	}
	if ver() == before {
		t.Error("Version nach Löschen gleich")
	}
	if err := h.deleteTerminalSlide(e.ctx, a.ID); err != nil {
		t.Errorf("zweites Löschen: %v", err)
	}
}

func TestTerminalGlobalScreen(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("gsmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("gsplain", "viewer", false)
	term := e.user("gsterm", "viewer", true)
	term2 := e.user("gsterm2", "viewer", true)
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
	// global: Wert und Vorhandensein von global_screen im Ereignis „settings“.
	global := func(uid string) (bool, bool) {
		ev, err := h.terminalSettingsEvent(httptest.NewRequest("GET", "/terminal/events", nil), uid)
		e.must(err)
		var d map[string]any
		for _, line := range strings.Split(ev, "\n") {
			if strings.HasPrefix(line, "data: ") {
				e.must(json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d))
			}
		}
		v, present := d["global_screen"]
		b, _ := v.(bool)
		return b, present
	}
	const sent = "terminal_global_screen_sent=1"

	if g, present := global(term); g || !present {
		t.Fatalf("Vorgabe: %v present=%v", g, present)
	}
	do("POST", "/users/"+term+"/master", "terminal_card_notify_sent=1&terminal_card_notify=banner", mgr)
	rec := do("POST", "/users/"+term+"/master", sent+"&terminal_global_screen=on", mgr)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("speichern: %s", loc)
	}
	if g, _ := global(term); !g {
		t.Error("Schalter nicht gesetzt")
	}
	// Ohne Marker (ältere Formulare): unverändert.
	do("POST", "/users/"+term+"/master", "work_location=Halle", mgr)
	if g, _ := global(term); !g {
		t.Error("Speichern ohne Marker hat den Schalter verändert")
	}
	// Anmeldeart und Benachrichtigung lassen ihn unberührt.
	do("POST", "/users/"+term+"/master", "terminal_manual_login=password", mgr)
	do("POST", "/users/"+term+"/master", "terminal_card_notify_sent=1&terminal_card_notify=front", mgr)
	if g, _ := global(term); !g {
		t.Error("andere Einstellungen überschreiben global_screen")
	}
	// Umgekehrt: global_screen lässt card_notify und manual_login unberührt.
	do("POST", "/users/"+term+"/master", sent+"&terminal_global_screen=on", mgr)
	var cn []string
	var ml string
	e.must(e.pool.QueryRow(e.ctx, `SELECT card_notify, manual_login FROM terminal_settings WHERE user_id = $1::uuid`, term).Scan(&cn, &ml))
	if len(cn) != 1 || cn[0] != "front" || ml != "password" {
		t.Errorf("andere Spalten verändert: %v %s", cn, ml)
	}
	// Ohne canManageUsers nichts gespeichert.
	do("POST", "/users/"+term+"/master", sent, plain)
	if g, _ := global(term); !g {
		t.Error("ohne canManageUsers gespeichert")
	}
	// Abhaken.
	do("POST", "/users/"+term+"/master", sent, mgr)
	if g, _ := global(term); g {
		t.Error("Schalter nicht gelöscht")
	}
	// Nicht-System-Benutzer: nichts.
	do("POST", "/users/"+plain+"/master", sent+"&terminal_global_screen=on", mgr)
	if n := mustInt(e, `SELECT count(*) FROM terminal_settings WHERE user_id = $1::uuid`, plain); n != 0 {
		t.Error("Nicht-System-Benutzer bekam Einstellungen")
	}
	// Formular.
	do("POST", "/users/"+term+"/master", sent+"&terminal_global_screen=on", mgr)
	b := do("GET", "/users/"+term+"?tab=master", "", mgr).Body.String()
	for _, want := range []string{`name="terminal_global_screen_sent" value="1"`, `name="terminal_global_screen" checked`,
		"Globales Terminal (Infobildschirm)", "ohne Anmeldung und ohne Kartenleser"} {
		if !strings.Contains(b, want) {
			t.Errorf("Formular ohne %q", want)
		}
	}

	// notifyGlobalTerminals: nur Terminals mit global_screen bekommen „slides“.
	e.must(h.saveTerminalGlobalScreen(e.ctx, term2, false))
	chG, unG := h.eventHub().subscribe(term)
	defer unG()
	chN, unN := h.eventHub().subscribe(term2)
	defer unN()
	h.notifyGlobalTerminals()
	select {
	case ev := <-chG:
		if ev != "slides" {
			t.Errorf("Ereignis: %q", ev)
		}
	case <-time.After(2 * time.Second):
		t.Error("globales Terminal bekam kein Ereignis")
	}
	select {
	case ev := <-chN:
		t.Errorf("anderes Terminal bekam %q", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestTerminalSlidesPermission(t *testing.T) {
	e := newATEnv(t)
	if n := mustInt(e, `SELECT count(*) FROM permissions WHERE key = 'terminal.slides.edit' AND label = 'Infobildschirm bearbeiten'`); n != 1 {
		t.Fatalf("Recht fehlt: %d", n)
	}
	if n := mustInt(e, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id JOIN roles r ON r.id = rp.role_id
		WHERE p.key = 'terminal.slides.edit' AND r.key = 'admin'`); n < 1 {
		t.Fatal("Admin ohne Recht")
	}
}

// Folienschau als Bildschirmschoner: eigener Marker, wird weder von anderen Formularen noch von global_screen zurückgesetzt.
func TestTerminalSaverSlidesSetting(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("ssmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	plain := e.user("ssplain", "viewer", false)
	term := e.user("ssterm", "viewer", true)
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
	saver := func() bool {
		ls, err := h.terminalLoginSettings(e.ctx, term)
		e.must(err)
		return ls.SaverSlides
	}
	const sent = "terminal_saver_slides_sent=1"
	if saver() {
		t.Fatal("Vorgabe muss aus sein")
	}
	do("POST", "/users/"+term+"/master", sent+"&terminal_saver_slides=on", mgr)
	if !saver() {
		t.Fatal("Schalter nicht gesetzt")
	}
	if ev, err := h.terminalSettingsEvent(httptest.NewRequest("GET", "/terminal/events", nil), term); err != nil || !strings.Contains(ev, `"saver_slides":true`) {
		t.Errorf("Ereignis settings ohne saver_slides: %v %s", err, ev)
	}
	// ohne Marker, mit anderen Schaltern, mit global_screen: unverändert
	do("POST", "/users/"+term+"/master", "work_location=Halle", mgr)
	do("POST", "/users/"+term+"/master", "terminal_global_screen_sent=1", mgr)
	do("POST", "/users/"+term+"/master", "terminal_card_notify_sent=1&terminal_card_notify=banner", mgr)
	if !saver() {
		t.Error("Schalter durch andere Speicherung zurückgesetzt")
	}
	do("POST", "/users/"+term+"/master", sent+"&terminal_global_screen_sent=1&terminal_saver_slides=on", plain)
	do("POST", "/users/"+term+"/master", sent, plain)
	if !saver() {
		t.Error("ohne canManageUsers verändert")
	}
	if b := do("GET", "/users/"+term+"?tab=master", "", mgr).Body.String(); !strings.Contains(b, `name="terminal_saver_slides" checked`) || !strings.Contains(b, "Folienschau als Bildschirmschoner") {
		t.Error("Formular ohne Schalter")
	}
	do("POST", "/users/"+term+"/master", sent, mgr)
	if saver() {
		t.Error("Schalter nicht gelöscht")
	}
	// Nicht-System-Benutzer: nichts
	do("POST", "/users/"+plain+"/master", sent+"&terminal_saver_slides=on", mgr)
	if n := mustInt(e, `SELECT count(*) FROM terminal_settings WHERE user_id = $1::uuid`, plain); n != 0 {
		t.Error("Nicht-System-Benutzer bekam Einstellungen")
	}
}
