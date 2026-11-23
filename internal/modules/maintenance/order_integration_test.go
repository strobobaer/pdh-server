//go:build integration

package maintenance

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Der Durchführungsauftrag entsteht erst mit dem Vorlauf (Arbeitstage), aus
// dem nächsten geplanten Termin; nie zwei offene je Plan.

func (e *jpEnv) planLead(unit string, count int, next time.Time, lead int) string {
	e.t.Helper()
	in := e.input(unit, count, next)
	in.LeadWorkdays = &lead
	id, err := e.svc.CreatePlan(e.ctx, in, e.userA)
	e.must(err)
	return id
}

func (e *jpEnv) openTasks(planID string) []*MaintenanceTask {
	e.t.Helper()
	ts, err := e.repo.FindTasks(e.ctx, TaskFilter{PlanID: planID, Open: true})
	e.must(err)
	return ts
}

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.Local)
}

func TestOrderOnLeadDay(t *testing.T) {
	e := jpNew(t)
	id := e.planLead(UnitMonth, 1, vday(2027, 2, 15), 5) // Mo 15.02.2027
	if n := len(e.openTasks(id)); n != 0 {
		t.Fatalf("Anlegen weit vor dem Vorlauf: %d Aufträge", n)
	}
	_, err := e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 7, 23, 0))
	e.must(err)
	if n := len(e.openTasks(id)); n != 0 {
		t.Fatalf("am Sonntag vor dem Vorlauftag: %d Aufträge", n)
	}
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 8, 0, 30))
	e.must(err)
	ts := e.openTasks(id)
	if len(ts) != 1 || !ts[0].DueDate.Local().Equal(vday(2027, 2, 15)) || ts[0].UpcomingFrom == nil || civil(*ts[0].UpcomingFrom) != vday(2027, 2, 8) {
		t.Fatalf("am Vorlauftag: %+v", ts)
	}
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 9, 8, 0))
	e.must(err)
	if n := len(e.openTasks(id)); n != 1 {
		t.Errorf("weiterer Lauf: %d offene Aufträge", n)
	}
}

func TestOrderVorlaufZeroAndFreeDay(t *testing.T) {
	e := jpNew(t)
	id := e.planLead(UnitMonth, 1, vday(2027, 2, 27), 0) // Sa 27.02.2027
	_, err := e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 25, 12, 0))
	e.must(err)
	if n := len(e.openTasks(id)); n != 0 {
		t.Fatalf("Donnerstag: %d", n)
	}
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 26, 6, 0))
	e.must(err)
	if n := len(e.openTasks(id)); n != 1 {
		t.Errorf("Freitag vor dem Samstagstermin: %d Aufträge, erwartet 1", n)
	}
}

func TestOrderMissedDay(t *testing.T) {
	e := jpNew(t)
	past := dayStart(time.Now()).AddDate(0, 0, -2)
	id := e.planLead(UnitMonth, 1, past, 5)
	ts := e.openTasks(id)
	if len(ts) != 1 || !ts[0].DueDate.Local().Equal(past) {
		t.Fatalf("verpasster Vorlauftag: %+v", ts)
	}
}

func TestOrderFromVisit(t *testing.T) {
	e := jpNew(t)
	id := e.planLead(UnitMonth, 1, vday(2027, 3, 1), 5)
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, at(2027, 1, 10, 9, 0))
	e.must(err)
	vs := e.visits(id)
	e.must(e.repo.MoveVisit(e.ctx, vs[0].ID, vday(2027, 3, 3), e.userB, at(2027, 1, 10, 9, 0)))
	e.must(e.repo.StaffVisit(e.ctx, vs[0].ID, &e.userB, nil, e.userB))
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 24, 8, 0)) // 5 Arbeitstage vor Mi 03.03.
	e.must(err)
	ts := e.openTasks(id)
	if len(ts) != 1 {
		t.Fatalf("Aufträge: %d", len(ts))
	}
	tk := ts[0]
	if !tk.DueDate.Local().Equal(vday(2027, 3, 3)) || tk.PlannedFor == nil || civil(*tk.PlannedFor) != vday(2027, 3, 1) ||
		!tk.DueManual || !tk.StaffManual || tk.AssignedTo == nil || *tk.AssignedTo != e.userB {
		t.Errorf("Auftrag aus verschobenem, besetztem Termin: %+v", tk)
	}
	v, err := e.repo.GetVisit(e.ctx, vs[0].ID)
	e.must(err)
	if v.State != VisitOrdered || v.TaskID == nil || *v.TaskID != tk.ID {
		t.Errorf("Termin nach dem Beauftragen: %+v", v)
	}
}

