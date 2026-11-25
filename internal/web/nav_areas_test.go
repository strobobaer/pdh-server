package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Bereichsleiste des neuen Rahmens (0.90.0 Teil 2, Entwurf 5.2).

func TestNavAreasCoverAllDefs(t *testing.T) {
	seen := map[string]int{}
	for _, a := range navAreaDefs {
		for _, k := range a.Items {
			seen[k]++
		}
	}
	for k := range navOutsideAreas {
		seen[k]++
	}
	for _, d := range navDefs {
		if seen[d.Key] != 1 {
			t.Errorf("Menüeintrag %s steht %d-mal in den Bereichen", d.Key, seen[d.Key])
		}
		delete(seen, d.Key)
	}
	for k := range seen {
		t.Errorf("Bereiche nennen unbekannten Eintrag %s", k)
	}
	if len(navAreaDefs) != 9 {
		t.Errorf("%d Bereiche statt 9", len(navAreaDefs))
	}
}

func areaKeys(v areaView) []string {
	var out []string
	for _, a := range v.Areas {
		out = append(out, a.Key)
	}
	return out
}

func areaItems(v areaView, key string) []string {
	for _, a := range v.Areas {
		if a.Key == key {
			var out []string
			for _, it := range a.Items {
				out = append(out, it.Key)
			}
			return out
		}
	}
	return nil
}

func TestBuildAreasPermissions(t *testing.T) {
	none := &BaseData{Page: "dashboard", navCan: func(string) bool { return false }}
	v := buildAreas(none, uiAreaPrefs{})
	if got := strings.Join(areaItems(v, "verwaltung"), ","); got != "categories" {
		t.Errorf("ohne Rechte in Verwaltung nur Kategorien, war %q", got)
	}
	if got := strings.Join(areaItems(v, "start"), ","); got != "dashboard,leitstand" || v.Areas[0].Key != "start" {
		t.Errorf("Start ohne Rechte: %q, erster Bereich %q", got, v.Areas[0].Key)
	}
	for _, k := range areaKeys(v) {
		if k == "daten" || k == "material" || k == "anlagen" || k == "dienste" {
			t.Errorf("Bereich %s ohne sichtbaren Eintrag erscheint", k)
		}
	}
	if v.Current != "start" || len(v.Tabs) != 2 {
		t.Errorf("aktueller Bereich %q, Reiter %d", v.Current, len(v.Tabs))
	}
	net := &BaseData{Page: "network", navCan: func(p string) bool { return p == permNetwork }}
	v = buildAreas(net, uiAreaPrefs{})
	if got := strings.Join(areaItems(v, "verwaltung"), ","); got != "categories,network" && got != "network,categories" {
		t.Errorf("mit system.network: %q", got)
	}
	if v.Current != "verwaltung" {
		t.Errorf("Seite Netzwerk gehört zu %q", v.Current)
	}
	sel := false
	for _, it := range v.Tabs {
		sel = sel || it.Key == "network"
	}
	if !sel {
		t.Error("Reiter der Verwaltung ohne Netzwerk")
	}
}

func TestBuildAreasPrefs(t *testing.T) {
	all := &BaseData{Page: "dashboard", navCan: func(string) bool { return true }}
	v := buildAreas(all, uiAreaPrefs{Order: []string{"material", "start", "gibt-es-nicht"}, Favorites: []string{"network", "unbekannt"}})
	if k := areaKeys(v); len(k) < 2 || k[0] != "material" || k[1] != "start" {
		t.Errorf("Reihenfolge: %v", k)
	}
	if len(v.Favorites) != 1 || v.Favorites[0].Key != "network" {
		t.Errorf("Favoriten: %+v", v.Favorites)
	}
	none := &BaseData{Page: "dashboard", navCan: func(string) bool { return false }}
	if v := buildAreas(none, uiAreaPrefs{Favorites: []string{"network"}}); len(v.Favorites) != 0 {
		t.Error("Favorit ohne Recht sichtbar")
	}
}

func jsonPost(t *testing.T, fn http.HandlerFunc, path, body string, user bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user {
		req = withTestUser(req)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func TestUIAreasSave(t *testing.T) {
	ctx := context.Background()
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	if rec := jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"order":["material"],"favorites":["tickets"]}`, true); rec.Code != http.StatusOK {
		t.Fatalf("speichern: HTTP %d %s", rec.Code, rec.Body.String())
	}
	if p := h.areaPrefs(ctx, "u1"); len(p.Order) != 1 || p.Order[0] != "material" || len(p.Favorites) != 1 {
		t.Errorf("gespeichert: %+v", p)
	}
	if rec := jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"order":["`+strings.Repeat("x", 9000)+`"]}`, true); rec.Code != http.StatusBadRequest {
		t.Errorf("zu groß: HTTP %d", rec.Code)
	}
	if rec := jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{}`, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("ohne Anmeldung: HTTP %d", rec.Code)
	}
	if rec := jsonPost(t, h.UIAreasDefaultSaveWeb, "/admin/gestaltung/areas", `{"order":["vorgaenge"]}`, true); rec.Code != http.StatusForbidden {
		t.Errorf("Firmenvorgabe ohne Recht: HTTP %d", rec.Code)
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permUIDefaults }
	if rec := jsonPost(t, h.UIAreasDefaultSaveWeb, "/admin/gestaltung/areas", `{"order":["vorgaenge"]}`, true); rec.Code != http.StatusOK {
		t.Errorf("Firmenvorgabe mit Recht: HTTP %d", rec.Code)
	}
	if rec := jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"reset":true}`, true); rec.Code != http.StatusOK {
		t.Errorf("zurücksetzen: HTTP %d", rec.Code)
	}
	if p := h.areaPrefs(ctx, "u1"); len(p.Order) != 1 || p.Order[0] != "vorgaenge" {
		t.Errorf("ohne eigene Anordnung gilt die Firmenvorgabe: %+v", p)
	}
	if _, perm, _, _, ok := matchGate("POST", "/admin/gestaltung/areas"); !ok || perm != permUIDefaults {
		t.Errorf("Gate-Regel fehlt: %v %q", ok, perm)
	}
}

func TestFrameRail(t *testing.T) {
	d := frameTestData(true)
	d.Page = "network"
	d.navCan = func(p string) bool { return p == permNetwork }
	d.Rail = buildAreas(&d.BaseData, uiAreaPrefs{Favorites: []string{"network"}})
	out := renderFrameForTest(t, "server_config", d)
	for _, want := range []string{`data-area="start"`, `data-area="verwaltung"`, `class="frame-tabs"`, `aria-selected="true"`,
		`class="rail-fav"`, `href="/admin/network"`, `data-rail-more`} {
		if !strings.Contains(out, want) {
			t.Errorf("Bereichsleiste ohne %q", want)
		}
	}
	if strings.Contains(out, `data-area="daten"`) {
		t.Error("Bereich ohne sichtbaren Eintrag in der Leiste")
	}
}
