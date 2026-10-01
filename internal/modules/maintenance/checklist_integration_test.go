//go:build integration

// Integrationstest gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration ./internal/modules/maintenance/
package maintenance

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestChecklistAndPlanResponsibleIntegration(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	repo := NewRepository(pool)
	suffix := time.Now().Format("150405.000000")

	var userID, infraID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name)
		VALUES ($1::text, $1::text || '@x', 'x', 'Eva', 'Prüf') RETURNING id::text`, "it"+suffix).Scan(&userID))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Presse '||$1::text, 'plant') RETURNING id::text`, suffix).Scan(&infraID))

	// Plan mit Verantwortlichem; Auftrag uebernimmt ihn
	plan := &MaintenancePlan{Name: "Plan " + suffix, Type: "preventive", InfrastructureID: infraID, Interval: IntervalMonthly,
		IntervalDays: 30, Priority: PrioMedium, NextDueAt: time.Now(), CreatedBy: userID, ResponsibleTo: &userID}
	must(repo.CreatePlan(ctx, plan))
	got, err := repo.GetPlanByID(ctx, plan.ID)
	must(err)
	if got.ResponsibleTo == nil || *got.ResponsibleTo != userID || got.ResponsibleName != "Eva Prüf" {
		t.Fatalf("Verantwortlicher am Plan: %+v", got)
	}
	plans, err := repo.ListPlans(ctx, infraID)
	must(err)
	if len(plans) != 1 || plans[0].ResponsibleName != "Eva Prüf" {
		t.Fatalf("ListPlans: %+v", plans)
	}
	task := &MaintenanceTask{PlanID: &plan.ID, Title: plan.Name, Type: "preventive", InfrastructureID: infraID,
		Priority: PrioMedium, DueDate: time.Now(), CreatedBy: userID}
	must(repo.CreateTask(ctx, task))
	var taskResp *string
	must(pool.QueryRow(ctx, `SELECT responsible_to::text FROM maintenance_tasks WHERE id=$1`, task.ID).Scan(&taskResp))
	if taskResp == nil || *taskResp != userID {
		t.Fatalf("Auftrag hat Verantwortlichen nicht übernommen: %v", taskResp)
	}

	// Vorlage: Messwert mit Soll/Min/Max, Pflicht-Checkbox, Freitext
	tpl, err := repo.CreateChecklistTemplate(ctx, "Vorlage "+suffix, "", userID)
	must(err)
	five, six, seven := 5.0, 6.0, 7.0
	measure := &ChecklistTemplateItem{TemplateID: tpl.ID, Label: "Druck", ItemType: "number", Required: true, IntervalDays: 1,
		SortOrder: 1, Unit: "bar", TargetValue: &six, MinValue: &five, MaxValue: &seven}
	check := &ChecklistTemplateItem{TemplateID: tpl.ID, Label: "Sichtprüfung", ItemType: "checkbox", Required: true, IntervalDays: 1, SortOrder: 2}
	text := &ChecklistTemplateItem{TemplateID: tpl.ID, Label: "Bemerkung", ItemType: "text", IntervalDays: 1, SortOrder: 3}
	for _, it := range []*ChecklistTemplateItem{measure, check, text} {
		must(repo.CreateChecklistTemplateItem(ctx, it))
	}
	// Darstellungsbild am Messpunkt
	must(exec(ctx, pool, `INSERT INTO attachments (ref_type, ref_id, filename, filepath, mimetype, created_by)
		VALUES ($1, $2, 'skizze.png', 'maint_check_item/x/1.png', 'image/png', $3)`, RefChecklistItemImage, measure.ID, userID))
	items, err := repo.ListChecklistTemplateItems(ctx, tpl.ID)
	must(err)
	if len(items) != 3 || items[0].Unit != "bar" || items[0].MinValue == nil || *items[0].MinValue != 5 || len(items[0].Images) != 1 || items[0].Images[0].URL != "/uploads/maint_check_item/x/1.png" {
		t.Fatalf("Vorlagenpunkte: %+v", items[0])
	}
	if items[1].TargetValue != nil || items[1].Images == nil {
		t.Fatalf("Checkbox ohne Vorgaben erwartet, Images nicht nil: %+v", items[1])
	}
	must(repo.AssignChecklistTemplatesToPlan(ctx, plan.ID, []string{tpl.ID}, 30))

	due, err := repo.DueChecklistItemsForTask(ctx, task.ID)
	must(err)
	if len(due) != 3 || due[0].MaxValue == nil || *due[0].MaxValue != 7 || len(due[0].RefImages) != 1 {
		t.Fatalf("fällige Punkte: %+v", due)
	}

	// Dokumentationsfoto vor dem Abschluss
	resID, err := repo.EnsureTaskChecklistResult(ctx, task.ID, measure.ID)
	must(err)
	again, err := repo.EnsureTaskChecklistResult(ctx, task.ID, measure.ID)
	must(err)
	if again != resID {
		t.Fatal("EnsureTaskChecklistResult muss dieselbe Zeile liefern")
	}
	must(exec(ctx, pool, `INSERT INTO attachments (ref_type, ref_id, filename, filepath, mimetype, created_by)
		VALUES ($1, $2, 'foto.jpg', 'maint_check_result/x/2.jpg', 'image/jpeg', $3)`, RefChecklistResultImage, resID, userID))

	// Pflichtpunkt fehlt -> Fehler, nichts gespeichert
	err = repo.SaveTaskChecklistResults(ctx, task.ID, userID,
		map[string]string{measure.ID: "8,5", check.ID: "", text.ID: ""}, map[string]bool{check.ID: false})
	if err == nil || !strings.Contains(err.Error(), "Sichtprüfung") {
		t.Fatalf("fehlender Pflichtpunkt nicht erkannt: %v", err)
	}
	// keine Zahl
	err = repo.SaveTaskChecklistResults(ctx, task.ID, userID, map[string]string{measure.ID: "abc"}, nil)
	if err == nil || !strings.Contains(err.Error(), "keine Zahl") {
		t.Fatalf("ungültige Zahl nicht erkannt: %v", err)
	}
	must(repo.SaveTaskChecklistResults(ctx, task.ID, userID,
		map[string]string{measure.ID: "8,5", check.ID: "erledigt", text.ID: "leichte Leckage"}, map[string]bool{check.ID: true}))

	proto, err := repo.TaskChecklistResults(ctx, task.ID)
	must(err)
	if len(proto) != 3 {
		t.Fatalf("Protokoll: %d Einträge", len(proto))
	}
	m := proto[0]
	if m.Value != "8,5" || m.InRange == nil || *m.InRange || len(m.DocImages) != 1 || len(m.RefImages) != 1 || m.CheckedBy != "Eva Prüf" {
		t.Fatalf("Messwert im Protokoll: %+v", m)
	}
	if !proto[1].Done || proto[2].Value != "leichte Leckage" {
		t.Fatalf("Checkbox/Freitext: %+v / %+v", proto[1], proto[2])
	}
	// erledigt -> heute nicht mehr faellig
	due, err = repo.DueChecklistItemsForTask(ctx, task.ID)
	must(err)
	if len(due) != 0 {
		t.Fatalf("nach Erledigung noch %d fällig", len(due))
	}
}

func exec(ctx context.Context, pool *pgxpool.Pool, sql string, args ...interface{}) error {
	_, err := pool.Exec(ctx, sql, args...)
	return err
}
