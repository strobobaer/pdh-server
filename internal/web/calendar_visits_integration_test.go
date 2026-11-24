//go:build integration

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/users"
	"pdh/internal/modules/maintenance"
)

// cvFx: Kalender-Ebene „Wartungstermine“ – Pläne an Anlage A und B, Personen
// mit Rechten nur in A („edit A“) und nur Ansicht überall („view“).
type cvFx struct {
	*jpWeb
	person, editA, viewAll, roleEditA, roleView string
}

func newCVFx(t *testing.T) *cvFx {
	f := &cvFx{jpWeb: newJPWeb(t)}
	f.h.users = users.NewService(users.NewRepository(f.pool), "test-secret", 1)
	f.roleEditA = f.role("vf_cved", map[string]string{"maintenance.view": f.deptA, "maintenance.edit": f.deptA})
	f.roleView = f.role("vf_cvvw", map[string]string{"maintenance.view": ""})
	none := f.role("vf_cvno", nil)
	f.person = f.user("vf_cvp", none)
	f.exec(`UPDATE users SET department_id = $2::uuid, first_name = 'Plan', last_name = 'Person' WHERE id = $1::uuid`, f.person, f.deptA)
	f.editA = f.user("vf_cved", f.roleEditA)
	f.viewAll = f.user("vf_cvvw", f.roleView)
	return f
}

// mk legt einen Plan mit Jahresplan an (Termine ab next, monatlich).
func (f *cvFx) mk(name, infra string, next time.Time, assigned, responsible *string, lead *int) string {
	f.t.Helper()
	ctx := context.Background()
	id, err := f.h.maint.CreatePlan(ctx, &maintenance.PlanInput{Name: name + " " + f.sfx, InfrastructureID: infra, IntervalUnit: "month", IntervalCount: 1,
		ScheduleMode: maintenance.ScheduleFixed, NextDueAt: next.Format("2006-01-02"), AssignedTo: assigned, ResponsibleTo: responsible, LeadWorkdays: lead}, f.creator)
	f.must(err)
	f.plans = append(f.plans, id)
	if _, err := f.h.maint.GenerateYearPlan(ctx, id, f.creator); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *cvFx) visits(planID string) []maintenance.Visit {
	f.t.Helper()
	vs, err := f.h.maint.Repo().PlanVisits(context.Background(), planID)
	f.must(err)
	return vs
}

func (f *cvFx) asUser(uid, role, target string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: uid, Role: users.Role(role)}))
}

// planChips: Chips der Ebene „Wartungstermine“ je Zelle (Datum → Titel) in allen Zeilen.
func planChips(d CalendarData) map[string][]calChip {
	out := map[string][]calChip{}
	for _, row := range d.Rows {
		for _, c := range row.Cells {
			for _, it := range c.Items {
				if it.Kind == "maintenance_planned" {
					out[c.Date] = append(out[c.Date], it)
				}
			}
		}
	}
	return out
}

func chipsWith(m map[string][]calChip, part string) []calChip {
	var out []calChip
	for _, cs := range m {
		for _, c := range cs {
			if strings.Contains(c.Title, part) {
				out = append(out, c)
			}
		}
	}
	return out
}

func calQuery(from time.Time, user string) string {
	return "/calendar?set=1&show=maintenance_planned&show=maintenance&days=42&busy=0&from=" + from.Format(dayISO) + "&user=" + user
}

