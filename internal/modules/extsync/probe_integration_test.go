package extsync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ── Gleicher Befehl wie der Ausgang ──────────────────────────────────────

// Der Schreibversuch baut seinen Befehl mit denselben Funktionen wie der
// Ausgang: gleiche Eingabe (aktuelle PDH-Werte) → gleicher Befehl samt
// Zeitzone und Kürzung, und er stammt von UpdateRow.
func TestProbeWriteUsesWorkerStatementBuilder(t *testing.T) {
	info := fakeMeldungenInfo(true)
	for i := range info.Columns {
		if info.Columns[i].Name == "Titel" {
			info.Columns[i].MaxLen = 12
		}
	}
	maps := append(stdMaps(RefTicket), FieldMap{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirOut, Position: 9})
	f := newOutFix(t, fixOpt{info: info, maps: maps, timeZone: "Europe/Berlin"})
	ctx := context.Background()
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2, due_date = $3", "Ein sehr langer neuer Titel", time.Date(2026, 11, 19, 23, 0, 0, 0, time.UTC))
	f.processAll(true)
	execs := f.ff.execStmts()
	if len(execs) != 1 {
		t.Fatalf("Ausgang: %d Befehle", len(execs))
	}
	worker := execs[0]
	cur, err := f.in.Targets[RefTicket].Read(ctx, id)
	must(t, err)

	st := &fdStats{}
	db := sql.OpenDB(&fdConnector{ff: f.ff, st: st})
	t.Cleanup(func() { db.Close() })
	d := ffDialect{Dialect: f.rd, ff: f.ff}
	sample := map[string]string{"title": cur["title"], "due_date": cur["due_date"]}
	res := ProbeWrite(ctx, db, d, f.p, f.maps, k(1), sample, true, 5*time.Second)
	if !res.OK || !res.RolledBack {
		t.Fatalf("Schreibversuch: %+v", res)
	}
	if len(st.execs) != 1 {
		t.Fatalf("Schreibversuch: %d Befehle", len(st.execs))
	}
	probe := st.execs[0]
	if probe.SQL != worker.SQL || !reflect.DeepEqual(probe.Args, worker.Args) {
		t.Fatalf("Befehle verschieden:\nAusgang  %+v\nVersuch  %+v", worker.Args, probe.Args)
	}
	if !f.rd.wasProduced(probe) {
		t.Fatal("Befehl stammt nicht von UpdateRow")
	}
	if !reflect.DeepEqual(res.Truncated, []string{"title"}) {
		t.Fatalf("Kürzung: %v", res.Truncated)
	}
	// DryOutbound zeigt genau diesen Befehl.
	dry := DryOutbound(ctx, f.ff, f.rd, f.p, f.maps, k(1), sample)
	if dry.Stmt.SQL != worker.SQL || !reflect.DeepEqual(dry.Stmt.Args, worker.Args) {
		t.Fatalf("Trockenlauf: %+v", dry.Stmt.Args)
	}
}

// ── Trockenlauf (Stufe 1) ────────────────────────────────────────────────

