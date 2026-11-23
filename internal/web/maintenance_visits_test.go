package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/modules/maintenance"
)

// Reiter „Regeln“ der Wartung: Vorlauf des Durchführungsauftrags in Arbeitstagen.
func TestMaintRulesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	k := workKinds["maintenance"]
	page := func(can bool) string {
		return renderPage(t, tmpl, "maintenance", MaintenancePageData{View: "area", LeadWorkdays: 5, CanMaintRules: can,
			Area: workArea{Kind: k, Tab: "rules"}})
	}
	edit := page(true)
	for _, want := range []string{"Wartungsregeln", `name="lead_workdays"`, `action="/maintenance/rules"`, `value="5"`, "Gilt für alle Pläne ohne eigenen Vorlauf"} {
		if !strings.Contains(edit, want) {
			t.Errorf("Wartungsregeln mit Recht ohne %q", want)
		}
	}
	view := page(false)
	if strings.Contains(view, `name="lead_workdays"`) || !strings.Contains(view, "5 Arbeitstage") {
		t.Errorf("Wartungsregeln ohne Recht: Formular sichtbar oder Wert fehlt")
	}
	board := renderPage(t, tmpl, "maintenance", MaintenancePageData{View: "area", LeadWorkdays: 5, CanMaintRules: true,
		Area: workArea{Kind: k, Tab: "board"}})
	if strings.Contains(board, "Wartungsregeln") {
		t.Error("Wartungsregeln nur im Reiter „Regeln“")
	}
}

func TestPlanEditorLeadField(t *testing.T) {
	tmpl := loadTestTemplates(t)
	plan := &maintenance.MaintenancePlan{ID: "p1", Name: "Kompressor", Active: true, IntervalUnit: "month", IntervalCount: 1}
	data := MaintenancePlanPageData{Plan: plan, PlanJSON: "{}", Templates: "[]", Infras: "[]", CanEdit: true, VisitCount: 12, DefaultLead: 5,
		TypeLabels: maintTypeShort}
	out := renderPage(t, tmpl, "maintenance_plan", data)
	for _, want := range []string{`id="mp-lead"`, `max="60"`, "Vorgabe: 5", "Vorlauf (Arbeitstage)", "Jahresplan erzeugen",
		`hx-post="/maintenance/plans/p1/yearplan"`, "12 geplante Termine"} {
		if !strings.Contains(out, want) {
			t.Errorf("Plan-Editor ohne %q", want)
		}
	}
	if strings.Contains(out, "Vorlauf (Tage)") || strings.Contains(out, "lead_days") {
		t.Error("Plan-Editor nennt noch den Vorlauf in Kalendertagen")
	}
	data.YearPlanDone = true
	if out := renderPage(t, tmpl, "maintenance_plan", data); !strings.Contains(out, "Jahresplan aktualisieren") {
		t.Error("mit Jahresplan: Knopf „Jahresplan aktualisieren“ fehlt")
	}
	data.CanEdit = false
	if out := renderPage(t, tmpl, "maintenance_plan", data); strings.Contains(out, "/yearplan") {
		t.Error("ohne Recht kein Knopf „Jahresplan“")
	}
	data.CanEdit, data.IsNew = true, true
	if out := renderPage(t, tmpl, "maintenance_plan", data); strings.Contains(out, "/yearplan") {
		t.Error("neuer Plan: Knopf erst nach dem Speichern")
	}
}

func TestYearplanGateRules(t *testing.T) {
	id := "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"
	for _, c := range []struct{ path, perm, ref, id string }{
		{"/maintenance/plans/" + id + "/yearplan", "maintenance.edit", "maintenance_plan", id},
		{"/maintenance/yearplan", "maintenance.edit", "", ""},
		{"/maintenance/rules", "maintenance.admin", "", ""},
	} {
		_, perm, ref, gotID, ok := matchGate(http.MethodPost, c.path)
		if !ok || perm != c.perm || ref != c.ref || gotID != c.id {
			t.Errorf("%s: %v %q %q %q, erwartet %q %q %q", c.path, ok, perm, ref, gotID, c.perm, c.ref, c.id)
		}
	}
}

func TestMaintRulesLocked(t *testing.T) {
	h := &Handler{tmpl: loadTestTemplates(t)}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	h.mlock.locked = true
	for name, fn := range map[string]http.HandlerFunc{"rules": h.MaintenanceRulesWeb, "yearplan": h.MaintenanceYearPlanAllWeb, "plan": h.MaintenanceYearPlanWeb} {
		r := httptest.NewRequest(http.MethodPost, "/maintenance/rules", strings.NewReader(url.Values{"lead_workdays": {"5"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		fn(w, withTestUser(r))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), errMaintLater) {
			t.Errorf("%s während der Wartung: %d %s", name, w.Code, w.Body.String())
		}
	}
}
