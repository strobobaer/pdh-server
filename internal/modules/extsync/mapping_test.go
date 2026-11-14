package extsync

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixedStr(s string) *string { return &s }

func ticketMaps() []FieldMap {
	return []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirBoth},
		{ForeignColumn: "Prio", PDHField: "priority", Direction: DirBoth,
			Translate: map[string]string{"1": "critical", "2": "high", "3": "medium", "*": "low"}},
		{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirBoth},
	}
}

func TestToPDHBasics(t *testing.T) {
	fields := Fields(RefTicket)
	due := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("x", 3600))
	row := map[string]any{"Titel": "  Drucker defekt  ", "Prio": int64(2), "Faellig": due}
	got, errs := ToPDH(ticketMaps(), fields, row, nil)
	if len(errs) != 0 {
		t.Fatalf("Fehler: %v", errs)
	}
	want := map[string]string{"title": "Drucker defekt", "priority": "high", "due_date": "2026-03-04T04:06:07Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestToPDHTextConversion(t *testing.T) {
	maps := []FieldMap{{ForeignColumn: "c", PDHField: "description", Direction: DirIn}}
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"  x ", "x"},
		{int64(42), "42"},
		{float64(3), "3"},
		{float64(2.5), "2.5"},
		{float64(1e21), "1000000000000000000000"},
		{true, "1"},
		{false, "0"},
		{[]byte("hallo"), "hallo"},
		{[]byte{0xff, 0x00, 0xab}, "ff00ab"},
		{time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02T03:04:05Z"},
	}
	for _, c := range cases {
		got, errs := ToPDH(maps, Fields(RefTicket), map[string]any{"c": c.in}, nil)
		if len(errs) != 0 || got["description"] != c.want {
			t.Errorf("%#v: got %q errs %v, want %q", c.in, got["description"], errs, c.want)
		}
	}
}

func TestToPDHNull(t *testing.T) {
	maps := []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn},
		{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn},
	}
	got, errs := ToPDH(maps, Fields(RefTicket), map[string]any{"Titel": nil},
		map[string]string{"title": "Ohne Titel", "priority": "medium"})
	if len(errs) != 0 {
		t.Fatalf("Fehler: %v", errs)
	}
	if got["title"] != "Ohne Titel" || got["priority"] != "medium" {
		t.Fatalf("Vorgabewerte fehlen: %v", got)
	}
}

func TestToPDHRequired(t *testing.T) {
	maps := []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}
	_, errs := ToPDH(maps, Fields(RefTicket), map[string]any{"Titel": "  "}, nil)
	want := []RunError{{Field: "title", Message: "Pflichtfeld ‚Titel‘ ist leer"}}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("got %v", errs)
	}
}

func TestToPDHTruncates(t *testing.T) {
	maps := []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}
	long := strings.Repeat("ä", 300) // 600 Bytes, 300 Zeichen
	got, errs := ToPDH(maps, Fields(RefTicket), map[string]any{"Titel": long}, nil)
	if len(errs) != 0 {
		t.Fatalf("Fehler: %v", errs)
	}
	if n := len([]rune(got["title"])); n != 255 {
		t.Fatalf("Länge %d, erwartet 255", n)
	}
	if strings.ContainsRune(got["title"], '�') {
		t.Fatal("Zeichen zerschnitten")
	}
}

func TestToPDHTranslate(t *testing.T) {
	fields := Fields(RefTicket)
	maps := []FieldMap{{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn,
		Translate: map[string]string{"Hoch": "high", "*": "low"}}}
	cases := []struct {
		in   any
		want string
	}{
		{"hoch", "high"}, {"  HOCH ", "high"}, {"unbekannt", "low"}, {int64(7), "low"},
	}
	for _, c := range cases {
		got, errs := ToPDH(maps, fields, map[string]any{"Prio": c.in}, nil)
		if len(errs) != 0 || got["priority"] != c.want {
			t.Errorf("%v: got %q %v want %q", c.in, got["priority"], errs, c.want)
		}
	}
	// Leer wird nie über "*" übersetzt.
	got, _ := ToPDH(maps, fields, map[string]any{"Prio": ""}, nil)
	if got["priority"] != "" {
		t.Errorf("leer sollte leer bleiben: %q", got["priority"])
	}
}

