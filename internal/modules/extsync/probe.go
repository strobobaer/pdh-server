package extsync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Test im Cockpit (Entwurf 4.6) – Regeln (verbindlich):
//
//   - Stufe 1, Trockenlauf (DryStage): schreibt nirgends – weder ins
//     Fremdsystem noch ins PDH (keine Verknüpfung, kein Lauf, kein Verlauf,
//     kein Zustand). Herein ist es Inbound.Run mit RunOptions{Dry: true};
//     hinaus zeigt DryOutbound für eine gewählte Meldung den genauen Befehl mit
//     Parametern, so wie er gesendet würde (Zeitzone, Kürzung).
//   - Stufe 2, Schreibversuch (ProbeWrite): neben dem Ausgang die EINZIGE
//     Stelle, die schreibende Befehle an die Fremddatenbank schickt – und sie
//     nimmt sie IMMER zurück. Garantie durch Aufbau: der Versuch sieht die
//     Transaktion nur als probeTx, und probeTx hat kein Commit. Rollback ist per
//     defer gleich nach dem Beginn eingetragen und läuft bei Erfolg, Fehler und
//     Panik. Die Transaktion beginnt mit dem Kontext der Gesamtzeit
//     (höchstens probeTotalMax): läuft sie ab, nimmt database/sql die
//     Transaktion auch selbst zurück, eine Sperre im Fremdsystem hält sie also
//     nie unbegrenzt offen. Jeder Befehl hat zusätzlich die Zeitgrenze der
//     Verbindung. Ein Befehl wie SET LOCK_TIMEOUT wird nicht gesendet (der
//     Dialekt kennt keinen, und freies SQL gibt es nicht); es gelten allein die
//     Zeitgrenzen, und beim ersten Fehler wird zurückgenommen.
//   - Befehle entstehen nur über den Dialekt (SelectOne, SelectRows,
//     UpdateRow, InsertChild) und mit denselben Funktionen wie der Ausgang
//     (buildOutUpdate, commentValues, outReadMaps, foreignPDH). Der Schlüssel
//     stammt immer aus einer gelesenen Zeile (ProbeRows) und wird vor jeder
//     Verwendung durch erneutes Lesen geprüft.
//   - Nach der Rücknahme wird außerhalb der Transaktion neu gelesen: die ganze
//     Zeile (alle Spalten, auch Änderungszähler) muss gleich sein wie vorher,
//     die Zahl der Kommentare zur Meldung ebenso. Sonst meldet das Ergebnis laut
//     RollbackUnconfirmed.
//   - Was eine Rücknahme NICHT aufhebt: Wirkungen von Auslösern außerhalb der
//     Transaktion (Nachrichten, Aufrufe anderer Systeme, Verbindungsserver) und
//     verbrauchte laufende Nummern bzw. Änderungszähler (WarnTriggers,
//     WarnCounters).

// Teststufen (Store.MarkTest).
const (
	TestStageDry   = "dry"
	TestStageWrite = "write"
)

// ProbeRowsMax: so viele Meldungen bietet ProbeRows höchstens zur Auswahl an.
const ProbeRowsMax = 20

// Texte des Tests (für die Oberfläche).
const (
	// ProbeCommentText ist der Text des Probekommentars (Verfasser "PDH").
	ProbeCommentText = "PDH-Test"
	// RollbackUnconfirmed: nach dem Versuch ist die Meldung (oder die Zahl ihrer
	// Kommentare) nicht wie vorher.
	RollbackUnconfirmed = "Achtung: Rücknahme nicht bestätigt – bitte die Meldung im Fremdsystem prüfen"
	// WarnTriggers steht im Ergebnis, wenn eine betroffene Tabelle Auslöser hat.
	WarnTriggers = "Auslöser an der Tabelle können Wirkungen haben, die eine Rücknahme nicht aufhebt (z. B. Nachrichten oder Aufrufe anderer Systeme)."
	// WarnCounters steht im Ergebnis, sobald ein Schreibbefehl gesendet wurde.
	WarnCounters = "Laufende Nummern und Änderungszähler des Fremdsystems werden durch den Versuch weitergezählt."
	// WarnCommentsNotCounted: die Kommentare ließen sich nicht nachzählen.
	WarnCommentsNotCounted = "Die Kommentare der Meldung wurden nicht nachgezählt (Kommentartabelle zu groß oder ohne eindeutigen Schlüssel); die Rücknahme gilt für die ganze Transaktion."
	// DryNeedsRow: der Trockenlauf hinaus braucht eine gewählte Meldung.
	DryNeedsRow = "Für den Trockenlauf hinaus eine Meldung wählen"
	// ProbeNothingToWrite: das Profil hat weder hinausgehende Zuordnungen noch Kommentare.
	ProbeNothingToWrite = "Dieses Profil schreibt nichts ins Fremdsystem"
	// ProbeRowNotFound: die gewählte Meldung gibt es (nicht mehr).
	ProbeRowNotFound = "Meldung nicht gefunden"
	probeBadKey      = "Schlüssel der Meldung ist ungültig"
	probeNoValues    = "Kein Wert zum Schreiben – bitte Probewerte für hinausgehende Felder angeben"
)

