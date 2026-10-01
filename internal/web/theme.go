package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Farbschemata (Theming) fuer die ganze Oberflaeche, unabhaengig vom
// Hell/Dunkel-Modus. Der Administrator legt unter Server-Einstellungen →
// Erscheinungsbild den Firmenstandard fest: eine Vorlage, eine eigene
// Firmenfarbe oder ein aus Home Assistant importiertes Theme (theme_ha.go).
// Sofern erlaubt, waehlt jeder Benutzer im Benutzermenue ein eigenes Schema
// (gespeichert im Browser, wie Hell/Dunkel). Umgesetzt ueber CSS-Variablen
// je html[data-palette]; leere Werte lassen die Grundfarben stehen.

const (
	keyBrandTheme      = "branding.theme"
	keyBrandThemeColor = "branding.theme_color"
	keyBrandThemeUser  = "branding.theme_user"
	keyBrandHAThemes   = "branding.ha_themes"

	defaultTheme = "blau"
	customTheme  = "eigen"
)

// themeColors: Farben fuer einen Modus. Akzente sind immer gesetzt, die
// Flaechen- und Textfarben nur bei importierten Themes.
type themeColors struct {
	Accent, Accent2     string
	Bg, Bg2, Bg3        string `json:",omitempty"`
	Text, Muted, Border string `json:",omitempty"`
	Green, Amber, Red   string `json:",omitempty"`
}

// ThemePalette ist ein Farbschema mit Farben fuer Dunkel und Hell.
type ThemePalette struct {
	ID, Name    string
	Source      string `json:",omitempty"` // "" Vorlage/Firmenfarbe, "ha" Home Assistant
	Dark, Light themeColors
}

// Swatch: Hintergrund fuer Auswahlknoepfe (Dunkel-Farben; bei vollstaendigen
// Themes halb Akzent, halb Kartenfarbe).
func (p ThemePalette) Swatch() template.CSS {
	if p.Dark.Bg2 != "" {
		return template.CSS("linear-gradient(135deg," + p.Dark.Accent + " 0 50%," + p.Dark.Bg2 + " 50%)")
	}
	return template.CSS("linear-gradient(135deg," + p.Dark.Accent + "," + p.Dark.Accent2 + ")")
}

