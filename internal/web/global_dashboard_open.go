package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Oeffnen aus dem Leitstand (/global) ohne bestehende Anmeldung.
//
// Wer am Leitstand einen Vorgang oder eine Anlage oeffnen will, scannt seine
// RFID-Karte. Der Server stellt dafuer eine KURZE Sitzung aus – keine
// dauerhafte Anmeldung:
//   - das Token laeuft nach der Override-Zeit ab (Rollen & Berechtigungen),
//   - die Cookies sind Browser-Sitzungs-Cookies (kein MaxAge),
//   - Inaktivitaet, „Zurück zum Leitstand“ oder ein erneuter Aufruf des
//     Leitstands beenden sie (Cookie pdh_board_visit markiert die Sitzung).
// Die Person arbeitet in dieser Zeit mit ihren eigenen Rechten.

const (
	boardVisitCookie     = "pdh_board_visit"
	boardVisitDefaultTTL = 15 * time.Minute
)

// boardVisitTTL: Dauer der Kurzsitzung = Override-Zeit, sonst 15 Minuten.
func (h *Handler) boardVisitTTL() time.Duration {
	if m := h.rbac.OverrideTimeoutMinutes(); m > 0 {
		return time.Duration(m) * time.Minute
	}
	return boardVisitDefaultTTL
}

// isBoardVisit: laeuft gerade eine Kurzsitzung aus dem Leitstand?
func isBoardVisit(r *http.Request) bool {
	c, err := r.Cookie(boardVisitCookie)
	return err == nil && c.Value != ""
}

// endBoardVisit loescht die Kurzsitzung samt Markierung.
func endBoardVisit(w http.ResponseWriter) {
	for _, name := range []string{"pdh_token", "pdh_user_id", boardVisitCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
	}
}

// GlobalDashboardOpen: POST /global/open {rfid_uid, next} → {url, name}
func (h *Handler) GlobalDashboardOpen(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		RFIDUID string `json:"rfid_uid"`
		Next    string `json:"next"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	in.RFIDUID = strings.TrimSpace(in.RFIDUID)
	if in.RFIDUID == "" {
		writeGlobalBoardError(w, http.StatusUnauthorized, "Bitte RFID-Karte scannen")
		return
	}
	actor, err := h.users.GetByRFID(r.Context(), in.RFIDUID)
	if err != nil || actor == nil || !actor.Active || actor.IsSystemUser {
		authLog(r, false, "rfid-leitstand-oeffnen", "Karte "+maskUID(in.RFIDUID), "", "", "karte unbekannt oder nicht zulaessig")
		writeGlobalBoardError(w, http.StatusUnauthorized, "RFID-Karte gehört keinem aktiven Mitarbeiter")
		return
	}
	token, err := h.users.IssueToken(actor, h.boardVisitTTL(), map[string]interface{}{"board_visit": true})
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Öffnen fehlgeschlagen")
		return
	}
	// Sitzungs-Cookies ohne MaxAge: enden spaetestens mit dem Browser, das Token vorher
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: actor.ID, Path: "/", SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: boardVisitCookie, Value: "1", Path: "/", SameSite: http.SameSiteLaxMode, HttpOnly: true})
	http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: "", Path: "/", MaxAge: -1})
	name := strings.TrimSpace(actor.FirstName + " " + actor.LastName)
	authLog(r, true, "rfid-leitstand-oeffnen", "Karte "+maskUID(in.RFIDUID), actor.ID, name, "")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": loginNext(in.Next), "name": name})
}
