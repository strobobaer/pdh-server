package extsync

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attrappe der Fremddatenbank: eine Tabelle im Speicher. fakeDialect erzeugt
// keine T-SQL, sondern einen kleinen Befehl ("page"), den fakeForeign versteht:
// Seite nach dem Cursor in Schlüssel- bzw. Änderungsfolge. KeyValues und
// NextCursor passen dazu wie beim echten Dialekt.

type fakeDialect struct{}

// fakePage ist der Befehl, den SelectRows in Stmt.Args[0] ablegt.
type fakePage struct {
	keyCols    []string
	changeCol  string
	changeKind string
	cursor     string
	limit      int
}

func (fakeDialect) Tables(context.Context, *sql.DB) ([]TableInfo, error) {
	return nil, errors.New("attrappe: Tables nicht vorgesehen")
}

func (fakeDialect) Describe(context.Context, *sql.DB, string, string) (*TableInfo, error) {
	return nil, errors.New("attrappe: Describe über fakeForeign")
}

func (fakeDialect) SelectRows(t *TableInfo, cols, keyCols []string, changeCol, changeKind, cursor, filter string, windowDays, limit int) (Stmt, error) {
	if t == nil {
		return Stmt{}, errors.New("Tabelle fehlt")
	}
	if limit < 1 {
		return Stmt{}, errors.New("Zeilenbegrenzung muss zwischen 1 und 10000 liegen")
	}
	if !KeyIsUnique(t, keyCols) {
		return Stmt{}, errors.New("Schlüssel ist nicht eindeutig (Primärschlüssel oder eindeutiger Index nötig)")
	}
	for _, c := range cols {
		if _, ok := fakeCol(t, c); !ok {
			return Stmt{}, fmt.Errorf("Spalte %q gibt es nicht", c)
		}
	}
	switch changeKind {
	case changeKindNone, "":
	case changeKindRowVersion:
		c, ok := fakeCol(t, changeCol)
		if !ok || !c.IsRowVersion {
			return Stmt{}, fmt.Errorf("Spalte %q ist keine rowversion-Spalte", changeCol)
		}
	case changeKindTimestamp:
		c, ok := fakeCol(t, changeCol)
		if !ok || !strings.HasPrefix(strings.ToLower(c.Type), "date") {
			return Stmt{}, fmt.Errorf("Änderungsspalte %q ist kein Zeitstempel", changeCol)
		}
	default:
		return Stmt{}, fmt.Errorf("unbekannte Art der Änderungserkennung %q", changeKind)
	}
	return Stmt{SQL: "page", Args: []any{fakePage{keyCols: keyCols, changeCol: changeCol,
		changeKind: changeKind, cursor: cursor, limit: limit}}}, nil
}

type fakeTSCursor struct {
	T string   `json:"t"`
	K []string `json:"k"`
}

func (d fakeDialect) NextCursor(t *TableInfo, keyCols []string, changeCol, changeKind string, lastRow map[string]any) (string, error) {
	kv, err := d.KeyValues(t, keyCols, lastRow)
	if err != nil {
		return "", err
	}
	switch changeKind {
	case changeKindNone, "":
		b, _ := json.Marshal(kv)
		return string(b), nil
	case changeKindRowVersion:
		b, ok := lastRow[changeCol].([]byte)
		if !ok || len(b) != 8 {
			return "", errors.New("Zeile enthält keine gültige rowversion")
		}
		return hex.EncodeToString(b), nil
	case changeKindTimestamp:
		tm, ok := lastRow[changeCol].(time.Time)
		if !ok {
			return "", errors.New("Zeile enthält keinen gültigen Zeitstempel")
		}
		b, _ := json.Marshal(fakeTSCursor{T: tm.UTC().Format(time.RFC3339Nano), K: kv})
		return string(b), nil
	}
	return "", fmt.Errorf("unbekannte Art der Änderungserkennung %q", changeKind)
}

func (fakeDialect) KeyValues(t *TableInfo, keyCols []string, row map[string]any) ([]string, error) {
	out := make([]string, 0, len(keyCols))
	for _, k := range keyCols {
		c, ok := fakeCol(t, k)
		if !ok {
			return nil, fmt.Errorf("Schlüsselspalte %q gibt es nicht", k)
		}
		var s string
		switch x := row[k].(type) {
		case int64:
			s = strconv.FormatInt(x, 10)
		case string:
			s = x
		case nil:
			return nil, fmt.Errorf("Schlüsselwert %q fehlt in der Zeile", k)
		default:
			return nil, fmt.Errorf("Schlüsselwert %q hat den Typ %T", k, x)
		}
		if strings.EqualFold(c.Type, "uniqueidentifier") {
			s = strings.ToLower(s)
		}
		out = append(out, s)
	}
	return out, nil
}

