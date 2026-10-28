package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"pdh/internal/modules/inventory"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Bestellvorgänge (Migration 149): ein Vorgang je Lieferant mit mehreren
// Positionen durchläuft Angebot → Bestellung → Auftragsbestätigung (AB) →
// Lieferung → Rechnung OK.
//
//   - AB und Bestellung werden je Position verglichen (Menge, Preis), der
//     AB-Liefertermin mit dem Wunschtermin.
//   - Lieferungen (auch Teillieferungen) werden mit der AB verglichen
//     (Menge, Termin) und buchen ins Lager (Lagerbewegung „Zugang“); volle
//     Kanban-Karten werden wie beim normalen Wareneingang zurückgesetzt.
//   - „Rechnung OK“ schließt den Vorgang. Die Rechnung selbst wird in ELO
//     bearbeitet, deshalb geht das auch ohne hinterlegte Rechnung.
//   - Der Vorgang erscheint beim Ersatzteil (Reiter Lieferanten & Einkauf)
//     und beim Lieferanten (Reiter Bestellungen).

const (
	permPurchaseView    = "procurement.view"
	permPurchaseEdit    = "procurement.purchase"
	permPurchaseReceive = "procurement.receive"
	permPurchaseInvoice = "procurement.invoice"
	refPurchase         = "purchase_order"
)

// purchaseSteps: die fünf Schritte in Reihenfolge.
var purchaseSteps = []struct{ Key, Label, Icon string }{
	{"quote", "Angebot", "ti-file-dollar"},
	{"ordered", "Bestellung", "ti-shopping-cart"},
	{"confirmed", "Auftragsbestätigung", "ti-file-check"},
	{"delivered", "Lieferung", "ti-truck-delivery"},
	{"closed", "Rechnung OK", "ti-receipt"},
}

var purchaseStatusLabels = map[string][2]string{
	"quote": {"Angebot", "b-gray"}, "ordered": {"bestellt", "b-blue"}, "confirmed": {"bestätigt", "b-blue"},
	"partial": {"teilgeliefert", "b-amber"}, "delivered": {"geliefert", "b-green"},
	"closed": {"abgeschlossen", "b-green"}, "cancelled": {"storniert", "b-red"},
}

// purchaseStepIndex: wie weit ein Status in den Schritten ist (0–5).
func purchaseStepIndex(status string) int {
	switch status {
	case "ordered":
		return 1
	case "confirmed":
		return 2
	case "partial":
		return 3
	case "delivered":
		return 4
	case "closed":
		return 5
	}
	return 0
}

type purchaseStepView struct {
	Key, Label, Icon string
	Done, Current    bool
	Warn             string // Abweichung in diesem Schritt
}

type purchaseItemView struct {
	ID, PartID, PartNo, PartName, Description, SupplierPartNo, Unit, StorageID string
	QtyQuoted, PriceQuoted, QtyOrdered, PriceOrdered               *float64
	QtyConfirmed, PriceConfirmed                                   *float64
	Delivered                                                      float64
	ABWarn, DeliveryWarn                                           string
}

// Open: noch zu liefernde Menge (bestätigt, sonst bestellt).
func (i purchaseItemView) Open() float64 {
	target := i.Target()
	if target <= i.Delivered {
		return 0
	}
	return target - i.Delivered
}

// Target: Sollmenge der Lieferung (AB vor Bestellung).
func (i purchaseItemView) Target() float64 {
	if i.QtyConfirmed != nil {
		return *i.QtyConfirmed
	}
	if i.QtyOrdered != nil {
		return *i.QtyOrdered
	}
	return 0
}

