package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"pdh/pkg/config"
)

// Taeglicher Bestellvorschlag: alle aktiven Ersatzteile unter
// Mindestbestand, je Einkaeufer (spare_parts.buyer_id) und Lieferant
// gruppiert, mit vorgeschlagener Bestellmenge. Hersteller und Lieferant
// sind getrennt - bestellt wird beim bevorzugten (sonst guenstigsten)
// freigegebenen Lieferanten aus spare_part_suppliers.

const (
	keyPurchaseEnabled      = "purchase_report_enabled"
	keyPurchaseTime         = "purchase_report_time"
	keyPurchaseWeekdaysOnly = "purchase_report_weekdays_only"
	keyPurchaseDefaultBuyer = "purchase_report_default_buyer"
	keyPurchaseLastRun      = "purchase_report_last_run"
	purchaseCronID          = "purchase-report"
)

// ConfigureMail setzt SMTP-/Public-URL-Konfiguration (siehe config.MailConfig).
func (h *Handler) ConfigureMail(cfg config.MailConfig) {
	h.mailCfg = cfg
}

// PurchaseLine ist eine Position des Bestellvorschlags.
type PurchaseLine struct {
	PartID, PartNumber, Name, Manufacturer, SupplierPartNo, Unit string
	Stock, MinQty, OrderQty, UnitPrice, LineTotal                string
	Critical                                                     bool
	total                                                        float64
}

// PurchaseSupplierGroup sind die Positionen eines Lieferanten.
type PurchaseSupplierGroup struct {
	PartnerID, Name, OrderEmail, OrderPhone, CustomerNo, ShopURL, LeadTime string
	Lines                                                                  []PurchaseLine
	Total                                                                  string
	total                                                                  float64
}

// PurchaseBuyerGroup ist der Bestellvorschlag eines Einkaeufers.
type PurchaseBuyerGroup struct {
	BuyerID, BuyerName, BuyerEmail string
	Suppliers                      []*PurchaseSupplierGroup
	Positions                      int
	Total                          string
}

func roundUpTo(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return math.Ceil(qty/step-1e-9) * step
}

// suggestOrderQty: Bestellmenge (reorder_qty) - reicht sie nicht, um
// ueber den Mindestbestand zu kommen, wird die Fehlmenge aufgeschlagen;
// danach auf die Mindestabnahme des Lieferanten aufgerundet.
func suggestOrderQty(stock, minQty, reorderQty, supplierMin float64) float64 {
	qty := reorderQty
	if stock+qty <= minQty {
		qty = minQty - stock + reorderQty
	}
	if qty <= 0 {
		qty = math.Max(minQty-stock, 1)
	}
	if supplierMin > 0 && qty < supplierMin {
		qty = supplierMin
	}
	return roundUpTo(qty, 1)
}

