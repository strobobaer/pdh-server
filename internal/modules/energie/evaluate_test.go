package energie

import (
	"math"
	"testing"
	"time"
)

var evEnd = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// evSeries liefert Werte für die Minutenenden end-from .. end.
func evSeries(from int, f func(i int) float64) []MinuteValue {
	var out []MinuteValue
	for i := from; i >= 0; i-- {
		out = append(out, MinuteValue{TS: evEnd.Add(-time.Duration(i) * time.Minute), Value: f(i)})
	}
	return out
}

func TestEvaluateInstantMean(t *testing.T) {
	v, ok := WindowValue(KindInstant, evSeries(2, func(i int) float64 { return float64(10 * (i + 1)) }), evEnd, 3)
	if !ok || v != 20 {
		t.Errorf("Mittelwert = %v/%v, erwartet 20", v, ok)
	}
}

func TestEvaluateCounterDelta(t *testing.T) {
	v, ok := WindowValue(KindCounter, evSeries(3, func(i int) float64 { return 100 - float64(i)*2 }), evEnd, 3)
	if !ok || v != 6 {
		t.Errorf("Differenz = %v/%v, erwartet 6", v, ok)
	}
	// ohne Startwert bei end-window kein Ergebnis
	if _, ok := WindowValue(KindCounter, evSeries(2, func(i int) float64 { return 1 }), evEnd, 3); ok {
		t.Error("Zähler ohne Startwert darf nicht bewertet werden")
	}
}

func TestEvaluateConsumptionSum(t *testing.T) {
	v, ok := WindowValue(KindConsumption, evSeries(2, func(i int) float64 { return 1.5 }), evEnd, 3)
	if !ok || v != 4.5 {
		t.Errorf("Summe = %v/%v, erwartet 4,5", v, ok)
	}
}

func TestEvaluateGapNotRated(t *testing.T) {
	vals := evSeries(4, func(i int) float64 { return 1 })
	gap := append([]MinuteValue{}, vals[:2]...)
	gap = append(gap, vals[3:]...) // eine Minute fehlt
	for _, k := range []string{KindInstant, KindConsumption, KindCounter} {
		if _, ok := WindowValue(k, gap, evEnd, 4); ok {
			t.Errorf("%s: Lücke wurde bewertet", k)
		}
	}
	if _, ok := WindowValue(KindInstant, nil, evEnd, 1); ok {
		t.Error("leere Reihe wurde bewertet")
	}
}

func TestEvaluateCounterNegativeNotRated(t *testing.T) {
	vals := evSeries(2, func(i int) float64 { return float64(i) * 10 }) // fällt zum Ende hin
	if _, ok := WindowValue(KindCounter, vals, evEnd, 2); ok {
		t.Error("negative Zählermenge wurde bewertet")
	}
}

func TestBreachedEquality(t *testing.T) {
	cases := []struct {
		cond     string
		v, limit float64
		want     bool
	}{
		{CondAbove, 10, 10, false}, {CondAbove, 10.1, 10, true}, {CondAbove, 9, 10, false},
		{CondBelow, 10, 10, false}, {CondBelow, 9.9, 10, true}, {CondBelow, 11, 10, false},
		{"x", 1, 0, false},
	}
	for _, c := range cases {
		if got := Breached(c.cond, c.v, c.limit); got != c.want {
			t.Errorf("Breached(%s,%v,%v) = %v", c.cond, c.v, c.limit, got)
		}
	}
}

func TestEvaluateEdgeCases(t *testing.T) {
	// Zähler, Zeitraum 1: Werte bei end und end-1
	v, ok := WindowValue(KindCounter, evSeries(1, func(i int) float64 { return 50 - float64(i)*3 }), evEnd, 1)
	if !ok || v != 3 {
		t.Errorf("Zähler window=1 = %v/%v, erwartet 3", v, ok)
	}
	if _, ok := WindowValue(KindCounter, evSeries(0, func(i int) float64 { return 1 }), evEnd, 1); ok {
		t.Error("Zähler window=1 ohne Startwert bewertet")
	}
	// Differenz 0 ist gültig
	if v, ok := WindowValue(KindCounter, evSeries(3, func(i int) float64 { return 7 }), evEnd, 3); !ok || v != 0 {
		t.Errorf("Differenz 0 = %v/%v", v, ok)
	}
	// Lücke genau am Zeitraumanfang (Minute end-window+1)
	for _, k := range []string{KindInstant, KindConsumption} {
		var vals []MinuteValue
		for _, m := range evSeries(3, func(i int) float64 { return 1 }) {
			if !m.TS.Equal(evEnd.Add(-2 * time.Minute)) {
				vals = append(vals, m)
			}
		}
		if _, ok := WindowValue(k, vals, evEnd, 3); ok {
			t.Errorf("%s: Lücke am Zeitraumanfang bewertet", k)
		}
	}
	// NaN/Inf
	if _, ok := WindowValue(KindInstant, evSeries(1, func(i int) float64 { return math.NaN() }), evEnd, 2); ok {
		t.Error("NaN bewertet")
	}
	if _, ok := WindowValue(KindConsumption, evSeries(1, func(i int) float64 { return math.Inf(1) }), evEnd, 2); ok {
		t.Error("Inf bewertet")
	}
	if Breached(CondAbove, math.NaN(), 1) || Breached(CondBelow, 1, math.NaN()) {
		t.Error("NaN darf keinen Alarm auslösen")
	}
}
