package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

)

// Befehlszeile Strg+K (0.90.0 Teil 2): Aktionen, Seiten, Anlagen – nur Erlaubtes.

func paletteKinds(items []paletteItem) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Kind]++
	}
	return out
}

func paletteHas(items []paletteItem, kind, label string) bool {
	for _, it := range items {
		if it.Kind == kind && it.Label == label {
			return true
		}
	}
	return false
}

func TestPaletteSearch(t *testing.T) {
	assets := []paletteAsset{{ID: "a1", Name: "Netzpresse 4"}}
	net := &BaseData{Page: "dashboard", navCan: func(p string) bool { return p == permNetwork || p == "faults.edit" }}
	items := paletteItems(net, buildAreas(net, uiAreaPrefs{}), "netz", assets)
	if !paletteHas(items, "page", "Netzwerk") {
		t.Errorf("Seite Netzwerk fehlt: %+v", items)
	}
	if paletteKinds(items)["asset"] != 0 {
		t.Error("Anlagen ohne Ansichtsrecht der Infrastruktur")
	}
	none := &BaseData{Page: "dashboard", navCan: func(string) bool { return false }}
	if paletteHas(paletteItems(none, buildAreas(none, uiAreaPrefs{}), "netz", assets), "page", "Netzwerk") {
		t.Error("Seite ohne Recht gefunden")
	}
	infra := &BaseData{Page: "dashboard", CanViewInfra: true, navCan: func(string) bool { return false }}
	items = paletteItems(infra, buildAreas(infra, uiAreaPrefs{}), "netz", assets)
	if !paletteHas(items, "asset", "Netzpresse 4") {
		t.Errorf("Anlage fehlt: %+v", items)
	}
	for _, it := range items {
		if it.Kind == "asset" && it.Href != "/infrastructure/a1" {
			t.Errorf("Anlage verlinkt %q", it.Href)
		}
	}
	short := paletteItems(net, buildAreas(net, uiAreaPrefs{Favorites: []string{"network"}}), "n", assets)
	k := paletteKinds(short)
	if k["asset"] != 0 || !paletteHas(short, "action", "Störung melden") || !paletteHas(short, "page", "Netzwerk") {
		t.Errorf("kurze Eingabe: nur Aktionen und Favoriten erwartet: %+v", short)
	}
	if paletteHas(short, "action", "Aufgabe anlegen") {
		t.Error("Aktion ohne Anlegen-Recht")
	}
	for _, it := range paletteItems(net, buildAreas(net, uiAreaPrefs{}), "", nil) {
		if it.Kind == "action" && !strings.HasPrefix(it.Href, "#create:") {
			t.Errorf("Aktion %q ohne Anlegen-Verweis: %q", it.Label, it.Href)
		}
	}
	b, err := json.Marshal(map[string]any{"items": []paletteItem{{Kind: "asset", Label: `<b>"x"</b>`}}})
	if err != nil || !json.Valid(b) || strings.Contains(string(b), "<b>") {
		t.Errorf("JSON nicht sicher: %s", b)
	}
	rec := httptest.NewRecorder()
	(&Handler{}).PaletteWeb(rec, httptest.NewRequest("GET", "/api/palette?q=netz", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("ohne Anmeldung: HTTP %d", rec.Code)
	}
}

func TestFramePaletteHooks(t *testing.T) {
	out := renderFrameForTest(t, "server_config", frameTestData(true))
	for _, want := range []string{`data-palette-open`, `placeholder="Was möchtest du tun?  (Strg&#43;K)"`, `aria-label="Was möchtest du tun?"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Kopfleiste des neuen Rahmens ohne %q", want)
		}
	}
	if strings.Contains(renderFrameForTest(t, "server_config", frameTestData(false)), "data-palette-open") {
		t.Error("Suchzeile im klassischen Rahmen")
	}
}
