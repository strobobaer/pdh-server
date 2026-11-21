package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdh/internal/integrations/unifi"
)

func TestMqttFirewallPorts(t *testing.T) {
	for name, c := range map[string]struct {
		cfg  map[string]string
		want string
	}{
		"nur MQTT-Port":                  {mqttCfg(), "Port 1883"},
		"mit Name und TLS":               {mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "8883"), "Ports 1883 und 8883"},
		"TLS-Port ohne Name zählt nicht": {mqttCfg("int_tls_port", "8883"), "Port 1883"},
		"fremder Broker":                 {map[string]string{"broker_mode": "external", "int_listen_port": "1883"}, ""},
	} {
		if got := mqttPortList(mqttFirewallPorts(c.cfg)); got != c.want {
			t.Errorf("%s: %q, erwartet %q", name, got, c.want)
		}
	}
	if got := mqttFwCommand([]int{1883, 8883}); got != "sudo ufw allow 1883/tcp && sudo ufw allow 8883/tcp" {
		t.Errorf("Befehl: %q", got)
	}
}

func fwState(firewall, note string, ports ...any) mqttFwState {
	st := mqttFwState{Firewall: firewall, Note: note}
	for i := 0; i+1 < len(ports); i += 2 {
		st.Ports = append(st.Ports, struct {
			Port int  `json:"port"`
			Open bool `json:"open"`
		}{ports[i].(int), ports[i+1].(bool)})
	}
	return st
}

