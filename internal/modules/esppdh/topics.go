package esppdh

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Themen eines Geräts mit Anzeigename, Einheit und Gruppe – für die Anzeige der letzten
// Werte im PDH (Reiter „ESPPDH“ an der Anlage, Terminal, Geräteseite). Was hier steht, muss
// zur Firmware passen (TestTopicsMatchGeneratedYAML).

// Gruppen der Anzeige (angelehnt an die ESPHome-Dokumentation).
const (
	GroupEnergy        = "Strom & Energie"
	GroupAnalogDigital = "Analog & Digital"
	GroupDevice        = "Gerät"
)

// Geräte-Themen unter esppdh/<kennung>/info/.
const (
	topicUptime  = "laufzeit"
	topicAddress = "adresse"
	nameUptime   = "Laufzeit"
	nameAddress  = "Adresse"
)

func infoTopic(slug, value string) string { return "esppdh/" + slug + "/info/" + value }

// TopicInfo: ein Zustands-Thema des Geräts. Interval 0 = meldet nur bei Änderung (nie veraltet).
type TopicInfo struct {
	Topic, Name, Unit, Group string
	Interval                 time.Duration
	Handshake                bool // Laufzeit-Zähler des Geräts
}

// blockValue: ein Wert eines Bausteins (Endung des Themas, Anzeigename, Einheit).
type blockValue struct{ key, label, unit string }

// blockValues: Werte je Bausteinart – dieselben Namen und Themen wie in blockSensors (yaml.go).
var blockValues = map[string][]blockValue{
	KindS0:      {{"leistung", "Leistung", "W"}, {"zaehlerstand", "Zählerstand", "kWh"}},
	KindSCT013:  {{"strom", "Strom", "A"}, {"scheinleistung", "Scheinleistung", "VA"}},
	KindPZEM:    {{"spannung", "Spannung", "V"}, {"strom", "Strom", "A"}, {"leistung", "Leistung", "W"}, {"energie", "Energie", "kWh"}},
	KindCounter: {{"stueckzahl", "Stückzahl", "Stk"}},
}

var blockGroup = map[string]string{
	KindS0: GroupEnergy, KindSCT013: GroupEnergy, KindPZEM: GroupEnergy, KindCounter: GroupAnalogDigital,
}

// Topics: alle Zustands-Themen des Geräts in der Reihenfolge der Bausteine, danach Laufzeit
// (Handshake) und Adresse. Fehler, wenn Platine oder Bausteine ungültig sind.
func Topics(d Device) ([]TopicInfo, error) {
	b, ok := BoardByKey(d.BoardKey)
	if !ok {
		return nil, fmt.Errorf("Platine %q unbekannt", d.BoardKey)
	}
	blocks, msgs := resolveBlocks(b, d.Blocks)
	if len(msgs) > 0 {
		return nil, errors.New(strings.Join(msgs, " "))
	}
	var out []TopicInfo
	for _, r := range blocks {
		for _, v := range blockValues[r.def.Kind] {
			out = append(out, TopicInfo{
				Topic:    fmt.Sprintf("esppdh/%s/%s-%d/%s", d.Slug, r.def.Kind, r.nr, v.key),
				Name:     fmt.Sprintf("%s %d %s", r.def.Label, r.nr, v.label),
				Unit:     v.unit,
				Group:    blockGroup[r.def.Kind],
				Interval: 10 * time.Second,
			})
		}
	}
	out = append(out,
		TopicInfo{Topic: infoTopic(d.Slug, topicUptime), Name: nameUptime, Unit: "s", Group: GroupDevice, Interval: 10 * time.Second, Handshake: true},
		TopicInfo{Topic: infoTopic(d.Slug, topicAddress), Name: nameAddress, Group: GroupDevice})
	return out, nil
}
