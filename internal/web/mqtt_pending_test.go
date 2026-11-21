package web

import "testing"

// Neuer oder geänderter Name: vorgemerkt; gleicher Name: Vormerkung aus dem gespeicherten
// Stand übernehmen (das Formular trägt den Schlüssel nicht mit); kein Name: weg.
func TestMqttMarkPending(t *testing.T) {
	cases := []struct {
		name          string
		stored, conf  map[string]string
		want, present bool
	}{
		{"neu mit Name", map[string]string{}, map[string]string{mqttHostKey: "mqtt.firma.de"}, true, true},
		{"Name geändert", map[string]string{mqttHostKey: "alt.firma.de"}, map[string]string{mqttHostKey: "mqtt.firma.de"}, true, true},
		{"nur Port geändert", map[string]string{mqttHostKey: "mqtt.firma.de", "int_websocket_port": "8083"}, map[string]string{mqttHostKey: "mqtt.firma.de", "int_websocket_port": "8093"}, false, false},
		{"gleicher Name, war vorgemerkt", map[string]string{mqttHostKey: "mqtt.firma.de", mqttPendingKey: "1"}, map[string]string{mqttHostKey: "mqtt.firma.de"}, true, true},
		{"Name geleert", map[string]string{mqttHostKey: "mqtt.firma.de", mqttPendingKey: "1"}, map[string]string{mqttHostKey: ""}, false, false},
		{"Schreibweise", map[string]string{mqttHostKey: "MQTT.Firma.de."}, map[string]string{mqttHostKey: "mqtt.firma.de"}, false, false},
	}
	for _, c := range cases {
		mqttMarkPending(c.stored, c.conf)
		v, ok := c.conf[mqttPendingKey]
		if ok != c.present || (v == "1") != c.want {
			t.Errorf("%s: Vormerkung %q (da=%v), erwartet da=%v", c.name, v, ok, c.present)
		}
	}
}

func TestMqttUpstreamInSkipsPending(t *testing.T) {
	sys := []mqttSystem{
		{Host: "neu.firma.de", WSPort: 8093, Pending: true},
		{Host: "mqtt.firma.de", WSPort: 8093},
	}
	if got := mqttUpstreamIn(sys, "neu.firma.de"); got != "" {
		t.Errorf("vorgemerkter Name hat ein Ziel: %q", got)
	}
	if got := mqttUpstreamIn(sys, "MQTT.firma.de."); got != "http://127.0.0.1:8093" {
		t.Errorf("Ziel: %q", got)
	}
}

func TestMqttTargetsSkipsPending(t *testing.T) {
	sys := []mqttSystem{
		{Host: "neu.firma.de", WSPort: 8093, Enabled: true, Pending: true},
		{Host: "mqtt.firma.de", WSPort: 8093, Enabled: true},
	}
	got := mqttTargets(sys, "firma.de")
	if len(got) != 1 || got[0].Host != "mqtt.firma.de" {
		t.Errorf("Tunnel-Ziele: %+v", got)
	}
}
