package esppdh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Erzeuger der ESPHome-Konfiguration (yaml.go). Die Erwartungsdateien liegen unter
// testdata/<fall>.yaml (Konfiguration) und testdata/<fall>.secrets.yaml (Geheimnis-Datei mit
// Test-Kennwörtern). Neu schreiben: go test ./internal/modules/esppdh -run TestGenerateGolden -update

var update = flag.Bool("update", false, "Erwartungsdateien neu schreiben")

// testDevice: gültiges WLAN-Gerät ohne TLS mit den angegebenen Bausteinen.
func testDevice(board string, blocks ...Block) Device { return sampleDevice(board, blocks...) }

// readGolden liest eine Erwartungsdatei; Zeilenenden aus einem Windows-Checkout zählen nicht.
func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (neu schreiben mit -update)", err)
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func TestGenerateGolden(t *testing.T) {
	for _, c := range Samples() {
		t.Run(c.Name, func(t *testing.T) {
			cfg, sec, err := Generate(c.Device)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range []struct {
				path string
				got  []byte
			}{
				{filepath.Join("testdata", c.Name+".yaml"), cfg},
				{filepath.Join("testdata", c.Name+".secrets.yaml"), sec},
			} {
				if *update {
					if err := os.WriteFile(f.path, f.got, 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if want := readGolden(t, f.path); !bytes.Equal(f.got, want) {
					t.Errorf("%s weicht ab.\n--- erzeugt ---\n%s\n--- erwartet ---\n%s", f.path, f.got, want)
				}
			}
			// dieselbe Eingabe ergibt dieselbe Datei
			cfg2, sec2, _ := Generate(c.Device)
			if !bytes.Equal(cfg, cfg2) || !bytes.Equal(sec, sec2) {
				t.Error("zweiter Lauf erzeugt andere Dateien")
			}
		})
	}
}

// parseYAML liest eine erzeugte Datei als Baum; die Wurzel ist eine Zuordnung.
func parseYAML(t *testing.T, b []byte) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("kein gültiges YAML: %v\n%s", err, b)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("Wurzel ist keine Zuordnung:\n%s", b)
	}
	return doc.Content[0]
}

// keys: Schlüssel einer Zuordnung in Reihenfolge.
func keys(n *yaml.Node) []string {
	var out []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}

