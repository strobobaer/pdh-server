package extsync

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Attrappe hinter *sql.DB ──────────────────────────────────────────────
//
// fdConnector ist ein Treiber für database/sql, der die Befehle von
// fakeDialect an fakeForeign weitergibt. So läuft der Schreibversuch über
// dieselben Wege wie im Betrieb (*sql.DB, BeginTx, *sql.Tx), und die Zähler
// zeigen, was der Treiber tatsächlich bekam: Beginn, Commit, Rollback. Ein
// Rollback stellt den Stand vom Beginn wieder her (wie eine Datenbank); mit
// noRestore bleibt er stehen (Rücknahme "verloren").

type fdStats struct {
	mu                        sync.Mutex
	begins, commits, rollback int
	execs                     []Stmt // jeder Schreibbefehl an den Treiber
	noRestore                 bool
	// fail läuft vor jedem Befehl (inTx: in der Transaktion); ein Fehler wird
	// zurückgegeben, eine Panik bleibt eine Panik im Treiber.
	fail func(st Stmt, inTx bool) error
}

func (s *fdStats) counts() (begins, commits, rollbacks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.begins, s.commits, s.rollback
}

type fdConnector struct {
	ff *fakeForeign
	st *fdStats
}

func (c *fdConnector) Connect(context.Context) (driver.Conn, error) { return &fdConn{c: c}, nil }
func (c *fdConnector) Driver() driver.Driver                        { return fdDriver{} }

type fdDriver struct{}

func (fdDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("attrappe: nur über Connector")
}

type fdSnap struct {
	rows     []map[string]any
	children map[string][]map[string]any
	rv       uint64
}

func copyRows(in []map[string]any) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, r := range in {
		cp := map[string]any{}
		for k, v := range r {
			cp[k] = v
		}
		out[i] = cp
	}
	return out
}

type fdConn struct {
	c  *fdConnector
	tx *fdTx
}

type fdTx struct {
	conn *fdConn
	snap fdSnap
}

func (c *fdConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("attrappe: Prepare nicht vorgesehen")
}
func (c *fdConn) Close() error { return nil }
func (c *fdConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fdConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	ff := c.c.ff
	ff.mu.Lock()
	snap := fdSnap{rows: copyRows(ff.rows), children: map[string][]map[string]any{}, rv: ff.rv}
	for n, ch := range ff.children {
		snap.children[n] = copyRows(ch.rows)
	}
	ff.mu.Unlock()
	c.c.st.mu.Lock()
	c.c.st.begins++
	c.c.st.mu.Unlock()
	c.tx = &fdTx{conn: c, snap: snap}
	return c.tx, nil
}

func (t *fdTx) Commit() error {
	st := t.conn.c.st
	st.mu.Lock()
	st.commits++
	st.mu.Unlock()
	t.conn.tx = nil
	return nil
}

func (t *fdTx) Rollback() error {
	st := t.conn.c.st
	st.mu.Lock()
	st.rollback++
	restore := !st.noRestore
	st.mu.Unlock()
	if restore {
		ff := t.conn.c.ff
		ff.mu.Lock()
		ff.rows = t.snap.rows
		ff.rv = t.snap.rv
		for n, rows := range t.snap.children {
			if ch := ff.children[n]; ch != nil {
				ch.rows = rows
			}
		}
		ff.mu.Unlock()
	}
	t.conn.tx = nil
	return nil
}

// CheckNamedValue nimmt die Befehlsdaten der Attrappe unverändert an.
func (c *fdConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fdConn) stmt(query string, args []driver.NamedValue) Stmt {
	st := Stmt{SQL: query}
	for _, a := range args {
		st.Args = append(st.Args, a.Value)
	}
	return st
}

func (c *fdConn) before(st Stmt) error {
	c.c.st.mu.Lock()
	fail := c.c.st.fail
	c.c.st.mu.Unlock()
	if fail != nil {
		return fail(st, c.tx != nil)
	}
	return nil
}

func (c *fdConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	st := c.stmt(query, args)
	c.c.st.mu.Lock()
	c.c.st.execs = append(c.c.st.execs, st)
	c.c.st.mu.Unlock()
	if err := c.before(st); err != nil {
		return nil, err
	}
	n, err := c.c.ff.Exec(ctx, st)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(n), nil
}

