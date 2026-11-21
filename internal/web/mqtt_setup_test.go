package web

import (
	"context"
	"strings"
	"testing"
)

// setupFake: Ersatz-Abhängigkeiten mit Zählern.
type setupFake struct {
	st                         mqttSetupState
	lanSet, cf, foreign        bool
	strict                     bool
	curService, curHost        string
	curWS                      int
	notes                      []string
	issued, armed, site, lanMk int
	tunnelTo                   []string
}

func (f *setupFake) deps() mqttSetupDeps {
	return mqttSetupDeps{
		state:         func(context.Context) mqttSetupState { return f.st },
		issueCert:     func(context.Context) error { f.issued++; return nil },
		waitCert:      func(context.Context) bool { return f.st.CertOK },
		lanConfigured: func(context.Context) bool { return f.lanSet },
		lanCreate:     func(context.Context) error { f.lanMk++; f.st.LANOK = true; return nil },
		openFirewall:  func(context.Context) error { f.st.FirewallOK = true; return nil },
		arm:           func(context.Context) error { f.armed++; return nil },
		writeSite:     func() { f.site++ },
		cfReady:       func(context.Context) bool { return f.cf },
		tunnelService: func(context.Context) (string, bool, error) { return f.curService, f.curService != "", nil },
		tunnelWrite: func(_ context.Context, service string) (bool, error) {
			if f.foreign {
				return true, nil
			}
			f.tunnelTo = append(f.tunnelTo, service)
			return false, nil
		},
		tunnelTarget: func(_ context.Context, ws int) string {
			if f.strict {
				return certStrictService
			}
			return mqttWSService(ws)
		},
		current: func(context.Context) (string, int, bool) {
			if f.curHost == "" {
				return "mqtt.firma.de", 8093, false
			}
			return f.curHost, f.curWS, false
		},
		saveNotes: func(_ context.Context, notes []string) { f.notes = notes },
	}
}

func TestMqttSetupStrictTarget(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, LANOK: true, FirewallOK: true}, cf: true, strict: true}
	mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, allRights)
	if len(f.tunnelTo) != 1 || f.tunnelTo[0] != certStrictService {
		t.Errorf("bei Full (strict) gehört das strenge Ziel in den Tunnel: %v", f.tunnelTo)
	}
}

func TestMqttSetupSavesNotes(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, FirewallOK: true, LANTarget: "192.0.2.202"}}
	mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, allRights)
	if !strings.Contains(strings.Join(f.notes, "\n"), "A mqtt.firma.de → 192.0.2.202") {
		t.Errorf("Hinweise nicht für die Prüfliste gespeichert: %v", f.notes)
	}
}

// Ein Lauf für einen inzwischen geänderten Namen oder Port schreibt nichts.
func TestMqttTunnelFollowPortStale(t *testing.T) {
	f := &setupFake{cf: true, curService: "http://127.0.0.1:8083", curHost: "mqtt.firma.de", curWS: 9001}
	if err := mqttTunnelFollowPort(context.Background(), f.deps(), 8083, 8093, allRights); err != nil || len(f.tunnelTo) != 0 {
		t.Errorf("veralteter Lauf hat geschrieben: %v %v", err, f.tunnelTo)
	}
}

func TestMqttTunnelByHand(t *testing.T) {
	if got := mqttTunnelByHand("mqtt.firma.de", "http://127.0.0.1:8093"); got != "Eintrag von Hand im Cloudflare-Tunnel: mqtt.firma.de → http://127.0.0.1:8093" {
		t.Errorf("Hinweis: %q", got)
	}
}

var allRights = mqttSetupRights{Cert: true, LAN: true, Cloudflare: true}

func TestMqttSetupArmsWhenAllGreen(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, LANOK: true, FirewallOK: true}, cf: true}
	res := mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, allRights)
	if !res.Armed || f.armed != 1 || f.site != 1 || len(f.tunnelTo) != 1 || f.tunnelTo[0] != "http://127.0.0.1:8093" {
		t.Errorf("Ergebnis %+v, scharf=%d Seite=%d Tunnel=%v", res, f.armed, f.site, f.tunnelTo)
	}
}

