package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"pdh/internal/core/users"
	"pdh/internal/ha"
)

// Core-Einstellungen → Karte „Betriebssystem“ (os_updates.go): Attrappe des Update-Agenten,
// Rechte-Attrappe (mwHandler) – kein Socket, keine Datenbank.

const osLastRunAt = "2026-11-20T03:10:00+01:00"

// osStatusBody: Antwort von GET /v1/os/status.
func osStatusBody(extra string) string {
	return `{"ok":true,"available":true,"reason":"","cluster":false,"docker":false,"script_outdated":false,
 "system":"Ubuntu 24.04.1 LTS","kernel":"6.8.0-48-generic","package_count":19,"security_count":3,
 "packages":[{"name":"openssl","old":"3.0.13-1","new":"3.0.13-2","security":true},{"name":"curl","old":"8.5.0-1","new":"8.5.0-2","security":false}],
 "reboot_required":true,"reboot_pkgs":["linux-image-6.8.0-48-generic"],"checked_at":"2026-11-24T08:00:00+01:00",
 "with_pdh_update":false,"job":null,"last_run":{"at":"` + osLastRunAt + `","packages":12,"rebooted":true,"ok":true,"message":"fertig"}` + extra + `}`
}

func osHandler(t *testing.T, statusBody string, deny ...string) (*Handler, *mwAgent) {
	t.Helper()
	h := mwHandler(deny...)
	a := &mwAgent{replies: map[string]fakeReply{"GET /v1/os/status": {200, statusBody}}}
	startMWAgent(t, h, a)
	return h, a
}

func osReq(path string, form url.Values) *http.Request { return mwReq(path, form) }

func osRender(t *testing.T, h *Handler) string {
	t.Helper()
	v := h.osUpdateViewFor(context.Background(), mwReq("/core/settings", nil))
	return renderPage(t, loadTestTemplates(t), "core_settings", CoreSettingsPageData{OS: v})
}

func TestOSRoutesNeedPermission(t *testing.T) {
	routes := []string{"/core/settings/os-update/check", "/core/settings/os-update/install", "/core/settings/os-update/config"}
	handlers := func(h *Handler) []http.HandlerFunc {
		return []http.HandlerFunc{h.OSUpdateCheckWeb, h.OSUpdateInstallWeb, h.OSUpdateConfigWeb}
	}
	// ohne system.os_update: 403, der Agent wird nie gerufen
	h, a := osHandler(t, osStatusBody(""), permOSUpdate)
	for i, fn := range handlers(h) {
		rec := httptest.NewRecorder()
		fn(rec, osReq(routes[i], url.Values{"with_pdh_update": {"1"}}))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: %d", routes[i], rec.Code)
		}
	}
	if len(a.calls) != 0 {
		t.Errorf("Agent ohne Recht gerufen: %v", a.calls)
	}
	// mit Recht: Weiterleitung auf die Core-Einstellungen mit Hinweis
	h, _ = osHandler(t, osStatusBody(""))
	for i, fn := range handlers(h) {
		rec := httptest.NewRecorder()
		fn(rec, osReq(routes[i], url.Values{"with_pdh_update": {"1"}}))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusSeeOther || loc.Path != "/core/settings" || loc.Query().Get("notice") == "" {
			t.Errorf("%s mit Recht: %d %q", routes[i], rec.Code, rec.Header().Get("Location"))
		}
	}
	// die Schreib-Routen stehen in gateRules (Recht überall)
	for _, p := range routes {
		if _, perm, ref, _, ok := matchGate("POST", p); !ok || perm != permOSUpdate || ref != "" {
			t.Errorf("POST %s: Recht %q (%q), erwartet %q", p, perm, ref, permOSUpdate)
		}
	}
}

