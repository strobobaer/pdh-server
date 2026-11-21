package web

// Gemeinsame Bausteine der Firmenstammdaten (0.82.18): Anschrift und Pflichtangaben in einer
// Zeile, Ansprechpartner aus dem Benutzerstamm, Fußzeile für Mails. Leere Felder entfallen
// samt Trenner; alle Werte werden als Text maskiert.

import (
	"bytes"
	"context"
	"html/template"
	"strings"
)

// joinDot verbindet die nicht leeren Teile mit „ · “.
func joinDot(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// companyAddressLine: „Straße · PLZ Ort“, das Land nur, wenn es nicht Deutschland ist.
func companyAddressLine(p companyProfile) string {
	country := p.Country
	if strings.EqualFold(strings.TrimSpace(country), "Deutschland") {
		country = ""
	}
	return joinDot(p.Street, strings.TrimSpace(p.PostalCode+" "+p.City), country)
}

// companyLegalLine: Pflichtangaben für geschäftliche Mails und Briefe in einer Zeile.
func companyLegalLine(p companyProfile) string {
	label := func(l, v string) string {
		if v = strings.TrimSpace(v); v == "" {
			return ""
		}
		return l + ": " + v
	}
	return joinDot(
		label("Sitz", p.RegisteredOffice),
		label("Registergericht", strings.TrimSpace(p.RegisterCourt+" "+p.RegisterNo)),
		label("Geschäftsführung", p.Management),
		label("Vorsitz Aufsichtsrat", p.SupervisoryChair),
		label("USt-IdNr.", p.VatID),
	)
}

// contactPerson: Ansprechpartner aus dem Benutzerstamm der auslösenden Person.
type contactPerson struct{ Name, Title, Phone, Mobile, Email string }

// contactPhone: Telefon der Person; fehlt es, die Firmennummer mit der Durchwahl dahinter
// (die Durchwahl allein ist nach außen keine vollständige Nummer).
func contactPhone(userPhone, userInternal, companyPhone string) string {
	userPhone, userInternal, companyPhone = strings.TrimSpace(userPhone), strings.TrimSpace(userInternal), strings.TrimSpace(companyPhone)
	switch {
	case userPhone != "":
		return userPhone
	case userInternal != "" && companyPhone != "":
		return companyPhone + "-" + userInternal
	default:
		return userInternal
	}
}

// contactPersonFor liest den Ansprechpartner; nil ohne ID, ohne Datenbank oder bei Fehler.
func (h *Handler) contactPersonFor(ctx context.Context, userID string) *contactPerson {
	if h.db == nil || strings.TrimSpace(userID) == "" {
		return nil
	}
	var first, last, title, phone, internal, mobile, email string
	if err := h.db.QueryRow(ctx, `SELECT COALESCE(first_name,''), COALESCE(last_name,''), COALESCE(job_title,''),
		COALESCE(phone,''), COALESCE(phone_internal,''), COALESCE(phone_mobile,''), COALESCE(email,'')
		FROM users WHERE id::text = $1`, userID).Scan(&first, &last, &title, &phone, &internal, &mobile, &email); err != nil {
		return nil
	}
	name := strings.TrimSpace(first + " " + last)
	if name == "" {
		return nil
	}
	return &contactPerson{Name: name, Title: strings.TrimSpace(title), Phone: contactPhone(phone, internal, h.company(ctx).Phone),
		Mobile: strings.TrimSpace(mobile), Email: strings.TrimSpace(email)}
}

// companyPrint: Firma im Druckkopf jeder Seite (base.gohtml, .print-brand).
type companyPrint struct{ Name, Address string }

func companyPrintFor(p companyProfile) companyPrint {
	if strings.TrimSpace(p.Name) == "" {
		return companyPrint{}
	}
	return companyPrint{Name: p.Name, Address: companyAddressLine(p)}
}

// contactLine: Ansprechpartner in einer Zeile (Wartungsprotokoll).
func contactLine(c *contactPerson) string {
	if c == nil {
		return ""
	}
	tel := ""
	if c.Phone != "" {
		tel = "Tel. " + c.Phone
	}
	return joinDot(c.Name, c.Title, tel, c.Email)
}

var companyFooterTmpl = template.Must(template.New("company-footer").Parse(
	`<div style="margin-top:20px;padding-top:10px;border-top:1px solid #ddd;color:#888;font-size:12px;line-height:1.5">` +
		`{{with .Person}}<div><b>Ansprechpartner:</b> {{.Name}}{{with .Title}} · {{.}}{{end}}{{with .Phone}} · Tel. {{.}}{{end}}{{with .Mobile}} · Mobil {{.}}{{end}}{{with .Email}} · {{.}}{{end}}</div>` +
		`{{else}}{{with .Contact}}<div><b>Kontakt:</b> {{.}}</div>{{end}}{{end}}` +
		`<div><b>{{.Name}}</b>{{with .Addition}} · {{.}}{{end}}</div>` +
		`{{with .Address}}<div>{{.}}</div>{{end}}` +
		`{{with .Reach}}<div>{{.}}</div>{{end}}` +
		`{{with .Legal}}<div>{{.}}</div>{{end}}` +
		`</div>`))

// companyFooterHTML: Fußzeile für Mails; leer ohne Firmennamen (dann wie vor 0.82.18).
func companyFooterHTML(p companyProfile, c *contactPerson) template.HTML {
	if strings.TrimSpace(p.Name) == "" {
		return ""
	}
	tel := ""
	if p.Phone != "" {
		tel = "Tel. " + p.Phone
	}
	data := map[string]any{
		"Person":   c,
		"Contact":  joinDot(tel, p.Email),
		"Name":     p.Name,
		"Addition": p.NameAddition,
		"Address":  companyAddressLine(p),
		"Reach":    joinDot(tel, p.Email, p.Website),
		"Legal":    companyLegalLine(p),
	}
	if c == nil {
		data["Person"] = nil
	}
	var b bytes.Buffer
	if err := companyFooterTmpl.Execute(&b, data); err != nil {
		return ""
	}
	return template.HTML(b.String()) //nolint:gosec // Werte vom Template maskiert
}
