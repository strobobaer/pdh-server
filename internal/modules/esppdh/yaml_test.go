package esppdh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"flag"
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
func testDevice(board string, blocks ...Block) Device {
	return Device{
		Slug: "halle1-s0", Name: "Presse 3", BoardKey: board,
		SSID: "Werk-Produktion", WLANPassword: "wlan-kennwort-1", APPassword: "notzugang-kennwort-1",
		BrokerHost: "mqtt.werk.example", BrokerPort: 1883,
		MQTTUser: "esppdh-halle1-s0", MQTTPassword: "mqtt-kennwort-1",
		Blocks: blocks,
	}
}

type goldenCase struct {
	name string
	dev  Device
}

func goldenCases() []goldenCase {
	// esp32dev mit S0-Zähler, TLS
	c1 := testDevice("esp32dev", Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4", "pulses": "1000"}})
	c1.Name = "Presse 3 / Halle 1"
	c1.BrokerTLS, c1.BrokerPort = true, 8883

	// D1 mini mit Stückzähler (fallende Flanke), ohne TLS
	c2 := testDevice("d1-mini", Block{Kind: "counter", Params: map[string]string{"pin": "D5", "edge": "fallend"}})
	c2.Slug, c2.MQTTUser, c2.Name = "linie2-takt", "esppdh-linie2-takt", "Linie 2 Takt"

	// WT32-ETH01 am Kabel mit zwei PZEM-004T an einem Anschluss, TLS
	c3 := testDevice("wt32-eth01",
		Block{Kind: "pzem", Params: map[string]string{"rx_pin": "GPIO5", "tx_pin": "GPIO17"}},
		Block{Kind: "pzem", Params: map[string]string{"rx_pin": "GPIO5", "tx_pin": "GPIO17", "address": "2"}},
	)
	c3.Slug, c3.MQTTUser, c3.Name = "verteiler-h2", "esppdh-verteiler-h2", "Verteiler Halle 2"
	c3.Wired, c3.SSID, c3.WLANPassword, c3.APPassword = true, "", "", ""
	c3.BrokerTLS, c3.BrokerPort = true, 8883

	// ESP32-S3 mit SCT-013 (Stromausgang, Bürde 33 Ω) und S0-Zähler, verborgenes WLAN
	c4 := testDevice("esp32-s3",
		Block{Kind: "sct013", Params: map[string]string{"pin": "GPIO1", "clamp": "100A/50mA", "burden": "33", "voltage": "230"}},
		Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4", "pulses": "800"}},
	)
	c4.Slug, c4.MQTTUser, c4.Name = "kompressor", "esppdh-kompressor", "Kompressor"
	c4.WLANHidden = true

	return []goldenCase{
		{"esp32dev-s0", c1},
		{"d1-mini-counter", c2},
		{"wt32-eth01-pzem", c3},
		{"esp32-s3-sct013-s0", c4},
	}
}

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
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			cfg, sec, err := Generate(c.dev)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range []struct {
				path string
				got  []byte
			}{
				{filepath.Join("testdata", c.name+".yaml"), cfg},
				{filepath.Join("testdata", c.name+".secrets.yaml"), sec},
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
			cfg2, sec2, _ := Generate(c.dev)
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
	d := testDevice("esp32dev", Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4"}})
	cfg, sec, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{d.WLANPassword, d.MQTTPassword, d.APPassword} {
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
	// Geheimnis-Datei: genau die drei Schlüssel
	if got := keys(parseYAML(t, sec)); !slices.Equal(got, []string{"wifi_password", "ap_password", "mqtt_password"}) {
		t.Errorf("Schlüssel der Geheimnis-Datei: %v", got)
	}
	// verkabelt: nur das MQTT-Kennwort
	w := testDevice("wt32-eth01", Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4"}})
	w.Wired = true
	_, sec, err = Generate(w)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(parseYAML(t, sec)); !slices.Equal(got, []string{"mqtt_password"}) {
		t.Errorf("verkabelt, Schlüssel der Geheimnis-Datei: %v", got)
	}
}

func TestGenerateEscapesInput(t *testing.T) {
	d := testDevice("esp32dev", Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4"}})
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
	if got := keys(root); !slices.Equal(got, []string{"esphome", "esp32", "logger", "wifi", "captive_portal", "mqtt", "sensor"}) {
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
	o := testDevice(BoardOther, Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4"}})
	o.CustomBoard = "nodemcuv2"
	cfg3, _, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	str(get(get(parseYAML(t, cfg3), "esp8266"), "board"), o.CustomBoard, "board")

	secRoot := parseYAML(t, sec)
	if got := keys(secRoot); !slices.Equal(got, []string{"wifi_password", "ap_password", "mqtt_password"}) {
		t.Errorf("Schlüssel der Geheimnis-Datei: %v", got)
	}
	str(get(secRoot, "wifi_password"), d.WLANPassword, "wifi_password")
	str(get(secRoot, "ap_password"), d.APPassword, "ap_password")
	str(get(secRoot, "mqtt_password"), d.MQTTPassword, "mqtt_password")

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
	d := testDevice("d1-mini", Block{Kind: "counter", Params: map[string]string{"pin": "D5"}})
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
}

func TestGenerateTLS(t *testing.T) {
	d := testDevice("esp32dev", Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4"}})
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
	d := testDevice("esp32dev", Block{Kind: "s0", Params: map[string]string{"pin": "GPIO4"}})
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
	d3.Blocks = []Block{{Kind: "s0", Params: map[string]string{"pin": "GPIO4", "pulses": "500"}}}
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
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, c.name+".yaml")
			for src, dst := range map[string]string{
				filepath.Join("testdata", c.name+".yaml"):         cfg,
				filepath.Join("testdata", c.name+".secrets.yaml"): filepath.Join(dir, "secrets.yaml"),
			} {
				if err := os.WriteFile(dst, readGolden(t, src), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, esphomeBin(), "config", cfg).CombinedOutput()
			if err != nil {
				t.Errorf("esphome config lehnt %s ab: %v\n%s", c.name, err, out)
			}
		})
	}
}