func TestToPDHEnumNoMatch(t *testing.T) {
	fields := Fields(RefTicket)
	maps := []FieldMap{{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn,
		Translate: map[string]string{"1": "high"}}}
	_, errs := ToPDH(maps, fields, map[string]any{"Prio": "X"}, nil)
	want := []RunError{{Field: "priority", Message: "Wert ‚X‘ für Feld ‚Priorität‘ ist nicht zugeordnet"}}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("got %v", errs)
	}
	// Ohne Übersetzung: gültiger Wert wird kanonisch übernommen.
	maps2 := []FieldMap{{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn}}
	got, errs := ToPDH(maps2, fields, map[string]any{"Prio": "HIGH"}, nil)
	if len(errs) != 0 || got["priority"] != "high" {
		t.Fatalf("got %v %v", got, errs)
	}
	_, errs = ToPDH(maps2, fields, map[string]any{"Prio": "dringend"}, nil)
	if len(errs) != 1 {
		t.Fatalf("erwartet Fehler: %v", errs)
	}
	// Übersetzung auf einen ungültigen PDH-Wert ist ebenfalls ein Fehler.
	maps3 := []FieldMap{{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn,
		Translate: map[string]string{"1": "sehr hoch"}}}
	_, errs = ToPDH(maps3, fields, map[string]any{"Prio": "1"}, nil)
	if len(errs) != 1 {
		t.Fatalf("erwartet Fehler: %v", errs)
	}
}

func TestToPDHTime(t *testing.T) {
	fields := Fields(RefTicket)
	maps := []FieldMap{{ForeignColumn: "d", PDHField: "due_date", Direction: DirIn}}
	ok := map[string]string{
		"2026-03-04":                "2026-03-04T00:00:00Z",
		"2026-03-04 05:06:07":       "2026-03-04T05:06:07Z",
		"2026-03-04T05:06:07Z":      "2026-03-04T05:06:07Z",
		"2026-03-04T06:06:07+01:00": "2026-03-04T05:06:07Z",
		"":                          "",
	}
	for in, want := range ok {
		got, errs := ToPDH(maps, fields, map[string]any{"d": in}, nil)
		if len(errs) != 0 || got["due_date"] != want {
			t.Errorf("%q: got %q %v want %q", in, got["due_date"], errs, want)
		}
	}
	_, errs := ToPDH(maps, fields, map[string]any{"d": "gestern"}, nil)
	want := []RunError{{Field: "due_date", Message: "Wert ‚gestern‘ für Feld ‚Fällig am‘ ist kein gültiges Datum"}}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("got %v", errs)
	}
}

func TestToPDHFixedAndDirection(t *testing.T) {
	fields := Fields(RefTicket)
	maps := []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn},
		{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn, FixedValue: fixedStr("medium")},
		{ForeignColumn: "Beschr", PDHField: "description", Direction: DirOut},
		{PDHField: "status", Direction: DirIn, FixedValue: fixedStr("open")},
	}
	row := map[string]any{"Titel": "T", "Prio": "critical", "Beschr": "nicht lesen"}
	got, errs := ToPDH(maps, fields, row, nil)
	if len(errs) != 0 {
		t.Fatalf("Fehler: %v", errs)
	}
	want := map[string]string{"title": "T", "priority": "medium", "status": "open"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestToPDHNoPanic(t *testing.T) {
	got, errs := ToPDH(nil, nil, nil, nil)
	if got == nil || len(errs) != 0 {
		t.Fatalf("got %v %v", got, errs)
	}
	maps := []FieldMap{
		{ForeignColumn: "A", PDHField: "gibtsnicht", Direction: DirIn},
		{ForeignColumn: "Fehlt", PDHField: "description", Direction: DirIn},
	}
	got, errs = ToPDH(maps, Fields(RefTicket), nil, nil)
	want := []RunError{{Field: "gibtsnicht", Message: "Feld ‚gibtsnicht‘ ist unbekannt"}}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("errs %v", errs)
	}
	if v, ok := got["description"]; !ok || v != "" {
		t.Fatalf("fehlende Spalte sollte leer sein: %v", got)
	}
}

