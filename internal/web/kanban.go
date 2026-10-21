package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/inventory"
)

// Kanban-Regelkreise (migrations/132): je Ersatzteil eine feste Zahl Karten
// mit fester Menge. Kreislauf einer Karte:
//   voll  → leer      Behälter angebrochen/aufgebraucht (Karte gescannt oder
//                     automatisch, wenn der Bestand unter den Kartenstand fällt)
//   leer  → bestellt  Einkauf bestellt (Bestellvorschlag), ruft ggf. den Rahmenvertrag ab
//   bestellt → voll   Wareneingang (Buchungserfassung oder „Geliefert“ an der Karte)
// Aktiviert wird Kanban im Ersatzteilstamm (Reiter „Kanban“).

var kanbanStatusLabels = map[string][2]string{
	"full":    {"voll", "b-green"},
	"empty":   {"leer – zu bestellen", "b-red"},
	"ordered": {"bestellt", "b-amber"},
}

type kanbanCardView struct {
	ID, Status, StatusLabel, StatusClass string
	No                                   int
	Since, OrderRef, Expected            string
	Late                                 bool
}

type kanbanLoopView struct {
	PartID, PartNumber, PartName, Unit, ImageURL string
	Active, AutoEmpty                            bool
	CardCount                                    int
	CardQty                                      float64
	StorageID, StoragePath                       string
	SupplierID, SupplierName, SupplierEmail      string
	ContractID, ContractLabel                    string
	LeadTime                                     int
	Notes                                        string
	Stock                                        float64
	UnitPrice                                    float64
	Cards                                        []kanbanCardView
	Full, Empty, Ordered                         int
}