// Review Focus 3: zwei Termine auf denselben Tag gezogen.
func TestOrderSwappedVisits(t *testing.T) {
	e := jpNew(t)
	id := e.planLead(UnitMonth, 1, vday(2027, 3, 1), 5)
	now := at(2027, 1, 10, 9, 0)
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, now)
	e.must(err)
	vs := e.visits(id)
	t1, t2 := vs[0], vs[1]
	e.must(e.repo.MoveVisit(e.ctx, t2.ID, vday(2027, 2, 22), e.userB, now))
	e.must(e.repo.MoveVisit(e.ctx, t1.ID, vday(2027, 2, 22), e.userB, now))
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 15, 8, 0))
	e.must(err)
	ts := e.openTasks(id)
	if len(ts) != 1 || ts[0].PlannedFor == nil || civil(*ts[0].PlannedFor) != vday(2027, 3, 1) {
		t.Fatalf("genau ein Auftrag aus T1 erwartet: %+v", ts)
	}
	if v, _ := e.repo.GetVisit(e.ctx, t2.ID); v.State != VisitPlanned || !v.CalcDay.Equal(vday(2027, 4, 1)) {
		t.Errorf("T2: %+v", v)
	}
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 16, 8, 0))
	e.must(err)
	if n := len(e.openTasks(id)); n != 1 {
		t.Errorf("zweiter offener Auftrag: %d", n)
	}
}

// Entscheidung 1: Rhythmus ab dem gerechneten Tag.
func TestFixedRhythmFromCalcDay(t *testing.T) {
	e := jpNew(t)
	start := dayStart(time.Now()).AddDate(0, 0, 3)
	id := e.planLead(UnitMonth, 1, start, 5)
	ts := e.openTasks(id)
	if len(ts) != 1 {
		t.Fatalf("Aufträge: %d", len(ts))
	}
	e.must(e.repo.UpdateDueDate(e.ctx, ts[0].ID, start.AddDate(0, 0, 9)))
	_, err := e.repo.ScheduleNext(e.ctx, ts[0].ID, e.userA, true)
	e.must(err)
	p, err := e.repo.GetPlan(e.ctx, id)
	e.must(err)
	if want := AddInterval(UnitMonth, 1, start); !dayStart(p.NextDueAt).Equal(want) {
		t.Errorf("nächster Termin %v, erwartet %v (ab gerechnetem Tag)", p.NextDueAt.Local(), want)
	}
}

// Entscheidung 2: verspätet erledigt → vergangene Termine entfallen.
func TestLateCompletionDrops(t *testing.T) {
	e := jpNew(t)
	start := dayStart(time.Now()).AddDate(0, 0, -21)
	id := e.planLead(UnitWeek, 1, start, 5)
	_, err := e.repo.db.Exec(e.ctx, `UPDATE maintenance_plans SET year_plan_at = NOW() WHERE id = $1::uuid`, id)
	e.must(err)
	_, err = e.repo.db.Exec(e.ctx, `INSERT INTO maintenance_visits (plan_id, calc_day, day) SELECT $1::uuid, d::date, d::date
		FROM generate_series($2::date + 7, $2::date + 70, interval '7 days') d`, id, start.Format("2006-01-02"))
	e.must(err)
	ts := e.openTasks(id)
	if len(ts) != 1 {
		t.Fatalf("Aufträge: %d", len(ts))
	}
	_, err = e.repo.db.Exec(e.ctx, `UPDATE maintenance_tasks SET status = 'done', completed_at = NOW() WHERE id = $1::uuid`, ts[0].ID)
	e.must(err)
	_, err = e.repo.ScheduleNext(e.ctx, ts[0].ID, e.userA, true)
	e.must(err)
	today := dayStart(time.Now())
	for _, v := range e.visits(id) {
		if v.Day.Before(today) && v.State != VisitDropped {
			t.Errorf("vergangener Termin %v: %s, erwartet entfallen", v.Day, v.State)
		}
	}
	if open := e.openTasks(id); len(open) > 1 || (len(open) == 1 && open[0].DueDate.Local().Before(today)) {
		t.Errorf("kein Rückstau erwartet, offene Aufträge: %+v", open)
	}
}