func (i purchaseItemView) Q(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
func (i purchaseItemView) F(v float64) string { return formatQty(v) }

type purchaseDeliveryView struct {
	ID, NoteNo, Date, By, Notes string
	Lines                       []string
}

type purchaseView struct {
	ID, Number, Title, SupplierID, SupplierName, DepartmentID, DepartmentName string
	Status, StatusLabel, StatusClass, StorageID, StoragePath                  string
	WantedDate, QuoteNo, QuoteDate, QuoteValid, OrderNo, OrderedAt, OrderedBy string
	ConfirmationNo, ConfirmationDate, ConfirmedDelivery                       string
	InvoiceRef, InvoiceNote, InvoiceOKAt, InvoiceOKBy, Notes, CreatedAt       string
	Value                                                                     float64
	Items                                                                     []purchaseItemView
	Deliveries                                                                []purchaseDeliveryView
	Steps                                                                     []purchaseStepView
	Warnings                                                                  []string
	Late                                                                      bool
}

func (p purchaseView) ValueLabel() string {
	if p.Value == 0 {
		return ""
	}
	return formatEuro(p.Value)
}

// D: Datum (JJJJ-MM-TT) deutsch anzeigen.
func (p purchaseView) D(iso string) string {
	if t, err := time.Parse("2006-01-02", iso); err == nil {
		return t.Format("02.01.2006")
	}
	return iso
}

// Final: abgeschlossen oder storniert – keine Änderungen mehr.
func (p purchaseView) Final() bool { return p.Status == "closed" || p.Status == "cancelled" }

func pDateISO(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func pDateDE(t *time.Time) string {
	if t == nil {
		return ""
	}
	return deDate(*t)
}

const purchaseSelect = `SELECT o.id::text, o.number, o.title, COALESCE(o.supplier_id::text, ''), COALESCE(bp.name, ''),
	COALESCE(o.department_id::text, ''), COALESCE(d.name, ''), o.status, COALESCE(o.storage_node_id::text, ''),
	o.wanted_date, o.quote_no, o.quote_date, o.quote_valid_until, o.order_no, o.ordered_at,
	COALESCE(ob.first_name || ' ' || ob.last_name, ''), o.confirmation_no, o.confirmation_date, o.confirmed_delivery,
	o.invoice_ref, o.invoice_note, o.invoice_ok_at, COALESCE(ib.first_name || ' ' || ib.last_name, ''), o.notes, o.created_at,
	COALESCE((SELECT SUM(COALESCE(i.qty_confirmed, i.qty_ordered, i.qty_quoted, 0) * COALESCE(i.price_confirmed, i.price_ordered, i.price_quoted, 0))::float8
		FROM purchase_order_items i WHERE i.order_id = o.id), 0)
	FROM purchase_orders o
	LEFT JOIN business_partners bp ON bp.id = o.supplier_id
	LEFT JOIN departments d ON d.id = o.department_id
	LEFT JOIN users ob ON ob.id = o.ordered_by
	LEFT JOIN users ib ON ib.id = o.invoice_ok_by`

func scanPurchase(row rowScanner) (*purchaseView, error) {
	var p purchaseView
	var wanted, qd, qv, cd, cdel *time.Time
	var orderedAt, okAt *time.Time
	var created time.Time
	if err := row.Scan(&p.ID, &p.Number, &p.Title, &p.SupplierID, &p.SupplierName, &p.DepartmentID, &p.DepartmentName, &p.Status,
		&p.StorageID, &wanted, &p.QuoteNo, &qd, &qv, &p.OrderNo, &orderedAt, &p.OrderedBy, &p.ConfirmationNo, &cd, &cdel,
		&p.InvoiceRef, &p.InvoiceNote, &okAt, &p.InvoiceOKBy, &p.Notes, &created, &p.Value); err != nil {
		return nil, err
	}
	p.WantedDate, p.QuoteDate, p.QuoteValid = pDateISO(wanted), pDateISO(qd), pDateISO(qv)
	p.ConfirmationDate, p.ConfirmedDelivery = pDateISO(cd), pDateISO(cdel)
	p.OrderedAt, p.InvoiceOKAt, p.CreatedAt = pDateDE(orderedAt), pDateDE(okAt), deDate(created)
	l := purchaseStatusLabels[p.Status]
	p.StatusLabel, p.StatusClass = l[0], l[1]
	// überfällig: bestätigter (sonst gewünschter) Termin vorbei und noch nicht vollständig geliefert
	due := cdel
	if due == nil {
		due = wanted
	}
	if due != nil && (p.Status == "ordered" || p.Status == "confirmed" || p.Status == "partial") && due.Before(time.Now().Truncate(24*time.Hour)) {
		p.Late = true
	}
	return &p, nil
}

func (h *Handler) loadPurchase(ctx context.Context, id string) (*purchaseView, error) {
	p, err := scanPurchase(h.db.QueryRow(ctx, purchaseSelect+` WHERE o.id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	_, paths := h.storagePaths(ctx)
	p.StoragePath = paths[p.StorageID]
	rows, err := h.db.Query(ctx, `SELECT i.id::text, COALESCE(i.part_id::text, ''), COALESCE(sp.part_number, ''), COALESCE(sp.name, ''),
		i.description, i.supplier_part_no, i.unit, COALESCE(i.storage_node_id::text, ''),
		i.qty_quoted::float8, i.price_quoted::float8, i.qty_ordered::float8, i.price_ordered::float8,
		i.qty_confirmed::float8, i.price_confirmed::float8,
		COALESCE((SELECT SUM(di.qty) FROM purchase_delivery_items di WHERE di.item_id = i.id), 0)::float8
		FROM purchase_order_items i LEFT JOIN spare_parts sp ON sp.id = i.part_id
		WHERE i.order_id = $1::uuid ORDER BY i.pos, i.id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it purchaseItemView
		if err := rows.Scan(&it.ID, &it.PartID, &it.PartNo, &it.PartName, &it.Description, &it.SupplierPartNo, &it.Unit, &it.StorageID,
			&it.QtyQuoted, &it.PriceQuoted, &it.QtyOrdered, &it.PriceOrdered, &it.QtyConfirmed, &it.PriceConfirmed, &it.Delivered); err != nil {
			rows.Close()
			return nil, err
		}
		p.Items = append(p.Items, it)
	}
	rows.Close()
	drows, err := h.db.Query(ctx, `SELECT d.id::text, d.delivery_note_no, d.delivered_on, COALESCE(u.first_name || ' ' || u.last_name, ''), d.notes,
		COALESCE(ARRAY(SELECT ` + qtySQL("di.qty") + ` || ' ' || i.unit || ' ' || COALESCE(NULLIF(i.description, ''), sp.name, 'Position') ||
			CASE WHEN di.booked THEN ' → Lager' ELSE '' END
			FROM purchase_delivery_items di JOIN purchase_order_items i ON i.id = di.item_id LEFT JOIN spare_parts sp ON sp.id = i.part_id
			WHERE di.delivery_id = d.id ORDER BY i.pos), '{}')
		FROM purchase_deliveries d LEFT JOIN users u ON u.id = d.received_by WHERE d.order_id = $1::uuid ORDER BY d.delivered_on, d.created_at`, id)
	if err == nil {
		for drows.Next() {
			var dv purchaseDeliveryView
			var on time.Time
			if drows.Scan(&dv.ID, &dv.NoteNo, &on, &dv.By, &dv.Notes, &dv.Lines) == nil {
				dv.Date = deDate(on)
				p.Deliveries = append(p.Deliveries, dv)
			}
		}
		drows.Close()
	}
	p.check()
	return p, nil
}

// check: Abgleich AB ↔ Bestellung und Lieferung ↔ AB, Schritte für die Anzeige.
func (p *purchaseView) check() {
	abWarn, delWarn := 0, 0
	for i := range p.Items {
		it := &p.Items[i]
		var w []string
		if it.QtyConfirmed != nil && it.QtyOrdered != nil && math.Abs(*it.QtyConfirmed-*it.QtyOrdered) > 1e-9 {
			w = append(w, "Menge "+formatQty(*it.QtyConfirmed)+" statt "+formatQty(*it.QtyOrdered))
		}
		if it.PriceConfirmed != nil && it.PriceOrdered != nil && math.Abs(*it.PriceConfirmed-*it.PriceOrdered) > 0.005 {
			w = append(w, "Preis "+formatEuro(*it.PriceConfirmed)+" statt "+formatEuro(*it.PriceOrdered))
		}
		if len(w) > 0 {
			it.ABWarn = strings.Join(w, ", ")
			abWarn++
		}
		target := it.Target()
		switch {
		case it.Delivered > target+1e-9 && target > 0:
			it.DeliveryWarn = "Überlieferung: " + formatQty(it.Delivered) + " statt " + formatQty(target)
			delWarn++
		case it.Delivered > 0 && it.Delivered < target-1e-9:
			it.DeliveryWarn = "offen: " + formatQty(target-it.Delivered)
		}
	}
	if abWarn > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Auftragsbestätigung weicht bei %d Position(en) von der Bestellung ab", abWarn))
	}
	if p.ConfirmedDelivery != "" && p.WantedDate != "" && p.ConfirmedDelivery > p.WantedDate {
		p.Warnings = append(p.Warnings, "Liefertermin laut AB liegt nach dem Wunschtermin")
	}
	if delWarn > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Lieferung weicht bei %d Position(en) von der AB ab (Überlieferung)", delWarn))
	}
	if p.Late {
		p.Warnings = append(p.Warnings, "Lieferung überfällig")
	}
	idx := purchaseStepIndex(p.Status)
	for i, s := range purchaseSteps {
		v := purchaseStepView{Key: s.Key, Label: s.Label, Icon: s.Icon, Done: i < idx || (p.Status == "closed"), Current: i == idx && p.Status != "closed" && p.Status != "cancelled"}
		if s.Key == "delivered" && p.Status == "partial" {
			v.Label, v.Current, v.Done = "Teillieferung", true, false
		}
		if s.Key == "confirmed" && (abWarn > 0 || (p.ConfirmedDelivery != "" && p.WantedDate != "" && p.ConfirmedDelivery > p.WantedDate)) {
			v.Warn = "weicht ab"
		}
		if s.Key == "delivered" && (delWarn > 0 || p.Late) {
			v.Warn = map[bool]string{true: "überfällig", false: "weicht ab"}[p.Late && delWarn == 0]
		}
		p.Steps = append(p.Steps, v)
	}
}

// refreshPurchaseStatus: Status aus dem Stand der Schritte ableiten.
func refreshPurchaseStatus(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconnTag, error)
}, id string) error {
	_, err := q.Exec(ctx, `UPDATE purchase_orders o SET status = CASE
		WHEN o.status = 'cancelled' THEN 'cancelled'
		WHEN o.invoice_ok_at IS NOT NULL THEN 'closed'
		WHEN EXISTS (SELECT 1 FROM purchase_delivery_items di JOIN purchase_order_items i ON i.id = di.item_id WHERE i.order_id = o.id) THEN
			CASE WHEN NOT EXISTS (SELECT 1 FROM purchase_order_items i WHERE i.order_id = o.id
				AND COALESCE(i.qty_confirmed, i.qty_ordered, 0) > COALESCE((SELECT SUM(di.qty) FROM purchase_delivery_items di WHERE di.item_id = i.id), 0) + 0.0000001)
			THEN 'delivered' ELSE 'partial' END
		WHEN o.confirmation_no <> '' OR o.confirmation_date IS NOT NULL OR o.confirmed_delivery IS NOT NULL THEN 'confirmed'
		WHEN o.ordered_at IS NOT NULL THEN 'ordered'
		ELSE 'quote' END, updated_at = NOW()
		WHERE o.id = $1::uuid`, id)
	return err
}

