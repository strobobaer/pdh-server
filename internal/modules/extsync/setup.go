package extsync

import (
	"fmt"
	"strings"
)

// Prüfregeln der Einrichtung (Cockpit Schritte Quelle und Zuordnungen). Die
// Oberfläche und die spätere KI-Einrichtung rufen dieselben Funktionen; die
// Handler enthalten keine eigene Logik. Geprüft wird immer gegen den GELESENEN
// Aufbau der Fremdtabelle – Namen kommen nie ungeprüft aus einem Formular.

// CheckSource prüft die Quelle eines Profils gegen den Aufbau t der gewählten
// Tabelle: Schlüssel eindeutig, Änderungsspalte und Anlagedatum-Spalte
// vorhanden und vom passenden Typ, Zeitzone bekannt, Filter erlaubt.
func CheckSource(d Dialect, t *TableInfo, p *Profile) error {
	if d == nil || t == nil || p == nil {
		return fmt.Errorf("Aufbau der Tabelle ist nicht gelesen")
	}
	if len(p.KeyColumns) == 0 {
		return fmt.Errorf("Bitte mindestens eine Schlüsselspalte wählen")
	}
	for _, k := range p.KeyColumns {
		if _, ok := findColumn(t, k); !ok {
			return fmt.Errorf("Schlüsselspalte ‚%s‘ gibt es in der Tabelle nicht", k)
		}
	}
	if !KeyIsUnique(t, p.KeyColumns) {
		return fmt.Errorf("Die gewählten Schlüsselspalten sind nicht eindeutig – nötig ist der Primärschlüssel oder ein eindeutiger Index")
	}
	switch p.ChangeKind {
	case "", changeKindNone:
		if p.ChangeColumn != "" {
			return fmt.Errorf("Ohne Änderungserkennung gibt es keine Änderungsspalte")
		}
	case changeKindRowVersion:
		c, ok := findColumn(t, p.ChangeColumn)
		if !ok {
			return fmt.Errorf("Änderungsspalte ‚%s‘ gibt es in der Tabelle nicht", p.ChangeColumn)
		}
		if !c.IsRowVersion {
			return fmt.Errorf("Spalte ‚%s‘ ist keine rowversion-Spalte", p.ChangeColumn)
		}
	case changeKindTimestamp:
		c, ok := findColumn(t, p.ChangeColumn)
		if !ok {
			return fmt.Errorf("Änderungsspalte ‚%s‘ gibt es in der Tabelle nicht", p.ChangeColumn)
		}
		if isTime, _ := d.TimeColumn(*c); !isTime {
			return fmt.Errorf("Änderungsspalte ‚%s‘ ist keine Datums-/Zeitspalte", p.ChangeColumn)
		}
	default:
		return fmt.Errorf("Art der Änderungserkennung ‚%s‘ ist unbekannt", p.ChangeKind)
	}
	if p.CreatedColumn != "" {
		c, ok := findColumn(t, p.CreatedColumn)
		if !ok {
			return fmt.Errorf("Anlagedatum-Spalte ‚%s‘ gibt es in der Tabelle nicht", p.CreatedColumn)
		}
		if isTime, _ := d.TimeColumn(*c); !isTime {
			return fmt.Errorf("Anlagedatum-Spalte ‚%s‘ ist keine Datums-/Zeitspalte", p.CreatedColumn)
		}
	}
	if _, err := p.ForeignLocation(nil); err != nil {
		return err
	}
	return ValidateFilter(p.FilterSQL)
}

// ChangeOption ist eine wählbare Änderungserkennung einer Tabelle.
type ChangeOption struct{ Kind, Column string }

// ChangeOptions liefert, womit sich Änderungen in t erkennen lassen:
// rowversion-Spalten zuerst (am sichersten), dann Datums-/Zeitspalten, zuletzt
// "ohne" (jede Zeile wird über einen Prüfwert verglichen).
func ChangeOptions(d Dialect, t *TableInfo) []ChangeOption {
	var rv, ts []ChangeOption
	if t != nil && d != nil {
		for _, c := range t.Columns {
			if c.IsRowVersion {
				rv = append(rv, ChangeOption{changeKindRowVersion, c.Name})
			} else if isTime, dateOnly := d.TimeColumn(c); isTime && !dateOnly {
				ts = append(ts, ChangeOption{changeKindTimestamp, c.Name})
			}
		}
	}
	return append(append(rv, ts...), ChangeOption{Kind: changeKindNone})
}

// TimeColumns liefert die Spalten von t, die als Anlagedatum taugen.
func TimeColumns(d Dialect, t *TableInfo) []string {
	var out []string
	if t == nil || d == nil {
		return nil
	}
	for _, c := range t.Columns {
		if isTime, _ := d.TimeColumn(c); isTime {
			out = append(out, c.Name)
		}
	}
	return out
}

// OutLockReason sagt, warum die Fremdspalte col nicht zurückgeschrieben werden
// darf ("" = erlaubt): Schreiben an der Verbindung aus, Schlüsselspalte,
// berechnete Spalte, rowversion oder fortlaufende Nummer.
func OutLockReason(t *TableInfo, p *Profile, col string, allowWrite bool) string {
	if !allowWrite {
		return "Schreiben ist an der Verbindung nicht erlaubt"
	}
	if col == "" || t == nil {
		return ""
	}
	if p != nil && containsStr(p.KeyColumns, col) {
		return "Schlüsselspalte wird nie geändert"
	}
	c, ok := findColumn(t, col)
	switch {
	case !ok:
		return ""
	case c.IsKey:
		return "Schlüsselspalte wird nie geändert"
	case c.IsRowVersion:
		return "rowversion-Spalte vergibt die Datenbank selbst"
	case c.IsComputed:
		return "berechnete Spalte"
	case c.IsIdentity:
		return "fortlaufende Nummer vergibt die Datenbank selbst"
	}
	return ""
}

