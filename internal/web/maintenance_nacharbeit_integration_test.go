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

// Planer, Reiter „Wartung“: Das Fenster schickt die jetzige Person immer mit.
// Nur ein neuer Tag darf die Besetzung nicht als „von Hand“ merken und die
// Person nicht noch einmal benachrichtigen.
func TestPlannerMaintSaveSamePerson(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	plan := f.mk("PMS", f.infraA, time.Now().AddDate(0, 0, 1), &f.person, nil, nil)
	var task string
	f.must(f.pool.QueryRow(ctx, `SELECT id::text FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, plan).Scan(&task))
	other := f.user("vf_pmo", f.role("vf_pmor", nil))
	f.exec(`UPDATE users SET first_name = 'Otto', last_name = 'Ersatz' WHERE id = $1::uuid`, other)
	post := func(due time.Time, who string) *httptest.ResponseRecorder {
		form := url.Values{"due_date": {due.Format(dayISO)}, "assigned_to": {who}, "month": {due.Format("2006-01")}}
		r := httptest.NewRequest(http.MethodPost, "/planner/maintenance/"+task, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: f.editA, Role: users.Role(f.roleEditA)}))
		w := httptest.NewRecorder()
		f.h.PlannerMaintSaveWeb(w, withChiParam(r, "id", task))
		return w
	}
	state := func() (manual bool, who string, hist int, newValue string) {
		f.must(f.pool.QueryRow(ctx, `SELECT staff_manual, COALESCE(assigned_to::text, '') FROM maintenance_tasks WHERE id = $1::uuid`, task).Scan(&manual, &who))
		f.must(f.pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(new_value), '') FROM record_history
			WHERE ref_type = 'maintenance_task' AND ref_id = $1::uuid AND field_name = 'Zugewiesen'`, task).Scan(&hist, &newValue))
		return
	}

	if w := post(time.Now().AddDate(0, 0, 3), f.person); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "msg=") {
		t.Fatalf("nur der Tag: %d %s", w.Code, w.Header().Get("Location"))
	}
	if manual, who, hist, _ := state(); manual || who != f.person || hist != 0 {
		t.Errorf("nur der Tag geändert: von Hand besetzt %v, Person %s, Verlauf „Zugewiesen“ %d", manual, who, hist)
	}

	if w := post(time.Now().AddDate(0, 0, 3), other); w.Code != http.StatusSeeOther {
		t.Fatalf("andere Person: %d", w.Code)
	}
	manual, who, hist, newValue := state()
	if !manual || who != other || hist != 1 {
		t.Errorf("andere Person: von Hand besetzt %v, Person %s, Verlauf %d", manual, who, hist)
	}
	if !strings.Contains(newValue, "Otto Ersatz") || strings.Contains(newValue, other) {
		t.Errorf("Verlauf soll den Namen zeigen, nicht die Kennung: %q", newValue)
	}
}

// Besetzen im Kalender und im Planer: der Verlauf am Plan nennt Namen.
func TestVisitStaffHistoryNames(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	plan := f.mk("PHN", f.infraA, time.Now().AddDate(0, 1, 0), &f.person, nil, nil)
	vs := f.visits(plan)
	other := f.user("vf_pho", f.role("vf_phor", nil))
	f.exec(`UPDATE users SET first_name = 'Otto', last_name = 'Ersatz' WHERE id = $1::uuid`, other)
	var group string
	f.must(f.pool.QueryRow(ctx, `SELECT name FROM user_groups WHERE id = $1::uuid`, f.groupA).Scan(&group))
	last := func(msg string) (oldV, newV string) {
		f.must(f.pool.QueryRow(ctx, `SELECT old_value, new_value FROM record_history WHERE ref_type = 'maintenance_plan'
			AND ref_id = $1::uuid AND message = $2 ORDER BY created_at DESC LIMIT 1`, plan, msg).Scan(&oldV, &newV))
		return
	}

	if w := f.postJSON(f.editA, f.roleEditA, f.h.MaintenanceVisitStaffWeb, "/maintenance/visits/"+vs[1].ID+"/staff", vs[1].ID,
		`{"assigned_to":"`+other+`","group_id":"`+f.groupA+`"}`); w.Code != http.StatusOK {
		t.Fatalf("besetzen: %d %s", w.Code, w.Body.String())
	}
	oldV, newV := last("Wartungstermin im Kalender besetzt")
	if !strings.Contains(oldV, "Plan Person") || !strings.Contains(newV, "Otto Ersatz") || !strings.Contains(newV, group) {
		t.Errorf("Kalender: Verlauf %q → %q, erwartet Namen von Person und Gruppe", oldV, newV)
	}
	if strings.Contains(oldV+newV, other) || strings.Contains(oldV+newV, f.person) || strings.Contains(newV, f.groupA) {
		t.Errorf("Kalender: Verlauf enthält Kennungen: %q → %q", oldV, newV)
	}

	form := url.Values{"due_date": {vs[2].Day.Format(dayISO)}, "assigned_to": {other}, "month": {vs[2].Day.Format("2006-01")}}
	r := httptest.NewRequest(http.MethodPost, "/planner/maintenance-visits/"+vs[2].ID, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: f.editA, Role: users.Role(f.roleEditA)}))
	w := httptest.NewRecorder()
	f.h.PlannerVisitSaveWeb(w, withChiParam(r, "id", vs[2].ID))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Planer: %d", w.Code)
	}
	oldV, newV = last("Wartungstermin im Planer besetzt")
	if oldV != "Plan Person" || newV != "Otto Ersatz" {
		t.Errorf("Planer: Verlauf %q → %q", oldV, newV)
	}
}
