package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"pdh/internal/core/users"
)

// Anmeldung am Terminal ohne Kartenleser: Karte + PIN, nur Karte oder Benutzer
// und Passwort. Wie /terminal/card nur aus Gerätesitzungen, nur JSON; die
// Fehlschläge zählen in dieselbe Bremse (cardFailLimit) wie unbekannte Karten.

const (
	msgLoginFailed  = "Anmeldung fehlgeschlagen"
	msgLoginTooMany = "Bitte kurz warten"
	loginMaxPIN     = 8   // Zeichen
	loginMaxCred    = 200 // Zeichen für Benutzer und Passwort
)

// loginKindAllowed: darf die Eingabeart am Terminal mit diesen Einstellungen genutzt werden?
// "none" sperrt alles; "password" ist zusätzlich über den Knopf erreichbar.
func loginKindAllowed(s terminalLoginSettings, kind string) bool {
	if s.ManualLogin == loginNone {
		return false
	}
	switch kind {
	case loginPassword:
		return s.PasswordButton || s.ManualLogin == loginPassword
	case loginCardPIN, loginCardOnly:
		return s.ManualLogin == kind
	}
	return false
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// TerminalLogin: POST /terminal/login
// {"kind":"card_pin","card":"…","pin":"…"} | {"kind":"card_only","card":"…"} |
// {"kind":"password","user":"…","password":"…"}
func (h *Handler) TerminalLogin(w http.ResponseWriter, r *http.Request) {
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
		Kind     string `json:"kind"`
		Card     string `json:"card"`
		PIN      string `json:"pin"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		cardFail(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	card := strings.TrimSpace(in.Card)
	user := strings.TrimSpace(in.User)
	valid := false
	switch in.Kind {
	case loginCardPIN:
		valid = runeLen(card) >= 1 && runeLen(card) <= cardMaxUID && in.PIN != "" && runeLen(in.PIN) <= loginMaxPIN
	case loginCardOnly:
		valid = runeLen(card) >= 1 && runeLen(card) <= cardMaxUID
	case loginPassword:
		valid = runeLen(user) >= 1 && runeLen(user) <= loginMaxCred && in.Password != "" && runeLen(in.Password) <= loginMaxCred
	}
	if !valid {
		cardFail(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}

	settings, err := h.terminalLoginSettings(r.Context(), term)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("anmeldeeinstellungen laden")
		cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
		return
	}
	if !loginKindAllowed(settings, in.Kind) {
		http.Error(w, "Diese Anmeldeart ist an diesem Terminal nicht erlaubt.", http.StatusForbidden)
		return
	}
	key := "card:" + term
	if cardFailLimit.blocked(key, time.Now()) {
		cardFail(w, http.StatusTooManyRequests, msgLoginTooMany)
		return
	}

	var p *users.User
	login := ""
	if in.Kind == loginPassword {
		login = "Benutzer " + user
		u, err := h.users.Authenticate(r.Context(), user, in.Password)
		if err == nil {
			p = u
		}
	} else {
		login = "Karte " + maskUID(card)
		u, err := h.users.GetByRFID(r.Context(), card)
		if err != nil && !isNoRows(err) {
			componentLog("terminal").Error().Err(err).Msg("karte nachschlagen")
			cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
			return
		}
		if err == nil {
			p = u
		}
	}
	// Unbekannt, inaktiv und Systembenutzer gelten gleich.
	good := p != nil && p.Active && !p.IsSystemUser
	if good && in.Kind == loginCardPIN {
		good, err = h.checkTerminalPIN(r.Context(), p.ID, in.PIN, time.Now())
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("terminal-pin prüfen")
			cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
			return
		}
	}
	if !good {
		cardFailLimit.fail(key, time.Now())
		authLog(r, false, "terminal-"+in.Kind, login, "", "", "anmeldung fehlgeschlagen")
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": msgLoginFailed})
		return
	}
	if err := h.cardSessionStart(r.Context(), term, p.ID); err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung starten")
		cardFail(w, http.StatusInternalServerError, "Daten nicht verfügbar")
		return
	}
	name := strings.TrimSpace(p.FirstName + " " + p.LastName)
	authLog(r, true, "terminal-"+in.Kind, login, p.ID, name, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}
