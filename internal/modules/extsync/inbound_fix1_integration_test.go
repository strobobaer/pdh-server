package extsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests der Fix-Runde 1 (R17 Ausgangswert im PDH, Cursor nie über unbearbeitete
// Zeilen, atomare Verknüpfungs-Schritte, Endzustände R18, weitere Lücken).

// conflictNotes zählt Verlaufseinträge "nicht übernommen" am Datensatz.
func (f *inFix) conflictNotes(id string) int {
	return f.count(`SELECT count(*) FROM record_history WHERE ref_id = $1 AND action = 'sync'
		AND message LIKE '%nicht übernommen%'`, id)
}

func (f *inFix) level(id string) string {
	var v string
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT priority::text FROM tickets WHERE id = $1`, id).Scan(&v))
	return v
}

func (f *inFix) due(tbl, id string) *time.Time {
	var v *time.Time
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT due_date FROM `+tbl+` WHERE id = $1`, id).Scan(&v))
	return v
}

// ── R17: vom PDH gesetzte Werte sind Ausgangswert, keine Benutzeränderung ──

func TestInboundDefaultPriorityIsBaseline(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Ohne Prio"), "Prio": nil, "Zustand": "offen"})
	f.run(RunOptions{})
	id, _, _, prio, _ := f.rec(k(1))
	if prio != "medium" {
		t.Fatalf("Vorgabe %q", prio)
	}
	f.ff.set(1, "Prio", "hoch")
	r := f.run(RunOptions{})
	if r.Changed != 1 || f.level(id) != "high" {
		t.Fatalf("Lauf %+v, Priorität %q", r, f.level(id))
	}
	if n := f.outbox(); n != 0 {
		t.Fatalf("%d Wartelisteneinträge, erwartet 0", n)
	}
	if n := f.conflictNotes(id); n != 0 {
		t.Fatalf("%d Widerspruchsvermerke", n)
	}
	if h := f.history(id, "sync"); len(h) != 1 || h[0].field != "priority" || h[0].newV != "high" {
		t.Fatalf("Verlauf: %+v", h)
	}
}

