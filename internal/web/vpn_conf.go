package web

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

// WireGuard-Konfigurationen für das VPN ins Maschinennetz (vpn.go) – ohne
// Datenbank, damit testbar. Der systemd-Dienst pdh-wireguard auf dem Host
// (scripts/pdh-wireguard.sh) liest die Dateien aus dem gemeinsamen Ordner und
// wendet sie an:
//
//	pdhhub.conf    Hub: Geräte (user) und Standort-Gateways wählen sich ein
//	pdhhub.routes  Netze hinter den Gateways (Änderung → Hub neu starten)
//	pdhoutN.conf   je ausgehender Tunnel eine Schnittstelle (PDH als Client, mit NAT)
//	forward.allow  „Quelle Ziel“ je Zeile: was eingewählte Geräte erreichen dürfen
//
// Zurück schreibt der Dienst status (je Gegenstelle: Schnittstelle, Schlüssel,
// Endpunkt, letzter Handshake, empfangen, gesendet), heartbeat und error.

const (
	vpnDefPort     = 51820
	vpnHubIf       = "pdhhub"
	vpnOutPrefix   = "pdhout"
	vpnDisabled    = "# pdh-disabled"
	vpnKeepalive   = 25
	vpnOnlineAfter = 3 * time.Minute // Handshake jünger → verbunden
)

// vpnSettings: Einstellungen aus app_settings (vpn.*).
type vpnSettings struct {
	Enabled          bool
	Endpoint         string // öffentlicher Name/IP des PDH-Servers
	Port             int    // öffentlicher UDP-Port
	Subnet           string // VPN-Netz, z. B. 10.66.0.0/24
	DNS              string
	ServerPublicKey  string
	serverPrivateKey string
}

// vpnPeer: Zugang (user), Standort-Gateway (gateway) oder ausgehender Tunnel (outbound).
type vpnPeer struct {
	ID, Kind, Name, UserID, UserName string
	Address, PublicKey, Routes       string
	Endpoint, RemotePublicKey        string
	CreatedAt                        time.Time
	Revoked                          bool
	Iface                            string // outbound: Schnittstelle (pdhout1 …)
	Status                           vpnPeerStatus
	privateKey, psk                  string
}

func (p vpnPeer) RouteList() []string { return splitRoutes(p.Routes) }

type vpnPeerStatus struct {
	Known            bool
	Online           bool
	Handshake        string // „vor 2 Min.“
	RemoteEndpoint   string
	Received, Sent   string
	handshakeAt      time.Time
	rxBytes, txBytes int64
}

// ── Schlüssel ───────────────────────────────────────────────

// vpnKeyPair: neuer WireGuard-Schlüssel (Curve25519), Base64 wie bei „wg genkey“.
func vpnKeyPair() (priv, pub string, err error) {
	var k [32]byte
	if _, err = rand.Read(k[:]); err != nil {
		return "", "", err
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	pub, err = vpnPublicKey(base64.StdEncoding.EncodeToString(k[:]))
	return base64.StdEncoding.EncodeToString(k[:]), pub, err
}

func vpnPublicKey(priv string) (string, error) {
	k, err := base64.StdEncoding.DecodeString(priv)
	if err != nil || len(k) != 32 {
		return "", errors.New("ungültiger privater Schlüssel")
	}
	p, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(p), nil
}

func vpnPSK() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

// vpnValidKey: 32 Byte Base64 (öffentlicher Schlüssel oder PSK).
func vpnValidKey(s string) bool {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	return err == nil && len(k) == 32
}

// ── Adressen und Netze ──────────────────────────────────────

// vpnSubnet: VPN-Netz prüfen (IPv4, /16 bis /28).
func vpnSubnet(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil || !p.Addr().Is4() || p.Bits() < 16 || p.Bits() > 28 {
		return netip.Prefix{}, errors.New("VPN-Netz: IPv4-Netz von /16 bis /28 angeben, z. B. 10.66.0.0/24")
	}
	return p.Masked(), nil
}

// vpnServerAddress: erste Adresse im VPN-Netz (z. B. 10.66.0.1/24).
func vpnServerAddress(sub netip.Prefix) netip.Addr { return sub.Addr().Next() }

// vpnNextAddress: nächste freie Adresse im VPN-Netz (ohne Server und Broadcast) als /32.
func vpnNextAddress(subnet string, used map[string]bool) (string, error) {
	sub, err := vpnSubnet(subnet)
	if err != nil {
		return "", err
	}
	a := vpnServerAddress(sub).Next()
	for ; sub.Contains(a); a = a.Next() {
		if !sub.Contains(a.Next()) { // Broadcast
			break
		}
		if host := a.String() + "/32"; !used[host] {
			return host, nil
		}
	}
	return "", errors.New("Keine freie Adresse im VPN-Netz – größeres Netz wählen")
}

// vpnParseRoutes: Netze hinter einem Gateway bzw. Tunnel (IPv4, kommagetrennt
// oder je Zeile; einzelne Adressen werden /32). Standardroute und Netze, die
// das VPN-Netz überlappen, sind nicht erlaubt.
func vpnParseRoutes(s, subnet string) (string, error) {
	sub, _ := vpnSubnet(subnet)
	seen := map[netip.Prefix]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return "", fmt.Errorf("„%s“ ist kein Netz – z. B. 192.168.10.0/24", f)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if !p.Addr().Is4() {
			return "", fmt.Errorf("„%s“: nur IPv4-Netze", f)
		}
		if p.Bits() == 0 {
			return "", errors.New("0.0.0.0/0 (alles durch den Tunnel) ist nicht erlaubt – bitte die Netze dahinter angeben")
		}
		p = p.Masked()
		if sub.IsValid() && p.Overlaps(sub) {
			return "", fmt.Errorf("%s überschneidet das VPN-Netz %s", p, sub)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p.String())
		}
	}
	return strings.Join(out, ", "), nil
}

