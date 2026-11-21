//go:build integration

// Wächter für schreibende App-Wege gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestAppWriteGuard ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppWriteGuard(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	uid := e.user("appw"+e.sfx, "user", false)

	runs, replays, status := 0, 0, http.StatusOK
	var lastOnce string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs++
		if appIsReplay(r) {
			replays++
		}
		lastOnce = appOnce(r)
		w.WriteHeader(status)
	})
	guard := h.appWrite(next)
	do := func(once string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/app/v1/x", strings.NewReader("{}"))
		if once != "" {
			req.Header.Set("X-PDH-Once", once)
		}
		req = req.WithContext(context.WithValue(req.Context(), appDeviceKey{}, &appDeviceCtx{ID: "d", UserID: uid}))
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		return rec
	}
	code := func(rec *httptest.ResponseRecorder) string {
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		s, _ := m["code"].(string)
		return s
	}
	once := func() string { return e.id(`SELECT pdh_uuid_v7()::text`) }

	// Spiegel
	h.mirror = MirrorInfo{On: true}
	if rec := do(once()); rec.Code != 409 || code(rec) != "app_read_only" || runs != 0 {
		t.Errorf("Spiegel: %d %s runs=%d", rec.Code, rec.Body.String(), runs)
	}
	h.mirror = MirrorInfo{}

	// Wartungssperre
	h.mlock.mu.Lock()
	h.mlock.locked = true
	h.mlock.mu.Unlock()
	rec := do(once())
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != 503 || code(rec) != "app_maintenance" || m["retry_after"] == nil || runs != 0 {
		t.Errorf("Wartung: %d %s runs=%d", rec.Code, rec.Body.String(), runs)
	}
	h.mlock.mu.Lock()
	h.mlock.locked = false
	h.mlock.mu.Unlock()

	// Once fehlt / ungültig
	for _, bad := range []string{"", "abc", "123e4567-e89b-12d3-a456"} {
		if rec := do(bad); rec.Code != 422 || code(rec) != "app_invalid_input" {
			t.Errorf("Once %q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if runs != 0 {
		t.Fatalf("Handler lief trotz ungültiger Once: %d", runs)
	}

	// gleiche Once zweimal: Handler genau einmal, zweite Antwort mit Replay-Kopf
	o := once()
	r1 := do(o)
	if r1.Code != 200 || r1.Header().Get("X-PDH-Once-Replay") != "" || runs != 1 || replays != 0 || lastOnce != o {
		t.Errorf("erste: %d replay=%q runs=%d once=%q", r1.Code, r1.Header().Get("X-PDH-Once-Replay"), runs, lastOnce)
	}
	r2 := do(o)
	if r2.Header().Get("X-PDH-Once-Replay") != "1" || replays != 1 {
		t.Errorf("zweite: Replay-Kopf %q replays=%d", r2.Header().Get("X-PDH-Once-Replay"), replays)
	}
	// Der Handler läuft auch beim Replay (antwortet nur mit dem Stand), schreibt aber nicht:
	// dafür sorgt appIsReplay; hier zählen wir die echten Ausführungen.
	if runs-replays != 1 {
		t.Errorf("echte Ausführungen = %d, will 1", runs-replays)
	}

	// Großschreibung ist dieselbe Once
	if rec := do(strings.ToUpper(o)); rec.Header().Get("X-PDH-Once-Replay") != "1" {
		t.Errorf("Once in Großbuchstaben nicht erkannt")
	}

	// Fehlschlag (500) gibt die Once frei
	o2 := once()
	status = 500
	runsBefore, replaysBefore := runs, replays
	if rec := do(o2); rec.Code != 500 {
		t.Errorf("500: %d", rec.Code)
	}
	status = 200
	rec = do(o2)
	if rec.Header().Get("X-PDH-Once-Replay") != "" || runs-runsBefore != 2 || replays != replaysBefore {
		t.Errorf("Once nach 500 nicht frei: replay=%q runs+=%d", rec.Header().Get("X-PDH-Once-Replay"), runs-runsBefore)
	}

	// M4: ohne Gerät (nicht hinter appAuth) → 401
	noDev := httptest.NewRequest("POST", "/api/app/v1/x", nil)
	noDev.Header.Set("X-PDH-Once", once())
	rec = httptest.NewRecorder()
	guard.ServeHTTP(rec, noDev)
	if rec.Code != 401 || code(rec) != "app_unauthorized" {
		t.Errorf("ohne Gerät: %d %s", rec.Code, rec.Body.String())
	}

	// M6: Replay eines erfolgreichen ersten Aufrufs: Handler läuft einmal mehr mit appIsReplay
	o6 := once()
	status = 200
	r0, p0 := runs, replays
	do(o6)
	do(o6)
	if runs-r0 != 2 || replays-p0 != 1 {
		t.Errorf("M6: runs+=%d replays+=%d, will 2/1", runs-r0, replays-p0)
	}

	// M3: Once eines anderen Benutzers oder Pfads → 409 app_conflict, Handler läuft nicht
	o3 := once()
	do(o3)
	rBefore := runs
	other := e.user("appx"+e.sfx, "user", false)
	req3 := httptest.NewRequest("POST", "/api/app/v1/x", nil)
	req3.Header.Set("X-PDH-Once", o3)
	req3 = req3.WithContext(context.WithValue(req3.Context(), appDeviceKey{}, &appDeviceCtx{ID: "d2", UserID: other}))
	rec = httptest.NewRecorder()
	guard.ServeHTTP(rec, req3)
	if rec.Code != 409 || code(rec) != "app_conflict" || rec.Header().Get("X-PDH-Once-Replay") != "" {
		t.Errorf("M3 anderer Benutzer: %d %s", rec.Code, rec.Body.String())
	}
	req3 = httptest.NewRequest("POST", "/api/app/v1/anderer", nil)
	req3.Header.Set("X-PDH-Once", o3)
	req3 = req3.WithContext(context.WithValue(req3.Context(), appDeviceKey{}, &appDeviceCtx{ID: "d", UserID: uid}))
	rec = httptest.NewRecorder()
	guard.ServeHTTP(rec, req3)
	if rec.Code != 409 || code(rec) != "app_conflict" || runs != rBefore {
		t.Errorf("M3 anderer Pfad: %d %s runs+=%d", rec.Code, rec.Body.String(), runs-rBefore)
	}

	// I1: erste Anfrage läuft noch → zweite 409 app_conflict mit Retry-After, ohne Handler;
	// scheitert die erste, ist die Once frei und die dritte läuft wirklich.
	o1 := once()
	started, release := make(chan struct{}), make(chan struct{})
	var bruns int
	var failing atomic.Bool
	failing.Store(true)
	blocking := h.appWrite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bruns++
		if bruns == 1 {
			close(started)
			<-release
		}
		if failing.Load() && bruns == 1 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	mk := func() *http.Request {
		q := httptest.NewRequest("POST", "/api/app/v1/x", nil)
		q.Header.Set("X-PDH-Once", o1)
		return q.WithContext(context.WithValue(q.Context(), appDeviceKey{}, &appDeviceCtx{ID: "d", UserID: uid}))
	}
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		ra := httptest.NewRecorder()
		blocking.ServeHTTP(ra, mk())
		done <- ra
	}()
	<-started
	rb := httptest.NewRecorder()
	blocking.ServeHTTP(rb, mk())
	if rb.Code != 409 || code(rb) != "app_conflict" || rb.Header().Get("Retry-After") != "2" || bruns != 1 {
		t.Errorf("I1 B: %d %s retry=%q runs=%d", rb.Code, rb.Body.String(), rb.Header().Get("Retry-After"), bruns)
	}
	close(release)
	if ra := <-done; ra.Code != 500 {
		t.Errorf("I1 A: %d", ra.Code)
	}
	rc := httptest.NewRecorder()
	blocking.ServeHTTP(rc, mk())
	if rc.Code != 200 || rc.Header().Get("X-PDH-Once-Replay") != "" || bruns != 2 {
		t.Errorf("I1 C: %d replay=%q runs=%d", rc.Code, rc.Header().Get("X-PDH-Once-Replay"), bruns)
	}

	// I2: Panik im Handler gibt die Once frei (und die Panik läuft weiter)
	o2p := once()
	pruns := 0
	panicky := h.appWrite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pruns++
		if pruns == 1 {
			panic("boom")
		}
		w.WriteHeader(200)
	}))
	mkp := func() *http.Request {
		q := httptest.NewRequest("POST", "/api/app/v1/x", nil)
		q.Header.Set("X-PDH-Once", o2p)
		return q.WithContext(context.WithValue(q.Context(), appDeviceKey{}, &appDeviceCtx{ID: "d", UserID: uid}))
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("I2: Panik wurde verschluckt")
			}
		}()
		panicky.ServeHTTP(httptest.NewRecorder(), mkp())
	}()
	rp := httptest.NewRecorder()
	panicky.ServeHTTP(rp, mkp())
	if rp.Code != 200 || rp.Header().Get("X-PDH-Once-Replay") != "" || pruns != 2 {
		t.Errorf("I2: %d replay=%q runs=%d", rp.Code, rp.Header().Get("X-PDH-Once-Replay"), pruns)
	}

	// Ohne Datenbank: vorübergehend nicht verfügbar statt ohne Schutz durchzureichen
	nodb := &Handler{}
	req := httptest.NewRequest("POST", "/api/app/v1/x", nil)
	req.Header.Set("X-PDH-Once", once())
	req = req.WithContext(context.WithValue(req.Context(), appDeviceKey{}, &appDeviceCtx{ID: "d", UserID: uid}))
	rec = httptest.NewRecorder()
	nodb.appWrite(next).ServeHTTP(rec, req)
	if rec.Code != 503 || code(rec) != "app_unavailable" {
		t.Errorf("ohne Datenbank: %d %s", rec.Code, rec.Body.String())
	}
}

