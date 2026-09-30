package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppIconsServed(t *testing.T) {
	h := AppIconHandler()
	want := map[string]string{".ico": "image/", ".svg": "image/svg+xml", ".png": "image/png", ".webmanifest": "application/manifest+json"}
	for _, name := range AppIconFiles {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("/%s: HTTP %d, %d Bytes", name, rec.Code, rec.Body.Len())
			continue
		}
		ext := name[strings.LastIndex(name, "."):]
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, want[ext]) {
			t.Errorf("/%s: Content-Type %q", name, ct)
		}
	}
}
