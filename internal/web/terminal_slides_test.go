package web

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

// Verwaltungsseite „Infobildschirm“ (terminal_slides.go).
// Aufruf: PDH_TEST_DSN=postgres://... go test -run 'TerminalSlidesPage|TerminalSlidesWeb|TerminalSlidesNav|TerminalSlidesGate' ./internal/web/

func slidesRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/terminal-slides", h.TerminalSlidesPage)
	r.Get("/terminal-slides/new", h.TerminalSlideNewPage)
	r.Post("/terminal-slides", h.TerminalSlideSaveWeb)
	r.Get("/terminal-slides/{id}", h.TerminalSlideEditPage)
	r.Post("/terminal-slides/{id}", h.TerminalSlideSaveWeb)
	r.Post("/terminal-slides/{id}/delete", h.TerminalSlideDeleteWeb)
	r.Post("/terminal-slides/{id}/move", h.TerminalSlideMoveWeb)
	r.Get("/terminal-slides/{id}/image", h.TerminalSlideImageWeb)
	return r
}

// Ohne das Recht antwortet jede Route 403, bevor sie die Datenbank anfasst.
func TestTerminalSlidesPermRequired(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	r := slidesRouter(h)
	id := "11111111-1111-4111-8111-111111111111"
	for _, c := range []struct{ method, path string }{
		{"GET", "/terminal-slides"}, {"GET", "/terminal-slides/new?kind=text"}, {"GET", "/terminal-slides/" + id},
		{"POST", "/terminal-slides"}, {"POST", "/terminal-slides/" + id}, {"POST", "/terminal-slides/" + id + "/delete"},
		{"POST", "/terminal-slides/" + id + "/move?d=1"}, {"GET", "/terminal-slides/" + id + "/image"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("kind=text&title=x")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s ohne Recht: %d", c.method, c.path, rec.Code)
		}
	}
	for _, p := range asked {
		if p != permTerminalSlides {
			t.Errorf("geprüftes Recht %q, erwartet %q", p, permTerminalSlides)
		}
	}
	if len(asked) != 8 {
		t.Errorf("Rechteprüfungen: %d, erwartet 8", len(asked))
	}
}

// Schreib-Routen stehen in gateRules; Lesen nicht.
func TestTerminalSlidesGateRules(t *testing.T) {
	id := "/11111111-1111-4111-8111-111111111111"
	for _, path := range []string{"/terminal-slides", "/terminal-slides" + id, "/terminal-slides" + id + "/delete", "/terminal-slides" + id + "/move"} {
		if _, perm, _, _, ok := matchGate("POST", path); !ok || perm != permTerminalSlides {
			t.Errorf("%s: Treffer %v Recht %q", path, ok, perm)
		}
	}
	if _, _, _, _, ok := matchGate("GET", "/terminal-slides"); ok {
		t.Error("GET steht in gateRules")
	}
}

// Das Menü zeigt „Infobildschirm“ nur mit dem Recht.
func TestTerminalSlidesNav(t *testing.T) {
	has := func(b *BaseData) bool {
		for _, g := range buildNav(b, nil) {
			for _, it := range g.Items {
				if it.Key == "terminal-slides" {
					return it.Href == "/terminal-slides"
				}
			}
		}
		return false
	}
	without := &BaseData{Lang: "de", navCan: func(string) bool { return false }}
	if has(without) {
		t.Error("Eintrag ohne Recht sichtbar")
	}
	with := &BaseData{Lang: "de", navCan: func(p string) bool { return p == permTerminalSlides }}
	if !has(with) {
		t.Error("Eintrag mit Recht fehlt")
	}
}

