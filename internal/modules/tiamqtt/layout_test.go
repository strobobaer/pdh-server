package tiamqtt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadDaten(t *testing.T) *DBSource {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "Daten.db"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := ParseDBSource(src)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func leafByPath(l *Layout, path string) *Leaf {
	for i := range l.Leaves {
		if l.Leaves[i].Path == path {
			return &l.Leaves[i]
		}
	}
	return nil
}

// manyReals baut einen Datenbaustein mit n Real-Werten.
func manyReals(n int) *DBSource {
	db := &DBSource{Name: "Viele"}
	for i := 0; i < n; i++ {
		db.Vars = append(db.Vars, &Var{Name: fmt.Sprintf("Wert%d", i), Kind: KindReal})
	}
	return db
}

func TestPlanPaths(t *testing.T) {
	l, err := Plan(loadDaten(t), FamilyS71500, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"Feld[1]":        "values.Feld[0]",
		"Feld[10]":       "values.Feld[9]",
		"Motor.Ein":      "values.Motor.Ein",
		"Motor.Drehzahl": "values.Motor.Drehzahl",
		"Takt":           "values.Takt",
		"Zeitstempel":    "values.Zeitstempel",
	}
	for path, want := range cases {
		lf := leafByPath(l, path)
		if lf == nil {
			t.Errorf("Wert %s fehlt", path)
			continue
		}
		if lf.JSONPath != want {
			t.Errorf("%s: JSONPath = %q, erwartet %q", path, lf.JSONPath, want)
		}
	}
	if lf := leafByPath(l, "Text"); lf == nil || lf.Kind != KindString || lf.StrLen != 20 || lf.Width != 42 {
		t.Errorf("Text: %+v", lf)
	}
	// 1 Zaehler + 1 Takt + 2 Motor + 10 Feld + 10 weitere
	if len(l.Leaves) != 24 {
		t.Errorf("%d Werte, erwartet 24", len(l.Leaves))
	}
	if len(l.Parts) != 1 || l.Limit != DefaultLimit {
		t.Errorf("Teile %d, Grenze %d", len(l.Parts), l.Limit)
	}
	if l.Leaves[0].Path != "Zaehler" || l.Leaves[1].Path != "Takt" {
		t.Errorf("Reihenfolge nicht wie in der Quelle: %s, %s", l.Leaves[0].Path, l.Leaves[1].Path)
	}
}

func TestValueWidths(t *testing.T) {
	cases := []struct {
		k    Kind
		n    int
		want int
	}{
		{KindBool, 0, 5}, {KindInt, 0, 6}, {KindDInt, 0, 11}, {KindUInt, 0, 5},
		{KindUDInt, 0, 10}, {KindWord, 0, 5}, {KindDWord, 0, 10}, {KindReal, 0, 15},
		{KindLReal, 0, 25}, {KindString, 20, 42}, {KindString, 254, 510},
		{KindDTL, 0, 21}, {KindDT, 0, 21},
	}
	for _, c := range cases {
		if got := ValueWidth(c.k, c.n); got != c.want {
			t.Errorf("ValueWidth(%d, %d) = %d, erwartet %d", c.k, c.n, got, c.want)
		}
	}
}

func TestPlanSplitsAtLimit(t *testing.T) {
	l, err := Plan(manyReals(300), FamilyS71500, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Parts) < 2 {
		t.Fatalf("nur %d Teil(e)", len(l.Parts))
	}
	seen := map[string]int{}
	for i, p := range l.Parts {
		if p.No != i+1 {
			t.Errorf("Teil %d hat Nummer %d", i+1, p.No)
		}
		if p.MaxBytes > 4096 {
			t.Errorf("Teil %d: MaxBytes %d > 4096", p.No, p.MaxBytes)
		}
		for _, lf := range p.Leaves {
			seen[lf.Path]++
		}
	}
	for i := 0; i < 300; i++ {
		if n := seen[fmt.Sprintf("Wert%d", i)]; n != 1 {
			t.Errorf("Wert%d kommt %d-mal vor", i, n)
		}
	}
	if len(seen) != 300 || len(l.Leaves) != 300 {
		t.Errorf("%d verschiedene Werte, %d gesamt", len(seen), len(l.Leaves))
	}
}

func TestPlanStructSplitAcrossParts(t *testing.T) {
	st := &Var{Name: "Motor", Kind: KindStruct}
	for i := 0; i < 200; i++ {
		st.Members = append(st.Members, &Var{Name: fmt.Sprintf("W%d", i), Kind: KindLReal})
	}
	l, err := Plan(&DBSource{Vars: []*Var{st}}, FamilyS71500, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Parts) < 2 {
		t.Fatalf("nur %d Teil(e)", len(l.Parts))
	}
	for _, p := range l.Parts {
		if first := p.Ops[0]; first.Kind != OpRaw || !strings.HasPrefix(first.Raw, `"Motor":{`) {
			t.Errorf("Teil %d öffnet die Struktur nicht: %+v", p.No, first)
		}
	}
}

func TestPlanArrayStaysWhole(t *testing.T) {
	db := manyReals(40)
	arr := &Var{Name: "Feld", Kind: KindArray, Lo: 1, Hi: 50, Elem: &Var{Kind: KindLReal}}
	db.Vars = append(db.Vars[:20], append([]*Var{arr}, db.Vars[20:]...)...)
	l, err := Plan(db, FamilyS71500, 2048)
	if err != nil {
		t.Fatal(err)
	}
	where := map[int]int{}
	for _, p := range l.Parts {
		for _, lf := range p.Leaves {
			if strings.HasPrefix(lf.Path, "Feld[") {
				where[p.No]++
			}
		}
	}
	if len(where) != 1 {
		t.Fatalf("Feld verteilt auf Teile %v", where)
	}
	for _, n := range where {
		if n != 50 {
			t.Errorf("%d Feldwerte, erwartet 50", n)
		}
	}
}

func TestPlanTooBigSkipped(t *testing.T) {
	db := &DBSource{Vars: []*Var{
		{Name: "Klein", Kind: KindInt},
		{Name: "Lang", Kind: KindString, StrLen: 2000},
		{Name: "Feld", Kind: KindArray, Lo: 0, Hi: 99, Elem: &Var{Kind: KindLReal}},
	}}
	l, err := Plan(db, FamilyS71500, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Leaves) != 1 || l.Leaves[0].Path != "Klein" {
		t.Errorf("Werte: %+v", l.Leaves)
	}
	want := map[string]bool{"Lang": false, "Feld": false}
	for _, s := range l.Skipped {
		if _, ok := want[s.Path]; ok && s.Reason == "zu groß für eine Nachricht" {
			want[s.Path] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("%s nicht als „zu groß“ übersprungen: %+v", p, l.Skipped)
		}
	}
}

func TestPlanS71200SkipsDT(t *testing.T) {
	db := loadDaten(t)
	before := len(db.Skipped)
	l, err := Plan(db, FamilyS71200, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if leafByPath(l, "Zeitstempel") != nil {
		t.Error("DT auf S7-1200 übernommen")
	}
	if leafByPath(l, "Zeit") == nil {
		t.Error("DTL fehlt")
	}
	found := false
	for _, s := range l.Skipped {
		if s.Path == "Zeitstempel" && s.Reason == "Date_And_Time gibt es auf S7-1200 nicht – DTL verwenden" && s.Line > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("Zeitstempel nicht übersprungen: %+v", l.Skipped)
	}
	if len(db.Skipped) != before {
		t.Error("Plan verändert DBSource.Skipped")
	}
	l2, err := Plan(db, FamilyS71500, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if leafByPath(l2, "Zeitstempel") == nil {
		t.Error("DT auf S7-1500 fehlt")
	}
}

func TestPlanKeepsParserSkipped(t *testing.T) {
	db := &DBSource{
		Vars:    []*Var{{Name: "A", Kind: KindBool}},
		Skipped: []Skipped{{Path: "Leer", Line: 3, Reason: "Struktur ohne unterstützte Variablen"}},
	}
	l, err := Plan(db, FamilyS71500, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Skipped) != 1 || l.Skipped[0].Path != "Leer" {
		t.Errorf("Skipped = %+v", l.Skipped)
	}
}

func TestPlanLimits(t *testing.T) {
	const tooMany = "Zu viele Werte: höchstens 50 Nachrichten bzw. 2000 Werte"
	if _, err := Plan(manyReals(2001), FamilyS71500, MaxLimit); err == nil || err.Error() != tooMany {
		t.Errorf("2001 Werte: %v", err)
	}
	if _, err := Plan(manyReals(2000), FamilyS71500, MaxLimit); err != nil {
		t.Errorf("2000 Werte: %v", err)
	}
	// 1900 LReal bei 1024 Byte brauchen mehr als 50 Nachrichten.
	db := &DBSource{}
	for i := 0; i < 1900; i++ {
		db.Vars = append(db.Vars, &Var{Name: fmt.Sprintf("W%d", i), Kind: KindLReal})
	}
	if _, err := Plan(db, FamilyS71500, MinLimit); err == nil || err.Error() != tooMany {
		t.Errorf("über 50 Teile: %v", err)
	}
	if _, err := Plan(&DBSource{}, FamilyS71500, DefaultLimit); err == nil || err.Error() != "Keine sendbaren Variablen gefunden" {
		t.Errorf("0 Werte: %v", err)
	}
	onlyBig := &DBSource{Vars: []*Var{{Name: "Lang", Kind: KindString, StrLen: 2000}}}
	if _, err := Plan(onlyBig, FamilyS71500, MinLimit); err == nil || err.Error() != "Keine sendbaren Variablen gefunden" {
		t.Errorf("nur zu große Werte: %v", err)
	}
	for _, lim := range []int{512, 1023, 16385} {
		if _, err := Plan(manyReals(3), FamilyS71500, lim); err == nil {
			t.Errorf("Grenze %d ohne Fehler", lim)
		}
	}
	for _, lim := range []int{MinLimit, MaxLimit} {
		if _, err := Plan(manyReals(3), FamilyS71500, lim); err != nil {
			t.Errorf("Grenze %d: %v", lim, err)
		}
	}
	if _, err := Plan(manyReals(3), Family("s7-300"), DefaultLimit); err == nil {
		t.Error("unbekannte Steuerung ohne Fehler")
	}
}

func TestHeaderWidth(t *testing.T) {
	// {"ts":"2026-11-15T10:01:00Z","part":1,"parts":1,"values":{ … }}
	if got, want := HeaderWidth(1), len(`{"ts":"2026-11-15T10:01:00Z","part":1,"parts":1,"values":{}}`); got != want {
		t.Errorf("HeaderWidth(1) = %d, erwartet %d", got, want)
	}
	if HeaderWidth(10) != HeaderWidth(9)+2 {
		t.Error("zweistellige Teilezahl nicht eingerechnet")
	}
}
