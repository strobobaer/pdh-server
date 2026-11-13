package extsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Dieses Paket ist der EINZIGE Ort, an dem SQL für die Fremddatenbank
// entsteht. Garantien (durch Aufbau, nicht durch Disziplin):
//   - Schreibend gibt es nur UpdateRow (UPDATE TOP (1) über den Schlüssel) und
//     InsertChild (INSERT in die Nebentabelle). Kein DELETE, TRUNCATE, DROP,
//     MERGE; kein INSERT in die Meldungstabelle.
//   - Tabellen- und Spaltennamen kommen nur aus dem gelesenen Aufbau
//     (TableInfo) und werden gequotet. Werte gehen nur als Parameter.
//   - Es gibt keine Funktion, die rohes SQL entgegennimmt und ausführt. Der
//     einzige freie Text ist der Lese-Filter; er läuft durch ValidateFilter.

// Arten der Änderungserkennung (Profile.ChangeKind).
const (
	changeKindNone       = "none"
	changeKindTimestamp  = "timestamp"
	changeKindRowVersion = "rowversion"
)

// Column beschreibt eine Spalte der Fremdtabelle.
type Column struct {
	Name, Type                                  string
	Nullable                                    bool
	MaxLen                                      int // in Zeichen; 0 = unbegrenzt/unbekannt
	IsKey, IsRowVersion, IsIdentity, IsComputed bool
}

// TableInfo beschreibt eine Fremdtabelle, wie sie im Katalog steht.
type TableInfo struct {
	Schema, Name string
	Columns      []Column
	Triggers     []string
	// UniqueKeys sind die eindeutigen Schlüssel (Primärschlüssel zuerst, dann
	// eindeutige Indizes ohne Filter und ohne Spalten, die NULL sein dürfen).
	UniqueKeys [][]string
}

// KeyIsUnique sagt, ob keyCols (als Menge) den Primärschlüssel oder einen
// eindeutigen Schlüssel der Tabelle bilden. Nur dann ist eine Zeile über
// diese Spalten eindeutig zu finden.
func KeyIsUnique(t *TableInfo, keyCols []string) bool {
	if t == nil || len(keyCols) == 0 {
		return false
	}
	want := map[string]bool{}
	for _, k := range keyCols {
		if want[k] {
			return false
		}
		want[k] = true
	}
	same := func(cols []string) bool {
		if len(cols) != len(want) {
			return false
		}
		seen := map[string]bool{}
		for _, c := range cols {
			if !want[c] || seen[c] {
				return false
			}
			seen[c] = true
		}
		return true
	}
	var pk []string
	for _, c := range t.Columns {
		if c.IsKey {
			pk = append(pk, c.Name)
		}
	}
	if len(pk) > 0 && same(pk) {
		return true
	}
	for _, u := range t.UniqueKeys {
		if len(u) > 0 && same(u) {
			return true
		}
	}
	return false
}

// Stmt ist ein fertiger Befehl: SQL-Text und die Parameter dazu. Reine Daten.
type Stmt struct {
	SQL  string
	Args []any
}

// ErrInsertIntoSource wird gegeben, wenn in die Meldungstabelle eingefügt werden soll.
var ErrInsertIntoSource = errors.New("in die Meldungstabelle wird nichts eingefügt")

// Dialect erzeugt die Befehle für eine Datenbankart und liest deren Aufbau.
// Die Schnittstelle enthält keine datenbankspezifischen Typen.
//
// Vertrag:
//   - Schlüssel sind Text: Schlüsselspalten dürfen ganzzahlig, Zeichenkette oder
//     GUID sein; SelectRows/SelectOne liefern sie als Text (GUID klein
//     geschrieben), KeyValues macht daraus die Schlüsselwerte für
//     UpdateRow/SelectOne.
//   - Der Schlüssel muss eindeutig sein (Primärschlüssel oder eindeutiger
//     Index, siehe KeyIsUnique); sonst lehnen UpdateRow und SelectOne ab.
//   - Cursor sind undurchsichtiger Text. Ihn erzeugt allein NextCursor aus der
//     letzten Zeile einer Seite; Aufrufer lesen oder bauen ihn nie selbst. Auch
//     ohne Änderungsspalte (none) gibt es einen Cursor: er hält die Schlüssel der
//     letzten Zeile, damit sich die ganze Tabelle in Schlüsselfolge durchlesen lässt.
//   - Zeitstempel-Spalten (Art timestamp) dürfen datetime, datetime2,
//     smalldatetime oder date sein. Sie werden als genauer datetime2-Wert gelesen
//     und mit einem Parameter des eigenen Spaltentyps verglichen. Zeilen ohne
//     Änderungszeit (NULL) werden nicht gelesen.
//   - Zeiten sind UTC: Parameter werden in UTC umgerechnet, gelesene Zeiten
//     gelten als UTC.
type Dialect interface {
	// Tables liefert die Benutzertabellen (nur Schema und Name).
	Tables(ctx context.Context, db *sql.DB) ([]TableInfo, error)
	// Describe liefert Spalten, Schlüssel und Auslöser; ErrNotFound bei unbekannter Tabelle.
	Describe(ctx context.Context, db *sql.DB, schema, table string) (*TableInfo, error)
	// SelectRows liest geänderte Zeilen nach dem Cursor (leer = von Anfang an).
	SelectRows(t *TableInfo, cols, keyCols []string, changeCol, changeKind, cursor, filter string, windowDays, limit int) (Stmt, error)
	// NextCursor bildet den Cursor für die LETZTE Zeile einer Seite von SelectRows.
	NextCursor(t *TableInfo, keyCols []string, changeCol, changeKind string, lastRow map[string]any) (string, error)
	// KeyValues macht aus einer von SelectRows gelieferten Zeile die Schlüsselwerte als Text.
	KeyValues(t *TableInfo, keyCols []string, row map[string]any) ([]string, error)
	// SelectOne liest genau die Zeile mit diesem Schlüssel.
	SelectOne(t *TableInfo, cols, keyCols, keyVals []string) (Stmt, error)
	// UpdateRow ändert genau eine Zeile über ihren (eindeutigen) Schlüssel.
	UpdateRow(t *TableInfo, keyCols, keyVals []string, set map[string]any) (Stmt, error)
	// InsertChild hängt eine Zeile an die Nebentabelle (nie an die Quelltabelle).
	InsertChild(source, child *TableInfo, values map[string]any) (Stmt, error)
	// Quote setzt einen Bezeichner in die Anführung der Datenbankart.
	Quote(name string) string
}

