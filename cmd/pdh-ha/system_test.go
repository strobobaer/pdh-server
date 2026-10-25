package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultGateway(t *testing.T) {
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000A0A\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
		"eth0\t00000000\t01000A0A\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
	gw, err := defaultGateway(route)
	if err != nil || gw != "10.10.0.1" {
		t.Fatalf("%q %v", gw, err)
	}
	if _, err := defaultGateway("Iface\tDestination\tGateway\n"); err == nil {
		t.Fatal("ohne Standardroute kein Fehler")
	}
}

func kvpRecord(key, val string) []byte {
	b := make([]byte, 512+2048)
	copy(b, key)
	copy(b[512:], val)
	return b
}

func TestHostIDHyperV(t *testing.T) {
	data := append(kvpRecord("VirtualMachineName", "pdh1"), kvpRecord("PhysicalHostNameFullyQualified", "hv01.firma.local")...)
	if got := kvpValue(data, "PhysicalHostNameFullyQualified"); got != "hv01.firma.local" {
		t.Fatalf("%q", got)
	}
	if got := kvpValue(data, "Fehlt"); got != "" {
		t.Fatalf("%q", got)
	}
	if got := hostIDFrom(data, "microsoft", nil); got != "hyperv:hv01.firma.local" {
		t.Fatalf("Hyper-V: %q", got)
	}
	if got := hostIDFrom(nil, "none", []byte("4C4C4544-0042\n")); got != "dmi:4c4c4544-0042" {
		t.Fatalf("Blech: %q", got)
	}
	if got := hostIDFrom(nil, "kvm", []byte("x")); got != "" {
		t.Fatalf("VM ohne Host-Kennung: %q", got)
	}
}

func TestUFWRules(t *testing.T) {
	cl := clusterConfig{Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{"10.0.0.1:7480", "10.8.0.1:7480"}},
		{ID: "b", Kind: "node", Addrs: []string{"10.0.0.2:7480", "10.8.0.2:7480"}},
	}}
	want := [][]string{
		{"allow", "from", "10.0.0.2", "to", "any", "port", "7480", "proto", "tcp", "comment", "pdh-ha"},
		{"allow", "from", "10.8.0.2", "to", "any", "port", "7480", "proto", "tcp", "comment", "pdh-ha"},
	}
	if got := ufwRules(cl, "a", false); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	got := ufwRules(cl, "a", true)
	if len(got) != 3 || strings.Join(got[2], " ") != "allow 7480/tcp comment pdh-ha-join" {
		t.Fatalf("mit Beitritt: %v", got)
	}
}

func TestUFWNumbered(t *testing.T) {
	out := `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 7480/tcp                   ALLOW IN    10.0.0.2                   # pdh-ha
[ 3] 7480/tcp                   ALLOW IN    Anywhere                   # pdh-ha-join
[ 4] 7480/tcp (v6)              ALLOW IN    Anywhere (v6)              # pdh-ha-join
[ 5] 80/tcp                     ALLOW IN    Anywhere                   # pdh-ha-andere
`
	if got := ufwNumbered(out); !reflect.DeepEqual(got, []int{4, 3, 2}) {
		t.Fatalf("%v", got)
	}
}

func TestIDFromHostname(t *testing.T) {
	cases := map[string]string{
		"PDH-Server.firma.local": "pdh-server-firma-local",
		"---":                    "server",
		strings.Repeat("a", 40):  strings.Repeat("a", 32),
		"Knoten_1":               "knoten-1",
	}
	for in, want := range cases {
		if got := idFromHostname(in); got != want || !validID(got) {
			t.Errorf("%q → %q", in, got)
		}
	}
}
