package web

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pdh"
	"pdh/internal/core/users"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Reiter „ESPPDH“: ESPHome hält sich selbst aktuell (esppdh_settings.go, esppdh_update.go).
// Schalter, „Jetzt prüfen“ und „Zurück zur vorigen Version“ prüfen dasselbe Recht wie die
// Einrichtung (canServerConfig); am Spiegel sind „Jetzt prüfen“ und „Zurück“ erlaubt, der
// Schalter nicht. Die Datenbank-Tests brauchen die Labor-Datenbank.

const (
	esppdhPathAuto     = "/admin/server-config/esppdh/auto"
	esppdhPathCheck    = "/admin/server-config/esppdh/check"
	esppdhPathRollback = "/admin/server-config/esppdh/rollback"
)

// esppdhConfigHandler: Handler eines Prüflauf-Tests mit Vorlagen und dem Recht system.server_config.
func esppdhConfigHandler(t *testing.T, u *esppdhUpdT, tmpl *template.Template) *Handler {
	t.Helper()
	u.h.tmpl = tmpl
	u.h.hasPermHook = func(_ *http.Request, p string) bool { return p == "system.server_config" }
	return u.h
}

// esppdhPost: POST als Administrator.
func esppdhPost(fn http.HandlerFunc, path string, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	fn(w, esppdhReq(http.MethodPost, path, users.RoleAdmin, form))
	return w
}

func esppdhGetPanel(h *Handler) string {
	w := httptest.NewRecorder()
	h.EsppdhPanelWeb(w, esppdhReq(http.MethodGet, "/admin/server-config/esppdh", users.RoleAdmin, nil))
	return w.Body.String()
}

// esppdhWaitIdle wartet, bis kein Prüflauf mehr läuft.
func esppdhWaitIdle(t *testing.T, h *Handler) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		h.esppdhUpd.mu.Lock()
		running := h.esppdhUpd.running
		h.esppdhUpd.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("die Prüfung endet nicht")
}

// ── ohne Datenbank ───────────────────────────────────────────────────────

// Die drei neuen Routen prüfen canServerConfig (403), bevor sie den Agenten fragen.
func TestEsppdhNewRoutesNeedServerConfig(t *testing.T) {
	esppdhTestEnv(t, false)
	srv, ag := esppdhFakeAgent(t, esppdhRunning, esppdhIdle)
	for _, c := range []struct {
		name string
		perm bool
		role users.Role
	}{{"ohne Recht", false, users.RoleAdmin}, {"Recht, aber kein Administrator", true, users.RoleManager}} {
		h := esppdhHandler(t, c.perm, srv.URL)
		for path, fn := range map[string]http.HandlerFunc{esppdhPathAuto: h.EsppdhAutoWeb, esppdhPathCheck: h.EsppdhCheckWeb, esppdhPathRollback: h.EsppdhRollbackWeb} {
			w := httptest.NewRecorder()
			fn(w, esppdhReq(http.MethodPost, path, c.role, url.Values{"on": {"1"}}))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: Status %d", c.name, path, w.Code)
			}
		}
	}
	if calls := ag.log(); len(calls) != 0 {
		t.Errorf("Agent ohne Recht gefragt: %v", calls)
	}
}

// Schalter ohne Recht 403, am Spiegel 409 mit der Sperrmeldung; die Spiegel-Sperre lässt „Jetzt
// prüfen“ und „Zurück“ durch, den Schalter und ähnliche Pfade nicht.
func TestEsppdhAutoSwitchRefused(t *testing.T) {
	esppdhTestEnv(t, false)
	h := esppdhHandler(t, true, "")
	h.SetMirror(MirrorInfo{On: true})
	w := esppdhPost(h.EsppdhAutoWeb, esppdhPathAuto, url.Values{"on": {"0"}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), mirrorReadOnlyMsg) {
		t.Errorf("Schalter am Spiegel: %d %q", w.Code, w.Body.String())
	}
	hd, passed := mirrorTestHandler(true)
	for path, want := range map[string]int{esppdhPathCheck: http.StatusOK, esppdhPathRollback: http.StatusOK,
		esppdhPathAuto: http.StatusConflict, esppdhPathCheck + "x": http.StatusConflict, esppdhPathRollback + "/x": http.StatusConflict} {
		if rec := mirrorServe(hd, http.MethodPost, path, nil); rec.Code != want {
			t.Errorf("POST %s am Spiegel: %d, erwartet %d", path, rec.Code, want)
		}
	}
	if *passed != 2 {
		t.Errorf("durchgereicht: %d, erwartet 2", *passed)
	}
}

