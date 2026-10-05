package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"pdh/internal/core/users"
)

// Passwort vergessen / zuruecksetzen / aendern.
//
// Ablauf "vergessen": Anmeldeseite → /login/forgot (E-Mail oder Benutzername)
// → PDH schickt einen einmaligen Link (1 Stunde gueltig) an die hinterlegte
// E-Mail-Adresse → /login/reset?token=… → neues Passwort. Gespeichert wird
// nur der SHA-256-Hash des Tokens. Die Antwort ist immer dieselbe, damit
// niemand ausprobieren kann, welche Konten es gibt. Der Link nutzt nur die
// eingestellte oeffentliche Adresse (PDH_PUBLIC_URL), nie den Host der
// Anfrage – sonst koennte ein Angreifer Links auf seinen Server umbiegen.

const (
	passwordResetTTL     = time.Hour
	passwordResetPerUser = 3  // Links je Konto und Stunde
	passwordResetPerIP   = 30 // Anfragen je Adresse und Stunde (hinter einem Proxy teilen sich viele eine Adresse)
)

// passwordResetBaseURL: oeffentliche Adresse fuer den Link ("" = nicht eingerichtet).
func (h *Handler) passwordResetBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(h.mailCfg.PublicURL), "/")
}

func (h *Handler) passwordResetAvailable() bool {
	return h.mailConfigured() && h.passwordResetBaseURL() != ""
}

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *Handler) LoginForgotPage(w http.ResponseWriter, r *http.Request) {
	d := h.loginDataFor(r, "")
	d.Mode = "forgot"
	if !h.passwordResetAvailable() {
		d.Error = tr(d.Lang, "Das Zurücksetzen per E-Mail ist auf diesem Server nicht eingerichtet. Bitte wende dich an einen Administrator – er kann dir ein neues Passwort vergeben.")
	}
	h.render(w, "login", d)
}

func (h *Handler) LoginForgotPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	d := h.loginDataFor(r, "")
	d.Mode = "forgot"
	if !h.passwordResetAvailable() {
		d.Error = tr(d.Lang, "Das Zurücksetzen per E-Mail ist auf diesem Server nicht eingerichtet. Bitte wende dich an einen Administrator – er kann dir ein neues Passwort vergeben.")
		h.render(w, "login", d)
		return
	}
	login := strings.TrimSpace(r.FormValue("email"))
	if login == "" || len(login) > 255 {
		d.Error = tr(d.Lang, "Bitte E-Mail oder Benutzername eingeben.")
		h.render(w, "login", d)
		return
	}
	// Immer dieselbe Antwort – ob es das Konto gibt, verraet die Seite nicht.
	d.Notice = tr(d.Lang, "Wenn es ein Konto mit diesen Angaben und einer hinterlegten E-Mail-Adresse gibt, haben wir dir einen Link zum Zurücksetzen geschickt. Er gilt eine Stunde. Schau auch im Spam-Ordner nach.")
	ip := clientIP(r)
	lang := d.Lang
	go h.sendPasswordReset(login, ip, lang)
	h.render(w, "login", d)
}

