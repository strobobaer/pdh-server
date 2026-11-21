package esppdh

import (
	"bytes"
	"testing"

	"gopkg.in/yaml.v3"
)

// Webseite des Geräts (web_server v3), Adresse und Laufzeit-Zähler (0.82.11).

func webDevice(board string) Device {
	d := testDevice(board, Block{Kind: "s0", Nr: 1, Params: map[string]string{"pin": "GPIO4"}})
	d.WebServer, d.WebPassword = true, "web-kennwort-x"
	return d
}

// sensorByPlatform: erster Eintrag der Liste section mit platform = p.
func sensorByPlatform(root *yaml.Node, section, p string) *yaml.Node {
	list := get(root, section)
	if list == nil {
		return nil
	}
	for _, s := range list.Content {
		if v := get(s, "platform"); v != nil && v.Value == p {
			return s
		}
	}
	return nil
}

func TestGenerateWebServer(t *testing.T) {
	cfg, sec, err := Generate(webDevice("esp32dev"))
	if err != nil {
		t.Fatal(err)
	}
	root := parseYAML(t, cfg)
	ws := get(root, "web_server")
	if ws == nil {
		t.Fatal("kein web_server")
	}
	// ota: false – sonst lädt ESPHome von selbst ota.web_server (über captive_portal) und die
	// Webseite nähme im Betrieb Firmware an; so nur noch im aktiven Notzugang (Captive Portal).
	for k, want := range map[string]string{"port": "80", "version": "3", "local": "true", "ota": "false"} {
		if v := get(ws, k); v == nil || v.Value != want {
			t.Errorf("web_server.%s = %+v, erwartet %s", k, v, want)
		}
	}
	auth := get(ws, "auth")
	if u := get(auth, "username"); u == nil || u.Value != "pdh" {
		t.Errorf("auth.username = %+v, erwartet pdh", u)
	}
	if pw := get(auth, "password"); pw == nil || pw.Tag != "!secret" || pw.Value != "web_password" {
		t.Errorf("auth.password ist kein !secret web_password: %+v", pw)
	}
	if get(root, "ota") != nil {
		t.Error("ota darf nicht erzeugt werden (keine Firmware über die Webseite)")
	}
	if bytes.Contains(cfg, []byte("web-kennwort-x")) {
		t.Error("Konfiguration enthält das Kennwort der Webseite")
	}
	if v := get(parseYAML(t, sec), "web_password"); v == nil || v.Value != "web-kennwort-x" {
		t.Errorf("Geheimnis-Datei ohne web_password: %+v", v)
	}
}

func TestGenerateWebServerESP8266NoLocal(t *testing.T) {
	d := webDevice("d1-mini")
	d.Blocks = []Block{{Kind: "counter", Nr: 1, Params: map[string]string{"pin": "D5"}}}
	cfg, _, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	ws := get(parseYAML(t, cfg), "web_server")
	if ws == nil {
		t.Fatal("kein web_server")
	}
	if get(ws, "local") != nil {
		t.Error("ESP8266: web_server ohne local erwartet (Speicher)")
	}
}

func TestGenerateWebServerOff(t *testing.T) {
	d := webDevice("esp32dev")
	d.WebServer = false
	cfg, sec, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	if get(parseYAML(t, cfg), "web_server") != nil {
		t.Error("web_server trotz ausgeschalteter Webseite")
	}
	if get(parseYAML(t, sec), "web_password") != nil {
		t.Error("web_password trotz ausgeschalteter Webseite")
	}
}

func TestGenerateWebServerNeedsPassword(t *testing.T) {
	d := webDevice("esp32dev")
	d.WebPassword = ""
	if _, _, err := Generate(d); err == nil {
		t.Error("Webseite ohne Kennwort wurde erzeugt")
	}
}

func TestGenerateInfoTopics(t *testing.T) {
	for _, wired := range []bool{false, true} {
		d := webDevice("wt32-eth01")
		if wired {
			d.Wired, d.SSID, d.WLANPassword, d.APPassword = true, "", "", ""
		}
		cfg, _, err := Generate(d)
		if err != nil {
			t.Fatal(err)
		}
		root := parseYAML(t, cfg)
		p := "wifi_info"
		if wired {
			p = "ethernet_info"
		}
		info := sensorByPlatform(root, "text_sensor", p)
		if info == nil {
			t.Fatalf("kein text_sensor %s", p)
		}
		if st := get(get(info, "ip_address"), "state_topic"); st == nil || st.Value != "esppdh/halle1-s0/info/adresse" {
			t.Errorf("%s: ip_address.state_topic = %+v", p, st)
		}
		up := sensorByPlatform(root, "sensor", "uptime")
		if up == nil {
			t.Fatal("kein uptime-Sensor")
		}
		for k, want := range map[string]string{"update_interval": "10s", "state_topic": "esppdh/halle1-s0/info/laufzeit", "retain": "false"} {
			if v := get(up, k); v == nil || v.Value != want {
				t.Errorf("uptime.%s = %+v, erwartet %s", k, v, want)
			}
		}
	}
}