func splitRoutes(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// vpnRouteConflict: Netz schon bei einem anderen Gateway/Tunnel eingetragen?
func vpnRouteConflict(routes string, peers []vpnPeer, skipID string) error {
	for _, r := range splitRoutes(routes) {
		a, err := netip.ParsePrefix(r)
		if err != nil {
			continue
		}
		for _, p := range peers {
			if p.ID == skipID || p.Revoked || p.Kind == "user" {
				continue
			}
			for _, o := range p.RouteList() {
				if b, err := netip.ParsePrefix(o); err == nil && a.Overlaps(b) {
					return fmt.Errorf("%s überschneidet %s von „%s“", a, b, p.Name)
				}
			}
		}
	}
	return nil
}

var vpnHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// vpnEndpoint: „host:port“ eines entfernten Servers prüfen.
func vpnEndpoint(s string) (string, error) {
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return "", errors.New("Endpunkt als host:port angeben, z. B. vpn.werk.de:51820")
	}
	host, port := s[:i], s[i+1:]
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || !vpnHostRe.MatchString(host) {
		return "", errors.New("Endpunkt als host:port angeben, z. B. vpn.werk.de:51820")
	}
	return host + ":" + port, nil
}

// ── Konfigurationen ─────────────────────────────────────────

// vpnReachable: Netze, die eingewählte Geräte erreichen (Gateways und ausgehende Tunnel).
func vpnReachable(peers []vpnPeer) []string {
	var out []string
	for _, p := range peers {
		if p.Revoked {
			continue
		}
		switch p.Kind {
		case "gateway":
			out = append(out, p.Address)
			out = append(out, p.RouteList()...)
		case "outbound":
			out = append(out, p.RouteList()...)
		}
	}
	return out
}

// vpnHubConf: pdhhub.conf des Servers (Hub); ListenPort = öffentlicher Port.
func vpnHubConf(s vpnSettings, peers []vpnPeer) (string, error) {
	if !s.Enabled {
		return vpnDisabled + "\n", nil
	}
	sub, err := vpnSubnet(s.Subnet)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# PDH – automatisch erzeugt, nicht von Hand ändern\n[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\nAddress = %s/%d\nListenPort = %d\n", s.serverPrivateKey, vpnServerAddress(sub), sub.Bits(), s.Port)
	for _, p := range peers {
		if p.Revoked || p.Kind == "outbound" {
			continue
		}
		allowed := append([]string{p.Address}, p.RouteList()...)
		if p.Kind == "user" {
			allowed = []string{p.Address}
		}
		fmt.Fprintf(&b, "\n# %s: %s\n[Peer]\nPublicKey = %s\n", p.Kind, oneLine(p.Name), p.PublicKey)
		if p.psk != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", p.psk)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(allowed, ", "))
	}
	return b.String(), nil
}

// vpnHubRoutes: Netze hinter den Gateways (Änderung → Hub im Dienst neu starten).
func vpnHubRoutes(peers []vpnPeer) string {
	var r []string
	for _, p := range peers {
		if !p.Revoked && p.Kind == "gateway" {
			r = append(r, p.RouteList()...)
		}
	}
	sort.Strings(r)
	return strings.Join(r, "\n") + "\n"
}

// vpnForwardRules: forward.allow – Geräte dürfen in Gateway- und Tunnelnetze, nicht zueinander.
func vpnForwardRules(peers []vpnPeer) string {
	dst := vpnReachable(peers)
	var b strings.Builder
	for _, p := range peers {
		if p.Revoked || p.Kind != "user" {
			continue
		}
		for _, d := range dst {
			fmt.Fprintf(&b, "%s %s\n", p.Address, d)
		}
	}
	return b.String()
}