// Fehlermeldung des Zustands steht maskiert in der Karte: kein <script> aus dem Verlauf.
func TestEsppdhPanelShowsFailure(t *testing.T) {
	p := esppdhPanel{AgentReady: true, Auto: true, Pinned: "2026.10.2", Env: esppdh.EnvState{Installed: true, Version: "2026.10.2", FreeMB: -1},
		Update: esppdh.UpdateState{CheckedAt: time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC), Result: "ESPHome 2026.11.1 hat die Probe nicht bestanden – es bleibt bei 2026.10.2",
			FailedVersion: "2026.11.1", FailedPDH: pdh.Version(), FailedLog: "Gerät „x“:\n<script>alert(1)</script>"}}
	out := renderSNMPTemplate(t, "esppdh-panel", p)
	for _, want := range []string{"2026.11.1", "<details", "<pre", "&lt;script&gt;alert(1)&lt;/script&gt;", "Zuletzt geprüft", "hat die Probe nicht bestanden",
		"Jetzt prüfen", `hx-post="/admin/server-config/esppdh/check"`, `hx-post="/admin/server-config/esppdh/auto"`, "ESPHome selbst aktuell halten", "Mindestversion"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>alert") {
		t.Error("Verlauf nicht maskiert")
	}
	if strings.Contains(out, "every 2s") || strings.Contains(out, "Zurück zur vorigen Version") {
		t.Error("Abfrage ohne Lauf oder Zurück-Knopf ohne vorige Version")
	}
	// ohne gescheiterte Version: kein Hinweis
	p.Update.FailedVersion, p.Update.FailedLog = "", ""
	if out = renderSNMPTemplate(t, "esppdh-panel", p); strings.Contains(out, "<details") {
		t.Error("Verlauf ohne gescheiterte Version")
	}
	// während der Prüfung: Hinweis und Abfrage alle 2 s
	p.Checking = true
	if out = renderSNMPTemplate(t, "esppdh-panel", p); !strings.Contains(out, "Prüfung läuft") || !strings.Contains(out, `hx-trigger="every 2s"`) {
		t.Errorf("Prüfung läuft:\n%s", out)
	}
}

