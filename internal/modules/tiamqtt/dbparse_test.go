package tiamqtt

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, src []byte) *DBSource {
	t.Helper()
	db, err := ParseDBSource(src)
	if err != nil {
		t.Fatalf("ParseDBSource: %v", err)
	}
	return db
}

func findVar(vars []*Var, name string) *Var {
	for _, v := range vars {
		if v.Name == name {
			return v
		}
	}
	return nil
}

func wantVar(t *testing.T, vars []*Var, name string, kind Kind) *Var {
	t.Helper()
	v := findVar(vars, name)
	if v == nil {
		t.Fatalf("Variable %q fehlt", name)
	}
	if v.Kind != kind {
		t.Fatalf("%s: Kind = %v, erwartet %v", name, v.Kind, kind)
	}
	return v
}

func TestParseTIAExport(t *testing.T) {
	db := mustParse(t, readTestdata(t, "Daten.db"))
	if db.Name != "Daten" {
		t.Errorf("Name = %q", db.Name)
	}
	if len(db.Skipped) != 0 {
		t.Errorf("unerwartet übersprungen: %+v", db.Skipped)
	}
	z := wantVar(t, db.Vars, "Zaehler", KindReal)
	if z.Line != 9 {
		t.Errorf("Zaehler Zeile = %d, erwartet 9", z.Line)
	}
	takt := wantVar(t, db.Vars, "Takt", KindInt)
	if takt.Init != "5" {
		t.Errorf("Takt Init = %q", takt.Init)
	}
	m := wantVar(t, db.Vars, "Motor", KindStruct)
	if len(m.Members) != 2 {
		t.Fatalf("Motor Members = %d", len(m.Members))
	}
	wantVar(t, m.Members, "Drehzahl", KindReal)
	wantVar(t, m.Members, "Ein", KindBool)
	f := wantVar(t, db.Vars, "Feld", KindArray)
	if f.Lo != 1 || f.Hi != 10 || f.Elem == nil || f.Elem.Kind != KindInt || f.Elem.Name != "" {
		t.Errorf("Feld = %+v elem %+v", f, f.Elem)
	}
	txt := wantVar(t, db.Vars, "Text", KindString)
	if txt.StrLen != 20 || txt.Init != "'abc'" {
		t.Errorf("Text StrLen=%d Init=%q", txt.StrLen, txt.Init)
	}
	if n := wantVar(t, db.Vars, "Name", KindString); n.StrLen != 254 {
		t.Errorf("Name StrLen = %d", n.StrLen)
	}
	wantVar(t, db.Vars, "Zeit", KindDTL)
	if w := wantVar(t, db.Vars, "Wort", KindWord); w.Init != "16#00FF" {
		t.Errorf("Wort Init = %q", w.Init)
	}
	wantVar(t, db.Vars, "Doppelwort", KindDWord)
	if g := wantVar(t, db.Vars, "Ganz", KindDInt); g.Init != "-7" {
		t.Errorf("Ganz Init = %q", g.Init)
	}
	wantVar(t, db.Vars, "Vorzeichenlos", KindUInt)
	wantVar(t, db.Vars, "Gross", KindUDInt)
	if l := wantVar(t, db.Vars, "Genau", KindLReal); l.Init != "1.5E+3" {
		t.Errorf("Genau Init = %q", l.Init)
	}
	wantVar(t, db.Vars, "Zeitstempel", KindDT)
	if len(db.Vars) != 14 {
		t.Errorf("Anzahl Variablen = %d", len(db.Vars))
	}
	if !db.HasRetain {
		t.Error("HasRetain = false")
	}
	if !strings.Contains(db.Begin, "Feld[1] := 3;") || strings.Contains(db.Begin, "END_DATA_BLOCK") {
		t.Errorf("Begin = %q", db.Begin)
	}
}

