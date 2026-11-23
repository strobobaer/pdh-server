//go:build integration

// Scannen und Störung melden in der PDH-App gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestAppScan|TestAppReportFault' ./internal/web/
package web

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/it"
)

const appScanBase = "https://pdh.scan.test"

func newAppScanEnv(t *testing.T) *appWorkEnv {
	e := newAppActEnv(t)
	e.h.mailCfg.PublicURL = appScanBase
	r := chi.NewRouter()
	r.With(e.h.AppAuth).Get("/api/app/v1/scan", e.h.AppScanAPI)
	r.With(e.h.AppAuth).Get("/api/app/v1/work/{type}/{id}", e.h.AppWorkDetailAPI)
	r.With(e.h.AppAuth, e.h.AppWrite).Post("/api/app/v1/faults", e.h.AppFaultCreateAPI)
	e.router = r
	return e
}

func (e *appWorkEnv) scan(code string) (int, map[string]any) {
	e.t.Helper()
	rec := e.call("GET", "/api/app/v1/scan?code="+url.QueryEscape(code), nil, e.key, e.token, "1.0.0")
	return rec.Code, appJSON(e.t, rec)
}

// scanTitles: Kurznamen der Vorgänge dieses Tests in der Antwort.
func (e *appWorkEnv) scanTitles(m map[string]any) []string {
	var out []string
	items, _ := m["items"].([]any)
	for _, it := range items {
		title, _ := it.(map[string]any)["title"].(string)
		if strings.HasSuffix(title, " "+e.sfx) {
			out = append(out, strings.TrimSuffix(title, " "+e.sfx))
		}
	}
	return out
}