func TestShortRhythmOneOpen(t *testing.T) {
	e := jpNew(t)
	id := e.planLead(UnitDay, 1, dayStart(time.Now()), 5)
	ts := e.openTasks(id)
	if len(ts) != 1 {
		t.Fatalf("Aufträge: %d", len(ts))
	}
	_, err := e.repo.db.Exec(e.ctx, `UPDATE maintenance_tasks SET status = 'done', completed_at = NOW() WHERE id = $1::uuid`, ts[0].ID)
	e.must(err)
	_, err = e.repo.ScheduleNext(e.ctx, ts[0].ID, e.userA, true)
	e.must(err)
	if n := len(e.openTasks(id)); n != 1 {
		t.Errorf("täglich mit Vorlauf 5: %d offene Aufträge nach dem Erledigen, erwartet 1", n)
	}
}

func TestAllPathsWaitForLead(t *testing.T) {
	e := jpNew(t)
	far := dayStart(time.Now()).AddDate(0, 0, 30)
	id := e.planLead(UnitMonth, 1, far, 5)
	in := e.input(UnitMonth, 1, far)
	lead := 5
	in.LeadWorkdays = &lead
	e.must(e.svc.UpdatePlan(e.ctx, id, in, e.userA))
	dup, err := e.svc.DuplicatePlan(e.ctx, id, e.userA)
	e.must(err)
	e.must(e.repo.SetPlanActive(e.ctx, id, false))
	e.must(e.repo.SetPlanActive(e.ctx, id, true))
	e.must(e.svc.EnsurePlanTask(e.ctx, id, e.userA))
	_, err = e.svc.GenerateTasks(e.ctx, e.userA)
	e.must(err)
	if n := len(e.openTasks(id)) + len(e.openTasks(dup)); n != 0 {
		t.Errorf("Termin in 30 Tagen: %d Aufträge angelegt", n)
	}
	soon := e.planLead(UnitMonth, 1, dayStart(time.Now()).AddDate(0, 0, 1), 5)
	if n := len(e.openTasks(soon)); n != 1 {
		t.Errorf("Termin morgen: %d Aufträge, erwartet 1", n)
	}
}

func TestUpdatePlanKeepsManual(t *testing.T) {
	e := jpNew(t)
	soon := dayStart(time.Now()).AddDate(0, 0, 2)
	id := e.planLead(UnitMonth, 1, soon, 5)
	ts := e.openTasks(id)
	if len(ts) != 1 {
		t.Fatalf("Aufträge: %d", len(ts))
	}
	e.must(e.repo.UpdateDueDate(e.ctx, ts[0].ID, soon.AddDate(0, 0, 1)))
	_, err := e.repo.db.Exec(e.ctx, `UPDATE maintenance_tasks SET assigned_to = $2::uuid, staff_manual = true WHERE id = $1::uuid`, ts[0].ID, e.userB)
	e.must(err)
	in := e.input(UnitMonth, 1, soon.AddDate(0, 0, 4))
	in.Name = "Neuer Name"
	other := e.userA
	in.AssignedTo = &other
	lead := 5
	in.LeadWorkdays = &lead
	e.must(e.svc.UpdatePlan(e.ctx, id, in, e.userA))
	tk, err := e.repo.GetTaskByID(e.ctx, ts[0].ID)
	e.must(err)
	if tk.Title != "Neuer Name" || !tk.DueDate.Local().Equal(soon.AddDate(0, 0, 1)) || tk.AssignedTo == nil || *tk.AssignedTo != e.userB {
		t.Errorf("Handänderungen überschrieben: Titel %q, fällig %v, Person %v", tk.Title, tk.DueDate.Local(), tk.AssignedTo)
	}
}

