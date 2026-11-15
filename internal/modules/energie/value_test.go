package energie

import "testing"

func TestParseValue(t *testing.T) {
	ok := map[string]float64{"12,5": 12.5, "12.5": 12.5, "-3": -3, " 7 ": 7, "0": 0}
	for in, want := range ok {
		got, good := ParseValue(in)
		if !good || got != want {
			t.Errorf("ParseValue(%q) = %v, %v; erwartet %v", in, got, good, want)
		}
	}
	for _, in := range []string{"", "n/a", "1.234,5", "12,5 kWh", "1,2,3", "NaN", "1e3"} {
		if v, good := ParseValue(in); good {
			t.Errorf("ParseValue(%q) = %v, true; erwartet false", in, v)
		}
	}
}
