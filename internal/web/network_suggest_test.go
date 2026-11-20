package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
)

func suggFind(t *testing.T, rows []netSuggRow, kind, ext string) netSuggRow {
	t.Helper()
	for _, r := range rows {
		if r.Kind == kind && r.ExtID == ext {
			return r
		}
	}
	t.Fatalf("keine Zeile %s/%s in %d Zeilen", kind, ext, len(rows))
	return netSuggRow{}
}

func TestNetSuggestNetMatchOrder(t *testing.T) {
	segs := []netPDHSeg{
		{ID: "p1", Name: "Alt", VLAN: 20, Source: "unifi", ExtID: "c-b"},
		{ID: "p2", Name: "Büro", VLAN: 10},
		{ID: "p3", Name: "gast", VLAN: 0},
	}
	cases := []struct {
		name string
		nets []netCtlNet
		want map[string]string // ExtID → PDH-ID ("" = neu)
	}{
		{"Kennung vor VLAN", []netCtlNet{{ExtID: "c-b", Name: "X", VLAN: 10}}, map[string]string{"c-b": "p1"}},
		{"VLAN vor Name", []netCtlNet{{ExtID: "c-x", Name: "gast", VLAN: 10}}, map[string]string{"c-x": "p2"}},
		{"Name ohne Groß/Klein", []netCtlNet{{ExtID: "c-g", Name: "GAST"}}, map[string]string{"c-g": "p3"}},
		{"VLAN 0 zählt nicht als VLAN", []netCtlNet{{ExtID: "c-n", Name: "Neu", VLAN: 0}}, map[string]string{"c-n": ""}},
		{"PDH-Zeile nie zweimal", []netCtlNet{{ExtID: "c-1", Name: "a", VLAN: 10}, {ExtID: "c-2", Name: "b", VLAN: 10}},
			map[string]string{"c-1": "p2", "c-2": ""}},
		{"Kennung hat Vorrang vor früherer VLAN-Zeile", []netCtlNet{{ExtID: "c-1", Name: "a", VLAN: 20}, {ExtID: "c-b", Name: "b", VLAN: 99}},
			map[string]string{"c-b": "p1", "c-1": ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, _ := netSuggest("unifi", c.nets, nil, segs, nil, nil)
			for ext, pid := range c.want {
				r := suggFind(t, rows, netKindSegment, ext)
				if r.PDHID != pid {
					t.Errorf("%s: PDHID = %q, erwartet %q", ext, r.PDHID, pid)
				}
				if (pid == "") != (r.State == netStateNew) {
					t.Errorf("%s: Zustand %q passt nicht zu PDHID %q", ext, r.State, pid)
				}
			}
		})
	}
}

func TestNetSuggestStates(t *testing.T) {
	segs := []netPDHSeg{{ID: "p1", Name: "Büro", VLAN: 10, Subnet: "10.10.1.0/24", Source: "unifi", ExtID: "c1"}}
	cases := []struct {
		name  string
		n     netCtlNet
		state string
		diffs []string
	}{
		{"gleich", netCtlNet{ExtID: "c1", Name: "Büro", VLAN: 10, Subnet: "10.10.1.0/24"}, netStateSame, nil},
		{"leerer Wert ist kein Unterschied", netCtlNet{ExtID: "c1", Name: "Büro"}, netStateSame, nil},
		{"alles anders", netCtlNet{ExtID: "c1", Name: "Office", VLAN: 11, Subnet: "10.10.2.0/24"}, netStateDiff,
			[]string{"Name: Büro → Office", "VLAN: 10 → 11", "Adressbereich: 10.10.1.0/24 → 10.10.2.0/24"}},
		{"neu", netCtlNet{ExtID: "c9", Name: "Neu", VLAN: 99}, netStateNew, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, _ := netSuggest("unifi", []netCtlNet{c.n}, nil, segs, nil, nil)
			if len(rows) != 1 {
				t.Fatalf("Zeilen = %d", len(rows))
			}
			r := rows[0]
			if r.State != c.state || strings.Join(r.Diffs, "|") != strings.Join(c.diffs, "|") {
				t.Errorf("Zustand %q Unterschiede %q, erwartet %q %q", r.State, r.Diffs, c.state, c.diffs)
			}
		})
	}
	// PDH ohne VLAN/Adressbereich zeigt „–“.
	rows, _ := netSuggest("unifi", []netCtlNet{{ExtID: "c2", Name: "X", VLAN: 5, Subnet: "10.0.0.0/8"}}, nil,
		[]netPDHSeg{{ID: "p2", Name: "X", Source: "unifi", ExtID: "c2"}}, nil, nil)
	if got := strings.Join(rows[0].Diffs, "|"); got != "VLAN: – → 5|Adressbereich: – → 10.0.0.0/8" {
		t.Errorf("Unterschiede = %q", got)
	}
}

