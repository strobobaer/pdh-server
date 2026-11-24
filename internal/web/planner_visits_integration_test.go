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

	"pdh/internal/core/users"
)

// Planer-Reiter „Wartung“: geplante Termine mit denselben Warnungen wie Aufträge.

// pvShift: Schicht für eine Person an einem Tag (Modell/Definition werden aufgeräumt).
func (f *cvFx) pvShift(uid string, day time.Time) {
	f.t.Helper()
	model := f.id(`INSERT INTO shift_models (name) VALUES ($1) RETURNING id::text`, "PV "+f.sfx)
	def := f.id(`INSERT INTO shift_definitions (model_id, name, short_name, start_time, end_time, color) VALUES ($1::uuid, 'Früh', 'F', '06:00', '14:00', '#336699') RETURNING id::text`, model)
	f.exec(`INSERT INTO shift_assignments (user_id, shift_id, date, note, created_by) VALUES ($1::uuid, $2::uuid, $3, '', $1::uuid)`, uid, def, day.Format(dayISO))
	f.t.Cleanup(func() {
		c := context.Background()
		f.pool.Exec(c, `DELETE FROM shift_assignments WHERE shift_id = $1::uuid`, def)
		f.pool.Exec(c, `DELETE FROM shift_models WHERE id = $1::uuid`, model)
	})
}

func TestPlannerTabVisits(t *testing.T) {
	f := newCVFx(t)
	now := time.Now()
	base := time.Date(now.Year(), now.Month()+2, 6, 0, 0, 0, 0, time.Local) // weit vor dem Vorlauf
	absentDay, shiftDay, noShiftDay := base, base.AddDate(0, 0, 1), base.AddDate(0, 0, 2)
	f.mk("PVABS", f.infraA, absentDay, &f.person, nil, nil)
	f.mk("PVNOS", f.infraA, noShiftDay, &f.person, nil, nil)
	f.mk("PVB", f.infraB, absentDay, &f.person, nil, nil)
	f.pvShift(f.person, shiftDay)
	f.exec(`INSERT INTO absences (user_id, type, status, start_date, end_date, days, note) VALUES ($1::uuid, 'vacation', 'approved', $2, $2, 1, '')`, f.person, absentDay.Format(dayISO))
	t.Cleanup(func() { f.pool.Exec(context.Background(), `DELETE FROM absences WHERE user_id = $1::uuid`, f.person) })

	d := f.h.maintTab(f.asUser(f.editA, f.roleEditA, "/planner?tab=maintenance&month="+base.Format("2006-01")))
	items := map[string]maintItem{}
	for _, w := range d.Weeks {
		for _, day := range w {
			for _, it := range day.Items {
				if it.Planned {
					items[strings.Fields(it.Title)[0]] = it
				}
			}
		}
	}
	if it, ok := items["PVABS"]; !ok || it.Kind != "planned" || !strings.Contains(it.Warn, "ist abwesend") || it.Date != absentDay.Format(dayISO) {
		t.Errorf("Termin bei Abwesenheit: %+v", it)
	}
	if it, ok := items["PVNOS"]; !ok || !strings.Contains(it.Warn, "hat keine Schicht") {
		t.Errorf("Termin ohne Schicht: %+v", it)
	}
	if _, ok := items["PVB"]; ok {
		t.Error("Termin der Abteilung B für Rolle A sichtbar")
	}
	for _, it := range d.Overdue {
		if it.Planned {
			t.Errorf("geplanter Termin unter „Überfällig“: %+v", it)
		}
	}
}

func TestPlannerVisitSave(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	start := time.Now().AddDate(0, 1, 0)
	pa := f.mk("PSA", f.infraA, start, &f.person, nil, nil)
	pb := f.mk("PSB", f.infraB, start, &f.person, nil, nil)
	va, vb := f.visits(pa)[1], f.visits(pb)[1]
	other := f.user("vf_pvo", f.role("vf_pvor", nil))
	post := func(id, due, who string) *httptest.ResponseRecorder {
		form := url.Values{"due_date": {due}, "assigned_to": {who}, "month": {"2027-01"}}
		r := httptest.NewRequest(http.MethodPost, "/planner/maintenance-visits/"+id, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: f.editA, Role: users.Role(f.roleEditA)}))
		r = withChiParam(r, "id", id)
		w := httptest.NewRecorder()
		f.h.PlannerVisitSaveWeb(w, r)
		return w
	}
	to := va.Day.AddDate(0, 0, 2)
	w := post(va.ID, to.Format(dayISO), other)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "msg=") {
		t.Fatalf("speichern: %d %s", w.Code, w.Header().Get("Location"))
	}
	got, _ := f.h.maint.GetVisit(ctx, va.ID)
	if got == nil || !got.Day.Equal(to) || !got.DateManual || !got.StaffManual || got.AssignedTo == nil || *got.AssignedTo != other {
		t.Errorf("nach dem Speichern: %+v", got)
	}
	if w := post(vb.ID, to.Format(dayISO), other); w.Code != http.StatusForbidden {
		t.Errorf("Abteilung B: %d, erwartet 403", w.Code)
	}
	if got, _ := f.h.maint.GetVisit(ctx, vb.ID); got == nil || !got.Day.Equal(vb.Day) || got.StaffManual {
		t.Errorf("Termin B verändert: %+v", got)
	}
}

