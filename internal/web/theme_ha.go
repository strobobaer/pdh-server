package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// Import von Home-Assistant-Themes (z. B. Nordic, Caule, iOS Dark Mode).
// Eine HA-Theme-Datei ist YAML: oben der Theme-Name, darunter CSS-Variablen
// ohne "--" – entweder direkt oder getrennt nach modes: light/dark. Werte
// verweisen oft aufeinander (primary-color: var(--accent-color)), daher
// werden var()-Ketten aufgeloest. Uebernommen werden nur die Farben, die
// es im PDH gibt; alles andere (Schriften, card-mod, Zustandsfarben …)
// wird ignoriert.

const (
	haThemeMaxBytes = 2 << 20
	haThemeMaxCount = 40
)

// haVarMap: PDH-Variable ← HA-Variablen in Reihenfolge der Bevorzugung.
var haVarMap = []struct {
	set  func(*themeColors, string)
	keys []string
}{
	{func(c *themeColors, v string) { c.Accent = v }, []string{"primary-color", "accent-color"}},
	{func(c *themeColors, v string) { c.Bg = v }, []string{"primary-background-color", "background-color"}},
	{func(c *themeColors, v string) { c.Bg2 = v }, []string{"card-background-color", "ha-card-background", "secondary-background-color"}},
	{func(c *themeColors, v string) { c.Text = v }, []string{"primary-text-color", "text-color"}},
	{func(c *themeColors, v string) { c.Muted = v }, []string{"secondary-text-color"}},
	{func(c *themeColors, v string) { c.Border = v }, []string{"divider-color"}},
	{func(c *themeColors, v string) { c.Green = v }, []string{"success-color", "green-color"}},
	{func(c *themeColors, v string) { c.Amber = v }, []string{"warning-color", "amber-color"}},
	{func(c *themeColors, v string) { c.Red = v }, []string{"error-color", "red-color"}},
}

var cssVarRe = regexp.MustCompile(`^var\(\s*--([\w-]+)\s*(?:,\s*(.+))?\)$`)

// resolveHAValue loest var()-Ketten auf; liefert "" fuer unbrauchbare Werte.
func resolveHAValue(vars map[string]string, v string) string {
	for i := 0; i < 12; i++ {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		m := cssVarRe.FindStringSubmatch(v)
		if m == nil {
			break
		}
		if next, ok := vars[m[1]]; ok {
			v = next
		} else if m[2] != "" {
			v = m[2]
		} else {
			return ""
		}
	}
	if !safeCSSColor(v) {
		return ""
	}
	return v
}