func TestNetSuggestWLAN(t *testing.T) {
	nets := []netCtlNet{{ExtID: "n1", Name: "Produktion", VLAN: 20, Subnet: "10.10.20.0/24"}}
	segs := []netPDHSeg{{ID: "ps1", Name: "Prod", VLAN: 20}}
	wl := []netCtlWLAN{
		{ExtID: "w1", SSID: "Werk", NetExtID: "n1", VLAN: 20, Security: unifi.SecPersonal, Password: "geheim123"},
		{ExtID: "w2", SSID: "Gast", Security: unifi.SecOpen},
		{ExtID: "w3", SSID: "Firma", Security: unifi.SecEnterprise},
		{ExtID: "w4", SSID: "Stumm", Security: unifi.SecPersonal},
		{ExtID: "w5", SSID: "Omadanetz", VLAN: 20, Security: unifi.SecOpen},
		{ExtID: "w6", SSID: "Kurz", Security: unifi.SecPersonal, Password: "kurz"},
	}
	rows, _ := netSuggest("unifi", nets, wl, segs, nil, nil)
	w1 := suggFind(t, rows, netKindWLAN, "w1")
	if w1.NetPDHID != "ps1" || w1.NetCreate != nil || w1.NetName != "Produktion" || !w1.PwKnown || w1.Hint != "Kennwort ✓" {
		t.Errorf("w1 = %+v", w1)
	}
	// Controller-Netz da, PDH-Netz nicht: anlegen.
	rows, _ = netSuggest("unifi", nets, wl[:1], nil, nil, nil)
	r := suggFind(t, rows, netKindWLAN, "w1")
	if r.NetCreate == nil || r.NetCreate.ExtID != "n1" || r.NetPDHID != "" {
		t.Errorf("NetCreate fehlt: %+v", r)
	}
	// Ohne Controller-Netz: über VLAN-Nummer.
	w5 := suggFind(t, rows2(nets[:0], wl, segs), netKindWLAN, "w5")
	if w5.NetPDHID != "ps1" || w5.NetCreate != nil {
		t.Errorf("w5 = %+v", w5)
	}
	hints := map[string]string{
		"w2": "offen",
		"w3": "Anmeldung mit Benutzer (Enterprise)",
		"w4": "Controller gibt das Kennwort nicht heraus – bitte von Hand eintragen",
		"w6": "Controller gibt das Kennwort nicht heraus – bitte von Hand eintragen",
	}
	for id, h := range hints {
		got := suggFind(t, rows2(nets, wl, segs), netKindWLAN, id)
		if got.Hint != h {
			t.Errorf("%s: Hinweis %q, erwartet %q", id, got.Hint, h)
		}
		if got.ESPPDHUnusable != (id == "w3") {
			t.Errorf("%s: ESPPDHUnusable = %v", id, got.ESPPDHUnusable)
		}
		if got.PwKnown {
			t.Errorf("%s: PwKnown", id)
		}
	}
}

func rows2(nets []netCtlNet, wl []netCtlWLAN, segs []netPDHSeg) []netSuggRow {
	r, _ := netSuggest("unifi", nets, wl, segs, nil, nil)
	return r
}

