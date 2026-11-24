package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh"
)

// renderStandalone rendert eine Seite mit eigenem Seitenkopf mit leeren
// Daten. Fehler beim Ausfuehren werden hingenommen (leere Daten), solange der
// Kopf der Seite – dort steht die Symbol-Stildatei – schon geschrieben ist.
func renderStandalone(t *testing.T, file, name string, data interface{}) string {
	t.Helper()
	i18nDir = filepath.Join("..", "..", "web", "i18n")
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	c = bindLang(c, "de")
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", file)); err != nil {
		t.Fatalf("%s parse: %v", file, err)
	}
	var buf bytes.Buffer
	_ = c.ExecuteTemplate(&buf, name, data)
	return buf.String()
}

// Alle Seiten mit eigenem oder gemeinsamem Kopf laden die neue Stildatei
// /vendor/<Version>/icons-<Kennung>.css, nicht mehr die Symbolschrift.
func TestPagesUseIconCSS(t *testing.T) {
	want := `href="/vendor/` + pdh.Version() + `/icons-`
	pages := map[string]string{
		"base":                  renderPage(t, loadTestTemplates(t), "icons", IconsPageData{}),
		"easy":                  renderStandalone(t, "easy.gohtml", "easy.gohtml", EasyPageData{}),
		"global_dashboard":      renderStandalone(t, "global_dashboard.gohtml", "global_dashboard.gohtml", GlobalDashboardPageData{}),
		"push_alert":            renderStandalone(t, "push_alert.gohtml", "push_alert.gohtml", pushAlertView{}),
		"terminal":              renderStandalone(t, "terminal.gohtml", "terminal.gohtml", terminalPage{}),
		"terminal_device_error": renderStandalone(t, "terminal_device_error.gohtml", "terminal_device_error.gohtml", terminalDeviceErrorPage{}),
	}
	for name, out := range pages {
		if !strings.Contains(out, want) {
			t.Errorf("%s: Stildatei %q fehlt", name, want)
		}
		if strings.Contains(out, "tabler-icons.min.css") {
			t.Errorf("%s: verweist noch auf tabler-icons.min.css", name)
		}
	}
}

// Die alte Adresse bleibt als Weiterleitung auf die aktuelle Stildatei (alte
// Zwischenspeicher, eingebettete Seiten); alles andere unter dem Pfad ist weg.
func TestOldIconCSSRedirects(t *testing.T) {
	r := chi.NewRouter()
	r.Handle("/vendor/tabler-icons/*", http.HandlerFunc(LegacyIconVendorWeb))
	for _, p := range []string{"/vendor/tabler-icons/tabler-icons.min.css?v=0.82.55", "/vendor/tabler-icons/tabler-icons.min.css"} {
		rec := iconGet(r, p, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != iconCSSHref() || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d Location=%q Cache-Control=%q", p, rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"))
		}
	}
	for _, p := range []string{"/vendor/tabler-icons/names.txt", "/vendor/tabler-icons/aliases.txt", "/vendor/tabler-icons/LICENSE.txt", "/vendor/tabler-icons/tabler-nodes-outline.json", "/vendor/tabler-icons/x"} {
		rec := iconGet(r, p, nil)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q, erwartet 404 no-store", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}

func readTemplate(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Kein Rueckfall mehr auf Schriftzeichen: ohne Symbolschrift gibt es keinen
// Schriftpruefer und keine Ersatzzeichen.
func TestNoIconFontFallback(t *testing.T) {
	b := readTemplate(t, "base.gohtml")
	for _, bad := range []string{"ensureIconFont", "icons-fallback", `"tabler-icons"`} {
		if strings.Contains(b, bad) {
			t.Errorf("base.gohtml enthaelt noch %q", bad)
		}
	}
}

var (
	iconFontFamilyRe = regexp.MustCompile(`(?i)font-family\s*:[^;}]*tabler`)
	iconCodepointRe  = regexp.MustCompile(`(?i)content\s*:\s*["']\\[ef][0-9a-f]{3}["']`)
)

// Weder Vorlagen noch eigene Dateien unter web/static nutzen die Symbolschrift
// (font-family tabler …) oder Zeichen aus ihr (content:"\eac5").
func TestNoIconFontUse(t *testing.T) {
	root := filepath.Join("..", "..", "web")
	scanned := 0
	for _, sub := range []string{"templates", "static"} {
		err := filepath.WalkDir(filepath.Join(root, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".gohtml", ".html", ".css", ".js", ".svg":
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanned++
			if m := iconFontFamilyRe.Find(b); m != nil {
				t.Errorf("%s: Symbolschrift %q", p, m)
			}
			if m := iconCodepointRe.Find(b); m != nil {
				t.Errorf("%s: Zeichen aus der Symbolschrift %q", p, m)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	if scanned < 50 {
		t.Fatalf("nur %d Dateien geprueft – Pfad falsch oder Suche leer", scanned)
	}
}

// Die Infoleiste zeichnet ihr Symbol mit der Maske, nicht mit der Schrift.
func TestInfobarUsesIconMask(t *testing.T) {
	b := readTemplate(t, filepath.Join("widgets", "work_board.gohtml"))
	for _, want := range []string{"/icons/ti-info-circle.svg", "/icons/ti-circle-check.svg", "/icons/ti-alert-triangle.svg", "/icons/ti-alert-circle.svg"} {
		if !strings.Contains(b, want) {
			t.Errorf("work_board.gohtml: %s fehlt", want)
		}
	}
	// Kontrastmodus: Systemfarbe statt Akzent-/Statusfarbe, sonst ist das Symbol unsichtbar
	fc := regexp.MustCompile(`@media \(forced-colors:\s*active\)\{[^@]*\.w11-infobar::before[^@]*\.w11-infobar\.success::before[^@]*\.w11-infobar\.warning::before[^@]*\.w11-infobar\.error::before\{[^}]*background-color:CanvasText`)
	if !fc.MatchString(b) {
		t.Error("work_board.gohtml: Kontrastmodus-Regel (forced-colors, CanvasText) fuer die Infoleiste fehlt")
	}
	used := map[string]bool{}
	for _, n := range usedIconNames() {
		used[n] = true
	}
	for _, n := range []string{"ti-info-circle", "ti-circle-check", "ti-alert-triangle", "ti-alert-circle"} {
		if !used[n] {
			t.Errorf("%s fehlt in icons_used.txt", n)
		}
	}
}

// Die Symbolauswahl mit der ganzen Liste laedt nur sichtbare Symbole.
func TestIconPickerLazy(t *testing.T) {
	out := renderPage(t, loadTestTemplates(t), "icons", IconsPageData{})
	m := regexp.MustCompile(`\.ic-grid button\{[^}]*\}`).FindString(out)
	if !strings.Contains(m, "content-visibility:auto") || !strings.Contains(m, "contain-intrinsic-size") {
		t.Errorf("Kacheln der Symbolauswahl ohne content-visibility/contain-intrinsic-size: %q", m)
	}
}
