//go:build integration

package web

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOutlookSourcesVisits(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	pa := f.mk("OUT", f.infraA, time.Now().AddDate(0, 1, 0), &f.person, nil, nil)
	pg := f.mk("GRP", f.infraA, time.Now().AddDate(0, 1, 0), nil, nil, nil)
	f.exec(`UPDATE maintenance_visits SET assigned_group_id = $2::uuid WHERE plan_id = $1::uuid`, pg, f.groupA)
	first := f.visits(pa)[0]
	sources, err := f.h.microsoftCalendarSources(ctx, f.person, microsoftCalendarPreferences{Maintenance: true})
	f.must(err)
	var planned []microsoftCalendarSource
	for _, s := range sources {
		if s.Type == "maintenance_planned" && strings.Contains(s.Title, f.sfx) {
			planned = append(planned, s)
		}
	}
	if len(planned) == 0 || !strings.Contains(planned[0].Title, "OUT "+f.sfx) {
		t.Fatalf("geplante Termine in Outlook: %+v", planned)
	}
	for _, s := range planned {
		if strings.Contains(s.Title, "GRP") {
			t.Error("Termin nur mit Gruppe geht nach Outlook")
		}
	}
	// beauftragen: der Termin wird zum Auftrag
	tomorrow := time.Now().AddDate(0, 0, 1)
	f.must(f.h.maint.MoveVisit(ctx, first.ID, tomorrow, f.creator))
	f.must(f.h.maint.EnsurePlanTask(ctx, pa, f.creator))
	var taskID string
	f.must(f.pool.QueryRow(ctx, `SELECT task_id::text FROM maintenance_visits WHERE id = $1::uuid`, first.ID).Scan(&taskID))
	sources, err = f.h.microsoftCalendarSources(ctx, f.person, microsoftCalendarPreferences{Maintenance: true})
	f.must(err)
	var asTask, asVisit bool
	for _, s := range sources {
		asTask = asTask || (s.Type == "maintenance" && s.ID == taskID)
		asVisit = asVisit || (s.Type == "maintenance_planned" && s.ID == first.ID)
	}
	if !asTask || asVisit {
		t.Errorf("nach dem Beauftragen: Auftrag %v, Termin %v", asTask, asVisit)
	}
}

func TestAdoptPlannedLink(t *testing.T) {
	f := newCVFx(t)
	ctx := context.Background()
	pa := f.mk("ADOPT", f.infraA, time.Now().AddDate(0, 1, 0), &f.person, nil, nil)
	first := f.visits(pa)[0]
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM microsoft_calendar_links WHERE user_id = $1::uuid`, f.person)
	})
	f.exec(`INSERT INTO microsoft_calendar_links (user_id, account_kind, source_type, source_id, microsoft_event_id) VALUES ($1::uuid, 'work', 'maintenance_planned', $2::uuid, 'ev1')`, f.person, first.ID)
	f.must(f.h.maint.MoveVisit(ctx, first.ID, time.Now().AddDate(0, 0, 1), f.creator))
	f.must(f.h.maint.EnsurePlanTask(ctx, pa, f.creator))
	var taskID string
	f.must(f.pool.QueryRow(ctx, `SELECT task_id::text FROM maintenance_visits WHERE id = $1::uuid`, first.ID).Scan(&taskID))
	f.must(f.h.adoptPlannedCalendarLink(ctx, f.person, "work", taskID))
	links := func() (task, visit int, ev string) {
		f.must(f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE source_type = 'maintenance' AND source_id = $2::uuid),
			count(*) FILTER (WHERE source_type = 'maintenance_planned'), COALESCE(MAX(microsoft_event_id) FILTER (WHERE source_type = 'maintenance'), '')
			FROM microsoft_calendar_links WHERE user_id = $1::uuid`, f.person, taskID).Scan(&task, &visit, &ev))
		return
	}
	if task, visit, ev := links(); task != 1 || visit != 0 || ev != "ev1" {
		t.Errorf("nach dem Übernehmen: Auftrag %d, Termin %d, Ereignis %q", task, visit, ev)
	}
	// besteht schon eine Verknüpfung für den Auftrag, bleibt alles, wie es ist
	f.exec(`INSERT INTO microsoft_calendar_links (user_id, account_kind, source_type, source_id, microsoft_event_id) VALUES ($1::uuid, 'work', 'maintenance_planned', $2::uuid, 'ev2')`, f.person, first.ID)
	f.must(f.h.adoptPlannedCalendarLink(ctx, f.person, "work", taskID))
	if task, visit, ev := links(); task != 1 || visit != 1 || ev != "ev1" {
		t.Errorf("mit vorhandener Verknüpfung: Auftrag %d, Termin %d, Ereignis %q", task, visit, ev)
	}
}
