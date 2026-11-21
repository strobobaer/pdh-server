//go:build integration

// Zugang anfragen und Token abholen (PDH-App) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run AppRequest ./internal/web/
package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/modules/tickets"

	"github.com/go-chi/chi/v5"
)

type appReqEnv struct {
	*atEnv
	h      *Handler
	router http.Handler
	ip     int
}

func newAppReqEnv(t *testing.T) *appReqEnv {
	e := newATEnv(t)
	appRequestLimit = newFailLimiter(3, time.Hour)
	appFailLimit = newFailLimiter(10, 5*time.Minute)
	h := &Handler{db: e.pool, tickets: tickets.NewService(tickets.NewRepository(e.pool))}
	r := chi.NewRouter()
	r.Post("/api/app/v1/requests", h.AppRequestCreateAPI)
	r.Get("/api/app/v1/requests/{id}", h.AppRequestStatusAPI)
	env := &appReqEnv{atEnv: e, h: h, router: r}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM tickets WHERE app_request_id IN (SELECT id FROM app_requests WHERE name LIKE '%'||$1||'%')`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM app_devices WHERE label LIKE '%'||$1||'%'`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM app_requests WHERE name LIKE '%'||$1||'%'`, e.sfx)
	})
	return env
}

// nextIP gibt je Szenario eine eigene Quelladresse.
func (e *appReqEnv) nextIP() string {
	e.ip++
	return fmt.Sprintf("192.0.2.%d:4000", e.ip)
}

func (e *appReqEnv) post(ip, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/app/v1/requests", strings.NewReader(body))
	req.RemoteAddr = ip
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *appReqEnv) get(ip, id string, k *ecdsa.PrivateKey, nonce string) *httptest.ResponseRecorder {
	target := "/api/app/v1/requests/" + id
	var req *http.Request
	if k == nil {
		req = httptest.NewRequest("GET", target, nil)
	} else {
		req = appSignedRequest(e.t, k, "GET", target, nil, time.Now(), nonce)
	}
	req.RemoteAddr = ip
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *appReqEnv) body(name, dept, platform, pub string) string {
	b, _ := json.Marshal(map[string]string{"name": name, "department_id": dept, "device_label": "Pixel " + e.sfx,
		"platform": platform, "app_version": "1.0.0", "public_key": pub})
	return string(b)
}

func (e *appReqEnv) nonce(s string) string { return "nonce-" + s + "-" + e.sfx + "0000000000" }

type appReqResp struct {
	RequestID   string `json:"request_id"`
	ConfirmCode string `json:"confirm_code"`
	Status      string `json:"status"`
	Token       string `json:"token"`
	DeviceID    string `json:"device_id"`
	Error       string `json:"error"`
}

func decodeAppReq(t *testing.T, rec *httptest.ResponseRecorder) appReqResp {
	t.Helper()
	var m appReqResp
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("antwort: %v: %s", err, rec.Body.String())
	}
	return m
}

func TestAppRequestCreate(t *testing.T) {
	e := newAppReqEnv(t)
	dept := e.dept("Abt")
	deptName := e.id(`SELECT name FROM departments WHERE id = $1::uuid`, dept)
	name := "Erika Muster " + e.sfx
	ip := e.nextIP()
	_, pub := appTestKey(t)

	rec := e.post(ip, e.body(name, dept, "android", pub))
	if rec.Code != 201 {
		t.Fatalf("neu: %d %s", rec.Code, rec.Body.String())
	}
	m := decodeAppReq(t, rec)
	if m.Status != "pending" || m.RequestID == "" || m.ConfirmCode != appConfirmCode(appKeyFingerprint(pub)) || len(m.ConfirmCode) != 6 {
		t.Fatalf("antwort: %+v", m)
	}
	var cnt int
	var title, desc, createdBy, group, status, prio string
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM tickets WHERE app_request_id = $1::uuid`, m.RequestID).Scan(&cnt))
	if cnt != 1 {
		t.Fatalf("tickets: %d", cnt)
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT title, description, created_by::text, assigned_group_id::text, status, priority FROM tickets WHERE app_request_id = $1::uuid`, m.RequestID).
		Scan(&title, &desc, &createdBy, &group, &status, &prio))
	if title != "App-Zugang: "+name+" ("+deptName+")" || createdBy != appSystemUserID || group != appAdminGroupID || status != "open" || prio != "medium" {
		t.Errorf("ticket: %q %s %s %s %s", title, createdBy, group, status, prio)
	}
	if !strings.Contains(desc, m.ConfirmCode) || !strings.Contains(desc, "android") || !strings.Contains(desc, "1.0.0") || !strings.Contains(desc, "Pixel") {
		t.Errorf("beschreibung: %q", desc)
	}
	var tid string
	e.must(e.pool.QueryRow(e.ctx, `SELECT COALESCE(ticket_id::text, '') FROM app_requests WHERE id = $1::uuid`, m.RequestID).Scan(&tid))
	if tid == "" {
		t.Error("ticket_id der Anfrage fehlt")
	}

	// Wiederholung mit demselben Schlüssel: dieselbe Anfrage, kein zweites Ticket, zählt nicht.
	for i := 0; i < 5; i++ {
		rec = e.post(ip, e.body(name, dept, "android", pub))
		if rec.Code != 200 {
			t.Fatalf("wiederholung %d: %d %s", i, rec.Code, rec.Body.String())
		}
		if m2 := decodeAppReq(t, rec); m2.RequestID != m.RequestID || m2.Status != "pending" || m2.ConfirmCode != m.ConfirmCode {
			t.Fatalf("wiederholung: %+v", m2)
		}
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM tickets WHERE app_request_id = $1::uuid`, m.RequestID).Scan(&cnt))
	if cnt != 1 {
		t.Errorf("tickets nach Wiederholung: %d", cnt)
	}

	// Ohne Abteilung: Titel ohne Klammer. Zweite neue Anfrage der Quelle.
	_, pub2 := appTestKey(t)
	rec = e.post(ip, e.body(name, "", "ios", pub2))
	if rec.Code != 201 {
		t.Fatalf("ohne Abteilung: %d %s", rec.Code, rec.Body.String())
	}
	m2 := decodeAppReq(t, rec)
	e.must(e.pool.QueryRow(e.ctx, `SELECT title FROM tickets WHERE app_request_id = $1::uuid`, m2.RequestID).Scan(&title))
	if title != "App-Zugang: "+name {
		t.Errorf("titel ohne Abteilung: %q", title)
	}
	// Dritte neue erlaubt, vierte neue -> 429.
	_, pub3 := appTestKey(t)
	if rec = e.post(ip, e.body(name, dept, "ios", pub3)); rec.Code != 201 {
		t.Fatalf("dritte: %d", rec.Code)
	}
	_, pub4 := appTestKey(t)
	if rec = e.post(ip, e.body(name, dept, "ios", pub4)); rec.Code != 429 {
		t.Errorf("vierte neue Anfrage: %d, erwartet 429", rec.Code)
	}
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM app_requests WHERE key_fingerprint = $1`, appKeyFingerprint(pub4)).Scan(&cnt))
	if cnt != 0 {
		t.Error("gesperrte Anfrage wurde angelegt")
	}
}

func TestAppRequestCreateInvalid(t *testing.T) {
	e := newAppReqEnv(t)
	dept := e.dept("Abt")
	ip := e.nextIP()
	_, pub := appTestKey(t)
	k384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der384, err := x509.MarshalPKIXPublicKey(&k384.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	p384 := base64.RawURLEncoding.EncodeToString(der384)
	cases := map[string]string{
		"leerer Name":       e.body("  ", dept, "ios", pub),
		"Name zu lang":      e.body(strings.Repeat("a", 121), dept, "ios", pub),
		"Steuerzeichen":     e.body("A‮B", dept, "ios", pub),
		"Abteilung fremd":   e.body("X "+e.sfx, "00000000-0000-4000-8000-00000000dead", "ios", pub),
		"Abteilung kein ID": e.body("X "+e.sfx, "nope", "ios", pub),
		"Plattform":         e.body("X "+e.sfx, dept, "windows", pub),
		"Schlüssel Müll":    e.body("X "+e.sfx, dept, "ios", p384),
		"unbekanntes Feld":  `{"name":"X","platform":"ios","app_version":"1","public_key":"` + pub + `","extra":1}`,
		"Body zu groß":      `{"name":"` + strings.Repeat("a", 9000) + `"}`,
		"kein JSON":         `nope`,
		"Version leer":      strings.Replace(e.body("X "+e.sfx, dept, "ios", pub), `"1.0.0"`, `""`, 1),
	}
	for n, b := range cases {
		if rec := e.post(ip, b); rec.Code != 422 {
			t.Errorf("%s: %d %s", n, rec.Code, rec.Body.String())
		}
	}
	// Ungültige Anfragen zählen nicht gegen das Limit.
	for i := 0; i < 3; i++ {
		_, k := appTestKey(t)
		if rec := e.post(ip, e.body("Ok "+e.sfx, dept, "ios", k)); rec.Code != 201 {
			t.Fatalf("gültige %d nach ungültigen: %d", i, rec.Code)
		}
	}
}

func TestAppRequestPickup(t *testing.T) {
	e := newAppReqEnv(t)
	ip := e.nextIP()
	priv, pub := appTestKey(t)
	rec := e.post(ip, e.body("Pick "+e.sfx, "", "android", pub))
	if rec.Code != 201 {
		t.Fatalf("anfrage: %d", rec.Code)
	}
	id := decodeAppReq(t, rec).RequestID

	// offen
	rec = e.get(ip, id, priv, e.nonce("a"))
	if rec.Code != 200 || decodeAppReq(t, rec).Status != "pending" {
		t.Fatalf("offen: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("Cache-Control fehlt")
	}
	// Nonce-Wiederverwendung wird abgelehnt.
	if rec = e.get(ip, id, priv, e.nonce("a")); rec.Code != 401 {
		t.Errorf("wiederverwendete Nonce: %d", rec.Code)
	}

	// ohne Signatur / falscher Schlüssel / unbekannte ID -> 401, zählt (eigene Quelle)
	ip2 := e.nextIP()
	other, _ := appTestKey(t)
	if rec = e.get(ip2, id, nil, ""); rec.Code != 401 {
		t.Errorf("ohne Signatur: %d", rec.Code)
	}
	if rec = e.get(ip2, id, other, e.nonce("b")); rec.Code != 401 {
		t.Errorf("fremder Schlüssel: %d", rec.Code)
	}
	if rec = e.get(ip2, "00000000-0000-4000-8000-00000000dead", priv, e.nonce("c")); rec.Code != 401 {
		t.Errorf("unbekannte ID: %d", rec.Code)
	}
	if rec = e.get(ip2, "kein-uuid", priv, e.nonce("d")); rec.Code != 401 {
		t.Errorf("keine UUID: %d", rec.Code)
	}
	for i := 0; i < 6; i++ { // bis zur Sperre (10 Fehlversuche)
		e.get(ip2, id, nil, "")
	}
	if rec = e.get(ip2, id, priv, e.nonce("e")); rec.Code != 429 {
		t.Errorf("nach Fehlversuchen: %d, erwartet 429", rec.Code)
	}

	// Freigabe wie Task 6
	uid := e.user("appusr", "worker", false)
	var devID string
	e.must(e.pool.QueryRow(e.ctx, `INSERT INTO app_devices (request_id, user_id, public_key, key_fingerprint, platform, app_version, label)
		SELECT id, $2::uuid, public_key, key_fingerprint, platform, app_version, device_label FROM app_requests WHERE id = $1::uuid RETURNING id::text`, id, uid).Scan(&devID))
	e.exec(`UPDATE app_requests SET status = 'approved', device_id = $2::uuid, decided_at = NOW() WHERE id = $1::uuid`, id, devID)

	rec = e.get(ip, id, priv, e.nonce("f"))
	m := decodeAppReq(t, rec)
	if rec.Code != 200 || m.Status != "approved" || !strings.HasPrefix(m.Token, "pdha_") || m.DeviceID != devID {
		t.Fatalf("freigegeben: %d %s", rec.Code, rec.Body.String())
	}
	var h1 string
	e.must(e.pool.QueryRow(e.ctx, `SELECT token_sha256 FROM app_devices WHERE id = $1::uuid`, devID).Scan(&h1))
	if h1 != hashAppToken(m.Token) {
		t.Error("gespeicherter Hash passt nicht")
	}
	var issued *time.Time
	e.must(e.pool.QueryRow(e.ctx, `SELECT token_issued_at FROM app_devices WHERE id = $1::uuid`, devID).Scan(&issued))
	if issued == nil {
		t.Error("token_issued_at fehlt")
	}
	// zweites Abholen vor Erstnutzung: neuer Token, alter Hash ersetzt
	rec = e.get(ip, id, priv, e.nonce("g"))
	m2 := decodeAppReq(t, rec)
	if m2.Token == "" || m2.Token == m.Token {
		t.Fatalf("kein neuer Token: %s", rec.Body.String())
	}
	var h2 string
	e.must(e.pool.QueryRow(e.ctx, `SELECT token_sha256 FROM app_devices WHERE id = $1::uuid`, devID).Scan(&h2))
	if h2 != hashAppToken(m2.Token) || h2 == h1 {
		t.Error("Hash nicht ersetzt")
	}
	// nach Erstnutzung: kein Token mehr
	e.exec(`UPDATE app_devices SET first_used_at = NOW() WHERE id = $1::uuid`, devID)
	rec = e.get(ip, id, priv, e.nonce("h"))
	m3 := decodeAppReq(t, rec)
	if rec.Code != 200 || m3.Status != "approved" || m3.Token != "" {
		t.Errorf("nach Erstnutzung: %s", rec.Body.String())
	}
	var h3 string
	e.must(e.pool.QueryRow(e.ctx, `SELECT token_sha256 FROM app_devices WHERE id = $1::uuid`, devID).Scan(&h3))
	if h3 != h2 {
		t.Error("Hash nach Erstnutzung verändert")
	}
}

func TestAppRequestRejected(t *testing.T) {
	e := newAppReqEnv(t)
	ip := e.nextIP()
	priv, pub := appTestKey(t)
	id := decodeAppReq(t, e.post(ip, e.body("Rej "+e.sfx, "", "ios", pub))).RequestID
	e.exec(`UPDATE app_requests SET status = 'rejected', decided_at = NOW() WHERE id = $1::uuid`, id)
	rec := e.get(ip, id, priv, e.nonce("r"))
	if rec.Code != 200 || decodeAppReq(t, rec).Status != "rejected" {
		t.Fatalf("abgelehnt: %d %s", rec.Code, rec.Body.String())
	}
	_, pub2 := appTestKey(t)
	rec = e.post(ip, e.body("Rej "+e.sfx, "", "ios", pub2))
	if rec.Code != 201 || decodeAppReq(t, rec).RequestID == id {
		t.Errorf("neue Anfrage nach Ablehnung: %d %s", rec.Code, rec.Body.String())
	}
	// Schon registrierter Gerätschlüssel: keine neue Anfrage.
	uid := e.user("appusr2", "worker", false)
	_, pub3 := appTestKey(t)
	e.exec(`INSERT INTO app_devices (user_id, public_key, key_fingerprint, label) VALUES ($1::uuid, $2, $3, $4)`, uid, pub3, appKeyFingerprint(pub3), "x "+e.sfx)
	if rec = e.post(ip, e.body("Dev "+e.sfx, "", "ios", pub3)); rec.Code != 409 {
		t.Errorf("Schlüssel eines Geräts: %d", rec.Code)
	}
}

// postXFF: wie post, mit X-Forwarded-For.
func (e *appReqEnv) postXFF(ip, xff, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/app/v1/requests", strings.NewReader(body))
	req.RemoteAddr = ip
	req.Header.Set("X-Forwarded-For", xff)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *appReqEnv) newBody(name string) (string, string) {
	_, k := appTestKey(e.t)
	return e.body(name+" "+e.sfx, "", "ios", k), k
}

// Das Limit hält auch bei parallelen Anfragen: höchstens 3 werden angelegt.
func TestAppRequestLimitParallel(t *testing.T) {
	e := newAppReqEnv(t)
	ip := e.nextIP()
	const n = 12
	bodies := make([]string, n)
	for i := range bodies {
		bodies[i], _ = e.newBody("Par")
	}
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = e.post(ip, bodies[i]).Code
		}(i)
	}
	wg.Wait()
	created, limited := 0, 0
	for _, c := range codes {
		switch c {
		case 201:
			created++
		case 429:
			limited++
		}
	}
	if created != 3 || limited != n-3 {
		t.Fatalf("codes: %v", codes)
	}
	var cnt int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM app_requests WHERE name LIKE '%'||$1||'%'`, e.sfx).Scan(&cnt))
	if cnt != 3 {
		t.Errorf("angelegte Anfragen: %d", cnt)
	}
}