// fakeOne ist der Befehl von SelectOne.
type fakeOne struct {
	keyCols, keyVals []string
}

func (fakeDialect) SelectOne(t *TableInfo, cols, keyCols, keyVals []string) (Stmt, error) {
	if !KeyIsUnique(t, keyCols) {
		return Stmt{}, errors.New("Schlüssel ist nicht eindeutig (Primärschlüssel oder eindeutiger Index nötig)")
	}
	if len(keyVals) != len(keyCols) {
		return Stmt{}, errors.New("zu jeder Schlüsselspalte wird genau ein Wert gebraucht")
	}
	for _, c := range cols {
		if _, ok := fakeCol(t, c); !ok {
			return Stmt{}, fmt.Errorf("Spalte %q gibt es nicht", c)
		}
	}
	return Stmt{SQL: "one", Args: []any{fakeOne{keyCols: keyCols, keyVals: keyVals}}}, nil
}

// fakeUpdate ist der Befehl von UpdateRow, fakeInsert der von InsertChild. Wie
// beim echten Dialekt prüfen beide Schlüssel, Spalten und Zieltabelle.
type fakeUpdate struct {
	table            string
	keyCols, keyVals []string
	set              map[string]any
}

type fakeInsert struct {
	table  string
	values map[string]any
}

func fakeTableName(t *TableInfo) string { return strings.ToLower(t.Schema + "." + t.Name) }

func (fakeDialect) UpdateRow(t *TableInfo, keyCols, keyVals []string, set map[string]any) (Stmt, error) {
	if t == nil {
		return Stmt{}, errors.New("Tabelle fehlt")
	}
	if len(set) == 0 {
		return Stmt{}, errors.New("nichts zu ändern")
	}
	if !KeyIsUnique(t, keyCols) {
		return Stmt{}, errors.New("Schlüssel ist nicht eindeutig (Primärschlüssel oder eindeutiger Index nötig)")
	}
	if len(keyVals) != len(keyCols) {
		return Stmt{}, errors.New("zu jeder Schlüsselspalte wird genau ein Wert gebraucht")
	}
	cp := map[string]any{}
	for n, v := range set {
		c, ok := fakeCol(t, n)
		if !ok {
			return Stmt{}, fmt.Errorf("Spalte %q gibt es nicht", n)
		}
		if c.IsKey || c.IsRowVersion || c.IsIdentity || c.IsComputed || containsStr(keyCols, n) {
			return Stmt{}, fmt.Errorf("Spalte %q darf nicht geändert werden", n)
		}
		cp[n] = v
	}
	return Stmt{SQL: "update", Args: []any{fakeUpdate{table: fakeTableName(t), keyCols: append([]string{}, keyCols...),
		keyVals: append([]string{}, keyVals...), set: cp}}}, nil
}

func (fakeDialect) InsertChild(source, child *TableInfo, values map[string]any) (Stmt, error) {
	if source == nil || child == nil {
		return Stmt{}, errors.New("Meldungstabelle und Nebentabelle müssen bekannt sein")
	}
	if fakeTableName(source) == fakeTableName(child) {
		return Stmt{}, ErrInsertIntoSource
	}
	if len(values) == 0 {
		return Stmt{}, errors.New("nichts einzufügen")
	}
	cp := map[string]any{}
	for n, v := range values {
		c, ok := fakeCol(child, n)
		if !ok {
			return Stmt{}, fmt.Errorf("Spalte %q gibt es nicht", n)
		}
		if c.IsRowVersion || c.IsIdentity || c.IsComputed {
			return Stmt{}, fmt.Errorf("Spalte %q kann nicht befüllt werden", n)
		}
		cp[n] = v
	}
	return Stmt{SQL: "insert", Args: []any{fakeInsert{table: fakeTableName(child), values: cp}}}, nil
}

// recDialect merkt sich jeden Befehl, den UpdateRow und InsertChild des
// umhüllten Dialekts erzeugt haben. Damit lässt sich prüfen, dass Exec nur
// solche Befehle bekommt.
type recDialect struct {
	Dialect
	mu       sync.Mutex
	produced []Stmt
}

