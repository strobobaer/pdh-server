//go:build integration

// Globales Terminal (Infobildschirm): Seite, gesperrte Kartenwege, slides.json, Bilder, Vorgang-Ansicht.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalGlobal ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
)

type gtEnv struct {
	e                                 *atEnv
	h                                 *Handler
	r                                 chi.Router
	termG, termNoTickets, termNormal  string // globale Terminals (Störungen+Tickets bzw. nur Störungen) und ein Anlagenterminal
	devG, devNT, devN                 string
	infraA, infraA1, infraB, infraC   string
	fA, fA1, fB, fC, tA, tB, fResolve string
}

func newGTEnv(t *testing.T) *gtEnv {
	t.Helper()
	e := newATEnv(t)
	g := &gtEnv{e: e}
	deptA, deptB := e.dept("GA"), e.dept("GB")
	g.infraA = e.infra("GAnlage A ", deptA, "")
	g.infraA1 = e.infra("GUnter A1 ", deptA, g.infraA)
	g.infraB = e.infra("GAnlage B ", deptB, "")
	g.infraC = e.infra("GAnlage C ", deptA, "")
	g.termG = e.user("gtg", "viewer", true)
	g.termNoTickets = e.user("gtn", "viewer", true)
	g.termNormal = e.user("gtx", "viewer", true)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, g.termNormal, g.infraA)
	for _, u := range []string{g.termG, g.termNoTickets, g.termNormal} {
		e.grant(u, "faults.view", deptA, true)
	}
	e.grant(g.termG, "tickets.view", deptA, true)
	e.grant(g.termNormal, "tickets.view", deptA, true)
	e.grant(g.termNoTickets, "tickets.view", "", false)
	e.warm()

	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	terminalTemplatePath = "../../web/templates/terminal.gohtml"
	g.h = h
	e.must(h.saveTerminalGlobalScreen(e.ctx, g.termG, true))
	e.must(h.saveTerminalGlobalScreen(e.ctx, g.termNoTickets, true))

	mkDev := func(term string) string {
		secret, err := newDeviceSecret()
		e.must(err)
		dev := e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi', $2) RETURNING id::text`, term, hashDeviceSecret(secret))
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM terminal_devices WHERE id = $1::uuid`, dev) })
		terminalDevices.forget(dev)
		return dev
	}
	g.devG, g.devNT, g.devN = mkDev(g.termG), mkDev(g.termNoTickets), mkDev(g.termNormal)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			uid, _, device := h.sessionToken(req)
			if uid != "" && (device == "" || terminalDevices.active(req.Context(), device, uid, h.deviceByID)) {
				if u, err := h.userByID(req.Context(), uid); err == nil && u != nil {
					req = req.WithContext(context.WithValue(req.Context(), "user", u))
				}
			}
			next.ServeHTTP(w, req)
		})
	})
	h.terminalRoutes(r)
	g.r = r

	fault := func(title, infra, desc string) string {
		return e.id(`INSERT INTO faults (title, description, status, infrastructure_id, created_by) VALUES ($1, $2, 'detected'::fault_status, $3::uuid, $4::uuid) RETURNING id::text`, title, desc, infra, g.termG)
	}
	ticket := func(title, infra string) string {
		return e.id(`INSERT INTO tickets (title, description, status, infrastructure_id, created_by) VALUES ($1, 'Beschreibung', 'open'::ticket_status, $2::uuid, $3::uuid) RETURNING id::text`, title, infra, g.termG)
	}
	g.fA = fault("F-A", g.infraA, "Beschreibung der Störung A")
	g.fA1 = fault("F-A1", g.infraA1, "")
	g.fB = fault("F-B", g.infraB, "")
	g.fC = fault("F-C", g.infraC, "")
	g.fResolve = fault("F-Gelöst", g.infraA, "")
	e.exec(`UPDATE faults SET status = 'resolved'::fault_status WHERE id = $1::uuid`, g.fResolve)
	g.tA = ticket("T-A", g.infraA)
	g.tB = ticket("T-B", g.infraB)
	e.exec(`INSERT INTO record_history (ref_type, ref_id, action, message, created_by) VALUES ('fault', $1::uuid, 'update', 'Ersatzteil bestellt', $2::uuid)`, g.fA, g.termG)

	e.exec(`DELETE FROM terminal_slides`)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM terminal_slides`) })
	return g
}