// Gleichzeitige Anfragen mit demselben Schlüssel: eine Anfrage, ein Ticket, ein Platz im Limit.
func TestAppRequestSameKeyParallel(t *testing.T) {
	e := newAppReqEnv(t)
	ip := e.nextIP()
	body, _ := e.newBody("Same")
	const n = 3 // innerhalb des Limits, damit der 23505-Weg geprüft wird
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recs[i] = e.post(ip, body)
		}(i)
	}
	wg.Wait()
	id, created := "", 0
	for _, rec := range recs {
		if rec.Code != 200 && rec.Code != 201 {
			t.Fatalf("antwort: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Code == 201 {
			created++
		}
		m := decodeAppReq(t, rec)
		if id == "" {
			id = m.RequestID
		} else if m.RequestID != id {
			t.Fatalf("verschiedene Anfragen: %s %s", id, m.RequestID)
		}
	}
	if created != 1 {
		t.Errorf("201 mal: %d", created)
	}
	var cnt int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM tickets WHERE app_request_id = $1::uuid`, id).Scan(&cnt))
	if cnt != 1 {
		t.Errorf("tickets: %d", cnt)
	}
	// Nur ein Platz belegt: zwei weitere neue Anfragen gehen noch.
	for i := 0; i < 2; i++ {
		b, _ := e.newBody("More")
		if rec := e.post(ip, b); rec.Code != 201 {
			t.Errorf("weitere %d: %d", i, rec.Code)
		}
	}
}

// Quelladresse: X-Forwarded-For zählt nur von Loopback.
func TestAppRequestClientAddress(t *testing.T) {
	e := newAppReqEnv(t)
	fill := func(ip, xff string) {
		for i := 0; i < 3; i++ {
			b, _ := e.newBody("Fill")
			if rec := e.postXFF(ip, xff, b); rec.Code != 201 {
				t.Fatalf("%s %s: %d", ip, xff, rec.Code)
			}
		}
	}
	// Von Loopback: getrennte Behälter je weitergereichter Adresse.
	fill("127.0.0.1:5000", "203.0.113.1")
	b, _ := e.newBody("A")
	if rec := e.postXFF("127.0.0.1:5000", "203.0.113.1", b); rec.Code != 429 {
		t.Errorf("gleiche Adresse: %d", rec.Code)
	}
	b, _ = e.newBody("B")
	if rec := e.postXFF("127.0.0.1:5000", "203.0.113.2", b); rec.Code != 201 {
		t.Errorf("andere Adresse: %d", rec.Code)
	}
	// Nicht von Loopback: Kopfzeile wird ignoriert.
	ip := e.nextIP()
	fill(ip, "203.0.113.50")
	b, _ = e.newBody("C")
	if rec := e.postXFF(ip, "203.0.113.51", b); rec.Code != 429 {
		t.Errorf("erfundene Adresse von außen: %d", rec.Code)
	}
}

// Gesperrtes Gerät: Abholen -> 401 app_revoked (auch vor der Erstnutzung); neue Anfrage -> 409.
func TestAppRequestRevokedDevice(t *testing.T) {
	e := newAppReqEnv(t)
	ip := e.nextIP()
	priv, pub := appTestKey(t)
	id := decodeAppReq(t, e.post(ip, e.body("Rev "+e.sfx, "", "ios", pub))).RequestID
	uid := e.user("apprev", "worker", false)
	var devID string
	e.must(e.pool.QueryRow(e.ctx, `INSERT INTO app_devices (request_id, user_id, public_key, key_fingerprint, label)
		SELECT id, $2::uuid, public_key, key_fingerprint, device_label FROM app_requests WHERE id = $1::uuid RETURNING id::text`, id, uid).Scan(&devID))
	e.exec(`UPDATE app_requests SET status = 'approved', device_id = $2::uuid WHERE id = $1::uuid`, id, devID)

	// aktiv: bestehende Meldung
	rec := e.post(ip, e.body("Rev "+e.sfx, "", "ios", pub))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "bereits eingerichtet") {
		t.Errorf("aktives Gerät: %d %s", rec.Code, rec.Body.String())
	}
	e.exec(`UPDATE app_devices SET revoked_at = NOW() WHERE id = $1::uuid`, devID)
	rec = e.get(ip, id, priv, e.nonce("v1"))
	if rec.Code != 401 || decodeAppReq(t, rec).Error != "app_revoked" || strings.Contains(rec.Body.String(), "pdha_") {
		t.Errorf("gesperrt vor Erstnutzung: %d %s", rec.Code, rec.Body.String())
	}
	e.exec(`UPDATE app_devices SET first_used_at = NOW() WHERE id = $1::uuid`, devID)
	rec = e.get(ip, id, priv, e.nonce("v2"))
	if rec.Code != 401 || decodeAppReq(t, rec).Error != "app_revoked" {
		t.Errorf("gesperrt nach Erstnutzung: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.post(ip, e.body("Rev "+e.sfx, "", "ios", pub))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "Dieses Gerät wurde gesperrt. Bitte die App zurücksetzen und den Zugang neu anfragen.") {
		t.Errorf("neue Anfrage gesperrt: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAppRequestNameMessage(t *testing.T) {
	e := newAppReqEnv(t)
	_, pub := appTestKey(t)
	rec := e.post(e.nextIP(), e.body("  ", "", "ios", pub))
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Der Name muss 1 bis 120 Zeichen lang sein") {
		t.Errorf("meldung: %d %s", rec.Code, rec.Body.String())
	}
}
