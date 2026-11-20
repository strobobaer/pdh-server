package omada

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const ssidPass = "Sommerwind-2026"

func TestLanNetworksV3(t *testing.T) {
	f := newFake(t)
	f.lans = []map[string]any{
		{"id": "n1", "name": "Büro", "vlan": 20, "gatewaySubnet": "192.168.20.1/24", "purpose": 1},
		{"id": "n2", "name": "Halle", "vlan": "30", "gatewaySubnet": "10.0.30.1/255.255.255.0"},
		{"id": "n3", "name": "Ohne Subnetz", "vlan": 40},
		{"id": "n4", "name": "Schalter", "vlan": 50, "ip": map[string]any{"ip": "172.16.5.9", "netmask": "255.255.0.0"}},
		{"id": "n5", "name": "Kaputt", "vlan": 60, "gatewaySubnet": "kein-netz"},
	}
	got, err := f.client().LanNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []LanNetwork{
		{ID: "n1", Name: "Büro", Subnet: "192.168.20.0/24", VLAN: 20},
		{ID: "n2", Name: "Halle", Subnet: "10.0.30.0/24", VLAN: 30},
		{ID: "n3", Name: "Ohne Subnetz", VLAN: 40},
		{ID: "n4", Name: "Schalter", Subnet: "172.16.0.0/16", VLAN: 50},
		{ID: "n5", Name: "Kaputt", VLAN: 60},
	}
	if len(got) != len(want) {
		t.Fatalf("Netze: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Netz %d: %+v, erwartet %+v", i, got[i], want[i])
		}
	}
}

func TestLanNetworksFallback(t *testing.T) {
	f := newFake(t)
	f.noV3, f.noV2 = true, true
	f.lans = []map[string]any{{"id": "n1", "name": "Alt", "vlan": 7}}
	got, err := f.client().LanNetworks(context.Background())
	if err != nil || len(got) != 1 || got[0].Name != "Alt" {
		t.Fatalf("Netze: %+v, %v", got, err)
	}
	var order []string
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/lan-networks") {
			order = append(order, c[strings.Index(c, "/openapi/")+9:strings.Index(c, "/openapi/")+11])
		}
	}
	if strings.Join(order, ",") != "v3,v2,v1" {
		t.Errorf("Reihenfolge: %v", order)
	}
}

func TestLanNetworksNoFallbackOnAuth(t *testing.T) {
	f := newFake(t)
	f.secret = "anders"
	_, err := f.client().LanNetworks(context.Background())
	if err == nil {
		t.Fatal("Anmeldefehler erwartet")
	}
	for _, c := range f.calls {
		if strings.Contains(c, "lan-networks") {
			t.Errorf("unerwarteter Abruf: %s", c)
		}
	}
}

func TestSSIDsDetail(t *testing.T) {
	f := newFake(t)
	f.ssids = []map[string]any{
		{"id": "s1", "name": "Gast", "broadcast": false, "security": 3, "vlanEnable": true, "vlanId": 30},
		{"id": "s2", "name": "Firma", "broadcast": true, "security": 2, "vlanEnable": true, "vlanId": "20"},
		{"id": "s3", "name": "Offen", "security": 0, "vlanEnable": false, "vlanId": 99},
		{"id": "s4", "name": "PPSK", "security": 4},
	}
	f.ssidDetail = map[string]map[string]any{
		"s1": {"pskSetting": map[string]any{"versionPsk": 2, "securityKey": ssidPass}},
		"s2": {"pskSetting": map[string]any{"securityKey": "darf-nicht-abgefragt-werden"}},
	}
	c := f.client()
	got, err := c.SSIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("WLANs: %+v", got)
	}
	if g := got[0]; g.Passphrase != ssidPass || !g.Hidden || g.Security != "personal" || g.VLAN != 30 {
		t.Errorf("WLAN 1: %#v", g.Passphrase)
	}
	if g := got[1]; g.Security != "enterprise" || g.Passphrase != "" || g.Hidden || g.VLAN != 20 {
		t.Errorf("WLAN 2: %+v", g)
	}
	if g := got[2]; g.Security != "open" || g.VLAN != 0 || g.Hidden {
		t.Errorf("WLAN 3: %+v", g)
	}
	if g := got[3]; g.Security != "" || g.VLAN != 0 {
		t.Errorf("WLAN 4: %+v", g)
	}
	base := "GET /openapi/v1/" + testOmadaID + "/sites/site-a/wireless-network/ssids/"
	if f.count(base+"s1") != 1 || f.count(base+"s2") != 0 || f.count(base+"s3") != 0 {
		t.Errorf("Einzelabfragen: %v", f.calls)
	}
}

