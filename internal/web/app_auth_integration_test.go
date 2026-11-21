//go:build integration

// Prüfung jeder App-Anfrage (Token, Signatur, Sperre, Mindestversion, Recht).
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run AppAuth ./internal/web/
package web

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pdh/internal/core/users"

	"github.com/go-chi/chi/v5"
)

type appAuthEnv struct {
	*atEnv
	h      *Handler
	router http.Handler
	uid    string
	devID  string
	token  string
	key    *ecdsa.PrivateKey
	n      int
}

func newAppAuthEnv(t *testing.T) *appAuthEnv {
	e := newATEnv(t)
	appFailLimit = newFailLimiter(10, 5*time.Minute)
	appDeviceCache = newDeviceCache(30 * time.Second)
	uid := e.user("appu", "worker", false)
	e.exec(`UPDATE users SET department = 'Fertigung', first_name = 'Erika', last_name = 'Muster' WHERE id = $1::uuid`, uid)
	e.grant(uid, permAppUse, "", true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-geheim", 1)}
	r := chi.NewRouter()
	r.With(h.AppAuth).Get("/api/app/v1/me", h.AppMeAPI)
	r.With(h.AppAuth).Post("/api/app/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	})
	k, pub := appTestKey(t)
	tok, err := newAppToken()
	e.must(err)
	devID := e.id(`INSERT INTO app_devices (user_id, public_key, key_fingerprint, token_sha256, platform, app_version, label, may_create)
		VALUES ($1::uuid, $2, $3, $4, 'android', '1.0.0', $5, true) RETURNING id::text`, uid, pub, appKeyFingerprint(pub), hashAppToken(tok), "auth "+e.sfx)
	t.Cleanup(func() {
		appDeviceCache.forget(devID)
		_, _ = e.pool.Exec(e.ctx, `DELETE FROM app_devices WHERE id = $1::uuid`, devID)
	})
	return &appAuthEnv{atEnv: e, h: h, router: r, uid: uid, devID: devID, token: tok, key: k}
}

// nonce liefert je Aufruf eine neue gültige Nonce (16–64 Zeichen).
func (e *appAuthEnv) nonce() string {
	e.n++
	return fmt.Sprintf("n%d-%s-0000000000", e.n, e.sfx)
}

func (e *appAuthEnv) call(method, target string, body []byte, k *ecdsa.PrivateKey, tok, version string) *httptest.ResponseRecorder {
	req := appSignedRequest(e.t, k, method, target, body, time.Now(), e.nonce())
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	if tok != "" {
		req.Header.Set("Authorization", "PDHApp "+tok)
	}
	if version != "" {
		req.Header.Set("X-PDH-App-Version", version)
	}
	req.RemoteAddr = "192.0.2.77:5000"
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *appAuthEnv) me(version string) *httptest.ResponseRecorder {
	return e.call("GET", "/api/app/v1/me", nil, e.key, e.token, version)
}

func appJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("antwort: %v: %s", err, rec.Body.String())
	}
	return m
}

func (e *appAuthEnv) wantErr(rec *httptest.ResponseRecorder, code int, errKey string) {
	e.t.Helper()
	if rec.Code != code || appJSON(e.t, rec)["error"] != errKey {
		e.t.Fatalf("erwartet %d %s, bekam %d %s", code, errKey, rec.Code, rec.Body.String())
	}
}

