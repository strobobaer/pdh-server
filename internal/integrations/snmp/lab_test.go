package snmp

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// Prüfung gegen die Labor-Gegenstelle (deploy/lab/snmp, echter net-snmp-Dienst). Läuft nur
// mit PDH_TEST_SNMP=127.0.0.1:1161. Benutzer und Kennwörter sind reine Laborwerte.
const (
	labAuthPass = "Labor-Auth-2026"
	labPrivPass = "Labor-Priv-2026"
)

func labConfig(t *testing.T) Config {
	t.Helper()
	addr := os.Getenv("PDH_TEST_SNMP")
	if addr == "" {
		t.Skip("PDH_TEST_SNMP nicht gesetzt (Labor: deploy/lab/snmp, z. B. 127.0.0.1:1161)")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("PDH_TEST_SNMP: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("PDH_TEST_SNMP: %v", err)
	}
	allowLoopback = true
	t.Cleanup(func() { allowLoopback = false })
	return Config{Host: host, Port: port, User: "pdhlab", AuthProto: "sha256", AuthPass: labAuthPass,
		PrivProto: "aes128", PrivPass: labPrivPass}
}

func TestLabPingUndDetail(t *testing.T) {
	cfg := labConfig(t)
	ctx := context.Background()
	c := New(cfg)
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v (Ursache: %v)", err, errors.Unwrap(err))
	}
	d, err := c.Detail(ctx)
	if err != nil {
		t.Fatalf("Detail: %v (Ursache: %v)", err, errors.Unwrap(err))
	}
	t.Logf("Detail: %+v", d)
	if d.Name != "pdh-lab-snmp" {
		t.Errorf("Name = %q, erwartet pdh-lab-snmp", d.Name)
	}
	if d.Model == "" {
		t.Error("Modell leer – erwartet die gekürzte Beschreibung (sysDescr)")
	}
	if d.Kind != DeviceOther {
		t.Errorf("Art = %q, erwartet other", d.Kind)
	}
	if len(d.Ports) == 0 || d.MAC == "" {
		t.Errorf("erwartet mindestens einen Ethernet-Anschluss mit MAC, bekam %d / %q", len(d.Ports), d.MAC)
	}
}

// Jede Kombination der Verfahren, die der Labor-Dienst kennt – auch AES-256 in der
// Standardvariante (Blumenthal).
func TestLabVerfahren(t *testing.T) {
	cfg := labConfig(t)
	for _, tc := range []struct{ user, auth, priv string }{
		{"pdhlab", "sha256", "aes128"},
		{"pdhlab512", "sha512", "aes256"},
		{"pdhlabsha1", "sha1", "aes128"},
	} {
		c := cfg
		c.User, c.AuthProto, c.PrivProto = tc.user, tc.auth, tc.priv
		if err := New(c).Ping(context.Background()); err != nil {
			t.Errorf("%s (%s/%s): %v (Ursache: %v)", tc.user, tc.auth, tc.priv, err, errors.Unwrap(err))
		}
	}
}

// Ein abgelehnter Zugang darf nie wie ein Ausfall aussehen.
func TestLabZugangAbgelehnt(t *testing.T) {
	cfg := labConfig(t)
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"falsches Anmelde-Kennwort", func(c *Config) { c.AuthPass = "ganz-falsch-1" }},
		{"falsches Verschlüsselungs-Kennwort", func(c *Config) { c.PrivPass = "ganz-falsch-2" }},
		{"unbekannter Benutzer", func(c *Config) { c.User = "gibtesnicht" }},
		{"falsches Anmelde-Verfahren", func(c *Config) { c.AuthProto = "sha512" }},
		{"falsches Verschlüsselungs-Verfahren", func(c *Config) { c.PrivProto = "aes256" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			tc.edit(&c)
			start := time.Now()
			err := New(c).Ping(context.Background())
			t.Logf("%v · Art %v · Ursache: %v · %s", err, Classify(err), errors.Unwrap(err), time.Since(start).Round(time.Millisecond))
			if Classify(err) != KindAuth {
				t.Errorf("Art = %v, erwartet auth", Classify(err))
			}
			if !errors.Is(err, ErrAuth) {
				t.Error("errors.Is(err, ErrAuth) = false")
			}
			if _, derr := New(c).Detail(context.Background()); Classify(derr) != KindAuth {
				t.Errorf("Detail: Art = %v, erwartet auth", Classify(derr))
			}
		})
	}
}

func TestLabFalscherPort(t *testing.T) {
	cfg := labConfig(t)
	cfg.Port = 1169 // dort lauscht nichts
	err := New(cfg).Ping(context.Background())
	t.Logf("%v · Ursache: %v", err, errors.Unwrap(err))
	if Classify(err) != KindTimeout || !errors.Is(err, ErrTimeout) {
		t.Errorf("Art = %v, erwartet timeout", Classify(err))
	}
}
