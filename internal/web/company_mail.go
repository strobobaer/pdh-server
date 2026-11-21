package web

// Mails mit Firmenstammdaten (0.82.18): Absendername = Firma (wenn PDH_SMTP_FROM keinen
// eigenen Namen hat) und Fußzeile mit Ansprechpartner, Anschrift und Pflichtangaben.

import (
	"context"
	"html/template"
	"net/mail"
	"strings"
)

// mailFromHeader: Absender für die SMTP-Kopfzeile. Ein eingestellter Anzeigename geht vor;
// sonst der Firmenname (RFC 2047 kodiert, wenn nötig). Ohne Firmennamen unverändert.
func mailFromHeader(from, company string) string {
	company = strings.TrimSpace(company)
	a, err := mail.ParseAddress(from)
	if err != nil || a.Name != "" || company == "" {
		return from
	}
	return (&mail.Address{Name: company, Address: a.Address}).String()
}

// mailAppendFooter setzt die Fußzeile vor </body>, sonst vor das letzte </div> am Ende,
// sonst ans Ende.
func mailAppendFooter(html string, footer template.HTML) string {
	if footer == "" {
		return html
	}
	f := string(footer)
	if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
		return html[:i] + f + html[i:]
	}
	trimmed := strings.TrimRight(html, " \r\n\t")
	if strings.HasSuffix(strings.ToLower(trimmed), "</div>") {
		i := len(trimmed) - len("</div>")
		return trimmed[:i] + f + trimmed[i:] + html[len(trimmed):]
	}
	return html + f
}

// sendMailBy verschickt wie sendMail, mit Fußzeile der Firma; actorID ist die auslösende
// Person (Ansprechpartner), leer = allgemeiner Firmenkontakt.
func (h *Handler) sendMailBy(to []string, subject, html, actorID string) error {
	ctx := context.Background()
	footer := companyFooterHTML(h.company(ctx), h.contactPersonFor(ctx, actorID))
	return h.sendMailRaw(to, subject, mailAppendFooter(html, footer))
}
