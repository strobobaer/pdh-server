//go:build integration

// PDH-App Paket 1: Migration 189 (Rechte, Tabellen, Systembenutzer, Gruppe).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestAppMigration ./internal/web/
package web

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAppMigration(t *testing.T) {
	e := newATEnv(t)

	for _, k := range []string{permAppUse, permAppCreate, permAppCockpit, permAppSensors, permAppAdmin} {
		var cat string
		if err := e.pool.QueryRow(e.ctx, `SELECT category FROM permissions WHERE key = $1`, k).Scan(&cat); err != nil {
			t.Fatalf("Recht %s fehlt: %v", k, err)
		}
		if cat != "App" {
			t.Errorf("Recht %s: Kategorie %q, erwartet App", k, cat)
		}
	}

	has := func(role, perm string) bool {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM role_permissions rp
			JOIN roles r ON r.id = rp.role_id JOIN permissions p ON p.id = rp.permission_id
			WHERE r.key = $1 AND p.key = $2`, role, perm).Scan(&n))
		return n > 0
	}
	// nur die eingebauten Rollen: andere Tests legen nach der Migration eigene Rollen an
	roles := []string{"admin", "manager", "technician", "worker", "viewer"}
	for _, r := range roles {
		if !has(r, permAppUse) {
			t.Errorf("Rolle %s hat app.use nicht", r)
		}
	}
	if has("viewer", permAppAdmin) {
		t.Error("viewer darf app.admin nicht haben")
	}
	for _, c := range []struct{ role, perm string }{
		{"manager", permAppAdmin}, {"manager", permAppCreate},
		{"admin", permAppAdmin}, {"admin", permAppCreate}, {"admin", permAppCockpit}, {"admin", permAppSensors},
	} {
		if !has(c.role, c.perm) {
			t.Errorf("Rolle %s hat %s nicht", c.role, c.perm)
		}
	}
	if has("manager", permAppCockpit) || has("manager", permAppSensors) {
		t.Error("manager darf Cockpit/Sensoren nicht automatisch haben")
	}

	var active, bot bool
	var role string
	e.must(e.pool.QueryRow(e.ctx, `SELECT active, is_bot, role FROM users WHERE id = $1::uuid`, appSystemUserID).Scan(&active, &bot, &role))
	if active || !bot || role != "worker" {
		t.Errorf("Systembenutzer: active=%v bot=%v role=%s", active, bot, role)
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM user_groups WHERE id = $1::uuid`, appAdminGroupID).Scan(&n))
	if n != 1 {
		t.Error("Gruppe App-Administratoren fehlt")
	}

	fp := "fp-" + e.sfx
	ins := `INSERT INTO app_requests (name, platform, app_version, public_key, key_fingerprint)
		VALUES ('Test', 'ios', '1.0', 'k', $1) RETURNING id::text`
	id := e.id(ins, fp)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM app_requests WHERE key_fingerprint = $1`, fp) })
	if len(id) != 36 || id[14] != '7' {
		t.Errorf("id %q ist keine UUID Version 7", id)
	}
	_, err := e.pool.Exec(e.ctx, `INSERT INTO app_requests (name, platform, app_version, public_key, key_fingerprint)
		VALUES ('Zweite', 'android', '1.0', 'k', $1)`, fp)
	pg, ok := err.(*pgconn.PgError)
	if !ok || pg.Code != "23505" {
		t.Errorf("zweite offene Anfrage: erwartet Unique-Verletzung, bekam %v", err)
	}
	// nach Entscheidung ist derselbe Schlüssel wieder frei
	e.exec(`UPDATE app_requests SET status = 'rejected' WHERE id = $1::uuid`, id)
	id2 := e.id(ins, fp)
	if !strings.HasPrefix(id2[14:15], "7") {
		t.Errorf("id2 %q keine UUIDv7", id2)
	}
	// Spalte am Ticket
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='tickets' AND column_name='app_request_id'`).Scan(&n))
	if n != 1 {
		t.Error("tickets.app_request_id fehlt")
	}
}
