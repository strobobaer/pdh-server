package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pdh"
)

var fontURLRe = regexp.MustCompile(`url\(([^)]*)\)`)

// TestFontsLocal: jede Webschrift der Darstellung kommt vom PDH-Server, ihre
// Stildatei verweist nur auf mitgelieferte Dateien, und diese werden mit dem
// richtigen Typ ausgeliefert.
func TestFontsLocal(t *testing.T) {
	r := iconTestRouter()
	dir := filepath.Join("..", "..", "web", "static", "vendor", "fonts")
	n := 0
	for _, f := range uiFonts {
		if f.Href == "" {
			continue
		}
		n++
		want := "/vendor/" + pdh.Version() + "/fonts/" + f.ID + ".css"
		if got := f.URL(); got != want {
			t.Errorf("%s: URL = %q, want %q", f.ID, got, want)
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, f.ID+".css"))
		if err != nil {
			t.Errorf("%s: %v", f.ID, err)
			continue
		}
		css := string(b)
		if strings.Contains(css, "http") {
			t.Errorf("%s.css enthaelt eine Internetadresse", f.ID)
		}
		urls := fontURLRe.FindAllStringSubmatch(css, -1)
		if len(urls) == 0 || !strings.Contains(css, "unicode-range") {
			t.Errorf("%s.css: keine Schriftdateien oder kein unicode-range", f.ID)
		}
		for _, m := range urls {
			name := m[1]
			if !vendorFileRe.MatchString(name) || !strings.HasSuffix(name, ".woff2") {
				t.Errorf("%s.css: url(%s) ist kein lokaler Dateiname", f.ID, name)
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("%s.css: %v", f.ID, err)
				continue
			}
			rec := iconGet(r, "/vendor/"+pdh.Version()+"/fonts/"+name, nil)
			if rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" ||
				rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Errorf("%s: HTTP %d, %q, %q", name, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
			}
		}
		rec := iconGet(r, want, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
			t.Errorf("%s: HTTP %d, %q", want, rec.Code, rec.Header().Get("Content-Type"))
		}
		// Lizenztext liegt daneben, wird aber nicht ausgeliefert
		lic := "OFL-" + f.ID + ".txt"
		if _, err := os.Stat(filepath.Join(dir, lic)); err != nil {
			t.Errorf("Lizenztext %s: %v", lic, err)
		}
		if rec := iconGet(r, "/vendor/"+pdh.Version()+"/fonts/"+lic, nil); rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s wird ausgeliefert: HTTP %d", lic, rec.Code)
		}
	}
	if n != 5 {
		t.Errorf("%d Webschriften geprueft, erwartet 5", n)
	}
	// die Seiten- und Vorschau-Adressen sind die lokalen
	if got := (Appearance{FontID: "dm-sans"}).FontHref(); !strings.HasPrefix(got, "/vendor/"+pdh.Version()+"/fonts/") {
		t.Errorf("FontHref = %q", got)
	}
	if js := string((Appearance{}).FontsJS()); strings.Contains(js, "googleapis") || !strings.Contains(js, "/vendor/") {
		t.Errorf("FontsJS: %s", js)
	}
}
