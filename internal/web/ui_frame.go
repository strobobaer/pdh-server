package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Neuer Rahmen (0.90.0 Teil 2): Jeder Benutzer wählt im Benutzermenü klassisch oder
// neu, ohne eigene Wahl gilt die Firmenvorgabe (Seite „Gestaltung“, Recht
// system.ui_defaults). Bis 0.90.0 erzwingt die Firma nichts (Entwurf 10.7).

const keyDefaultUIFrame = "defaults.ui_frame"

// uiFrameChoice: Formularwert des Schalters → gespeicherter Wert ('' = wie Firma).
func uiFrameChoice(v string) (string, bool) {
	switch v {
	case "", "default":
		return "", true
	case "classic", "new":
		return v, true
	}
	return "", false
}

// frameTemplateName: Wurzelvorlage für die Seitendaten – frame.gohtml, wenn die
// eingebetteten BaseData den neuen Rahmen verlangen, sonst base.gohtml.
func frameTemplateName(data any) string {
	if f, ok := data.(interface{ UseNewFrame() bool }); ok && f.UseNewFrame() {
		return "frame.gohtml"
	}
	return "base.gohtml"
}

// UseNewFrame: Seite im neuen Rahmen zeigen (h.render, frameTemplateName).
func (b BaseData) UseNewFrame() bool { return b.NewFrame }

// frameFor: neuer Rahmen? Eigene Wahl vor Firmenvorgabe (fehlt = klassisch).
func (h *Handler) frameFor(ctx context.Context, userFrame string) bool {
	switch userFrame {
	case "new":
		return true
	case "classic":
		return false
	}
	return h.firmUIFrame(ctx) == "new"
}

// firmUIFrame: Firmenvorgabe „classic“ oder „new“ (aus dem Zwischenspeicher der Vorgaben).
func (h *Handler) firmUIFrame(ctx context.Context) string {
	if h.db == nil { // nur Tests ohne Datenbank
		return h.getUpdateSetting(ctx, keyDefaultUIFrame, "classic")
	}
	h.loadDefaults(ctx)
	defaultsCache.RLock()
	defer defaultsCache.RUnlock()
	return defaultsCache.uiFrame
}

// userUIFrame: eigene Wahl des Benutzers ('' = wie Firma).
func (h *Handler) userUIFrame(ctx context.Context, uid string) string {
	if uid == "" {
		return ""
	}
	if h.db == nil { // nur Tests ohne Datenbank
		return h.getUpdateSetting(ctx, "users."+uid+".ui_frame", "")
	}
	if row := userRowFrom(ctx, uid); row != nil {
		return row.uiFrame
	}
	var v string
	_ = h.db.QueryRow(ctx, `SELECT ui_frame FROM users WHERE id = $1::uuid`, uid).Scan(&v)
	return v
}

func (h *Handler) setUserUIFrame(ctx context.Context, uid, v string) error {
	if h.db == nil {
		return h.setUpdateSetting(ctx, "users."+uid+".ui_frame", v)
	}
	_, err := h.db.Exec(ctx, `UPDATE users SET ui_frame = $2 WHERE id = $1::uuid`, uid, v)
	return err
}

// localPath: s ist ein Pfad dieser Adresse („/…“ samt Abfrage) – ohne Backslash und
// Steuerzeichen, die Browser zu „//fremd“ umdeuten, ohne Schema und ohne Rechner.
func localPath(s string) (string, bool) {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsRune(s, '\\') {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return "", false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		return "", false
	}
	return s, true
}

// backTo: zurück auf die Seite, von der das Formular kam – nur Pfad und Abfrage der
// eigenen Adresse (kein Sprung auf fremde Seiten).
func backTo(r *http.Request) string {
	if b, ok := localPath(r.FormValue("back")); ok {
		return b // vom Formular mitgegeben (location.pathname + location.search)
	}
	u, err := url.Parse(r.Referer())
	if err != nil || (u.Host != "" && u.Host != r.Host) {
		return "/"
	}
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	if b, ok := localPath(p); ok {
		return b
	}
	return "/"
}

// UIFrameSaveWeb: POST /account/ui-frame – eigene Wahl (Formular frame=default|classic|new).
func (h *Handler) UIFrameSaveWeb(w http.ResponseWriter, r *http.Request) {
	uid := getUser(r).ID
	if uid == "" {
		http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
		return
	}
	v, ok := uiFrameChoice(r.FormValue("frame"))
	if !ok {
		http.Error(w, "unbekannte Oberfläche", http.StatusBadRequest)
		return
	}
	if err := h.setUserUIFrame(r.Context(), uid, v); err != nil {
		http.Error(w, "nicht gespeichert", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, backTo(r), http.StatusSeeOther)
}

// UIFrameDefaultSaveWeb: POST /admin/gestaltung/frame – Firmenvorgabe (frame=classic|new).
func (h *Handler) UIFrameDefaultSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permUIDefaults) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	v := r.FormValue("frame")
	if v != "classic" && v != "new" {
		http.Error(w, "unbekannte Oberfläche", http.StatusBadRequest)
		return
	}
	if err := h.setUpdateSetting(r.Context(), keyDefaultUIFrame, v); err != nil {
		http.Error(w, "nicht gespeichert", http.StatusInternalServerError)
		return
	}
	resetDefaultsCache()
	http.Redirect(w, r, "/admin/gestaltung?saved=frame", http.StatusSeeOther)
}
