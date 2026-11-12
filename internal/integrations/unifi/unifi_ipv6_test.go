package unifi

import (
	"context"
	"strings"
	"testing"
)

const (
	testV6       = "2a02:f90:e198:1700::15"
	testPolicies = "/proxy/network/integration/v1/sites/88f7af54-98f8-306a-a1c7-c9349722b1f6/dns/policies"
)

// lastBody liefert den Rumpf der letzten Anfrage mit der Kennung call.
func (f *fakeUDM) lastBody(t *testing.T, call string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i] == call {
			return f.bodies[i]
		}
	}
	t.Fatalf("Aufruf %q nicht gesehen: %v", call, f.calls)
	return ""
}

func TestCreateAAAA(t *testing.T) {
	f := newFakeUDM(t)
	if _, err := f.client().Create(context.Background(), "nc.firma.de", testV6); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"AAAA_RECORD","enabled":true,"domain":"nc.firma.de","ipv6Address":"` + testV6 + `","ttlSeconds":300}`
	if got := f.lastBody(t, "POST "+testPolicies); got != want {
		t.Errorf("Rumpf:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(f.lastBody(t, "POST "+testPolicies), "ipv4Address") {
		t.Error("ipv4Address darf nicht gesendet werden")
	}
}

func TestUpdateAAAA(t *testing.T) {
	f := newFakeUDM(t)
	if err := f.client().Update(context.Background(), "abc", "nc.firma.de", "2A02:0F90:E198:1700:0:0:0:15"); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"AAAA_RECORD","enabled":true,"domain":"nc.firma.de","ipv6Address":"` + testV6 + `","ttlSeconds":300}`
	if got := f.lastBody(t, "PUT "+testPolicies+"/abc"); got != want {
		t.Errorf("Rumpf:\n got %s\nwant %s", got, want)
	}
}

func TestCreateAAAALegacy(t *testing.T) {
	f := newFakeUDM(t)
	f.legacy = true
	if _, err := f.client().Create(context.Background(), "nc.firma.de", testV6); err != nil {
		t.Fatal(err)
	}
	want := `{"key":"nc.firma.de","record_type":"AAAA","value":"` + testV6 + `","enabled":true,"ttl":0}`
	if got := f.lastBody(t, "POST /proxy/network/v2/api/site/default/static-dns"); got != want {
		t.Errorf("Rumpf:\n got %s\nwant %s", got, want)
	}
}

func TestCheckRecord(t *testing.T) {
	for _, ip := range []string{"fe80::1", "::1", "::", "fd00::1%eth0", "2a02::15/64", "::ffff:10.0.0.1", "fd00::1", "2001:db8::1"} {
		if _, err := checkRecord("nc.firma.de", ip); err == nil || !strings.Contains(err.Error(), "keine nutzbare IPv6-Adresse") {
			t.Errorf("%q: Fehler erwartet, bekam %v", ip, err)
		}
	}
	if v6, err := checkRecord("nc.firma.de", testV6); err != nil || !v6 {
		t.Errorf("IPv6: %v %v", v6, err)
	}
	if v6, err := checkRecord("nc.firma.de", "10.10.0.15"); err != nil || v6 {
		t.Errorf("IPv4: %v %v", v6, err)
	}
	if _, err := checkRecord("nc.firma.de", "kein-ip"); err == nil || !strings.Contains(err.Error(), "keine IPv4-Adresse") {
		t.Errorf("Unsinn: %v", err)
	}
}
