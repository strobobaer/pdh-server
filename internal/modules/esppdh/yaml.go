package esppdh

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Erzeuger der ESPHome-Konfiguration. Die YAML-Dateien entstehen als Knotenbaum mit yaml.v3,
// nie per Textverkettung. Eingaben (Name, WLAN-Name, Broker, Platinen-Kennung, Kennwörter)
// stehen immer als !literal "…" (siehe literal): ESPHome liest sie so unverändert als Text,
// ohne Ersetzung von $-Ausdrücken. Kennwörter stehen nur in der Geheimnis-Datei; die
// Konfiguration verweist mit !secret darauf.
//
// Das Gerät spricht nur MQTT (keine ESPHome-API, kein OTA in Paket 1) und bleibt unter seinem
// Thema esppdh/<kennung>/: Client-ID gleich Benutzername (der Broker verlangt das), kein
// Home-Assistant-Discovery, kein discover_ip (esphome/discover, esphome/ping), kein
// MQTT-Protokoll. Zustand: esppdh/<kennung>/status (online/offline, Letzter Wille,
// gespeichert); Werte: esppdh/<kennung>/<baustein>-<n>/<wert>.

// Device: alles, was der Erzeuger für ein Gerät braucht.
type Device struct {
	Slug        string // Kennung, 3–32 Zeichen a–z, 0–9, „-“ (ValidSlug)
	Name        string // Anzeigename (friendly_name)
	BoardKey    string // Board.Key
	CustomBoard string // Platinen-Kennung von ESPHome bei BoardOther

	Wired        bool   // Ethernet statt WLAN (nur Platinen mit Ethernet)
	SSID         string // WLAN-Name
	WLANPassword string // leer = offenes WLAN
	WLANHidden   bool   // WLAN sendet seinen Namen nicht
	APPassword   string // Kennwort des Notzugang-WLANs (8–64 Zeichen), nur bei WLAN

	BrokerHost string // Name oder IPv4-Adresse des Brokers; bei TLS der Name
	BrokerPort int
	BrokerTLS  bool // verschlüsselt (mqtts); vertraut den Stammzertifikaten von Let's Encrypt

	MQTTUser     string // Benutzername am Broker, immer „esppdh-<Slug>“
	MQTTPassword string

	Blocks []Block
}

// Schlüssel der Geheimnis-Datei (secrets.yaml).
const (
	secretWiFi = "wifi_password"
	secretAP   = "ap_password"
	secretMQTT = "mqtt_password"
)

// Grenzen von ESPHome: Gerätename (esphome.name) höchstens 31 Zeichen, friendly_name höchstens
// 120 Byte ohne „/“ (core/config.py), WLAN-Name höchstens 32 Byte.
const (
	maxDeviceName   = 31
	maxFriendlyName = 120
	maxSSID         = 32
)

// Messwerte gehen alle 10 s an den Broker.
const interval = "10s"

