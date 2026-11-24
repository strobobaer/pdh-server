package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Nur genau "lan" und "tailnet" gelten; alles andere (Großschreibung, Leerzeichen, Fremdes, leer) liefert "".
func TestTerminalRoute(t *testing.T) {
	for _, c := range []struct{ v, want string }{
		{"", ""}, {"lan", "lan"}, {"tailnet", "tailnet"}, {"LAN", ""}, {"vpn", ""}, {" tailnet", ""}, {"tailnet ", ""}, {"Tailnet", ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.v != "" {
			r.Header.Set("X-Terminal-Route", c.v)
		}
		if got := terminalRoute(r); got != c.want {
			t.Errorf("%q: %q, erwartet %q", c.v, got, c.want)
		}
	}
}

// Fehlende Kopfzeile = LAN (NULL schreiben), gültig = schreiben, ungültig (auch leer vorhanden) = stehen lassen.
func TestTerminalRouteUpdate(t *testing.T) {
	for _, c := range []struct {
		hdr     bool
		v       string
		want    any
		wantSet bool
	}{
		{false, "", nil, true},
		{true, "lan", "lan", true},
		{true, "tailnet", "tailnet", true},
		{true, "LAN", nil, false},
		{true, "vpn", nil, false},
		{true, " tailnet", nil, false},
		{true, "", nil, false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.hdr {
			r.Header.Set("X-Terminal-Route", c.v)
		}
		got, set := terminalRouteUpdate(r)
		if got != c.want || set != c.wantSet {
			t.Errorf("%q: (%v, %v), erwartet (%v, %v)", c.v, got, set, c.want, c.wantSet)
		}
	}
}

func TestTerminalRouteLabel(t *testing.T) {
	s := func(v string) *string { return &v }
	for _, c := range []struct {
		in   *string
		want string
	}{{nil, "LAN"}, {s("lan"), "LAN"}, {s("tailnet"), "Tailnet"}, {s("x"), "LAN"}} {
		if got := terminalRouteLabel(c.in); got != c.want {
			t.Errorf("%v: %q, erwartet %q", c.in, got, c.want)
		}
	}
}

// Die Liste Kiosk-Terminals zeigt den Weg je Gerät.
func TestKioskTerminalsListShowsRoute(t *testing.T) {
	b := renderPage(t, loadTestTemplates(t), "kiosk_terminals", KioskTerminalsPageData{PDHTerminals: []pdhTerminalView{
		{DeviceID: "d1", Name: "Pi-Tailnet", Route: "Tailnet", Version: "0.4.7"},
		{DeviceID: "d2", Name: "Pi-Haus", Route: "LAN", Version: "0.4.6"},
	}})
	if !strings.Contains(b, "Weg Tailnet") || !strings.Contains(b, "Weg LAN") {
		t.Errorf("Weg fehlt: %.500s", b)
	}
}
