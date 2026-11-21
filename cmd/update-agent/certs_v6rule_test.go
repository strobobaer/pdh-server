package main

import (
	"strings"
	"testing"
)

// Hört eine andere Seite auf Port 443 nur über IPv4, darf die neue Seite nicht auf IPv6
// hören – sonst bekäme jeder Aufruf über IPv6 (auch ::1 vom Tunnel) ihr Zertifikat.
func TestCertV6OffWhenOtherSiteIPv4Only(t *testing.T) {
	certSetIPv6(t, true)
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    listen 443 ssl;\n    server_name pdh.firma.de;\n}\n")
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	site := f.read(t, mqttSitePath)
	if !strings.Contains(site, "listen 443 ssl;") || strings.Contains(site, "listen [::]:443 ssl;") {
		t.Errorf("Seite soll nur auf IPv4 hören:\n%s", site)
	}
	if strings.Contains(site, certSiteV6Off) {
		t.Errorf("kein Rückfall-Vermerk erwartet:\n%s", site)
	}
}

func TestCertV6OnWhenAllOthersIPv6(t *testing.T) {
	certSetIPv6(t, true)
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    listen 443 ssl;\n    listen [::]:443 ssl;\n    server_name pdh.firma.de;\n}\n")
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if site := f.read(t, mqttSitePath); !strings.Contains(site, "listen [::]:443 ssl;") {
		t.Errorf("Seite soll auch auf IPv6 hören:\n%s", site)
	}
}

func TestCertV6IgnoresPort80Sites(t *testing.T) {
	certSetIPv6(t, true)
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/alt.conf", "server {\n    listen 80;\n    server_name alt.firma.de;\n}\n")
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if site := f.read(t, mqttSitePath); !strings.Contains(site, "listen [::]:443 ssl;") {
		t.Errorf("Seiten nur auf Port 80 zählen nicht:\n%s", site)
	}
}

// Ohne IPv6-Zeilen ist die Seite richtig, wenn eine andere nur IPv4 kann – der Status
// darf sie dann nicht als veraltet melden (sonst schriebe der tägliche Lauf sie neu).
func TestCertStatusNotStaleWhenOthersIPv4Only(t *testing.T) {
	certSetIPv6(t, true)
	f := newCertFixture(t)
	f.write(t, "/etc/nginx/conf.d/hand.conf", "server {\n    listen 443 ssl;\n    server_name pdh.firma.de;\n}\n")
	if resp := mqttInstall(t, f, "mqtt", "http://127.0.0.1:8093"); resp["ok"] != true {
		t.Fatalf("Antwort: %v", resp)
	}
	if got := mqttStatusUpstream(t, f); got != "http://127.0.0.1:8093" {
		t.Errorf("Status meldet die Seite nicht: %q", got)
	}
}