func TestInboundDefaultDueDateIsBaseline(t *testing.T) {
	maps := append(stdMaps(RefTicket), FieldMap{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirIn, Position: 4})
	f := newInFix(t, fixOpt{maps: maps})
	f.row(1, "Ohne Termin", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	if d := f.due("tickets", id); d == nil {
		t.Fatal("keine Vorgabe-Fälligkeit")
	}
	want := time.Date(2027, 3, 4, 10, 0, 0, 0, time.UTC)
	f.ff.set(1, "Faellig", want)
	r := f.run(RunOptions{})
	if d := f.due("tickets", id); r.Changed != 1 || d == nil || !d.Equal(want) {
		t.Fatalf("Lauf %+v, fällig %v", r, d)
	}
	if f.outbox() != 0 || f.conflictNotes(id) != 0 {
		t.Fatalf("Warteliste %d, Widersprüche %d", f.outbox(), f.conflictNotes(id))
	}
}

func TestInboundIgnoredEmptyIsBaselineAndUserEditStillConflicts(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Leer", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	// Fremde Priorität geleert: PDH behält "high" (Pflichtfeld).
	f.ff.set(1, "Prio", nil)
	f.run(RunOptions{})
	if f.level(id) != "high" {
		t.Fatalf("Priorität %q", f.level(id))
	}
	// Später gesetzt: übernommen, kein Widerspruch.
	f.ff.set(1, "Prio", "mittel")
	f.run(RunOptions{})
	if f.level(id) != "medium" || f.outbox() != 0 || f.conflictNotes(id) != 0 {
		t.Fatalf("Priorität %q, Warteliste %d, Widersprüche %d", f.level(id), f.outbox(), f.conflictNotes(id))
	}
	// Leerer Titel: Zeile scheitert, später gesetzter Titel wird übernommen.
	f.ff.set(1, "Titel", "")
	if r := f.run(RunOptions{}); r.Failed != 1 {
		t.Fatalf("leerer Titel: %+v", r)
	}
	f.ff.set(1, "Titel", f.title("Wieder da"))
	f.run(RunOptions{})
	if _, title, _, _, _ := f.rec(k(1)); title != f.title("Wieder da") || f.outbox() != 0 {
		t.Fatalf("Titel %q, Warteliste %d", title, f.outbox())
	}
	// Echte Benutzeränderung danach bleibt ein Widerspruch (PDH gewinnt).
	f.pdhUpdate(id, "priority = 'critical'")
	f.ff.set(1, "Prio", "niedrig")
	f.run(RunOptions{})
	if f.level(id) != "critical" || f.outbox() != 1 || f.conflictNotes(id) != 1 {
		t.Fatalf("Priorität %q, Warteliste %d, Widersprüche %d", f.level(id), f.outbox(), f.conflictNotes(id))
	}
}

func TestInboundClosedNoPerpetualDiffAndReopen(t *testing.T) {
	maps := stdMaps(RefTicket)
	maps = append(maps, FieldMap{ForeignColumn: "Loesung", PDHField: "resolution", Direction: DirIn, Position: 5})
	info := fakeMeldungenInfo(true)
	info.Columns = append(info.Columns, Column{Name: "Loesung", Type: "nvarchar", Nullable: true})
	f := newInFix(t, fixOpt{maps: maps, info: info})
	f.row(1, "Zu", "hoch", "geschlossen")
	f.run(RunOptions{})
	id, _, _, _, st := f.rec(k(1))
	if st != "resolved" {
		t.Fatalf("Status %q", st)
	}
	hist := len(f.history(id, ""))
	f.p.Cursor = ""
	if r := f.run(RunOptions{}); r.Changed != 0 || len(f.history(id, "")) != hist {
		t.Fatalf("Dauerunterschied: %+v", r)
	}
	// Eine fremde Lösung kommt später: übernommen statt Widerspruch.
	f.ff.set(1, "Loesung", "Teil getauscht")
	f.run(RunOptions{})
	if f.conflictNotes(id) != 0 || f.outbox() != 0 {
		t.Fatalf("Widersprüche %d, Warteliste %d", f.conflictNotes(id), f.outbox())
	}
	f.ff.set(1, "Zustand", "offen")
	f.run(RunOptions{})
	if _, _, _, _, st = f.rec(k(1)); st != "open" || f.outbox() != 0 {
		t.Fatalf("Status %q, Warteliste %d", st, f.outbox())
	}
}

func TestInboundReopenFromResolved(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Wieder auf", "hoch", "erledigt")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	f.ff.set(1, "Zustand", "offen")
	r := f.run(RunOptions{})
	var st string
	var archived bool
	must(t, f.sf.pool.QueryRow(context.Background(), `SELECT status::text, archived_at IS NOT NULL FROM tickets WHERE id = $1`, id).Scan(&st, &archived))
	if r.Changed != 1 || st != "open" || archived {
		t.Fatalf("Lauf %+v, Status %q, archiviert %v", r, st, archived)
	}
	h := f.history(id, "sync")
	if len(h) != 1 || h[0].field != "status" || h[0].oldV != "resolved" || h[0].newV != "open" {
		t.Fatalf("Verlauf: %+v", h)
	}
	if f.outbox() != 0 {
		t.Fatalf("%d Wartelisteneinträge", f.outbox())
	}
}

// ── Kritisch 2: der Cursor geht nie über unbearbeitete Zeilen ────────────

func TestInboundReserveFailureKeepsCursor(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.row(2, "Zwei", "hoch", "offen")
	f.row(3, "Drei", "hoch", "offen")
	f.in.testFail = func(step, fk string) error {
		if step == "reserve" && fk == k(2) {
			return errors.New("PDH-Datenbank nicht erreichbar")
		}
		return nil
	}
	r, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{})
	if err == nil {
		t.Fatal("Fehler erwartet")
	}
	if r.Cursor != f.cursorAfter(1) {
		t.Fatalf("Cursor %q, erwartet hinter Zeile 1", r.Cursor)
	}
	f.p.Cursor = r.Cursor
	f.in.testFail = nil
	r = f.run(RunOptions{})
	if r.New != 2 || f.records() != 3 {
		t.Fatalf("Lauf %+v, %d Tickets", r, f.records())
	}
}