// sendPasswordReset laeuft im Hintergrund, damit die Antwortzeit nichts verraet.
func (h *Handler) sendPasswordReset(login, ip, lang string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ev := func(ok bool, userID, reason string) {
		e := componentLog("auth").Info()
		if !ok {
			e = componentLog("auth").Warn()
		}
		e.Str("verfahren", "passwort-vergessen").Str("login", login).Str("ip", ip).Str("user", userID).Str("grund", reason).Msg("passwort zuruecksetzen angefordert")
	}
	var ipCount int
	if err := h.db.QueryRow(ctx, `SELECT COUNT(*) FROM password_resets WHERE requested_ip = $1 AND created_at > NOW() - INTERVAL '1 hour'`, ip).Scan(&ipCount); err != nil {
		ev(false, "", err.Error())
		return
	}
	if ipCount >= passwordResetPerIP {
		ev(false, "", "zu viele anfragen von dieser adresse")
		return
	}
	u, err := h.users.GetByIdentifier(ctx, login)
	if err != nil {
		ev(false, "", "kein passendes konto")
		return
	}
	if u.IsSystemUser || strings.TrimSpace(u.Email) == "" {
		ev(false, u.ID, "systemnutzer oder keine e-mail-adresse")
		return
	}
	var userCount int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM password_resets WHERE user_id = $1::uuid AND created_at > NOW() - INTERVAL '1 hour'`, u.ID).Scan(&userCount)
	if userCount >= passwordResetPerUser {
		ev(false, u.ID, "zu viele links fuer dieses konto")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		ev(false, u.ID, err.Error())
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := h.db.Exec(ctx, `INSERT INTO password_resets (user_id, token_hash, expires_at, requested_ip) VALUES ($1::uuid, $2, $3, $4)`,
		u.ID, hashResetToken(token), time.Now().Add(passwordResetTTL), ip); err != nil {
		ev(false, u.ID, err.Error())
		return
	}
	link := h.passwordResetBaseURL() + "/login/reset?token=" + token
	app := firstNonEmpty(h.branding().AppName, "PDH")
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	esc := template.HTMLEscapeString
	body := fmt.Sprintf(`<div style="font-family:Arial,Helvetica,sans-serif;font-size:14px;color:#111;max-width:520px">
<p>%s %s,</p>
<p>%s</p>
<p><a href="%s" style="display:inline-block;background:#4f6ef7;color:#fff;padding:10px 18px;border-radius:8px;text-decoration:none">%s</a></p>
<p style="font-size:12px;color:#555">%s<br><span style="word-break:break-all">%s</span></p>
<p style="font-size:12px;color:#555">%s</p>
</div>`,
		esc(tr(lang, "Hallo")), esc(name),
		esc(fmt.Sprintf(tr(lang, "für dein Konto bei %s wurde ein neues Passwort angefordert. Über den Link vergibst du ein neues Passwort. Er gilt eine Stunde und nur einmal."), app)),
		esc(link), esc(tr(lang, "Neues Passwort vergeben")),
		esc(tr(lang, "Funktioniert der Knopf nicht, kopiere diese Adresse in den Browser:")), esc(link),
		esc(tr(lang, "Du hast nichts angefordert? Dann ignoriere diese E-Mail – dein Passwort bleibt unverändert.")))
	if err := h.sendMail([]string{u.Email}, app+" – "+tr(lang, "Passwort zurücksetzen"), body); err != nil {
		ev(false, u.ID, "e-mail: "+err.Error())
		return
	}
	ev(true, u.ID, "")
}

// resetTokenUser: gueltiger, unbenutzter Link → Benutzer-ID.
func (h *Handler) resetTokenUser(ctx context.Context, token string) (string, error) {
	if token == "" || len(token) > 100 {
		return "", errors.New("ungueltig")
	}
	var userID string
	err := h.db.QueryRow(ctx, `SELECT pr.user_id::text FROM password_resets pr JOIN users u ON u.id = pr.user_id
		WHERE pr.token_hash = $1 AND pr.used_at IS NULL AND pr.expires_at > NOW() AND u.active = true`, hashResetToken(token)).Scan(&userID)
	return userID, err
}

const resetLinkInvalid = "Der Link ist abgelaufen oder wurde schon benutzt. Fordere einfach einen neuen an."

func (h *Handler) LoginResetPage(w http.ResponseWriter, r *http.Request) {
	d := h.loginDataFor(r, "")
	d.Mode = "reset"
	d.Token = r.URL.Query().Get("token")
	if _, err := h.resetTokenUser(r.Context(), d.Token); err != nil {
		d.Mode, d.Token = "forgot", ""
		d.Error = tr(d.Lang, resetLinkInvalid)
	}
	h.render(w, "login", d)
}

func (h *Handler) LoginResetPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ctx := r.Context()
	d := h.loginDataFor(r, "")
	d.Mode = "reset"
	d.Token = r.FormValue("token")
	pw := r.FormValue("password")
	if pw != r.FormValue("password2") {
		d.Error = tr(d.Lang, "Die beiden Passwörter stimmen nicht überein.")
		h.render(w, "login", d)
		return
	}
	if err := users.ValidatePassword(pw); err != nil {
		d.Error = tr(d.Lang, err.Error())
		h.render(w, "login", d)
		return
	}
	// Link verbrauchen (atomar) – erst dann das Passwort setzen
	var userID string
	err := h.db.QueryRow(ctx, `UPDATE password_resets pr SET used_at = NOW() FROM users u
		WHERE u.id = pr.user_id AND u.active = true AND pr.token_hash = $1 AND pr.used_at IS NULL AND pr.expires_at > NOW()
		RETURNING pr.user_id::text`, hashResetToken(d.Token)).Scan(&userID)
	if err != nil {
		d.Mode, d.Token = "forgot", ""
		d.Error = tr(d.Lang, resetLinkInvalid)
		h.render(w, "login", d)
		return
	}
	if err := h.users.SetPassword(ctx, userID, pw); err != nil {
		componentLog("auth").Error().Err(err).Str("user", userID).Msg("passwort zuruecksetzen fehlgeschlagen")
		d.Mode, d.Token = "forgot", ""
		d.Error = tr(d.Lang, "Das Passwort konnte nicht gespeichert werden. Bitte fordere einen neuen Link an.")
		h.render(w, "login", d)
		return
	}
	// weitere offene Links dieses Kontos entwerten
	_, _ = h.db.Exec(ctx, `UPDATE password_resets SET used_at = NOW() WHERE user_id = $1::uuid AND used_at IS NULL`, userID)
	authLog(r, true, "passwort-zurueckgesetzt", "", userID, h.cachedUserName(ctx, userID), "")
	d = h.loginDataFor(r, "")
	d.Notice = tr(d.Lang, "Dein Passwort wurde geändert. Du kannst dich jetzt damit anmelden.")
	h.render(w, "login", d)
}

// AccountPasswordWeb: POST /users/me/password – eigenes Passwort aendern (HTMX).
func (h *Handler) AccountPasswordWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fail := func(msg string) {
		fmt.Fprintf(w, `<span style="color:var(--red)"><i class="ti ti-alert-circle"></i> %s</span>`, template.HTMLEscapeString(msg))
	}
	pw := r.FormValue("password")
	if pw != r.FormValue("password2") {
		fail("Die beiden neuen Passwörter stimmen nicht überein.")
		return
	}
	err := h.users.ChangePassword(r.Context(), u.ID, r.FormValue("current"), pw)
	if err != nil {
		if errors.Is(err, users.ErrWrongPassword) {
			authLog(r, false, "passwort-aendern", "", u.ID, strings.TrimSpace(u.FirstName+" "+u.LastName), "aktuelles passwort falsch")
		}
		fail(err.Error())
		return
	}
	authLog(r, true, "passwort-aendern", "", u.ID, strings.TrimSpace(u.FirstName+" "+u.LastName), "")
	fmt.Fprint(w, `<span style="color:var(--green)"><i class="ti ti-check"></i> Passwort geändert – ab der nächsten Anmeldung gilt das neue.</span>`)
}
