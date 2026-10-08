package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"pdh/internal/core/users"
)

// Passwortwechsel-Zwang und Richtlinie (users/password_policy.go).
//
// Wer sich mit Passwort anmeldet und wechseln muss (Erstanmeldung, Passwort
// vom Administrator gesetzt, Wechsel verlangt oder Passwort abgelaufen),
// bekommt noch KEINE Sitzung: PDH setzt nur ein zweckgebundenes Token
// (scope "pwchange", 15 Minuten) und zeigt /login/change. Erst mit dem neuen
// Passwort entsteht die normale Anmeldung. Anmeldung per Karte, Microsoft
// oder Nextcloud braucht kein Passwort und bleibt unberuehrt; Systemnutzer
// (Terminals) sind ausgenommen.

const (
	pwChangeCookie = "pdh_pwchange"
	pwChangeScope  = "pwchange"
	pwChangeTTL    = 15 * time.Minute
)

// passwordChangeDue: muss dieser Benutzer bei der Passwort-Anmeldung wechseln?
func (h *Handler) passwordChangeDue(r *http.Request, u *users.User) (bool, string) {
	if u == nil || u.IsSystemUser || h.db == nil {
		return false, ""
	}
	st := h.users.PasswordState(r.Context(), u.ID)
	switch {
	case st.MustChange:
		return true, "Bitte vergib jetzt ein eigenes Passwort – das bisherige wurde vom Administrator vergeben oder der Wechsel wurde verlangt."
	case st.Expired:
		return true, "Dein Passwort ist abgelaufen. Bitte vergib ein neues."
	}
	return false, ""
}