// ── Seiten ───────────────────────────────────────────────────

type PurchasesPageData struct {
	BaseData
	Tab, Query       string
	Orders           []*purchaseView
	CountOpen        int
	CountDone        int
	CanEdit          bool
	Suppliers        []partnerOption
	Message, Error   string
	StatusFilterOpts []struct{ Key, Label string }
}

func (h *Handler) canPurchaseView(r *http.Request) bool { return h.hasPerm(r, permPurchaseView) }

// PurchasesPage: GET /purchases – Liste der Bestellvorgänge.
func (h *Handler) PurchasesPage(w http.ResponseWriter, r *http.Request) {
	if !h.canPurchaseView(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := PurchasesPageData{BaseData: h.baseData(r, "purchases", "Bestellvorgänge", "Bestellvorgänge"),
		Tab: q.Get("tab"), Query: strings.TrimSpace(q.Get("q")), CanEdit: h.hasPerm(r, permPurchaseEdit),
		Message: q.Get("msg"), Error: q.Get("err")}
	if d.Tab != "done" && d.Tab != "all" {
		d.Tab = "open"
	}
	for _, o := range h.supplierOptions(ctx) {
		d.Suppliers = append(d.Suppliers, partnerOption{Value: o.Value, Label: o.Label})
	}
	rows, err := h.db.Query(ctx, purchaseSelect+` ORDER BY o.created_at DESC LIMIT 1000`)
	if err == nil {
		for rows.Next() {
			p, err := scanPurchase(rows)
			if err != nil {
				continue
			}
			if !h.hasPermIn(r, permPurchaseView, deptList(p.DepartmentID)) {
				continue
			}
			final := p.Status == "closed" || p.Status == "cancelled"
			if final {
				d.CountDone++
			} else {
				d.CountOpen++
			}
			if (d.Tab == "open" && final) || (d.Tab == "done" && !final) {
				continue
			}
			if d.Query != "" && !strings.Contains(strings.ToLower(p.Number+" "+p.Title+" "+p.SupplierName+" "+p.OrderNo+" "+p.ConfirmationNo+" "+p.InvoiceRef), strings.ToLower(d.Query)) {
				continue
			}
			d.Orders = append(d.Orders, p)
		}
		rows.Close()
	}
	// angezeigte Vorgänge vollständig laden – die Prüfung braucht die Positionen
	for i, p := range d.Orders {
		if i >= 300 {
			p.check()
			continue
		}
		if full, err := h.loadPurchase(ctx, p.ID); err == nil {
			d.Orders[i] = full
		}
	}
	h.render(w, "purchases", d)
}

func deptList(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

type PurchaseDetailData struct {
	BaseData
	P                                     *purchaseView
	Tab, Message, Error                   string
	CanEdit, CanReceive, CanInvoice       bool
	CanDelete                             bool
	Suppliers, Storage                    []partnerOption
	Departments                           []deptView
	Parts                                 []purchasePartOption
	History                               []HistoryView
	Today                                 string
	HasOpen                               bool
}

type purchasePartOption struct{ ID, No, Name, Unit string }

// PurchaseDetailPage: GET /purchases/{id}
func (h *Handler) PurchaseDetailPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) || !h.hasPermRec(r, permPurchaseView, refPurchase, id) {
		http.Redirect(w, r, "/purchases", http.StatusFound)
		return
	}
	ctx := r.Context()
	p, err := h.loadPurchase(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/purchases?err="+url.QueryEscape("Bestellvorgang nicht gefunden"), http.StatusFound)
		return
	}
	q := r.URL.Query()
	d := PurchaseDetailData{BaseData: h.baseData(r, "purchase", p.Number, "Bestellvorgänge"), P: p,
		Tab: q.Get("tab"), Message: q.Get("msg"), Error: q.Get("err"),
		CanEdit:    !p.Final() && h.hasPermRec(r, permPurchaseEdit, refPurchase, id),
		CanReceive: !p.Final() && p.Status != "quote" && h.hasPermRec(r, permPurchaseReceive, refPurchase, id),
		CanInvoice: !p.Final() && p.Status != "quote" && h.hasPermRec(r, permPurchaseInvoice, refPurchase, id),
		CanDelete:  h.hasPermRec(r, "procurement.admin", refPurchase, id),
		Today:      time.Now().Format("2006-01-02"),
		History:    h.recordHistory(ctx, refPurchase, id),
	}
	for _, it := range p.Items {
		if it.Open() > 0 {
			d.HasOpen = true
		}
	}
	d.Storage, _ = h.storagePaths(ctx)
	if d.CanEdit {
		d.Suppliers = h.supplierOptions(ctx)
		d.Departments = h.loadDepartments(ctx)
		if rows, err := h.db.Query(ctx, `SELECT id::text, part_number, name, unit FROM spare_parts WHERE active AND hidden_at IS NULL ORDER BY part_number LIMIT 5000`); err == nil {
			for rows.Next() {
				var o purchasePartOption
				if rows.Scan(&o.ID, &o.No, &o.Name, &o.Unit) == nil {
					d.Parts = append(d.Parts, o)
				}
			}
			rows.Close()
		}
	}
	h.render(w, "purchase_detail", d)
}