// cancelTarget bricht den Kontext nach dem Anlegen eines bestimmten Titels ab.
type cancelTarget struct {
	Target
	title  string
	cancel context.CancelFunc
}

func (c *cancelTarget) Create(ctx context.Context, values map[string]string, user, origin string) (string, error) {
	id, err := c.Target.Create(ctx, values, user, origin)
	if values["title"] == c.title {
		c.cancel()
	}
	return id, err
}

func TestInboundCancelMidPageLosesNothing(t *testing.T) {
	f := newInFix(t, fixOpt{})
	for i := int64(1); i <= 4; i++ {
		f.row(i, fmt.Sprintf("Zeile %d", i), "hoch", "offen")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	real := f.in.Targets[RefTicket]
	f.in.Targets[RefTicket] = &cancelTarget{Target: real, title: f.title("Zeile 2"), cancel: cancel}
	r, _, err := f.in.Run(ctx, f.p, f.maps, f.ff, f.d, RunOptions{})
	if err == nil {
		t.Fatal("Abbruch erwartet")
	}
	if r.Cursor != f.cursorAfter(1) {
		t.Fatalf("Cursor %q, erwartet hinter Zeile 1", r.Cursor)
	}
	f.p.Cursor = r.Cursor
	f.in.Targets[RefTicket] = real
	f.run(RunOptions{})
	for i := 1; i <= 4; i++ {
		if n := f.count(`SELECT count(*) FROM tickets WHERE title = $1`, f.title(fmt.Sprintf("Zeile %d", i))); n != 1 {
			t.Fatalf("Zeile %d: %d Tickets", i, n)
		}
	}
	if l := f.link(k(2)); l.RefID == nil || l.LastError != "" {
		t.Fatalf("Verknüpfung Zeile 2: %+v", l)
	}
}

// readFailTarget: Lesen scheitert.
type readFailTarget struct{ Target }

func (readFailTarget) Read(context.Context, string) (map[string]string, error) {
	return nil, errors.New("Lesen gestört")
}

func TestInboundReadAndLinkErrorFailKeepsCursor(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	start := f.p.Cursor
	f.ff.set(1, "Titel", f.title("Neu"))
	real := f.in.Targets[RefTicket]
	f.in.Targets[RefTicket] = readFailTarget{real}
	f.in.testFail = func(step, fk string) error {
		if step == "linkerror" {
			return errors.New("PDH-Datenbank nicht erreichbar")
		}
		return nil
	}
	r, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{})
	if err == nil || r.Cursor != start {
		t.Fatalf("Fehler %v, Cursor %q (Start %q)", err, r.Cursor, start)
	}
	f.in.Targets[RefTicket] = real
	f.in.testFail = nil
	f.run(RunOptions{})
	if _, title, _, _, _ := f.rec(k(1)); title != f.title("Neu") {
		t.Fatalf("Änderung verloren: %q", title)
	}
}

// ── Wichtig 1: Teilfehler, Wiederholung ohne Doppel ──────────────────────