func TestToForeignReverse(t *testing.T) {
	fields := Fields(RefTicket)
	due := "2026-03-04T05:06:07Z"
	got, errs := ToForeign(ticketMaps(), fields, map[string]string{
		"title": "T", "priority": "HIGH", "due_date": due,
	})
	if len(errs) != 0 {
		t.Fatalf("Fehler: %v", errs)
	}
	if got["Titel"] != "T" || got["Prio"] != "2" {
		t.Fatalf("got %v", got)
	}
	if tm, ok := got["Faellig"].(time.Time); !ok || !tm.Equal(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)) || tm.Location() != time.UTC {
		t.Fatalf("Zeit: %#v", got["Faellig"])
	}
	// "*" zählt nicht, kleinster Schlüssel gewinnt.
	m := []FieldMap{{ForeignColumn: "P", PDHField: "priority", Direction: DirOut,
		Translate: map[string]string{"B": "low", "A": "low", "*": "low"}}}
	for i := 0; i < 20; i++ {
		got, _ = ToForeign(m, fields, map[string]string{"priority": "low"})
		if got["P"] != "A" {
			t.Fatalf("nicht deterministisch: %v", got)
		}
	}
}

func TestToForeignEmptyTimeAndErrors(t *testing.T) {
	fields := Fields(RefTicket)
	got, errs := ToForeign(ticketMaps(), fields, map[string]string{"due_date": ""})
	if len(errs) != 0 {
		t.Fatalf("Fehler: %v", errs)
	}
	if v, ok := got["Faellig"]; !ok || v != nil {
		t.Fatalf("leere Zeit sollte nil sein: %#v", got)
	}
	_, errs = ToForeign(ticketMaps(), fields, map[string]string{"priority": "low"})
	// "low" hat nur über "*" ein Gegenstück, das nicht zählt.
	want := []RunError{{Field: "priority", Message: "Wert ‚low‘ für Feld ‚Priorität‘ ist nicht zugeordnet"}}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("got %v", errs)
	}
	_, errs = ToForeign(ticketMaps(), fields, map[string]string{"due_date": "morgen"})
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "kein gültiges Datum") {
		t.Fatalf("got %v", errs)
	}
}

func TestToForeignSkips(t *testing.T) {
	fields := Fields(RefTicket)
	maps := []FieldMap{
		{ForeignColumn: "T", PDHField: "title", Direction: DirIn},
		{ForeignColumn: "P", PDHField: "priority", Direction: DirOut, FixedValue: fixedStr("high")},
		{PDHField: "description", Direction: DirOut},
		{ForeignColumn: "S", PDHField: "status", Direction: DirOut}, // nicht in pdhValues
		{ForeignColumn: "X", PDHField: "unbekannt", Direction: DirOut},
	}
	got, errs := ToForeign(maps, fields, map[string]string{
		"title": "t", "priority": "high", "description": "d",
	})
	if len(got) != 0 || len(errs) != 0 {
		t.Fatalf("got %v %v", got, errs)
	}
	got, errs = ToForeign(maps, fields, map[string]string{"unbekannt": "x"})
	if len(got) != 0 || len(errs) != 1 || errs[0].Message != "Feld ‚unbekannt‘ ist unbekannt" {
		t.Fatalf("got %v %v", got, errs)
	}
	got, errs = ToForeign(nil, nil, nil)
	if got == nil || len(errs) != 0 {
		t.Fatalf("nil-Eingaben: %v %v", got, errs)
	}
}

func TestRowMarkStableOrder(t *testing.T) {
	a := RowMark(changeKindNone, nil, map[string]string{"a": "1", "b": "2", "c": "3"})
	for i := 0; i < 20; i++ {
		if b := RowMark(changeKindNone, nil, map[string]string{"c": "3", "b": "2", "a": "1"}); a != b {
			t.Fatalf("instabil: %s %s", a, b)
		}
	}
	if len(a) != 64 {
		t.Fatalf("kein SHA-256: %q", a)
	}
	// Keine Kollision durch Aneinanderhängen.
	x := RowMark(changeKindNone, nil, map[string]string{"a": "bc", "d": ""})
	y := RowMark(changeKindNone, nil, map[string]string{"a": "b", "cd": ""})
	if x == y {
		t.Fatal("Kollision")
	}
	if RowMark(changeKindNone, nil, nil) != RowMark(changeKindNone, nil, map[string]string{}) {
		t.Fatal("nil und leer sollten gleich sein")
	}
}

