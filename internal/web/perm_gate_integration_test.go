package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Rechte auf Abteilungsebene und Bereichsadministrator (perm_gate.go, rbac/scope.go).
func TestDepartmentPermissionsIntegration(t *testing.T) {
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
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	werk := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Werk "+sfx)
	halle := id(`INSERT INTO departments (name, parent_id) VALUES ($1, $2::uuid) RETURNING id::text`, "Halle "+sfx, werk)
	buero := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Büro "+sfx)
	infraH := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "Presse "+sfx, halle)
	infraB := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "Drucker "+sfx, buero)
	role := "meister" + sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'Meister', 40)`, role)
	must(err)
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'M', 'S', $2, '', '') RETURNING id::text`, "m"+sfx, role)
	fH := id(`INSERT INTO faults (title, created_by, infrastructure_id) VALUES ('Halle', $1::uuid, $2::uuid) RETURNING id::text`, user, infraH)
	fB := id(`INSERT INTO faults (title, created_by, infrastructure_id) VALUES ('Büro', $1::uuid, $2::uuid) RETURNING id::text`, user, infraB)
	fN := id(`INSERT INTO faults (title, created_by) VALUES ('ohne Anlage', $1::uuid) RETURNING id::text`, user)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM faults WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, fH, fB, fN)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, user)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE key = $1`, role)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, infraH, infraB)
		_, _ = pool.Exec(ctx, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, halle, werk, buero)
	}()
	// Störungen bearbeiten nur im Werk (samt Halle), Bereichsadmin Wartung nirgends
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT r.id, p.id, $2::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key = 'faults.edit'`, role, werk)
	must(err)
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	h := &Handler{db: pool, rbac: rb}
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: user, Role: users.Role(role)}))
	if !h.hasPermRec(req, "faults.edit", "fault", fH) {
		t.Error("Störung in der Halle (unter dem Werk) darf bearbeitet werden")
	}
	if h.hasPermRec(req, "faults.edit", "fault", fB) {
		t.Error("Störung im Büro darf nicht bearbeitet werden")
	}
	if !h.hasPermRec(req, "faults.edit", "fault", fN) {
		t.Error("Störung ohne Anlage gilt für alle mit dem Recht")
	}
	if h.isAreaAdmin(req, "fault", fH) {
		t.Error("ohne faults.admin kein Bereichsadministrator")
	}
	// Bereichsadmin Störungen nur für das Büro
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT r.id, p.id, $2::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key = 'faults.admin'`, role, buero)
	must(err)
	must(rb.RefreshCache(ctx))
	if !h.isAreaAdmin(req, "fault", fB) || h.isAreaAdmin(req, "fault", fH) {
		t.Error("Bereichsadministrator gilt nur im Büro")
	}
	if !h.hasPermRec(req, "faults.resolve", "fault", fB) {
		t.Error("Bereichsadministrator hat alle Rechte des Bereichs (faults.resolve im Büro)")
	}
	if h.hasPermRec(req, "faults.resolve", "fault", fH) {
		t.Error("faults.resolve in der Halle hat die Rolle nicht")
	}
}
