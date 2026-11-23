//go:build integration

// Funktionen-Abgleich zwischen PDH-Terminal und PDH (Migration 201) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TerminalFn|KioskPDH' ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

// fnDevice legt ein Gerät mit bekanntem Geheimnis am Terminal-Benutzer an.
func (g *gtEnv) fnDevice(term string) (id, secret string) {
	g.e.t.Helper()
	secret, err := newDeviceSecret()
	g.e.must(err)
	id = g.e.id(`INSERT INTO terminal_devices (user_id, name, secret_sha256) VALUES ($1::uuid, 'Pi Fn', $2) RETURNING id::text`, term, hashDeviceSecret(secret))
	g.e.t.Cleanup(func() {
		_, _ = g.e.pool.Exec(context.Background(), `DELETE FROM terminal_devices WHERE id = $1::uuid`, id)
	})
	return id, secret
}

func fnPut(h *Handler, ip, secret, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/terminal/v1/functions", strings.NewReader(body))
	req.RemoteAddr = ip + ":4000"
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	h.TerminalFunctionsAPI(rec, req)
	return rec
}

type fnWireState struct {
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at"`
}

func fnDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]fnWireState {
	t.Helper()
	var out struct {
		Functions map[string]fnWireState `json:"functions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("keine JSON-Antwort: %v %s", err, rec.Body.String())
	}
	return out.Functions
}

func fnBody(key string, enabled bool, at time.Time) string {
	b, _ := json.Marshal(map[string]any{"functions": map[string]any{key: map[string]any{"enabled": enabled, "updated_at": fnWire(at)}}})
	return string(b)
}

// Regel je Schalter: strikt jünger gewinnt, Gleichstand PDH, Zukunft über 5 Minuten wird geklemmt.
func TestTerminalFnMergeRule(t *testing.T) {
	g := newGTEnv(t)
	h, term := g.h, g.termNormal
	now := time.Now().UTC().Truncate(time.Millisecond)
	get := func(key string) terminalFn {
		set, _, err := h.terminalFunctions(g.e.ctx, term)
		g.e.must(err)
		return set[key]
	}
	merge := func(in map[string]terminalFnIn) []string {
		ch, err := h.mergeTerminalFunctions(g.e.ctx, term, in, now)
		g.e.must(err)
		return ch
	}

	if f := get("ticket"); !f.Enabled || !f.At.IsZero() {
		t.Fatalf("Grundwert: %+v", f)
	}
	// Terminal jünger als „nie“: übernommen, so wie gesendet
	t1 := now.Add(-10 * time.Minute)
	if ch := merge(map[string]terminalFnIn{"ticket": {false, t1}}); len(ch) != 1 || ch[0] != "ticket" {
		t.Fatalf("Terminal jünger: geändert %v", ch)
	}
	if f := get("ticket"); f.Enabled || !f.At.Equal(t1) {
		t.Fatalf("nach Übernahme: %+v", f)
	}
	// Terminal älter: PDH behält (ohne Fehler, ohne Änderung)
	if ch := merge(map[string]terminalFnIn{"ticket": {true, t1.Add(-time.Minute)}}); len(ch) != 0 || get("ticket").Enabled {
		t.Errorf("älter wurde übernommen: %v", ch)
	}
	// Gleichstand: PDH gewinnt, der Wert des Terminals wird nicht angenommen
	if ch := merge(map[string]terminalFnIn{"ticket": {true, t1}}); len(ch) != 0 || get("ticket").Enabled {
		t.Errorf("Gleichstand: PDH muss gewinnen (%v, %+v)", ch, get("ticket"))
	}
	// PDH jünger: eine Änderung in PDH (jetzt) schlägt ein älteres Terminal
	if ch, err := h.setTerminalFunctions(g.e.ctx, term, map[string]bool{"ticket": true}); err != nil || len(ch) != 1 {
		t.Fatalf("PDH-Änderung: %v %v", ch, err)
	}
	pdhAt := get("ticket").At
	if !pdhAt.After(t1) {
		t.Fatalf("PDH-Zeitpunkt nicht jünger: %v", pdhAt)
	}
	if ch := merge(map[string]terminalFnIn{"ticket": {false, pdhAt.Add(-time.Second)}}); len(ch) != 0 || !get("ticket").Enabled {
		t.Errorf("PDH jünger: %v %+v", ch, get("ticket"))
	}
	// gleicher Wert in PDH gesetzt: Zeitpunkt bleibt (nur bei geändertem Wert NOW())
	if _, err := h.setTerminalFunctions(g.e.ctx, term, map[string]bool{"ticket": true}); err != nil || !get("ticket").At.Equal(pdhAt) {
		t.Errorf("Zeitpunkt bei gleichem Wert verändert: %v", get("ticket").At)
	}
	// Zukunft über 5 Minuten: auf die Serverzeit geklemmt; bis 5 Minuten bleibt sie
	if ch := merge(map[string]terminalFnIn{"hmi": {false, now.Add(time.Hour)}}); len(ch) != 1 {
		t.Fatalf("Zukunft: %v", ch)
	}
	if f := get("hmi"); f.Enabled || !f.At.Equal(now) {
		t.Errorf("Klemme: %+v, erwartet Serverzeit %v", f, now)
	}
	soon := now.Add(3 * time.Minute)
	merge(map[string]terminalFnIn{"ruesten": {false, soon}})
	if f := get("ruesten"); f.Enabled || !f.At.Equal(soon) {
		t.Errorf("3 Minuten Zukunft bleiben: %+v", f)
	}
	// vergangene Zeitpunkte gelten wie gesendet
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	merge(map[string]terminalFnIn{"wartungen": {false, old}})
	if f := get("wartungen"); f.Enabled || !f.At.Equal(old) {
		t.Errorf("alter Zeitpunkt: %+v", f)
	}
	// gemischtes Paket: ein Schlüssel übernommen, einer abgelehnt
	ch := merge(map[string]terminalFnIn{"stoerung": {false, now.Add(-time.Hour)}, "ticket": {false, pdhAt.Add(-time.Hour)}})
	if len(ch) != 1 || ch[0] != "stoerung" || !get("ticket").Enabled || get("stoerung").Enabled {
		t.Errorf("gemischtes Paket: %v", ch)
	}
	// Idempotenz
	if ch := merge(map[string]terminalFnIn{"stoerung": {false, now.Add(-time.Hour)}}); len(ch) != 0 {
		t.Errorf("Wiederholung änderte etwas: %v", ch)
	}
}

func TestTerminalFnPutAPI(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	g := newGTEnv(t)
	h, term := g.h, g.termNormal
	_, secret := g.fnDevice(term)
	ch, unsub := h.eventHub().subscribe(term)
	defer unsub()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)

	// Anmeldung: ohne oder mit falschem Geheimnis 401; Gerät gesperrt 401
	if rec := fnPut(h, "198.51.100.10", "", fnBody("ticket", false, at)); rec.Code != 401 {
		t.Errorf("ohne Bearer: %d", rec.Code)
	}
	if rec := fnPut(h, "198.51.100.11", "pdht_falsch", fnBody("ticket", false, at)); rec.Code != 401 || !strings.Contains(rec.Body.String(), msgDeviceInvalid) {
		t.Errorf("falsches Geheimnis: %d %s", rec.Code, rec.Body.String())
	}
	g.e.exec(`UPDATE terminal_devices SET revoked_at = NOW() WHERE secret_sha256 = $1`, hashDeviceSecret(secret))
	if rec := fnPut(h, "198.51.100.12", secret, fnBody("ticket", false, at)); rec.Code != 401 {
		t.Errorf("gesperrt: %d", rec.Code)
	}
	g.e.exec(`UPDATE terminal_devices SET revoked_at = NULL WHERE secret_sha256 = $1`, hashDeviceSecret(secret))
	select {
	case <-ch:
		t.Fatal("Ereignis trotz abgelehnter Anfragen")
	default:
	}
	// Fehlversuche zählen, danach 429
	for i := 0; i < 5; i++ {
		fnPut(h, "198.51.100.13", "pdht_falsch", "{}")
	}
	if rec := fnPut(h, "198.51.100.13", secret, fnBody("ticket", false, at)); rec.Code != 429 {
		t.Errorf("zu viele Fehlversuche: %d", rec.Code)
	}

	// Formfehler: nichts übernommen (alles oder nichts)
	for _, bad := range []string{`{}`, `kaputt`, `{"functions":{"ticket":{"enabled":"ja","updated_at":"2026-11-22T11:00:00Z"},"hmi":{"enabled":false,"updated_at":"` + fnWire(at) + `"}}}`} {
		if rec := fnPut(h, "198.51.100.14", secret, bad); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if set, _, _ := h.terminalFunctions(g.e.ctx, term); !set["hmi"].Enabled {
		t.Error("bei Formfehler wurde etwas übernommen")
	}
	if rec := fnPut(h, "198.51.100.14", secret, `{"functions":{"x":"`+strings.Repeat("a", 5000)+`"}}`); rec.Code != 413 {
		t.Errorf("zu groß: %d", rec.Code)
	}

	// Übernahme: Antwort mit vollständigem Stand, Ereignis an den Terminal-Benutzer
	rec := fnPut(h, "198.51.100.15", secret, fnBody("ticket", false, at))
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	st := fnDecode(t, rec)
	if len(st) != len(terminalFnDefs) || st["ticket"].Enabled || st["ticket"].UpdatedAt != fnWire(at) || !st["hmi"].Enabled || st["hmi"].UpdatedAt != "1970-01-01T00:00:00.000Z" {
		t.Errorf("Stand: %+v", st)
	}
	select {
	case ev := <-ch:
		if ev != "settings" {
			t.Errorf("Ereignis %q", ev)
		}
	default:
		t.Error("kein Ereignis settings nach Änderung")
	}
	// Wiederholung ist idempotent: keine Änderung, kein Ereignis; unbekannter Schlüssel wird übergangen
	rec = fnPut(h, "198.51.100.15", secret, `{"functions":{"ticket":{"enabled":false,"updated_at":"`+fnWire(at)+`"},"neu":{"enabled":true}}}`)
	if rec.Code != 200 || fnDecode(t, rec)["ticket"].UpdatedAt != fnWire(at) {
		t.Errorf("Wiederholung: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case ev := <-ch:
		t.Errorf("Ereignis ohne Änderung: %q", ev)
	default:
	}
	// Ablehnung ist kein Fehler: PDH führt den Wert mit seinem Zeitpunkt
	rec = fnPut(h, "198.51.100.15", secret, fnBody("ticket", true, at.Add(-time.Hour)))
	if st = fnDecode(t, rec); rec.Code != 200 || st["ticket"].Enabled || st["ticket"].UpdatedAt != fnWire(at) {
		t.Errorf("abgelehnt: %d %+v", rec.Code, st["ticket"])
	}
	// Zukunft: geklemmt, der gespeicherte Zeitpunkt kommt zurück
	before := time.Now().UTC()
	rec = fnPut(h, "198.51.100.15", secret, fnBody("hmi", false, time.Now().Add(time.Hour)))
	st = fnDecode(t, rec)
	got, err := time.Parse(time.RFC3339Nano, st["hmi"].UpdatedAt)
	if err != nil || got.Before(before.Truncate(time.Second)) || got.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("Klemme: %v %v", st["hmi"], err)
	}
}

func TestTerminalFnDeviceAndEvent(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	g := newGTEnv(t)
	h, term := g.h, g.termNormal
	devID, secret := g.fnDevice(term)

	dev := func() map[string]any {
		rec := deviceReq(h, "198.51.100.20", secret)
		if rec.Code != 200 {
			t.Fatalf("device: %d %s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		g.e.must(json.Unmarshal(rec.Body.Bytes(), &m))
		return m
	}
	m := dev()
	fn, _ := m["functions"].(map[string]any)
	th, _ := m["theme"].(map[string]any)
	if len(fn) != len(terminalFnDefs) || th["scheme"] != "dark" || th["bg"] != "#202020" || m["device_id"] != devID {
		t.Fatalf("device-Antwort: %v", m)
	}

	// Folienschau: altes Feld saver_slides bleibt, neu auch unter functions (mit Zeitpunkt)
	g.e.must(h.saveTerminalSaverSlides(g.e.ctx, term, true))
	ev, err := h.terminalSettingsEvent(httptest.NewRequest("GET", "/terminal/events", nil), term)
	g.e.must(err)
	if !strings.Contains(ev, `"saver_slides":true`) || !strings.Contains(ev, `"functions":{`) || !strings.Contains(ev, `"theme":{"`) {
		t.Errorf("Ereignis: %s", ev)
	}
	ss := dev()["functions"].(map[string]any)["saver_slides"].(map[string]any)
	if ss["enabled"] != true || ss["updated_at"] == "1970-01-01T00:00:00.000Z" {
		t.Errorf("saver_slides in functions: %v", ss)
	}
	if ls, err := h.terminalLoginSettings(g.e.ctx, term); err != nil || !ls.SaverSlides {
		t.Errorf("alte Spalte: %v %v", ls, err)
	}
	// Terminal schaltet die Folienschau am Gerät aus (jünger): die Spalte folgt, das alte Feld auch
	rec := fnPut(h, "198.51.100.21", secret, fnBody("saver_slides", false, time.Now().UTC()))
	if rec.Code != 200 || fnDecode(t, rec)["saver_slides"].Enabled {
		t.Fatalf("PUT saver: %d %s", rec.Code, rec.Body.String())
	}
	if ls, _ := h.terminalLoginSettings(g.e.ctx, term); ls.SaverSlides {
		t.Error("saver_slides-Spalte folgt dem Terminal nicht")
	}

	// Ereigniskanal mit Bearer-Geheimnis über authMiddleware: 200 text/event-stream mit functions
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/terminal/events", nil).WithContext(ctx)
	req.RemoteAddr = "198.51.100.22:1"
	req.Header.Set("Authorization", "Bearer "+secret)
	w := &fnSyncRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() {
		h.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("Sitzungsweg statt Bearer") })).ServeHTTP(w, req)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(w.body(), `"functions"`) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if w.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(w.body(), "event: settings") || !strings.Contains(w.body(), `"functions"`) {
		t.Errorf("Bearer-Ereigniskanal: %q %q", w.Header().Get("Content-Type"), w.body())
	}
	// falsches Geheimnis: 401, kein Strom
	req = httptest.NewRequest("GET", "/terminal/events", nil)
	req.RemoteAddr = "198.51.100.23:1"
	req.Header.Set("Authorization", "Bearer pdht_falsch")
	rr := httptest.NewRecorder()
	h.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rr, req)
	if rr.Code != 401 || strings.Contains(rr.Header().Get("Content-Type"), "event-stream") {
		t.Errorf("falsches Geheimnis am Kanal: %d", rr.Code)
	}
}