var (
	slugRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$`)
	customBoardRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	hostRe        = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// ValidSlug: Kennung eines Geräts gültig? 3–32 Zeichen aus a–z, 0–9 und „-“, ohne „-“ am
// Anfang (sähe für ESPHome wie ein Schalter aus) und am Ende (deviceName schneidet es ab – „ab-“
// hieße dann wie „ab“, mit demselben Notzugang-WLAN).
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// deviceUser: Benutzername und Client-ID am Broker.
func deviceUser(slug string) string { return "esppdh-" + slug }

// deviceName: Gerätename für ESPHome und Name des Notzugang-WLANs („esppdh-<kennung>“). Bei
// langen Kennungen gekürzt: ESPHome erlaubt höchstens 31 Zeichen.
func deviceName(slug string) string {
	n := deviceUser(slug)
	if len(n) > maxDeviceName {
		n = n[:maxDeviceName]
	}
	return strings.TrimRight(n, "-")
}

// APName: Name des Notzugang-WLANs eines Geräts (wie im Erzeuger, für die Detailseite).
func APName(slug string) string { return deviceName(slug) }

// friendlyName: Anzeigename für ESPHome – „/“ ist dort verboten und wird zu „-“, höchstens
// 120 Byte (ohne ein Zeichen zu zerschneiden).
func friendlyName(name string) string {
	s := strings.TrimSpace(strings.ReplaceAll(name, "/", "-"))
	for len(s) > maxFriendlyName {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

// platformOf: Plattform eines Geräts ("esp32", "esp8266"; leer bei unbekannter Platine).
func platformOf(b Board, d Device) string {
	if b.Key == BoardOther {
		if !customBoardRe.MatchString(d.CustomBoard) {
			return ""
		}
		return customPlatform(d.CustomBoard)
	}
	return b.Platform
}

// PlatformOf: Plattform einer Platine ("esp32", "esp8266"); bei „Andere Platine“ aus der
// Platinen-Kennung von ESPHome. Leer bei unbekannter Platine oder ungültiger Kennung. Die
// Seiten brauchen das, weil nur ESP32 über TLS verbinden kann.
func PlatformOf(boardKey, customBoard string) string {
	b, ok := BoardByKey(boardKey)
	if !ok {
		return ""
	}
	return platformOf(b, Device{CustomBoard: customBoard})
}

// ValidateDevice prüft ein ganzes Gerät (Kennung, Platine, Netz, Broker, Bausteine).
// Rückgabe: Meldungen auf Deutsch, leer = in Ordnung. Generate baut nur gültige Geräte.
func ValidateDevice(d Device) []string {
	var msgs []string
	add := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }

	// Ungültiges UTF-8 (z. B. aus einer falsch kodierten Übernahme) kann yaml.v3 nicht schreiben.
	utf8OK := func(v, what string) bool {
		if !utf8.ValidString(v) {
			add("%s enthält ungültige Zeichen.", what)
			return false
		}
		return true
	}

	if !ValidSlug(d.Slug) {
		add("Die Kennung muss 3 bis 32 Zeichen lang sein, darf nur a–z, 0–9 und „-“ enthalten und nicht mit „-“ beginnen oder enden.")
	}
	utf8OK(d.Name, "Der Name")
	b, ok := BoardByKey(d.BoardKey)
	switch {
	case !ok:
		add("Die Platine „%s“ gibt es nicht.", short(d.BoardKey))
	case b.Key == BoardOther && d.CustomBoard == "":
		add("Bei „Andere Platine“ die Platinen-Kennung von ESPHome eintragen, z. B. nodemcuv2.")
	case b.Key == BoardOther && !customBoardRe.MatchString(d.CustomBoard):
		add("Die Platinen-Kennung „%s“ ist ungültig.", short(d.CustomBoard))
	}
	platform := platformOf(b, d)

	if d.Wired {
		if ok && !b.Ethernet {
			add("Diese Platine hat kein Ethernet – bitte ein WLAN wählen.")
		}
	} else {
		switch {
		case d.SSID == "":
			add("Kein WLAN gewählt.")
		case !utf8OK(d.SSID, "Der WLAN-Name"):
		case len(d.SSID) > maxSSID:
			add("Der WLAN-Name ist länger als %d Byte.", maxSSID)
		}
		if n := utf8.RuneCountInString(d.WLANPassword); utf8OK(d.WLANPassword, "Das WLAN-Kennwort") && d.WLANPassword != "" && (n < 8 || n > 64) {
			add("Das WLAN-Kennwort muss 8 bis 64 Zeichen haben.")
		}
		if n := utf8.RuneCountInString(d.APPassword); utf8OK(d.APPassword, "Das Kennwort für den Notzugang") && (n < 8 || n > 64) {
			add("Das Kennwort für den Notzugang muss 8 bis 64 Zeichen haben.")
		}
	}

	switch {
	case d.BrokerHost == "":
		add("Die Broker-Adresse fehlt.")
	case len(d.BrokerHost) > 253 || !hostRe.MatchString(d.BrokerHost):
		add("Die Broker-Adresse „%s“ ist ungültig.", short(d.BrokerHost))
	}
	if d.BrokerPort < 1 || d.BrokerPort > 65535 {
		add("Der Broker-Port %d ist ungültig.", d.BrokerPort)
	}
	if d.BrokerTLS {
		// ESPHome kann MQTT über TLS nur auf ESP32 (certificate_authority); ESP8266 bliebe
		// sonst stillschweigend unverschlüsselt.
		if platform == "esp8266" {
			add("ESP8266-Platinen können sich nicht verschlüsselt (TLS) mit dem Broker verbinden – bitte eine ESP32-Platine wählen.")
		}
		if net.ParseIP(d.BrokerHost) != nil {
			add("Für TLS braucht das Gerät den Namen des Brokers, keine IP-Adresse – das Zertifikat lautet auf den Namen.")
		}
	}
	if ValidSlug(d.Slug) && d.MQTTUser != deviceUser(d.Slug) {
		add("Der MQTT-Benutzer muss „%s“ heißen.", deviceUser(d.Slug))
	}
	if d.MQTTPassword == "" {
		add("Das MQTT-Kennwort fehlt.")
	} else {
		utf8OK(d.MQTTPassword, "Das MQTT-Kennwort")
	}
	if ok {
		msgs = append(msgs, ValidateBlocks(b, d.Blocks)...)
	}
	return msgs
}

// Generate erzeugt die ESPHome-Konfiguration und die Geheimnis-Datei (secrets.yaml) eines
// Geräts. config enthält Kennwörter nur als !secret-Verweise. Ungültige Geräte (ValidateDevice)
// ergeben einen Fehler mit allen Meldungen.
func Generate(d Device) (config, secrets []byte, err error) {
	if msgs := ValidateDevice(d); len(msgs) > 0 {
		return nil, nil, errors.New(strings.Join(msgs, " "))
	}
	b, _ := BoardByKey(d.BoardKey)
	blocks, _ := resolveBlocks(b, d.Blocks)
	platform := platformOf(b, d)
	board := plain(b.ESPHomeBoard)
	if b.Key == BoardOther {
		board = literal(d.CustomBoard)
	}

	esphome := newMap().add("name", plain(deviceName(d.Slug)))
	if fn := friendlyName(d.Name); fn != "" {
		esphome.add("friendly_name", literal(fn))
	}
	root := newMap().
		add("esphome", esphome.Node).
		add(platform, newMap().add("board", board).Node).
		add("logger", empty())
	if d.Wired {
		root.add("ethernet", ethernetNode(b.eth))
	} else {
		root.add("wifi", wifiNode(d)).add("captive_portal", empty())
	}
	root.add("mqtt", mqttNode(d))

	var uarts, modbus, sensors []*yaml.Node
	seenBus := map[int]bool{}
	for _, r := range blocks {
		if r.def.Kind == KindPZEM && !seenBus[r.bus] {
			seenBus[r.bus] = true
			uarts = append(uarts, newMap().
				add("id", plain(fmt.Sprintf("pzem_uart_%d", r.bus))).
				add("rx_pin", plain(r.p["rx_pin"])).
				add("tx_pin", plain(r.p["tx_pin"])).
				add("baud_rate", plain("9600")).Node)
			modbus = append(modbus, newMap().
				add("id", plain(fmt.Sprintf("pzem_modbus_%d", r.bus))).
				add("uart_id", plain(fmt.Sprintf("pzem_uart_%d", r.bus))).Node)
		}
		sensors = append(sensors, blockSensors(d.Slug, r, platform, b.adcScale)...)
	}
	if len(uarts) > 0 {
		root.add("uart", seq(uarts...)).add("modbus", seq(modbus...))
	}
	root.add("sensor", seq(sensors...))

	sec := newMap()
	if !d.Wired {
		if d.WLANPassword != "" {
			sec.add(secretWiFi, literal(d.WLANPassword))
		}
		sec.add(secretAP, literal(d.APPassword))
	}
	sec.add(secretMQTT, literal(d.MQTTPassword))

	if config, err = encode(root.Node, "ESPPDH: vom PDH erzeugt – Änderungen gehen beim nächsten Bau verloren.\n"+
		"Kennwörter stehen nicht hier, sondern als !secret in der Geheimnis-Datei.", true); err != nil {
		return nil, nil, err
	}
	if secrets, err = encode(sec.Node, "ESPPDH: Geheimnis-Datei – nur für den Bau, nicht weitergeben.", false); err != nil {
		return nil, nil, err
	}
	return config, secrets, nil
}

// ConfigHash: SHA-256 über Konfiguration und Geheimnis-Datei (hex). Ändert sich etwas davon,
// passt die gebaute Firmware nicht mehr. Leer bei ungültigem Gerät.
func ConfigHash(d Device) string {
	cfg, sec, err := Generate(d)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write(cfg)
	h.Write([]byte{0}) // Trenner: YAML enthält kein NUL-Byte
	h.Write(sec)
	return hex.EncodeToString(h.Sum(nil))
}

func ethernetNode(e *ethernetPHY) *yaml.Node {
	return newMap().
		add("type", plain(e.typ)).
		add("mdc_pin", plain(e.mdc)).
		add("mdio_pin", plain(e.mdio)).
		add("clk", newMap().add("mode", plain(e.clkMode)).add("pin", plain(e.clkPin)).Node).
		add("phy_addr", plain(strconv.Itoa(e.phyAddr))).
		add("power_pin", plain(e.powerPin)).Node
}

// wifiNode: WLAN des Geräts und Notzugang (eigenes WLAN „esppdh-<kennung>“, wenn das Gerät
// sein WLAN nicht findet; mit captive_portal lässt sich dort ein anderes WLAN eintragen).
func wifiNode(d Device) *yaml.Node {
	nw := newMap().add("ssid", literal(d.SSID))
	if d.WLANPassword != "" {
		nw.add("password", secretRef(secretWiFi))
	}
	if d.WLANHidden {
		nw.add("hidden", plain("true"))
	}
	return newMap().
		add("networks", seq(nw.Node)).
		add("ap", newMap().add("ssid", plain(deviceName(d.Slug))).add("password", secretRef(secretAP)).Node).Node
}

func mqttNode(d Device) *yaml.Node {
	prefix := "esppdh/" + d.Slug
	msg := func(payload string) *yaml.Node {
		return newMap().
			add("topic", plain(prefix+"/status")).
			add("payload", plain(payload)).
			add("retain", plain("true")).Node
	}
	m := newMap().
		add("broker", literal(d.BrokerHost)).
		add("port", plain(strconv.Itoa(d.BrokerPort))).
		add("username", plain(d.MQTTUser)).
		add("password", secretRef(secretMQTT)).
		add("client_id", plain(d.MQTTUser)).
		add("topic_prefix", plain(prefix)).
		add("discovery", plain("false")).
		add("discover_ip", plain("false")).
		add("log_topic", plain("null")).
		// Ohne Broker nicht neu starten (Vorgabe von ESPHome: nach 15 min): Zählerstand und
		// Stückzahl begännen sonst bei jedem längeren Ausfall des PDH (Update, Umschaltung) bei 0.
		// Das Gerät zählt weiter und verbindet sich neu, sobald der Broker wieder da ist. Das WLAN
		// startet wegen des Notzugangs (ap:) ohnehin nie neu (wifi_component.cpp: nur ohne AP).
		add("reboot_timeout", plain("0s")).
		add("birth_message", msg("online")).
		add("will_message", msg("offline")).
		add("shutdown_message", msg("offline"))
	if d.BrokerTLS {
		// Mit certificate_authority verbindet ESPHome auf ESP32 über TLS und prüft Zertifikat
		// und Namen des Brokers; der eingebaute Broker hat sein Zertifikat von Let's Encrypt.
		m.add("certificate_authority", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.LiteralStyle, Value: letsEncryptRoots})
	}
	return m.Node
}

// blockSensors: Sensoren eines Bausteins. adcScale: Spannungsteiler vor dem Analog-Pin (0 = keiner).
func blockSensors(slug string, r resolvedBlock, platform string, adcScale float64) []*yaml.Node {
	topic := func(value string) *yaml.Node {
		return plain(fmt.Sprintf("esppdh/%s/%s-%d/%s", slug, r.def.Kind, r.nr, value))
	}
	name := func(value string) *yaml.Node { return plain(fmt.Sprintf("%s %d %s", r.def.Label, r.nr, value)) }
	id := func(suffix string) *yaml.Node { return plain(fmt.Sprintf("%s_%d_%s", r.def.Kind, r.nr, suffix)) }
	num := func(key string) float64 { f, _ := strconv.ParseFloat(r.p[key], 64); return f }
	filter := func(kind, value string) *yaml.Node { return newMap().add(kind, plain(value)).Node }

	switch r.def.Kind {
	case KindS0:
		// pulse_meter misst Impulse je Minute aus dem Abstand der Impulse:
		// W = Impulse/min · 60 min/h · 1000 W/kW / (Impulse je kWh). Ein S0-Impuls dauert
		// mindestens 30 ms (DIN EN 62053-31) – 20 ms Filter verwirft nur Störungen.
		imp := num("pulses")
		return []*yaml.Node{newMap().
			add("platform", plain("pulse_meter")).
			add("name", name("Leistung")).
			add("pin", inputPin(r.p["pin"])).
			add("internal_filter", plain("20ms")).
			add("unit_of_measurement", plain("W")).
			add("device_class", plain("power")).
			add("state_class", plain("measurement")).
			add("accuracy_decimals", plain("0")).
			add("filters", seq(filter("multiply", fnum(60000/imp)), filter("throttle_average", interval))).
			add("state_topic", topic("leistung")).
			add("total", newMap().
				add("name", name("Zählerstand")).
				add("unit_of_measurement", plain("kWh")).
				add("device_class", plain("energy")).
				add("state_class", plain("total_increasing")).
				add("accuracy_decimals", plain("3")).
				add("filters", seq(filter("multiply", fnum(1/imp)), filter("heartbeat", interval))).
				add("state_topic", topic("zaehlerstand")).Node).Node}

	case KindSCT013:
		// ct_clamp liefert den Effektivwert der Wechselspannung am Analog-Pin (ohne
		// Vorspannung); mal A je V der Zange (bzw. 2000/Bürde) ergibt den Strom.
		factor := clampAmpsPerVolt[r.p["clamp"]]
		if r.p["clamp"] == clampCurrentOut {
			factor = sct013Turns / num("burden")
		}
		if adcScale > 0 {
			factor *= adcScale
		}
		adc := newMap().
			add("platform", plain("adc")).
			add("id", id("adc")).
			add("pin", plain(r.p["pin"]))
		if platform == "esp32" {
			adc.add("attenuation", plain("12db")) // Messbereich bis etwa 3,1 V
		}
		adc.add("internal", plain("true")).add("update_interval", plain("never"))
		current := newMap().
			add("platform", plain("ct_clamp")).
			add("id", id("strom")).
			add("name", name("Strom")).
			add("sensor", id("adc")).
			add("update_interval", plain(interval)).
			add("filters", seq(filter("multiply", fnum(factor)))).
			add("state_topic", topic("strom"))
		apparent := newMap().
			add("platform", plain("copy")).
			add("source_id", id("strom")).
			add("name", name("Scheinleistung")).
			add("unit_of_measurement", plain("VA")).
			add("device_class", plain("apparent_power")).
			add("state_class", plain("measurement")).
			add("accuracy_decimals", plain("0")).
			add("filters", seq(filter("multiply", r.p["voltage"]))).
			add("state_topic", topic("scheinleistung"))
		return []*yaml.Node{adc.Node, current.Node, apparent.Node}

	case KindPZEM:
		value := func(label, t string) *yaml.Node {
			return newMap().add("name", name(label)).add("state_topic", topic(t)).Node
		}
		// Der PZEM zählt die Energie in Wh, dauerhaft im Messgerät; gesendet wird kWh.
		energy := newMap().
			add("name", name("Energie")).
			add("unit_of_measurement", plain("kWh")).
			add("accuracy_decimals", plain("3")).
			add("filters", seq(filter("multiply", "0.001"))).
			add("state_topic", topic("energie"))
		return []*yaml.Node{newMap().
			add("platform", plain("pzemac")).
			add("modbus_id", plain(fmt.Sprintf("pzem_modbus_%d", r.bus))).
			add("address", plain(r.p["address"])).
			add("update_interval", plain(interval)).
			add("voltage", value("Spannung", "spannung")).
			add("current", value("Strom", "strom")).
			add("power", value("Leistung", "leistung")).
			add("energy", energy.Node).Node}

	case KindCounter:
		// pulse_counter in Software: Flanken innerhalb der Entprellzeit nach der letzten
		// Flanke zählen nicht. Die Hardware-Zähler (PCNT) der ESP32 filtern höchstens 13 µs.
		rising, falling := "INCREMENT", "DISABLE"
		if r.p["edge"] == "fallend" {
			rising, falling = "DISABLE", "INCREMENT"
		}
		c := newMap().
			add("platform", plain("pulse_counter")).
			add("id", id("takt")).
			add("internal", plain("true")).
			add("pin", inputPin(r.p["pin"])).
			add("count_mode", newMap().add("rising_edge", plain(rising)).add("falling_edge", plain(falling)).Node)
		if platform == "esp32" {
			c.add("use_pcnt", plain("false"))
		}
		return []*yaml.Node{c.
			add("internal_filter", plain(r.p["debounce"]+"ms")).
			add("update_interval", plain(interval)).
			add("total", newMap().
				add("name", name("Stückzahl")).
				add("unit_of_measurement", plain("Stk")).
				add("state_topic", topic("stueckzahl")).Node).Node}
	}
	return nil
}

// inputPin: digitaler Eingang mit internem Pull-up (S0-Ausgang und Sensoren mit offenem
// Kollektor ziehen gegen GND).
func inputPin(pin string) *yaml.Node {
	return newMap().
		add("number", plain(pin)).
		add("mode", newMap().add("input", plain("true")).add("pullup", plain("true")).Node).Node
}

// --- YAML-Knoten ---

// ymap: Zuordnung mit fester Reihenfolge der Schlüssel.
type ymap struct{ *yaml.Node }

func newMap() ymap { return ymap{&yaml.Node{Kind: yaml.MappingNode}} }

func (m ymap) add(key string, v *yaml.Node) ymap {
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	return m
}

func seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Content: items}
}

// plain: fester, vom Erzeuger gebildeter Wert (Zahl, Pin, Thema …) ohne Anführungszeichen.
func plain(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

// literal: Eingabe als !literal "…". ESPHome wertet sonst „$name“ und „${…}“ in jeder
// Zeichenkette als Jinja-Ausdruck aus – auch ohne substitutions: und auch in Werten aus der
// Geheimnis-Datei. Die Jinja-Umgebung ist nicht abgeschottet: Ein Gerätename wie
// „${ lipsum.__globals__ … }“ liefe beim Bauen als Python-Code am Server. !literal schließt
// den Wert von jeder Ersetzung aus; die doppelten Anführungszeichen halten ihn für YAML 1.1
// (yes/on/…) als Text.
func literal(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!literal", Style: yaml.DoubleQuotedStyle, Value: v}
}

// secretRef: Verweis !secret <schlüssel> auf die Geheimnis-Datei.
func secretRef(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!secret", Value: key}
}

// empty: Abschnitt ohne Einstellungen („logger:“).
func empty() *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"} }

// encode schreibt einen Knotenbaum mit Kopfkommentar und zwei Leerzeichen Einzug; mit
// sections steht vor jedem Hauptabschnitt eine Leerzeile.
func encode(root *yaml.Node, head string, sections bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, HeadComment: head, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if !sections {
		return buf.Bytes(), nil
	}
	// In Spalte 0 stehen nur Hauptabschnitte, Kommentare und Leerzeilen: Werte darunter sind
	// eingerückt (auch mehrzeilige Zeichenketten).
	var out bytes.Buffer
	lines := bytes.SplitAfter(buf.Bytes(), []byte("\n"))
	for i, l := range lines {
		if i > 0 && len(l) > 0 && strings.IndexByte(" #\n-", l[0]) < 0 &&
			!bytes.Equal(lines[i-1], []byte("\n")) && !bytes.HasPrefix(lines[i-1], []byte("#")) {
			out.WriteByte('\n')
		}
		out.Write(l)
	}
	return out.Bytes(), nil
}

// letsEncryptRoots: Stammzertifikate von Let's Encrypt, ISRG Root X1 (RSA, bis 2035) und
// ISRG Root X2 (ECDSA, bis 2040), aus dem CA-Bündel von certifi im ESPHome-Abbild 2026.10.2.
// Der eingebaute Broker bezieht sein Zertifikat von Let's Encrypt (ECDSA, Kette zu X2, über
// die Querzertifizierung auch zu X1). Zertifikate der Testumgebung (staging) prüfen damit nicht.
const letsEncryptRoots = `-----BEGIN CERTIFICATE-----
MIIFazCCA1OgAwIBAgIRAIIQz7DSQONZRGPgu2OCiwAwDQYJKoZIhvcNAQELBQAw
TzELMAkGA1UEBhMCVVMxKTAnBgNVBAoTIEludGVybmV0IFNlY3VyaXR5IFJlc2Vh
cmNoIEdyb3VwMRUwEwYDVQQDEwxJU1JHIFJvb3QgWDEwHhcNMTUwNjA0MTEwNDM4
WhcNMzUwNjA0MTEwNDM4WjBPMQswCQYDVQQGEwJVUzEpMCcGA1UEChMgSW50ZXJu
ZXQgU2VjdXJpdHkgUmVzZWFyY2ggR3JvdXAxFTATBgNVBAMTDElTUkcgUm9vdCBY
MTCCAiIwDQYJKoZIhvcNAQEBBQADggIPADCCAgoCggIBAK3oJHP0FDfzm54rVygc
h77ct984kIxuPOZXoHj3dcKi/vVqbvYATyjb3miGbESTtrFj/RQSa78f0uoxmyF+
0TM8ukj13Xnfs7j/EvEhmkvBioZxaUpmZmyPfjxwv60pIgbz5MDmgK7iS4+3mX6U
A5/TR5d8mUgjU+g4rk8Kb4Mu0UlXjIB0ttov0DiNewNwIRt18jA8+o+u3dpjq+sW
T8KOEUt+zwvo/7V3LvSye0rgTBIlDHCNAymg4VMk7BPZ7hm/ELNKjD+Jo2FR3qyH
B5T0Y3HsLuJvW5iB4YlcNHlsdu87kGJ55tukmi8mxdAQ4Q7e2RCOFvu396j3x+UC
B5iPNgiV5+I3lg02dZ77DnKxHZu8A/lJBdiB3QW0KtZB6awBdpUKD9jf1b0SHzUv
KBds0pjBqAlkd25HN7rOrFleaJ1/ctaJxQZBKT5ZPt0m9STJEadao0xAH0ahmbWn
OlFuhjuefXKnEgV4We0+UXgVCwOPjdAvBbI+e0ocS3MFEvzG6uBQE3xDk3SzynTn
jh8BCNAw1FtxNrQHusEwMFxIt4I7mKZ9YIqioymCzLq9gwQbooMDQaHWBfEbwrbw
qHyGO0aoSCqI3Haadr8faqU9GY/rOPNk3sgrDQoo//fb4hVC1CLQJ13hef4Y53CI
rU7m2Ys6xt0nUW7/vGT1M0NPAgMBAAGjQjBAMA4GA1UdDwEB/wQEAwIBBjAPBgNV
HRMBAf8EBTADAQH/MB0GA1UdDgQWBBR5tFnme7bl5AFzgAiIyBpY9umbbjANBgkq
hkiG9w0BAQsFAAOCAgEAVR9YqbyyqFDQDLHYGmkgJykIrGF1XIpu+ILlaS/V9lZL
ubhzEFnTIZd+50xx+7LSYK05qAvqFyFWhfFQDlnrzuBZ6brJFe+GnY+EgPbk6ZGQ
3BebYhtF8GaV0nxvwuo77x/Py9auJ/GpsMiu/X1+mvoiBOv/2X/qkSsisRcOj/KK
NFtY2PwByVS5uCbMiogziUwthDyC3+6WVwW6LLv3xLfHTjuCvjHIInNzktHCgKQ5
ORAzI4JMPJ+GslWYHb4phowim57iaztXOoJwTdwJx4nLCgdNbOhdjsnvzqvHu7Ur
TkXWStAmzOVyyghqpZXjFaH3pO3JLF+l+/+sKAIuvtd7u+Nxe5AW0wdeRlN8NwdC
jNPElpzVmbUq4JUagEiuTDkHzsxHpFKVK7q4+63SM1N95R1NbdWhscdCb+ZAJzVc
oyi3B43njTOQ5yOf+1CceWxG1bQVs5ZufpsMljq4Ui0/1lvh+wjChP4kqKOJ2qxq
4RgqsahDYVvTH9w7jXbyLeiNdd8XM2w9U/t7y0Ff/9yi0GE44Za4rF2LN9d11TPA
mRGunUHBcnWEvgJBQl9nJEiU0Zsnvgc/ubhPgXRR4Xq37Z0j4r7g1SgEEzwxA57d
emyPxgcYxn/eR44/KJ4EBs+lVDR3veyJm+kXQ99b21/+jh5Xos1AnX5iItreGCc=
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
MIICGzCCAaGgAwIBAgIQQdKd0XLq7qeAwSxs6S+HUjAKBggqhkjOPQQDAzBPMQsw
CQYDVQQGEwJVUzEpMCcGA1UEChMgSW50ZXJuZXQgU2VjdXJpdHkgUmVzZWFyY2gg
R3JvdXAxFTATBgNVBAMTDElTUkcgUm9vdCBYMjAeFw0yMDA5MDQwMDAwMDBaFw00
MDA5MTcxNjAwMDBaME8xCzAJBgNVBAYTAlVTMSkwJwYDVQQKEyBJbnRlcm5ldCBT
ZWN1cml0eSBSZXNlYXJjaCBHcm91cDEVMBMGA1UEAxMMSVNSRyBSb290IFgyMHYw
EAYHKoZIzj0CAQYFK4EEACIDYgAEzZvVn4CDCuwJSvMWSj5cz3es3mcFDR0HttwW
+1qLFNvicWDEukWVEYmO6gbf9yoWHKS5xcUy4APgHoIYOIvXRdgKam7mAHf7AlF9
ItgKbppbd9/w+kHsOdx1ymgHDB/qo0IwQDAOBgNVHQ8BAf8EBAMCAQYwDwYDVR0T
AQH/BAUwAwEB/zAdBgNVHQ4EFgQUfEKWrt5LSDv6kviejM9ti6lyN5UwCgYIKoZI
zj0EAwMDaAAwZQIwe3lORlCEwkSHRhtFcP9Ymd70/aTSVaYgLXTWNLxBo1BfASdW
tL4ndQavEi51mI38AjEAi/V3bNTIZargCyzuFJ0nN6T5U6VR5CmD1/iQMVtCnwr1
/q4AaOeMSQ+2b1tbFfLn
-----END CERTIFICATE-----
`
