package main

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// Zugangsweg: mitwandernde Adresse (VIP) und Skripte beim Rollenwechsel.
// Dieser Teil enthält nur Einstellung, Prüfungen und Kartenwahl.

// accessConfig ist der Block "access" der gemeinsamen Verbund-Konfiguration.
type accessConfig struct {
	VIP     string     `json:"vip,omitempty"`      // Adresse mit Präfix, z. B. 10.10.0.202/24
	VIPOn   bool       `json:"vip_on,omitempty"`   // Schalter mitwandernde Adresse
	HooksOn bool       `json:"hooks_on,omitempty"` // Schalter Skripte beim Rollenwechsel
	Tested  *vipTested `json:"vip_tested,omitempty"`
}

// vipTested merkt sich den letzten erfolgreichen Test in beide Richtungen.
type vipTested struct {
	Net    string            `json:"net"`    // Netz als Präfix, z. B. 10.10.0.0/24
	Ifaces map[string]string `json:"ifaces"` // Knoten → Netzwerkkarte
	At     time.Time         `json:"at"`
}

// covers: der Test gilt für jede Adresse im selben Netz auf denselben Karten.
// Knoten, die in ifaces fehlen, gelten als unverändert.
func (t *vipTested) covers(vip netip.Prefix, ifaces map[string]string) bool {
	if t == nil {
		return false
	}
	nw, err := netip.ParsePrefix(t.Net)
	if err != nil || !nw.Contains(vip.Addr()) {
		return false
	}
	for node, iface := range ifaces {
		if t.Ifaces[node] != iface {
			return false
		}
	}
	return true
}

// parseVIP: nur IPv4 mit Präfix 8–32; Netz- und Broadcast-Adresse sind nicht erlaubt.
func parseVIP(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("Adresse %q ungültig – erwartet z. B. 10.10.0.202/24", s)
	}
	if !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("Adresse %s: nur IPv4 wird unterstützt", s)
	}
	if p.Bits() < 8 || p.Bits() > 32 {
		return netip.Prefix{}, fmt.Errorf("Adresse %s: Präfix muss zwischen 8 und 32 liegen", s)
	}
	if p.Bits() <= 30 {
		a := p.Addr().As4()
		host := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
		mask := ^uint32(0) >> uint(p.Bits())
		if host&mask == 0 {
			return netip.Prefix{}, fmt.Errorf("Adresse %s ist die Netzadresse", s)
		}
		if host&mask == mask {
			return netip.Prefix{}, fmt.Errorf("Adresse %s ist die Broadcast-Adresse", s)
		}
	}
	return p, nil
}

// validateAccess prüft die Einstellung gegen die Knotenliste des Verbunds.
func validateAccess(cl clusterConfig, a accessConfig) error {
	if a.VIP == "" {
		if a.VIPOn {
			return fmt.Errorf("Mitwandernde Adresse einschalten geht nur mit eingetragener Adresse")
		}
		return nil
	}
	vip, err := parseVIP(a.VIP)
	if err != nil {
		return err
	}
	for _, n := range cl.Nodes {
		for _, ad := range n.Addrs {
			if ip, err := netip.ParseAddr(hostOfAddr(ad)); err == nil && ip == vip.Addr() {
				return fmt.Errorf("Adresse %s gehört schon zu Knoten %s", vip.Addr(), n.ID)
			}
		}
	}
	// Beide Server müssen eine eigene Adresse im Netz der mitwandernden Adresse haben;
	// der Zeuge bekommt die Adresse nie und zählt nicht.
	nw := vip.Masked()
	for _, n := range cl.Nodes {
		if n.Kind != "node" {
			continue
		}
		found := false
		for _, ad := range n.Addrs {
			if ip, err := netip.ParseAddr(hostOfAddr(ad)); err == nil && nw.Contains(ip) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("Adresse %s liegt in keinem Netz von %s", vip.Addr(), n.ID)
		}
	}
	return nil
}

// ifAddr ist eine Adresse einer Netzwerkkarte.
type ifAddr struct {
	Iface  string
	Prefix netip.Prefix
}

// pickInterface wählt die Karte für die mitwandernde Adresse: den Override aus
// node.json, sonst die einzige Karte mit einer Adresse im Netz der Adresse.
func pickInterface(addrs []ifAddr, vip netip.Prefix, override string) (string, error) {
	if override != "" {
		for _, a := range addrs {
			if a.Iface == override {
				return override, nil
			}
		}
		return "", fmt.Errorf("Netzwerkkarte %s (vip_interface) nicht gefunden oder nicht aktiv", override)
	}
	nw := vip.Masked()
	seen := map[string]bool{}
	var names []string
	for _, a := range addrs {
		if nw.Contains(a.Prefix.Addr()) && !seen[a.Iface] {
			seen[a.Iface] = true
			names = append(names, a.Iface)
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return "", fmt.Errorf("keine Netzwerkkarte im Netz %s", nw)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("Netzwerkkarte nicht eindeutig (%s) – in node.json vip_interface eintragen", strings.Join(names, ", "))
}

// listIfAddrs liefert die IPv4-Adressen aller aktiven Karten ohne Loopback.
func listIfAddrs() ([]ifAddr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []ifAddr
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			p, ok := netip.AddrFromSlice(ipn.IP)
			if !ok || !p.Unmap().Is4() {
				continue
			}
			ones, _ := ipn.Mask.Size()
			out = append(out, ifAddr{Iface: ifc.Name, Prefix: netip.PrefixFrom(p.Unmap(), ones)})
		}
	}
	return out, nil
}
