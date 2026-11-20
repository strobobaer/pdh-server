// Package hostnet liest den Netzwerkzustand eines Linux-Servers: Adresse,
// Standardroute, DNS-Server, fest oder DHCP, Netzwerkkarte und ob das
// System Einstellungen über netplan und systemd-networkd zulässt.
//
// Das Paket führt selbst keine Befehle aus. Der Aufrufer reicht eine
// RunFunc und eine Lesefunktion herein: der Update-Agent läuft über nsenter
// im Wirtssystem, pdh-ha ruft die Befehle als root direkt auf. So lassen
// sich alle Auswertungen mit aufgezeichneten Ausgaben ohne Linux prüfen.
package hostnet

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Art der Adresse.
const (
	ModeStatic  = "static"
	ModeDHCP    = "dhcp"
	ModeUnknown = "unknown"
)

// Summary ist die Kurzfassung des Netzwerkzustands. Sie reist im Lebenszeichen
// von pdh-ha, daher kurze Schlüssel und nichts Überflüssiges.
type Summary struct {
	Iface   string   `json:"if,omitempty"`   // Netzwerkkarte der Standardroute
	Addr    string   `json:"addr,omitempty"` // eigene IPv4-Adresse mit Präfix
	Mode    string   `json:"mode"`           // static, dhcp, unknown
	Gateway string   `json:"gw,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	IPv6    []string `json:"v6,omitempty"` // nur zur Anzeige
	Netplan bool     `json:"np,omitempty"` // Aktionen möglich (Ubuntu, netplan, systemd-networkd)
}

// DNS4 liefert nur die IPv4-DNS-Server.
func (s Summary) DNS4() []string {
	var out []string
	for _, d := range s.DNS {
		if a, err := netip.ParseAddr(d); err == nil && a.Is4() {
			out = append(out, d)
		}
	}
	return out
}

// RunFunc führt einen Befehl aus und liefert seine Ausgabe.
type RunFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Collect sammelt die Zusammenfassung. Jeder fehlgeschlagene Befehl lässt nur
// sein Feld leer. Ohne Netplan-Unterstützung oder ohne Adresse ist die Art
// „unbekannt“; Adresse, Gateway und DNS bleiben trotzdem gefüllt.
func Collect(ctx context.Context, run RunFunc, readFile func(string) ([]byte, error)) Summary {
	s := Summary{Mode: ModeUnknown}
	if out, err := run(ctx, "ip", "-4", "route", "show", "default"); err == nil {
		s.Gateway, s.Iface = ParseDefaultRoute(string(out))
	}
	dynamic := false
	if s.Iface != "" {
		if out, err := run(ctx, "ip", "-o", "addr", "show", "dev", s.Iface); err == nil {
			s.Addr, dynamic, s.IPv6 = ParseAddrs(string(out), s.Iface)
		}
	} else if out, err := run(ctx, "ip", "-o", "addr", "show", "scope", "global"); err == nil {
		// keine Standardroute: erste globale Adresse nehmen
		var iface string
		s.Addr, iface, dynamic, s.IPv6 = parseAddrs(string(out), "")
		s.Iface = iface
	}
	if s.Iface != "" {
		if out, err := run(ctx, "resolvectl", "dns", s.Iface); err == nil {
			s.DNS = ParseResolvectl(string(out))
		}
	}
	if len(s.DNS) == 0 {
		for _, f := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
			if b, err := readFile(f); err == nil {
				if s.DNS = ParseResolvConf(string(b)); len(s.DNS) > 0 {
					break
				}
			}
		}
	}
	s.Netplan = netplanSupported(ctx, run, readFile)
	if s.Netplan && s.Addr != "" {
		s.Mode = ModeStatic
		if dynamic {
			s.Mode = ModeDHCP
		}
	}
	return s
}

// netplanSupported: Ubuntu, systemd-networkd aktiv und netplan lesbar.
func netplanSupported(ctx context.Context, run RunFunc, readFile func(string) ([]byte, error)) bool {
	b, err := readFile("/etc/os-release")
	if err != nil || !osReleaseIsUbuntu(string(b)) {
		return false
	}
	out, err := run(ctx, "systemctl", "is-active", "systemd-networkd")
	if err != nil || strings.TrimSpace(string(out)) != "active" {
		return false
	}
	_, err = run(ctx, "netplan", "get")
	return err == nil
}

func osReleaseIsUbuntu(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			return strings.Trim(v, `"'`) == "ubuntu"
		}
	}
	return false
}

// ParseDefaultRoute wertet `ip -4 route show default` aus (erste Zeile, die
// Kernel sortiert nach Metrik). Ohne „via“ bleibt das Gateway leer.
func ParseDefaultRoute(out string) (gw, iface string) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "default" {
			continue
		}
		for i := 1; i+1 < len(f); i++ {
			switch f[i] {
			case "via":
				if a, err := netip.ParseAddr(f[i+1]); err == nil && a.Is4() {
					gw = f[i+1]
				}
			case "dev":
				iface = f[i+1]
			}
		}
		return gw, iface
	}
	return "", ""
}

