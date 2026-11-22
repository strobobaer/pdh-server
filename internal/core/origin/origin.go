// Package origin: Herkunft von Tickets und Störungen (Migration 201) – woher ein Datensatz kam.
package origin

import (
	"strings"
	"unicode/utf8"
)

// Die erlaubten Werte (wie der CHECK der Migration 201). Leer = unbekannt.
const (
	Web      = "web"      // von Hand im Web (Formular, Leitstand)
	Easy     = "easy"     // Easy-Modus (QR-Code)
	Terminal = "terminal" // Anlagenterminal; Label = Name des Terminals
	App      = "app"      // PDH-App
	Sync     = "sync"     // Abgleich mit einem Fremdsystem (Profil über sync_links)
	Alarm    = "alarm"    // Alarm-Ticket; Label z. B. „Energiedaten“, „IT-Netz“
	System   = "system"   // vom PDH selbst; Label z. B. „Firewall-Freigabe“, „Rüstzeit“
)

const maxLabel = 120

// Clean liefert Herkunft und Zusatz zum Speichern: Unbekannte Werte werden leer (nie eine
// Fehlermeldung beim Anlegen), der Zusatz wird gekürzt und ohne Steuerzeichen.
func Clean(o, label string) (string, string) {
	switch o {
	case Web, Easy, Terminal, App, Sync, Alarm, System:
	default:
		return "", ""
	}
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(label))
	if utf8.RuneCountInString(label) > maxLabel {
		label = string([]rune(label)[:maxLabel])
	}
	return o, label
}