func TestParseUDTFromSameFile(t *testing.T) {
	db := mustParse(t, readTestdata(t, "MitTyp.db"))
	if db.Name != "Anlage" || db.HasRetain {
		t.Errorf("Name=%q HasRetain=%v", db.Name, db.HasRetain)
	}
	if len(db.Vars) != 3 {
		t.Fatalf("Anzahl Variablen = %d", len(db.Vars))
	}
	for _, name := range []string{"Pumpe", "Luefter"} {
		p := wantVar(t, db.Vars, name, KindStruct)
		if len(p.Members) != 4 {
			t.Fatalf("%s Members = %d", name, len(p.Members))
		}
		wantVar(t, p.Members, "Drehzahl", KindReal)
		wantVar(t, p.Members, "Strom", KindReal)
		wantVar(t, p.Members, "Ein", KindBool)
		if s := wantVar(t, p.Members, "Meldung", KindString); s.StrLen != 40 {
			t.Errorf("Meldung StrLen = %d", s.StrLen)
		}
	}
	// Jede Verwendung bekommt eigene Members.
	if findVar(db.Vars, "Pumpe").Members[0] == findVar(db.Vars, "Luefter").Members[0] {
		t.Error("Members werden zwischen Verwendungen geteilt")
	}
	wantVar(t, db.Vars, "Zustand", KindInt)
}

func TestParseTypedDBFromSameFile(t *testing.T) {
	src := `TYPE "Antrieb"
VERSION : 0.1
   STRUCT
      Drehzahl : Real;
   END_STRUCT;
END_TYPE

DATA_BLOCK "Pumpe1"
{ S7_Optimized_Access := 'TRUE' }
VERSION : 0.1
NON_RETAIN
"Antrieb"

BEGIN
END_DATA_BLOCK
`
	db := mustParse(t, []byte(src))
	wantVar(t, db.Vars, "Drehzahl", KindReal)
}