func (r *recDialect) UpdateRow(t *TableInfo, keyCols, keyVals []string, set map[string]any) (Stmt, error) {
	st, err := r.Dialect.UpdateRow(t, keyCols, keyVals, set)
	if err == nil {
		r.mu.Lock()
		r.produced = append(r.produced, st)
		r.mu.Unlock()
	}
	return st, err
}

func (r *recDialect) InsertChild(source, child *TableInfo, values map[string]any) (Stmt, error) {
	st, err := r.Dialect.InsertChild(source, child, values)
	if err == nil {
		r.mu.Lock()
		r.produced = append(r.produced, st)
		r.mu.Unlock()
	}
	return st, err
}

// wasProduced: st ist genau ein von UpdateRow/InsertChild erzeugter Befehl.
func (r *recDialect) wasProduced(st Stmt) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.produced {
		if p.SQL == st.SQL && reflect.DeepEqual(p.Args, st.Args) {
			return true
		}
	}
	return false
}

func (fakeDialect) Quote(name string) string { return "[" + name + "]" }

// TruncateText kürzt wie der echte Dialekt (Maß der Datenbankart).
func (fakeDialect) TruncateText(c Column, s string) string {
	return (&mssqlDialect{}).TruncateText(c, s)
}

func (fakeDialect) TimeColumn(c Column) (isTime, dateOnly bool) {
	if c.IsRowVersion {
		return false, false
	}
	switch strings.ToLower(c.Type) {
	case "datetime", "datetime2", "smalldatetime":
		return true, false
	case "date":
		return true, true
	}
	return false, false
}

func fakeCol(t *TableInfo, name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

// fakeForeign ist die Tabelle im Speicher.
type fakeForeign struct {
	mu        sync.Mutex
	info      *TableInfo
	rows      []map[string]any
	rv        uint64
	queries   int
	execs     int
	ones      int           // Abfragen über SelectOne
	failQuery map[int]error // Nummer der Abfrage (ab 1) -> Fehler
	// gate hält die nächste Abfrage an: entered meldet das Warten, Schließen von gate gibt frei.
	gate    chan struct{}
	entered chan struct{}
	// Schreiben (nur die Befehle von fakeDialect.UpdateRow/InsertChild):
	// children sind Nebentabellen samt Zeilen, execLog jeder Exec-Aufruf in
	// Reihenfolge (auch abgelehnte). failExec liefert einen Fehler statt
	// auszuführen; beforeExec läuft vor dem Ausführen ohne Sperre.
	children    map[string]*fakeChild
	execLog     []Stmt
	failExec    func(st Stmt) error
	beforeExec  func(st Stmt)
	execCtxErrs []error // ctx.Err() je Exec, nach beforeExec
}

type fakeChild struct {
	info *TableInfo
	rows []map[string]any
}

func newFakeForeign(info *TableInfo) *fakeForeign {
	return &fakeForeign{info: info, failQuery: map[int]error{}, children: map[string]*fakeChild{}}
}

// addChild legt eine Nebentabelle an.
func (f *fakeForeign) addChild(info *TableInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.children[fakeTableName(info)] = &fakeChild{info: info}
}

// childRows liefert eine Kopie der Zeilen einer Nebentabelle.
func (f *fakeForeign) childRows(schema, table string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.children[strings.ToLower(schema+"."+table)]
	if c == nil {
		return nil
	}
	return append([]map[string]any{}, c.rows...)
}

// execs liefert eine Kopie des Exec-Protokolls.
func (f *fakeForeign) execStmts() []Stmt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Stmt{}, f.execLog...)
}

// get liefert eine Kopie der Zeile mit dem (ersten) Schlüsselwert key oder nil.
func (f *fakeForeign) get(key any) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	k0 := f.keyCols()[0]
	for _, r := range f.rows {
		if fmt.Sprint(r[k0]) == fmt.Sprint(key) {
			cp := map[string]any{}
			for k, v := range r {
				cp[k] = v
			}
			return cp
		}
	}
	return nil
}

func (f *fakeForeign) rvCol() string {
	for _, c := range f.info.Columns {
		if c.IsRowVersion {
			return c.Name
		}
	}
	return ""
}

func (f *fakeForeign) keyCols() []string {
	var ks []string
	for _, c := range f.info.Columns {
		if c.IsKey {
			ks = append(ks, c.Name)
		}
	}
	return ks
}

func (f *fakeForeign) sameKey(a, b map[string]any) bool {
	for _, k := range f.keyCols() {
		if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
			return false
		}
	}
	return true
}

