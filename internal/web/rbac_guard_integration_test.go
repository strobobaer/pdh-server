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

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rechte nur weitergeben, die man selbst hat (rbac_guard.go).
func TestGrantOnlyOwnPermissionsIntegration(t *testing.T) {
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
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	boss, low := "chef"+sfx, "tief"+sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'Chef', 80), ($2, 'Tief', 20)`, boss, low)
	must(err)
	var uid, lowID, target string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'C', 'T', $2, '', '') RETURNING id::text`, "c"+sfx, boss).Scan(&uid))
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'T', 'T', $2, '', '') RETURNING id::text`, "t"+sfx, low).Scan(&target))
	must(pool.QueryRow(ctx, `SELECT id::text FROM roles WHERE key = $1`, low).Scan(&lowID))
	// Chef darf Rollen verwalten und hat shifts.view – aber nicht system.backup
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r, permissions p WHERE r.key = $1 AND p.key IN ('system.manage_roles', 'shifts.view')`, boss)
	must(err)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1::uuid, $2::uuid)`, uid, target)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE key IN ($1, $2)`, boss, low)
	}()
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	us := users.NewService(users.NewRepository(pool), "test-secret", 1)
	h := &Handler{db: pool, rbac: rb, users: us}
	permID := func(key string) string {
		var id string
		must(pool.QueryRow(ctx, `SELECT id::text FROM permissions WHERE key = $1`, key).Scan(&id))
		return id
	}
	actor := &users.User{ID: uid, Role: users.Role(boss)}
	call := func(handler http.HandlerFunc, path string, form url.Values, params map[string]string) int {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rc := chi.NewRouteContext()
		for k, v := range params {
			rc.URLParams.Add(k, v)
		}
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), chi.RouteCtxKey, rc), "user", actor))
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Code
	}
	matrix := func(key string, grant bool) int {
		return call(h.RoleMatrixWeb, "/admin/roles/matrix-web", url.Values{"role_id": {lowID}, "permission_id": {permID(key)}, "granted": {map[bool]string{true: "true", false: "false"}[grant]}}, nil)
	}
	if c := matrix("system.backup", true); c != http.StatusForbidden {
		t.Errorf("fremdes Recht über die Matrix vergeben: %d", c)
	}
	if c := matrix("shifts.view", true); c != http.StatusOK {
		t.Errorf("eigenes Recht über die Matrix vergeben: %d", c)
	}
	if c := matrix("shifts.view", false); c != http.StatusOK {
		t.Errorf("Recht wegnehmen: %d", c)
	}
	single := func(key, mode string) int {
		return call(h.UserPermissionSetWeb, "/users/x/permissions", url.Values{"permission_id": {permID(key)}, "mode": {mode}}, map[string]string{"id": target})
	}
	if c := single("system.backup", "allow"); c != http.StatusForbidden {
		t.Errorf("fremdes Einzelrecht vergeben: %d", c)
	}
	if c := single("system.backup", "deny"); c != http.StatusOK {
		t.Errorf("Einzelrecht verbieten: %d", c)
	}
	if c := single("shifts.view", "allow"); c != http.StatusOK {
		t.Errorf("eigenes Einzelrecht vergeben: %d", c)
	}
	if c := call(h.RoleMatrixWeb, "/admin/roles/matrix-web", url.Values{"role_id": {lowID}, "permission_id": {"00000000-0000-0000-0000-000000000000"}, "granted": {"true"}}, nil); c != http.StatusBadRequest {
		t.Errorf("unbekanntes Recht: %d", c)
	}
}
