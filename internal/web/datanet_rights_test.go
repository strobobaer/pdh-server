package web

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/datanet"
)

// dataNetTestRole legt eine Testrolle mit Rechten an; Wert nil = überall, sonst in einer Abteilung.
func dataNetTestRole(t *testing.T, pool *pgxpool.Pool, perms map[string]any) string {
	t.Helper()
	ctx := context.Background()
	var role string
	key := "dn_t_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'DN Test', 40) RETURNING id`, key).Scan(&role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, role)
		pool.Exec(context.Background(), `DELETE FROM departments WHERE name = $1`, key)
	})
	for p, dept := range perms {
		var d any
		if dept != nil {
			var id string
			if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE name = $1`, key).Scan(&id); err != nil {
				if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ($1) RETURNING id`, key).Scan(&id); err != nil {
					t.Fatal(err)
				}
			}
			d = id
		}
		if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
			SELECT $1, id, $3 FROM permissions WHERE key = $2`, role, p, d); err != nil {
			t.Fatal(err)
		}
	}
	return role
}

func dataNetGrants(t *testing.T, pool *pgxpool.Pool, role string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT p.key FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1 AND p.key LIKE 'datanet.%' ORDER BY p.key`, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

// dataNetRerun führt die ganze Migration erneut aus (alle Anweisungen sind wiederholbar).
func dataNetRerun(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/160_datanet.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(raw)); err != nil {
		t.Fatal(err)
	}
}

// sync.admin nur in einer Abteilung ergibt kein globales datanet.*.
func TestDataNetMigrationDepartmentSyncAdminGetsNothing(t *testing.T) {
	pool := dataNetTestPool(t)
	role := dataNetTestRole(t, pool, map[string]any{"sync.admin": "dept"})
	dataNetRerun(t, pool)
	if g := dataNetGrants(t, pool, role); g != "" {
		t.Fatalf("Rolle mit sync.admin nur in Abteilung hat %q", g)
	}
	role2 := dataNetTestRole(t, pool, map[string]any{"sync.admin": nil})
	dataNetRerun(t, pool)
	if g := dataNetGrants(t, pool, role2); g != "datanet.admin,datanet.edit,datanet.view" {
		t.Fatalf("globales sync.admin: %q", g)
	}
}

// datanet.admin: globales system.server_config plus ein anderes .admin.
func TestDataNetMigrationAdminViaServerConfig(t *testing.T) {
	pool := dataNetTestPool(t)
	both := dataNetTestRole(t, pool, map[string]any{"system.server_config": nil, "setup.admin": nil})
	deptCfg := dataNetTestRole(t, pool, map[string]any{"system.server_config": "dept", "setup.admin": nil})
	dataNetRerun(t, pool)
	if g := dataNetGrants(t, pool, both); g != "datanet.admin,datanet.edit,datanet.view" {
		t.Errorf("server_config + setup.admin: %q", g)
	}
	if g := dataNetGrants(t, pool, deptCfg); g != "" {
		t.Errorf("server_config nur in Abteilung: %q", g)
	}
}

// Zweimal ausführen: nichts doppelt, nichts eingeengt.
func TestDataNetMigrationIdempotent(t *testing.T) {
	pool := dataNetTestPool(t)
	count := func() (n int) {
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			WHERE p.key LIKE 'datanet.%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	dataNetRerun(t, pool)
	before := count()
	dataNetRerun(t, pool)
	if after := count(); after != before {
		t.Errorf("Zuordnungen vorher %d, nachher %d", before, after)
	}
	var perms int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM permissions WHERE key LIKE 'datanet.%'`).Scan(&perms)
	if perms != 3 {
		t.Errorf("Rechte: %d", perms)
	}
}

func TestDataNetSaveReportRevokedAndTruncation(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	name := "testreport-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM datanet_events WHERE client_name = $1`, name)
		pool.Exec(context.Background(), `DELETE FROM datanet_clients WHERE name = $1`, name)
	})
	c, _, err := h.dataNetCreate(ctx, name, "")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("ä", 200)
	rep := datanet.Report{Hostname: long, Version: long, OS: long}
	if err := h.dataNetSaveReport(ctx, c.ID, long, rep); err != nil {
		t.Fatal(err)
	}
	got, _ := h.dataNetGet(ctx, c.ID)
	if len([]rune(got.Addr)) != 64 || len([]rune(got.Hostname)) != 120 || len([]rune(got.Version)) != 40 || len([]rune(got.OS)) != 40 {
		t.Errorf("Kürzung: %d/%d/%d/%d", len([]rune(got.Addr)), len([]rune(got.Hostname)), len([]rune(got.Version)), len([]rune(got.OS)))
	}
	if err := h.dataNetRevoke(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.dataNetSaveReport(ctx, c.ID, "1.1.1.1", rep); !errors.Is(err, errDataNetRevoked) {
		t.Errorf("gesperrt: %v", err)
	}
	if err := h.dataNetSaveReport(ctx, "00000000-0000-0000-0000-000000000000", "", rep); !errors.Is(err, errDataNetRevoked) {
		t.Errorf("unbekannt: %v", err)
	}
}