func TestRowMarkChangeValue(t *testing.T) {
	if got := RowMark(changeKindRowVersion, []byte{0x00, 0x00, 0xAB, 0x01}, nil); got != "0000ab01" {
		t.Fatalf("rowversion: %q", got)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 123456700, time.FixedZone("x", 7200))
	if got := RowMark(changeKindTimestamp, ts, nil); got != "2026-01-02T01:04:05.1234567Z" {
		t.Fatalf("timestamp: %q", got)
	}
	// Fehlender Änderungswert: Prüfwert.
	if got := RowMark(changeKindTimestamp, nil, map[string]string{"a": "1"}); len(got) != 64 {
		t.Fatalf("Rückfall: %q", got)
	}
}

func TestDecideRules(t *testing.T) {
	maps := []FieldMap{
		{ForeignColumn: "T", PDHField: "title", Direction: DirBoth},
		{ForeignColumn: "D", PDHField: "description", Direction: DirIn},
		{ForeignColumn: "R", PDHField: "resolution", Direction: DirOut},
		{ForeignColumn: "F", PDHField: "status", Direction: DirIn, FixedValue: fixedStr("open")},
	}
	// Fremd geändert, PDH unverändert: übernehmen.
	d := Decide(maps,
		map[string]string{"title": "a", "description": "x", "resolution": "r", "status": "open"},
		map[string]string{},
		map[string]string{"title": "b", "description": "x", "resolution": "neu", "status": "closed"},
		map[string]string{"title": "a", "description": "x", "resolution": "r", "status": "open"})
	if !reflect.DeepEqual(d.Apply, map[string]string{"title": "b"}) {
		t.Fatalf("Apply %v", d.Apply)
	}
	if d.KeepPDH == nil || d.ForeignLost == nil || len(d.KeepPDH) != 0 || len(d.ForeignLost) != 0 {
		t.Fatalf("Maps %v %v", d.KeepPDH, d.ForeignLost)
	}
}

func TestDecideEcho(t *testing.T) {
	maps := []FieldMap{{ForeignColumn: "T", PDHField: "title", Direction: DirBoth}}
	// Fremdwert ist das, was PDH zuletzt zurückgeschrieben hat: keine fremde Änderung.
	d := Decide(maps,
		map[string]string{"title": "alt"},
		map[string]string{"title": "pdh"},
		map[string]string{"title": "pdh"},
		map[string]string{"title": "pdh"})
	if len(d.Apply)+len(d.KeepPDH)+len(d.ForeignLost) != 0 {
		t.Fatalf("%+v", d)
	}
	// Fremd leer geworden, nie zurückgeschrieben: echte fremde Änderung.
	d = Decide(maps, map[string]string{"title": "alt"}, nil, map[string]string{}, map[string]string{"title": "alt"})
	if v, ok := d.Apply["title"]; !ok || v != "" {
		t.Fatalf("Leerung ging verloren: %+v", d)
	}
}

func TestDecideConflict(t *testing.T) {
	maps := []FieldMap{{ForeignColumn: "T", PDHField: "title", Direction: DirBoth}}
	d := Decide(maps,
		map[string]string{"title": "alt"}, nil,
		map[string]string{"title": " fremd "}, map[string]string{"title": "pdh"})
	if len(d.Apply) != 0 {
		t.Fatalf("Apply %v", d.Apply)
	}
	if d.KeepPDH["title"] != "pdh" || d.ForeignLost["title"] != "fremd" {
		t.Fatalf("%+v", d)
	}
	// PDH hat zuletzt zurückgeschriebenes geändert, Fremd danach geändert: auch Widerspruch.
	d = Decide(maps,
		map[string]string{"title": "alt"}, map[string]string{"title": "w"},
		map[string]string{"title": "fremd"}, map[string]string{"title": "pdh"})
	if len(d.KeepPDH) != 1 || len(d.ForeignLost) != 1 {
		t.Fatalf("%+v", d)
	}
	// Beide gleich geändert: übernehmen ist unschädlich.
	d = Decide(maps,
		map[string]string{"title": "alt"}, nil,
		map[string]string{"title": "neu"}, map[string]string{"title": "neu"})
	if d.Apply["title"] != "neu" || len(d.KeepPDH) != 0 {
		t.Fatalf("%+v", d)
	}
	// Nur Leerraum geändert: keine Änderung; Groß-/Kleinschreibung dagegen schon.
	d = Decide(maps, map[string]string{"title": "a"}, nil, map[string]string{"title": " a "}, map[string]string{"title": "a"})
	if len(d.Apply) != 0 {
		t.Fatalf("%+v", d)
	}
	d = Decide(maps, map[string]string{"title": "a"}, nil, map[string]string{"title": "A"}, map[string]string{"title": "a"})
	if d.Apply["title"] != "A" {
		t.Fatalf("%+v", d)
	}
}

