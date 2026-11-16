package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/it"
)

// itCatEnv: Handler mit Testdatenbank und IT-Dienst; räumt eigene Kategorien auf.
func itCatEnv(t *testing.T) (*Handler, string) {
	t.Helper()
	h, pool, _ := itNetPageEnv(t)
	h.it = it.NewService(it.NewRepository(pool))
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE name LIKE $1`, "%"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM it_asset_categories WHERE name LIKE $1`, "%"+sfx)
	})
	return h, sfx
}

// itCatDo wie itNetDo, setzt aber den Pfadparameter {key}.
func itCatDo(fn http.HandlerFunc, method, path, key, uid string, form url.Values) *httptest.ResponseRecorder {
	return itNetDo(func(w http.ResponseWriter, r *http.Request) {
		chi.RouteContext(r.Context()).URLParams.Add("key", key)
		fn(w, r)
	}, method, path, "", uid, form)
}

func TestITCategoryIcon(t *testing.T) {
	if got := itCategoryIcon(it.Category{Key: "server", Icon: "ti-camera"}); got != "ti-camera" {
		t.Errorf("eigenes Symbol: %q", got)
	}
	if got := itCategoryIcon(it.Category{Key: "printer"}); got != iconClass("it.printer") {
		t.Errorf("Vorgabe der Symbolverwaltung: %q", got)
	}
	if got := itCategoryIcon(it.Category{Key: "kameras"}); got != "ti-box" {
		t.Errorf("neue Kategorie ohne Symbol: %q", got)
	}
}

func TestITCategoriesPerms(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	if w := itNetDo(h.ITCategoriesPage, "GET", "/it/categories", "", "", nil); w.Code != http.StatusForbidden {
		t.Errorf("Seite ohne it.view: %d", w.Code)
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == "it.view" }
	for name, fn := range map[string]http.HandlerFunc{
		"anlegen": h.ITCategoryCreateWeb, "ändern": h.ITCategoryUpdateWeb,
		"ausblenden": h.ITCategoryActiveWeb, "löschen": h.ITCategoryDeleteWeb,
	} {
		if w := itNetDo(fn, "POST", "/it/categories/x", "", "", url.Values{"name": {"X"}}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne it.categories: %d", name, w.Code)
		}
	}
	for _, p := range []string{"/it/categories", "/it/categories/kameras", "/it/categories/kameras/active", "/it/categories/kameras/delete"} {
		_, perm, ref, _, hit := matchGate("POST", p)
		if !hit || perm != "it.categories" || ref != "" {
			t.Errorf("Gate %s: %v %q %q", p, hit, perm, ref)
		}
	}
}

func TestITCategoriesPageAndForms(t *testing.T) {
	h, sfx := itCatEnv(t)
	ctx := context.Background()
	c := &it.Category{Name: "Kameras " + sfx, Icon: "ti-camera", SortOrder: 900, MonitorDefault: true}
	if err := h.it.CreateCategory(ctx, c); err != nil {
		t.Fatal(err)
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == "it.view" }
	w := itNetDo(h.ITCategoriesPage, "GET", "/it/categories", "", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Seite: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Kameras "+sfx) || !strings.Contains(body, "ti-camera") || !strings.Contains(body, "Sonstiges") {
		t.Error("Kategorien fehlen in der Liste")
	}
	if strings.Contains(body, `action="/it/categories`) {
		t.Error("ohne it.categories sind Formulare sichtbar")
	}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	body = itNetDo(h.ITCategoriesPage, "GET", "/it/categories", "", "", nil).Body.String()
	for _, want := range []string{`action="/it/categories"`, `action="/it/categories/` + c.Key + `"`, `action="/it/categories/` + c.Key + `/delete"`} {
		if !strings.Contains(body, want) {
			t.Errorf("mit it.categories fehlt: %s", want)
		}
	}
	if strings.Contains(body, `action="/it/categories/network/delete"`) || strings.Contains(body, `action="/it/categories/other/active"`) {
		t.Error("feste Kategorien dürfen weder löschbar noch ausblendbar sein")
	}
}