func TestNetSuggestWLANMatching(t *testing.T) {
	segs := []netPDHSeg{{ID: "ps1", Name: "Prod", VLAN: 20, Source: "unifi", ExtID: "n1"}}
	pw := []netPDHWLAN{
		{ID: "pw1", SSID: "Alt", SegmentID: "ps1", Source: "unifi", ExtID: "w1", PasswordOK: true},
		{ID: "pw2", SSID: "Gast", PasswordOK: true},
	}
	nets := []netCtlNet{{ExtID: "n1", Name: "Prod", VLAN: 20}}
	wl := []netCtlWLAN{
		{ExtID: "w1", SSID: "Neu", NetExtID: "n1", Security: unifi.SecOpen},
		{ExtID: "w2", SSID: "Gast", Security: unifi.SecOpen},
		{ExtID: "w3", SSID: "Gast", Security: unifi.SecOpen},
	}
	rows, _ := netSuggest("unifi", nets, wl, segs, pw, nil)
	if r := suggFind(t, rows, netKindWLAN, "w1"); r.PDHID != "pw1" || r.State != netStateDiff || r.Diffs[0] != "Name: Alt → Neu" {
		t.Errorf("w1 = %+v", r)
	}
	if r := suggFind(t, rows, netKindWLAN, "w2"); r.PDHID != "pw2" {
		t.Errorf("w2 = %+v", r)
	}
	if r := suggFind(t, rows, netKindWLAN, "w3"); r.State != netStateNew {
		t.Errorf("w3 darf pw2 nicht noch einmal nehmen: %+v", r)
	}
	// Netz und versteckt als Unterschied.
	segs2 := []netPDHSeg{{ID: "ps1", Name: "Prod", VLAN: 20, Source: "unifi", ExtID: "n1"}, {ID: "ps2", Name: "Alt"}}
	pw2 := []netPDHWLAN{{ID: "pw1", SSID: "A", SegmentID: "ps2", Source: "unifi", ExtID: "w1", PasswordOK: true}}
	rows, _ = netSuggest("unifi", nets, []netCtlWLAN{{ExtID: "w1", SSID: "A", NetExtID: "n1", Hidden: true, Security: unifi.SecOpen}}, segs2, pw2, nil)
	if got := strings.Join(rows[1].Diffs, "|"); got != "Netz: Alt → Prod|versteckt: nein → ja" {
		t.Errorf("Unterschiede = %q", got)
	}
	pw3 := []netPDHWLAN{{ID: "pw1", SSID: "A", SegmentID: "ps1", Source: "unifi", ExtID: "w1", Hidden: true, PasswordOK: true}}
	rows, _ = netSuggest("unifi", nets, []netCtlWLAN{{ExtID: "w1", SSID: "A", NetExtID: "n1", Security: unifi.SecOpen}}, segs, pw3, nil)
	if got := strings.Join(rows[1].Diffs, "|"); got != "versteckt: ja → nein" {
		t.Errorf("Unterschiede = %q", got)
	}
}

