package extsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Reine Logik ohne Datenbankzugriff: Fremdzeile -> PDH-Werte, PDH-Werte ->
// Fremdspalten und die Entscheidung je Feld, wenn sich eine oder beide
// Seiten geändert haben. Keine Eingabe darf zu einem Absturz führen.

const wildcardKey = "*"

// Feldarten des Katalogs (siehe PDHField.Kind).
const (
	fieldKindEnum = "enum"
	fieldKindTime = "time"
)

func fieldIndex(fields []PDHField) map[string]PDHField {
	idx := make(map[string]PDHField, len(fields))
	for _, f := range fields {
		idx[f.Key] = f
	}
	return idx
}

func unknownFieldErr(key string) RunError {
	return RunError{Field: key, Message: fmt.Sprintf("Feld ‚%s‘ ist unbekannt", key)}
}

func notMappedErr(f PDHField, val string) RunError {
	return RunError{Field: f.Key, Message: fmt.Sprintf("Wert ‚%s‘ für Feld ‚%s‘ ist nicht zugeordnet", val, f.Label)}
}

func badDateErr(f PDHField, val string) RunError {
	return RunError{Field: f.Key, Message: fmt.Sprintf("Wert ‚%s‘ für Feld ‚%s‘ ist kein gültiges Datum", val, f.Label)}
}

