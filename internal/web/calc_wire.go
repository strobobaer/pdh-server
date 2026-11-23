package web

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/internal/modules/calc"
)

// Berechnete Werte (virtuelle Messwerte): Verdrahtung von Speicher und
// Rechenwerk (internal/modules/calc). Seite und Formular: calc_values.go.

// calcStore liefert den Speicher der berechneten Werte (nil ohne Datenbank).
// „Heute“ ist der Ortstag des Servers (localDate), wie bei allen Tagesgrenzen.
func (h *Handler) calcStore() *calc.Store {
	if h.db == nil {
		return nil
	}
	s := calc.NewStore(h.db)
	s.Today = func() time.Time { return localDate(time.Now()) }
	return s
}

// calcNudge stößt die Rechnung an: ein Eingangswert hat sich geändert oder
// eine Formel wurde gespeichert. Blockiert nie; ohne Rechenwerk (Spiegel,
// Tests) wirkungslos.
func (h *Handler) calcNudge() {
	h.calcEngine.Load().Nudge()
}

// StartCalcEngine startet das Rechenwerk der berechneten Werte (nur am aktiven
// Server; ruht in der Wartungssperre).
func (h *Handler) StartCalcEngine(ctx context.Context) {
	st := h.calcStore()
	if st == nil {
		log.Warn().Msg("berechnete werte: rechenwerk nicht gestartet (keine datenbank)")
		return
	}
	eng := calc.NewEngine(st, h.MaintLocked)
	h.calcEngine.Store(eng) // Konnektoren stoßen schon an, während die Dienste starten
	go eng.Start(ctx)
}
