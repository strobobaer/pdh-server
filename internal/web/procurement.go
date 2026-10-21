package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Beschaffung & Einkauf (/procurement): Kanban-Tafel, Bestellvorschlag aus
// leeren Kanban-Karten (plus klassisch: Teile unter Mindestbestand ohne
// Kanban), offene Bestellungen und Rahmenverträge. Rechte: procurement.*.

type procurementGroup struct {
	SupplierID, SupplierName, SupplierEmail string
	Loops                                   []*kanbanLoopView
	Total                                   string
	MailTo                                  string
	total                                   float64
}

type contractView struct {
	ID, No, Title, SupplierID, SupplierName string
	ValidFrom, ValidUntil                   string // Anzeige
	FromInput, UntilInput                   string // yyyy-mm-dd
	TotalValue, CalledValue                 string
	TotalRaw, CalledRaw                     float64
	UsedPct                                 int
	Items                                   int
	Active                                  bool
	Notes                                   string
	StatusLabel, StatusClass                string
}

type contractItemView struct {
	ID, PartID, PartNumber, PartName, Unit, SupplierPartNo string
	Price, ContractQty, CalledQty                          float64
	UsedPct                                                int
	Kanban                                                 bool
}

func (c contractItemView) Q(v float64) string  { return formatQty(v) }
func (c contractItemView) E(v float64) string  { return formatEuro(v) }
func (c contractItemView) In(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

type ProcurementData struct {
	BaseData
	Tab                          string
	Loops                        []*kanbanLoopView
	Groups                       []*procurementGroup
	NoSupplier                   *procurementGroup
	Ordered                      []*kanbanLoopView
	Contracts                    []contractView
	MinStock                     []*PurchaseBuyerGroup
	CountFull, CountEmpty        int
	CountOrdered, CountLate      int
	ProposalValue                string
	CanKanban, CanOrder, CanEdit bool
	CanContracts                 bool
	Today                        string
	Message, Error               string
}

func (h *Handler) contractStatus(c *contractView, until *time.Time) {
	today := time.Now().Truncate(24 * time.Hour)
	switch {
	case !c.Active:
		c.StatusLabel, c.StatusClass = "inaktiv", "b-gray"
	case until != nil && until.Before(today):
		c.StatusLabel, c.StatusClass = "abgelaufen", "b-red"
	case c.TotalRaw > 0 && c.CalledRaw >= c.TotalRaw:
		c.StatusLabel, c.StatusClass = "ausgeschöpft", "b-red"
	case until != nil && until.Before(today.AddDate(0, 0, 31)):
		c.StatusLabel, c.StatusClass = "läuft bald ab", "b-amber"
	case c.TotalRaw > 0 && c.CalledRaw >= 0.8*c.TotalRaw:
		c.StatusLabel, c.StatusClass = "fast ausgeschöpft", "b-amber"
	default:
		c.StatusLabel, c.StatusClass = "aktiv", "b-green"
	}
}

const contractSelect = `
	SELECT fc.id::text, fc.contract_no, fc.title, fc.supplier_id::text, COALESCE(bp.name, ''), fc.valid_from, fc.valid_until,
	       fc.total_value::float8, COALESCE((SELECT SUM(i.called_qty * i.price) FROM framework_contract_items i WHERE i.contract_id = fc.id), 0)::float8,
	       (SELECT COUNT(*) FROM framework_contract_items i WHERE i.contract_id = fc.id), fc.active, fc.notes
	FROM framework_contracts fc LEFT JOIN business_partners bp ON bp.id = fc.supplier_id`

func (h *Handler) scanContract(row interface{ Scan(...any) error }) (contractView, error) {
	var c contractView
	var from, until *time.Time
	if err := row.Scan(&c.ID, &c.No, &c.Title, &c.SupplierID, &c.SupplierName, &from, &until, &c.TotalRaw, &c.CalledRaw, &c.Items, &c.Active, &c.Notes); err != nil {
		return c, err
	}
	if from != nil {
		c.ValidFrom, c.FromInput = from.Format("02.01.2006"), from.Format("2006-01-02")
	}
	if until != nil {
		c.ValidUntil, c.UntilInput = until.Format("02.01.2006"), until.Format("2006-01-02")
	}
	c.CalledValue = formatEuro(c.CalledRaw)
	if c.TotalRaw > 0 {
		c.TotalValue = formatEuro(c.TotalRaw)
		c.UsedPct = int(c.CalledRaw / c.TotalRaw * 100)
	}
	h.contractStatus(&c, until)
	return c, nil
}

func (h *Handler) contractsList(ctx context.Context, supplierID string, onlyActive bool) []contractView {
	where := []string{"TRUE"}
	args := []any{}
	if supplierID != "" {
		args = append(args, supplierID)
		where = append(where, fmt.Sprintf("fc.supplier_id = $%d::uuid", len(args)))
	}
	if onlyActive {
		where = append(where, "fc.active")
	}
	rows, err := h.db.Query(ctx, contractSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY fc.active DESC, bp.name, fc.contract_no`, args...)
	if err != nil {
		componentLog("beschaffung").Warn().Err(err).Msg("rahmenverträge laden")
		return nil
	}
	defer rows.Close()
	var out []contractView
	for rows.Next() {
		if c, err := h.scanContract(rows); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// ProcurementPage: GET /procurement?tab=board|proposal|orders|contracts
func (h *Handler) ProcurementPage(w http.ResponseWriter, r *http.Request) {
	if !h.canProcurement(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := ProcurementData{
		BaseData: h.baseData(r, "procurement", "Beschaffung & Einkauf", "Material"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Error: q.Get("err"),
		CanKanban: h.canKanbanSetup(r), CanOrder: h.canProcureOrder(r), CanContracts: h.canContracts(r),
		CanEdit: h.canEditParts(r), Today: time.Now().Format("2006-01-02"),
	}
	if d.Tab == "" {
		d.Tab = "board"
	}
	h.kanbanSync(ctx, "")
	loops, err := h.kanbanLoops(ctx, true)
	if err != nil {
		d.Error = friendlyDBError(err)
	}
	d.Loops = loops
	groups := map[string]*procurementGroup{}
	var total float64
	for _, k := range loops {
		d.CountFull += k.Full
		d.CountEmpty += k.Empty
		d.CountOrdered += k.Ordered
		for _, c := range k.Cards {
			if c.Late {
				d.CountLate++
			}
		}
		if k.Ordered > 0 {
			d.Ordered = append(d.Ordered, k)
		}
		if k.Empty == 0 {
			continue
		}
		key := k.SupplierID
		g := groups[key]
		if g == nil {
			g = &procurementGroup{SupplierID: k.SupplierID, SupplierName: k.SupplierName, SupplierEmail: k.SupplierEmail}
			if key == "" {
				g.SupplierName = "Ohne Lieferant"
			}
			groups[key] = g
		}
		g.Loops = append(g.Loops, k)
		v := float64(k.Empty) * k.CardQty * k.UnitPrice
		g.total += v
		total += v
	}
	for key, g := range groups {
		g.Total = formatEuro(g.total)
		g.MailTo = kanbanOrderMail(g)
		if key == "" {
			d.NoSupplier = g
			continue
		}
		d.Groups = append(d.Groups, g)
	}
	sort.Slice(d.Groups, func(i, j int) bool { return d.Groups[i].SupplierName < d.Groups[j].SupplierName })
	if d.NoSupplier != nil {
		d.Groups = append(d.Groups, d.NoSupplier)
	}
	d.ProposalValue = formatEuro(total)
	if d.Tab == "proposal" {
		d.MinStock, _ = h.buildPurchaseSuggestions(ctx)
	}
	d.Contracts = h.contractsList(ctx, "", false)
	h.render(w, "procurement", d)
}

// kanbanOrderMail: mailto-Link mit Bestelltext für einen Lieferanten.
func kanbanOrderMail(g *procurementGroup) string {
	if g.SupplierEmail == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Guten Tag,\n\nbitte liefern Sie uns:\n\n")
	for _, k := range g.Loops {
		fmt.Fprintf(&b, "- %s %s  %s (%s)", formatQty(float64(k.Empty)*k.CardQty), k.Unit, k.PartName, k.PartNumber)
		if k.ContractLabel != "" {
			fmt.Fprintf(&b, " – Rahmenvertrag %s", k.ContractLabel)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nVielen Dank und freundliche Grüße")
	return "mailto:" + g.SupplierEmail + "?subject=" + url.PathEscape("Bestellung") + "&body=" + url.PathEscape(b.String())
}

func procurementBack(w http.ResponseWriter, r *http.Request, tab, msg string, err error) {
	target := "/procurement?tab=" + url.QueryEscape(tab)
	if err != nil {
		target += "&err=" + url.QueryEscape(friendlyPartError(err))
	} else if msg != "" {
		target += "&msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ProcurementOrderWeb: POST /procurement/order – ausgewählte leere Karten
// als bestellt melden (card=…, order_ref, expected_at). Ruft den
// Rahmenvertrag des Regelkreises ab.
func (h *Handler) ProcurementOrderWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canProcureOrder(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	var cards []string
	for _, c := range r.Form["card"] {
		if uuidInPathRe.MatchString(c) {
			cards = append(cards, c)
		}
	}
	if len(cards) == 0 {
		procurementBack(w, r, "proposal", "", errors.New("Keine Karte ausgewählt"))
		return
	}
	ref := truncateRunes(strings.TrimSpace(r.FormValue("order_ref")), 100)
	var expected *time.Time
	if s := strings.TrimSpace(r.FormValue("expected_at")); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			procurementBack(w, r, "proposal", "", errors.New("Liefertermin ungültig"))
			return
		}
		expected = &t
	}
	uid := getUser(r).ID
	tx, err := h.db.Begin(ctx)
	if err != nil {
		procurementBack(w, r, "proposal", "", err)
		return
	}
	defer tx.Rollback(ctx)
	// Liefertermin ohne Angabe: heute + Lieferzeit des Regelkreises
	rows, err := tx.Query(ctx, `UPDATE kanban_cards c SET status = 'ordered', ordered_at = NOW(), ordered_by = $2::uuid, order_ref = $3,
			expected_at = COALESCE($4::date, CASE WHEN k.lead_time_days > 0 THEN CURRENT_DATE + k.lead_time_days END)
		FROM kanban_loops k WHERE k.part_id = c.part_id AND c.id::text = ANY($1) AND c.status = 'empty'
		RETURNING c.part_id::text, k.card_qty::float8, COALESCE(k.contract_id::text, '')`, cards, nullID(uid), ref, expected)
	if err != nil {
		procurementBack(w, r, "proposal", "", err)
		return
	}
	type agg struct {
		qty      float64
		n        int
		contract string
	}
	byPart := map[string]*agg{}
	for rows.Next() {
		var part, contract string
		var qty float64
		if rows.Scan(&part, &qty, &contract) == nil {
			a := byPart[part]
			if a == nil {
				a = &agg{contract: contract}
				byPart[part] = a
			}
			a.qty += qty
			a.n++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		procurementBack(w, r, "proposal", "", err)
		return
	}
	n := 0
	for part, a := range byPart {
		n += a.n
		if a.contract != "" {
			if _, err := tx.Exec(ctx, `UPDATE framework_contract_items SET called_qty = called_qty + $3 WHERE contract_id = $1::uuid AND part_id = $2::uuid`,
				a.contract, part, a.qty); err != nil {
				procurementBack(w, r, "proposal", "", err)
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		procurementBack(w, r, "proposal", "", err)
		return
	}
	for part, a := range byPart {
		msg := fmt.Sprintf("Kanban: %d Karte(n) bestellt", a.n)
		if ref != "" {
			msg += " · " + ref
		}
		h.addHistory(ctx, "spare_part", part, "update", "Kanban", "leer", "bestellt", msg, uid)
	}
	if n == 0 {
		procurementBack(w, r, "proposal", "", errors.New("Die ausgewählten Karten sind nicht mehr leer – Seite neu laden"))
		return
	}
	componentLog("beschaffung").Info().Int("karten", n).Str("bestellung", ref).Str("von", uid).Msg("kanban bestellt")
	procurementBack(w, r, "orders", fmt.Sprintf("%d Kanban-Karte(n) als bestellt gemeldet", n), nil)
}

// ── Rahmenverträge ─────────────────────────────────────────────

type ContractPageData struct {
	BaseData
	Contract       contractView
	IsNew          bool
	Items          []contractItemView
	Suppliers      []partnerOption
	Parts          []partnerOption // Teilenummer → Bezeichnung (Datalist)
	CanEdit        bool
	Message, Error string
}

// ContractPage: GET /procurement/contracts/{id} (id = new: anlegen)
func (h *Handler) ContractPage(w http.ResponseWriter, r *http.Request) {
	if !h.canProcurement(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	q := r.URL.Query()
	d := ContractPageData{
		BaseData: h.baseData(r, "procurement", "Rahmenvertrag", "Beschaffung"),
		IsNew:    id == "new", CanEdit: h.canContracts(r), Message: q.Get("msg"), Error: q.Get("err"),
	}
	if d.IsNew {
		if !d.CanEdit {
			http.Redirect(w, r, "/procurement?tab=contracts", http.StatusFound)
			return
		}
		d.Contract = contractView{Active: true, SupplierID: q.Get("supplier")}
		d.BaseData.Title = "Neuer Rahmenvertrag"
	} else {
		if !uuidInPathRe.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		c, err := h.scanContract(h.db.QueryRow(ctx, contractSelect+` WHERE fc.id = $1::uuid`, id))
		if err != nil {
			procurementBack(w, r, "contracts", "", errors.New("Rahmenvertrag nicht gefunden"))
			return
		}
		d.Contract = c
		d.BaseData.Title = "Rahmenvertrag " + c.No
		rows, err := h.db.Query(ctx, `SELECT i.id::text, i.part_id::text, sp.part_number, sp.name, sp.unit, i.supplier_part_no,
				i.price::float8, i.contract_qty::float8, i.called_qty::float8,
				EXISTS (SELECT 1 FROM kanban_loops k WHERE k.part_id = i.part_id AND k.contract_id = i.contract_id AND k.active)
			FROM framework_contract_items i JOIN spare_parts sp ON sp.id = i.part_id
			WHERE i.contract_id = $1::uuid ORDER BY sp.name`, id)
		if err == nil {
			for rows.Next() {
				var it contractItemView
				if rows.Scan(&it.ID, &it.PartID, &it.PartNumber, &it.PartName, &it.Unit, &it.SupplierPartNo, &it.Price, &it.ContractQty, &it.CalledQty, &it.Kanban) == nil {
					if it.ContractQty > 0 {
						it.UsedPct = int(it.CalledQty / it.ContractQty * 100)
					}
					d.Items = append(d.Items, it)
				}
			}
			rows.Close()
		}
	}
	if d.CanEdit {
		d.Suppliers = h.supplierOptions(ctx)
		if rows, err := h.db.Query(ctx, `SELECT part_number, name FROM spare_parts WHERE active ORDER BY part_number LIMIT 5000`); err == nil {
			for rows.Next() {
				var o partnerOption
				if rows.Scan(&o.Value, &o.Label) == nil {
					d.Parts = append(d.Parts, o)
				}
			}
			rows.Close()
		}
	}
	h.render(w, "procurement_contract", d)
}

func contractBack(w http.ResponseWriter, r *http.Request, id, msg string, err error) {
	target := "/procurement/contracts/" + id
	if err != nil {
		target += "?err=" + url.QueryEscape(friendlyPartError(err))
	} else if msg != "" {
		target += "?msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func parseOptDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, errors.New("Datum ungültig")
	}
	return &t, nil
}

// ContractSaveWeb: POST /procurement/contracts/{id} (new: anlegen)
func (h *Handler) ContractSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canContracts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if id != "new" && !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	no, title, supplier := truncateRunes(v("contract_no"), 60), truncateRunes(v("title"), 200), v("supplier_id")
	if no == "" || !uuidInPathRe.MatchString(supplier) {
		contractBack(w, r, id, "", errors.New("Vertragsnummer und Lieferant sind Pflicht"))
		return
	}
	from, err := parseOptDate(v("valid_from"))
	if err != nil {
		contractBack(w, r, id, "", err)
		return
	}
	until, err := parseOptDate(v("valid_until"))
	if err != nil {
		contractBack(w, r, id, "", err)
		return
	}
	if from != nil && until != nil && until.Before(*from) {
		contractBack(w, r, id, "", errors.New("„Gültig bis“ liegt vor „Gültig ab“"))
		return
	}
	total := 0.0
	if s := v("total_value"); s != "" {
		if total, err = parseQty(s); err != nil || total < 0 {
			contractBack(w, r, id, "", errors.New("Vertragsvolumen ungültig"))
			return
		}
	}
	active := r.FormValue("active") == "1"
	uid := getUser(r).ID
	if id == "new" {
		err = h.db.QueryRow(ctx, `INSERT INTO framework_contracts (contract_no, title, supplier_id, valid_from, valid_until, total_value, notes, active, created_by)
			VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
			no, title, supplier, from, until, total, truncateRunes(v("notes"), 4000), active, nullID(uid)).Scan(&id)
		if err != nil {
			if strings.Contains(err.Error(), "idx_framework_contracts_no") {
				err = errors.New("Diese Vertragsnummer gibt es bei diesem Lieferanten schon")
			}
			contractBack(w, r, "new", "", err)
			return
		}
		componentLog("beschaffung").Info().Str("vertrag", no).Str("von", uid).Msg("rahmenvertrag angelegt")
		contractBack(w, r, id, "Rahmenvertrag angelegt – jetzt die Positionen ergänzen", nil)
		return
	}
	_, err = h.db.Exec(ctx, `UPDATE framework_contracts SET contract_no = $2, title = $3, supplier_id = $4::uuid, valid_from = $5, valid_until = $6,
			total_value = $7, notes = $8, active = $9, updated_at = NOW() WHERE id = $1::uuid`,
		id, no, title, supplier, from, until, total, truncateRunes(v("notes"), 4000), active)
	if err != nil && strings.Contains(err.Error(), "idx_framework_contracts_no") {
		err = errors.New("Diese Vertragsnummer gibt es bei diesem Lieferanten schon")
	}
	contractBack(w, r, id, "Rahmenvertrag gespeichert", err)
}

