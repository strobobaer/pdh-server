package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Microsoft 365 → E-Mail-Signaturen (/m365?tab=signatures). Signaturen sind
// HTML mit Platzhaltern wie {Telefon}. Ausgerollt werden sie als Exchange-
// Transportregeln (PowerShell-Skript zum Herunterladen), die Exchange mit den
// Entra-Attributen füllt (%%PhoneNumber%% …). Damit dort die PDH-Werte stehen,
// schreibt „Werte nach Microsoft 365 übertragen“ den Benutzerstamm nach Entra
// (nur gefüllte PDH-Felder – leere bleiben, wie sie in Entra sind).

type sigField struct {
	Key      string // Platzhalter {Key}
	Exchange string // Transportregel-Attribut %%Exchange%%
}

// sigFields: Reihenfolge = Knöpfe im Editor.
var sigFields = []sigField{
	{"Vorname", "FirstName"}, {"Nachname", "LastName"}, {"Name", "DisplayName"},
	{"E-Mail", "Email"}, {"Position", "Title"}, {"Abteilung", "Department"},
	{"Telefon", "PhoneNumber"}, {"Mobil", "MobileNumber"}, {"Standort", "Office"}, {"Firma", "Company"},
}

// microsoftSignatureSelect: zusätzliche Entra-Felder im Verzeichnisabgleich.
const microsoftSignatureSelect = "givenName,surname,jobTitle,businessPhones,mobilePhone,officeLocation,companyName"

// m365CompanySetting: Firmenname für {Firma} (app_settings).
const m365CompanySetting = "m365_company_name"

// sigRuleMarker steht unsichtbar in jeder Signatur: Regeln greifen nicht, wenn
// er schon im Text steht (Antworten, zweite Regel).
const sigRuleMarker = "pdhsig"

// sigMaxHTML: Grenze von Exchange für ApplyHtmlDisclaimerText.
const sigMaxHTML = 5000

var sigPlaceholderRe = regexp.MustCompile(`\{([A-Za-z][A-Za-z-]*)\}`)

type mailSignatureRow struct {
	ID, Name, Department, HTML, Updated string
	Active                              bool
	RuleLength                          int
}

func (s mailSignatureRow) TooLong() bool { return s.RuleLength > sigMaxHTML }

// unknownSigPlaceholders: Platzhalter, die es nicht gibt (Tippfehler).
func unknownSigPlaceholders(src string) []string {
	known := map[string]bool{}
	for _, f := range sigFields {
		known[f.Key] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range sigPlaceholderRe.FindAllStringSubmatch(src, -1) {
		if !known[m[1]] && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, "{"+m[1]+"}")
		}
	}
	return out
}

// renderSignature setzt Werte ein (HTML-maskiert) – für die Vorschau.
func renderSignature(src string, values map[string]string) string {
	return sigPlaceholderRe.ReplaceAllStringFunc(src, func(m string) string {
		key := m[1 : len(m)-1]
		if v, ok := values[key]; ok {
			return html.EscapeString(v)
		}
		return m
	})
}

