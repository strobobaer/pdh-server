package web

import (
	"context"
	"net"
	"reflect"
	"testing"
)

func TestCertNameInZone(t *testing.T) {
	ok := []struct{ name, zone, want string }{
		{"NC.Strobl-Home.net.", "strobl-home.net", "nc.strobl-home.net"},
		{"strobl-home.net", "strobl-home.net", "strobl-home.net"},
		{"a.b.strobl-home.net", "Strobl-Home.net.", "a.b.strobl-home.net"},
	}
	for _, c := range ok {
		got, err := certNameInZone(c.name, c.zone)
		if err != nil || got != c.want {
			t.Errorf("certNameInZone(%q,%q) = %q, %v; erwartet %q", c.name, c.zone, got, err, c.want)
		}
	}
	bad := []struct{ name, zone string }{
		{"*.strobl-home.net", "strobl-home.net"},
		{"nc.andere.de", "strobl-home.net"},
		{"evilstrobl-home.net", "strobl-home.net"},
		{"", "strobl-home.net"},
		{"1.2.3.4", "2.3.4"},
		{"nc.strobl-home.net", ""},
	}
	for _, c := range bad {
		if got, err := certNameInZone(c.name, c.zone); err == nil {
			t.Errorf("certNameInZone(%q,%q) = %q ohne Fehler", c.name, c.zone, got)
		}
	}
}

func TestCertSuggested(t *testing.T) {
	cases := []struct {
		host, nc string
		want     []string
	}{
		{"pdh.firma.de", "https://nc.firma.de/", []string{"pdh.firma.de", "nc.firma.de"}},
		{"pdh.firma.de", "https://pdh.firma.de:8443/x", []string{"pdh.firma.de"}},
		{"10.10.0.15", "http://192.168.1.2/", nil},
		{"", "", nil},
		{"PDH.Firma.de:8080", "", []string{"pdh.firma.de"}},
	}
	for _, c := range cases {
		got := certSuggested(c.host, c.nc)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("certSuggested(%q,%q) = %v; erwartet %v", c.host, c.nc, got, c.want)
		}
	}
}

func TestCertLANState(t *testing.T) {
	own := []string{"10.10.0.17", "10.10.0.15"}
	if got := certLANState([]string{"10.10.0.15"}, own); got != "direkt" {
		t.Errorf("eigene Adresse: %q", got)
	}
	if got := certLANState([]string{"188.114.96.3"}, own); got != "cloudflare" {
		t.Errorf("fremde Adresse: %q", got)
	}
	if got := certLANState(nil, own); got != "unbekannt" {
		t.Errorf("ohne Antwort: %q", got)
	}
}

func TestCertOwnAddrsOhneVerbund(t *testing.T) {
	h := &Handler{}
	for _, a := range h.certOwnAddrs(context.Background()) {
		if ip := net.ParseIP(a); ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Errorf("unerwartete Adresse %q", a)
		}
	}
}
