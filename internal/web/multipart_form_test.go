package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// buildMultipartRequest baut eine echte multipart/form-data-Anfrage, wie
// sie new FormData(form) im Browser erzeugt (siehe users.gohtml
// submitUserForm) - kein Mock, echtes net/http/httptest + mime/multipart.
func buildMultipartRequest(fields map[string]string) *http.Request {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/users/save-web", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestParseFormDoesNotReadMultipartBody dokumentiert die Ursache des
// Bugs, der UserSaveWeb kaputt gemacht hat: r.ParseForm() liest den
// Body NUR bei application/x-www-form-urlencoded - bei multipart/
// form-data (das new FormData(form) im Browser erzeugt) bleiben alle
// Felder leer, ohne dass ein Fehler zurueckkommt. Regressionsschutz
// dafuer, dass niemand UserSaveWeb versehentlich wieder auf
// r.ParseForm() zurueckstellt.
func TestParseFormDoesNotReadMultipartBody(t *testing.T) {
	req := buildMultipartRequest(map[string]string{
		"username": "mmuster", "email": "m@example.com",
		"first_name": "Max", "last_name": "Mustermann",
	})
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if got := req.FormValue("username"); got != "" {
		t.Fatalf("ParseForm should NOT populate fields from a multipart body, got username=%q", got)
	}
}

// TestParseMultipartFormReadsMultipartBody bestaetigt die Korrektur:
// r.ParseMultipartForm() liest denselben Body korrekt.
func TestParseMultipartFormReadsMultipartBody(t *testing.T) {
	req := buildMultipartRequest(map[string]string{
		"username": "mmuster", "email": "m@example.com",
		"first_name": "Max", "last_name": "Mustermann",
	})
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	cases := map[string]string{
		"username": "mmuster", "email": "m@example.com",
		"first_name": "Max", "last_name": "Mustermann",
	}
	for field, want := range cases {
		if got := req.FormValue(field); got != want {
			t.Errorf("FormValue(%q) = %q, want %q", field, got, want)
		}
	}
}
