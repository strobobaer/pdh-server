package calc

import "testing"

// Eingangswerte kommen als Text von der Verbindung. Nur eine reine Zahl oder
// ein Wahrheitswort zählt – Text mit Einheit oder leerer Text nie (sonst
// stünde am Ende still eine falsche Zahl).
func TestParseInput(t *testing.T) {
	for in, want := range map[string]float64{
		"12.5": 12.5, "12,5": 12.5, " 7 ": 7, "-3": -3, "+4": 4, "0": 0,
		"TRUE": 1, "true": 1, "Ein": 1, "on": 1, "JA": 1,
		"off": 0, "nein": 0, "False": 0, "aus": 0,
	} {
		if got, ok := ParseInput(in); !ok || got != want {
			t.Errorf("ParseInput(%q) = %v, %v; erwartet %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "12,5 kW", "1.234,5", "abc", "1e3", "NaN", "Inf", "12,", ",5", "1 2", "wahr"} {
		if v, ok := ParseInput(in); ok {
			t.Errorf("ParseInput(%q) = %v, true; erwartet false", in, v)
		}
	}
}

func TestFormatValue(t *testing.T) {
	cases := []struct {
		v        float64
		decimals int
		want     string
	}{
		{37.649, 1, "37.6"}, {2.5, 0, "3"}, {-2.5, 0, "-3"}, {-0.001, 2, "0.00"}, {1e6, 0, "1000000"},
		{24.3, 2, "24.30"}, {1.0 / 3, 6, "0.333333"}, {0, 0, "0"}, {-0.4, 0, "0"},
	}
	for _, c := range cases {
		if got := FormatValue(c.v, c.decimals); got != c.want {
			t.Errorf("FormatValue(%v, %d) = %q, erwartet %q", c.v, c.decimals, got, c.want)
		}
	}
}