// ParseAddrs wertet `ip -o addr show dev <karte>` aus. Addr ist die erste
// globale IPv4-Adresse, die nicht „secondary“ und kein /32 ist (die
// mitwandernde Adresse des Ausfallschutzes zählt nicht als eigene); dynamic
// bedeutet DHCP. v6 sind globale IPv6-Adressen ohne temporary/deprecated.
func ParseAddrs(out, iface string) (addr string, dynamic bool, v6 []string) {
	addr, _, dynamic, v6 = parseAddrs(out, iface)
	return
}

func parseAddrs(out, iface string) (addr, ifc string, dynamic bool, v6 []string) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// 2: eth0    inet 10.10.0.17/24 brd … scope global dynamic eth0\ valid_lft …
		if len(f) < 4 {
			continue
		}
		name := strings.TrimSuffix(f[1], ":")
		if iface != "" && name != iface && !strings.HasPrefix(name, iface+":") {
			continue
		}
		has := func(w string) bool {
			for _, x := range f[4:] {
				if strings.TrimSuffix(x, `\`) == w {
					return true
				}
			}
			return false
		}
		if !has("global") {
			continue
		}
		switch f[2] {
		case "inet":
			p, err := netip.ParsePrefix(f[3])
			if err != nil || p.Bits() == 32 || has("secondary") || addr != "" {
				continue
			}
			addr, ifc, dynamic = f[3], name, has("dynamic")
		case "inet6":
			p, err := netip.ParsePrefix(f[3])
			if err != nil || has("temporary") || has("deprecated") {
				continue
			}
			v6 = append(v6, p.Addr().String())
		}
	}
	return
}

// ParseResolvectl wertet `resolvectl dns <karte>` aus
// („Link 2 (eth0): 10.10.0.1 1.1.1.1“). Ohne Server (leere Liste) bleibt es leer.
func ParseResolvectl(out string) []string {
	var res []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest := ""
		if i := strings.Index(line, "):"); i >= 0 && strings.HasPrefix(line, "Link") {
			rest = line[i+2:]
		} else if r, ok := strings.CutPrefix(line, "Global:"); ok {
			rest = r
		}
		for _, w := range strings.Fields(rest) {
			if i := strings.IndexAny(w, "#%"); i >= 0 {
				w = w[:i] // „1.1.1.1#cloudflare-dns.com“, „fe80::1%eth0“
			}
			if a, err := netip.ParseAddr(w); err == nil {
				res = appendUnique(res, a.String())
			}
		}
	}
	return res
}

// ParseResolvConf liest die „nameserver“-Zeilen; der lokale Zwischenspeicher
// 127.0.0.53 ist kein echter Server und fällt weg.
func ParseResolvConf(content string) []string {
	var res []string
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		a, err := netip.ParseAddr(f[1])
		if err != nil || a.String() == "127.0.0.53" {
			continue
		}
		res = appendUnique(res, a.String())
	}
	return res
}

func appendUnique(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

// ValidDNS: ein brauchbarer IPv4-DNS-Server (nicht unspezifiziert, Loopback,
// Link-Local, Multicast, 255.255.255.255).
func ValidDNS(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return false
	}
	return !a.IsUnspecified() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!a.IsMulticast() && a != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// probeWait: längste Wartezeit, wenn der Aufrufer keine Frist setzt.
const probeWait = 3 * time.Second

// ProbeDNS fragt den Server („a.b.c.d“ oder „a.b.c.d:port“) per UDP nach
// github.com. Jede Antwort mit gleicher Kennung und gesetztem QR-Bit zählt,
// auch NXDOMAIN und SERVFAIL – er lebt dann. Sonst ein Fehler.
func ProbeDNS(ctx context.Context, server string) error {
	host, addr := server, server
	if h, _, err := net.SplitHostPort(server); err == nil {
		host = h
	} else {
		addr = net.JoinHostPort(server, "53")
	}
	fail := fmt.Errorf("Der DNS-Server %s antwortet nicht", host)

	var idb [2]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return err
	}
	id := uint16(idb[0])<<8 | uint16(idb[1])
	name, err := dnsmessage.NewName("github.com.")
	if err != nil {
		return err
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	pkt, err := msg.Pack()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, probeWait) // eine frühere Frist des Aufrufers gilt weiter
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return fail
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write(pkt); err != nil {
		return fail
	}
	buf := make([]byte, 1500)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return fail // Frist um oder Anschluss geschlossen (ICMP „nicht erreichbar“)
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err == nil && h.ID == id && h.Response {
			return nil
		}
		// falsche Kennung oder keine Antwort: weiter warten bis zur Frist
	}
}