func TestInboundPartialFailureRetryIsIdempotent(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	f.pdhUpdate(id, "title = $2", f.title("PDH"))
	f.ff.set(1, "Titel", f.title("Fremd"))
	f.ff.set(1, "Beschreibung", "Neue Beschreibung")
	failed := false
	f.in.testFail = func(step, fk string) error {
		if step == "commit" && !failed {
			failed = true
			return errors.New("Verbindung weg")
		}
		return nil
	}
	r := f.run(RunOptions{})
	if r.Failed != 1 {
		t.Fatalf("erster Lauf: %+v", r)
	}
	if l := f.link(k(1)); l.LastError == "" {
		t.Fatal("kein Fehler an der Verknüpfung")
	}
	r = f.run(RunOptions{})
	if r.Failed != 0 {
		t.Fatalf("Wiederholung: %+v", r)
	}
	f.run(RunOptions{})
	if n := f.conflictNotes(id); n != 1 {
		t.Fatalf("%d Widerspruchsvermerke, erwartet 1", n)
	}
	if n := f.outbox(); n != 1 {
		t.Fatalf("%d Wartelisteneinträge, erwartet 1", n)
	}
	desc := 0
	for _, h := range f.history(id, "sync") {
		if h.field == "description" {
			desc++
		}
	}
	if desc != 1 {
		t.Fatalf("%d Verlaufseinträge Beschreibung, erwartet 1", desc)
	}
}

// ── Wichtig 3: weitere Lücken ────────────────────────────────────────────

func TestInboundTimestampKindWithTies(t *testing.T) {
	info := &TableInfo{Schema: "dbo", Name: "MeldungenZeit", Columns: []Column{
		{Name: "Nr", Type: "int", IsKey: true},
		{Name: "Titel", Type: "nvarchar"},
		{Name: "Geaendert", Type: "datetime2"},
	}}
	f := newInFix(t, fixOpt{kind: changeKindTimestamp, changeCol: "Geaendert", info: info,
		maps: []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}})
	f.in.pageSize = 2
	t0 := time.Date(2026, 11, 1, 8, 0, 0, 123456700, time.UTC)
	for i := int64(1); i <= 3; i++ {
		f.ff.put(map[string]any{"Nr": i, "Titel": f.title(fmt.Sprintf("Gleichzeitig %d", i)), "Geaendert": t0})
	}
	f.ff.put(map[string]any{"Nr": int64(4), "Titel": f.title("Später"), "Geaendert": t0.Add(time.Second)})
	r := f.run(RunOptions{})
	if r.New != 4 {
		t.Fatalf("erster Lauf: %+v", r)
	}
	if r = f.run(RunOptions{}); r.New != 0 || r.Changed != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	f.ff.put(map[string]any{"Nr": int64(2), "Titel": f.title("Geändert"), "Geaendert": t0.Add(2 * time.Second)})
	r = f.run(RunOptions{})
	if r.Changed != 1 || r.New != 0 {
		t.Fatalf("dritter Lauf: %+v", r)
	}
	if _, title, _, _, _ := f.rec(k(2)); title != f.title("Geändert") {
		t.Fatalf("Titel %q", title)
	}
}

func TestInboundFaultDueDateFromForeign(t *testing.T) {
	maps := append(stdMaps(RefFault), FieldMap{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirIn, Position: 4})
	f := newInFix(t, fixOpt{target: RefFault, maps: maps})
	d1 := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Termin"), "Prio": "hoch", "Zustand": "offen", "Faellig": d1})
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	if d := f.due("faults", id); d == nil || !d.Equal(d1) {
		t.Fatalf("Fälligkeit %v, erwartet %v", d, d1)
	}
	d2 := d1.Add(48 * time.Hour)
	f.ff.set(1, "Faellig", d2)
	f.run(RunOptions{})
	if d := f.due("faults", id); d == nil || !d.Equal(d2) {
		t.Fatalf("Fälligkeit %v, erwartet %v", d, d2)
	}
	if h := f.history(id, "sync"); len(h) != 1 || h[0].field != "due_date" {
		t.Fatalf("Verlauf: %+v", h)
	}
	if f.outbox() != 0 {
		t.Fatalf("%d Wartelisteneinträge", f.outbox())
	}
}

