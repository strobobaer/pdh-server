package tiamqtt

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/database"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	if err := database.RunMigrations(ctx, pool, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestTIAMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Recht und Kategorie.
	var cat string
	if err := pool.QueryRow(ctx, `SELECT category FROM permissions WHERE key = 'import.tia'`).Scan(&cat); err != nil {
		t.Fatalf("Recht import.tia: %v", err)
	}
	if cat != "Daten & Schnittstellen" {
		t.Errorf("Kategorie = %q", cat)
	}

	// Spalte heartbeat_seconds: 0 scheitert, 60 und NULL gehen.
	var infra, conn string
	must(t, pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('TIA-Migrationstest', 'device') RETURNING id`).Scan(&infra))
	must(t, pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name) VALUES ('import', 'mqtt', 'TIA-Migrationstest') RETURNING id`).Scan(&conn))
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1`, conn)
		pool.Exec(context.Background(), `DELETE FROM infrastructure WHERE id = $1`, infra)
	})
	ins := `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, heartbeat_seconds)
		VALUES ($1, $2, $3, $4, $5)`
	if _, err := pool.Exec(ctx, ins, conn, "t/hb0", infra, "hb0", 0); err == nil {
		t.Error("heartbeat_seconds = 0 muss scheitern")
	}
	if _, err := pool.Exec(ctx, ins, conn, "t/hb86401", infra, "hb86401", 86401); err == nil {
		t.Error("heartbeat_seconds = 86401 muss scheitern")
	}
	if _, err := pool.Exec(ctx, ins, conn, "t/hb60", infra, "hb60", 60); err != nil {
		t.Errorf("heartbeat_seconds = 60: %v", err)
	}
	if _, err := pool.Exec(ctx, ins, conn, "t/hbnull", infra, "hbnull", nil); err != nil {
		t.Errorf("heartbeat_seconds = NULL: %v", err)
	}
}

// Die Rechte-Abschnitt der Migration übernimmt die Abteilung von import.read.
func TestTIAMigrationPermissionScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../../migrations/162_tia_mqtt.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	from := strings.Index(sql, "INSERT INTO role_permissions (role_id, permission_id, department_id)")
	if from < 0 {
		t.Fatal("Rechte-Abschnitt der Migration nicht gefunden")
	}
	section := sql[from:]

	var dept, roleA, roleB string
	must(t, pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('TIA-Abteilung D') RETURNING id`).Scan(&dept))
	must(t, pool.QueryRow(ctx, `INSERT INTO roles (key, label) VALUES ('tia_scope_a', 'TIA A') RETURNING id`).Scan(&roleA))
	must(t, pool.QueryRow(ctx, `INSERT INTO roles (key, label) VALUES ('tia_scope_b', 'TIA B') RETURNING id`).Scan(&roleB))
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM roles WHERE id IN ($1, $2)`, roleA, roleB)
		pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1`, dept)
	})
	grant := func(role string, d any) {
		t.Helper()
		_, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
			SELECT $1, id, $2 FROM permissions WHERE key = 'import.read'`, role, d)
		must(t, err)
	}
	grant(roleA, nil)  // überall
	grant(roleB, dept) // nur in Abteilung D

	if _, err := pool.Exec(ctx, section); err != nil {
		t.Fatal(err)
	}
	scope := func(role string) (found bool, dep *string) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT rp.department_id::text FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			WHERE rp.role_id = $1 AND p.key = 'import.tia'`, role).Scan(&dep)
		return err == nil, dep
	}
	if found, dep := scope(roleA); !found || dep != nil {
		t.Errorf("Rolle A: import.tia soll überall gelten (gefunden %v, Abteilung %v)", found, dep)
	}
	if found, dep := scope(roleB); !found || dep == nil || *dep != dept {
		t.Errorf("Rolle B: import.tia soll nur in Abteilung D gelten (gefunden %v, Abteilung %v)", found, dep)
	}
}