func (c *fdConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	st := c.stmt(query, args)
	if err := c.before(st); err != nil {
		return nil, err
	}
	rows, err := c.c.ff.Query(ctx, st)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			set[k] = true
		}
	}
	cols := make([]string, 0, len(set))
	for k := range set {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return &fdRows{cols: cols, rows: rows}, nil
}

type fdRows struct {
	cols []string
	rows []map[string]any
	i    int
}

func (r *fdRows) Columns() []string { return r.cols }
func (r *fdRows) Close() error      { return nil }
func (r *fdRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	for j, c := range r.cols {
		dest[j] = r.rows[r.i][c]
	}
	r.i++
	return nil
}

// ffDialect liest den Aufbau aus der Attrappe statt aus dem Katalog.
type ffDialect struct {
	Dialect
	ff *fakeForeign
}

func (d ffDialect) Describe(ctx context.Context, _ *sql.DB, schema, table string) (*TableInfo, error) {
	return d.ff.Describe(ctx, schema, table)
}

// probeFake: Attrappe mit drei Meldungen hinter *sql.DB, Profil mit Titel
// "beides" (hinausgehend) und auf Wunsch Kommentartabelle.
type probeFake struct {
	ff   *fakeForeign
	st   *fdStats
	db   *sql.DB
	rd   *recDialect
	p    *Profile
	maps []FieldMap
}

func newProbeFake(t *testing.T, comments bool) *probeFake {
	t.Helper()
	ff := newFakeForeign(fakeMeldungenInfo(true))
	for i, ti := range []string{"Pumpe", "Band", "Lampe"} {
		ff.put(map[string]any{"Nr": int64(i + 1), "Titel": ti, "Beschreibung": "Text " + ti, "Prio": "hoch", "Zustand": "offen"})
	}
	st := &fdStats{}
	db := sql.OpenDB(&fdConnector{ff: ff, st: st})
	t.Cleanup(func() { db.Close() })
	pf := &probeFake{ff: ff, st: st, db: db, rd: &recDialect{Dialect: ffDialect{Dialect: fakeDialect{}, ff: ff}},
		p: &Profile{ID: "probe", Name: "Probe", TargetType: RefTicket, SourceSchema: "dbo", SourceTable: "Meldungen",
			KeyColumns: []string{"Nr"}, ChangeColumn: "RV", ChangeKind: changeKindRowVersion, TimeZone: "UTC"},
		maps: stdMaps(RefTicket)}
	if comments {
		ff.addChild(kommentareInfo())
		ff.mu.Lock()
		ff.children["dbo.meldungkommentare"].rows = []map[string]any{{"Id": int64(1), "MeldungNr": int64(1), "Text": "alt"}}
		ff.mu.Unlock()
		pf.p.CommentTable = stdCommentTable
	}
	return pf
}

func (pf *probeFake) probe(sample map[string]string, allowWrite bool) WriteProbe {
	return ProbeWrite(context.Background(), pf.db, pf.rd, pf.p, pf.maps, k(1), sample, allowWrite, 5*time.Second)
}

// rowsNow: Stand der Attrappe (Meldungen und Kommentare) zum Vergleich.
func (pf *probeFake) rowsNow() ([]map[string]any, []map[string]any) {
	pf.ff.mu.Lock()
	defer pf.ff.mu.Unlock()
	var kids []map[string]any
	if c := pf.ff.children["dbo.meldungkommentare"]; c != nil {
		kids = copyRows(c.rows)
	}
	return copyRows(pf.ff.rows), kids
}

// ── Brauchen Schreibversuch? ─────────────────────────────────────────────

func TestNeedsWriteTest(t *testing.T) {
	fixed := "x"
	base := func() *Profile {
		return &Profile{TargetType: RefTicket, KeyColumns: []string{"Nr"}}
	}
	withCT := func(keys ...string) *Profile {
		p := base()
		p.KeyColumns = keys
		p.CommentTable = stdCommentTable
		return p
	}
	cases := []struct {
		name string
		maps []FieldMap
		p    *Profile
		want bool
	}{
		{"ohne Zuordnung", nil, base(), false},
		{"nur herein", []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}, base(), false},
		{"hinaus", []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirOut}}, base(), true},
		{"beides", []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirBoth}}, base(), true},
		{"fester Wert", []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirOut, FixedValue: &fixed}}, base(), false},
		{"ohne Fremdspalte", []FieldMap{{PDHField: "title", Direction: DirOut}}, base(), false},
		{"unbekanntes Feld", []FieldMap{{ForeignColumn: "X", PDHField: "gibtsnicht", Direction: DirOut}}, base(), false},
		{"Kommentartabelle", nil, withCT("Nr"), true},
		{"Kommentartabelle, zusammengesetzter Schlüssel", nil, withCT("Id", "Werk"), false},
		{"ohne Profil", []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirOut}}, nil, false},
	}
	for _, c := range cases {
		if got := NeedsWriteTest(c.maps, c.p); got != c.want {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.want)
		}
	}
}

