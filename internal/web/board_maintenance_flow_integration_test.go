//go:build integration

// Ende-zu-Ende: Wartung am Leitstand per Karte fertig melden – mit
// Checkliste im Assistenten, Statuswechsel und Wartungsprotokoll.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run BoardMaintenanceFlow ./internal/web/
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
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
	var bossID string // Verantwortliche(r) des Plans bekommt die Rueckmeldung
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Vera', 'Verantwortlich', 'Instandhaltung', '') RETURNING id::text`, "vb"+sfx).Scan(&bossID))
	// Plan mit einer Checkliste; der erste Auftrag entsteht beim Anlegen
	tpl, err := mrepo.CreateTemplate(ctx, "Prüfung "+sfx, "", userID)
	must(err)
	_, err = mrepo.AddTemplateItem(ctx, tpl.ID, &maintenance.ChecklistItemInput{Label: "Ölstand", ItemType: "checkbox", Required: true})
	must(err)
	planID, err := h.maint.CreatePlan(ctx, &maintenance.PlanInput{Name: "Plan " + sfx, Type: "inspection", InfrastructureID: infraID,
		IntervalUnit: maintenance.UnitMonth, IntervalCount: 1, NextDueAt: time.Now().Format("2006-01-02"), ResponsibleTo: &bossID,
		Checklists: []maintenance.PlanChecklistInput{{TemplateID: tpl.ID}}}, userID)
	must(err)
	plan := struct{ ID string }{planID}
	open, err := mrepo.FindTasks(ctx, maintenance.TaskFilter{PlanID: planID, Open: true})
	must(err)
	if len(open) != 1 {
		t.Fatalf("offene Aufträge nach Anlegen: %d", len(open))
	}
	task := open[0]

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
	router.Post("/complete/{type}/{id}/steps/{stepID}", h.CompletionStepWeb)
	router.Post("/complete/{type}/{id}/steps/{stepID}/photo", h.CompletionStepPhotoWeb)
	callPath := func(method, path, ctype string, body *bytes.Buffer) (int, map[string]any) {
		req := httptest.NewRequest(method, "/complete/maintenance/"+task.ID+path, body)
		req.Header.Set("Authorization", "Bearer "+start["token"])
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
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
	stepID := cl[0].(map[string]any)["id"].(string)

	// 3) Ohne Pflichtpunkt kein Abschluss
	base := `"no_parts":true,"minutes":30,"start":"` + time.Now().Add(-time.Hour).Format("2006-01-02T15:04") + `","comment":"Ölstand geprüft","finish":true`
	code, out := call("POST", `{`+base+`}`)
	if code != 400 || !strings.Contains(out["error"].(string), "Ölstand") {
		t.Fatalf("Pflichtpunkt fehlt: %d %v", code, out)
	}

	// 4) Punkt per Karte abhaken und ein Foto anhaengen (gleicher Token, Unterpfade)
	code, out = callPath("POST", "/steps/"+stepID, "application/json", bytes.NewBufferString(`{"done":true}`))
	if code != 200 || out["success"] != true {
		t.Fatalf("Punkt speichern: %d %v", code, out)
	}
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	fw, _ := mw.CreateFormFile("files", "oel.png")
	_ = png.Encode(fw, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	_ = mw.Close()
	code, out = callPath("POST", "/steps/"+stepID+"/photo", mw.FormDataContentType(), &mp)
	if code != 200 || out["success"] != true {
		t.Fatalf("Foto per Karte: %d %v", code, out)
	}
	// fremder Schritt ueber den Token: abgelehnt
	if code, _ = callPath("POST", "/steps/00000000-0000-0000-0000-000000000000", "application/json", bytes.NewBufferString(`{"done":true}`)); code != 400 {
		t.Fatalf("fremder Punkt: %d", code)
	}

	// 5) Fertig
	code, out = call("POST", `{`+base+`}`)
	if code != 200 || out["closed"] != true || !strings.Contains(out["message"].(string), "Nächster Termin") {
		t.Fatalf("Fertig: %d %v", code, out)
	}
	// Folgeauftrag und automatische Rueckmeldung an die Verantwortliche
	var followUps int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM maintenance_tasks WHERE plan_id=$1::uuid AND status='open'`, plan.ID).Scan(&followUps))
	if followUps != 1 {
		t.Fatalf("Folgeaufträge: %d", followUps)
	}
	var feedback string
	_ = pool.QueryRow(ctx, `SELECT m.body FROM chat_messages m JOIN chat_members cm ON cm.conversation_id = m.conversation_id
		WHERE cm.user_id = $1::uuid AND m.body LIKE '%Wartung erledigt%' ORDER BY m.created_at DESC LIMIT 1`, bossID).Scan(&feedback)
	if !strings.Contains(feedback, "Tim Technik") || !strings.Contains(feedback, "Nächster Termin") || !strings.Contains(feedback, "Protokoll: /uploads/") {
		t.Fatalf("Rückmeldung: %q", feedback)
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
	steps, err := mrepo.Steps(ctx, task.ID)
	must(err)
	if len(steps) != 1 || !steps[0].Done || len(steps[0].DocImages) != 1 || steps[0].CheckedBy != "Tim Technik" {
		t.Fatalf("Schritt im Protokoll: %d Schritte, erster %+v", len(steps), *steps[0])
	}
	var taskPDF int
	must(pool.QueryRow(ctx, `SELECT COUNT(*) FROM attachments WHERE ref_type='maintenance_task' AND ref_id=$1::uuid AND mimetype='application/pdf'`, task.ID).Scan(&taskPDF))
	if taskPDF != 1 {
		t.Fatalf("Wartungsprotokoll am Auftrag: %d", taskPDF)
	}
	var lastDone *time.Time
	must(pool.QueryRow(ctx, `SELECT last_done_at FROM maintenance_plan_checklists WHERE plan_id=$1::uuid`, planID).Scan(&lastDone))
	if lastDone == nil {
		t.Fatal("Takt der Plan-Checkliste nicht fortgeschrieben")
	}
	_ = http.StatusOK
}
