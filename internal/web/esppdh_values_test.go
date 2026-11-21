package web

// Werte-Ansicht der ESPPDH-Geräte (0.82.11): letzte Werte aus dem Broker, Gruppen, veraltet,
// Handshake, bestätigte Adresse.

import (
	"net"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/mqttbroker"
)

// fakeTopics ersetzt den Broker in den Tests.
type fakeTopics struct {
	running bool
	vals    []mqttbroker.TopicValue
	remote  string
}

func (f fakeTopics) TopicValues(_, prefix string) ([]mqttbroker.TopicValue, bool) {
	if !f.running {
		return nil, false
	}
	var out []mqttbroker.TopicValue
	for _, v := range f.vals {
		if strings.HasPrefix(v.Topic, prefix) {
			out = append(out, v)
		}
	}
	return out, true
}

func (f fakeTopics) ClientRemoteIP(_, _ string) (string, bool) { return f.remote, f.remote != "" }

func TestEsppdhAddressOK(t *testing.T) {
	own := []net.IP{net.ParseIP("192.0.2.17")}
	for _, c := range []struct {
		reported, remote, subnet string
		ok                       bool
	}{
		{"10.20.0.5", "10.20.0.5", "10.20.0.0/24", true},
		{"10.20.0.5", "10.20.0.5", "", true},
		{"10.20.0.5", "10.20.0.9", "", false},
		{"127.0.0.1", "127.0.0.1", "", false},
		{"169.254.1.1", "169.254.1.1", "", false},
		{"224.0.0.1", "224.0.0.1", "", false},
		{"0.0.0.0", "0.0.0.0", "", false},
		{"192.0.2.17", "192.0.2.17", "", false},
		{"10.30.0.5", "10.30.0.5", "10.20.0.0/24", false},
		{"fe80::1", "fe80::1", "", false},
		{"", "10.20.0.5", "", false},
	} {
		ok, note := esppdhAddressOK(c.reported, c.remote, c.subnet, own)
		if ok != c.ok {
			t.Errorf("%q (Absender %q, Netz %q): ok=%v (%s), erwartet %v", c.reported, c.remote, c.subnet, ok, note, c.ok)
		}
		if !ok && note == "" {
			t.Errorf("%q: kein Hinweis zum Grund", c.reported)
		}
	}
	if _, note := esppdhAddressOK("10.20.0.5", "10.20.0.9", "", own); note != "Adresse nicht bestätigt" {
		t.Errorf("abweichender Absender: Hinweis %q", note)
	}
}

// valuesDevice: S0-Gerät „vw“ an einer Verbindung, eingeschaltete Webseite.
func valuesDevice() esppdhDevice {
	return esppdhDevice{ID: "00000000-0000-4000-8000-0000000000a1", Slug: "vw", Name: "Presse", ConnID: "00000000-0000-4000-8000-0000000000c1",
		BoardKey: "esp32dev", WebEnabled: true, NetSubnet: "10.20.0.0/24",
		Blocks: []esppdhBlock{{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}}}}
}

func TestEsppdhValuesBrokerDown(t *testing.T) {
	h := &Handler{topicSrc: fakeTopics{running: false}}
	v := h.esppdhValues(t.Context(), []esppdhDevice{valuesDevice()}, time.Now())
	if len(v) != 1 || v[0].Available || len(v[0].Groups) != 0 || v[0].Handshake.Stopped {
		t.Errorf("Broker aus: %+v, erwartet nicht verfügbar ohne Zeilen", v)
	}
}

func TestEsppdhValuesGroupsAndStale(t *testing.T) {
	now := time.Now()
	src := fakeTopics{running: true, remote: "10.20.0.5", vals: []mqttbroker.TopicValue{
		{Topic: "esppdh/vw/s0-1/leistung", Payload: "1500", At: now.Add(-5 * time.Second)},
		{Topic: "esppdh/vw/s0-1/zaehlerstand", Payload: "12.345", At: now.Add(-40 * time.Second)},
		{Topic: "esppdh/vw/info/laufzeit", Payload: "300", At: now.Add(-3 * time.Second)},
		{Topic: "esppdh/vw/info/adresse", Payload: "10.20.0.5", At: now.Add(-time.Hour)},
		{Topic: "esppdh/fremd/s0-1/leistung", Payload: "9", At: now},
	}}
	h := &Handler{topicSrc: src}
	v := h.esppdhValues(t.Context(), []esppdhDevice{valuesDevice()}, now)[0]
	if !v.Available {
		t.Fatal("nicht verfügbar trotz laufendem Broker")
	}
	if len(v.Groups) != 1 || v.Groups[0].Name != "Strom & Energie" {
		t.Fatalf("Gruppen: %+v", v.Groups)
	}
	rows := map[string]esppdhValueRow{}
	for _, r := range v.Groups[0].Rows {
		rows[r.Name] = r
	}
	if r := rows["S0-Zähler 1 Leistung"]; r.Value != "1500" || r.Unit != "W" || r.Stale {
		t.Errorf("Leistung: %+v", r)
	}
	if r := rows["S0-Zähler 1 Zählerstand"]; !r.Stale {
		t.Errorf("Zählerstand vor 40 s bei 10 s Intervall: %+v, erwartet veraltet", r)
	}
	if !v.Handshake.Running || v.Handshake.Count != "300" {
		t.Errorf("Handshake: %+v", v.Handshake)
	}
	if v.Address != "10.20.0.5" || v.DirectURL != "http://10.20.0.5/" {
		t.Errorf("Adresse %q, Link %q", v.Address, v.DirectURL)
	}
}

func TestEsppdhValuesStoppedUptime(t *testing.T) {
	now := time.Now()
	src := fakeTopics{running: true, vals: []mqttbroker.TopicValue{{Topic: "esppdh/vw/info/laufzeit", Payload: "300", At: now.Add(-40 * time.Second)}}}
	h := &Handler{topicSrc: src}
	v := h.esppdhValues(t.Context(), []esppdhDevice{valuesDevice()}, now)[0]
	if v.Handshake.Running {
		t.Errorf("Laufzeit vor 40 s: Handshake läuft, erwartet steht")
	}
	if v.DirectURL != "" {
		t.Errorf("ohne bestätigte Adresse ein Link: %q", v.DirectURL)
	}
}

func TestEsppdhValuesNoLinkWithoutWebServer(t *testing.T) {
	now := time.Now()
	src := fakeTopics{running: true, remote: "10.20.0.5", vals: []mqttbroker.TopicValue{{Topic: "esppdh/vw/info/adresse", Payload: "10.20.0.5", At: now}}}
	d := valuesDevice()
	d.WebEnabled = false
	v := (&Handler{topicSrc: src}).esppdhValues(t.Context(), []esppdhDevice{d}, now)[0]
	if v.DirectURL != "" || v.Address != "10.20.0.5" {
		t.Errorf("Webseite aus: Link %q, Adresse %q", v.DirectURL, v.Address)
	}
}
