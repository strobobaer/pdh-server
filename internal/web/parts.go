package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/internal/modules/inventory"
)

// Ersatzteilstamm: Liste mit Kategorie-Reitern und Detailseite mit Reitern
// (Uebersicht, Stammdaten, Feldsaetze, Lager & Bewegungen, Lieferanten &
// Einkauf, Verwendung, Dokumente, Historie). Hersteller und Hauptlieferant
// werden gleichwertig gepflegt und nebeneinander dargestellt.

// Teilebild: die Liste speichert die URL bisher als Zeile im Beschreibungstext.
var partImageLineRe = regexp.MustCompile(`(?m)^PDH_IMAGE_URL:\s*(\S+)\s*$\n?`)

func splitPartImage(desc string) (clean, image string) {
	if m := partImageLineRe.FindStringSubmatch(desc); m != nil {
		image = m[1]
	}
	return strings.TrimSpace(partImageLineRe.ReplaceAllString(desc, "")), image
}

func joinPartImage(desc, image string) string {
	desc = strings.TrimSpace(desc)
	if image = strings.TrimSpace(image); image == "" {
		return desc
	}
	if desc == "" {
		return "PDH_IMAGE_URL: " + image
	}
	return desc + "\nPDH_IMAGE_URL: " + image
}

func partStatus(stock, min, critical float64) (key, label, class string) {
	switch {
	case stock <= 0:
		return "empty", "Leer", "b-red"
	case stock <= critical:
		return "critical", "Kritisch", "b-red"
	case stock <= min:
		return "low", "Niedrig", "b-amber"
	}
	return "ok", "OK", "b-green"
}

func (h *Handler) canEditParts(r *http.Request) bool { return h.hasPerm(r, "inventory.edit") }

