package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pdh/internal/core/infrastructure"
	"pdh/internal/core/users"
)

// Korrekturen aus der Schlussprüfung von 0.82.65 (gebaut als 0.82.63) (Codex und Claude).

// Rücksprung nach dem Umschalten: nur eigene, saubere Pfade (kein „/\“, keine
// Steuerzeichen, die Browser zu „//fremd“ machen).
func TestBackToRejectsTricks(t *testing.T) {
	for _, back := range []string{"/\\fremd.example", "/\t/fremd.example", "/\n/fremd.example", "\\\\fremd", "//fremd", "/x\\y", "https://fremd.example/"} {
		req := httptest.NewRequest("POST", "/account/ui-frame", strings.NewReader("back="+urlQueryEscape(back)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := backTo(req); got != "/" {
			t.Errorf("back=%q → %q, erwartet /", back, got)
		}
	}
	for _, ref := range []string{"http://example.com/%5Cfremd.example", "http://example.com/%2F%2Ffremd", "http://example.com/a%09b"} {
		req := httptest.NewRequest("POST", "/account/ui-frame", nil)
		req.Header.Set("Referer", ref)
		if got := backTo(req); got != "/" && strings.ContainsAny(got, "\\\t") {
			t.Errorf("Referer %q → %q", ref, got)
		} else if strings.HasPrefix(got, "//") || strings.HasPrefix(got, "/\\") {
			t.Errorf("Referer %q → %q", ref, got)
		}
	}
	req := httptest.NewRequest("POST", "/account/ui-frame", strings.NewReader("back=%2Ffaults%3Ftab%3Da"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got := backTo(req); got != "/faults?tab=a" {
		t.Errorf("gültiger Pfad: %q", got)
	}
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer("%", "%25", "&", "%26", "=", "%3D", "+", "%2B", "\t", "%09", "\n", "%0A", "\\", "%5C", "/", "%2F", ":", "%3A", "?", "%3F", " ", "%20")
	return r.Replace(s)
}

// Alle Seiten gehen über frameTemplateName – sonst erscheint eine Seite im neuen
// Rahmen klassisch mit totem Suchfeld (Anlagen-Detail, Schnellübersicht).
func TestNoDirectBaseRender(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`ExecuteTemplate\([^)]*"base\.gohtml"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if re.Match(b) {
			t.Errorf("%s führt base.gohtml direkt aus – frameTemplateName(data) verwenden", f)
		}
	}
}

// Favoriten und Reihenfolge getrennt: ein Favorit friert die Firmenreihenfolge nicht
// ein, ausgeblendete Favoriten bleiben erhalten, die Firmenvorgabe trägt keine Favoriten.
func TestAreaPrefsSeparate(t *testing.T) {
	ctx := context.Background()
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return p == permUIDefaults }}
	if rec := jsonPost(t, h.UIAreasDefaultSaveWeb, "/admin/gestaltung/areas", `{"order":["anlagen","start"],"favorites":["faults"]}`, true); rec.Code != http.StatusOK {
		t.Fatalf("Firmenvorgabe: HTTP %d", rec.Code)
	}
	if p := h.firmAreaPrefs(ctx); len(p.Favorites) != 0 {
		t.Errorf("Firmenvorgabe mit Favoriten: %+v", p)
	}
	if rec := jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"fav":"faults"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("Favorit: HTTP %d %s", rec.Code, rec.Body.String())
	}
	if p := h.areaPrefs(ctx, "u1"); len(p.Favorites) != 1 || p.Favorites[0] != "faults" || len(p.Order) != 2 || p.Order[0] != "anlagen" {
		t.Errorf("Favorit angeheftet: %+v", p)
	}
	jsonPost(t, h.UIAreasDefaultSaveWeb, "/admin/gestaltung/areas", `{"order":["material","start"]}`, true)
	if p := h.areaPrefs(ctx, "u1"); len(p.Order) != 2 || p.Order[0] != "material" {
		t.Errorf("geänderte Firmenreihenfolge kommt nicht an: %+v", p)
	}
	// ausgeblendeter Favorit (backup) bleibt beim Anheften eines weiteren erhalten
	_ = h.setUpdateSetting(ctx, "users.u1.ui_areas", `{"favorites":["backup","faults"]}`)
	jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"fav":"tickets"}`, true)
	if p := h.areaPrefs(ctx, "u1"); strings.Join(p.Favorites, ",") != "backup,faults,tickets" {
		t.Errorf("Favoriten nach Anheften: %v", p.Favorites)
	}
	jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"fav":"faults"}`, true)
	if p := h.areaPrefs(ctx, "u1"); strings.Join(p.Favorites, ",") != "backup,tickets" {
		t.Errorf("Favorit lösen: %v", p.Favorites)
	}
	// Reihenfolge: ausgeblendete Bereiche behalten ihren Platz, Favoriten bleiben
	_ = h.setUpdateSetting(ctx, "users.u1.ui_areas", `{"order":["verwaltung","start","vorgaenge"],"favorites":["faults"]}`)
	jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"order":["vorgaenge","start"]}`, true)
	if p := h.areaPrefs(ctx, "u1"); strings.Join(p.Order, ",") != "verwaltung,vorgaenge,start" || len(p.Favorites) != 1 {
		t.Errorf("Reihenfolge gespeichert: %+v", p)
	}
	// „Wie Firmenvorgabe“ nur für die Reihenfolge
	jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"reset_order":true}`, true)
	if p := h.areaPrefs(ctx, "u1"); len(p.Order) != 2 || p.Order[0] != "material" || len(p.Favorites) != 1 {
		t.Errorf("Reihenfolge zurück auf Firma: %+v", p)
	}
	// gespeichertes null zählt nicht als eigene Anordnung
	_ = h.setUpdateSetting(ctx, "users.u1.ui_areas", `null`)
	if p := h.areaPrefs(ctx, "u1"); len(p.Order) != 2 || p.Order[0] != "material" {
		t.Errorf("null: %+v", p)
	}
}

// Untere Leiste am Handy: Vorgabe Start, Vorgänge, Anlagen, Material (Entwurf 10.10).
func TestAreaDefaultOrder(t *testing.T) {
	all := &BaseData{Page: "dashboard", navCan: func(string) bool { return true }, CanViewInfra: true}
	if k := areaKeys(buildAreas(all, uiAreaPrefs{})); len(k) < 4 || strings.Join(k[:4], ",") != "start,vorgaenge,anlagen,material" {
		t.Errorf("Vorgabe-Reihenfolge: %v", k)
	}
}

// Schalter dreiwertig (Entwurf 10.7): mit eigener Wahl gibt es „Wie Firmenvorgabe“;
// am Terminal (Systembenutzer ohne eigene Anmeldung) kein Schalter.
func TestFrameSwitchMenu(t *testing.T) {
	d := frameTestData(true)
	d.UIFrame = "new"
	if out := renderFrameForTest(t, "server_config", d); !strings.Contains(out, `name="frame" value="default"`) {
		t.Error("eigene Wahl ohne „Wie Firmenvorgabe“")
	}
	if out := renderFrameForTest(t, "server_config", frameTestData(true)); strings.Contains(out, `name="frame" value="default"`) {
		t.Error("„Wie Firmenvorgabe“ ohne eigene Wahl")
	}
	sys := frameTestData(false)
	sys.IsSystemUser = true
	if strings.Contains(renderFrameForTest(t, "server_config", sys), `action="/account/ui-frame"`) {
		t.Error("Schalter am Terminal des Systembenutzers")
	}
}

// Beide Assistenten (und der KVP-Assistent) sind im neuen Rahmen da – Vorgabe des
// Anwenders vom 25.11.2026.
func TestFrameHasWizards(t *testing.T) {
	d := frameTestData(true)
	d.Ribbon = ribbonFor(&d.BaseData)
	out := renderFrameForTest(t, "server_config", d)
	for _, want := range []string{`id="crw"`, `id="cw"`, "window.pdhCreate", "window.pdhComplete", `data-ribbon-action="create"`} {
		if !strings.Contains(out, want) {
			t.Errorf("neuer Rahmen ohne %q", want)
		}
	}
}

// Seiten mit eigenem Kopf führen ihre eigene Vorlage aus und kennen den Schalter nicht.
func TestOwnHeadPagesIgnoreFrame(t *testing.T) {
	for file, tmpl := range map[string]string{"easy_mode.go": "easy.gohtml", "global_dashboard.go": "global_dashboard.gohtml", "terminal_page.go": "terminal.gohtml"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `ExecuteTemplate(w, "`+tmpl+`"`) || strings.Contains(string(b), "frameTemplateName") {
			t.Errorf("%s soll %s direkt ausführen", file, tmpl)
		}
	}
	for _, f := range []string{"login.gohtml", "easy.gohtml", "global_dashboard.gohtml", "terminal.gohtml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `template "ui-topbar"`) || strings.Contains(string(b), "/account/ui-frame") {
			t.Errorf("%s nutzt Teile des neuen Rahmens", f)
		}
	}
}

// Strg+K: Anlagen nur mit Recht am Datensatz, mit Pfad als Hinweis, Treffer der Suche
// nicht erneut nach dem Namen gefiltert; Störung und Ticket wie im Neu-anlegen-Assistenten.
func TestPaletteAssetsAndActions(t *testing.T) {
	nodes := []*infrastructure.Infrastructure{{ID: "a1", Name: "Presse 4"}, {ID: "a2", Name: "Presse 5"}, {ID: "a3", Name: "Ohne Namenstreffer"}}
	got := paletteAssets(nodes, func(id string) bool { return id != "a2" }, map[string]string{"a1": "Werk / Halle 1 / Presse 4"})
	if len(got) != 2 || got[0].ID != "a1" || got[0].Path != "Werk / Halle 1 / Presse 4" || got[1].ID != "a3" {
		t.Errorf("Anlagen: %+v", got)
	}
	infra := &BaseData{Page: "dashboard", CanViewInfra: true, navCan: func(string) bool { return false }}
	items := paletteItems(infra, buildAreas(infra, uiAreaPrefs{}), "presse", got)
	if !paletteHas(items, "asset", "Ohne Namenstreffer") {
		t.Errorf("Treffer der Suche erneut gefiltert: %+v", items)
	}
	for _, it := range items {
		if it.Kind == "asset" && it.Label == "Presse 4" && it.Hint != "Werk / Halle 1 / Presse 4" {
			t.Errorf("Hinweis ohne Pfad: %q", it.Hint)
		}
	}
	none := &BaseData{Page: "dashboard", navCan: func(string) bool { return false }}
	short := paletteItems(none, buildAreas(none, uiAreaPrefs{}), "", nil)
	if !paletteHas(short, "action", "Störung melden") || !paletteHas(short, "action", "Ticket anlegen") || paletteHas(short, "action", "Aufgabe anlegen") {
		t.Errorf("Aktionen ohne Rechte: %+v", short)
	}
}

// Zweite Runde (Gegenprüfung des Korrekturdurchgangs).

// Backslash in der Abfrage ist harmlos (Browser lesen ihn nur im Pfad als „/“).
func TestBackToKeepsBackslashInQuery(t *testing.T) {
	if got, ok := localPath(`/storage?q=C:\Lager`); !ok || got != `/storage?q=C:\Lager` {
		t.Errorf("Abfrage mit Backslash: %q %v", got, ok)
	}
	for _, bad := range []string{`/\fremd`, `/a\b?q=1`, "/a?q=\t", "/a?q=\x7f"} {
		if _, ok := localPath(bad); ok {
			t.Errorf("%q angenommen", bad)
		}
	}
}

// Terminal des Systembenutzers: keine eigene Oberfläche, keine eigenen Bereiche –
// dort gilt immer die Firmenvorgabe.
func TestSystemUserCannotChangeFrame(t *testing.T) {
	sysReq := func(path, body, ct string) *http.Request {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		return req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "sys1", Role: "viewer", IsSystemUser: true})) //nolint:staticcheck // Schlüssel wie getUser
	}
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.UIFrameSaveWeb(rec, sysReq("/account/ui-frame", "frame=new", "application/x-www-form-urlencoded"))
	if rec.Code != http.StatusForbidden || h.userUIFrame(context.Background(), "sys1") != "" {
		t.Errorf("Schalter als Systembenutzer: HTTP %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.UIAreasSaveWeb(rec, sysReq("/account/ui-areas", `{"fav":"faults"}`, "application/json"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("Bereiche als Systembenutzer: HTTP %d", rec.Code)
	}
	b := &BaseData{Page: "faults", IsSystemUser: true, navCan: func(string) bool { return true }}
	for _, tab := range ribbonFor(b) {
		for _, g := range tab.Groups {
			for _, it := range g.Items {
				if it.Action == "fav" || it.Action == "areas-order" || it.Action == "areas-firm" {
					t.Errorf("Menüband am Terminal mit %q", it.Action)
				}
			}
		}
	}
	sys := frameTestData(true)
	sys.IsSystemUser, sys.IsOverrideSession = true, true
	if strings.Contains(renderFrameForTest(t, "server_config", sys), `action="/account/ui-frame"`) {
		t.Error("Schalter am Terminal mit Rückkehr-Cookie")
	}
}

// Höchstzahl der Favoriten: der 13. verdrängt den ältesten (auch einen ausgeblendeten),
// statt in einer Sackgasse „zu viele Favoriten“ zu enden.
func TestFavoritesRollOver(t *testing.T) {
	ctx := context.Background()
	h := &Handler{}
	keys := []string{"backup", "faults", "tickets", "tasks", "maintenance", "inventory", "storage", "calendar", "users", "roles", "network", "printers"}
	_ = h.setUpdateSetting(ctx, "users.u1.ui_areas", `{"favorites":["`+strings.Join(keys, `","`)+`"]}`)
	if rec := jsonPost(t, h.UIAreasSaveWeb, "/account/ui-areas", `{"fav":"kvp"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("13. Favorit: HTTP %d %s", rec.Code, rec.Body.String())
	}
	p := h.areaPrefs(ctx, "u1")
	if len(p.Favorites) != maxAreaFavorites || p.Favorites[0] != "faults" || p.Favorites[len(p.Favorites)-1] != "kvp" {
		t.Errorf("Favoriten: %v", p.Favorites)
	}
}