// startPasswordChange: statt der Sitzung nur das Wechsel-Token setzen.
func (h *Handler) startPasswordChange(w http.ResponseWriter, r *http.Request, u *users.User, next string) {
	tok, err := h.users.IssueToken(u, pwChangeTTL, map[string]interface{}{"scope": pwChangeScope})
	if err != nil {
		h.render(w, "login", h.loginDataFor(r, "Anmeldung fehlgeschlagen"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: pwChangeCookie, Value: tok, Path: "/login", MaxAge: int(pwChangeTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode})
	target := "/login/change"
	if next != "" && next != "/" {
		target += "?next=" + url.QueryEscape(next)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// pwChangeUser: Benutzer aus dem Wechsel-Token (nil = abgelaufen/ungueltig).
func (h *Handler) pwChangeUser(r *http.Request) *users.User {
	c, err := r.Cookie(pwChangeCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	tok, err := jwt.Parse(c.Value, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || !tok.Valid {
		return nil
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	if scope, _ := claims["scope"].(string); scope != pwChangeScope {
		return nil
	}
	sub, _ := claims["sub"].(string)
	u, err := h.users.GetByID(r.Context(), sub)
	if err != nil || !u.Active {
		return nil
	}
	return u
}

func (h *Handler) passwordRules(r *http.Request, lang string) []string {
	if h.db == nil {
		return nil
	}
	var out []string
	for _, rule := range h.users.Policy(r.Context()).Rules() {
		out = append(out, tr(lang, rule))
	}
	return out
}

// LoginChangePage: GET /login/change – Pflichtwechsel.
func (h *Handler) LoginChangePage(w http.ResponseWriter, r *http.Request) {
	u := h.pwChangeUser(r)
	if u == nil {
		h.render(w, "login", h.loginDataFor(r, "Die Anmeldung ist abgelaufen. Bitte melde dich erneut an."))
		return
	}
	d := h.loginDataFor(r, "")
	d.Mode = "change"
	d.Next = loginNext(r.URL.Query().Get("next"))
	_, d.Notice = h.passwordChangeDue(r, u)
	d.Notice = tr(d.Lang, d.Notice)
	d.Rules = h.passwordRules(r, d.Lang)
	h.render(w, "login", d)
}

// LoginChangePost: POST /login/change – neues Passwort, danach normale Anmeldung.
func (h *Handler) LoginChangePost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := h.pwChangeUser(r)
	if u == nil {
		h.render(w, "login", h.loginDataFor(r, "Die Anmeldung ist abgelaufen. Bitte melde dich erneut an."))
		return
	}
	d := h.loginDataFor(r, "")
	d.Mode = "change"
	d.Next = loginNext(r.FormValue("next"))
	d.Rules = h.passwordRules(r, d.Lang)
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	pw := r.FormValue("password")
	if pw != r.FormValue("password2") {
		d.Error = tr(d.Lang, "Die beiden Passwörter stimmen nicht überein.")
		h.render(w, "login", d)
		return
	}
	if users.CheckPassword(u, pw) {
		d.Error = tr(d.Lang, "Das neue Passwort muss sich vom bisherigen unterscheiden.")
		h.render(w, "login", d)
		return
	}
	if err := h.users.SetPassword(r.Context(), u.ID, pw); err != nil {
		d.Error = tr(d.Lang, err.Error())
		h.render(w, "login", d)
		return
	}
	token, err := h.users.SessionToken(u)
	if err != nil {
		h.render(w, "login", h.loginDataFor(r, "Anmeldung fehlgeschlagen"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: pwChangeCookie, Value: "", Path: "/login", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: u.ID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	authLog(r, true, "passwort-pflichtwechsel", "", u.ID, name, "")
	http.Redirect(w, r, d.Next, http.StatusFound)
}

// UserRequirePasswordChangeWeb: POST /users/{id}/password-change – Wechsel bei der
// naechsten Anmeldung verlangen (oder mit on=0 erlassen). Nur Benutzerverwaltung.
func (h *Handler) UserRequirePasswordChangeWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.rbac.HasPermissionForUser(getUser(r).ID, string(getUser(r).Role), "system.manage_users") ||
		(target.ID != getUser(r).ID && !h.outranksRole(r, string(target.Role))) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	on := r.FormValue("on") != "0"
	if err := h.users.SetMustChange(r.Context(), id, on); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg := "Passwortwechsel bei der nächsten Anmeldung erlassen"
	if on {
		msg = "Passwortwechsel bei der nächsten Anmeldung verlangt"
	}
	h.addHistory(r.Context(), "user", id, "update", "must_change_password", "", strconv.FormatBool(on), msg, getUser(r).ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> ` + esc(msg) + `</span>`))
}

// PasswordPolicySaveWeb: POST /admin/server-config/password-policy
func (h *Handler) PasswordPolicySaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	num := func(k string) int { n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(k))); return n }
	on := func(k string) bool { return r.FormValue(k) == "on" }
	p := users.PasswordPolicy{
		MinLength: num("min_length"), MaxAgeDays: num("max_age_days"), History: num("history"),
		RequireUpper: on("require_upper"), RequireLower: on("require_lower"), RequireDigit: on("require_digit"), RequireSpecial: on("require_special"),
		ChangeOnFirstLogin: on("change_on_first_login"), ChangeAfterAdminReset: on("change_after_admin_reset"),
	}
	err := h.users.SavePolicy(r.Context(), p)
	if err == nil {
		componentLog("auth").Info().Str("user", getUser(r).ID).Interface("richtlinie", p.Normalize()).Msg("passwort-richtlinie geaendert")
		if r.FormValue("expire_all") == "on" {
			// alle Konten mit Passwort (ausser Systemnutzern) muessen beim naechsten Mal wechseln
			_, err = h.db.Exec(r.Context(), `UPDATE users SET must_change_password = true WHERE active AND NOT is_system_user AND COALESCE(password_hash, '') <> ''`)
		}
	}
	serverConfigRedirect(w, r, "passwords", "Passwort-Richtlinie gespeichert", err)
}

// passwordHint: Regeln fuer Passwortfelder; admin = vom Administrator gesetzt
// (Benutzerstamm), first = beim Anlegen.
func (h *Handler) passwordHint(r *http.Request, admin, first bool) string {
	if h.db == nil {
		return ""
	}
	p := h.users.Policy(r.Context())
	s := "Richtlinie: " + strings.Join(p.Rules(), ", ") + "."
	if admin && ((first && p.ChangeOnFirstLogin) || (!first && p.ChangeAfterAdminReset)) {
		s += " Die Person muss es bei der nächsten Anmeldung ändern."
	}
	return s
}