func TestParseEncodings(t *testing.T) {
	base := readTestdata(t, "Daten.db")
	ref := mustParse(t, base)

	bom := append([]byte{0xEF, 0xBB, 0xBF}, base...)
	crlf := bytes.ReplaceAll(bytes.ReplaceAll(base, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	both := append([]byte{0xEF, 0xBB, 0xBF}, crlf...)
	for name, src := range map[string][]byte{"BOM": bom, "CRLF": crlf, "BOM+CRLF": both} {
		db := mustParse(t, src)
		if db.Name != ref.Name || len(db.Vars) != len(ref.Vars) {
			t.Errorf("%s: Name=%q Vars=%d", name, db.Name, len(db.Vars))
			continue
		}
		for i := range db.Vars {
			if db.Vars[i].Name != ref.Vars[i].Name || db.Vars[i].Line != ref.Vars[i].Line {
				t.Errorf("%s: Variable %d = %q Zeile %d", name, i, db.Vars[i].Name, db.Vars[i].Line)
			}
		}
		if strings.Contains(db.Begin, "\r") {
			t.Errorf("%s: Begin enthält \\r", name)
		}
	}

	cp := readTestdata(t, "Daten_cp1252.db")
	if !bytes.Contains(cp, []byte{'Z', 0xE4, 'h'}) {
		t.Fatal("Testdatei ist nicht Windows-1252")
	}
	db := mustParse(t, cp)
	if db.Name != "Daten" {
		t.Errorf("Name = %q", db.Name)
	}
	z := wantVar(t, db.Vars, "Zähler", KindReal)
	if z.Line != 6 {
		t.Errorf("Zähler Zeile = %d", z.Line)
	}
	if txt := wantVar(t, db.Vars, "Text", KindString); txt.Init != "'Grüße'" {
		t.Errorf("Text Init = %q", txt.Init)
	}
}

func TestParseSkipped(t *testing.T) {
	src := `DATA_BLOCK "Daten"
{ S7_Optimized_Access := 'TRUE' }
VERSION : 0.1
NON_RETAIN
   VAR
      Gut : Int;
      Zeichen : Char;
      Breit : WString[20];
      Matrix : Array[1..2, 1..3] of Int;
      Liste : Array[1..2] of Struct
         a : Int;
      END_STRUCT;
      Fehlt : "Unbekannt";
      "a.b" : Real;
      Motor : Struct
         Drehzahl : Real;
         Zeichen : Char;
      END_STRUCT;
      Leer : Struct
         Lang : LInt;
      END_STRUCT;
      Gross : Array[0..1000] of Int;
      Dauer : Time := T#5s;
      Auch : Bool;
      Texte : Array[0..2] of String[8];
      Antriebe : Array[1..2] of "Unbekannt";
      Lang : String[300];
   END_VAR

BEGIN
END_DATA_BLOCK
`
	db := mustParse(t, []byte(src))
	want := []Skipped{
		{"Zeichen", 7, "Typ Char wird nicht unterstützt"},
		{"Breit", 8, "Typ WString wird nicht unterstützt"},
		{"Matrix", 9, "Mehrdimensionale Felder werden nicht unterstützt"},
		{"Liste", 10, "Feld von Strukturen wird nicht unterstützt"},
		{"Fehlt", 13, `PLC-Datentyp "Unbekannt" fehlt – Quelle mit abhängigen Bausteinen exportieren`},
		{`"a.b"`, 14, "Name enthält Zeichen, die im JSON-Pfad nicht gehen"},
		{"Motor.Zeichen", 17, "Typ Char wird nicht unterstützt"},
		{"Leer.Lang", 20, "Typ LInt wird nicht unterstützt"},
		{"Leer", 19, "Struktur ohne unterstützte Variablen"},
		{"Gross", 22, "Feld mit mehr als 1000 Elementen wird nicht unterstützt"},
		{"Dauer", 23, "Typ Time wird nicht unterstützt"},
		{"Antriebe", 26, `PLC-Datentyp "Unbekannt" fehlt – Quelle mit abhängigen Bausteinen exportieren`},
		{"Lang", 27, "Länge der Zeichenkette muss eine Zahl von 1 bis 254 sein"},
	}
	if len(db.Skipped) != len(want) {
		t.Fatalf("Skipped = %+v", db.Skipped)
	}
	for i, w := range want {
		if db.Skipped[i] != w {
			t.Errorf("Skipped[%d] = %+v, erwartet %+v", i, db.Skipped[i], w)
		}
	}
	if len(db.Vars) != 4 {
		t.Fatalf("Vars = %d", len(db.Vars))
	}
	if tx := wantVar(t, db.Vars, "Texte", KindArray); tx.Lo != 0 || tx.Hi != 2 || tx.Elem.Kind != KindString || tx.Elem.StrLen != 8 {
		t.Errorf("Texte = %+v elem %+v", tx, tx.Elem)
	}
	wantVar(t, db.Vars, "Gut", KindInt)
	wantVar(t, db.Vars, "Auch", KindBool)
	m := wantVar(t, db.Vars, "Motor", KindStruct)
	if len(m.Members) != 1 || m.Members[0].Name != "Drehzahl" {
		t.Errorf("Motor Members = %+v", m.Members)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, src string
		line      int
		msg       string
	}{
		{"leer", "", 0, "Die Datei enthält keinen Datenbaustein"},
		{"nur FB", "FUNCTION_BLOCK \"Motor\"\nVERSION : 0.1\n   VAR_INPUT\n      ein : Bool;\n   END_VAR\n\nBEGIN\n   #ein := TRUE;\nEND_FUNCTION_BLOCK\n", 0, "Die Datei enthält keinen Datenbaustein"},
		{"zwei DBs", "DATA_BLOCK \"A\"\n   VAR\n      x : Int;\n   END_VAR\nBEGIN\nEND_DATA_BLOCK\n\nDATA_BLOCK \"B\"\n   VAR\n      y : Int;\n   END_VAR\nBEGIN\nEND_DATA_BLOCK\n", 8, "Bitte nur einen Datenbaustein exportieren"},
		{"Instanz-DB", "DATA_BLOCK \"Motor_DB\"\n{ S7_Optimized_Access := 'TRUE' }\nVERSION : 0.1\nNON_RETAIN\n\"Motor\"\n\nBEGIN\nEND_DATA_BLOCK\n", 5, "Instanz- oder Typ-Datenbaustein ohne eigene Variablen – bitte einen Global-DB mit Variablenliste exportieren"},
		{"Semikolon fehlt", "DATA_BLOCK \"Daten\"\n   VAR\n      Zaehler : Real\n      Takt : Int;\n   END_VAR\nBEGIN\nEND_DATA_BLOCK\n", 3, "„;“ erwartet"},
		{"END_VAR fehlt", "DATA_BLOCK \"Daten\"\n   VAR\n      Zaehler : Real;\n", 3, "„END_VAR“ erwartet"},
		{"Kommentar offen", "DATA_BLOCK \"Daten\"\n(* offen\n   VAR\n", 2, "Kommentar „(*“ wird nicht geschlossen"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseDBSource([]byte(c.src))
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Fehler = %v, erwartet *ParseError", err)
			}
			if pe.Line != c.line || pe.Msg != c.msg {
				t.Errorf("Fehler = Zeile %d %q, erwartet Zeile %d %q", pe.Line, pe.Msg, c.line, c.msg)
			}
		})
	}
}

