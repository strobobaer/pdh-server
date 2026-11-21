package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
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
	old := certSettle
	certSettle = time.Millisecond
	t.Cleanup(func() { certSettle = old })
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n    server_name pdh.firma.de;\n}\n")
	return f
}

const mqttChainPath = "/etc/ssl/pdh/" + mqttName + "/fullchain.pem"

// ownFP: Fingerabdruck des Zertifikats, das gerade für den MQTT-Namen abgelegt ist.
func ownFP(t *testing.T, f *certFixture) string {
	t.Helper()
	if !f.exists(mqttChainPath) {
		return ""
	}
	return certChainFP([]byte(f.read(t, mqttChainPath)))
}

// hijackProbe: über ::1 kommt für pdh.firma.de das MQTT-Zertifikat, solange die MQTT-Seite
// auf IPv6 hört; über 127.0.0.1 kommt immer das eigene („A“).
func hijackProbe(t *testing.T, f *certFixture) func(addr, name string) (string, error) {
	return func(addr, name string) (string, error) {
		if name != "pdh.firma.de" {
			return "", errors.New("unbekannt")
		}
		if addr == "[::1]:443" {
			if f.exists(mqttSitePath) && strings.Contains(f.read(t, mqttSitePath), "listen [::]:443") {
				return ownFP(t, f), nil
			}
			return "", errors.New("keine Antwort")
		}
		return "A", nil
	}
}

// Vorfall 202 reparieren: Die alte MQTT-Seite hört auf IPv6 und bedient pdh.firma.de über ::1
// (falsch). Die neue Seite hört nur auf IPv4 – über ::1 antwortet dann niemand. Das ist die
// Reparatur, keine Störung (vorher kam ohnehin das eigene Zertifikat).
func TestCertGuardRepairsHijackedIPv6(t *testing.T) {
	f := guardFixture(t)
	f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    listen 443 ssl;\n    server_name pdh.firma.de;\n}\n")
	oldChain, oldKey := certTestPEM(t, mqttName)
	f.write(t, mqttChainPath, oldChain)
	f.write(t, "/etc/ssl/pdh/"+mqttName+"/privkey.pem", oldKey)
	f.write(t, mqttSitePath, certSiteConf(mqttName, "http://127.0.0.1:8083", false, true))
	certSetProbe(t, hijackProbe(t, f))
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Reparatur abgelehnt: %v", resp)
	}
	if site := f.read(t, mqttSitePath); strings.Contains(site, "listen [::]:443") {
		t.Errorf("Seite hört weiter auf IPv6:\n%s", site)
	}
}

// Vorfall-Muster neu: Vorher antwortete über ::1 niemand, nachher kommt unser Zertifikat für
// einen fremden Namen, der über 127.0.0.1 sein eigenes hat – das ist eine Störung.
func TestCertGuardDetectsNewIPv6Hijack(t *testing.T) {
	f := guardFixture(t)
	certSetProbe(t, hijackProbe(t, f))
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if site := f.read(t, mqttSitePath); strings.Contains(site, "listen [::]:443 ssl;") {
		t.Errorf("Seite hätte auf IPv4 zurückfallen müssen:\n%s", site)
	}
}

// nginx lädt im Hintergrund neu: Die erste Probe danach kann noch den alten Stand zeigen.
func TestCertGuardWaitsForReload(t *testing.T) {
	f := guardFixture(t)
	calls := 0
	certSetProbe(t, func(addr, name string) (string, error) {
		if name != "pdh.firma.de" || addr != "127.0.0.1:443" {
			return "", errors.New("unbekannt")
		}
		if !f.exists(mqttSitePath) {
			return "A", nil
		}
		calls++
		if calls == 1 {
			return "A", nil // alte Arbeitsprozesse
		}
		return "B", nil
	})
	resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093")
	if msg, _ := resp["error"].(string); resp["ok"] == true || !strings.Contains(msg, "stören") {
		t.Errorf("Störung nach dem Neuladen nicht erkannt: %v", resp)
	}
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
		{map[string]string{"a|x": "OWN"}, map[string]string{"a|x": ""}, false},
		{map[string]string{"[::1]:443|x": "", "127.0.0.1:443|x": "A"}, map[string]string{"[::1]:443|x": "OWN", "127.0.0.1:443|x": "A"}, true},
		{map[string]string{"[::1]:443|x": "", "127.0.0.1:443|x": ""}, map[string]string{"[::1]:443|x": "OWN", "127.0.0.1:443|x": ""}, false},
	}
	for i, c := range cases {
		if _, bad := certDisturbed(c.before, c.after, map[string]bool{"OWN": true}); bad != c.bad {
			t.Errorf("Fall %d: gestört=%v, erwartet %v", i, bad, c.bad)
		}
	}
}