func TestAppScan(t *testing.T) {
	t.Run("Anlage über /a/, /e/ und nackte UUID mit offener Störung", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.seed()
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		for _, code := range []string{appScanBase + "/a/" + e.infra, appScanBase + "/e/" + e.infra, e.infra, strings.ToUpper(e.infra), "/a/" + e.infra} {
			status, m := e.scan(code)
			if status != 200 || m["kind"] != "asset" {
				t.Fatalf("%s: %d %v", code, status, m)
			}
			a := m["asset"].(map[string]any)
			if a["id"] != e.infra || a["name"] != e.infraName || a["path"] == "" || a["path"] == nil {
				t.Errorf("%s: anlage %v", code, a)
			}
			if got := strings.Join(e.scanTitles(m), ","); got != "Wartung,Stoerung" {
				t.Errorf("%s: items %q", code, got)
			}
			if m["may_report"] != true {
				t.Errorf("%s: may_report %v", code, m["may_report"])
			}
			for _, it := range m["items"].([]any) {
				x := it.(map[string]any)
				if x["type"] == nil || x["id"] == nil || x["asset"].(map[string]any)["id"] != e.infra || x["updated_at"] == nil {
					t.Errorf("%s: form %v", code, x)
				}
			}
		}
	})
	t.Run("Unteranlage zählt zur Anlage", func(t *testing.T) {
		e := newAppScanEnv(t)
		sub := e.atEnv.infra("Unter ", "", e.infra)
		e.exec(`INSERT INTO faults (title, description, severity, status, infrastructure_id, created_by) VALUES ($1, '', 'low', 'detected', $2::uuid, $3::uuid)`, e.title("UnterStoerung"), sub, e.other)
		_, m := e.scan(e.infra)
		if got := strings.Join(e.scanTitles(m), ","); got != "UnterStoerung" {
			t.Errorf("items %q", got)
		}
	})
	t.Run("ohne app.create kein may_report", func(t *testing.T) {
		e := newAppScanEnv(t)
		_, m := e.scan(e.infra)
		if m["may_report"] != false {
			t.Errorf("may_report %v", m["may_report"])
		}
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		e.exec(`UPDATE app_devices SET may_create = false WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		_, m = e.scan(e.infra)
		if m["may_report"] != false {
			t.Errorf("Gerät ohne may_create: %v", m["may_report"])
		}
	})
	t.Run("Lager und Kanban nur im Web", func(t *testing.T) {
		e := newAppScanEnv(t)
		for code, want := range map[string]string{
			appScanBase + "/inventory/x/booking":         appScanBase + "/inventory/x/booking",
			appScanBase + "/inventory/lookup?code=a%20b": appScanBase + "/inventory/lookup?code=a%20b",
			appScanBase + "/k/abc123":                    appScanBase + "/k/abc123",
			"/k/abc123":                                  appScanBase + "/k/abc123",
		} {
			status, m := e.scan(code)
			if status != 200 || m["kind"] != "web" || m["url"] != want {
				t.Errorf("%s: %d %v", code, status, m)
			}
		}
	})
	t.Run("unbekannt und fremder Host: 404", func(t *testing.T) {
		e := newAppScanEnv(t)
		for _, code := range []string{
			"", "irgendein text", "4006381333931",
			"00000000-0000-7000-8000-000000000000",
			appScanBase + "/a/00000000-0000-7000-8000-000000000000",
			"https://fremd.example/a/" + e.infra,
			"https://fremd.example/inventory/x/booking",
			appScanBase + ".fremd.example/a/" + e.infra,
			"ftp://pdh.scan.test/a/" + e.infra,
			appScanBase + "/etwas/anderes",
		} {
			status, m := e.scan(code)
			if status != 404 || m["code"] != "app_not_found" {
				t.Errorf("%q: %d %v", code, status, m)
			}
		}
	})
	t.Run("Gerät ohne fault zeigt keine Störungen", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.seed()
		e.exec(`UPDATE app_devices SET allowed_types = '{ticket,task,maintenance}' WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		_, m := e.scan(e.infra)
		if got := strings.Join(e.scanTitles(m), ","); got != "Wartung" {
			t.Errorf("items %q", got)
		}
	})
	t.Run("ohne maintenance.view keine Wartungen", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.seed()
		e.exec(`UPDATE user_permissions SET granted = false WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'maintenance.view')`, e.uid)
		e.warm()
		_, m := e.scan(e.infra)
		if got := strings.Join(e.scanTitles(m), ","); got != "Stoerung" {
			t.Errorf("items %q", got)
		}
	})
	t.Run("geschlossene Vorgänge fehlen", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.seed()
		e.exec(`UPDATE faults SET status = 'closed' WHERE title LIKE '%'||$1`, e.sfx)
		_, m := e.scan(e.infra)
		if got := strings.Join(e.scanTitles(m), ","); got != "Wartung" {
			t.Errorf("items %q", got)
		}
	})
	t.Run("Adressen: Standard-Port, Schema-relativ, Pfad mit ..", func(t *testing.T) {
		e := newAppScanEnv(t)
		for code, want := range map[string]string{
			"https://PDH.scan.test:443/inventory/x": appScanBase + "/inventory/x",
			"http://pdh.scan.test/k/abc":            appScanBase + "/k/abc", // Adresse des Servers gewinnt
			appScanBase + "/inventory//x/./y":       appScanBase + "/inventory/x/y",
			appScanBase + "/inventory/a%3Fb":        appScanBase + "/inventory/a%3Fb", // „?“ bleibt kodiert, kein Abfrageteil
		} {
			status, m := e.scan(code)
			if status != 200 || m["kind"] != "web" || m["url"] != want {
				t.Errorf("%s: %d %v", code, status, m)
			}
		}
		if status, m := e.scan("https://pdh.scan.test:443/a/" + e.infra); status != 200 || m["kind"] != "asset" {
			t.Errorf("anlage mit :443: %d %v", status, m)
		}
		for _, code := range []string{
			"//pdh.scan.test/a/" + e.infra, "//fremd.example/a/" + e.infra, "//pdh.scan.test/inventory/x",
			appScanBase + "/inventory/../a/" + e.infra, appScanBase + "/inventory/%2e%2e/x", "/inventory/../k/x",
			appScanBase + ":8443/a/" + e.infra,
			appScanBase + "/inventory/%252e%252e/x", appScanBase + `/inventory/a`, appScanBase + "/inventory/a%5Cb",
		} {
			if status, m := e.scan(code); status != 404 || m["code"] != "app_not_found" {
				t.Errorf("%q: %d %v", code, status, m)
			}
		}
	})
	t.Run("Anlage in fremder Abteilung: 403", func(t *testing.T) {
		e := newAppScanEnv(t)
		deptA, deptB := e.dept("DA"), e.dept("DB")
		infraB := e.atEnv.infra("Fremd ", deptB, "")
		role := "scandept" + e.sfx
		e.exec(`INSERT INTO roles (key, label, level, department_id) VALUES ($1, 'SD', 10, $2::uuid)`, role, deptA)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
		e.exec(`UPDATE users SET role = $2 WHERE id = $1::uuid`, e.uid, role)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		appDeviceCache.forget(e.devID)
		if status, m := e.scan(infraB); status != 403 || m["code"] != "app_no_permission" {
			t.Errorf("scan: %d %v", status, m)
		}
		if status, m := e.scan(e.infra); status != 200 { // Anlage ohne Abteilung
			t.Errorf("eigene anlage: %d %v", status, m)
		}
		rec, m := e.post("/api/app/v1/faults", map[string]any{"asset_id": infraB, "title": "Fremd " + e.sfx}, "")
		if rec.Code != 403 || m["code"] != "app_no_permission" {
			t.Errorf("melden: %d %v", rec.Code, m)
		}
	})
	t.Run("IT-Asset über /e/: Vorgänge ja, may_report nein", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.h.it = it.NewService(it.NewRepository(e.pool))
		e.grant(e.uid, permAppCreate, "", true)
		e.grant(e.uid, "it.view", "", true)
		e.warm()
		id := e.id(`INSERT INTO it_assets (name, type, status, created_by) VALUES ($1, 'server', 'active', $2::uuid) RETURNING id::text`, "ITA "+e.sfx, e.uid)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM it_assets WHERE id = $1::uuid`, id) })
		e.exec(`INSERT INTO faults (title, description, severity, status, it_asset_id, created_by) VALUES ($1, '', 'low', 'detected', $2::uuid, $3::uuid)`, e.title("ITStoerung"), id, e.other)
		status, m := e.scan(appScanBase + "/e/" + id)
		if status != 200 || m["kind"] != "asset" || m["may_report"] != false {
			t.Fatalf("%d %v", status, m)
		}
		if got := strings.Join(e.scanTitles(m), ","); got != "ITStoerung" {
			t.Errorf("items %q", got)
		}
	})
	t.Run("ohne Anmeldung 401", func(t *testing.T) {
		e := newAppScanEnv(t)
		rec := e.call("GET", "/api/app/v1/scan?code="+e.infra, nil, e.key, "", "1.0.0")
		if rec.Code != 401 {
			t.Errorf("%d", rec.Code)
		}
	})
}

func TestAppReportFault(t *testing.T) {
	report := func(e *appWorkEnv, once string, in map[string]any) (int, map[string]any) {
		rec, m := e.post("/api/app/v1/faults", in, once)
		return rec.Code, m
	}
	t.Run("mit may_create und app.create: Störung mittel", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "  Lager heult " + e.sfx + "  ", "text": " Seit heute früh "})
		if status != 201 || m["type"] != "fault" || m["id"] == nil || m["id"] == "" {
			t.Fatalf("%d %v", status, m)
		}
		id := m["id"].(string)
		var title, desc, sev, infra, by string
		var stopped bool
		e.must(e.pool.QueryRow(e.ctx, `SELECT title, description, severity::text, infrastructure_id::text, created_by::text, plant_stopped FROM faults WHERE id = $1::uuid`, id).
			Scan(&title, &desc, &sev, &infra, &by, &stopped))
		if title != "Lager heult "+e.sfx || desc != "Seit heute früh" || sev != "medium" || infra != e.infra || by != e.uid || stopped {
			t.Errorf("störung: %q %q %q %q %q %v", title, desc, sev, infra, by, stopped)
		}
		if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND action = 'create'`, id); n != 1 {
			t.Errorf("historie create: %d", n)
		}
		// ohne Text erlaubt; Titel genau 200 Zeichen erlaubt
		if status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": strings.Repeat("x", 190) + e.sfx[:10]}); status != 201 {
			t.Errorf("langer titel: %d %v", status, m)
		}
	})
	t.Run("Anlage steht: hoch, als stehend markiert, Historie", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "Steht " + e.sfx, "text": "", "plant_stopped": true})
		if status != 201 {
			t.Fatalf("%d %v", status, m)
		}
		id := m["id"].(string)
		var sev string
		var stopped bool
		e.must(e.pool.QueryRow(e.ctx, `SELECT severity::text, plant_stopped FROM faults WHERE id = $1::uuid`, id).Scan(&sev, &stopped))
		if sev != "high" || !stopped {
			t.Errorf("schwere %q, steht %v", sev, stopped)
		}
		if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND field_name = 'plant_stopped'`, id); n != 1 {
			t.Errorf("historie anlage steht: %d", n)
		}
	})
	t.Run("ohne Recht 403, nichts angelegt", func(t *testing.T) {
		e := newAppScanEnv(t)
		in := map[string]any{"asset_id": e.infra, "title": "Ohne " + e.sfx}
		status, m := report(e, "", in)
		if status != 403 || m["code"] != "app_no_permission" {
			t.Fatalf("ohne app.create: %d %v", status, m)
		}
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		e.exec(`UPDATE app_devices SET may_create = false WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		if status, m := report(e, "", in); status != 403 || m["code"] != "app_no_permission" {
			t.Fatalf("Gerät ohne may_create: %d %v", status, m)
		}
		if n := e.count(`SELECT COUNT(*) FROM faults WHERE title LIKE '%'||$1`, e.sfx); n != 0 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("Eingaben prüfen", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		bad := []map[string]any{
			{"asset_id": e.infra, "title": ""},
			{"asset_id": e.infra, "title": "   "},
			{"asset_id": e.infra, "title": strings.Repeat("x", 201)},
			{"asset_id": e.infra, "title": "T " + e.sfx, "text": strings.Repeat("y", 4001)},
			{"title": "T " + e.sfx},
			{"asset_id": "kein-uuid", "title": "T " + e.sfx},
		}
		for i, in := range bad {
			if status, m := report(e, "", in); status != 422 || m["code"] != "app_invalid_input" {
				t.Errorf("%d: %d %v", i, status, m)
			}
		}
		// Anlage gibt es nicht
		if status, m := report(e, "", map[string]any{"asset_id": "00000000-0000-7000-8000-000000000000", "title": "T " + e.sfx}); status != 404 || m["code"] != "app_not_found" {
			t.Errorf("unbekannte anlage: %d %v", status, m)
		}
		// ohne Once
		rec := e.call("POST", "/api/app/v1/faults", []byte(`{}`), e.key, e.token, "1.0.0")
		if rec.Code != 422 {
			t.Errorf("ohne once: %d", rec.Code)
		}
		if n := e.count(`SELECT COUNT(*) FROM faults WHERE title LIKE '%'||$1`, e.sfx); n != 0 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("doppelte Once: eine Störung, zweite Antwort 200 mit derselben ID", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		once := e.once()
		in := map[string]any{"asset_id": e.infra, "title": "Doppelt " + e.sfx, "plant_stopped": true}
		status, first := report(e, once, in)
		if status != 201 {
			t.Fatalf("%d %v", status, first)
		}
		status, again := report(e, once, in)
		if status != 200 || again["id"] != first["id"] || again["type"] != "fault" {
			t.Fatalf("wiederholung: %d %v (erste %v)", status, again, first)
		}
		if n := e.count(`SELECT COUNT(*) FROM faults WHERE title LIKE '%'||$1`, e.sfx); n != 1 {
			t.Errorf("störungen: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND action = 'create'`, first["id"]); n != 1 {
			t.Errorf("historie create: %d", n)
		}
		// gleiche Meldung mit neuer Once = neue Störung; die erste Once zeigt weiter auf die erste
		status, second := report(e, "", in)
		if status != 201 || second["id"] == first["id"] {
			t.Fatalf("zweite meldung: %d %v", status, second)
		}
		if status, again := report(e, once, in); status != 200 || again["id"] != first["id"] {
			t.Errorf("erste once nach zweiter meldung: %d %v", status, again)
		}
	})
	t.Run("Wiederholung ordnet die richtige Störung zu, auch wenn die Anlage inzwischen fehlt", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		in := map[string]any{"asset_id": e.infra, "title": "Gleich " + e.sfx}
		onceA, onceB := e.once(), e.once()
		_, a := report(e, onceA, in)
		_, b := report(e, onceB, in)
		if a["id"] == nil || b["id"] == nil || a["id"] == b["id"] {
			t.Fatalf("a %v b %v", a, b)
		}
		if status, again := report(e, onceB, in); status != 200 || again["id"] != b["id"] {
			t.Errorf("wiederholung B: %d %v (erwartet %v)", status, again, b["id"])
		}
		e.exec(`UPDATE infrastructure SET hidden_at = now() WHERE id = $1::uuid`, e.infra)
		if status, again := report(e, onceA, in); status != 200 || again["id"] != a["id"] {
			t.Errorf("wiederholung A bei verborgener Anlage: %d %v (erwartet %v)", status, again, a["id"])
		}
	})
	t.Run("Spiegelserver und Wartung lehnen ab", func(t *testing.T) {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		e.h.mirror = MirrorInfo{On: true}
		status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "Spiegel " + e.sfx})
		if status != 409 || m["code"] != "app_read_only" {
			t.Errorf("spiegel: %d %v", status, m)
		}
	})
}

