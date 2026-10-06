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
	if len(due) != 3 || due[0].MaxValue == nil || *due[0].MaxValue != 7 || len(due[0].RefImages) != 1 ||
		due[0].TemplateID != tpl.ID || due[0].TemplateName != tpl.Name || !due[0].Assigned {
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
	// am selben Auftrag bleiben die Punkte samt Werten sichtbar (schrittweises Abarbeiten)
	due, err = repo.DueChecklistItemsForTask(ctx, task.ID)
	must(err)
	if len(due) != 3 || due[0].Value != "8,5" || !due[1].Done {
		t.Fatalf("eigene Ergebnisse am Auftrag: %+v", due)
	}
	// erledigt -> fuer den naechsten Auftrag heute nicht mehr faellig
	next := &MaintenanceTask{PlanID: &plan.ID, Title: plan.Name, Type: "preventive", InfrastructureID: infraID,
		Priority: PrioMedium, DueDate: time.Now(), CreatedBy: userID}
	must(repo.CreateTask(ctx, next))
	due, err = repo.DueChecklistItemsForTask(ctx, next.ID)
	must(err)
	if len(due) != 0 {
		t.Fatalf("nach Erledigung noch %d fällig", len(due))
	}
	// Folgeauftrag des Plans mit spaeterem Termin: Punkte sind zum Termin wieder faellig
	later := &MaintenanceTask{PlanID: &plan.ID, Title: plan.Name, Type: "preventive", InfrastructureID: infraID,
		Priority: PrioMedium, DueDate: time.Now().AddDate(0, 0, 30), CreatedBy: userID}
	must(repo.CreateTask(ctx, later))
	if due, err = repo.DueChecklistItemsForTask(ctx, later.ID); err != nil || len(due) != 3 {
		t.Fatalf("Folgeauftrag in 30 Tagen: %d fällig / %v", len(due), err)
	}

	// Auftrag ohne Plan: ausgewaehlte Checkliste wird abgearbeitet und bleibt danach haengen
	tpl2, err := repo.CreateChecklistTemplate(ctx, "Sonder "+suffix, "", userID)
	must(err)
	extra := &ChecklistTemplateItem{TemplateID: tpl2.ID, Label: "Öl prüfen", ItemType: "checkbox", IntervalDays: 1, SortOrder: 1}
	must(repo.CreateChecklistTemplateItem(ctx, extra))
	adhoc := &MaintenanceTask{Title: "Sonder " + suffix, Type: "inspection", InfrastructureID: infraID,
		Priority: PrioMedium, DueDate: time.Now(), CreatedBy: userID}
	must(repo.CreateTask(ctx, adhoc))
	if due, err = repo.DueChecklistItemsForTask(ctx, adhoc.ID); err != nil || len(due) != 0 {
		t.Fatalf("ohne Plan und Auswahl: %d / %v", len(due), err)
	}
	due, err = repo.DueChecklistItemsForTask(ctx, adhoc.ID, tpl2.ID)
	must(err)
	if len(due) != 1 || due[0].TemplateID != tpl2.ID || due[0].Assigned {
		t.Fatalf("ausgewählte Checkliste: %+v", due)
	}
	must(repo.SaveTaskChecklistResults(ctx, adhoc.ID, userID, map[string]string{extra.ID: "erledigt"}, map[string]bool{extra.ID: true}))
	if due, err = repo.DueChecklistItemsForTask(ctx, adhoc.ID); err != nil || len(due) != 1 || !due[0].Done {
		t.Fatalf("bearbeitete Checkliste bleibt am Auftrag: %+v / %v", due, err)
	}
}

func exec(ctx context.Context, pool *pgxpool.Pool, sql string, args ...interface{}) error {
	_, err := pool.Exec(ctx, sql, args...)
	return err
}
