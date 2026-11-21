package esppdh

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// stateTopics: alle state_topic-Werte im erzeugten YAML mit dem Namen daneben.
func stateTopics(n *yaml.Node, out map[string]string) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		var name, topic string
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "name":
				name = n.Content[i+1].Value
			case "state_topic":
				topic = n.Content[i+1].Value
			}
		}
		if topic != "" {
			out[topic] = name
		}
	}
	for _, c := range n.Content {
		stateTopics(c, out)
	}
}

// Die Themenliste (Reiter, Terminal) und die Firmware dürfen nicht auseinanderlaufen.
func TestTopicsMatchGeneratedYAML(t *testing.T) {
	for _, s := range Samples() {
		cfg, _, err := Generate(s.Device)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		inYAML := map[string]string{}
		stateTopics(parseYAML(t, cfg), inYAML)
		list, err := Topics(s.Device)
		if err != nil {
			t.Fatalf("%s: Topics: %v", s.Name, err)
		}
		got := map[string]string{}
		for _, ti := range list {
			got[ti.Topic] = ti.Name
		}
		for topic, name := range inYAML {
			if n, ok := got[topic]; !ok {
				t.Errorf("%s: Thema %q fehlt in Topics", s.Name, topic)
			} else if n != name {
				t.Errorf("%s: Name zu %q: Topics %q, Firmware %q", s.Name, topic, n, name)
			}
		}
		for topic := range got {
			if _, ok := inYAML[topic]; !ok {
				t.Errorf("%s: Topics nennt %q, die Firmware sendet es nicht", s.Name, topic)
			}
		}
	}
}

// Laufzeit und Adresse hängen nicht an den Bausteinen – auch ein Gerät ohne (gültige) Bausteine
// liefert sie, damit Handshake und Adresse angezeigt werden.
func TestTopicsInfoWithoutBlocks(t *testing.T) {
	d := webDevice("esp32dev")
	d.Blocks = nil
	list, err := Topics(d)
	if err == nil {
		t.Log("ohne Bausteine kein Fehler")
	}
	var hs, addr bool
	for _, ti := range list {
		hs = hs || ti.Handshake
		addr = addr || ti.Topic == "esppdh/halle1-s0/info/adresse"
	}
	if !hs || !addr {
		t.Errorf("ohne Bausteine: %+v – Laufzeit und Adresse fehlen", list)
	}
}

func TestTopicsGroupsAndHandshake(t *testing.T) {
	d := webDevice("esp32dev")
	d.Blocks = append(d.Blocks, Block{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "GPIO5"}})
	list, err := Topics(d)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]string{}
	var hs []string
	for _, ti := range list {
		groups[ti.Topic] = ti.Group
		if ti.Handshake {
			hs = append(hs, ti.Topic)
		}
	}
	for topic, want := range map[string]string{
		"esppdh/halle1-s0/s0-1/leistung":        GroupEnergy,
		"esppdh/halle1-s0/counter-1/stueckzahl": GroupAnalogDigital,
		"esppdh/halle1-s0/info/laufzeit":        GroupDevice,
		"esppdh/halle1-s0/info/adresse":         GroupDevice,
	} {
		if groups[topic] != want {
			t.Errorf("Gruppe von %s = %q, erwartet %q", topic, groups[topic], want)
		}
	}
	if !slices.Equal(hs, []string{"esppdh/halle1-s0/info/laufzeit"}) {
		t.Errorf("Handshake-Themen %v, erwartet nur die Laufzeit", hs)
	}
	for _, ti := range list {
		if ti.Topic == "esppdh/halle1-s0/s0-1/leistung" && ti.Unit != "W" {
			t.Errorf("Einheit Leistung %q, erwartet W", ti.Unit)
		}
	}
}
