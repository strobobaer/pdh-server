//go:build integration

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Archiv: Filter nach Anlage (inkl. Unteranlagen), Plan, Text, Status,
// Abweichungen, Zeitraum und Sichtbarkeit; Ausfuehrende und Seiten.
func TestArchiveIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	repo := NewRepository(pool)
	svc := NewService(repo)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var user, hall, press string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text||'@x', 'x', 'Archie', 'Archiv', '', '') RETURNING id::text`, "ar"+sfx).Scan(&user))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Werk '||$1::text, 'building') RETURNING id::text`, sfx).Scan(&hall))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ('Presse '||$1::text, 'device', $2::uuid) RETURNING id::text`, sfx, hall).Scan(&press))

	tpl, err := repo.CreateTemplate(ctx, "Druckprüfung "+sfx, "", user)
	must(err)
	min, max := 5.0, 7.0
	_, err = repo.AddTemplateItem(ctx, tpl.ID, &ChecklistItemInput{Label: "Druck", ItemType: "number", Required: true, Unit: "bar", MinValue: &min, MaxValue: &max})
	must(err)
	planID, err := svc.CreatePlan(ctx, &PlanInput{Name: "Archivplan " + sfx, InfrastructureID: press, IntervalUnit: UnitWeek, IntervalCount: 1,
		NextDueAt: time.Now().Format("2006-01-02"), Checklists: []PlanChecklistInput{{TemplateID: tpl.ID}}}, user)
	must(err)

	// 1. Termin: Druck ausserhalb → Abweichung
	plan, err := repo.GetPlan(ctx, planID)
	must(err)
	first := plan.OpenTaskID
	steps, err := svc.Steps(ctx, first)
	must(err)
	_, err = svc.SaveStep(ctx, first, steps[0].ID, "8,2", false, user)
	must(err)
	_, err = svc.AddAction(ctx, first, "Druck zu hoch, Ventil nachgestellt", user)
	must(err)
	must(svc.Complete(ctx, first, user, &CompleteTaskInput{NoPartsNeeded: true}))
	// 2. Termin: uebersprungen
	plan, err = repo.GetPlan(ctx, planID)
	must(err)
	second := plan.OpenTaskID
	_, err = svc.Skip(ctx, second, user)
	must(err)

	get := func(f ArchiveFilter) ([]*ArchiveEntry, int) {
		t.Helper()
		out, total, err := repo.ArchiveTasks(ctx, f)
		must(err)
		return out, total
	}
	ids := func(es []*ArchiveEntry) string {
		var s []string
		for _, e := range es {
			s = append(s, e.Task.ID)
		}
		return strings.Join(s, ",")
	}

	es, total := get(ArchiveFilter{InfraID: hall}) // Werk → Presse (Unteranlage)
	if total != 2 || len(es) != 2 {
		t.Fatalf("Anlage inkl. Unteranlagen: %d (%s)", total, ids(es))
	}
	var done *ArchiveEntry
	for _, e := range es {
		if e.Task.ID == first {
			done = e
		}
	}
	if done == nil || done.Executors != "Archie Archiv" || done.Summary.OutOfRange != 1 || done.Summary.Total != 1 {
		t.Fatalf("Eintrag: %+v", done)
	}
	if es, _ := get(ArchiveFilter{InfraID: hall, Deviations: true}); ids(es) != first {
		t.Fatalf("nur Abweichungen: %s", ids(es))
	}
	if es, _ := get(ArchiveFilter{InfraID: hall, Status: string(TaskSkipped)}); ids(es) != second {
		t.Fatalf("nur übersprungen: %s", ids(es))
	}
	if _, n := get(ArchiveFilter{Query: "archivplan " + sfx}); n != 2 {
		t.Fatalf("Suche nach Planname (ohne Groß/klein): %d", n)
	}
	future := time.Now().AddDate(0, 0, 3)
	if _, n := get(ArchiveFilter{PlanID: planID, From: &future}); n != 0 {
		t.Fatalf("Zeitraum in der Zukunft: %d", n)
	}
	today := time.Now()
	if _, n := get(ArchiveFilter{PlanID: planID, From: &today, To: &today}); n < 1 {
		t.Fatalf("Zeitraum heute: %d", n)
	}
	if _, n := get(ArchiveFilter{PlanID: planID, OnlyIDs: []string{}}); n != 0 {
		t.Fatalf("keine Sichtbarkeit: %d", n)
	}
	if es, n := get(ArchiveFilter{PlanID: planID, OnlyIDs: []string{second}}); n != 1 || ids(es) != second {
		t.Fatalf("Sichtbarkeit eingeschränkt: %d", n)
	}
	if es, n := get(ArchiveFilter{PlanID: planID, Limit: 1, Offset: 1}); n != 2 || len(es) != 1 {
		t.Fatalf("Seite 2: %d gesamt, %d auf der Seite", n, len(es))
	}
}
