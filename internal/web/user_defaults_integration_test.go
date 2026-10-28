package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Standards für alle: eigene Einstellung → Firmenstandard → Vorgabe im Code.
func TestDefaultsIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	h := &Handler{db: pool, rbac: rb}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	newUser := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', $2, 'T', $3, '', '') RETURNING id::text`, name+sfx, name, role).Scan(&id))
		return id
	}
	admin := newUser("anna", "admin")
	plain := newUser("bert", "worker")
	own := newUser("carl", "worker")
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, admin, plain, own)
		for _, k := range []string{keyDefaultNav, keyDefaultWidgets, defaultsInfoKey("nav"), defaultsInfoKey("dashboard")} {
			_, _ = pool.Exec(ctx, `DELETE FROM app_settings WHERE key = $1`, k)
		}
		resetDefaultsCache()
	}()
	_, err = pool.Exec(ctx, `UPDATE users SET dashboard_widgets = '[{"id":"a1","type":"note","size":2,"config":{"note":"Hallo"}}]',
		nav_layout = '[{"key":"top","label":"","items":["dashboard"]},{"key":"gx","label":"Meins","items":["tickets"]}]' WHERE id = $1::uuid`, admin)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET dashboard_widgets = '[{"id":"c1","type":"scan","size":1}]' WHERE id = $1::uuid`, own)
	must(err)
	resetDefaultsCache()

	post := func(what, action string, resetUsers bool) int {
		form := url.Values{"what": {what}, "action": {action}}
		if resetUsers {
			form.Set("reset_users", "on")
		}
		r := httptest.NewRequest(http.MethodPost, "/core/settings/defaults", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: admin, Role: "admin", FirstName: "Anna"}))
		w := httptest.NewRecorder()
		h.DefaultsSaveWeb(w, r)
		return w.Code
	}
	firstType := func(uid string) string { return h.userWidgets(ctx, uid)[0].Type }

	// ohne Recht: verboten
	r := httptest.NewRequest(http.MethodPost, "/core/settings/defaults", nil)
	r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: plain, Role: "worker"}))
	w := httptest.NewRecorder()
	h.DefaultsSaveWeb(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("ohne Recht: %d", w.Code)
	}

	// vorher: Vorgabe im Code
	if firstType(plain) != defaultWidgets()[0].Type || h.userNavLayout(ctx, plain) != nil {
		t.Fatal("vorher muss die Werksvorgabe gelten")
	}
	// Dashboard und Menü der Admin-Person als Standard
	if c := post("dashboard", "set", false); c != http.StatusSeeOther {
		t.Fatalf("Dashboard setzen: %d", c)
	}
	if c := post("nav", "set", false); c != http.StatusSeeOther {
		t.Fatalf("Menü setzen: %d", c)
	}
	if firstType(plain) != "note" {
		t.Errorf("ohne eigene Auswahl muss der Standard gelten, ist %q", firstType(plain))
	}
	if firstType(own) != "scan" {
		t.Errorf("eigene Auswahl muss bleiben, ist %q", firstType(own))
	}
	if l := h.userNavLayout(ctx, plain); len(l) != 2 || l[1].Label != "Meins" {
		t.Errorf("Menü-Standard: %+v", l)
	}
	var adminOwn *string
	must(pool.QueryRow(ctx, `SELECT dashboard_widgets::text FROM users WHERE id = $1::uuid`, admin).Scan(&adminOwn))
	if adminOwn != nil || firstType(admin) != "note" {
		t.Error("die Admin-Person folgt danach selbst dem Standard")
	}
	// auch eigene Einstellungen aller ersetzen
	if c := post("dashboard", "set", true); c != http.StatusSeeOther {
		t.Fatalf("Dashboard für alle: %d", c)
	}
	if firstType(own) != "note" {
		t.Errorf("eigene Auswahl hätte ersetzt werden müssen, ist %q", firstType(own))
	}
	// Werksvorgabe
	if c := post("dashboard", "reset", false); c != http.StatusSeeOther {
		t.Fatalf("Werksvorgabe: %d", c)
	}
	if firstType(plain) != defaultWidgets()[0].Type {
		t.Errorf("Werksvorgabe gilt nicht wieder: %q", firstType(plain))
	}
	if c := post("nav", "reset", false); c != http.StatusSeeOther || h.userNavLayout(ctx, plain) != nil {
		t.Errorf("Menü-Werksvorgabe: %d", c)
	}
}
