//go:build integration

package maintenance

import (
	"strings"
	"testing"
	"time"
)

// breakInserts lässt das Einfügen in table für einen Plan scheitern (wie ein
// Plan mit unbrauchbaren Daten) und räumt die Sperre nach dem Test weg.
func (e *jpEnv) breakInserts(table, planID string) {
	e.t.Helper()
	name := "jp_kaputt_" + strings.ReplaceAll(planID, "-", "")[20:]
	_, err := e.repo.db.Exec(e.ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'kaputt (Test)'; END $$`)
	e.must(err)
	e.t.Cleanup(func() {
		_, _ = e.repo.db.Exec(e.ctx, `DROP TRIGGER IF EXISTS `+name+` ON `+table)
		_, _ = e.repo.db.Exec(e.ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
	_, err = e.repo.db.Exec(e.ctx, `CREATE TRIGGER `+name+` BEFORE INSERT ON `+table+` FOR EACH ROW
		WHEN (NEW.plan_id = '`+planID+`'::uuid) EXECUTE FUNCTION `+name+`()`)
	e.must(err)
}

// Ein Plan, dessen Auftrag sich nicht anlegen lässt, darf die übrigen Pläne
// nicht aufhalten: sie bekommen ihren Auftrag, der Fehler wird gemeldet.
func TestEnsurePlanTasksSkipsBrokenPlan(t *testing.T) {
	e := jpNew(t)
	due := vday(2026, 11, 24) // morgen: im Vorlauf
	before := e.mkPlan(UnitMonth, 1, due)
	broken := e.mkPlan(UnitMonth, 1, due)
	after := e.mkPlan(UnitMonth, 1, due)
	e.breakInserts("maintenance_tasks", broken)

	_, err := e.repo.EnsurePlanTasksAt(e.ctx, e.now)
	if err == nil || !strings.Contains(err.Error(), broken) {
		t.Errorf("Fehler soll den kaputten Plan nennen: %v", err)
	}
	if len(e.openTasks(before)) != 1 || len(e.openTasks(after)) != 1 {
		t.Errorf("Pläne neben dem kaputten ohne Auftrag: davor %d, danach %d", len(e.openTasks(before)), len(e.openTasks(after)))
	}
	if len(e.openTasks(broken)) != 0 {
		t.Error("kaputter Plan hat trotzdem einen Auftrag")
	}
}

// Dasselbe beim täglichen Verlängern der Jahrespläne.
func TestExtendYearPlansSkipsBrokenPlan(t *testing.T) {
	e := jpNew(t)
	start := vday(2026, 12, 1)
	ids := []string{e.mkPlan(UnitMonth, 1, start), e.mkPlan(UnitMonth, 1, start), e.mkPlan(UnitMonth, 1, start)}
	for _, id := range ids {
		_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
		e.must(err)
	}
	e.breakInserts("maintenance_visits", ids[1])
	later := e.now.AddDate(0, 3, 0)

	_, err := e.repo.ExtendYearPlans(e.ctx, later)
	if err == nil || !strings.Contains(err.Error(), ids[1]) {
		t.Errorf("Fehler soll den kaputten Plan nennen: %v", err)
	}
	for _, id := range []string{ids[0], ids[2]} {
		if n := len(e.visits(id)); n <= 12 {
			t.Errorf("Plan %s nicht verlängert: %d Termine", id, n)
		}
	}
}

// Wird aus dem Termin ein Auftrag, während jemand ihn verschiebt oder
// besetzt, darf das nicht als gespeichert gelten.
func TestVisitChangeLosesAgainstOrder(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	vs := e.visits(id)
	race := func(visitID string, change func() error) error {
		tx, err := e.repo.db.Begin(e.ctx)
		e.must(err)
		defer tx.Rollback(e.ctx)
		_, err = tx.Exec(e.ctx, `UPDATE maintenance_visits SET state = 'ordered' WHERE id = $1::uuid`, visitID)
		e.must(err)
		done := make(chan error, 1)
		go func() { done <- change() }() // liest „geplant“, wartet dann auf die Zeile
		time.Sleep(500 * time.Millisecond)
		e.must(tx.Commit(e.ctx))
		return <-done
	}
	err = race(vs[2].ID, func() error { return e.repo.MoveVisit(e.ctx, vs[2].ID, vday(2027, 2, 3), e.userB, e.now) })
	if !IsInputError(err) || !strings.Contains(err.Error(), "Auftrag") {
		t.Errorf("verschieben nach dem Beauftragen: %v", err)
	}
	err = race(vs[3].ID, func() error { return e.repo.StaffVisit(e.ctx, vs[3].ID, &e.userB, nil, e.userB) })
	if !IsInputError(err) || !strings.Contains(err.Error(), "Auftrag") {
		t.Errorf("besetzen nach dem Beauftragen: %v", err)
	}
}
