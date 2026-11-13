package extsync

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// mssqlDialect erzeugt T-SQL für Microsoft SQL Server (Treiber go-mssqldb,
// Parameter @p1, @p2, …).
type mssqlDialect struct{}

// Quote setzt einen Bezeichner in eckige Klammern; ] wird verdoppelt.
func (*mssqlDialect) Quote(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

func (d *mssqlDialect) qualified(t *TableInfo) string {
	return d.Quote(t.Schema) + "." + d.Quote(t.Name)
}

// checkIdent lehnt Namen ab, die es im Katalog nicht geben kann.
func checkIdent(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s fehlt", kind)
	}
	if len([]rune(name)) > 128 {
		return fmt.Errorf("%s ist zu lang", kind)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s enthält ein Steuerzeichen", kind)
		}
	}
	return nil
}

// checkTable prüft die Tabelle selbst (Namen stammen aus dem Katalog).
func checkTable(t *TableInfo) error {
	if t == nil {
		return errors.New("Tabelle fehlt")
	}
	if err := checkIdent("Schemaname", t.Schema); err != nil {
		return err
	}
	return checkIdent("Tabellenname", t.Name)
}

func findColumn(t *TableInfo, name string) (*Column, bool) {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i], true
		}
	}
	return nil, false
}

// requireColumns prüft, dass alle Namen im Aufbau stehen; gibt sie ohne Doppelte zurück.
func requireColumns(t *TableInfo, names []string, what string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if err := checkIdent("Spaltenname", n); err != nil {
			return nil, err
		}
		if _, ok := findColumn(t, n); !ok {
			return nil, fmt.Errorf("%s: Spalte %q gibt es in %s.%s nicht", what, n, t.Schema, t.Name)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// prepValue lässt nur die Go-Typen zu, die der Treiber sicher als Parameter
// überträgt, und rechnet Zeiten auf UTC um.
func prepValue(col string, v any) (any, error) {
	switch x := v.(type) {
	case nil, string, int64, float64, bool, []byte:
		return v, nil
	case time.Time:
		return x.UTC(), nil
	}
	return nil, fmt.Errorf("Spalte %q: Werttyp %T wird nicht unterstützt", col, v)
}

// placeholder liefert den Parameter @pN. Zeiten gehen als datetime2(7) in den
// Vergleich bzw. in die Spalte (nicht als datetimeoffset), damit Vergleiche mit
// datetime/datetime2-Spalten ohne Umrechnung der Spalte auskommen und der
// gespeicherte Wert nicht von der Zeitzone des Aufrufers abhängt. Festtext.
func placeholder(n int, v any) string {
	if _, ok := v.(time.Time); ok {
		return "CAST(@p" + strconv.Itoa(n) + " AS datetime2(7))"
	}
	return "@p" + strconv.Itoa(n)
}

// keyPlaceholder liefert den Parameter für einen Schlüsselwert, im Typbereich der
// Schlüsselspalte: Zeichenspalten (char/varchar) vergleichen als varchar, Unicode-
// Spalten als nvarchar. Sonst würde der Server die Spalte nach nvarchar umwandeln:
// andere Reihenfolge als ORDER BY (Schlüsselfolge verliert Zeilen) und kein
// Index-Zugriff. Die Kollation der Spalte bleibt maßgeblich (Spalte schlägt den
// Parameter mit Standardkollation). Ganzzahlen und GUID: der Parameter wird in den
// Typ der Spalte umgewandelt, Vergleich und Sortierung laufen im selben Bereich.
// Nur Festtexte per switch, nie aus dem Katalogtext zusammengesetzt.
func keyPlaceholder(n int, c *Column) string {
	p := "@p" + strconv.Itoa(n)
	switch strings.ToLower(c.Type) {
	case "char", "varchar":
		return "CAST(" + p + " AS varchar(8000))"
	case "nchar", "nvarchar":
		return "CAST(" + p + " AS nvarchar(4000))"
	}
	return p
}

// keyTypeOK: erlaubte Typen für Schlüsselspalten.
func keyTypeOK(c *Column) bool {
	switch strings.ToLower(c.Type) {
	case "tinyint", "smallint", "int", "bigint", "char", "varchar", "nchar", "nvarchar", "uniqueidentifier":
		return true
	}
	return false
}

func isGUID(c *Column) bool { return strings.EqualFold(c.Type, "uniqueidentifier") }

// requireKey prüft Schlüsselspalten: vorhanden, ohne Doppelte, erlaubter Typ.
func requireKey(t *TableInfo, keyCols []string) error {
	keys, err := requireColumns(t, keyCols, "Schlüssel")
	if err != nil {
		return err
	}
	if len(keys) == 0 || len(keys) != len(keyCols) {
		return errors.New("Schlüsselspalten fehlen oder sind doppelt")
	}
	for _, k := range keys {
		c, _ := findColumn(t, k)
		if !keyTypeOK(c) {
			return fmt.Errorf("Schlüsselspalte vom Typ %s wird nicht unterstützt", c.Type)
		}
	}
	return nil
}

// requireUniqueKey: zusätzlich muss der Schlüssel eindeutig sein.
func requireUniqueKey(t *TableInfo, keyCols []string) error {
	if err := requireKey(t, keyCols); err != nil {
		return err
	}
	if !KeyIsUnique(t, keyCols) {
		return errors.New("Schlüssel ist nicht eindeutig: die Spalten bilden weder den Primärschlüssel noch einen eindeutigen Index")
	}
	return nil
}

// ---------- Aufbau lesen ----------

// Tables liefert die Benutzertabellen (Schema, Name).
func (*mssqlDialect) Tables(ctx context.Context, db *sql.DB) ([]TableInfo, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT s.name, t.name FROM sys.tables t JOIN sys.schemas s ON s.schema_id = t.schema_id
		 WHERE t.is_ms_shipped = 0 ORDER BY s.name, t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TableInfo
	for rows.Next() {
		var ti TableInfo
		if err := rows.Scan(&ti.Schema, &ti.Name); err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

// Describe liest Spalten, Primärschlüssel und Auslöser einer Tabelle.
func (*mssqlDialect) Describe(ctx context.Context, db *sql.DB, schema, table string) (*TableInfo, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT c.name, ty.name, c.is_nullable, c.max_length, c.is_identity, c.is_computed,
		        CASE WHEN EXISTS (
		            SELECT 1 FROM sys.indexes i
		            JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		            WHERE i.object_id = t.object_id AND i.is_primary_key = 1 AND ic.column_id = c.column_id
		        ) THEN 1 ELSE 0 END
		 FROM sys.columns c
		 JOIN sys.tables t ON t.object_id = c.object_id
		 JOIN sys.schemas s ON s.schema_id = t.schema_id
		 JOIN sys.types ty ON ty.user_type_id = c.user_type_id
		 WHERE s.name = @p1 AND t.name = @p2
		 ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ti := &TableInfo{Schema: schema, Name: table}
	for rows.Next() {
		var c Column
		var maxBytes int
		var nullable, ident, computed, key bool
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &maxBytes, &ident, &computed, &key); err != nil {
			return nil, err
		}
		c.Nullable, c.IsIdentity, c.IsComputed, c.IsKey = nullable, ident, computed, key
		c.IsRowVersion = c.Type == "timestamp" || c.Type == "rowversion"
		switch {
		case maxBytes < 0:
			c.MaxLen = 0
		case c.Type == "nvarchar" || c.Type == "nchar":
			c.MaxLen = maxBytes / 2
		case c.Type == "varchar" || c.Type == "char" || c.Type == "varbinary" || c.Type == "binary":
			c.MaxLen = maxBytes
		}
		ti.Columns = append(ti.Columns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ti.Columns) == 0 {
		return nil, fmt.Errorf("Tabelle %s.%s: %w", schema, table, ErrNotFound)
	}
	trows, err := db.QueryContext(ctx,
		`SELECT tr.name FROM sys.triggers tr
		 JOIN sys.tables t ON t.object_id = tr.parent_id
		 JOIN sys.schemas s ON s.schema_id = t.schema_id
		 WHERE s.name = @p1 AND t.name = @p2 ORDER BY tr.name`, schema, table)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var n string
		if err := trows.Scan(&n); err != nil {
			return nil, err
		}
		ti.Triggers = append(ti.Triggers, n)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	// Eindeutige Schlüssel: Primärschlüssel zuerst, dann eindeutige Indizes/Constraints
	// ohne Filter und ohne Spalten, die NULL sein dürfen.
	urows, err := db.QueryContext(ctx,
		`SELECT i.index_id, c.name, c.is_nullable
		 FROM sys.indexes i
		 JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		 JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		 JOIN sys.tables t ON t.object_id = i.object_id
		 JOIN sys.schemas s ON s.schema_id = t.schema_id
		 WHERE s.name = @p1 AND t.name = @p2
		   AND i.is_unique = 1 AND i.has_filter = 0 AND i.is_disabled = 0 AND i.is_hypothetical = 0
		   AND ic.is_included_column = 0 AND ic.key_ordinal > 0
		 ORDER BY i.is_primary_key DESC, i.index_id, ic.key_ordinal`, schema, table)
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	var cur []string
	curID, curNullable := -1, false
	flush := func() {
		if len(cur) > 0 && !curNullable {
			ti.UniqueKeys = append(ti.UniqueKeys, cur)
		}
		cur, curNullable = nil, false
	}
	for urows.Next() {
		var id int
		var name string
		var nullable bool
		if err := urows.Scan(&id, &name, &nullable); err != nil {
			return nil, err
		}
		if id != curID {
			flush()
			curID = id
		}
		cur = append(cur, name)
		curNullable = curNullable || nullable
	}
	if err := urows.Err(); err != nil {
		return nil, err
	}
	flush()
	return ti, nil
}

// ---------- Lesen ----------

// selectList bildet die Spaltenliste: Schlüssel zuerst, dann die übrigen
// sortiert, zuletzt die Änderungsspalte. GUID-Spalten gehen als kleingeschriebener
// Text hinaus, damit der Treiber sie nicht in gemischter Byte-Reihenfolge liefert.
func (d *mssqlDialect) selectList(t *TableInfo, cols, keyCols []string, changeCol string, tsChange bool) (string, error) {
	keys, err := requireColumns(t, keyCols, "Schlüssel")
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("Schlüsselspalten fehlen")
	}
	cs, err := requireColumns(t, cols, "Spalte")
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	var list []string
	add := func(n string) {
		used[n] = true
		c, _ := findColumn(t, n)
		if isGUID(c) {
			list = append(list, "LOWER(CONVERT(nvarchar(36), "+d.Quote(n)+")) AS "+d.Quote(n))
		} else if tsChange && n == changeCol {
			// Den genauen gespeicherten Wert lesen (datetime ist sonst auf Millisekunden gerundet).
			list = append(list, "CONVERT(datetime2(7), "+d.Quote(n)+") AS "+d.Quote(n))
		} else {
			list = append(list, d.Quote(n))
		}
	}
	for _, k := range keys {
		add(k)
	}
	sort.Strings(cs)
	for _, c := range cs {
		if !used[c] && c != changeCol {
			add(c)
		}
	}
	if changeCol != "" && !used[changeCol] {
		add(changeCol)
	}
	return strings.Join(list, ", "), nil
}

// tsCursor ist der Aufbau des Zeitstempel-Cursors (Schlüsselfolge-Cursor).
type tsCursor struct {
	T string   `json:"t"`
	K []string `json:"k"`
}

// keyCursor ist der Cursor ohne Änderungsspalte (Art none): nur die Schlüsselwerte.
type keyCursor struct {
	K []string `json:"k"`
}

// tsCast liefert den festen CAST-Text für den Vergleich einer Zeitstempel-Spalte
// mit dem Cursor-Parameter: immer der EIGENE Typ der Spalte, damit der Vergleich
// genau ist und der Index nutzbar bleibt. Nur diese Festtexte, nie aus dem Katalog.
func tsCast(colType string) (string, bool) {
	switch strings.ToLower(colType) {
	case "datetime":
		return "datetime", true
	case "datetime2":
		return "datetime2(7)", true
	case "smalldatetime":
		return "smalldatetime", true
	case "date":
		return "date", true
	}
	return "", false
}

// TimeColumn: Zeitspalten sind genau die Typen, die auch als Zeitstempel-Spalte
// erlaubt sind (tsCast); date ist reines Datum.
func (*mssqlDialect) TimeColumn(c Column) (isTime, dateOnly bool) {
	if c.IsRowVersion {
		return false, false
	}
	if _, ok := tsCast(c.Type); !ok {
		return false, false
	}
	return true, strings.EqualFold(c.Type, "date")
}

// requireTimestampColumn: Typ der Änderungsspalte im Zeitstempel-Modus.
func requireTimestampColumn(c *Column) (string, error) {
	if c.IsRowVersion {
		return "", fmt.Errorf("Spalte %q ist eine rowversion, kein Zeitstempel", c.Name)
	}
	cast, ok := tsCast(c.Type)
	if !ok {
		return "", fmt.Errorf("Änderungsspalte vom Typ %s wird als Zeitstempel nicht unterstützt", c.Type)
	}
	return cast, nil
}

// NextCursor bildet den Cursor für die letzte Zeile einer Seite.
func (d *mssqlDialect) NextCursor(t *TableInfo, keyCols []string, changeCol, changeKind string, lastRow map[string]any) (string, error) {
	if t == nil {
		return "", errors.New("Tabelle fehlt")
	}
	switch changeKind {
	case changeKindNone, "":
		kv, err := d.KeyValues(t, keyCols, lastRow)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(keyCursor{K: kv})
		return string(b), err
	case changeKindRowVersion:
		c, ok := findColumn(t, changeCol)
		if !ok || !c.IsRowVersion {
			return "", fmt.Errorf("Änderungsspalte %q ist keine rowversion-Spalte", changeCol)
		}
		b, ok := lastRow[changeCol].([]byte)
		if !ok || len(b) != 8 {
			return "", errors.New("Zeile enthält keine gültige rowversion")
		}
		return hex.EncodeToString(b), nil
	case changeKindTimestamp:
		c, ok := findColumn(t, changeCol)
		if !ok {
			return "", fmt.Errorf("Änderungsspalte %q gibt es nicht", changeCol)
		}
		if _, err := requireTimestampColumn(c); err != nil {
			return "", err
		}
		tm, ok := lastRow[changeCol].(time.Time)
		if !ok {
			return "", errors.New("Zeile enthält keinen gültigen Zeitstempel")
		}
		kv, err := d.KeyValues(t, keyCols, lastRow)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(tsCursor{T: tm.UTC().Format(time.RFC3339Nano), K: kv})
		return string(b), err
	}
	return "", fmt.Errorf("unbekannte Art der Änderungserkennung %q", changeKind)
}

// KeyValues macht aus einer gelesenen Zeile die Schlüsselwerte als Text.
func (d *mssqlDialect) KeyValues(t *TableInfo, keyCols []string, row map[string]any) ([]string, error) {
	if t == nil {
		return nil, errors.New("Tabelle fehlt")
	}
	if err := requireKey(t, keyCols); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keyCols))
	for _, k := range keyCols {
		c, _ := findColumn(t, k)
		v, ok := row[k]
		if !ok || v == nil {
			return nil, fmt.Errorf("Schlüsselwert %q fehlt in der Zeile", k)
		}
		var s string
		switch x := v.(type) {
		case int64:
			s = strconv.FormatInt(x, 10)
		case int:
			s = strconv.Itoa(x)
		case int32:
			s = strconv.FormatInt(int64(x), 10)
		case int16:
			s = strconv.FormatInt(int64(x), 10)
		case uint8:
			s = strconv.FormatInt(int64(x), 10)
		case string:
			s = x
		default:
			return nil, fmt.Errorf("Schlüsselwert %q hat den Typ %T", k, v)
		}
		if isGUID(c) {
			s = strings.ToLower(s)
		}
		out = append(out, s)
	}
	return out, nil
}

// keysetCondition: Schlüsselfolge über alle Schlüsselspalten. first ist die
// Nummer des ersten Schlüsselparameters.
func (d *mssqlDialect) keysetCondition(t *TableInfo, keys []string, first int) string {
	var rec func(i int) string
	rec = func(i int) string {
		q := d.Quote(keys[i])
		c, _ := findColumn(t, keys[i])
		p := keyPlaceholder(first+i, c)
		if i == len(keys)-1 {
			return q + " > " + p
		}
		return "(" + q + " > " + p + " OR (" + q + " = " + p + " AND " + rec(i+1) + "))"
	}
	return rec(0)
}

// SelectRows liest Zeilen ab dem Stand des Cursors.
func (d *mssqlDialect) SelectRows(t *TableInfo, cols, keyCols []string, changeCol, changeKind, cursor, filter string, windowDays, limit int) (Stmt, error) {
	if err := checkTable(t); err != nil {
		return Stmt{}, err
	}
	if limit < 1 || limit > 10000 {
		return Stmt{}, errors.New("Zeilenbegrenzung muss zwischen 1 und 10000 liegen")
	}
	if windowDays < 0 || windowDays > 3650 {
		return Stmt{}, errors.New("Zeitfenster muss zwischen 1 und 3650 Tagen liegen")
	}
	if windowDays > 0 && changeKind != changeKindTimestamp {
		return Stmt{}, errors.New("Zeitfenster geht nur mit einer Änderungsspalte der Art Zeitstempel")
	}
	if err := ValidateFilter(filter); err != nil {
		return Stmt{}, err
	}
	// Eindeutig auch beim Lesen: Verknüpfungen und der Schlüsselfolge-Cursor brauchen es.
	if err := requireUniqueKey(t, keyCols); err != nil {
		return Stmt{}, err
	}
	var args []any
	var conds []string
	orderBy := ""
	var ordKeys []string
	for _, k := range keyCols {
		ordKeys = append(ordKeys, "[t]."+d.Quote(k))
	}
	chg := ""
	switch changeKind {
	case changeKindNone, "":
		if cursor != "" {
			var cur keyCursor
			dec := json.NewDecoder(strings.NewReader(cursor))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&cur); err != nil || len(cur.K) != len(keyCols) {
				return Stmt{}, errors.New("Stand der Schlüsselfolge ist ungültig")
			}
			first := len(args) + 1
			for _, kv := range cur.K {
				args = append(args, kv)
			}
			conds = append(conds, d.keysetCondition(t, keyCols, first))
		}
		orderBy = strings.Join(ordKeys, ", ")
	case changeKindRowVersion, changeKindTimestamp:
		c, ok := findColumn(t, changeCol)
		if !ok {
			return Stmt{}, fmt.Errorf("Änderungsspalte %q gibt es nicht", changeCol)
		}
		chg = changeCol
		q := d.Quote(changeCol)
		qt := "[t]." + q
		if changeKind == changeKindRowVersion {
			if !c.IsRowVersion {
				return Stmt{}, fmt.Errorf("Spalte %q ist keine rowversion-Spalte", changeCol)
			}
			if cursor != "" {
				b, err := hex.DecodeString(cursor)
				if err != nil || len(b) != 8 {
					return Stmt{}, errors.New("Stand der rowversion ist kein gültiger Hex-Wert")
				}
				args = append(args, b)
				conds = append(conds, q+" > @p"+strconv.Itoa(len(args)))
			}
			// Zeilen noch offener Transaktionen nicht überspringen.
			conds = append(conds, q+" < MIN_ACTIVE_ROWVERSION()")
			orderBy = qt
		} else {
			cast, err := requireTimestampColumn(c)
			if err != nil {
				return Stmt{}, err
			}
			// Zeilen ohne Änderungszeit werden nicht abgeglichen (und blockieren den Cursor nicht).
			conds = append(conds, q+" IS NOT NULL")
			if cursor != "" {
				var cur tsCursor
				dec := json.NewDecoder(strings.NewReader(cursor))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&cur); err != nil || len(cur.K) != len(keyCols) {
					return Stmt{}, errors.New("Stand des Zeitstempels ist ungültig")
				}
				tm, err := time.Parse(time.RFC3339Nano, cur.T)
				if err != nil {
					return Stmt{}, errors.New("Stand des Zeitstempels ist keine gültige Zeit")
				}
				tm = tm.UTC()
				args = append(args, tm)
				tp := "CAST(@p" + strconv.Itoa(len(args)) + " AS " + cast + ")"
				first := len(args) + 1
				for _, kv := range cur.K {
					args = append(args, kv)
				}
				conds = append(conds, "("+q+" > "+tp+" OR ("+q+" = "+tp+" AND "+d.keysetCondition(t, keyCols, first)+"))")
			}
			if windowDays > 0 {
				conds = append(conds, q+" >= DATEADD(day, -"+strconv.Itoa(windowDays)+", SYSUTCDATETIME())")
			}
			orderBy = qt + ", " + strings.Join(ordKeys, ", ")
		}
	default:
		return Stmt{}, fmt.Errorf("unbekannte Art der Änderungserkennung %q", changeKind)
	}
	list, err := d.selectList(t, cols, keyCols, chg, changeKind == changeKindTimestamp)
	if err != nil {
		return Stmt{}, err
	}
	if filter != "" {
		conds = append(conds, "("+filter+")")
	}
	sqlText := "SELECT TOP (" + strconv.Itoa(limit) + ") " + list + " FROM " + d.qualified(t) + " AS [t]"
	if len(conds) > 0 {
		sqlText += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlText += " ORDER BY " + orderBy
	return Stmt{SQL: sqlText, Args: args}, nil
}