// signatureRuleHTML: Platzhalter → Exchange-Attribute, plus Marker.
func signatureRuleHTML(src string) string {
	ex := map[string]string{}
	for _, f := range sigFields {
		ex[f.Key] = "%%" + f.Exchange + "%%"
	}
	out := sigPlaceholderRe.ReplaceAllStringFunc(src, func(m string) string {
		if v, ok := ex[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
	return out + `<span style="color:#ffffff;font-size:1px">` + sigRuleMarker + `</span>`
}

// signatureValues: Werte einer Person – PDH-Benutzerstamm führt, leere Felder
// kommen aus dem Microsoft-Verzeichnisabgleich.
func (h *Handler) signatureValues(ctx context.Context, userID string) (map[string]string, error) {
	var p, m [9]string
	err := h.db.QueryRow(ctx, `
		SELECT u.first_name, u.last_name, COALESCE(u.email,''), COALESCE(u.job_title,''), COALESCE(u.department,''),
		       COALESCE(u.phone,''), COALESCE(u.phone_mobile,''), COALESCE(u.work_location,''), '',
		       COALESCE(d.given_name,''), COALESCE(d.surname,''), COALESCE(d.email,''), COALESCE(d.job_title,''), COALESCE(d.department,''),
		       COALESCE(d.business_phone,''), COALESCE(d.mobile_phone,''), COALESCE(d.office_location,''), COALESCE(d.company_name,'')
		FROM users u
		LEFT JOIN microsoft_directory_users d ON d.pdh_user_id = u.id AND d.tenant_id = $2
		WHERE u.id = $1::uuid`, userID, h.microsoft.TenantID).Scan(
		&p[0], &p[1], &p[2], &p[3], &p[4], &p[5], &p[6], &p[7], &p[8],
		&m[0], &m[1], &m[2], &m[3], &m[4], &m[5], &m[6], &m[7], &m[8])
	if err != nil {
		return nil, err
	}
	p[8] = h.getUpdateSetting(ctx, m365CompanySetting, "")
	return mergeSignatureValues(p, m), nil
}

// mergeSignatureValues: Reihenfolge Vorname, Nachname, E-Mail, Position,
// Abteilung, Telefon, Mobil, Standort, Firma.
func mergeSignatureValues(pdh, ms [9]string) map[string]string {
	pick := func(i int) string {
		if v := strings.TrimSpace(pdh[i]); v != "" {
			return v
		}
		return strings.TrimSpace(ms[i])
	}
	v := map[string]string{
		"Vorname": pick(0), "Nachname": pick(1), "E-Mail": pick(2), "Position": pick(3),
		"Abteilung": pick(4), "Telefon": pick(5), "Mobil": pick(6), "Standort": pick(7), "Firma": pick(8),
	}
	v["Name"] = strings.TrimSpace(v["Vorname"] + " " + v["Nachname"])
	return v
}

func (h *Handler) loadSignatures(ctx context.Context) []mailSignatureRow {
	rows, err := h.db.Query(ctx, `SELECT id::text, name, department, html, active, updated_at FROM mail_signatures
		ORDER BY (department = ''), department, name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []mailSignatureRow
	for rows.Next() {
		var s mailSignatureRow
		var t time.Time
		if rows.Scan(&s.ID, &s.Name, &s.Department, &s.HTML, &s.Active, &t) == nil {
			s.Updated = t.Local().Format("02.01.2006 15:04")
			s.RuleLength = len(signatureRuleHTML(s.HTML))
			out = append(out, s)
		}
	}
	return out
}

func m365Back(w http.ResponseWriter, r *http.Request, tab, extra, msg string, err error) {
	q := "tab=" + tab + extra
	if err != nil {
		q += "&err=" + url.QueryEscape(err.Error())
	} else if msg != "" {
		q += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, "/m365?"+q, http.StatusSeeOther)
}

// M365SignatureSaveWeb: POST /m365/signatures (neu) und /m365/signatures/{id}
func (h *Handler) M365SignatureSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	name := strings.TrimSpace(r.FormValue("name"))
	dept := strings.TrimSpace(r.FormValue("department"))
	src := r.FormValue("html")
	active := r.FormValue("active") == "1"
	if name == "" {
		m365Back(w, r, "signatures", "&sig="+id, "", errors.New("Bitte einen Namen eingeben"))
		return
	}
	if bad := unknownSigPlaceholders(src); len(bad) > 0 {
		m365Back(w, r, "signatures", "&sig="+id, "", fmt.Errorf("Unbekannte Platzhalter: %s", strings.Join(bad, ", ")))
		return
	}
	var err error
	if id == "" {
		err = h.db.QueryRow(ctx, `INSERT INTO mail_signatures (name, department, html, active, updated_by) VALUES ($1,$2,$3,$4,$5::uuid) RETURNING id::text`,
			name, dept, src, active, getUser(r).ID).Scan(&id)
	} else {
		_, err = h.db.Exec(ctx, `UPDATE mail_signatures SET name=$2, department=$3, html=$4, active=$5, updated_by=$6::uuid, updated_at=NOW() WHERE id=$1::uuid`,
			id, name, dept, src, active, getUser(r).ID)
	}
	if err != nil {
		m365Back(w, r, "signatures", "&sig="+id, "", errors.New(friendlyDBError(err)))
		return
	}
	msg := "Signatur gespeichert – zum Ausrollen das Exchange-Skript neu herunterladen und ausführen"
	if len(signatureRuleHTML(src)) > sigMaxHTML {
		msg = fmt.Sprintf("Signatur gespeichert – aber zu lang für Exchange (%d von %d Zeichen)", len(signatureRuleHTML(src)), sigMaxHTML)
	}
	m365Back(w, r, "signatures", "&sig="+id, msg, nil)
}

// M365SignatureDeleteWeb: POST /m365/signatures/{id}/delete
func (h *Handler) M365SignatureDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM mail_signatures WHERE id=$1::uuid`, chi.URLParam(r, "id"))
	m365Back(w, r, "signatures", "", "Signatur gelöscht – die Transportregel entfernt das Exchange-Skript beim nächsten Lauf", err)
}

// M365SignaturePreviewWeb: POST /m365/signatures/preview (htmx) – Vorschau
// für eine Person, in einem abgeschotteten iframe.
func (h *Handler) M365SignaturePreviewWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	userID := r.FormValue("preview_user")
	if userID == "" {
		userID = getUser(r).ID
	}
	values, err := h.signatureValues(r.Context(), userID)
	if err != nil {
		_, _ = w.Write([]byte(`<p class="rec-sub">Keine Werte für diese Person.</p>`))
		return
	}
	src := r.FormValue("html")
	out := `<iframe sandbox="" title="Signaturvorschau" style="width:100%;min-height:220px;border:1px solid var(--border);border-radius:8px;background:#fff" srcdoc="` +
		html.EscapeString(`<!doctype html><meta charset="utf-8"><body style="font-family:Calibri,Arial,sans-serif;font-size:11pt;color:#000;margin:12px">`+renderSignature(src, values)) + `"></iframe>`
	if bad := unknownSigPlaceholders(src); len(bad) > 0 {
		out += `<p style="color:var(--red);font-size:12px"><i class="ti ti-alert-circle"></i> Unbekannte Platzhalter: ` + html.EscapeString(strings.Join(bad, ", ")) + `</p>`
	}
	if n := len(signatureRuleHTML(src)); n > sigMaxHTML {
		out += fmt.Sprintf(`<p style="color:var(--red);font-size:12px"><i class="ti ti-alert-circle"></i> Zu lang für Exchange: %d von %d Zeichen</p>`, n, sigMaxHTML)
	}
	_, _ = w.Write([]byte(out))
}

// M365CompanySaveWeb: POST /m365/company – Firmenname für {Firma}
func (h *Handler) M365CompanySaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	err := h.setUpdateSetting(r.Context(), m365CompanySetting, strings.TrimSpace(r.FormValue("company")))
	m365Back(w, r, "settings", "", "Firmenname gespeichert", err)
}

// signatureRulesScript: PowerShell für Exchange Online. Legt je aktiver
// Signatur eine Regel „PDH-Signatur: …“ an (Abteilungsregeln zuerst) und
// entfernt alle anderen PDH-Signaturregeln.
func signatureRulesScript(sigs []mailSignatureRow, now time.Time) string {
	psq := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	var active []mailSignatureRow
	var depts []string
	for _, s := range sigs {
		if s.Active {
			active = append(active, s)
			if s.Department != "" {
				depts = append(depts, s.Department)
			}
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].Department != "" && active[j].Department == "" })
	var b strings.Builder
	b.WriteString("# PDH – E-Mail-Signaturen als Exchange-Online-Transportregeln\r\n")
	b.WriteString("# Erzeugt am " + now.Format("02.01.2006 15:04") + ". Ausführen in PowerShell mit dem Modul ExchangeOnlineManagement:\r\n")
	b.WriteString("#   Connect-ExchangeOnline; .\\pdh-signaturen.ps1\r\n")
	b.WriteString("$ErrorActionPreference = 'Stop'\r\n")
	b.WriteString("if (-not (Get-Command Get-TransportRule -ErrorAction SilentlyContinue)) { Connect-ExchangeOnline }\r\n")
	b.WriteString("function Dec([string]$s) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($s)) }\r\n")
	b.WriteString("$rules = @(\r\n")
	used := map[string]bool{}
	for i, s := range active {
		name := truncateRunes("PDH-Signatur: "+s.Name, 58)
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = truncateRunes("PDH-Signatur: "+s.Name, 54) + fmt.Sprintf(" (%d)", n)
		}
		used[strings.ToLower(name)] = true
		b.WriteString("  @{ Name = " + psq(name) + "; Department = " + psq(s.Department) +
			"; Html = (Dec '" + base64.StdEncoding.EncodeToString([]byte(signatureRuleHTML(s.HTML))) + "') }")
		if i < len(active)-1 {
			b.WriteString(",")
		}
		b.WriteString("\r\n")
	}
	b.WriteString(")\r\n")
	b.WriteString("$others = @(")
	for i, d := range depts {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(psq("Department:^" + regexp.QuoteMeta(d) + "$"))
	}
	b.WriteString(")\r\n")
	b.WriteString(`Get-TransportRule | Where-Object { $_.Name -like 'PDH-Signatur:*' } | ForEach-Object { Remove-TransportRule -Identity $_.Identity -Confirm:$false }` + "\r\n")
	b.WriteString(`$priority = 0
foreach ($r in $rules) {
  $p = @{
    Name = $r.Name
    Priority = $priority
    FromScope = 'InOrganization'
    ApplyHtmlDisclaimerLocation = 'Append'
    ApplyHtmlDisclaimerFallbackAction = 'Wrap'
    ApplyHtmlDisclaimerText = $r.Html
    ExceptIfSubjectOrBodyContainsWords = '` + sigRuleMarker + `'
  }
  if ($r.Department) {
    $p.SenderADAttributeMatchesPatterns = 'Department:^' + [regex]::Escape($r.Department) + '$'
  } elseif ($others.Count -gt 0) {
    $p.ExceptIfSenderADAttributeMatchesPatterns = $others
  }
  New-TransportRule @p | Out-Null
  Write-Host ('Regel angelegt: ' + $r.Name)
  $priority++
}
Write-Host ('Fertig: ' + $rules.Count + ' Signaturregel(n).')
`)
	return strings.ReplaceAll(strings.ReplaceAll(b.String(), "\r\n", "\n"), "\n", "\r\n")
}

