package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	coreusers "pdh/internal/core/users"
	"pdh/pkg/appsettings"
)

// Wer am Leitstand handelt (Annehmen, Verwerfen, Fertigmeldung starten):
// eine Person aus Instandhaltung oder IT – aus der Liste gewaehlt oder per
// RFID-Karte. Die Auswahl ist ohne Nachweis; mit KeyBoardPickTerminalOnly
// zeigt nur ein angemeldetes Terminal (Systembenutzer) die Liste, an allen
// anderen Geraeten bleibt die Karte Pflicht.

// KeyBoardPickTerminalOnly: 1 = Personenauswahl nur am Terminal, 0 = ueberall (Standard).
const KeyBoardPickTerminalOnly = "global_board_pick_terminal_only"

func (h *Handler) boardPickTerminalOnly(ctx context.Context) bool {
	return h.db != nil && appsettings.GetInt(ctx, h.db, KeyBoardPickTerminalOnly, 0) == 1
}

// boardPickAllowed: darf an diesem Geraet aus der Liste gewaehlt werden?
func (h *Handler) boardPickAllowed(r *http.Request) bool {
	if !h.boardPickTerminalOnly(r.Context()) {
		return true
	}
	u := h.sessionUser(r)
	return u != nil && u.IsSystemUser
}

// boardActorEligible: aktive Person aus Instandhaltung/IT, kein Systemkonto, kein Betrachter.
func boardActorEligible(u *coreusers.User) bool {
	return u != nil && u.Active && !u.IsSystemUser && u.Role != coreusers.RoleViewer && isGlobalBoardDepartment(u.Department)
}

// boardActors: Auswahlliste (Instandhaltung und IT).
func (h *Handler) boardActors(ctx context.Context) []UserOption {
	out := make([]UserOption, 0)
	rows, err := h.db.Query(ctx, `SELECT id::text, first_name, last_name, COALESCE(department, '') FROM users
		WHERE active = true AND is_system_user = false AND role::text <> $1 ORDER BY last_name, first_name`, string(coreusers.RoleViewer))
	if err != nil {
		componentLog("leitstand").Warn().Err(err).Msg("instandhaltung laden")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, first, last, dept string
		if rows.Scan(&id, &first, &last, &dept) == nil && isGlobalBoardDepartment(dept) {
			out = append(out, UserOption{ID: id, Name: strings.TrimSpace(first + " " + last)})
		}
	}
	return out
}

// boardActor ermittelt die handelnde Person; how = "Auswahl" oder "Karte" (Protokoll).
func (h *Handler) boardActor(r *http.Request, userID, rfidUID string) (actor *coreusers.User, how string, err error) {
	userID, rfidUID = strings.TrimSpace(userID), strings.TrimSpace(rfidUID)
	switch {
	case userID != "":
		if !h.boardPickAllowed(r) {
			return nil, "", errors.New("Auswahl nur am Terminal – bitte RFID-Karte scannen")
		}
		u, e := h.users.GetByID(r.Context(), userID)
		if e != nil || !boardActorEligible(u) {
			return nil, "", errors.New("Bitte eine Person aus Instandhaltung oder IT auswählen")
		}
		return u, "Auswahl", nil
	case rfidUID != "":
		_, u, e := h.users.LoginByRFID(r.Context(), rfidUID)
		if e != nil || !boardActorEligible(u) {
			return nil, "", errors.New("RFID-Karte gehört keinem aktiven Mitarbeiter aus Instandhaltung oder IT")
		}
		return u, "Karte", nil
	}
	if h.boardPickAllowed(r) {
		return nil, "", errors.New("Bitte auswählen, wer die Aktion ausführt")
	}
	return nil, "", errors.New("Bitte RFID-Karte zur Bestätigung scannen")
}

// BoardPickSettingsWeb: POST /core/settings/board-pick (terminal_only=on) – Core-Einstellungen.
func (h *Handler) BoardPickSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	v, msg := "0", "Leitstand: Personenauswahl an allen Geräten ohne Karte"
	if r.FormValue("terminal_only") == "on" {
		v, msg = "1", "Leitstand: Personenauswahl nur am Terminal – sonst RFID-Karte"
	}
	if err := h.setUpdateSetting(r.Context(), KeyBoardPickTerminalOnly, v); err != nil {
		http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}