// whereKey bildet "[k1]=@pN AND [k2]=@pN+1"; Schlüsselwerte hängen sich an args.
func (d *mssqlDialect) whereKey(t *TableInfo, keys, vals []string, args []any) (string, []any) {
	var parts []string
	for i, k := range keys {
		args = append(args, vals[i])
		c, _ := findColumn(t, k)
		parts = append(parts, d.Quote(k)+"="+keyPlaceholder(len(args), c))
	}
	return strings.Join(parts, " AND "), args
}

// SelectOne liest genau die Zeile mit diesem Schlüssel.
func (d *mssqlDialect) SelectOne(t *TableInfo, cols, keyCols, keyVals []string) (Stmt, error) {
	if err := checkTable(t); err != nil {
		return Stmt{}, err
	}
	if err := requireUniqueKey(t, keyCols); err != nil {
		return Stmt{}, err
	}
	if len(keyVals) != len(keyCols) {
		return Stmt{}, errors.New("zu jeder Schlüsselspalte wird genau ein Wert gebraucht")
	}
	list, err := d.selectList(t, cols, keyCols, "", false)
	if err != nil {
		return Stmt{}, err
	}
	where, args := d.whereKey(t, keyCols, keyVals, nil)
	return Stmt{SQL: "SELECT TOP (1) " + list + " FROM " + d.qualified(t) + " WHERE " + where, Args: args}, nil
}