func TestSSIDDetailMissing(t *testing.T) {
	f := newFake(t)
	f.noDetail = true
	f.ssids = []map[string]any{{"id": "s1", "name": "Gast", "security": 3}}
	got, err := f.client().SSIDs(context.Background())
	if err != nil || len(got) != 1 || got[0].Passphrase != "" || got[0].Security != "personal" {
		t.Fatalf("WLANs: %+v, %v", got, err)
	}
}

func TestSSIDDetailRespectsContext(t *testing.T) {
	f := newFake(t)
	f.ssids = []map[string]any{{"id": "s1", "name": "A", "security": 3}, {"id": "s2", "name": "B", "security": 3}}
	ctx, cancel := context.WithCancel(context.Background())
	c := f.client()
	if _, err := c.SSIDs(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := c.SSIDs(ctx); err == nil {
		t.Error("abgebrochener Kontext muss einen Fehler liefern")
	}
}

func TestSSIDStringHidesPassphrase(t *testing.T) {
	w := SSID{ID: "1", Name: "Gast", Security: "personal", Passphrase: ssidPass}
	outs := []string{
		fmt.Sprintf("%v", w), fmt.Sprintf("%+v", w), fmt.Sprintf("%#v", w), fmt.Sprintf("%s", w),
		fmt.Sprint([]SSID{w}), fmt.Sprintf("%v", &w), fmt.Sprintf("%+v", []*SSID{&w}),
		fmt.Sprintf("%#v", []SSID{w}), w.String(), w.GoString(),
		fmt.Sprintf("%q", w), fmt.Sprintf("%x", w), fmt.Sprintf("%v", map[string]SSID{"a": w}),
		fmt.Sprintln(w), fmt.Sprintln(&w), fmt.Sprintf("%#v", map[string]*SSID{"a": &w}),
	}
	for _, v := range []any{w, &w, []SSID{w}, map[string]SSID{"a": w}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, string(b))
	}
	for i, s := range outs {
		if strings.Contains(s, ssidPass) || strings.Contains(s, "Sommer") {
			t.Errorf("Ausgabe %d enthält das Kennwort: %s", i, s)
		}
		if i != 12 && !strings.Contains(s, "Gast") { // %x gibt Hex aus
			t.Errorf("Ausgabe %d ohne Namen: %s", i, s)
		}
	}
}

func TestOmadaNetOnlyReads(t *testing.T) {
	f := newFake(t)
	f.lans = []map[string]any{{"id": "n1", "name": "A", "vlan": 1}}
	f.ssids = []map[string]any{{"id": "s1", "name": "A", "security": 3}}
	f.ssidDetail = map[string]map[string]any{"s1": {"pskSetting": map[string]any{"securityKey": ssidPass}}}
	c := f.client()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.LanNetworks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SSIDs(ctx); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if !strings.HasPrefix(call, http.MethodGet+" ") && call != "POST /openapi/authorize/token" {
			t.Errorf("schreibender Aufruf: %s", call)
		}
	}
}

// Eine Antwort ohne Liste ergibt einen Fehler (die Seitenzahl selbst begrenzt paged mit maxPages).
func TestLanNetworksNoListIsError(t *testing.T) {
	f := newFake(t)
	f.noData = true
	if _, err := f.client().LanNetworks(context.Background()); err == nil {
		t.Error("Fehler erwartet")
	}
}