func TestNetSuggestPassword(t *testing.T) {
	const ssid = "W"
	cases := []struct {
		name string
		w    netCtlWLAN
		pdh  netPDHWLAN
		want string // "" = gleich
	}{
		{"gleich", netCtlWLAN{Security: unifi.SecPersonal, Password: "geheim123"}, netPDHWLAN{HasPassword: true, PasswordOK: true, Password: "geheim123"}, ""},
		{"geändert", netCtlWLAN{Security: unifi.SecPersonal, Password: "geheim123"}, netPDHWLAN{HasPassword: true, PasswordOK: true, Password: "anders456"}, "Kennwort geändert"},
		{"PDH leer", netCtlWLAN{Security: unifi.SecPersonal, Password: "geheim123"}, netPDHWLAN{PasswordOK: true}, "Kennwort neu"},
		{"jetzt offen", netCtlWLAN{Security: unifi.SecOpen}, netPDHWLAN{HasPassword: true, PasswordOK: true, Password: "x1234567"}, "jetzt offen (Kennwort entfällt)"},
		{"offen und PDH ohne", netCtlWLAN{Security: unifi.SecOpen}, netPDHWLAN{PasswordOK: true}, ""},
		{"Controller liefert keins", netCtlWLAN{Security: unifi.SecPersonal}, netPDHWLAN{HasPassword: true, PasswordOK: true, Password: "geheim123"}, ""},
		{"verdeckt", netCtlWLAN{Security: unifi.SecPersonal, Password: "********"}, netPDHWLAN{HasPassword: true, PasswordOK: true, Password: "geheim123"}, ""},
		{"PDH nicht lesbar", netCtlWLAN{Security: unifi.SecPersonal, Password: "geheim123"}, netPDHWLAN{HasPassword: true, PasswordOK: false}, ""},
		{"offen, PDH nicht lesbar", netCtlWLAN{Security: unifi.SecOpen}, netPDHWLAN{HasPassword: true, PasswordOK: false}, ""},
		{"Enterprise", netCtlWLAN{Security: unifi.SecEnterprise}, netPDHWLAN{HasPassword: true, PasswordOK: true, Password: "geheim123"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.w.ExtID, c.w.SSID = "w1", ssid
			c.pdh.ID, c.pdh.SSID, c.pdh.Source, c.pdh.ExtID = "pw1", ssid, "unifi", "w1"
			rows, _ := netSuggest("unifi", nil, []netCtlWLAN{c.w}, nil, []netPDHWLAN{c.pdh}, nil)
			r := rows[0]
			if c.want == "" {
				if r.State != netStateSame {
					t.Errorf("Zustand %q %q", r.State, r.Diffs)
				}
				return
			}
			if r.State != netStateDiff || len(r.Diffs) != 1 || r.Diffs[0] != c.want {
				t.Errorf("Zustand %q Unterschiede %q, erwartet %q", r.State, r.Diffs, c.want)
			}
		})
	}
	if netPwUsable("********") || netPwUsable("••••••••") || netPwUsable("kurz") || netPwUsable(strings.Repeat("a", 64)) || netPwUsable("abcd\x00efgh") {
		t.Error("unbrauchbares Kennwort als brauchbar erkannt")
	}
	if !netPwUsable("abcdefgh") || !netPwUsable(strings.Repeat("a", 63)) {
		t.Error("brauchbares Kennwort abgelehnt")
	}
}

func TestNetSuggestHidden(t *testing.T) {
	n := netCtlNet{ExtID: "c1", Name: "Prod", VLAN: 20, Subnet: "10.0.0.0/24"}
	fp := netFingerprint(&n, nil)
	hid := []netHidden{{Source: "unifi", Kind: netKindSegment, ExtID: "c1", Fingerprint: fp}}
	if rows, _ := netSuggest("unifi", []netCtlNet{n}, nil, nil, nil, hid); len(rows) != 0 {
		t.Errorf("ausgeblendeter Vorschlag erscheint: %d", len(rows))
	}
	// Andere Quelle blendet nicht aus.
	if rows, _ := netSuggest("omada", []netCtlNet{n}, nil, nil, nil, hid); len(rows) != 1 {
		t.Errorf("andere Quelle: %d Zeilen", len(rows))
	}
	n2 := n
	n2.Name = "Produktion"
	if rows, _ := netSuggest("unifi", []netCtlNet{n2}, nil, nil, nil, hid); len(rows) != 1 {
		t.Errorf("geänderter Vorschlag fehlt: %d", len(rows))
	}
	// ANDERS lässt sich ausblenden.
	segs := []netPDHSeg{{ID: "p1", Name: "Alt", Source: "unifi", ExtID: "c1"}}
	if rows, _ := netSuggest("unifi", []netCtlNet{n}, nil, segs, nil, hid); len(rows) != 0 {
		t.Errorf("ANDERS nicht ausgeblendet: %d", len(rows))
	}
	// Kennwort verändert den Fingerabdruck nicht.
	w := netCtlWLAN{ExtID: "w1", SSID: "W", Security: unifi.SecPersonal, NetExtID: "c1", VLAN: 20, Password: "geheim123"}
	w2 := w
	w2.Password = "ganz-anders"
	if netFingerprint(nil, &w) != netFingerprint(nil, &w2) {
		t.Error("Kennwort geht in den Fingerabdruck ein")
	}
	w3 := w
	w3.Hidden = true
	if netFingerprint(nil, &w) == netFingerprint(nil, &w3) || netFingerprint(nil, &w) == fp {
		t.Error("Fingerabdruck unterscheidet nicht")
	}
	hid = []netHidden{{Source: "unifi", Kind: netKindWLAN, ExtID: "w1", Fingerprint: netFingerprint(nil, &w)}}
	if rows, _ := netSuggest("unifi", nil, []netCtlWLAN{w2}, nil, nil, hid); len(rows) != 0 {
		t.Error("WLAN mit anderem Kennwort wieder aufgetaucht")
	}
	if rows, _ := netSuggest("unifi", nil, []netCtlWLAN{w3}, nil, nil, hid); len(rows) != 1 {
		t.Error("WLAN mit anderem Merkmal fehlt")
	}
}

