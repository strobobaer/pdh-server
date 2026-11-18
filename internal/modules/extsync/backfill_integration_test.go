package extsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/modules/tickets"
)

// Tests zu Spec 4.10: neue Meldungen benachrichtigen, rückwirkende bleiben still,
// abgeschlossene rückwirkende werden archiviert.

// Feste Uhr der Tests: 13.11.2026 12:00 in Berlin.
var (
	bfBerlin, _  = time.LoadLocation("Europe/Berlin")
	bfNow        = time.Date(2026, 11, 13, 12, 0, 0, 0, bfBerlin)
	bfYesterday  = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	bfToday      = time.Date(2026, 11, 13, 8, 15, 0, 0, time.UTC)
	bfLastMonth  = time.Date(2026, 10, 2, 7, 0, 0, 0, time.UTC)
	bfNoteResolv = "Im Fremdsystem erledigt, rückwirkend übernommen"
)

// backfillInfo: Standard-Attrappe plus Anlagedatum-Spalte vom Typ colType.
func backfillInfo(colType string) *TableInfo {
	t := fakeMeldungenInfo(true)
	t.Columns = append(t.Columns, Column{Name: "Angelegt", Type: colType, Nullable: true})
	return t
}

// newBackfillFix: Profil mit Anlagedatum-Spalte "Angelegt" und fester Uhr.
func newBackfillFix(t *testing.T, o fixOpt) *inFix {
	t.Helper()
	if o.info == nil {
		o.info = backfillInfo("datetime2")
	}
	if o.createdCol == "" {
		o.createdCol = "Angelegt"
	}
	f := newInFix(t, o)
	f.in.Now = func() time.Time { return bfNow }
	f.in.Location = bfBerlin
	t.Cleanup(func() { f.cleanupQueue() })
	return f
}

// cleanupQueue entfernt die Benachrichtigungseinträge der Datensätze des Profils.
func (f *inFix) cleanupQueue() {
	_, _ = f.sf.pool.Exec(context.Background(), `DELETE FROM record_change_queue WHERE record_id IN (
		SELECT ref_id FROM sync_links WHERE profile_id = $1 AND ref_id IS NOT NULL)`, f.p.ID)
}

// rowAt legt eine Zeile mit Anlagedatum an.
func (f *inFix) rowAt(nr int64, titel, prio, zustand string, created any) {
	f.ff.put(map[string]any{"Nr": nr, "Titel": f.title(titel), "Beschreibung": "Text " + titel,
		"Prio": prio, "Zustand": zustand, "Angelegt": created})
}

// queue zählt Benachrichtigungseinträge eines Datensatzes (kind leer = alle).
func (f *inFix) queue(id, kind string) int {
	return f.count(`SELECT count(*) FROM record_change_queue WHERE record_id = $1 AND ($2 = '' OR kind = $2)`, id, kind)
}

func (f *inFix) tbl() string {
	return map[string]string{RefTicket: "tickets", RefFault: "faults"}[f.target]
}

type recTimes struct {
	created, detected, resolved, archived *time.Time
	resolution                            string
	rootCause                             *string
	noParts                               bool
	archivedBy                            *string
}

