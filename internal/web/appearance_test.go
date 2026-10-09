package web

import (
	"bytes"
	"html/template"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppearanceEffective(t *testing.T) {
	b := Branding{Theme: "petrol", Font: "inter", Scale: 120, ThemeUserChoice: true}
	a := Appearance{Brand: b, Allow: true, User: UserAppearance{Palette: "violett", Font: "roboto", Scale: 90}}
	a.FontID, a.Scale = "roboto", 90
	if a.PaletteID() != "violett" {
		t.Errorf("eigenes Schema: %s", a.PaletteID())
	}
	css := string(a.CSS())
	if !strings.Contains(css, "--font:'Roboto'") || !strings.Contains(css, "--ui-scale:0.90") {
		t.Errorf("CSS: %s", css)
	}
	// gesperrt oder unbekannt → Firmenstandard
	a.Allow = false
	if a.PaletteID() != "petrol" {
		t.Error("Sperre greift nicht")
	}
	a.Allow, a.User.Palette = true, "gibtsnicht"
	if a.PaletteID() != "petrol" {
		t.Error("unbekanntes Schema übernommen")
	}
	// Standard ohne Angaben
	d := Branding{}.DefaultLook()
	if d.FontID != defaultFont || d.Scale != 100 || !strings.Contains(string(d.CSS()), "--ui-scale:1.00") {
		t.Errorf("Standard: %+v", d)
	}
	if (Appearance{}).FontHref() == "" || !strings.Contains(string((Appearance{}).CSS()), "DM Sans") {
		t.Error("leere Darstellung muss auf DM Sans zurückfallen")
	}
	// Zeilenhoehe: Standard 100 %, eigene Wahl als --row-scale
	if !strings.Contains(string(d.CSS()), "--row-scale:1.00") {
		t.Errorf("Zeilenhöhe Standard: %s", d.CSS())
	}
	a.Row = 80
	if !strings.Contains(string(a.CSS()), "--row-scale:0.80") {
		t.Errorf("Zeilenhöhe: %s", a.CSS())
	}
	if validRow(60) || validRow(160) || !validRow(70) || !validRow(150) {
		t.Error("Grenzen der Zeilenhöhe")
	}
	if validScale(50) || validScale(200) || !validScale(70) || !validScale(160) {
		t.Error("Grenzen der Größe")
	}
}

func TestGalleryOnServerConfig(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{}
	d.Brand = Branding{ThemeUserChoice: true, Font: "atkinson", Scale: 120}
	nordic, _ := galleryTheme("ha-nordic")
	d.Brand.HAThemes = []ThemePalette{nordic}
	d.Look = d.Brand.DefaultLook()
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"Theme-Galerie aus Home Assistant", "Metro &amp; Fluent", "Bas Nijholt", `action="/admin/branding/gallery/ha-metro-blue"`,
		"--da:#8fbcbb", `name="font"`, `<option value="atkinson" selected>`, `<option value="120" selected>`, "--ui-scale:1.20", "Atkinson+Hyperlegible", "übernommen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
}

func TestLoginUsesCompanyLook(t *testing.T) {
	root := filepath.Join("..", "..", "web", "templates")
	tmpl, err := template.New("login.gohtml").Funcs(TemplateFuncs()).ParseFiles(filepath.Join(root, "login.gohtml"), filepath.Join(root, "widgets", "lang_switch.gohtml"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, LoginData{Brand: Branding{Theme: "orange", Font: "verdana", Scale: 130}}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`dataset.palette = "orange"`, "--font:Verdana", "--ui-scale:1.30", `html[data-palette="orange"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("Anmeldeseite enthält %q nicht", want)
		}
	}
}