// MissingRequired liefert die Pflichtfelder des Ziels, die weder eine Zuordnung
// (Spalte oder fester Wert, Richtung herein/beides) noch einen Vorgabewert haben.
func MissingRequired(p *Profile, maps []FieldMap) []PDHField {
	if p == nil {
		return nil
	}
	mapped := map[string]bool{}
	for _, m := range maps {
		if m.Direction == DirOut {
			continue
		}
		if m.ForeignColumn != "" || (m.FixedValue != nil && strings.TrimSpace(*m.FixedValue) != "") {
			mapped[m.PDHField] = true
		}
	}
	var out []PDHField
	for _, f := range Fields(p.TargetType) {
		if f.Required && !mapped[f.Key] && strings.TrimSpace(p.Defaults[f.Key]) == "" {
			out = append(out, f)
		}
	}
	return out
}

// CheckMaps prüft die Zuordnung gegen den Aufbau t (comments = Aufbau der
// Kommentartabelle, nil = keine oder nicht gelesen). Jeder Befund nennt das
// PDH-Feld (Field) und den Grund; leer = in Ordnung. Fehlende Pflichtfelder
// sind KEIN Befund (sie werden markiert, das Profil lässt sich speichern, aber
// nicht bestehen der Trockenlauf) – siehe MissingRequired.
func CheckMaps(d Dialect, t *TableInfo, comments *TableInfo, p *Profile, maps []FieldMap, allowWrite bool) []RunError {
	var errs []RunError
	bad := func(field, format string, args ...any) {
		errs = append(errs, RunError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if t == nil || p == nil {
		return []RunError{{Message: "Aufbau der Tabelle ist nicht gelesen"}}
	}
	fields := fieldIndex(Fields(p.TargetType))
	seen := map[string]bool{}
	for _, m := range maps {
		f, ok := fields[m.PDHField]
		if !ok {
			bad(m.PDHField, "Feld ‚%s‘ gibt es im Ziel nicht", m.PDHField)
			continue
		}
		if seen[m.PDHField] {
			bad(m.PDHField, "Feld ‚%s‘ ist mehrfach zugeordnet", f.Label)
			continue
		}
		seen[m.PDHField] = true
		fixed := m.FixedValue != nil && *m.FixedValue != ""
		switch m.Direction {
		case DirIn, DirOut, DirBoth:
		default:
			bad(m.PDHField, "%s: Richtung ‚%s‘ ist unbekannt", f.Label, m.Direction)
			continue
		}
		if m.ForeignColumn == "" && !fixed {
			bad(m.PDHField, "%s: weder Spalte noch fester Wert", f.Label)
			continue
		}
		if fixed {
			if m.Direction != DirIn {
				bad(m.PDHField, "%s: ein fester Wert gilt nur hereinkommend", f.Label)
			}
			if f.Kind == "enum" {
				if _, ok := canonEnum(f, *m.FixedValue); !ok {
					bad(m.PDHField, "%s: fester Wert ‚%s‘ ist kein gültiger Wert", f.Label, *m.FixedValue)
				}
			}
		}
		if m.ForeignColumn != "" {
			if _, ok := findColumn(t, m.ForeignColumn); !ok {
				bad(m.PDHField, "%s: Spalte ‚%s‘ gibt es in der Tabelle nicht", f.Label, m.ForeignColumn)
				continue
			}
			if m.Direction == DirOut || m.Direction == DirBoth {
				if why := OutLockReason(t, p, m.ForeignColumn, allowWrite); why != "" {
					bad(m.PDHField, "%s: Zurückschreiben nicht möglich – %s", f.Label, why)
				}
			}
		}
		if f.Kind == "enum" {
			for foreign, pdh := range m.Translate {
				if _, ok := canonEnum(f, pdh); !ok {
					bad(m.PDHField, "%s: Übersetzung ‚%s‘ → ‚%s‘ führt zu keinem gültigen Wert", f.Label, foreign, pdh)
				}
			}
		}
	}
	if ct := p.CommentTable; ct != nil {
		switch {
		case !allowWrite:
			bad("", "Kommentare: Schreiben ist an der Verbindung nicht erlaubt")
		case len(p.KeyColumns) != 1:
			bad("", "Kommentare: nur bei genau einer Schlüsselspalte möglich")
		case comments == nil:
			bad("", "Kommentare: Tabelle ‚%s.%s‘ gibt es nicht", ct.Schema, ct.Table)
		case strings.EqualFold(comments.Schema, t.Schema) && strings.EqualFold(comments.Name, t.Name):
			bad("", "Kommentare: die Meldungstabelle selbst kann nicht die Kommentartabelle sein")
		default:
			need := map[string]string{"Verweis auf die Meldung": ct.FKColumn, "Text": ct.TextColumn}
			for what, col := range need {
				if col == "" {
					bad("", "Kommentare: Spalte für %s fehlt", what)
				}
			}
			for _, col := range []string{ct.FKColumn, ct.TextColumn, ct.AuthorColumn, ct.TimeColumn} {
				if col == "" {
					continue
				}
				if _, ok := findColumn(comments, col); !ok {
					bad("", "Kommentare: Spalte ‚%s‘ gibt es in ‚%s.%s‘ nicht", col, ct.Schema, ct.Table)
				}
			}
		}
	}
	return errs
}

// EnumValue liefert den gültigen Wert eines Aufzählungsfelds (Groß-/
// Kleinschreibung egal) – für Vorgabewerte im Cockpit.
func EnumValue(f PDHField, v string) (string, bool) { return canonEnum(f, v) }