// storagePaths liefert alle aktiven Lagerplaetze mit vollem Pfad.
func (h *Handler) storagePaths(ctx context.Context) ([]partnerOption, map[string]string) {
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE t AS (
			SELECT id, name::text AS path FROM storage_nodes WHERE parent_id IS NULL AND active
			UNION ALL
			SELECT n.id, t.path || ' › ' || n.name FROM storage_nodes n JOIN t ON n.parent_id = t.id WHERE n.active
		) SELECT id::text, path FROM t ORDER BY path`)
	byID := map[string]string{}
	if err != nil {
		return nil, byID
	}
	defer rows.Close()
	var list []partnerOption
	for rows.Next() {
		var o partnerOption
		if rows.Scan(&o.Value, &o.Label) == nil {
			list = append(list, o)
			byID[o.Value] = o.Label
		}
	}
	return list, byID
}

// ── Liste ────────────────────────────────────────────────────

type PartListRow struct {
	ID, PartNumber, Name, Category, ImageURL                 string
	ManufacturerID, ManufacturerName, SupplierID, Supplier   string
	Stock, MinQty, Unit, StatusKey, StatusLabel, StatusClass string
	Locations                                                int
	BuyerName                                                string
}

type partCategoryTab struct {
	Key, Label string
	Count      int
}

type InventoryListData struct {
	BaseData
	Rows          []PartListRow
	Tabs          []partCategoryTab
	Category      string
	Status        string
	Query         string
	Total         int
	LowStock      int
	Critical      int
	Empty         int
	NoSupplier    int
	TotalValue    string
	CanEdit       bool
	Categories    []string
	LowStockParts []PartView // Kontextleiste
	Error         string
}

const partNoCategory = "__none"

func (h *Handler) Inventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	data := InventoryListData{
		BaseData: h.baseData(r, "inventory", "Ersatzteillager", "Unter Mindestbestand"),
		Category: q.Get("cat"), Status: q.Get("status"), Query: strings.TrimSpace(q.Get("q")),
		CanEdit: h.canEditParts(r), Error: q.Get("err"),
	}
	var value float64
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE stock_qty <= min_qty AND stock_qty > critical_qty AND stock_qty > 0),
		       COUNT(*) FILTER (WHERE stock_qty <= critical_qty AND stock_qty > 0),
		       COUNT(*) FILTER (WHERE stock_qty <= 0),
		       COUNT(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM spare_part_suppliers s WHERE s.part_id = sp.id)),
		       COALESCE(SUM(stock_qty * price), 0)::float8
		FROM spare_parts sp WHERE active`).Scan(&data.Total, &data.LowStock, &data.Critical, &data.Empty, &data.NoSupplier, &value)
	data.TotalValue = formatEuro(value)

	if rows, err := h.db.Query(ctx, `
		SELECT COALESCE(category, ''), COUNT(*) FROM spare_parts WHERE active
		GROUP BY 1 ORDER BY (COALESCE(category, '') = ''), lower(COALESCE(category, ''))`); err == nil {
		for rows.Next() {
			var t partCategoryTab
			if rows.Scan(&t.Key, &t.Count) == nil {
				t.Label = t.Key
				if t.Key == "" {
					t.Key, t.Label = partNoCategory, "Ohne Kategorie"
				} else {
					data.Categories = append(data.Categories, t.Key)
				}
				data.Tabs = append(data.Tabs, t)
			}
		}
		rows.Close()
	}

	where := []string{"sp.active"}
	var args []interface{}
	add := func(cond string, v interface{}) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	switch data.Category {
	case "":
	case partNoCategory:
		where = append(where, "COALESCE(sp.category, '') = ''")
	default:
		add("sp.category = $?", data.Category)
	}
	switch data.Status {
	case "low":
		where = append(where, "sp.stock_qty <= sp.min_qty")
	case "critical":
		where = append(where, "sp.stock_qty <= sp.critical_qty")
	case "empty":
		where = append(where, "sp.stock_qty <= 0")
	case "nosupplier":
		where = append(where, "sup.partner_id IS NULL")
	case "inactive": // deaktiviert/vorgemerkt, aber nicht vom Bereinigungslauf ausgeblendet
		where[0] = "NOT sp.active AND sp.hidden_at IS NULL"
	case "locked":
		where = append(where, "sp.locked_at IS NOT NULL")
	default:
		data.Status = ""
	}
	if data.Query != "" {
		add(`(sp.part_number || ' ' || sp.name || ' ' || COALESCE(sp.category, '') || ' ' || COALESCE(sp.manufacturer_part, '') || ' ' ||
		      COALESCE(mf.name, sp.manufacturer, '') || ' ' || COALESCE(sb.name, '') || ' ' || COALESCE(sup.supplier_part_no, '')) ILIKE $?`,
			likePattern(data.Query))
	}
	rows, err := h.db.Query(ctx, `
		SELECT sp.id::text, sp.part_number, sp.name, COALESCE(sp.category, ''), COALESCE(sp.description, ''),
		       COALESCE(sp.manufacturer_id::text, ''), COALESCE(NULLIF(mf.name, ''), sp.manufacturer, ''),
		       COALESCE(sup.partner_id::text, ''), COALESCE(sb.name, ''),
		       sp.stock_qty::float8, sp.min_qty::float8, sp.critical_qty::float8, sp.unit,
		       (SELECT COUNT(*) FROM spare_part_stock st WHERE st.part_id = sp.id AND st.qty <> 0),
		       COALESCE(TRIM(bu.first_name || ' ' || bu.last_name), '')
		FROM spare_parts sp
		LEFT JOIN business_partners mf ON mf.id = sp.manufacturer_id
		LEFT JOIN LATERAL (SELECT s.partner_id, s.supplier_part_no FROM spare_part_suppliers s
		                   WHERE s.part_id = sp.id ORDER BY s.preferred DESC, s.price NULLS LAST LIMIT 1) sup ON true
		LEFT JOIN business_partners sb ON sb.id = sup.partner_id
		LEFT JOIN users bu ON bu.id = sp.buyer_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY sp.name LIMIT 2000`, args...)
	if err != nil {
		data.Error = err.Error()
	} else {
		for rows.Next() {
			var p PartListRow
			var desc string
			var stock, min, crit float64
			if rows.Scan(&p.ID, &p.PartNumber, &p.Name, &p.Category, &desc, &p.ManufacturerID, &p.ManufacturerName,
				&p.SupplierID, &p.Supplier, &stock, &min, &crit, &p.Unit, &p.Locations, &p.BuyerName) != nil {
				continue
			}
			_, p.ImageURL = splitPartImage(desc)
			p.Stock, p.MinQty = formatQty(stock), formatQty(min)
			p.StatusKey, p.StatusLabel, p.StatusClass = partStatus(stock, min, crit)
			data.Rows = append(data.Rows, p)
			if p.StatusKey != "ok" && len(data.LowStockParts) < 15 {
				data.LowStockParts = append(data.LowStockParts, PartView{ID: p.ID, Name: p.Name, StockQty: p.Stock,
					StatusDot: map[string]string{"low": "d-amber", "critical": "d-red", "empty": "d-red"}[p.StatusKey]})
			}
		}
		rows.Close()
	}
	h.render(w, "inventory", data)
}

