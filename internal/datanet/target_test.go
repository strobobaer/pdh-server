package datanet

import "testing"

func TestParseTarget(t *testing.T) {
	ok := map[string]Target{
		"1433":               {1433, "127.0.0.1:1433"},
		"3306=10.0.0.5:3306": {3306, "10.0.0.5:3306"},
	}
	for in, want := range ok {
		got, err := ParseTarget(in)
		if err != nil || got != want {
			t.Errorf("ParseTarget(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "70000", "1433=nohostport", "abc"} {
		if _, err := ParseTarget(in); err == nil {
			t.Errorf("ParseTarget(%q): Fehler erwartet", in)
		}
	}
}

func TestValidateTargets(t *testing.T) {
	if err := ValidateTargets(nil); err == nil {
		t.Error("leer: Fehler erwartet")
	}
	var many []Target
	for i := 1; i <= 17; i++ {
		many = append(many, Target{Port: i, Addr: "127.0.0.1:1"})
	}
	if err := ValidateTargets(many); err == nil {
		t.Error("17 Ziele: Fehler erwartet")
	}
	if err := ValidateTargets(many[:16]); err != nil {
		t.Errorf("16 Ziele: %v", err)
	}
	if err := ValidateTargets([]Target{{1433, "a:1"}, {1433, "b:1"}}); err == nil {
		t.Error("doppelter Port: Fehler erwartet")
	}
	if err := ValidateTargets([]Target{{ReportPort, "a:1"}}); err == nil {
		t.Error("Meldeport: Fehler erwartet")
	}
}