func (h *Handler) appSetting(ctx context.Context, key, fallback string) string {
	var v string
	if err := h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (h *Handler) setAppSetting(ctx context.Context, key, value string) error {
	_, err := h.db.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
	return err
}

// buildPurchaseSuggestions liefert die Bestellvorschlaege je Einkaeufer.
// Teile ohne Einkaeufer landen beim Standard-Einkaeufer (falls gesetzt),
// sonst in einer Gruppe "Ohne Einkäufer".
func (h *Handler) buildPurchaseSuggestions(ctx context.Context) ([]*PurchaseBuyerGroup, error) {
	defaultBuyer := h.appSetting(ctx, keyPurchaseDefaultBuyer, "")
	rows, err := h.db.Query(ctx, `
		SELECT sp.id::text, sp.part_number, sp.name, sp.unit,
		       sp.stock_qty::float8, sp.min_qty::float8, sp.reorder_qty::float8, sp.critical_qty::float8, sp.price::float8,
		       COALESCE(sp.buyer_id::text, ''), COALESCE(NULLIF(mf.name, ''), sp.manufacturer, ''),
		       COALESCE(sup.partner_id::text, ''), COALESCE(sup.supplier_part_no, ''),
		       sup.price::float8, sup.min_order_qty::float8, sup.lead_time_days
		FROM spare_parts sp
		LEFT JOIN business_partners mf ON mf.id = sp.manufacturer_id
		LEFT JOIN LATERAL (
			SELECT s.partner_id, s.supplier_part_no, s.price, s.min_order_qty, s.lead_time_days
			FROM spare_part_suppliers s JOIN business_partners b ON b.id = s.partner_id
			WHERE s.part_id = sp.id AND b.active AND b.approval_status <> 'blocked'
			ORDER BY s.preferred DESC, s.price NULLS LAST, s.created_at
			LIMIT 1) sup ON true
		WHERE sp.active AND sp.min_qty > 0 AND sp.stock_qty <= sp.min_qty
		ORDER BY sp.name`)
	if err != nil {
		return nil, err
	}
	type partRow struct {
		line      PurchaseLine
		buyerID   string
		partnerID string
		lead      *int
	}
	var parts []partRow
	partnerIDs := map[string]bool{}
	buyerIDs := map[string]bool{}
	for rows.Next() {
		var pr partRow
		var stock, minQty, reorder, critical, price float64
		var sPrice, sMin *float64
		if err := rows.Scan(&pr.line.PartID, &pr.line.PartNumber, &pr.line.Name, &pr.line.Unit,
			&stock, &minQty, &reorder, &critical, &price, &pr.buyerID, &pr.line.Manufacturer,
			&pr.partnerID, &pr.line.SupplierPartNo, &sPrice, &sMin, &pr.lead); err != nil {
			continue
		}
		if pr.buyerID == "" {
			pr.buyerID = defaultBuyer
		}
		unitPrice := price
		if sPrice != nil {
			unitPrice = *sPrice
		}
		supplierMin := 0.0
		if sMin != nil {
			supplierMin = *sMin
		}
		qty := suggestOrderQty(stock, minQty, reorder, supplierMin)
		pr.line.Stock, pr.line.MinQty, pr.line.OrderQty = formatQty(stock), formatQty(minQty), formatQty(qty)
		pr.line.UnitPrice = formatEuro(unitPrice)
		pr.line.total = qty * unitPrice
		pr.line.LineTotal = formatEuro(pr.line.total)
		pr.line.Critical = stock <= critical
		parts = append(parts, pr)
		if pr.partnerID != "" {
			partnerIDs[pr.partnerID] = true
		}
		if pr.buyerID != "" {
			buyerIDs[pr.buyerID] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Lieferanten-Stammdaten (Bestellkontakt, Kundennr., Shop-Link)
	suppliers := map[string]PurchaseSupplierGroup{}
	if len(partnerIDs) > 0 {
		ids := make([]string, 0, len(partnerIDs))
		for id := range partnerIDs {
			ids = append(ids, id)
		}
		prow, err := h.db.Query(ctx, `
			SELECT bp.id::text, bp.name,
			       COALESCE(NULLIF(bp.spare_parts_email, ''), NULLIF(bp.order_email, ''), bp.email),
			       COALESCE(NULLIF(bp.spare_parts_phone, ''), bp.phone), bp.customer_no,
			       COALESCE((SELECT l.url FROM business_partner_links l WHERE l.partner_id = bp.id AND l.kind = 'shop'
			                 ORDER BY l.created_at LIMIT 1), '')
			FROM business_partners bp WHERE bp.id::text = ANY($1)`, ids)
		if err == nil {
			for prow.Next() {
				var s PurchaseSupplierGroup
				if prow.Scan(&s.PartnerID, &s.Name, &s.OrderEmail, &s.OrderPhone, &s.CustomerNo, &s.ShopURL) == nil {
					suppliers[s.PartnerID] = s
				}
			}
			prow.Close()
		}
	}
	type buyerInfo struct{ name, email string }
	buyers := map[string]buyerInfo{}
	if len(buyerIDs) > 0 {
		ids := make([]string, 0, len(buyerIDs))
		for id := range buyerIDs {
			ids = append(ids, id)
		}
		brow, err := h.db.Query(ctx, `
			SELECT id::text, TRIM(first_name || ' ' || last_name), email
			FROM users WHERE id::text = ANY($1) AND active`, ids)
		if err == nil {
			for brow.Next() {
				var id string
				var b buyerInfo
				if brow.Scan(&id, &b.name, &b.email) == nil {
					buyers[id] = b
				}
			}
			brow.Close()
		}
	}

	var result []*PurchaseBuyerGroup
	byBuyer := map[string]*PurchaseBuyerGroup{}
	bySupplier := map[string]*PurchaseSupplierGroup{}
	for _, pr := range parts {
		buyerKey := pr.buyerID
		if _, ok := buyers[buyerKey]; !ok {
			buyerKey = "" // Einkaeufer inaktiv/geloescht -> ohne Einkaeufer
		}
		bg := byBuyer[buyerKey]
		if bg == nil {
			bg = &PurchaseBuyerGroup{BuyerID: buyerKey, BuyerName: "Ohne Einkäufer"}
			if b, ok := buyers[buyerKey]; ok {
				bg.BuyerName, bg.BuyerEmail = b.name, b.email
			}
			byBuyer[buyerKey] = bg
			result = append(result, bg)
		}
		sKey := buyerKey + "|" + pr.partnerID
		sg := bySupplier[sKey]
		if sg == nil {
			if s, ok := suppliers[pr.partnerID]; ok {
				cp := s
				sg = &cp
			} else {
				sg = &PurchaseSupplierGroup{Name: "Kein Lieferant hinterlegt"}
			}
			bySupplier[sKey] = sg
			bg.Suppliers = append(bg.Suppliers, sg)
		}
		if pr.lead != nil {
			sg.LeadTime = strconv.Itoa(*pr.lead) + " Tage"
		}
		sg.Lines = append(sg.Lines, pr.line)
		sg.total += pr.line.total
		sg.Total = formatEuro(sg.total)
		bg.Positions++
	}
	for _, bg := range result {
		var t float64
		for _, s := range bg.Suppliers {
			t += s.total
		}
		bg.Total = formatEuro(t)
	}
	return result, nil
}

// ── Mailversand ──────────────────────────────────────────────

func (h *Handler) mailConfigured() bool {
	return h.mailCfg.Host != "" && h.mailCfg.From != ""
}

// sendMail verschickt eine HTML-Mail per SMTP (STARTTLS, implizites TLS
// oder unverschluesselt je nach PDH_SMTP_TLS).
func (h *Handler) sendMail(to []string, subject, html string) error {
	c := h.mailCfg
	if !h.mailConfigured() {
		return errors.New("SMTP ist nicht konfiguriert (PDH_SMTP_HOST / PDH_SMTP_FROM)")
	}
	port := c.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}

	var client *smtp.Client
	var err error
	if strings.EqualFold(c.TLSMode, "tls") {
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: 20 * time.Second}, "tcp", addr, tlsCfg)
		if derr != nil {
			return derr
		}
		client, err = smtp.NewClient(conn, c.Host)
	} else {
		conn, derr := net.DialTimeout("tcp", addr, 20*time.Second)
		if derr != nil {
			return derr
		}
		client, err = smtp.NewClient(conn, c.Host)
		if err == nil && !strings.EqualFold(c.TLSMode, "none") {
			err = client.StartTLS(tlsCfg)
		}
	}
	if err != nil {
		return err
	}
	defer client.Close()
	if c.User != "" && !strings.EqualFold(c.TLSMode, "none") {
		if err := client.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(c.From); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n",
		c.From, strings.Join(to, ", "), mime.QEncoding.Encode("utf-8", subject), time.Now().Format(time.RFC1123Z))
	msg.WriteString("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(html))
	for len(enc) > 76 {
		msg.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	msg.WriteString(enc + "\r\n")
	if _, err := wc.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// sendPurchaseReports verschickt je Einkaeufer eine Mail. Gruppen ohne
// Einkaeufer bzw. ohne E-Mail werden nicht versendet (erscheinen aber in
// der Web-Ansicht). Rueckgabe: Anzahl versendeter Mails.
func (h *Handler) sendPurchaseReports(ctx context.Context) (int, []string, error) {
	groups, err := h.buildPurchaseSuggestions(ctx)
	if err != nil {
		return 0, nil, err
	}
	sent := 0
	var problems []string
	for _, g := range groups {
		if g.BuyerEmail == "" {
			if g.Positions > 0 {
				problems = append(problems, fmt.Sprintf("%d Position(en) ohne Einkäufer/E-Mail (%s)", g.Positions, g.BuyerName))
			}
			continue
		}
		var body bytes.Buffer
		t, err := h.tmpl.Clone()
		if err != nil {
			return sent, problems, err
		}
		if err := t.ExecuteTemplate(&body, "purchase-report-mail", map[string]interface{}{
			"Group": g, "BaseURL": strings.TrimRight(h.mailCfg.PublicURL, "/"), "Date": time.Now().Format("02.01.2006"),
		}); err != nil {
			return sent, problems, err
		}
		subject := fmt.Sprintf("PDH Bestellvorschlag %s – %d Position(en)", time.Now().Format("02.01.2006"), g.Positions)
		if err := h.sendMail([]string{g.BuyerEmail}, subject, body.String()); err != nil {
			problems = append(problems, g.BuyerName+": "+err.Error())
			continue
		}
		sent++
	}
	_ = h.setAppSetting(ctx, keyPurchaseLastRun, fmt.Sprintf("%s · %d Mail(s)", time.Now().Format("02.01.2006 15:04"), sent))
	return sent, problems, nil
}

// purchaseCronSpec baut aus Uhrzeit (HH:MM) und Werktag-Option den
// Cron-Ausdruck; leer = deaktiviert.
func purchaseCronSpec(enabled bool, hhmm string, weekdaysOnly bool) string {
	if !enabled {
		return ""
	}
	t, err := time.Parse("15:04", strings.TrimSpace(hhmm))
	if err != nil {
		t, _ = time.Parse("15:04", "06:00")
	}
	days := "*"
	if weekdaysOnly {
		days = "1-5"
	}
	return fmt.Sprintf("%d %d * * %s", t.Minute(), t.Hour(), days)
}

// StartPurchaseReportSchedule plant den taeglichen Versand gemaess den
// App-Einstellungen ein (auch nach Aenderung der Einstellungen aufrufen).
func (h *Handler) StartPurchaseReportSchedule(ctx context.Context) {
	if h.db == nil || h.exportCron == nil {
		return
	}
	spec := purchaseCronSpec(h.appSetting(ctx, keyPurchaseEnabled, "1") == "1",
		h.appSetting(ctx, keyPurchaseTime, "06:00"), h.appSetting(ctx, keyPurchaseWeekdaysOnly, "1") == "1")
	if !h.mailConfigured() {
		spec = ""
		log.Info().Msg("bestellvorschlag: kein SMTP konfiguriert - automatischer versand inaktiv")
	}
	if err := h.exportCron.Schedule(purchaseCronID, spec, func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		sent, problems, err := h.sendPurchaseReports(runCtx)
		if err != nil {
			log.Error().Err(err).Msg("bestellvorschlag fehlgeschlagen")
			return
		}
		log.Info().Int("sent", sent).Strs("problems", problems).Msg("bestellvorschlag versendet")
	}); err != nil {
		log.Error().Err(err).Str("spec", spec).Msg("bestellvorschlag einplanen fehlgeschlagen")
	}
}

// ── Web ──────────────────────────────────────────────────────

type PurchaseReportData struct {
	BaseData
	Groups         []*PurchaseBuyerGroup
	CanEdit        bool
	MailConfigured bool
	Enabled        bool
	Time           string
	WeekdaysOnly   bool
	DefaultBuyer   string
	LastRun        string
	Users          []UserOption
	OnlyMine       bool
	Message, Error string
}

func (h *Handler) PurchaseReportPage(w http.ResponseWriter, r *http.Request) {
	if !h.canViewPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	data := PurchaseReportData{
		BaseData:       h.baseData(r, "directory", "Bestellvorschlag", "Einkauf"),
		CanEdit:        h.canEditPartners(r),
		MailConfigured: h.mailConfigured(),
		Enabled:        h.appSetting(ctx, keyPurchaseEnabled, "1") == "1",
		Time:           h.appSetting(ctx, keyPurchaseTime, "06:00"),
		WeekdaysOnly:   h.appSetting(ctx, keyPurchaseWeekdaysOnly, "1") == "1",
		DefaultBuyer:   h.appSetting(ctx, keyPurchaseDefaultBuyer, ""),
		LastRun:        h.appSetting(ctx, keyPurchaseLastRun, ""),
		OnlyMine:       q.Get("mine") == "1",
		Message:        q.Get("msg"),
		Error:          q.Get("err"),
	}
	groups, err := h.buildPurchaseSuggestions(ctx)
	if err != nil {
		data.Error = err.Error()
	}
	me := getUser(r).ID
	for _, g := range groups {
		if !data.OnlyMine || g.BuyerID == me {
			data.Groups = append(data.Groups, g)
		}
	}
	if data.CanEdit {
		data.Users = h.userOptions(ctx)
	}
	h.render(w, "purchase_report", data)
}

func (h *Handler) PurchaseReportSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	hhmm := strings.TrimSpace(r.FormValue("time"))
	if _, err := time.Parse("15:04", hhmm); err != nil {
		http.Redirect(w, r, "/directory/purchase-report?err="+url.QueryEscape("Ungültige Uhrzeit"), http.StatusSeeOther)
		return
	}
	boolStr := func(k string) string {
		if r.FormValue(k) == "on" {
			return "1"
		}
		return "0"
	}
	var err error
	for k, v := range map[string]string{
		keyPurchaseEnabled: boolStr("enabled"), keyPurchaseTime: hhmm,
		keyPurchaseWeekdaysOnly: boolStr("weekdays_only"), keyPurchaseDefaultBuyer: strings.TrimSpace(r.FormValue("default_buyer")),
	} {
		if err == nil {
			err = h.setAppSetting(ctx, k, v)
		}
	}
	if err != nil {
		http.Redirect(w, r, "/directory/purchase-report?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	h.StartPurchaseReportSchedule(context.Background())
	http.Redirect(w, r, "/directory/purchase-report?msg="+url.QueryEscape("Einstellungen gespeichert"), http.StatusSeeOther)
}

func (h *Handler) PurchaseReportSendWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	sent, problems, err := h.sendPurchaseReports(r.Context())
	target := "/directory/purchase-report?"
	switch {
	case err != nil:
		target += "err=" + url.QueryEscape(err.Error())
	case len(problems) > 0:
		target += "msg=" + url.QueryEscape(fmt.Sprintf("%d Mail(s) versendet", sent)) + "&err=" + url.QueryEscape(strings.Join(problems, " · "))
	default:
		target += "msg=" + url.QueryEscape(fmt.Sprintf("%d Mail(s) versendet", sent))
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// PartPurchasingWeb setzt den Einkaeufer eines Ersatzteils.
func (h *Handler) PartPurchasingWeb(w http.ResponseWriter, r *http.Request) {
	partID := chi.URLParam(r, "id")
	u := getUser(r)
	if !h.canEditPartners(r) && !h.rbac.HasPermissionForUser(u.ID, string(u.Role), "inventory.edit") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	back := "/inventory/" + partID
	if _, err := h.db.Exec(r.Context(), `UPDATE spare_parts SET buyer_id = NULLIF($1, '')::uuid, updated_at = NOW() WHERE id = $2`,
		strings.TrimSpace(r.FormValue("buyer_id")), partID); err != nil {
		back += "?err=" + url.QueryEscape(friendlyDBError(err))
	}
	http.Redirect(w, r, withTab(back, "purchasing"), http.StatusSeeOther)
}