// M365SignatureScriptWeb: GET /m365/signatures/script – PowerShell-Datei
func (h *Handler) M365SignatureScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	for _, s := range h.loadSignatures(r.Context()) {
		if s.Active && s.TooLong() {
			m365Back(w, r, "signatures", "", "", fmt.Errorf("Signatur „%s“ ist zu lang für Exchange (%d von %d Zeichen)", s.Name, s.RuleLength, sigMaxHTML))
			return
		}
	}
	script := signatureRulesScript(h.loadSignatures(r.Context()), time.Now())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-signaturen.ps1"`)
	// UTF-8 mit BOM, damit Windows PowerShell 5.1 Umlaute richtig liest
	_, _ = w.Write(append([]byte{0xEF, 0xBB, 0xBF}, script...))
}

// entraPatch: Graph-Felder, die aus dem PDH nach Entra geschrieben werden –
// nur gefüllte PDH-Werte, die sich vom letzten Abgleich unterscheiden.
func entraPatch(pdh, ms [9]string) map[string]any {
	keys := [9]string{"givenName", "surname", "", "jobTitle", "department", "businessPhones", "mobilePhone", "officeLocation", "companyName"}
	patch := map[string]any{}
	for i, k := range keys {
		v := strings.TrimSpace(pdh[i])
		if k == "" || v == "" || v == strings.TrimSpace(ms[i]) {
			continue
		}
		if k == "businessPhones" {
			patch[k] = []string{v}
		} else {
			patch[k] = v
		}
	}
	return patch
}