// foreignText wandelt einen Wert aus der Fremddatenbank in Text.
func foreignText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		// Ganzzahlige Werte ohne ".0", nie mit Exponent.
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case []byte:
		if utf8.Valid(x) {
			return strings.TrimSpace(string(x))
		}
		return hex.EncodeToString(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

// parseTime liest die erlaubten Zeitformen; Ergebnis in UTC.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// canonTime ist die PDH-Textform der Zeit: RFC 3339, UTC, auf Sekunden.
func canonTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

// canonEnum liefert die kanonische Schreibweise eines Aufzählungswerts.
func canonEnum(f PDHField, v string) (string, bool) {
	for _, c := range f.Values {
		if strings.EqualFold(c, v) {
			return c, true
		}
	}
	return "", false
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// lookupTranslate sucht den Fremdwert ohne Beachtung von Groß/Klein. Bei
// mehreren Schlüsseln, die gleich falten, gewinnt der kleinste (deterministisch).
// "*" ist nur Auffangwert und wird hier nie als Treffer gewertet.
func lookupTranslate(tr map[string]string, foreign string) (string, bool) {
	want := strings.ToLower(foreign)
	best, found := "", false
	var bestKey string
	for k, v := range tr {
		if k == wildcardKey || strings.ToLower(strings.TrimSpace(k)) != want {
			continue
		}
		if !found || k < bestKey {
			best, bestKey, found = v, k, true
		}
	}
	return best, found
}

// ToPDH wandelt eine Fremdzeile in PDH-Feldwerte. Je Fremdzeile gilt: ein
// Fehler betrifft nur das jeweilige Feld; ForeignKey bleibt leer (füllt der
// Aufrufer). Defaults füllen nur zugeordnete Felder, die leer sind.
func ToPDH(maps []FieldMap, fields []PDHField, row map[string]any, defaults map[string]string) (map[string]string, []RunError) {
	idx := fieldIndex(fields)
	values := map[string]string{}
	var errs []RunError
	for _, m := range maps {
		if m.FixedValue == nil && m.Direction != DirIn && m.Direction != DirBoth {
			continue // nur hinaus: wird nicht aus der Fremdzeile gelesen
		}
		f, ok := idx[m.PDHField]
		if !ok {
			errs = append(errs, unknownFieldErr(m.PDHField))
			continue
		}
		var text string
		if m.FixedValue != nil {
			text = strings.TrimSpace(*m.FixedValue)
		} else {
			text = foreignText(row[m.ForeignColumn])
			if text != "" && len(m.Translate) > 0 {
				if v, hit := lookupTranslate(m.Translate, text); hit {
					text = strings.TrimSpace(v)
				} else if v, fb := m.Translate[wildcardKey]; fb {
					text = strings.TrimSpace(v)
				}
			}
		}
		if text == "" {
			text = strings.TrimSpace(defaults[f.Key])
		}
		val, err := finishPDHValue(f, text)
		if err != nil {
			errs = append(errs, *err)
			continue
		}
		if val == "" && f.Required {
			errs = append(errs, RunError{Field: f.Key, Message: fmt.Sprintf("Pflichtfeld ‚%s‘ ist leer", f.Label)})
			continue
		}
		values[f.Key] = val
	}
	return values, errs
}

// finishPDHValue prüft und normalisiert nach der Übersetzung: Aufzählung,
// Zeit, Kürzung. Leer bleibt leer.
func finishPDHValue(f PDHField, text string) (string, *RunError) {
	if text == "" {
		return "", nil
	}
	switch f.Kind {
	case fieldKindEnum:
		c, ok := canonEnum(f, text)
		if !ok {
			e := notMappedErr(f, text)
			return "", &e
		}
		return c, nil
	case fieldKindTime:
		t, ok := parseTime(text)
		if !ok {
			e := badDateErr(f, text)
			return "", &e
		}
		return canonTime(t), nil
	}
	return truncateRunes(text, f.MaxLen), nil
}

// reverseTranslate kehrt Translate um. Treffen mehrere Fremdwerte auf den
// PDH-Wert zu, gewinnt der kleinste Schlüssel in einfacher Zeichenfolge
// (deterministisch, unabhängig von der Map-Reihenfolge). "*" zählt nicht.
func reverseTranslate(tr map[string]string, pdh string) (string, bool) {
	best, found := "", false
	for k, v := range tr {
		if k == wildcardKey || !strings.EqualFold(strings.TrimSpace(v), pdh) {
			continue
		}
		if !found || k < best {
			best, found = k, true
		}
	}
	return best, found
}

// ToForeign wandelt PDH-Werte in Fremdspaltenwerte (nur Richtung hinaus oder
// beides). Umgewandelt werden nur Felder, die in pdhValues stehen. Felder mit
// festem Wert werden nie zurückgeschrieben. Leere Zeit ergibt nil.
func ToForeign(maps []FieldMap, fields []PDHField, pdhValues map[string]string) (map[string]any, []RunError) {
	idx := fieldIndex(fields)
	set := map[string]any{}
	var errs []RunError
	for _, m := range maps {
		if (m.Direction != DirOut && m.Direction != DirBoth) || m.ForeignColumn == "" || m.FixedValue != nil {
			continue
		}
		raw, present := pdhValues[m.PDHField]
		if !present {
			continue
		}
		f, ok := idx[m.PDHField]
		if !ok {
			errs = append(errs, unknownFieldErr(m.PDHField))
			continue
		}
		val := strings.TrimSpace(raw)
		if f.Kind == fieldKindTime {
			if val == "" {
				set[m.ForeignColumn] = nil
				continue
			}
			t, ok := parseTime(val)
			if !ok {
				errs = append(errs, badDateErr(f, val))
				continue
			}
			set[m.ForeignColumn] = t
			continue
		}
		if val != "" && len(m.Translate) > 0 {
			if k, hit := reverseTranslate(m.Translate, val); hit {
				val = k
			} else if f.Kind == fieldKindEnum {
				errs = append(errs, notMappedErr(f, val))
				continue
			}
		}
		set[m.ForeignColumn] = val
	}
	return set, errs
}

// RowMark liefert die Änderungsmarke einer Fremdzeile: bei rowversion Hex des
// Änderungswerts, bei timestamp die Zeit in UTC (RFC 3339 mit Nanosekunden),
// sonst (oder ohne Änderungswert) SHA-256 über die zugeordneten Werte als
// JSON-Feld aus [Schlüssel, Wert]-Paaren in sortierter Reihenfolge.
func RowMark(changeKind string, changeVal any, mapped map[string]string) string {
	if changeVal != nil && (changeKind == changeKindRowVersion || changeKind == changeKindTimestamp) {
		switch x := changeVal.(type) {
		case []byte:
			return hex.EncodeToString(x)
		case time.Time:
			return x.UTC().Format(time.RFC3339Nano)
		default:
			return foreignText(x)
		}
	}
	keys := make([]string, 0, len(mapped))
	for k := range mapped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, len(keys))
	for i, k := range keys {
		pairs[i] = [2]string{k, mapped[k]}
	}
	b, _ := json.Marshal(pairs)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Decision sagt je PDH-Feld, was zu tun ist. Alle drei Maps sind nie nil und
// enthalten nur Felder mit Handlungsbedarf. KeepPDH und ForeignLost haben
// stets dieselben Schlüssel.
type Decision struct {
	Apply       map[string]string // Fremdwert ins PDH übernehmen
	KeepPDH     map[string]string // PDH-Wert bleibt (und geht auf die Warteliste)
	ForeignLost map[string]string // der dabei verworfene Fremdwert, für den Verlauf
}

// Decide entscheidet je Feld mit Richtung herein/beides. Alle Werte stehen in
// PDH-Form und sind nach PDH-Feld geschlüsselt; verglichen wird nach
// strings.TrimSpace, ohne Groß/Klein-Faltung. foreignNow und pdhNow: fehlend
// zählt als "". seen und written: ein fehlender Eintrag heißt "nie gesehen"
// bzw. "nie zurückgeschrieben" und trifft nie, auch nicht auf einen leeren
// Wert (sonst ginge eine Leerung im Fremdsystem verloren). Ist weder seen
// noch written bekannt (erster Abgleich nach der Verknüpfung), ist die Fremd-
// seite die Quelle: nichts bei Gleichheit, sonst übernehmen. Sind beide Seiten
// geändert, gewinnt PDH.
func Decide(maps []FieldMap, seen, written, foreignNow, pdhNow map[string]string) Decision {
	d := Decision{Apply: map[string]string{}, KeepPDH: map[string]string{}, ForeignLost: map[string]string{}}
	for _, m := range maps {
		if m.FixedValue != nil || m.PDHField == "" || (m.Direction != DirIn && m.Direction != DirBoth) {
			continue
		}
		k := m.PDHField
		f := strings.TrimSpace(foreignNow[k])
		p := strings.TrimSpace(pdhNow[k])
		s, hasSeen := seen[k]
		w, hasWritten := written[k]
		s, w = strings.TrimSpace(s), strings.TrimSpace(w)
		switch {
		case !hasSeen && !hasWritten:
			if f != p {
				d.Apply[k] = f
			}
		case hasSeen && f == s:
		case hasWritten && f == w:
		case (hasSeen && p == s) || (hasWritten && p == w) || p == f:
			d.Apply[k] = f
		default:
			d.KeepPDH[k] = p
			d.ForeignLost[k] = f
		}
	}
	return d
}