func TestDryStage(t *testing.T) {
	f := newInFix(t, fixOpt{})
	ctx := context.Background()
	f.row(1, "Pumpe", "hoch", "offen")
	f.row(2, "Band", "mittel", "in Arbeit")
	stateBefore := f.p.State
	res := DryStage(ctx, f.in, f.p, f.maps, f.ff, f.d, k(1), map[string]string{"title": "Probe"})
	if !res.Passed || !res.Inbound.Passed || !res.NeedsOutbound || res.Outbound == nil || len(res.Outbound.Errors) != 0 {
		t.Fatalf("bestanden erwartet: %+v", res)
	}
	if res.Inbound.Run.New != 2 || len(res.Inbound.Previews) != 2 {
		t.Fatalf("Vorschau: %+v", res.Inbound)
	}
	// Ohne gewählte Meldung: hinaus nicht geprüft, nicht bestanden.
	res = DryStage(ctx, f.in, f.p, f.maps, f.ff, f.d, "", nil)
	if res.Passed || res.Error != DryNeedsRow {
		t.Fatalf("ohne Meldung: %+v", res)
	}
	// Eine Zeile mit nicht zugeordnetem Wert: nicht bestanden.
	f.row(3, "Lampe", "unbekannt", "offen")
	res = DryStage(ctx, f.in, f.p, f.maps, f.ff, f.d, k(1), nil)
	if res.Passed || res.Inbound.Passed || res.Inbound.Run.Failed != 1 {
		t.Fatalf("Fehler erwartet: %+v", res.Inbound)
	}
	// Fehler hinaus (Probewert) allein: nicht bestanden.
	f.ff.del(3)
	res = DryStage(ctx, f.in, f.p, f.maps, f.ff, f.d, k(1), map[string]string{"gibtsnicht": "x"})
	if res.Passed || !res.Inbound.Passed || len(res.Outbound.Errors) != 1 {
		t.Fatalf("Fehler hinaus: %+v", res)
	}
	// Ohne hinausgehende Zuordnung genügt der Trockenlauf herein.
	inOnly := []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}
	res = DryStage(ctx, f.in, f.p, inOnly, f.ff, f.d, "", nil)
	if !res.Passed || res.NeedsOutbound || res.Outbound != nil {
		t.Fatalf("nur herein: %+v", res)
	}
	// Geschrieben wurde nirgends.
	f.reload()
	if f.links() != 0 || f.runsCount() != 0 || f.outbox() != 0 || f.records() != 0 || len(f.ff.execStmts()) != 0 || f.p.State != stateBefore {
		t.Fatalf("Trockenlauf hat geschrieben: links %d runs %d outbox %d records %d execs %d state %s",
			f.links(), f.runsCount(), f.outbox(), f.records(), len(f.ff.execStmts()), f.p.State)
	}
}

// ── Testzeiten und Zustand ───────────────────────────────────────────────

// testProfile legt ein Profil an; mit out=true hat es eine hinausgehende
// Zuordnung (Schreibversuch nötig), sonst nur hereinkommende.
func (f *storeFixture) testProfile(t *testing.T, name string, out bool) *Profile {
	t.Helper()
	p := f.profile(t, name)
	dir := DirIn
	if out {
		dir = DirBoth
	}
	must(t, f.s.ReplaceMaps(context.Background(), p.ID, []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: dir}}))
	return p
}

// setTestState setzt Zustand und Testzeiten direkt (Ausgangslage im Test).
func (f *storeFixture) setTestState(t *testing.T, id, state string, dry, write bool) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), `UPDATE sync_profiles SET state = $2,
		test_dry_at = CASE WHEN $3 THEN now() END, test_write_at = CASE WHEN $4 THEN now() END WHERE id = $1`,
		id, state, dry, write)
	must(t, err)
}

func TestMarkTestTransitions(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	pOut := f.testProfile(t, "mark-out", true)
	pIn := f.testProfile(t, "mark-in", false)
	n := 0
	for _, needsWrite := range []bool{true, false} {
		p := pIn
		if needsWrite {
			p = pOut
		}
		for _, state := range []string{StateDraft, StateTested, StateArmed, StateHalted} {
			for _, stage := range []string{TestStageDry, TestStageWrite} {
				for _, ok := range []bool{true, false} {
					for _, otherPassed := range []bool{true, false} {
						dry0, write0 := otherPassed, otherPassed
						if stage == TestStageDry {
							dry0 = !ok // Ausgangswert der eigenen Stufe: das Gegenteil
						} else {
							write0 = !ok
						}
						f.setTestState(t, p.ID, state, dry0, write0)
						must(t, f.s.MarkTest(ctx, p.ID, stage, ok))
						g, err := f.s.GetProfile(ctx, p.ID)
						must(t, err)
						dry, write := dry0, write0
						if stage == TestStageDry {
							dry = ok
						} else {
							write = ok
						}
						want := state
						switch {
						case state == StateDraft && dry && (!needsWrite || write):
							want = StateTested
						case state == StateTested && !ok:
							want = StateDraft
						}
						name := fmt.Sprintf("schreiben=%v %s %s ok=%v andere=%v", needsWrite, state, stage, ok, otherPassed)
						if g.State != want || (g.TestDryAt != nil) != dry || (g.TestWriteAt != nil) != write {
							t.Errorf("%s: Zustand %s (erwartet %s), trocken %v (%v), schreiben %v (%v)",
								name, g.State, want, g.TestDryAt != nil, dry, g.TestWriteAt != nil, write)
						}
						n++
					}
				}
			}
		}
	}
	if n != 64 {
		t.Fatalf("%d Fälle", n)
	}
	if err := f.s.MarkTest(ctx, pOut.ID, "sonstwas", true); err == nil {
		t.Fatal("unbekannte Stufe angenommen")
	}
	if err := f.s.MarkTest(ctx, "00000000-0000-4000-8000-0000000000aa", TestStageDry, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbekanntes Profil: %v", err)
	}
}

