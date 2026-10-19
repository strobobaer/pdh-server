package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh/internal/integrations/nextcloud"
)

// Benutzerstamm → „Nextcloud-Konto anlegen“: legt das Konto in Nextcloud an
// (Provisioning-API) und verknüpft es mit der Person (nextcloud_user_id).

// microsoftNextcloudSetting: Nextcloud-Konten beim Microsoft-Abgleich anlegen
// und als Nextcloud-Benutzer-ID die Microsoft-E-Mail nehmen (passt zur
// Nextcloud-Anmeldung mit Microsoft über user_oidc, --mapping-uid=email).
const microsoftNextcloudSetting = "microsoft_nextcloud_provision"

// nextcloudAccountReady: Knopf zeigen? (Nextcloud eingerichtet, Person noch ohne Konto)
func nextcloudAccountReady(linked string) bool {
	return strings.TrimSpace(linked) == "" && nextcloud.UserClientFromEnv().Enabled()
}

// nextcloudIDFromMicrosoftEmail: Nextcloud-Benutzer-ID für ein Microsoft-Konto.
// Nur wenn die E-Mail unverändert als ID taugt – sonst fände die Microsoft-
// Anmeldung in Nextcloud das Konto nicht. Gastkonten (#EXT#) fallen heraus.
func nextcloudIDFromMicrosoftEmail(email string) (string, bool) {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") || nextcloud.SanitizeUserID(email) != email {
		return "", false
	}
	return email, true
}

// nextcloudIDForUser: Microsoft-E-Mail, wenn die Microsoft-Anmeldung für
// Nextcloud eingeschaltet und die Person im Verzeichnis zugeordnet ist,
// sonst der PDH-Benutzername.
func (h *Handler) nextcloudIDForUser(ctx context.Context, userID, username string) string {
	if h.getUpdateSetting(ctx, microsoftNextcloudSetting, "") == "1" {
		var email string
		_ = h.db.QueryRow(ctx, `SELECT email FROM microsoft_directory_users WHERE tenant_id=$1 AND pdh_user_id=$2::uuid LIMIT 1`,
			h.microsoft.TenantID, userID).Scan(&email)
		if id, ok := nextcloudIDFromMicrosoftEmail(email); ok {
			return id
		}
	}
	return nextcloud.SanitizeUserID(username)
}

// createAndLinkNextcloudAccount legt das Nextcloud-Konto an (ein vorhandenes
// wird übernommen) und trägt es bei der Person ein. pw ist das Startpasswort
// eines neu angelegten Kontos.
func (h *Handler) createAndLinkNextcloudAccount(ctx context.Context, userID, ncID, displayName, email, actorID string) (pw string, existed bool, err error) {
	var taken bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE nextcloud_user_id = $1 AND id <> $2::uuid)`, ncID, userID).Scan(&taken)
	if taken {
		return "", false, fmt.Errorf("Das Nextcloud-Konto „%s“ ist schon einer anderen Person zugeordnet", ncID)
	}
	pw, err = nextcloud.UserClientFromEnv().CreateUser(ctx, nextcloud.NewUser{UserID: ncID, DisplayName: displayName, Email: email})
	existed = errors.Is(err, nextcloud.ErrUserExists)
	if err != nil && !existed {
		componentLog("nextcloud").Warn().Err(err).Str("user", userID).Msg("konto anlegen")
		return "", false, err
	}
	if _, err := h.db.Exec(ctx, `UPDATE users SET nextcloud_user_id = $1, nextcloud_synced = true, updated_at = NOW() WHERE id = $2::uuid`, ncID, userID); err != nil {
		return "", existed, fmt.Errorf("Konto angelegt, Verknüpfung fehlgeschlagen: %w", err)
	}
	msg := "Nextcloud-Konto „" + ncID + "“ angelegt"
	if existed {
		msg = "Nextcloud-Konto „" + ncID + "“ gab es schon – verknüpft"
	}
	h.addHistory(ctx, "user", userID, "update", "nextcloud_user_id", "", ncID, msg, actorID)
	componentLog("nextcloud").Info().Str("user", userID).Str("nextcloud", ncID).Bool("vorhanden", existed).Msg("konto verknüpft")
	return pw, existed, nil
}

// UserNextcloudAccountWeb: POST /users/{id}/nextcloud-account (htmx)
func (h *Handler) UserNextcloudAccountWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(ctx, id)
	if err != nil || target == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.canManageUsers(r) || (target.ID != getUser(r).ID && !h.outranksRole(r, string(target.Role))) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fail := func(msg string) {
		_, _ = w.Write([]byte(`<span style="color:var(--red);font-size:12px"><i class="ti ti-alert-circle"></i> ` + esc(msg) + `</span>`))
	}
	if target.NextcloudUserID != "" {
		fail("Bereits mit dem Nextcloud-Konto „" + target.NextcloudUserID + "“ verknüpft")
		return
	}
	ncID := h.nextcloudIDForUser(ctx, id, target.Username)
	if ncID == "" {
		fail("Aus dem Benutzernamen lässt sich keine Nextcloud-Benutzer-ID bilden")
		return
	}
	pw, existed, err := h.createAndLinkNextcloudAccount(ctx, id, ncID,
		strings.TrimSpace(target.FirstName+" "+target.LastName), target.Email, getUser(r).ID)
	if err != nil {
		fail(err.Error())
		return
	}
	msg := "Nextcloud-Konto „" + ncID + "“ angelegt"
	if existed {
		msg = "Nextcloud-Konto „" + ncID + "“ gab es schon – verknüpft"
	}
	out := `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> ` + esc(msg)
	if pw != "" {
		out += ` – Startpasswort (wird nur jetzt angezeigt): <code style="user-select:all">` + esc(pw) + `</code>`
	}
	out += `</span><input id="ud-nextcloud" name="nextcloud_user_id" class="form-input" value="` + esc(ncID) + `" hx-swap-oob="true">`
	_, _ = w.Write([]byte(out))
}
