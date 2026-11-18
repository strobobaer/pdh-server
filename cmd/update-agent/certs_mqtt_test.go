package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

const mqttName = "mqtt.firma.de"
const mqttSitePath = "/etc/nginx/conf.d/mqtt-mqtt.firma.de.conf"

func mqttInstall(t *testing.T, f *certFixture, role, upstream string) map[string]any {
	t.Helper()
	chain, key := certTestPEM(t, mqttName)
	body := map[string]any{"name": mqttName, "chain_pem": chain, "key_pem": key, "role": role}
	if upstream != "" {
		body["upstream"] = upstream
	}
	_, resp := f.call(t, f.agent.certInstallHandler, http.MethodPost, body)
	return resp
}

func mqttStatusUpstream(t *testing.T, f *certFixture) string {
	t.Helper()
	_, st := f.call(t, func(w http.ResponseWriter, r *http.Request) { f.agent.certStatusHandler(w, r) }, http.MethodGet, nil)
	for _, e := range st["certs"].([]any) {
		if m := e.(map[string]any); m["name"] == mqttName {
			s, _ := m["mqtt_upstream"].(string)
			return s
		}
	}
	return ""
}

// Rolle mqtt: eigene Seite je Name, 443 mit Zertifikat an den WebSocket-Port, ohne
// Umleitung von Port 80; pdh.conf bleibt unberührt; der Status nennt das Ziel.
func TestCertInstallMQTT(t *testing.T) {
	certSetIPv6(t, false)
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/pdh.conf", certSiteHeader+" (PDH)\nserver { server_name pdh.firma.de; }\n")
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083"); resp["ok"] != true || resp["note"] != nil {
		t.Fatalf("Antwort: %v", resp)
	}
	site := f.read(t, mqttSitePath)
	for _, want := range []string{certSiteHeader, "server_name " + mqttName + ";", "listen 443 ssl;",
		"proxy_pass http://127.0.0.1:8083;", "proxy_set_header Upgrade $http_upgrade;",
		"ssl_certificate /etc/ssl/pdh/" + mqttName + "/fullchain.pem;"} {
		if !strings.Contains(site, want) {
			t.Errorf("Seite ohne %q:\n%s", want, site)
		}
	}
	if strings.Contains(site, "listen 80;") || strings.Contains(site, "return 301") {
		t.Errorf("MQTT-Seite darf Port 80 nicht umleiten:\n%s", site)
	}
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); !strings.Contains(got, "(PDH)") {
		t.Errorf("pdh.conf wurde angefasst: %q", got)
	}
	if got := mqttStatusUpstream(t, f); got != "http://127.0.0.1:8083" {
		t.Errorf("Status nennt das Ziel nicht: %q", got)
	}

	// geänderter WebSocket-Port: dieselbe Datei wird neu geschrieben
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:9001"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if got := mqttStatusUpstream(t, f); got != "http://127.0.0.1:9001" {
		t.Errorf("Ziel nach Änderung: %q", got)
	}
}

func TestCertInstallMQTTRejectsBadUpstream(t *testing.T) {
	f := newCertFixture(t)
	for _, up := range []string{"", "http://10.0.0.5:8083", "http://127.0.0.1:443", "http://127.0.0.1:8083; evil"} {
		if resp := mqttInstall(t, f, "mqtt", up); resp["ok"] != false {
			t.Errorf("Ziel %q hätte abgelehnt werden müssen: %v", up, resp)
		}
	}
	if f.exists(mqttSitePath) || f.exists("/etc/ssl/pdh/"+mqttName) {
		t.Error("trotz Ablehnung wurde etwas geschrieben")
	}
}

// Bedient schon eine andere Seite den Namen (auch pdh.conf) oder ist die Datei fremd, legt der
// Agent nur die Dateien ab und sagt es im Hinweis.
func TestCertInstallMQTTRefusesTakenName(t *testing.T) {
	cases := map[string]func(f *certFixture){
		"pdh.conf": func(f *certFixture) {
			f.write(t, "/etc/nginx/conf.d/pdh.conf", certSiteHeader+"\nserver {\n    server_name "+mqttName+";\n}\n")
		},
		"andere": func(f *certFixture) {
			f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    server_name "+mqttName+";\n}\n")
		},
		"fremde Datei": func(f *certFixture) { f.write(t, mqttSitePath, "# von Hand\nserver { }\n") },
	}
	for name, prep := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCertFixture(t)
			prep(f)
			before := f.snapshot(t)
			resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083")
			note, _ := resp["note"].(string)
			if resp["ok"] != true || !strings.Contains(note, "nicht geändert") {
				t.Fatalf("Antwort: %v", resp)
			}
			after := f.snapshot(t)
			for p, data := range before {
				if strings.HasPrefix(p, "etc/nginx/") && after[p] != data {
					t.Errorf("%s wurde geändert", p)
				}
			}
			if !f.exists("/etc/ssl/pdh/" + mqttName + "/fullchain.pem") {
				t.Error("Zertifikat hätte abgelegt werden müssen")
			}
		})
	}
}