func TestCalendarShowsVisits(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	start := time.Now().AddDate(0, 0, 20)
	resp := f.user("vf_cvr", f.role("vf_cvrr", nil))
	pa := f.mk("PA", f.infraA, start, &f.person, nil, nil)
	f.mk("PB", f.infraB, start, &f.person, nil, nil)
	f.mk("PR", f.infraA, start, nil, &resp, nil)
	first := f.visits(pa)[0].Day

	d := f.h.buildCalendar(f.asUser(f.editA, f.roleEditA, calQuery(first, f.person)), nil)
	got := planChips(d)
	if cs := chipsWith(got, "PA "+f.sfx); len(cs) == 0 || cs[0].Drag != "visit" || !strings.HasPrefix(cs[0].Title, "geplant · ") {
		t.Errorf("„edit A“: PA fehlt oder nicht ziehbar: %+v", cs)
	}
	if cs := chipsWith(got, "PB "+f.sfx); len(cs) != 0 {
		t.Errorf("„edit A“ sieht den Plan der Abteilung B: %+v", cs)
	}
	d = f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, calQuery(first, f.person)), nil)
	got = planChips(d)
	pas, pbs := chipsWith(got, "PA "+f.sfx), chipsWith(got, "PB "+f.sfx)
	if len(pas) == 0 || len(pbs) == 0 || pas[0].Drag != "" || pbs[0].Drag != "" {
		t.Errorf("„view“: beide Pläne, nicht ziehbar erwartet: PA %+v PB %+v", pas, pbs)
	}
	d = f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, calQuery(first, resp)), nil)
	if cs := chipsWith(planChips(d), "PR "+f.sfx); len(cs) == 0 {
		t.Error("Plan ohne Person: Termin fehlt beim Verantwortlichen")
	}

	// beauftragter Termin: steht als Auftrag unter „Wartungen“, nicht als geplanter Termin
	soon := f.mk("PC", f.infraA, time.Now().AddDate(0, 0, 1), &f.person, nil, nil)
	var taskDue time.Time
	f.must(f.pool.QueryRow(ctx, `SELECT due_date FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, soon).Scan(&taskDue))
	d = f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, calQuery(time.Now(), f.person)), nil)
	day := taskDue.Local().Format(dayISO)
	for _, c := range planChips(d)[day] {
		if strings.Contains(c.Title, "PC "+f.sfx) {
			t.Errorf("beauftragter Termin %s steht noch als geplant da", day)
		}
	}
	var asTask bool
	for _, row := range d.Rows {
		for _, c := range row.Cells {
			for _, it := range c.Items {
				if it.Kind == "maintenance" && strings.Contains(it.Title, "PC "+f.sfx) {
					asTask = true
				}
			}
		}
	}
	if !asTask {
		t.Error("Auftrag des Plans PC fehlt unter „Wartungen“")
	}
}

// Review Focus 2: ein Termin am Sonntag der Zeitumstellung steht in seiner Spalte.
func TestCalendarVisitDSTDay(t *testing.T) {
	withLocal(t, berlin(t))
	f := newCVFx(t)
	dst := time.Date(2027, 3, 28, 0, 0, 0, 0, time.Local)
	f.mk("PD", f.infraA, dst, &f.person, nil, nil)
	d := f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, "/calendar?set=1&show=maintenance_planned&days=7&busy=0&from=2027-03-22&user="+f.person), nil)
	if cs := chipsWith(map[string][]calChip{"x": planChips(d)["2027-03-28"]}, "PD "+f.sfx); len(cs) != 1 {
		t.Errorf("Termin am 28.03.2027 nicht in seiner Spalte: %+v", planChips(d))
	}
}

func (f *cvFx) postJSON(uid, role string, fn http.HandlerFunc, path, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: uid, Role: users.Role(role)}))
	r = withChiParam(r, "id", id)
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func TestVisitMoveWeb(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	start := time.Now().AddDate(0, 1, 0)
	pa := f.mk("PA", f.infraA, start, &f.person, nil, nil)
	pb := f.mk("PB", f.infraB, start, &f.person, nil, nil)
	va, vb := f.visits(pa)[1], f.visits(pb)[1]
	to := va.Day.AddDate(0, 0, 2).Format(dayISO)
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitMoveWeb, "/maintenance/visits/"+va.ID+"/move", va.ID, `{"to_date":"`+to+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PA verschieben: %d %s", w.Code, w.Body.String())
	}
	if v, _ := f.h.maint.GetVisit(ctx, va.ID); v == nil || v.Day.Format(dayISO) != to || !v.DateManual {
		t.Errorf("PA nach dem Verschieben: %+v", v)
	}
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitMoveWeb, "/maintenance/visits/"+vb.ID+"/move", vb.ID, `{"to_date":"`+to+`"}`); w.Code != http.StatusForbidden {
		t.Errorf("PB (Abteilung B): %d, erwartet 403", w.Code)
	}
	if v, _ := f.h.maint.GetVisit(ctx, vb.ID); v == nil || !v.Day.Equal(vb.Day) {
		t.Errorf("PB wurde verändert: %+v", v)
	}
	past := time.Now().AddDate(0, 0, -3).Format(dayISO)
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitMoveWeb, "/maintenance/visits/"+va.ID+"/move", va.ID, `{"to_date":"`+past+`"}`); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "nicht in die Vergangenheit") {
		t.Errorf("in die Vergangenheit: %d %s", w.Code, w.Body.String())
	}
	// nur beteiligt (zugewiesen), ohne Recht: kein Verschieben
	noneRole := f.role("vf_cvin", nil)
	involved := f.user("vf_cvin", noneRole)
	f.exec(`UPDATE maintenance_visits SET assigned_to = $2::uuid WHERE id = $1::uuid`, va.ID, involved)
	if w := f.postJSON(involved, noneRole, f.h.MaintenanceVisitMoveWeb, "/maintenance/visits/"+va.ID+"/move", va.ID, `{"to_date":"`+to+`"}`); w.Code != http.StatusForbidden {
		t.Errorf("nur beteiligt: %d, erwartet 403", w.Code)
	}
	// in den Vorlauf gezogen: der Auftrag entsteht sofort
	v0 := f.visits(pa)[0]
	tomorrow := time.Now().AddDate(0, 0, 1).Format(dayISO)
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitMoveWeb, "/maintenance/visits/"+v0.ID+"/move", v0.ID, `{"to_date":"`+tomorrow+`"}`); w.Code != http.StatusOK {
		t.Fatalf("auf morgen: %d %s", w.Code, w.Body.String())
	}
	var open int
	f.must(f.pool.QueryRow(ctx, `SELECT count(*) FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, pa).Scan(&open))
	if open != 1 {
		t.Errorf("Auftrag nach dem Ziehen in den Vorlauf: %d", open)
	}
	var hist int
	f.must(f.pool.QueryRow(ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'maintenance_plan' AND ref_id = $1::uuid AND message LIKE 'Wartungstermin im Kalender verschoben%'`, pa).Scan(&hist))
	if hist < 1 {
		t.Error("Verlauf am Plan fehlt")
	}
}

