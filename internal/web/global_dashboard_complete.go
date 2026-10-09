package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	coreusers "pdh/internal/core/users"
)

// Fertigmeldung aus dem Leitstand (/global) ueber den Abschluss-Assistenten.
//
// Der Leitstand ist ohne Anmeldung offen; wer dort "Fertig" tippt, scannt
// zuerst seine RFID-Karte. Der Server prueft die Person wie bei den anderen
// Leitstand-Aktionen und stellt ein kurzlebiges Token aus, das NUR fuer
// diesen einen Vorgang gilt:
//   - /complete/{type}/{id} (Assistent: Daten laden, abschliessen),
//   - Material dieses Vorgangs (/api/v1/…/{id}/pending-parts),
//   - Lagerorte und Ersatzteilliste lesen (Auswahl im Schritt Material).
// Damit laeuft der normale Assistent im Namen und mit den Rechten der Person
// mit der Karte – sonst nichts. Das Token liegt nur im Speicher der Seite.

const (
	boardCompleteScope = "board-complete"
	boardCompleteTTL   = 20 * time.Minute
)

type boardCompleteKey struct{}

// boardCompleteFrom: Fertigmeldung ueber den Leitstand (Token statt Sitzung)?
func boardCompleteFrom(ctx context.Context) bool {
	v, _ := ctx.Value(boardCompleteKey{}).(bool)
	return v
}

// GlobalDashboardCompleteStart: POST /global/complete-start {type, id, user_id | rfid_uid}
func (h *Handler) GlobalDashboardCompleteStart(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		RFIDUID string `json:"rfid_uid"`
		UserID  string `json:"user_id"` // Auswahl statt Karte (global_dashboard_actor.go)
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Anfrage")
		return
	}
	in.ID, in.RFIDUID = strings.TrimSpace(in.ID), strings.TrimSpace(in.RFIDUID)
	if _, ok := completionKinds[in.Type]; !ok || !globalBoardTypeAllowed(in.Type) || !uuidInPathRe.MatchString(in.ID) || len(in.ID) != 36 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Unbekannter Vorgang")
		return
	}
	actor, how, err := h.boardActor(r, in.UserID, in.RFIDUID)
	if err != nil {
		if in.RFIDUID != "" {
			authLog(r, false, "rfid-leitstand-fertig", "Karte "+maskUID(in.RFIDUID), "", "", "karte unbekannt oder nicht berechtigt")
		}
		writeGlobalBoardError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !h.globalBoardRecordActive(r, in.Type, in.ID) {
		writeGlobalBoardError(w, http.StatusConflict, "Der Vorgang ist nicht mehr offen")
		return
	}
	token, err := h.users.IssueToken(actor, boardCompleteTTL, map[string]interface{}{"scope": boardCompleteScope, "ref": in.Type + ":" + in.ID})
	if err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Fertigmeldung konnte nicht gestartet werden")
		return
	}
	name := strings.TrimSpace(actor.FirstName + " " + actor.LastName)
	if how == "Karte" {
		authLog(r, true, "rfid-leitstand-fertig", "Karte "+maskUID(in.RFIDUID), actor.ID, name, "")
	} else {
		authLog(r, true, "auswahl-leitstand-fertig", "Auswahl am Leitstand", actor.ID, name, "")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "name": name})
}

// boardCompleteUser: Person hinter einem gueltigen Leitstand-Token, sofern
// die Anfrage genau den freigegebenen Vorgang betrifft (/complete/{type}/{id}).
func (h *Handler) boardCompleteUser(r *http.Request) *coreusers.User {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil
	}
	token, err := jwt.Parse(strings.TrimPrefix(auth, "Bearer "), func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unerwartete signaturmethode")
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return nil
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || claims["scope"] != boardCompleteScope {
		return nil
	}
	ref, _ := claims["ref"].(string)
	typ, id, ok := strings.Cut(ref, ":")
	// der Vorgang selbst und seine Unterpfade (Checklistenpunkte, Fotos)
	base := "/complete/" + typ + "/" + id
	if !ok || (r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/steps/")) {
		return nil
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil
	}
	u, err := h.users.GetByID(r.Context(), sub)
	if err != nil || u == nil || !u.Active {
		return nil
	}
	return u
}

// logBoardCompletion: abgeschlossene Fertigmeldung wie die anderen
// Leitstand-Aktionen protokollieren und (falls eingerichtet) nach Teams melden.
func (h *Handler) logBoardCompletion(ctx context.Context, refType, id, comment, actorID string) {
	if len([]rune(comment)) > 1000 {
		comment = string([]rune(comment)[:1000])
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO global_dashboard_actions (ref_type, ref_id, action, comment, action_user_id)
		VALUES ($1, $2::uuid, 'done', $3, $4::uuid)`, refType, id, comment, actorID); err != nil {
		componentLog("leitstand").Warn().Err(err).Str("ref", refType+":"+id).Msg("fertigmeldung nicht protokolliert")
	}
	go h.notifyMicrosoftTeamsBoardAction(GlobalBoardActionInput{Type: refType, ID: id, Action: "done", Comment: comment}, nil)
}
