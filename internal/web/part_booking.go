package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/inventory"
)

// Buchungserfassung je Ersatzteil (GET/POST /inventory/{id}/booking) – das
// Ziel des QR-Codes auf dem Lagerplatz-Etikett. Zeigt Bild, Beschreibung und
// Bestand und bucht:
//   in       Einbuchung (Wareneingang) – meldet bestellte Kanban-Karten als geliefert
//   out      Ausbuchung → Pflicht: Vorgang oder Anlage
//   reserve  Reservierung → Pflicht: Vorgang oder Anlage
//   other    Sonstiges (Zugang/Abgang mit Grund, z. B. Ausschuss, Rücksendung)
//   correct  Korrektur (gezählter Bestand am Lagerplatz, mit Grund)
// Rechte: inventory.book (in/out/reserve), inventory.correct (other/correct).

type bookingAction struct{ Key, Label, Icon, Hint string }

var bookingActions = []bookingAction{
	{"in", "Einbuchung", "ti-download", "Wareneingang an einen Lagerplatz"},
	{"out", "Ausbuchung", "ti-upload", "Entnahme für einen Vorgang oder eine Anlage"},
	{"reserve", "Reservierung", "ti-bookmark", "Für einen Vorgang oder eine Anlage zurücklegen"},
	{"other", "Sonstiges", "ti-dots", "Zugang oder Abgang mit Grund, z. B. Ausschuss"},
	{"correct", "Korrektur", "ti-adjustments", "Gezählten Bestand am Lagerplatz eintragen"},
}

// Gründe für „Sonstiges“ und „Korrektur“ (Freitext zusätzlich möglich)
var bookingOtherReasons = []string{"Ausschuss / defekt", "Rücksendung an Lieferant", "Muster / Leihgabe", "Umbau / Eigenbedarf", "Fund / Rückgabe", "Sonstiges"}
var bookingCorrectReasons = []string{"Zählfehler", "Fehlbuchung", "Schwund", "Bruch / Verlust", "Sonstiges"}

// bookingRefKinds: Vorgangsarten für Ausbuchung und Reservierung
// (Schlüssel = Art im Lager-Modul, siehe inventory.reservationKinds).
var bookingRefKinds = []struct{ Kind, Label, Table, URL, Open string }{
	{"fault", "Störung", "faults", "/faults/", "r.status::text IN ('detected', 'analyzing', 'in_progress', 'pending')"},
	{"ticket", "Ticket", "tickets", "/tickets/", "r.status::text IN ('open', 'in_progress', 'pending')"},
	{"task", "Aufgabe", "tasks", "/tasks/", "r.status IN ('open', 'in_progress', 'pending')"},
	{"maintenance_task", "Wartung", "maintenance_tasks", "/maintenance/tasks/", "r.status::text IN ('open', 'in_progress', 'pending')"},
}

type PartBookingData struct {
	BaseData
	Part           PartMaster
	Stock          []PartStockRow
	Reservations   []PartReservationRow
	Moves          []PartMovementRow
	StorageOptions []partnerOption
	DefaultStore   string
	Actions        []bookingAction
	Action         string
	OtherReasons   []string
	CorrectReasons []string
	CanBook        bool
	CanCorrect     bool
	CanDetail      bool
	Kanban         *kanbanLoopView
	RefValue       string // Vorbelegung „kind:id“ (z. B. aus einem Vorgang heraus)
	RefLabel       string
	Message, Error string
}

func (h *Handler) canBookParts(r *http.Request) bool {
	return h.hasPerm(r, "inventory.book") || h.hasPerm(r, "inventory.edit")
}
func (h *Handler) canCorrectParts(r *http.Request) bool { return h.hasPerm(r, "inventory.correct") }

