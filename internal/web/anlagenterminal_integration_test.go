//go:build integration

// Anlagenterminal gegen eine echte PostgreSQL (Migration 156).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Anlagenterminal ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/pkg/database"
)

func TestAnlagenterminalHMIFields(t *testing.T) {
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
	must(database.RunMigrations(ctx, pool, "../../migrations"))

	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	uid := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Anna', 'Muster', 'admin', '', '') RETURNING id::text`, "at"+sfx)
	admin := &users.User{ID: uid, Role: users.Role("admin"), FirstName: "Anna", LastName: "Muster"}
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'plant') RETURNING id::text`, "Presse "+sfx)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, uid)
	}()
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))

	h := &Handler{db: pool, rbac: rb, jwtSecret: "test-geheim"}
	r := chi.NewRouter()
	h.hmiRoutes(r)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), "user", admin))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	list := func() hmiView {
		t.Helper()
		rec := do("GET", "/infrastructure/"+infra+"/hmis", "")
		var out struct {
			HMIs []hmiView `json:"hmis"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &out))
		if len(out.HMIs) != 1 {
			t.Fatalf("Liste: %s", rec.Body.String())
		}
		return out.HMIs[0]
	}

	// neues HMI ohne Terminal-Felder (alter Client) → Standardwerte
	rec := do("POST", "/infrastructure/"+infra+"/hmis", `{"name":"Panel","host":"10.0.0.5","allow_control":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("anlegen: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	must(json.Unmarshal(rec.Body.Bytes(), &created))
	v := list()
	if v.TerminalView != "everyone" || v.TerminalControl != "card" || v.TerminalControlEff != "card" {
		t.Errorf("Standard: %+v", v)
	}

	// Bedienen: jeder bei allow_control=false → gespeichert, wirksam never
	upd := "/infrastructure/" + infra + "/hmis/" + created.ID
	rec = do("POST", upd, `{"name":"Panel","host":"10.0.0.5","allow_control":false,"terminal_view":"card","terminal_control":"everyone"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("speichern: %d %s", rec.Code, rec.Body.String())
	}
	v = list()
	if v.TerminalView != "card" || v.TerminalControl != "everyone" || v.TerminalControlEff != "never" {
		t.Errorf("gespeichert: %+v", v)
	}

	// ungültige Werte → 422, Zeile unverändert
	for _, body := range []string{
		`{"name":"Neu","host":"10.0.0.9","allow_control":true,"terminal_view":"alle"}`,
		`{"name":"Neu","host":"10.0.0.9","allow_control":true,"terminal_control":"Card"}`,
	} {
		rec = do("POST", upd, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("ungültig: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do("POST", "/infrastructure/"+infra+"/hmis", `{"name":"Zwei","host":"10.0.0.9","terminal_view":"x"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Ansehen am Terminal ungültig") {
		t.Errorf("ungültig anlegen: %d %s", rec.Code, rec.Body.String())
	}
	v = list()
	if v.Name != "Panel" || v.TerminalView != "card" || v.TerminalControl != "everyone" || v.AllowControl {
		t.Errorf("Zeile verändert: %+v", v)
	}
}
