package esppdh

import (
	"slices"
	"strings"
	"testing"
)

// Platinen, Bausteine und ihre Prüfung (boards.go, blocks.go).

func mustBoard(t *testing.T, key string) Board {
	t.Helper()
	b, ok := BoardByKey(key)
	if !ok {
		t.Fatalf("Platine %q fehlt", key)
	}
	return b
}

// hasMsg: enthält eine der Meldungen alle Teile?
func hasMsg(msgs []string, parts ...string) bool {
	for _, m := range msgs {
		ok := true
		for _, p := range parts {
			if !strings.Contains(m, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestBoards(t *testing.T) {
	var keys []string
	for _, b := range Boards() {
		keys = append(keys, b.Key)
		if b.Label == "" {
			t.Errorf("%s: Bezeichnung fehlt", b.Key)
		}
		if b.Key == BoardOther {
			if b.Platform != "" || b.ESPHomeBoard != "" || len(b.ADCPins)+len(b.InputPins) > 0 {
				t.Errorf("other: Platform, Platine und Pins müssen leer sein: %+v", b)
			}
			continue
		}
		if b.Platform != "esp32" && b.Platform != "esp8266" {
			t.Errorf("%s: Platform %q", b.Key, b.Platform)
		}
		if b.ESPHomeBoard == "" || len(b.ADCPins) == 0 || len(b.InputPins) == 0 {
			t.Errorf("%s: Platine oder Pins fehlen: %+v", b.Key, b)
		}
		if b.Ethernet != (b.eth != nil) {
			t.Errorf("%s: Ethernet %v, Anschlussdaten %v", b.Key, b.Ethernet, b.eth)
		}
	}
	want := []string{"esp32dev", "esp32-s3", "esp32-c3", "d1-mini", "wt32-eth01", "olimex-poe", "other"}
	if !slices.Equal(keys, want) {
		t.Errorf("Platinen %v, erwartet %v", keys, want)
	}
	if _, ok := BoardByKey("gibtsnicht"); ok {
		t.Error("unbekannte Platine gefunden")
	}
	// Boards liefert Kopien: Änderungen bleiben beim Aufrufer.
	bs := Boards()
	bs[0].InputPins[0] = "kaputt"
	if b := mustBoard(t, bs[0].Key); b.InputPins[0] == "kaputt" {
		t.Error("Boards gibt die eigene Liste heraus")
	}
}

func TestCustomBoardPlatform(t *testing.T) {
	for id, want := range map[string]string{
		"nodemcuv2": "esp8266", "d1_mini_pro": "esp8266", "esp01_1m": "esp8266",
		"lolin_s2_mini": "esp32", "esp32-c6-devkitc-1": "esp32",
	} {
		if got := customPlatform(id); got != want {
			t.Errorf("customPlatform(%q) = %q, erwartet %q", id, got, want)
		}
	}
}

// PlatformOf: Plattform für die Seiten (TLS geht nur auf ESP32) – auch bei „Andere Platine“.
func TestPlatformOf(t *testing.T) {
	for _, c := range []struct{ board, custom, want string }{
		{"esp32dev", "", "esp32"}, {"d1-mini", "", "esp8266"}, {"wt32-eth01", "", "esp32"},
		{BoardOther, "nodemcuv2", "esp8266"}, {BoardOther, "lolin_s2_mini", "esp32"},
		{BoardOther, "", ""}, {BoardOther, "kaputt!", ""}, {"gibt-es-nicht", "", ""},
	} {
		if got := PlatformOf(c.board, c.custom); got != c.want {
			t.Errorf("PlatformOf(%q, %q) = %q, erwartet %q", c.board, c.custom, got, c.want)
		}
	}
}

func TestBlockDefs(t *testing.T) {
	defs := BlockDefs()
	var kinds []string
	for _, d := range defs {
		kinds = append(kinds, d.Kind)
		if d.Label == "" || d.Help == "" || len(d.Fields) == 0 {
			t.Errorf("%s: Bezeichnung, Hilfe oder Felder fehlen", d.Kind)
		}
		for _, f := range d.Fields {
			switch f.Type {
			case "pin", "number", "select", "text":
			default:
				t.Errorf("%s.%s: Feldtyp %q", d.Kind, f.Key, f.Type)
			}
			if f.Type == "select" && len(f.Options) == 0 {
				t.Errorf("%s.%s: Auswahl ohne Werte", d.Kind, f.Key)
			}
		}
	}
	if !slices.Equal(kinds, []string{"s0", "sct013", "pzem", "counter"}) {
		t.Errorf("Bausteine %v", kinds)
	}
	field := func(kind, key string) Field {
		for _, d := range defs {
			for _, f := range d.Fields {
				if d.Kind == kind && f.Key == key {
					return f
				}
			}
		}
		t.Fatalf("Feld %s.%s fehlt", kind, key)
		return Field{}
	}
	// Vorgaben aus dem Entwurf
	for _, c := range []struct{ kind, key, def string }{
		{"s0", "pulses", "1000"}, {"sct013", "voltage", "230"},
		// Flanke: Vorgabe „fallend“ – mit Pull-up zieht ein schaltender Sensor/Kontakt den Pin auf GND
		{"pzem", "address", "1"}, {"counter", "debounce", "50"}, {"counter", "edge", "fallend"},
	} {
		if f := field(c.kind, c.key); f.Default != c.def {
			t.Errorf("%s.%s: Vorgabe %q, erwartet %q", c.kind, c.key, f.Default, c.def)
		}
	}
	if f := field("sct013", "clamp"); !slices.Equal(f.Options, []string{"5A/1V", "30A/1V", "50A/1V", "100A/50mA"}) {
		t.Errorf("Zangentypen %v", f.Options)
	}
	if f := field("counter", "edge"); !slices.Equal(f.Options, []string{"steigend", "fallend"}) {
		t.Errorf("Flanken %v", f.Options)
	}
	// Pin-Auswahl je Feld: analog nur ADC-Pins
	b := mustBoard(t, "esp32dev")
	if got := b.PinsFor(field("sct013", "pin")); !slices.Equal(got, b.ADCPins) {
		t.Errorf("SCT-013-Pins %v", got)
	}
	if got := b.PinsFor(field("s0", "pin")); !slices.Equal(got, b.InputPins) {
		t.Errorf("S0-Pins %v", got)
	}
	if got := b.PinsFor(field("s0", "pulses")); got != nil {
		t.Errorf("Zahlenfeld mit Pins %v", got)
	}
}

func TestValidateBlocks(t *testing.T) {
	esp32 := mustBoard(t, "esp32dev")
	other := mustBoard(t, BoardOther)

	// in Ordnung
	ok := []Block{
		{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}},
		{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO34", "clamp": "30A/1V"}},
		{Kind: "pzem", Nr: 1, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO17"}},
		{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "gpio18", "edge": "fallend"}},
	}
	if msgs := ValidateBlocks(esp32, ok); len(msgs) > 0 {
		t.Fatalf("gültige Bausteine abgelehnt: %v", msgs)
	}

	// kein Baustein
	if msgs := ValidateBlocks(esp32, nil); !hasMsg(msgs, "Baustein") {
		t.Errorf("ohne Baustein: %v", msgs)
	}

	// SCT-013 an einem Pin ohne ADC
	msgs := ValidateBlocks(esp32, []Block{{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO4", "clamp": "30A/1V"}}})
	if !hasMsg(msgs, "Baustein 1", "GPIO4", "analog") {
		t.Errorf("SCT-013 an GPIO4: %v", msgs)
	}

	// derselbe Pin zweimal
	msgs = ValidateBlocks(esp32, []Block{
		{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}},
		{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "4"}},
	})
	if !hasMsg(msgs, "GPIO4", "mehrfach", "Baustein 1 und 2") {
		t.Errorf("Pin doppelt: %v", msgs)
	}

	// Pin, den die Platine nicht freigibt (Flash, nur Eingang ohne Pull-up)
	for _, pin := range []string{"GPIO6", "GPIO34", "GPIO99"} {
		msgs = ValidateBlocks(esp32, []Block{{Kind: "s0", Nr: 1, Params: map[string]string{"pin": pin}}})
		if !hasMsg(msgs, pin, "nicht nutzbar") {
			t.Errorf("S0 an %s: %v", pin, msgs)
		}
	}

	// other: keine Pin-Prüfung gegen eine Liste, nur Schreibweise und Doppelbelegung
	if msgs := ValidateBlocks(other, []Block{
		{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO4", "clamp": "5A/1V"}},
		{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO34"}},
	}); len(msgs) > 0 {
		t.Errorf("other mit freien Pins abgelehnt: %v", msgs)
	}
	if msgs := ValidateBlocks(other, []Block{{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4: x"}}}); !hasMsg(msgs, "Pin", "ungültig") {
		t.Errorf("other mit unsinnigem Pin: %v", msgs)
	}
	if msgs := ValidateBlocks(other, []Block{
		{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO5"}},
		{Kind: "s0", Nr: 2, Params: map[string]string{"pin": "5"}},
	}); !hasMsg(msgs, "GPIO5", "mehrfach") {
		t.Errorf("other mit doppeltem Pin: %v", msgs)
	}

	// Pflichtfelder, Zahlen, Auswahl
	for _, c := range []struct {
		name  string
		block Block
		parts []string
	}{
		{"unbekannt", Block{Kind: "relais"}, []string{"Baustein 1", "relais"}},
		{"Pin fehlt", Block{Kind: "s0", Nr: 1, Params: map[string]string{}}, []string{"Pin", "fehlt"}},
		{"Impulse 0", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4", "pulses": "0"}}, []string{"Impulse je kWh"}},
		{"Impulse mit Tausenderpunkt", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4", "pulses": "1.000"}}, []string{"Impulse je kWh", "ganze Zahl"}},
		{"Zange fehlt", Block{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO34"}}, []string{"Zangentyp", "fehlt"}},
		{"Zange falsch", Block{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO34", "clamp": "20A/1V"}}, []string{"Zangentyp", "20A/1V"}},
		{"Bürde fehlt", Block{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO34", "clamp": "100A/50mA"}}, []string{"Bürdenwiderstand", "100A/50mA"}},
		{"Spannung Text", Block{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO34", "clamp": "5A/1V", "voltage": "viel"}}, []string{"Netzspannung"}},
		{"Adresse 300", Block{Kind: "pzem", Nr: 1, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO17", "address": "300"}}, []string{"Adresse", "247"}},
		{"RX gleich TX", Block{Kind: "pzem", Nr: 1, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO16"}}, []string{"RX", "TX"}},
		{"Flanke falsch", Block{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "GPIO4", "edge": "beide"}}, []string{"Flanke", "beide"}},
	} {
		if msgs := ValidateBlocks(esp32, []Block{c.block}); !hasMsg(msgs, c.parts...) {
			t.Errorf("%s: %v", c.name, msgs)
		}
	}

	// Dezimalzahlen: Komma oder Punkt; mehrdeutige Schreibweisen (Tausenderpunkt) werden gemeldet
	sct := func(clamp, burden, voltage string) []Block {
		p := map[string]string{"pin": "GPIO34", "clamp": clamp, "burden": burden}
		if voltage != "" {
			p["voltage"] = voltage
		}
		return []Block{{Kind: "sct013", Nr: 1, Params: p}}
	}
	for _, burden := range []string{"22,5", "22.5", "33", "4,75"} {
		if msgs := ValidateBlocks(esp32, sct(clampCurrentOut, burden, "")); len(msgs) > 0 {
			t.Errorf("Bürde %s: %v", burden, msgs)
		}
	}
	for _, c := range []struct{ burden, voltage, field string }{
		{"1.000", "", "Bürdenwiderstand"},   // deutscher Tausenderpunkt, nicht 1 Ω
		{"1.000,5", "", "Bürdenwiderstand"}, // beide Trennzeichen
		{"33", "230.000", "Netzspannung"},
	} {
		if msgs := ValidateBlocks(esp32, sct(clampCurrentOut, c.burden, c.voltage)); !hasMsg(msgs, c.field, "nicht eindeutig") {
			t.Errorf("Bürde %q, Spannung %q: %v", c.burden, c.voltage, msgs)
		}
	}
	// Die Bürde zählt nur beim Stromausgang – bei anderen Zangen wird sie nicht geprüft.
	for _, burden := range []string{"abc", "1.000", "-5"} {
		if msgs := ValidateBlocks(esp32, sct("30A/1V", burden, "")); len(msgs) > 0 {
			t.Errorf("30A/1V mit Bürde %q: %v", burden, msgs)
		}
	}
	// „22,5“ und „22.5“ ergeben dieselbe Konfiguration
	comma := testDevice("esp32dev", sct(clampCurrentOut, "22,5", "")...)
	point := testDevice("esp32dev", sct(clampCurrentOut, "22.5", "")...)
	if ConfigHash(comma) == "" || ConfigHash(comma) != ConfigHash(point) {
		t.Error("22,5 und 22.5 ergeben verschiedene Konfigurationen")
	}

	// PZEM: mehrere an einem Anschluss mit verschiedenen Adressen sind erlaubt, gleiche Adresse nicht
	bus := func(nr int, addr string) Block {
		return Block{Kind: "pzem", Nr: nr, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO17", "address": addr}}
	}
	if msgs := ValidateBlocks(esp32, []Block{bus(1, "1"), bus(2, "2"), bus(3, "3")}); len(msgs) > 0 {
		t.Errorf("drei PZEM an einem Anschluss: %v", msgs)
	}
	if msgs := ValidateBlocks(esp32, []Block{bus(1, "1"), bus(2, "1")}); !hasMsg(msgs, "Baustein 2", "Adresse 1", "Baustein 1") {
		t.Errorf("gleiche Adresse: %v", msgs)
	}
	// halber Anschluss: RX gleich, TX verschieden → Pin doppelt belegt
	if msgs := ValidateBlocks(esp32, []Block{bus(1, "1"), {Kind: "pzem", Nr: 2, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO18", "address": "2"}}}); !hasMsg(msgs, "GPIO16", "mehrfach") {
		t.Errorf("RX geteilt, TX verschieden: %v", msgs)
	}
	// ESP32 hat neben dem Protokoll-Anschluss nur zwei serielle Anschlüsse
	msgs = ValidateBlocks(esp32, []Block{
		{Kind: "pzem", Nr: 1, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO17"}},
		{Kind: "pzem", Nr: 2, Params: map[string]string{"rx_pin": "GPIO18", "tx_pin": "GPIO19"}},
		{Kind: "pzem", Nr: 3, Params: map[string]string{"rx_pin": "GPIO21", "tx_pin": "GPIO22"}},
	})
	if !hasMsg(msgs, "2", "PZEM") {
		t.Errorf("drei PZEM-Anschlüsse am ESP32: %v", msgs)
	}

	// verkabelt auf Platine ohne Ethernet (Prüfung des ganzen Geräts)
	d := testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	d.Wired = true
	if msgs := ValidateDevice(d); !hasMsg(msgs, "Ethernet") {
		t.Errorf("verkabelt ohne Ethernet: %v", msgs)
	}
	d.BoardKey = "wt32-eth01"
	if msgs := ValidateDevice(d); len(msgs) > 0 {
		t.Errorf("verkabelt mit Ethernet: %v", msgs)
	}
	// Ethernet-Pins sind auf der Ethernet-Platine nicht frei
	d.Blocks = []Block{{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO18"}}}
	if msgs := ValidateDevice(d); !hasMsg(msgs, "GPIO18", "nicht nutzbar") {
		t.Errorf("S0 am MDIO-Pin: %v", msgs)
	}
}

func TestValidateDevice(t *testing.T) {
	base := func() Device {
		return testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	}
	if msgs := ValidateDevice(base()); len(msgs) > 0 {
		t.Fatalf("gültiges Gerät abgelehnt: %v", msgs)
	}
	for _, c := range []struct {
		name  string
		edit  func(*Device)
		parts []string
	}{
		{"Kennung", func(d *Device) { d.Slug = "Halle 1"; d.MQTTUser = "esppdh-Halle 1" }, []string{"Kennung"}},
		{"Platine", func(d *Device) { d.BoardKey = "esp99" }, []string{"Platine", "esp99"}},
		{"andere ohne Kennung", func(d *Device) { d.BoardKey = BoardOther }, []string{"Platinen-Kennung"}},
		{"andere unsinnig", func(d *Device) { d.BoardKey, d.CustomBoard = BoardOther, "esp32: x" }, []string{"Platinen-Kennung", "ungültig"}},
		{"kein WLAN", func(d *Device) { d.SSID = "" }, []string{"WLAN"}},
		{"WLAN-Name zu lang", func(d *Device) { d.SSID = strings.Repeat("ä", 17) }, []string{"WLAN-Name", "32"}},
		{"WLAN-Kennwort kurz", func(d *Device) { d.WLANPassword = "kurz" }, []string{"WLAN-Kennwort"}},
		{"Notzugang", func(d *Device) { d.APPassword = "" }, []string{"Notzugang"}},
		{"Broker leer", func(d *Device) { d.BrokerHost = "" }, []string{"Broker"}},
		{"Broker unsinnig", func(d *Device) { d.BrokerHost = "broker.example: x" }, []string{"Broker", "ungültig"}},
		{"Port", func(d *Device) { d.BrokerPort = 0 }, []string{"Port"}},
		{"Benutzer", func(d *Device) { d.MQTTUser = "pdh" }, []string{"MQTT-Benutzer", "esppdh-halle1-s0"}},
		{"MQTT-Kennwort", func(d *Device) { d.MQTTPassword = "" }, []string{"MQTT-Kennwort"}},
		{"TLS mit IP", func(d *Device) { d.BrokerTLS, d.BrokerHost = true, "10.10.0.15" }, []string{"TLS", "Namen"}},
		{"TLS am ESP8266", func(d *Device) {
			d.BrokerTLS, d.BoardKey = true, "d1-mini"
			d.Blocks = []Block{{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "D5"}}}
		}, []string{"ESP8266", "TLS"}},
		{"TLS an anderer ESP8266-Platine", func(d *Device) { d.BrokerTLS, d.BoardKey, d.CustomBoard = true, BoardOther, "nodemcuv2" }, []string{"ESP8266", "TLS"}},
		{"Baustein", func(d *Device) { d.Blocks = nil }, []string{"Baustein"}},
		// ungültiges UTF-8 (z. B. aus einer falsch kodierten Übernahme)
		{"Name UTF-8", func(d *Device) { d.Name = "Presse \xff 3" }, []string{"Name", "ungültige Zeichen"}},
		{"WLAN-Name UTF-8", func(d *Device) { d.SSID = "Werk\xc3" }, []string{"WLAN-Name", "ungültige Zeichen"}},
		{"WLAN-Kennwort UTF-8", func(d *Device) { d.WLANPassword = "kennwort\xfe12" }, []string{"WLAN-Kennwort", "ungültige Zeichen"}},
		{"Notzugang UTF-8", func(d *Device) { d.APPassword = "kennwort\xfe12" }, []string{"Notzugang", "ungültige Zeichen"}},
		{"MQTT-Kennwort UTF-8", func(d *Device) { d.MQTTPassword = "kennwort\xfe12" }, []string{"MQTT-Kennwort", "ungültige Zeichen"}},
	} {
		d := base()
		c.edit(&d)
		if msgs := ValidateDevice(d); !hasMsg(msgs, c.parts...) {
			t.Errorf("%s: %v", c.name, msgs)
		}
	}
	// offenes WLAN ohne Kennwort ist erlaubt, verborgenes WLAN auch
	d := base()
	d.WLANPassword, d.WLANHidden = "", true
	if msgs := ValidateDevice(d); len(msgs) > 0 {
		t.Errorf("offenes, verborgenes WLAN: %v", msgs)
	}
	// verkabelt braucht weder WLAN noch Notzugang
	d = base()
	d.BoardKey, d.Wired, d.SSID, d.WLANPassword, d.APPassword = "olimex-poe", true, "", "", ""
	if msgs := ValidateDevice(d); len(msgs) > 0 {
		t.Errorf("verkabelt ohne WLAN: %v", msgs)
	}
}

func TestBlockNumbers(t *testing.T) {
	esp32 := mustBoard(t, "esp32dev")
	s0 := func(nr int, pin string) Block {
		return Block{Kind: "s0", Nr: nr, Params: map[string]string{"pin": pin}}
	}
	for _, nr := range []int{0, -1} {
		if msgs := ValidateBlocks(esp32, []Block{s0(nr, "GPIO4")}); !hasMsg(msgs, "Baustein 1", "Nummer") {
			t.Errorf("Nummer %d: %v", nr, msgs)
		}
	}
	if msgs := ValidateBlocks(esp32, []Block{s0(1, "GPIO4"), s0(1, "GPIO5")}); !hasMsg(msgs, "Baustein 2", "Nummer 1", "Baustein 1") {
		t.Errorf("Nummer doppelt: %v", msgs)
	}
	// gleiche Nummer bei verschiedenen Arten und Lücken sind in Ordnung
	if msgs := ValidateBlocks(esp32, []Block{s0(2, "GPIO4"), s0(5, "GPIO5"),
		{Kind: "counter", Nr: 2, Params: map[string]string{"pin": "GPIO13"}}}); len(msgs) > 0 {
		t.Errorf("Lücken / andere Art: %v", msgs)
	}

	blocks := []Block{s0(1, "GPIO4"), s0(4, "GPIO5"), {Kind: "counter", Nr: 7}}
	for kind, want := range map[string]int{"s0": 5, "counter": 8, "pzem": 1} {
		if got := NextBlockNr(blocks, kind); got != want {
			t.Errorf("NextBlockNr(%s) = %d, erwartet %d", kind, got, want)
		}
	}
	if got := NextBlockNr(nil, "s0"); got != 1 {
		t.Errorf("NextBlockNr ohne Bausteine = %d", got)
	}
}

func TestSlugRule(t *testing.T) {
	for s, want := range map[string]bool{
		"Halle 1":               false,
		"halle1-s0":             true,
		"abc":                   true,
		"ab":                    false,
		strings.Repeat("a", 32): true,
		strings.Repeat("a", 33): false,
		"halle_1":               false,
		"hälle1":                false,
		"halle1\n":              false,
		"":                      false,
	} {
		if got := ValidSlug(s); got != want {
			t.Errorf("ValidSlug(%q) = %v, erwartet %v", s, got, want)
		}
	}
}
