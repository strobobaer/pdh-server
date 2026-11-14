package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDataNetMigration(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()

	var n int
	for _, tb := range []string{"datanet_clients", "datanet_events"} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, tb).Scan(&n); err != nil || n != 1 {
			t.Fatalf("Tabelle %s fehlt: %d, %v", tb, n, err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key IN ('datanet.view','datanet.edit','datanet.admin') AND category = 'Datennetz'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("Rechte in Kategorie Datennetz: %d, %v", n, err)
	}

	// Jede Rolle mit system.server_config (ohne Abteilung) oder sync.admin hat view und edit.
	for _, key := range []string{"datanet.view", "datanet.edit"} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM roles r
			WHERE (EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
					WHERE rp.role_id = r.id AND rp.department_id IS NULL AND o.key = 'system.server_config')
				OR EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
					WHERE rp.role_id = r.id AND o.key = 'sync.admin'))
			AND NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions p ON p.id = x.permission_id
					WHERE x.role_id = r.id AND p.key = $1)`, key).Scan(&n); err != nil || n != 0 {
			t.Errorf("%d Rollen mit Server-Verwaltung ohne %s: %v", n, key, err)
		}
	}
	// Rollen mit sync.admin haben datanet.admin; die Rolle admin ebenfalls.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM roles r
		WHERE EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id
				WHERE rp.role_id = r.id AND o.key = 'sync.admin')
		AND NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions p ON p.id = x.permission_id
				WHERE x.role_id = r.id AND p.key = 'datanet.admin')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d Rollen mit sync.admin ohne datanet.admin: %v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		JOIN roles r ON r.id = rp.role_id WHERE r.key = 'admin' AND p.key = 'datanet.admin'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("datanet.admin für admin: %d, %v", n, err)
	}
	// Keine datanet.*-Zuordnung hat eine Abteilung.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE p.key LIKE 'datanet.%' AND rp.department_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("datanet.* mit Abteilung: %d, %v", n, err)
	}
}

// Eine Rolle, die nur vpn.use hat (eigene VPN-Geräte nutzen), bekommt aus der
// Migration kein datanet.*. Die Rechte-Abschnitte werden auf einer solchen
// Rolle erneut ausgeführt (alle Anweisungen sind wiederholbar).
func TestDataNetMigrationVpnUseGetsNothing(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../migrations/160_datanet.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	from := strings.Index(sql, "INSERT INTO role_permissions")
	if from < 0 {
		t.Fatal("Rechte-Abschnitt der Migration nicht gefunden")
	}

	key := "dn_vpn_" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var role string
	if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'DN VPN', 40) RETURNING id`, key).Scan(&role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, role) })
	if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT $1, id FROM permissions WHERE key = 'vpn.use'`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, sql[from:]); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1 AND p.key LIKE 'datanet.%'`, role).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Rolle nur mit vpn.use hat %d datanet-Rechte: %v", n, err)
	}

	// Gegenprobe: mit system.server_config (ohne Abteilung) bekommt sie view und edit, aber kein admin.
	if _, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT $1, id FROM permissions WHERE key = 'system.server_config'`, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, sql[from:]); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1 AND p.key IN ('datanet.view','datanet.edit')`, role).Scan(&n); err != nil || n != 2 {
		t.Errorf("view/edit mit server_config: %d, %v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1 AND p.key = 'datanet.admin'`, role).Scan(&n); err != nil || n != 0 {
		t.Errorf("datanet.admin ohne anderes .admin: %d, %v", n, err)
	}
}
