//go:build integration

// Löschen gesperrter Terminal-Geräte und Umzug bei erneuter Kopplung gegen eine echte PostgreSQL.
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestTerminalDeviceDeleteWeb(t *testing.T) {
	e := newATEnv(t)
	admin := e.user("dl_a", "admin", false)
	e.grant(admin, "system.manage_users", "", true)
	plain := e.user("dl_p", "viewer", false)
	e.grant(plain, "system.manage_users", "", false)
	term := e.user("dl_t", "viewer", true)
	other := e.user("dl_o", "viewer", true)
	person := e.user("dl_x", "viewer", false)
	e.warm()

	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Post("/users/{id}/devices/{dev}/delete", h.TerminalDeviceDeleteWeb)

	mk := func(uid, name string, revoked bool) string {
		s, err := newDeviceSecret()
		e.must(err)
		id := e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256, revoked_at) VALUES ($1::uuid, $2, $3, CASE WHEN $4 THEN NOW() END) RETURNING id::text`, uid, name, hashDeviceSecret(s), revoked)
		t.Cleanup(func() {
			_, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_pdh_sessions WHERE device_id = $1::uuid`, id)
			_, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_devices WHERE id = $1::uuid`, id)
		})
		return id
	}
	exists := func(id string) bool {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid`, id).Scan(&n))
		return n == 1
	}
	post := func(uid, devID, actor, role string) *httptest.ResponseRecorder {
		req := withUser(httptest.NewRequest("POST", "/users/"+uid+"/devices/"+devID+"/delete", nil), &users.User{ID: actor, Role: users.Role(role)})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	sessions := func(devID string) int {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM terminal_pdh_sessions WHERE device_id = $1::uuid`, devID).Scan(&n))
		return n
	}

	revokedDev := mk(term, "Pi alt", true)
	activeDev := mk(term, "Pi aktiv", false)
	foreign := mk(other, "Pi fremd", true)
	for _, d := range []string{revokedDev, activeDev} {
		e.exec(`INSERT INTO terminal_pdh_sessions (terminal_user_id, person_id, device_id, ticket_sha256, ticket_expires_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, NOW() + interval '1 minute')`, term, person, d, "t"+d)
	}

	if rec := post(term, revokedDev, plain, "viewer"); rec.Code != http.StatusForbidden || !exists(revokedDev) {
		t.Errorf("ohne Recht: %d, vorhanden=%v", rec.Code, exists(revokedDev))
	}
	if rec := post(term, activeDev, admin, "admin"); rec.Code != http.StatusConflict || !exists(activeDev) || sessions(activeDev) != 1 {
		t.Errorf("aktives Gerät: %d, vorhanden=%v", rec.Code, exists(activeDev))
	}
	if rec := post(term, foreign, admin, "admin"); rec.Code != http.StatusNotFound || !exists(foreign) {
		t.Errorf("Gerät eines anderen Benutzers: %d", rec.Code)
	}
	rec := post(term, revokedDev, admin, "admin")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "Ger%C3%A4t+gel%C3%B6scht") {
		t.Errorf("Löschen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if exists(revokedDev) || sessions(revokedDev) != 0 {
		t.Error("Gerät oder Sitzungen noch vorhanden")
	}
	if !exists(activeDev) || sessions(activeDev) != 1 {
		t.Error("Löschen berührte ein anderes Gerät")
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND action='terminal_deleted' AND old_value='Pi alt' AND created_by=$2::uuid`, term, admin).Scan(&n))
	if n != 1 {
		t.Errorf("Verlaufseinträge: %d", n)
	}
}