// PartBookingPage: GET /inventory/{id}/booking
func (h *Handler) PartBookingPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	p, err := h.loadPartMaster(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/inventory?err="+url.QueryEscape("Ersatzteil nicht gefunden"), http.StatusFound)
		return
	}
	q := r.URL.Query()
	d := PartBookingData{
		BaseData: h.baseData(r, "inventory", p.Name, "Buchen"),
		Part:     p, Actions: bookingActions, Action: q.Get("action"),
		OtherReasons: bookingOtherReasons, CorrectReasons: bookingCorrectReasons,
		CanBook: h.canBookParts(r), CanCorrect: h.canCorrectParts(r), CanDetail: h.hasPerm(r, "inventory.view") || h.canEditParts(r),
		Message: q.Get("msg"), Error: q.Get("err"),
	}
	if d.Action == "" {
		d.Action = "out"
	}
	if !d.CanBook && d.CanCorrect && (d.Action != "other" && d.Action != "correct") {
		d.Action = "correct"
	}
	if ref := q.Get("ref"); ref != "" {
		if kind, rid, ok := strings.Cut(ref, ":"); ok {
			if label, err := h.bookingRefLabel(ctx, kind, rid); err == nil {
				d.RefValue, d.RefLabel = ref, label
			}
		}
	}
	h.kanbanSync(ctx, id)
	storageOpts, paths := h.storagePaths(ctx)
	d.StorageOptions = storageOpts
	var best float64
	if rows, err := h.db.Query(ctx, `SELECT storage_node_id::text, qty::float8 FROM spare_part_stock WHERE part_id = $1::uuid AND qty <> 0 ORDER BY qty DESC`, id); err == nil {
		for rows.Next() {
			var node string
			var qty float64
			if rows.Scan(&node, &qty) == nil {
				d.Stock = append(d.Stock, PartStockRow{NodeID: node, Path: paths[node], Qty: formatQty(qty)})
				if qty > best || d.DefaultStore == "" {
					best, d.DefaultStore = qty, node
				}
			}
		}
		rows.Close()
	}
	// Lagerplatz vom Etikett (…?loc=) hat Vorrang
	if loc := q.Get("loc"); loc != "" && paths[loc] != "" {
		d.DefaultStore = loc
	}
	if res, err := h.inv.Reservations(ctx, id); err == nil {
		for _, rv := range res {
			d.Reservations = append(d.Reservations, PartReservationRow{
				ID: rv.ID, Kind: rv.Kind, RefID: rv.RefID, RefLabel: rv.KindLabel + ": " + rv.RefTitle, RefURL: rv.RefURL,
				Path: paths[rv.StorageNodeID], Qty: formatQty(rv.Qty), QtyRaw: rv.Qty, User: rv.CreatedByName,
				Date: rv.CreatedAt.Local().Format("02.01.2006 15:04"),
			})
		}
	}
	d.Moves = h.partMovements(ctx, id, paths, 8)
	if k := h.kanbanLoop(ctx, id); k != nil && k.Active {
		d.Kanban = k
	}
	h.render(w, "inventory_booking", d)
}