const (
	// probeTotalMax: Gesamtzeit eines Schreibversuchs (Transaktion offen höchstens so lange).
	probeTotalMax = 60 * time.Second
	// probeChildPage und probeChildScanLimit: Nachzählen der Kommentare seitenweise,
	// höchstens so viele Zeilen der Kommentartabelle.
	probeChildPage      = 1000
	probeChildScanLimit = 100000
)

// ErrNotTested: Store.Arm lehnt ab, weil eine nötige Teststufe fehlt. Der
// Fehler ist ein *NotTestedError mit der Stufe.
var ErrNotTested = errors.New("Profil ist nicht getestet")

// NotTestedError nennt die fehlende Teststufe (TestStageDry oder TestStageWrite).
type NotTestedError struct{ Stage string }

func (e *NotTestedError) Error() string {
	if e.Stage == TestStageWrite {
		return "Erst den Schreibversuch bestehen"
	}
	return "Erst den Trockenlauf bestehen"
}

// Is macht errors.Is(err, ErrNotTested) wahr.
func (e *NotTestedError) Is(target error) bool { return target == ErrNotTested }

// writesComments: der Ausgang hängt Kommentare an (Kommentartabelle vollständig
// und einspaltiger Schlüssel, siehe Outbound.comment).
func writesComments(p *Profile) bool {
	ct := p.CommentTable
	return ct != nil && ct.Table != "" && ct.FKColumn != "" && ct.TextColumn != "" && len(p.KeyColumns) == 1
}

// NeedsWriteTest: das Profil schreibt ins Fremdsystem – es hat eine
// hinausgehende Zuordnung, die der Ausgang schreiben würde (outMaps), oder
// hängt Kommentare an. Nur dann ist Stufe 2 (Schreibversuch) nötig.
func NeedsWriteTest(maps []FieldMap, p *Profile) bool {
	if p == nil {
		return false
	}
	if writesComments(p) {
		return true
	}
	fields := Fields(p.TargetType)
	return fields != nil && len(outMaps(maps, fields)) > 0
}

// ── Lesen ────────────────────────────────────────────────────────────────

// foreignReader liest die Fremddatenbank (Foreign oder die Verbindung des Schreibversuchs).
type foreignReader interface {
	Describe(ctx context.Context, schema, table string) (*TableInfo, error)
	Query(ctx context.Context, st Stmt) ([]map[string]any, error)
}

// allMappedReadMaps: alle zugeordneten Felder (jede Richtung, ohne festen Wert,
// im Feldkatalog) zum Lesen in PDH-Form, je Feld die erste Zuordnung.
func allMappedReadMaps(maps []FieldMap, fields []PDHField) ([]FieldMap, []string) {
	idx := fieldIndex(fields)
	byField := map[string]FieldMap{}
	var keys []string
	for _, m := range maps {
		if m.FixedValue != nil || m.ForeignColumn == "" {
			continue
		}
		if _, ok := idx[m.PDHField]; !ok {
			continue
		}
		if _, dup := byField[m.PDHField]; !dup {
			byField[m.PDHField] = m
			keys = append(keys, m.PDHField)
		}
	}
	return outReadMaps(byField, keys)
}

// columnNames: alle Spalten der Tabelle (für den Vergleich vor/nach dem Versuch).
func columnNames(t *TableInfo) []string {
	out := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		out = append(out, c.Name)
	}
	return out
}

