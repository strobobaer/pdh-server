package web

import "testing"

// Zustände von UniFi (UP/DOWN/UNKNOWN, PoE zusätzlich LIMITED) erscheinen
// in der Port-Tabelle auf Deutsch.
func TestITNetPortTexts(t *testing.T) {
	for in, want := range map[string]string{"UP": "aktiv", "up": "aktiv", "DOWN": "getrennt", "UNKNOWN": "unbekannt", "": "unbekannt", "SONST": "unbekannt"} {
		if got := itNetPortState(in); got != want {
			t.Errorf("itNetPortState(%q) = %q, erwartet %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"UP": "an", "DOWN": "aus", "LIMITED": "begrenzt", "UNKNOWN": "unbekannt", "": "", "SONST": "unbekannt"} {
		if got := itNetPoE(in); got != want {
			t.Errorf("itNetPoE(%q) = %q, erwartet %q", in, got, want)
		}
	}
}