func purchaseBack(w http.ResponseWriter, r *http.Request, id, tab, msg string, err error) {
	target := "/purchases/" + id
	v := url.Values{}
	if tab != "" {
		v.Set("tab", tab)
	}
	if err != nil {
		v.Set("err", friendlyPartError(err))
	} else if msg != "" {
		v.Set("msg", msg)
	}
	if len(v) > 0 {
		target += "?" + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// PurchaseCreateWeb: POST /purchases – neuer Vorgang (Lieferant, Titel, optional ein Ersatzteil).
func (h *Handler) PurchaseCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permPurchaseEdit) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	u := getUser(r)
	supplier := strings.TrimSpace(r.FormValue("supplier_id"))
	title := strings.TrimSpace(r.FormValue("title"))
	partID := strings.TrimSpace(r.FormValue("part_id"))
	// Abteilung: die der Person, wenn sie das Recht nur dort hat
	var dept string
	if all, depts := h.rbac.Scope(u.ID, string(u.Role), permPurchaseEdit); !all && len(depts) > 0 {
		dept = depts[0]
	}
	var id string
	err := h.db.QueryRow(ctx, `INSERT INTO purchase_orders (number, title, supplier_id, department_id, created_by)
		VALUES ('B-' || to_char(NOW(), 'YYYY') || '-' || lpad(nextval('purchase_order_seq')::text, 4, '0'), $1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4::uuid)
		RETURNING id::text`, truncateRunes(title, 200), supplier, dept, nullID(u.ID)).Scan(&id)
	if err != nil {
		http.Redirect(w, r, "/purchases?err="+url.QueryEscape(friendlyPartError(err)), http.StatusSeeOther)
		return
	}
	if partID != "" {
		h.addPurchasePart(ctx, id, supplier, partID, 1)
	}
	h.addHistory(ctx, refPurchase, id, "create", "", "", "", "Bestellvorgang angelegt", u.ID)
	purchaseBack(w, r, id, "", "Bestellvorgang angelegt – Positionen und Angebot erfassen", nil)
}

