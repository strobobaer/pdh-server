package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh"
	"pdh/internal/web/tablericons"
)

// Ersatz fuer TestIconCSSHasRuleForEveryName: iconcss_rest_test.go
// (TestIconCSSSplitCoversEveryName).

func TestIconCSSEmbedsUsed(t *testing.T) {
	set := tablericons.Default()
	used := usedIconNames()
	if len(used) < 300 {
		t.Fatalf("nur %d genutzte Symbole", len(used))
	}
	mainCSS, restCSS := buildIconCSS(set, used, "9.9.9")
	css := string(mainCSS)
	for _, n := range used {
		if !strings.Contains(css, "\n."+n+`{--ti:url("data:image/svg+xml,`) {
			t.Errorf("%s nicht eingebettet", n)
		}
	}
	if !strings.Contains(string(restCSS), ".ti-ad-2{--ti:url(/vendor/9.9.9/icons/ti-ad-2.svg)}") {
		t.Error("ti-ad-2 soll als Adresse in der Rest-Datei stehen")
	}
}

var iconRefRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_-])(ti-[a-z0-9-]+)`)

// iconUseRe: „ti ti-name“ in Klassenlisten und Zeichenketten (Namen mit nachfolgender
// Vorlagenaktion oder Verkettung, z. B. „ti ti-{{…}}“, werden nicht erfasst).
var iconUseRe = regexp.MustCompile(`\bti (ti-[a-z0-9]+(?:-[a-z0-9]+)*)(?:["'\s}]|$)`)

// notIcons: Klassen mit Praefix ti-, die keine Symbole sind.
var notIcons = map[string]bool{}

// TestIconsUsedListComplete: jede im Quelltext vorkommende, bekannte Symbolklasse
// steht in icons_used.txt (sonst wuerde sie erst beim Anzeigen nachgeladen).
func TestIconsUsedListComplete(t *testing.T) {
	set := tablericons.Default()
	listed := map[string]bool{}
	for _, n := range usedIconNames() {
		listed[n] = true
	}
	root := filepath.Join("..", "..")
	check := func(file string) {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range iconRefRe.FindAllStringSubmatch(string(b), -1) {
			if set.Has(m[1]) && !listed[m[1]] {
				t.Errorf("%s: %s fehlt in icons_used.txt", file, m[1])
			}
		}
		// als Symbol verwendet („ti ti-…“), aber die Bibliothek kennt es nicht:
		// es bliebe unsichtbar (Tippfehler wie ti-tool-off statt ti-tools-off)
		for _, m := range iconUseRe.FindAllStringSubmatch(string(b), -1) {
			if !set.Has(m[1]) && !notIcons[m[1]] {
				t.Errorf("%s: %s ist als Symbol verwendet, steht aber nicht in der Bibliothek", file, m[1])
			}
		}
	}
	_ = filepath.WalkDir(filepath.Join(root, "web", "templates"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".gohtml") {
			check(p)
		}
		return nil
	})
	_ = filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			check(p)
		}
		return nil
	})
	js, _ := filepath.Glob(filepath.Join(root, "web", "static", "*.js"))
	for _, p := range js {
		check(p)
	}
}

// TestIconCSSFallbackAndHidden: ohne passende ti-…-Klasse (unbekannter oder leerer
// Name) darf kein gefuelltes Quadrat erscheinen, und das hidden-Attribut gilt weiter.
func TestIconCSSFallbackAndHidden(t *testing.T) {
	mainCSS, _ := buildIconCSS(tablericons.Default(), nil, "9.9.9")
	css := string(mainCSS)
	fb := "var(--ti,linear-gradient(transparent,transparent)) center/contain no-repeat"
	for _, want := range []string{"-webkit-mask:" + fb, ";mask:" + fb, ".ti[hidden]{display:none}"} {
		if !strings.Contains(css, want) {
			t.Errorf("Stildatei ohne %q", want)
		}
	}
}

func TestIconCSSPrintAndContrast(t *testing.T) {
	mainCSS, _ := buildIconCSS(tablericons.Default(), nil, "9.9.9")
	css := string(mainCSS)
	for _, want := range []string{"print-color-adjust:exact", "forced-colors"} {
		if !strings.Contains(css, want) {
			t.Errorf("Stildatei ohne %q", want)
		}
	}
}

// TestVendorRequestsQuiet: erfolgreiche Anfragen an /vendor/ werden nicht auf
// Stufe Info protokolliert (die Symbolauswahl holt bis zu 300 Symbole je Suche).
func TestVendorRequestsQuiet(t *testing.T) {
	for _, p := range []string{"/vendor/0.82.57/icons/ti-plus.svg", "/vendor/0.82.57/htmx/htmx.min.js", "/vendor/0.82.57/icons-abc.css", "/vendor/0.82.57/icons-rest-abc.css"} {
		if !quietPath(p) {
			t.Errorf("%s soll leise protokolliert werden", p)
		}
	}
}

func iconTestRouter() http.Handler {
	r := chi.NewRouter()
	RegisterVendorRoutes(r, &Handler{})
	return r
}

func iconGet(r http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestIconRoutesCaching(t *testing.T) {
	resetIconCSSCache()
	r := iconTestRouter()
	v := "/vendor/" + pdh.Version()

	rec := iconGet(r, v+"/icons/ti-plus.svg", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("SVG: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	for _, p := range []string{v + "/icons/ti-gibt-es-nicht.svg", "/vendor/0.0.1/icons/ti-gibt-es-nicht.svg"} {
		rec = iconGet(r, p, nil)
		if rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	// fremde Version, bekannter Name: Weiterleitung auf die laufende Version, nie zwischenspeichern
	_, _ = (&Handler{}).iconCSS(t.Context()) // Kennung festlegen, bevor die Zieladresse gelesen wird
	for p, want := range map[string]string{
		"/vendor/0.0.1/icons/ti-plus.svg": v + "/icons/ti-plus.svg",
		"/vendor/0.0.1/icons-abc.css":     iconCSSHref(),
	} {
		rec = iconGet(r, p, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q -> %q, erwartet %q", p, rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Location"), want)
		}
	}

	_, key := (&Handler{}).iconCSS(t.Context())
	href := iconCSSHref()
	if href != v+"/icons-"+key+".css" {
		t.Errorf("iconCSSHref = %s, key %s", href, key)
	}
	rec = iconGet(r, href, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") == "" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("CSS: %d %v", rec.Code, rec.Header())
	}
	rec2 := iconGet(r, href, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": rec.Header().Get("ETag")})
	if rec2.Code != http.StatusNotModified {
		t.Errorf("ETag: %d, erwartet 304", rec2.Code)
	}
	rec = iconGet(r, v+"/icons-0000000000.css", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("fremde Kennung: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
