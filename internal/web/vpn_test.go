package web

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"pdh"
)

func TestVPNKeys(t *testing.T) {
	// Beispielpaar aus der WireGuard-Dokumentation (wg(8))
	if pub, err := vpnPublicKey("yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="); err != nil || pub != "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=" {
		t.Errorf("öffentlicher Schlüssel: %s %v", pub, err)
	}
	priv, pub, err := vpnKeyPair()
	if err != nil || !vpnValidKey(priv) || !vpnValidKey(pub) {
		t.Fatalf("Schlüsselpaar: %v", err)
	}
	if again, _ := vpnPublicKey(priv); again != pub {
		t.Error("öffentlicher Schlüssel passt nicht zum privaten")
	}
	if vpnValidKey("abc") || vpnValidKey("") {
		t.Error("ungültige Schlüssel akzeptiert")
	}
}

func TestVPNAddresses(t *testing.T) {
	a, err := vpnNextAddress("10.66.0.0/24", map[string]bool{"10.66.0.2/32": true})
	if err != nil || a != "10.66.0.3/32" {
		t.Errorf("nächste Adresse: %s %v", a, err)
	}
	used := map[string]bool{}
	for i := 2; i <= 14; i++ {
		used["10.66.0."+strconv.Itoa(i)+"/32"] = true
	}
	if _, err := vpnNextAddress("10.66.0.0/28", used); err == nil {
		t.Error("volles Netz nicht erkannt")
	}
	for _, bad := range []string{"10.66.0.0/8", "10.66.0.0/30", "fd00::/64", "x"} {
		if _, err := vpnSubnet(bad); err == nil {
			t.Errorf("VPN-Netz %q akzeptiert", bad)
		}
	}
}