func TestMqttFwRow(t *testing.T) {
	ports := []int{1883, 8883}
	const cmd = "sudo ufw allow 1883/tcp && sudo ufw allow 8883/tcp"

	// alles offen
	r := mqttFwRow(ports, true, fwState("active", "", 1883, true, 8883, true), nil)
	if r.Tone != "ok" || r.Action != "" || r.State != "Ports 1883 und 8883 offen" {
		t.Errorf("offen: %+v", r)
	}
	// ein Port gesperrt: Knopf
	r = mqttFwRow(ports, true, fwState("active", "", 1883, true, 8883, false), nil)
	if r.Tone != "warn" || r.Action != "firewall" || r.State != "Port 8883 gesperrt" {
		t.Errorf("gesperrt: %+v", r)
	}
	// Öffnen gescheitert: Befehl nur für die gesperrten Ports, Knopf bleibt
	r = mqttFwRow(ports, true, fwState("active", "ufw: Fehler", 1883, true, 8883, false), nil)
	if r.Action != "firewall" || !strings.Contains(r.Hint, "ufw: Fehler") || !strings.Contains(r.Hint, "sudo ufw allow 8883/tcp") || strings.Contains(r.Hint, "1883") {
		t.Errorf("gescheitert: %+v", r)
	}
	// keine Firewall: in Ordnung, kein Knopf
	for _, fw := range []string{"none", "inactive"} {
		if r = mqttFwRow(ports, true, fwState(fw, ""), nil); r.Tone != "ok" || r.Action != "" {
			t.Errorf("%s: %+v", fw, r)
		}
	}
	// kein Agent, Docker, Fehler, unlesbar: kein Knopf, aber der Befehl von Hand
	for name, got := range map[string]mqttCheckRow{
		"kein Agent": mqttFwRow(ports, false, mqttFwState{}, nil),
		"Docker":     mqttFwRow(ports, true, fwState("unsupported", ""), nil),
		"Fehler":     mqttFwRow(ports, true, mqttFwState{}, errors.New("Der Update-Agent ist zu alt – zuerst das PDH aktualisieren")),
		"unlesbar":   mqttFwRow(ports, true, fwState("unknown", "ufw status: kaputt"), nil),
	} {
		if got.Tone == "ok" || got.Action != "" || !strings.Contains(got.Hint, cmd) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestMqttLANJudge(t *testing.T) {
	const host = "mqtt.firma.de"
	own := []string{"192.0.2.17", "192.0.2.15"}
	a := func(name, value string, on bool) unifi.Record {
		return unifi.Record{ID: "1", Name: name, Value: value, Type: "A", Enabled: on}
	}
	cf4, cf6 := "104.16.1.1", "2606:4700::6810:101"

	// Antwort des Servers ist eine eigene Adresse
	if s, tone, note := mqttLANJudge(host, []string{"192.0.2.15"}, own, nil); tone != "ok" || s != "zeigt auf diesen Server" || note != "" {
		t.Errorf("direkt: %q %q %q", s, tone, note)
	}
	// Server sieht Cloudflare, die UDM Pro hat den Eintrag: in Ordnung, mit Erklärung
	s, tone, note := mqttLANJudge(host, []string{cf4}, own, []unifi.Record{a("MQTT.firma.de.", "192.0.2.15", true)})
	if tone != "ok" || !strings.Contains(s, "an der UDM Pro eingetragen") || !strings.Contains(s, "192.0.2.15") || !strings.Contains(note, "außerhalb des Hauses") {
		t.Errorf("UDM Pro hat den Eintrag: %q %q %q", s, tone, note)
	}
	// abgeschalteter Eintrag, fremde Adresse, anderer Name: zählt nicht
	for name, recs := range map[string][]unifi.Record{
		"abgeschaltet":   {a(host, "192.0.2.15", false)},
		"fremde Adresse": {a(host, "192.0.2.99", true)},
		"anderer Name":   {a("pdh.firma.de", "192.0.2.15", true)},
		"UDM Pro stumm":  nil,
	} {
		if s, tone, _ := mqttLANJudge(host, []string{cf4}, own, recs); tone != "warn" || s != "zeigt im Haus auf Cloudflare" {
			t.Errorf("%s: %q %q", name, s, tone)
		}
	}
	if s, tone, _ := mqttLANJudge(host, nil, own, nil); tone != "warn" || s != "fehlt" {
		t.Errorf("ohne Antwort: %q %q", s, tone)
	}
	// IPv6 zeigt nach außen: Hinweis – außer die UDM Pro hat einen IPv6-Eintrag
	// oder eine eigene IPv6-Adresse kommt zurück
	_, tone, note = mqttLANJudge(host, []string{"192.0.2.15", cf6}, own, nil)
	if tone != "ok" || !strings.Contains(note, "IPv6") {
		t.Errorf("IPv6 nach außen: %q %q", tone, note)
	}
	six := unifi.Record{ID: "2", Name: host, Value: "fd00::15", Type: "AAAA", Enabled: true}
	if _, _, note = mqttLANJudge(host, []string{"192.0.2.15", cf6}, own, []unifi.Record{six}); note != "" {
		t.Errorf("IPv6-Eintrag an der UDM Pro: %q", note)
	}
	if _, _, note = mqttLANJudge(host, []string{"192.0.2.15", "fd00::15"}, append(own, "fd00::15"), nil); note != "" {
		t.Errorf("eigene IPv6-Adresse: %q", note)
	}
	// ohne gültigen Namen im Haus kein IPv6-Hinweis (erst das Wichtige)
	if _, tone, note = mqttLANJudge(host, []string{cf4, cf6}, own, nil); tone != "warn" || note != "" {
		t.Errorf("Cloudflare mit IPv6: %q %q", tone, note)
	}
}

// Der Agent wird mit den Ports des Brokers gefragt; „öffnen“ kommt nur vom Knopf und vom Start.
func TestMqttFirewallAsksAgent(t *testing.T) {
	var got struct {
		Ports []int `json:"ports"`
		Open  bool  `json:"open"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/firewall/ports" || r.Header.Get("Authorization") != "Bearer geheim" {
			http.Error(w, "falsch", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"firewall":"active","ports":[{"port":1883,"open":true},{"port":8883,"open":false}]}`))
	}))
	defer srv.Close()
	h := &Handler{updateAgentURL: srv.URL, updateAgentToken: "geheim"}
	cfg := mqttCfg("int_public_host", "mqtt.firma.de", "int_websocket_port", "8083", "int_tls_port", "8883")

	row, ok := h.mqttFwCheckRow(t.Context(), cfg)
	if !ok || got.Open || len(got.Ports) != 2 || row.Action != "firewall" || row.State != "Port 8883 gesperrt" {
		t.Fatalf("Prüfliste: ok=%v anfrage=%+v zeile=%+v", ok, got, row)
	}
	if st, err := h.mqttFirewall(t.Context(), mqttFirewallPorts(cfg), true); err != nil || !got.Open || len(st.closed()) != 1 {
		t.Fatalf("öffnen: %v anfrage=%+v stand=%+v", err, got, st)
	}
	// älterer Agent ohne die Schnittstelle: Zeile mit Befehl statt Knopf
	h.updateAgentToken = "anders"
	if row, _ = h.mqttFwCheckRow(t.Context(), cfg); row.Action != "" || !strings.Contains(row.Hint, "zu alt") || !strings.Contains(row.Hint, "sudo ufw allow") {
		t.Fatalf("alter Agent: %+v", row)
	}
	// ohne Agent
	if row, _ = (&Handler{}).mqttFwCheckRow(t.Context(), cfg); row.Tone != "off" || !strings.Contains(row.Hint, "sudo ufw allow 1883/tcp") {
		t.Fatalf("ohne Agent: %+v", row)
	}
}
