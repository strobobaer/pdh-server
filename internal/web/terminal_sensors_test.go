//go:build integration

// Sensoren am Anlagenterminal (0.82.11): nur nach Kartenanmeldung einer Person mit esppdh.values
// für die Anlage (Abteilung zählt); Geräte und Heartbeat-Zuordnungen der Anlage samt Unteranlagen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalSensors ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

type sensorsEnv struct {
	*atEnv
	h               *Handler
	term, root, sub string
	devRoot, devSub string
	deptID          string
}

func newSensorsEnv(t *testing.T) *sensorsEnv {
	e := &sensorsEnv{atEnv: newATEnv(t)}
	e.h = newCardHandler(e.atEnv)
	dept := e.dept("SA")
	e.root = e.infra("S-Anlage ", dept, "")
	e.sub = e.infra("S-Unter ", dept, e.root)
	e.term = e.user("st", "viewer", true)
	e.exec(`UPDATE users SET terminal_infrastructure_id = $2::uuid WHERE id = $1::uuid`, e.term, e.root)
	conn := e.id(`INSERT INTO import_export_connections (direction, kind, name, config, enabled) VALUES ('import', 'mqtt', $1, '{}', true) RETURNING id::text`, "S-Conn "+e.sfx)
	dev := func(slug, infra string) string {
		return e.id(`INSERT INTO esppdh_devices (slug, name, infrastructure_id, connection_id, board_key, mqtt_password_enc)
			VALUES ($1, $1, $2::uuid, $3::uuid, 'esp32dev', 'x') RETURNING id::text`, slug, infra, conn)
	}
	e.devRoot, e.devSub = dev("s-root-"+e.sfx, e.root), dev("s-sub-"+e.sfx, e.sub)
	e.exec(`INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, heartbeat_seconds, last_value, last_received_at)
		VALUES ($1::uuid, 'sps/x/heartbeat#count', $2::uuid, $3, 60, '42', now())`, conn, e.sub, "SPS-HB "+e.sfx)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM import_mappings WHERE connection_id = $1::uuid`, conn)
		_, _ = e.pool.Exec(bg, `DELETE FROM esppdh_devices WHERE connection_id = $1::uuid`, conn)
		_, _ = e.pool.Exec(bg, `DELETE FROM terminal_card_sessions WHERE terminal_user_id = $1::uuid`, e.term)
		_, _ = e.pool.Exec(bg, `DELETE FROM import_export_connections WHERE id = $1::uuid`, conn)
	})
	e.deptID = dept
	return e
}

// sensors: GET /terminal/sensors.json als Terminal-Benutzer.
func (e *sensorsEnv) sensors() (int, map[string]any, string) {
	e.t.Helper()
	r := chi.NewRouter()
	e.h.terminalRoutes(r)
	req := withUser(httptest.NewRequest("GET", "/terminal/sensors.json", nil), &users.User{ID: e.term, Role: users.Role("viewer"), IsSystemUser: true})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestTerminalSensorsNoCard(t *testing.T) {
	e := newSensorsEnv(t)
	code, out, body := e.sensors()
	if code != http.StatusOK || out["show"] != false || strings.Contains(body, "s-root-") {
		t.Errorf("ohne Kartenanmeldung: %d %s", code, body)
	}
}

func TestTerminalSensorsCardWithoutPerm(t *testing.T) {
	e := newSensorsEnv(t)
	p := e.user("sp0", "viewer", false)
	e.warm()
	e.must(e.h.cardSessionStart(e.ctx, e.term, p))
	if _, out, body := e.sensors(); out["show"] != false || strings.Contains(body, "s-root-") {
		t.Errorf("Person ohne esppdh.values: %s", body)
	}
}

func TestTerminalSensorsCardWithPerm(t *testing.T) {
	e := newSensorsEnv(t)
	p := e.user("sp1", "viewer", false)
	e.grant(p, permESPValues, e.deptID, true)
	e.warm()
	e.must(e.h.cardSessionStart(e.ctx, e.term, p))
	code, out, body := e.sensors()
	if code != http.StatusOK || out["show"] != true {
		t.Fatalf("Person mit esppdh.values: %d %s", code, body)
	}
	for _, want := range []string{"s-root-" + e.sfx, "s-sub-" + e.sfx, "SPS-HB " + e.sfx, `"42"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Antwort ohne %q: %s", want, body)
		}
	}
	if strings.Contains(body, "http://") {
		t.Error("Terminal-Antwort enthält einen Link zum Gerät")
	}
}

func TestTerminalSensorsDeptScope(t *testing.T) {
	e := newSensorsEnv(t)
	other := e.dept("SB")
	p := e.user("sp2", "viewer", false)
	e.grant(p, permESPValues, other, true)
	e.warm()
	e.must(e.h.cardSessionStart(e.ctx, e.term, p))
	if _, out, body := e.sensors(); out["show"] != false || strings.Contains(body, "s-root-") {
		t.Errorf("Recht nur in fremder Abteilung: %s", body)
	}
}
