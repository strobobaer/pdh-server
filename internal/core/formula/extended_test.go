package formula

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func extGet(k string) (float64, bool) {
	v, ok := map[string]float64{"A": 30, "B": 20, "Z": 0}[k]
	return v, ok
}

func TestExtendedEval(t *testing.T) {
	for src, want := range map[string]float64{
		"A + B": 50, "[A] + B": 50,
		"A > B": 1, "A <= B": 0, "A = 30": 1, "A <> 30": 0, "A ≥ 30": 1, "A + 1 > B * 2": 0,
		"2 ^ 3": 8, "2 ^ 3 ^ 2": 512, "-2 ^ 2": -4, "2 ^ -1": 0.5, "2 * 3 ^ 2": 18,
		"MIN(A; B; 25)": 20, "max(A; B)": 30, "SUMME(A; B; 1,5)": 51.5, "MITTELWERT(A; B)": 25, "avg(A; B)": 25,
		"BETRAG(B - A)": 10, "RUNDEN(A / 7; 2)": 4.29, "RUNDEN(2,5)": 3, "RUNDEN(-2,5)": -3, "WURZEL(B + 5)": 5,
		"WENN(Z > 0; A / Z; -1)": -1, "WENN(A; 1; X)": 1, "UND(A > B; B > 0)": 1, "ODER(Z; 0)": 0, "NICHT(Z)": 1,
		"MIN(1,5; 2)": 1.5, "WENN(Z; X; 2)": 2,
		"sum(A; B)": 50, "abs(0 - A)": 30, "round(2,4)": 2, "sqrt(A - 5)": 5, "if(A > B; 1; 0)": 1, "and(1; 0)": 0, "or(1; 0)": 1, "not(1)": 0, "average(A; B)": 25,
		"A ≤ 30": 1, "A ≠ 30": 0, "A < B": 0, "A >= 31": 0, "(A > B) + (B > A)": 1,
	} {
		f, err := Parse(src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if got, why := f.EvalWhy(extGet); why != nil || math.Abs(got-want) > 1e-9 {
			t.Errorf("%q = %v (%+v), erwartet %v", src, got, why, want)
		}
		if got, ok := f.Eval(extGet); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("Eval %q = %v (%v)", src, got, ok)
		}
	}
}

func TestExtendedNoValue(t *testing.T) {
	for src, want := range map[string]NoValue{
		"A / Z": {Kind: NoValueDivZero}, "X + 1": {Kind: NoValueRef, Ref: "X"}, "a + B": {Kind: NoValueRef, Ref: "a"},
		"WURZEL(0 - A)": {Kind: NoValueNotFinite}, "WENN(X; 1; 2)": {Kind: NoValueRef, Ref: "X"},
		"MIN(A; X)": {Kind: NoValueRef, Ref: "X"}, "(0 - 8) ^ 0,5": {Kind: NoValueNotFinite}, "ODER(1; X)": {Kind: NoValueRef, Ref: "X"},
	} {
		f, err := Parse(src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		_, why := f.EvalWhy(extGet)
		if why == nil || *why != want {
			t.Errorf("%q: %+v, erwartet %+v", src, why, want)
		}
		if _, ok := f.Eval(extGet); ok {
			t.Errorf("Eval %q: Wert trotz Grund", src)
		}
	}
}

func TestExtendedSyntax(t *testing.T) {
	for _, src := range []string{"A < B < C", "FOO(1)", "WENN(A; 1)", "MIN()", "RUNDEN(A; 7)", "RUNDEN(A; B)", "RUNDEN(A; 1,5)", "RUNDEN(A; 1; 2)",
		"MIN(1, 2)", "A ;", "1 2", "A B", "(A", "A ^", "NICHT(1; 2)", "MIN(A;)", "MIN(A", "A >", "WENN()"} {
		_, err := Parse(src)
		var se *SyntaxError
		if !errors.As(err, &se) || se.Pos < 1 {
			t.Errorf("%q: erwartet *SyntaxError mit Stelle, bekam %v", src, err)
		}
	}
	_, err := Parse("MIN(1, 2)")
	if err == nil || !strings.Contains(err.Error(), "„;“") {
		t.Errorf("Hinweis auf das Trennzeichen fehlt: %v", err)
	}
	_, err = Parse("1 + FOO(2)")
	if err == nil || err.Error() != "Formel, Zeichen 5: unbekannte Funktion „FOO“" {
		t.Errorf("Meldung: %v", err)
	}
	_, err = Parse("wenn(A; 1)")
	if err == nil || err.Error() != "Formel, Zeichen 1: WENN braucht 3 Angaben (Bedingung; dann; sonst)" {
		t.Errorf("Meldung: %v", err)
	}
	_, err = Parse("A < B < C")
	if err == nil || !strings.Contains(err.Error(), "Vergleiche bitte klammern") {
		t.Errorf("Meldung: %v", err)
	}
}

func TestExtendedFlag(t *testing.T) {
	for src, want := range map[string]bool{
		"[A] + [B] * 2": false, "-([A] / 3,5)": false, "12": false,
		"A + [B]": true, "[A] > 1": true, "[A] ^ 2": true, "MIN([A]; [B])": true,
	} {
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if got := f.Extended(); got != want {
			t.Errorf("%q: Extended = %v", src, got)
		}
	}
}

func TestValidIdent(t *testing.T) {
	for _, s := range []string{"A", "B2", "Öl_1", "x", strings.Repeat("a", 20)} {
		if !ValidIdent(s) {
			t.Errorf("%q sollte gelten", s)
		}
	}
	for _, s := range []string{"", "1A", "A-B", "MIN", "wenn", "Avg", strings.Repeat("a", 21), "A B", "_A", "A[1]"} {
		if ValidIdent(s) {
			t.Errorf("%q sollte nicht gelten", s)
		}
	}
	if !IsFunction("mittelwert") || IsFunction("A") {
		t.Error("IsFunction")
	}
}

func TestRefsIncludeAllBranches(t *testing.T) {
	f, err := Parse("WENN(A; B; [C]) + MIN(D; 2 ^ E)")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Refs(); !reflect.DeepEqual(got, []string{"A", "B", "C", "D", "E"}) {
		t.Errorf("Refs: %v", got)
	}
}

func TestFunctionsList(t *testing.T) {
	var names []string
	for _, fi := range Functions() {
		names = append(names, fi.Name)
		if !IsFunction(fi.Name) || !strings.HasPrefix(fi.Insert, fi.Name+"(") || fi.Help == "" {
			t.Errorf("%+v", fi)
		}
	}
	want := []string{"MIN", "MAX", "SUMME", "MITTELWERT", "BETRAG", "RUNDEN", "WURZEL", "WENN", "UND", "ODER", "NICHT"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Namen: %v", names)
	}
}
