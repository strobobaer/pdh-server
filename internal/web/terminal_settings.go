package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"pdh/pkg/appsettings"
)

// Rückkehrzeit des Anlagenterminals: nach so vielen Sekunden ohne Berührung
// kehrt die Seite zum Bildschirmschoner zurück. Je Terminal (Tabelle
// terminal_settings) einstellbar; ohne eigenen Wert gilt die globale
// Einstellung (app_settings, KeyTerminalIdle), sonst der Standard.
const (
	terminalIdleDefault = 120
	terminalIdleMin     = 30
	terminalIdleMax     = 3600

	// KeyTerminalIdle: globale Rückkehrzeit in Sekunden.
	KeyTerminalIdle = "terminal.idle_seconds"
)

var errTerminalIdle = errors.New("Rückkehrzeit muss zwischen 30 und 3600 Sekunden liegen")

// parseTerminalIdle prüft die Eingabe einer Rückkehrzeit (Sekunden).
func parseTerminalIdle(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < terminalIdleMin || n > terminalIdleMax {
		return 0, errTerminalIdle
	}
	return n, nil
}

// globalTerminalIdle liefert die globale Rückkehrzeit (ungültig/fehlend = Standard).
func (h *Handler) globalTerminalIdle(ctx context.Context) int {
	if h.db == nil {
		return terminalIdleDefault
	}
	n := appsettings.GetInt(ctx, h.db, KeyTerminalIdle, terminalIdleDefault)
	if n < terminalIdleMin || n > terminalIdleMax {
		return terminalIdleDefault
	}
	return n
}

// terminalIdleSeconds: Wert des Terminal-Benutzers, sonst globaler Wert, sonst 120.
func (h *Handler) terminalIdleSeconds(ctx context.Context, userID string) int {
	if h.db == nil {
		return terminalIdleDefault
	}
	var n *int
	if userID != "" {
		_ = h.db.QueryRow(ctx, `SELECT idle_seconds FROM terminal_settings WHERE user_id = $1::uuid`, userID).Scan(&n)
	}
	if n != nil && *n >= terminalIdleMin && *n <= terminalIdleMax {
		return *n
	}
	return h.globalTerminalIdle(ctx)
}

// terminalIdleField liefert den eigenen Wert des Terminal-Benutzers für das Formular ("" = global).
func (h *Handler) terminalIdleField(ctx context.Context, userID string) string {
	var n *int
	_ = h.db.QueryRow(ctx, `SELECT idle_seconds FROM terminal_settings WHERE user_id = $1::uuid`, userID).Scan(&n)
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

// saveTerminalIdle speichert die Rückkehrzeit eines System-Benutzers
// (leer = globalen Wert verwenden). Bei Nicht-System-Benutzern passiert nichts.
func (h *Handler) saveTerminalIdle(ctx context.Context, userID, raw string) error {
	raw = strings.TrimSpace(raw)
	var val any
	if raw != "" {
		n, err := parseTerminalIdle(raw)
		if err != nil {
			return err
		}
		val = n
	}
	_, err := h.db.Exec(ctx, `
		INSERT INTO terminal_settings (user_id, idle_seconds, updated_at)
		SELECT id, $2, NOW() FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO UPDATE SET idle_seconds = EXCLUDED.idle_seconds, updated_at = NOW()`, userID, val)
	return err
}

// TerminalSettingsWeb: POST /core/settings/terminal – globale Rückkehrzeit.
func (h *Handler) TerminalSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	n, err := parseTerminalIdle(r.FormValue("idle_seconds"))
	if err != nil {
		http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if err := h.setUpdateSetting(r.Context(), KeyTerminalIdle, strconv.Itoa(n)); err != nil {
		http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape("Rückkehrzeit des Terminals gespeichert"), http.StatusSeeOther)
}

func (h *Handler) isSystemUser(ctx context.Context, userID string) bool {
	var sys bool
	_ = h.db.QueryRow(ctx, `SELECT is_system_user FROM users WHERE id = $1::uuid`, userID).Scan(&sys)
	return sys
}
