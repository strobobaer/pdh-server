package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Kennungen in anderer Schreibweise (32 Hexziffern, Klammern, urn:uuid:) werden früh abgewiesen,
// bevor Abteilungs- und Rechteprüfung den Pfad ansehen (uuid_path.go). Die groß geschriebene
// Normalform wird auf die Kleinschreibung umgeleitet (GET/HEAD 301, sonst 308).
func TestNonCanonicalUUIDPathRejected(t *testing.T) {
	const id = "01a18200-0000-7000-8000-000000000001"
	const upper = "01A18200-0000-7000-8000-000000000001"
	const hex = "01a18200000070008000000000000001"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := NonCanonicalUUIDPathMiddleware(ok)
	do := func(method, p, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", nil)
		r.URL.Path, r.URL.RawQuery = p, query
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, p := range []string{
		"/projects/" + hex, "/api/v1/projects/" + hex, "/api/v1/projects/{" + id + "}", "/api/v1/projects/{" + hex + "}",
		"/api/v1/projects/urn:uuid:" + id, "/api/v1/projects/URN:UUID:" + id, "/tickets/" + hex + "/edit",
		"/api/v1/attachments/kvp/" + hex, "/records/fault/" + hex, "/projects/01a18200-0000-7000-80000000-00000001",
		"/projects/01A18200000070008000000000000001", "/certificates/" + hex + "/p12",
	} {
		if c := do("GET", p, "").Code; c != http.StatusNotFound {
			t.Errorf("%s: %d, erwartet 404", p, c)
		}
	}
	// Schnittstelle: JSON statt Text
	w := do("GET", "/api/v1/projects/"+hex, "")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || strings.TrimSpace(w.Body.String()) != `{"success":false,"error":"nicht gefunden"}` {
		t.Errorf("API-Antwort: %q %q", ct, w.Body.String())
	}
	if ct := do("GET", "/projects/"+hex, "").Header().Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		t.Errorf("Web-Antwort sollte kein JSON sein: %q", ct)
	}
	// groß geschriebene Normalform: GET/HEAD -> 301 auf Kleinschreibung, Abfrage bleibt
	for _, m := range []string{"GET", "HEAD"} {
		w := do(m, "/api/v1/projects/"+upper+"/tasks", "a=1&b=X")
		if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/api/v1/projects/"+id+"/tasks?a=1&b=X" {
			t.Errorf("%s groß: %d %q", m, w.Code, w.Header().Get("Location"))
		}
	}
	if w := do("GET", "/records/"+upper+"/x/"+upper, ""); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/records/"+id+"/x/"+id {
		t.Errorf("mehrere Segmente: %d %q", w.Code, w.Header().Get("Location"))
	}
	// Kein offener Redirect: nie auf einen anderen Rechner, nie „//“ oder „/\“ am Anfang
	for _, p := range []string{"//evil.example/" + upper, "///evil.example/" + upper, `/\evil.example/` + upper, "/projects//" + upper} {
		w := do("GET", p, "")
		if loc := w.Header().Get("Location"); strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) || strings.Contains(loc, "evil.example") {
			t.Errorf("%q leitet auf %q um", p, loc)
		}
	}
	if c := do("GET", "//evil.example/"+upper, "").Code; c != http.StatusNotFound {
		t.Errorf("// am Anfang: %d, erwartet 404", c)
	}
	if c := do("GET", `/\evil.example/`+upper, "").Code; c != http.StatusNotFound {
		t.Errorf(`/\ am Anfang: %d, erwartet 404`, c)
	}
	// kodierter Schrägstrich: bleibt auf dem Rechner (404), wird nicht zu „//evil.example“
	{
		r := httptest.NewRequest("GET", "/%2F%2Fevil.example/"+upper, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if loc := w.Header().Get("Location"); w.Code != http.StatusNotFound || loc != "" {
			t.Errorf("%%2F: %d %q", w.Code, loc)
		}
	}
	if w := do("GET", "/api/v1//evil.example/"+upper, ""); w.Code == http.StatusMovedPermanently && !strings.HasPrefix(w.Header().Get("Location"), "/api/") {
		t.Errorf("API: %q", w.Header().Get("Location"))
	}
	// andere Methoden: 308 (Methode und Inhalt bleiben), dieselben Regeln gegen fremde Rechner
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := do(m, "/api/v1/projects/"+upper+"/edit", "x=1")
		if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != "/api/v1/projects/"+id+"/edit?x=1" {
			t.Errorf("%s groß: %d %q, erwartet 308 auf Kleinschreibung", m, w.Code, w.Header().Get("Location"))
		}
		for _, p := range []string{"//evil.example/" + upper, `/\evil.example/` + upper} {
			if w := do(m, p, ""); w.Code != http.StatusNotFound || w.Header().Get("Location") != "" {
				t.Errorf("%s %q: %d %q, erwartet 404 ohne Umleitung", m, p, w.Code, w.Header().Get("Location"))
			}
		}
		r := httptest.NewRequest(m, "/%2F%2Fevil.example/"+upper, nil)
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, r)
		if rw.Code != http.StatusNotFound || rw.Header().Get("Location") != "" {
			t.Errorf("%s %%2F: %d %q", m, rw.Code, rw.Header().Get("Location"))
		}
		if w := do(m, "/projects//"+upper, ""); w.Code == http.StatusPermanentRedirect && !strings.HasPrefix(w.Header().Get("Location"), "/projects/") {
			t.Errorf("%s doppelter Schrägstrich: %q", m, w.Header().Get("Location"))
		}
	}
	for _, p := range []string{
		"/projects/" + id, "/api/v1/projects/" + id, "/projects", "/api/v1/projects/", "/tickets/" + id + "/edit",
		// Bereiche mit Dateinamen/Prüfsummen bleiben unberührt
		"/uploads/" + hex + ".png", "/static/" + hex + ".js", "/global/b/" + hex, "/pki/ca.crt",
		"/import/esppdh/" + id + "/firmware/" + id + "/factory.bin",
	} {
		if c := do("GET", p, "").Code; c != http.StatusOK {
			t.Errorf("%s: %d, erwartet 200", p, c)
		}
	}
}

// Durch die echte Reihenfolge der Prüfungen (Middleware vor Rechte-Sperre und Abteilungsfilter, API
// unter /api/v1 mit APIDepartmentScope): die Schreibweisen kommen gar nicht erst bis zum Handler.
func TestNonCanonicalUUIDPathThroughChain(t *testing.T) {
	const id = "01a18200-0000-7000-8000-000000000001"
	h := &Handler{}
	reached := false
	r := chi.NewRouter()
	r.Use(NonCanonicalUUIDPathMiddleware, h.PermissionGateMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(h.APIDepartmentScope)
		r.Get("/projects/{id}", func(w http.ResponseWriter, r *http.Request) { reached = true })
	})
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path = p
		r.ServeHTTP(w, req)
		return w
	}
	if w := get("/api/v1/projects/01a18200000070008000000000000001"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"success":false`) || reached {
		t.Errorf("32 Hex: %d %q erreicht=%v", w.Code, w.Body.String(), reached)
	}
	if w := get("/api/v1/projects/01A18200-0000-7000-8000-000000000001"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/api/v1/projects/"+id || reached {
		t.Errorf("groß: %d %q erreicht=%v", w.Code, w.Header().Get("Location"), reached)
	}
}
