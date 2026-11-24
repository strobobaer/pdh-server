//go:build integration

package maintenance

import (
	"testing"
	"time"
)

// Befunde der Schlussprüfung (C1, I1, I3): Planänderung mit offenem Auftrag,
// Ruhen/Löschen bei beauftragtem Termin, Zeitumstellung, „ab Durchführung“.

func (e *jpEnv) openTask(planID string) (id string, plannedFor time.Time, ok bool) {
	e.t.Helper()
	err := e.repo.db.QueryRow(e.ctx, `SELECT id::text, COALESCE(planned_for, due_date::date) FROM maintenance_tasks
		WHERE plan_id = $1::uuid AND status = 'open'`, planID).Scan(&id, &plannedFor)
	return id, civil(plannedFor), err == nil
}

func today() time.Time { n := time.Now(); return vday(n.Year(), n.Month(), n.Day()) }

// C1: „Nächster Termin“ eines Plans mit Jahresplan und offenem Auftrag ändern –
// am neuen Tag darf kein zusätzlicher geplanter Termin entstehen.
func TestPlanDateChangeNoDuplicate(t *testing.T) {
	e := jpNew(t)
	start := today().AddDate(0, 0, 1)
	in := e.input(UnitMonth, 1, start)
	id, err := e.svc.CreatePlan(e.ctx, in, e.userA)
	e.must(err)
	if _, _, ok := e.openTask(id); !ok {
		t.Fatal("kein Auftrag für morgen")
	}
	_, err = e.repo.GenerateYearPlan(e.ctx, id, e.userA, time.Now())
	e.must(err)
	n := start.AddDate(0, 0, 3)
	in.NextDueAt = n.Format("2006-01-02")
	e.must(e.repo.UpdatePlan(e.ctx, id, in))
	_, pf, ok := e.openTask(id)
	if !ok || !pf.Equal(n) {
		t.Fatalf("offener Auftrag nicht auf %s: %v %s", n.Format("02.01."), ok, pf)
	}
	for _, v := range planned(e.visits(id)) {
		if v.Day.Equal(n) || v.CalcDay.Equal(n) {
			t.Errorf("zusätzlicher geplanter Termin am %s neben dem Auftrag", n.Format("02.01."))
		}
	}
}

// I1: Ruhen und Aktivieren mit einem beauftragten Termin – der Termin geht nicht verloren.
func TestPauseReactivateKeepsOrderedTerm(t *testing.T) {
	e := jpNew(t)
	start := today().AddDate(0, 0, 20)
	id := e.mkPlan(UnitMonth, 1, start)
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, time.Now())
	e.must(err)
	first := e.visits(id)[0]
	e.must(e.svc.MoveVisit(e.ctx, first.ID, today().AddDate(0, 0, 1), e.userA))
	e.must(e.svc.EnsurePlanTask(e.ctx, id, e.userA))
	if _, _, ok := e.openTask(id); !ok {
		t.Fatal("kein Auftrag nach dem Ziehen in den Vorlauf")
	}
	e.must(e.repo.SetPlanActive(e.ctx, id, false))
	e.must(e.repo.SetPlanActive(e.ctx, id, true))
	if _, _, ok := e.openTask(id); ok {
		return // Auftrag wieder da
	}
	for _, v := range planned(e.visits(id)) {
		if v.CalcDay.Equal(first.CalcDay) {
			return // Termin wieder geplant
		}
	}
	t.Errorf("Termin %s nach Ruhen/Aktivieren verloren", first.CalcDay.Format("02.01."))
}

// I1: Wird der Auftrag eines Termins gelöscht, ist der Termin wieder geplant.
func TestDeleteTaskReplansVisit(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, today().AddDate(0, 0, 20))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, time.Now())
	e.must(err)
	first := e.visits(id)[0]
	e.must(e.svc.MoveVisit(e.ctx, first.ID, today().AddDate(0, 0, 1), e.userA))
	e.must(e.svc.EnsurePlanTask(e.ctx, id, e.userA))
	taskID, _, ok := e.openTask(id)
	if !ok {
		t.Fatal("kein Auftrag")
	}
	e.must(e.repo.DeleteTask(e.ctx, taskID))
	v, err := e.repo.GetVisit(e.ctx, first.ID)
	e.must(err)
	if v.State != VisitPlanned || v.TaskID != nil {
		t.Errorf("Termin nach dem Löschen des Auftrags: %s %v", v.State, v.TaskID)
	}
}

// I3: Termin am Tag der Zeitumstellung (So 28.03.2027) wird zum Auftrag mit diesem Tag.
func TestOrderDSTDay(t *testing.T) {
	e := jpNew(t)
	dst := vday(2027, 3, 28)
	id := e.mkPlan(UnitMonth, 1, dst)
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, time.Now())
	e.must(err)
	taskID, err := e.repo.ensureTaskForPlan(e.ctx, id, "", time.Date(2027, 3, 26, 9, 0, 0, 0, time.Local))
	e.must(err)
	if taskID == "" {
		t.Fatal("kein Auftrag zwei Tage vor dem Termin")
	}
	var due, pf time.Time
	e.must(e.repo.db.QueryRow(e.ctx, `SELECT due_date, planned_for FROM maintenance_tasks WHERE id = $1::uuid`, taskID).Scan(&due, &pf))
	if d := due.In(time.Local).Format("2006-01-02"); d != "2027-03-28" {
		t.Errorf("Fälligkeit: %s, erwartet 2027-03-28", d)
	}
	if civil(pf).Format("2006-01-02") != "2027-03-28" {
		t.Errorf("planned_for: %s", pf)
	}
}

// I3: „ab Durchführung“ – nach dem Abschluss rechnet PDH die Termine ab dem Erledigt-Tag neu.
func TestCompletionModeRecalc(t *testing.T) {
	e := jpNew(t)
	in := e.input(UnitMonth, 1, today().AddDate(0, 0, 1))
	in.ScheduleMode = ScheduleFromCompletion
	id, err := e.svc.CreatePlan(e.ctx, in, e.userA)
	e.must(err)
	_, err = e.repo.GenerateYearPlan(e.ctx, id, e.userA, time.Now())
	e.must(err)
	taskID, _, ok := e.openTask(id)
	if !ok {
		t.Fatal("kein Auftrag")
	}
	_, err = e.repo.db.Exec(e.ctx, `UPDATE maintenance_tasks SET status = 'done', completed_at = NOW() WHERE id = $1::uuid`, taskID)
	e.must(err)
	_, err = e.repo.ScheduleNext(e.ctx, taskID, e.userA, true)
	e.must(err)
	want := AddInterval(UnitMonth, 1, today())
	ps := planned(e.visits(id))
	if len(ps) == 0 || !ps[0].CalcDay.Equal(want) {
		got := "keiner"
		if len(ps) > 0 {
			got = ps[0].CalcDay.Format("02.01.2006")
		}
		t.Errorf("erster geplanter Termin nach dem Abschluss: %s, erwartet %s", got, want.Format("02.01.2006"))
	}
}
