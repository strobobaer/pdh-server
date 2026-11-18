package main

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
)

func TestNAPacket(t *testing.T) {
	mac, _ := net.ParseMAC("00:15:5d:00:b2:02")
	b := naPacket(netip.MustParseAddr("2a02:f90:e198:1700::15"), mac)
	if len(b) != 32 || b[0] != 136 || b[1] != 0 || b[4] != 0x20 || b[5] != 0 {
		t.Fatalf("Kopf: % x", b[:8])
	}
	if netip.AddrFrom16([16]byte(b[8:24])).String() != "2a02:f90:e198:1700::15" {
		t.Fatal("Ziel")
	}
	if b[24] != 2 || b[25] != 1 || !bytes.Equal(b[26:32], mac) {
		t.Fatalf("Option: % x", b[24:])
	}
}
