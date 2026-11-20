package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
)

// Auswertung der Vorschläge aus UniFi und Omada (rein, ohne Datenbank): ordnet die
// Netze und WLANs des Controllers den Einträgen im PDH zu und bestimmt je Zeile
// NEU / ANDERS / gleich. Das Kennwort eines WLANs liegt nur in netCtlWLAN (für den
// Vergleich im Speicher und die spätere Verschlüsselung) und gelangt in keine
// Zeile, die an die Vorlage geht – dort steht nur PwKnown.

const (
	netStateNew  = "neu"
	netStateDiff = "anders"
	netStateSame = "gleich"
)

// Hinweise zum Kennwort (wörtlich laut Entwurf).
const (
	netHintOpen       = "offen"
	netHintEnterprise = "Anmeldung mit Benutzer (Enterprise)"
	netHintPwOK       = "Kennwort ✓"
	netHintPwManual   = "Controller gibt das Kennwort nicht heraus – bitte von Hand eintragen"
)

// netCtlNet: ein Netz des Controllers. VLAN 0 = unbekannt, Subnet "" = unbekannt.
type netCtlNet struct {
	ExtID, Name, Subnet string
	VLAN                int
	Default             bool
}

// netCtlWLAN: ein WLAN des Controllers. Password ist nur für den Vergleich im
// Speicher und das verschlüsselte Speichern gedacht und wird nie ausgegeben.
type netCtlWLAN struct {
	ExtID, SSID, NetExtID, Security, Password string
	VLAN                                      int
	Hidden                                    bool
}

func (w netCtlWLAN) String() string {
	return fmt.Sprintf("netCtlWLAN{ExtID:%s SSID:%s NetExtID:%s Security:%s VLAN:%d Hidden:%t}",
		w.ExtID, w.SSID, w.NetExtID, w.Security, w.VLAN, w.Hidden)
}

func (w netCtlWLAN) GoString() string { return w.String() }

// Format maskiert das Kennwort bei jedem fmt-Verb.
func (w netCtlWLAN) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, w.String()) }

// MarshalJSON lässt das Kennwort weg.
func (w netCtlWLAN) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ExtID, SSID, NetExtID, Security string
		VLAN                            int
		Hidden                          bool
	}{w.ExtID, w.SSID, w.NetExtID, w.Security, w.VLAN, w.Hidden})
}

// netSuggRow: eine Zeile der Vorschlagsliste. Enthält kein Kennwort; net/wlan
// (unexportiert) tragen die Controller-Werte für das spätere Übernehmen.
type netSuggRow struct {
	Source, Kind, ExtID, State, Fingerprint, PDHID, Name, Subnet, NetName, NetPDHID, Security, Hint string
	VLAN                                                                                            int
	PwKnown, Hidden, ESPPDHUnusable                                                                 bool
	NetCreate                                                                                       *netCtlNet
	Diffs                                                                                           []string
	net                                                                                             *netCtlNet
	wlan                                                                                            *netCtlWLAN
}

// netCtlFromUnifi wandelt die UniFi-Daten um: Native → Netz mit Default; VLAN = VLAN des Netzes.
func netCtlFromUnifi(nets []unifi.Network, wl []unifi.WifiBroadcast) ([]netCtlNet, []netCtlWLAN) {
	var cn []netCtlNet
	vlanOf := map[string]int{}
	defID := ""
	for _, n := range nets {
		cn = append(cn, netCtlNet{ExtID: n.ID, Name: n.Name, Subnet: n.Subnet, VLAN: n.VLAN, Default: n.Default})
		vlanOf[n.ID] = n.VLAN
		if n.Default && defID == "" {
			defID = n.ID
		}
	}
	var cw []netCtlWLAN
	for _, w := range wl {
		id := w.NetworkID
		if w.Native {
			id = defID
		}
		cw = append(cw, netCtlWLAN{ExtID: w.ID, SSID: w.Name, NetExtID: id, Security: w.Security,
			Password: w.Passphrase, VLAN: vlanOf[id], Hidden: w.Hidden})
	}
	return cn, cw
}