func TestMarkTestBothStages(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.testProfile(t, "mark-both", true)
	must(t, f.s.MarkTest(ctx, p.ID, TestStageDry, true))
	g, _ := f.s.GetProfile(ctx, p.ID)
	if g.State != StateDraft || g.TestDryAt == nil || g.TestWriteAt != nil {
		t.Fatalf("nach Trockenlauf: %+v", g)
	}
	must(t, f.s.MarkTest(ctx, p.ID, TestStageWrite, true))
	g, _ = f.s.GetProfile(ctx, p.ID)
	if g.State != StateTested || g.TestDryAt == nil || g.TestWriteAt == nil {
		t.Fatalf("nach Schreibversuch: %+v", g)
	}
	must(t, f.s.MarkTest(ctx, p.ID, TestStageWrite, false))
	g, _ = f.s.GetProfile(ctx, p.ID)
	if g.State != StateDraft || g.TestDryAt == nil || g.TestWriteAt != nil {
		t.Fatalf("nach gescheitertem Schreibversuch: %+v", g)
	}
}

func TestArmRequiresTests(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	p := f.testProfile(t, "arm-out", true)
	notTested := func(stage, text string) {
		t.Helper()
		err := f.s.Arm(ctx, p.ID, day)
		var nt *NotTestedError
		if !errors.Is(err, ErrNotTested) || !errors.As(err, &nt) || nt.Stage != stage || err.Error() != text {
			t.Fatalf("Arm: %v (erwartet %s)", err, text)
		}
		g, _ := f.s.GetProfile(ctx, p.ID)
		if g.State == StateArmed {
			t.Fatalf("trotzdem scharf: %+v", g)
		}
	}
	notTested(TestStageDry, "Erst den Trockenlauf bestehen")
	if g, _ := f.s.GetProfile(ctx, p.ID); g.NotifyFrom != nil {
		t.Fatalf("Stichtag ohne Scharfschalten gesetzt: %v", g.NotifyFrom)
	}
	must(t, f.s.MarkTest(ctx, p.ID, TestStageDry, true))
	notTested(TestStageWrite, "Erst den Schreibversuch bestehen")
	must(t, f.s.MarkTest(ctx, p.ID, TestStageWrite, true))
	must(t, f.s.Arm(ctx, p.ID, day))
	g, _ := f.s.GetProfile(ctx, p.ID)
	if g.State != StateArmed || g.NotifyFrom == nil || !g.NotifyFrom.Equal(day) {
		t.Fatalf("scharf: %+v", g)
	}
	must(t, f.s.Arm(ctx, p.ID, day.AddDate(0, 0, 3))) // schon scharf: nichts ändert sich
	// Testen eines scharfen Profils schaltet es nicht ab.
	must(t, f.s.MarkTest(ctx, p.ID, TestStageWrite, false))
	g, _ = f.s.GetProfile(ctx, p.ID)
	if g.State != StateArmed || !g.NotifyFrom.Equal(day) || g.TestWriteAt != nil {
		t.Fatalf("scharf getestet: %+v", g)
	}
	// Neue Zuordnung: Entwurf, beide Testzeiten leer, nicht scharf zu schalten.
	must(t, f.s.MarkTest(ctx, p.ID, TestStageWrite, true))
	must(t, f.s.ReplaceMaps(ctx, p.ID, []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirOut}}))
	g, _ = f.s.GetProfile(ctx, p.ID)
	if g.State != StateDraft || g.TestDryAt != nil || g.TestWriteAt != nil {
		t.Fatalf("nach neuer Zuordnung: %+v", g)
	}
	notTested(TestStageDry, "Erst den Trockenlauf bestehen")
	// Getestet, aber die Testzeiten sind leer (Weg der Verbindung geändert).
	f.setTestState(t, p.ID, StateTested, false, false)
	notTested(TestStageDry, "Erst den Trockenlauf bestehen")
	// Profil ohne Schreiben: der Trockenlauf genügt.
	q := f.testProfile(t, "arm-in", false)
	must(t, f.s.MarkTest(ctx, q.ID, TestStageDry, true))
	must(t, f.s.Arm(ctx, q.ID, day))
	// SetState schaltet nie scharf.
	r := f.testProfile(t, "arm-setstate", false)
	if err := f.s.SetState(ctx, r.ID, StateArmed, ""); err == nil {
		t.Fatal("SetState hat scharf geschaltet")
	}
	if g, _ := f.s.GetProfile(ctx, r.ID); g.State != StateDraft {
		t.Fatalf("Zustand %s", g.State)
	}
	if err := f.s.Arm(ctx, "00000000-0000-4000-8000-0000000000aa", day); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbekanntes Profil: %v", err)
	}
}

