package web

import (
	"strconv"
	"strings"
	"testing"
	"time"
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
			Endpoint: "vpn.sued.de:51820", RemotePublicKey: "REMOTE", Routes: "192.168.20.0/24", Iface: "pdho1"},
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

func TestVPNPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	s, peers := vpnTestSetup()
	d := VPNPageData{Settings: s, Service: vpnService{State: "ok", Label: "läuft"}, CanManage: true,
		Mine: []vpnPeer{peers[0]}, Users: peers[:2], Gateways: []vpnPeer{peers[2]}, Outbound: []vpnPeer{peers[3]},
		Events: []vpnEvent{{At: "23.10.2026 10:00", User: "Max", Peer: "Laptop", Note: "Konfiguration heruntergeladen"}},
		People: []UserOption{{ID: "x", Name: "Max Muster"}}, Reachable: vpnReachable(peers),
		Show: &peers[0], ShowConf: "[Interface]\nPrivateKey = U1PRIV", ShowQR: "<svg></svg>"}
	d.Mine[0].Status = vpnPeerStatus{Online: true, Handshake: "vor 1 Min.", Received: "1 KB", Sent: "2 KB"}
	out := renderPage(t, tmpl, "vpn", d)
	for _, want := range []string{"VPN läuft", "Konfiguration „Laptop“", "PrivateKey = U1PRIV", "/vpn/peers/u1/config", "<svg></svg>",
		`name="kind" value="gateway"`, `name="kind" value="outbound"`, "Halle 2", "pdho1", "O1PUB", "gesperrt", "/vpn/peers/u2/delete",
		"Max Muster", "Konfiguration heruntergeladen", "192.168.20.0/24", "vp-dot on", "SERVERPUB"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	// ohne Verwaltung: nur eigene Geräte
	out = renderPage(t, tmpl, "vpn", VPNPageData{Settings: s, Mine: []vpnPeer{peers[0]}})
	if strings.Contains(out, `id="settings"`) || strings.Contains(out, `value="gateway"`) || !strings.Contains(out, "Meine Geräte") {
		t.Error("Verwaltung ohne Recht sichtbar")
	}
}