func TestVPNParseRoutes(t *testing.T) {
	got, err := vpnParseRoutes("192.168.10.7/24; 192.168.10.0/24\n10.1.2.3, 172.16.0.0/16", "10.66.0.0/24")
	if err != nil || got != "192.168.10.0/24, 10.1.2.3/32, 172.16.0.0/16" {
		t.Errorf("Netze: %q %v", got, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "10.66.0.0/16", "fd00::/64", "Halle 2"} {
		if _, err := vpnParseRoutes(bad, "10.66.0.0/24"); err == nil {
			t.Errorf("%q akzeptiert", bad)
		}
	}
	peers := []vpnPeer{{ID: "g1", Kind: "gateway", Name: "Halle 1", Routes: "192.168.10.0/24"}}
	if err := vpnRouteConflict("192.168.10.128/25", peers, ""); err == nil {
		t.Error("Überschneidung mit Gateway nicht erkannt")
	}
	if err := vpnRouteConflict("192.168.10.0/24", peers, "g1"); err != nil {
		t.Errorf("eigener Eintrag als Konflikt: %v", err)
	}
}

func TestVPNEndpoint(t *testing.T) {
	if e, err := vpnEndpoint(" vpn.werk.de:51820 "); err != nil || e != "vpn.werk.de:51820" {
		t.Errorf("%s %v", e, err)
	}
	for _, bad := range []string{"vpn.werk.de", "vpn.werk.de:0", "http://x:1", ":51820"} {
		if _, err := vpnEndpoint(bad); err == nil {
			t.Errorf("%q akzeptiert", bad)
		}
	}
}

func vpnTestSetup() (vpnSettings, []vpnPeer) {
	s := vpnSettings{Enabled: true, Endpoint: "vpn.firma.de", Port: 51820, Subnet: "10.66.0.0/24", DNS: "192.168.10.1",
		ServerPublicKey: "SERVERPUB", serverPrivateKey: "SERVERPRIV"}
	peers := []vpnPeer{
		{ID: "u1", Kind: "user", Name: "Laptop", Address: "10.66.0.2/32", PublicKey: "U1PUB", privateKey: "U1PRIV", psk: "U1PSK"},
		{ID: "u2", Kind: "user", Name: "Altes Handy", Address: "10.66.0.4/32", PublicKey: "U2PUB", Revoked: true},
		{ID: "g1", Kind: "gateway", Name: "Halle 2", Address: "10.66.0.3/32", PublicKey: "G1PUB", privateKey: "G1PRIV", Routes: "192.168.10.0/24"},
		{ID: "o1", Kind: "outbound", Name: "Werk Süd", Address: "10.8.0.5/32", PublicKey: "O1PUB", privateKey: "O1PRIV",
			Endpoint: "vpn.sued.de:51820", RemotePublicKey: "REMOTE", Routes: "192.168.20.0/24", Iface: "pdhout1"},
	}
	return s, peers
}

func TestVPNConfigs(t *testing.T) {
	s, peers := vpnTestSetup()
	hub, err := vpnHubConf(s, peers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PrivateKey = SERVERPRIV", "Address = 10.66.0.1/24", "ListenPort = 51820",
		"PublicKey = U1PUB\nPresharedKey = U1PSK\nAllowedIPs = 10.66.0.2/32", "PublicKey = G1PUB\nAllowedIPs = 10.66.0.3/32, 192.168.10.0/24"} {
		if !strings.Contains(hub, want) {
			t.Errorf("Hub enthält %q nicht:\n%s", want, hub)
		}
	}
	if strings.Contains(hub, "U2PUB") || strings.Contains(hub, "REMOTE") {
		t.Error("gesperrter Zugang oder ausgehender Tunnel im Hub")
	}
	off, _ := vpnHubConf(vpnSettings{}, peers)
	if !strings.HasPrefix(off, vpnDisabled) {
		t.Error("ausgeschaltet ohne Markierung")
	}

	user, _ := vpnClientConf(s, peers[0], peers)
	for _, want := range []string{"PrivateKey = U1PRIV", "Address = 10.66.0.2/32", "DNS = 192.168.10.1", "PublicKey = SERVERPUB",
		"PresharedKey = U1PSK", "Endpoint = vpn.firma.de:51820", "AllowedIPs = 10.66.0.1/32, 10.66.0.3/32, 192.168.10.0/24, 192.168.20.0/24"} {
		if !strings.Contains(user, want) {
			t.Errorf("Gerät enthält %q nicht:\n%s", want, user)
		}
	}
	gw, _ := vpnClientConf(s, peers[2], peers)
	if !strings.Contains(gw, "AllowedIPs = 10.66.0.0/24") || strings.Contains(gw, "DNS") {
		t.Errorf("Gateway:\n%s", gw)
	}
	out := vpnOutboundConf(peers[3])
	for _, want := range []string{"PrivateKey = O1PRIV", "Address = 10.8.0.5/32", "PublicKey = REMOTE", "Endpoint = vpn.sued.de:51820", "AllowedIPs = 192.168.20.0/24"} {
		if !strings.Contains(out, want) {
			t.Errorf("Tunnel enthält %q nicht:\n%s", want, out)
		}
	}
	if got := vpnForwardRules(peers); got != "10.66.0.2/32 10.66.0.3/32\n10.66.0.2/32 192.168.10.0/24\n10.66.0.2/32 192.168.20.0/24\n" {
		t.Errorf("Weiterleitung:\n%s", got)
	}
	if got := vpnHubRoutes(peers); got != "192.168.10.0/24\n" {
		t.Errorf("Hub-Routen: %q", got)
	}
}

func TestVPNParseImport(t *testing.T) {
	priv, _, _ := vpnKeyPair()
	_, pub, _ := vpnKeyPair()
	conf := "[Interface]\nPrivateKey = " + priv + "\nAddress = 10.8.0.5/24, fd00::5/64\nDNS = 1.1.1.1\n\n[Peer] # mGuard\nPublicKey = " + pub +
		"\nEndpoint = vpn.sued.de:51820\nAllowedIPs = 0.0.0.0/0, ::/0\n"
	in, err := vpnParseImport(conf)
	if err != nil || in.Address != "10.8.0.5/24" || in.PublicKey != pub || in.Endpoint != "vpn.sued.de:51820" || !in.FullTunnel || in.Routes != "" {
		t.Errorf("Import: %+v %v", in, err)
	}
	in, _ = vpnParseImport(strings.Replace(conf, "0.0.0.0/0, ::/0", "192.168.20.0/24, 10.8.0.0/24", 1))
	if in.FullTunnel || in.Routes != "192.168.20.0/24, 10.8.0.0/24" {
		t.Errorf("Netze aus AllowedIPs: %+v", in)
	}
	if _, err := vpnParseImport("[Interface]\nAddress = 10.8.0.5/32\n"); err == nil {
		t.Error("Konfiguration ohne Schlüssel akzeptiert")
	}
}

func TestVPNParseStatus(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st := vpnParseStatus("wg0 U1PUB 203.0.113.5:4321 1799999950 1048576 2048\nwg0 G1PUB (none) 0 0 0\nkaputt\n", now)
	u := st["U1PUB"]
	if !u.Online || u.RemoteEndpoint != "203.0.113.5:4321" || u.Received != "1.0 MB" || u.Sent != "2 KB" {
		t.Errorf("Gerät: %+v", u)
	}
	if g := st["G1PUB"]; !g.Known || g.Online || g.Handshake != "" {
		t.Errorf("Gateway nie verbunden: %+v", g)
	}
	if len(st) != 2 {
		t.Errorf("%d Einträge statt 2", len(st))
	}
}

func vpnTestView(admin bool) vpnView {
	s, peers := vpnTestSetup()
	status := map[string]vpnPeerStatus{
		"U1PUB":  {Known: true, Online: true, Handshake: "vor 1 Min.", Received: "1 KB", Sent: "2 KB", RemoteEndpoint: "203.0.113.5:4321"},
		"REMOTE": {Known: true, Handshake: "vor 10 Min.", Received: "5 MB", Sent: "1 MB"},
	}
	for i := range peers {
		peers[i] = vpnWithStatus(peers[i], status, true)
	}
	base, back := "/vpn", "/vpn?"
	if admin {
		base, back = vpnAdminURL+"/vpn", vpnAdminURL+"?tab=vpn&"
	}
	v := vpnView{Settings: s, Service: vpnService{State: "ok", Label: "läuft"}, Admin: admin, Base: base, Back: back,
		Mine: []vpnPeer{peers[0]}, Reachable: vpnReachable(peers, false), Host: vpnHostView{Dir: "/srv/pdh-wireguard", UID: 10001, GID: 10001, Docker: true}}
	if admin {
		v.Users, v.Gateways, v.Outbound = peers[:2], []vpnPeer{peers[2]}, []vpnPeer{peers[3]}
		v.Events = []vpnEvent{{At: "23.10.2026 10:00", User: "Max", Peer: "Laptop", Note: "Konfiguration heruntergeladen"}}
		v.People = []UserOption{{ID: "x", Name: "Max Muster"}}
	}
	return v
}

func TestVPNPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	v := vpnTestView(false)
	_, peers := vpnTestSetup()
	v.Show, v.ShowConf, v.ShowQR = &peers[0], "[Interface]\nPrivateKey = U1PRIV", "<svg></svg>"
	out := renderPage(t, tmpl, "vpn", VPNPageData{VPN: v, CanAdmin: true})
	for _, want := range []string{"VPN läuft", "Meine Geräte", "Konfiguration „Laptop“", "PrivateKey = U1PRIV", `href="/vpn/peers/u1/config"`,
		"<svg></svg>", `action="/vpn/peers"`, `action="/vpn/peers/u1/revoke"`, "vp-dot on", "192.168.20.0/24", `href="/admin/server-config?tab=vpn"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	for _, not := range []string{`value="gateway"`, `id="settings"`, "Alle Geräte", "/vpn/script"} {
		if strings.Contains(out, not) {
			t.Errorf("Seite für Benutzer enthält Verwaltung %q", not)
		}
	}
	if out := renderPage(t, tmpl, "vpn", VPNPageData{VPN: vpnTestView(false)}); strings.Contains(out, "tab=vpn") {
		t.Error("Link zur Einrichtung ohne Admin-Recht")
	}
}

func TestVPNAdminPaneRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	var b strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&b, "vpn-admin", vpnTestView(true)); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"pdh-wireguard", "/srv/pdh-wireguard", `href="/admin/server-config/vpn/script"`, `action="/admin/server-config/vpn/settings"`,
		`name="kind" value="gateway"`, `name="kind" value="outbound"`, "Halle 2", "pdhout1", "O1PUB", "gesperrt",
		`action="/admin/server-config/vpn/peers/u2/delete"`, `href="/admin/server-config?tab=vpn&amp;show=g1#show"`, "Max Muster",
		"Konfiguration heruntergeladen", "SERVERPUB", "UDP-Port 51820"} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter VPN enthält %q nicht", want)
		}
	}
}

func TestVPNInstallScript(t *testing.T) {
	sc := vpnInstallScript(vpnHostView{Dir: "/srv/pdh-wireguard", UID: 10001, GID: 10001}, 51999)
	for _, want := range []string{"#!/usr/bin/env bash", "DIR='/srv/pdh-wireguard'", "PDH_UID='10001'", "PORT='51999'",
		"cat >/usr/local/bin/pdh-wireguard <<'PDH_WG_DAEMON'", "pdh-wireguard – systemd-Dienst auf dem Host", "\nPDH_WG_DAEMON\n",
		"/etc/systemd/system/pdh-wireguard.service", "Environment=PDH_WIREGUARD_DIR=$DIR", "systemctl restart pdh-wireguard",
		"ip link add pdhtest type wireguard", `ufw allow "$PORT/udp"`} {
		if !strings.Contains(sc, want) {
			t.Errorf("Skript enthält %q nicht", want)
		}
	}
	if strings.Contains(sc, "{{") {
		t.Error("Platzhalter nicht ersetzt")
	}
	if strings.Contains(sc, "PrivateKey") {
		t.Error("Skript darf keine Schlüssel enthalten")
	}
}

func TestVPNNewDevice(t *testing.T) {
	s, peers := vpnTestSetup()
	p, err := vpnNewDevice(s, peers, "x1", "VPN")
	if err != nil || p.Address != "10.66.0.4/32" || !vpnValidKey(p.PublicKey) || p.psk == "" || p.UserID != "x1" {
		t.Fatalf("neues Gerät: %+v %v", p, err) // .4 ist frei (gesperrter Zugang)
	}
	peers = append(peers, p)
	q, _ := vpnNewDevice(s, peers, "x2", "VPN")
	if q.Address == p.Address {
		t.Error("doppelte Adresse")
	}
	for i := 0; i < 4; i++ {
		d, err := vpnNewDevice(s, peers, "x1", "VPN")
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, d)
	}
	if _, err := vpnNewDevice(s, peers, "x1", "VPN"); err == nil {
		t.Error("mehr als 5 Geräte je Person erlaubt")
	}
}

func TestVPNImportSummary(t *testing.T) {
	priv, _, _ := vpnKeyPair()
	_, pub, _ := vpnKeyPair()
	conf := "[Interface]\nPrivateKey = " + priv + "\nAddress = 10.8.0.5/32\n[Peer]\nPublicKey = " + pub + "\nPresharedKey = " + pub +
		"\nEndpoint = vpn.sued.de:51820\nAllowedIPs = 0.0.0.0/0\n"
	if _, err := vpnImportSummary(conf, "", "10.66.0.0/24"); err == nil || !strings.Contains(err.Error(), "0.0.0.0/0") {
		t.Errorf("alles durch den Tunnel nicht erkannt: %v", err)
	}
	sum, err := vpnImportSummary(conf, "192.168.20.0/24", "10.66.0.0/24")
	if err != nil || !strings.Contains(sum, "vpn.sued.de:51820") || !strings.Contains(sum, "192.168.20.0/24") || !strings.Contains(sum, "mit Pre-Shared-Key") {
		t.Errorf("Vorschau: %q %v", sum, err)
	}
	if _, err := vpnImportSummary(conf, "10.66.0.0/25", "10.66.0.0/24"); err == nil {
		t.Error("Überschneidung mit dem VPN-Netz nicht erkannt")
	}
}

func TestVPNZipIDs(t *testing.T) {
	const a, b = "0191a2b3-c4d5-7e6f-8a9b-0c1d2e3f4a5b", "0191a2b3-c4d5-7e6f-8a9b-0c1d2e3f4a5c"
	if !vpnZipIDsOK(a) || !vpnZipIDsOK(a+","+b) {
		t.Error("gültige IDs abgelehnt")
	}
	for _, bad := range []string{"", a + ",", "x", a + ",<script>"} {
		if vpnZipIDsOK(bad) {
			t.Errorf("%q akzeptiert", bad)
		}
	}
}

func TestVPNAdminImportAndBulkRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	v := vpnTestView(true)
	v.ZipIDs = "u1,g1"
	c, _ := tmpl.Clone()
	var b strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&b, "vpn-admin", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"WireGuard-Konfiguration importieren", `type="file" accept=".conf`, `hx-post="/admin/server-config/vpn/import-check"`,
		`action="/admin/server-config/vpn/peers/o1/import"`, `id="vp-check-o1"`, "Neue Konfiguration einspielen",
		`action="/admin/server-config/vpn/bulk"`, `name="user_id" value="x"`, "Konfigurationen für Benutzer erstellen",
		`href="/admin/server-config/vpn/zip?ids=u1%2cg1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter VPN enthält %q nicht", want)
		}
	}
}

func TestVPNConnState(t *testing.T) {
	cases := []struct {
		p       vpnPeer
		ok      bool
		state   string
		contain string
	}{
		{vpnPeer{Revoked: true}, true, "revoked", "gesperrt"},
		{vpnPeer{Disabled: true}, true, "off", "deaktiviert"},
		{vpnPeer{}, false, "unknown", "Dienst"},
		{vpnPeer{Status: vpnPeerStatus{Online: true, Handshake: "vor 1 Min."}}, true, "on", "verbunden"},
		{vpnPeer{Status: vpnPeerStatus{Handshake: "vor 9 Min."}}, true, "lost", "letzter Kontakt vor 9 Min."},
		{vpnPeer{Kind: "outbound"}, true, "wait", "keine Antwort"},
		{vpnPeer{Kind: "user"}, true, "never", "noch nie"},
	}
	for _, c := range cases {
		st, label := vpnConn(c.p, c.ok)
		if st != c.state || !strings.Contains(label, c.contain) {
			t.Errorf("%+v: %s %q", c.p, st, label)
		}
	}
	_, peers := vpnTestSetup()
	peers[2].Disabled = true // Gateway deaktiviert
	for i := range peers {
		peers[i] = vpnWithStatus(peers[i], map[string]vpnPeerStatus{"U1PUB": {Online: true, Handshake: "jetzt"}, "REMOTE": {Online: true, Handshake: "jetzt"}}, true)
	}
	if sum := vpnSummarize(peers); sum != (vpnSummary{Tunnels: 1, TunnelsOn: 1, Gateways: 0, GatewaysOn: 0, Devices: 1, DevicesOn: 1}) {
		t.Errorf("Zusammenfassung: %+v", sum)
	}
}

func TestVPNDisabledLeavesConfig(t *testing.T) {
	s, peers := vpnTestSetup()
	peers[2].Disabled = true // Gateway
	peers[0].Disabled = true // Gerät
	hub, _ := vpnHubConf(s, peers)
	if strings.Contains(hub, "G1PUB") || strings.Contains(hub, "U1PUB") {
		t.Errorf("deaktivierte Einträge im Hub:\n%s", hub)
	}
	if got := vpnForwardRules(peers); got != "" {
		t.Errorf("Weiterleitung für deaktiviertes Gerät: %q", got)
	}
	peers[0].Disabled = false
	if got := vpnForwardRules(peers); strings.Contains(got, "192.168.10.0/24") || !strings.Contains(got, "192.168.20.0/24") {
		t.Errorf("deaktiviertes Gateway noch erreichbar: %q", got)
	}
	// Gerätekonfiguration behält die Netze des deaktivierten Gateways (gilt nach dem Aktivieren weiter)
	if conf, _ := vpnClientConf(s, peers[0], peers); !strings.Contains(conf, "192.168.10.0/24") {
		t.Errorf("Gerätekonfiguration ohne Gateway-Netz:\n%s", conf)
	}
}

func TestVPNToggleAndLiveRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	v := vpnTestView(true)
	v.Gateways[0].Disabled = true
	v.Gateways[0] = vpnWithStatus(v.Gateways[0], nil, true)
	v.Summary = vpnSummarize(append(append(append([]vpnPeer{}, v.Users...), v.Gateways...), v.Outbound...))
	c, _ := tmpl.Clone()
	var b strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&b, "vpn-admin", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`action="/admin/server-config/vpn/peers/g1/toggle"`, "Aktivieren", "Deaktivieren", `class="vp-conn off" data-vpn-conn="g1">deaktiviert`,
		`class="vp-conn on" data-vpn-conn="u1">verbunden`, `data-vpn-conn="o1">getrennt – letzter Kontakt vor 10 Min.`, "von 203.0.113.5:4321",
		`data-vpn-sum="Tunnels">0/1`, `data-vpn-sum="Devices">1/1`, "data-vpn-service", "setInterval(tick, 10000)", `"/admin/server-config/vpn" + '/status'`} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter VPN enthält %q nicht", want)
		}
	}
	page := renderPage(t, tmpl, "vpn", VPNPageData{VPN: vpnTestView(false)})
	for _, want := range []string{`action="/vpn/peers/u1/toggle"`, `"/vpn" + '/status'`, `data-vpn-sum="Devices"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(page, `data-vpn-sum="Tunnels"`) {
		t.Error("Benutzer sieht Tunnel-Zusammenfassung")
	}
}

func TestVPNOutboundWithoutHub(t *testing.T) {
	off := vpnSettings{Subnet: "10.66.0.0/24"}
	tunnel := vpnPeer{Kind: "outbound", Iface: "pdhout1"}
	if vpnServiceWanted(off, nil) {
		t.Error("ohne Hub und ohne Tunnel soll der Dienst nicht laufen")
	}
	if !vpnServiceWanted(off, []vpnPeer{tunnel}) {
		t.Error("ausgehender Tunnel braucht den Hub nicht – Dienst soll laufen")
	}
	tunnel.Disabled = true
	if vpnServiceWanted(off, []vpnPeer{tunnel, {Kind: "user"}}) {
		t.Error("deaktivierter Tunnel hält den Dienst an")
	}
	if !vpnServiceWanted(vpnSettings{Enabled: true, Endpoint: "vpn.firma.de"}, nil) {
		t.Error("eingeschalteter Hub: Dienst soll laufen")
	}
	// der Dienst auf dem Host baut Tunnel nicht ab, nur weil der Hub aus ist
	daemon := pdh.WireGuardDaemon
	i := strings.Index(daemon, "apply_outbound() {")
	j := strings.Index(daemon[i:], "\n}\n")
	if i < 0 || j < 0 || strings.Contains(daemon[i:i+j], "disabled") {
		t.Error("apply_outbound hängt noch am Hub (disabled)")
	}
}