func TestCreateTaskLinksVisit(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2027, 3, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, at(2027, 1, 10, 9, 0))
	e.must(err)
	vs := e.visits(id)
	tk := &MaintenanceTask{PlanID: &id, Title: "von Hand", Type: PlanInspection, InfrastructureID: e.infra, Priority: PrioMedium,
		DueDate: vday(2027, 3, 1), CreatedBy: e.userA}
	e.must(e.repo.CreateTask(e.ctx, tk))
	if v, _ := e.repo.GetVisit(e.ctx, vs[0].ID); v.State != VisitOrdered || v.TaskID == nil || *v.TaskID != tk.ID {
		t.Errorf("Termin am selben Tag: %+v", v)
	}
	tk2 := &MaintenanceTask{PlanID: &id, Title: "anderer Tag", Type: PlanInspection, InfrastructureID: e.infra, Priority: PrioMedium,
		DueDate: vday(2027, 3, 20), CreatedBy: e.userA}
	e.must(e.repo.CreateTask(e.ctx, tk2))
	for _, v := range e.visits(id) {
		if v.TaskID != nil && *v.TaskID == tk2.ID {
			t.Error("Auftrag an einem anderen Tag wurde mit einem Termin verknüpft")
		}
	}
}

func TestPlanTaskCreatedHook(t *testing.T) {
	e := jpNew(t)
	var calls atomic.Int32
	var gotTask atomic.Value
	OnPlanTaskCreated(func(_ context.Context, taskID, actorID string) {
		calls.Add(1)
		gotTask.Store(taskID + "|" + actorID)
	})
	id := e.planLead(UnitMonth, 1, vday(2027, 2, 15), 5)
	before := calls.Load()
	_, err := e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 1, 8, 0))
	e.must(err)
	if calls.Load() != before {
		t.Error("kein Auftrag, trotzdem Hook")
	}
	_, err = e.repo.EnsurePlanTasksAt(e.ctx, at(2027, 2, 8, 8, 0))
	e.must(err)
	ts := e.openTasks(id)
	if calls.Load() != before+1 || len(ts) != 1 || gotTask.Load() != ts[0].ID+"|" {
		t.Errorf("Hook: %d Aufrufe, zuletzt %v", calls.Load()-before, gotTask.Load())
	}
}

func TestNextTermForTask(t *testing.T) {
	e := jpNew(t)
	start := dayStart(time.Now()).AddDate(0, 0, 2)
	id := e.planLead(UnitMonth, 1, start, 5)
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, time.Now())
	e.must(err)
	ts := e.openTasks(id)
	if len(ts) != 1 {
		t.Fatalf("Aufträge: %d", len(ts))
	}
	_, err = e.repo.db.Exec(e.ctx, `UPDATE maintenance_tasks SET status = 'done', completed_at = NOW() WHERE id = $1::uuid`, ts[0].ID)
	e.must(err)
	_, err = e.repo.ScheduleNext(e.ctx, ts[0].ID, e.userA, true)
	e.must(err)
	nt := e.repo.NextTermForTask(e.ctx, ts[0].ID, time.Now())
	want := AddInterval(UnitMonth, 1, start)
	if nt == nil || !nt.Day.Equal(want) || nt.TaskID != "" || nt.OrderOn.IsZero() || nt.OrderOn.After(want) {
		t.Errorf("nächster Termin: %+v, erwartet %v ohne Auftrag", nt, want)
	}
}
