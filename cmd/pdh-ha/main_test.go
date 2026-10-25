package main

import (
	"bytes"
	"strings"
	"testing"
)

// Der Beitrittscode kann über PDH_HA_JOIN kommen (nicht in der Befehlszeile).
func TestJoinCodeFromEnv(t *testing.T) {
	t.Setenv("PDH_HA_ETC", t.TempDir())
	t.Setenv("PDH_HA_STATE", t.TempDir())

	t.Setenv("PDH_HA_JOIN", "")
	var out, errb bytes.Buffer
	if rc := runCLI([]string{"join"}, &out, &errb); rc != 2 {
		t.Fatalf("ohne Code: rc %d", rc)
	}

	t.Setenv("PDH_HA_JOIN", "!!kaputt!!")
	errb.Reset()
	if rc := runCLI([]string{"join"}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "Beitrittscode ist unlesbar") {
		t.Fatalf("Code aus PDH_HA_JOIN nicht gelesen: rc %d %s", rc, errb.String())
	}

	// Argument hat Vorrang
	errb.Reset()
	code := joinCode{Addrs: []string{"10.0.0.1:7480"}, FP: "x", Secret: "y", Cluster: "z", Role: "witness"}.String()
	if rc := runCLI([]string{"join", code}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "Zeugen") {
		t.Fatalf("Argument: rc %d %s", rc, errb.String())
	}
}
