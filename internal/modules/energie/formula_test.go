package energie

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func vals(m map[string]float64) func(string) (float64, bool) {
	return func(k string) (float64, bool) { v, ok := m[k]; return v, ok }
}

func TestParseFormulaEval(t *testing.T) {
	v := vals(map[string]float64{"G": 100, "A": 30, "B": 20})
	cases := []struct {
		src  string
		want float64
	}{
		{"[G] − [A] − [B]", 50},
		{"([A]+[B]) × 0,5", 25},
		{"-[A] + 2*3", -24},
		{"[A] ÷ 4", 7.5},
		{"2.5 * 2", 5},
	}
	for _, c := range cases {
		f, err := ParseFormula(c.src)
		if err != nil {
			t.Fatalf("%q: %v", c.src, err)
		}
		got, ok := f.Eval(v)
		if !ok || got != c.want {
			t.Errorf("%q = %v (%v), erwartet %v", c.src, got, ok, c.want)
		}
	}
}

func TestParseFormulaErrors(t *testing.T) {
	for _, src := range []string{"", "[A] +", "([A]", "[A]]", "[]", "[A] [B]", "5 % 2", strings.Repeat("1", 2001)} {
		_, err := ParseFormula(src)
		var fe *FormulaError
		if !errors.As(err, &fe) {
			t.Errorf("%q: erwartet *FormulaError, bekam %v", src, err)
			continue
		}
		if fe.Pos < 1 {
			t.Errorf("%q: Pos fehlt", src)
		}
	}
	_, err := ParseFormula("([A]")
	if err == nil || err.Error() != "Formel, Zeichen 5: Klammer fehlt" {
		t.Errorf("Meldung: %v", err)
	}
}

func TestFormulaRefs(t *testing.T) {
	f, err := ParseFormula("[B]+[A]+[B]")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Refs(); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("Refs = %v", got)
	}
}

func TestEvalMissingRef(t *testing.T) {
	f, _ := ParseFormula("[A] + [B]")
	if _, ok := f.Eval(vals(map[string]float64{"A": 1})); ok {
		t.Error("fehlender Bezug muss false liefern")
	}
}

func TestEvalDivZero(t *testing.T) {
	f, _ := ParseFormula("[A] / ([B] - [B])")
	if _, ok := f.Eval(vals(map[string]float64{"A": 1, "B": 2})); ok {
		t.Error("Division durch 0 muss false liefern")
	}
}

func TestCheckFormulasUnknown(t *testing.T) {
	err := CheckFormulas(map[string]string{"V": "[A] + [X]"}, map[string]bool{"A": true, "V": true})
	var ue *UnknownRefError
	if !errors.As(err, &ue) || ue.Key != "X" {
		t.Fatalf("erwartet UnknownRefError X, bekam %v", err)
	}
	if err.Error() != "unbekannte Messstelle [X]" {
		t.Errorf("Meldung: %v", err)
	}
	if err := CheckFormulas(map[string]string{"V": "[A]"}, map[string]bool{"A": true}); err != nil {
		t.Errorf("gültig, bekam %v", err)
	}
}

func TestCheckFormulasCycle(t *testing.T) {
	known := map[string]bool{"A": true, "B": true, "C": true}
	err := CheckFormulas(map[string]string{"A": "[B]", "B": "[C]", "C": "[A]"}, known)
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("erwartet CycleError, bekam %v", err)
	}
	if len(ce.Path) < 3 || ce.Path[0] != ce.Path[len(ce.Path)-1] {
		t.Errorf("Path = %v", ce.Path)
	}
	err = CheckFormulas(map[string]string{"A": "[A] + 1"}, known)
	if !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), "Zirkelbezug: [A] → [A]") {
		t.Errorf("Selbstbezug: %v", err)
	}
}

func TestEvalOrderChain(t *testing.T) {
	got, err := EvalOrder(map[string]string{"C": "[B]+1", "B": "[A]*2"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"B", "C"}) {
		t.Errorf("Reihenfolge = %v", got)
	}
}
