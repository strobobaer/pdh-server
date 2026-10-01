package web

import (
	"os"
	"strings"
	"testing"
)

func TestThemePresetsContrast(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range themePresets {
		if seen[p.ID] {
			t.Errorf("doppelte Kennung %q", p.ID)
		}
		seen[p.ID] = true
		// Kontrast nur fuer die Hauptfarbe (Text, Schaltflaechen); die
		// Zweitfarbe dient nur fuer Farbverlaeufe.
		if c := p.Dark.Accent; !themeContrastOK(c, false) {
			t.Errorf("%s: %s im Dunkel-Modus zu wenig Kontrast (weiß %.2f, Fläche %.2f)", p.ID, c, contrast(c, "#ffffff"), contrast(c, darkSurface))
		}
		if c := p.Light.Accent; !themeContrastOK(c, true) {
			t.Errorf("%s: %s im Hell-Modus zu wenig Kontrast (%.2f)", p.ID, c, contrast(c, "#ffffff"))
		}
		for _, c := range []string{p.Dark.Accent2, p.Light.Accent2} {
			if !hexColorRe.MatchString(c) {
				t.Errorf("%s: Zweitfarbe %q ungültig", p.ID, c)
			}
		}
	}
	if !seen[defaultTheme] {
		t.Error("Standardschema fehlt")
	}
}

func TestCustomPaletteReadable(t *testing.T) {
	for _, in := range []string{"#ffff00", "#000000", "#0a7cc1", "#ffffff", "#e30613", "#00ff7f"} {
		p := customPalette(in)
		if !themeContrastOK(p.Dark.Accent, false) || !themeContrastOK(p.Light.Accent, true) {
			t.Errorf("%s → %s/%s nicht lesbar", in, p.Dark.Accent, p.Light.Accent)
		}
		if !hexColorRe.MatchString(p.Dark.Accent2) || !hexColorRe.MatchString(p.Light.Accent2) {
			t.Errorf("%s: Zweitfarbe ungültig", in)
		}
	}
}

func TestThemeSelection(t *testing.T) {
	b := Branding{Theme: "gibtsnicht"}
	if b.ThemeID() != defaultTheme {
		t.Errorf("unbekanntes Schema → %q", b.ThemeID())
	}
	b = Branding{Theme: customTheme}
	if b.ThemeID() != defaultTheme {
		t.Error("Firmenfarbe ohne Farbe darf nicht gelten")
	}
	b.ThemeColor = "#0a7cc1"
	if b.ThemeID() != customTheme {
		t.Error("Firmenfarbe nicht übernommen")
	}
	css := string(b.ThemeCSS())
	for _, want := range []string{`html[data-palette="eigen"]:not([data-theme="light"])`, `html[data-palette="petrol"][data-theme="light"]`, "--accent-rgb:", "--on-accent:"} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS ohne %q", want)
		}
	}
}

const haSample = `
"Test Dark":
  polar: "#2e3440"
  card: "#3B4252"
  frost: "#8fbcbb"
  accent-color: "var(--frost)"
  primary-color: var(--accent-color)
  primary-background-color: var(--polar)
  card-background-color: "var(--ha-card-background)"
  ha-card-background: var(--card)
  primary-text-color: "#eceff4"
  secondary-text-color: "rgb(229, 233, 240)"
  divider-color: "rgba(255,255,255,.12)"
  error-color: "var(--missing, #bf616a)"
  ha-card-border-radius: 5px
  button-color-fill: "color-mix(in srgb, var(--frost) 30%, black)"
  card-mod-theme: Test Dark
"Zwei Modi":
  modes:
    light:
      primary-color: "#5e81ac"
      primary-background-color: "#eceff4"
    dark:
      primary-color: "#88c0d0"
      primary-background-color: "#2e3440"
"Evil":
  primary-color: "red;}body{display:none"
  primary-background-color: "url(javascript:alert(1))"
"Nur Basis":
  ha-card-border-radius: 5px
`