// M365PushEntraWeb: POST /m365/signatures/push – Benutzerstamm nach Entra
// (braucht die Anwendungsberechtigung User.ReadWrite.All).
func (h *Handler) M365PushEntraWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageM365(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if !h.microsoftAppConfigured() {
		m365Back(w, r, "signatures", "", "", errors.New("Microsoft 365 ist nicht eingerichtet (Server-Einstellungen → Microsoft 365)"))
		return
	}
	token, err := h.microsoftDirectoryToken(ctx)
	if err != nil {
		m365Back(w, r, "signatures", "", "", err)
		return
	}
	company := h.getUpdateSetting(ctx, m365CompanySetting, "")
	rows, err := h.db.Query(ctx, `
		SELECT d.microsoft_user_id, u.id::text, u.first_name, u.last_name, COALESCE(u.job_title,''), COALESCE(u.department,''),
		       COALESCE(u.phone,''), COALESCE(u.phone_mobile,''), COALESCE(u.work_location,''),
		       d.given_name, d.surname, d.job_title, d.department, d.business_phone, d.mobile_phone, d.office_location, d.company_name
		FROM microsoft_directory_users d JOIN users u ON u.id = d.pdh_user_id
		WHERE d.tenant_id = $1 AND d.account_enabled AND u.active`, h.microsoft.TenantID)
	if err != nil {
		m365Back(w, r, "signatures", "", "", err)
		return
	}
	type job struct {
		msID, userID string
		patch        map[string]any
		p            [9]string
	}
	var jobs []job
	for rows.Next() {
		var j job
		var m [9]string
		if rows.Scan(&j.msID, &j.userID, &j.p[0], &j.p[1], &j.p[3], &j.p[4], &j.p[5], &j.p[6], &j.p[7],
			&m[0], &m[1], &m[3], &m[4], &m[5], &m[6], &m[7], &m[8]) != nil {
			continue
		}
		j.p[8] = company
		if j.patch = entraPatch(j.p, m); len(j.patch) > 0 {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	updated, failed := 0, 0
	var firstErr error
	for _, j := range jobs {
		err := h.microsoftGraphRequest(ctx, token, http.MethodPatch, microsoftGraphBase+"/v1.0/users/"+j.msID, j.patch, nil)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			componentLog("microsoft").Warn().Err(err).Str("user", j.userID).Msg("entra-felder schreiben")
			continue
		}
		updated++
		_, _ = h.db.Exec(ctx, `UPDATE microsoft_directory_users SET
			given_name=CASE WHEN $3<>'' THEN $3 ELSE given_name END, surname=CASE WHEN $4<>'' THEN $4 ELSE surname END,
			job_title=CASE WHEN $5<>'' THEN $5 ELSE job_title END, department=CASE WHEN $6<>'' THEN $6 ELSE department END,
			business_phone=CASE WHEN $7<>'' THEN $7 ELSE business_phone END, mobile_phone=CASE WHEN $8<>'' THEN $8 ELSE mobile_phone END,
			office_location=CASE WHEN $9<>'' THEN $9 ELSE office_location END, company_name=CASE WHEN $10<>'' THEN $10 ELSE company_name END
			WHERE tenant_id=$1 AND microsoft_user_id=$2`,
			h.microsoft.TenantID, j.msID, strings.TrimSpace(j.p[0]), strings.TrimSpace(j.p[1]), strings.TrimSpace(j.p[3]), strings.TrimSpace(j.p[4]),
			strings.TrimSpace(j.p[5]), strings.TrimSpace(j.p[6]), strings.TrimSpace(j.p[7]), strings.TrimSpace(j.p[8]))
	}
	msg := fmt.Sprintf("Nach Microsoft 365 übertragen: %d Personen aktualisiert", updated)
	if len(jobs) == 0 {
		msg = "Alles aktuell – keine Werte zu übertragen (vorher Core-Einstellungen → Microsoft 365 → Jetzt abgleichen)"
	}
	if failed > 0 {
		hint := ""
		var se microsoftGraphStatusError
		if errors.As(firstErr, &se) && se.StatusCode == http.StatusForbidden {
			hint = " – der App fehlt die Anwendungsberechtigung User.ReadWrite.All"
		}
		m365Back(w, r, "signatures", "", "", fmt.Errorf("%s, %d nicht möglich%s (Server-Protokoll)", msg, failed, hint))
		return
	}
	m365Back(w, r, "signatures", "", msg, nil)
}
