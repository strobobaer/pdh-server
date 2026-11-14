package web

import (
	"fmt"
	"strings"
	"time"

	"pdh/internal/modules/ruesten"
)

// Rüstzeiten als Vorgangsart – Anbindung an den Rest des PDH. Die Seiten
// stehen in setup_pages.go, die Aktionen in setup_actions.go, gespeichert wird
// über ruesten.Store (internal/modules/ruesten).

// setupRef: Datensatz-Typ einer Rüstzeit (Verlauf, Feldsätze, Rechte auf
// Abteilungsebene, Anhänge). Zugleich der Pfad /setup.
const setupRef = "setup"

// Rechte des Bereichs (Migration 159).
const (
	permSetupView  = "setup.view"
	permSetupEdit  = "setup.edit"
	permSetupAdmin = "setup.admin"
)

// setupTitleSQL: Titel einer Rüstzeit für Listen, Verlauf und Verknüpfungen,
// z. B. „RZ-0001 · Formatwechsel“. Von Hand angelegte haben keine Rüsttaste –
// dann steht der Grund da. Alias der Tabelle setup_times: s.
const setupTitleSQL = `'RZ-' || lpad(s.number::text, 4, '0') || ' · ' ||
	COALESCE(NULLIF(s.button_name, ''), (SELECT sr.name FROM setup_reasons sr WHERE sr.id = s.reason_id), '')`

// setupStore: Speicher der Rüstzeiten; ohne Datenbank (Tests) nil.
func (h *Handler) setupStore() *ruesten.Store {
	if h.db == nil {
		return nil
	}
	return ruesten.NewStore(h.db)
}

// setupStatusText: Beschriftung des Status einer Rüstzeit.
func setupStatusText(s string) string {
	switch s {
	case ruesten.StatusRunning:
		return "Rüsten"
	case ruesten.StatusDone:
		return "Beendet"
	case ruesten.StatusFault:
		return "Störung durch Rüsten"
	}
	return s
}

// setupDuration: Dauer als „2 Std. 15 Min.“ bzw. „45 Min.“.
func setupDuration(from, to time.Time) string {
	m := int(to.Sub(from).Round(time.Minute) / time.Minute)
	if m < 0 {
		m = 0
	}
	if m < 60 {
		return fmt.Sprintf("%d Min.", m)
	}
	return fmt.Sprintf("%d Std. %d Min.", m/60, m%60)
}

// setupFormTime liest ein Feld datetime-local als Ortszeit des Servers. Der
// Speicher bestimmt den Schichtführer anhand der Uhrzeit des Werts – eine als
// UTC gelesene Eingabe träfe die falsche Schicht.
func setupFormTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("ungültige Zeitangabe: %q", v)
}
