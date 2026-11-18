package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/modules/ruesten"
)

// Rüstzeit im Erstellungs-Assistenten („Neu anlegen“): Kachel, eigene Schritte,
// Anlegen über POST /setup mit JSON-Antwort; nicht im Leitstand.

func TestCreateWizardOffersSetup(t *testing.T) {
	page := renderPage(t, loadTestTemplates(t), "work_board", WorkBoardData{Area: workArea{Kind: workKinds["ticket"], Tab: "dashboard"}})
	for _, want := range []string{`data-type="setup"`, `id="crw-type-setup"`, `id="crw-reason"`, `id="crw-setup-when"`, `id="crw-sstart"`,
		`id="crw-send"`, `id="crw-sdur"`, `id="crw-setup-mode"`, "TYPES.setup", "applySetupOpts", "setupSubmit", `fetch('/setup'`,
		`'Accept': 'application/json'`, "srvSkew", "localDT(srvNow())", `id="crw-what-hint"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Assistent enthält %q nicht", want)
		}
	}
	// Die Kachel ist versteckt, bis /create/options die Gründe liefert (Recht setup.edit).
	if !strings.Contains(page, `id="crw-type-setup" style="display:none"`) {
		t.Error("Rüstzeit-Kachel muss bis zum Laden der Optionen versteckt sein")
	}
	checkScripts(t, "assistent-ruestzeit", page)
}

func TestBoardWizardHasNoSetup(t *testing.T) {
	c, err := loadTestTemplates(t).Clone()
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := c.ExecuteTemplate(&buf, "create-wizard", map[string]any{"Board": true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, bad := range []string{`data-type="setup"`, `id="crw-reason"`, `id="crw-setup-when"`} {
		if strings.Contains(out, bad) {
			t.Errorf("Leitstand-Assistent enthält %q", bad)
		}
	}
	if !strings.Contains(out, "if(!BOARD) TYPES.setup") {
		t.Error("Art Rüstzeit muss im Leitstand-Modus fehlen")
	}
}

func TestCreateSetupOptionsNeedPermission(t *testing.T) {
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { return false }}
	r := httptest.NewRequest("GET", "/create/options", nil)
	if got := h.createSetupOptions(r); got != nil {
		t.Errorf("ohne setup.edit: %+v", got)
	}
	var asked string
	h.hasPermHook = func(_ *http.Request, p string) bool { asked = p; return true }
	if got := h.createSetupOptions(r); got != nil { // ohne Datenbank keine Gründe → keine Kachel
		t.Errorf("ohne Gründe: %+v", got)
	}
	if asked != permSetupEdit {
		t.Errorf("geprüft %q, erwartet %s", asked, permSetupEdit)
	}
}

// setupDoJSON: wie setupDo, aber als Aufruf aus dem Assistenten (Accept: application/json).
func setupDoJSON(h http.HandlerFunc, uid string, form url.Values) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest("POST", "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	ctx := req.Context()
	if uid != "" {
		ctx = context.WithValue(ctx, "user", &users.User{ID: uid, FirstName: "Rita", LastName: "Rüst", Role: "admin"})
	}
	h(w, req.WithContext(ctx))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestSetupCreateJSONValidation(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	w, body := setupDoJSON(h.SetupCreateWeb, "", url.Values{"started_at": {"2026-11-14T08:00"}})
	if w.Code != http.StatusUnprocessableEntity || body["success"] != false {
		t.Fatalf("ohne Anlage: %d %s", w.Code, w.Body.String())
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "Anlage und Grund") {
		t.Errorf("Meldung %q", msg)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("JSON-Aufruf darf nicht weiterleiten: %q", loc)
	}
	// Ohne Recht: 403 wie beim Formular
	h.hasPermHook = func(*http.Request, string) bool { return false }
	if w, _ := setupDoJSON(h.SetupCreateWeb, "", url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
}

func TestSetupCreateJSON(t *testing.T) {
	f := newSetupFx(t)
	infra, reason, uid := f.infra("json"), f.reason(), f.user()
	start := time.Now().Add(-30 * time.Minute).Truncate(time.Minute)

	// /create/options liefert mit Recht die aktiven Gründe (Kachel sichtbar)
	o := f.h.createSetupOptions(httptest.NewRequest("GET", "/create/options", nil))
	if o == nil || len(o.Reasons) == 0 {
		t.Fatalf("Optionen ohne Gründe: %+v", o)
	}
	// Serverzeit im Format des Formulars: der Assistent rechnet damit (POST /setup liest Ortszeit des Servers)
	if now, err := setupFormTime(o.Now); err != nil || time.Since(now) > 2*time.Minute || time.Until(now) > time.Minute {
		t.Errorf("now = %q (%v)", o.Now, err)
	}
	found := false
	for _, x := range o.Reasons {
		found = found || x.ID == reason
	}
	if !found {
		t.Errorf("Grund %s fehlt in %+v", reason, o.Reasons)
	}

	// Läuft gerade: nur Beginn
	w, body := setupDoJSON(f.h.SetupCreateWeb, uid, url.Values{
		"infrastructure_id": {infra}, "reason_id": {reason}, "started_at": {localForm(start)}, "note": {"aus dem Assistenten"}})
	if w.Code != http.StatusCreated || body["success"] != true {
		t.Fatalf("anlegen: %d %s", w.Code, w.Body.String())
	}
	data, _ := body["data"].(map[string]any)
	id, _ := data["id"].(string)
	if num, _ := data["number"].(string); !strings.HasPrefix(num, "RZ-") {
		t.Errorf("Nummer %q", num)
	}
	if e := f.entry(id); e.Status != ruesten.StatusRunning || e.Note != "aus dem Assistenten" || e.InfraID != infra || e.Origin != ruesten.OriginManual {
		t.Errorf("angelegt: %+v", e)
	}

	// Zweite laufende an derselben Anlage: Meldung als JSON, keine Weiterleitung
	w, body = setupDoJSON(f.h.SetupCreateWeb, uid, url.Values{"infrastructure_id": {infra}, "reason_id": {reason}, "started_at": {localForm(start)}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body["error"].(string), "läuft bereits") {
		t.Errorf("doppelt laufend: %d %s", w.Code, w.Body.String())
	}

	// Nachtragen mit Dauer: beendet
	other := f.infra("json2")
	w, body = setupDoJSON(f.h.SetupCreateWeb, uid, url.Values{"infrastructure_id": {other}, "reason_id": {reason},
		"started_at": {localForm(start.Add(-2 * time.Hour))}, "duration_min": {"45"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("nachtragen: %d %s", w.Code, w.Body.String())
	}
	id2 := body["data"].(map[string]any)["id"].(string)
	if e := f.entry(id2); e.Status != ruesten.StatusDone || e.EndedAt == nil || e.EndedAt.Sub(e.StartedAt) != 45*time.Minute {
		t.Errorf("nachgetragen: %+v", e)
	}
}

func TestAssignmentNewLinksSetup(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "assignment_new", AssignmentNewPageData{CanCreateSetup: true, PresetInfraID: "11111111-1111-4111-8111-111111111111"})
	if !strings.Contains(out, `href="/setup/new?infra=11111111-1111-4111-8111-111111111111"`) {
		t.Error("Neu anlegen: Knopf Rüstzeit mit Anlage fehlt")
	}
	if out := renderPage(t, tmpl, "assignment_new", AssignmentNewPageData{}); strings.Contains(out, `href="/setup/new`) {
		t.Error("ohne setup.edit kein Knopf Rüstzeit")
	}
}
