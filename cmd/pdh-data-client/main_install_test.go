package main

import "testing"

func TestParseInstallArgs(t *testing.T) {
	c, err := parseInstallArgs([]string{
		"--id", testID, "--token", "tok", "--pdh", "100.64.0.1, 100.64.0.2:9000",
		"--target", "1433", "--target", "5432=10.0.0.5:5432",
		"--authkey", "tskey-x", "--hostname", "kunde1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PDH) != 2 || c.PDH[0] != "100.64.0.1:8443" || c.PDH[1] != "100.64.0.2:9000" {
		t.Errorf("PDH = %v", c.PDH)
	}
	if len(c.Targets) != 2 || c.Targets[0].Addr != "127.0.0.1:1433" || c.Targets[1].Addr != "10.0.0.5:5432" {
		t.Errorf("Targets = %+v", c.Targets)
	}
	if c.AuthKey != "tskey-x" || c.Hostname != "kunde1" || c.StateDir == "" {
		t.Errorf("Config = %+v", c)
	}
}

func TestParseInstallArgsErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"ohne Ziel":       {"--id", testID, "--token", "t", "--pdh", "100.64.0.1"},
		"ungültiges Ziel": {"--id", testID, "--token", "t", "--pdh", "100.64.0.1", "--target", "0.0.0.0"},
		"doppelter Port":  {"--id", testID, "--token", "t", "--pdh", "100.64.0.1", "--target", "1433", "--target", "1433=10.0.0.1:1"},
		"ohne PDH":        {"--id", testID, "--token", "t", "--target", "1433"},
	} {
		if _, err := parseInstallArgs(args); err == nil {
			t.Errorf("%s: Fehler erwartet", name)
		}
	}
}
