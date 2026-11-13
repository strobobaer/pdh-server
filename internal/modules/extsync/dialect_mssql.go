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

// checkValue lässt nur die Go-Typen zu, die der Treiber sicher als Parameter überträgt.
func checkValue(col string, v any) error {
	switch v.(type) {
	case nil, string, time.Time, int64, float64, bool, []byte:
		return nil
	}
	return fmt.Errorf("Spalte %q: Werttyp %T wird nicht unterstützt", col, v)
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
	return ti, trows.Err()
}

// ---------- Lesen ----------

// selectList bildet die Spaltenliste: Schlüssel zuerst, dann die übrigen
// sortiert, zuletzt die Änderungsspalte.
func (d *mssqlDialect) selectList(t *TableInfo, cols, keyCols []string, changeCol string) (string, error) {
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
	for _, k := range keys {
		used[k] = true
		list = append(list, d.Quote(k))
	}
	sort.Strings(cs)
	for _, c := range cs {
		if !used[c] && c != changeCol {
			used[c] = true
			list = append(list, d.Quote(c))
		}
	}
	if changeCol != "" && !used[changeCol] {
		list = append(list, d.Quote(changeCol))
	}
	return strings.Join(list, ", "), nil
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
	var args []any
	var conds []string
	orderBy := ""
	keys, err := requireColumns(t, keyCols, "Schlüssel")
	if err != nil {
		return Stmt{}, err
	}
	if len(keys) == 0 {
		return Stmt{}, errors.New("Schlüsselspalten fehlen")
	}
	var quotedKeys []string
	for _, k := range keys {
		quotedKeys = append(quotedKeys, d.Quote(k))
	}
	chg := ""
	switch changeKind {
	case changeKindNone, "":
		if cursor != "" {
			return Stmt{}, errors.New("ohne Änderungsspalte gibt es keinen Stand")
		}
		orderBy = strings.Join(quotedKeys, ", ")
	case changeKindRowVersion, changeKindTimestamp:
		c, ok := findColumn(t, changeCol)
		if !ok {
			return Stmt{}, fmt.Errorf("Änderungsspalte %q gibt es nicht", changeCol)
		}
		if err := checkIdent("Spaltenname", changeCol); err != nil {
			return Stmt{}, err
		}
		chg = changeCol
		q := d.Quote(changeCol)
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
			orderBy = q
		} else {
			if c.IsRowVersion {
				return Stmt{}, fmt.Errorf("Spalte %q ist eine rowversion, kein Zeitstempel", changeCol)
			}
			if cursor != "" {
				tm, err := time.Parse(time.RFC3339Nano, cursor)
				if err != nil {
					return Stmt{}, errors.New("Stand des Zeitstempels ist keine gültige Zeit")
				}
				args = append(args, tm)
				conds = append(conds, q+" >= @p"+strconv.Itoa(len(args)))
			}
			if windowDays > 0 {
				conds = append(conds, q+" >= DATEADD(day, -"+strconv.Itoa(windowDays)+", SYSUTCDATETIME())")
			}
			orderBy = q + ", " + strings.Join(quotedKeys, ", ")
		}
	default:
		return Stmt{}, fmt.Errorf("unbekannte Art der Änderungserkennung %q", changeKind)
	}
	list, err := d.selectList(t, cols, keyCols, chg)
	if err != nil {
		return Stmt{}, err
	}
	if filter != "" {
		conds = append(conds, "("+filter+")")
	}
	sqlText := "SELECT TOP (" + strconv.Itoa(limit) + ") " + list + " FROM " + d.qualified(t)
	if len(conds) > 0 {
		sqlText += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlText += " ORDER BY " + orderBy
	return Stmt{SQL: sqlText, Args: args}, nil
}

// whereKey bildet "[k1]=@pN AND [k2]=@pN+1"; Schlüsselwerte hängen sich an args.
func (d *mssqlDialect) whereKey(keys, vals []string, args []any) (string, []any) {
	var parts []string
	for i, k := range keys {
		args = append(args, vals[i])
		parts = append(parts, d.Quote(k)+"=@p"+strconv.Itoa(len(args)))
	}
	return strings.Join(parts, " AND "), args
}