// ---------- Schreiben: genau zwei Befehlsformen ----------

// UpdateRow erzeugt "UPDATE TOP (1) … SET … WHERE <Schlüssel>".
//
// Schlüsselregel: keyCols müssen als Menge der Primärschlüssel oder ein
// eindeutiger Schlüssel der Tabelle sein (KeyIsUnique), sonst könnte "TOP (1)"
// eine beliebige von mehreren gleichen Zeilen treffen. Schlüsselspalten dürfen
// nicht im SET stehen.
func (d *mssqlDialect) UpdateRow(t *TableInfo, keyCols, keyVals []string, set map[string]any) (Stmt, error) {
	if err := checkTable(t); err != nil {
		return Stmt{}, err
	}
	if len(set) == 0 {
		return Stmt{}, errors.New("nichts zu ändern")
	}
	if err := requireUniqueKey(t, keyCols); err != nil {
		return Stmt{}, err
	}
	if len(keyVals) != len(keyCols) {
		return Stmt{}, errors.New("zu jeder Schlüsselspalte wird genau ein Wert gebraucht")
	}
	isKey := map[string]bool{}
	for _, k := range keyCols {
		isKey[k] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	var args []any
	for _, n := range names {
		if err := checkIdent("Spaltenname", n); err != nil {
			return Stmt{}, err
		}
		c, ok := findColumn(t, n)
		if !ok {
			return Stmt{}, fmt.Errorf("Spalte %q gibt es in %s.%s nicht", n, t.Schema, t.Name)
		}
		if c.IsKey || isKey[n] {
			return Stmt{}, fmt.Errorf("Schlüsselspalte %q darf nicht geändert werden", n)
		}
		if c.IsIdentity || c.IsComputed || c.IsRowVersion {
			return Stmt{}, fmt.Errorf("Spalte %q (Identität, berechnet oder rowversion) darf nicht geändert werden", n)
		}
		v, err := prepValue(n, set[n])
		if err != nil {
			return Stmt{}, err
		}
		args = append(args, v)
		parts = append(parts, d.Quote(n)+"="+placeholder(len(args), v))
	}
	where, args := d.whereKey(t, keyCols, keyVals, args)
	return Stmt{
		SQL:  "UPDATE TOP (1) " + d.qualified(t) + " SET " + strings.Join(parts, ", ") + " WHERE " + where,
		Args: args,
	}, nil
}

// InsertChild erzeugt "INSERT INTO <Nebentabelle> (…) VALUES (…)". In die
// Quelltabelle wird nie eingefügt; ohne bekannte Quelle (nil) wird gar nicht eingefügt.
func (d *mssqlDialect) InsertChild(source, child *TableInfo, values map[string]any) (Stmt, error) {
	if source == nil || child == nil {
		return Stmt{}, errors.New("Meldungstabelle und Nebentabelle müssen bekannt sein")
	}
	if err := checkTable(child); err != nil {
		return Stmt{}, err
	}
	if strings.EqualFold(source.Schema, child.Schema) && strings.EqualFold(source.Name, child.Name) {
		return Stmt{}, ErrInsertIntoSource
	}
	if len(values) == 0 {
		return Stmt{}, errors.New("nichts einzufügen")
	}
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	var cols, ph []string
	var args []any
	for _, n := range names {
		if err := checkIdent("Spaltenname", n); err != nil {
			return Stmt{}, err
		}
		c, ok := findColumn(child, n)
		if !ok {
			return Stmt{}, fmt.Errorf("Spalte %q gibt es in %s.%s nicht", n, child.Schema, child.Name)
		}
		if c.IsIdentity || c.IsComputed || c.IsRowVersion {
			return Stmt{}, fmt.Errorf("Spalte %q (Identität, berechnet oder rowversion) kann nicht befüllt werden", n)
		}
		v, err := prepValue(n, values[n])
		if err != nil {
			return Stmt{}, err
		}
		args = append(args, v)
		cols = append(cols, d.Quote(n))
		ph = append(ph, placeholder(len(args), v))
	}
	return Stmt{
		SQL:  "INSERT INTO " + d.qualified(child) + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")",
		Args: args,
	}, nil
}