// Gantt: geplante Termine als eigene Balken; Abteilungssperre und Leitstand-Arten gelten.
func TestGanttVisits(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	start := time.Now().AddDate(0, 0, 20)
	pa := f.mk("GA", f.infraA, start, &f.person, nil, nil)
	pb := f.mk("GB", f.infraB, start, nil, nil, nil)
	items := f.h.ganttVisits(ctx, time.Now())
	var mine []GanttItem
	for _, it := range items {
		if strings.Contains(it.Title, f.sfx) {
			mine = append(mine, it)
		}
	}
	var a GanttItem
	for _, it := range mine {
		if it.DetailURL == "/maintenance/plans/"+pa {
			a = it
		}
	}
	if !a.IsPlanned || a.RefType != "maintenance_planned" || a.DueDateEndpoint != "" || a.StartISO != a.EndISO || a.StartISO == "" {
		t.Fatalf("Gantt-Eintrag: %+v", a)
	}
	// Abteilungssperre der Rolle (Abteilung A)
	f.exec(`UPDATE roles SET department_id = $2::uuid WHERE key = $1`, f.roleEditA, f.deptA)
	sc := f.h.deptScopeFor(ctx, f.editA, f.roleEditA)
	if sc == nil {
		t.Fatal("Rolle A ohne Abteilungssperre")
	}
	for _, it := range f.h.scopeGantt(ctx, sc, append([]GanttItem(nil), mine...)) {
		if it.DetailURL == "/maintenance/plans/"+pb {
			t.Error("Termin an Anlage B trotz Abteilungssperre A")
		}
	}
	// Leitstand ohne Art „maintenance“ zeigt keine Termine
	if boardShowsKind(&leitstand{Types: []string{"fault"}}, nil, "maintenance_planned", a.ID) {
		t.Error("Leitstand ohne Wartungen zeigt Termine")
	}
	if !boardShowsKind(&leitstand{Types: []string{"maintenance"}}, nil, "maintenance_planned", a.ID) {
		t.Error("Leitstand mit Wartungen zeigt keine Termine")
	}
}

// Schlussprüfung I2: Nur verschieben besetzt den Termin nicht still mit der
// verantwortlichen Person; dieselbe Person erneut gewählt setzt nichts „von Hand“.
func TestPlannerVisitDateOnlyKeepsStaff(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	base := time.Now().AddDate(0, 2, 0)
	resp := f.user("vf_pvr", f.role("vf_pvrr", nil))
	pr := f.mk("PVRESP", f.infraA, base, nil, &resp, nil)
	pp := f.mk("PVPERS", f.infraA, base, &f.person, nil, nil)
	d := f.h.maintTab(f.asUser(f.editA, f.roleEditA, "/planner?tab=maintenance&month="+base.Format("2006-01")))
	for _, w := range d.Weeks {
		for _, day := range w {
			for _, it := range day.Items {
				if it.Planned && strings.HasPrefix(it.Title, "PVRESP") && (it.AssigneeID != resp || it.StaffID != "") {
					t.Errorf("Termin ohne Person: Zeile %q, Vorbelegung %q", it.AssigneeID, it.StaffID)
				}
			}
		}
	}
	post := func(id, due, who string) {
		form := url.Values{"due_date": {due}, "assigned_to": {who}}
		r := httptest.NewRequest(http.MethodPost, "/planner/maintenance-visits/"+id, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: f.editA, Role: users.Role(f.roleEditA)}))
		w := httptest.NewRecorder()
		f.h.PlannerVisitSaveWeb(w, withChiParam(r, "id", id))
		if w.Code != http.StatusSeeOther {
			t.Fatalf("speichern: %d", w.Code)
		}
	}
	v := f.visits(pp)[1]
	post(v.ID, v.Day.AddDate(0, 0, 1).Format(dayISO), f.person) // dieselbe Person wie bisher
	if got, _ := f.h.maint.GetVisit(ctx, v.ID); got == nil || got.StaffManual {
		t.Errorf("gleiche Person gewählt: staff_manual gesetzt: %+v", got)
	}
	_ = pr
}
