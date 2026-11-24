package web

import (
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdh"
)

// TestNoExternalAssets: keine Vorlage, kein statisches Skript ausserhalb von
// web/static/vendor und kein Go-Code (ohne Tests) verweist auf die fünf
// Hosts (Bibliotheken und Schriften).
func TestNoExternalAssets(t *testing.T) {
	hosts := []string{"unpkg.com", "cdn.jsdelivr.net", "cdnjs.cloudflare.com", "fonts.googleapis.com", "fonts.gstatic.com"}
	root := filepath.Join("..", "..")
	scanned := 0
	for _, dir := range []string{"web/templates", "web/static", "internal", "cmd", "pkg"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel == "web/static/vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(rel, "internal/web/testdata/") {
				return nil
			}
			switch {
			case strings.HasPrefix(rel, "web/"):
			case strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go"):
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanned++
			for _, h := range hosts {
				if strings.Contains(string(b), h) {
					t.Errorf("%s verweist auf %s", rel, h)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	if scanned < 100 {
		t.Fatalf("nur %d Dateien geprueft – Suche greift ins Leere", scanned)
	}
}

var vendorFiles = map[string]string{
	"htmx/htmx.min.js":                 "text/javascript; charset=utf-8",
	"sortablejs/Sortable.min.js":       "text/javascript; charset=utf-8",
	"frappe-gantt/frappe-gantt.min.js": "text/javascript; charset=utf-8",
	"frappe-gantt/frappe-gantt.css":    "text/css; charset=utf-8",
	"chartjs/chart.umd.min.js":         "text/javascript; charset=utf-8",
	"xlsx/xlsx.full.min.js":            "text/javascript; charset=utf-8",
	"mammoth/mammoth.browser.min.js":   "text/javascript; charset=utf-8",
}

func TestVendorRoute(t *testing.T) {
	r := iconTestRouter()
	v := "/vendor/" + pdh.Version() + "/"
	for f, ct := range vendorFiles {
		rec := iconGet(r, v+f, map[string]string{"Accept-Encoding": "gzip"})
		if rec.Code != 200 {
			t.Errorf("%s: HTTP %d", f, rec.Code)
			continue
		}
		if rec.Header().Get("Content-Type") != ct {
			t.Errorf("%s: Content-Type %q", f, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control %q", f, rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") == "" {
			t.Errorf("%s: gzip/ETag fehlen", f)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: leer", f)
		}
	}
	for _, p := range []string{
		v + "htmx/..%2F..%2Fx",
		v + "htmx/nicht-da.js",
		v + "htmx/LICENSE.txt",
		v + "tabler-icons/LICENSE.txt",
		v + "novnc/rfb.min.js",
		v + "esp-web-tools/SHA256SUMS",
		v + "unbekannt/x.js",
		"/vendor/0.0.1/htmx/nicht-da.js",
		"/vendor/0.0.1/unbekannt/x.js",
	} {
		rec := iconGet(r, p, nil)
		if rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: HTTP %d, Cache-Control %q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	// fremde Version, bekannte Datei: Weiterleitung auf die laufende Version,
	// nie zwischenspeichern (Seiten, die ueber ein Update offen blieben)
	rec0 := iconGet(r, "/vendor/0.0.1/htmx/htmx.min.js", nil)
	if rec0.Code != http.StatusFound || rec0.Header().Get("Cache-Control") != "no-store" ||
		rec0.Header().Get("Location") != v+"htmx/htmx.min.js" {
		t.Errorf("fremde Version: HTTP %d, Cache-Control %q, Location %q", rec0.Code, rec0.Header().Get("Cache-Control"), rec0.Header().Get("Location"))
	}
	// zu viele Pfadteile passen zu keiner Route: 404 (vom Router, ohne Datei)
	if rec := iconGet(r, v+"htmx/../../x", nil); rec.Code != 404 {
		t.Errorf("Pfad mit ..: HTTP %d", rec.Code)
	}
	// die feste Adresse /icons/ gewinnt gegen {lib} (echte Reihenfolge der Routen)
	rec := iconGet(r, v+"icons/ti-plus.svg", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("icons/ti-plus.svg: HTTP %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if iconGet(r, v+"icons-abc.css", nil).Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("icons-{key}.css wird nicht als Stildatei geliefert")
	}
	// icons-rest-{key}.css geht an die Rest-Datei, nicht an die Hauptdatei (Kennung „rest-…“)
	rest := iconGet(r, v+"icons-rest-"+iconCSSKey(usedIconNames())+".css", nil)
	if rest.Code != 200 || strings.Contains(rest.Body.String(), ".ti::before") || !strings.Contains(rest.Body.String(), "/icons/ti-") {
		t.Errorf("icons-rest-{key}.css wird nicht als Rest-Datei geliefert: HTTP %d", rest.Code)
	}
}

func TestVendorURLFunc(t *testing.T) {
	f, ok := TemplateFuncs()["vendorURL"].(func(string) string)
	if !ok {
		t.Fatal("vendorURL fehlt in TemplateFuncs")
	}
	if got, want := f("htmx/htmx.min.js"), "/vendor/"+pdh.Version()+"/htmx/htmx.min.js"; got != want {
		t.Errorf("vendorURL = %q, want %q", got, want)
	}
	// in JS-Zeichenkette bleibt die Adresse nutzbar
	tpl := template.Must(template.New("x").Funcs(TemplateFuncs()).Parse(`<script>lib('{{vendorURL "xlsx/xlsx.full.min.js"}}','XLSX')</script>`))
	var sb strings.Builder
	if err := tpl.Execute(&sb, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(sb.String(), `\/`, "/"), "/vendor/"+pdh.Version()+"/xlsx/xlsx.full.min.js") {
		t.Errorf("JS-Kontext: %s", sb.String())
	}
}

// TestVendorFilesPinned: jede Datei nennt die festgelegte Version.
func TestVendorFilesPinned(t *testing.T) {
	want := map[string]string{
		"htmx/htmx.min.js":               `version:"1.9.12"`,
		"sortablejs/Sortable.min.js":     "Sortable 1.15.2",
		"chartjs/chart.umd.min.js":       `static version="4.4.0"`,
		"xlsx/xlsx.full.min.js":          `e.version="0.18.5"`,
		"mammoth/mammoth.browser.min.js": "mammoth",
	}
	for f, marker := range want {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "vendor", filepath.FromSlash(f)))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !strings.Contains(string(b), marker) {
			t.Errorf("%s nennt %q nicht", f, marker)
		}
	}
	// die Lizenztexte liegen daneben, und die Liste nennt jede Bibliothek
	lic, err := os.ReadFile(filepath.Join("..", "..", "THIRD_PARTY_LICENSES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range []string{"htmx", "sortablejs", "frappe-gantt", "chartjs", "xlsx", "mammoth"} {
		if _, err := os.Stat(filepath.Join("..", "..", "web", "static", "vendor", lib, "LICENSE.txt")); err != nil {
			t.Errorf("Lizenztext %s: %v", lib, err)
		}
		if !strings.Contains(string(lic), "web/static/vendor/"+lib+"/LICENSE.txt") {
			t.Errorf("THIRD_PARTY_LICENSES.md nennt %s nicht", lib)
		}
	}
}