// ── Detail ───────────────────────────────────────────────────

type PartMaster struct {
	ID, PartNumber, Name, Description, ImageURL, Category, Unit string
	ManufacturerID, ManufacturerName, ManufacturerLegacy        string
	ManufacturerPart                                            string
	MinQty, CriticalQty, ReorderQty, Price, StockQty            float64
	InfraID, InfraName, BuyerID, BuyerName                      string
	StatusKey, StatusLabel, StatusClass                         string
	CreatedAt, UpdatedAt                                        string
}

// fmt-Helfer fuer Templates (Komma statt Punkt)
func (p PartMaster) Q(v float64) string { return formatQty(v) }
func (p PartMaster) E(v float64) string { return formatEuro(v) }

// Eingabewert fuer number-Felder (Punkt als Dezimaltrenner)
func (p PartMaster) In(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

type partnerContact struct {
	ID, Name, Phone, Email, Website, Hotline string
}

type PartStockRow struct {
	NodeID, Path, Qty string
}

type PartMovementRow struct {
	Date, TypeKey, TypeLabel, Qty, Before, After, Path, User, Reference, Notes string
	RecordURL, RecordLabel                                                     string
}

type PartUsageRow struct {
	ModuleLabel, Title, URL, Qty, Date string
}

type PartDetailData struct {
	BaseData
	Tab, Message, Error  string
	CanEdit, CanPartners bool
	CanPurchasing        bool
	Part                 PartMaster
	Manufacturer         *partnerContact
	MainSupplier         *PartSupplierView
	MainSupplierID       string
	MainSupplierContact  *partnerContact
	Suppliers            []PartSupplierView
	Stock                []PartStockRow
	StockValue           string
	Movements            []PartMovementRow
	Usage                []PartUsageRow
	Categories           []string
	Users                []UserOption
	SupplierOptions      []partnerOption
	StorageOptions       []partnerOption
	History              []HistoryView
}

var movementTypeLabels = map[string]string{
	"in": "Zugang", "out": "Abgang", "transfer": "Umlagerung", "correction": "Korrektur", "inventory": "Inventur",
}

func (h *Handler) loadPartMaster(ctx context.Context, id string) (PartMaster, error) {
	var p PartMaster
	var desc string
	var created, updated time.Time
	err := h.db.QueryRow(ctx, `
		SELECT sp.id::text, sp.part_number, sp.name, COALESCE(sp.description, ''), COALESCE(sp.category, ''), sp.unit,
		       COALESCE(sp.manufacturer_id::text, ''), COALESCE(mf.name, ''), COALESCE(sp.manufacturer, ''),
		       COALESCE(sp.manufacturer_part, ''),
		       sp.min_qty::float8, sp.critical_qty::float8, sp.reorder_qty::float8, sp.price::float8, sp.stock_qty::float8,
		       COALESCE(sp.infrastructure_id::text, ''), COALESCE(i.name, ''),
		       COALESCE(sp.buyer_id::text, ''), COALESCE(TRIM(bu.first_name || ' ' || bu.last_name), ''),
		       sp.created_at, sp.updated_at
		FROM spare_parts sp
		LEFT JOIN business_partners mf ON mf.id = sp.manufacturer_id
		LEFT JOIN infrastructure i ON i.id = sp.infrastructure_id
		LEFT JOIN users bu ON bu.id = sp.buyer_id
		WHERE sp.id = $1::uuid`, id).Scan(&p.ID, &p.PartNumber, &p.Name, &desc, &p.Category, &p.Unit,
		&p.ManufacturerID, &p.ManufacturerName, &p.ManufacturerLegacy, &p.ManufacturerPart,
		&p.MinQty, &p.CriticalQty, &p.ReorderQty, &p.Price, &p.StockQty,
		&p.InfraID, &p.InfraName, &p.BuyerID, &p.BuyerName, &created, &updated)
	if err != nil {
		return p, err
	}
	p.Description, p.ImageURL = splitPartImage(desc)
	p.StatusKey, p.StatusLabel, p.StatusClass = partStatus(p.StockQty, p.MinQty, p.CriticalQty)
	p.CreatedAt, p.UpdatedAt = created.Local().Format("02.01.2006"), updated.Local().Format("02.01.2006 15:04")
	return p, nil
}

func (h *Handler) partnerContactInfo(ctx context.Context, id string, forParts bool) *partnerContact {
	if id == "" {
		return nil
	}
	c := &partnerContact{ID: id}
	// Fuer Ersatzteile zuerst die Ersatzteil-Kontakte, sonst Zentrale
	phoneExpr, mailExpr := "phone", "email"
	if forParts {
		phoneExpr = "COALESCE(NULLIF(spare_parts_phone, ''), NULLIF(service_phone, ''), phone)"
		mailExpr = "COALESCE(NULLIF(spare_parts_email, ''), NULLIF(order_email, ''), email)"
	}
	if err := h.db.QueryRow(ctx, `SELECT name, `+phoneExpr+`, `+mailExpr+`, website, support_hotline FROM business_partners WHERE id = $1::uuid`, id).
		Scan(&c.Name, &c.Phone, &c.Email, &c.Website, &c.Hotline); err != nil {
		return nil
	}
	return c
}

func (h *Handler) PartDetailPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	p, err := h.loadPartMaster(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/inventory?err="+url.QueryEscape("Ersatzteil nicht gefunden"), http.StatusFound)
		return
	}
	q := r.URL.Query()
	data := PartDetailData{
		BaseData: h.baseData(r, "inventory", p.Name, "Ersatzteil"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Error: q.Get("err"),
		CanEdit: h.canEditParts(r), CanPartners: h.canEditPartners(r),
		Part: p,
	}
	data.CanPurchasing = data.CanEdit || data.CanPartners
	if data.Tab == "" {
		data.Tab = "overview"
	}
	data.Manufacturer = h.partnerContactInfo(ctx, p.ManufacturerID, true)
	data.Suppliers = h.partSuppliers(ctx, id)
	if len(data.Suppliers) > 0 {
		main := data.Suppliers[0] // bevorzugt, sonst guenstigster (Sortierung in partSuppliers)
		data.MainSupplier = &main
		data.MainSupplierID = main.PartnerID
		data.MainSupplierContact = h.partnerContactInfo(ctx, main.PartnerID, true)
	}

	storageOpts, paths := h.storagePaths(ctx)
	if rows, err := h.db.Query(ctx, `SELECT storage_node_id::text, qty::float8 FROM spare_part_stock WHERE part_id = $1::uuid AND qty <> 0`, id); err == nil {
		for rows.Next() {
			var s PartStockRow
			var qty float64
			if rows.Scan(&s.NodeID, &qty) == nil {
				s.Path, s.Qty = paths[s.NodeID], formatQty(qty)
				if s.Path == "" {
					s.Path = "(Lagerplatz nicht mehr aktiv)"
				}
				data.Stock = append(data.Stock, s)
			}
		}
		rows.Close()
	}
	data.StockValue = formatEuro(p.StockQty * p.Price)

	if rows, err := h.db.Query(ctx, `
		SELECT sm.created_at, sm.type::text, sm.qty::float8, sm.qty_before::float8, sm.qty_after::float8,
		       COALESCE(sm.storage_node_id::text, ''), COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
		       COALESCE(sm.reference, ''), COALESCE(sm.notes, ''),
		       CASE WHEN f.id IS NOT NULL THEN '/faults/' || f.id WHEN t.id IS NOT NULL THEN '/tickets/' || t.id
		            WHEN ta.id IS NOT NULL THEN '/tasks/' || ta.id WHEN m.id IS NOT NULL THEN '/maintenance/tasks/' || m.id ELSE '' END,
		       COALESCE('Störung: ' || f.title, 'Ticket: ' || t.title, 'Aufgabe: ' || ta.title, 'Wartung: ' || m.title, '')
		FROM stock_movements sm
		LEFT JOIN users u ON u.id = sm.created_by
		LEFT JOIN faults f ON f.id = sm.fault_id
		LEFT JOIN tickets t ON t.id = sm.ticket_id
		LEFT JOIN tasks ta ON ta.id = sm.task_id
		LEFT JOIN maintenance_tasks m ON m.id = sm.maintenance_task_id
		WHERE sm.part_id = $1::uuid ORDER BY sm.created_at DESC LIMIT 150`, id); err == nil {
		for rows.Next() {
			var mv PartMovementRow
			var at time.Time
			var qty, before, after float64
			var node string
			if rows.Scan(&at, &mv.TypeKey, &qty, &before, &after, &node, &mv.User, &mv.Reference, &mv.Notes, &mv.RecordURL, &mv.RecordLabel) != nil {
				continue
			}
			mv.Date = at.Local().Format("02.01.2006 15:04")
			mv.TypeLabel = movementTypeLabels[mv.TypeKey]
			mv.Qty, mv.Before, mv.After, mv.Path = formatQty(qty), formatQty(before), formatQty(after), paths[node]
			data.Movements = append(data.Movements, mv)
		}
		rows.Close()
	}

	if rows, err := h.db.Query(ctx, `
		SELECT label, id, title, SUM(net)::float8, MAX(at) FROM (
			SELECT 'Störung' AS label, '/faults/' || f.id AS id, f.title AS title,
			       CASE WHEN sm.type = 'out' THEN sm.qty ELSE -sm.qty END AS net, sm.created_at AS at
			FROM stock_movements sm JOIN faults f ON f.id = sm.fault_id WHERE sm.part_id = $1::uuid AND sm.type IN ('out', 'in')
			UNION ALL SELECT 'Ticket', '/tickets/' || t.id, t.title, CASE WHEN sm.type = 'out' THEN sm.qty ELSE -sm.qty END, sm.created_at
			FROM stock_movements sm JOIN tickets t ON t.id = sm.ticket_id WHERE sm.part_id = $1::uuid AND sm.type IN ('out', 'in')
			UNION ALL SELECT 'Aufgabe', '/tasks/' || ta.id, ta.title, CASE WHEN sm.type = 'out' THEN sm.qty ELSE -sm.qty END, sm.created_at
			FROM stock_movements sm JOIN tasks ta ON ta.id = sm.task_id WHERE sm.part_id = $1::uuid AND sm.type IN ('out', 'in')
			UNION ALL SELECT 'Wartung', '/maintenance/tasks/' || m.id, m.title, CASE WHEN sm.type = 'out' THEN sm.qty ELSE -sm.qty END, sm.created_at
			FROM stock_movements sm JOIN maintenance_tasks m ON m.id = sm.maintenance_task_id WHERE sm.part_id = $1::uuid AND sm.type IN ('out', 'in')
		) x GROUP BY label, id, title ORDER BY MAX(at) DESC LIMIT 200`, id); err == nil {
		for rows.Next() {
			var us PartUsageRow
			var qty float64
			var at time.Time
			if rows.Scan(&us.ModuleLabel, &us.URL, &us.Title, &qty, &at) == nil {
				us.Qty, us.Date = formatQty(qty), at.Local().Format("02.01.2006")
				data.Usage = append(data.Usage, us)
			}
		}
		rows.Close()
	}

	if rows, err := h.db.Query(ctx, `SELECT DISTINCT category FROM spare_parts WHERE category <> '' ORDER BY category`); err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				data.Categories = append(data.Categories, c)
			}
		}
		rows.Close()
	}
	if data.CanEdit || data.CanPurchasing {
		data.Users = h.userOptions(ctx)
		data.SupplierOptions = h.supplierOptions(ctx)
		data.StorageOptions = storageOpts
	}
	data.History = h.recordHistory(ctx, "spare_part", id)
	h.render(w, "inventory_detail", data)
}