func TestOSCardRender(t *testing.T) {
	h, _ := osHandler(t, osStatusBody(""))
	out := osRender(t, h)
	for _, want := range []string{"19 Pakete offen", "3 Sicherheit", "Neustart nötig", "linux-image-6.8.0-48-generic",
		"Ubuntu 24.04.1 LTS", "6.8.0-48-generic", "openssl", "3.0.13-2", "Betriebssystem aktualisieren", "Betriebssystem prüfen",
		`name="with_pdh_update"`, `action="/core/settings/os-update/install"`, `action="/core/settings/os-update/check"`,
		"Geprüft", "Letzter Lauf", "12 Pakete", "Neustart ja", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("Karte ohne %q", want)
		}
	}
	// ohne Recht: Zahlen ja, Knöpfe und Haken nein
	h, _ = osHandler(t, osStatusBody(""), permOSUpdate)
	out = osRender(t, h)
	if !strings.Contains(out, "19 Pakete offen") || !strings.Contains(out, "3 Sicherheit") {
		t.Error("ohne Recht: Zahlen fehlen")
	}
	for _, not := range []string{"os-update/install", "os-update/check", "with_pdh_update"} {
		if strings.Contains(out, not) {
			t.Errorf("ohne Recht: %q sichtbar", not)
		}
	}
	// Docker
	h, _ = osHandler(t, `{"ok":true,"available":false,"reason":"im Docker-Betrieb nicht verfügbar","docker":true,"packages":[],"reboot_pkgs":[]}`)
	out = osRender(t, h)
	if !strings.Contains(out, "im Docker-Betrieb nicht verfügbar") || strings.Contains(out, "os-update/install") || strings.Contains(out, "with_pdh_update") {
		t.Errorf("Docker: Hinweis oder Knöpfe falsch")
	}
	// Verbund
	h, _ = osHandler(t, `{"ok":true,"available":false,"reason":"Verbund","cluster":true,"packages":[],"reboot_pkgs":[]}`)
	out = osRender(t, h)
	if !strings.Contains(out, "Im Verbund kommt das Betriebssystem-Update mit einer späteren Version") || strings.Contains(out, "os-update/install") {
		t.Errorf("Verbund: Hinweis oder Knöpfe falsch")
	}
	// Agent zu alt (404): Hinweis, Seite rendert
	h, a404 := osHandler(t, "")
	a404.replies["GET /v1/os/status"] = fakeReply{404, "404 page not found"}
	out = osRender(t, h)
	if !strings.Contains(out, "Update-Agent ist zu alt") || strings.Contains(out, "os-update/install") {
		t.Errorf("404: Hinweis fehlt")
	}
	// kein Agent
	out = osRender(t, mwHandler())
	if !strings.Contains(out, "Update-Agent ist nicht eingerichtet") {
		t.Errorf("ohne Agent: Hinweis fehlt")
	}
	// aktuell
	h, _ = osHandler(t, strings.ReplaceAll(strings.ReplaceAll(osStatusBody(""), `"package_count":19`, `"package_count":0`), `"reboot_required":true`, `"reboot_required":false`))
	out = osRender(t, h)
	if !strings.Contains(out, "Das Betriebssystem ist aktuell") || strings.Contains(out, "Neustart nötig") {
		t.Errorf("aktuell: Text falsch")
	}
}

func osJobBody(phase string) string {
	return osStatusBody(`,"job":{"active":true,"kind":"os","phase":"` + phase + `","text":"Abmeldung in Kürze – bitte Arbeit speichern","percent":5}`)
}

func TestOSCardRunningJob(t *testing.T) {
	// Job läuft: Fortschritt statt Knöpfe; Abbrechen nur vor dem Einspielen und mit dem Recht „Wartung abbrechen“
	for _, c := range []struct {
		phase string
		deny  []string
		abort bool
	}{
		{"countdown", nil, true},
		{"vorbereiten", nil, true},
		{"countdown", []string{permMaintAbort}, false},
		{"locked", nil, false},
	} {
		h, _ := osHandler(t, osJobBody(c.phase), c.deny...)
		h.mlock.admin = func(u *users.User) bool { return h.hasPerm(mwReq("/", nil), permMaintAbort) }
		out := osRender(t, h)
		if !strings.Contains(out, "Abmeldung in Kürze") || !strings.Contains(out, `<progress`) || strings.Contains(out, "os-update/install") {
			t.Errorf("%s: Fortschritt/Knöpfe falsch", c.phase)
		}
		if got := strings.Contains(out, `action="/wartung/abbrechen"`); got != c.abort {
			t.Errorf("%s deny=%v: Abbrechen sichtbar=%v, erwartet %v", c.phase, c.deny, got, c.abort)
		}
	}
}

