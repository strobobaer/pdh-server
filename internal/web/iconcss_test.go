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

func TestIconCSSHasRuleForEveryName(t *testing.T) {
	set := tablericons.Default()
	css := string(buildIconCSS(set, usedIconNames(), "9.9.9"))
	for _, n := range set.Names() {
		if c := strings.Count(css, "\n."+n+"{--ti:"); c != 1 {
			t.Errorf("%s: %d Regeln, erwartet 1", n, c)
		}
	}
}

func TestIconCSSEmbedsUsed(t *testing.T) {
	set := tablericons.Default()
	used := usedIconNames()
	if len(used) < 300 {
		t.Fatalf("nur %d genutzte Symbole", len(used))
	}
	css := string(buildIconCSS(set, used, "9.9.9"))
	for _, n := range used {
		if !strings.Contains(css, "\n."+n+`{--ti:url("data:image/svg+xml,`) {
			t.Errorf("%s nicht eingebettet", n)
		}
	}
	if !strings.Contains(css, "\n.ti-ad-2{--ti:url(/vendor/9.9.9/icons/ti-ad-2.svg)}") {
		t.Error("ti-ad-2 soll als Adresse stehen")
	}
}

var iconRefRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_-])(ti-[a-z0-9-]+)`)

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

func TestIconCSSPrintAndContrast(t *testing.T) {
	css := string(buildIconCSS(tablericons.Default(), nil, "9.9.9"))
	for _, want := range []string{"print-color-adjust:exact", "forced-colors"} {
		if !strings.Contains(css, want) {
			t.Errorf("Stildatei ohne %q", want)
		}
	}
}

func iconTestRouter() http.Handler {
	h := &Handler{}
	r := chi.NewRouter()
	r.Get("/vendor/{ver}/icons-{key}.css", h.IconCSSWeb)
	r.Get("/vendor/{ver}/icons/{name}.svg", h.IconSVGWeb)
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
	for _, p := range []string{v + "/icons/ti-gibt-es-nicht.svg", "/vendor/0.0.1/icons/ti-plus.svg", "/vendor/0.0.1/icons-abc.css"} {
		rec = iconGet(r, p, nil)
		if rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Header().Get("Cache-Control"))
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