func TestArmResumeFromPaused(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	p := f.testProfile(t, "resume", true)
	must(t, f.s.MarkTest(ctx, p.ID, TestStageDry, true))
	must(t, f.s.MarkTest(ctx, p.ID, TestStageWrite, true))
	must(t, f.s.Arm(ctx, p.ID, day))
	must(t, f.s.SetState(ctx, p.ID, StateHalted, "Zu viele neue Meldungen"))
	must(t, f.s.Arm(ctx, p.ID, day.AddDate(0, 0, 2)))
	g, _ := f.s.GetProfile(ctx, p.ID)
	if g.State != StateArmed || g.StateNote != "" || !g.NotifyFrom.Equal(day) {
		t.Fatalf("fortgesetzt: %+v", g)
	}
	// Angehalten ohne bestandene Stufen: nicht fortzusetzen.
	f.setTestState(t, p.ID, StateHalted, true, false)
	if err := f.s.Arm(ctx, p.ID, day); !errors.Is(err, ErrNotTested) {
		t.Fatalf("ohne Schreibversuch fortgesetzt: %v", err)
	}
}

// ── Labor: echter SQL Server ─────────────────────────────────────────────

// labState ist der vollständige Stand einer Meldung samt Kommentaren und
// Zählern des Servers, außerhalb jeder Transaktion gelesen.
type labState struct {
	row                   string // alle Spalten, auch Geaendert und RV
	rv                    string
	comments, allComments int
	ident                 string // IDENT_CURRENT der Kommentartabelle
	dbts                  string // @@DBTS (Zähler der rowversion)
}