func TestOSInstallAskFlow(t *testing.T) {
	a409 := fakeReply{409, `{"ok":false,"error":"Sicherung nicht eingerichtet","hints":["x"]}`}
	h, a := osHandler(t, osStatusBody(""))
	a.replies["POST /v1/os/job/start"] = a409
	rec := httptest.NewRecorder()
	h.OSUpdateInstallWeb(rec, osReq("/core/settings/os-update/install", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || loc.Query().Get("ask") != "os" || loc.Query().Get("notice") != "" {
		t.Fatalf("Weiterleitung: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if body, _ := a.called("POST /v1/os/job/start"); body != `{"force":false}` {
		t.Errorf("erster Aufruf: %q", body)
	}
	ak, ok := updateAskGet("a1")
	if !ok || ak.Reason != "Sicherung nicht eingerichtet" || len(ak.Hints) != 1 || ak.Token == "" {
		t.Fatalf("Rückfrage im Speicher: %+v", ak)
	}
	// Rückfrage auf der Seite
	v := h.osUpdateViewFor(context.Background(), mwReq("/core/settings?ask=os", nil))
	if v.Ask == "" || v.AskToken != ak.Token || len(v.AskHints) != 1 {
		t.Fatalf("Rückfrage in der Ansicht: %+v", v)
	}
	out := renderPage(t, loadTestTemplates(t), "core_settings", CoreSettingsPageData{OS: v})
	for _, want := range []string{"Sicherung nicht eingerichtet", `name="force" value="1"`, `name="token" value="` + ak.Token + `"`, "Trotzdem aktualisieren"} {
		if !strings.Contains(out, want) {
			t.Errorf("Rückfrage ohne %q", want)
		}
	}
	// Wiederholung mit Token: force=true
	a.mu.Lock()
	a.calls, a.bodies = nil, nil
	a.replies["POST /v1/os/job/start"] = fakeReply{200, `{"ok":true,"hints":[]}`}
	a.mu.Unlock()
	rec = httptest.NewRecorder()
	h.OSUpdateInstallWeb(rec, osReq("/core/settings/os-update/install", url.Values{"force": {"1"}, "token": {ak.Token}}))
	if body, _ := a.called("POST /v1/os/job/start"); body != `{"force":true}` {
		t.Errorf("mit Token: %q", body)
	}
	if loc, _ := url.Parse(rec.Header().Get("Location")); !strings.Contains(loc.Query().Get("notice"), "gestartet") {
		t.Errorf("Hinweis: %q", rec.Header().Get("Location"))
	}
	// falscher Token: Rückfall auf force=false
	updateAskPut("a1", "x", nil)
	a.mu.Lock()
	a.calls, a.bodies = nil, nil
	a.mu.Unlock()
	rec = httptest.NewRecorder()
	h.OSUpdateInstallWeb(rec, osReq("/core/settings/os-update/install", url.Values{"force": {"1"}, "token": {"falsch"}}))
	if body, _ := a.called("POST /v1/os/job/start"); body != `{"force":false}` {
		t.Errorf("falscher Token: %q", body)
	}
}

func TestOSInstallErrors(t *testing.T) {
	h, a := osHandler(t, osStatusBody(""))
	a.replies["POST /v1/os/job/start"] = fakeReply{400, `{"ok":false,"error":"Ein Betriebssystem-Update läuft bereits"}`}
	rec := httptest.NewRecorder()
	h.OSUpdateInstallWeb(rec, osReq("/core/settings/os-update/install", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if n := loc.Query().Get("notice"); !strings.Contains(n, "nicht gestartet") || !strings.Contains(n, "läuft bereits") || loc.Query().Get("ask") != "" {
		t.Errorf("Fehler: %q", rec.Header().Get("Location"))
	}
}

func TestOSConfigWeb(t *testing.T) {
	for form, want := range map[string]string{"1": `{"with_pdh_update":true}`, "": `{"with_pdh_update":false}`} {
		h, a := osHandler(t, osStatusBody(""))
		v := url.Values{}
		if form != "" {
			v.Set("with_pdh_update", form)
		}
		rec := httptest.NewRecorder()
		h.OSUpdateConfigWeb(rec, osReq("/core/settings/os-update/config", v))
		if body, ok := a.called("POST /v1/os/config"); !ok || body != want {
			t.Errorf("with_pdh_update=%q: Agent erhielt %q, erwartet %q", form, body, want)
		}
	}
}

func TestOSCheckWeb(t *testing.T) {
	h, a := osHandler(t, osStatusBody(""))
	a.replies["POST /v1/os/check"] = fakeReply{200, osStatusBody("")}
	rec := httptest.NewRecorder()
	h.OSUpdateCheckWeb(rec, osReq("/core/settings/os-update/check", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if n := loc.Query().Get("notice"); !strings.Contains(n, "19 Pakete offen") || !strings.Contains(n, "3 Sicherheit") {
		t.Errorf("Hinweis: %q", n)
	}
	a.replies["POST /v1/os/check"] = fakeReply{409, `{"ok":false,"error":"Ein Betriebssystem-Update läuft"}`}
	rec = httptest.NewRecorder()
	h.OSUpdateCheckWeb(rec, osReq("/core/settings/os-update/check", nil))
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if n := loc.Query().Get("notice"); !strings.Contains(n, "fehlgeschlagen") || !strings.Contains(n, "läuft") {
		t.Errorf("Fehler: %q", n)
	}
}

func TestOSAutoCheck(t *testing.T) {
	for _, c := range []struct {
		enabled string
		want    bool
	}{{"true", true}, {"false", false}} {
		h, a := osHandler(t, osStatusBody(""))
		h.githubCheckHook = func(context.Context) error { return nil }
		_ = h.setUpdateSetting(context.Background(), "update_auto_check_enabled", c.enabled)
		h.checkUpdateIfDue(context.Background())
		if _, got := a.called("POST /v1/os/check"); got != c.want {
			t.Errorf("auto_check_enabled=%s: Agent-Prüfung=%v, erwartet %v", c.enabled, got, c.want)
		}
	}
	// ein Fehler der GitHub-Prüfung hält die Betriebssystem-Prüfung nicht auf
	h, a := osHandler(t, osStatusBody(""))
	h.githubCheckHook = func(context.Context) error { return http.ErrHandlerTimeout }
	h.checkUpdateIfDue(context.Background())
	if _, got := a.called("POST /v1/os/check"); !got {
		t.Error("GitHub-Fehler: Betriebssystem-Prüfung fehlt")
	}
}

func TestOSLastRunLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	old := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = old })
	h, a := osHandler(t, osStatusBody(""))
	for i := 0; i < 2; i++ {
		h.osUpdateViewFor(context.Background(), mwReq("/core/settings", nil))
	}
	if n := strings.Count(buf.String(), "Betriebssystem aktualisiert: 12 Pakete, Neustart ja"); n != 1 {
		t.Fatalf("%d Einträge, erwartet 1: %s", n, buf.String())
	}
	if !strings.Contains(buf.String(), `"bereich":"os-update"`) {
		t.Errorf("Bereich fehlt: %s", buf.String())
	}
	// ein neuerer Lauf wird wieder einmal eingetragen
	a.mu.Lock()
	a.replies["GET /v1/os/status"] = fakeReply{200, strings.ReplaceAll(osStatusBody(""), osLastRunAt, "2026-11-21T03:10:00+01:00")}
	a.mu.Unlock()
	for i := 0; i < 2; i++ {
		h.osUpdateViewFor(context.Background(), mwReq("/core/settings", nil))
	}
	if n := strings.Count(buf.String(), "Betriebssystem aktualisiert"); n != 2 {
		t.Fatalf("nach neuem Lauf %d Einträge, erwartet 2", n)
	}
}

// ── Abbrechen für den Betriebssystem-Auftrag am Einzelserver ──────────────

func osAbortView(phase string) ha.UpdateView {
	return ha.UpdateView{Active: true, Kind: "os", Phase: phase, Text: "Abmeldung in Kürze – bitte Arbeit speichern", Percent: 5,
		CountdownUntil: time.Now().Add(2 * time.Minute)}
}

func osStatusFor(t *testing.T, v ha.UpdateView, tok string, cluster bool) map[string]any {
	t.Helper()
	h, _ := maintTestHandler(t, v)
	h.mlock.cluster = func() bool { return cluster }
	h.mlock.confirmer = func(u *users.User) bool { return u != nil && u.ID == "admin" }
	hd, _ := maintPlain(h)
	rec := maintDo(hd, maintReq{method: "GET", path: "/wartung/status", tok: tok})
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Band: Einzelserver, Betriebssystem-Auftrag – Abbrechen vor dem Einspielen, nur mit dem Recht.
func TestOSBandOffersAbort(t *testing.T) {
	adm := maintToken(t, "admin", time.Now())
	usr := maintToken(t, "u1", time.Now())
	for _, ph := range []string{"countdown", "vorbereiten"} {
		out := osStatusFor(t, osAbortView(ph), adm, false)
		if out["can_abort"] != true || out["phase"] != ph || out["can_confirm"] == true {
			t.Errorf("%s mit Recht: %v", ph, out)
		}
		out = osStatusFor(t, osAbortView(ph), usr, false)
		if _, ok := out["can_abort"]; ok {
			t.Errorf("%s ohne Recht: Knopf angeboten: %v", ph, out)
		}
	}
	// nach dem Einspielen (gesperrt, Freigabe …): nichts mehr anbieten
	if out := osStatusFor(t, osAbortView("release"), adm, false); out["can_abort"] == true {
		t.Errorf("Freigabe: %v", out)
	}
	// PDH-Auftrag am Einzelserver unverändert: keine Zusatzfelder
	pdh := osAbortView("countdown")
	pdh.Kind = "sequential"
	if out := osStatusFor(t, pdh, adm, false); out["can_abort"] != nil || out["phase"] != nil {
		t.Errorf("PDH-Auftrag Einzelserver: %v", out)
	}
	// Verbund unverändert: Rechte wie bisher
	if out := osStatusFor(t, pdh, adm, true); out["can_abort"] != true {
		t.Errorf("Verbund: %v", out)
	}
}

// Wartungsbildschirm in der Sperre: kein Abbrechen für den Betriebssystem-Auftrag, stattdessen Text.
func TestOSMaintScreenNoAbort(t *testing.T) {
	v := ha.UpdateView{Active: true, Locked: true, Kind: "os", Phase: "locked", Step: 1, Percent: 25, Text: "Betriebssystem-Pakete werden eingespielt",
		Steps: []string{"Abmelden", "Pakete einspielen", "Neustart", "Prüfen", "Freigabe"}}
	body := maintScreenBody(t, v, maintToken(t, "admin", time.Now()), "/wartung")
	if strings.Contains(body, `action="/wartung/abbrechen"`) {
		t.Error("Betriebssystem-Auftrag: Abbrechen-Knopf in der Sperre")
	}
	for _, want := range []string{"Betriebssystem-Pakete werden eingespielt", "Pakete einspielen", "Abbrechen ist nicht mehr möglich"} {
		if !strings.Contains(body, want) {
			t.Errorf("Bildschirm ohne %q", want)
		}
	}
	// PDH-Auftrag unverändert
	pdh := lockedView()
	if b := maintScreenBody(t, pdh, maintToken(t, "admin", time.Now()), "/wartung"); !strings.Contains(b, `action="/wartung/abbrechen"`) {
		t.Error("PDH-Auftrag: Abbrechen-Knopf fehlt")
	}
}

// Fortschrittskasten (Ausfallschutz) nennt den Betriebssystem-Auftrag.
func TestOSProgressTitle(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	var sb strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&sb, "maint-progress", maintBox{UpdateView: osAbortView("countdown")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "Betriebssystem-Update läuft") {
		t.Errorf("Titel: %s", sb.String())
	}
}

// Fix 1: nur system.os_update genügt nicht – die Seite verlangt system.manage_roles, die Routen auch.
func TestOSRoutesNeedManageRoles(t *testing.T) {
	h, a := osHandler(t, osStatusBody(""), "system.manage_roles")
	routes := []string{"/core/settings/os-update/check", "/core/settings/os-update/install", "/core/settings/os-update/config"}
	for i, fn := range []http.HandlerFunc{h.OSUpdateCheckWeb, h.OSUpdateInstallWeb, h.OSUpdateConfigWeb} {
		rec := httptest.NewRecorder()
		fn(rec, osReq(routes[i], url.Values{"with_pdh_update": {"1"}}))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s ohne Rollenverwaltung: %d", routes[i], rec.Code)
		}
	}
	if len(a.calls) != 0 {
		t.Errorf("Agent gerufen: %v", a.calls)
	}
}

// Fix 2: Abbrechen von der Karte führt zurück auf die Core-Einstellungen; fremde Ziele werden ignoriert.
func TestOSAbortReturnsToCoreSettings(t *testing.T) {
	h, _ := maintTestHandler(t, osAbortView("countdown"))
	h.mlock.abort = func(context.Context, string) error { return nil }
	hd, _ := maintPlain(h)
	tok := maintToken(t, "admin", time.Now())
	for ret, want := range map[string]string{"/core/settings": "/core/settings?", "https://evil.example/": "/admin/server-config", "//evil.example": "/admin/server-config"} {
		rec := maintDo(hd, maintReq{method: "POST", path: "/wartung/abbrechen", body: "return=" + url.QueryEscape(ret), tok: tok, hdr: htmlAccept})
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, want) {
			t.Errorf("return=%q: %d %q, erwartet Präfix %q", ret, rec.Code, loc, want)
		}
		if ret == "/core/settings" && !strings.Contains(loc, "notice=") {
			t.Errorf("Hinweis fehlt: %q", loc)
		}
	}
}

// Fix 3 und 4: Zeitgrenzen – Seite rendert trotz langsamem Agenten, Installieren meldet Zeitüberschreitung lesbar.
func TestOSSlowAgent(t *testing.T) {
	old := osStatusTimeout
	osStatusTimeout = 100 * time.Millisecond
	defer func() { osStatusTimeout = old }()
	h := mwHandler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }))
	defer srv.Close()
	h.updateAgentURL, h.updateAgentToken = srv.URL, "tok"
	start := time.Now()
	v := h.osUpdateViewFor(context.Background(), mwReq("/core/settings", nil))
	if time.Since(start) > 500*time.Millisecond || v.Err == "" {
		t.Errorf("Status wartet zu lange (%v) oder ohne Hinweis: %q", time.Since(start), v.Err)
	}
	oldI := osInstallTimeout
	osInstallTimeout = 100 * time.Millisecond
	defer func() { osInstallTimeout = oldI }()
	rec := httptest.NewRecorder()
	h.OSUpdateInstallWeb(rec, osReq("/core/settings/os-update/install", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc.Query().Get("notice"), "nicht rechtzeitig") {
		t.Errorf("Installieren: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
