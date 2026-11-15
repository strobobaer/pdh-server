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

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/internal/modules/energie"
	"pdh/pkg/database"
)

// Schwellwerte mit echtem RBAC: Ein Benutzer mit energy.thresholds und
// energy.admin nur in Abteilung A darf an Messstellen von Anlagen in A
// speichern und löschen, an Messstellen in Abteilung B nicht (403).
func TestThresholdDepartmentScope(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("Labor-Datenbank nicht erreichbar: ", err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(database.RunMigrations(ctx, pool, "../../migrations"))
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	deptA := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Scope A "+sfx)
	deptB := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Scope B "+sfx)
	infraA := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "Scope-Anlage A "+sfx, deptA)
	infraB := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "Scope-Anlage B "+sfx, deptB)
	meterA := id(`INSERT INTO energy_meters (meter_key, name, infrastructure_id, kind, unit) VALUES ($1, 'Zähler A', $2::uuid, 'instant', 'bar') RETURNING id::text`, "SA-"+sfx, infraA)
	meterB := id(`INSERT INTO energy_meters (meter_key, name, infrastructure_id, kind, unit) VALUES ($1, 'Zähler B', $2::uuid, 'instant', 'bar') RETURNING id::text`, "SB-"+sfx, infraB)
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Anna', 'Scope', 'viewer', '', '') RETURNING id::text`, "scope"+sfx)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM energy_meters WHERE id IN ($1::uuid, $2::uuid)`, meterA, meterB) // Schwellwerte folgen per Cascade
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, user)                                // user_permissions folgen per Cascade
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, infraA, infraB)
		_, _ = pool.Exec(ctx, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, deptA, deptB)
	}()
	for _, perm := range []string{permEnergyThresholds, permEnergyAdmin} {
		_, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
			SELECT $1::uuid, p.id, true, $2::uuid FROM permissions p WHERE p.key = $3`, user, deptA, perm)
		must(err)
	}
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	// Rolle „viewer“ ohne Energie-Rechte: nur die Einzelrechte des Benutzers zählen
	h := &Handler{db: pool, rbac: rb, tmpl: loadTestTemplates(t)}
	if _, err := h.tmpl.ParseFiles("../../web/templates/widgets/energy_thresholds.gohtml"); err != nil {
		t.Fatal(err)
	}

	do := func(fn http.HandlerFunc, path string, params map[string]string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rc := chi.NewRouteContext()
		for k, v := range params {
			rc.URLParams.Add(k, v)
		}
		c := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		c = context.WithValue(c, "user", &users.User{ID: user, FirstName: "Anna", LastName: "Scope", Role: "viewer"})
		w := httptest.NewRecorder()
		fn(w, req.WithContext(c))
		return w
	}
	form := func() url.Values {
		return url.Values{"name": {"zu hoch"}, "condition": {"above"}, "limit": {"8"}, "window_minutes": {"5"},
			"reset_minutes": {"15"}, "priority": {"high"}, "active": {"1"}}
	}
	save := func(mid string) *httptest.ResponseRecorder {
		return do(h.EnergyThresholdSaveWeb, "/energy/meters/"+mid+"/thresholds/save-web", map[string]string{"id": mid}, form())
	}
	del := func(tid string) *httptest.ResponseRecorder {
		return do(h.EnergyThresholdDeleteWeb, "/energy/thresholds/"+tid+"/delete-web", map[string]string{"tid": tid}, url.Values{})
	}

	// Abteilung B: Speichern abgelehnt, nichts angelegt
	st := energie.NewStore(pool)
	if w := save(meterB); w.Code != http.StatusForbidden {
		t.Fatalf("Speichern in B: %d, erwartet 403", w.Code)
	}
	if list, _ := st.Thresholds(ctx, meterB); len(list) != 0 {
		t.Fatalf("in B darf nichts angelegt werden: %+v", list)
	}
	// Abteilung A: Speichern gelingt
	if w := save(meterA); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Schwellwert gespeichert.") {
		t.Fatalf("Speichern in A: %d %s", w.Code, w.Body.String())
	}
	listA, err := st.Thresholds(ctx, meterA)
	must(err)
	if len(listA) != 1 {
		t.Fatalf("in A angelegt: %+v", listA)
	}
	// ein Schwellwert in B (direkt angelegt): Löschen abgelehnt, bleibt bestehen
	thrB := &energie.Threshold{MeterID: meterB, Name: "B", Condition: energie.CondAbove, Limit: 8, WindowMinutes: 5, ResetMinutes: 15, Priority: "high", Active: true}
	must(st.SaveThreshold(ctx, thrB))
	if w := del(thrB.ID); w.Code != http.StatusForbidden {
		t.Fatalf("Löschen in B: %d, erwartet 403", w.Code)
	}
	if _, err := st.Threshold(ctx, thrB.ID); err != nil {
		t.Fatalf("Schwellwert in B muss bleiben: %v", err)
	}
	// Abteilung A: Löschen gelingt
	if w := del(listA[0].ID); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Schwellwert gelöscht.") {
		t.Fatalf("Löschen in A: %d %s", w.Code, w.Body.String())
	}
	if _, err := st.Threshold(ctx, listA[0].ID); err == nil {
		t.Error("Schwellwert in A sollte gelöscht sein")
	}
}
