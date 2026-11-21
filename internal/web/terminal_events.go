package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Ereigniskanal GET /terminal/events: Das Terminal erfährt sofort, wenn sich
// Anmeldeart, Passwortknopf oder Rückkehrzeit ändern (Server-Sent Events).
// Neue Ereignisse sind für ältere Terminalseiten unsichtbar, sie lesen nur
// die Ereignisse, die sie kennen.

const terminalEventPing = 25 * time.Second

// terminalEventHub verteilt Ereignisse an die offenen Ereigniskanäle je Terminal-Benutzer.
type terminalEventHub struct {
	mu   sync.Mutex
	subs map[string]map[*terminalEventSub]struct{}
}

type terminalEventSub struct {
	ch chan string
}

func newTerminalEventHub() *terminalEventHub {
	return &terminalEventHub{subs: map[string]map[*terminalEventSub]struct{}{}}
}

// subscribe meldet einen Abonnenten für userID an. Der Kanal hat Puffer 1;
// die Funktion meldet ab (mehrfach aufrufbar). closeUser schließt den Kanal.
func (b *terminalEventHub) subscribe(userID string) (<-chan string, func()) {
	s := &terminalEventSub{ch: make(chan string, 1)}
	b.mu.Lock()
	if b.subs[userID] == nil {
		b.subs[userID] = map[*terminalEventSub]struct{}{}
	}
	b.subs[userID][s] = struct{}{}
	b.mu.Unlock()
	return s.ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.remove(userID, s)
	}
}

// remove entfernt den Abonnenten (b.mu gehalten).
func (b *terminalEventHub) remove(userID string, s *terminalEventSub) {
	if m := b.subs[userID]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(b.subs, userID)
		}
	}
}

// publish sendet event an alle Abonnenten von userID ("" = an alle). Blockiert
// nie: Ist der Puffer eines Abonnenten voll, wartet dort schon ein Ereignis
// (doppelte Ereignisse werden zusammengefasst).
func (b *terminalEventHub) publish(userID, event string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	send := func(m map[*terminalEventSub]struct{}) {
		for s := range m {
			select {
			case s.ch <- event:
			default:
			}
		}
	}
	if userID == "" {
		for _, m := range b.subs {
			send(m)
		}
		return
	}
	send(b.subs[userID])
}

// closeUser beendet alle Abonnements des Benutzers (Kanäle werden geschlossen).
// Das trennt bewusst alle Geräte dieses Terminal-Benutzers; nicht gesperrte Geräte
// verbinden sich von selbst neu.
func (b *terminalEventHub) closeUser(userID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs[userID] {
		close(s.ch)
	}
	delete(b.subs, userID)
}

// eventHub: Verteiler der Terminal-Ereignisse (bei Bedarf angelegt, damit auch
// von Hand gebaute Handler ihn haben).
func (h *Handler) eventHub() *terminalEventHub {
	h.termEventsOnce.Do(func() {
		if h.termEvents == nil {
			h.termEvents = newTerminalEventHub()
		}
	})
	return h.termEvents
}

// notifyTerminalSettings meldet dem Terminal des Benutzers geänderte Einstellungen.
func (h *Handler) notifyTerminalSettings(userID string) {
	h.eventHub().publish(userID, "settings")
}

// terminalSettingsEvent: Nutzdaten des Ereignisses „settings“.
func (h *Handler) terminalSettingsEvent(r *http.Request, userID string) (string, error) {
	s, err := h.terminalLoginSettings(r.Context(), userID)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{
		"manual_login":    s.ManualLogin,
		"password_button": s.PasswordButton,
		"idle":            h.terminalIdleSeconds(r.Context(), userID),
		"card_notify":     effectiveCardNotify(s.CardNotify),
		"global_screen":   s.GlobalScreen,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: settings\ndata: %s\n\n", b), nil
}

// TerminalEvents: GET /terminal/events – nur Gerätesitzungen.
func (h *Handler) TerminalEvents(w http.ResponseWriter, r *http.Request) {
	term, ok := h.cardTerminal(w, r)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming nicht möglich", http.StatusInternalServerError)
		return
	}
	// Erst anmelden, dann lesen: Änderungen zwischen beiden Schritten gehen nicht verloren.
	ch, unsub := h.eventHub().subscribe(term)
	defer unsub()
	first, err := h.terminalSettingsEvent(r, term)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("ereigniskanal: einstellungen lesen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	// Dauerverbindung: Schreibfrist des Servers aufheben (wie serveEvents im Gerät).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, first); err != nil {
		return
	}
	fl.Flush()
	tick := time.NewTicker(terminalEventPing)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if ev == "slides" {
				// Folien geändert: das globale Terminal lädt sie neu.
				if _, err := fmt.Fprint(w, "event: slides\ndata: {}\n\n"); err != nil {
					return
				}
				fl.Flush()
				continue
			}
			if ev != "settings" {
				continue
			}
			msg, err := h.terminalSettingsEvent(r, term)
			if err != nil {
				componentLog("terminal").Error().Err(err).Msg("ereigniskanal: einstellungen lesen")
				continue
			}
			if _, err := fmt.Fprint(w, msg); err != nil {
				return
			}
			fl.Flush()
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
