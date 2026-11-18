package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"
)

// naPacket baut eine unaufgeforderte Neighbor Advertisement (ICMPv6 Typ 136)
// mit gesetztem Override-Flag und der Option „Target Link-Layer Address“.
// Die Prüfsumme bleibt null – den Wert setzt der Kernel beim Senden.
func naPacket(addr netip.Addr, mac net.HardwareAddr) []byte {
	b := make([]byte, 32)
	b[0] = 136  // Neighbor Advertisement
	b[4] = 0x20 // nur Override
	a := addr.As16()
	copy(b[8:24], a[:])
	b[24] = 2 // Option: Target Link-Layer Address
	b[25] = 1 // Länge in 8-Byte-Einheiten
	copy(b[26:32], mac)
	return b
}

// icmpNA ist der echte naSender: sendet über einen rohen ICMPv6-Socket.
type icmpNA struct{}

// Announce sendet die Ankündigung zweimal im Abstand von 200 ms an
// ff02::1 (alle Knoten) auf der Karte iface; Absender ist addr.
func (icmpNA) Announce(ctx context.Context, iface string, addr netip.Addr) error {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return fmt.Errorf("IPv6-Adresse %s ankündigen: %w", addr, err)
	}
	if len(ifc.HardwareAddr) != 6 {
		return fmt.Errorf("IPv6-Adresse %s ankündigen: Karte %s hat keine 6-Byte-Hardwareadresse", addr, iface)
	}
	local := addr.String() + "%" + iface
	c, err := icmp.ListenPacket("ip6:ipv6-icmp", local)
	if err != nil {
		// Adresse evtl. noch nicht fertig (DAD) – einmal erneut versuchen.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		c, err = icmp.ListenPacket("ip6:ipv6-icmp", local)
		if err != nil {
			return fmt.Errorf("IPv6-Adresse %s ankündigen: %w", addr, err)
		}
	}
	defer c.Close()

	pc := c.IPv6PacketConn()
	if err := pc.SetMulticastHopLimit(255); err != nil {
		return fmt.Errorf("IPv6-Adresse %s ankündigen: %w", addr, err)
	}
	if err := pc.SetMulticastInterface(ifc); err != nil {
		return fmt.Errorf("IPv6-Adresse %s ankündigen: %w", addr, err)
	}
	pkt := naPacket(addr, ifc.HardwareAddr)
	dst := &net.IPAddr{IP: net.ParseIP("ff02::1"), Zone: iface}
	cm := &ipv6.ControlMessage{HopLimit: 255, IfIndex: ifc.Index}
	for i := 0; i < 2; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		if _, err := pc.WriteTo(pkt, cm, dst); err != nil {
			return fmt.Errorf("IPv6-Adresse %s ankündigen: %w", addr, err)
		}
	}
	return nil
}
