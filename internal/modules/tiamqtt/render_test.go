package tiamqtt

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var testTS = time.Date(2026, 11, 15, 10, 1, 0, 0, time.UTC)

// readPath liest wie jsonAtPath in PDH (Felder mit Index ab 0).
var testIndexRe = regexp.MustCompile(`^(.*)\[(\d+)\]$`)

func readPath(data any, path string) (any, bool) {
	cur := data
	for _, part := range strings.Split(path, ".") {
		var idx []int
		for {
			m := testIndexRe.FindStringSubmatch(part)
			if m == nil {
				break
			}
			n, _ := strconv.Atoi(m[2])
			idx = append([]int{n}, idx...)
			part = m[1]
		}
		if part != "" {
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = obj[part]; !ok {
				return nil, false
			}
		}
		for _, n := range idx {
			arr, ok := cur.([]any)
			if !ok || n >= len(arr) {
				return nil, false
			}
			cur = arr[n]
		}
	}
	return cur, true
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	if !json.Valid(b) {
		t.Fatalf("kein gültiges JSON: %s", b)
	}
	if !utf8.Valid(b) {
		t.Fatalf("kein gültiges UTF-8: %q", b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func singlePart(t *testing.T, vars ...*Var) (*Layout, Part) {
	t.Helper()
	l, err := Plan(&DBSource{Vars: vars}, FamilyS71500, DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Parts) != 1 {
		t.Fatalf("%d Teile", len(l.Parts))
	}
	return l, l.Parts[0]
}

// mixedDB enthält alle Typen, Strukturen, Felder und Umlaute im Namen.
func mixedDB(t *testing.T) *DBSource {
	db := loadDaten(t)
	db.Vars = append(db.Vars,
		&Var{Name: "Zähler", Kind: KindStruct, Members: []*Var{
			{Name: "Stück", Kind: KindUDInt},
			{Name: "Innen", Kind: KindStruct, Members: []*Var{
				{Name: "Wert", Kind: KindLReal},
				{Name: "Texte", Kind: KindArray, Lo: -1, Hi: 2, Elem: &Var{Kind: KindString, StrLen: 10}},
			}},
			{Name: "Ende", Kind: KindBool},
		}},
	)
	for i := 0; i < 120; i++ {
		db.Vars = append(db.Vars, &Var{Name: fmt.Sprintf("Text%d", i), Kind: KindString, StrLen: 30})
	}
	return db
}

func TestRenderWorstCaseFits(t *testing.T) {
	for _, lim := range []int{MinLimit, DefaultLimit, MaxLimit} {
		for _, fam := range []Family{FamilyS71500, FamilyS71200} {
			l, err := Plan(mixedDB(t), fam, lim)
			if err != nil {
				t.Fatal(err)
			}
			if lim == MinLimit && len(l.Parts) < 2 {
				t.Errorf("Grenze %d: nur %d Teil", lim, len(l.Parts))
			}
			for _, p := range l.Parts {
				byPath := map[string]Leaf{}
				for _, lf := range p.Leaves {
					byPath[lf.Path] = lf
				}
				b := Render(p, len(l.Parts), testTS, func(path string) any {
					lf, ok := byPath[path]
					if !ok {
						t.Fatalf("Render fragt unbekannten Pfad %q", path)
					}
					return WorstCase(lf)
				})
				if len(b) > p.MaxBytes || p.MaxBytes > lim {
					t.Errorf("Grenze %d, Teil %d: %d Byte, MaxBytes %d", lim, p.No, len(b), p.MaxBytes)
				}
				m := decode(t, b)
				if m["part"] != float64(p.No) || m["parts"] != float64(len(l.Parts)) {
					t.Errorf("part/parts = %v/%v", m["part"], m["parts"])
				}
			}
		}
	}
}

// TestRenderWorstCaseExact prüft, dass die Breite für jeden Typ genau der
// längste Wert ist (MaxBytes ist dann keine grobe Schätzung).
func TestRenderWorstCaseExact(t *testing.T) {
	for _, k := range []Kind{KindBool, KindInt, KindDInt, KindUInt, KindUDInt, KindWord, KindDWord, KindDTL, KindDT} {
		_, p := singlePart(t, &Var{Name: "X", Kind: k})
		b := Render(p, 1, testTS, func(string) any { return WorstCase(p.Leaves[0]) })
		if len(b) != p.MaxBytes {
			t.Errorf("Typ %d: %d Byte, MaxBytes %d: %s", k, len(b), p.MaxBytes, b)
		}
	}
}

func testValue(lf Leaf, i int) any {
	switch lf.Kind {
	case KindBool:
		return i%2 == 0
	case KindInt:
		return int64(-i)
	case KindDInt:
		return int64(-100000 * i)
	case KindUInt, KindWord:
		return int64(i)
	case KindUDInt, KindDWord:
		return int64(4000000000 - i)
	case KindReal:
		return float32(i) * 1.25
	case KindLReal:
		return float64(i) / 3
	case KindString:
		s := fmt.Sprintf("s%d\xe4", i)
		if len(s) > lf.StrLen {
			s = s[:lf.StrLen]
		}
		return s
	case KindDTL, KindDT:
		return testTS.Add(time.Duration(i) * time.Second)
	}
	return nil
}

func TestRenderMatchesPaths(t *testing.T) {
	l, err := Plan(mixedDB(t), FamilyS71500, MinLimit)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	for i, lf := range l.Leaves {
		values[lf.Path] = testValue(lf, i)
	}
	checked := 0
	for _, p := range l.Parts {
		m := decode(t, Render(p, len(l.Parts), testTS, func(path string) any { return values[path] }))
		for _, lf := range p.Leaves {
			got, ok := readPath(m, lf.JSONPath)
			if !ok {
				t.Errorf("Teil %d: %s nicht gefunden", p.No, lf.JSONPath)
				continue
			}
			checked++
			want := values[lf.Path]
			switch v := want.(type) {
			case bool:
				if got != v {
					t.Errorf("%s = %v, erwartet %v", lf.JSONPath, got, v)
				}
			case int64:
				if got != float64(v) {
					t.Errorf("%s = %v, erwartet %v", lf.JSONPath, got, v)
				}
			case float32:
				if f, _ := got.(float64); float32(f) != v {
					t.Errorf("%s = %v, erwartet %v", lf.JSONPath, got, v)
				}
			case float64:
				if f, _ := got.(float64); math.Abs(f-v) > 1e-12 {
					t.Errorf("%s = %v, erwartet %v", lf.JSONPath, got, v)
				}
			case string:
				if w := latin1(v); got != w {
					t.Errorf("%s = %q, erwartet %q", lf.JSONPath, got, w)
				}
			case time.Time:
				if w := v.Format("2006-01-02T15:04:05"); got != w {
					t.Errorf("%s = %v, erwartet %v", lf.JSONPath, got, w)
				}
			}
		}
	}
	if checked != len(l.Leaves) {
		t.Errorf("%d von %d Werten geprüft", checked, len(l.Leaves))
	}
}

func latin1(s string) string {
	r := make([]rune, 0, len(s))
	for i := 0; i < len(s); i++ {
		r = append(r, rune(s[i]))
	}
	return string(r)
}

func TestRenderEscapesStrings(t *testing.T) {
	_, p := singlePart(t, &Var{Name: "Zähler", Kind: KindString, StrLen: 20})
	b := Render(p, 1, testTS, func(string) any { return "a\"b\\c\t\xe4" })
	m := decode(t, b)
	if !strings.Contains(string(b), `"Zähler":"a\"b\\c ä"`) {
		t.Errorf("Ausgabe: %s", b)
	}
	if got := m["values"].(map[string]any)["Zähler"]; got != "a\"b\\c ä" {
		t.Errorf("gelesen: %q", got)
	}
}

func TestRenderRealNaN(t *testing.T) {
	_, p := singlePart(t,
		&Var{Name: "R1", Kind: KindReal}, &Var{Name: "R2", Kind: KindReal},
		&Var{Name: "L1", Kind: KindLReal}, &Var{Name: "L2", Kind: KindLReal},
		&Var{Name: "R3", Kind: KindReal}, &Var{Name: "L3", Kind: KindLReal},
		&Var{Name: "L4", Kind: KindLReal},
	)
	vals := map[string]any{
		"R1": float32(math.NaN()), "R2": float32(math.Inf(1)),
		"L1": math.NaN(), "L2": math.Inf(-1), "R3": float32(1450),
		// würde auf 1.79769313486232E+308 gerundet – größer als jede
		// LReal-Zahl, JSON-Leser weisen die ganze Nachricht ab
		"L3": -math.MaxFloat64, "L4": 1.79769313486231e308,
	}
	b := Render(p, 1, testTS, func(path string) any { return vals[path] })
	m := decode(t, b)
	v := m["values"].(map[string]any)
	for _, k := range []string{"R1", "R2", "L1", "L2", "L3"} {
		if got, ok := v[k]; !ok || got != nil {
			t.Errorf("%s = %v, erwartet null", k, got)
		}
	}
	if !strings.Contains(string(b), `"L4":1.79769313486231E+308`) {
		t.Errorf("größte lesbare LReal fehlt: %s", b)
	}
	if !strings.Contains(string(b), `"R3":1.450000E+03`) {
		t.Errorf("Real-Format: %s", b)
	}
}

func TestRenderTimestamp(t *testing.T) {
	_, p := singlePart(t, &Var{Name: "A", Kind: KindBool})
	local := time.Date(2026, 11, 15, 11, 1, 0, 0, time.FixedZone("MEZ", 3600))
	b := Render(p, 1, local, func(string) any { return true })
	want := `{"ts":"2026-11-15T10:01:00Z","part":1,"parts":1,"values":{"A":true}}`
	if string(b) != want {
		t.Errorf("Render = %s\nerwartet %s", b, want)
	}
}

func TestRenderStructAndArray(t *testing.T) {
	_, p := singlePart(t,
		&Var{Name: "Motor", Kind: KindStruct, Members: []*Var{
			{Name: "Ein", Kind: KindBool},
			{Name: "Feld", Kind: KindArray, Lo: 1, Hi: 3, Elem: &Var{Kind: KindInt}},
		}},
		&Var{Name: "Takt", Kind: KindInt},
	)
	vals := map[string]any{"Motor.Ein": true, "Motor.Feld[1]": int64(1), "Motor.Feld[2]": int64(-2), "Motor.Feld[3]": int64(3), "Takt": int64(5)}
	b := Render(p, 1, testTS, func(path string) any { return vals[path] })
	want := `{"ts":"2026-11-15T10:01:00Z","part":1,"parts":1,"values":{"Motor":{"Ein":true,"Feld":[1,-2,3]},"Takt":5}}`
	if string(b) != want {
		t.Errorf("Render = %s\nerwartet %s", b, want)
	}
	for i := 1; i < len(p.Ops); i++ {
		if p.Ops[i].Kind == OpRaw && p.Ops[i-1].Kind == OpRaw {
			t.Errorf("zwei feste Teile hintereinander bei Op %d", i)
		}
	}
}