func TestAppAuth(t *testing.T) {
	t.Run("gültig, erste Nutzung gesetzt", func(t *testing.T) {
		e := newAppAuthEnv(t)
		rec := e.me("1.0.0")
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		m := appJSON(t, rec)
		u := m["user"].(map[string]any)
		if u["id"] != e.uid || u["name"] != "Erika Muster" || u["department"] != "Fertigung" || m["device_id"] != e.devID {
			t.Errorf("antwort: %v", m)
		}
		perms := m["perms"].(map[string]any)
		if perms["app.use"] != true || perms["app.create"] != false || perms["app.admin"] != false {
			t.Errorf("perms: %v", perms)
		}
		if ts, ok := m["allowed_types"].([]any); !ok || len(ts) != 4 {
			t.Errorf("allowed_types: %v", m["allowed_types"])
		}
		var first, seen *time.Time
		e.must(e.pool.QueryRow(e.ctx, `SELECT first_used_at, last_seen_at FROM app_devices WHERE id = $1::uuid`, e.devID).Scan(&first, &seen))
		if first == nil || seen == nil {
			t.Fatalf("first_used_at/last_seen_at: %v %v", first, seen)
		}
		// last_seen_at höchstens einmal je Minute
		old := time.Now().Add(-10 * time.Second)
		e.exec(`UPDATE app_devices SET last_seen_at = $2 WHERE id = $1::uuid`, e.devID, old)
		if rec := e.me("1.0.0"); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
		var seen2 time.Time
		e.must(e.pool.QueryRow(e.ctx, `SELECT last_seen_at FROM app_devices WHERE id = $1::uuid`, e.devID).Scan(&seen2))
		if seen2.After(old.Add(time.Second)) {
			t.Errorf("last_seen_at innerhalb der Minute erneut gesetzt: %v", seen2)
		}
		// älter als eine Minute: wird aktualisiert
		e.exec(`UPDATE app_devices SET last_seen_at = NOW() - interval '2 minutes' WHERE id = $1::uuid`, e.devID)
		e.me("1.0.0")
		e.must(e.pool.QueryRow(e.ctx, `SELECT last_seen_at FROM app_devices WHERE id = $1::uuid`, e.devID).Scan(&seen2))
		if time.Since(seen2) > 30*time.Second {
			t.Errorf("last_seen_at nicht aktualisiert: %v", seen2)
		}
	})

	t.Run("Signatur mit fremdem Schlüssel", func(t *testing.T) {
		e := newAppAuthEnv(t)
		other, _ := appTestKey(t)
		e.wantErr(e.call("GET", "/api/app/v1/me", nil, other, e.token, "1.0.0"), 401, "app_unauthorized")
	})

	t.Run("unbekanntes Token, fehlende Kopfzeile", func(t *testing.T) {
		e := newAppAuthEnv(t)
		e.wantErr(e.call("GET", "/api/app/v1/me", nil, e.key, "pdha_unbekannt", "1.0.0"), 401, "app_unauthorized")
		e.wantErr(e.call("GET", "/api/app/v1/me", nil, e.key, "", "1.0.0"), 401, "app_unauthorized")
	})

	t.Run("Wiederholung mit gleicher Nonce", func(t *testing.T) {
		e := newAppAuthEnv(t)
		req := appSignedRequest(t, e.key, "GET", "/api/app/v1/me", nil, time.Now(), e.nonce())
		req.Header.Set("Authorization", "PDHApp "+e.token)
		for i, want := range []int{200, 401} {
			rec := httptest.NewRecorder()
			e.router.ServeHTTP(rec, req.Clone(req.Context()))
			if rec.Code != want {
				t.Fatalf("Versuch %d: %d %s", i, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("Sperre mit forget", func(t *testing.T) {
		e := newAppAuthEnv(t)
		if rec := e.me("1.0.0"); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
		e.exec(`UPDATE app_devices SET revoked_at = NOW() WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		e.wantErr(e.me("1.0.0"), 401, "app_revoked")
	})

	t.Run("Sperre wirkt ohne forget nach Ablauf des Caches", func(t *testing.T) {
		e := newAppAuthEnv(t)
		appDeviceCache = newDeviceCache(0)
		if rec := e.me("1.0.0"); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
		e.exec(`UPDATE app_devices SET revoked_at = NOW() WHERE id = $1::uuid`, e.devID)
		e.wantErr(e.me("1.0.0"), 401, "app_revoked")
	})

	t.Run("Benutzer inaktiv", func(t *testing.T) {
		e := newAppAuthEnv(t)
		e.exec(`UPDATE users SET active = false WHERE id = $1::uuid`, e.uid)
		appDeviceCache.forget(e.devID)
		e.wantErr(e.me("1.0.0"), 401, "app_revoked")
	})

	t.Run("Mindestversion", func(t *testing.T) {
		e := newAppAuthEnv(t)
		e.setting(keyAppLatestPrefix+"android", "1.1.0")
		e.setting(keyAppAllowPrevious, "0")
		rec := e.me("1.0.0")
		if rec.Code != 426 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if m := appJSON(t, rec); m["error"] != "app_outdated" || m["min"] != "1.1.0" {
			t.Errorf("antwort: %v", m)
		}
		for _, v := range []string{"", "abc", "1.x.0", "1..0"} {
			if rec := e.me(v); rec.Code != 426 {
				t.Errorf("Version %q: %d", v, rec.Code)
			}
		}
		for _, v := range []string{"1.1.0", "1.1", "1.10.0", "2", "1.1.0.0"} {
			if rec := e.me(v); rec.Code != 200 {
				t.Errorf("Version %q: %d %s", v, rec.Code, rec.Body.String())
			}
		}
		e.setting(keyAppLatestPrefix+"android", "1.9.0")
		if rec := e.me("1.10.0"); rec.Code != 200 {
			t.Errorf("1.10.0 gegen 1.9.0: %d", rec.Code)
		}
		if rec := e.me("1.9.0"); rec.Code != 200 {
			t.Errorf("1.9.0 gegen 1.9.0: %d", rec.Code)
		}
		if rec := e.me("1.8.9"); rec.Code != 426 {
			t.Errorf("1.8.9 gegen 1.9.0: %d", rec.Code)
		}
	})

	t.Run("ohne app.use", func(t *testing.T) {
		e := newAppAuthEnv(t)
		e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, e.uid)
		e.grant(e.uid, permAppUse, "", false)
		e.warm()
		e.wantErr(e.me("1.0.0"), 403, "app_no_permission")
	})

	t.Run("may_create", func(t *testing.T) {
		e := newAppAuthEnv(t)
		if m := appJSON(t, e.me("1.0.0")); m["may_create"] != false {
			t.Fatalf("Gerät true, Benutzer ohne app.create: %v", m["may_create"])
		}
		e.grant(e.uid, permAppCreate, "", true)
		e.warm()
		if m := appJSON(t, e.me("1.0.0")); m["may_create"] != true {
			t.Errorf("mit app.create: %v", m["may_create"])
		}
		e.exec(`UPDATE app_devices SET may_create = false WHERE id = $1::uuid`, e.devID)
		if m := appJSON(t, e.me("1.0.0")); m["may_create"] != false {
			t.Errorf("Gerät false: %v", m["may_create"])
		}
	})

	t.Run("Body bleibt lesbar, Signatur deckt ihn ab", func(t *testing.T) {
		e := newAppAuthEnv(t)
		body := []byte(`{"a":1}`)
		rec := e.call("POST", "/api/app/v1/echo", body, e.key, e.token, "1.0.0")
		if rec.Code != 200 || rec.Body.String() != string(body) {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
		// Signatur über anderen Body → ungültig
		req := appSignedRequest(t, e.key, "POST", "/api/app/v1/echo", []byte(`{"a":2}`), time.Now(), e.nonce())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.Header.Set("Authorization", "PDHApp "+e.token)
		rec = httptest.NewRecorder()
		e.router.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("manipulierter Body: %d", rec.Code)
		}
		big := bytes.Repeat([]byte("x"), appAuthMaxBody+1)
		if rec := e.call("POST", "/api/app/v1/echo", big, e.key, e.token, "1.0.0"); rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("zu großer Body: %d", rec.Code)
		}
	})

	t.Run("Fehlversuche sperren die Quelle", func(t *testing.T) {
		e := newAppAuthEnv(t)
		other, _ := appTestKey(t)
		for i := 0; i < 10; i++ {
			e.call("GET", "/api/app/v1/me", nil, other, e.token, "1.0.0")
		}
		if rec := e.me("1.0.0"); rec.Code != 429 {
			t.Errorf("nach 10 Fehlern: %d", rec.Code)
		}
	})
}
