package main

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParseVIP(t *testing.T) {
	p, err := parseVIP("10.10.0.202/24")
	if err != nil || p.Addr().String() != "10.10.0.202" || p.Bits() != 24 {
		t.Fatalf("gültige Adresse abgelehnt: %v %v", p, err)
	}
	for _, s := range []string{"10.10.0.202", "fe80::1/64", "10.10.0.0/24", "10.10.0.255/24", "10.10.0.202/7", "", "x/24"} {
		if _, err := parseVIP(s); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", s)
		}
	}
}

func accessCluster() clusterConfig {
	return clusterConfig{Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{"10.10.0.14:7480"}},
		{ID: "b", Kind: "node", Addrs: []string{"10.10.0.17"}},
		{ID: "w", Kind: "witness", Addrs: []string{"10.10.0.20:7480"}},
	}}
}

func TestValidateAccess(t *testing.T) {
	cl := accessCluster()
	if err := validateAccess(cl, accessConfig{VIP: "10.10.0.15/24", VIPOn: true}); err != nil {
		t.Fatalf("gültige Einstellung abgelehnt: %v", err)
	}
	if err := validateAccess(cl, accessConfig{}); err != nil {
		t.Fatalf("leere Einstellung abgelehnt: %v", err)
	}
	err := validateAccess(cl, accessConfig{VIP: "10.10.0.14/24"})
	if err == nil || !strings.Contains(err.Error(), "gehört schon zu Knoten a") {
		t.Errorf("eigene Adresse: %v", err)
	}
	err = validateAccess(cl, accessConfig{VIP: "10.10.0.20/24"})
	if err == nil || !strings.Contains(err.Error(), "gehört schon zu Knoten w") {
		t.Errorf("Adresse des Zeugen: %v", err)
	}
	err = validateAccess(cl, accessConfig{VIP: "192.168.1.5/24"})
	if err == nil || !strings.Contains(err.Error(), "liegt in keinem Netz von") {
		t.Errorf("fremdes Netz: %v", err)
	}
	// nur ein Knoten im Netz der Adresse genügt nicht
	cl2 := accessCluster()
	cl2.Nodes[1].Addrs = []string{"10.20.0.17"}
	err = validateAccess(cl2, accessConfig{VIP: "10.10.0.15/24"})
	if err == nil || !strings.Contains(err.Error(), "liegt in keinem Netz von b") {
		t.Errorf("Knoten b im anderen Netz: %v", err)
	}
	if err := validateAccess(cl, accessConfig{VIPOn: true}); err == nil {
		t.Error("vip_on ohne vip hätte abgelehnt werden müssen")
	}
	if err := validateAccess(cl, accessConfig{VIP: "murks"}); err == nil {
		t.Error("ungültige Adresse hätte abgelehnt werden müssen")
	}
}

func TestPickInterface(t *testing.T) {
	vip := netip.MustParsePrefix("10.10.0.15/24")
	addrs := []ifAddr{
		{"eth0", netip.MustParsePrefix("10.10.0.14/24")},
		{"eth1", netip.MustParsePrefix("192.168.5.2/24")},
	}
	if n, err := pickInterface(addrs, vip, ""); err != nil || n != "eth0" {
		t.Fatalf("eine passende Karte: %q %v", n, err)
	}
	if n, err := pickInterface(addrs, vip, "eth1"); err != nil || n != "eth1" {
		t.Fatalf("Override vorhanden: %q %v", n, err)
	}
	if _, err := pickInterface(addrs, vip, "eth9"); err == nil {
		t.Fatal("Override nicht vorhanden hätte abgelehnt werden müssen")
	}
}

