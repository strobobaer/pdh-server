package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// certSetProbe ersetzt die TLS-Probe; fn bekommt Adresse und Namen.
func certSetProbe(t *testing.T, fn func(addr, name string) (string, error)) {
	t.Helper()
	old := certProbe
	certProbe = func(_ context.Context, addr, name string) (string, error) { return fn(addr, name) }
	t.Cleanup(func() { certProbe = old })
}

// guardFixture: pdh.firma.de von Hand auf IPv4 und IPv6 (die Regel lässt IPv6 also zu).
func guardFixture(t *testing.T) *certFixture {
	t.Helper()
	certSetIPv6(t, true)
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n    server_name pdh.firma.de;\n}\n")
	return f
}

// Über IPv6 bekäme pdh.firma.de mit der neuen Seite ein anderes Zertifikat: die Seite
// wird ohne IPv6-Zeilen geschrieben, und der Hinweis nennt den Namen.
func TestCertGuardFallsBackToIPv4(t *testing.T) {
	f := guardFixture(t)
	certSetProbe(t, func(addr, name string) (string, error) {
		if name == "pdh.firma.de" && addr == "[::1]:443" && f.exists(mqttSitePath) && strings.Contains(f.read(t, mqttSitePath), "listen [::]:443") {
			return "B", nil
		}
		return "A", nil
	})
	resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093")
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	site := f.read(t, mqttSitePath)
	if strings.Contains(site, "listen [::]:443 ssl;") || !strings.Contains(site, certSiteV6Off) {
		t.Errorf("Seite soll nur auf IPv4 hören (mit Vermerk):\n%s", site)
	}
	if note, _ := resp["note"].(string); !strings.Contains(note, "pdh.firma.de") {
		t.Errorf("Hinweis nennt den Namen nicht: %q", note)
	}
}

// Stört die Seite auch ohne IPv6, wird alles zurückgenommen.
func TestCertGuardRollsBack(t *testing.T) {
	f := guardFixture(t)
	certSetProbe(t, func(addr, name string) (string, error) {
		if name == "pdh.firma.de" && f.exists(mqttSitePath) {
			return "B", nil
		}
		return "A", nil
	})
	resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093")
	if resp["ok"] == true {
		t.Fatalf("Antwort: %v", resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "würde pdh.firma.de stören") {
		t.Errorf("Fehler: %q", msg)
	}
	if f.exists(mqttSitePath) {
		t.Errorf("Seite hätte zurückgenommen werden müssen:\n%s", f.read(t, mqttSitePath))
	}
	if n := certCountCmds(f, "reload"); n < 2 {
		t.Errorf("nginx nach der Rücknahme nicht neu geladen (%d)", n)
	}
}

// Ein Name, der vorher nicht antwortete, gilt nicht als gestört.
func TestCertGuardIgnoresUnreachable(t *testing.T) {
	f := guardFixture(t)
	certSetProbe(t, func(addr, name string) (string, error) {
		if !f.exists(mqttSitePath) {
			return "", errors.New("keine Antwort")
		}
		return "B", nil
	})
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
}

func TestCertOtherNames(t *testing.T) {
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/a.conf", "server {\n    listen 443 ssl;\n    server_name pdh.firma.de www.firma.de;\n}\nserver {\n    listen 443 ssl;\n    server_name _;\n}\n")
	f.write(t, "/etc/nginx/conf.d/b.conf", "server {\n    listen 443 ssl;\n    server_name *.firma.de;\n}\n")
	f.write(t, "/etc/nginx/conf.d/c.conf", "server {\n    listen 80;\n    server_name alt.firma.de;\n}\n")
	f.write(t, mqttSitePath, "server {\n    listen 443 ssl;\n    server_name mqtt.firma.de;\n}\n")
	got := f.agent.certOtherNames(certMQTTSite(mqttName))
	if want := []string{"pdh.firma.de", "www.firma.de"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Namen: %v, erwartet %v", got, want)
	}
}

func TestCertDisturbed(t *testing.T) {
	cases := []struct {
		before, after map[string]string
		bad           bool
	}{
		{map[string]string{"a|x": "A"}, map[string]string{"a|x": "A"}, false},
		{map[string]string{"a|x": "A"}, map[string]string{"a|x": "B"}, true},
		{map[string]string{"a|x": "A"}, map[string]string{"a|x": ""}, true},
		{map[string]string{"a|x": ""}, map[string]string{"a|x": "B"}, false},
	}
	for i, c := range cases {
		if _, bad := certDisturbed(c.before, c.after); bad != c.bad {
			t.Errorf("Fall %d: gestört=%v, erwartet %v", i, bad, c.bad)
		}
	}
}