// DialectFor liefert den Dialekt zur Datenbankart.
func DialectFor(kind string) (Dialect, error) {
	if kind == "mssql" {
		return &mssqlDialect{}, nil
	}
	return nil, errors.New("Datenbankart wird noch nicht unterstützt")
}

// maxFilterLen begrenzt den Lese-Filter.
const maxFilterLen = 2000

// verbotene Wörter im Filter (Großschreibung).
var forbiddenFilterWords = map[string]bool{
	"DELETE": true, "UPDATE": true, "INSERT": true, "MERGE": true, "DROP": true,
	"TRUNCATE": true, "EXEC": true, "EXECUTE": true, "ALTER": true, "CREATE": true,
	"GRANT": true, "INTO": true, "OPENROWSET": true, "OPENQUERY": true,
	"OPENDATASOURCE": true, "WAITFOR": true, "SHUTDOWN": true, "BACKUP": true,
	"RESTORE": true, "DBCC": true, "KILL": true, "DENY": true, "REVOKE": true,
	"USE": true, "SELECT": true, "UNION": true,
}

// ValidateFilter prüft den frei eingegebenen Lese-Filter (Bedingung für WHERE).
// Erlaubt sind Vergleiche, AND/OR/NOT, IN, LIKE, IS NULL und Klammern. Zeichenketten
// in '...' und Bezeichner in [...] werden nicht nach Wörtern durchsucht.
func ValidateFilter(filter string) error {
	if filter == "" {
		return nil
	}
	if len([]rune(filter)) > maxFilterLen {
		return fmt.Errorf("Filter ist zu lang (höchstens %d Zeichen)", maxFilterLen)
	}
	for _, r := range filter {
		if r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return errors.New("Filter enthält ein Steuerzeichen")
		}
	}
	for _, bad := range []string{";", "--", "/*", "*/"} {
		if strings.Contains(filter, bad) {
			return fmt.Errorf("Filter darf %q nicht enthalten", bad)
		}
	}
	rs := []rune(filter)
	depth := 0
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\'' || r == '[' || r == '"':
			closer := r
			if r == '[' {
				closer = ']'
			}
			j := i + 1
			closed := false
			for j < len(rs) {
				if rs[j] == closer {
					if j+1 < len(rs) && rs[j+1] == closer { // verdoppelt = Zeichen selbst
						j += 2
						continue
					}
					closed = true
					break
				}
				j++
			}
			if !closed {
				if r == '[' {
					return errors.New("Filter: eckige Klammer [ ist nicht geschlossen")
				}
				return errors.New("Filter: Anführungszeichen ist nicht geschlossen")
			}
			i = j + 1
		case r == '(':
			depth++
			i++
		case r == ')':
			depth--
			if depth < 0 {
				return errors.New("Filter: Klammern passen nicht zusammen")
			}
			i++
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '@' || rs[j] == '#' || rs[j] == '$') {
				j++
			}
			w := strings.ToUpper(string(rs[i:j]))
			if forbiddenFilterWords[w] {
				return fmt.Errorf("Filter darf das Wort %s nicht enthalten", w)
			}
			if strings.HasPrefix(w, "XP_") {
				return errors.New("Filter darf keine Prozedur xp_… aufrufen")
			}
			if strings.HasPrefix(w, "SP_") {
				return errors.New("Filter darf keine Prozedur sp_… aufrufen")
			}
			i = j
		default:
			i++
		}
	}
	if depth != 0 {
		return errors.New("Filter: Klammern passen nicht zusammen")
	}
	return nil
}