// ── Zeilen zur Auswahl ───────────────────────────────────────────────────

func TestProbeRowsOffersKeysFromRows(t *testing.T) {
	pf := newProbeFake(t, false)
	ctx := context.Background()
	rows, err := ProbeRows(ctx, pf.ff, pf.rd, pf.p, pf.maps, 2)
	must(t, err)
	if len(rows) != 2 || rows[0].Key != k(1) || rows[1].Key != k(2) {
		t.Fatalf("Zeilen: %+v", rows)
	}
	if rows[0].Values["title"] != "Pumpe" || rows[0].Values["priority"] != "high" || rows[0].Values["status"] != "open" {
		t.Fatalf("Anzeige: %+v", rows[0].Values)
	}
	// Höchstens 20, ohne Angabe 20.
	for i := 4; i <= 30; i++ {
		pf.ff.put(map[string]any{"Nr": int64(i), "Titel": "T", "Zustand": "offen"})
	}
	for _, lim := range []int{0, -1, 25, 1000} {
		rows, err = ProbeRows(ctx, pf.ff, pf.rd, pf.p, pf.maps, lim)
		must(t, err)
		if len(rows) != ProbeRowsMax {
			t.Fatalf("Grenze %d: %d Zeilen", lim, len(rows))
		}
	}
	// Jeder Schlüssel ist genau der Text, den DryOutbound/ProbeWrite annehmen.
	for _, r := range rows {
		if _, err := DecodeKey(r.Key); err != nil {
			t.Fatalf("Schlüssel %q: %v", r.Key, err)
		}
	}
	if len(pf.ff.execStmts()) != 0 {
		t.Fatal("ProbeRows hat geschrieben")
	}
}

// ── Trockenlauf hinaus ───────────────────────────────────────────────────

func TestDryOutboundShowsStatement(t *testing.T) {
	pf := newProbeFake(t, true)
	ctx := context.Background()
	out := DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, k(1), map[string]string{"title": "Neu"})
	if len(out.Errors) != 0 {
		t.Fatalf("Fehler: %+v", out.Errors)
	}
	u, ok := out.Stmt.Args[0].(fakeUpdate)
	if !ok || out.Stmt.SQL != "update" || !reflect.DeepEqual(u.set, map[string]any{"Titel": "Neu"}) || !reflect.DeepEqual(u.keyVals, []string{"1"}) {
		t.Fatalf("Befehl: %+v", out.Stmt)
	}
	if out.SQL != "update" || len(out.Params) != 1 || out.Values["title"] != "Neu" || out.Key != k(1) {
		t.Fatalf("Anzeige: %+v", out)
	}
	if out.CommentStmt == nil || out.CommentStmt.SQL != "insert" {
		t.Fatalf("Kommentarbefehl: %+v", out.CommentStmt)
	}
	ins := out.CommentStmt.Args[0].(fakeInsert)
	if ins.values["Text"] != ProbeCommentText || ins.values["Verfasser"] != "PDH" || ins.values["MeldungNr"] != "1" {
		t.Fatalf("Kommentar: %+v", ins.values)
	}
	if len(pf.ff.execStmts()) != 0 || pf.st.begins != 0 {
		t.Fatal("Trockenlauf hat geschrieben")
	}

	// Echter T-SQL-Dialekt: Text und Parameter zur Anzeige.
	ms, err := DialectFor("mssql")
	must(t, err)
	src := fakeMeldungenInfo(true)
	rf := &recForeign{src: src, child: kommentareInfo()}
	p := *pf.p
	p.CommentTable = nil
	p.KeyColumns = []string{"Nr"}
	out = DryOutbound(ctx, rf, ms, &p, pf.maps, k(1), map[string]string{"title": "Neu 'x'"})
	if len(out.Errors) != 0 {
		t.Fatalf("Fehler: %+v", out.Errors)
	}
	if out.SQL != "UPDATE TOP (1) [dbo].[Meldungen] SET [Titel]=@p1 WHERE [Nr]=@p2" {
		t.Fatalf("SQL: %s", out.SQL)
	}
	if !reflect.DeepEqual(out.Params, []string{`@p1 = "Neu 'x'"`, `@p2 = "1"`}) {
		t.Fatalf("Parameter: %q", out.Params)
	}
	if len(rf.log) != 0 {
		t.Fatal("Trockenlauf hat geschrieben")
	}
}