func TestTerminalPairMove(t *testing.T) {
	terminalFailLimit = newFailLimiter(50, 5*time.Minute)
	e := newATEnv(t)
	oldU := e.user("um_alt", "viewer", true)
	newU := e.user("um_neu", "viewer", true)
	person := e.user("um_p", "viewer", false)
	h := &Handler{db: e.pool}

	mk := func(uid, name string, revoked bool) (id, secret string) {
		secret, err := newDeviceSecret()
		e.must(err)
		id = e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256, revoked_at) VALUES ($1::uuid, $2, $3, CASE WHEN $4 THEN NOW() END) RETURNING id::text`, uid, name, hashDeviceSecret(secret), revoked)
		t.Cleanup(func() {
			_, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_pdh_sessions WHERE device_id = $1::uuid`, id)
			_, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_devices WHERE id = $1::uuid`, id)
		})
		return id, secret
	}
	count := func(q string, args ...any) int {
		var n int
		e.must(e.pool.QueryRow(e.ctx, q, args...).Scan(&n))
		return n
	}
	pair := func(user, ip, previous string) (map[string]any, int) {
		body := `{"code":"` + e.pairCode(user) + `","name":"Pi Halle"` + previous + `}`
		rec := pairReq(h, ip, body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m, rec.Code
	}
	prev := func(id, secret string) string {
		return `,"previous":{"device_id":"` + id + `","secret":"` + secret + `"}`
	}
	t.Cleanup(func() {
		for _, u := range []string{oldU, newU} {
			_, _ = e.pool.Exec(e.ctx, `DELETE FROM terminal_devices WHERE user_id = $1::uuid`, u)
		}
	})

	// Fremde Geräte, die nie angefasst werden dürfen
	bystander, _ := mk(oldU, "Zuschauer", false)
	victim, victimSecret := mk(oldU, "Opfer", false)
	e.exec(`INSERT INTO terminal_pdh_sessions (terminal_user_id, person_id, device_id, ticket_sha256, ticket_expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'vic', NOW() + interval '1 minute')`, oldU, person, victim)

	// falsches Geheimnis, unbekannte Kennung, falsch geformte Angaben: normal koppeln, nichts berühren
	cases := []string{
		prev(victim, "pdht_falsch"),
		prev("00000000-0000-4000-8000-000000000001", victimSecret),
		prev("kein-uuid", victimSecret),
		`,"previous":"text"`,
		`,"previous":null`,
		`,"previous":{}`,
		`,"previous":{"device_id":5,"secret":[]}`,
		`,"previous":{"device_id":"` + victim + `","secret":""}`,
	}
	for i, c := range cases {
		m, code := pair(newU, "198.51.110."+strconv.Itoa(i+1), c)
		if code != 200 || m["moved"] != nil || m["device_id"] == "" || m["secret"] == "" {
			t.Errorf("Fall %d (%s): %d %v", i, c, code, m)
		}
	}
	if count(`SELECT COUNT(*) FROM terminal_devices WHERE id IN ($1::uuid,$2::uuid)`, victim, bystander) != 2 ||
		count(`SELECT COUNT(*) FROM terminal_pdh_sessions WHERE device_id = $1::uuid`, victim) != 1 {
		t.Error("ein falscher previous hat Zeilen berührt")
	}
	if n := count(`SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND action IN ('terminal_moved','terminal_taken_over')`, oldU); n != 0 {
		t.Errorf("Verlauf trotz Nichtübereinstimmung: %d", n)
	}

	// passend, Gerät aktiv, anderer Benutzer
	m, code := pair(newU, "198.51.111.1", prev(victim, victimSecret))
	if code != 200 || m["moved"] != true {
		t.Fatalf("Umzug: %d %v", code, m)
	}
	newID := m["device_id"].(string)
	if count(`SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid`, victim) != 0 ||
		count(`SELECT COUNT(*) FROM terminal_pdh_sessions WHERE device_id = $1::uuid`, victim) != 0 {
		t.Error("alte Zeile oder Sitzungen noch da")
	}
	if count(`SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL`, newID, newU) != 1 {
		t.Error("neue Zeile fehlt")
	}
	if count(`SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid`, bystander) != 1 {
		t.Error("anderes Gerät des alten Benutzers berührt")
	}
	if count(`SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND action='terminal_moved' AND message='Terminal Opfer umgezogen zu T U'`, oldU) != 1 ||
		count(`SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND action='terminal_taken_over' AND message='Terminal Opfer übernommen von T U'`, newU) != 1 {
		t.Error("Verlaufseinträge fehlen")
	}

	// gesperrtes altes Gerät zieht ebenfalls um; dasselbe Geheimnis passt danach nicht mehr
	rev, revSecret := mk(oldU, "Gesperrt", true)
	m, code = pair(newU, "198.51.112.1", prev(rev, revSecret))
	if code != 200 || m["moved"] != true || count(`SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid`, rev) != 0 {
		t.Errorf("gesperrt: %d %v", code, m)
	}
	m, code = pair(newU, "198.51.112.2", prev(rev, revSecret))
	if code != 200 || m["moved"] != nil {
		t.Errorf("zweiter Umzug: %d %v", code, m)
	}

	// derselbe Benutzer ersetzt die Zeile
	same, sameSecret := mk(newU, "Gleich", false)
	m, code = pair(newU, "198.51.113.1", prev(same, sameSecret))
	if code != 200 || m["moved"] != true || count(`SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid`, same) != 0 {
		t.Errorf("gleicher Benutzer: %d %v", code, m)
	}

	// ungültiger Kopplungscode bleibt ein Fehler und lässt das alte Gerät stehen
	other, otherSecret := mk(oldU, "Bleibt", false)
	rec := pairReq(h, "198.51.114.1", `{"code":"AAAA-AAAA","name":"Pi"`+prev(other, otherSecret)+`}`)
	if rec.Code != 422 || count(`SELECT COUNT(*) FROM terminal_devices WHERE id = $1::uuid`, other) != 1 {
		t.Errorf("ungültiger Code: %d", rec.Code)
	}
}