// „veraltet“ heißt: älter als die Mindestversion.
func TestEsppdhPanelOutdatedMeansBelowMinimum(t *testing.T) {
	p := esppdhPanel{AgentReady: true, Pinned: "2026.10.2", Env: esppdh.EnvState{Installed: true, Version: "2026.9.1", Outdated: true, FreeMB: -1}}
	out := renderSNMPTemplate(t, "esppdh-panel", p)
	for _, want := range []string{"veraltet", "älter als die Mindestversion", "ESPHome 2026.10.2"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	if strings.Contains(out, "festgelegt") {
		t.Error("alter Wortlaut „festgelegt“ steht noch da")
	}
}

// Knopf „Zurück“: nur mit vorige Version; bei der Grundumgebung mit eigener Beschriftung.
func TestEsppdhPanelRollbackButton(t *testing.T) {
	base := esppdhPanel{AgentReady: true, Pinned: "2026.10.2", Env: esppdh.EnvState{Installed: true, Version: "2026.11.1", FreeMB: -1}}
	if out := renderSNMPTemplate(t, "esppdh-panel", base); strings.Contains(out, "Zurück zur vorigen Version") {
		t.Error("Knopf ohne vorige Version")
	}
	base.Previous = "2026.10.2"
	out := renderSNMPTemplate(t, "esppdh-panel", base)
	for _, want := range []string{"Zurück zur vorigen Version (2026.10.2)", `hx-post="/admin/server-config/esppdh/rollback"`, "hx-confirm="} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt", want)
		}
	}
	base.Previous = "base"
	if out = renderSNMPTemplate(t, "esppdh-panel", base); !strings.Contains(out, "Zurück zur vorigen Version (Grundumgebung)") {
		t.Errorf("Grundumgebung:\n%s", out)
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

// Schalter speichert esppdh.autoupdate; nur "0" heißt aus.
func TestEsppdhAutoSwitch(t *testing.T) {
	esppdhTestEnv(t, false)
	pool := connTestPool(t)
	h := esppdhHandler(t, true, "")
	h.db = pool
	esppdhKeepSetting(t, h, esppdhSetAuto, "1")
	ctx := context.Background()
	for _, c := range []struct {
		on, want string
		auto     bool
	}{{"0", "0", false}, {"1", "1", true}} {
		w := esppdhPost(h.EsppdhAutoWeb, esppdhPathAuto, url.Values{"on": {c.on}})
		var v string
		_ = pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, esppdhSetAuto).Scan(&v)
		if w.Code != http.StatusOK || v != c.want || h.esppdhAutoUpdate(ctx) != c.auto || !strings.Contains(w.Body.String(), `id="esppdh-panel"`) {
			t.Errorf("on=%s: %d, gespeichert %q, auto=%v\n%s", c.on, w.Code, v, h.esppdhAutoUpdate(ctx), w.Body.String())
		}
	}
	// unbekannter Wert ändert nichts
	if w := esppdhPost(h.EsppdhAutoWeb, esppdhPathAuto, url.Values{"on": {"x"}}); w.Code != http.StatusBadRequest {
		t.Errorf("on=x: %d", w.Code)
	}
}

