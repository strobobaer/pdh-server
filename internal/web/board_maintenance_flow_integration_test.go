//go:build integration

// Ende-zu-Ende: Wartung am Leitstand per Karte fertig melden – mit
// Checkliste im Assistenten, Statuswechsel und Wartungsprotokoll.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run BoardMaintenanceFlow ./internal/web/
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/internal/modules/maintenance"
)

func TestBoardMaintenanceFlowIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	us := users.NewService(users.NewRepository(pool), "test-secret", 1)
	mrepo := maintenance.NewRepository(pool)
	h := &Handler{db: pool, rbac: rb, users: us, maint: maintenance.NewService(mrepo), jwtSecret: "test-secret"}
	maintenance.OnTaskCompleted(h.saveMaintenanceProtocol)
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	must(os.Chdir(dir)) // Protokoll landet unter uploads/
	defer os.Chdir(cwd)

	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	card := "flow" + sfx
	var userID, infraID string
	// Techniker ohne Rechte (Rollen starten leer) aus der Instandhaltung
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone, rfid_uid)
		VALUES ($1::text, $1::text || '@x', 'x', 'Tim', 'Technik', 'technician', 'Instandhaltung', '', $2) RETURNING id::text`, "fl"+sfx, card).Scan(&userID))
	must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Presse '||$1::text, 'plant') RETURNING id::text`, sfx).Scan(&infraID))
	plan := &maintenance.MaintenancePlan{Name: "Plan " + sfx, Type: "inspection", InfrastructureID: infraID, Interval: maintenance.IntervalMonthly,
		IntervalDays: 30, Priority: maintenance.PrioMedium, NextDueAt: time.Now(), CreatedBy: userID}
	must(mrepo.CreatePlan(ctx, plan))
	tpl, err := mrepo.CreateChecklistTemplate(ctx, "Prüfung "+sfx, "", userID)
	must(err)
	item := &maintenance.ChecklistTemplateItem{TemplateID: tpl.ID, Label: "Ölstand", ItemType: "checkbox", Required: true, IntervalDays: 1, SortOrder: 1}
	must(mrepo.CreateChecklistTemplateItem(ctx, item))
	must(mrepo.AssignChecklistTemplatesToPlan(ctx, plan.ID, []string{tpl.ID}, 30))
	task := &maintenance.MaintenanceTask{PlanID: &plan.ID, Title: "Prüfung Presse " + sfx, Type: "inspection", InfrastructureID: infraID,
		Priority: maintenance.PrioMedium, DueDate: time.Now(), CreatedBy: userID}
	must(mrepo.CreateTask(ctx, task))

	// 1) Karte scannen → Token
	rec := httptest.NewRecorder()
	h.GlobalDashboardCompleteStart(rec, httptest.NewRequest("POST", "/global/complete-start",
		strings.NewReader(`{"type":"maintenance","id":"`+task.ID+`","rfid_uid":"`+card+`"}`)))
	if rec.Code != 200 {
		t.Fatalf("complete-start: %d %s", rec.Code, rec.Body.String())
	}
	var start map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &start)

	router := chi.NewRouter()
	router.Use(h.authMiddleware)
	router.Get("/complete/{type}/{id}", h.CompletionInfoWeb)
	router.Post("/complete/{type}/{id}", h.CompletionWeb)
	call := func(method, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, "/complete/maintenance/"+task.ID, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+start["token"])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code >= 300 && rec.Code < 400 {
			t.Fatalf("%s umgeleitet nach %s", method, rec.Header().Get("Location"))
		}
		return rec.Code, out
	}

	// 2) Assistent laden: Checkliste und „Fertig“ verfuegbar
	code, info := call("GET", "")
	if code != 200 || info["success"] != true {
		t.Fatalf("Assistent laden: %d %v", code, info)
	}
	data := info["data"].(map[string]any)
	cl, _ := data["checklist"].([]any)
	if len(cl) != 1 || data["can_finish"] != true {
		t.Fatalf("Checkliste %d Punkte, can_finish %v", len(cl), data["can_finish"])
	}

	// 3) Ohne Pflichtpunkt kein Abschluss
	base := `"no_parts":true,"minutes":30,"start":"` + time.Now().Add(-time.Hour).Format("2006-01-02T15:04") + `","comment":"Ölstand geprüft","finish":true`
	code, out := call("POST", `{`+base+`,"checklist":{"values":{"`+item.ID+`":""},"done":{"`+item.ID+`":false}}}`)
	if code != 400 || !strings.Contains(out["error"].(string), "Ölstand") {
		t.Fatalf("Pflichtpunkt fehlt: %d %v", code, out)
	}

	// 4) Fertig mit Checkliste
	code, out = call("POST", `{`+base+`,"checklist":{"values":{"`+item.ID+`":"erledigt"},"done":{"`+item.ID+`":true}}}`)
	if code != 200 || out["closed"] != true {
		t.Fatalf("Fertig: %d %v", code, out)
	}
	var status string
	must(pool.QueryRow(ctx, `SELECT status::text FROM maintenance_tasks WHERE id=$1::uuid`, task.ID).Scan(&status))
	if status != "done" {
		t.Fatalf("Status nach Fertig: %s", status)
	}
	var docs int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM attachments WHERE ref_type='infrastructure' AND ref_id=$1::uuid AND mimetype='application/pdf'`, infraID).Scan(&docs))
	if docs != 1 {
		t.Fatalf("Wartungsprotokoll an der Anlage: %d", docs)
	}
	var protoItems int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_task_checklist_results WHERE task_id=$1::uuid AND done`, task.ID).Scan(&protoItems))
	if protoItems != 1 {
		t.Fatalf("Checkliste gespeichert: %d", protoItems)
	}
	_ = http.StatusOK
}