// Ausgeschaltete Funktionen: Kachel weg, Routen antworten 404 (GET und POST).
func TestTerminalFnEnforcement(t *testing.T) {
	g := newGTEnv(t)
	c := g.cookie(g.termNormal, g.devN)
	g.e.must(func() error { _, err := g.h.setTerminalFunctions(g.e.ctx, g.termNormal, map[string]bool{}); return err }())
	g.e.exec(`UPDATE terminal_settings SET fn_stoerung = true, fn_ticket = true, fn_ruesten = true, fn_stillstand = true, fn_hmi = true, fn_wartungen = true WHERE user_id = $1::uuid`, g.termNormal)

	neu := func() string {
		rec := g.do("GET", "/terminal/neu", c, nil)
		if rec.Code != 200 {
			t.Fatalf("neu: %d", rec.Code)
		}
		return rec.Body.String()
	}
	b := neu()
	for _, want := range []string{`href="/terminal/stoerung"`, `href="/terminal/ticket"`, `class="t-stop`} {
		if !strings.Contains(b, want) {
			t.Fatalf("Grundzustand: %q fehlt", want)
		}
	}
	type route struct{ method, path, key string }
	routes := []route{
		{"GET", "/terminal/stoerung", "stoerung"}, {"POST", "/terminal/stoerung", "stoerung"},
		{"GET", "/terminal/ticket", "ticket"}, {"POST", "/terminal/ticket", "ticket"},
		{"GET", "/terminal/rusten", "ruesten"}, {"POST", "/terminal/rusten/start", "ruesten"}, {"POST", "/terminal/rusten/finish", "ruesten"}, {"POST", "/terminal/rusten/add", "ruesten"},
		{"GET", "/terminal/stillstand", "stillstand"}, {"POST", "/terminal/stillstand/start", "stillstand"}, {"POST", "/terminal/stillstand/finish", "stillstand"},
		{"GET", "/terminal/hmi", "hmi"}, {"GET", "/terminal/hmi/" + g.infraA + "/" + g.infraA, "hmi"},
		{"GET", "/terminal/wartung", "wartungen"},
	}
	for _, r := range routes {
		if rec := g.do(r.method, r.path, c, nil); rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), msgTerminalFnOff) {
			t.Errorf("%s %s eingeschaltet: gesperrt", r.method, r.path)
		}
	}
	for _, r := range routes {
		d, _ := terminalFnDefByKey(r.key)
		g.e.exec(`UPDATE terminal_settings SET `+d.Col+` = false WHERE user_id = $1::uuid`, g.termNormal)
		if rec := g.do(r.method, r.path, c, nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), msgTerminalFnOff) {
			t.Errorf("%s %s ausgeschaltet: %d %q", r.method, r.path, rec.Code, rec.Body.String())
		}
	}
	// alles aus: keine Kachel
	b = neu()
	for _, bad := range []string{`href="/terminal/stoerung"`, `href="/terminal/ticket"`, `class="t-stop`, `href="/terminal/rusten"`, `href="/terminal/hmi"`, `href="/terminal/wartung"`} {
		if strings.Contains(b, bad) {
			t.Errorf("ausgeschaltete Funktion zeigt %q", bad)
		}
	}
	// nur Ticket wieder an: Kachel Ticket da, Störung weiter weg
	g.e.exec(`UPDATE terminal_settings SET fn_ticket = true WHERE user_id = $1::uuid`, g.termNormal)
	b = neu()
	if !strings.Contains(b, `href="/terminal/ticket"`) || strings.Contains(b, `href="/terminal/stoerung"`) {
		t.Error("Einzelschalter wirkt nicht")
	}
	// Änderung in PDH setzt den Zeitpunkt und löst (über notify) das Ereignis aus
	ch, unsub := g.h.eventHub().subscribe(g.termNormal)
	defer unsub()
	changed, err := g.h.setTerminalFunctions(g.e.ctx, g.termNormal, map[string]bool{"stoerung": true})
	g.e.must(err)
	g.h.notifyTerminalSettings(g.termNormal)
	if len(changed) != 1 || changed[0] != "stoerung" {
		t.Errorf("Änderung: %v", changed)
	}
	if set, _, _ := g.h.terminalFunctions(g.e.ctx, g.termNormal); set["stoerung"].At.IsZero() {
		t.Error("PDH-Änderung ohne Zeitpunkt")
	}
	if ev := <-ch; ev != "settings" {
		t.Errorf("Ereignis %q", ev)
	}
}

