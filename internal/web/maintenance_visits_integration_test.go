//go:build integration

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/workdays"
	"pdh/internal/modules/maintenance"
	"pdh/pkg/appsettings"
)

// jpWeb: Wartungsdienst am Prüf-Handler aus newViewFx, Pläne werden aufgeräumt.
type jpWeb struct {
	*viewFx
	plans []string
}

func newJPWeb(t *testing.T) *jpWeb {
	f := &jpWeb{viewFx: newViewFx(t)}
	f.h.maint = maintenance.NewService(maintenance.NewRepository(f.pool))
	t.Cleanup(func() {
		c := context.Background()
		f.pool.Exec(c, `DELETE FROM maintenance_tasks WHERE plan_id::text = ANY($1)`, f.plans)
		f.pool.Exec(c, `DELETE FROM maintenance_plans WHERE id::text = ANY($1)`, f.plans)
	})
	return f
}

func (f *jpWeb) plan(infra string, next time.Time, lead *int) string {
	f.t.Helper()
	id, err := f.h.maint.CreatePlan(context.Background(), &maintenance.PlanInput{Name: "JP " + f.sfx, InfrastructureID: infra,
		IntervalUnit: "month", IntervalCount: 1, ScheduleMode: maintenance.ScheduleFixed, NextDueAt: next.Format("2006-01-02"),
		LeadWorkdays: lead}, f.creator)
	f.must(err)
	f.plans = append(f.plans, id)
	return id
}

func (f *jpWeb) post(r *http.Request, fn http.HandlerFunc, path string, form url.Values, params map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func TestYearPlanWebDept(t *testing.T) {
	f := newJPWeb(t)
	role := f.role("vf_jp", map[string]string{"maintenance.edit": f.deptA})
	r := f.req(f.user("vf_jp", role), role)
	next := time.Now().AddDate(0, 2, 0)
	pA, pB := f.plan(f.infraA, next, nil), f.plan(f.infraB, next, nil)

	if w := f.post(r, f.h.MaintenanceYearPlanWeb, "/maintenance/plans/"+pB+"/yearplan", nil, map[string]string{"id": pB}); w.Code != http.StatusForbidden {
		t.Errorf("Einzelknopf an Anlage B: %d, erwartet 403", w.Code)
	}
	plans, visits, skipped := f.h.yearPlanFor(r, []string{pA, pB})
	if plans != 1 || visits < 10 || skipped != 1 {
		t.Errorf("Sammelknopf: %d Pläne, %d Termine, %d übersprungen", plans, visits, skipped)
	}
	if got := yearPlanAllText(plans, visits, skipped); !strings.Contains(got, "1 Plan anderer Abteilungen übersprungen") || !strings.Contains(got, "für 1 Plan erzeugt") {
		t.Errorf("Rückmeldung: %q", got)
	}
	repo := f.h.maint.Repo()
	if vs, _ := repo.PlanVisits(context.Background(), pB); len(vs) != 0 {
		t.Errorf("Anlage B hat %d Termine", len(vs))
	}
	if vs, _ := repo.PlanVisits(context.Background(), pA); len(vs) == 0 {
		t.Error("Anlage A ohne Termine")
	}
	w := f.post(r, f.h.MaintenanceYearPlanWeb, "/maintenance/plans/"+pA+"/yearplan", nil, map[string]string{"id": pA})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Jahresplan ist aktuell") {
		t.Errorf("zweiter Lauf: %d %s", w.Code, w.Body.String())
	}
}

