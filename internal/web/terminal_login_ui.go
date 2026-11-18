package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
)

var errTerminalLoginSave = errors.New("Anmeldeart konnte nicht gespeichert werden")
var errTerminalPINSave = errors.New("PIN konnte nicht gespeichert werden")
var errTerminalLoginKind = errors.New("Unbekannte Anmeldeart")
var errTerminalPINSystem = errors.New("Systembenutzer haben keine Terminal-PIN")

// terminalPINView liefert für die Oberfläche: hat der Benutzer eine PIN, und ob
// sie gesperrt ist ("HH:MM" in der Ortszeit des Servers, "" = nicht gesperrt).
// Die PIN selbst ist nirgends lesbar (nur der Hash liegt in der Datenbank).
func (h *Handler) terminalPINView(ctx context.Context, userID string) (bool, string) {
	if h.db == nil {
		return false, ""
	}
	has, locked, err := h.terminalPINState(ctx, userID, time.Now())
	if err != nil {
		componentLog("terminal").Error().Err(err).Str("user", userID).Msg("pin-status (formular)")
		return false, ""
	}
	if locked != nil {
		return has, locked.Local().Format("15:04")
	}
	return has, ""
}

// applyTerminalPIN führt die Formularaktion (set|clear) für einen Benutzer aus.
// Rückgabe: Meldung für die Oberfläche (nie mit der PIN) oder Fehler.
func (h *Handler) applyTerminalPIN(ctx context.Context, userID, actorID, action, pin string) (string, error) {
	sys, err := h.isSystemUser(ctx, userID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Str("user", userID).Msg("systembenutzer prüfen")
		return "", errTerminalPINSave
	}
	if sys {
		return "", errTerminalPINSystem
	}
	switch action {
	case "set":
		if err := validTerminalPIN(pin); err != nil {
			return "", err
		}
		if err := h.setTerminalPIN(ctx, userID, pin); err != nil {
			componentLog("terminal").Error().Err(err).Str("user", userID).Msg("pin setzen")
			return "", errTerminalPINSave
		}
		// Verlauf ohne PIN-Wert.
		h.addHistory(ctx, "user", userID, "update", "Terminal-PIN", "", "", "Terminal-PIN gesetzt", actorID)
		return "PIN gesetzt", nil
	case "clear":
		if err := h.clearTerminalPIN(ctx, userID); err != nil {
			componentLog("terminal").Error().Err(err).Str("user", userID).Msg("pin entfernen")
			return "", errTerminalPINSave
		}
		h.addHistory(ctx, "user", userID, "update", "Terminal-PIN", "", "", "Terminal-PIN entfernt", actorID)
		return "PIN entfernt", nil
	}
	return "", errors.New("Unbekannte Aktion")
}

// UserTerminalPINWeb: POST /users/{id}/terminal-pin (action=set|clear, pin) -
// nur mit „Benutzer verwalten“ (und Rang über dem Zielbenutzer).
func (h *Handler) UserTerminalPINWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(r.Context(), id)
	if err != nil || target == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canManageUsers(r) || (target.ID != getUser(r).ID && !h.outranksRole(r, string(target.Role))) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	msg, err := h.applyTerminalPIN(r.Context(), id, getUser(r).ID, r.FormValue("action"), r.FormValue("pin"))
	userRedirect(w, r, id, "master", msg, err)
}

// AccountTerminalPINWeb: POST /account/terminal-pin - jeder Benutzer nur für sich.
func (h *Handler) AccountTerminalPINWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if u == nil || u.ID == "" {
		http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
		return
	}
	msg, err := h.applyTerminalPIN(r.Context(), u.ID, u.ID, r.FormValue("action"), r.FormValue("pin"))
	if err != nil {
		msg = err.Error()
	}
	http.Redirect(w, r, "/account?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}
