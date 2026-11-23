package web

import "testing"

func TestValidProposalSource(t *testing.T) {
	queries := map[string]bool{"Q1": true}
	columns := map[string]bool{"Strom": true}
	for _, c := range []struct {
		kind, ref string
		ok        bool
	}{
		{"mqtt", "halle/a/temp", true},
		{"mqtt", "halle/a#data.temp", true},
		{"mqtt", "halle/+/temp", false},
		{"mqtt", "halle/#", false},
		{"mqtt", "", false},
		{"modbus", "holding:1:float32", true},
		{"modbus", "holding:x", false},
		{"modbus", "q:Q1:wert", true},
		{"modbus", "q:Q9:wert", false},
		{"opcua", "ns=2;s=Linie1.Temp", true},
		{"opcua", "Linie1.Temp", false},
		{"csv", "Strom", true},
		{"csv", "Fehlt", false},
		{"excel", "q:Q1:Spalte", true},
		{"mysql", "q:Q1:wert", true},
		{"mysql", "tabelle.spalte", false},
		{"rest_api", "data.temp", false},
	} {
		got := validProposalSource(c.kind, c.ref, queries, columns)
		if (got == "") != c.ok {
			t.Errorf("%s %q: %q", c.kind, c.ref, got)
		}
	}
}

func TestParseRowList(t *testing.T) {
	got, err := parseRowList(" 0, 2,2 ,5")
	if err != nil || len(got) != 3 || got[0] != 0 || got[1] != 2 || got[2] != 5 {
		t.Errorf("%v %v", got, err)
	}
	for _, bad := range []string{"", "a", "1,-1", "1,,x"} {
		if _, err := parseRowList(bad); err == nil {
			t.Errorf("%q angenommen", bad)
		}
	}
}