func TestDryOutboundShowsTruncationAndZone(t *testing.T) {
	pf := newProbeFake(t, false)
	ctx := context.Background()
	for i := range pf.ff.info.Columns {
		if pf.ff.info.Columns[i].Name == "Titel" {
			pf.ff.info.Columns[i].MaxLen = 10
		}
	}
	pf.p.TimeZone = "Europe/Berlin"
	pf.maps = append(pf.maps, FieldMap{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirOut, Position: 9})
	out := DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, k(1),
		map[string]string{"title": "Ein sehr langer Titel", "due_date": "2026-07-01T10:00:00Z"})
	if len(out.Errors) != 0 {
		t.Fatalf("Fehler: %+v", out.Errors)
	}
	set := out.Stmt.Args[0].(fakeUpdate).set
	if set["Titel"] != "Ein sehr l" {
		t.Fatalf("gekürzt: %q", set["Titel"])
	}
	// 10:00 UTC im Sommer = 12:00 Wanduhr Berlin (zonenlose Spalte).
	if tm, ok := set["Faellig"].(time.Time); !ok || !tm.Equal(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("Zeit: %v", set["Faellig"])
	}
	if !reflect.DeepEqual(out.Truncated, []string{"title"}) {
		t.Fatalf("Kürzung nicht gemeldet: %v", out.Truncated)
	}
	if len(out.Params) != 1 || !strings.Contains(out.Params[0], "Ein sehr l") {
		t.Fatalf("Anzeige der gesendeten Werte: %q", out.Params)
	}
}

func TestDryOutboundErrorsAndCurrentValues(t *testing.T) {
	pf := newProbeFake(t, false)
	ctx := context.Background()
	pf.maps = append(pf.maps,
		FieldMap{ForeignColumn: "Prio", PDHField: "priority", Direction: DirOut, Translate: stdTranslatePrio, Position: 8},
		FieldMap{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirOut, Position: 9})
	// Probewerte prüfen: unbekanntes Feld, nicht übersetzbarer Wert, kein Datum.
	out := DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, k(1),
		map[string]string{"gibtsnicht": "x", "priority": "critical", "due_date": "morgen"})
	if len(out.Errors) != 3 || out.Stmt.SQL != "" {
		t.Fatalf("Prüfung: %+v", out)
	}
	// Felder ohne hinausgehende Zuordnung werden nicht geschrieben.
	out = DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, k(1), map[string]string{"description": "x", "title": "T"})
	if len(out.Errors) != 0 || !reflect.DeepEqual(out.Stmt.Args[0].(fakeUpdate).set, map[string]any{"Titel": "T"}) {
		t.Fatalf("nur hinaus: %+v", out)
	}
	// Ohne Probewerte: die aktuellen Werte der Textfelder (geringster Eingriff).
	out = DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, k(2), nil)
	if len(out.Errors) != 0 || !reflect.DeepEqual(out.Stmt.Args[0].(fakeUpdate).set, map[string]any{"Titel": "Band"}) {
		t.Fatalf("aktuelle Werte: %+v", out)
	}
	// Unbekannte Meldung, unlesbarer Schlüssel.
	for _, key := range []string{k(99), "kaputt", `["1","2"]`} {
		out = DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, key, map[string]string{"title": "T"})
		if len(out.Errors) != 1 || out.Stmt.SQL != "" {
			t.Fatalf("Schlüssel %s: %+v", key, out)
		}
	}
	if out := DryOutbound(ctx, pf.ff, pf.rd, pf.p, pf.maps, k(99), nil); out.Errors[0].Message != "Meldung nicht gefunden" {
		t.Fatalf("Text: %+v", out.Errors)
	}
	if len(pf.ff.execStmts()) != 0 {
		t.Fatal("Trockenlauf hat geschrieben")
	}
}

// ── Schreibversuch (Attrappe) ────────────────────────────────────────────