// addPurchasePart: Ersatzteil als Position anlegen (Lieferanten-Artikelnummer und Preis aus dem Teil).
func (h *Handler) addPurchasePart(ctx context.Context, orderID, supplierID, partID string, qty float64) {
	_, _ = h.db.Exec(ctx, `INSERT INTO purchase_order_items (order_id, pos, part_id, description, supplier_part_no, unit, qty_quoted, price_quoted, storage_node_id)
		SELECT $1::uuid, COALESCE((SELECT MAX(pos) FROM purchase_order_items WHERE order_id = $1::uuid), 0) + 1, sp.id, sp.name,
			COALESCE(s.supplier_part_no, ''), COALESCE(NULLIF(sp.unit, ''), 'Stk'), $4, COALESCE(s.price, sp.price),
			(SELECT st.storage_node_id FROM spare_part_stock st WHERE st.part_id = sp.id ORDER BY st.qty DESC LIMIT 1)
		FROM spare_parts sp LEFT JOIN spare_part_suppliers s ON s.part_id = sp.id AND s.partner_id = NULLIF($3, '')::uuid
		WHERE sp.id = $2::uuid`, orderID, partID, supplierID, qty)
}

func purchaseNum(s string) *float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// PurchaseSaveWeb: POST /purchases/{id} – Kopf, Positionen und Schritte.
//
//	action: save | quote_to_order | order | ab_equal | cancel | reopen
func (h *Handler) PurchaseSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	u := getUser(r)
	if !uuidInPathRe.MatchString(id) || !h.hasPermRec(r, permPurchaseEdit, refPurchase, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		purchaseBack(w, r, id, "", "", err)
		return
	}
	old, err := h.loadPurchase(ctx, id)
	if err != nil {
		purchaseBack(w, r, id, "", "", err)
		return
	}
	action := r.FormValue("action")
	if old.Final() && action != "reopen" {
		purchaseBack(w, r, id, "", "", errors.New("Der Vorgang ist abgeschlossen – erst wieder öffnen"))
		return
	}
	dateOrNil := func(k string) any {
		if t, err := parseOptDate(r.FormValue(k)); err == nil && t != nil {
			return *t
		}
		return nil
	}
	msg := "Gespeichert"
	err = pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		switch action {
		case "cancel":
			msg = "Bestellvorgang storniert"
			_, err := tx.Exec(ctx, `UPDATE purchase_orders SET status = 'cancelled', updated_at = NOW() WHERE id = $1::uuid`, id)
			return err
		case "reopen":
			msg = "Bestellvorgang wieder geöffnet"
			if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET status = 'quote', invoice_ok_at = NULL, invoice_ok_by = NULL WHERE id = $1::uuid`, id); err != nil {
				return err
			}
			return refreshPurchaseStatus(ctx, txExec{tx}, id)
		}
		dept := strings.TrimSpace(r.FormValue("department_id"))
		if dept != "" && !h.mayGrantIn(r, permPurchaseEdit, dept) && !h.hasPermIn(r, permPurchaseEdit, []string{dept}) {
			return errors.New("Diese Abteilung kannst du nicht wählen")
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET title = $2, supplier_id = NULLIF($3, '')::uuid, department_id = NULLIF($4, '')::uuid,
			wanted_date = $5, storage_node_id = NULLIF($6, '')::uuid, quote_no = $7, quote_date = $8, quote_valid_until = $9,
			order_no = $10, confirmation_no = $11, confirmation_date = $12, confirmed_delivery = $13, notes = $14, updated_at = NOW()
			WHERE id = $1::uuid`, id, truncateRunes(strings.TrimSpace(r.FormValue("title")), 200), r.FormValue("supplier_id"), dept,
			dateOrNil("wanted_date"), r.FormValue("storage_node_id"), truncateRunes(strings.TrimSpace(r.FormValue("quote_no")), 80),
			dateOrNil("quote_date"), dateOrNil("quote_valid_until"), truncateRunes(strings.TrimSpace(r.FormValue("order_no")), 80),
			truncateRunes(strings.TrimSpace(r.FormValue("confirmation_no")), 80), dateOrNil("confirmation_date"), dateOrNil("confirmed_delivery"),
			strings.TrimSpace(r.FormValue("notes"))); err != nil {
			return err
		}
		if err := h.savePurchaseItems(ctx, tx, r, id); err != nil {
			return err
		}
		switch action {
		case "quote_to_order":
			msg = "Angebot in die Bestellung übernommen"
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_items SET qty_ordered = COALESCE(qty_ordered, qty_quoted), price_ordered = COALESCE(price_ordered, price_quoted) WHERE order_id = $1::uuid`, id); err != nil {
				return err
			}
		case "order":
			msg = "Als bestellt markiert"
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_items SET qty_ordered = COALESCE(qty_ordered, qty_quoted), price_ordered = COALESCE(price_ordered, price_quoted) WHERE order_id = $1::uuid`, id); err != nil {
				return err
			}
			var missing int
			_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_order_items WHERE order_id = $1::uuid AND COALESCE(qty_ordered, 0) <= 0`, id).Scan(&missing)
			var n int
			_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_order_items WHERE order_id = $1::uuid`, id).Scan(&n)
			if n == 0 || missing > 0 {
				return errors.New("Zum Bestellen braucht jede Position eine Bestellmenge")
			}
			if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET ordered_at = COALESCE(ordered_at, NOW()), ordered_by = COALESCE(ordered_by, $2::uuid),
				order_no = CASE WHEN order_no = '' THEN number ELSE order_no END WHERE id = $1::uuid`, id, nullID(u.ID)); err != nil {
				return err
			}
		case "ab_equal":
			msg = "AB wie Bestellung übernommen"
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_items SET qty_confirmed = qty_ordered, price_confirmed = price_ordered WHERE order_id = $1::uuid`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET confirmation_date = COALESCE(confirmation_date, CURRENT_DATE) WHERE id = $1::uuid`, id); err != nil {
				return err
			}
		}
		return refreshPurchaseStatus(ctx, txExec{tx}, id)
	})
	if err == nil {
		if n, e := h.loadPurchase(ctx, id); e == nil {
			if n.Status != old.Status {
				h.addHistory(ctx, refPurchase, id, "update", "Status", old.StatusLabel, n.StatusLabel, msg, u.ID)
			} else {
				h.addHistory(ctx, refPurchase, id, "update", "", "", "", msg, u.ID)
			}
			if action == "ab_equal" || r.FormValue("confirmation_no") != old.ConfirmationNo {
				for _, wn := range n.Warnings {
					if strings.HasPrefix(wn, "Auftragsbestätigung") || strings.HasPrefix(wn, "Liefertermin") {
						h.addHistory(ctx, refPurchase, id, "update", "Prüfung AB", "", "", wn, u.ID)
					}
				}
			}
		}
	}
	purchaseBack(w, r, id, r.FormValue("tab"), msg, err)
}

// qtySQL: Menge als Text ohne überflüssige Nachkommastellen, deutsches Komma.
func qtySQL(expr string) string {
	return `replace(rtrim(rtrim((` + expr + `)::numeric(14,3)::text, '0'), '.'), '.', ',')`
}

type pgconnTag = pgconn.CommandTag

// txExec: pgx.Tx mit der Exec-Signatur für refreshPurchaseStatus.
type txExec struct{ tx pgx.Tx }

func (t txExec) Exec(ctx context.Context, sql string, args ...any) (pgconnTag, error) {
	return t.tx.Exec(ctx, sql, args...)
}

// savePurchaseItems: Positionen aus dem Formular (parallele Felder je Zeile).
// Zeilen ohne Teil und ohne Beschreibung werden ignoriert; fehlende Zeilen
// gelöscht, sofern noch nichts darauf geliefert wurde.
func (h *Handler) savePurchaseItems(ctx context.Context, tx pgx.Tx, r *http.Request, orderID string) error {
	f := r.Form
	ids := f["item_id"]
	if _, sent := f["item_id"]; !sent {
		return nil
	}
	get := func(k string, i int) string {
		if v := f[k]; i < len(v) {
			return strings.TrimSpace(v[i])
		}
		return ""
	}
	keep := []string{}
	for i := range ids {
		itemID, partNo, desc := get("item_id", i), get("item_part", i), get("item_desc", i)
		var partID string
		if partNo != "" {
			_ = tx.QueryRow(ctx, `SELECT id::text FROM spare_parts WHERE part_number = $1 OR id::text = $1 LIMIT 1`, partNo).Scan(&partID)
			if partID == "" {
				return fmt.Errorf("Ersatzteil „%s“ nicht gefunden", partNo)
			}
		}
		if partID == "" && desc == "" {
			continue
		}
		args := []any{partID, truncateRunes(desc, 300), truncateRunes(get("item_supno", i), 100), truncateRunes(firstNonEmpty(get("item_unit", i), "Stk"), 20),
			purchaseNum(get("item_qq", i)), purchaseNum(get("item_pq", i)), purchaseNum(get("item_qo", i)), purchaseNum(get("item_po", i)),
			purchaseNum(get("item_qc", i)), purchaseNum(get("item_pc", i)), get("item_storage", i), i + 1}
		if itemID != "" {
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_items SET part_id = NULLIF($1, '')::uuid, description = $2, supplier_part_no = $3, unit = $4,
				qty_quoted = $5, price_quoted = $6, qty_ordered = $7, price_ordered = $8, qty_confirmed = $9, price_confirmed = $10,
				storage_node_id = NULLIF($11, '')::uuid, pos = $12 WHERE id = $13::uuid AND order_id = $14::uuid`,
				append(args, itemID, orderID)...); err != nil {
				return err
			}
			keep = append(keep, itemID)
			continue
		}
		var newID string
		if err := tx.QueryRow(ctx, `INSERT INTO purchase_order_items (part_id, description, supplier_part_no, unit, qty_quoted, price_quoted,
			qty_ordered, price_ordered, qty_confirmed, price_confirmed, storage_node_id, pos, order_id)
			VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, '')::uuid, $12, $13::uuid) RETURNING id::text`,
			append(args, orderID)...).Scan(&newID); err != nil {
			return err
		}
		keep = append(keep, newID)
	}
	var delivered int
	_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_order_items i WHERE i.order_id = $1::uuid AND NOT (i.id::text = ANY($2))
		AND EXISTS (SELECT 1 FROM purchase_delivery_items di WHERE di.item_id = i.id)`, orderID, keep).Scan(&delivered)
	if delivered > 0 {
		return errors.New("Positionen mit Lieferung können nicht entfernt werden")
	}
	_, err := tx.Exec(ctx, `DELETE FROM purchase_order_items WHERE order_id = $1::uuid AND NOT (id::text = ANY($2))`, orderID, keep)
	return err
}


// PurchaseDeliveryWeb: POST /purchases/{id}/delivery – Wareneingang (auch
// Teillieferung) erfassen und ins Lager buchen.
func (h *Handler) PurchaseDeliveryWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	u := getUser(r)
	if !uuidInPathRe.MatchString(id) || !h.hasPermRec(r, permPurchaseReceive, refPurchase, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		purchaseBack(w, r, id, "delivery", "", err)
		return
	}
	p, err := h.loadPurchase(ctx, id)
	if err != nil {
		purchaseBack(w, r, id, "delivery", "", err)
		return
	}
	if p.Final() || p.Status == "quote" {
		purchaseBack(w, r, id, "delivery", "", errors.New("Wareneingang erst nach der Bestellung und vor dem Abschluss"))
		return
	}
	type line struct {
		item    purchaseItemView
		qty     float64
		storage string
	}
	var lines []line
	for _, it := range p.Items {
		q := purchaseNum(r.FormValue("recv_" + it.ID))
		if q == nil || *q <= 0 {
			continue
		}
		st := firstNonEmpty(strings.TrimSpace(r.FormValue("recv_storage_"+it.ID)), it.StorageID, p.StorageID)
		if it.PartID != "" && st == "" {
			purchaseBack(w, r, id, "delivery", "", fmt.Errorf("Für %s fehlt der Lagerort", firstNonEmpty(it.Description, it.PartName)))
			return
		}
		lines = append(lines, line{it, *q, st})
	}
	if len(lines) == 0 {
		purchaseBack(w, r, id, "delivery", "", errors.New("Bitte bei mindestens einer Position eine Menge eintragen"))
		return
	}
	noteNo := truncateRunes(strings.TrimSpace(r.FormValue("delivery_note_no")), 80)
	var on any = time.Now()
	if t, err := parseOptDate(r.FormValue("delivered_on")); err == nil && t != nil {
		on = *t
	}
	var deliveryID string
	if err := h.db.QueryRow(ctx, `INSERT INTO purchase_deliveries (order_id, delivery_note_no, delivered_on, notes, received_by)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid) RETURNING id::text`, id, noteNo, on, strings.TrimSpace(r.FormValue("notes")), nullID(u.ID)).Scan(&deliveryID); err != nil {
		purchaseBack(w, r, id, "delivery", "", err)
		return
	}
	ref := "Wareneingang " + p.Number
	if noteNo != "" {
		ref += " · LS " + noteNo
	}
	booked, failed := 0, []string{}
	for _, l := range lines {
		isBooked := false
		if l.item.PartID != "" && h.inv != nil {
			if _, err := h.inv.Book(ctx, &inventory.BookMovementInput{PartID: l.item.PartID, Type: inventory.MovementIn, Qty: l.qty,
				StorageNodeID: l.storage, Reference: truncateRunes(ref, 250)}, u.ID); err != nil {
				failed = append(failed, firstNonEmpty(l.item.PartName, l.item.Description)+": "+err.Error())
			} else {
				isBooked = true
				booked++
				h.kanbanReceive(ctx, l.item.PartID, l.qty, u.ID)
				h.addHistory(ctx, "spare_part", l.item.PartID, "update", "Wareneingang", "", formatQty(l.qty)+" "+l.item.Unit, ref, u.ID)
			}
		}
		_, _ = h.db.Exec(ctx, `INSERT INTO purchase_delivery_items (delivery_id, item_id, qty, storage_node_id, booked)
			VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, '')::uuid, $5)`, deliveryID, l.item.ID, l.qty, l.storage, isBooked)
	}
	_ = refreshPurchaseStatus(ctx, h.db, id)
	n, _ := h.loadPurchase(ctx, id)
	msg := fmt.Sprintf("Wareneingang erfasst – %d Position(en), %d ins Lager gebucht", len(lines), booked)
	if n != nil {
		h.addHistory(ctx, refPurchase, id, "update", "Status", p.StatusLabel, n.StatusLabel, msg+firstNonEmpty(map[bool]string{true: " (LS " + noteNo + ")"}[noteNo != ""]), u.ID)
		for _, it := range n.Items {
			if it.DeliveryWarn != "" && strings.HasPrefix(it.DeliveryWarn, "Über") {
				h.addHistory(ctx, refPurchase, id, "update", "Prüfung Lieferung", "", "", firstNonEmpty(it.Description, it.PartName)+": "+it.DeliveryWarn, u.ID)
			}
		}
	}
	if len(failed) > 0 {
		purchaseBack(w, r, id, "delivery", "", errors.New(msg+". Nicht gebucht: "+strings.Join(failed, "; ")))
		return
	}
	purchaseBack(w, r, id, "delivery", msg, nil)
}

// PurchaseInvoiceWeb: POST /purchases/{id}/invoice – Rechnung OK (schließt den Vorgang).
func (h *Handler) PurchaseInvoiceWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	u := getUser(r)
	if !uuidInPathRe.MatchString(id) || !h.hasPermRec(r, permPurchaseInvoice, refPurchase, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	p, err := h.loadPurchase(ctx, id)
	if err != nil || p.Final() || p.Status == "quote" {
		purchaseBack(w, r, id, "invoice", "", errors.New("Rechnung OK geht erst nach der Bestellung"))
		return
	}
	invRef := truncateRunes(strings.TrimSpace(r.FormValue("invoice_ref")), 120)
	if _, err := h.db.Exec(ctx, `UPDATE purchase_orders SET invoice_ref = $2, invoice_note = $3, invoice_ok_at = NOW(), invoice_ok_by = $4::uuid, status = 'closed', updated_at = NOW()
		WHERE id = $1::uuid`, id, invRef, strings.TrimSpace(r.FormValue("invoice_note")), nullID(u.ID)); err != nil {
		purchaseBack(w, r, id, "invoice", "", err)
		return
	}
	msg := "Rechnung OK – Vorgang abgeschlossen"
	if invRef == "" {
		msg += " (Rechnung in ELO)"
	}
	if p.Status != "delivered" {
		msg += " – Hinweis: noch nicht vollständig geliefert"
	}
	h.addHistory(ctx, refPurchase, id, "update", "Status", p.StatusLabel, "abgeschlossen", msg, u.ID)
	purchaseBack(w, r, id, "", msg, nil)
}

// PurchaseDeleteWeb: POST /purchases/{id}/delete – nur Bereichsadministrator
// Beschaffung; gebuchte Lagerbewegungen bleiben bestehen.
func (h *Handler) PurchaseDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) || !h.hasPermRec(r, "procurement.admin", refPurchase, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM purchase_orders WHERE id = $1::uuid`, id); err != nil {
		purchaseBack(w, r, id, "", "", err)
		return
	}
	_, _ = h.db.Exec(r.Context(), `DELETE FROM record_history WHERE ref_type = $1 AND ref_id = $2::uuid`, refPurchase, id)
	http.Redirect(w, r, "/purchases?msg="+url.QueryEscape("Bestellvorgang gelöscht"), http.StatusSeeOther)
}

