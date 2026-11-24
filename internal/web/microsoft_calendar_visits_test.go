package web

import (
	"testing"
	"time"
)

// Outlook: geplante Wartungstermine gehören zur Auswahl „Wartung“.
func TestMicrosoftSourceSelected(t *testing.T) {
	only := func(p microsoftCalendarPreferences) map[string]bool {
		out := map[string]bool{}
		for _, k := range []string{"shift", "task", "maintenance", "maintenance_planned", "ticket", "fault"} {
			out[k] = microsoftSourceSelected(k, p)
		}
		return out
	}
	m := only(microsoftCalendarPreferences{Maintenance: true})
	if !m["maintenance"] || !m["maintenance_planned"] || m["shift"] || m["task"] || m["ticket"] {
		t.Errorf("nur Wartung: %v", m)
	}
	if m := only(microsoftCalendarPreferences{Shifts: true, Tasks: true, TicketsFaults: true}); m["maintenance_planned"] || !m["shift"] || !m["task"] || !m["ticket"] || !m["fault"] {
		t.Errorf("ohne Wartung: %v", m)
	}
}

func TestPlannedCalendarSource(t *testing.T) {
	day := time.Date(2027, 3, 12, 0, 0, 0, 0, time.Local)
	s := plannedCalendarSource("v1", "Filter wechseln", day)
	if s.Type != "maintenance_planned" || s.ID != "v1" || !s.AllDay || s.Title != "PDH · Wartung (geplant) · Filter wechseln" {
		t.Errorf("Termin: %+v", s)
	}
	if s.Start.Format("2006-01-02") != "2027-03-12" || !s.End.Equal(s.Start.Add(24*time.Hour)) {
		t.Errorf("Zeitraum: %s – %s", s.Start, s.End)
	}
}