func (k *kanbanLoopView) Q(v float64) string { return formatQty(v) }
func (k *kanbanLoopView) In(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Total: Gesamtmenge im Regelkreis (Karten × Menge)
func (k *kanbanLoopView) Total() string { return formatQty(float64(k.CardCount) * k.CardQty) }

// OrderQty / OrderValue: was die leeren Karten auslösen
func (k *kanbanLoopView) OrderQty() string { return formatQty(float64(k.Empty) * k.CardQty) }
func (k *kanbanLoopView) OrderValue() string {
	return formatEuro(float64(k.Empty) * k.CardQty * k.UnitPrice)
}

func (h *Handler) canProcurement(r *http.Request) bool { return h.hasPerm(r, "procurement.view") }
func (h *Handler) canKanbanSetup(r *http.Request) bool { return h.hasPerm(r, "procurement.kanban") }
func (h *Handler) canProcureOrder(r *http.Request) bool {
	return h.hasPerm(r, "procurement.order")
}
func (h *Handler) canContracts(r *http.Request) bool { return h.hasPerm(r, "procurement.contracts") }

const kanbanLoopSelect = `
	SELECT k.part_id::text, sp.part_number, sp.name, sp.unit, COALESCE(sp.description, ''),
	       k.active, k.auto_empty, k.card_count, k.card_qty::float8,
	       COALESCE(k.storage_node_id::text, ''), COALESCE(k.supplier_id::text, ''), COALESCE(bp.name, ''),
	       COALESCE(NULLIF(bp.order_email, ''), NULLIF(bp.spare_parts_email, ''), bp.email, ''),
	       COALESCE(k.contract_id::text, ''), COALESCE(fc.contract_no || CASE WHEN fc.title <> '' THEN ' · ' || fc.title ELSE '' END, ''),
	       k.lead_time_days, k.notes, sp.stock_qty::float8,
	       COALESCE(fci.price, sps.price, sp.price, 0)::float8
	FROM kanban_loops k
	JOIN spare_parts sp ON sp.id = k.part_id
	LEFT JOIN business_partners bp ON bp.id = k.supplier_id
	LEFT JOIN framework_contracts fc ON fc.id = k.contract_id
	LEFT JOIN framework_contract_items fci ON fci.contract_id = k.contract_id AND fci.part_id = k.part_id
	LEFT JOIN spare_part_suppliers sps ON sps.part_id = k.part_id AND sps.partner_id = k.supplier_id`

func scanKanbanLoop(row pgx.Row, paths map[string]string) (*kanbanLoopView, error) {
	k := &kanbanLoopView{}
	var desc string
	if err := row.Scan(&k.PartID, &k.PartNumber, &k.PartName, &k.Unit, &desc, &k.Active, &k.AutoEmpty, &k.CardCount, &k.CardQty,
		&k.StorageID, &k.SupplierID, &k.SupplierName, &k.SupplierEmail, &k.ContractID, &k.ContractLabel,
		&k.LeadTime, &k.Notes, &k.Stock, &k.UnitPrice); err != nil {
		return nil, err
	}
	_, k.ImageURL = splitPartImage(desc)
	k.StoragePath = paths[k.StorageID]
	return k, nil
}

// kanbanLoop: Regelkreis eines Teils mit Karten (nil = kein Kanban eingerichtet).
func (h *Handler) kanbanLoop(ctx context.Context, partID string) *kanbanLoopView {
	_, paths := h.storagePaths(ctx)
	k, err := scanKanbanLoop(h.db.QueryRow(ctx, kanbanLoopSelect+` WHERE k.part_id = $1::uuid`, partID), paths)
	if err != nil {
		return nil
	}
	cards, _ := h.kanbanCards(ctx, []string{partID})
	k.setCards(cards[partID])
	return k
}

// kanbanLoops: alle Regelkreise (für Tafel und Bestellvorschlag).
func (h *Handler) kanbanLoops(ctx context.Context, onlyActive bool) ([]*kanbanLoopView, error) {
	_, paths := h.storagePaths(ctx)
	where := ""
	if onlyActive {
		where = " WHERE k.active"
	}
	rows, err := h.db.Query(ctx, kanbanLoopSelect+where+` ORDER BY sp.name`)
	if err != nil {
		return nil, err
	}
	var loops []*kanbanLoopView
	var ids []string
	for rows.Next() {
		k, err := scanKanbanLoop(rows, paths)
		if err != nil {
			rows.Close()
			return nil, err
		}
		loops = append(loops, k)
		ids = append(ids, k.PartID)
	}
	rows.Close()
	cards, err := h.kanbanCards(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, k := range loops {
		k.setCards(cards[k.PartID])
	}
	return loops, nil
}

func (k *kanbanLoopView) setCards(cards []kanbanCardView) {
	k.Cards = cards
	k.Full, k.Empty, k.Ordered = 0, 0, 0
	for _, c := range cards {
		switch c.Status {
		case "full":
			k.Full++
		case "empty":
			k.Empty++
		case "ordered":
			k.Ordered++
		}
	}
}

func (h *Handler) kanbanCards(ctx context.Context, partIDs []string) (map[string][]kanbanCardView, error) {
	out := map[string][]kanbanCardView{}
	if len(partIDs) == 0 {
		return out, nil
	}
	rows, err := h.db.Query(ctx, `SELECT part_id::text, id::text, card_no, status,
			COALESCE(CASE status WHEN 'empty' THEN emptied_at WHEN 'ordered' THEN ordered_at END, NULL),
			order_ref, expected_at
		FROM kanban_cards WHERE part_id::text = ANY($1) ORDER BY card_no`, partIDs)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	today := time.Now().Truncate(24 * time.Hour)
	for rows.Next() {
		var partID string
		var c kanbanCardView
		var since, expected *time.Time
		if rows.Scan(&partID, &c.ID, &c.No, &c.Status, &since, &c.OrderRef, &expected) != nil {
			continue
		}
		l := kanbanStatusLabels[c.Status]
		c.StatusLabel, c.StatusClass = l[0], l[1]
		if since != nil {
			c.Since = since.Local().Format("02.01.2006 15:04")
		}
		if expected != nil {
			c.Expected = expected.Format("02.01.2006")
			c.Late = c.Status == "ordered" && expected.Before(today)
		}
		out[partID] = append(out[partID], c)
	}
	return out, rows.Err()
}

// kanbanSync meldet volle Karten automatisch leer, wenn der Bestand nicht
// mehr für sie reicht (volle Karten > ⌈Bestand / Kartenmenge⌉). Wieder voll
// wird eine Karte nur über den Wareneingang. partID leer: alle Regelkreise.
func (h *Handler) kanbanSync(ctx context.Context, partID string) {
	if h.db == nil {
		return
	}
	_, err := h.db.Exec(ctx, `
		WITH need AS (
			SELECT k.part_id, CEIL(GREATEST(sp.stock_qty, 0) / k.card_qty)::int AS need_full
			FROM kanban_loops k JOIN spare_parts sp ON sp.id = k.part_id
			WHERE k.active AND k.auto_empty AND ($1 = '' OR k.part_id::text = $1)
		), ranked AS (
			SELECT c.id, n.need_full,
			       ROW_NUMBER() OVER (PARTITION BY c.part_id ORDER BY c.card_no DESC) AS rn,
			       COUNT(*) OVER (PARTITION BY c.part_id) AS full_cnt
			FROM kanban_cards c JOIN need n ON n.part_id = c.part_id
			WHERE c.status = 'full'
		)
		UPDATE kanban_cards c SET status = 'empty', emptied_at = NOW(), emptied_by = NULL, order_ref = '', expected_at = NULL
		FROM ranked r WHERE r.id = c.id AND r.rn <= r.full_cnt - r.need_full`, partID)
	if err != nil {
		componentLog("kanban").Warn().Err(err).Str("teil", partID).Msg("karten abgleichen")
	}
}

// kanbanReceive: Wareneingang von qty – bestellte Karten (älteste zuerst)
// werden voll, solange die Menge je Karte reicht. Liefert die Zahl der Karten.
func (h *Handler) kanbanReceive(ctx context.Context, partID string, qty float64, uid string) int {
	var cardQty float64
	if err := h.db.QueryRow(ctx, `SELECT card_qty::float8 FROM kanban_loops WHERE part_id = $1::uuid AND active`, partID).Scan(&cardQty); err != nil || cardQty <= 0 {
		return 0
	}
	n := int(math.Floor(qty/cardQty + 1e-9))
	if n <= 0 {
		return 0
	}
	tag, err := h.db.Exec(ctx, `UPDATE kanban_cards SET status = 'full', order_ref = '', expected_at = NULL
		WHERE id IN (SELECT id FROM kanban_cards WHERE part_id = $1::uuid AND status = 'ordered' ORDER BY ordered_at, card_no LIMIT $2)`, partID, n)
	if err != nil {
		componentLog("kanban").Warn().Err(err).Str("teil", partID).Msg("wareneingang")
		return 0
	}
	if c := int(tag.RowsAffected()); c > 0 {
		h.addHistory(ctx, "spare_part", partID, "update", "Kanban", "", fmt.Sprintf("%d Karte(n) voll", c), "Kanban: Wareneingang", uid)
		return c
	}
	return 0
}

// setKanbanCardCount legt Karten an oder entfernt die höchsten Nummern
// (bevorzugt volle, damit offene Bestellungen erhalten bleiben).
func setKanbanCardCount(ctx context.Context, tx pgx.Tx, partID string, count int, stock, cardQty float64) error {
	var have int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM kanban_cards WHERE part_id = $1::uuid`, partID).Scan(&have); err != nil {
		return err
	}
	if have > count {
		_, err := tx.Exec(ctx, `DELETE FROM kanban_cards WHERE id IN (
			SELECT id FROM kanban_cards WHERE part_id = $1::uuid
			ORDER BY CASE status WHEN 'full' THEN 0 WHEN 'empty' THEN 1 ELSE 2 END, card_no DESC LIMIT $2)`, partID, have-count)
		if err != nil {
			return err
		}
		// Nummern lückenlos 1..n
		_, err = tx.Exec(ctx, `UPDATE kanban_cards c SET card_no = -x.rn FROM (
			SELECT id, ROW_NUMBER() OVER (ORDER BY card_no) AS rn FROM kanban_cards WHERE part_id = $1::uuid) x WHERE x.id = c.id`, partID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE kanban_cards SET card_no = -card_no WHERE part_id = $1::uuid`, partID)
		}
		return err
	}
	var full int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM kanban_cards WHERE part_id = $1::uuid AND status = 'full'`, partID).Scan(&full); err != nil {
		return err
	}
	needFull := int(math.Ceil(math.Max(stock, 0)/cardQty - 1e-9))
	var maxNo int
	_ = tx.QueryRow(ctx, `SELECT COALESCE(MAX(card_no), 0) FROM kanban_cards WHERE part_id = $1::uuid`, partID).Scan(&maxNo)
	for i := have; i < count; i++ {
		status := "empty"
		if full < needFull {
			status, full = "full", full+1
		}
		maxNo++
		if _, err := tx.Exec(ctx, `INSERT INTO kanban_cards (part_id, card_no, status, emptied_at) VALUES ($1::uuid, $2, $3,
			CASE WHEN $3 = 'empty' THEN NOW() END)`, partID, maxNo, status); err != nil {
			return err
		}
	}
	return nil
}

// PartKanbanSaveWeb: POST /inventory/{id}/kanban – Kanban aktivieren,
// einstellen oder abschalten (action=save|off).
func (h *Handler) PartKanbanSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !h.canKanbanSetup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	uid := getUser(r).ID
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	if v("action") == "off" {
		_, err := h.db.Exec(ctx, `UPDATE kanban_loops SET active = false, updated_at = NOW() WHERE part_id = $1::uuid`, id)
		if err == nil {
			h.addHistory(ctx, "spare_part", id, "update", "Kanban", "aktiv", "aus", "Kanban abgeschaltet", uid)
		}
		partRedirect(w, r, id, "kanban", "Kanban abgeschaltet – Karten und Einstellungen bleiben erhalten", err)
		return
	}
	count, err := strconv.Atoi(v("card_count"))
	if err != nil || count < 1 || count > 99 {
		partRedirect(w, r, id, "kanban", "", errors.New("Anzahl der Karten: 1 bis 99"))
		return
	}
	cardQty, err := parseQty(v("card_qty"))
	if err != nil || cardQty <= 0 {
		partRedirect(w, r, id, "kanban", "", errors.New("Menge je Karte muss größer als 0 sein"))
		return
	}
	lead, _ := strconv.Atoi(v("lead_time_days"))
	if lead < 0 {
		lead = 0
	}
	store, supplier, contract := v("storage_node_id"), v("supplier_id"), v("contract_id")
	if store == "" {
		partRedirect(w, r, id, "kanban", "", errors.New("Bitte den Kanban-Lagerplatz wählen – dort wird der Wareneingang gebucht"))
		return
	}
	if contract != "" {
		// Vertrag muss zum Lieferanten passen; ohne Lieferant gilt der des Vertrags
		var cs string
		if err := h.db.QueryRow(ctx, `SELECT supplier_id::text FROM framework_contracts WHERE id = $1::uuid`, contract).Scan(&cs); err != nil {
			partRedirect(w, r, id, "kanban", "", errors.New("Rahmenvertrag nicht gefunden"))
			return
		}
		if supplier == "" {
			supplier = cs
		} else if supplier != cs {
			partRedirect(w, r, id, "kanban", "", errors.New("Der Rahmenvertrag gehört zu einem anderen Lieferanten"))
			return
		}
	}
	if supplier == "" {
		// ohne Auswahl: Hauptlieferant des Teils (bevorzugt, sonst günstigster)
		_ = h.db.QueryRow(ctx, `SELECT partner_id::text FROM spare_part_suppliers WHERE part_id = $1::uuid
			ORDER BY preferred DESC, price NULLS LAST, created_at LIMIT 1`, id).Scan(&supplier)
	}
	var stock float64
	if err := h.db.QueryRow(ctx, `SELECT stock_qty::float8 FROM spare_parts WHERE id = $1::uuid`, id).Scan(&stock); err != nil {
		partRedirect(w, r, id, "kanban", "", errors.New("Ersatzteil nicht gefunden"))
		return
	}
	old := h.kanbanLoop(ctx, id)
	tx, err := h.db.Begin(ctx)
	if err != nil {
		partRedirect(w, r, id, "kanban", "", err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO kanban_loops (part_id, active, card_count, card_qty, storage_node_id, supplier_id, contract_id,
			lead_time_days, auto_empty, notes, created_by)
		VALUES ($1::uuid, true, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7, $8, $9, $10)
		ON CONFLICT (part_id) DO UPDATE SET active = true, card_count = EXCLUDED.card_count, card_qty = EXCLUDED.card_qty,
			storage_node_id = EXCLUDED.storage_node_id, supplier_id = EXCLUDED.supplier_id, contract_id = EXCLUDED.contract_id,
			lead_time_days = EXCLUDED.lead_time_days, auto_empty = EXCLUDED.auto_empty, notes = EXCLUDED.notes, updated_at = NOW()`,
		id, count, cardQty, store, supplier, contract, lead, r.FormValue("auto_empty") == "1", truncateRunes(v("notes"), 1000), nullID(uid))
	if err == nil {
		err = setKanbanCardCount(ctx, tx, id, count, stock, cardQty)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		partRedirect(w, r, id, "kanban", "", err)
		return
	}
	msg := "Kanban-Einstellungen gespeichert"
	if old == nil || !old.Active {
		msg = fmt.Sprintf("Kanban aktiviert: %d Karten à %s", count, formatQty(cardQty))
		h.addHistory(ctx, "spare_part", id, "update", "Kanban", "", fmt.Sprintf("%d × %s", count, formatQty(cardQty)), "Kanban aktiviert", uid)
	} else if old.CardCount != count || old.CardQty != cardQty {
		h.addHistory(ctx, "spare_part", id, "update", "Kanban", fmt.Sprintf("%d × %s", old.CardCount, formatQty(old.CardQty)),
			fmt.Sprintf("%d × %s", count, formatQty(cardQty)), "Kanban geändert", uid)
	}
	h.kanbanSync(ctx, id)
	partRedirect(w, r, id, "kanban", msg, nil)
}

