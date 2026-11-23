package web

// Einstellung „Bearbeiten am Terminal“ (Vorgänge): ohne_karte (Eingewöhnung) oder nur_karte.

import "context"

const (
	vorgModusOhneKarte = "ohne_karte"
	vorgModusNurKarte  = "nur_karte"
)

// terminalVorgaengeModus: Betriebsart des Terminal-Benutzers; ohne Zeile ohne_karte.
func (h *Handler) terminalVorgaengeModus(ctx context.Context, termUserID string) string {
	m := vorgModusOhneKarte
	if h.db != nil && termUserID != "" {
		_ = h.db.QueryRow(ctx, `SELECT vorgaenge_modus FROM terminal_settings WHERE user_id = $1::uuid`, termUserID).Scan(&m)
	}
	if m != vorgModusNurKarte {
		return vorgModusOhneKarte
	}
	return m
}

// saveTerminalVorgaengeModus speichert die Betriebsart (Aufrufer prüft Recht und Systembenutzer).
func (h *Handler) saveTerminalVorgaengeModus(ctx context.Context, uid, modus, by string) error {
	if modus != vorgModusNurKarte {
		modus = vorgModusOhneKarte
	}
	old := h.terminalVorgaengeModus(ctx, uid)
	if _, err := h.db.Exec(ctx, `INSERT INTO terminal_settings (user_id) SELECT id FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO NOTHING`, uid); err != nil {
		return err
	}
	if _, err := h.db.Exec(ctx, `UPDATE terminal_settings SET vorgaenge_modus = $2, updated_at = NOW() WHERE user_id = $1::uuid`, uid, modus); err != nil {
		return err
	}
	if old != modus {
		lbl := map[string]string{vorgModusOhneKarte: "ohne Karte (Eingewöhnung)", vorgModusNurKarte: "nur mit Karte"}
		h.addHistory(ctx, "user", uid, "update", "Bearbeiten am Terminal", lbl[old], lbl[modus], "Bearbeiten am Terminal: "+lbl[modus], by)
	}
	return nil
}