func TestITCategoriesCRUDWeb(t *testing.T) {
	h, sfx := itCatEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	loc := func(h http.Header) string { return h.Get("Location") }

	// Anlegen
	w := itNetDo(h.ITCategoryCreateWeb, "POST", "/it/categories", "", "",
		url.Values{"name": {"Drohnen " + sfx}, "icon": {"ti-plane"}, "sort_order": {"901"}, "monitor": {"1"}, "grace": {"15"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), "n=created") {
		t.Fatalf("anlegen: %d %s", w.Code, loc(w.Header()))
	}
	list, _ := h.it.Categories(ctx, true)
	var key string
	for _, c := range list {
		if c.Name == "Drohnen "+sfx {
			key = c.Key
			if c.Icon != "ti-plane" || c.SortOrder != 901 || !c.MonitorDefault || c.GraceMinutes == nil || *c.GraceMinutes != 15 {
				t.Errorf("gespeichert: %+v", c)
			}
		}
	}
	if key == "" {
		t.Fatal("Kategorie nicht angelegt")
	}
	// Ungültig: Name leer, Symbol, Karenz, Reihenfolge
	for _, bad := range []struct {
		form url.Values
		e    string
	}{
		{url.Values{"name": {"  "}}, "e=name"},
		{url.Values{"name": {"X " + sfx}, "icon": {"Kamera!"}}, "e=icon"},
		{url.Values{"name": {"X " + sfx}, "grace": {"0"}}, "e=grace"},
		{url.Values{"name": {"X " + sfx}, "grace": {"abc"}}, "e=grace"},
		{url.Values{"name": {"X " + sfx}, "sort_order": {"x"}}, "e=sort"},
	} {
		w := itNetDo(h.ITCategoryCreateWeb, "POST", "/it/categories", "", "", bad.form)
		if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), bad.e) {
			t.Errorf("%v: %d %s", bad.form, w.Code, loc(w.Header()))
		}
	}
	// Ändern (Karenz leeren)
	w = itCatDo(h.ITCategoryUpdateWeb, "POST", "/it/categories/"+key, key, "",
		url.Values{"name": {"Drohnen neu " + sfx}, "icon": {""}, "sort_order": {"5"}, "grace": {""}})
	if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), "n=saved") {
		t.Fatalf("ändern: %d %s", w.Code, loc(w.Header()))
	}
	c, _ := h.it.Category(ctx, key)
	if c.Name != "Drohnen neu "+sfx || c.Icon != "" || c.SortOrder != 5 || c.MonitorDefault || c.GraceMinutes != nil {
		t.Errorf("nach Ändern: %+v", c)
	}
	// Unbekannt: 404
	if w := itCatDo(h.ITCategoryUpdateWeb, "POST", "/it/categories/gibtsnicht", "gibtsnicht", "", url.Values{"name": {"A"}}); w.Code != http.StatusNotFound {
		t.Errorf("unbekannt ändern: %d", w.Code)
	}
	// Ausblenden / Einblenden
	w = itCatDo(h.ITCategoryActiveWeb, "POST", "/it/categories/"+key+"/active", key, "", url.Values{"on": {"0"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), "n=hidden") {
		t.Errorf("ausblenden: %d %s", w.Code, loc(w.Header()))
	}
	if c, _ := h.it.Category(ctx, key); c.Active {
		t.Error("noch aktiv")
	}
	w = itCatDo(h.ITCategoryActiveWeb, "POST", "/it/categories/"+key+"/active", key, "", url.Values{"on": {"1"}})
	if !strings.Contains(loc(w.Header()), "n=shown") {
		t.Errorf("einblenden: %s", loc(w.Header()))
	}
	// Feste Kategorie: Fehlermeldung, unverändert
	for _, fn := range []http.HandlerFunc{h.ITCategoryActiveWeb, h.ITCategoryDeleteWeb} {
		w = itCatDo(fn, "POST", "/it/categories/network/x", "network", "", url.Values{"on": {"0"}})
		if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), "e=builtin") {
			t.Errorf("fest: %d %s", w.Code, loc(w.Header()))
		}
	}
	// Benutzt: Löschen abgelehnt
	if _, err := h.db.Exec(ctx, `INSERT INTO it_assets (name, type, status, created_by) VALUES ($1, $2, 'active', $3::uuid)`,
		"Asset "+sfx, key, pdhSystemUserID); err != nil {
		t.Fatal(err)
	}
	w = itCatDo(h.ITCategoryDeleteWeb, "POST", "/it/categories/"+key+"/delete", key, "", nil)
	if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), "e=inuse") {
		t.Errorf("löschen benutzt: %d %s", w.Code, loc(w.Header()))
	}
	if _, err := h.it.Category(ctx, key); err != nil {
		t.Error("benutzte Kategorie wurde gelöscht")
	}
	_, _ = h.db.Exec(ctx, `DELETE FROM it_assets WHERE name = $1`, "Asset "+sfx)
	w = itCatDo(h.ITCategoryDeleteWeb, "POST", "/it/categories/"+key+"/delete", key, "", nil)
	if w.Code != http.StatusSeeOther || !strings.Contains(loc(w.Header()), "n=deleted") {
		t.Errorf("löschen: %d %s", w.Code, loc(w.Header()))
	}
	if _, err := h.it.Category(ctx, key); err == nil {
		t.Error("Kategorie noch da")
	}
}