// Die Seiten rendern Titel und Text maskiert (kein HTML).
func TestTerminalSlidesTemplatesEscape(t *testing.T) {
	tmpl := loadTestTemplates(t)
	evil := `<script>alert(1)</script>`
	list := TerminalSlidesPageData{Slides: []terminalSlideView{
		{terminalSlide: terminalSlide{ID: "a", Kind: "text", Title: evil, Active: true, DurationSeconds: 15}, KindLabel: "Text", Summary: evil, First: true, Last: true},
		{terminalSlide: terminalSlide{ID: "b", Kind: "image", Title: "Bild", ImageType: "image/png", DurationSeconds: 15}, KindLabel: "Bild", HasImage: true},
	}}
	out := renderPage(t, tmpl, "terminal_slides", list)
	if strings.Contains(out, evil) {
		t.Error("Liste: Titel nicht maskiert")
	}
	if !strings.Contains(out, "/terminal-slides/b/image?v=") || !strings.Contains(out, "/terminal-slides/a/move?d=-1") {
		t.Error("Liste: Vorschau oder Verschieben fehlt")
	}
	form := terminalSlideFormData{Slide: terminalSlide{ID: "a", Kind: "text", Title: evil, Body: evil, DurationSeconds: 15, Active: true},
		InfraLabels: "{}", MaxBody: slideMaxBody, MaxTitle: slideMaxTitle, MaxMB: 5, Err: evil}
	out = renderPage(t, tmpl, "terminal_slide_form", form)
	if strings.Contains(out, evil) {
		t.Error("Formular: Eingaben nicht maskiert")
	}
	for _, want := range []string{`name="kind"`, `name="duration"`, `name="title"`, `name="image"`, `name="body"`, `name="infra_ids"`,
		`name="show_faults"`, `name="show_tickets"`, `name="active"`, `enctype="multipart/form-data"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Formular ohne %s", want)
		}
	}
}

// ── mit Datenbank ──

type slidesEnv struct {
	h    *Handler
	pool *pgxpool.Pool
	r    chi.Router
	user *users.User
}

func newSlidesEnv(t *testing.T) *slidesEnv {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	h, pool := newITNetTestHandler(t)
	ctx := context.Background()
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	h.rbac = rb
	h.tmpl = loadTestTemplates(t)
	cwd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'S', 'L', 'admin', '', '') RETURNING id::text`, "slides"+sfx).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, uid) })
	_, _ = pool.Exec(ctx, `DELETE FROM terminal_slides`)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM terminal_slides`) })
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permTerminalSlides }
	return &slidesEnv{h: h, pool: pool, r: slidesRouter(h), user: &users.User{ID: uid, FirstName: "S", LastName: "L", Role: "admin"}}
}

func (e *slidesEnv) do(method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req = req.WithContext(context.WithValue(req.Context(), "user", e.user))
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec
}

// post schickt ein Formular als multipart (mit optionaler Bilddatei).
func (e *slidesEnv) post(path string, fields map[string]string, file []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if file != nil {
		fw, _ := mw.CreateFormFile("image", "bild.png")
		_, _ = fw.Write(file)
	}
	_ = mw.Close()
	return e.do("POST", path, buf.Bytes(), mw.FormDataContentType())
}

func (e *slidesEnv) slides(t *testing.T) []terminalSlide {
	t.Helper()
	list, err := e.h.listTerminalSlides(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func slidesTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func wantRedirect(t *testing.T, rec *httptest.ResponseRecorder, notice string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Status %d, erwartet Weiterleitung: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "n="+notice) && notice != "" {
		t.Fatalf("Weiterleitung %q, erwartet n=%s", loc, notice)
	}
}

func TestTerminalSlidesWebCreateEachKind(t *testing.T) {
	e := newSlidesEnv(t)
	infra := "22222222-2222-4222-8222-222222222222"
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "text", "title": "Mitteilung", "body": "Zeile 1\r\nZeile 2", "duration": "20", "active": "1"}, nil), "created")
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "image", "title": "Aushang", "duration": "", "active": "1"}, slidesTestPNG(t)), "created")
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "vorgaenge", "title": "Lage", "infra_ids": infra + ", " + strings.ToUpper(infra),
		"show_faults": "1", "duration": "30", "active": "1"}, nil), "created")
	// urlencoded geht auch (Textfolie ohne Bild)
	form := url.Values{"kind": {"text"}, "title": {"Aus"}, "body": {"x"}}
	wantRedirect(t, e.do("POST", "/terminal-slides", []byte(form.Encode()), "application/x-www-form-urlencoded"), "created")

	got := e.slides(t)
	if len(got) != 4 {
		t.Fatalf("Folien: %d", len(got))
	}
	txt, img, vor, off := got[0], got[1], got[2], got[3]
	if txt.Kind != "text" || txt.Body != "Zeile 1\nZeile 2" || txt.DurationSeconds != 20 || !txt.Active || txt.ImageType != "" {
		t.Errorf("Text: %+v", txt)
	}
	if img.Kind != "image" || img.ImageType != "image/png" || img.DurationSeconds != 15 || img.Title != "Aushang" {
		t.Errorf("Bild: %+v", img)
	}
	if vor.Kind != "vorgaenge" || len(vor.InfraIDs) != 1 || vor.InfraIDs[0] != infra || !vor.ShowFaults || vor.ShowTickets || vor.DurationSeconds != 30 {
		t.Errorf("Vorgänge: %+v", vor)
	}
	if off.Active {
		t.Errorf("ohne Haken muss die Folie aus sein: %+v", off)
	}

	// Liste und Formulare rendern; Vorschau liefert das Bild, nur mit Recht
	rec := e.do("GET", "/terminal-slides", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/terminal-slides/"+img.ID+"/image") || !strings.Contains(rec.Body.String(), "Aushang") {
		t.Errorf("Liste: %d", rec.Code)
	}
	for _, path := range []string{"/terminal-slides/new?kind=vorgaenge", "/terminal-slides/new", "/terminal-slides/" + txt.ID, "/terminal-slides/" + img.ID, "/terminal-slides/" + vor.ID} {
		if rec := e.do("GET", path, nil, ""); rec.Code != 200 {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec = e.do("GET", "/terminal-slides/"+img.ID+"/image", nil, "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(rec.Body.Bytes(), slidesTestPNG(t)) {
		t.Errorf("Bild: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := e.do("GET", "/terminal-slides/"+txt.ID+"/image", nil, ""); rec.Code != 404 {
		t.Errorf("Bild einer Textfolie: %d", rec.Code)
	}
	if rec := e.do("GET", "/terminal-slides/33333333-3333-4333-8333-333333333333", nil, ""); rec.Code != 404 {
		t.Errorf("unbekannte Folie: %d", rec.Code)
	}
}

func TestTerminalSlidesWebInvalidInput(t *testing.T) {
	e := newSlidesEnv(t)
	big := append(slidesTestPNG(t), bytes.Repeat([]byte{0}, 5*1024*1024)...)
	for name, c := range map[string]struct {
		fields map[string]string
		file   []byte
		want   string
	}{
		"Dauer zu klein":   {map[string]string{"kind": "text", "title": "T", "duration": "4"}, nil, "zwischen 5 und 600"},
		"Dauer zu groß":    {map[string]string{"kind": "text", "title": "T", "duration": "601"}, nil, "zwischen 5 und 600"},
		"Dauer kein Wert":  {map[string]string{"kind": "text", "title": "T", "duration": "abc"}, nil, "ganze Zahl"},
		"Text zu lang":     {map[string]string{"kind": "text", "body": strings.Repeat("a", 2001)}, nil, "höchstens 2000 Zeichen"},
		"Text leer":        {map[string]string{"kind": "text"}, nil, "Titel oder einen Text"},
		"Art unbekannt":    {map[string]string{"kind": "video", "title": "T"}, nil, "unbekannte Folienart"},
		"Bild fehlt":       {map[string]string{"kind": "image", "title": "T"}, nil, "braucht ein Bild"},
		"Bild kein Bild":   {map[string]string{"kind": "image"}, []byte("<html>kein bild</html>"), "JPEG, PNG oder WebP"},
		"Bild zu groß":     {map[string]string{"kind": "image"}, big, "höchstens 5 MB"},
		"Anlage ungültig":  {map[string]string{"kind": "vorgaenge", "infra_ids": "keine-uuid"}, nil, "Ungültige Anlage"},
		"Titel zu lang":    {map[string]string{"kind": "text", "title": strings.Repeat("t", 201), "body": "x"}, nil, "höchstens 200 Zeichen"},
		"Anlagen zu viele": {map[string]string{"kind": "vorgaenge", "infra_ids": manyUUIDs(slideMaxInfra + 1)}, nil, "höchstens 200 Anlagen"},
	} {
		rec := e.post("/terminal-slides", c.fields, c.file)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: Status %d, erwartet 422", name, rec.Code)
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, c.want) || !strings.Contains(body, `name="kind"`) {
			t.Errorf("%s: Fehlermeldung %q fehlt im Formular", name, c.want)
		}
	}
	// Über der Anfrage-Obergrenze (Bild plus 1 MB): auch abgelehnt, nichts gespeichert
	huge := bytes.Repeat([]byte{1}, 7*1024*1024)
	if rec := e.post("/terminal-slides", map[string]string{"kind": "image"}, huge); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "höchstens 5 MB") {
		t.Errorf("7 MB: %d", rec.Code)
	}
	if n := len(e.slides(t)); n != 0 {
		t.Errorf("trotz Fehlern gespeichert: %d Folien", n)
	}
	// Ändern mit Fehler: Folie bleibt unverändert, Formular zeigt die Meldung
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "text", "title": "Alt", "body": "x", "active": "1"}, nil), "created")
	id := e.slides(t)[0].ID
	rec := e.post("/terminal-slides/"+id, map[string]string{"kind": "text", "title": "Neu", "body": "x", "duration": "9999"}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "zwischen 5 und 600") || !strings.Contains(rec.Body.String(), `value="Neu"`) {
		t.Errorf("Ändern mit Fehler: %d", rec.Code)
	}
	if s := e.slides(t)[0]; s.Title != "Alt" || s.DurationSeconds != 15 {
		t.Errorf("Folie trotz Fehler geändert: %+v", s)
	}
	// Eine Bildfolie ohne Bild: Art ändern auf Bild ohne Datei wird abgelehnt
	rec = e.post("/terminal-slides/"+id, map[string]string{"kind": "image", "title": "Alt", "active": "1"}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "braucht ein Bild") {
		t.Errorf("Art Bild ohne Bild: %d", rec.Code)
	}
	if s := e.slides(t)[0]; s.Kind != "text" {
		t.Errorf("Art trotz Fehler geändert: %+v", s)
	}
	// Ändern einer unbekannten Folie: 404
	if rec := e.post("/terminal-slides/33333333-3333-4333-8333-333333333333", map[string]string{"kind": "text", "title": "x"}, nil); rec.Code != 404 {
		t.Errorf("unbekannte Folie ändern: %d", rec.Code)
	}
}

func manyUUIDs(n int) string {
	var s []string
	for i := 0; i < n; i++ {
		s = append(s, fmt.Sprintf("00000000-0000-4000-8000-%012d", i))
	}
	return strings.Join(s, ",")
}

func TestTerminalSlidesWebEditMoveDelete(t *testing.T) {
	e := newSlidesEnv(t)
	for _, title := range []string{"A", "B", "C"} {
		wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "text", "title": title, "body": "x", "active": "1"}, nil), "created")
	}
	titles := func() string {
		var out []string
		for _, s := range e.slides(t) {
			out = append(out, s.Title)
		}
		return strings.Join(out, "")
	}
	ids := func() []string {
		var out []string
		for _, s := range e.slides(t) {
			out = append(out, s.ID)
		}
		return out
	}
	a, b, c := ids()[0], ids()[1], ids()[2]
	wantRedirect(t, e.do("POST", "/terminal-slides/"+c+"/move?d=-1", nil, ""), "")
	if titles() != "ACB" {
		t.Errorf("nach oben: %s", titles())
	}
	wantRedirect(t, e.do("POST", "/terminal-slides/"+a+"/move?d=1", nil, ""), "")
	if titles() != "CAB" {
		t.Errorf("nach unten: %s", titles())
	}
	wantRedirect(t, e.do("POST", "/terminal-slides/"+c+"/move?d=-1", nil, ""), "") // Rand
	if titles() != "CAB" {
		t.Errorf("Rand: %s", titles())
	}
	if rec := e.do("POST", "/terminal-slides/"+a+"/move?d=2", nil, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("ungültige Richtung: %d", rec.Code)
	}

	// Ändern: Titel, Dauer, aus; Position bleibt
	wantRedirect(t, e.post("/terminal-slides/"+a, map[string]string{"kind": "text", "title": "A2", "body": "neu", "duration": "60"}, nil), "saved")
	s, err := e.h.getTerminalSlide(context.Background(), a)
	if err != nil || s.Title != "A2" || s.Body != "neu" || s.DurationSeconds != 60 || s.Active || titles() != "CA2B" {
		t.Errorf("geändert: %+v %v %s", s, err, titles())
	}

	// Bild ersetzen und Bild behalten
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "image", "title": "Bild"}, slidesTestPNG(t)), "created")
	img := e.slides(t)[3].ID
	wantRedirect(t, e.post("/terminal-slides/"+img, map[string]string{"kind": "image", "title": "Bild 2", "active": "1"}, nil), "saved")
	if data, typ, err := e.h.terminalSlideImage(context.Background(), img); err != nil || typ != "image/png" || len(data) == 0 {
		t.Errorf("Bild nach Ändern ohne Datei: %v %q", err, typ)
	}
	// Art wechseln lässt das Bild fallen
	wantRedirect(t, e.post("/terminal-slides/"+img, map[string]string{"kind": "text", "title": "Nur Text", "body": "t", "active": "1"}, nil), "saved")
	if _, _, err := e.h.terminalSlideImage(context.Background(), img); err == nil {
		t.Error("Bild blieb nach Artwechsel")
	}

	// Löschen
	wantRedirect(t, e.do("POST", "/terminal-slides/"+b+"/delete", nil, ""), "deleted")
	if titles() != "CA2Nur Text" {
		t.Errorf("nach Löschen: %s", titles())
	}
	wantRedirect(t, e.do("POST", "/terminal-slides/"+b+"/delete", nil, ""), "deleted") // schon weg: kein Fehler
}

// Jede Änderung meldet den globalen Terminals „slides“.
func TestTerminalSlidesWebNotifiesGlobalTerminals(t *testing.T) {
	e := newSlidesEnv(t)
	ctx := context.Background()
	var uid string
	if err := e.pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, is_system_user)
		VALUES ($1::text, $1::text || '@x', 'x', 'G', 'T', 'viewer', '', '', true) RETURNING id::text`, fmt.Sprintf("slidesterm%d", time.Now().UnixNano())).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM terminal_settings WHERE user_id = $1::uuid`, uid)
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	if _, err := e.pool.Exec(ctx, `INSERT INTO terminal_settings (user_id, global_screen) VALUES ($1::uuid, true)
		ON CONFLICT (user_id) DO UPDATE SET global_screen = true`, uid); err != nil {
		t.Fatal(err)
	}
	ch, cancel := e.h.eventHub().subscribe(uid)
	defer cancel()
	drain := func() int {
		n := 0
		for {
			select {
			case ev := <-ch:
				if ev == "slides" {
					n++
				}
			case <-time.After(200 * time.Millisecond):
				return n
			}
		}
	}
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": "text", "title": "N", "body": "x", "active": "1"}, nil), "created")
	if drain() < 1 {
		t.Error("Anlegen meldet nichts")
	}
	id := e.slides(t)[0].ID
	wantRedirect(t, e.post("/terminal-slides/"+id, map[string]string{"kind": "text", "title": "N2", "body": "x", "active": "1"}, nil), "saved")
	if drain() < 1 {
		t.Error("Ändern meldet nichts")
	}
	wantRedirect(t, e.do("POST", "/terminal-slides/"+id+"/delete", nil, ""), "deleted")
	if drain() < 1 {
		t.Error("Löschen meldet nichts")
	}
}