func TestVisitStaffAndReset(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	pa := f.mk("PA", f.infraA, time.Now().AddDate(0, 1, 0), &f.person, nil, nil)
	v := f.visits(pa)[1]
	other := f.user("vf_cvq", f.role("vf_cvqr", nil))
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitStaffWeb, "/maintenance/visits/"+v.ID+"/staff", v.ID,
		`{"assigned_to":"`+other+`","group_id":"`+f.groupA+`"}`); w.Code != http.StatusOK {
		t.Fatalf("besetzen: %d %s", w.Code, w.Body.String())
	}
	got, _ := f.h.maint.GetVisit(ctx, v.ID)
	if got == nil || got.AssignedTo == nil || *got.AssignedTo != other || got.GroupID == nil || *got.GroupID != f.groupA || !got.StaffManual {
		t.Fatalf("nach dem Besetzen: %+v", got)
	}
	moved := v.Day.AddDate(0, 0, 3).Format(dayISO)
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitMoveWeb, "/maintenance/visits/"+v.ID+"/move", v.ID, `{"to_date":"`+moved+`"}`); w.Code != http.StatusOK {
		t.Fatalf("verschieben: %d", w.Code)
	}
	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitResetWeb, "/maintenance/visits/"+v.ID+"/reset", v.ID, `{}`); w.Code != http.StatusOK {
		t.Fatalf("zurück zum Plan: %d %s", w.Code, w.Body.String())
	}
	for _, x := range f.visits(pa) { // Kennungen können sich beim Neurechnen ändern
		if x.CalcDay.Equal(v.CalcDay) {
			if !x.Day.Equal(x.CalcDay) || x.DateManual || x.StaffManual || x.AssignedTo == nil || *x.AssignedTo != f.person || x.GroupID != nil {
				t.Errorf("nach „Zurück zum Plan“: %+v", x)
			}
			return
		}
	}
	t.Error("Termin nach „Zurück zum Plan“ nicht gefunden")
}

func TestCalendarSavedPrefGetsPlanned(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	f.h.setUserPref(ctx, f.viewAll, "calendar", "show=maintenance&show=ticket")
	d := f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, "/calendar"), nil)
	if !d.Show["maintenance_planned"] {
		t.Error("gemerkte Auswahl mit „Wartungen“: „Wartungstermine“ fehlt")
	}
	f.h.setUserPref(ctx, f.viewAll, "calendar", "show=maintenance&pv=2")
	d = f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, "/calendar"), nil)
	if d.Show["maintenance_planned"] {
		t.Error("bewusst abgewählte „Wartungstermine“ wieder eingeschaltet")
	}
	f.h.buildCalendar(f.asUser(f.viewAll, f.roleView, "/calendar?set=1&show=maintenance"), nil)
	if p := f.h.userPref(ctx, f.viewAll, "calendar"); !strings.Contains(p, "pv=2") {
		t.Errorf("Merken ohne pv=2: %q", p)
	}
}