func TestInboundTwoProfilesConcurrently(t *testing.T) {
	a := newInFix(t, fixOpt{})
	b := newInFix(t, fixOpt{})
	for i := int64(1); i <= 3; i++ {
		a.row(i, fmt.Sprintf("A %d", i), "hoch", "offen")
		b.row(i, fmt.Sprintf("B %d", i), "hoch", "offen")
	}
	gate := make(chan struct{})
	a.ff.gate = gate
	a.ff.entered = make(chan struct{}, 1)
	done := make(chan *Run, 1)
	pa := *a.p
	go func() {
		r, _, err := a.in.Run(context.Background(), &pa, a.maps, a.ff, a.d, RunOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	<-a.ff.entered // A hält seine Sperre
	rb := b.run(RunOptions{})
	if rb.SkippedReason != "" || rb.New != 3 {
		t.Fatalf("Profil B: %+v", rb)
	}
	close(gate)
	ra := <-done
	if ra == nil || ra.New != 3 {
		t.Fatalf("Profil A: %+v", ra)
	}
	if a.records() != 3 || b.records() != 3 {
		t.Fatalf("Tickets A %d, B %d", a.records(), b.records())
	}
}

// ── R18: Endzustände nur einmal melden ───────────────────────────────────

func TestInboundRecordGoneReportedOnce(t *testing.T) {
	f := newInFix(t, fixOpt{kind: changeKindNone})
	f.row(1, "Weg", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	_, err := f.sf.pool.Exec(context.Background(), `DELETE FROM tickets WHERE id = $1`, id)
	must(t, err)
	f.ff.set(1, "Titel", f.title("Weg 2"))
	if r := f.run(RunOptions{}); r.Failed != 1 || r.Errors[0].Message != LinkErrRecordGone {
		t.Fatalf("erste Meldung: %+v", r)
	}
	if r := f.run(RunOptions{}); r.Failed != 0 {
		t.Fatalf("zweite Meldung ohne Änderung: %+v", r)
	}
	f.ff.set(1, "Titel", f.title("Weg 3"))
	if r := f.run(RunOptions{}); r.Failed != 1 || r.New != 0 {
		t.Fatalf("nach neuer Änderung: %+v", r)
	}
	if f.records() != 0 {
		t.Fatal("neu angelegt")
	}
}

func TestInboundForeignGoneTerminal(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.row(2, "Zwei", "dringend", "offen")
	f.run(RunOptions{})
	f.ff.del(2)
	r := f.run(RunOptions{})
	if r.Failed != 1 || r.Errors[0].Message != LinkErrForeignGone || r.Errors[0].ForeignKey != k(2) {
		t.Fatalf("Meldung: %+v", r)
	}
	if l := f.link(k(2)); l.LastError != LinkErrForeignGone {
		t.Fatalf("Verknüpfung: %+v", l)
	}
	ones := f.ff.oneCount()
	if r = f.run(RunOptions{}); r.Failed != 0 || f.ff.oneCount() != ones {
		t.Fatalf("erneut gemeldet: %+v", r)
	}
	// Die Zeile taucht wieder auf (berichtigt): wird normal angelegt.
	f.row(2, "Zwei", "hoch", "offen")
	r = f.run(RunOptions{})
	if r.New != 1 || r.Failed != 0 || f.records() != 2 {
		t.Fatalf("wieder da: %+v, %d Tickets", r, f.records())
	}
	if l := f.link(k(2)); l.LastError != "" || l.RefID == nil {
		t.Fatalf("Verknüpfung: %+v", l)
	}
}

func TestInboundBadKeyTerminal(t *testing.T) {
	f := newInFix(t, fixOpt{})
	_, _, err := f.s.ReserveLink(context.Background(), f.p.ID, "kaputt", RefTicket)
	must(t, err)
	r := f.run(RunOptions{})
	if r.Failed != 1 || !strings.Contains(r.Errors[0].Message, "ungültig") {
		t.Fatalf("Meldung: %+v", r)
	}
	if l := f.link("kaputt"); l.LastError != LinkErrBadKey {
		t.Fatalf("Verknüpfung: %+v", l)
	}
	if r = f.run(RunOptions{}); r.Failed != 0 {
		t.Fatalf("erneut gemeldet: %+v", r)
	}
}
