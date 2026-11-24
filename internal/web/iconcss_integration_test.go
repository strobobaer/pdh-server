//go:build integration

package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/core/users"
)

// TestIconCSSChosenViaSave: ueber den echten Speicher-Handler der Kategorien
// gewaehltes Symbol wird sofort eingebettet, die Adresse der Stildatei aendert sich.
func TestIconCSSChosenViaSave(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("ic", "admin", false)
	e.warm()
	admin := &users.User{ID: uid, Role: users.Role("admin")}
	h := &Handler{db: e.pool, rbac: e.rb}
	resetIconCSSCache()
	h.iconCSSEntry(e.ctx)
	href0 := iconCSSHref()
	name := "Symbolweg " + e.sfx
	t.Cleanup(func() {
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM categories WHERE name = $1`, name)
		resetIconCSSCache()
	})

	form := url.Values{"name": {name}, "color": {"#112233"}, "icon": {"ti-ad-2"}}
	req := withUser(httptest.NewRequest("POST", "/categories", strings.NewReader(form.Encode())), admin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.CategorySaveWeb(rec, req)
	if rec.Code != 303 || strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("speichern: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if href1 := iconCSSHref(); href1 == href0 {
		t.Errorf("Adresse der Stildatei unveraendert: %s", href1)
	}
	css, _ := h.iconCSS(e.ctx)
	if !strings.Contains(string(css), "\n.ti-ad-2{--ti:url(\"data:image/svg+xml,") {
		t.Error("ti-ad-2 soll nach dem Speichern eingebettet sein")
	}
}

// TestIconCSSRefreshTick: der Hintergrundtakt bemerkt eine geaenderte Auswahl
// (z. B. am anderen Server des Verbunds) und baut nur dann neu.
func TestIconCSSRefreshTick(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	resetIconCSSCache()
	h.iconCSSEntry(e.ctx)
	href0 := iconCSSHref()
	if h.refreshIconCSS(); iconCSSHref() != href0 {
		t.Fatal("ohne Aenderung darf sich die Adresse nicht aendern")
	}
	name := "Symboltakt " + e.sfx
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO categories (name, icon) VALUES ($1, 'ti-ad-2')`, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM categories WHERE name = $1`, name)
		resetIconCSSCache()
	})
	h.refreshIconCSS()
	if iconCSSHref() == href0 {
		t.Error("Adresse soll sich nach dem Takt geaendert haben")
	}
}

// TestIconCSSChosen: ein in einer Kategorie gewaehltes (sonst ungenutztes) Symbol
// wird in die Stildatei eingebettet; die Kennung aendert sich.
func TestIconCSSChosen(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	resetIconCSSCache()
	css0, key0 := h.iconCSS(e.ctx)
	if !strings.Contains(string(css0), "\n.ti-ad-2{--ti:url(/vendor/") {
		t.Fatal("ti-ad-2 soll vor der Wahl als Adresse stehen")
	}
	name := "Symboltest " + e.sfx
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO categories (name, icon) VALUES ($1, 'ti-ad-2')`, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM categories WHERE name = $1`, name)
		resetIconCSSCache()
	})

	resetIconCSSCache()
	css1, key1 := h.iconCSS(e.ctx)
	if !strings.Contains(string(css1), "\n.ti-ad-2{--ti:url(\"data:image/svg+xml,") {
		t.Error("ti-ad-2 soll nach der Wahl eingebettet sein")
	}
	if key0 == key1 {
		t.Errorf("Kennung unveraendert: %s", key0)
	}
}