func partRedirect(w http.ResponseWriter, r *http.Request, id, tab, msg string, err error) {
	target := "/inventory/" + id + "?tab=" + url.QueryEscape(tab)
	if err != nil {
		target += "&err=" + url.QueryEscape(friendlyPartError(err))
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func friendlyPartError(err error) string {
	s := err.Error()
	if strings.Contains(s, "idx_spare_partno") {
		return "Diese Teilenummer ist bereits vergeben"
	}
	return friendlyDBError(err)
}

func formFloat(r *http.Request, key string) (float64, error) {
	v, err := parseOptFloat(r.FormValue(key))
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

// PartMasterSaveWeb speichert die Stammdaten inkl. Hauptlieferant und
// protokolliert geaenderte Felder in der Historie.
func (h *Handler) PartMasterSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canEditParts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	r.ParseForm()
	old, err := h.loadPartMaster(ctx, id)
	if err != nil {
		partRedirect(w, r, id, "master", "", err)
		return
	}
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	n := PartMaster{
		PartNumber: v("part_number"), Name: v("name"), Category: v("category"), Unit: v("unit"),
		Description: v("description"), ImageURL: v("image_url"),
		ManufacturerID: v("manufacturer_id"), ManufacturerPart: v("manufacturer_part"),
		InfraID: v("infrastructure_id"), BuyerID: v("buyer_id"),
	}
	if n.Unit == "" {
		n.Unit = "Stück"
	}
	if n.PartNumber == "" || n.Name == "" {
		partRedirect(w, r, id, "master", "", errors.New("Teilenummer und Name sind Pflicht"))
		return
	}
	for key, dst := range map[string]*float64{"min_qty": &n.MinQty, "critical_qty": &n.CriticalQty, "reorder_qty": &n.ReorderQty, "price": &n.Price} {
		if *dst, err = formFloat(r, key); err != nil {
			partRedirect(w, r, id, "master", "", err)
			return
		}
		if *dst < 0 {
			partRedirect(w, r, id, "master", "", errors.New("Mengen und Preise dürfen nicht negativ sein"))
			return
		}
	}
	supply, err := supplyConditionsFromForm(r)
	if err != nil {
		partRedirect(w, r, id, "master", "", err)
		return
	}
	mainSupplier := v("main_supplier_id")

	tx, err := h.db.Begin(ctx)
	if err != nil {
		partRedirect(w, r, id, "master", "", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		UPDATE spare_parts SET part_number=$1, name=$2, category=$3, unit=$4, description=$5,
		       manufacturer_id=NULLIF($6, '')::uuid, manufacturer_part=$7, min_qty=$8, critical_qty=$9, reorder_qty=$10,
		       price=$11, infrastructure_id=NULLIF($12, '')::uuid, buyer_id=NULLIF($13, '')::uuid, updated_at=NOW()
		WHERE id=$14::uuid`,
		n.PartNumber, n.Name, n.Category, n.Unit, joinPartImage(n.Description, n.ImageURL),
		n.ManufacturerID, n.ManufacturerPart, n.MinQty, n.CriticalQty, n.ReorderQty, n.Price,
		n.InfraID, n.BuyerID, id); err != nil {
		partRedirect(w, r, id, "master", "", err)
		return
	}
	// Hauptlieferant = bevorzugter Lieferant mit Konditionen
	if mainSupplier != "" {
		if _, err = tx.Exec(ctx, `
			INSERT INTO spare_part_suppliers (part_id, partner_id, supplier_part_no, price, min_order_qty, lead_time_days, preferred, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, true, $7)
			ON CONFLICT (part_id, partner_id) DO UPDATE SET supplier_part_no = EXCLUDED.supplier_part_no, price = EXCLUDED.price,
			    min_order_qty = EXCLUDED.min_order_qty, lead_time_days = EXCLUDED.lead_time_days, preferred = true, updated_at = NOW()`,
			id, mainSupplier, supply.SupplierPartNo, supply.Price, supply.MinOrderQty, supply.LeadTimeDays, nullID(getUser(r).ID)); err == nil {
			_, err = tx.Exec(ctx, `UPDATE spare_part_suppliers SET preferred = false WHERE part_id = $1::uuid AND partner_id <> $2::uuid`, id, mainSupplier)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE spare_part_suppliers SET preferred = false WHERE part_id = $1::uuid`, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		partRedirect(w, r, id, "master", "", err)
		return
	}
	h.logPartChanges(ctx, r, id, old, n)
	partRedirect(w, r, id, "overview", "Stammdaten gespeichert", nil)
}

func (h *Handler) logPartChanges(ctx context.Context, r *http.Request, id string, old, n PartMaster) {
	type diff struct{ field, o, n string }
	changes := []diff{
		{"Teilenummer", old.PartNumber, n.PartNumber}, {"Name", old.Name, n.Name},
		{"Kategorie", old.Category, n.Category}, {"Einheit", old.Unit, n.Unit},
		{"Beschreibung", old.Description, n.Description}, {"Teilebild", old.ImageURL, n.ImageURL},
		{"Hersteller-Teilenummer", old.ManufacturerPart, n.ManufacturerPart},
		{"Mindestbestand", formatQty(old.MinQty), formatQty(n.MinQty)},
		{"Kritischer Bestand", formatQty(old.CriticalQty), formatQty(n.CriticalQty)},
		{"Nachbestellmenge", formatQty(old.ReorderQty), formatQty(n.ReorderQty)},
		{"Preis", formatEuro(old.Price), formatEuro(n.Price)},
		{"Hersteller", old.ManufacturerID, n.ManufacturerID}, {"Anlage", old.InfraID, n.InfraID},
		{"Einkäufer", old.BuyerID, n.BuyerID},
	}
	u := getUser(r)
	for _, c := range changes {
		if c.o == c.n {
			continue
		}
		_, _ = h.db.Exec(ctx, `
			INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, created_by, message)
			VALUES ('spare_part', $1::uuid, 'update', $2, $3, $4, $5, 'Stammdaten geändert')`,
			id, c.field, c.o, c.n, nullID(u.ID))
	}
}

// PartBookWeb bucht eine Lagerbewegung direkt aus der Detailseite.
func (h *Handler) PartBookWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.hasPerm(r, "inventory.edit") && !h.hasPerm(r, "inventory.view") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	qty, err := formFloat(r, "qty")
	if err == nil {
		_, err = h.inv.Book(r.Context(), &inventory.BookMovementInput{
			PartID: id, Type: inventory.MovementType(r.FormValue("type")), Qty: qty,
			StorageNodeID: strings.TrimSpace(r.FormValue("storage_node_id")),
			Reference:     strings.TrimSpace(r.FormValue("reference")), Notes: strings.TrimSpace(r.FormValue("notes")),
		}, getUser(r).ID)
	}
	partRedirect(w, r, id, "stock", fmt.Sprintf("Buchung erfasst (%s)", movementTypeLabels[r.FormValue("type")]), err)
}

// withTab haengt den aktiven Reiter an eine (ggf. schon parametrisierte) URL.
func withTab(target, tab string) string {
	if strings.Contains(target, "?") {
		return target + "&tab=" + url.QueryEscape(tab)
	}
	return target + "?tab=" + url.QueryEscape(tab)
}