// partMovements: letzte Bewegungen eines Teils mit Bezug (Vorgang oder Anlage).
func (h *Handler) partMovements(ctx context.Context, partID string, paths map[string]string, limit int) []PartMovementRow {
	rows, err := h.db.Query(ctx, `
		SELECT sm.created_at, sm.type::text, sm.qty::float8, sm.qty_before::float8, sm.qty_after::float8,
		       COALESCE(sm.storage_node_id::text, ''), COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
		       COALESCE(sm.reference, ''), COALESCE(sm.notes, ''),
		       CASE WHEN f.id IS NOT NULL THEN '/faults/' || f.id WHEN t.id IS NOT NULL THEN '/tickets/' || t.id
		            WHEN ta.id IS NOT NULL THEN '/tasks/' || ta.id WHEN m.id IS NOT NULL THEN '/maintenance/tasks/' || m.id
		            WHEN i.id IS NOT NULL THEN '/infrastructure/' || i.id ELSE '' END,
		       COALESCE('Störung: ' || f.title, 'Ticket: ' || t.title, 'Aufgabe: ' || ta.title, 'Wartung: ' || m.title, 'Anlage: ' || i.name, '')
		FROM stock_movements sm
		LEFT JOIN users u ON u.id = sm.created_by
		LEFT JOIN faults f ON f.id = sm.fault_id
		LEFT JOIN tickets t ON t.id = sm.ticket_id
		LEFT JOIN tasks ta ON ta.id = sm.task_id
		LEFT JOIN maintenance_tasks m ON m.id = sm.maintenance_task_id
		LEFT JOIN infrastructure i ON i.id = sm.infrastructure_id
		WHERE sm.part_id = $1::uuid ORDER BY sm.created_at DESC LIMIT $2`, partID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PartMovementRow
	for rows.Next() {
		var mv PartMovementRow
		var qty, before, after float64
		var node string
		var at struct{ t time.Time }
		if rows.Scan(&at.t, &mv.TypeKey, &qty, &before, &after, &node, &mv.User, &mv.Reference, &mv.Notes, &mv.RecordURL, &mv.RecordLabel) != nil {
			continue
		}
		mv.Date = at.t.Local().Format("02.01.2006 15:04")
		mv.TypeLabel = movementTypeLabels[mv.TypeKey]
		mv.Qty, mv.Before, mv.After, mv.Path = formatQty(qty), formatQty(before), formatQty(after), paths[node]
		out = append(out, mv)
	}
	return out
}

// bookingRefLabel prüft einen Vorgang (muss offen sein) und liefert seine Bezeichnung.
func (h *Handler) bookingRefLabel(ctx context.Context, kind, id string) (string, error) {
	if !uuidInPathRe.MatchString(id) {
		return "", errors.New("Vorgang ungültig")
	}
	for _, k := range bookingRefKinds {
		if k.Kind != kind {
			continue
		}
		var title string
		var open bool
		if err := h.db.QueryRow(ctx, `SELECT r.title, (`+k.Open+` AND r.archived_at IS NULL) FROM `+k.Table+` r WHERE r.id = $1::uuid`, id).Scan(&title, &open); err != nil {
			return "", errors.New(k.Label + " nicht gefunden")
		}
		if !open {
			return "", fmt.Errorf("%s „%s“ ist bereits abgeschlossen", k.Label, title)
		}
		return k.Label + ": " + title, nil
	}
	return "", errors.New("Unbekannte Vorgangsart")
}

// BookingRefsWeb: GET /inventory/booking/refs?q= – offene Vorgänge für die
// Zuordnung (eigene zuerst, dann die neuesten). Achtet auf Abteilungsrechte.
func (h *Handler) BookingRefsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canBookParts(r) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "keine berechtigung"})
		return
	}
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	uid := getUser(r).ID
	scope := h.requestScope(r)
	type hit struct {
		Value string `json:"value"`
		Label string `json:"label"`
		Sub   string `json:"sub"`
		mine  bool
		at    string
	}
	var hits []hit
	for _, k := range bookingRefKinds {
		args := []any{"%" + q + "%", nullID(uid)}
		where := k.Open + ` AND r.archived_at IS NULL AND ($1 = '%%' OR r.title ILIKE $1 OR COALESCE(i.name, '') ILIKE $1 OR r.id::text = TRIM(BOTH '%' FROM $1))`
		if s, a := scopeSQL(scope, k.Kind, "r", len(args)); s != "" {
			where += s
			args = append(args, a...)
		}
		mine := `r.created_by = $2::uuid`
		if k.Kind != "task" {
			mine += ` OR r.assigned_to = $2::uuid`
		} else {
			mine += ` OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id AND ta.user_id = $2::uuid)`
		}
		rows, err := h.db.Query(ctx, `SELECT r.id::text, r.title, COALESCE(i.name, ''), r.created_at, (`+mine+`)
			FROM `+k.Table+` r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE `+where+` ORDER BY r.created_at DESC LIMIT 15`, args...)
		if err != nil {
			componentLog("lager").Warn().Err(err).Str("art", k.Kind).Msg("vorgänge für buchung")
			continue
		}
		for rows.Next() {
			var id, title, infra string
			var at struct{ t time.Time }
			var mine bool
			if rows.Scan(&id, &title, &infra, &at.t, &mine) != nil {
				continue
			}
			sub := at.t.Local().Format("02.01.2006")
			if infra != "" {
				sub = infra + " · " + sub
			}
			hits = append(hits, hit{Value: k.Kind + ":" + id, Label: k.Label + ": " + title, Sub: sub, mine: mine, at: at.t.UTC().Format("20060102150405")})
		}
		rows.Close()
	}
	// eigene zuerst, dann neueste
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0; j-- {
			a, b := hits[j-1], hits[j]
			if (b.mine && !a.mine) || (a.mine == b.mine && b.at > a.at) {
				hits[j-1], hits[j] = b, a
			} else {
				break
			}
		}
	}
	if len(hits) > 25 {
		hits = hits[:25]
	}
	out := make([]map[string]string, 0, len(hits))
	for _, x := range hits {
		out = append(out, map[string]string{"value": x.Value, "label": x.Label, "sub": x.Sub})
	}
	completionJSON(w, http.StatusOK, map[string]any{"results": out})
}

