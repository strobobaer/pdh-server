package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// Das Token kann statt über --token aus Standardeingabe, Datei oder Umgebung
// kommen, damit es nicht in der Shell-Historie landet.
func TestParseInstallArgsTokenSources(t *testing.T) {
	base := []string{"--id", testID, "--pdh", "100.64.0.1", "--target", "1433"}
	dir := t.TempDir()
	file := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(file, []byte("  aus-datei\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func(old io.Reader) { installStdin = old }(installStdin)

	cases := []struct {
		name  string
		args  []string
		env   string
		stdin string
		want  string
		fail  bool
	}{
		{"Datei", append([]string{"--token-file", file}, base...), "", "", "aus-datei", false},
		{"Standardeingabe", append([]string{"--token-file", "-"}, base...), "", "aus-stdin\n", "aus-stdin", false},
		{"Umgebung", base, "aus-env", "", "aus-env", false},
		{"Schalter vor Umgebung", append([]string{"--token", "direkt"}, base...), "aus-env", "", "direkt", false},
		{"Datei vor Umgebung", append([]string{"--token-file", file}, base...), "aus-env", "", "aus-datei", false},
		{"beide Schalter", append([]string{"--token", "x", "--token-file", file}, base...), "", "", "", true},
		{"Datei fehlt", append([]string{"--token-file", filepath.Join(dir, "gibt-es-nicht")}, base...), "", "", "", true},
		{"leere Datei", append([]string{"--token-file", "-"}, base...), "", "  \n", "", true},
		{"kein Token", base, "", "", "", true},
	}
	for _, tc := range cases {
		t.Setenv(tokenEnv, tc.env)
		installStdin = strings.NewReader(tc.stdin)
		c, err := parseInstallArgs(tc.args)
		if tc.fail {
			if err == nil {
				t.Errorf("%s: Fehler erwartet", tc.name)
			}
			continue
		}
		if err != nil || c.Token != tc.want {
			t.Errorf("%s: Token %q, Fehler %v; erwartet %q", tc.name, c.Token, err, tc.want)
		}
	}
}

func TestParseInstallArgsPDHMehrfach(t *testing.T) {
	c, err := parseInstallArgs([]string{
		"--id", testID, "--token", "tok", "--pdh", "pdh-a,100.64.0.1", "--pdh", "pdh-b",
		"--target", "1433",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pdh-a:8443", "100.64.0.1:8443", "pdh-b:8443"}
	if len(c.PDH) != 3 || c.PDH[0] != want[0] || c.PDH[1] != want[1] || c.PDH[2] != want[2] {
		t.Errorf("PDH = %v, erwartet %v", c.PDH, want)
	}
}