func TestNetSuggestGone(t *testing.T) {
	segs := []netPDHSeg{
		{ID: "s-da", Source: "unifi", ExtID: "c1", Name: "A"},
		{ID: "s-weg", Source: "unifi", ExtID: "c2", Name: "B"},
		{ID: "s-omada", Source: "omada", ExtID: "o1", Name: "C"},
		{ID: "s-hand", Name: "D"},
		{ID: "s-ohne", Source: "unifi", Name: "E"},
	}
	pw := []netPDHWLAN{
		{ID: "w-da", Source: "unifi", ExtID: "x1", SSID: "A"},
		{ID: "w-weg", Source: "unifi", ExtID: "x2", SSID: "B"},
		{ID: "w-omada", Source: "omada", ExtID: "x3", SSID: "C"},
	}
	_, gone := netSuggest("unifi", []netCtlNet{{ExtID: "c1", Name: "A"}}, []netCtlWLAN{{ExtID: "x1", SSID: "A"}}, segs, pw, nil)
	if strings.Join(gone, ",") != "s-weg,w-weg" {
		t.Errorf("gone = %v", gone)
	}
}

func TestNetSuggestTwoSources(t *testing.T) {
	// Von UniFi angelegt; bei Omada mit gleicher VLAN-Nummer nie „neu“.
	segs := []netPDHSeg{{ID: "p1", Name: "Prod", VLAN: 20, Subnet: "10.0.20.0/24", Source: "unifi", ExtID: "u-1"}}
	cases := []struct {
		n     netCtlNet
		state string
	}{
		{netCtlNet{ExtID: "o-1", Name: "Prod", VLAN: 20, Subnet: "10.0.20.0/24"}, netStateSame},
		{netCtlNet{ExtID: "o-1", Name: "Produktion", VLAN: 20, Subnet: "10.0.20.0/24"}, netStateDiff},
	}
	for _, c := range cases {
		rows, gone := netSuggest("omada", []netCtlNet{c.n}, nil, segs, nil, nil)
		if rows[0].State != c.state || rows[0].PDHID != "p1" || len(gone) != 0 {
			t.Errorf("%+v gone=%v", rows[0], gone)
		}
	}
	// UniFi-Zeile fällt bei Omada nicht in gone, auch wenn die Kennung fehlt.
	if _, gone := netSuggest("omada", nil, nil, segs, nil, nil); len(gone) != 0 {
		t.Errorf("gone = %v", gone)
	}
}

