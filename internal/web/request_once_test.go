package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestOnce(t *testing.T) {
	tok := "0192a4b0-7c1e-7d2a-9f00-1234567890ab"
	r := httptest.NewRequest("POST", "/tickets?_once="+tok, nil)
	if onceToken(r) != tok {
		t.Error("Schlüssel aus der Adresse")
	}
	r = httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-PDH-Once", "0192A4B0-7C1E-7D2A-9F00-1234567890AB")
	if onceToken(r) != tok {
		t.Error("Schlüssel aus der Kopfzeile (Groß-/Kleinschreibung egal)")
	}
	for _, bad := range []string{"", "abc", "'; DROP TABLE x;--", tok + "0"} {
		r = httptest.NewRequest("POST", "/x", nil)
		r.Header.Set("X-PDH-Once", bad)
		if onceToken(r) != "" {
			t.Errorf("%q akzeptiert", bad)
		}
	}

	// Doppelung: htmx unverändert, Formular zurück zur vorherigen Seite (nur gleiche Seite)
	w := httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://pdh/tickets", nil)
	r.Header.Set("HX-Request", "true")
	duplicateSubmit(w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("HX-Reswap") != "none" {
		t.Errorf("htmx: %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://pdh/tickets", nil)
	r.Header.Set("Referer", "http://pdh/tickets/new?x=1")
	duplicateSubmit(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/tickets/new?x=1" {
		t.Errorf("Formular: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://pdh/tickets", nil)
	r.Header.Set("Referer", "https://boese.example/phish")
	duplicateSubmit(w, r)
	if w.Header().Get("Location") != "/" {
		t.Errorf("fremder Referer: %s", w.Header().Get("Location"))
	}

	// ohne Datenbank bzw. ohne Schlüssel: durchreichen
	called := 0
	h := (&Handler{}).RequestOnceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x?_once="+tok, nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x?_once="+tok, nil))
	if called != 2 {
		t.Error("ohne Datenbank darf nichts blockiert werden")
	}

	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	_, _ = rec.Write([]byte("x"))
	rec.WriteHeader(500)
	if rec.status != 200 {
		t.Error("erster Status zählt")
	}
}