// ── Verknüpfung bei Ersatzteil und Lieferant ─────────────────

type purchaseLinkView struct {
	ID, Number, Title, Supplier, Status, StatusLabel, StatusClass, Date, Qty string
}

// purchasesFor: Bestellvorgänge eines Ersatzteils (partID) oder Lieferanten (supplierID).
func (h *Handler) purchasesFor(ctx context.Context, partID, supplierID string) []purchaseLinkView {
	if h.db == nil {
		return nil
	}
	cond, arg := `o.supplier_id = $1::uuid`, supplierID
	if partID != "" {
		cond, arg = `EXISTS (SELECT 1 FROM purchase_order_items i WHERE i.order_id = o.id AND i.part_id = $1::uuid)`, partID
	}
	rows, err := h.db.Query(ctx, `SELECT o.id::text, o.number, o.title, COALESCE(bp.name, ''), o.status, COALESCE(o.ordered_at, o.created_at),
		COALESCE((SELECT string_agg(` + qtySQL("COALESCE(i.qty_confirmed, i.qty_ordered, i.qty_quoted, 0)") + ` || ' ' || i.unit, ', ')
			FROM purchase_order_items i WHERE i.order_id = o.id AND i.part_id = NULLIF($2, '')::uuid), '')
		FROM purchase_orders o LEFT JOIN business_partners bp ON bp.id = o.supplier_id
		WHERE `+cond+` ORDER BY o.created_at DESC LIMIT 50`, arg, partID)
	if err != nil {
		componentLog("bestellung").Warn().Err(err).Msg("verknüpfte bestellvorgänge")
		return nil
	}
	defer rows.Close()
	var out []purchaseLinkView
	for rows.Next() {
		var v purchaseLinkView
		var at time.Time
		if rows.Scan(&v.ID, &v.Number, &v.Title, &v.Supplier, &v.Status, &at, &v.Qty) == nil {
			l := purchaseStatusLabels[v.Status]
			v.StatusLabel, v.StatusClass, v.Date = l[0], l[1], deDate(at)
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := out[i].Status == "closed" || out[i].Status == "cancelled", out[j].Status == "closed" || out[j].Status == "cancelled"
		return !fi && fj
	})
	return out
}