// themePresets: mitgelieferte Vorlagen. Die Hauptfarben erfuellen die
// Kontrastregeln aus themeContrastOK (siehe TestThemePresetsContrast).
var themePresets = []ThemePalette{
	{ID: "blau", Name: "PDH-Blau", Dark: themeColors{Accent: "#4f6ef7", Accent2: "#7c3aed"}, Light: themeColors{Accent: "#3457d5", Accent2: "#6d28d9"}},
	{ID: "petrol", Name: "Petrol", Dark: themeColors{Accent: "#14939a", Accent2: "#2f7fd8"}, Light: themeColors{Accent: "#0f766e", Accent2: "#1d4ed8"}},
	{ID: "gruen", Name: "Industriegrün", Dark: themeColors{Accent: "#2b8f52", Accent2: "#11867c"}, Light: themeColors{Accent: "#15803d", Accent2: "#0f766e"}},
	{ID: "orange", Name: "Signalorange", Dark: themeColors{Accent: "#d4570c", Accent2: "#d0303a"}, Light: themeColors{Accent: "#c2410c", Accent2: "#b91c1c"}},
	{ID: "bordeaux", Name: "Bordeaux", Dark: themeColors{Accent: "#d23f58", Accent2: "#a052e0"}, Light: themeColors{Accent: "#be123c", Accent2: "#9333ea"}},
	{ID: "violett", Name: "Violett", Dark: themeColors{Accent: "#8b5cf6", Accent2: "#d0337a"}, Light: themeColors{Accent: "#6d28d9", Accent2: "#be185d"}},
	{ID: "anthrazit", Name: "Anthrazit", Dark: themeColors{Accent: "#6b7891", Accent2: "#7a6a9c"}, Light: themeColors{Accent: "#475569", Accent2: "#334155"}},
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// customPalette leitet aus einer Firmenfarbe ein Schema ab: Farbton und
// Saettigung bleiben, die Helligkeit wird je Modus so angepasst, dass Text
// und Schaltflaechen lesbar bleiben. Die Zweitfarbe ist um 35° gedreht.
func customPalette(hex string) ThemePalette {
	h, s, _ := hexToHSL(hex)
	h2 := math.Mod(h+35, 360)
	return ThemePalette{
		ID: customTheme, Name: "Firmenfarbe",
		Dark:  themeColors{Accent: fitLightness(h, s, false), Accent2: fitLightness(h2, s, false)},
		Light: themeColors{Accent: fitLightness(h, s, true), Accent2: fitLightness(h2, s, true)},
	}
}

// Kontrastregeln: Hell – Akzent als Text auf Weiss (WCAG AA 4,5:1).
// Dunkel – weisse Schrift auf dem Akzent und Akzent auf dem dunklen
// Kartenhintergrund jeweils mindestens 3:1.
const darkSurface = "#1a1d27"

func themeContrastOK(hex string, light bool) bool {
	if light {
		return contrast(hex, "#ffffff") >= 4.5
	}
	return contrast(hex, "#ffffff") >= 3 && contrast(hex, darkSurface) >= 3
}

// fitLightness sucht die mittlere Helligkeit am naechsten liegende, die die
// Kontrastregeln erfuellt.
func fitLightness(h, s float64, light bool) string {
	start := 0.5
	if light {
		start = 0.42
	}
	for d := 0.0; d <= 0.5; d += 0.01 {
		for _, l := range []float64{start - d, start + d} {
			if l < 0.05 || l > 0.95 {
				continue
			}
			if c := hslToHex(h, s, l); themeContrastOK(c, light) {
				return c
			}
		}
	}
	if light {
		return "#334155"
	}
	return "#6b7891"
}

// Palettes: alle waehlbaren Schemata – Vorlagen, Firmenfarbe (falls gesetzt)
// und importierte Home-Assistant-Themes.
func (b Branding) Palettes() []ThemePalette {
	out := append([]ThemePalette(nil), themePresets...)
	if hexColorRe.MatchString(b.ThemeColor) {
		out = append(out, customPalette(b.ThemeColor))
	}
	return append(out, b.HAThemes...)
}

// ImportedThemes: nur die aus Home Assistant uebernommenen Schemata.
func (b Branding) ImportedThemes() []ThemePalette { return b.HAThemes }

func (b Branding) palette(id string) (ThemePalette, bool) {
	for _, p := range b.Palettes() {
		if p.ID == id {
			return p, true
		}
	}
	return ThemePalette{}, false
}

// ThemeID: wirksames Firmenstandard-Schema (fuer data-palette).
func (b Branding) ThemeID() string {
	if p, ok := b.palette(b.Theme); ok {
		return p.ID
	}
	return defaultTheme
}

// ThemeName: Anzeigename des Firmenstandards.
func (b Branding) ThemeName() string {
	p, _ := b.palette(b.ThemeID())
	return p.Name
}

// PaletteIDs: Kennungen aller Schemata (fuer das Skript im Seitenkopf).
func (b Branding) PaletteIDs() []string {
	var ids []string
	for _, p := range b.Palettes() {
		ids = append(ids, p.ID)
	}
	return ids
}

// ThemeCSS: CSS-Variablen je Schema und Modus. Alle Werte stammen aus den
// Vorlagen, einer geprueften #rrggbb-Farbe oder aus safeCSSColor-geprueften
// Importwerten. Die Dunkel-Regel gilt nur ohne data-theme="light", damit
// Flaechenfarben eines dunklen Themes nicht in den Hell-Modus durchschlagen.
func (b Branding) ThemeCSS() template.CSS {
	var sb strings.Builder
	for _, p := range b.Palettes() {
		fmt.Fprintf(&sb, "html[data-palette=%q]:not([data-theme=\"light\"]){%s}\n", p.ID, themeVars(p.Dark))
		fmt.Fprintf(&sb, "html[data-palette=%q][data-theme=\"light\"]{%s}\n", p.ID, themeVars(p.Light))
	}
	return template.CSS(sb.String())
}

func themeVars(c themeColors) string {
	var parts []string
	add := func(name, v string) {
		if v != "" && safeCSSColor(v) {
			parts = append(parts, "--"+name+":"+v)
		}
	}
	add("accent", c.Accent)
	add("accent2", c.Accent2)
	if r, g, bl, ok := parseCSSColor(c.Accent); ok {
		parts = append(parts, fmt.Sprintf("--accent-rgb:%d,%d,%d", r, g, bl))
		on := "#ffffff"
		if contrast(rgbHex(r, g, bl), "#ffffff") < 3 {
			on = "#111827"
		}
		parts = append(parts, "--on-accent:"+on)
	}
	add("bg", c.Bg)
	add("bg2", c.Bg2)
	add("bg3", c.Bg3)
	add("text", c.Text)
	add("muted", c.Muted)
	add("border", c.Border)
	add("green", c.Green)
	add("amber", c.Amber)
	add("red", c.Red)
	return strings.Join(parts, ";")
}

// loadHAThemes liest die importierten Themes aus den Einstellungen.
func (h *Handler) loadHAThemes(ctx context.Context) []ThemePalette {
	raw := h.appSetting(ctx, keyBrandHAThemes, "")
	if raw == "" {
		return nil
	}
	var list []ThemePalette
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil
	}
	return list
}

func (h *Handler) saveHAThemes(ctx context.Context, list []ThemePalette) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return h.setAppSetting(ctx, keyBrandHAThemes, string(data))
}