// Ohne Eintrag im Haus wird der Name trotzdem scharf (Entscheidung des Anwenders 21.11.2026:
// „MQTT ohne DNS ok“) – der fehlende Eintrag steht nur als Hinweis da.
func TestMqttSetupArmsWithoutLAN(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, FirewallOK: true, LANTarget: "192.0.2.202"}, cf: true}
	res := mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, mqttSetupRights{Cert: true, Cloudflare: true})
	if !res.Armed || f.armed != 1 || f.site != 1 || len(f.tunnelTo) != 1 {
		t.Errorf("soll ohne Eintrag im Haus scharf werden: %+v scharf=%d Seite=%d Tunnel=%v", res, f.armed, f.site, f.tunnelTo)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "A mqtt.firma.de → 192.0.2.202") {
		t.Errorf("Hinweis nennt den Eintrag nicht: %v", res.Notes)
	}
}

func TestMqttSetupCreatesLANEntry(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, FirewallOK: true}, lanSet: true}
	res := mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, allRights)
	if f.lanMk != 1 || !res.Armed {
		t.Errorf("Eintrag im Haus nicht angelegt: %+v (%d)", res, f.lanMk)
	}
}

func TestMqttSetupNoCertRightStaysPending(t *testing.T) {
	f := &setupFake{st: mqttSetupState{LANOK: true, FirewallOK: true}}
	res := mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, mqttSetupRights{LAN: true})
	if res.Armed || f.issued != 0 || f.armed != 0 {
		t.Errorf("ohne Recht kein Zertifikat und nicht scharf: %+v ausgestellt=%d", res, f.issued)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "system.certs") {
		t.Errorf("Hinweis nennt das fehlende Recht nicht: %v", res.Notes)
	}
}

func TestMqttSetupForeignDNSNotReplaced(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, LANOK: true, FirewallOK: true}, cf: true, foreign: true}
	res := mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, true, allRights)
	if !res.Armed || !res.Replace || f.site != 1 || len(f.tunnelTo) != 0 {
		t.Errorf("fremder Eintrag: %+v Seite=%d Tunnel=%v", res, f.site, f.tunnelTo)
	}
}

func TestMqttSetupWithoutCloudflareNamesEntry(t *testing.T) {
	f := &setupFake{st: mqttSetupState{CertOK: true, LANOK: true, FirewallOK: true}}
	res := mqttNameSetup(context.Background(), f.deps(), "mqtt.firma.de", 8093, false, allRights)
	if !res.Armed || f.armed != 0 || f.site != 1 {
		t.Errorf("schon scharf: %+v scharf=%d Seite=%d", res, f.armed, f.site)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "mqtt.firma.de → http://127.0.0.1:8093") {
		t.Errorf("Hinweis zum Tunnel fehlt: %v", res.Notes)
	}
}

func TestMqttTunnelFollowPort(t *testing.T) {
	f := &setupFake{cf: true, curService: "http://localhost:8083"}
	if err := mqttTunnelFollowPort(context.Background(), f.deps(), 8083, 8093, allRights); err != nil || len(f.tunnelTo) != 1 || f.tunnelTo[0] != "http://127.0.0.1:8093" {
		t.Errorf("Zeile nicht nachgezogen: %v %v", err, f.tunnelTo)
	}
	f = &setupFake{cf: true, curService: "http://127.0.0.1:9999"}
	if err := mqttTunnelFollowPort(context.Background(), f.deps(), 8083, 8093, allRights); err != nil || len(f.tunnelTo) != 0 {
		t.Errorf("fremdes Ziel geändert: %v %v", err, f.tunnelTo)
	}
	for _, c := range []struct {
		cf  bool
		can mqttSetupRights
	}{{false, allRights}, {true, mqttSetupRights{}}} {
		f = &setupFake{cf: c.cf, curService: "http://127.0.0.1:8083"}
		if err := mqttTunnelFollowPort(context.Background(), f.deps(), 8083, 8093, c.can); err != nil || len(f.tunnelTo) != 0 {
			t.Errorf("ohne Zugang/Recht geschrieben: %v %v", err, f.tunnelTo)
		}
	}
}