// netCtlFromOmada wandelt die Omada-Daten um: NetExtID = Netz mit gleicher VLAN-Nummer,
// SSID-VLAN 0 → Netz mit VLAN 1.
func netCtlFromOmada(nets []omada.LanNetwork, ss []omada.SSID) ([]netCtlNet, []netCtlWLAN) {
	var cn []netCtlNet
	byVLAN := map[int]string{}
	for _, n := range nets {
		cn = append(cn, netCtlNet{ExtID: n.ID, Name: n.Name, Subnet: n.Subnet, VLAN: n.VLAN})
		if _, ok := byVLAN[n.VLAN]; !ok {
			byVLAN[n.VLAN] = n.ID
		}
	}
	var cw []netCtlWLAN
	for _, s := range ss {
		vlan := s.VLAN
		if vlan == 0 {
			vlan = 1
		}
		id := byVLAN[vlan]
		if id == "" {
			vlan = s.VLAN
		}
		cw = append(cw, netCtlWLAN{ExtID: s.ID, SSID: s.Name, NetExtID: id, Security: s.Security,
			Password: s.Passphrase, VLAN: vlan, Hidden: s.Hidden})
	}
	return cn, cw
}

// netPwUsable: ein WLAN-Kennwort ist brauchbar, wenn es 8–63 Zeichen hat, keine
// Steuerzeichen enthält und nicht nur aus Sternchen bzw. Punkten (verdeckt) besteht.
func netPwUsable(pw string) bool {
	n := utf8.RuneCountInString(pw)
	if n < 8 || n > 63 {
		return false
	}
	masked := true
	for _, r := range pw {
		if unicode.IsControl(r) {
			return false
		}
		if r != '*' && r != '•' {
			masked = false
		}
	}
	return !masked
}