// Kein Weg führt zu Commit: der Treiber bekommt nie ein Commit, jede begonnene
// Transaktion wird zurückgenommen – bei Erfolg, Fehler im Ändern, im
// Zurücklesen, im Kommentar und bei einer Panik im Treiber.
func TestProbeWriteNeverCommits(t *testing.T) {
	boom := errors.New("Datenbank lehnt ab")
	cases := []struct {
		name string
		fail func(st Stmt, inTx bool) error
		ok   bool
	}{
		{"Erfolg", nil, true},
		{"Ändern scheitert", func(st Stmt, inTx bool) error {
			if st.SQL == "update" {
				return boom
			}
			return nil
		}, false},
		{"Zurücklesen scheitert", func(st Stmt, inTx bool) error {
			if st.SQL == "one" && inTx {
				return boom
			}
			return nil
		}, false},
		{"Kommentar scheitert", func(st Stmt, inTx bool) error {
			if st.SQL == "insert" {
				return boom
			}
			return nil
		}, false},
		{"Panik im Treiber", func(st Stmt, inTx bool) error {
			if st.SQL == "update" {
				panic("Treiber kaputt")
			}
			return nil
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pf := newProbeFake(t, true)
			before, kidsBefore := pf.rowsNow()
			pf.st.fail = c.fail
			res := pf.probe(map[string]string{"title": "Probe"}, true)
			begins, commits, rollbacks := pf.st.counts()
			if commits != 0 {
				t.Fatalf("Commit %d mal", commits)
			}
			if begins != 1 || rollbacks != 1 {
				t.Fatalf("Transaktionen %d, Rücknahmen %d", begins, rollbacks)
			}
			if res.OK != c.ok || !res.RolledBack {
				t.Fatalf("Ergebnis: %+v", res)
			}
			if !c.ok && res.Error == "" {
				t.Fatal("Fehler ohne Text")
			}
			after, kidsAfter := pf.rowsNow()
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(kidsBefore, kidsAfter) {
				t.Fatalf("Stand verändert:\n%v\n%v", before, after)
			}
			assertOnlyDialectWrites(t, pf.ff, pf.rd)
		})
	}
}

// Panik des Treibers mitten in einer Abfrage der Transaktion: database/sql
// behält dann eine interne Sperre, tx.Rollback kehrt nie zurück. Der Versuch
// hängt trotzdem nicht, bestätigt nie und meldet die Rücknahme laut als nicht
// bestätigt (die Attrappe nimmt ohne Rollback nichts zurück).
func TestProbeWriteDriverPanicInQuery(t *testing.T) {
	pf := newProbeFake(t, true)
	pf.st.fail = func(st Stmt, inTx bool) error {
		if st.SQL == "one" && inTx {
			panic("Treiber kaputt")
		}
		return nil
	}
	done := make(chan WriteProbe, 1)
	go func() {
		done <- ProbeWrite(context.Background(), pf.db, pf.rd, pf.p, pf.maps, k(1), map[string]string{"title": "Probe"}, true, time.Second)
	}()
	var res WriteProbe
	select {
	case res = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Schreibversuch hängt")
	}
	_, commits, _ := pf.st.counts()
	if commits != 0 || res.OK || res.RolledBack || !strings.Contains(res.Error, RollbackUnconfirmed) {
		t.Fatalf("Commit %d, Ergebnis %+v", commits, res)
	}
}

// Im Quelltext des Schreibversuchs steht kein Commit-Aufruf.
func TestProbeSourceHasNoCommit(t *testing.T) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "probe.go", nil, 0)
	must(t, err)
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Commit" {
			t.Errorf("%s: Commit im Schreibversuch", fs.Position(sel.Pos()))
		}
		return true
	})
}

