//go:build integration

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Ein Plan mit drei Checklisten (immer / alle 2 Wochen / monatlich):
// Zusammenfuehren, Pflichtpunkte, Abschluss, Takt, Folgeauftrag, Ueberspringen,
// Warten, Annehmen, ein offener Auftrag je Plan.
func TestServiceFlowIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	repo := NewRepository(pool)
	svc := NewService(repo)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var user, infra string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text||'@x', 'x', 'Tina', 'Test', '', '') RETURNING id::text`, "sv"+sfx).Scan(&user))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Linie '||$1::text, 'plant') RETURNING id::text`, sfx).Scan(&infra))

	mkList := func(name string, items ...string) string {
		t.Helper()
		tpl, err := repo.CreateTemplate(ctx, name+" "+sfx, "", user)
		must(err)
		for _, l := range items {
			typ := "checkbox"
			in := &ChecklistItemInput{Label: l, ItemType: typ, Required: true}
			if strings.HasPrefix(l, "#") {
				min, max := 5.0, 7.0
				in = &ChecklistItemInput{Label: l[1:], ItemType: "number", Required: true, Unit: "bar", MinValue: &min, MaxValue: &max}
			}
			_, err := repo.AddTemplateItem(ctx, tpl.ID, in)
			must(err)
		}
		return tpl.ID
	}
	always := mkList("Täglich", "Sichtprüfung", "#Druck")
	twoWeeks := mkList("Zweiwöchentlich", "Filter reinigen")
	monthly := mkList("Monatlich", "Keilriemen prüfen", "Schmieren")

	planID, err := svc.CreatePlan(ctx, &PlanInput{Name: "Linie " + sfx, InfrastructureID: infra, IntervalUnit: UnitWeek, IntervalCount: 1,
		NextDueAt: time.Now().Format("2006-01-02"), EstimatedMin: 40, Checklists: []PlanChecklistInput{
			{TemplateID: always}, {TemplateID: twoWeeks, RhythmUnit: UnitWeek, RhythmCount: 2}, {TemplateID: monthly, RhythmUnit: UnitMonth, RhythmCount: 1}}}, user)
	must(err)
	openTasks := func() []*MaintenanceTask {
		ts, err := repo.FindTasks(ctx, TaskFilter{PlanID: planID, Open: true})
		must(err)
		return ts
	}
	tasks := openTasks()
	if len(tasks) != 1 {
		t.Fatalf("nach Anlegen %d offene Aufträge, erwartet 1", len(tasks))
	}
	task := tasks[0]

	// erste Durchfuehrung: noch nie erledigt → alle drei Listen zusammengefuehrt, in Plan-Reihenfolge
	steps, err := svc.Steps(ctx, task.ID)
	must(err)
	if len(steps) != 5 || steps[0].ChecklistName != "Täglich "+sfx || steps[4].ChecklistName != "Monatlich "+sfx {
		t.Fatalf("zusammengeführt: %d Schritte, erste %q, letzte %q", len(steps), steps[0].ChecklistName, steps[len(steps)-1].ChecklistName)
	}

	// Pflichtpunkte fehlen → kein Abschluss
	_, err = svc.AddAction(ctx, task.ID, "Linie geprüft", user)
	must(err)
	if err := svc.Complete(ctx, task.ID, user, &CompleteTaskInput{NoPartsNeeded: true}); !IsInputError(err) || !strings.Contains(err.Error(), "Sichtprüfung") {
		t.Fatalf("fehlende Pflichtpunkte: %v", err)
	}
	if _, err := svc.SaveStep(ctx, task.ID, steps[1].ID, "abc", false, user); !IsInputError(err) {
		t.Fatalf("keine Zahl: %v", err)
	}
	for _, s := range steps {
		val := ""
		if s.ItemType == "number" {
			val = "7,8"
		}
		st, err := svc.SaveStep(ctx, task.ID, s.ID, val, true, user)
		must(err)
		if s.ItemType == "number" && (st.InRange == nil || *st.InRange) {
			t.Fatalf("7,8 bar müsste außerhalb liegen: %+v", st)
		}
	}
	// erfasst → Liste bleibt stabil, auch wenn der Plan geaendert wird
	if n, err := repo.BuildSteps(ctx, task.ID, true); err != nil || n != 5 {
		t.Fatalf("nach Erfassen neu aufgebaut: %d / %v", n, err)
	}
	must(svc.Complete(ctx, task.ID, user, &CompleteTaskInput{Notes: "ok", DurationMin: 35, NoPartsNeeded: true}))
	done, _ := svc.GetTaskByID(ctx, task.ID)
	if done.Status != TaskDone || done.DurationMin == nil || *done.DurationMin != 35 {
		t.Fatalf("abgeschlossen: %+v", done)
	}
	if err := svc.Complete(ctx, task.ID, user, &CompleteTaskInput{NoPartsNeeded: true}); !IsInputError(err) {
		t.Fatalf("doppelt abschließen: %v", err)
	}

	// Folgeauftrag in einer Woche: nur „immer“ faellig (2 Wochen / Monat noch nicht)
	tasks = openTasks()
	if len(tasks) != 1 || !dayStart(tasks[0].DueDate).Equal(AddInterval(UnitWeek, 1, time.Now())) {
		t.Fatalf("Folgeauftrag: %d / %v", len(tasks), tasks)
	}
	next := tasks[0]
	steps, err = svc.Steps(ctx, next.ID)
	must(err)
	if len(steps) != 2 {
		t.Fatalf("Folgeauftrag nach 1 Woche: %d Schritte, erwartet 2 (nur „immer“)", len(steps))
	}
	// Termin 3 Wochen spaeter: dann ist auch die 2-Wochen-Liste wieder dran
	must(svc.UpdateDueDate(ctx, next.ID, time.Now().AddDate(0, 0, 21)))
	if steps, _ = svc.Steps(ctx, next.ID); len(steps) != 3 {
		t.Fatalf("nach 3 Wochen: %d Schritte, erwartet 3", len(steps))
	}

	// Warten → wartet; Annehmen → in Arbeit; Ueberspringen → neuer Auftrag
	must(svc.Wait(ctx, next.ID, time.Now().AddDate(0, 0, 2)))
	if x, _ := svc.GetTaskByID(ctx, next.ID); x.Status != TaskPending {
		t.Fatalf("warten: %s", x.Status)
	}
	must(svc.Accept(ctx, next.ID, user))
	if x, _ := svc.GetTaskByID(ctx, next.ID); x.Status != TaskInProgress || x.AssignedTo == nil || *x.AssignedTo != user {
		t.Fatalf("annehmen: %+v", x)
	}
	nd, err := svc.Skip(ctx, next.ID, user)
	must(err)
	if nd == nil || len(openTasks()) != 1 || openTasks()[0].ID == next.ID {
		t.Fatal("überspringen: kein neuer Auftrag")
	}

	// stuendlicher Abgleich legt nichts doppelt an
	_, err = repo.EnsurePlanTasks(ctx)
	must(err)
	if n := len(openTasks()); n != 1 {
		t.Fatalf("Abgleich: %d offene Aufträge", n)
	}

	// Plan ruhen lassen: offener, unberuehrter Auftrag verschwindet; wieder aktiv → neuer Auftrag
	must(svc.SetPlanActive(ctx, planID, false))
	if n := len(openTasks()); n != 0 {
		t.Fatalf("ruhender Plan: %d offene Aufträge", n)
	}
	must(svc.SetPlanActive(ctx, planID, true))
	if n := len(openTasks()); n != 1 {
		t.Fatalf("wieder aktiv: %d offene Aufträge", n)
	}

	// Auftrag ohne Plan mit gewaehlter Checkliste
	adhoc, err := svc.CreateTask(ctx, &CreateTaskInput{Title: "Sonder " + sfx, InfrastructureID: infra, TemplateIDs: []string{monthly}}, user)
	must(err)
	if steps, _ = svc.Steps(ctx, adhoc.ID); len(steps) != 2 || steps[0].PlanChecklistID != "" {
		t.Fatalf("Sonderauftrag: %d Schritte", len(steps))
	}
}