// SelectOne liest genau die Zeile mit diesem Schlüssel.
func (d *mssqlDialect) SelectOne(t *TableInfo, cols, keyCols, keyVals []string) (Stmt, error) {
	if err := checkTable(t); err != nil {
		return Stmt{}, err
	}
	keys, err := requireColumns(t, keyCols, "Schlüssel")
	if err != nil {
		return Stmt{}, err
	}
	if len(keys) == 0 || len(keys) != len(keyCols) || len(keyVals) != len(keyCols) {
		return Stmt{}, errors.New("zu jeder Schlüsselspalte wird genau ein Wert gebraucht")
	}
	if err := checkPrimaryKey(t, keyCols); err != nil {
		return Stmt{}, err
	}
	list, err := d.selectList(t, cols, keyCols, "")
	if err != nil {
		return Stmt{}, err
	}
	where, args := d.whereKey(keyCols, keyVals, nil)
	return Stmt{SQL: "SELECT TOP (1) " + list + " FROM " + d.qualified(t) + " WHERE " + where, Args: args}, nil
}

// ---------- Schreiben: genau zwei Befehlsformen ----------

// checkPrimaryKey: Hat die Tabelle einen Primärschlüssel, müssen keyCols genau
// diese Spalten sein; sonst gelten die genannten (bereits geprüften) Spalten.
func checkPrimaryKey(t *TableInfo, keyCols []string) error {
	var pk []string
	for _, c := range t.Columns {
		if c.IsKey {
			pk = append(pk, c.Name)
		}
	}
	if len(pk) == 0 {
		return nil
	}
	a := append([]string(nil), keyCols...)
	sort.Strings(a)
	sort.Strings(pk)
	if !equalStrings(a, pk) {
		return errors.New("die Schlüsselspalten sind nicht der Primärschlüssel der Tabelle")
	}
	return nil
}

// UpdateRow erzeugt "UPDATE TOP (1) … SET … WHERE <Schlüssel>".
//
// Schlüsselregel: Hat die Tabelle einen Primärschlüssel (IsKey), müssen keyCols
// genau diese Spalten sein. Hat sie keinen deklarierten Primärschlüssel, gelten
// die vom Aufrufer genannten Spalten, sofern sie existieren und nicht im SET stehen.
func (d *mssqlDialect) UpdateRow(t *TableInfo, keyCols, keyVals []string, set map[string]any) (Stmt, error) {
	if err := checkTable(t); err != nil {
		return Stmt{}, err
	}
	if len(set) == 0 {
		return Stmt{}, errors.New("nichts zu ändern")
	}
	keys, err := requireColumns(t, keyCols, "Schlüssel")
	if err != nil {
		return Stmt{}, err
	}
	if len(keys) == 0 || len(keys) != len(keyCols) {
		return Stmt{}, errors.New("Schlüsselspalten fehlen oder sind doppelt")
	}
	if len(keyVals) != len(keyCols) {
		return Stmt{}, errors.New("zu jeder Schlüsselspalte wird genau ein Wert gebraucht")
	}
	if err := checkPrimaryKey(t, keyCols); err != nil {
		return Stmt{}, err
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
		if err := checkValue(n, set[n]); err != nil {
			return Stmt{}, err
		}
		args = append(args, set[n])
		parts = append(parts, d.Quote(n)+"=@p"+strconv.Itoa(len(args)))
	}
	where, args := d.whereKey(keyCols, keyVals, args)
	return Stmt{
		SQL:  "UPDATE TOP (1) " + d.qualified(t) + " SET " + strings.Join(parts, ", ") + " WHERE " + where,
		Args: args,
	}, nil
}

// InsertChild erzeugt "INSERT INTO <Nebentabelle> (…) VALUES (…)". In die
// Quelltabelle wird nie eingefügt.
func (d *mssqlDialect) InsertChild(source, child *TableInfo, values map[string]any) (Stmt, error) {
	if err := checkTable(child); err != nil {
		return Stmt{}, err
	}
	if source != nil && strings.EqualFold(source.Schema, child.Schema) && strings.EqualFold(source.Name, child.Name) {
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
		if err := checkValue(n, values[n]); err != nil {
			return Stmt{}, err
		}
		args = append(args, values[n])
		cols = append(cols, d.Quote(n))
		ph = append(ph, "@p"+strconv.Itoa(len(args)))
	}
	return Stmt{
		SQL:  "INSERT INTO " + d.qualified(child) + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")",
		Args: args,
	}, nil
}

func equalStrings(a, b []string) bool {
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