// get: Wert zum Schlüssel einer Zuordnung (nil = fehlt).
func get(n *yaml.Node, key string) *yaml.Node {
	for i := 0; n != nil && i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func TestGenerateNoSecretsInConfig(t *testing.T) {
	d := testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	cfg, sec, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{d.WLANPassword, d.MQTTPassword, d.APPassword, d.WebPassword} {
		if bytes.Contains(cfg, []byte(pw)) {
			t.Errorf("Konfiguration enthält das Kennwort %q", pw)
		}
		if !bytes.Contains(sec, []byte(pw)) {
			t.Errorf("Geheimnis-Datei ohne Kennwort %q", pw)
		}
	}
	// Kennwörter stehen als !secret-Verweis in der Konfiguration
	root := parseYAML(t, cfg)
	for _, c := range []struct {
		node *yaml.Node
		key  string
	}{
		{get(get(root, "wifi"), "networks").Content[0], "wifi_password"},
		{get(get(root, "wifi"), "ap"), "ap_password"},
		{get(root, "mqtt"), "mqtt_password"},
	} {
		pw := get(c.node, "password")
		if pw == nil || pw.Tag != "!secret" || pw.Value != c.key {
			t.Errorf("password ist kein Verweis !secret %s: %+v", c.key, pw)
		}
	}
	// Geheimnis-Datei: genau die vier Schlüssel (mit der Webseite des Geräts)
	if got := keys(parseYAML(t, sec)); !slices.Equal(got, []string{"wifi_password", "ap_password", "mqtt_password", "web_password", "ota_password"}) {
		t.Errorf("Schlüssel der Geheimnis-Datei: %v", got)
	}
	// verkabelt: nur das MQTT-Kennwort
	w := testDevice("wt32-eth01", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	w.Wired = true
	_, sec, err = Generate(w)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(parseYAML(t, sec)); !slices.Equal(got, []string{"mqtt_password", "web_password", "ota_password"}) {
		t.Errorf("verkabelt, Schlüssel der Geheimnis-Datei: %v", got)
	}
}

func TestGenerateEscapesInput(t *testing.T) {
	d := testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	d.Name = "x: y\n  lambda: |-\n    boom"
	d.SSID = "Werk #1 'ost'"
	d.WLANPassword = `pw: "x" #1 'y' {a: b} ${ 7*7 }`
	d.MQTTPassword = "yes"
	d.APPassword = "  ~ null & *a !lambda $x"
	cfg, sec, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	root := parseYAML(t, cfg)
	if got := keys(root); !slices.Equal(got, []string{"esphome", "esp32", "logger", "wifi", "captive_portal", "web_server", "ota", "safe_mode", "mqtt", "sensor", "text_sensor"}) {
		t.Errorf("Hauptabschnitte: %v", got)
	}
	esp := get(root, "esphome")
	if got := keys(esp); !slices.Equal(got, []string{"name", "friendly_name"}) {
		t.Errorf("Schlüssel unter esphome: %v", got)
	}
	wifi := get(root, "wifi")
	if got := keys(wifi); !slices.Equal(got, []string{"networks", "ap"}) {
		t.Errorf("Schlüssel unter wifi: %v", got)
	}
	nets := get(wifi, "networks")
	if nets == nil || len(nets.Content) != 1 {
		t.Fatalf("networks: %+v", nets)
	}
	if got := keys(nets.Content[0]); !slices.Equal(got, []string{"ssid", "password"}) {
		t.Errorf("Schlüssel des WLANs: %v", got)
	}
	// Werte kommen unverändert als !literal "…" zurück – so liest sie auch ESPHome als Text:
	// ohne Auswertung von $-Ausdrücken (Jinja) und ohne YAML-1.1-Deutung (yes = Wahrheitswert).
	str := func(n *yaml.Node, want, what string) {
		t.Helper()
		if n == nil || n.Kind != yaml.ScalarNode || n.Value != want || n.Tag != "!literal" || n.Style&yaml.DoubleQuotedStyle == 0 {
			t.Errorf("%s: %+v, erwartet !literal %q in doppelten Anführungszeichen", what, n, want)
		}
	}
	str(get(esp, "friendly_name"), d.Name, "friendly_name")
	str(get(nets.Content[0], "ssid"), d.SSID, "ssid")
	str(get(get(root, "mqtt"), "broker"), d.BrokerHost, "broker")

	// ein Name mit Jinja-Ausdruck bleibt ebenfalls Text
	d.Name = "Presse ${ 7*7 } $name"
	cfg2, _, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	str(get(get(parseYAML(t, cfg2), "esphome"), "friendly_name"), d.Name, "friendly_name mit $")

	// andere Platine: die Platinen-Kennung ist ebenfalls Eingabe
	o := testDevice(BoardOther, Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	o.CustomBoard = "nodemcuv2"
	cfg3, _, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	str(get(get(parseYAML(t, cfg3), "esp8266"), "board"), o.CustomBoard, "board")

	secRoot := parseYAML(t, sec)
	if got := keys(secRoot); !slices.Equal(got, []string{"wifi_password", "ap_password", "mqtt_password", "web_password", "ota_password"}) {
		t.Errorf("Schlüssel der Geheimnis-Datei: %v", got)
	}
	str(get(secRoot, "wifi_password"), d.WLANPassword, "wifi_password")
	str(get(secRoot, "ap_password"), d.APPassword, "ap_password")
	str(get(secRoot, "mqtt_password"), d.MQTTPassword, "mqtt_password")
	str(get(secRoot, "web_password"), d.WebPassword, "web_password")

	// auch ein einfacher Leser ohne Baum sieht nur Zeichenketten
	var flat map[string]any
	if err := yaml.Unmarshal(sec, &flat); err != nil {
		t.Fatal(err)
	}
	for k, v := range flat {
		if _, ok := v.(string); !ok {
			t.Errorf("%s ist keine Zeichenkette: %#v", k, v)
		}
	}
}

func TestFriendlyName(t *testing.T) {
	for in, want := range map[string]string{
		"Presse 3":           "Presse 3",
		"Presse 3 / Halle 1": "Presse 3 - Halle 1", // „/“ verbietet ESPHome im Gerätenamen
		"  Leerzeichen  ":    "Leerzeichen",
		"":                   "",
	} {
		if got := friendlyName(in); got != want {
			t.Errorf("friendlyName(%q) = %q, erwartet %q", in, got, want)
		}
	}
	// höchstens 120 Byte, ohne ein Zeichen zu zerschneiden
	long := friendlyName(strings.Repeat("ä", 100))
	if len(long) > 120 || !strings.HasPrefix(strings.Repeat("ä", 100), long) || len(long) != 120 {
		t.Errorf("langer Name: %d Byte %q", len(long), long)
	}
}

func TestDeviceName(t *testing.T) {
	if got := deviceName("halle1-s0"); got != "esppdh-halle1-s0" {
		t.Errorf("deviceName = %q", got)
	}
	// ESPHome erlaubt höchstens 31 Zeichen, ein WLAN-Name höchstens 32
	long := deviceName(strings.Repeat("a", 23) + "-" + strings.Repeat("b", 8))
	if len(long) > 31 || strings.HasSuffix(long, "-") || !strings.HasPrefix(long, "esppdh-") {
		t.Errorf("lange Kennung: %q", long)
	}
}

func TestGenerateRejectsInvalid(t *testing.T) {
	d := testDevice("d1-mini", Block{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "D5"}})
	d.BrokerTLS = true
	cfg, sec, err := Generate(d)
	if err == nil || cfg != nil || sec != nil {
		t.Fatalf("ESP8266 mit TLS erzeugt: %v", err)
	}
	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("Meldung: %v", err)
	}
	if h := ConfigHash(d); h != "" {
		t.Errorf("ConfigHash eines ungültigen Geräts: %q", h)
	}

	// ungültiges UTF-8 fängt schon die Prüfung ab – mit deutscher Meldung, nicht als YAML-Fehler
	u := testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	u.Name = "Presse \xff 3"
	if _, _, err := Generate(u); err == nil || !strings.Contains(err.Error(), "ungültige Zeichen") {
		t.Errorf("Name mit ungültigem UTF-8: %v", err)
	}
	if h := ConfigHash(u); h != "" {
		t.Errorf("ConfigHash mit ungültigem UTF-8: %q", h)
	}
}

// sensorsBy: Sensoren der Konfiguration mit der Plattform platform.
func sensorsBy(t *testing.T, cfg []byte, platform string) []*yaml.Node {
	t.Helper()
	var out []*yaml.Node
	for _, s := range get(parseYAML(t, cfg), "sensor").Content {
		if p := get(s, "platform"); p != nil && p.Value == platform {
			out = append(out, s)
		}
	}
	return out
}

// Themen hängen an der festen Nummer des Bausteins, nicht an seiner Stelle in der Liste:
// Löschen oder Verschieben eines Bausteins lässt die Werte der anderen nicht wandern.
func TestTopicsFollowBlockNr(t *testing.T) {
	a := Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}}
	b := Block{Kind: "s0", Nr: 2, Params: map[string]string{"pin": "GPIO5"}}
	topics := func(blocks ...Block) map[string]string { // Thema → Pin
		t.Helper()
		cfg, _, err := Generate(testDevice("esp32dev", blocks...))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, s := range sensorsBy(t, cfg, "pulse_meter") {
			out[get(s, "state_topic").Value] = get(get(s, "pin"), "number").Value
			out[get(get(s, "total"), "state_topic").Value] = get(get(s, "pin"), "number").Value
		}
		return out
	}
	both := map[string]string{
		"esppdh/halle1-s0/s0-1/leistung": "GPIO4", "esppdh/halle1-s0/s0-1/zaehlerstand": "GPIO4",
		"esppdh/halle1-s0/s0-2/leistung": "GPIO5", "esppdh/halle1-s0/s0-2/zaehlerstand": "GPIO5",
	}
	if got := topics(a, b); !maps.Equal(got, both) {
		t.Errorf("zwei S0: %v", got)
	}
	if got := topics(b, a); !maps.Equal(got, both) {
		t.Errorf("vertauscht: %v", got)
	}
	if got, want := topics(b), map[string]string{
		"esppdh/halle1-s0/s0-2/leistung": "GPIO5", "esppdh/halle1-s0/s0-2/zaehlerstand": "GPIO5",
	}; !maps.Equal(got, want) {
		t.Errorf("erster gelöscht: %v", got)
	}

	// PZEM an denselben Pins: der Anschluss heißt nach der kleinsten Nummer, egal in welcher Reihenfolge
	p := func(nr int, addr string) Block {
		return Block{Kind: "pzem", Nr: nr, Params: map[string]string{"rx_pin": "GPIO16", "tx_pin": "GPIO17", "address": addr}}
	}
	for _, order := range [][]Block{{p(3, "1"), p(2, "2")}, {p(2, "2"), p(3, "1")}} {
		cfg, _, err := Generate(testDevice("esp32dev", order...))
		if err != nil {
			t.Fatal(err)
		}
		root := parseYAML(t, cfg)
		if u := get(get(root, "uart").Content[0], "id"); u.Value != "pzem_uart_2" {
			t.Errorf("UART-Kennung %q", u.Value)
		}
		for _, s := range sensorsBy(t, cfg, "pzemac") {
			if m := get(s, "modbus_id").Value; m != "pzem_modbus_2" {
				t.Errorf("modbus_id %q", m)
			}
		}
	}
}

