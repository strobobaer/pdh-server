//go:build integration

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"pdh/pkg/database"
)

// jpEnv: zwei Personen, eine Gruppe, eine Anlage; „jetzt“ ist der 23.11.2026, 10 Uhr.
type jpEnv struct {
	t                          *testing.T
	repo                       *Repository
	svc                        *Service
	ctx                        context.Context
	userA, userB, group, infra string
	now                        time.Time
}

func jpNew(t *testing.T) *jpEnv {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	must(database.RunMigrations(ctx, pool, "../../../migrations"))
	e := &jpEnv{t: t, repo: NewRepository(pool), ctx: ctx, now: time.Date(2026, 11, 23, 10, 0, 0, 0, time.Local)}
	e.svc = NewService(e.repo)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	q := func(sql string, args ...any) string {
		var v string
		must(pool.QueryRow(ctx, sql, args...).Scan(&v))
		return v
	}
	user := `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text||'@x', 'x', $2, 'Plan', '', '') RETURNING id::text`
	e.userA = q(user, "jpa"+sfx, "Anna")
	e.userB = q(user, "jpb"+sfx, "Bert")
	e.group = q(`INSERT INTO user_groups (name) VALUES ('Jahresplan '||$1::text) RETURNING id::text`, sfx)
	e.infra = q(`INSERT INTO infrastructure (name, type) VALUES ('Jahresplan '||$1::text, 'plant') RETURNING id::text`, sfx)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM maintenance_tasks WHERE infrastructure_id = $1::uuid`, e.infra)
		_, _ = pool.Exec(c, `DELETE FROM maintenance_plans WHERE infrastructure_id = $1::uuid`, e.infra)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1::uuid`, e.infra)
		_, _ = pool.Exec(c, `DELETE FROM user_groups WHERE id = $1::uuid`, e.group)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1::uuid[])`, []string{e.userA, e.userB})
	})
	return e
}

func (e *jpEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *jpEnv) input(unit string, count int, next time.Time) *PlanInput {
	return &PlanInput{Name: "Jahresplan", InfrastructureID: e.infra, IntervalUnit: unit, IntervalCount: count,
		ScheduleMode: ScheduleFixed, NextDueAt: next.Format("2006-01-02"), AssignedTo: &e.userA}
}

// mkPlan legt einen Plan an und entfernt den sofort angelegten Auftrag
// (bis der Auftrag erst mit dem Vorlauf entsteht, Aufgabe 5).
func (e *jpEnv) mkPlan(unit string, count int, next time.Time) string {
	e.t.Helper()
	id, err := e.svc.CreatePlan(e.ctx, e.input(unit, count, next), e.userA)
	e.must(err)
	_, err = e.repo.db.Exec(e.ctx, `DELETE FROM maintenance_tasks WHERE plan_id = $1::uuid AND status = 'open'`, id)
	e.must(err)
	return id
}

func (e *jpEnv) visits(planID string) []Visit {
	e.t.Helper()
	vs, err := e.repo.PlanVisits(e.ctx, planID)
	e.must(err)
	return vs
}

func planned(vs []Visit) []Visit {
	var out []Visit
	for _, v := range vs {
		if v.State == VisitPlanned {
			out = append(out, v)
		}
	}
	return out
}

func vday(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }

func TestGenerateYearPlan(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	added, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	vs := e.visits(id)
	if added != 12 || len(vs) != 12 || !vs[0].Day.Equal(vday(2026, 12, 1)) || !vs[11].Day.Equal(vday(2027, 11, 1)) {
		t.Fatalf("12 Termine 01.12.2026–01.11.2027 erwartet: %d angelegt, %d da", added, len(vs))
	}
	if vs[0].AssignedTo == nil || *vs[0].AssignedTo != e.userA || vs[0].AssigneeName != "Anna Plan" || !vs[0].CalcDay.Equal(vs[0].Day) {
		t.Errorf("Termin: %+v", vs[0])
	}
	p, err := e.repo.GetPlan(e.ctx, id)
	e.must(err)
	if p.YearPlanAt == nil || p.VisitCount != 12 {
		t.Errorf("Plan: Jahresplan %v, %d Termine", p.YearPlanAt, p.VisitCount)
	}
	if again, _ := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now); again != 0 {
		t.Errorf("zweiter Aufruf legt %d an", again)
	}
	// offener Auftrag am ersten Termin: dieser Tag bekommt keinen geplanten Termin
	id2 := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	_, err = e.repo.db.Exec(e.ctx, `INSERT INTO maintenance_tasks (plan_id, title, type, infrastructure_id, priority, status, due_date, created_by)
		VALUES ($1::uuid, 'offen', 'inspection', $2::uuid, 'medium', 'open', '2026-12-01', $3::uuid)`, id2, e.infra, e.userA)
	e.must(err)
	_, err = e.repo.GenerateYearPlan(e.ctx, id2, e.userA, e.now)
	e.must(err)
	if vs := e.visits(id2); len(vs) != 11 || vs[0].Day.Equal(vday(2026, 12, 1)) {
		t.Errorf("mit offenem Auftrag am 01.12.: %d Termine", len(vs))
	}
	// ruhender Plan
	e.must(e.repo.SetPlanActive(e.ctx, id2, false))
	if _, err := e.repo.GenerateYearPlan(e.ctx, id2, e.userA, e.now); !IsInputError(err) {
		t.Errorf("ruhender Plan: %v", err)
	}
}

func TestExtendYearPlans(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	other := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1)) // ohne Jahresplan
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	if _, err := e.repo.ExtendYearPlans(e.ctx, vday(2026, 12, 1)); err != nil {
		t.Fatal(err)
	}
	if n := len(e.visits(id)); n != 12 {
		t.Errorf("noch mehr als einen Monat Vorrat: %d Termine, erwartet 12", n)
	}
	_, err = e.repo.ExtendYearPlans(e.ctx, time.Date(2027, 11, 5, 10, 0, 0, 0, time.Local))
	e.must(err)
	vs := e.visits(id)
	if len(vs) != 24 || !vs[12].Day.Equal(vday(2027, 12, 1)) || !vs[23].Day.Equal(vday(2028, 11, 1)) {
		t.Errorf("verlängert bis heute + 12 Monate: %d Termine", len(vs))
	}
	if n := len(e.visits(other)); n != 0 {
		t.Errorf("Plan ohne Jahresplan bekam %d Termine", n)
	}
}

func TestRecalcKeepsManual(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitWeek, 1, vday(2026, 12, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	vs := e.visits(id)
	moved, staffed := vs[1], vs[2] // 08.12. und 15.12.
	e.must(e.repo.MoveVisit(e.ctx, moved.ID, vday(2026, 12, 10), e.userB, e.now))
	e.must(e.repo.StaffVisit(e.ctx, staffed.ID, &e.userB, nil, e.userB))
	e.must(e.svc.UpdatePlan(e.ctx, id, e.input(UnitWeek, 2, vday(2026, 12, 1)), e.userA)) // jetzt 14-täglich

	seen := map[string]bool{}
	for _, v := range planned(e.visits(id)) {
		k := v.CalcDay.Format("2006-01-02")
		if seen[k] {
			t.Fatalf("doppelter Termin am %s", k)
		}
		seen[k] = true
		switch v.ID {
		case moved.ID:
			if !v.Day.Equal(vday(2026, 12, 10)) || !v.OffPlan {
				t.Errorf("verschobener Termin: %+v (erwartet 10.12., weicht vom Plan ab)", v)
			}
		case staffed.ID:
			if v.AssignedTo == nil || *v.AssignedTo != e.userB || v.OffPlan {
				t.Errorf("besetzter Termin: %+v", v)
			}
		default:
			if int(v.CalcDay.Sub(vday(2026, 12, 1)).Hours()/24+0.5)%14 != 0 {
				t.Errorf("Termin %s liegt nicht im 14-Tage-Raster", k)
			}
		}
	}
	if !seen["2026-12-08"] || !seen["2026-12-15"] {
		t.Error("der verschobene oder der besetzte Termin fehlt")
	}
}

func TestStaffOnlyChange(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	keep := e.visits(id)[2]
	e.must(e.repo.StaffVisit(e.ctx, keep.ID, nil, &e.group, e.userA))
	in := e.input(UnitMonth, 1, vday(2026, 12, 1))
	in.AssignedTo = &e.userB
	e.must(e.svc.UpdatePlan(e.ctx, id, in, e.userA))
	vs := e.visits(id)
	for _, v := range vs {
		switch {
		case v.ID == keep.ID:
			if v.AssignedTo != nil || v.GroupID == nil || *v.GroupID != e.group {
				t.Errorf("besetzter Termin geändert: %+v", v)
			}
		case v.AssignedTo == nil || *v.AssignedTo != e.userB:
			t.Errorf("Termin %v hat die neue Person nicht", v.Day)
		}
	}
	if len(vs) != 12 {
		t.Errorf("nur die Besetzung änderte sich, trotzdem %d Termine", len(vs))
	}
}

func TestMoveVisitRules(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	vs := e.visits(id)
	if err := e.repo.MoveVisit(e.ctx, vs[0].ID, vday(2026, 11, 20), e.userB, e.now); !IsInputError(err) || !strings.Contains(err.Error(), "Vergangenheit") {
		t.Errorf("in die Vergangenheit: %v", err)
	}
	_, err = e.repo.db.Exec(e.ctx, `UPDATE maintenance_visits SET state = 'ordered' WHERE id = $1::uuid`, vs[1].ID)
	e.must(err)
	if err := e.repo.MoveVisit(e.ctx, vs[1].ID, vday(2027, 1, 5), e.userB, e.now); !IsInputError(err) || !strings.Contains(err.Error(), "Auftrag") {
		t.Errorf("beauftragter Termin: %v", err)
	}
	e.must(e.repo.MoveVisit(e.ctx, vs[2].ID, vday(2027, 2, 3), e.userB, e.now))
	v, err := e.repo.GetVisit(e.ctx, vs[2].ID)
	e.must(err)
	if !v.DateManual || !v.Day.Equal(vday(2027, 2, 3)) || !v.CalcDay.Equal(vday(2027, 2, 1)) {
		t.Errorf("verschoben: %+v", v)
	}
	if _, err := e.repo.GetVisit(e.ctx, "00000000-0000-7000-8000-000000000009"); err != ErrVisitNotFound {
		t.Errorf("unbekannter Termin: %v", err)
	}
}

func TestResetVisit(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitWeek, 1, vday(2026, 12, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	vs := e.visits(id)
	e.must(e.repo.MoveVisit(e.ctx, vs[1].ID, vday(2026, 12, 10), e.userB, e.now))
	e.must(e.repo.ResetVisit(e.ctx, vs[1].ID, e.userB, e.now))
	found := false
	for _, v := range planned(e.visits(id)) {
		if v.CalcDay.Equal(vday(2026, 12, 8)) {
			found = v.Day.Equal(v.CalcDay) && !v.DateManual && !v.StaffManual && !v.OffPlan
		}
	}
	if !found {
		t.Error("nach „Zurück zum Plan“ steht der Termin nicht wieder am 08.12.")
	}
	// weicht vom Plan ab → entfällt beim Zurücksetzen (nach dem Zurücksetzen sind die Termine neu angelegt)
	vs = planned(e.visits(id))
	e.must(e.repo.MoveVisit(e.ctx, vs[3].ID, vday(2026, 12, 23), e.userB, e.now)) // gerechnet 22.12.
	e.must(e.svc.UpdatePlan(e.ctx, id, e.input(UnitWeek, 2, vday(2026, 12, 1)), e.userA))
	e.must(e.repo.ResetVisit(e.ctx, vs[3].ID, e.userB, e.now))
	for _, v := range planned(e.visits(id)) {
		if v.CalcDay.Equal(vday(2026, 12, 22)) {
			t.Error("Termin außerhalb des Rasters ist nach dem Zurücksetzen noch da")
		}
	}
}

func TestDeactivateRemovesPlanned(t *testing.T) {
	e := jpNew(t)
	id := e.mkPlan(UnitMonth, 1, vday(2026, 12, 1))
	_, err := e.repo.GenerateYearPlan(e.ctx, id, e.userA, e.now)
	e.must(err)
	ordered := e.visits(id)[5]
	_, err = e.repo.db.Exec(e.ctx, `UPDATE maintenance_visits SET state = 'ordered' WHERE id = $1::uuid`, ordered.ID)
	e.must(err)
	e.must(e.repo.SetPlanActive(e.ctx, id, false))
	if vs := e.visits(id); len(vs) != 1 || vs[0].ID != ordered.ID {
		t.Fatalf("ruhend: %d Termine (erwartet nur den beauftragten)", len(vs))
	}
	e.must(e.repo.SetPlanActive(e.ctx, id, true))
	if n := len(planned(e.visits(id))); n < 11 {
		t.Errorf("wieder eingeschaltet: %d geplante Termine", n)
	}
	_, err = e.repo.db.Exec(e.ctx, `DELETE FROM maintenance_tasks WHERE plan_id = $1::uuid`, id)
	e.must(err)
	_, err = e.repo.db.Exec(e.ctx, `DELETE FROM maintenance_plans WHERE id = $1::uuid`, id)
	e.must(err)
	if n := len(e.visits(id)); n != 0 {
		t.Errorf("Plan gelöscht: %d Termine übrig", n)
	}
}