func TestDecideFirstSync(t *testing.T) {
	maps := []FieldMap{
		{ForeignColumn: "T", PDHField: "title", Direction: DirBoth},
		{ForeignColumn: "D", PDHField: "description", Direction: DirBoth},
	}
	d := Decide(maps, nil, nil,
		map[string]string{"title": "f", "description": "gleich"},
		map[string]string{"title": "p", "description": "gleich"})
	if !reflect.DeepEqual(d.Apply, map[string]string{"title": "f"}) || len(d.KeepPDH) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestDecideKeysAlwaysAligned(t *testing.T) {
	maps := []FieldMap{{ForeignColumn: "T", PDHField: "title", Direction: DirBoth}}
	d := Decide(maps, map[string]string{"title": "a"}, nil, map[string]string{"title": "b"}, map[string]string{"title": "c"})
	if len(d.KeepPDH) != len(d.ForeignLost) {
		t.Fatalf("%+v", d)
	}
	d = Decide(nil, nil, nil, nil, nil)
	if d.Apply == nil || d.KeepPDH == nil || d.ForeignLost == nil {
		t.Fatalf("nil-Maps: %+v", d)
	}
}

// TestDecideTable deckt jeden Zweig von Decide ab. Ein fehlender Schlüssel
// (nil-Map bzw. fehlender Eintrag) ist hier bewusst etwas anderes als ein
// leerer Wert.
func TestDecideTable(t *testing.T) {
	maps := []FieldMap{
		{ForeignColumn: "T", PDHField: "title", Direction: DirBoth},
		{ForeignColumn: "D", PDHField: "description", Direction: DirIn},
	}
	type m = map[string]string
	cases := []struct {
		name                        string
		seen, written, foreign, pdh m
		apply, keepPDH, foreignLost m
	}{
		{name: "Fremd gleich gesehen", seen: m{"title": "a"}, foreign: m{"title": "a"}, pdh: m{"title": "b"}},
		{name: "Echo des eigenen Zurueckschreibens", seen: m{"title": "a"}, written: m{"title": "x"}, foreign: m{"title": "x"}, pdh: m{"title": "x"}},
		{name: "Nur Fremd geaendert", seen: m{"title": "a"}, foreign: m{"title": "b"}, pdh: m{"title": "a"}, apply: m{"title": "b"}},
		{name: "Fremd geaendert nach Zurueckschreiben, PDH noch auf Zurueckgeschriebenem", seen: m{"title": "a"}, written: m{"title": "x"}, foreign: m{"title": "b"}, pdh: m{"title": "x"}, apply: m{"title": "b"}},
		{name: "Nur PDH geaendert", seen: m{"title": "a"}, foreign: m{"title": "a"}, pdh: m{"title": "c"}},
		{name: "PDH geleert, Fremd unveraendert", seen: m{"title": "a"}, foreign: m{"title": "a"}, pdh: m{"title": ""}},
		{name: "PDH geleert, Fremd geaendert: Widerspruch", seen: m{"title": "a"}, foreign: m{"title": "b"}, pdh: m{"title": ""}, keepPDH: m{"title": ""}, foreignLost: m{"title": "b"}},
		{name: "Beide verschieden geaendert", seen: m{"title": "a"}, foreign: m{"title": "b"}, pdh: m{"title": "c"}, keepPDH: m{"title": "c"}, foreignLost: m{"title": "b"}},
		{name: "Beide gleich geaendert", seen: m{"title": "a"}, foreign: m{"title": "b"}, pdh: m{"title": "b"}, apply: m{"title": "b"}},
		{name: "Widerspruch nach Zurueckschreiben", seen: m{"title": "a"}, written: m{"title": "x"}, foreign: m{"title": "b"}, pdh: m{"title": "c"}, keepPDH: m{"title": "c"}, foreignLost: m{"title": "b"}},
		{name: "seen leer vorhanden, Fremd weiter leer", seen: m{"title": ""}, foreign: m{"title": ""}, pdh: m{"title": "x"}},
		{name: "seen leer vorhanden, Fremd gefuellt", seen: m{"title": ""}, foreign: m{"title": "b"}, pdh: m{"title": ""}, apply: m{"title": "b"}},
		{name: "written ohne seen, Fremd geaendert, PDH auf written", written: m{"title": "x"}, foreign: m{"title": "b"}, pdh: m{"title": "x"}, apply: m{"title": "b"}},
		{name: "written ohne seen, Fremd gleich written", written: m{"title": "x"}, foreign: m{"title": "x"}, pdh: m{"title": "x"}},
		{name: "written ohne seen, beide geaendert", written: m{"title": "x"}, foreign: m{"title": "b"}, pdh: m{"title": "c"}, keepPDH: m{"title": "c"}, foreignLost: m{"title": "b"}},
		{name: "written ohne seen: fehlendes seen trifft nicht auf leeres PDH", written: m{"title": "x"}, foreign: m{"title": "b"}, pdh: m{"title": ""}, keepPDH: m{"title": ""}, foreignLost: m{"title": "b"}},
		{name: "written ohne seen: fehlendes seen trifft nicht auf leeren Fremdwert", written: m{"title": "x"}, foreign: m{}, pdh: m{"title": "c"}, keepPDH: m{"title": "c"}, foreignLost: m{"title": ""}},
		{name: "Fremd geleert nach Zurueckschreiben", seen: m{"title": "a"}, written: m{"title": "x"}, foreign: m{}, pdh: m{"title": "x"}, apply: m{"title": ""}},
		{name: "Fremd geleert, nie zurueckgeschrieben", seen: m{"title": "a"}, foreign: m{}, pdh: m{"title": "a"}, apply: m{"title": ""}},
		{name: "Fremd geleert, PDH geleert", seen: m{"title": "a"}, foreign: m{"title": ""}, pdh: m{}, apply: m{"title": ""}},
		{name: "fehlendes written trifft nicht auf PDH leer", seen: m{"title": "a"}, foreign: m{"title": "b"}, pdh: m{}, keepPDH: m{"title": ""}, foreignLost: m{"title": "b"}},
		{name: "Erster Abgleich, gleich", foreign: m{"title": "a"}, pdh: m{"title": "a"}},
		{name: "Erster Abgleich, verschieden", foreign: m{"title": "b"}, pdh: m{"title": "c"}, apply: m{"title": "b"}},
		{name: "Erster Abgleich, Fremd leer", foreign: m{}, pdh: m{"title": "c"}, apply: m{"title": ""}},
		{name: "Leerraum ist keine Aenderung", seen: m{"title": "a"}, foreign: m{"title": " a "}, pdh: m{"title": "a"}},
		{name: "Feld mit seen neben Feld ohne seen", seen: m{"title": "a"}, foreign: m{"title": "b", "description": "d"}, pdh: m{"title": "a", "description": "e"}, apply: m{"title": "b", "description": "d"}},
		{name: "Feld mit seen neben Feld ohne seen, Widerspruch nur am ersten", seen: m{"title": "a"}, foreign: m{"title": "b", "description": "d"}, pdh: m{"title": "c", "description": "d"}, keepPDH: m{"title": "c"}, foreignLost: m{"title": "b"}},
	}
	norm := func(x m) m {
		if x == nil {
			return m{}
		}
		return x
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(maps, c.seen, c.written, c.foreign, c.pdh)
			if !reflect.DeepEqual(d.Apply, norm(c.apply)) {
				t.Errorf("Apply %v, erwartet %v", d.Apply, norm(c.apply))
			}
			if !reflect.DeepEqual(d.KeepPDH, norm(c.keepPDH)) {
				t.Errorf("KeepPDH %v, erwartet %v", d.KeepPDH, norm(c.keepPDH))
			}
			if !reflect.DeepEqual(d.ForeignLost, norm(c.foreignLost)) {
				t.Errorf("ForeignLost %v, erwartet %v", d.ForeignLost, norm(c.foreignLost))
			}
		})
	}
}