// haScalars: einfache Werte eines YAML-Knotens (Zahlen/Texte), ohne Unterknoten.
func haScalars(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		switch x := v.(type) {
		case string:
			out[k] = x
		case int, int64, float64, bool:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

func haColors(vars map[string]string) themeColors {
	var c themeColors
	get := func(keys ...string) string {
		for _, k := range keys {
			if raw, ok := vars[k]; ok {
				if v := resolveHAValue(vars, raw); v != "" {
					return v
				}
			}
		}
		return ""
	}
	for _, m := range haVarMap {
		m.set(&c, get(m.keys...))
	}
	// Zweitfarbe: accent-color, falls sie sich von der Hauptfarbe unterscheidet
	if a2 := get("accent-color"); a2 != "" && a2 != c.Accent {
		c.Accent2 = a2
	} else if a2 := get("accent-medium-color", "state-icon-color", "info-color"); a2 != "" {
		c.Accent2 = a2
	} else {
		c.Accent2 = c.Accent
	}
	// Eingabefelder/Hover (bg3): etwas zur Schrift hin gemischte Kartenfarbe
	b2, okB := hexOf(c.Bg2)
	tx, okT := hexOf(c.Text)
	if okB && okT {
		c.Bg3 = mixHex(b2, tx, 0.06)
	}
	return c
}

func hexOf(v string) (string, bool) {
	r, g, b, ok := parseCSSColor(v)
	if !ok {
		return "", false
	}
	return rgbHex(r, g, b), true
}

// isDark: dunkler Hintergrund? (unbekannt → ok=false)
func isDark(c themeColors) (dark, ok bool) {
	h, ok := hexOf(c.Bg)
	if !ok {
		return false, false
	}
	return luminance(h) < 0.25, true
}

// parseHAThemes liest eine HA-Theme-Datei. Themes ohne verwertbare Farben
// (z. B. reine Basis-Themes) werden uebersprungen.
func parseHAThemes(data []byte) ([]ThemePalette, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("Die Datei ist kein gültiges YAML: %v", err)
	}
	names := make([]string, 0, len(doc))
	for k := range doc {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []ThemePalette
	for _, name := range names {
		body, ok := doc[name].(map[string]any)
		if !ok {
			continue
		}
		base := haScalars(body)
		merged := func(mode string) map[string]string {
			vars := map[string]string{}
			for k, v := range base {
				vars[k] = v
			}
			if modes, ok := body["modes"].(map[string]any); ok {
				if mm, ok := modes[mode].(map[string]any); ok {
					for k, v := range haScalars(mm) {
						vars[k] = v
					}
				}
			}
			return vars
		}
		_, hasModes := body["modes"].(map[string]any)
		dark, light := haColors(merged("dark")), haColors(merged("light"))
		if dark.Accent == "" && light.Accent == "" {
			continue // kein Akzent → nichts Brauchbares
		}
		if !hasModes {
			// Ein Satz Farben: gehoert zu dem Modus, zu dem der Hintergrund
			// passt; der andere Modus uebernimmt nur die Akzentfarben.
			if d, ok := isDark(dark); ok {
				if d {
					light = themeColors{Accent: dark.Accent, Accent2: dark.Accent2}
				} else {
					dark = themeColors{Accent: light.Accent, Accent2: light.Accent2}
				}
			}
		}
		if dark.Accent == "" {
			dark.Accent, dark.Accent2 = light.Accent, light.Accent2
		}
		if light.Accent == "" {
			light.Accent, light.Accent2 = dark.Accent, dark.Accent2
		}
		out = append(out, ThemePalette{ID: "ha-" + slugify(name), Name: strings.TrimSpace(name), Source: "ha", Dark: dark, Light: light})
	}
	if len(out) == 0 {
		return nil, errors.New("In der Datei wurde kein Home-Assistant-Theme mit Farben gefunden (erwartet z. B. primary-color, primary-background-color).")
	}
	return out, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(strings.ToLower(s))
	s = strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "theme"
	}
	return s
}

// mergeHAThemes ersetzt gleichnamige Themes und haengt neue an.
func mergeHAThemes(old, add []ThemePalette) []ThemePalette {
	out := append([]ThemePalette(nil), old...)
	for _, p := range add {
		replaced := false
		for i := range out {
			if out[i].ID == p.ID {
				out[i], replaced = p, true
				break
			}
		}
		if !replaced {
			out = append(out, p)
		}
	}
	return out
}

// BrandingHAImportWeb: POST /admin/branding/ha-import (YAML einfuegen oder Datei)
func (h *Handler) BrandingHAImportWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, haThemeMaxBytes+(1<<20))
	var added []ThemePalette
	err := func() error {
		if err := r.ParseMultipartForm(haThemeMaxBytes); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return errors.New("Die Datei ist zu groß (höchstens 2 MB).")
		}
		data := []byte(r.FormValue("yaml"))
		if f, _, err := r.FormFile("file"); err == nil {
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, haThemeMaxBytes+1))
			if err != nil {
				return err
			}
			if len(b) > haThemeMaxBytes {
				return errors.New("Die Datei ist zu groß (höchstens 2 MB).")
			}
			data = b
		}
		if strings.TrimSpace(string(data)) == "" {
			return errors.New("Bitte eine Theme-Datei wählen oder den YAML-Text einfügen.")
		}
		list, err := parseHAThemes(data)
		if err != nil {
			return err
		}
		all := mergeHAThemes(h.loadHAThemes(r.Context()), list)
		if len(all) > haThemeMaxCount {
			return fmt.Errorf("Höchstens %d importierte Themes – bitte zuerst nicht benötigte entfernen.", haThemeMaxCount)
		}
		added = list
		return h.saveHAThemes(r.Context(), all)
	}()
	resetBrandingCache()
	msg := ""
	if err == nil {
		names := make([]string, len(added))
		for i, p := range added {
			names[i] = p.Name
		}
		msg = fmt.Sprintf("%d Theme(s) übernommen: %s. Jetzt oben als Farbschema auswählen.", len(added), strings.Join(names, ", "))
		componentLog("konfiguration").Info().Int("themes", len(added)).Str("user", getUser(r).ID).Msg("home-assistant-themes importiert")
	}
	brandingRedirect(w, r, msg, err)
}

// BrandingHADeleteWeb: POST /admin/branding/ha/{id}/delete
func (h *Handler) BrandingHADeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	var keep []ThemePalette
	for _, p := range h.loadHAThemes(ctx) {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	err := h.saveHAThemes(ctx, keep)
	if err == nil && h.appSetting(ctx, keyBrandTheme, defaultTheme) == id {
		err = h.setAppSetting(ctx, keyBrandTheme, defaultTheme)
	}
	resetBrandingCache()
	brandingRedirect(w, r, "Theme entfernt.", err)
}