// ContractDeleteWeb: POST /procurement/contracts/{id}/delete
func (h *Handler) ContractDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canContracts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM framework_contracts WHERE id = $1::uuid`, id)
	procurementBack(w, r, "contracts", "Rahmenvertrag gelöscht – Kanban-Regelkreise bestellen ab jetzt ohne Vertrag", err)
}

// ContractItemSaveWeb: POST /procurement/contracts/{id}/items – Position
// anlegen oder ändern (Teil über die Teilenummer).
func (h *Handler) ContractItemSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canContracts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	number := v("part_number")
	if i := strings.Index(number, " · "); i > 0 { // Auswahl aus der Liste: „Nummer · Name“
		number = number[:i]
	}
	var partID string
	if err := h.db.QueryRow(ctx, `SELECT id::text FROM spare_parts WHERE part_number = $1 AND active ORDER BY created_at LIMIT 1`, number).Scan(&partID); err != nil {
		contractBack(w, r, id, "", fmt.Errorf("Teilenummer „%s“ nicht gefunden", number))
		return
	}
	price, err := parseQty(firstNonEmpty(v("price"), "0"))
	if err != nil || price < 0 {
		contractBack(w, r, id, "", errors.New("Preis ungültig"))
		return
	}
	cqty, err := parseQty(firstNonEmpty(v("contract_qty"), "0"))
	if err != nil || cqty < 0 {
		contractBack(w, r, id, "", errors.New("Rahmenmenge ungültig"))
		return
	}
	called := -1.0
	if s := v("called_qty"); s != "" && h.canContracts(r) {
		if called, err = parseQty(s); err != nil || called < 0 {
			contractBack(w, r, id, "", errors.New("Abgerufene Menge ungültig"))
			return
		}
	}
	_, err = h.db.Exec(ctx, `INSERT INTO framework_contract_items (contract_id, part_id, supplier_part_no, price, contract_qty, called_qty)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, GREATEST($6::numeric, 0))
		ON CONFLICT (contract_id, part_id) DO UPDATE SET supplier_part_no = EXCLUDED.supplier_part_no, price = EXCLUDED.price,
			contract_qty = EXCLUDED.contract_qty,
			called_qty = CASE WHEN $6::numeric >= 0 THEN $6::numeric ELSE framework_contract_items.called_qty END`,
		id, partID, truncateRunes(v("supplier_part_no"), 100), price, cqty, called)
	if err == nil {
		_, _ = h.db.Exec(ctx, `UPDATE framework_contracts SET updated_at = NOW() WHERE id = $1::uuid`, id)
	}
	contractBack(w, r, id, "Position gespeichert", err)
}

// ContractItemDeleteWeb: POST /procurement/contracts/{id}/items/{item}/delete
func (h *Handler) ContractItemDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canContracts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id, item := chi.URLParam(r, "id"), chi.URLParam(r, "item")
	if !uuidInPathRe.MatchString(id) || !uuidInPathRe.MatchString(item) {
		http.NotFound(w, r)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM framework_contract_items WHERE id = $1::uuid AND contract_id = $2::uuid`, item, id)
	contractBack(w, r, id, "Position entfernt", err)
}