// PartBookingSaveWeb: POST /inventory/{id}/booking
func (h *Handler) PartBookingSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	action := v("action")
	back := func(msg string, err error) {
		target := "/inventory/" + id + "/booking?action=" + url.QueryEscape(action)
		if err != nil {
			target += "&err=" + url.QueryEscape(friendlyPartError(triggerError(err)))
			if ref := v("ref"); ref != "" {
				target += "&ref=" + url.QueryEscape(ref)
			}
		} else {
			target += "&msg=" + url.QueryEscape(msg)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	switch action {
	case "in", "out", "reserve":
		if !h.canBookParts(r) {
			http.Error(w, "keine berechtigung", http.StatusForbidden)
			return
		}
	case "other", "correct":
		if !h.canCorrectParts(r) {
			http.Error(w, "keine berechtigung", http.StatusForbidden)
			return
		}
	default:
		back("", errors.New("Bitte eine Buchungsart wählen"))
		return
	}
	p, err := h.loadPartMaster(ctx, id)
	if err != nil {
		back("", errors.New("Ersatzteil nicht gefunden"))
		return
	}
	store := v("storage_node_id")
	if store == "" {
		back("", errors.New("Bitte den Lagerplatz wählen"))
		return
	}
	qty, err := parseQty(v("qty"))
	if err != nil || (action != "correct" && qty <= 0) || qty < 0 {
		back("", errors.New("Bitte eine gültige Menge eingeben"))
		return
	}
	uid := getUser(r).ID
	in := &inventory.BookMovementInput{PartID: id, Qty: qty, StorageNodeID: store, Notes: truncateRunes(v("notes"), 1000)}
	unit := p.Unit
	var msg string

	// Zuordnung (Ausbuchung/Reservierung): Vorgang „kind:id“ oder Anlage
	var refKind, refID, refLabel string
	if action == "out" || action == "reserve" {
		switch v("target") {
		case "infra":
			refKind, refID = "infrastructure", v("infrastructure_id")
			if !uuidInPathRe.MatchString(refID) {
				back("", errors.New("Bitte die Anlage im Infrastruktur-Baum auswählen"))
				return
			}
			var name string
			if h.db.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id = $1::uuid`, refID).Scan(&name) != nil {
				back("", errors.New("Anlage nicht gefunden"))
				return
			}
			refLabel = "Anlage: " + name
		default:
			kind, rid, ok := strings.Cut(v("ref"), ":")
			if !ok || rid == "" {
				back("", errors.New("Bitte den Vorgang auswählen, dem die Teile zugeordnet werden – oder „Anlage“ wählen"))
				return
			}
			if refLabel, err = h.bookingRefLabel(ctx, kind, rid); err != nil {
				back("", err)
				return
			}
			if !h.recordInScope(ctx, h.requestScope(r), kind, rid) {
				http.Error(w, "keine berechtigung", http.StatusForbidden)
				return
			}
			refKind, refID = kind, rid
		}
	}

	switch action {
	case "in":
		in.Type = inventory.MovementIn
		in.Reference = truncateRunes(firstNonEmpty(v("reference"), "Einbuchung"), 250)
		if _, err = h.inv.Book(ctx, in, uid); err == nil {
			msg = fmt.Sprintf("%s %s eingebucht", formatQty(qty), unit)
			if n := h.kanbanReceive(ctx, id, qty, uid); n > 0 {
				msg += fmt.Sprintf(" – %d Kanban-Karte(n) wieder voll", n)
			}
		}
	case "out":
		in.Type, in.NoNegative = inventory.MovementOut, true
		in.Reference = truncateRunes("Ausbuchung · "+refLabel, 250)
		switch refKind {
		case "fault":
			in.FaultID = refID
		case "ticket":
			in.TicketID = refID
		case "task":
			in.TaskID = refID
		case "maintenance_task":
			in.MaintenanceTaskID = refID
		case "infrastructure":
			in.InfrastructureID = refID
		}
		if _, err = h.inv.Book(ctx, in, uid); err == nil {
			msg = fmt.Sprintf("%s %s ausgebucht (%s)", formatQty(qty), unit, refLabel)
		}
	case "reserve":
		if _, err = h.inv.Reserve(ctx, refKind, refID, id, store, qty, uid); err == nil {
			msg = fmt.Sprintf("%s %s reserviert (%s)", formatQty(qty), unit, refLabel)
		}
	case "other":
		reason := firstNonEmpty(v("reason_text"), v("reason"))
		if reason == "" {
			back("", errors.New("Bitte einen Grund angeben"))
			return
		}
		in.Type, in.NoNegative = inventory.MovementOut, true
		dir := "Abgang"
		if v("direction") == "in" {
			in.Type, in.NoNegative, dir = inventory.MovementIn, false, "Zugang"
		}
		in.Reference = truncateRunes("Sonstiges ("+dir+"): "+reason, 250)
		if _, err = h.inv.Book(ctx, in, uid); err == nil {
			msg = fmt.Sprintf("Sonstiges gebucht: %s %s %s – %s", dir, formatQty(qty), unit, reason)
		}
	case "correct":
		reason := firstNonEmpty(v("reason_text"), v("reason"))
		if reason == "" {
			back("", errors.New("Bitte einen Grund für die Korrektur angeben"))
			return
		}
		in.Type = inventory.MovementCorrect
		in.Reference = truncateRunes("Korrektur: "+reason, 250)
		var mv *inventory.StockMovement
		if mv, err = h.inv.Book(ctx, in, uid); err == nil {
			msg = fmt.Sprintf("Bestand am Lagerplatz auf %s %s korrigiert (%s)", formatQty(qty), unit, signedQty(mv.Qty))
		}
	}
	if err != nil {
		back("", err)
		return
	}
	h.kanbanSync(ctx, id)
	componentLog("lager").Info().Str("teil", id).Str("art", action).Float64("menge", qty).Str("bezug", refLabel).Str("von", uid).Msg("buchung")
	back(msg, nil)
}

func signedQty(v float64) string {
	if v > 0 {
		return "+" + formatQty(v)
	}
	return formatQty(v)
}

// ReservationConsumeWeb: POST /reservations/{kind}/{ref}/{id}/consume – eine
// Reservierung als verbraucht buchen (Anlagen-Reservierungen haben keinen
// Vorgangsabschluss, der das sonst erledigt).
func (h *Handler) ReservationConsumeWeb(w http.ResponseWriter, r *http.Request) {
	k, ok := reservationKinds[chi.URLParam(r, "kind")]
	if !ok {
		completionJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Unbekannte Vorgangsart."})
		return
	}
	if !h.canFn(r)(k[1]) {
		completionJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Keine Berechtigung."})
		return
	}
	ref, id := chi.URLParam(r, "ref"), chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(ref) || !uuidInPathRe.MatchString(id) || !h.recordInScope(r.Context(), h.requestScope(r), k[0], ref) {
		completionJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Keine Berechtigung."})
		return
	}
	if err := h.inv.ConsumeReservation(r.Context(), k[0], ref, id); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
		return
	}
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}