func TestPickInterfaceAmbiguous(t *testing.T) {
	vip := netip.MustParsePrefix("10.10.0.15/24")
	addrs := []ifAddr{
		{"eth0", netip.MustParsePrefix("10.10.0.14/24")},
		{"docker0", netip.MustParsePrefix("10.10.0.1/24")},
	}
	_, err := pickInterface(addrs, vip, "")
	if err == nil || !strings.Contains(err.Error(), "nicht eindeutig") || !strings.Contains(err.Error(), "vip_interface") {
		t.Fatalf("mehrdeutig: %v", err)
	}
	_, err = pickInterface([]ifAddr{{"eth1", netip.MustParsePrefix("192.168.5.2/24")}}, vip, "")
	if err == nil || !strings.Contains(err.Error(), "keine Netzwerkkarte im Netz") {
		t.Fatalf("keine passende: %v", err)
	}
	// dieselbe Karte mit zwei Adressen im Netz ist eindeutig
	two := []ifAddr{
		{"eth0", netip.MustParsePrefix("10.10.0.14/24")},
		{"eth0", netip.MustParsePrefix("10.10.0.202/24")},
	}
	if n, err := pickInterface(two, vip, ""); err != nil || n != "eth0" {
		t.Fatalf("eine Karte, zwei Adressen: %q %v", n, err)
	}
}

func TestVIPTestedCovers(t *testing.T) {
	ts := &vipTested{Net: "10.10.0.0/24", Ifaces: map[string]string{"a": "eth0", "b": "ens3"}, At: time.Now()}
	vip := netip.MustParsePrefix("10.10.0.202/24")
	if !ts.covers(vip, map[string]string{"a": "eth0", "b": "ens3"}) {
		t.Error("gleiches Netz und Karten müssen gelten")
	}
	if !ts.covers(vip, map[string]string{"a": "eth0"}) {
		t.Error("Teilmenge (nur eigener Knoten) muss gelten")
	}
	if ts.covers(netip.MustParsePrefix("10.20.0.5/24"), map[string]string{"a": "eth0"}) {
		t.Error("anderes Netz darf nicht gelten")
	}
	if ts.covers(vip, map[string]string{"a": "eth1"}) {
		t.Error("andere Karte darf nicht gelten")
	}
	if ts.covers(vip, map[string]string{"c": "eth0"}) {
		t.Error("unbekannter Knoten darf nicht gelten")
	}
	var nilT *vipTested
	if nilT.covers(vip, nil) {
		t.Error("nil darf nicht gelten")
	}
}

func TestClusterConfigAccessRoundTrip(t *testing.T) {
	s := store{etc: t.TempDir(), state: t.TempDir()}
	if err := s.writeEtc("cluster.json", []byte(`{"cluster_id":"x","version":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := s.loadCluster()
	if err != nil || c.Access.VIP != "" || c.Access.VIPOn || c.Access.HooksOn || c.Access.Tested != nil {
		t.Fatalf("alte Datei: %+v %v", c.Access, err)
	}
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c.Access = accessConfig{VIP: "10.10.0.15/24", VIPOn: true, HooksOn: true,
		Tested: &vipTested{Net: "10.10.0.0/24", Ifaces: map[string]string{"a": "eth0"}, At: at}}
	if err := s.saveCluster(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.loadCluster()
	if err != nil {
		t.Fatal(err)
	}
	a := got.Access
	if a.VIP != "10.10.0.15/24" || !a.VIPOn || !a.HooksOn || a.Tested == nil ||
		a.Tested.Net != "10.10.0.0/24" || a.Tested.Ifaces["a"] != "eth0" || !a.Tested.At.Equal(at) {
		t.Fatalf("Round-Trip verlustbehaftet: %+v", a)
	}
}

func TestNodeConfigVIPInterface(t *testing.T) {
	s := store{etc: t.TempDir(), state: t.TempDir()}
	if err := s.saveNode(nodeConfig{ID: "a", Kind: "node", VIPInterface: "ens3"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.loadNode()
	if err != nil || n.VIPInterface != "ens3" {
		t.Fatalf("%+v %v", n, err)
	}
}