func TestNetCtlFromUnifi(t *testing.T) {
	nets := []unifi.Network{
		{ID: "n-def", Name: "Default", VLAN: 1, Default: true, Subnet: "10.0.0.0/24"},
		{ID: "n-20", Name: "Prod", VLAN: 20},
	}
	wl := []unifi.WifiBroadcast{
		{ID: "w1", Name: "Nativ", Native: true, Security: unifi.SecPersonal, Passphrase: "geheim123"},
		{ID: "w2", Name: "Prod", NetworkID: "n-20", Security: unifi.SecOpen, Hidden: true},
		{ID: "w3", Name: "Ohne", NetworkID: "unbekannt"},
	}
	cn, cw := netCtlFromUnifi(nets, wl)
	if len(cn) != 2 || cn[0] != (netCtlNet{ExtID: "n-def", Name: "Default", Subnet: "10.0.0.0/24", VLAN: 1, Default: true}) {
		t.Errorf("Netze = %+v", cn)
	}
	if cw[0].NetExtID != "n-def" || cw[0].VLAN != 1 || cw[0].Password != "geheim123" || cw[0].Security != unifi.SecPersonal {
		t.Errorf("w1 = %#v", cw[0])
	}
	if cw[1].NetExtID != "n-20" || cw[1].VLAN != 20 || !cw[1].Hidden || cw[1].SSID != "Prod" || cw[1].ExtID != "w2" {
		t.Errorf("w2 = %#v", cw[1])
	}
	if cw[2].NetExtID != "unbekannt" || cw[2].VLAN != 0 {
		t.Errorf("w3 = %#v", cw[2])
	}
}

func TestNetCtlFromOmada(t *testing.T) {
	nets := []omada.LanNetwork{{ID: "o-1", Name: "Standard", VLAN: 1}, {ID: "o-30", Name: "IoT", VLAN: 30, Subnet: "10.0.30.0/24"}}
	ss := []omada.SSID{
		{ID: "s0", Name: "Ohne", VLAN: 0, Security: unifi.SecOpen},
		{ID: "s30", Name: "IoT", VLAN: 30, Security: unifi.SecPersonal, Passphrase: "geheim123", Hidden: true},
		{ID: "s99", Name: "Fern", VLAN: 99},
	}
	cn, cw := netCtlFromOmada(nets, ss)
	if len(cn) != 2 || cn[1].ExtID != "o-30" || cn[1].Subnet != "10.0.30.0/24" || cn[1].VLAN != 30 {
		t.Errorf("Netze = %+v", cn)
	}
	if cw[0].NetExtID != "o-1" || cw[0].VLAN != 1 {
		t.Errorf("VLAN 0 → VLAN 1: %#v", cw[0])
	}
	if cw[1].NetExtID != "o-30" || cw[1].VLAN != 30 || cw[1].Password != "geheim123" || !cw[1].Hidden {
		t.Errorf("s30 = %#v", cw[1])
	}
	if cw[2].NetExtID != "" || cw[2].VLAN != 99 {
		t.Errorf("s99 = %#v", cw[2])
	}
}

func TestNetCtlWLANStringHidesPassword(t *testing.T) {
	const pw = "streng-geheim-42"
	w := netCtlWLAN{ExtID: "w1", SSID: "W", Security: unifi.SecPersonal, Password: pw}
	row := netSuggRow{Kind: netKindWLAN, wlan: &w}
	rowNoPtr := netSuggRow{Kind: netKindWLAN}
	rowNoPtr.wlan = &w
	b, _ := json.Marshal(w)
	rb, _ := json.Marshal(row)
	outs := []string{
		w.String(), w.GoString(), fmt.Sprintf("%v", w), fmt.Sprintf("%+v", w), fmt.Sprintf("%#v", w), fmt.Sprintf("%s", w),
		fmt.Sprintf("%v", &w), fmt.Sprintf("%+v", &w), fmt.Sprintf("%#v", &w), string(b), string(rb),
		fmt.Sprintf("%v", row), fmt.Sprintf("%+v", row), fmt.Sprintf("%#v", row), fmt.Sprintf("%v", []netCtlWLAN{w}),
	}
	for i, o := range outs {
		if strings.Contains(o, pw) {
			t.Errorf("Ausgabe %d enthält das Kennwort: %s", i, o)
		}
	}
}
