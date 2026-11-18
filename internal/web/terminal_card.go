package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Kartenanmeldung am Anlagenterminal: Die Terminalseite liest die RFID-Karte
// und meldet die UID hierher. PDH ordnet sie einer Person zu und führt pro
// Terminal-Benutzer eine Kartensitzung (terminal_card_store.go). Das ist keine
// PDH-Anmeldung. Nur JSON (SameSite=Lax-Cookies + Content-Type application/json
// verhindern Cross-Site-Formular-Posts).

const (
	cardMaxBody    = 1024 // Bytes
	cardMaxUID     = 100  // Zeichen
	msgCardUnknown = "Karte nicht registriert"
	msgCardTooMany = "Zu viele unbekannte Karten. Bitte kurz warten."
)

// cardFailLimit: Fehlversuche aller Anmeldewege pro Terminal (unbekannte Karten, falsche
// PIN/Passwort über /terminal/login; Schlüssel "card:"+Terminal-ID);
// ab 10 Fehlversuchen in 1 min antwortet der Handler mit 429.
// Hinweis: Der Zähler liegt im Speicher und gilt je PDH-Instanz; unter HA ist die
// effektive Grenze daher 10 × Anzahl der Instanzen.
var cardFailLimit = newFailLimiter(10, time.Minute)

func (h *Handler) cardRoutes(r chi.Router) {
	r.Post("/terminal/card", h.TerminalCard)
	r.Post("/terminal/card/ping", h.TerminalCardPing)
	r.Post("/terminal/card/abmelden", h.TerminalCardLogout)
	r.Post("/terminal/login", h.TerminalLogin)
	r.Get("/terminal/events", h.TerminalEvents)
}

// cardTerminal: Terminal-Benutzer des Geräts; sonst 403 (wie terminalGate).
// Nur Gerätesitzungen: Nur diese abonnieren den Kartenstrom (data-card-events),
// eine Override-/Passwortsitzung eines Terminal-Benutzers darf keine Karte melden.
func (h *Handler) cardTerminal(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid := h.terminalUserID(r)
	if uid == "" || !h.terminalDeviceSession(r) {
		http.Error(w, "Diese Seite ist nur für Terminal-Benutzer (Systembenutzer) bestimmt.", http.StatusForbidden)
		return "", false
	}
	return uid, true
}

func cardFail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "message": msg})
}

// TerminalCard: POST /terminal/card {"uid":"…"}
func (h *Handler) TerminalCard(w http.ResponseWriter, r *http.Request) {
	term, ok := h.cardTerminal(w, r)
	if !ok {
		return
	}
	if ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])); ct != "application/json" {
		cardFail(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cardMaxBody)
	var in struct {
		UID string `json:"uid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		cardFail(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	uid := strings.TrimSpace(in.UID)
	if n := utf8.RuneCountInString(uid); n < 1 || n > cardMaxUID {
		cardFail(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	key := "card:" + term
	if cardFailLimit.blocked(key, time.Now()) {
		cardFail(w, http.StatusTooManyRequests, msgCardTooMany)
		return
	}
	p, err := h.users.GetByRFID(r.Context(), uid)
	if err != nil && !isNoRows(err) {
		componentLog("terminal").Error().Err(err).Msg("karte nachschlagen")
		cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
		return
	}
	// Unbekannt, inaktiv (GetByRFID filtert) und Systembenutzer gelten gleich.
	if err != nil || p == nil || !p.Active || p.IsSystemUser {
		cardFailLimit.fail(key, time.Now())
		authLog(r, false, "rfid-terminal", "Karte "+maskUID(uid), "", "", "karte unbekannt oder nicht zulässig")
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": msgCardUnknown})
		return
	}
	if err := h.cardSessionStart(r.Context(), term, p.ID); err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung starten")
		cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
		return
	}
	name := strings.TrimSpace(p.FirstName + " " + p.LastName)
	authLog(r, true, "rfid-terminal", "Karte "+maskUID(uid), p.ID, name, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

// TerminalCardPing: POST /terminal/card/ping – verlängert die Sitzung.
func (h *Handler) TerminalCardPing(w http.ResponseWriter, r *http.Request) {
	term, ok := h.cardTerminal(w, r)
	if !ok {
		return
	}
	touched, err := h.cardSessionTouch(r.Context(), term)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung verlängern")
		cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
		return
	}
	if touched {
		if p, err := h.cardSessionPerson(r.Context(), term); err == nil && p != nil {
			writeJSON(w, http.StatusOK, map[string]any{"active": true, "name": strings.TrimSpace(p.FirstName + " " + p.LastName)})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": false})
}

// TerminalCardLogout: POST /terminal/card/abmelden – Sitzung beenden.
func (h *Handler) TerminalCardLogout(w http.ResponseWriter, r *http.Request) {
	term, ok := h.cardTerminal(w, r)
	if !ok {
		return
	}
	if err := h.cardSessionEnd(r.Context(), term); err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung beenden")
		cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