func TestParseHAThemes(t *testing.T) {
	list, err := parseHAThemes([]byte(haSample))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ThemePalette{}
	for _, p := range list {
		byID[p.ID] = p
	}
	if len(list) != 2 {
		t.Fatalf("erwartet 2 Themes (Evil und Nur Basis übersprungen), bekommen %d: %+v", len(list), list)
	}
	d := byID["ha-test-dark"]
	if d.Dark.Accent != "#8fbcbb" || d.Dark.Bg != "#2e3440" || d.Dark.Bg2 != "#3B4252" || d.Dark.Red != "#bf616a" {
		t.Errorf("var()-Ketten falsch aufgelöst: %+v", d.Dark)
	}
	if d.Dark.Muted != "rgb(229, 233, 240)" || d.Dark.Border != "rgba(255,255,255,.12)" || d.Dark.Bg3 == "" {
		t.Errorf("rgb/rgba/bg3: %+v", d.Dark)
	}
	if d.Light.Bg != "" || d.Light.Accent != "#8fbcbb" {
		t.Errorf("dunkles Theme ohne Modi: Hell soll nur den Akzent übernehmen: %+v", d.Light)
	}
	m := byID["ha-zwei-modi"]
	if m.Light.Accent != "#5e81ac" || m.Dark.Accent != "#88c0d0" || m.Light.Bg != "#eceff4" {
		t.Errorf("Modi falsch: %+v", m)
	}
	css := string(Branding{HAThemes: list}.ThemeCSS())
	if strings.Contains(css, "display:none") || strings.Contains(css, "javascript") || strings.Contains(css, "color-mix") {
		t.Error("unsichere Werte im CSS")
	}
	if !strings.Contains(css, `html[data-palette="ha-test-dark"]:not([data-theme="light"]){--accent:#8fbcbb`) {
		t.Errorf("CSS für importiertes Theme fehlt:\n%s", css)
	}
}

func TestParseHAThemesErrors(t *testing.T) {
	if _, err := parseHAThemes([]byte("a: [")); err == nil {
		t.Error("kaputtes YAML akzeptiert")
	}
	if _, err := parseHAThemes([]byte("x:\n  font-size: 3px\n")); err == nil {
		t.Error("Datei ohne Farben akzeptiert")
	}
}

func TestMergeHAThemes(t *testing.T) {
	a := []ThemePalette{{ID: "ha-a", Name: "A"}, {ID: "ha-b", Name: "B"}}
	out := mergeHAThemes(a, []ThemePalette{{ID: "ha-b", Name: "B2"}, {ID: "ha-c", Name: "C"}})
	if len(out) != 3 || out[1].Name != "B2" || out[2].ID != "ha-c" {
		t.Errorf("merge: %+v", out)
	}
}

// Optional: echte Theme-Datei pruefen (PDH_HA_THEME=pfad/zur/datei.yaml).
func TestParseHAThemeFile(t *testing.T) {
	path := os.Getenv("PDH_HA_THEME")
	if path == "" {
		t.Skip("PDH_HA_THEME nicht gesetzt")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseHAThemes(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		t.Logf("%s (%s)\n  dunkel: %+v\n  hell:   %+v", p.Name, p.ID, p.Dark, p.Light)
	}
}

func TestThemeInLayout(t *testing.T) {
	tmpl := loadTestTemplates(t)
	ha, err := parseHAThemes([]byte(haSample))
	if err != nil {
		t.Fatal(err)
	}
	d := ServerConfigData{EnvFile: ".env", FileWritable: true}
	d.Brand = Branding{Theme: "ha-test-dark", ThemeUserChoice: true, HAThemes: ha}
	d.Title = "Server-Einstellungen"
	d.Look = d.Brand.DefaultLook()
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{
		`<style id="pdh-theme">html[data-palette="blau"]`, `html[data-palette="ha-test-dark"]:not([data-theme="light"]){--accent:#8fbcbb`,
		`window.PDH_LOOK_DEFAULT = {palette: "ha-test-dark"`, `"petrol"`, `dataset.palette = "ha-test-dark"`, // Kopf-Skript
		`class="pal-menu look-menu"`, `data-palette-pick="ha-zwei-modi"`, `id="look-font"`, "lookStep(10)", // Benutzermenue
		`<style id="pdh-ui">:root{--font:'DM Sans'`, "--ui-scale:1.00", `id="pdh-font"`, // Schrift & Groesse
		`action="/admin/branding/theme"`, `value="ha-test-dark" checked`, "aus Home Assistant", // Auswahl
		`action="/admin/branding/ha-import"`, `/admin/branding/ha/ha-test-dark/delete`, "Firmenstandard",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	// Benutzerwahl abgeschaltet: kein Farbschema im Benutzermenue
	d.Brand.ThemeUserChoice = false
	d.Look = d.Brand.DefaultLook()
	out = renderPage(t, tmpl, "server_config", d)
	if strings.Contains(out, `data-palette-pick="ha-zwei-modi"`) || strings.Contains(out, `id="look-font"`) {
		t.Error("Farbschema-/Schriftauswahl trotz Sperre angeboten")
	}
	if !strings.Contains(out, "lookStep(10)") {
		t.Error("Größe muss immer einstellbar sein")
	}
}

func TestTopbarClock(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "user_detail", UserDetailData{User: UserView{ID: "u1"}})
	for _, want := range []string{`href="/time" class="topbar-clock" id="pdh-clock" data-server-ms="1`, `class="clk-time"`, "Zeiterfassung öffnen", "function kw(d)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Kopfleiste enthält %q nicht", want)
		}
	}
}
