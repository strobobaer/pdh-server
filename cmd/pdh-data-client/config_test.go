package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"pdh/internal/datanet"
)

const testID = "6f1c1c5e-8a3e-4b8e-9d0a-2f4f3f6a7b11"

func goodConfig() Config {
	return Config{
		ClientID: testID, Token: "tok", Hostname: "kunde", StateDir: "/tmp/x",
		PDH:     []string{"100.64.0.1:8443"},
		Targets: []datanet.Target{{Port: 1433, Addr: "127.0.0.1:1433"}},
	}
}

func TestConfigValidate(t *testing.T) {
	if err := goodConfig().Validate(); err != nil {
		t.Fatalf("gültige Konfiguration abgelehnt: %v", err)
	}
	cases := map[string]func(*Config){
		"keine UUID":      func(c *Config) { c.ClientID = "abc" },
		"ohne Token":      func(c *Config) { c.Token = "" },
		"ohne PDH":        func(c *Config) { c.PDH = nil },
		"ungültiges Ziel": func(c *Config) { c.Targets = []datanet.Target{{Port: 1433, Addr: "0.0.0.0:1433"}} },
		"ohne Ziel":       func(c *Config) { c.Targets = nil },
	}
	for name, mut := range cases {
		c := goodConfig()
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Fehler erwartet", name)
		}
	}
}

func TestConfigLoadAddsPDHPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := goodConfig()
	c.PDH = []string{"100.64.0.1", "100.64.0.2:9000"}
	if err := SaveConfig(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.PDH[0] != "100.64.0.1:8443" || got.PDH[1] != "100.64.0.2:9000" {
		t.Fatalf("PDH = %v", got.PDH)
	}
}

func TestConfigSaveMode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Dateirechte nur unter Linux")
	}
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := SaveConfig(p, goodConfig()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("Rechte %v, erwartet 0600", fi.Mode().Perm())
	}
	// Überschreiben behält die Rechte.
	if err := SaveConfig(p, goodConfig()); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("Rechte nach Überschreiben %v", fi.Mode().Perm())
	}
}