// TestAppReportFaultOffline: Kennung vom Telefon und Zusammenführen an eine offene Störung
// (Spec 9.3.5).
func TestAppReportFaultOffline(t *testing.T) {
	report := func(e *appWorkEnv, once string, in map[string]any) (int, map[string]any) {
		rec, m := e.post("/api/app/v1/faults", in, once)
		return rec.Code, m
	}
	env := func(t *testing.T) *appWorkEnv {
		e := newAppScanEnv(t)
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		t.Cleanup(func() {
			_, _ = e.pool.Exec(context.Background(), `DELETE FROM app_id_aliases WHERE created_by = $1::uuid`, e.uid)
		})
		return e
	}
	// openFault: Störung an der Anlage, dem Benutzer zugewiesen (sichtbar, Maßnahme erlaubt).
	openFault := func(e *appWorkEnv, k, status string) string {
		return e.id(`INSERT INTO faults (title, description, severity, status, infrastructure_id, assigned_to, created_by)
			VALUES ($1, 'Alt', 'low', $2, $3::uuid, $4::uuid, $5::uuid) RETURNING id::text`, e.title(k), status, e.infra, e.uid, e.other)
	}
	faultCount := func(e *appWorkEnv) int {
		return e.count(`SELECT COUNT(*) FROM faults WHERE title LIKE '%'||$1`, e.sfx)
	}

	t.Run("mit Kennung: Störung hat genau diese Kennung, Wiederholung nach 3 Tagen", func(t *testing.T) {
		e := env(t)
		id, once := e.once(), e.once()
		in := map[string]any{"asset_id": e.infra, "title": "Mit Id " + e.sfx, "text": "x", "id": id}
		status, m := report(e, once, in)
		if status != 201 || m["id"] != id || m["type"] != "fault" || m["merged"] != nil {
			t.Fatalf("%d %v", status, m)
		}
		e.exec(`UPDATE app_steps SET created_at = now() - interval '3 days' WHERE id = $1::uuid`, once)
		status, again := report(e, once, in)
		if status != 200 || again["id"] != id || again["merged"] != nil {
			t.Fatalf("wiederholung: %d %v", status, again)
		}
		if n := faultCount(e); n != 1 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("Kennung ungültig: 422", func(t *testing.T) {
		e := env(t)
		for _, id := range []string{"kein-uuid", "00000000-0000-4000-8000-000000000001", "00000000-0000-7000-c000-000000000001"} {
			status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "Bad " + e.sfx, "id": id})
			if status != 422 || m["code"] != "app_invalid_input" || m["error"] != "Kennung ist ungültig." {
				t.Errorf("%s: %d %v", id, status, m)
			}
		}
		if n := faultCount(e); n != 0 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("Kennung einer vorhandenen Störung oder eines Alias: 409 ohne Retry-After", func(t *testing.T) {
		e := env(t)
		fid := openFault(e, "Fremd", "detected")
		rec, m := e.post("/api/app/v1/faults", map[string]any{"asset_id": e.infra, "title": "Neu " + e.sfx, "id": fid}, "")
		if rec.Code != 409 || m["code"] != "app_conflict" || rec.Header().Get("Retry-After") != "" {
			t.Fatalf("störung: %d %v", rec.Code, m)
		}
		alias := e.once()
		e.exec(`INSERT INTO app_id_aliases (id, target_type, target_id, created_by) VALUES ($1::uuid, 'fault', $2::uuid, $3::uuid)`, alias, fid, e.uid)
		rec, m = e.post("/api/app/v1/faults", map[string]any{"asset_id": e.infra, "title": "Neu " + e.sfx, "id": alias}, "")
		if rec.Code != 409 || m["code"] != "app_conflict" {
			t.Fatalf("alias: %d %v", rec.Code, m)
		}
		if n := faultCount(e); n != 1 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("offline bei offener Störung: angehängt, Alias, Replay wieder merged", func(t *testing.T) {
		e := env(t)
		fid := openFault(e, "Offen", "in_progress")
		id, once := e.once(), e.once()
		in := map[string]any{"asset_id": e.infra, "title": "Meldung " + e.sfx, "text": "Lager heult", "id": id, "offline": true, "plant_stopped": true}
		status, m := report(e, once, in)
		hint := "An der Anlage ist bereits die Störung „" + e.title("Offen") + "“ offen. Deine Meldung wurde dort angehängt."
		if status != 200 || m["id"] != fid || m["type"] != "fault" || m["merged"] != true || m["hint"] != hint {
			t.Fatalf("%d %v", status, m)
		}
		if n := faultCount(e); n != 1 {
			t.Errorf("störungen: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid AND description LIKE '%Lager heult%'`, fid); n != 1 {
			t.Errorf("maßnahme: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM app_id_aliases WHERE id = $1::uuid AND target_type = 'fault' AND target_id = $2::uuid AND created_by = $3::uuid`, id, fid, e.uid); n != 1 {
			t.Errorf("alias: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND message = $2`, fid, hint); n != 1 {
			t.Errorf("historie hinweis: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM faults WHERE id = $1::uuid AND plant_stopped`, fid); n != 1 {
			t.Errorf("anlage steht: %d", n)
		}
		status, again := report(e, once, in)
		if status != 200 || again["id"] != fid || again["merged"] != true || again["hint"] != hint {
			t.Fatalf("replay: %d %v", status, again)
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 1 {
			t.Errorf("maßnahmen nach replay: %d", n)
		}
		// Replay auch nach Tagen
		e.exec(`UPDATE app_steps SET created_at = now() - interval '5 days' WHERE id = $1::uuid`, once)
		if status, again := report(e, once, in); status != 200 || again["id"] != fid || again["merged"] != true {
			t.Fatalf("replay nach tagen: %d %v", status, again)
		}
		// Die Kennung gehört nun zum Alias: mit neuer Once ist sie vergeben.
		if status, _ := report(e, "", in); status != 409 {
			t.Errorf("alias vergeben: %d", status)
		}
	})
	t.Run("offline bei geschlossener Störung: neue Störung mit der Telefon-Kennung", func(t *testing.T) {
		e := env(t)
		openFault(e, "Zu", "closed")
		id := e.once()
		status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "Neu " + e.sfx, "id": id, "offline": true})
		if status != 201 || m["id"] != id || m["merged"] != nil {
			t.Fatalf("%d %v", status, m)
		}
		if n := faultCount(e); n != 2 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("offline false bei offener Störung: neue Störung", func(t *testing.T) {
		e := env(t)
		openFault(e, "Offen", "detected")
		id := e.once()
		status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "Neu " + e.sfx, "id": id, "offline": false})
		if status != 201 || m["id"] != id {
			t.Fatalf("%d %v", status, m)
		}
		if n := faultCount(e); n != 2 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("offline bei nicht sichtbarer offener Störung: neue Störung", func(t *testing.T) {
		e := env(t)
		fid := e.id(`INSERT INTO faults (title, description, severity, status, infrastructure_id, created_by) VALUES ($1, '', 'low', 'detected', $2::uuid, $3::uuid) RETURNING id::text`,
			e.title("Unsichtbar"), e.infra, e.other)
		e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'faults.view')`, e.uid)
		e.grant(e.uid, "faults.view", "", false)
		e.warm()
		appDeviceCache.forget(e.devID)
		if code, _ := e.detail("fault", fid); code != 403 {
			t.Skipf("Störung ist trotz Entzug sichtbar (%d): Test nicht aussagekräftig", code)
		}
		id := e.once()
		status, m := report(e, "", map[string]any{"asset_id": e.infra, "title": "Neu " + e.sfx, "id": id, "offline": true})
		if status != 201 || m["id"] != id {
			t.Fatalf("%d %v", status, m)
		}
	})
	// fremdFault: offene Störung eines anderen, nicht zugewiesen – sichtbar (faults.view),
	// aber ohne Recht, Maßnahmen einzutragen.
	fremdFault := func(e *appWorkEnv, k string) string {
		return e.id(`INSERT INTO faults (title, description, severity, status, infrastructure_id, created_by)
			VALUES ($1, 'Alt', 'low', 'detected', $2::uuid, $3::uuid) RETURNING id::text`, e.title(k), e.infra, e.other)
	}
	t.Run("Zusammenführen ohne Maßnahmerecht: Meldung und Hinweis im Verlauf, Fehler → 503 ohne Alias", func(t *testing.T) {
		e := env(t)
		fid := fremdFault(e, "Fremd")
		e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'faults.edit')`, e.uid)
		e.grant(e.uid, "faults.edit", "", false) // Rolle „worker“ hat es sonst
		e.warm()
		appDeviceCache.forget(e.devID)
		if code, d := e.detail("fault", fid); code != 200 || d["can"].(map[string]any)["action"] != false {
			t.Fatalf("Vorbedingung (sichtbar, keine Maßnahme): %d %v", code, d["can"])
		}
		id, once := e.once(), e.once()
		in := map[string]any{"asset_id": e.infra, "title": "Meldung " + e.sfx, "text": "Lager heult", "id": id, "offline": true}
		orig := appMergeHistoryWrite
		t.Cleanup(func() { appMergeHistoryWrite = orig })
		appMergeHistoryWrite = func(context.Context, *Handler, string, string, string, []string) error {
			return errors.New("db weg")
		}
		status, m := report(e, once, in)
		if status != 503 || m["code"] != "app_unavailable" {
			t.Fatalf("fehlerfall: %d %v", status, m)
		}
		if n := e.count(`SELECT COUNT(*) FROM app_id_aliases WHERE id = $1::uuid`, id); n != 0 {
			t.Errorf("alias nach fehler: %d", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM app_steps WHERE id = $1::uuid`, once); n != 0 {
			t.Errorf("once nach fehler behalten: %d", n)
		}
		appMergeHistoryWrite = orig
		status, m = report(e, once, in) // dieselbe Once: der Schritt kommt erneut aus der Warteschlange
		hint := "An der Anlage ist bereits die Störung „" + e.title("Fremd") + "“ offen. Deine Meldung wurde dort angehängt."
		if status != 200 || m["id"] != fid || m["merged"] != true || m["hint"] != hint {
			t.Fatalf("%d %v", status, m)
		}
		if n := e.count(`SELECT COUNT(*) FROM fault_actions WHERE fault_id = $1::uuid`, fid); n != 0 {
			t.Errorf("maßnahme ohne recht: %d", n)
		}
		msg := "Meldung aus der App: Meldung " + e.sfx + " – Lager heult"
		for _, want := range []string{msg, hint} {
			if n := e.count(`SELECT COUNT(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND message = $2`, fid, want); n != 1 {
				t.Errorf("verlauf %q: %d", want, n)
			}
		}
		if n := faultCount(e); n != 1 {
			t.Errorf("störungen: %d", n)
		}
	})
	t.Run("Wiederholung ohne Ergebnis: fremder Alias zählt nicht", func(t *testing.T) {
		e := env(t)
		fid := openFault(e, "Fremd", "detected")
		id, once := e.once(), e.once()
		e.exec(`INSERT INTO app_steps (id, user_id, device_id, path) VALUES ($1::uuid, $2::uuid, $3::uuid, '/api/app/v1/faults')`, once, e.uid, e.devID)
		e.exec(`INSERT INTO app_id_aliases (id, target_type, target_id, created_by) VALUES ($1::uuid, 'fault', $2::uuid, $3::uuid)`, id, fid, e.other)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM app_id_aliases WHERE id = $1::uuid`, id) })
		status, m := report(e, once, map[string]any{"asset_id": e.infra, "title": "Meldung " + e.sfx, "id": id, "offline": true})
		if status != 409 || m["code"] != "app_conflict" {
			t.Fatalf("fremder alias: %d %v", status, m)
		}
		e.exec(`UPDATE app_id_aliases SET created_by = $2::uuid WHERE id = $1::uuid`, id, e.uid)
		status, m = report(e, once, map[string]any{"asset_id": e.infra, "title": "Meldung " + e.sfx, "id": id, "offline": true})
		if status != 200 || m["id"] != fid || m["merged"] != true {
			t.Fatalf("eigener alias: %d %v", status, m)
		}
	})
}