// BrandingThemeWeb: POST /admin/branding/theme (Firmenstandard und Benutzerwahl)
func (h *Handler) BrandingThemeWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	theme := r.FormValue("theme")
	color := strings.ToLower(strings.TrimSpace(r.FormValue("theme_color")))
	if color != "" && !hexColorRe.MatchString(color) {
		brandingRedirect(w, r, "", errors.New("Die Firmenfarbe muss als #RRGGBB angegeben werden, z. B. #0a7cc1."))
		return
	}
	if theme == customTheme && color == "" {
		brandingRedirect(w, r, "", errors.New("Für „Firmenfarbe“ bitte eine Farbe wählen."))
		return
	}
	probe := h.branding()
	probe.ThemeColor = color
	if _, ok := probe.palette(theme); !ok {
		brandingRedirect(w, r, "", errors.New("Unbekanntes Farbschema."))
		return
	}
	user := "0"
	if r.FormValue("theme_user") == "1" {
		user = "1"
	}
	font := r.FormValue("font")
	if _, ok := uiFont(font); !ok {
		font = defaultFont
	}
	scale, _ := strconv.Atoi(r.FormValue("scale"))
	if !validScale(scale) {
		scale = defaultScale
	}
	ctx := r.Context()
	var err error
	for k, v := range map[string]string{keyBrandTheme: theme, keyBrandThemeColor: color, keyBrandThemeUser: user, keyBrandFont: font, keyBrandScale: strconv.Itoa(scale)} {
		if e := h.setAppSetting(ctx, k, v); e != nil {
			err = e
		}
	}
	resetBrandingCache()
	componentLog("konfiguration").Info().Str("farbschema", theme).Str("user", getUser(r).ID).Msg("farbschema gespeichert")
	brandingRedirect(w, r, "Farbschema und Schrift gespeichert.", err)
}

// ── Farbrechnung ────────────────────────────────────────────

var (
	cssHexRe  = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	cssFuncRe = regexp.MustCompile(`^(rgb|rgba|hsl|hsla)\(\s*[0-9.,%/\sdeg]+\)$`)
	cssNameRe = regexp.MustCompile(`^[a-zA-Z]{3,20}$`)
)

// safeCSSColor: nur einfache Farbangaben (Hex, rgb/hsl, Farbname) – damit
// importierte Werte kein CSS einschleusen koennen.
func safeCSSColor(v string) bool {
	return cssHexRe.MatchString(v) || cssFuncRe.MatchString(v) || cssNameRe.MatchString(v)
}

var cssNamed = map[string][3]int{"white": {255, 255, 255}, "black": {0, 0, 0}, "red": {255, 0, 0}, "green": {0, 128, 0}, "blue": {0, 0, 255}, "orange": {255, 165, 0}, "gray": {128, 128, 128}, "grey": {128, 128, 128}}

// parseCSSColor liefert RGB fuer Hex-, rgb()/rgba()- und einige Farbnamen.
func parseCSSColor(v string) (int, int, int, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	if cssHexRe.MatchString(v) {
		x := v[1:]
		if len(x) <= 4 {
			x = string([]byte{x[0], x[0], x[1], x[1], x[2], x[2]})
		}
		r, g, b := hexRGB("#" + x[:6])
		return r, g, b, true
	}
	if strings.HasPrefix(v, "rgb") {
		inner := v[strings.Index(v, "(")+1 : len(v)-1]
		f := strings.FieldsFunc(inner, func(c rune) bool { return c == ',' || c == ' ' || c == '/' })
		if len(f) >= 3 {
			var c [3]int
			for i := 0; i < 3; i++ {
				if _, err := fmt.Sscanf(f[i], "%d", &c[i]); err != nil || c[i] < 0 || c[i] > 255 {
					return 0, 0, 0, false
				}
			}
			return c[0], c[1], c[2], true
		}
	}
	if c, ok := cssNamed[v]; ok {
		return c[0], c[1], c[2], true
	}
	return 0, 0, 0, false
}

func rgbHex(r, g, b int) string { return fmt.Sprintf("#%02x%02x%02x", r, g, b) }

func hexRGB(hex string) (int, int, int) {
	var r, g, b int
	_, _ = fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func luminance(hex string) float64 {
	r, g, b := hexRGB(hex)
	lin := func(v int) float64 {
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func hexToHSL(hex string) (h, s, l float64) {
	ri, gi, bi := hexRGB(hex)
	r, g, b := float64(ri)/255, float64(gi)/255, float64(bi)/255
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h * 60, s, l
}

func hslToHex(h, s, l float64) string {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	to := func(v float64) int { return int(math.Round((v + m) * 255)) }
	return rgbHex(to(r), to(g), to(b))
}

// mixHex mischt zwei Farben (t = Anteil von b).
func mixHex(a, b string, t float64) string {
	ar, ag, ab := hexRGB(a)
	br, bg, bb := hexRGB(b)
	m := func(x, y int) int { return int(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return rgbHex(m(ar, br), m(ag, bg), m(ab, bb))
}