func TestCertInstallMQTTRestoresOnFailedCheck(t *testing.T) {
	f := newCertFixture(t)
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	before := f.snapshot(t)
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:9001"); resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	// Rolle "" entfernte die Seite – auch das kommt bei einem Fehler zurück
	if resp := mqttInstall(t, f, "", ""); resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	after := f.snapshot(t)
	if len(after) != len(before) {
		t.Errorf("Dateistand weicht ab: vorher %d, nachher %d", len(before), len(after))
	}
	for p, data := range before {
		if strings.HasPrefix(p, "etc/nginx/") && after[p] != data {
			t.Errorf("%s ist nicht zurück", p)
		}
	}
}

// Trägt kein Broker mehr den Namen (Rolle ""), verschwindet die eigene Seite; die Dateien bleiben.
func TestCertInstallRoleEmptyDropsMQTTSite(t *testing.T) {
	f := newCertFixture(t)
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if resp := mqttInstall(t, f, "", ""); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if f.exists(mqttSitePath) {
		t.Error("veraltete MQTT-Seite ist geblieben")
	}
	if !f.exists("/etc/ssl/pdh/" + mqttName + "/privkey.pem") {
		t.Error("Zertifikatsdateien müssen bleiben")
	}
	if got := mqttStatusUpstream(t, f); got != "" {
		t.Errorf("Status nennt noch ein Ziel: %q", got)
	}
}

// Wird der Name des Brokers später der Name des PDH, zählt die eigene MQTT-Seite nicht als
// fremde Seite: Sie geht weg, und die PDH-Seite wird im selben Auftrag geschrieben.
func TestCertInstallPDHReplacesOwnMQTTSite(t *testing.T) {
	f := newCertFixture(t)
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	resp := mqttInstall(t, f, "pdh", "http://127.0.0.1:8090")
	if resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if note, _ := resp["note"].(string); note != "" {
		t.Errorf("Hinweis statt Seite: %q", note)
	}
	if f.exists(mqttSitePath) {
		t.Error("MQTT-Seite ist geblieben")
	}
	if got := f.read(t, "/etc/nginx/conf.d/pdh.conf"); !strings.Contains(got, mqttName) || !strings.Contains(got, "127.0.0.1:8090") {
		t.Errorf("PDH-Seite fehlt oder ist falsch:\n%s", got)
	}
}

func TestCertRemoveMQTT(t *testing.T) {
	for _, role := range []string{"mqtt", ""} { // "" = Broker trägt den Namen nicht mehr
		t.Run("rolle="+role, func(t *testing.T) {
			f := newCertFixture(t)
			if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083"); resp["ok"] != true {
				t.Fatalf("Antwort: %v", resp)
			}
			_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": mqttName, "role": role})
			if resp["ok"] != true {
				t.Fatalf("Antwort: %v", resp)
			}
			if f.exists(mqttSitePath) || f.exists("/etc/ssl/pdh/"+mqttName) {
				t.Error("Seite und Zertifikatsordner hätten entfernt werden müssen")
			}
		})
	}
}

func TestCertRemoveMQTTFailedCheckRestores(t *testing.T) {
	f := newCertFixture(t)
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8083"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	before := f.snapshot(t)
	f.nginxTest = func() (string, error) { return "kaputt", errors.New("exit status 1") }
	_, resp := f.call(t, f.agent.certRemoveHandler, http.MethodPost, map[string]any{"name": mqttName, "role": "mqtt"})
	if resp["ok"] != false {
		t.Fatalf("ok muss false sein: %v", resp)
	}
	after := f.snapshot(t)
	for p, data := range before {
		if after[p] != data {
			t.Errorf("%s ist nicht zurück", p)
		}
	}
}

func TestCertUnknownRoleStillRejected(t *testing.T) {
	f := newCertFixture(t)
	if resp := mqttInstall(t, f, "opcua", "http://127.0.0.1:8083"); resp["ok"] != false {
		t.Fatalf("unbekannte Rolle angenommen: %v", resp)
	}
}