// readRow liest die Zeile mit dem Schlüssel kv (alle Spalten); nil = keine.
func readRow(ctx context.Context, r foreignReader, d Dialect, t *TableInfo, keyCols, kv []string) (map[string]any, error) {
	st, err := d.SelectOne(t, columnNames(t), keyCols, kv)
	if err != nil {
		return nil, err
	}
	rows, err := r.Query(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("Fremddatenbank lesen: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// ProbeRow ist eine Meldung zur Auswahl für den Test: Key ist der Schlüssel
// (EncodeKey), Values die zugeordneten Felder in PDH-Form zur Anzeige.
type ProbeRow struct {
	Key    string
	Values map[string]string
}

// ProbeRows liest bis zu limit (höchstens ProbeRowsMax, 0 = ProbeRowsMax)
// Meldungen der Quelltabelle von Anfang an (Filter und Zeitfenster des
// Profils) zur Auswahl. Das Cockpit bietet nur diese Schlüssel an; ein
// Schlüssel wird nie eingetippt. Liest nur.
func ProbeRows(ctx context.Context, f Foreign, d Dialect, p *Profile, maps []FieldMap, limit int) ([]ProbeRow, error) {
	if p == nil || f == nil || d == nil {
		return nil, errors.New("abgleich: profil oder verbindung fehlt")
	}
	if limit <= 0 || limit > ProbeRowsMax {
		limit = ProbeRowsMax
	}
	fields := Fields(p.TargetType)
	if fields == nil {
		return nil, fmt.Errorf("Ziel %q ist unbekannt", p.TargetType)
	}
	t, err := f.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		return nil, fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	fLoc, err := p.ForeignLocation(time.Local)
	if err != nil {
		return nil, err
	}
	rm, cols := allMappedReadMaps(maps, fields)
	st, err := d.SelectRows(t, cols, p.KeyColumns, p.ChangeColumn, p.ChangeKind, "", p.FilterSQL, p.WindowDays, limit)
	if err != nil {
		return nil, err
	}
	rows, err := f.Query(ctx, st)
	if err != nil {
		return nil, fmt.Errorf("Fremddatenbank lesen: %w", err)
	}
	wall := wallColumns(t, d, rm, fields)
	out := make([]ProbeRow, 0, len(rows))
	for _, row := range rows {
		kv, err := d.KeyValues(t, p.KeyColumns, row)
		if err != nil {
			return nil, err
		}
		vals, _ := foreignPDH(rm, fields, row, wall, fLoc, nil) // nicht lesbare Werte fehlen
		out = append(out, ProbeRow{Key: EncodeKey(kv), Values: vals})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// ── Plan: was geschrieben würde ──────────────────────────────────────────

// probeEnv: Ort des Servers, Uhr, Gesamtzeit und (nur Tests) ein Haken.
type probeEnv struct {
	loc   *time.Location // nil = time.Local (wie Outbound/Inbound ohne Location)
	now   func() time.Time
	total time.Duration // 0 oder mehr als probeTotalMax = probeTotalMax
	// hook ist nur für Tests: "begun" läuft gleich nach dem Beginn der Transaktion.
	hook func(step string)
}

func (e probeEnv) location() *time.Location {
	if e.loc == nil {
		return time.Local
	}
	return e.loc
}

func (e probeEnv) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// probePlan ist das Ergebnis der Vorbereitung: gelesene Zeile, Werte und Befehle.
type probePlan struct {
	fields    []PDHField
	t, child  *TableInfo
	fLoc      *time.Location
	kv        []string
	before    map[string]any // die Zeile vorher (alle Spalten)
	out       map[string]FieldMap
	vals      map[string]string // PDH-Form, die geschrieben würden
	update    *Stmt
	truncated []string
	comment   *Stmt
	errs      []RunError
}

// planProbe liest die gewählte Meldung neu und baut die Befehle – genau wie
// der Ausgang (buildOutUpdate, commentValues). sample: PDH-Feld -> Probewert
// in PDH-Form; nur Felder mit hinausgehender Zuordnung zählen, fehlende werden
// nicht geschrieben. Ohne Probewerte gelten die aktuellen Werte der Textfelder
// der Meldung (der kleinste mögliche Eingriff; gibt es keine, alle
// hinausgehenden Felder mit Wert). Fehler der Werte stehen in plan.errs; dann
// gibt es keinen Befehl. err: die Fremddatenbank war nicht lesbar.
func planProbe(ctx context.Context, r foreignReader, d Dialect, p *Profile, maps []FieldMap, key string, sample map[string]string, env probeEnv) (*probePlan, error) {
	pl := &probePlan{fields: Fields(p.TargetType)}
	if pl.fields == nil {
		return nil, fmt.Errorf("Ziel %q ist unbekannt", p.TargetType)
	}
	kv, err := DecodeKey(key)
	if err != nil || len(kv) != len(p.KeyColumns) || len(kv) == 0 {
		pl.errs = []RunError{{ForeignKey: key, Message: probeBadKey}}
		return pl, nil
	}
	pl.kv = kv
	if pl.t, err = r.Describe(ctx, p.SourceSchema, p.SourceTable); err != nil {
		return nil, fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	if pl.fLoc, err = p.ForeignLocation(env.location()); err != nil {
		return nil, err
	}
	if pl.before, err = readRow(ctx, r, d, pl.t, p.KeyColumns, kv); err != nil {
		return nil, err
	}
	if pl.before == nil {
		pl.errs = []RunError{{ForeignKey: key, Message: ProbeRowNotFound}}
		return pl, nil
	}
	idx := fieldIndex(pl.fields)
	pl.out = outMaps(maps, pl.fields)
	pl.vals = map[string]string{}
	if len(sample) > 0 {
		keys := make([]string, 0, len(sample))
		for k := range sample {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f, ok := idx[k]
			if !ok {
				pl.errs = append(pl.errs, unknownFieldErr(k))
				continue
			}
			if _, ok := pl.out[k]; !ok {
				continue // nicht hinausgehend: wird nicht geschrieben
			}
			v, e := finishPDHValue(f, strings.TrimSpace(sample[k]))
			if e != nil {
				pl.errs = append(pl.errs, *e)
				continue
			}
			pl.vals[k] = v
		}
	} else if len(pl.out) > 0 {
		keys := make([]string, 0, len(pl.out))
		for k := range pl.out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rm, _ := outReadMaps(pl.out, keys)
		cur, _ := foreignPDH(rm, pl.fields, pl.before, wallColumns(pl.t, d, rm, pl.fields), pl.fLoc, nil)
		for _, textOnly := range []bool{true, false} {
			for _, k := range keys {
				kind := idx[k].Kind
				if v := cur[k]; v != "" && (!textOnly || (kind != fieldKindEnum && kind != fieldKindTime)) {
					pl.vals[k] = v
				}
			}
			if len(pl.vals) > 0 {
				break
			}
		}
	}
	if len(pl.out) > 0 && len(pl.vals) == 0 && len(pl.errs) == 0 {
		pl.errs = append(pl.errs, RunError{Message: probeNoValues})
	}
	// Auch bei Fehlern einzelner Probewerte die übrigen umsetzen: alle Fehler
	// (z. B. nicht übersetzbare Werte) auf einmal melden; gesendet wird dann nichts.
	if len(pl.vals) > 0 {
		st, cut, errs, err := buildOutUpdate(pl.t, d, p.KeyColumns, kv, maps, pl.fields, pl.vals, pl.fLoc)
		switch {
		case len(errs) > 0:
			pl.errs = append(pl.errs, errs...)
		case err != nil:
			pl.errs = append(pl.errs, RunError{Message: err.Error()})
		default:
			pl.update = &st
			for k := range cut {
				pl.truncated = append(pl.truncated, k)
			}
			sort.Strings(pl.truncated)
		}
	}
	if writesComments(p) {
		ct := p.CommentTable
		if pl.child, err = r.Describe(ctx, ct.Schema, ct.Table); err != nil {
			return nil, fmt.Errorf("Aufbau der Kommentartabelle %s.%s lesen: %w", ct.Schema, ct.Table, err)
		}
		st, err := d.InsertChild(pl.t, pl.child, commentValues(d, pl.child, ct, kv[0], ProbeCommentText, "PDH", env.clock(), pl.fLoc))
		if err != nil {
			pl.errs = append(pl.errs, RunError{Message: err.Error()})
		} else {
			pl.comment = &st
		}
	}
	for i := range pl.errs {
		pl.errs[i].ForeignKey = key
	}
	if len(pl.errs) > 0 {
		pl.update, pl.comment = nil, nil
	}
	return pl, nil
}

// renderStmt: Text und Parameter eines Befehls zur Anzeige ("@p1 = …").
func renderStmt(st *Stmt) (string, []string) {
	if st == nil {
		return "", nil
	}
	params := make([]string, len(st.Args))
	for i, a := range st.Args {
		params[i] = fmt.Sprintf("@p%d = %s", i+1, renderParam(a))
	}
	return st.SQL, params
}

func renderParam(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return strconv.Quote(x)
	case time.Time:
		return x.UTC().Format("2006-01-02 15:04:05.0000000")
	case []byte:
		return "0x" + strings.ToUpper(hex.EncodeToString(x))
	}
	return fmt.Sprint(v)
}

// ── Stufe 1: Trockenlauf ─────────────────────────────────────────────────

// DryOut ist der Trockenlauf hinaus für eine Meldung: der Änderungsbefehl
// (Stmt; SQL und Params zur Anzeige, Werte so, wie sie gesendet würden –
// Zeitzone umgerechnet, Text gekürzt), die Felder in PDH-Form (Values), die
// gekürzten Felder (Truncated) und – mit Kommentartabelle – der Befehl des
// Probekommentars. Errors: Fehler der Probewerte oder der Meldung; dann gibt
// es keinen Befehl.
type DryOut struct {
	Key           string
	Stmt          Stmt
	SQL           string
	Params        []string
	Values        map[string]string
	Truncated     []string
	CommentStmt   *Stmt
	CommentSQL    string
	CommentParams []string
	Errors        []RunError
}

// DryOutbound zeigt für die Meldung key (aus ProbeRows) und die Probewerte
// sample den genauen Befehl, den der Ausgang senden würde. Liest nur (die
// Meldung wird zur Prüfung neu gelesen), schreibt nirgends.
func DryOutbound(ctx context.Context, f Foreign, d Dialect, p *Profile, maps []FieldMap, key string, sample map[string]string) (out DryOut) {
	out.Key = key
	defer func() {
		if rec := recover(); rec != nil {
			out = DryOut{Key: key, Errors: []RunError{{ForeignKey: key, Message: fmt.Sprintf("Interner Fehler im Trockenlauf: %v", rec)}}}
		}
	}()
	if p == nil || f == nil || d == nil {
		out.Errors = []RunError{{Message: "Profil oder Verbindung fehlt"}}
		return out
	}
	pl, err := planProbe(ctx, f, d, p, maps, key, sample, probeEnv{})
	if err != nil {
		out.Errors = []RunError{{ForeignKey: key, Message: err.Error()}}
		return out
	}
	out.Errors = pl.errs
	out.Values = pl.vals
	out.Truncated = pl.truncated
	if pl.update != nil {
		out.Stmt = *pl.update
		out.SQL, out.Params = renderStmt(pl.update)
	}
	if pl.comment != nil {
		out.CommentStmt = pl.comment
		out.CommentSQL, out.CommentParams = renderStmt(pl.comment)
	}
	return out
}

// InboundDry ist der Trockenlauf herein: Lauf mit Zählern (New, Changed,
// Backfilled, Archived, Failed, CountTruncated) und Vorschau. Passed: der Lauf
// ist nicht gescheitert, keine Zeile hat Fehler (Run.Failed == 0) und keine
// Vorschau trägt Fehler.
type InboundDry struct {
	Run      *Run
	Previews []Preview
	Passed   bool
	Error    string
}

// DryInbound fährt den Probelauf herein (Inbound.Run, RunOptions{Dry: true}):
// liest, schreibt nichts.
func DryInbound(ctx context.Context, in *Inbound, p *Profile, maps []FieldMap, f Foreign, d Dialect) InboundDry {
	if in == nil {
		return InboundDry{Error: "abgleich: lauf fehlt"}
	}
	run, prev, err := in.Run(ctx, p, maps, f, d, RunOptions{Dry: true})
	res := InboundDry{Run: run, Previews: prev}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Passed = run != nil && run.Failed == 0 && run.SkippedReason == ""
	for _, pv := range prev {
		if len(pv.Errors) > 0 {
			res.Passed = false
		}
	}
	return res
}

// DryStageResult ist Stufe 1 als Ganzes. Passed: herein bestanden und – wenn
// das Profil schreibt (NeedsOutbound = NeedsWriteTest) – der Trockenlauf
// hinaus für die gewählte Meldung ohne Fehler.
type DryStageResult struct {
	Inbound       InboundDry
	Outbound      *DryOut
	NeedsOutbound bool
	Passed        bool
	Error         string
}

// DryStage fährt Stufe 1: herein DryInbound, hinaus DryOutbound für die
// Meldung key (aus ProbeRows; leer = keine gewählt). Schreibt nirgends; das
// Ergebnis hält der Aufrufer mit Store.MarkTest(…, TestStageDry, res.Passed) fest.
func DryStage(ctx context.Context, in *Inbound, p *Profile, maps []FieldMap, f Foreign, d Dialect, key string, sample map[string]string) DryStageResult {
	res := DryStageResult{Inbound: DryInbound(ctx, in, p, maps, f, d), NeedsOutbound: NeedsWriteTest(maps, p)}
	if res.NeedsOutbound {
		if key == "" {
			res.Error = DryNeedsRow
		} else {
			o := DryOutbound(ctx, f, d, p, maps, key, sample)
			res.Outbound = &o
		}
	}
	res.Passed = res.Inbound.Passed && (!res.NeedsOutbound || (res.Outbound != nil && len(res.Outbound.Errors) == 0))
	if res.Error == "" && res.Inbound.Error != "" {
		res.Error = res.Inbound.Error
	}
	return res
}

// ── Stufe 2: Schreibversuch mit Rücknahme ────────────────────────────────

// probeConn ist die Verbindung des Schreibversuchs: Lesen außerhalb jeder
// Transaktion und Beginn einer Transaktion, die nur zurückgenommen werden kann.
type probeConn interface {
	foreignReader
	Begin(ctx context.Context) (probeTx, error)
}

// probeTx ist eine Transaktion OHNE Commit: der Schreibversuch kann sie nur
// zurücknehmen.
type probeTx interface {
	Query(ctx context.Context, st Stmt) ([]map[string]any, error)
	Exec(ctx context.Context, st Stmt) (int64, error)
	Rollback() error
}

type sqlProbeConn struct {
	db      *sql.DB
	f       Foreign
	timeout time.Duration
}

// newSQLProbeConn: Verbindung über db; jeder Befehl hat die Zeitgrenze timeout
// (0 = 30 Sekunden).
func newSQLProbeConn(db *sql.DB, d Dialect, timeout time.Duration) probeConn {
	if timeout <= 0 {
		timeout = defaultForeignTimeout
	}
	return &sqlProbeConn{db: db, f: NewSQLForeign(db, d, timeout), timeout: timeout}
}

func (c *sqlProbeConn) Describe(ctx context.Context, schema, table string) (*TableInfo, error) {
	return c.f.Describe(ctx, schema, table)
}

func (c *sqlProbeConn) Query(ctx context.Context, st Stmt) ([]map[string]any, error) {
	return c.f.Query(ctx, st)
}

// Begin beginnt die Transaktion mit ctx (Gesamtzeit): läuft ctx ab, nimmt
// database/sql sie selbst zurück.
func (c *sqlProbeConn) Begin(ctx context.Context) (probeTx, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlProbeTx{tx: tx, timeout: c.timeout}, nil
}

// sqlProbeTx hält die *sql.Tx verborgen und gibt nur Lesen, Schreiben und
// Rollback weiter.
type sqlProbeTx struct {
	tx      *sql.Tx
	timeout time.Duration
}

func (t *sqlProbeTx) Query(ctx context.Context, st Stmt) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	rows, err := t.tx.QueryContext(ctx, st.SQL, st.Args...)
	if err != nil {
		return nil, err
	}
	return scanRowMaps(rows)
}

func (t *sqlProbeTx) Exec(ctx context.Context, st Stmt) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	res, err := t.tx.ExecContext(ctx, st.SQL, st.Args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Rollback nimmt zurück und wartet darauf höchstens die Zeitgrenze eines
// Befehls. Hintergrund: Bricht der Treiber mitten in einer Abfrage der
// Transaktion mit einer Panik ab, behält database/sql eine interne Sperre und
// Rollback kehrt nie zurück. Der Versuch hängt dann nicht; die Prüfung danach
// meldet die Rücknahme als nicht bestätigt (RollbackUnconfirmed).
func (t *sqlProbeTx) Rollback() error {
	done := make(chan error, 1)
	go func() { done <- t.tx.Rollback() }()
	select {
	case err := <-done:
		return err
	case <-time.After(t.timeout):
		return errors.New("Rücknahme antwortet nicht")
	}
}

// ProbeTriggers liefert die Auslöser der Quelltabelle und – wenn Kommentare
// geschrieben werden – der Kommentartabelle (aus Describe), ohne Doppelte.
// Das Cockpit zeigt sie vor dem Schreibversuch mit WarnTriggers an.
func ProbeTriggers(ctx context.Context, f Foreign, p *Profile) ([]string, error) {
	if p == nil || f == nil {
		return nil, errors.New("abgleich: profil oder verbindung fehlt")
	}
	return probeTriggers(ctx, f, p)
}

func probeTriggers(ctx context.Context, r foreignReader, p *Profile) ([]string, error) {
	t, err := r.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		return nil, fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	out := []string{}
	add := func(names []string) {
		for _, n := range names {
			if !containsStr(out, n) {
				out = append(out, n)
			}
		}
	}
	add(t.Triggers)
	if writesComments(p) {
		ct := p.CommentTable
		c, err := r.Describe(ctx, ct.Schema, ct.Table)
		if err != nil {
			return nil, fmt.Errorf("Aufbau der Kommentartabelle %s.%s lesen: %w", ct.Schema, ct.Table, err)
		}
		add(c.Triggers)
	}
	return out, nil
}

// WriteProbe ist das Ergebnis des Schreibversuchs.
//   - OK: geschrieben, zurückgelesen und die Rücknahme bestätigt.
//   - RolledBack: nach dem Versuch ist die Meldung (alle Spalten) und die Zahl
//     ihrer Kommentare wie vorher. false nach einem Versuch heißt: Error
//     beginnt mit RollbackUnconfirmed – laut anzeigen.
//   - Triggers: Auslöser der betroffenen Tabellen; Warnings: WarnTriggers (mit
//     Auslösern), WarnCounters (sobald geschrieben wurde), ggf.
//     WarnCommentsNotCounted.
//   - Statement/Params, CommentStatement/CommentParams: die gesendeten Befehle
//     zur Anzeige; Values die Felder in PDH-Form, Truncated die gekürzten.
//   - ReadBack: die geänderten Felder, in der Transaktion zurückgelesen (PDH-Form).
//   - Errors: Fehler der Probewerte (dann wurde nichts gesendet); Error: der
//     Grund des Scheiterns, bei abgelehntem Befehl der Text der Datenbank.
type WriteProbe struct {
	OK, RolledBack   bool
	Triggers         []string
	Warnings         []string
	Statement        string
	Params           []string
	CommentStatement string
	CommentParams    []string
	Values           map[string]string
	Truncated        []string
	ReadBack         map[string]string
	Errors           []RunError
	Error            string
}

// ProbeWrite fährt Stufe 2 für die Meldung key (aus ProbeRows): in EINER
// Transaktion auf db den Änderungsbefehl (genau eine Zeile), Zurücklesen der
// Zeile und – mit Kommentartabelle – den Probekommentar ProbeCommentText;
// danach wird IMMER zurückgenommen, nie bestätigt. Anschließend wird außerhalb
// der Transaktion geprüft, dass alles wie vorher ist. allowWrite ist "Schreiben
// erlaubt" der Verbindung (aus: nichts wird gesendet). timeout ist die
// Zeitgrenze je Befehl; der ganze Versuch dauert höchstens probeTotalMax. Das
// Ergebnis hält der Aufrufer mit Store.MarkTest(…, TestStageWrite, res.OK) fest.
// Kein Absturz verlässt ProbeWrite.
func ProbeWrite(ctx context.Context, db *sql.DB, d Dialect, p *Profile, maps []FieldMap, key string, sample map[string]string, allowWrite bool, timeout time.Duration) WriteProbe {
	if db == nil || d == nil {
		return WriteProbe{Error: "Verbindung zum Fremdsystem fehlt"}
	}
	return probeWrite(ctx, newSQLProbeConn(db, d, timeout), d, p, maps, key, sample, allowWrite, probeEnv{})
}

func probeWrite(ctx context.Context, c probeConn, d Dialect, p *Profile, maps []FieldMap, key string, sample map[string]string, allowWrite bool, env probeEnv) (res WriteProbe) {
	defer func() {
		if rec := recover(); rec != nil {
			res.OK = false
			res.Error = fmt.Sprintf("Interner Fehler im Schreibversuch: %v", rec)
		}
	}()
	switch {
	case p == nil || c == nil || d == nil:
		res.Error = "Profil oder Verbindung fehlt"
		return res
	case !NeedsWriteTest(maps, p):
		res.Error = ProbeNothingToWrite
		return res
	case !allowWrite:
		res.Error = ErrWriteNotAllowed.Error()
		return res
	}
	total := env.total
	if total <= 0 || total > probeTotalMax {
		total = probeTotalMax
	}
	tctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()

	trg, err := probeTriggers(tctx, c, p)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Triggers = trg
	pl, err := planProbe(tctx, c, d, p, maps, key, sample, env)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Values, res.Truncated = pl.vals, pl.truncated
	res.Statement, res.Params = renderStmt(pl.update)
	res.CommentStatement, res.CommentParams = renderStmt(pl.comment)
	if len(pl.errs) > 0 {
		res.Errors = pl.errs
		res.Error = joinErrs(pl.errs)
		return res
	}
	if pl.update == nil && pl.comment == nil {
		res.Error = ProbeNothingToWrite
		return res
	}
	// Kommentare zur Meldung vorher zählen (außerhalb der Transaktion).
	ct := p.CommentTable
	countBefore, counted := 0, false
	if pl.comment != nil {
		if countBefore, counted, err = countChildRows(tctx, c, d, pl.child, ct.FKColumn, pl.kv[0]); err != nil {
			res.Error = err.Error()
			return res
		}
	}

	attempted := false
	attemptErr := runRolledBack(tctx, c, env, func(tx probeTx) error {
		if pl.update != nil {
			attempted = true
			n, err := tx.Exec(tctx, *pl.update)
			if err != nil {
				return fmt.Errorf("Fremddatenbank schreiben: %w", err)
			}
			if n != 1 {
				return fmt.Errorf("Die Änderung traf %d Zeilen statt genau einer", n)
			}
			keys := make([]string, 0, len(pl.vals))
			for k := range pl.vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			rm, cols := outReadMaps(pl.out, keys)
			sel, err := d.SelectOne(pl.t, cols, p.KeyColumns, pl.kv)
			if err != nil {
				return err
			}
			rows, err := tx.Query(tctx, sel)
			if err != nil {
				return fmt.Errorf("Fremddatenbank lesen: %w", err)
			}
			if len(rows) == 0 {
				return errors.New(ProbeRowNotFound)
			}
			res.ReadBack, _ = foreignPDH(rm, pl.fields, rows[0], wallColumns(pl.t, d, rm, pl.fields), pl.fLoc, nil)
		}
		if pl.comment != nil {
			attempted = true
			n, err := tx.Exec(tctx, *pl.comment)
			if err != nil {
				return fmt.Errorf("Fremddatenbank schreiben (Kommentar): %w", err)
			}
			if n != 1 {
				return errors.New("Kommentar wurde nicht eingefügt")
			}
		}
		return nil
	})
	if len(res.Triggers) > 0 {
		res.Warnings = append(res.Warnings, WarnTriggers)
	}
	if attempted {
		res.Warnings = append(res.Warnings, WarnCounters)
	}

	// Prüfen außerhalb der Transaktion, auch wenn die Gesamtzeit abgelaufen
	// ist (jeder Befehl hat seine eigene Zeitgrenze).
	vctx := context.WithoutCancel(ctx)
	problem := ""
	after, err := readRow(vctx, c, d, pl.t, p.KeyColumns, pl.kv)
	switch {
	case err != nil:
		problem = err.Error()
	case after == nil:
		problem = "Meldung nach dem Versuch nicht mehr lesbar"
	default:
		if col, same := sameRow(pl.before, after); !same {
			problem = fmt.Sprintf("Spalte ‚%s‘ ist nicht wie vorher", col)
		}
	}
	if problem == "" && pl.comment != nil && counted {
		n, ok, err := countChildRows(vctx, c, d, pl.child, ct.FKColumn, pl.kv[0])
		switch {
		case err != nil:
			problem = err.Error()
		case ok && n != countBefore:
			problem = fmt.Sprintf("Kommentare zur Meldung: vorher %d, nachher %d", countBefore, n)
		case !ok:
			counted = false
		}
	}
	if pl.comment != nil && !counted {
		res.Warnings = append(res.Warnings, WarnCommentsNotCounted)
	}
	res.RolledBack = problem == ""
	res.OK = attemptErr == nil && res.RolledBack
	switch {
	case !res.RolledBack && attemptErr != nil:
		res.Error = fmt.Sprintf("%s (%s; %s)", RollbackUnconfirmed, problem, attemptErr.Error())
	case !res.RolledBack:
		res.Error = fmt.Sprintf("%s (%s)", RollbackUnconfirmed, problem)
	case attemptErr != nil:
		res.Error = attemptErr.Error()
	}
	return res
}

// runRolledBack beginnt eine Transaktion, führt fn darin aus und nimmt sie
// IMMER zurück – per defer, gleich nach dem Beginn eingetragen, bei Erfolg,
// Fehler und Panik (die Panik wird zum Fehler). Ein Commit gibt es nicht
// (probeTx kennt keins).
func runRolledBack(ctx context.Context, c probeConn, env probeEnv, fn func(tx probeTx) error) (err error) {
	tx, err := c.Begin(ctx)
	if err != nil {
		return fmt.Errorf("Transaktion beginnen: %w", err)
	}
	defer func() {
		// Nach Ablauf der Gesamtzeit hat database/sql schon zurückgenommen
		// (sql.ErrTxDone); ob alles wie vorher ist, prüft der Aufrufer.
		_ = tx.Rollback()
		if rec := recover(); rec != nil {
			err = fmt.Errorf("Interner Fehler im Schreibversuch: %v", rec)
		}
	}()
	if env.hook != nil {
		env.hook("begun")
	}
	return fn(tx)
}

// sameRow vergleicht zwei Lesungen derselben Zeile spaltenweise (als Text).
func sameRow(before, after map[string]any) (string, bool) {
	cols := make([]string, 0, len(before))
	for c := range before {
		cols = append(cols, c)
	}
	for c := range after {
		if _, ok := before[c]; !ok {
			cols = append(cols, c)
		}
	}
	sort.Strings(cols)
	for _, c := range cols {
		if foreignText(before[c]) != foreignText(after[c]) {
			return c, false
		}
	}
	return "", true
}

// uniqueKeyOf: Primärschlüssel oder erster eindeutiger Schlüssel; nil = keiner.
func uniqueKeyOf(t *TableInfo) []string {
	var pk []string
	for _, c := range t.Columns {
		if c.IsKey {
			pk = append(pk, c.Name)
		}
	}
	if KeyIsUnique(t, pk) {
		return pk
	}
	for _, u := range t.UniqueKeys {
		if KeyIsUnique(t, u) {
			return u
		}
	}
	return nil
}

// countChildRows zählt die Zeilen der Kommentartabelle mit Fremdschlüssel fk
// – seitenweise über SelectRows (der Dialekt kennt kein COUNT; Werte nie im
// Filter). checked = false: nicht gezählt (kein eindeutiger Schlüssel oder
// mehr als probeChildScanLimit Zeilen).
func countChildRows(ctx context.Context, r foreignReader, d Dialect, child *TableInfo, fkCol, fk string) (n int, checked bool, err error) {
	keys := uniqueKeyOf(child)
	if keys == nil {
		return 0, false, nil
	}
	cursor := ""
	read := 0
	for {
		st, err := d.SelectRows(child, []string{fkCol}, keys, "", changeKindNone, cursor, "", 0, probeChildPage)
		if err != nil {
			return 0, false, err
		}
		rows, err := r.Query(ctx, st)
		if err != nil {
			return 0, false, fmt.Errorf("Kommentartabelle lesen: %w", err)
		}
		for _, row := range rows {
			if foreignText(row[fkCol]) == fk {
				n++
			}
		}
		read += len(rows)
		if len(rows) < probeChildPage {
			return n, true, nil
		}
		if read >= probeChildScanLimit {
			return 0, false, nil
		}
		if cursor, err = d.NextCursor(child, keys, "", changeKindNone, rows[len(rows)-1]); err != nil {
			return 0, false, err
		}
	}
}
