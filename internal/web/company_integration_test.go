//go:build integration

package web

// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Company ./internal/web/

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/pkg/database"
)

func TestCompanyProfileRoundTrip(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // nach dem Zurücksetzen (Cleanup läuft rückwärts)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var before companyProfile
	h := &Handler{db: pool}
	before = h.company(ctx)
	t.Cleanup(func() {
		if err := h.saveCompany(context.Background(), before, ""); err != nil {
			t.Errorf("zurücksetzen: %v", err)
		}
	})

	var uid string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	p := companyProfile{Name: "Muster GmbH", City: "Augsburg", Country: "Deutschland", IBAN: "DE89370400440532013000"}
	if err := h.saveCompany(ctx, p, uid); err != nil {
		t.Fatal(err)
	}
	got := h.company(ctx)
	if got.Name != "Muster GmbH" || got.City != "Augsburg" || got.IBAN != p.IBAN || got.UpdatedBy != uid || got.UpdatedAt.IsZero() {
		t.Errorf("gelesen: %+v", got)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key='company.edit'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Recht company.edit fehlt (%d, %v)", n, err)
	}
}

// Hat eine Rolle eines der Ausgangsrechte überall und ein anderes nur in einer Abteilung,
// bekommt sie company.edit überall (sonst wäre sie ausgesperrt).
func TestCompanyEditGrantPrefersGlobal(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var dept, role string
	if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Test Firmenrecht') RETURNING id::text`).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1::uuid`, dept) })
	if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ('test_firmenrecht', 'Test Firmenrecht', 1) RETURNING id::text`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM role_permissions WHERE role_id = $1::uuid`, role)
		_, _ = pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1::uuid`, role)
	})
	exec(`INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT $1::uuid, id, $2::uuid FROM permissions WHERE key = 'system.manage_roles'`, role, dept)
	exec(`INSERT INTO role_permissions (role_id, permission_id)
		SELECT $1::uuid, id FROM permissions WHERE key = 'm365.templates.manage'`, role)
	sql, err := os.ReadFile(companyMigrationFile(t))
	if err != nil {
		t.Fatal(err)
	}
	exec(string(sql)) // die Migration ist wiederholbar
	var global bool
	if err := pool.QueryRow(ctx, `SELECT rp.department_id IS NULL FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1::uuid AND p.key = 'company.edit'`, role).Scan(&global); err != nil || !global {
		t.Errorf("company.edit nicht überall vergeben (global=%v, %v)", global, err)
	}
}

func companyMigrationFile(t *testing.T) string {
	t.Helper()
	m, _ := filepath.Glob("../../migrations/*_company_profile.up.sql")
	if len(m) != 1 {
		t.Fatalf("Migration company_profile: %v", m)
	}
	return m[0]
}
