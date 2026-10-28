package main

import (
	"os/exec"
	"strings"
	"testing"
)

// bashOrSkip liefert den Pfad zu bash oder überspringt den Test.
func bashOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash nicht verfügbar")
	}
	return p
}

// TestAdresseScriptSyntax: pdh-ha-adresse.sh ist syntaktisch gültig und nennt
// ohne Argumente (Ausgang 2) alle fünf Schritte.
func TestAdresseScriptSyntax(t *testing.T) {
	bash := bashOrSkip(t)
	script := "../../scripts/pdh-ha-adresse.sh"
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	out, err := exec.Command(bash, script).CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("ohne Argumente: erwartet Ausgang 2, bekam %v\n%s", err, out)
	}
	for _, step := range []string{"neu", "verbund", "test", "pruefliste", "uebergabe"} {
		if !strings.Contains(string(out), "pdh-ha-adresse.sh "+step) {
			t.Errorf("Hilfe nennt Schritt %q nicht:\n%s", step, out)
		}
	}
}

// TestJoinScriptSyntax: das erweiterte Beitrittsskript bleibt gültig.
func TestJoinScriptSyntax(t *testing.T) {
	bash := bashOrSkip(t)
	if out, err := exec.Command(bash, "-n", "../../scripts/pdh-ha-join.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
