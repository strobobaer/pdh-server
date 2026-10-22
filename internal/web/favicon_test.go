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

func TestIconCSSCompressedAndCached(t *testing.T) {
	h := AppIconHandler()
	const url = "/vendor/tabler-icons/tabler-icons.min.css?v=1.2.3"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.Len() > 1200000 {
		t.Errorf("nicht komprimiert: %q, %d Bytes", rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control %q", cc)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("kein ETag")
	}
	// Erneute Anfrage mit ETag: 304 ohne Inhalt statt 1,4 MB
	req = httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("Revalidierung: HTTP %d, %d Bytes", rec.Code, rec.Body.Len())
	}
	// Ohne gzip: Rohdatei
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), "tabler-icons") {
		t.Errorf("unkomprimierte Auslieferung fehlerhaft")
	}
}