// put legt eine Zeile an oder ersetzt sie (gleicher Schlüssel); die rowversion
// steigt wie im echten SQL Server bei jeder Änderung.
func (f *fakeForeign) put(row map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[string]any{}
	for k, v := range row {
		cp[k] = v
	}
	if rc := f.rvCol(); rc != "" {
		f.rv++
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, f.rv)
		cp[rc] = b
	}
	for i, r := range f.rows {
		if f.sameKey(r, cp) {
			f.rows[i] = cp
			return
		}
	}
	f.rows = append(f.rows, cp)
}

// set ändert eine Spalte der Zeile mit dem (ersten) Schlüsselwert key.
func (f *fakeForeign) set(key any, col string, val any) {
	f.mu.Lock()
	var row map[string]any
	k0 := f.keyCols()[0]
	for _, r := range f.rows {
		if fmt.Sprint(r[k0]) == fmt.Sprint(key) {
			row = map[string]any{}
			for k, v := range r {
				row[k] = v
			}
		}
	}
	f.mu.Unlock()
	if row == nil {
		panic(fmt.Sprintf("attrappe: Zeile %v fehlt", key))
	}
	row[col] = val
	f.put(row)
}

// del löscht die Zeile mit dem (ersten) Schlüsselwert key.
func (f *fakeForeign) del(key any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k0 := f.keyCols()[0]
	out := f.rows[:0]
	for _, r := range f.rows {
		if fmt.Sprint(r[k0]) != fmt.Sprint(key) {
			out = append(out, r)
		}
	}
	f.rows = out
}

func (f *fakeForeign) oneCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ones
}

func (f *fakeForeign) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries
}