func (g *gtEnv) cookie(term, device string) *http.Cookie {
	claims := jwt.MapClaims{"sub": term, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	if device != "" {
		claims["device"] = device
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(g.h.jwtSecret))
	g.e.must(err)
	return &http.Cookie{Name: "pdh_token", Value: s}
}

func (g *gtEnv) do(method, path string, c *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	g.r.ServeHTTP(rec, req)
	return rec
}

func (g *gtEnv) slide(s terminalSlide) string {
	g.e.t.Helper()
	g.e.must(g.h.saveTerminalSlide(g.e.ctx, &s))
	return s.ID
}

var gtPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func TestTerminalGlobalPageAndCardLock(t *testing.T) {
	g := newGTEnv(t)
	cG, cN := g.cookie(g.termG, g.devG), g.cookie(g.termNormal, g.devN)

	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu"} {
		rec := g.do("GET", p, cG, nil)
		b := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(b, `data-mode="global"`) || !strings.Contains(b, `id="g-stage"`) {
			t.Errorf("%s: %d kein Modus global", p, rec.Code)
		}
		for _, bad := range []string{"data-card-events", "login-panel", "data-card-bar", "data-manual-login", "login-dialog"} {
			if strings.Contains(b, bad) {
				t.Errorf("%s: globale Seite enthält %q", p, bad)
			}
		}
	}
	if rec := g.do("GET", "/terminal/info.json", cG, nil); rec.Code != http.StatusNotFound {
		t.Errorf("info.json global: %d", rec.Code)
	}

	// Kartenwege gesperrt
	for _, p := range []string{"/terminal/card", "/terminal/card/ping", "/terminal/login"} {
		rec := g.do("POST", p, cG, map[string]string{"Content-Type": "application/json"})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Globales Terminal: keine Anmeldung") {
			t.Errorf("%s global: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	// ... der Ereigniskanal bleibt für die Folien offen (nur Kopfzeile prüfen)
	// Anlagenterminal: unverändert
	rec := g.do("GET", "/terminal", cN, nil)
	if b := rec.Body.String(); rec.Code != 200 || !strings.Contains(b, `data-mode="schoner"`) || !strings.Contains(b, `data-card-events="1"`) || strings.Contains(b, `id="g-stage"`) {
		t.Errorf("Anlagenterminal: %d", rec.Code)
	}
	if rec := g.do("POST", "/terminal/card/ping", cN, nil); rec.Code != 200 {
		t.Errorf("ping am Anlagenterminal: %d %s", rec.Code, rec.Body.String())
	}
	// Anlagenterminal: Folienwege gesperrt
	for _, p := range []string{"/terminal/slides.json", "/terminal/slides/" + g.fA + "/image", "/terminal/vorgang/fault/" + g.fA} {
		if rec := g.do("GET", p, cN, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s am Anlagenterminal: %d", p, rec.Code)
		}
	}
	// Globales Terminal ohne Gerätesitzung: kein Modus global, Folienwege gesperrt
	cNoDev := g.cookie(g.termG, "")
	for _, p := range []string{"/terminal/slides.json", "/terminal/vorgang/fault/" + g.fA} {
		if rec := g.do("GET", p, cNoDev, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Gerät: %d", p, rec.Code)
		}
	}
	// normaler Benutzer
	normal := g.e.user("gtnorm", "user", false)
	if rec := g.do("GET", "/terminal/slides.json", g.cookie(normal, ""), nil); rec.Code != http.StatusForbidden {
		t.Errorf("normaler Benutzer: %d", rec.Code)
	}
}

type gtSlidesDoc struct {
	Version string `json:"version"`
	Idle    int    `json:"idle"`
	Slides  []struct {
		ID           string         `json:"id"`
		Kind         string         `json:"kind"`
		Title        string         `json:"title"`
		Body         string         `json:"body"`
		Duration     int            `json:"duration"`
		ImageURL     string         `json:"image_url"`
		Faults       []terminalItem `json:"faults"`
		Tickets      []terminalItem `json:"tickets"`
		FaultsTotal  int            `json:"faults_total"`
		TicketsTotal int            `json:"tickets_total"`
	} `json:"slides"`
}

func (g *gtEnv) slidesJSON(c *http.Cookie) gtSlidesDoc {
	g.e.t.Helper()
	rec := g.do("GET", "/terminal/slides.json", c, nil)
	if rec.Code != 200 {
		g.e.t.Fatalf("slides.json: %d %s", rec.Code, rec.Body.String())
	}
	var d gtSlidesDoc
	g.e.must(json.Unmarshal(rec.Body.Bytes(), &d))
	return d
}

func itemTitles(items []terminalItem) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Title)
	}
	return strings.Join(s, "|")
}

