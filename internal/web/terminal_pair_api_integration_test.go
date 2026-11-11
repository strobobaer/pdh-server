//go:build integration

// Kopplungs-API und Gerätezustand gegen eine echte PostgreSQL (Migration 157).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TerminalPairAPI|TerminalDeviceAPI' ./internal/web/
package web

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pairReq(h *Handler, ip, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/terminal/v1/pair", strings.NewReader(body))
	req.RemoteAddr = ip + ":4000"
	rec := httptest.NewRecorder()
	h.TerminalPairAPI(rec, req)
	return rec
}

func deviceReq(h *Handler, ip, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/terminal/v1/device", nil)
	req.RemoteAddr = ip + ":4000"
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	h.TerminalDeviceAPI(rec, req)
	return rec
}

func (e *atEnv) pairCode(user string) string {
	code, err := newPairingCode()
	e.must(err)
	e.exec(`INSERT INTO terminal_pairing_codes (code_sha256, user_id, expires_at) VALUES ($1, $2::uuid, NOW() + interval '10 minutes')`, hashPairingCode(code), user)
	return code
}

func TestTerminalPairAPI(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	e := newATEnv(t)
	term := e.user("pq", "viewer", true)
	normal := e.user("pr", "viewer", false)
	h := &Handler{db: e.pool}

	code := e.pairCode(term)
	typed := strings.ToLower(formatPairingCode(code))
	rec := pairReq(h, "198.51.100.1", `{"code":"`+typed+`","name":"Pi Halle 1"}`)
	if rec.Code != 200 {
		t.Fatalf("Kopplung: %d %s", rec.Code, rec.Body.String())
	}
	var m map[string]string
	e.must(json.Unmarshal(rec.Body.Bytes(), &m))
	devID, secret := m["device_id"], m["secret"]
	if !strings.HasPrefix(secret, "pdht_") || devID == "" || m["user"] == "" {
		t.Fatalf("Antwort unvollständig: %s", rec.Body.String())
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM terminal_devices WHERE id=$1::uuid AND user_id=$2::uuid AND name='Pi Halle 1' AND secret_sha256=$3 AND revoked_at IS NULL`,
		devID, term, hashDeviceSecret(secret)).Scan(&n))
	if n != 1 {
		t.Error("Geräte-Zeile fehlt oder falsch")
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND action='terminal_paired'`, term).Scan(&n))
	if n != 1 {
		t.Error("Historieneintrag fehlt")
	}

	if rec = pairReq(h, "198.51.100.2", `{"code":"`+code+`","name":"Pi"}`); rec.Code != 422 {
		t.Errorf("zweite Verwendung: %d", rec.Code)
	}

	code = e.pairCode(term)
	e.exec(`UPDATE terminal_pairing_codes SET expires_at = NOW() - interval '1 minute' WHERE code_sha256=$1`, hashPairingCode(code))
	if rec = pairReq(h, "198.51.100.3", `{"code":"`+code+`","name":"Pi"}`); rec.Code != 422 {
		t.Errorf("abgelaufen: %d", rec.Code)
	}

	code = e.pairCode(normal)
	if rec = pairReq(h, "198.51.100.4", `{"code":"`+code+`","name":"Pi"}`); rec.Code != 422 {
		t.Errorf("Nicht-System-Benutzer: %d", rec.Code)
	}

	code = e.pairCode(term)
	e.exec(`UPDATE users SET active=false WHERE id=$1::uuid`, term)
	if rec = pairReq(h, "198.51.100.5", `{"code":"`+code+`","name":"Pi"}`); rec.Code != 422 {
		t.Errorf("deaktiviert: %d", rec.Code)
	}
	e.exec(`UPDATE users SET active=true WHERE id=$1::uuid`, term)

	code = e.pairCode(term)
	var wg sync.WaitGroup
	var ok int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if pairReq(h, "198.51.101."+strconv.Itoa(i), `{"code":"`+code+`","name":"Pi"}`).Code == 200 {
				atomic.AddInt32(&ok, 1)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 {
		t.Errorf("parallele Kopplungen erfolgreich: %d", ok)
	}

	code = e.pairCode(term)
	for i := 0; i < 5; i++ {
		if rec = pairReq(h, "198.51.102.1", `{"code":"AAAA-AAAA","name":"Pi"}`); rec.Code != 422 {
			t.Fatalf("Fehlversuch %d: %d", i, rec.Code)
		}
	}
	if rec = pairReq(h, "198.51.102.1", `{"code":"`+code+`","name":"Pi"}`); rec.Code != 429 {
		t.Errorf("gesperrt erwartet: %d", rec.Code)
	}
}

func TestTerminalDeviceAPI(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	e := newATEnv(t)
	term := e.user("ps", "viewer", true)
	h := &Handler{db: e.pool}
	mk := func(name string) (id, secret string) {
		secret, err := newDeviceSecret()
		e.must(err)
		id = e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, $2, $3) RETURNING id::text`, term, name, hashDeviceSecret(secret))
		return id, secret
	}
	id, secret := mk("Pi A")
	rec := deviceReq(h, "198.51.103.1", secret)
	if rec.Code != 200 {
		t.Fatalf("gültig: %d %s", rec.Code, rec.Body.String())
	}
	var m map[string]string
	e.must(json.Unmarshal(rec.Body.Bytes(), &m))
	if m["device_id"] != id || m["name"] != "Pi A" || m["user"] == "" {
		t.Errorf("Antwort: %s", rec.Body.String())
	}
	var seen bool
	e.must(e.pool.QueryRow(e.ctx, `SELECT last_seen_at IS NOT NULL FROM terminal_devices WHERE id=$1::uuid`, id).Scan(&seen))
	if !seen {
		t.Error("last_seen_at nicht gesetzt")
	}
	uid, active, err := h.deviceByID(e.ctx, id)
	if err != nil || uid != term || !active {
		t.Errorf("deviceByID: %q %v %v", uid, active, err)
	}

	e.exec(`UPDATE terminal_devices SET revoked_at=NOW() WHERE id=$1::uuid`, id)
	if rec = deviceReq(h, "198.51.103.2", secret); rec.Code != 401 || !strings.Contains(rec.Body.String(), "Gerät nicht gekoppelt oder gesperrt") {
		t.Errorf("gesperrt: %d %s", rec.Code, rec.Body.String())
	}
	if _, active, _ = h.deviceByID(e.ctx, id); active {
		t.Error("gesperrtes Gerät darf nicht aktiv sein")
	}

	id2, secret2 := mk("Pi B")
	e.exec(`UPDATE users SET active=false WHERE id=$1::uuid`, term)
	if rec = deviceReq(h, "198.51.103.3", secret2); rec.Code != 401 {
		t.Errorf("Benutzer deaktiviert: %d", rec.Code)
	}
	if _, active, _ = h.deviceByID(e.ctx, id2); active {
		t.Error("Gerät deaktivierter Benutzer darf nicht aktiv sein")
	}
	e.exec(`UPDATE users SET active=true WHERE id=$1::uuid`, term)

	for i := 0; i < 5; i++ {
		if rec = deviceReq(h, "198.51.104.1", "pdht_falsch"); rec.Code != 401 {
			t.Fatalf("Fehlversuch %d: %d", i, rec.Code)
		}
	}
	if rec = deviceReq(h, "198.51.104.1", secret2); rec.Code != 429 {
		t.Errorf("gesperrt erwartet: %d", rec.Code)
	}
}
