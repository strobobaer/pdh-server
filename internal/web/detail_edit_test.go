package web

import (
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/timetracking"
)

// Detailseiten im KVP-/Windows-11-Aufbau: Reiter „Bearbeiten“ mit Einstellungs-Zeilen,
// Reiter „Zeit“, gemeinsamer Ersatzteil-Dialog – mit gefuellten Daten gerendert.
func TestDetailEditTabs(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := 42
	run := &timetracking.TimeEntry{ID: "r1", Description: "läuft", StartedAt: time.Now()}
	entries := []*timetracking.TimeEntry{{ID: "e1", Description: "Montage", DurationMin: &d, UserName: "Max"}, {ID: "e2"}}
	users := []UserOption{{ID: "u1", Name: "Max"}}

	out := renderPage(t, tmpl, "ticket_detail", TicketDetailData{
		Ticket: TicketView{ID: "t1", Title: "T", Status: "open", DueDate: "01.01.2027", DueDateISO: "2027-01-01", CostCenterNumber: "4711", CostCenterName: "Fertigung", RecordImageURL: "/x.png", ResponsibleID: "u1"},
		Users:  users, RunningTime: run, TimeEntries: entries,
		StatusOptions: []StatusOption{{"open", "Offen"}, {"resolved", "Gelöst"}},
	})
	for _, w := range []string{`data-save="/records/ticket/t1/people"`, "4711 – Fertigung", "42 min", "Zeit stoppen", `id="ticket-part-modal"`, `class="w11-dialog"`, `data-fn="bookTicketPart"`, "pdhRecSave(this)"} {
		if !strings.Contains(out, w) {
			t.Errorf("ticket: %q fehlt", w)
		}
	}
	if strings.Contains(out, `value="resolved"`) {
		t.Error("ticket: Gelöst ohne Assistent wählbar")
	}

	out = renderPage(t, tmpl, "fault_detail", FaultDetailData{
		Fault: FaultDetailView{ID: "f1", Title: "F", Status: "detected", TicketPending: true, CostCenterNumber: "1", DueDate: "x"},
		Users: users, TimeEntries: entries,
	})
	for _, w := range []string{`data-save="/faults/f1/infrastructure-web"`, "w11-infobar warning", "Zeit erfassen", `data-fn="filterFaultParts"`, `data-mod="fault"`} {
		if !strings.Contains(out, w) {
			t.Errorf("fault: %q fehlt", w)
		}
	}

	out = renderPage(t, tmpl, "task_detail", TaskDetailData{
		Task:  TaskView{ID: "k1", Title: "K", CanResolve: true, AssignedIDSet: map[string]bool{"u1": true}, ProjectID: "p1", ProjectName: "P"},
		Users: users, RunningTime: run, TimeEntries: entries,
	})
	for _, w := range []string{`data-clear="infrastructure_id:clear_infrastructure"`, `name="assigned_to_ids"`, `href="/projects/p1"`, `id="task-part-modal"`, `name="color"`, "stopTaskTime("} {
		if !strings.Contains(out, w) {
			t.Errorf("task: %q fehlt", w)
		}
	}
}
