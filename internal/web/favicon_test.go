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
	resetIconCSSCache()
	r := iconTestRouter()
	_, _ = (&Handler{}).iconCSS(t.Context())
	url := iconCSSHref()
	rec := iconGet(r, url, map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	// 5 918 Regeln, die meisten nur als Adresse: klein genug fuer den ersten Aufruf
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.Len() > 300000 {
		t.Errorf("nicht komprimiert oder zu gross: %q, %d Bytes", rec.Header().Get("Content-Encoding"), rec.Body.Len())
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
	// Erneute Anfrage mit ETag: 304 ohne Inhalt
	rec = iconGet(r, url, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("Revalidierung: HTTP %d, %d Bytes", rec.Code, rec.Body.Len())
	}
	// Ohne gzip: Rohdatei mit den Symbolregeln
	rec = iconGet(r, url, nil)
	if rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), ".ti::before") {
		t.Errorf("unkomprimierte Auslieferung fehlerhaft")
	}
}
