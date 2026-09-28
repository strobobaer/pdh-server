package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

func (h *Handler) notifyMicrosoftTeamsBoardAction(in GlobalBoardActionInput, assignedTo *string) {
	if h.microsoft.TeamsSenderUserID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	token, grantedScopes, err := h.microsoftTeamsSenderToken(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Microsoft Teams sender token unavailable")
		return
	}
	title, err := h.globalBoardTitle(ctx, in.Type, in.ID)
	if err != nil {
		log.Warn().Err(err).Msg("Microsoft Teams notification title unavailable")
		return
	}
	message := fmt.Sprintf("PDH: %s · %s", microsoftActionLabel(in.Action), title)

	if h.microsoft.TeamsID != "" && h.microsoft.TeamsChannelID != "" && hasMicrosoftScope(grantedScopes, "ChannelMessage.Send") {
		endpoint := "https://graph.microsoft.com/v1.0/teams/" + url.PathEscape(h.microsoft.TeamsID) + "/channels/" + url.PathEscape(h.microsoft.TeamsChannelID) + "/messages"
		payload := map[string]interface{}{"body": map[string]string{"contentType": "text", "content": message}}
		if err := h.microsoftGraphRequest(ctx, token, http.MethodPost, endpoint, payload, nil); err != nil {
			log.Warn().Err(err).Msg("Microsoft Teams channel notification failed")
		}
	}

	if !hasMicrosoftScope(grantedScopes, "Chat.ReadWrite") {
		return
	}
	var recipientIDs []string
	if assignedTo != nil {
		recipientIDs = []string{*assignedTo}
	} else {
		recipientIDs, err = h.globalBoardAssignees(ctx, in.Type, in.ID)
		if err != nil || len(recipientIDs) == 0 {
			return
		}
	}
	senderObjectID := h.microsoftSenderObjectID(ctx)
	for _, recipientID := range recipientIDs {
		h.sendMicrosoftTeamsDirectMessage(ctx, token, senderObjectID, recipientID, message)
	}
}

// sendMicrosoftTeamsDirectMessage schickt message per 1:1-Chat an einen
// einzelnen Empfaenger - ausgelagert aus notifyMicrosoftTeamsBoardAction,
// da eine Aufgabe jetzt mehrere Zugewiesene gleichzeitig haben kann.
func (h *Handler) sendMicrosoftTeamsDirectMessage(ctx context.Context, token, senderObjectID, recipientID, message string) {
	var graphUserID string
	if err := h.db.QueryRow(ctx, `
		SELECT microsoft_user_id FROM microsoft_directory_users
		WHERE tenant_id=$1 AND pdh_user_id=$2::uuid AND account_enabled=true`,
		h.microsoft.TenantID, recipientID).Scan(&graphUserID); err != nil || graphUserID == "" {
		return
	}
	if graphUserID == senderObjectID {
		return
	}
	var chat struct {
		ID string `json:"id"`
	}
	chatPayload := map[string]interface{}{
		"chatType": "oneOnOne",
		"members": []interface{}{map[string]interface{}{
			"@odata.type":     "#microsoft.graph.aadUserConversationMember",
			"roles":           []string{"owner"},
			"user@odata.bind": "https://graph.microsoft.com/v1.0/users('" + graphUserID + "')",
		}},
	}
	if err := h.microsoftGraphRequest(ctx, token, http.MethodPost, "https://graph.microsoft.com/v1.0/chats", chatPayload, &chat); err != nil || chat.ID == "" {
		if err != nil {
			log.Warn().Err(err).Msg("Microsoft Teams chat creation failed")
		}
		return
	}
	chatEndpoint := "https://graph.microsoft.com/v1.0/chats/" + url.PathEscape(chat.ID) + "/messages"
	messagePayload := map[string]interface{}{"body": map[string]string{"contentType": "text", "content": message}}
	if err := h.microsoftGraphRequest(ctx, token, http.MethodPost, chatEndpoint, messagePayload, nil); err != nil {
		log.Warn().Err(err).Msg("Microsoft Teams direct message failed")
	}
}

func (h *Handler) microsoftTeamsSenderToken(ctx context.Context) (string, string, error) {
	var email, displayName, microsoftUserID, grantedScopes string
	if err := h.db.QueryRow(ctx, `
		SELECT microsoft_email, display_name, microsoft_user_id, granted_scopes
		FROM microsoft_user_connections WHERE user_id=$1::uuid`, h.microsoft.TeamsSenderUserID,
	).Scan(&email, &displayName, &microsoftUserID, &grantedScopes); err != nil {
		return "", "", err
	}
	if !hasMicrosoftScope(grantedScopes, "Chat.ReadWrite") && !hasMicrosoftScope(grantedScopes, "ChannelMessage.Send") {
		return "", grantedScopes, fmt.Errorf("Teams permissions have not been granted by the sender account")
	}
	token, err := h.microsoftAccessToken(ctx, h.microsoft.TeamsSenderUserID)
	return token, grantedScopes, err
}

func (h *Handler) microsoftSenderObjectID(ctx context.Context) string {
	var id string
	_ = h.db.QueryRow(ctx, `SELECT microsoft_user_id FROM microsoft_user_connections WHERE user_id=$1::uuid`, h.microsoft.TeamsSenderUserID).Scan(&id)
	return id
}

func (h *Handler) globalBoardTitle(ctx context.Context, refType, id string) (string, error) {
	var table string
	switch refType {
	case "fault":
		table = "faults"
	case "ticket":
		table = "tickets"
	case "maintenance":
		table = "maintenance_tasks"
	case "task":
		table = "tasks"
	default:
		return "", fmt.Errorf("unsupported board type")
	}
	var title string
	query := "SELECT title FROM " + table + " WHERE id=$1::uuid"
	err := h.db.QueryRow(ctx, query, id).Scan(&title)
	return title, err
}

// globalBoardAssignees liefert die aktuell zugewiesenen Konten eines
// Vorgangs - bei Störung/Ticket/Wartung hoechstens eines (einzelne
// assigned_to-Spalte), bei Aufgaben moeglicherweise mehrere (task_assignees).
func (h *Handler) globalBoardAssignees(ctx context.Context, refType, id string) ([]string, error) {
	tables := map[string]string{"fault": "faults", "ticket": "tickets", "maintenance": "maintenance_tasks"}
	if table, ok := tables[refType]; ok {
		var assignedTo *string
		query := "SELECT assigned_to::text FROM " + table + " WHERE id=$1::uuid"
		if err := h.db.QueryRow(ctx, query, id).Scan(&assignedTo); err != nil || assignedTo == nil {
			return nil, err
		}
		return []string{*assignedTo}, nil
	}
	if refType != "task" {
		return nil, fmt.Errorf("unsupported board type")
	}
	rows, err := h.db.Query(ctx, `SELECT user_id::text FROM task_assignees WHERE task_id=$1::uuid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	return ids, rows.Err()
}

func microsoftActionLabel(action string) string {
	labels := map[string]string{"accept": "angenommen", "done": "erledigt", "discard": "verworfen", "wait": "zur Wiedervorlage gesetzt"}
	if label, ok := labels[strings.ToLower(action)]; ok {
		return label
	}
	return "aktualisiert"
}

func hasMicrosoftScope(scopes, required string) bool {
	for _, scope := range strings.Fields(scopes) {
		if strings.EqualFold(scope, required) {
			return true
		}
	}
	return false
}