func readLab(t *testing.T, db *sql.DB, nr int64) labState {
	t.Helper()
	ctx := context.Background()
	var s labState
	var titel, zustand string
	var beschr, prio sql.NullString
	var faellig sql.NullTime
	var geaendert time.Time
	var rv, dbts []byte
	var ident sql.NullString
	must(t, db.QueryRowContext(ctx, `SELECT Titel, Beschreibung, Prio, Zustand, Faellig, Geaendert, RV
		FROM dbo.Meldungen WHERE MeldungNr = @p1`, nr).Scan(&titel, &beschr, &prio, &zustand, &faellig, &geaendert, &rv))
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.MeldungKommentare WHERE MeldungNr = @p1`, nr).Scan(&s.comments))
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.MeldungKommentare`).Scan(&s.allComments))
	must(t, db.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(40), IDENT_CURRENT('dbo.MeldungKommentare')), @@DBTS`).Scan(&ident, &dbts))
	s.rv = hex.EncodeToString(rv)
	s.ident = ident.String
	s.dbts = hex.EncodeToString(dbts)
	s.row = fmt.Sprintf("%q|%v|%v|%q|%v|%s|%s", titel, beschr, prio, zustand, faellig, geaendert.Format(time.RFC3339Nano), s.rv)
	return s
}

// labProfile: Profil auf dbo.Meldungen mit Titel (beides), Priorität und
// Fälligkeit (hinaus) und Kommentartabelle. Nur im Speicher.
func labProfile() (*Profile, []FieldMap) {
	p := &Profile{ID: "lab-probe", Name: "Labor-Probe", TargetType: RefTicket, SourceSchema: "dbo", SourceTable: "Meldungen",
		KeyColumns: []string{"MeldungNr"}, ChangeColumn: "RV", ChangeKind: changeKindRowVersion, TimeZone: "Europe/Berlin",
		CommentTable: stdCommentTable}
	maps := []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirBoth, Position: 0},
		{ForeignColumn: "Prio", PDHField: "priority", Direction: DirOut, Translate: stdTranslatePrio, Position: 1},
		{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirOut, Position: 2},
		{ForeignColumn: "Zustand", PDHField: "status", Direction: DirIn, Position: 3,
			Translate: map[string]string{"offen": "open", "in Arbeit": "in_progress", "erledigt": "resolved"}},
	}
	return p, maps
}

// labKey wählt die erste angebotene Meldung (nie ein eingetippter Schlüssel).
func labKey(t *testing.T, f Foreign, d Dialect, p *Profile, maps []FieldMap) (string, int64) {
	t.Helper()
	rows, err := ProbeRows(context.Background(), f, d, p, maps, 5)
	must(t, err)
	if len(rows) == 0 {
		t.Fatal("keine Meldung angeboten")
	}
	kv, err := DecodeKey(rows[0].Key)
	must(t, err)
	var nr int64
	_, err = fmt.Sscan(kv[0], &nr)
	must(t, err)
	t.Logf("angeboten: %d Meldungen, gewählt %s %v", len(rows), rows[0].Key, rows[0].Values)
	return rows[0].Key, nr
}

// assertLabUnchanged vergleicht den Stand nach dem Versuch mit dem vorher.
func assertLabUnchanged(t *testing.T, before, after labState) {
	t.Helper()
	t.Logf("vorher:  %s, Kommentare %d/%d, IDENT_CURRENT %s, @@DBTS %s", before.row, before.comments, before.allComments, before.ident, before.dbts)
	t.Logf("nachher: %s, Kommentare %d/%d, IDENT_CURRENT %s, @@DBTS %s", after.row, after.comments, after.allComments, after.ident, after.dbts)
	if after.row != before.row || after.rv != before.rv {
		t.Errorf("Meldung verändert:\nvorher  %s\nnachher %s", before.row, after.row)
	}
	if after.comments != before.comments || after.allComments != before.allComments {
		t.Errorf("Kommentare verändert: %d/%d -> %d/%d", before.comments, before.allComments, after.comments, after.allComments)
	}
}

func TestProbeWriteRollsBack(t *testing.T) {
	db := openLab(t)
	ctx := context.Background()
	ms, err := DialectFor("mssql")
	must(t, err)
	f := NewSQLForeign(db, ms, 30*time.Second)
	p, maps := labProfile()
	key, nr := labKey(t, f, ms, p, maps)
	before := readLab(t, db, nr)
	tag := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	sample := map[string]string{"title": "PDH-Probe " + tag, "priority": "low", "due_date": "2027-01-02T03:04:05Z"}

	dry := DryOutbound(ctx, f, ms, p, maps, key, sample)
	if len(dry.Errors) != 0 {
		t.Fatalf("Trockenlauf: %+v", dry.Errors)
	}
	t.Logf("Trockenlauf: %s %q; Kommentar: %s %q", dry.SQL, dry.Params, dry.CommentSQL, dry.CommentParams)

	res := ProbeWrite(ctx, db, ms, p, maps, key, sample, true, 30*time.Second)
	t.Logf("Schreibversuch: OK=%v RolledBack=%v Fehler=%q Auslöser=%v Warnungen=%q", res.OK, res.RolledBack, res.Error, res.Triggers, res.Warnings)
	t.Logf("Befehl: %s %q", res.Statement, res.Params)
	t.Logf("Kommentar: %s %q", res.CommentStatement, res.CommentParams)
	t.Logf("zurückgelesen: %v", res.ReadBack)
	if !res.OK || !res.RolledBack || res.Error != "" {
		t.Fatalf("Schreibversuch: %+v", res)
	}
	if res.ReadBack["title"] != sample["title"] || res.ReadBack["priority"] != "low" || res.ReadBack["due_date"] != sample["due_date"] {
		t.Fatalf("zurückgelesen: %v", res.ReadBack)
	}
	if res.Statement != dry.SQL || !reflect.DeepEqual(res.Params, dry.Params) {
		t.Fatalf("Versuch und Trockenlauf verschieden: %s / %s", res.Statement, dry.SQL)
	}
	after := readLab(t, db, nr)
	assertLabUnchanged(t, before, after)
	if after.ident != before.ident {
		t.Logf("Laufende Nummer der Kommentartabelle weitergezählt: %s -> %s", before.ident, after.ident)
	}
	if after.dbts == before.dbts {
		t.Errorf("@@DBTS unverändert – Änderungszähler nicht verbraucht? %s", after.dbts)
	}
}

func TestProbeWriteReportsTriggers(t *testing.T) {
	db := openLab(t)
	ctx := context.Background()
	ms, err := DialectFor("mssql")
	must(t, err)
	f := NewSQLForeign(db, ms, 30*time.Second)
	p, maps := labProfile()
	trg, err := ProbeTriggers(ctx, f, p)
	must(t, err)
	if !reflect.DeepEqual(trg, []string{"trg_Meldungen_Geaendert"}) {
		t.Fatalf("Auslöser: %v", trg)
	}
	key, nr := labKey(t, f, ms, p, maps)
	before := readLab(t, db, nr)
	res := ProbeWrite(ctx, db, ms, p, maps, key, nil, true, 30*time.Second)
	t.Logf("Schreibversuch: OK=%v RolledBack=%v Warnungen=%q Befehl=%s %q", res.OK, res.RolledBack, res.Warnings, res.Statement, res.Params)
	if !res.OK || !reflect.DeepEqual(res.Triggers, trg) || !reflect.DeepEqual(res.Warnings, []string{WarnTriggers, WarnCounters}) {
		t.Fatalf("%+v", res)
	}
	assertLabUnchanged(t, before, readLab(t, db, nr))
}

func TestProbeWriteReportsPermissionError(t *testing.T) {
	db := openLab(t)
	ctx := context.Background()
	ms, err := DialectFor("mssql")
	must(t, err)
	f := NewSQLForeign(db, ms, 30*time.Second)
	// pdh_sync darf in MeldungKommentare einfügen, aber nicht ändern.
	p := &Profile{ID: "lab-perm", TargetType: RefTicket, SourceSchema: "dbo", SourceTable: "MeldungKommentare",
		KeyColumns: []string{"Id"}, ChangeKind: changeKindNone, TimeZone: "UTC"}
	maps := []FieldMap{{ForeignColumn: "Text", PDHField: "title", Direction: DirBoth}}
	rows, err := ProbeRows(ctx, f, ms, p, maps, 1)
	must(t, err)
	if len(rows) != 1 {
		t.Fatalf("Zeilen: %+v", rows)
	}
	var n0 int
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.MeldungKommentare`).Scan(&n0))
	res := ProbeWrite(ctx, db, ms, p, maps, rows[0].Key, map[string]string{"title": "Probe"}, true, 30*time.Second)
	t.Logf("Schreibversuch: OK=%v RolledBack=%v Fehler=%q", res.OK, res.RolledBack, res.Error)
	if res.OK || !res.RolledBack || !strings.Contains(res.Error, "UPDATE") || !strings.Contains(strings.ToLower(res.Error), "denied") {
		t.Fatalf("%+v", res)
	}
	var n1 int
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.MeldungKommentare`).Scan(&n1))
	if n1 != n0 {
		t.Fatalf("Kommentare %d -> %d", n0, n1)
	}
}

// Eine Sperre im Fremdsystem hält den Versuch auf: nach der Gesamtzeit wird
// abgebrochen und zurückgenommen, das Labor bleibt unverändert.
func TestProbeWriteDeadlineRollsBack(t *testing.T) {
	db := openLab(t)
	ctx := context.Background()
	ms, err := DialectFor("mssql")
	must(t, err)
	f := NewSQLForeign(db, ms, 30*time.Second)
	p, maps := labProfile()
	key, nr := labKey(t, f, ms, p, maps)
	before := readLab(t, db, nr)

	var holder *sql.Tx
	release := func() {
		if holder != nil {
			_ = holder.Rollback()
		}
	}
	t.Cleanup(release)
	env := probeEnv{total: 3 * time.Second, hook: func(step string) {
		if step != "begun" {
			return
		}
		// Ein anderer Benutzer sperrt die Meldung (eigene Transaktion, wird zurückgenommen).
		var err error
		holder, err = db.BeginTx(ctx, nil)
		must(t, err)
		_, err = holder.ExecContext(ctx, `UPDATE dbo.Meldungen SET Titel = Titel WHERE MeldungNr = @p1`, nr)
		must(t, err)
		time.AfterFunc(6*time.Second, release)
	}}
	start := time.Now()
	res := probeWrite(ctx, newSQLProbeConn(db, ms, 30*time.Second), ms, p, maps, key,
		map[string]string{"title": "PDH-Probe Sperre"}, true, env)
	took := time.Since(start)
	t.Logf("Schreibversuch nach %v: OK=%v RolledBack=%v Fehler=%q", took.Round(time.Millisecond), res.OK, res.RolledBack, res.Error)
	if res.OK || !res.RolledBack || res.Error == "" {
		t.Fatalf("%+v", res)
	}
	assertLabUnchanged(t, before, readLab(t, db, nr))
}
