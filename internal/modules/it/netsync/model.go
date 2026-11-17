// Package netsync gleicht eine Netz-Momentaufnahme (z. B. aus UniFi) mit den
// IT-Assets ab. Das Paket kennt keine Quelle im Einzelnen und ist deshalb
// unabhängig vom jeweiligen Client.
package netsync

import (
	"context"
	"strings"
)

// State ist der Netzzustand eines Assets.
type State string

const (
	StateOnline  State = "online"
	StateOffline State = "offline"
	StateUnknown State = "unknown"
)

// Port ist ein Anschluss eines Netzgeräts.
type Port struct {
	Idx          int
	Connector    string
	State        string
	SpeedMbps    int
	MaxSpeedMbps int
	PoEState     string
}

// Device ist ein Netzgerät der Quelle (Switch, Access Point, Gateway …).
type Device struct {
	ExternalID, MAC, Name, Model, Kind, IP, Firmware string
	FirmwareUpdate                                   bool
	State                                            State
	UplinkExternalID                                 string
	Ports                                            []Port
	// HasDetail: Ports und Firmware stammen aus einem frischen Detail-Abruf.
	HasDetail bool
	// Serial: Seriennummer (nur SNMP). Sie wird nur eingetragen, solange das Feld am
	// Asset leer ist – eine von Hand gepflegte Nummer bleibt.
	Serial string
}

// Client ist ein verbundenes Endgerät. Kind ist "wired" oder "wireless".
type Client struct {
	ExternalID, MAC, Name, IP, Kind, UplinkExternalID string
}

// Snapshot ist der Stand der Quelle zu einem Zeitpunkt.
type Snapshot struct {
	Devices []Device
	Clients []Client
}

// Source liefert Momentaufnahmen.
type Source interface {
	Kind() string
	Fetch(ctx context.Context) (Snapshot, error)
}

// NormMAC bringt eine MAC-Adresse in die Form "aa:bb:cc:dd:ee:ff".
// Ungültige Eingaben ergeben "".
func NormMAC(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b.WriteRune(r)
		case r == ':' || r == '-' || r == '.':
		default:
			return ""
		}
	}
	h := b.String()
	if len(h) != 12 {
		return ""
	}
	return h[0:2] + ":" + h[2:4] + ":" + h[4:6] + ":" + h[6:8] + ":" + h[8:10] + ":" + h[10:12]
}