func TestParseErrorText(t *testing.T) {
	e := &ParseError{Line: 12, Msg: "„;“ erwartet"}
	if e.Error() != "Zeile 12: „;“ erwartet" {
		t.Errorf("Error() = %q", e.Error())
	}
	if (&ParseError{Msg: "x"}).Error() != "x" {
		t.Error("ohne Zeile")
	}
}

func TestParseLimits(t *testing.T) {
	big := make([]byte, 1<<20+1)
	for i := range big {
		big[i] = ' '
	}
	if _, err := ParseDBSource(big); err == nil {
		t.Error("Datei über 1 MiB angenommen")
	}

	var b strings.Builder
	b.WriteString("DATA_BLOCK \"Tief\"\nVAR\n")
	for i := 0; i < 9; i++ {
		b.WriteString("s : Struct\n")
	}
	b.WriteString("x : Int;\n")
	for i := 0; i < 9; i++ {
		b.WriteString("END_STRUCT;\n")
	}
	b.WriteString("END_VAR\nBEGIN\nEND_DATA_BLOCK\n")
	var pe *ParseError
	if _, err := ParseDBSource([]byte(b.String())); !errors.As(err, &pe) {
		t.Errorf("Verschachtelung 9: %v", err)
	}

	// 8 Ebenen gehen noch.
	b.Reset()
	b.WriteString("DATA_BLOCK \"Tief\"\nVAR\n")
	for i := 0; i < 7; i++ {
		b.WriteString("s : Struct\n")
	}
	b.WriteString("x : Int;\n")
	for i := 0; i < 7; i++ {
		b.WriteString("END_STRUCT;\n")
	}
	b.WriteString("END_VAR\nBEGIN\nEND_DATA_BLOCK\n")
	if _, err := ParseDBSource([]byte(b.String())); err != nil {
		t.Errorf("Verschachtelung 8: %v", err)
	}

	// Sich selbst enthaltender Typ darf nicht endlos laufen.
	loop := "TYPE \"A\"\nSTRUCT\n  x : \"A\";\nEND_STRUCT;\nEND_TYPE\nDATA_BLOCK \"D\"\nVAR\n  a : \"A\";\nEND_VAR\nBEGIN\nEND_DATA_BLOCK\n"
	if _, err := ParseDBSource([]byte(loop)); !errors.As(err, &pe) {
		t.Errorf("Typ-Schleife: %v", err)
	}
}