// kanbanCardByID: Karte mit Regelkreis laden.
func (h *Handler) kanbanCardByID(ctx context.Context, cardID string) (*kanbanLoopView, *kanbanCardView, error) {
	var partID string
	if err := h.db.QueryRow(ctx, `SELECT part_id::text FROM kanban_cards WHERE id = $1::uuid`, cardID).Scan(&partID); err != nil {
		return nil, nil, errors.New("Kanban-Karte nicht gefunden")
	}
	k := h.kanbanLoop(ctx, partID)
	if k == nil {
		return nil, nil, errors.New("Kanban-Karte nicht gefunden")
	}
	for i := range k.Cards {
		if k.Cards[i].ID == cardID {
			return k, &k.Cards[i], nil
		}
	}
	return nil, nil, errors.New("Kanban-Karte nicht gefunden")
}

// KanbanCardActionWeb: POST /procurement/cards/{id}/{action}
//
//	empty   – Karte leer melden (Behälter aufgebraucht): inventory.book oder procurement.order
//	receive – Wareneingang: Menge der Karte an den Kanban-Lagerplatz buchen, Karte voll
//	cancel  – Bestellung zurücknehmen (wieder leer): procurement.order
//	full    – Karte ohne Buchung auf voll setzen (Korrektur): procurement.kanban
func (h *Handler) KanbanCardActionWeb(w http.ResponseWriter, r *http.Request) {
	cardID, action := chi.URLParam(r, "id"), chi.URLParam(r, "action")
	if !uuidInPathRe.MatchString(cardID) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	back := safeBack(r.FormValue("back"), "/k/"+cardID)
	done := func(msg string, err error) {
		key, text := "msg", msg
		if err != nil {
			key, text = "err", friendlyPartError(triggerError(err))
		}
		http.Redirect(w, r, scanNotice(back, key, text), http.StatusSeeOther)
	}
	allowed := map[string]bool{
		"empty":   h.canBookParts(r) || h.canProcureOrder(r),
		"receive": h.canProcureOrder(r) || h.canBookParts(r),
		"cancel":  h.canProcureOrder(r),
		"full":    h.canKanbanSetup(r),
	}[action]
	if !allowed {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	k, c, err := h.kanbanCardByID(ctx, cardID)
	if err != nil {
		done("", err)
		return
	}
	uid := getUser(r).ID
	label := fmt.Sprintf("Karte %d/%d", c.No, k.CardCount)
	switch action {
	case "empty":
		if c.Status != "full" {
			done("", fmt.Errorf("%s ist bereits %s", label, c.StatusLabel))
			return
		}
		_, err = h.db.Exec(ctx, `UPDATE kanban_cards SET status = 'empty', emptied_at = NOW(), emptied_by = $2::uuid WHERE id = $1::uuid AND status = 'full'`, cardID, nullID(uid))
		if err == nil {
			h.addHistory(ctx, "spare_part", k.PartID, "update", "Kanban", "voll", "leer", "Kanban: "+label+" leer gemeldet", uid)
		}
		done(label+" leer gemeldet – steht jetzt im Bestellvorschlag", err)
	case "receive":
		if c.Status != "ordered" && c.Status != "empty" {
			done("", fmt.Errorf("%s ist schon voll", label))
			return
		}
		if k.StorageID == "" {
			done("", errors.New("Im Kanban-Regelkreis ist kein Lagerplatz eingetragen"))
			return
		}
		ref := "Kanban-Wareneingang " + label
		if c.OrderRef != "" {
			ref += " · Bestellung " + c.OrderRef
		}
		if _, err = h.inv.Book(ctx, &inventory.BookMovementInput{PartID: k.PartID, Type: inventory.MovementIn, Qty: k.CardQty,
			StorageNodeID: k.StorageID, Reference: truncateRunes(ref, 250)}, uid); err == nil {
			_, err = h.db.Exec(ctx, `UPDATE kanban_cards SET status = 'full', order_ref = '', expected_at = NULL WHERE id = $1::uuid`, cardID)
		}
		if err == nil {
			h.addHistory(ctx, "spare_part", k.PartID, "update", "Kanban", c.StatusLabel, "voll", "Kanban: "+label+" geliefert", uid)
		}
		done(fmt.Sprintf("%s geliefert – %s %s an %s eingebucht", label, formatQty(k.CardQty), k.Unit, k.StoragePath), err)
	case "cancel":
		if c.Status != "ordered" {
			done("", fmt.Errorf("%s ist nicht bestellt", label))
			return
		}
		tx, terr := h.db.Begin(ctx)
		if terr != nil {
			done("", terr)
			return
		}
		defer tx.Rollback(ctx)
		_, err = tx.Exec(ctx, `UPDATE kanban_cards SET status = 'empty', order_ref = '', expected_at = NULL WHERE id = $1::uuid`, cardID)
		if err == nil && k.ContractID != "" {
			_, err = tx.Exec(ctx, `UPDATE framework_contract_items SET called_qty = GREATEST(called_qty - $3, 0)
				WHERE contract_id = $1::uuid AND part_id = $2::uuid`, k.ContractID, k.PartID, k.CardQty)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err == nil {
			h.addHistory(ctx, "spare_part", k.PartID, "update", "Kanban", "bestellt", "leer", "Kanban: Bestellung "+label+" zurückgenommen", uid)
		}
		done("Bestellung zurückgenommen – "+label+" ist wieder leer", err)
	case "full":
		_, err = h.db.Exec(ctx, `UPDATE kanban_cards SET status = 'full', order_ref = '', expected_at = NULL WHERE id = $1::uuid`, cardID)
		if err == nil {
			h.addHistory(ctx, "spare_part", k.PartID, "update", "Kanban", c.StatusLabel, "voll", "Kanban: "+label+" ohne Buchung auf voll gesetzt", uid)
		}
		done(label+" auf voll gesetzt (ohne Buchung)", err)
	default:
		http.NotFound(w, r)
	}
}

// safeBack: nur lokale Rücksprungadressen.
func safeBack(b, fallback string) string {
	if strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "//") && !strings.ContainsAny(b, "\r\n") {
		return b
	}
	return fallback
}

type KanbanCardPageData struct {
	BaseData
	Loop           *kanbanLoopView
	Card           *kanbanCardView
	CanEmpty       bool
	CanReceive     bool
	CanCancel      bool
	CanFull        bool
	Message, Error string
}

// KanbanCardPage: GET /k/{id} – Ziel des QR-Codes auf der Kanban-Karte.
func (h *Handler) KanbanCardPage(w http.ResponseWriter, r *http.Request) {
	cardID := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(cardID) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	k, c, err := h.kanbanCardByID(ctx, cardID)
	if err != nil {
		http.Redirect(w, r, "/inventory?err="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	q := r.URL.Query()
	d := KanbanCardPageData{
		BaseData: h.baseData(r, "procurement", fmt.Sprintf("Kanban-Karte %d/%d", c.No, k.CardCount), k.PartName),
		Loop:     k, Card: c,
		CanEmpty: h.canBookParts(r) || h.canProcureOrder(r), CanReceive: h.canProcureOrder(r) || h.canBookParts(r),
		CanCancel: h.canProcureOrder(r), CanFull: h.canKanbanSetup(r),
		Message: q.Get("msg"), Error: q.Get("err"),
	}
	h.render(w, "kanban_card", d)
}

// KanbanPrintPage: GET /procurement/kanban/{id}/print – Kanban-Karten (A6 auf A4) mit QR-Code.
func (h *Handler) KanbanPrintPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !h.canProcurement(r) && !h.canKanbanSetup(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	k := h.kanbanLoop(r.Context(), id)
	if k == nil {
		http.NotFound(w, r)
		return
	}
	type printCard struct {
		No  int
		QR  template.HTML
		URL string
	}
	base := h.publicBaseURL(r)
	var cards []printCard
	only := r.URL.Query().Get("card")
	for _, c := range k.Cards {
		if only != "" && only != c.ID {
			continue
		}
		pc := printCard{No: c.No, URL: base + "/k/" + c.ID}
		if svg, err := qrSVG(pc.URL); err == nil {
			pc.QR = svg
		}
		cards = append(cards, pc)
	}
	h.renderPlain(w, "kanban_print", "kanban-print-page", map[string]any{"Loop": k, "Cards": cards, "Logo": h.labelLogo()})
}

// renderPlain: Seite ohne PDH-Rahmen (Druckansichten), define = Name des
// Haupttemplates in web/templates/<tmpl>.gohtml.
func (h *Handler) renderPlain(w http.ResponseWriter, tmpl, define string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		_, err = t.ParseFiles("web/templates/" + tmpl + ".gohtml")
	}
	if err == nil {
		err = t.ExecuteTemplate(w, define, data)
	}
	if err != nil {
		http.Error(w, "Template-Fehler: "+err.Error(), http.StatusInternalServerError)
	}
}
