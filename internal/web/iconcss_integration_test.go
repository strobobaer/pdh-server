//go:build integration

package web

import (
	"strings"
	"testing"
)

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
