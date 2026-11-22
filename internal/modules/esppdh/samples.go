package esppdh

// Musterkonfigurationen: je Platinenfamilie ein vollständiges Gerät mit festen Prüfwerten.
// Die Tests vergleichen sie mit den Erwartungsdateien unter testdata/; der Server baut sie
// später zur Probe einer neuen ESPHome-Version (esphome config). Die Kennwörter sind
// Prüfwerte, keine echten Zugangsdaten.

// Sample ist eine Musterkonfiguration.
type Sample struct {
	Name   string
	Device Device
}

// sampleDevice: gültiges WLAN-Gerät ohne TLS mit den angegebenen Bausteinen.
func sampleDevice(board string, blocks ...Block) Device {
	return Device{
		Slug: "halle1-s0", Name: "Presse 3", BoardKey: board,
		SSID: "Werk-Produktion", WLANPassword: "wlan-kennwort-1", APPassword: "notzugang-kennwort-1",
		BrokerHost: "mqtt.werk.example", BrokerPort: 1883,
		MQTTUser: "esppdh-halle1-s0", MQTTPassword: "mqtt-kennwort-1",
		WebServer: true, WebPassword: "web-kennwort-1", OTAPassword: "ota-kennwort-1",
		Blocks: blocks,
	}
}

// Samples liefert die Musterkonfigurationen in fester Reihenfolge.
func Samples() []Sample {
	// esp32dev mit S0-Zähler, TLS
	c1 := sampleDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4", "pulses": "1000"}})
	c1.Name = "Presse 3 / Halle 1"
	c1.BrokerTLS, c1.BrokerPort = true, 8883

	// D1 mini mit Stückzähler (fallende Flanke), ohne TLS
	c2 := sampleDevice("d1-mini", Block{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "D5", "edge": "fallend"}})
	c2.Slug, c2.MQTTUser, c2.Name = "linie2-takt", "esppdh-linie2-takt", "Linie 2 Takt"

	// WT32-ETH01 am Kabel mit zwei PZEM-004T an einem Anschluss, TLS
	c3 := sampleDevice("wt32-eth01",
		Block{Kind: "pzem", Nr: 1, Params: map[string]string{"rx_pin": "GPIO5", "tx_pin": "GPIO17"}},
		Block{Kind: "pzem", Nr: 2, Params: map[string]string{"rx_pin": "GPIO5", "tx_pin": "GPIO17", "address": "2"}},
	)
	c3.Slug, c3.MQTTUser, c3.Name = "verteiler-h2", "esppdh-verteiler-h2", "Verteiler Halle 2"
	c3.Wired, c3.SSID, c3.WLANPassword, c3.APPassword = true, "", "", ""
	c3.BrokerTLS, c3.BrokerPort = true, 8883

	// ESP32-S3 mit SCT-013 (Stromausgang, Bürde 20 Ω – passend bis 100 A) und S0-Zähler, verborgenes WLAN
	c4 := sampleDevice("esp32-s3",
		Block{Kind: "sct013", Nr: 1, Params: map[string]string{"pin": "GPIO1", "clamp": "100A/50mA", "burden": "20", "voltage": "230"}},
		Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4", "pulses": "800"}},
	)
	c4.Slug, c4.MQTTUser, c4.Name = "kompressor", "esppdh-kompressor", "Kompressor"
	c4.WLANHidden = true

	// ESP32-C3 mit Stückzähler, WLAN, ohne TLS
	c5 := sampleDevice("esp32-c3", Block{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "GPIO4", "edge": "steigend"}})
	c5.Slug, c5.MQTTUser, c5.Name = "stanze-c3", "esppdh-stanze-c3", "Stanze C3"

	return []Sample{
		{"esp32dev-s0", c1},
		{"d1-mini-counter", c2},
		{"wt32-eth01-pzem", c3},
		{"esp32-s3-sct013-s0", c4},
		{"esp32-c3-counter", c5},
	}
}