// R2: Spiegel- und Wartungsantwort der Wurzel-Kette tragen für /api/app/v1 einen code.
func TestAppRootGuardCodes(t *testing.T) {
	h := &Handler{}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	call := func(h http.Handler, path string) (int, map[string]any) {
		req := httptest.NewRequest("POST", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	h.mirror = MirrorInfo{On: true}
	mw := h.MirrorMiddleware(ok)
	if c, m := call(mw, "/api/app/v1/x"); c != 409 || m["code"] != "app_read_only" {
		t.Errorf("Spiegel app: %d %v", c, m)
	}
	if c, m := call(mw, "/api/v1/x"); c != 409 || m["code"] != nil {
		t.Errorf("Spiegel andere API verändert: %d %v", c, m)
	}
	h.mirror = MirrorInfo{}
	h.mlock.mu.Lock()
	h.mlock.locked, h.mlock.view = true, lockedView()
	h.mlock.mu.Unlock()
	ml := h.MaintLockMiddleware(ok)
	if c, m := call(ml, "/api/app/v1/x"); c != 503 || m["code"] != "app_maintenance" || m["retry_after"] == nil {
		t.Errorf("Wartung app: %d %v", c, m)
	}
	if c, m := call(ml, "/api/v1/x"); c != 503 || m["code"] != nil {
		t.Errorf("Wartung andere API verändert: %d %v", c, m)
	}
}

func TestAppServerTime(t *testing.T) {
	e := newATEnv(t)
	h := &Handler{db: e.pool}
	rec := httptest.NewRecorder()
	h.AppServerAPI(rec, httptest.NewRequest("GET", "/api/app/v1/server", nil))
	var m struct {
		Time int64 `json:"time"`
	}
	e.must(json.Unmarshal(rec.Body.Bytes(), &m))
	if d := time.Now().Unix() - m.Time; d < -5 || d > 5 {
		t.Errorf("time weicht ab: %d", d)
	}
}
