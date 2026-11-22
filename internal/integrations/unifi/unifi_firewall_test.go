package unifi

// Firewall-Zonen und -Regeln (offizielle Schnittstelle ab UniFi Network 10).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFirewallZones(t *testing.T) {
	f := newFakeUDM(t)
	f.lists = map[string][]byte{"/firewall/zones": []byte(`{"offset":0,"limit":200,"count":2,"totalCount":2,"data":[
		{"id":"z-int","name":"Internal","networkIds":["n1","n2"],"metadata":{"origin":"SYSTEM_DEFINED"}},
		{"id":"z-iot","name":"IoT","networkIds":["n3"],"metadata":{"origin":"USER_DEFINED"}}]}`)}
	zs, err := f.client().FirewallZones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(zs) != 2 || zs[0].ID != "z-int" || len(zs[0].NetworkIDs) != 2 || zs[1].Name != "IoT" || zs[1].NetworkIDs[0] != "n3" {
		t.Errorf("Zonen: %+v", zs)
	}
}

func TestFirewallOldVersion(t *testing.T) {
	f := newFakeUDM(t)
	f.gone = "zones"
	if _, err := f.client().FirewallZones(context.Background()); !errors.Is(err, ErrFirewallUnsupported) {
		t.Errorf("Zonen bei alter Version: %v", err)
	}
}

func TestFirewallPolicies(t *testing.T) {
	f := newFakeUDM(t)
	f.lists = map[string][]byte{"/firewall/policies": []byte(`{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[
		{"id":"p1","name":"PDH – ESPPDH-Update IoT","enabled":true,"index":10000}]}`)}
	ps, err := f.client().FirewallPolicies(context.Background())
	if err != nil || len(ps) != 1 || ps[0] != (FirewallPolicy{ID: "p1", Name: "PDH – ESPPDH-Update IoT", Enabled: true}) {
		t.Errorf("Regeln: %+v, %v", ps, err)
	}
}

func TestCreateFirewallAllow(t *testing.T) {
	f := newFakeUDM(t)
	f.lists = map[string][]byte{"/firewall/policies": []byte(`{"id":"neu-1","name":"PDH – ESPPDH-Update IoT","enabled":true}`)}
	id, err := f.client().CreateFirewallAllow(context.Background(), FirewallAllow{Name: "PDH – ESPPDH-Update IoT", Description: "vom PDH",
		SrcZone: "z-int", DstZone: "z-iot", SrcIPs: []string{"10.10.0.14", "10.10.0.17"}, DstSubnet: "10.20.0.0/24", Ports: []int{3232, 8266}})
	if err != nil || id != "neu-1" {
		t.Fatalf("anlegen: %q, %v", id, err)
	}
	var body string
	for i, c := range f.calls {
		if strings.HasPrefix(c, "POST ") && strings.HasSuffix(c, "/firewall/policies") {
			body = f.bodies[i]
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("Körper %q: %v", body, err)
	}
	want := `{"action":{"allowReturnTraffic":true,"type":"ALLOW"},"description":"vom PDH",` +
		`"destination":{"trafficFilter":{"ipAddressFilter":{"items":[{"type":"SUBNET","value":"10.20.0.0/24"}],"matchOpposite":false,"type":"IP_ADDRESSES"},` +
		`"portFilter":{"items":[{"type":"PORT_NUMBER","value":3232},{"type":"PORT_NUMBER","value":8266}],"matchOpposite":false,"type":"PORTS"},"type":"IP_ADDRESS"},"zoneId":"z-iot"},` +
		`"enabled":true,"ipProtocolScope":{"ipVersion":"IPV4","protocolFilter":{"matchOpposite":false,"protocol":{"name":"tcp"},"type":"NAMED_PROTOCOL"}},` +
		`"loggingEnabled":false,"name":"PDH – ESPPDH-Update IoT",` +
		`"source":{"trafficFilter":{"ipAddressFilter":{"items":[{"type":"IP_ADDRESS","value":"10.10.0.14"},{"type":"IP_ADDRESS","value":"10.10.0.17"}],"matchOpposite":false,"type":"IP_ADDRESSES"},"type":"IP_ADDRESS"},"zoneId":"z-int"}}`
	norm, _ := json.Marshal(got)
	if string(norm) != want {
		t.Errorf("Körper:\n%s\nerwartet:\n%s", norm, want)
	}
}

func TestCreateFirewallAllowRejectsBadInput(t *testing.T) {
	f := newFakeUDM(t)
	for _, in := range []FirewallAllow{
		{Name: "x", SrcZone: "a", DstZone: "b", SrcIPs: []string{"10.0.0.1"}, DstSubnet: "kein-netz", Ports: []int{3232}},
		{Name: "x", SrcZone: "a", DstZone: "b", SrcIPs: []string{"::1"}, DstSubnet: "10.0.0.0/24", Ports: []int{3232}},
		{Name: "x", SrcZone: "a", DstZone: "b", SrcIPs: nil, DstSubnet: "10.0.0.0/24", Ports: []int{3232}},
		{Name: "x", SrcZone: "a", DstZone: "b", SrcIPs: []string{"10.0.0.1"}, DstSubnet: "10.0.0.0/24", Ports: []int{0}},
		{Name: "", SrcZone: "a", DstZone: "b", SrcIPs: []string{"10.0.0.1"}, DstSubnet: "10.0.0.0/24", Ports: []int{3232}},
	} {
		if _, err := f.client().CreateFirewallAllow(context.Background(), in); err == nil {
			t.Errorf("angenommen: %+v", in)
		}
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "POST ") {
			t.Errorf("trotz ungültiger Angaben gesendet: %s", c)
		}
	}
}
