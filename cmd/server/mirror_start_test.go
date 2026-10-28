package main

import "testing"

func TestSchemaBehind(t *testing.T) {
	cases := []struct {
		name        string
		pending     []string
		initialized bool
		want        bool
	}{
		{"nicht initialisiert", []string{"001.sql"}, false, false},
		{"keine offenen", nil, true, false},
		{"offene", []string{"042_x.sql"}, true, true},
	}
	for _, c := range cases {
		if got := schemaBehind(c.pending, c.initialized); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
