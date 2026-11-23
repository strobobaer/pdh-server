//go:build integration

// Anlagenliste für den Offline-Speicher der PDH-App gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestAppAssets ./internal/web/
package web

import (
	"context"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newAppAssetsEnv(t *testing.T) *appWorkEnv {
	e := newAppWorkEnv(t)
	r := chi.NewRouter()
	r.With(e.h.AppAuth).Get("/api/app/v1/assets", e.h.AppAssetsAPI)
	e.router = r
	return e
}

// assets: Anlagen dieses Tests (Kennung = Namensendung) als „Pfad“ in Antwortreihenfolge.
func (e *appWorkEnv) assets() (int, map[string]any, []string) {
	e.t.Helper()
	rec := e.call("GET", "/api/app/v1/assets", nil, e.key, e.token, "1.0.0")
	m := appJSON(e.t, rec)
	var paths []string
	items, _ := m["items"].([]any)
	for _, it := range items {
		x := it.(map[string]any)
		if name, _ := x["name"].(string); strings.HasSuffix(name, e.sfx) {
			p, _ := x["path"].(string)
			paths = append(paths, strings.ReplaceAll(p, " "+e.sfx, ""))
			if id, _ := x["id"].(string); id == "" {
				e.t.Errorf("ohne id: %v", x)
			}
		}
	}
	return rec.Code, m, paths
}

func TestAppAssets(t *testing.T) {
	t.Run("im Bereich, ohne ausgeblendete samt Unterbaum, mit Pfad, sortiert", func(t *testing.T) {
		e := newAppAssetsEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		root := e.atEnv.infra("Halle ", "", "")
		e.atEnv.infra("Presse ", "", root)
		hidden := e.atEnv.infra("Weg ", "", root)
		e.exec(`UPDATE infrastructure SET hidden_at = now() WHERE id = $1::uuid`, hidden)
		e.atEnv.infra("Kind ", "", hidden) // unter einer ausgeblendeten Anlage: fehlt mit ihr
		hiddenRoot := e.atEnv.infra("Alt ", "", "")
		e.exec(`UPDATE infrastructure SET hidden_at = now() WHERE id = $1::uuid`, hiddenRoot)
		e.atEnv.infra("AltKind ", "", hiddenRoot)
		status, m, paths := e.assets()
		if status != 200 || m["truncated"] != false {
			t.Fatalf("%d %v", status, m["truncated"])
		}
		// e.infra (Presse, aus newAppWorkEnv) ist Wurzel; Halle und Unteranlage dazu
		want := []string{"Halle", "Halle › Presse", "Presse"}
		if strings.Join(paths, "|") != strings.Join(want, "|") {
			t.Errorf("pfade %q, erwartet %q", paths, want)
		}
	})
	t.Run("Anlage in fremder Abteilung fehlt", func(t *testing.T) {
		e := newAppAssetsEnv(t)
		deptA, deptB := e.dept("DA"), e.dept("DB")
		e.atEnv.infra("Eigen ", deptA, "")
		fremd := e.atEnv.infra("Fremd ", deptB, "")
		e.atEnv.infra("FremdKind ", "", fremd) // erbt Abteilung B
		role := "assetdept" + e.sfx
		e.exec(`INSERT INTO roles (key, label, level, department_id) VALUES ($1, 'AD', 10, $2::uuid)`, role, deptA)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
		e.exec(`UPDATE users SET role = $2 WHERE id = $1::uuid`, e.uid, role)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		appDeviceCache.forget(e.devID)
		_, _, paths := e.assets()
		want := []string{"Eigen", "Presse"} // Presse: ohne Abteilung, für alle
		if strings.Join(paths, "|") != strings.Join(want, "|") {
			t.Errorf("pfade %q, erwartet %q", paths, want)
		}
	})
	t.Run("Gerät ohne may_create oder Benutzer ohne app.create: leere Liste", func(t *testing.T) {
		e := newAppAssetsEnv(t)
		status, m, paths := e.assets() // Benutzer ohne app.create
		if items, _ := m["items"].([]any); status != 200 || items == nil || len(items) != 0 || len(paths) != 0 || m["truncated"] != false {
			t.Fatalf("ohne app.create: %d %v", status, m)
		}
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		e.exec(`UPDATE app_devices SET may_create = false WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		if status, m, _ = e.assets(); status != 200 || len(m["items"].([]any)) != 0 {
			t.Errorf("Gerät ohne may_create: %d %v", status, m)
		}
	})
	t.Run("truncated bei Grenze", func(t *testing.T) {
		e := newAppAssetsEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		e.atEnv.infra("A ", "", "")
		e.atEnv.infra("B ", "", "")
		old := appAssetsMax
		appAssetsMax = 2
		t.Cleanup(func() { appAssetsMax = old })
		_, m, _ := e.assets()
		if n := len(m["items"].([]any)); n != 2 || m["truncated"] != true {
			t.Errorf("items %d truncated %v", n, m["truncated"])
		}
	})
	t.Run("ohne Anmeldung 401", func(t *testing.T) {
		e := newAppAssetsEnv(t)
		rec := e.call("GET", "/api/app/v1/assets", nil, e.key, "", "1.0.0")
		if rec.Code != 401 {
			t.Errorf("%d", rec.Code)
		}
	})
}