// Flanke des Stückzählers: Pin mit Pull-up, Sensor oder Kontakt schaltet gegen GND.
// „fallend“ zählt beim Schalten (Pin fällt auf GND), „steigend“ beim Loslassen.
func TestCounterEdge(t *testing.T) {
	for edge, want := range map[string][2]string{
		"steigend": {"INCREMENT", "DISABLE"},
		"fallend":  {"DISABLE", "INCREMENT"},
		"":         {"DISABLE", "INCREMENT"}, // Vorgabe: fallend
	} {
		d := testDevice("esp32dev", Block{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "GPIO4", "edge": edge}})
		cfg, _, err := Generate(d)
		if err != nil {
			t.Fatal(err)
		}
		s := sensorsBy(t, cfg, "pulse_counter")[0]
		cm := get(s, "count_mode")
		if got := [2]string{get(cm, "rising_edge").Value, get(cm, "falling_edge").Value}; got != want {
			t.Errorf("Flanke %q: rising/falling %v, erwartet %v", edge, got, want)
		}
		if pu := get(get(get(s, "pin"), "mode"), "pullup"); pu == nil || pu.Value != "true" {
			t.Errorf("Flanke %q: kein Pull-up", edge)
		}
	}
}

func TestGenerateTLS(t *testing.T) {
	d := testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	cfg, _, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	if get(get(parseYAML(t, cfg), "mqtt"), "certificate_authority") != nil {
		t.Error("ohne TLS steht ein Stammzertifikat in der Konfiguration")
	}
	d.BrokerTLS, d.BrokerPort = true, 8883
	cfg, _, err = Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	ca := get(get(parseYAML(t, cfg), "mqtt"), "certificate_authority")
	if ca == nil || ca.Value != letsEncryptRoots {
		t.Fatalf("Stammzertifikate fehlen: %+v", ca)
	}
}

