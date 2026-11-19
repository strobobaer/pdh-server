package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildsSort(t *testing.T) {
	e := newHistEnv(t)
	anna, bert := e.user("Anna", "Alpha"), e.user("Bert", "Beta")
	infra := e.infra("ESP-Anlage", "", "")
	conn := e.id(`INSERT INTO import_export_connections (direction, kind, name, config, enabled) VALUES ('import', 'mqtt', $1, '{}', true) RETURNING id::text`, "bs-"+e.sfx)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM import_export_connections WHERE id = $1::uuid`, conn) })
	dev := e.id(`INSERT INTO esppdh_devices (slug, name, infrastructure_id, connection_id, board_key, mqtt_password_enc)
		VALUES ($1, 'Bau-Sortierung', $2::uuid, $3::uuid, 'esp32dev', 'x') RETURNING id::text`, "bs-"+e.sfx, infra, conn)
	e.t.Cleanup(func() { _, _ = e.pool.Exec(e.ctx, `DELETE FROM esppdh_devices WHERE id = $1::uuid`, dev) })
	// Art (Status) als Text: canceled (r1) < failed (r3) < ok (r2)
	for i, b := range []struct{ ver, status, by string }{{"r1", "canceled", anna.ID}, {"r2", "ok", bert.ID}, {"r3", "failed", anna.ID}} {
		e.exec(`INSERT INTO esppdh_builds (device_id, status, config_hash, esphome_version, requested_by, created_at)
			VALUES ($1::uuid, $2, 'h', $3, $4::uuid, $5)`, dev, b.status, b.ver, b.by, logSeedAt(i))
	}
	tmpl := loadTestTemplates(t)
	for _, c := range []struct{ mode, want string }{{"new", "r3|r2|r1"}, {"old", "r1|r2|r3"}, {"person", "r3|r1|r2"}, {"kind", "r1|r3|r2"}} {
		req := httptest.NewRequest("GET", "/import/esppdh/"+dev+"?ls_builds="+c.mode, nil)
		mode := e.h.sortMode(req, "builds")
		list, err := e.h.esppdhRuns(e.ctx, dev, esppdhListRuns, mode)
		if err != nil {
			t.Fatal(err)
		}
		var vers []string
		for _, b := range list {
			vers = append(vers, b.Version)
		}
		if got := strings.Join(vers, "|"); got != c.want {
			t.Errorf("%s: Reihenfolge %q, erwartet %q", c.mode, got, c.want)
		}
		d := ESPPDHDevicePageData{Runs: list, RunList: list, LatestRunID: list[0].ID, RunBar: e.h.sortBar(req, "builds", mode, req.URL.Path, "")}
		d.Dev.ID = dev
		out := renderPage(t, tmpl, "esppdh_device", d)
		logBarOK(t, out, "builds", c.mode)
		if got := logOrder(strings.ReplaceAll(out, "ESPHome ", "")); !strings.HasPrefix(got, c.want) && !strings.Contains(got, c.want) {
			t.Errorf("%s: Liste im HTML %q, erwartet %q", c.mode, got, c.want)
		}
	}
}
