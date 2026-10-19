package web

import (
	"context"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Benutzerstamm → Übersicht → E-Mail: Testmail an die Person und Schalter
// „Statusmeldungen auch per E-Mail“ (users.mail_notifications, migrations/124).
// Die Meldungen selbst kommen von PDH-System (systemNotify, pdh_system.go).

var mailBoldRe = regexp.MustCompile(`\*\*(.+?)\*\*`)

// systemMailContent: Chat-Hinweis (Markdown-light) als Betreff und HTML.
// Zeilen, die mit „/“ beginnen, sind Links auf den Vorgang.
func systemMailContent(body, baseURL, app string) (subject, htmlBody string) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	subject = strings.TrimSpace(strings.ReplaceAll(lines[0], "**", ""))
	if r := []rune(subject); len(r) > 120 {
		subject = string(r[:119]) + "…"
	}
	if app != "" {
		subject = app + " – " + subject
	}
	var b strings.Builder
	b.WriteString(`<div style="font-family:Arial,sans-serif;font-size:14px;line-height:1.5;color:#222">`)
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "/") && !strings.ContainsAny(l, " \t") {
			href := baseURL + l
			b.WriteString(`<p><a href="` + html.EscapeString(href) + `" style="color:#2563eb">Im PDH öffnen</a></p>`)
			continue
		}
		b.WriteString(mailBoldRe.ReplaceAllString(html.EscapeString(l), "<b>$1</b>") + "<br>")
	}
	b.WriteString(`<p style="color:#888;font-size:12px;margin-top:16px">Automatische Statusmeldung. Abschalten: im PDH unter Mein Konto → Übersicht → „Statusmeldungen auch per E-Mail“.</p></div>`)
	return subject, b.String()
}

// mailSystemNotify: Statusmeldung zusätzlich per E-Mail an alle Empfänger mit
// eingeschaltetem Schalter. Läuft im Hintergrund, damit Speichern nicht wartet.
func (h *Handler) mailSystemNotify(ctx context.Context, userIDs []string, body string) {
	if h.db == nil || len(userIDs) == 0 || !h.mailConfigured() {
		return
	}
	rows, err := h.db.Query(ctx, `SELECT email FROM users WHERE id::text = ANY($1) AND active AND NOT is_bot AND mail_notifications AND COALESCE(email, '') <> ''`, userIDs)
	if err != nil {
		componentLog("benachrichtigung").Error().Err(err).Msg("e-mail-empfänger für statusmeldung")
		return
	}
	var to []string
	for rows.Next() {
		var e string
		if rows.Scan(&e) == nil {
			to = append(to, e)
		}
	}
	rows.Close()
	if len(to) == 0 {
		return
	}
	subject, htmlBody := systemMailContent(body, h.passwordResetBaseURL(), h.branding().AppName)
	go func() {
		// Einzeln senden: Empfänger sehen einander nicht
		for _, addr := range to {
			if err := h.sendMail([]string{addr}, subject, htmlBody); err != nil {
				componentLog("benachrichtigung").Warn().Err(err).Str("to", addr).Msg("statusmeldung per e-mail")
			}
		}
	}()
}

func mailResult(w http.ResponseWriter, ok bool, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	color, icon := "var(--green)", "ti-check"
	if !ok {
		color, icon = "var(--red)", "ti-alert-circle"
	}
	_, _ = w.Write([]byte(`<span style="color:` + color + `;font-size:12px"><i class="ti ` + icon + `"></i> ` + html.EscapeString(msg) + `</span>`))
}

// mailTarget: Person laden und prüfen, ob der Angemeldete ihre E-Mail-Einstellungen ändern darf.
func (h *Handler) mailTarget(w http.ResponseWriter, r *http.Request) (id, email, name string, ok bool) {
	id = chi.URLParam(r, "id")
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return "", "", "", false
	}
	_, editMaster, _, self := h.userAccess(r, id, string(u.Role))
	if !self && !editMaster {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return "", "", "", false
	}
	return id, strings.TrimSpace(u.Email), strings.TrimSpace(u.FirstName + " " + u.LastName), true
}

// UserTestMailWeb: POST /users/{id}/test-mail (htmx)
func (h *Handler) UserTestMailWeb(w http.ResponseWriter, r *http.Request) {
	id, email, name, ok := h.mailTarget(w, r)
	if !ok {
		return
	}
	if email == "" {
		mailResult(w, false, "Keine E-Mail-Adresse im Benutzerstamm")
		return
	}
	if !h.mailConfigured() {
		mailResult(w, false, "E-Mail-Versand ist nicht eingerichtet (Server-Einstellungen → E-Mail)")
		return
	}
	app := h.branding().AppName
	if app == "" {
		app = "PDH"
	}
	body := `<div style="font-family:Arial,sans-serif;font-size:14px;color:#222"><p>Hallo ` + html.EscapeString(name) + `,</p>` +
		`<p>dies ist eine Testmail aus ` + html.EscapeString(app) + `. Der E-Mail-Versand an diese Adresse funktioniert.</p>` +
		`<p style="color:#888;font-size:12px">Gesendet am ` + time.Now().Format("02.01.2006 15:04") + ` von ` + html.EscapeString(getUser(r).FirstName+" "+getUser(r).LastName) + `.</p></div>`
	if err := h.sendMail([]string{email}, app+" – Testmail", body); err != nil {
		componentLog("benachrichtigung").Warn().Err(err).Str("user", id).Msg("testmail")
		mailResult(w, false, "Nicht gesendet: "+err.Error())
		return
	}
	h.addHistory(r.Context(), "user", id, "update", "", "", "", "Testmail an "+email+" gesendet", getUser(r).ID)
	mailResult(w, true, "Testmail an "+email+" gesendet")
}

// UserMailNotificationsWeb: POST /users/{id}/mail-notifications (on=1|0, htmx)
func (h *Handler) UserMailNotificationsWeb(w http.ResponseWriter, r *http.Request) {
	id, _, _, ok := h.mailTarget(w, r)
	if !ok {
		return
	}
	on := r.FormValue("on") == "1"
	if _, err := h.db.Exec(r.Context(), `UPDATE users SET mail_notifications = $2 WHERE id = $1::uuid`, id, on); err != nil {
		mailResult(w, false, friendlyDBError(err))
		return
	}
	msg := "Statusmeldungen per E-Mail ausgeschaltet"
	if on {
		msg = "Statusmeldungen per E-Mail eingeschaltet"
	}
	h.addHistory(r.Context(), "user", id, "update", "mail_notifications", "", "", msg, getUser(r).ID)
	mailResult(w, true, msg)
}