func TestTerminalGlobalSlidesJSON(t *testing.T) {
	g := newGTEnv(t)
	cG, cNT := g.cookie(g.termG, g.devG), g.cookie(g.termNoTickets, g.devNT)

	// ohne Folien: leere Liste, Version und Rückkehrzeit da
	d := g.slidesJSON(cG)
	if len(d.Slides) != 0 || d.Version == "" || d.Idle <= 0 {
		t.Errorf("ohne Folien: %+v", d)
	}

	g.slide(terminalSlide{Kind: "text", Title: "Willkommen", Body: "Guten Morgen", Active: true, DurationSeconds: 20})
	g.slide(terminalSlide{Kind: "text", Title: "Versteckt", Body: "x", Active: false})
	s3 := g.slide(terminalSlide{Kind: "vorgaenge", Title: "Anlage A", InfraIDs: []string{g.infraA}, ShowFaults: true, ShowTickets: true, Active: true})
	s4 := g.slide(terminalSlide{Kind: "image", Title: "Aushang", Image: gtPNG, Active: true})

	d = g.slidesJSON(cG)
	if len(d.Slides) != 3 || d.Slides[0].Title != "Willkommen" || d.Slides[1].ID != s3 || d.Slides[2].ID != s4 {
		t.Fatalf("aktive Folien in Reihenfolge: %+v", d.Slides)
	}
	if d.Slides[0].Duration != 20 || d.Slides[0].Body != "Guten Morgen" || d.Slides[0].Kind != "text" {
		t.Errorf("Textfolie: %+v", d.Slides[0])
	}
	if !strings.HasPrefix(d.Slides[2].ImageURL, "/terminal/slides/"+s4+"/image") {
		t.Errorf("image_url: %q", d.Slides[2].ImageURL)
	}
	v := d.Slides[1]
	// Anlage A samt Unteranlage A1; nicht B/C, nicht gelöst
	if itemTitles(v.Faults) != "F-A1|F-A" && itemTitles(v.Faults) != "F-A|F-A1" || v.FaultsTotal != 2 {
		t.Errorf("Störungen der Folie: %s total %d", itemTitles(v.Faults), v.FaultsTotal)
	}
	if itemTitles(v.Tickets) != "T-A" || v.TicketsTotal != 1 {
		t.Errorf("Tickets der Folie: %s total %d", itemTitles(v.Tickets), v.TicketsTotal)
	}
	for _, it := range v.Faults {
		if it.ID == "" || it.StatusLabel == "" || it.Age == "" {
			t.Errorf("Eintrag unvollständig: %+v", it)
		}
	}
	for _, it := range v.Faults {
		if it.Title == "F-A1" && !strings.Contains(it.Path, "GUnter A1") {
			t.Errorf("Pfad: %q", it.Path)
		}
	}

	// ohne Ticket-Recht: keine Tickets
	dn := g.slidesJSON(cNT)
	if len(dn.Slides) != 3 || len(dn.Slides[1].Tickets) != 0 || dn.Slides[1].TicketsTotal != 0 || dn.Slides[1].FaultsTotal != 2 {
		t.Errorf("ohne Ticket-Recht: %+v", dn.Slides[1])
	}

	// leer = alle Anlagen, nur Störungen; fremde Abteilung (B) bleibt außen vor; höchstens 12
	for i := 0; i < 14; i++ {
		g.e.exec(`INSERT INTO faults (title, status, infrastructure_id, created_by) VALUES ('F-Viele', 'detected'::fault_status, $1::uuid, $2::uuid)`, g.infraC, g.termG)
	}
	s5 := g.slide(terminalSlide{Kind: "vorgaenge", Title: "Alle", ShowFaults: true, ShowTickets: false, Active: true})
	d = g.slidesJSON(cG)
	var all = d.Slides[len(d.Slides)-1]
	if all.ID != s5 {
		t.Fatalf("letzte Folie: %+v", all)
	}
	if len(all.Faults) != 12 || all.FaultsTotal != 2+15 { // F-A, F-A1, F-C und 14 weitere
		t.Errorf("alle Anlagen: %d Einträge, total %d", len(all.Faults), all.FaultsTotal)
	}
	for _, it := range all.Faults {
		if it.Title == "F-B" {
			t.Error("Störung aus fremder Abteilung sichtbar")
		}
	}
	if len(all.Tickets) != 0 || all.TicketsTotal != 0 {
		t.Errorf("Tickets trotz ausgeschaltet: %+v", all.Tickets)
	}

	// Version ändert sich mit Inhalt, nicht mit der Zeit
	v1 := g.slidesJSON(cG).Version
	if v2 := g.slidesJSON(cG).Version; v1 != v2 {
		t.Error("Version ohne Änderung verschieden")
	}
	g.e.exec(`UPDATE faults SET status = 'in_progress'::fault_status WHERE id = $1::uuid`, g.fA)
	if g.slidesJSON(cG).Version == v1 {
		t.Error("Version nach Statusänderung gleich")
	}
}

