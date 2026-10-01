package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHAGalleryEmbedded(t *testing.T) {
	g := haGallery()
	if len(g) < 4 {
		t.Fatalf("Galerie hat nur %d Familien", len(g))
	}
	ids := map[string]bool{}
	for _, f := range g {
		if f.Author == "" || !strings.HasPrefix(f.URL, "https://") {
			t.Errorf("%s: Urheber/Quelle fehlt", f.Name)
		}
		for _, p := range f.Themes {
			if ids[p.ID] {
				t.Errorf("doppelte Kennung %s", p.ID)
			}
			ids[p.ID] = true
			if !strings.HasPrefix(p.ID, "ha-") || p.Dark.Accent == "" || p.Light.Accent == "" {
				t.Errorf("%s unvollständig: %+v", p.ID, p)
			}
			for _, c := range []themeColors{p.Dark, p.Light} {
				for _, v := range []string{c.Accent, c.Accent2, c.Bg, c.Bg2, c.Bg3, c.Text, c.Muted, c.Border, c.Green, c.Amber, c.Red} {
					if v != "" && !safeCSSColor(v) {
						t.Errorf("%s: unsicherer Wert %q", p.ID, v)
					}
				}
			}
		}
	}
	for _, want := range []string{"ha-nordic", "ha-metro-blue", "ha-ios-dark-mode"} {
		if !ids[want] {
			t.Errorf("Galerie ohne %s", want)
		}
	}
	if _, ok := galleryTheme("ha-nordic"); !ok {
		t.Error("galleryTheme findet Nordic nicht")
	}
}

// Erzeugt ha_theme_gallery.json aus den Original-Theme-Dateien neu:
//
//	PDH_HA_GALLERY_DIR=/pfad/zu/themes go test ./internal/web/ -run TestGenerateHAThemeGallery
//
// Erwartete Dateien im Ordner: nordic.yaml, nordic-blue.yaml, caule.yaml,
// ios-dark-mode.yaml, ios-themes.yaml, metro.yaml (aus Home Assistant themes/).
func TestGenerateHAThemeGallery(t *testing.T) {
	dir := os.Getenv("PDH_HA_GALLERY_DIR")
	if dir == "" {
		t.Skip("PDH_HA_GALLERY_DIR nicht gesetzt")
	}
	read := func(names ...string) []byte {
		var all []byte
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, '\n')
			all = append(all, b...)
		}
		return all
	}
	families := []galleryFamily{
		{Name: "Nordic", Author: "Colton Dick", URL: "https://github.com/coltondick/nordic-theme"},
		{Name: "Caule Themes Pack", Author: "Ricardo Correia (Home Assistant Brasil)", URL: "https://github.com/orickcorreia"},
		{Name: "iOS Themes", Author: "Bas Nijholt", URL: "https://github.com/basnijholt/lovelace-ios-themes"},
		{Name: "Metro & Fluent", Author: "Madelena Mak", URL: "https://github.com/Madelena/Metrology-for-Hass"},
	}
	files := map[string][]byte{
		"Nordic":            read("nordic.yaml", "nordic-blue.yaml"),
		"Caule Themes Pack": read("caule.yaml"),
		"iOS Themes":        read("ios-dark-mode.yaml", "ios-themes.yaml"),
		"Metro & Fluent":    read("metro.yaml"),
	}
	g, err := buildHAGallery(families, files)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("ha_theme_gallery.json", append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range g {
		t.Logf("%s: %d Themes", f.Name, len(f.Themes))
	}
}
