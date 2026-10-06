//go:build integration

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Kontrollrundgang: Bereich mit drei Anlagen, gleiche Checkliste an mehreren
// Stationen, eine Station nur monatlich. Schritte tragen ihre Station, der
// Abschluss schreibt den Takt je Station fort.
func TestRoundIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	must := mustT(t)
	repo := NewRepository(pool)
	svc := NewService(repo)
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var user, hall string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text||'@x', 'x', 'Rudi', 'Rundgang', '', '') RETURNING id::text`, "rg"+sfx).Scan(&user))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Halle '||$1::text, 'building') RETURNING id::text`, sfx).Scan(&hall))
	machine := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'device', $2::uuid) RETURNING id::text`, name+" "+sfx, hall).Scan(&id))
		return id
	}
	press, saw, crane := machine("Presse"), machine("Säge"), machine("Kran")

	tpl, err := repo.CreateTemplate(ctx, "Sichtkontrolle "+sfx, "", user)
	must(err)
	_, err = repo.AddTemplateItem(ctx, tpl.ID, &ChecklistItemInput{Label: "Leckagen", ItemType: "checkbox", Required: true})
	must(err)
	_, err = repo.AddTemplateItem(ctx, tpl.ID, &ChecklistItemInput{Label: "Schutzeinrichtungen", ItemType: "checkbox", Required: true})
	must(err)
	craneTpl, err := repo.CreateTemplate(ctx, "Kranprüfung "+sfx, "", user)
	must(err)
	_, err = repo.AddTemplateItem(ctx, craneTpl.ID, &ChecklistItemInput{Label: "Seil", ItemType: "checkbox", Required: true})
	must(err)

	// ohne Station kein Rundgang
	if _, err := svc.CreatePlan(ctx, &PlanInput{Name: "leer", InfrastructureID: hall, IsRound: true}, user); !IsInputError(err) {
		t.Fatalf("Rundgang ohne Station: %v", err)
	}
	// dieselbe Station doppelt → abgelehnt
	if _, err := svc.CreatePlan(ctx, &PlanInput{Name: "doppelt", InfrastructureID: hall, IsRound: true,
		Checklists: []PlanChecklistInput{{TemplateID: tpl.ID, InfrastructureID: press}, {TemplateID: tpl.ID, InfrastructureID: press}}}, user); !IsInputError(err) {
		t.Fatalf("doppelte Station: %v", err)
	}

	planID, err := svc.CreatePlan(ctx, &PlanInput{Name: "Schichtrundgang " + sfx, InfrastructureID: hall, IsRound: true,
		IntervalUnit: UnitWeek, IntervalCount: 1, NextDueAt: time.Now().Format("2006-01-02"), Checklists: []PlanChecklistInput{
			{TemplateID: tpl.ID, InfrastructureID: press},
			{TemplateID: tpl.ID, InfrastructureID: saw},
			{TemplateID: craneTpl.ID, InfrastructureID: crane, RhythmUnit: UnitMonth, RhythmCount: 1}}}, user)
	must(err)
	plan, err := repo.GetPlan(ctx, planID)
	must(err)
	if !plan.IsRound || len(plan.Checklists) != 3 || plan.Checklists[1].InfraName != "Säge "+sfx || plan.OpenTaskID == "" {
		t.Fatalf("Plan: round=%v Stationen=%d offen=%q", plan.IsRound, len(plan.Checklists), plan.OpenTaskID)
	}
	// normaler Plan ignoriert Stationsangaben
	normalID, err := svc.CreatePlan(ctx, &PlanInput{Name: "normal " + sfx, InfrastructureID: press,
		Checklists: []PlanChecklistInput{{TemplateID: tpl.ID, InfrastructureID: saw}}}, user)
	must(err)
	normal, err := repo.GetPlan(ctx, normalID)
	must(err)
	if normal.IsRound || normal.Checklists[0].InfrastructureID != "" {
		t.Fatalf("normaler Plan mit Station: %+v", normal.Checklists[0])
	}

	steps, err := svc.Steps(ctx, plan.OpenTaskID)
	must(err)
	if len(steps) != 5 || steps[0].StationName != "Presse "+sfx || steps[2].StationName != "Säge "+sfx || steps[4].StationInfraID != crane {
		t.Fatalf("Schritte: %d, %q / %q / %q", len(steps), steps[0].StationName, steps[2].StationName, steps[4].StationInfraID)
	}
	if steps[0].GroupName() != "Presse "+sfx+" · Sichtkontrolle "+sfx {
		t.Fatalf("Gruppe: %q", steps[0].GroupName())
	}
	for _, s := range steps {
		_, err := svc.SaveStep(ctx, plan.OpenTaskID, s.ID, "", true, user)
		must(err)
	}
	_, err = svc.AddAction(ctx, plan.OpenTaskID, "Rundgang ohne Befund", user)
	must(err)
	must(svc.Complete(ctx, plan.OpenTaskID, user, &CompleteTaskInput{NoPartsNeeded: true}))

	// Folgerundgang in einer Woche: Kran (monatlich) noch nicht faellig
	plan, err = repo.GetPlan(ctx, planID)
	must(err)
	next, err := svc.Steps(ctx, plan.OpenTaskID)
	must(err)
	if len(next) != 4 {
		t.Fatalf("Folgerundgang: %d Schritte, erwartet 4 (ohne Kran)", len(next))
	}
	for _, s := range next {
		if s.StationInfraID == crane {
			t.Fatal("Kran im Folgerundgang, obwohl nur monatlich")
		}
	}

	// Station umsortieren/entfernen: Saege raus, Reihenfolge Kran vor Presse
	must(repo.UpdatePlan(ctx, planID, &PlanInput{Name: plan.Name, InfrastructureID: hall, IsRound: true, IntervalUnit: UnitWeek, IntervalCount: 1,
		Checklists: []PlanChecklistInput{{TemplateID: craneTpl.ID, InfrastructureID: crane, RhythmUnit: UnitMonth, RhythmCount: 1},
			{TemplateID: tpl.ID, InfrastructureID: press}}}))
	plan, err = repo.GetPlan(ctx, planID)
	must(err)
	if len(plan.Checklists) != 2 || plan.Checklists[0].InfrastructureID != crane || plan.Checklists[0].LastDoneAt == nil {
		t.Fatalf("nach Änderung: %d Stationen, erste %q, zuletzt %v", len(plan.Checklists), plan.Checklists[0].InfraName, plan.Checklists[0].LastDoneAt)
	}
}