func TestTerminalGlobalSlideImage(t *testing.T) {
	g := newGTEnv(t)
	cG, cN := g.cookie(g.termG, g.devG), g.cookie(g.termNormal, g.devN)
	id := g.slide(terminalSlide{Kind: "image", Image: gtPNG, Active: true})
	off := g.slide(terminalSlide{Kind: "image", Image: gtPNG, Active: false})
	txt := g.slide(terminalSlide{Kind: "text", Title: "T", Active: true})

	rec := g.do("GET", "/terminal/slides/"+id+"/image", cG, nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "private, max-age=300" || rec.Body.Len() != len(gtPNG) {
		t.Fatalf("Bild: %d %v", rec.Code, rec.Header())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag fehlt")
	}
	if rec := g.do("GET", "/terminal/slides/"+id+"/image", cG, map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	for name, p := range map[string]string{"inaktiv": off, "Textfolie": txt, "unbekannt": "00000000-0000-0000-0000-000000000000", "ungültig": "x"} {
		if rec := g.do("GET", "/terminal/slides/"+p+"/image", cG, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := g.do("GET", "/terminal/slides/"+id+"/image", cN, nil); rec.Code != http.StatusForbidden {
		t.Errorf("nicht global: %d", rec.Code)
	}
}

// Review-Schwerpunkt 1: erratene IDs von Vorgängen, die nicht freigegeben sind, antworten 404.
func TestTerminalGlobalVorgangView(t *testing.T) {
	g := newGTEnv(t)
	cG, cNT := g.cookie(g.termG, g.devG), g.cookie(g.termNoTickets, g.devNT)
	view := func(c *http.Cookie, kind, id string) *httptest.ResponseRecorder {
		return g.do("GET", "/terminal/vorgang/"+kind+"/"+id, c, nil)
	}

	// ohne Vorgänge-Folie ist nichts freigegeben
	if rec := view(cG, "fault", g.fA); rec.Code != http.StatusNotFound {
		t.Errorf("ohne Folie: %d", rec.Code)
	}
	s3 := g.slide(terminalSlide{Kind: "vorgaenge", InfraIDs: []string{g.infraA}, ShowFaults: true, ShowTickets: true, Active: true})

	rec := view(cG, "fault", g.fA)
	b := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("erlaubte Anlage: %d", rec.Code)
	}
	for _, want := range []string{"F-A", "Beschreibung der Störung A", "Ersatzteil bestellt", "Erkannt", "GAnlage A", `data-mode="vorgang"`, `href="/terminal"`} {
		if !strings.Contains(b, want) {
			t.Errorf("Ansicht enthält %q nicht", want)
		}
	}
	if strings.Count(b, "<a ") != 1 || strings.Count(b, "href=") != 1 || strings.Contains(b, "<form") || strings.Contains(b, "<button") || strings.Contains(b, "<input") {
		t.Errorf("Ansicht hat Links/Formulare über „Zurück“ hinaus:\n%s", b)
	}
	if rec := view(cG, "fault", g.fA1); rec.Code != 200 {
		t.Errorf("Unteranlage: %d", rec.Code)
	}
	if rec := view(cG, "ticket", g.tA); rec.Code != 200 || !strings.Contains(rec.Body.String(), "T-A") {
		t.Errorf("Ticket: %d", rec.Code)
	}

	// fremde Anlagen (nicht in der Folie bzw. andere Abteilung), gelöst, ungültig: 404
	bad := map[string]*httptest.ResponseRecorder{
		"Anlage C (erlaubt, aber in keiner Folie)": view(cG, "fault", g.fC),
		"Anlage B":                                 view(cG, "fault", g.fB),
		"Ticket in Anlage B":                       view(cG, "ticket", g.tB),
		"gelöst":                                   view(cG, "fault", g.fResolve),
		"Art vertauscht":                           view(cG, "ticket", g.fA),
		"unbekannte Art":                           view(cG, "kvp", g.fA),
		"ungültige ID":                             view(cG, "fault", "abc"),
		"unbekannte ID":                            view(cG, "fault", "00000000-0000-0000-0000-000000000000"),
		"Ticket ohne Ticket-Recht":                 view(cNT, "ticket", g.tA),
	}
	for name, rec := range bad {
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d statt 404", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "F-C") || strings.Contains(rec.Body.String(), "F-B") || strings.Contains(rec.Body.String(), "T-A") {
			t.Errorf("%s: 404-Seite verrät Inhalt", name)
		}
	}
	if rec := view(cNT, "fault", g.fA); rec.Code != 200 {
		t.Errorf("Störung ohne Ticket-Recht: %d", rec.Code)
	}

	// Folie ohne Tickets: Ticket nicht freigegeben, Störung schon
	g.slide(terminalSlide{ID: s3, Kind: "vorgaenge", InfraIDs: []string{g.infraA}, ShowFaults: true, ShowTickets: false, Active: true})
	if rec := view(cG, "ticket", g.tA); rec.Code != http.StatusNotFound {
		t.Errorf("Tickets der Folie ausgeschaltet: %d", rec.Code)
	}
	if rec := view(cG, "fault", g.fA); rec.Code != 200 {
		t.Errorf("Störung bei Folie ohne Tickets: %d", rec.Code)
	}
	// Folie „alle Anlagen“: Anlage C jetzt freigegeben, B bleibt (fremde Abteilung) gesperrt
	g.slide(terminalSlide{Kind: "vorgaenge", ShowFaults: true, Active: true})
	if rec := view(cG, "fault", g.fC); rec.Code != 200 {
		t.Errorf("alle Anlagen, Anlage C: %d", rec.Code)
	}
	if rec := view(cG, "fault", g.fB); rec.Code != http.StatusNotFound {
		t.Errorf("alle Anlagen, Anlage B: %d", rec.Code)
	}
	// Folie inaktiv: Anlage A wieder gesperrt, soweit keine andere Folie sie abdeckt – die „alle“-Folie deckt sie
	g.slide(terminalSlide{ID: s3, Kind: "vorgaenge", InfraIDs: []string{g.infraA}, ShowFaults: true, Active: false})
	g.e.exec(`DELETE FROM terminal_slides WHERE kind = 'vorgaenge' AND infra_ids = '{}'`)
	if rec := view(cG, "fault", g.fA); rec.Code != http.StatusNotFound {
		t.Errorf("inaktive Folie: %d", rec.Code)
	}
	// nicht global
	if rec := view(g.cookie(g.termNormal, g.devN), "fault", g.fA); rec.Code != http.StatusForbidden {
		t.Errorf("nicht global: %d", rec.Code)
	}
}

// Schoner und Seite „keine Anlage“ hören auf /terminal/events (Wechsel zum globalen Terminal), die globale Seite nicht
// über das Attribut (sie öffnet ihren Strom selbst); der Schoner des globalen Terminals zeigt die Instanz statt „PDH“-Festtext.
func TestTerminalGlobalSettingsEventsAttribute(t *testing.T) {
	g := newGTEnv(t)
	// Anlagenterminal: Schoner und Info tragen das Attribut
	cN := g.cookie(g.termNormal, g.devN)
	for _, p := range []string{"/terminal", "/terminal/info", "/terminal/neu"} {
		if b := g.do("GET", p, cN, nil).Body.String(); !strings.Contains(b, `data-settings-events="1"`) {
			t.Errorf("%s am Anlagenterminal: data-settings-events fehlt", p)
		}
	}
	// ohne Gerätesitzung kein Strom
	if b := g.do("GET", "/terminal", g.cookie(g.termNormal, ""), nil).Body.String(); strings.Contains(b, "data-settings-events") {
		t.Error("ohne Gerätesitzung darf data-settings-events nicht stehen")
	}
	// Terminal ohne Anlage (Gerätesitzung): Seite „keine Anlage“ trägt das Attribut
	noInfra := g.e.user("gtnoinfra", "viewer", true)
	secret, err := newDeviceSecret()
	g.e.must(err)
	dev := g.e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi', $2) RETURNING id::text`, noInfra, hashDeviceSecret(secret))
	t.Cleanup(func() { _, _ = g.e.pool.Exec(context.Background(), `DELETE FROM terminal_devices WHERE id = $1::uuid`, dev) })
	terminalDevices.forget(dev)
	rec := g.do("GET", "/terminal", g.cookie(noInfra, dev), nil)
	if b := rec.Body.String(); rec.Code != 200 || !strings.Contains(b, "keine Anlage zugeordnet") || !strings.Contains(b, `data-settings-events="1"`) {
		t.Errorf("Seite ohne Anlage: %d, Attribut fehlt", rec.Code)
	}
	// globale Seite: Modus global, kein Kartenattribut
	if b := g.do("GET", "/terminal", g.cookie(g.termG, g.devG), nil).Body.String(); !strings.Contains(b, `data-mode="global"`) || strings.Contains(b, "data-settings-events") {
		t.Error("globale Seite: falscher Modus oder unerwartetes Attribut")
	}
}

// Folienschau als Bildschirmschoner: Schoner zeigt den Player statt des Platzhalters; Karten wecken weiter,
// Folienwege sind offen, Anmeldewege nicht gesperrt (anders als beim globalen Terminal).
func TestTerminalSaverSlidesPage(t *testing.T) {
	g := newGTEnv(t)
	cN := g.cookie(g.termNormal, g.devN)
	g.slide(terminalSlide{Kind: "text", Title: "Hallo", Body: "Willkommen", DurationSeconds: 10, Active: true})

	off := g.do("GET", "/terminal", cN, nil).Body.String()
	if !strings.Contains(off, `class="t-saver"`) || strings.Contains(off, `id="g-stage"`) || strings.Contains(off, `data-saver-slides="1"`) {
		t.Error("aus: Platzhalter-Schoner erwartet")
	}
	if rec := g.do("GET", "/terminal/slides.json", cN, nil); rec.Code != http.StatusForbidden {
		t.Errorf("aus: slides.json %d", rec.Code)
	}

	g.e.must(g.h.saveTerminalSaverSlides(g.e.ctx, g.termNormal, true))
	rec := g.do("GET", "/terminal", cN, nil)
	on := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(on, `data-mode="schoner"`) || !strings.Contains(on, `data-saver-slides="1"`) || !strings.Contains(on, `id="g-stage"`) || strings.Contains(on, `class="t-saver"`) {
		t.Errorf("an: Player statt Platzhalter erwartet (%d)", rec.Code)
	}
	// Kartenereignisse bleiben, das Terminal bleibt ein normales
	if !strings.Contains(on, `data-card-events="1"`) || !strings.Contains(on, `data-settings-events="1"`) {
		t.Error("an: Karten- und Einstellungsereignisse fehlen (Karte würde nicht wecken)")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("an: frame-src-Regel der Folien fehlt")
	}
	if d := g.slidesJSON(cN); len(d.Slides) != 1 {
		t.Errorf("an: Folien %d", len(d.Slides))
	}
	if rec := g.do("GET", "/terminal/vorgang/fault/"+g.fA, cN, nil); rec.Code == http.StatusForbidden {
		t.Error("an: Vorgang-Ansicht gesperrt")
	}
	// Info bleibt die normale Info-Seite (Anmeldung per Karte), nicht die Folienschau
	if b := g.do("GET", "/terminal/info", cN, nil).Body.String(); strings.Contains(b, `id="g-stage"`) || !strings.Contains(b, `data-mode="info"`) {
		t.Error("an: /terminal/info ist nicht die normale Info-Seite")
	}
	if rec := g.do("POST", "/terminal/card/ping", cN, nil); rec.Code != 200 {
		t.Errorf("an: Kartenweg %d", rec.Code)
	}
	// Ohne Gerätesitzung kein Player
	if b := g.do("GET", "/terminal", g.cookie(g.termNormal, ""), nil).Body.String(); strings.Contains(b, `id="g-stage"`) {
		t.Error("ohne Gerätesitzung: Player")
	}
}