// Die Liste der Kiosk-Terminals zeigt gekoppelte PDH-Terminals mit Typ, Version, Online-Status und Kopplung.
func TestKioskPDHTerminals(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	g := newGTEnv(t)
	h := g.h
	devID, secret := g.fnDevice(g.termNormal)
	rows, err := h.loadPDHTerminals(g.e.ctx)
	g.e.must(err)
	find := func() *pdhTerminalView {
		for i := range rows {
			if rows[i].DeviceID == devID {
				return &rows[i]
			}
		}
		return nil
	}
	v := find()
	if v == nil || v.Online || v.Version != "unbekannt" || v.Type != "PDH-Terminal" || v.Revoked || v.Link != "/users/"+g.termNormal+"?tab=master" {
		t.Fatalf("vor der ersten Meldung: %+v", v)
	}
	req := httptest.NewRequest("GET", "/api/terminal/v1/device", nil)
	req.RemoteAddr = "198.51.100.30:1"
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Terminal-Version", "0.4.3")
	rec := httptest.NewRecorder()
	h.TerminalDeviceAPI(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	rows, err = h.loadPDHTerminals(g.e.ctx)
	g.e.must(err)
	if v = find(); v == nil || !v.Online || v.Version != "0.4.3" {
		t.Fatalf("nach der Meldung: %+v", v)
	}
	g.e.exec(`UPDATE terminal_devices SET last_seen_at = NOW() - interval '10 minutes' WHERE id = $1::uuid`, devID)
	rows, _ = h.loadPDHTerminals(g.e.ctx)
	if v = find(); v.Online {
		t.Error("nach 10 Minuten noch online")
	}
	g.e.exec(`UPDATE terminal_devices SET revoked_at = NOW() WHERE id = $1::uuid`, devID)
	rows, _ = h.loadPDHTerminals(g.e.ctx)
	if v = find(); !v.Revoked || v.Online {
		t.Errorf("gesperrt: %+v", v)
	}

	// Seite: PDH-Terminal und die vorhandenen Kiosk-Einträge nebeneinander
	pin := strings.Repeat("ab", 32)
	d := KioskTerminalsPageData{
		Terminals:    []kioskTerminalView{{ID: "6f1c0b8e-2d7a-4c1e-9f3b-0a1b2c3d4e5f", Name: "Terminal Presse 2", BaseURL: "https://10.1.2.3:8443", Fingerprint: pin, FingerprintColons: kioskColons(pin), HasKey: true, Active: true}},
		PDHTerminals: []pdhTerminalView{{DeviceID: devID, Name: "Pi Fn", UserName: "Kasse", Type: "PDH-Terminal", Version: "0.4.3", Online: true, Paired: "22.11.2026 10:00", LastSeen: "22.11.2026 10:05", Link: "/users/" + g.termNormal + "?tab=master"}},
	}
	out := renderPage(t, loadTestTemplates(t), "kiosk_terminals", d)
	for _, want := range []string{"PDH-Terminals", "Pi Fn", "Version 0.4.3", "online", "Gekoppelt am 22.11.2026", `href="/users/` + g.termNormal + `?tab=master"`, "Terminal Presse 2", "Verbindung prüfen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
}

// Terminalseiten tragen das gewählte Farbschema.
func TestTerminalFnScheme(t *testing.T) {
	g := newGTEnv(t)
	c := g.cookie(g.termNormal, g.devN)
	g.e.exec(`INSERT INTO terminal_settings (user_id, theme_scheme) VALUES ($1::uuid, 'dark') ON CONFLICT (user_id) DO UPDATE SET theme_scheme = 'dark'`, g.termNormal)
	if b := g.do("GET", "/terminal/neu", c, nil).Body.String(); !strings.Contains(b, `<html lang="de" data-theme="dark">`) {
		t.Error("nicht dunkel")
	}
	g.e.exec(`UPDATE terminal_settings SET theme_scheme = 'light' WHERE user_id = $1::uuid`, g.termNormal)
	if b := g.do("GET", "/terminal/neu", c, nil).Body.String(); !strings.Contains(b, `<html lang="de" data-theme="light">`) {
		t.Error("hell nicht gesetzt")
	}
	if _, scheme, _ := g.h.terminalFunctions(g.e.ctx, g.termNormal); scheme != "light" {
		t.Errorf("Schema %q", scheme)
	}
}

// fnSyncRecorder: ResponseRecorder, dessen Inhalt während des Schreibens sicher gelesen werden kann.
type fnSyncRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (s *fnSyncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ResponseRecorder.Write(b)
}
func (s *fnSyncRecorder) WriteString(x string) (int, error) { return s.Write([]byte(x)) }
func (s *fnSyncRecorder) Flush()                            {}
func (s *fnSyncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Body.String()
}

// Benutzerdetail (PDH-Seite des Terminals): sieben Schalter und Farbschema, Zeitpunkt nur bei Änderung, Ereignis.
func TestTerminalFnUserDetailForm(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("fnmgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	term := e.user("fnterm", "viewer", true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Get("/users/{id}", h.UserDetailPage)
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := withUser(httptest.NewRequest(method, target, strings.NewReader(body)), &users.User{ID: mgr, Role: users.Role("admin")})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	page := do("GET", "/users/"+term+"?tab=master", "").Body.String()
	for _, want := range []string{`name="terminal_fn_stoerung" checked`, `name="terminal_fn_ticket" checked`, `name="terminal_fn_ruesten" checked`, `name="terminal_fn_stillstand" checked`, `name="terminal_fn_hmi" checked`, `name="terminal_fn_wartungen" checked`, `name="terminal_saver_slides"`, `name="terminal_scheme"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Formular ohne %q", want)
		}
	}
	ch, unsub := h.eventHub().subscribe(term)
	defer unsub()
	// Ticket und Wartungen aus, Schema hell
	do("POST", "/users/"+term+"/master", "terminal_fn_sent=1&terminal_fn_stoerung=on&terminal_fn_ruesten=on&terminal_fn_stillstand=on&terminal_fn_hmi=on&terminal_scheme=light")
	set, scheme, err := h.terminalFunctions(e.ctx, term)
	e.must(err)
	if set["ticket"].Enabled || set["wartungen"].Enabled || !set["stoerung"].Enabled || scheme != "light" {
		t.Fatalf("Formular nicht übernommen: %+v %s", set, scheme)
	}
	if set["ticket"].At.IsZero() || set["wartungen"].At.IsZero() || !set["stoerung"].At.IsZero() {
		t.Errorf("Zeitpunkte nur bei geänderten Schaltern: ticket %v stoerung %v", set["ticket"].At, set["stoerung"].At)
	}
	select {
	case ev := <-ch:
		if ev != "settings" {
			t.Errorf("Ereignis %q", ev)
		}
	default:
		t.Error("kein Ereignis nach der Änderung")
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT COUNT(*) FROM record_history WHERE ref_type='user' AND ref_id=$1 AND message LIKE 'Terminal-Funktion Ticket%'`, term).Scan(&n))
	if n != 1 {
		t.Errorf("Verlaufseintrag: %d", n)
	}
	// dasselbe noch einmal: nichts ändert sich, keine neuen Zeitpunkte
	at := set["ticket"].At
	do("POST", "/users/"+term+"/master", "terminal_fn_sent=1&terminal_fn_stoerung=on&terminal_fn_ruesten=on&terminal_fn_stillstand=on&terminal_fn_hmi=on&terminal_scheme=light")
	if set2, _, _ := h.terminalFunctions(e.ctx, term); !set2["ticket"].At.Equal(at) {
		t.Error("Zeitpunkt bei unverändertem Wert neu gesetzt")
	}
	// ohne Marker ändert ein Formular nichts
	do("POST", "/users/"+term+"/master", "terminal_scheme=dark")
	if _, scheme, _ = h.terminalFunctions(e.ctx, term); scheme != "light" {
		t.Error("Formular ohne Marker änderte das Schema")
	}
}

// Verlorene Änderung: Ändert das Terminal einen Schalter, während die Seite offen ist, darf das Speichern der
// Seite (auch ein Speichern, das nur etwas anderes ändert) ihn nicht zurücksetzen.
func TestTerminalFnFormKeepsConcurrentChange(t *testing.T) {
	e := newATEnv(t)
	mgr := e.user("lumgr", "admin", false)
	e.grant(mgr, "system.manage_users", "", true)
	term := e.user("luterm", "viewer", true)
	e.warm()
	h := &Handler{db: e.pool, rbac: e.rb, users: users.NewService(users.NewRepository(e.pool), "test-secret", 1), jwtSecret: "test-geheim", tmpl: loadTestTemplates(t)}
	r := chi.NewRouter()
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	wd, err := os.Getwd()
	e.must(err)
	e.must(os.Chdir("../.."))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	post := func(body string) {
		req := withUser(httptest.NewRequest("POST", "/users/"+term+"/master", strings.NewReader(body)), &users.User{ID: mgr, Role: users.Role("admin")})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	// Die Seite wurde mit allen Schaltern an (und Folienschau aus) geöffnet ...
	const page = "terminal_fn_sent=1&terminal_saver_slides_sent=1" +
		"&orig_stoerung=1&orig_ticket=1&orig_ruesten=1&orig_stillstand=1&orig_hmi=1&orig_wartungen=1&orig_saver_slides=0" +
		"&terminal_fn_stoerung=on&terminal_fn_ticket=on&terminal_fn_ruesten=on&terminal_fn_stillstand=on&terminal_fn_hmi=on&terminal_fn_wartungen=on"
	post("terminal_fn_sent=1") // legt die Zeile an (alle Schalter ohne orig: gelten als geändert, hier aus)
	e.exec(`UPDATE terminal_settings SET fn_stoerung = true, fn_ticket = true, fn_ruesten = true, fn_stillstand = true, fn_hmi = true, fn_wartungen = true, fn_stoerung_at = NULL, fn_ticket_at = NULL, fn_ruesten_at = NULL, fn_stillstand_at = NULL, fn_hmi_at = NULL, fn_wartungen_at = NULL WHERE user_id = $1::uuid`, term)
	// ... das Terminal schaltet Ticket aus und die Folienschau an ...
	now := time.Now().UTC()
	_, err = h.mergeTerminalFunctions(e.ctx, term, map[string]terminalFnIn{"ticket": {false, now.Add(-time.Second)}, "saver_slides": {true, now.Add(-time.Second)}}, now)
	e.must(err)
	// ... die Person speichert die Seite, ohne diese Schalter anzufassen, und schaltet nur HMI aus
	post(strings.Replace(page, "&terminal_fn_hmi=on", "", 1))
	set, _, err := h.terminalFunctions(e.ctx, term)
	e.must(err)
	if set["ticket"].Enabled || !set["saver_slides"].Enabled {
		t.Errorf("Änderung des Terminals überschrieben: ticket %v saver %v", set["ticket"].Enabled, set["saver_slides"].Enabled)
	}
	if set["hmi"].Enabled || set["hmi"].At.IsZero() {
		t.Errorf("Änderung der Person fehlt: %+v", set["hmi"])
	}
	// ein Speichern ohne jede Änderung (nur Marker und Stände) ändert nichts
	ticketAt := set["ticket"].At
	post(page)
	if s2, _, _ := h.terminalFunctions(e.ctx, term); s2["ticket"].Enabled || !s2["ticket"].At.Equal(ticketAt) || !s2["stoerung"].At.IsZero() {
		t.Errorf("unverändertes Speichern: %+v", s2["ticket"])
	}
	// alt/neu stammen aus der Transaktion
	cs, err := h.setTerminalFunctionsDetail(e.ctx, term, map[string]bool{"ticket": true})
	if err != nil || len(cs) != 1 || cs[0].Key != "ticket" || cs[0].Old || !cs[0].New {
		t.Errorf("fnChange: %+v %v", cs, err)
	}
}

// Der Bearer-Ereigniskanal endet, sobald das Gerät gesperrt wird (Prüfung bei jedem Ping).
func TestTerminalFnEventStreamClosesWhenRevoked(t *testing.T) {
	terminalFailLimit = newFailLimiter(5, 5*time.Minute)
	old := terminalEventPing
	terminalEventPing = 50 * time.Millisecond
	t.Cleanup(func() { terminalEventPing = old })
	g := newGTEnv(t)
	devID, secret := g.fnDevice(g.termNormal)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/terminal/events", nil).WithContext(ctx)
	req.RemoteAddr = "198.51.100.40:1"
	req.Header.Set("Authorization", "Bearer "+secret)
	w := &fnSyncRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() { g.h.TerminalEventsBearer(w, req); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(w.body(), "event: settings") {
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("Strom endete ohne Sperre")
	case <-time.After(200 * time.Millisecond):
	}
	g.e.exec(`UPDATE terminal_devices SET revoked_at = NOW() WHERE id = $1::uuid`, devID)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Strom läuft nach der Sperre weiter")
	}
}
