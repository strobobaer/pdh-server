package energie

import (
	"math"
	"testing"
)

// refs baut die Bezugsfunktion für evalMinute aus einer Tabelle.
func refs(m map[string]refValue) func(string) (refValue, bool) {
	return func(k string) (refValue, bool) {
		r, ok := m[k]
		return r, ok
	}
}

func mustFormula(t *testing.T, src string) *Formula {
	t.Helper()
	f, err := ParseFormula(src)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEvalMinuteCounterIncrement(t *testing.T) {
	f := mustFormula(t, "[A]+[B]")
	v, ok, rebase := evalMinute(f, KindCounter, refs(map[string]refValue{
		"A": {Kind: KindCounter, Now: 110, HasNow: true, Base: 100, HasBase: true},
		"B": {Kind: KindCounter, Now: 52, HasNow: true, Base: 50, HasBase: true},
	}), true, 1000)
	if !ok || rebase || math.Abs(v-1012) > 1e-9 {
		t.Errorf("v=%v ok=%v rebase=%v, erwartet 1012 true false", v, ok, rebase)
	}
}

func TestEvalMinuteMissingRef(t *testing.T) {
	f := mustFormula(t, "[A]+[B]")
	for name, b := range map[string]map[string]refValue{
		"ohne Wert": {"A": {Kind: KindCounter, Now: 110, HasNow: true, Base: 100, HasBase: true}, "B": {Kind: KindCounter, Base: 50, HasBase: true}},
		"unbekannt": {"A": {Kind: KindCounter, Now: 110, HasNow: true, Base: 100, HasBase: true}},
	} {
		_, ok, rebase := evalMinute(f, KindCounter, refs(b), true, 1000)
		if ok || rebase {
			t.Errorf("%s: ok=%v rebase=%v, erwartet false false", name, ok, rebase)
		}
	}
}

func TestEvalMinuteCounterReset(t *testing.T) {
	f := mustFormula(t, "[A]")
	for name, a := range map[string]refValue{
		"Rückgang":       {Kind: KindCounter, Now: 5, HasNow: true, Base: 100, HasBase: true},
		"ohne Basiswert": {Kind: KindCounter, Now: 5, HasNow: true},
	} {
		_, ok, rebase := evalMinute(f, KindCounter, refs(map[string]refValue{"A": a}), true, 1000)
		if ok || !rebase {
			t.Errorf("%s: ok=%v rebase=%v, erwartet false true", name, ok, rebase)
		}
	}
}

func TestEvalMinuteNoBase(t *testing.T) {
	f := mustFormula(t, "[A]+[C]")
	v, ok, _ := evalMinute(f, KindCounter, refs(map[string]refValue{
		"A": {Kind: KindCounter, Now: 110, HasNow: true},
		"C": {Kind: KindConsumption, Now: 3, HasNow: true},
	}), false, 0)
	if !ok || math.Abs(v-3) > 1e-9 {
		t.Errorf("v=%v ok=%v, erwartet 3 (Zähler ohne Basis tragen 0 bei)", v, ok)
	}
}

func TestEvalMinuteInstantExtended(t *testing.T) {
	f := mustFormula(t, "MAX([A]; [B])")
	v, ok, _ := evalMinute(f, KindInstant, refs(map[string]refValue{
		"A": {Kind: KindInstant, Now: 3, HasNow: true},
		"B": {Kind: KindInstant, Now: 7, HasNow: true},
	}), true, 99)
	if !ok || math.Abs(v-7) > 1e-9 {
		t.Errorf("v=%v ok=%v, erwartet 7 (Momentanwert ohne Vorwert)", v, ok)
	}
}

func TestEvalMinuteNegativeCounter(t *testing.T) {
	f := mustFormula(t, "[A]-[B]")
	_, ok, rebase := evalMinute(f, KindCounter, refs(map[string]refValue{
		"A": {Kind: KindCounter, Now: 100, HasNow: true, Base: 100, HasBase: true},
		"B": {Kind: KindCounter, Now: 51, HasNow: true, Base: 50, HasBase: true},
	}), true, 1000)
	if ok || rebase {
		t.Errorf("ok=%v rebase=%v, erwartet false false (Zuwachs −1)", ok, rebase)
	}
}
