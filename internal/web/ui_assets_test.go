package web

import (
	"encoding/json"
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

var scriptBlockRe = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// Skripte der Grundseite kommen als Dateien: jede Datei aus web/static/ui/*.js
// wird eingebunden und ausgeliefert, im Quelltext bleibt kein großes Skript ohne
// Vorlagen-Platzhalter.
func TestBaseScriptsExternal(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "web", "static", "ui", "*.js"))
	if len(files) < 7 {
		t.Fatalf("nur %d Skriptdateien in web/static/ui", len(files))
	}
	// Quelltext statt gerenderter Seite: manche Skripte stehen wie vorher in
	// Bedingungen (Chat nur mit Chat-Recht, Push nur angemeldet).
	src := baseSource(t)
	if out := renderBaseForTest(t); !strings.Contains(out, `<script src="`+uiURL("pdh-viewer.js")+`"></script>`) {
		t.Error("gerenderte Grundseite bindet pdh-viewer.js nicht ein")
	}
	r := iconTestRouter()
	for _, f := range files {
		name := filepath.Base(f)
		if !strings.Contains(src, `<script src="{{uiURL "`+name+`"}}"></script>`) {
			t.Errorf("base.gohtml bindet %s nicht ein", name)
		}
		rec := iconGet(r, uiURL(name), nil)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") || rec.Body.Len() == 0 {
			t.Errorf("%s: HTTP %d, %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	for _, m := range scriptBlockRe.FindAllStringSubmatch(baseSource(t), -1) {
		if len(m[2]) > 1500 && !strings.Contains(m[2], "{{") {
			t.Errorf("Inline-Skript ohne Platzhalter mit %d Byte: %.60q", len(m[2]), strings.TrimSpace(m[2]))
		}
	}
}

var bootBlockRe = regexp.MustCompile(`(?s)<script id="pdh-boot" type="application/json">(.*?)</script>`)

// Seitendaten der Grundseite stehen als JSON im Kopf (pdh-boot); die ausgelagerten
// Skripte lesen sie über window.PDH_BOOT. Sonderzeichen bleiben gültiges JSON und
// beenden den Block nicht.
func TestBootDataJSON(t *testing.T) {
	d := ServerConfigData{EnvFile: ".env", FileWritable: true}
	d.Title = "Server-Einstellungen"
	d.Look = d.Brand.DefaultLook()
	d.IsAdmin = true
	d.IdleTimeoutMinutes = 17
	d.CopilotRef = `a</script><b>"x'`
	out := renderPage(t, loadTestTemplates(t), "server_config", d)
	all := bootBlockRe.FindAllStringSubmatch(out, -1)
	if len(all) != 1 {
		t.Fatalf("%d pdh-boot-Blöcke", len(all))
	}
	var boot map[string]any
	if err := json.Unmarshal([]byte(all[0][1]), &boot); err != nil {
		t.Fatalf("pdh-boot ist kein JSON: %v\n%.300s", err, all[0][1])
	}
	if boot["version"] != pdh.Version() || boot["isAdmin"] != true || boot["idleMinutes"] != float64(17) || boot["copilotRef"] != `a</script><b>"x'` {
		t.Errorf("Werte: version=%v isAdmin=%v idleMinutes=%v copilotRef=%v", boot["version"], boot["isAdmin"], boot["idleMinutes"], boot["copilotRef"])
	}
	// Schriften als Objekt (kein doppelt kodierter Text), das Skript hat einen Rückfall
	if f, ok := boot["fonts"].(map[string]any); !ok || len(f) == 0 {
		t.Errorf("fonts ist kein Objekt mit Schriften: %T %v", boot["fonts"], boot["fonts"])
	}
	if !strings.Contains(uiFile(t, "pdh-app.js"), "const PDH_FONTS = PDH_BOOT.fonts || {};") {
		t.Error("pdh-app.js liest die Schriften nicht mit Rückfall")
	}
	if tr, ok := boot["t"].(map[string]any); !ok || tr["Gruppe nach oben"] == nil {
		t.Errorf("Übersetzungen fehlen: %v", boot["t"])
	}
	if strings.Contains(all[0][1], "</script") || strings.Contains(all[0][1], "<b>") {
		t.Error("Sonderzeichen nicht maskiert")
	}
	if i, j := strings.Index(out, `id="pdh-boot"`), strings.Index(out, "PDH_BOOT"); i < 0 || j < i {
		t.Error("pdh-boot steht nicht vor dem ersten Skript, das ihn liest")
	}
}

// bootValue: Wert eines Schlüssels aus dem Datenblock pdh-boot der Seite als Text.
func bootValue(t *testing.T, page, key string) string {
	t.Helper()
	m := bootBlockRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("Seite ohne pdh-boot")
	}
	var boot map[string]any
	if err := json.Unmarshal([]byte(m[1]), &boot); err != nil {
		t.Fatalf("pdh-boot: %v", err)
	}
	s, _ := boot[key].(string)
	return s
}

// Dateien unter web/static/ui/ gehen roh an den Browser: Vorlagen-Syntax darin
// würde nie ausgeführt und das Skript brechen. Werte kommen über PDH_BOOT, Texte
// über pdhT (Eintrag im Datenblock von base.gohtml).
func TestUIFilesNoTemplateSyntax(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "web", "static", "ui", "*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if i := strings.Index(string(b), "{{"); i >= 0 {
			t.Errorf("%s enthält Vorlagen-Syntax: %.60q – Werte über PDH_BOOT, Texte über pdhT", filepath.Base(f), string(b[i:]))
		}
	}
}

// Der Rahmen schickt kaum noch Stil und Skript direkt in der Seite mit.
func TestBaseInlineBudget(t *testing.T) {
	src := bootBlockRe.ReplaceAllString(baseSource(t), "")
	n := 0
	for _, m := range styleBlockRe.FindAllStringSubmatch(src, -1) {
		n += len(m[2])
	}
	for _, m := range scriptBlockRe.FindAllStringSubmatch(src, -1) {
		n += len(m[2])
	}
	if n > 16384 {
		t.Errorf("Inline-Stil und -Skript in base.gohtml: %d Byte (höchstens 16384)", n)
	}
}

var uiScriptSrcRe = regexp.MustCompile(`<script src="/vendor/[^/"]+/ui/([^"]+)"></script>`)

// withUIScripts: gerenderte Seite plus Inhalt der Oberflächen-Skripte, die sie
// einbindet – so, wie der Browser sie lädt. Für Tests, die nach Funktionen der
// Seite suchen, die seit 0.82.61 in web/static/ui/ stehen.
func withUIScripts(t *testing.T, page string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(page)
	for _, m := range uiScriptSrcRe.FindAllStringSubmatch(page, -1) {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "ui", m[1]))
		if err != nil {
			t.Fatalf("eingebundenes Skript %s fehlt: %v", m[1], err)
		}
		sb.WriteString("\n")
		sb.Write(b)
	}
	return sb.String()
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
