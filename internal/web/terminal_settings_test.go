package web

import "testing"

func TestParseTerminalIdle(t *testing.T) {
	for in, want := range map[string]int{"120": 120, "30": 30, "3600": 3600, " 45 ": 45} {
		got, err := parseTerminalIdle(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-5", "29", "3601", "abc", "1.5"} {
		_, err := parseTerminalIdle(in)
		if err == nil || err.Error() != "Rückkehrzeit muss zwischen 30 und 3600 Sekunden liegen" {
			t.Errorf("%q: Fehler %v", in, err)
		}
	}
}