// Ohne Broker startet das Gerät nicht neu (ESPHome-Vorgabe wären 15 min): Zählerstand und
// Stückzahl bleiben bei einem Ausfall des PDH erhalten – bei WLAN und am Kabel.
func TestMQTTNoRebootWithoutBroker(t *testing.T) {
	for _, c := range Samples() {
		cfg, _, err := Generate(c.Device)
		if err != nil {
			t.Fatal(err)
		}
		if rt := get(get(parseYAML(t, cfg), "mqtt"), "reboot_timeout"); rt == nil || rt.Value != "0s" {
			t.Errorf("%s: mqtt.reboot_timeout = %+v, erwartet 0s", c.Name, rt)
		}
	}
}

// Die eingebetteten Stammzertifikate von Let's Encrypt sind unverändert (SHA-256 der Zertifikate).
func TestLetsEncryptRoots(t *testing.T) {
	want := []string{
		"96bcec06264976f37460779acf28c5a7cfe8a3c0aae11a8ffcee05c0bddf08c6", // ISRG Root X1
		"69729b8e15a86efc177a57afb7171dfc64add28c2fca8cf1507e34453ccb1470", // ISRG Root X2
	}
	rest := []byte(letsEncryptRoots)
	var got []string
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(blk.Bytes)
		got = append(got, hex.EncodeToString(sum[:]))
	}
	if !slices.Equal(got, want) {
		t.Errorf("Fingerabdrücke %v, erwartet %v", got, want)
	}
}

func TestConfigHash(t *testing.T) {
	d := testDevice("esp32dev", Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	h := ConfigHash(d)
	if len(h) != 64 || h != ConfigHash(d) {
		t.Fatalf("ConfigHash %q", h)
	}
	// ändert sich mit einem Geheimnis (steht nur in der Geheimnis-Datei) …
	d2 := d
	d2.MQTTPassword = "anderes-kennwort"
	if ConfigHash(d2) == h {
		t.Error("neues MQTT-Kennwort ändert den Hash nicht")
	}
	// … und mit einem Baustein
	d3 := d
	d3.Blocks = []Block{{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4", "pulses": "500"}}}
	if ConfigHash(d3) == h {
		t.Error("anderer Baustein ändert den Hash nicht")
	}
}

// TestEsphomeConfigAccepts prüft jede Erwartungsdatei mit „esphome config“ (nur prüfen, nicht
// bauen). Ohne eingerichtete Python-Umgebung wird der Test übersprungen.
func TestEsphomeConfigAccepts(t *testing.T) {
	st := CheckEnv(context.Background())
	if !st.Installed {
		t.Skip("ESPHome ist nicht eingerichtet (" + venvDir() + ")")
	}
	for _, c := range Samples() {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, c.Name+".yaml")
			for src, dst := range map[string]string{
				filepath.Join("testdata", c.Name+".yaml"):         cfg,
				filepath.Join("testdata", c.Name+".secrets.yaml"): filepath.Join(dir, "secrets.yaml"),
			} {
				if err := os.WriteFile(dst, readGolden(t, src), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, esphomeBin(), "config", cfg).CombinedOutput()
			if err != nil {
				t.Errorf("esphome config lehnt %s ab: %v\n%s", c.Name, err, out)
			}
		})
	}
}