func TestMaintRulesWeb(t *testing.T) {
	f := newJPWeb(t)
	ctx := context.Background()
	var old string
	hadOld := f.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, appsettings.KeyMaintenanceLeadWorkdays).Scan(&old) == nil
	t.Cleanup(func() {
		if hadOld {
			f.pool.Exec(context.Background(), `UPDATE app_settings SET value = $2 WHERE key = $1`, appsettings.KeyMaintenanceLeadWorkdays, old)
		} else {
			f.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, appsettings.KeyMaintenanceLeadWorkdays)
		}
	})
	// offener Auftrag vor der Änderung (Termin morgen → Auftrag entsteht sofort)
	open := f.plan(f.infraA, time.Now().AddDate(0, 0, 1), nil)
	var taskID string
	var upBefore time.Time
	f.must(f.pool.QueryRow(ctx, `SELECT id::text, upcoming_from FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, open).Scan(&taskID, &upBefore))

	roleA := f.role("vf_ra", map[string]string{"maintenance.admin": f.deptA})
	rA := f.req(f.user("vf_ra", roleA), roleA)
	if w := f.post(rA, f.h.MaintenanceRulesWeb, "/maintenance/rules", url.Values{"lead_workdays": {"10"}}, nil); w.Code != http.StatusForbidden {
		t.Errorf("Bereichsadministrator nur in A: %d, erwartet 403", w.Code)
	}
	roleAll := f.role("vf_rall", map[string]string{"maintenance.admin": ""})
	rAll := f.req(f.user("vf_rall", roleAll), roleAll)
	if w := f.post(rAll, f.h.MaintenanceRulesWeb, "/maintenance/rules", url.Values{"lead_workdays": {"61"}}, nil); w.Code != http.StatusSeeOther ||
		!strings.Contains(w.Header().Get("Location"), url.QueryEscape("Der Vorlauf muss zwischen 0 und 60 Arbeitstagen liegen")) {
		t.Errorf("61 Arbeitstage: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := f.post(rAll, f.h.MaintenanceRulesWeb, "/maintenance/rules", url.Values{"lead_workdays": {"10"}}, nil); w.Code != http.StatusSeeOther ||
		!strings.Contains(w.Header().Get("Location"), "tab=rules") {
		t.Errorf("Speichern: %d %s", w.Code, w.Header().Get("Location"))
	}
	repo := f.h.maint.Repo()
	if got := repo.DefaultLead(ctx); got != 10 {
		t.Fatalf("Vorgabe nach dem Speichern: %d", got)
	}
	day := time.Date(2027, 3, 15, 0, 0, 0, 0, time.Local)
	if got, want := repo.OrderDay(ctx, day, nil), workdays.LeadDay(ctx, f.pool, day, 10); !got.Equal(want) {
		t.Errorf("Plan ohne eigenen Vorlauf: %s, erwartet %s", got, want)
	}
	three := 3
	if got, want := repo.OrderDay(ctx, day, &three), workdays.LeadDay(ctx, f.pool, day, 3); !got.Equal(want) {
		t.Errorf("Plan mit 3 Arbeitstagen: %s, erwartet %s", got, want)
	}
	var upAfter time.Time
	f.must(f.pool.QueryRow(ctx, `SELECT upcoming_from FROM maintenance_tasks WHERE id = $1::uuid`, taskID).Scan(&upAfter))
	if !upAfter.Equal(upBefore) {
		t.Errorf("offener Auftrag: upcoming_from %s → %s", upBefore, upAfter)
	}
}

func TestYearRowsFromVisits(t *testing.T) {
	f := newJPWeb(t)
	ctx := context.Background()
	next := time.Now().AddDate(0, 2, 0)
	withPlan, without := f.plan(f.infraA, next, nil), f.plan(f.infraA, next, nil)
	if _, err := f.h.maint.GenerateYearPlan(ctx, withPlan, f.creator); err != nil {
		t.Fatal(err)
	}
	repo := f.h.maint.Repo()
	vs, err := repo.PlanVisits(ctx, withPlan)
	if err != nil || len(vs) < 3 {
		t.Fatalf("Termine: %d %v", len(vs), err)
	}
	moved := vs[0].Day.AddDate(0, 0, 3)
	f.must(f.h.maint.MoveVisit(ctx, vs[0].ID, moved, f.creator))
	f.exec(`UPDATE maintenance_visits SET state = 'dropped' WHERE id = $1::uuid`, vs[1].ID)

	data := MaintenancePageData{Year: moved.Year()}
	f.h.maintYearRows(ctx, &data, map[string]bool{withPlan: true, without: true})
	marks := map[string][]yearMark{}
	for _, row := range data.YearRows {
		for _, m := range row.Months {
			marks[row.PlanID] = append(marks[row.PlanID], m...)
		}
	}
	has := func(planID, kind, date string) bool {
		for _, m := range marks[planID] {
			if m.Kind == kind && (date == "" || m.Date == date) {
				return true
			}
		}
		return false
	}
	if !has(withPlan, "planned", moved.Format("02.01.")) {
		t.Errorf("verschobener Termin %s fehlt: %+v", moved.Format("02.01."), marks[withPlan])
	}
	if vs[1].Day.Year() == data.Year && !has(withPlan, "dropped", vs[1].Day.Format("02.01.")) {
		t.Errorf("entfallener Termin fehlt: %+v", marks[withPlan])
	}
	if !has(without, "planned", "") {
		t.Errorf("Plan ohne Jahresplan ohne Prognose: %+v", marks[without])
	}
}