// vpnClientConf: Konfiguration für ein Gerät bzw. Standort-Gateway (Gegenstelle ist PDH).
func vpnClientConf(s vpnSettings, p vpnPeer, peers []vpnPeer) (string, error) {
	sub, err := vpnSubnet(s.Subnet)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# PDH-VPN: %s\n[Interface]\nPrivateKey = %s\nAddress = %s\n", oneLine(p.Name), p.privateKey, p.Address)
	allowed := []string{sub.String()}
	if p.Kind == "user" {
		if s.DNS != "" {
			fmt.Fprintf(&b, "DNS = %s\n", s.DNS)
		}
		allowed = append([]string{vpnServerAddress(sub).String() + "/32"}, vpnReachable(peers)...)
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\n", s.ServerPublicKey)
	if p.psk != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", p.psk)
	}
	fmt.Fprintf(&b, "Endpoint = %s:%d\nAllowedIPs = %s\nPersistentKeepalive = %d\n", s.Endpoint, s.Port, strings.Join(allowed, ", "), vpnKeepalive)
	return b.String(), nil
}

// vpnOutboundConf: pdhoutN.conf – PDH als Client eines entfernten WireGuard-Servers.
func vpnOutboundConf(p vpnPeer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PDH – ausgehender Tunnel: %s\n[Interface]\nPrivateKey = %s\nAddress = %s\n", oneLine(p.Name), p.privateKey, p.Address)
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\n", p.RemotePublicKey)
	if p.psk != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", p.psk)
	}
	fmt.Fprintf(&b, "Endpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n", p.Endpoint, p.Routes, vpnKeepalive)
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ── Einlesen einer fremden Konfiguration (ausgehender Tunnel) ──

type vpnImport struct {
	PrivateKey, Address, PublicKey, PSK, Endpoint, Routes string
	FullTunnel                                            bool // AllowedIPs enthielt 0.0.0.0/0
}

// vpnParseImport: vom Router erzeugte Client-Konfiguration ([Interface]/[Peer]).
func vpnParseImport(text string) (vpnImport, error) {
	var in vpnImport
	var allowed []string
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[] "))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch section + "." + k {
		case "interface.privatekey":
			in.PrivateKey = v
		case "interface.address":
			for _, a := range strings.Split(v, ",") {
				if p, err := netip.ParsePrefix(strings.TrimSpace(a)); err == nil && p.Addr().Is4() && in.Address == "" {
					in.Address = p.String()
				}
			}
		case "peer.publickey":
			in.PublicKey = v
		case "peer.presharedkey":
			in.PSK = v
		case "peer.endpoint":
			in.Endpoint = v
		case "peer.allowedips":
			for _, a := range strings.Split(v, ",") {
				a = strings.TrimSpace(a)
				switch {
				case a == "0.0.0.0/0":
					in.FullTunnel = true
				case strings.Contains(a, ":"): // IPv6
				case a != "":
					allowed = append(allowed, a)
				}
			}
		}
	}
	in.Routes = strings.Join(allowed, ", ")
	switch {
	case !vpnValidKey(in.PrivateKey):
		return in, errors.New("Konfiguration ohne gültigen PrivateKey ([Interface])")
	case in.Address == "":
		return in, errors.New("Konfiguration ohne IPv4-Address ([Interface])")
	case !vpnValidKey(in.PublicKey):
		return in, errors.New("Konfiguration ohne gültigen PublicKey ([Peer])")
	case in.Endpoint == "":
		return in, errors.New("Konfiguration ohne Endpoint ([Peer])")
	}
	return in, nil
}

// ── Status vom Dienst ───────────────────────────────────────

// vpnParseStatus: Zeilen „Schnittstelle Schlüssel Endpunkt Handshake(Unix) rx tx“.
func vpnParseStatus(text string, now time.Time) map[string]vpnPeerStatus {
	out := map[string]vpnPeerStatus{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		st := vpnPeerStatus{Known: true}
		if f[2] != "(none)" {
			st.RemoteEndpoint = f[2]
		}
		if hs, err := strconv.ParseInt(f[3], 10, 64); err == nil && hs > 0 {
			st.handshakeAt = time.Unix(hs, 0)
			st.Handshake = timeAgo(st.handshakeAt)
			st.Online = now.Sub(st.handshakeAt) < vpnOnlineAfter
		}
		st.rxBytes, _ = strconv.ParseInt(f[4], 10, 64)
		st.txBytes, _ = strconv.ParseInt(f[5], 10, 64)
		st.Received, st.Sent = vpnBytes(st.rxBytes), vpnBytes(st.txBytes)
		out[f[1]] = st
	}
	return out
}

func vpnBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
