package web

import "testing"

func TestValidHMITerminalView(t *testing.T) {
	for _, s := range []string{"everyone", "card", "never"} {
		if !validHMITerminalView(s) {
			t.Errorf("%q sollte gültig sein", s)
		}
	}
	for _, s := range []string{"", "all", "Card"} {
		if validHMITerminalView(s) {
			t.Errorf("%q sollte ungültig sein", s)
		}
	}
}

func TestValidHMITerminalControl(t *testing.T) {
	for _, s := range []string{"everyone", "card", "never"} {
		if !validHMITerminalControl(s) {
			t.Errorf("%q sollte gültig sein", s)
		}
	}
	for _, s := range []string{"", "all", "Card"} {
		if validHMITerminalControl(s) {
			t.Errorf("%q sollte ungültig sein", s)
		}
	}
}

func TestEffectiveHMITerminalControl(t *testing.T) {
	cases := []struct {
		allow bool
		in    string
		want  string
	}{
		{false, "everyone", "never"},
		{false, "card", "never"},
		{true, "card", "card"},
		{true, "everyone", "everyone"},
		{true, "never", "never"},
	}
	for _, c := range cases {
		if got := effectiveHMITerminalControl(c.allow, c.in); got != c.want {
			t.Errorf("effective(%v,%q)=%q, erwartet %q", c.allow, c.in, got, c.want)
		}
	}
}