func TestProbeWriteVerifiesRollback(t *testing.T) {
	pf := newProbeFake(t, true)
	res := pf.probe(map[string]string{"title": "Probe"}, true)
	if !res.OK || !res.RolledBack || res.Error != "" {
		t.Fatalf("Erfolg: %+v", res)
	}
	if res.ReadBack["title"] != "Probe" {
		t.Fatalf("Zurückgelesen: %+v", res.ReadBack)
	}
	if !strings.HasPrefix(res.Statement, "update") || !strings.HasPrefix(res.CommentStatement, "insert") {
		t.Fatalf("Befehle: %q %q", res.Statement, res.CommentStatement)
	}
	// Die Rücknahme "geht verloren": laut gemeldet.
	pf = newProbeFake(t, true)
	pf.st.noRestore = true
	res = pf.probe(map[string]string{"title": "Probe"}, true)
	if res.OK || res.RolledBack || !strings.Contains(res.Error, RollbackUnconfirmed) {
		t.Fatalf("verlorene Rücknahme: %+v", res)
	}
	// Nur der Kommentar bleibt: ebenfalls gemeldet.
	pf = newProbeFake(t, true)
	pf.st.fail = func(st Stmt, inTx bool) error {
		if st.SQL == "insert" {
			pf.st.noRestore = true
		}
		return nil
	}
	pf.maps = []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}
	res = pf.probe(nil, true)
	if res.OK || res.RolledBack || !strings.Contains(res.Error, RollbackUnconfirmed) {
		t.Fatalf("Kommentar geblieben: %+v", res)
	}
}

func TestProbeWriteNotAllowed(t *testing.T) {
	pf := newProbeFake(t, true)
	res := pf.probe(map[string]string{"title": "Probe"}, false)
	if res.OK || res.Error != "Schreiben ist an der Verbindung nicht erlaubt" {
		t.Fatalf("%+v", res)
	}
	if b, _, _ := pf.st.counts(); b != 0 || len(pf.st.execs) != 0 {
		t.Fatalf("Transaktion %d, Befehle %d", b, len(pf.st.execs))
	}
}

func TestProbeWriteRowNotFound(t *testing.T) {
	pf := newProbeFake(t, true)
	res := ProbeWrite(context.Background(), pf.db, pf.rd, pf.p, pf.maps, k(99), map[string]string{"title": "x"}, true, 5*time.Second)
	if res.OK || res.Error != "Meldung nicht gefunden" {
		t.Fatalf("%+v", res)
	}
	if b, _, _ := pf.st.counts(); b != 0 || len(pf.st.execs) != 0 {
		t.Fatalf("Transaktion %d, Befehle %d", b, len(pf.st.execs))
	}
	// Ungültige Probewerte: nichts gesendet.
	res = pf.probe(map[string]string{"due_date": "morgen", "gibtsnicht": "1"}, true)
	if res.OK || len(res.Errors) != 1 {
		t.Fatalf("Probewerte: %+v", res)
	}
	if b, _, _ := pf.st.counts(); b != 0 {
		t.Fatal("Transaktion trotz Fehler in den Probewerten")
	}
}

func TestProbeWriteNoOutboundMappings(t *testing.T) {
	pf := newProbeFake(t, false)
	pf.maps = []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}
	if NeedsWriteTest(pf.maps, pf.p) {
		t.Fatal("NeedsWriteTest")
	}
	res := pf.probe(map[string]string{"title": "x"}, true)
	if res.OK || res.Error != "Dieses Profil schreibt nichts ins Fremdsystem" {
		t.Fatalf("%+v", res)
	}
	if b, _, _ := pf.st.counts(); b != 0 {
		t.Fatal("Transaktion begonnen")
	}
}

func TestProbeWriteWarnings(t *testing.T) {
	pf := newProbeFake(t, false)
	res := pf.probe(map[string]string{"title": "x"}, true)
	if !res.OK || len(res.Triggers) != 0 || !reflect.DeepEqual(res.Warnings, []string{WarnCounters}) {
		t.Fatalf("ohne Auslöser: %+v", res)
	}
	pf = newProbeFake(t, true)
	pf.ff.info.Triggers = []string{"trg_A"}
	pf.ff.children["dbo.meldungkommentare"].info.Triggers = []string{"trg_K"}
	trg, err := ProbeTriggers(context.Background(), pf.ff, pf.p)
	must(t, err)
	if !reflect.DeepEqual(trg, []string{"trg_A", "trg_K"}) {
		t.Fatalf("Auslöser: %v", trg)
	}
	res = pf.probe(map[string]string{"title": "x"}, true)
	if !reflect.DeepEqual(res.Triggers, trg) || !reflect.DeepEqual(res.Warnings, []string{WarnTriggers, WarnCounters}) {
		t.Fatalf("mit Auslösern: %+v", res)
	}
	// Ohne Schreiben (nicht erlaubt): keine Warnung zu Zählern.
	res = pf.probe(map[string]string{"title": "x"}, false)
	for _, w := range res.Warnings {
		if w == WarnCounters {
			t.Fatalf("Zählerwarnung ohne Schreiben: %+v", res)
		}
	}
}