// netFingerprint: SHA-256 über die nicht geheimen Felder (nie das Kennwort).
func netFingerprint(n *netCtlNet, w *netCtlWLAN) string {
	var s string
	if n != nil {
		// %q je Feld: ein „|“ oder Anführungszeichen im Namen kann nichts verschieben.
		s = fmt.Sprintf("segment|%q|%d|%q", n.Name, n.VLAN, n.Subnet)
	} else if w != nil {
		s = fmt.Sprintf("wlan|%q|%q|%t|%q|%d", w.SSID, w.Security, w.Hidden, w.NetExtID, w.VLAN)
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func vlanText(v int) string {
	if v <= 0 {
		return "–"
	}
	return strconv.Itoa(v)
}

func jaNein(b bool) string {
	if b {
		return "ja"
	}
	return "nein"
}

// netSuggest ordnet die Controller-Daten einer Quelle den PDH-Einträgen zu. gone: PDH-IDs
// dieser Quelle mit Kennung, die am Controller kein Gegenstück mehr haben.
func netSuggest(source string, nets []netCtlNet, wlans []netCtlWLAN, segs []netPDHSeg, pw []netPDHWLAN, hidden []netHidden) (rows []netSuggRow, gone []string) {
	return netSuggestFetched(source, nets, wlans, segs, pw, hidden, netFetched{Nets: true, WLANs: true})
}

// netFetched sagt, welche Art beim Controller erfolgreich abgefragt wurde. Eine
// fehlgeschlagene Abfrage darf nichts als „am Controller nicht mehr vorhanden“
// (gone) markieren; der Aufrufer (Task 5) übergibt hier das Ergebnis je Art.
type netFetched struct{ Nets, WLANs bool }

// netSuggestFetched wie netSuggest, berechnet gone nur für erfolgreich abgefragte Arten.
// Annahmen für das Übernehmen (Aufgabe des Aufrufers, nicht der Auswertung): zwei WLANs
// oder Netz-Zeile und WLAN mit demselben fehlenden Netz dürfen es nicht doppelt anlegen –
// das Netz wird beim Übernehmen über Quelle+Kennung, dann VLAN neu aufgelöst, bevor es entsteht.
func netSuggestFetched(source string, nets []netCtlNet, wlans []netCtlWLAN, segs []netPDHSeg, pw []netPDHWLAN, hidden []netHidden, fetched netFetched) (rows []netSuggRow, gone []string) {
	// Netze: Kennung, dann VLAN, dann Name; jede PDH-Zeile höchstens einmal.
	segOf := make([]int, len(nets)) // Index in segs oder -1
	for i := range segOf {
		segOf[i] = -1
	}
	segUsed := make([]bool, len(segs))
	for i, n := range nets {
		if n.ExtID == "" {
			continue
		}
		for j, s := range segs {
			if !segUsed[j] && s.Source == source && s.ExtID == n.ExtID {
				segOf[i], segUsed[j] = j, true
				break
			}
		}
	}
	for i, n := range nets {
		if segOf[i] >= 0 || n.VLAN <= 0 {
			continue
		}
		for j, s := range segs {
			if !segUsed[j] && s.VLAN == n.VLAN {
				segOf[i], segUsed[j] = j, true
				break
			}
		}
	}
	for i, n := range nets {
		if segOf[i] >= 0 || n.Name == "" {
			continue
		}
		for j, s := range segs {
			if !segUsed[j] && strings.EqualFold(s.Name, n.Name) {
				segOf[i], segUsed[j] = j, true
				break
			}
		}
	}
	netIdx := map[string]int{} // Controller-Kennung → Index in nets
	for i, n := range nets {
		if _, ok := netIdx[n.ExtID]; !ok && n.ExtID != "" {
			netIdx[n.ExtID] = i
		}
	}

	hide := map[string]string{}
	for _, h := range hidden {
		if h.Source == source {
			hide[h.Kind+"\x00"+h.ExtID] = h.Fingerprint
		}
	}
	isHidden := func(kind, ext, fp string) bool {
		f, ok := hide[kind+"\x00"+ext]
		return ok && f == fp
	}

	for i := range nets {
		n := nets[i]
		r := netSuggRow{Source: source, Kind: netKindSegment, ExtID: n.ExtID, Name: n.Name, Subnet: n.Subnet,
			VLAN: n.VLAN, Fingerprint: netFingerprint(&n, nil), net: &n}
		if j := segOf[i]; j >= 0 {
			s := segs[j]
			r.PDHID = s.ID
			if n.Name != "" && n.Name != s.Name {
				r.Diffs = append(r.Diffs, "Name: "+s.Name+" → "+n.Name)
			}
			if n.VLAN > 0 && n.VLAN != s.VLAN {
				r.Diffs = append(r.Diffs, "VLAN: "+vlanText(s.VLAN)+" → "+strconv.Itoa(n.VLAN))
			}
			if n.Subnet != "" && n.Subnet != s.Subnet {
				r.Diffs = append(r.Diffs, "Adressbereich: "+orDash(s.Subnet)+" → "+n.Subnet)
			}
		}
		r.State = suggState(r.PDHID, r.Diffs)
		if r.State != netStateSame && isHidden(r.Kind, r.ExtID, r.Fingerprint) {
			continue
		}
		rows = append(rows, r)
	}

	// WLANs: Kennung, dann SSID (genau).
	wlOf := make([]int, len(wlans))
	for i := range wlOf {
		wlOf[i] = -1
	}
	pwUsed := make([]bool, len(pw))
	for i, w := range wlans {
		if w.ExtID == "" {
			continue
		}
		for j, p := range pw {
			if !pwUsed[j] && p.Source == source && p.ExtID == w.ExtID {
				wlOf[i], pwUsed[j] = j, true
				break
			}
		}
	}
	for i, w := range wlans {
		if wlOf[i] >= 0 || w.SSID == "" {
			continue
		}
		for j, p := range pw {
			if !pwUsed[j] && p.SSID == w.SSID {
				wlOf[i], pwUsed[j] = j, true
				break
			}
		}
	}

	for i := range wlans {
		w := wlans[i]
		r := netSuggRow{Source: source, Kind: netKindWLAN, ExtID: w.ExtID, Name: w.SSID, VLAN: w.VLAN,
			Security: w.Security, Hidden: w.Hidden, Fingerprint: netFingerprint(nil, &w), wlan: &w}

		// Netz des WLANs: Controller-Netz → dessen PDH-Netz; sonst PDH-Netz mit der VLAN-Nummer.
		var netName, netPDH string
		if ci, ok := netIdx[w.NetExtID]; ok && w.NetExtID != "" {
			netName = nets[ci].Name
			if j := segOf[ci]; j >= 0 {
				netPDH = segs[j].ID
			} else {
				c := nets[ci]
				r.NetCreate = &c
			}
		} else if w.VLAN > 0 {
			for _, s := range segs {
				if s.VLAN == w.VLAN {
					netName, netPDH = s.Name, s.ID
					break
				}
			}
		}
		r.NetName, r.NetPDHID = netName, netPDH

		switch w.Security {
		case unifi.SecOpen:
			r.Hint = netHintOpen
		case unifi.SecEnterprise:
			r.Hint = netHintEnterprise
			r.ESPPDHUnusable = true
		case unifi.SecPersonal:
			if netPwUsable(w.Password) {
				r.PwKnown = true
				r.Hint = netHintPwOK
			} else {
				r.Hint = netHintPwManual
			}
		default:
			r.Hint = netHintPwManual
		}

		if j := wlOf[i]; j >= 0 {
			p := pw[j]
			r.PDHID = p.ID
			if w.SSID != "" && w.SSID != p.SSID {
				r.Diffs = append(r.Diffs, "Name: "+p.SSID+" → "+w.SSID)
			}
			if netName != "" && netPDH != p.SegmentID {
				old := ""
				for _, s := range segs {
					if s.ID == p.SegmentID {
						old = s.Name
						break
					}
				}
				r.Diffs = append(r.Diffs, "Netz: "+orDash(old)+" → "+netName)
			}
			if w.Hidden != p.Hidden {
				r.Diffs = append(r.Diffs, "versteckt: "+jaNein(p.Hidden)+" → "+jaNein(w.Hidden))
			}
			switch {
			case w.Security == unifi.SecOpen && p.HasPassword:
				// Ob das WLAN jetzt offen ist, hängt nicht vom Entschlüsseln ab.
				r.Diffs = append(r.Diffs, "jetzt offen (Kennwort entfällt)")
			case r.PwKnown && !p.HasPassword:
				r.Diffs = append(r.Diffs, "Kennwort neu")
			case r.PwKnown && !p.PasswordOK:
				r.Diffs = append(r.Diffs, "Gespeichertes Kennwort nicht lesbar – bitte prüfen")
			case r.PwKnown && p.Password != w.Password:
				r.Diffs = append(r.Diffs, "Kennwort geändert")
			}
			if netName != "" && netPDH == "" && r.NetCreate != nil {
				r.Diffs = append(r.Diffs, "Netz fehlt im PDH: "+netName)
			}
		}
		r.State = suggState(r.PDHID, r.Diffs)
		if r.State != netStateSame && isHidden(r.Kind, r.ExtID, r.Fingerprint) {
			continue
		}
		rows = append(rows, r)
	}

	for j, s := range segs {
		if fetched.Nets && !segUsed[j] && s.Source == source && s.ExtID != "" {
			gone = append(gone, s.ID)
		}
	}
	for j, p := range pw {
		if fetched.WLANs && !pwUsed[j] && p.Source == source && p.ExtID != "" {
			gone = append(gone, p.ID)
		}
	}
	return rows, gone
}

func suggState(pdhID string, diffs []string) string {
	switch {
	case pdhID == "":
		return netStateNew
	case len(diffs) > 0:
		return netStateDiff
	}
	return netStateSame
}