// „Jetzt prüfen“ antwortet sofort mit „Prüfung läuft“ (Karte fragt alle 2 s nach), startet den Lauf im
// Hintergrund (nicht an die Anfrage gebunden) und kein zweites Mal; am Spiegel erlaubt.
func TestEsppdhCheckNow(t *testing.T) {
	tmpl := loadTestTemplates(t) // vor der Umgebung: sie wechselt den Arbeitsordner
	u := newEsppdhUpd(t)
	h := esppdhConfigHandler(t, u, tmpl)
	release := make(chan struct{})
	u.set(func() {
		u.pypiFn = func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"releases":{}}`))
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	req := esppdhReq(http.MethodPost, esppdhPathCheck, users.RoleAdmin, nil).WithContext(context.WithValue(ctx, "user", &users.User{ID: "u-esppdh", Role: users.RoleAdmin}))
	start := time.Now()
	w := httptest.NewRecorder()
	h.EsppdhCheckWeb(w, req)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Start wartet %s", took)
	}
	cancel() // die Anfrage ist zu Ende – der Lauf darf davon nicht abbrechen
	if out := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(out, "Prüfung läuft") || !strings.Contains(out, `hx-trigger="every 2s"`) {
		t.Fatalf("nach dem Klick %d:\n%s", w.Code, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for u.hits() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// zweiter Klick: kein zweiter Lauf
	w = esppdhPost(h.EsppdhCheckWeb, esppdhPathCheck, nil)
	if !strings.Contains(w.Body.String(), esppdhMsgUpdBusy) || !strings.Contains(w.Body.String(), `hx-trigger="every 2s"`) {
		t.Errorf("zweiter Klick:\n%s", w.Body.String())
	}
	if out := esppdhGetPanel(h); !strings.Contains(out, `hx-trigger="every 2s"`) {
		t.Errorf("Abfrage während des Laufs hört auf:\n%s", out)
	}
	time.Sleep(50 * time.Millisecond)
	if n := u.hits(); n != 1 {
		t.Errorf("PyPI %d-mal gefragt, erwartet 1", n)
	}
	close(release)
	esppdhWaitIdle(t, h)
	out := esppdhGetPanel(h)
	if strings.Contains(out, "every 2s") || !strings.Contains(out, "Zuletzt geprüft") || !strings.Contains(out, "PyPI nennt keine Ausgaben") {
		t.Errorf("nach dem Lauf:\n%s", out)
	}
	if st := esppdh.LoadUpdateState(); st.CheckedAt.IsZero() || !strings.Contains(st.Result, "PyPI nennt keine Ausgaben") {
		t.Errorf("Zustand: %+v", st)
	}
	if n := u.hits(); n != 1 {
		t.Errorf("PyPI nach dem Lauf %d-mal gefragt, erwartet 1", n)
	}

	// am Spiegel erlaubt (Datenbank nur lesend – der Lauf schreibt nur die Zustandsdatei)
	h.SetMirror(MirrorInfo{On: true})
	w = esppdhPost(h.EsppdhCheckWeb, esppdhPathCheck, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Prüfung läuft") {
		t.Errorf("am Spiegel: %d\n%s", w.Code, w.Body.String())
	}
	u.set(func() { u.pypiFn = nil })
	esppdhWaitIdle(t, h)
}

// „Jetzt prüfen“ nennt den Grund, wenn nichts geprüft werden kann (z. B. ESPPDH aus) – ohne Lauf.
func TestEsppdhCheckNowBlocked(t *testing.T) {
	tmpl := loadTestTemplates(t)
	u := newEsppdhUpd(t)
	h := esppdhConfigHandler(t, u, tmpl)
	esppdhKeepSetting(t, h, esppdhSetEnabled, "0")
	w := esppdhPost(h.EsppdhCheckWeb, esppdhPathCheck, nil)
	if !strings.Contains(w.Body.String(), esppdhMsgOff) || strings.Contains(w.Body.String(), "every 2s") || u.hits() != 0 {
		t.Errorf("ESPPDH aus: %d, PyPI %d\n%s", w.Code, u.hits(), w.Body.String())
	}
}

// „Zurück zur vorigen Version“: der Agent schaltet zurück; die verlassene Version gilt danach als
// gescheitert (Zustandsdatei), die laufende ist die vorige.
func TestEsppdhRollback(t *testing.T) {
	tmpl := loadTestTemplates(t)
	u := newEsppdhUpd(t)
	h := esppdhConfigHandler(t, u, tmpl)
	u.set(func() { u.previous = esppdhUpdOld })
	old := esppdhCheckEnv
	t.Cleanup(func() { esppdhCheckEnv = old })
	esppdhCheckEnv = func(context.Context) esppdh.EnvState {
		v := esppdhUpdNew
		if esppdhCount(u.events(), "rollback") > 0 {
			v = esppdhUpdOld
		}
		return esppdh.EnvState{Installed: true, Version: v, FreeMB: -1}
	}
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{Installed: esppdhUpdNew, Result: "Auf ESPHome " + esppdhUpdNew + " umgestellt"}); err != nil {
		t.Fatal(err)
	}
	if out := esppdhGetPanel(h); !strings.Contains(out, "Zurück zur vorigen Version ("+esppdhUpdOld+")") {
		t.Fatalf("Knopf fehlt:\n%s", out)
	}
	w := esppdhPost(h.EsppdhRollbackWeb, esppdhPathRollback, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Zurück: %d %s", w.Code, w.Body.String())
	}
	if n := esppdhCount(u.events(), "rollback"); n != 1 {
		t.Errorf("Agent bekam %d-mal „rollback“: %v", n, u.events())
	}
	st := esppdh.LoadUpdateState()
	if st.Installed != esppdhUpdOld || st.FailedVersion != esppdhUpdNew || st.FailedPDH != pdh.Version() || !strings.Contains(st.Result, "Zurück auf ESPHome "+esppdhUpdOld) {
		t.Errorf("Zustand: %+v", st)
	}
	out := w.Body.String()
	if !strings.Contains(out, "Zurück auf ESPHome "+esppdhUpdOld) || !strings.Contains(out, esppdhUpdNew) {
		t.Errorf("Karte nach dem Zurückstellen:\n%s", out)
	}
	// keine vorige Umgebung mehr: der Knopf fehlt
	u.set(func() { u.previous = "" })
	if out := esppdhGetPanel(h); strings.Contains(out, "Zurück zur vorigen Version") {
		t.Errorf("Knopf ohne vorige Version:\n%s", out)
	}
	// Grundumgebung als vorige
	u.set(func() { u.previous = "base" })
	if out := esppdhGetPanel(h); !strings.Contains(out, "Zurück zur vorigen Version (Grundumgebung)") {
		t.Errorf("Grundumgebung:\n%s", out)
	}
}

// Läuft gerade eine Prüfung, wird nicht zurückgestellt.
func TestEsppdhRollbackWhileChecking(t *testing.T) {
	tmpl := loadTestTemplates(t)
	u := newEsppdhUpd(t)
	h := esppdhConfigHandler(t, u, tmpl)
	u.set(func() { u.previous = esppdhUpdOld })
	h.esppdhUpd.mu.Lock()
	h.esppdhUpd.running = true
	h.esppdhUpd.mu.Unlock()
	w := esppdhPost(h.EsppdhRollbackWeb, esppdhPathRollback, nil)
	if esppdhCount(u.events(), "rollback") != 0 || !strings.Contains(w.Body.String(), "Prüfung läuft") {
		t.Errorf("Zurück während der Prüfung: %v\n%s", u.events(), w.Body.String())
	}
	h.esppdhUpd.mu.Lock()
	h.esppdhUpd.running = false
	h.esppdhUpd.mu.Unlock()
}

// Geräteseite: Hinweis, wenn die Firmware mit einer älteren ESPHome-Version gebaut wurde als die
// auf dem Server (Ton „info“); gleiche Version oder leerer Zustand: kein Hinweis.
func TestDeviceOlderFirmwareHint(t *testing.T) {
	e, fb, _ := newEsppdhBuildEnv(t)
	e.start(t)
	id := e.buildable(t, "oh-"+e.sfx)
	e.built(t, fb, id, "oh") // gebaut mit ESPHome 2099.1.0
	page := func() string { return esppdhDo(e.h.EsppdhDevicePage, "GET", "/x", id, esppdhUser, nil).Body.String() }
	const hint = "Neu bauen und flashen ist möglich, aber nicht nötig."

	if err := esppdh.SaveUpdateState(esppdh.UpdateState{}); err != nil {
		t.Fatal(err)
	}
	if p := page(); strings.Contains(p, hint) {
		t.Error("Hinweis bei leerem Zustand")
	}
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{Installed: "2099.1.0"}); err != nil {
		t.Fatal(err)
	}
	if p := page(); strings.Contains(p, hint) {
		t.Error("Hinweis bei gleicher Version")
	}
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{Installed: "2098.12.0"}); err != nil {
		t.Fatal(err)
	}
	if p := page(); strings.Contains(p, hint) {
		t.Error("Hinweis, obwohl die Firmware neuer ist")
	}
	if err := esppdh.SaveUpdateState(esppdh.UpdateState{Installed: "2100.1.0"}); err != nil {
		t.Fatal(err)
	}
	p := page()
	want := "Firmware mit ESPHome 2099.1.0 gebaut, auf dem Server ist 2100.1.0. " + hint
	i := strings.Index(p, want)
	if i < 0 {
		t.Fatalf("Hinweis fehlt:\n%s", p)
	}
	if start := strings.LastIndex(p[:i], "<div class=\"card esp-msg"); start < 0 || !strings.Contains(p[start:i], "esp-hint-info") {
		t.Error("Hinweis nicht im Ton „info“")
	}
}