func TestCalendarGuestVisits(t *testing.T) {
	f := newCVFx(t)
	start := time.Now().AddDate(0, 0, 20)
	pa := f.mk("PA", f.infraA, start, &f.person, nil, nil)
	f.mk("PB", f.infraB, start, nil, nil, nil) // ohne Person: stünde sonst in „Ohne Person“
	first := f.visits(pa)[0].Day
	board := func(slug, types string) {
		id := f.id(`INSERT INTO leitstaende (slug, name, department_id, types) VALUES ($1, $1, $2::uuid, $3) RETURNING id::text`, slug, f.deptA, types)
		t.Cleanup(func() { f.pool.Exec(context.Background(), `DELETE FROM leitstaende WHERE id = $1::uuid`, id) })
	}
	board("cvm"+f.sfx, "maintenance")
	board("cvf"+f.sfx, "fault")
	guest := func(slug string) map[string][]calChip {
		r := httptest.NewRequest(http.MethodGet, "/global/calendar?b="+slug+"&days=42&from="+first.Format(dayISO), nil)
		return planChips(f.h.buildCalendar(r, f.h.boardBySlug(r, slug)))
	}
	got := guest("cvm" + f.sfx)
	if len(chipsWith(got, "PA "+f.sfx)) == 0 || len(chipsWith(got, "PB "+f.sfx)) != 0 {
		t.Errorf("Gast im Leitstand der Abteilung A: %+v", got)
	}
	for _, c := range chipsWith(got, "PA "+f.sfx) {
		if c.Drag != "" {
			t.Error("Gäste dürfen nicht ziehen")
		}
	}
	if got := guest("cvf" + f.sfx); len(got) != 0 {
		t.Errorf("Leitstand ohne Wartungen zeigt Termine: %+v", got)
	}
}

func TestMyCalendarVisits(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	zero := 0
	f.mk("MEIN", f.infraA, time.Now().AddDate(0, 0, 3), &f.person, nil, &zero)
	f.mk("FREMD", f.infraA, time.Now().AddDate(0, 0, 3), &f.editA, nil, &zero)
	res, err := loadMyCalendar(f.h, ctx, f.person, WidgetInstance{}, func(p string) bool { return p == "maintenance.view" })
	f.must(err)
	var titles []string
	for _, d := range res.([]calWidgetDay) {
		for _, it := range d.Items {
			titles = append(titles, it.Title)
		}
	}
	all := strings.Join(titles, "|")
	if !strings.Contains(all, "geplant · MEIN "+f.sfx) || strings.Contains(all, "FREMD") {
		t.Errorf("Mein Kalender: %q", all)
	}
	res, _ = loadMyCalendar(f.h, ctx, f.person, WidgetInstance{}, func(string) bool { return false })
	for _, d := range res.([]calWidgetDay) {
		for _, it := range d.Items {
			if strings.Contains(it.Title, "MEIN") {
				t.Error("ohne maintenance.view kein geplanter Termin")
			}
		}
	}
}

// Geplante Termine sind keine Aufträge: nirgends als offen gezählt.
func TestVisitsNeverCountAsOpen(t *testing.T) {
	f := newCVFx(t)
	pa := f.mk("OFFEN", f.infraA, time.Now().AddDate(0, 2, 0), &f.person, nil, nil)
	if n := len(f.visits(pa)); n < 10 {
		t.Fatalf("Termine: %d", n)
	}
	r := f.asUser(f.viewAll, f.roleView, "/maintenance")
	for _, c := range f.h.workOpenCards(r, workKinds["maintenance"]) {
		if strings.Contains(c.Title, "OFFEN "+f.sfx) {
			t.Errorf("geplanter Termin als offener Auftrag: %+v", c)
		}
	}
	var n int
	f.must(f.pool.QueryRow(context.Background(), `SELECT count(*) FROM maintenance_tasks WHERE plan_id = $1::uuid`, pa).Scan(&n))
	if n != 0 {
		t.Errorf("Aufträge zum Plan: %d", n)
	}
}
