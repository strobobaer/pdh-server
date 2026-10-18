package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"pdh/internal/integrations/nextcloud"
)

// Benutzerstamm → „Nextcloud-Konto anlegen“: legt das Konto in Nextcloud an
// (Provisioning-API) und verknüpft es mit der Person (nextcloud_user_id).

// nextcloudAccountReady: Knopf zeigen? (Nextcloud eingerichtet, Person noch ohne Konto)
func nextcloudAccountReady(linked string) bool {
	return strings.TrimSpace(linked) == "" && nextcloud.UserClientFromEnv().Enabled()
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
	ncID := nextcloud.SanitizeUserID(target.Username)
	if ncID == "" {
		fail("Aus dem Benutzernamen lässt sich keine Nextcloud-Benutzer-ID bilden")
		return
	}
	var taken bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE nextcloud_user_id = $1 AND id <> $2::uuid)`, ncID, id).Scan(&taken)
	if taken {
		fail("Das Nextcloud-Konto „" + ncID + "“ ist schon einer anderen Person zugeordnet")
		return
	}
	pw, err := nextcloud.UserClientFromEnv().CreateUser(ctx, nextcloud.NewUser{
		UserID: ncID, DisplayName: strings.TrimSpace(target.FirstName + " " + target.LastName), Email: target.Email,
	})
	existed := errors.Is(err, nextcloud.ErrUserExists)
	if err != nil && !existed {
		componentLog("nextcloud").Warn().Err(err).Str("user", id).Msg("konto anlegen")
		fail(err.Error())
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE users SET nextcloud_user_id = $1, nextcloud_synced = true, updated_at = NOW() WHERE id = $2::uuid`, ncID, id); err != nil {
		fail("Konto angelegt, Verknüpfung fehlgeschlagen: " + err.Error())
		return
	}
	msg := "Nextcloud-Konto „" + ncID + "“ angelegt"
	if existed {
		msg = "Nextcloud-Konto „" + ncID + "“ gab es schon – verknüpft"
	}
	h.addHistory(ctx, "user", id, "update", "nextcloud_user_id", "", ncID, msg, getUser(r).ID)
	componentLog("nextcloud").Info().Str("user", id).Str("nextcloud", ncID).Bool("vorhanden", existed).Msg("konto verknüpft")
	out := `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> ` + esc(msg)
	if pw != "" {
		out += ` – Startpasswort (wird nur jetzt angezeigt): <code style="user-select:all">` + esc(pw) + `</code>`
	}
	out += `</span><input id="ud-nextcloud" name="nextcloud_user_id" class="form-input" value="` + esc(ncID) + `" hx-swap-oob="true">`
	_, _ = w.Write([]byte(out))
}
