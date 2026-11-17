package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Während eines Updates liegt die neue Seitendatei schon auf der Platte, das Programm kennt
// den neuen Baustein noch nicht: Dann kommt ein Hinweis statt „Template-Fehler“.
func TestRenderStale(t *testing.T) {
	tm := template.Must(template.New("base.gohtml").Parse(`<main>{{template "fehlt" .}}</main>`))
	w := httptest.NewRecorder()
	err := tm.ExecuteTemplate(w, "base.gohtml", nil)
	if err == nil || !renderStale(err) {
		t.Fatalf("fehlender Baustein nicht erkannt: %v", err)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("vor dem Fehler wurde schon ausgegeben: %q", w.Body.String())
	}
	_, ferr := template.New("p").Parse(`{{neueFunktion .}}`)
	if !renderStale(ferr) {
		t.Fatalf("fehlende Funktion nicht erkannt: %v", ferr)
	}
	for _, e := range []error{nil, errors.New("template: x:1:2: executing \"x\" at <.A>: can't evaluate field A"), errors.New("unexpected EOF")} {
		if renderStale(e) {
			t.Errorf("echter Fehler als Update gewertet: %v", e)
		}
	}

	w = httptest.NewRecorder()
	renderUpdating(w)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "15" ||
		!strings.Contains(w.Body.String(), "wird gerade aktualisiert") || !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Fatalf("Hinweisseite: %d %q", w.Code, w.Body.String())
	}
}
