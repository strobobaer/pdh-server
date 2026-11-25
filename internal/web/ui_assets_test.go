package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pdh"
)

// Dateien der Oberfläche unter /vendor/<Version>/ui/ (ui_assets.go).
func TestUIAssetServed(t *testing.T) {
	r := iconTestRouter()
	rec := iconGet(r, "/vendor/"+pdh.Version()+"/ui/pdh.css", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("pdh.css: HTTP %d, %q, %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	// Seite blieb über ein Update offen: alte Version → laufende, nie zwischengespeichert
	rec = iconGet(r, "/vendor/0.0.0/ui/pdh.css", nil)
	if rec.Code != 302 || rec.Header().Get("Location") != uiURL("pdh.css") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("fremde Version: HTTP %d, Location %q, %q", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"))
	}
	v := "/vendor/" + pdh.Version() + "/ui/"
	for _, p := range []string{v + "fehlt.css", v + "..%2Fx.css", v + "pdh.txt", "/vendor/0.0.0/ui/fehlt.css"} {
		if rec := iconGet(r, p, nil); rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: HTTP %d, %q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}

// renderBaseForTest: Grundseite mit einer schlichten Seite darin.
func renderBaseForTest(t *testing.T) string {
	t.Helper()
	d := ServerConfigData{EnvFile: ".env", FileWritable: true}
	d.Title = "Server-Einstellungen"
	d.Look = d.Brand.DefaultLook()
	return renderPage(t, loadTestTemplates(t), "server_config", d)
}

var styleBlockRe = regexp.MustCompile(`(?s)<style([^>]*)>(.*?)</style>`)

func TestBaseLinksUIStylesheet(t *testing.T) {
	out := renderBaseForTest(t)
	head, _, ok := strings.Cut(out, "</head>")
	if !ok {
		t.Fatal("kein </head>")
	}
	link := `<link rel="stylesheet" href="` + uiURL("pdh.css") + `">`
	i, j := strings.Index(head, link), strings.Index(head, `<style id="pdh-theme">`)
	if i < 0 || j < 0 || i > j {
		t.Errorf("Stilverweis fehlt im Kopf oder steht nach pdh-theme (%d, %d)", i, j)
	}
	// Der Rahmen selbst (Quelltext von base.gohtml) hat keinen großen Inline-Stil
	// mehr; Bausteine einzelner Seiten zählen hier nicht.
	for _, m := range styleBlockRe.FindAllStringSubmatch(baseSource(t), -1) {
		if len(m[2]) > 1500 && !strings.Contains(m[1], `id="pdh-theme"`) && !strings.Contains(m[1], `id="pdh-ui"`) {
			t.Errorf("Inline-Stil in base.gohtml mit %d Byte (%s)", len(m[2]), strings.TrimSpace(m[1]))
		}
	}
}

// baseWithUI: Quelltext von base.gohtml und aller ausgelagerten Dateien der
// Oberfläche (web/static/ui/*.css, *.js, sortiert) – für Tests, die prüfen, ob
// eine Regel oder Funktion im Rahmen vorhanden ist, egal ob inline oder ausgelagert.
func baseWithUI(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(baseSource(t))
	files, err := filepath.Glob(filepath.Join("..", "..", "web", "static", "ui", "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("keine Dateien in web/static/ui: %v", err)
	}
	for _, f := range files { // Glob liefert sortiert
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sb.WriteString("\n")
		sb.WriteString(strings.ReplaceAll(string(b), "\r", ""))
	}
	return sb.String()
}

// baseSource: Quelltext von base.gohtml ohne CR (Zeilenenden je nach Arbeitskopie).
func baseSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "base.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r", "")
}

func TestUICSSComplete(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "ui", "pdh.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`html[data-style="fiori"]`, `html[data-theme="light"]`, `@media print`, `.app{`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("pdh.css enthält %q nicht", want)
		}
	}
}
