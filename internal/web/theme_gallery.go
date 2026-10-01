package web

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

// Theme-Galerie: bekannte Home-Assistant-Themes (Nordic, Caule, iOS, Metro/
// Fluent), deren Farben bereits mit parseHAThemes ausgelesen und als JSON
// eingebettet sind. Der Administrator uebernimmt sie mit einem Klick in die
// importierten Themes – ohne Datei-Upload. Neu erzeugen: siehe
// TestGenerateHAThemeGallery (theme_gallery_test.go).

//go:embed ha_theme_gallery.json
var haGalleryJSON []byte

// galleryFamily: eine Theme-Familie mit Urheber-Angabe.
type galleryFamily struct {
	Name, Author, URL string
	Themes            []ThemePalette
}

var (
	galleryOnce sync.Once
	gallery     []galleryFamily
)

func haGallery() []galleryFamily {
	galleryOnce.Do(func() {
		if err := json.Unmarshal(haGalleryJSON, &gallery); err != nil {
			componentLog("system").Error().Err(err).Msg("theme-galerie defekt")
		}
	})
	return gallery
}

func galleryTheme(id string) (ThemePalette, bool) {
	for _, f := range haGallery() {
		for _, p := range f.Themes {
			if p.ID == id {
				return p, true
			}
		}
	}
	return ThemePalette{}, false
}

// GalleryView: Familien fuer die Admin-Seite, mit Markierung bereits
// uebernommener Themes.
type GalleryView struct {
	galleryFamily
	Imported map[string]bool
}

func (b Branding) Gallery() []GalleryView {
	have := map[string]bool{}
	for _, p := range b.HAThemes {
		have[p.ID] = true
	}
	var out []GalleryView
	for _, f := range haGallery() {
		out = append(out, GalleryView{galleryFamily: f, Imported: have})
	}
	return out
}

// GalleryCount: Anzahl Themes in der Galerie.
func (b Branding) GalleryCount() int {
	n := 0
	for _, f := range haGallery() {
		n += len(f.Themes)
	}
	return n
}

// BrandingGalleryWeb: POST /admin/branding/gallery/{id} – Galerie-Theme
// uebernehmen (optional gleich als Firmenstandard).
func (h *Handler) BrandingGalleryWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBranding(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	p, ok := galleryTheme(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "unbekannt", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	all := mergeHAThemes(h.loadHAThemes(ctx), []ThemePalette{p})
	if len(all) > haThemeMaxCount {
		brandingRedirect(w, r, "", fmt.Errorf("Höchstens %d übernommene Themes – bitte zuerst nicht benötigte entfernen.", haThemeMaxCount))
		return
	}
	err := h.saveHAThemes(ctx, all)
	msg := "„" + p.Name + "“ übernommen – jetzt bei den Farbschemata auswählbar."
	if err == nil && r.FormValue("default") == "1" {
		err = h.setAppSetting(ctx, keyBrandTheme, p.ID)
		msg = "„" + p.Name + "“ übernommen und als Firmenstandard gesetzt."
	}
	resetBrandingCache()
	brandingRedirect(w, r, msg, err)
}

// Preview: CSS-Variablen fuer die Vorschau-Kachel (links Dunkel, rechts
// Hell; fehlende Flaechenfarben wie im PDH-Standard).
func (p ThemePalette) Preview() template.CSS {
	or := func(v, d string) string {
		if v == "" || !safeCSSColor(v) {
			return d
		}
		return v
	}
	return template.CSS(strings.Join([]string{
		"--da:" + or(p.Dark.Accent, "#4f6ef7"), "--db:" + or(p.Dark.Bg, "#0f1117"), "--dc:" + or(p.Dark.Bg2, "#1a1d27"), "--dt:" + or(p.Dark.Text, "#e8eaf6"),
		"--la:" + or(p.Light.Accent, "#3457d5"), "--lb:" + or(p.Light.Bg, "#f5f7fb"), "--lc:" + or(p.Light.Bg2, "#ffffff"), "--lt:" + or(p.Light.Text, "#111827"),
	}, ";"))
}

// buildHAGallery erzeugt die Galerie aus Theme-Dateien (nur fuer den
// Generator-Test). Gleiche Farbsaetze innerhalb einer Familie werden
// zusammengefasst.
func buildHAGallery(families []galleryFamily, files map[string][]byte) ([]galleryFamily, error) {
	var out []galleryFamily
	for _, f := range families {
		data, ok := files[f.Name]
		if !ok {
			return nil, fmt.Errorf("datei fuer %s fehlt", f.Name)
		}
		list, err := parseHAThemes(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		seen := map[string]bool{}
		for _, p := range list {
			sig, _ := json.Marshal([]themeColors{p.Dark, p.Light})
			if seen[string(sig)] {
				continue
			}
			seen[string(sig)] = true
			f.Themes = append(f.Themes, p)
		}
		sort.Slice(f.Themes, func(i, j int) bool { return strings.ToLower(f.Themes[i].Name) < strings.ToLower(f.Themes[j].Name) })
		out = append(out, f)
	}
	return out, nil
}