func (f *fakeForeign) Describe(_ context.Context, schema, table string) (*TableInfo, error) {
	if strings.EqualFold(f.info.Schema, schema) && strings.EqualFold(f.info.Name, table) {
		return f.info, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.children[strings.ToLower(schema+"."+table)]; c != nil {
		return c.info, nil
	}
	return nil, fmt.Errorf("tabelle %s.%s: %w", schema, table, ErrNotFound)
}

// Exec führt nur die Befehle "update" (eine Zeile der Meldungstabelle über
// ihren Schlüssel, rowversion steigt) und "insert" (Nebentabelle) aus. Alles
// andere wird abgelehnt – aber protokolliert, damit Tests es sehen.
func (f *fakeForeign) Exec(ctx context.Context, st Stmt) (int64, error) {
	f.mu.Lock()
	f.execs++
	f.execLog = append(f.execLog, st)
	before, fail := f.beforeExec, f.failExec
	f.mu.Unlock()
	if before != nil {
		before(st)
	}
	f.mu.Lock()
	f.execCtxErrs = append(f.execCtxErrs, ctx.Err()) // Zustand des Kontexts beim Ausführen
	f.mu.Unlock()
	if fail != nil {
		if err := fail(st); err != nil {
			return 0, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case st.SQL == "update" && len(st.Args) == 1:
		u, ok := st.Args[0].(fakeUpdate)
		if !ok || u.table != fakeTableName(f.info) {
			return 0, errors.New("attrappe: update nur in der Meldungstabelle")
		}
		for i, r := range f.rows {
			kv, err := fakeDialect{}.KeyValues(f.info, u.keyCols, r)
			if err != nil {
				return 0, err
			}
			if !sameKeys(kv, u.keyVals) {
				continue
			}
			cp := map[string]any{}
			for k, v := range r {
				cp[k] = v
			}
			for k, v := range u.set {
				cp[k] = v
			}
			if rc := f.rvCol(); rc != "" {
				f.rv++
				b := make([]byte, 8)
				binary.BigEndian.PutUint64(b, f.rv)
				cp[rc] = b
			}
			f.rows[i] = cp
			return 1, nil
		}
		return 0, nil
	case st.SQL == "insert" && len(st.Args) == 1:
		in, ok := st.Args[0].(fakeInsert)
		if !ok || in.table == fakeTableName(f.info) {
			return 0, errors.New("attrappe: insert nie in die Meldungstabelle")
		}
		c := f.children[in.table]
		if c == nil {
			return 0, fmt.Errorf("attrappe: Nebentabelle %s fehlt", in.table)
		}
		cp := map[string]any{}
		for k, v := range in.values {
			cp[k] = v
		}
		c.rows = append(c.rows, cp)
		return 1, nil
	}
	return 0, errors.New("attrappe: unbekannter Schreibbefehl")
}

// sameKeys: gleiche Schlüsseltexte.
func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cmpKey vergleicht Schlüsseltexte; Ganzzahlen nach Zahlenwert.
func cmpKey(a, b []string) int {
	for i := range a {
		x, ex := strconv.ParseInt(a[i], 10, 64)
		y, ey := strconv.ParseInt(b[i], 10, 64)
		if ex == nil && ey == nil {
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}
	return 0
}

func (f *fakeForeign) Query(_ context.Context, st Stmt) ([]map[string]any, error) {
	f.mu.Lock()
	g := f.gate
	f.gate = nil
	f.mu.Unlock()
	if g != nil {
		f.entered <- struct{}{}
		<-g
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if err := f.failQuery[f.queries]; err != nil {
		return nil, err
	}
	if st.SQL == "one" && len(st.Args) == 1 {
		f.ones++
		one := st.Args[0].(fakeOne)
		for _, r := range f.rows {
			kv, err := fakeDialect{}.KeyValues(f.info, one.keyCols, r)
			if err != nil {
				return nil, err
			}
			if sameKeys(kv, one.keyVals) {
				cp := map[string]any{}
				for k, v := range r {
					cp[k] = v
				}
				return []map[string]any{cp}, nil
			}
		}
		return nil, nil
	}
	if st.SQL != "page" || len(st.Args) != 1 {
		return nil, errors.New("attrappe: unbekannter Befehl")
	}
	pg := st.Args[0].(fakePage)
	d := fakeDialect{}
	type item struct {
		row map[string]any
		kv  []string
	}
	var items []item
	for _, r := range f.rows {
		kv, err := d.KeyValues(f.info, pg.keyCols, r)
		if err != nil {
			return nil, err
		}
		cp := map[string]any{}
		for k, v := range r {
			cp[k] = v
		}
		items = append(items, item{cp, kv})
	}
	less := func(a, b item) bool { return cmpKey(a.kv, b.kv) < 0 }
	after := func(it item) bool { return true }
	switch pg.changeKind {
	case changeKindNone, "":
		if pg.cursor != "" {
			var cur []string
			if err := json.Unmarshal([]byte(pg.cursor), &cur); err != nil {
				return nil, err
			}
			after = func(it item) bool { return cmpKey(it.kv, cur) > 0 }
		}
	case changeKindRowVersion:
		rv := func(it item) string { return hex.EncodeToString(it.row[pg.changeCol].([]byte)) }
		less = func(a, b item) bool { return rv(a) < rv(b) }
		if pg.cursor != "" {
			after = func(it item) bool { return rv(it) > pg.cursor }
		}
	case changeKindTimestamp:
		ts := func(it item) time.Time { return it.row[pg.changeCol].(time.Time) }
		less = func(a, b item) bool {
			if !ts(a).Equal(ts(b)) {
				return ts(a).Before(ts(b))
			}
			return cmpKey(a.kv, b.kv) < 0
		}
		if pg.cursor != "" {
			var cur fakeTSCursor
			if err := json.Unmarshal([]byte(pg.cursor), &cur); err != nil {
				return nil, err
			}
			ct, _ := time.Parse(time.RFC3339Nano, cur.T)
			after = func(it item) bool {
				return ts(it).After(ct) || (ts(it).Equal(ct) && cmpKey(it.kv, cur.K) > 0)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) })
	var out []map[string]any
	for _, it := range items {
		if !after(it) {
			continue
		}
		out = append(out, it.row)
		if len(out) == pg.limit {
			break
		}
	}
	return out, nil
}

// fakeMeldungenInfo ist der Aufbau der Standard-Attrappe (wie dbo.Meldungen im Labor).
func fakeMeldungenInfo(withRV bool) *TableInfo {
	t := &TableInfo{Schema: "dbo", Name: "Meldungen", Columns: []Column{
		{Name: "Nr", Type: "int", IsKey: true},
		{Name: "Titel", Type: "nvarchar"},
		{Name: "Beschreibung", Type: "nvarchar", Nullable: true},
		{Name: "Prio", Type: "nvarchar", Nullable: true},
		{Name: "Zustand", Type: "nvarchar"},
		{Name: "Faellig", Type: "datetime2", Nullable: true},
	}}
	if withRV {
		t.Columns = append(t.Columns, Column{Name: "RV", Type: "timestamp", IsRowVersion: true})
	}
	return t
}