func (f *inFix) times(id string) recTimes {
	f.t.Helper()
	var r recTimes
	detected := "NULL::timestamptz"
	if f.target == RefFault {
		detected = "detected_at"
	}
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT created_at, `+detected+`, resolved_at, archived_at,
		COALESCE(resolution, ''), root_cause, no_parts_needed, archived_by::text FROM `+f.tbl()+` WHERE id = $1`, id).
		Scan(&r.created, &r.detected, &r.resolved, &r.archived, &r.resolution, &r.rootCause, &r.noParts, &r.archivedBy))
	return r
}

// plainUpdate ändert den Datensatz wie ein Benutzer (ohne Abgleich-Kennzeichen).
func (f *inFix) plainUpdate(id, newTitle string) {
	f.t.Helper()
	_, err := f.sf.pool.Exec(context.Background(), `UPDATE `+f.tbl()+` SET title = $2 WHERE id = $1`, id, newTitle)
	must(f.t, err)
}

// silentLeaks prüft alle freien Verbindungen des Pools: keine darf pdh.sync_silent tragen.
func silentLeaks(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	conns := pool.AcquireAllIdle(ctx)
	leaks := 0
	for _, c := range conns {
		var v *string
		must(t, c.QueryRow(ctx, `SELECT current_setting('pdh.sync_silent', true)`).Scan(&v))
		if v != nil && *v == "1" {
			leaks++
		}
		c.Release()
	}
	if len(conns) == 0 {
		t.Fatal("keine freie Verbindung zum Prüfen")
	}
	return leaks
}

// ── Tests ────────────────────────────────────────────────────────────────

func TestNewRowNotifies(t *testing.T) {
	for _, target := range []string{RefTicket, RefFault} {
		t.Run(target, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{target: target})
			f.rowAt(1, "Heute", "hoch", "offen", bfToday)
			r := f.run(RunOptions{})
			if r.New != 1 || r.Backfilled != 0 || r.Failed != 0 {
				t.Fatalf("Lauf: %+v", r)
			}
			id, _, _, _, _ := f.rec(k(1))
			if n := f.queue(id, "created"); n != 1 {
				t.Fatalf("%d Einträge 'created', erwartet 1", n)
			}
			// Anlagezeit ist die echte (nicht das Fremddatum).
			if tm := f.times(id); tm.created == nil || tm.created.Equal(bfToday) {
				t.Fatalf("Anlagezeit %v", tm.created)
			}
		})
	}
}

func TestNoCreatedColumnMeansNew(t *testing.T) {
	f := newInFix(t, fixOpt{info: backfillInfo("datetime2")})
	f.in.Now = func() time.Time { return bfNow }
	f.in.Location = bfBerlin
	t.Cleanup(f.cleanupQueue)
	f.rowAt(1, "Alt", "hoch", "offen", bfLastMonth)
	r := f.run(RunOptions{})
	if r.New != 1 || r.Backfilled != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	id, _, _, _, _ := f.rec(k(1))
	if n := f.queue(id, "created"); n != 1 {
		t.Fatalf("%d Einträge 'created', erwartet 1", n)
	}
}

func TestBackfillNullCreatedCountsAsNew(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Ohne Datum", "hoch", "offen", nil)
	r := f.run(RunOptions{})
	if r.New != 1 || r.Backfilled != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	id, _, _, _, _ := f.rec(k(1))
	if n := f.queue(id, "created"); n != 1 {
		t.Fatalf("%d Einträge 'created', erwartet 1", n)
	}
}

func TestBackfillSilentNoNotification(t *testing.T) {
	for _, target := range []string{RefTicket, RefFault} {
		t.Run(target, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{target: target})
			f.rowAt(1, "Gestern", "hoch", "in Arbeit", bfYesterday)
			r := f.run(RunOptions{})
			if r.New != 0 || r.Backfilled != 1 || r.Failed != 0 {
				t.Fatalf("Lauf: %+v", r)
			}
			id, _, _, _, st := f.rec(k(1))
			if st != "in_progress" {
				t.Fatalf("Status %q", st)
			}
			if n := f.queue(id, ""); n != 0 {
				t.Fatalf("%d Benachrichtigungseinträge, erwartet 0", n)
			}
			if n := f.outbox(); n != 0 {
				t.Fatalf("%d Wartelisteneinträge", n)
			}
			runs, err := f.s.ListRuns(context.Background(), f.p.ID, 1)
			must(t, err)
			if runs[0].Backfilled != 1 || runs[0].New != 0 {
				t.Fatalf("gespeicherter Lauf: %+v", runs[0])
			}
		})
	}
}

func TestBackfillCreatedAtFromForeign(t *testing.T) {
	for _, target := range []string{RefTicket, RefFault} {
		t.Run(target, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{target: target})
			f.rowAt(1, "Damals", "mittel", "offen", bfLastMonth)
			f.run(RunOptions{})
			id, _, _, _, _ := f.rec(k(1))
			tm := f.times(id)
			if tm.created == nil || !tm.created.Equal(bfLastMonth) {
				t.Fatalf("created_at %v, erwartet %v", tm.created, bfLastMonth)
			}
			if target == RefFault && (tm.detected == nil || !tm.detected.Equal(bfLastMonth)) {
				t.Fatalf("detected_at %v, erwartet %v", tm.detected, bfLastMonth)
			}
		})
	}
}

func TestBackfillClosedIsArchived(t *testing.T) {
	for _, target := range []string{RefTicket, RefFault} {
		t.Run(target, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{target: target})
			rt := f.in.Targets[target].(*recordTarget)
			afterCalls := 0
			rt.onAfter = func(string) { afterCalls++ }
			f.rowAt(1, "Erledigt", "hoch", "erledigt", bfLastMonth)
			r := f.run(RunOptions{})
			if r.New != 0 || r.Backfilled != 1 || r.Archived != 1 || r.Failed != 0 {
				t.Fatalf("Lauf: %+v", r)
			}
			id, _, _, _, st := f.rec(k(1))
			if st != "resolved" {
				t.Fatalf("Status %q", st)
			}
			tm := f.times(id)
			if tm.archived == nil || tm.resolved == nil || tm.resolution != resolvedText || !tm.noParts ||
				tm.archivedBy == nil || *tm.archivedBy != systemUser {
				t.Fatalf("Archiv: %+v", tm)
			}
			if !tm.resolved.Equal(bfLastMonth) || tm.archived.Before(*tm.resolved) {
				t.Fatalf("Zeiten: erledigt %v, archiviert %v", tm.resolved, tm.archived)
			}
			if n := f.queue(id, ""); n != 0 {
				t.Fatalf("%d Benachrichtigungseinträge, erwartet 0", n)
			}
			if n := f.outbox(); n != 0 {
				t.Fatalf("%d Wartelisteneinträge, erwartet 0", n)
			}
			if afterCalls != 0 {
				t.Fatalf("Folgeschritte des Anlegens %d-mal gelaufen", afterCalls)
			}
			// Kein QuickResolve (eigener Verlaufseintrag "resolved"), sondern ein Eintrag "sync".
			if h := f.history(id, "resolved"); len(h) != 0 {
				t.Fatalf("QuickResolve gelaufen: %+v", h)
			}
			h := f.history(id, "sync")
			if len(h) != 1 || h[0].field != "status" || h[0].newV != "resolved" || h[0].msg != bfNoteResolv {
				t.Fatalf("Verlauf: %+v", h)
			}
			// Ausgangswerte wie beim normalen Anlegen: nächster Lauf ändert nichts.
			l := f.link(k(1))
			if l.SeenValues["status"] != "resolved" || l.LastError != "" {
				t.Fatalf("Verknüpfung: %+v", l)
			}
			f.p.Cursor = ""
			if r = f.run(RunOptions{}); r.Changed != 0 || r.New != 0 || r.Backfilled != 0 {
				t.Fatalf("zweiter Lauf: %+v", r)
			}
		})
	}
}

func TestBackfillArchivedReopenedInForeign(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Wieder offen", "hoch", "erledigt", bfLastMonth)
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	f.ff.set(1, "Zustand", "offen")
	r := f.run(RunOptions{})
	if r.Changed != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	_, _, _, _, st := f.rec(k(1))
	tm := f.times(id)
	if st != "open" || tm.archived != nil || tm.resolved != nil {
		t.Fatalf("Status %q, archiviert %v, erledigt %v", st, tm.archived, tm.resolved)
	}
	if n := f.queue(id, "update"); n != 1 {
		t.Fatalf("%d Änderungshinweise, erwartet 1", n)
	}
}

func TestBackfillOpenStaysOpenAndSyncsLater(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Offen alt", "mittel", "offen", bfYesterday)
	f.run(RunOptions{})
	id, _, _, _, st := f.rec(k(1))
	if st != "open" || f.times(id).archived != nil {
		t.Fatalf("Status %q", st)
	}
	if n := f.queue(id, ""); n != 0 {
		t.Fatalf("%d Einträge beim Anlegen", n)
	}
	f.ff.set(1, "Titel", f.title("Offen neu"))
	r := f.run(RunOptions{})
	if r.Changed != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	if _, title, _, _, _ := f.rec(k(1)); title != f.title("Offen neu") {
		t.Fatalf("Titel %q", title)
	}
	if n := f.queue(id, "update"); n != 1 {
		t.Fatalf("%d Änderungshinweise, erwartet 1", n)
	}
}

func TestBackfillOpenRunsAfterCreateWithoutUserNotification(t *testing.T) {
	for _, target := range []string{RefTicket, RefFault} {
		t.Run(target, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{target: target})
			rt := f.in.Targets[target].(*recordTarget)
			var after []string
			rt.onAfter = func(id string) { after = append(after, id) }
			f.rowAt(1, "Offen", "hoch", "offen", bfYesterday)
			f.run(RunOptions{})
			id, _, _, _, _ := f.rec(k(1))
			if len(after) != 1 || after[0] != id {
				t.Fatalf("Folgeschritte: %v", after)
			}
			if n := f.queue(id, ""); n != 0 {
				t.Fatalf("%d Benachrichtigungseinträge", n)
			}
		})
	}
}

func TestBackfillKeepsBaselineRules(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Ohne Prio", "", "offen", bfYesterday)
	f.run(RunOptions{})
	id, _, _, prio, _ := f.rec(k(1))
	if prio != "medium" {
		t.Fatalf("Vorgabe %q", prio)
	}
	if l := f.link(k(1)); l.WrittenValues["priority"] != "medium" {
		t.Fatalf("Ausgangswert: %+v", l.WrittenValues)
	}
	f.ff.set(1, "Prio", "hoch")
	r := f.run(RunOptions{})
	if r.Changed != 1 || f.level(id) != "high" || f.outbox() != 0 || f.conflictNotes(id) != 0 {
		t.Fatalf("Lauf %+v, Priorität %q, Warteliste %d, Widersprüche %d", r, f.level(id), f.outbox(), f.conflictNotes(id))
	}
}

func TestBackfillDoesNotPauseProfile(t *testing.T) {
	f := newBackfillFix(t, fixOpt{maxNew: 200})
	for i := int64(1); i <= 300; i++ {
		f.rowAt(i, fmt.Sprintf("Alt %d", i), "mittel", "offen", bfLastMonth)
	}
	r := f.run(RunOptions{})
	if r.Backfilled != 200 || r.New != 0 || r.Failed != 0 || r.SkippedReason != "" {
		t.Fatalf("erster Lauf: %+v", r)
	}
	g, err := f.s.GetProfile(context.Background(), f.p.ID)
	must(t, err)
	if g.State != StateDraft {
		t.Fatalf("Zustand %q", g.State)
	}
	r = f.run(RunOptions{})
	if r.Backfilled != 100 || r.New != 0 || r.Failed != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	if n := f.records(); n != 300 {
		t.Fatalf("%d Tickets", n)
	}
}

func TestBackfillOverflowCreatedInLaterRunsNotFailed(t *testing.T) {
	f := newBackfillFix(t, fixOpt{maxNew: 5})
	f.in.pageSize = 4
	for i := int64(1); i <= 12; i++ {
		f.rowAt(i, fmt.Sprintf("Alt %d", i), "mittel", "offen", bfLastMonth)
	}
	r := f.run(RunOptions{})
	if r.Backfilled != 5 || r.Failed != 0 || len(r.Errors) != 0 || r.SkippedReason != "" {
		t.Fatalf("erster Lauf: %+v", r)
	}
	// Der Cursor steht hinter allen 12 Zeilen; die übrigen warten an der Verknüpfung.
	if r.Cursor != f.cursorAfter(12) {
		t.Fatalf("Cursor %q, erwartet hinter Zeile 12", r.Cursor)
	}
	if n := f.links(); n != 12 {
		t.Fatalf("%d Verknüpfungen, erwartet 12", n)
	}
	waiting := f.count(`SELECT count(*) FROM sync_links WHERE profile_id = $1 AND ref_id IS NULL AND last_error = $2`,
		f.p.ID, LinkErrBackfillWaiting)
	if waiting != 7 {
		t.Fatalf("%d wartende Verknüpfungen, erwartet 7", waiting)
	}
	runs, err := f.s.ListRuns(context.Background(), f.p.ID, 1)
	must(t, err)
	if runs[0].Failed != 0 || len(runs[0].Errors) != 0 || runs[0].Backfilled != 5 {
		t.Fatalf("gespeicherter Lauf: %+v", runs[0])
	}
	// Überlastung: höchstens Limit rückwirkende je Lauf.
	r = f.run(RunOptions{Limit: 3})
	if r.Backfilled != 3 || r.Failed != 0 {
		t.Fatalf("Lauf mit Grenze 3: %+v", r)
	}
	r = f.run(RunOptions{})
	if r.Backfilled != 4 || r.Failed != 0 || len(r.Errors) != 0 {
		t.Fatalf("dritter Lauf: %+v", r)
	}
	if n := f.records(); n != 12 {
		t.Fatalf("%d Tickets, erwartet 12", n)
	}
	for i := 1; i <= 12; i++ {
		if n := f.count(`SELECT count(*) FROM tickets WHERE title = $1`, f.title(fmt.Sprintf("Alt %d", i))); n != 1 {
			t.Fatalf("Zeile %d: %d Tickets", i, n)
		}
	}
	if r = f.run(RunOptions{}); r.Backfilled != 0 || r.New != 0 || r.Failed != 0 {
		t.Fatalf("vierter Lauf: %+v", r)
	}
}

func TestMaxNewCountsOnlyToday(t *testing.T) {
	f := newBackfillFix(t, fixOpt{maxNew: 2})
	f.rowAt(1, "Heute 1", "hoch", "offen", bfToday)
	f.rowAt(2, "Heute 2", "hoch", "offen", bfToday)
	f.rowAt(3, "Alt 3", "hoch", "offen", bfYesterday)
	f.rowAt(4, "Alt 4", "hoch", "offen", bfYesterday)
	f.rowAt(5, "Alt 5", "hoch", "offen", bfYesterday)
	r := f.run(RunOptions{})
	if r.New != 2 || r.Backfilled != 2 || r.Failed != 0 || r.SkippedReason != "" {
		t.Fatalf("erster Lauf: %+v", r)
	}
	// Drei neue von heute: über der Obergrenze 2 – das Profil hält an.
	f.rowAt(6, "Heute 6", "hoch", "offen", bfToday)
	f.rowAt(7, "Heute 7", "hoch", "offen", bfToday)
	f.rowAt(8, "Heute 8", "hoch", "offen", bfToday)
	r = f.run(RunOptions{})
	if !strings.Contains(r.SkippedReason, "Zu viele neue Meldungen") || r.New != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	g, err := f.s.GetProfile(context.Background(), f.p.ID)
	must(t, err)
	if g.State != StateHalted {
		t.Fatalf("Zustand %q", g.State)
	}
}

func TestBackfillDateColumnUsesCalendarDate(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	must(t, err)
	// Zeiten im Fremdsystem = Ortszeit des Servers (R28: die Anlagezeit einer
	// Datumsspalte ist Mitternacht am Ort des Fremdsystems).
	f := newBackfillFix(t, fixOpt{info: backfillInfo("date"), timeZone: "server"})
	// 13.11. 09:00 in New York: Tagesbeginn ist 13.11. 05:00 UTC.
	f.in.Now = func() time.Time { return time.Date(2026, 11, 13, 9, 0, 0, 0, ny) }
	f.in.Location = ny
	// Datumsspalten kommen als Mitternacht UTC an.
	f.rowAt(1, "Datum heute", "hoch", "offen", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	f.rowAt(2, "Datum gestern", "hoch", "offen", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	r := f.run(RunOptions{})
	if r.New != 1 || r.Backfilled != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	id1, _, _, _, _ := f.rec(k(1))
	id2, _, _, _, _ := f.rec(k(2))
	if f.queue(id1, "created") != 1 || f.queue(id2, "") != 0 {
		t.Fatalf("Benachrichtigungen: heute %d, gestern %d", f.queue(id1, "created"), f.queue(id2, ""))
	}
	// Anlagezeit einer Datumsspalte: Tagesbeginn des Kalendertags am Ort des Fremdsystems (hier des Servers).
	if tm := f.times(id2); tm.created == nil || !tm.created.Equal(time.Date(2026, 11, 12, 0, 0, 0, 0, ny)) {
		t.Fatalf("created_at %v", tm.created)
	}
}

func TestBackfillCreatedColumnWrongType(t *testing.T) {
	for _, c := range []struct{ col, want string }{
		{"Titel", "Anlagedatum-Spalte ist keine Datumsspalte"},
		{"Fehlt", "Anlagedatum-Spalte ‚Fehlt‘ gibt es in der Tabelle nicht"},
	} {
		t.Run(c.col, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{createdCol: c.col})
			f.rowAt(1, "Eins", "hoch", "offen", bfYesterday)
			for _, dry := range []bool{false, true} {
				_, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: dry})
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("Probelauf %v: Fehler %v, erwartet %q", dry, err, c.want)
				}
			}
			if f.links() != 0 || f.records() != 0 {
				t.Fatal("trotz Einstellungsfehler angelegt")
			}
		})
	}
}

func TestPreviewSeparatesBackfill(t *testing.T) {
	f := newBackfillFix(t, fixOpt{maxNew: 1})
	f.rowAt(1, "Heute", "hoch", "offen", bfToday)
	f.rowAt(2, "Heute 2", "hoch", "offen", bfToday)
	f.rowAt(3, "Alt offen", "hoch", "offen", bfYesterday)
	f.rowAt(4, "Alt erledigt", "hoch", "erledigt", bfLastMonth)
	f.rowAt(5, "Alt geschlossen", "hoch", "geschlossen", bfLastMonth)
	r, prev, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: true})
	must(t, err)
	if r.New != 2 || r.Backfilled != 3 || r.Archived != 2 || r.Failed != 0 || r.SkippedReason != "" {
		t.Fatalf("Probelauf: %+v", r)
	}
	by := map[string]Preview{}
	for _, p := range prev {
		by[p.ForeignKey] = p
	}
	want := map[string][2]bool{k(1): {false, false}, k(2): {false, false}, k(3): {true, false}, k(4): {true, true}, k(5): {true, true}}
	for key, w := range want {
		p, ok := by[key]
		if !ok || !p.IsNew || p.Backfill != w[0] || p.Archive != w[1] {
			t.Fatalf("Vorschau %s: %+v", key, p)
		}
	}
	if f.links() != 0 || f.records() != 0 || f.runsCount() != 0 {
		t.Fatal("Probelauf hat geschrieben")
	}
}

func TestSilentGucDoesNotLeak(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Still", "hoch", "offen", bfYesterday)
	f.rowAt(2, "Still erledigt", "hoch", "erledigt", bfYesterday)
	f.run(RunOptions{})
	if n := silentLeaks(t, f.sf.pool); n != 0 {
		t.Fatalf("%d Verbindungen mit pdh.sync_silent", n)
	}
	id, _, _, _, _ := f.rec(k(1))
	f.plainUpdate(id, f.title("Von Hand"))
	if n := f.queue(id, "update"); n != 1 {
		t.Fatalf("%d Änderungshinweise nach Benutzeränderung, erwartet 1", n)
	}
	// Eigener kleiner Pool: das stille Anlegen und eine Benutzeränderung teilen sich
	// zwingend dieselben Verbindungen.
	pool := smallPool(t, f.sf.pool, 2)
	tg := NewTicketTarget(pool, tickets.NewService(tickets.NewRepository(pool)))
	created := bfYesterday
	nid, err := tg.Create(context.Background(), map[string]string{"title": f.title("Klein"), "priority": "low"}, systemUser,
		"test|klein|", CreateOptions{Silent: true, CreatedAt: &created})
	must(t, err)
	t.Cleanup(func() { deleteTicket(f.sf.pool, nid) })
	if n := silentLeaks(t, pool); n != 0 {
		t.Fatalf("%d Verbindungen mit pdh.sync_silent", n)
	}
	if n := f.queue(nid, ""); n != 0 {
		t.Fatalf("stilles Anlegen: %d Einträge", n)
	}
	for i := 0; i < 2; i++ {
		_, err := pool.Exec(context.Background(), `UPDATE tickets SET title = $2 WHERE id = $1`, nid, f.title(fmt.Sprintf("Hand %d", i)))
		must(t, err)
	}
	if n := f.queue(nid, "update"); n != 2 {
		t.Fatalf("%d Änderungshinweise, erwartet 2", n)
	}
}

func TestBackfillSilentRollbackDoesNotLeakGuc(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	pool := smallPool(t, f.sf.pool, 2)
	tg := NewTicketTarget(pool, tickets.NewService(tickets.NewRepository(pool))).(*recordTarget)
	var inserted string
	tg.afterInsert = func(id string) error { inserted = id; return errors.New("Absturz nach dem Einfügen") }
	_, err := tg.Create(context.Background(), map[string]string{"title": f.title("Zurück"), "priority": "low"}, systemUser,
		"test|zurück|", CreateOptions{Silent: true})
	if err == nil || inserted == "" {
		t.Fatalf("Fehler erwartet: %v", err)
	}
	if n := silentLeaks(t, pool); n != 0 {
		t.Fatalf("%d Verbindungen mit pdh.sync_silent nach Rücknahme", n)
	}
	// Danach benachrichtigt ein normales Anlegen und Ändern über denselben Pool.
	tg.afterInsert = nil
	id, err := tg.Create(context.Background(), map[string]string{"title": f.title("Normal"), "priority": "low"}, systemUser,
		"test|normal|", CreateOptions{})
	must(t, err)
	t.Cleanup(func() { deleteTicket(f.sf.pool, id) })
	if n := f.queue(id, "created"); n != 1 {
		t.Fatalf("%d Einträge 'created', erwartet 1", n)
	}
	_, err = pool.Exec(context.Background(), `UPDATE tickets SET title = $2 WHERE id = $1`, id, f.title("Normal 2"))
	must(t, err)
	if n := f.queue(id, "update"); n != 1 {
		t.Fatalf("%d Änderungshinweise, erwartet 1", n)
	}
	// Auch im ganzen Lauf: eine scheiternde stille Zeile lässt nichts zurück.
	rt := f.in.Targets[RefTicket].(*recordTarget)
	rt.afterInsert = func(string) error { return errors.New("Absturz nach dem Einfügen") }
	f.rowAt(1, "Rücknahme", "hoch", "offen", bfYesterday)
	if r := f.run(RunOptions{}); r.Failed != 1 || r.Backfilled != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	if n := silentLeaks(t, f.sf.pool); n != 0 {
		t.Fatalf("%d Verbindungen mit pdh.sync_silent nach dem Lauf", n)
	}
	rt.afterInsert = nil
	if r := f.run(RunOptions{}); r.Backfilled != 1 || r.Failed != 0 {
		t.Fatalf("Wiederholung: %+v", r)
	}
}

// smallPool öffnet einen eigenen Pool mit höchstens n Verbindungen.
func smallPool(t *testing.T, base *pgxpool.Pool, n int32) *pgxpool.Pool {
	t.Helper()
	cfg := base.Config().Copy()
	cfg.MaxConns = n
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	must(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func deleteTicket(pool *pgxpool.Pool, id string) {
	c := context.Background()
	_, _ = pool.Exec(c, `DELETE FROM record_change_queue WHERE record_id = $1`, id)
	_, _ = pool.Exec(c, `DELETE FROM record_history WHERE ref_id = $1`, id)
	_, _ = pool.Exec(c, `DELETE FROM tickets WHERE id = $1`, id)
}
