package web

import (
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/maintenance"
)

func TestNextTermText(t *testing.T) {
	day := time.Date(2027, 3, 12, 0, 0, 0, 0, time.Local)
	order := time.Date(2027, 3, 5, 0, 0, 0, 0, time.Local)
	if got := nextTermText("", &maintenance.NextTerm{Day: day, TaskID: "t2", OrderOn: order}); got != "Nächster Termin: 12.03.2027" {
		t.Errorf("mit Auftrag: %q", got)
	}
	if got := nextTermText("", &maintenance.NextTerm{Day: day, OrderOn: order}); got != "Nächster Termin: 12.03.2027 – Auftrag entsteht am 05.03.2027" {
		t.Errorf("ohne Auftrag: %q", got)
	}
	if got := nextTermText("", nil); got != "" {
		t.Errorf("ohne Termin: %q", got)
	}
}

func TestMaintDoneMessageNext(t *testing.T) {
	p := &maintProtocol{Title: "Kompressor", CompletedAt: time.Date(2027, 2, 1, 9, 0, 0, 0, time.Local)}
	day := time.Date(2027, 3, 12, 0, 0, 0, 0, time.Local)
	order := time.Date(2027, 3, 5, 0, 0, 0, 0, time.Local)
	if got := maintDoneMessage(p, "t1", "", &maintenance.NextTerm{Day: day, OrderOn: order}); !strings.Contains(got, "📅 Nächster Termin: 12.03.2027 (Auftrag entsteht am 05.03.2027)") {
		t.Errorf("ohne Folgeauftrag:\n%s", got)
	}
	got := maintDoneMessage(p, "t1", "", &maintenance.NextTerm{Day: day, TaskID: "t2", OrderOn: order})
	if !strings.Contains(got, "📅 Nächster Termin: 12.03.2027\n") || strings.Contains(got, "Auftrag entsteht") {
		t.Errorf("mit Folgeauftrag:\n%s", got)
	}
	if got := maintDoneMessage(p, "t1", "", nil); strings.Contains(got, "Nächster Termin") {
		t.Errorf("ohne Plan:\n%s", got)
	}
}

func TestMaintAssignedText(t *testing.T) {
	due := time.Date(2027, 3, 12, 0, 0, 0, 0, time.Local)
	want := "🔧 Dir wurde die Wartung **Kompressor** am 12.03.2027 zugewiesen. [Öffnen](/maintenance/tasks/t1)"
	if got := maintAssignedText("Kompressor", due, "t1"); got != want {
		t.Errorf("%q", got)
	}
}

func TestTaskDetailNextTermRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	base := MaintenanceTaskDetailData{Task: MaintenanceTaskDetailView{ID: "t1", Title: "Kompressor", Status: "done", CompletedAt: "01.02.2027 09:00"}}
	d := base
	d.NextDay, d.NextOrderOn = "12.03.2027", "05.03.2027"
	out := renderPage(t, tmpl, "maintenance_detail", d)
	if !strings.Contains(out, "nächster Termin 12.03.2027 (geplant) · Auftrag entsteht am 05.03.2027") {
		t.Error("ohne Folgeauftrag: geplanter Termin und Auftragstag fehlen")
	}
	d = base
	d.NextTaskID, d.NextTaskDue = "t2", "12.03.2027"
	out = renderPage(t, tmpl, "maintenance_detail", d)
	if !strings.Contains(out, `nächster Termin <a href="/maintenance/tasks/t2">12.03.2027</a>`) || strings.Contains(out, "Auftrag entsteht") {
		t.Error("mit Folgeauftrag: Verweis auf den Auftrag fehlt")
	}
}
