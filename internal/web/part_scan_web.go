package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/inventory"
)

// Seiten zu part_scan.go: Übersicht (/inventory/scan), Ergebnis
// (/inventory/scan/{id}) und Übernahme (Lieferschein buchen, Teil anlegen).

type scanListRow struct {
	ID, Kind, Status, Error, Created, Who, NoteNo, AppliedNote string
	Applied                                                    bool
}

func (s scanListRow) KindLabel() string {
	if s.Kind == "part" {
		return "Bauteil"
	}
	return "Lieferschein"
}

type InventoryScanPageData struct {
	BaseData
	Tab, Message, Err string
	VisionReady       bool
	Recent            []scanListRow
}

type deliveryLine struct {
	Index      int
	Item       deliveryItem
	Candidates []partCandidate
	Action     string // book | new | skip
	PartID     string
	StorageID  string
	NewNumber  string
	NewName    string
	NewUnit    string
	Qty        string
}

type scanNewPart struct {
	Number, Name, ManufacturerPart, Category, Description, Unit, ManufacturerID string
}

type InventoryScanDetailData struct {
	BaseData
	Message, Err    string
	Scan            scanListRow
	Files           []scanFile
	Delivery        *deliveryResult
	Lines           []deliveryLine
	SupplierID      string
	SupplierLabel   string
	Suppliers       []partnerOption
	Storages        []partnerOption
	DefaultStorage  string
	Part            *partResult
	PartCandidates  []partCandidate
	NewPart         scanNewPart
	Manufacturers   []partnerOption
	ManufacturerHit string
}

func (h *Handler) loadScan(ctx context.Context, id string) (scanListRow, []scanFile, []byte, string, error) {
	var s scanListRow
	var filesRaw, result []byte
	var created time.Time
	var applied *time.Time
	var supplier string
	err := h.db.QueryRow(ctx, `SELECT c.id::text, c.kind, c.status, c.error, c.created_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
			c.note_no, c.applied_at, c.applied_note, c.files, c.result, COALESCE(c.supplier_id::text, '')
		FROM inventory_scans c LEFT JOIN users u ON u.id = c.created_by WHERE c.id = $1::uuid`, id).
		Scan(&s.ID, &s.Kind, &s.Status, &s.Error, &created, &s.Who, &s.NoteNo, &applied, &s.AppliedNote, &filesRaw, &result, &supplier)
	if err != nil {
		return s, nil, nil, "", err
	}
	s.Created = created.Local().Format("02.01.2006 15:04")
	s.Applied = applied != nil
	var files []scanFile
	_ = json.Unmarshal(filesRaw, &files)
	return s, files, result, supplier, nil
}

// InventoryScanPage: GET /inventory/scan?tab=delivery|part
func (h *Handler) InventoryScanPage(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		http.Redirect(w, r, "/inventory", http.StatusFound)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := InventoryScanPageData{
		BaseData: h.baseData(r, "inventory-scan", "Scannen & erkennen", "Ersatzteile"),
		Tab:      q.Get("tab"), Message: q.Get("msg"), Err: q.Get("err"),
		VisionReady: h.faults != nil && h.faults.Copilot() != nil && h.faults.Copilot().VisionAvailable(),
	}
	if d.Tab != "part" {
		d.Tab = "delivery"
	}
	rows, err := h.db.Query(ctx, `SELECT c.id::text, c.kind, c.status, c.error, c.created_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''),
			c.note_no, c.applied_at IS NOT NULL, c.applied_note
		FROM inventory_scans c LEFT JOIN users u ON u.id = c.created_by ORDER BY c.created_at DESC LIMIT 25`)
	if err == nil {
		for rows.Next() {
			var s scanListRow
			var created time.Time
			if rows.Scan(&s.ID, &s.Kind, &s.Status, &s.Error, &created, &s.Who, &s.NoteNo, &s.Applied, &s.AppliedNote) == nil {
				s.Created = created.Local().Format("02.01.2006 15:04")
				d.Recent = append(d.Recent, s)
			}
		}
		rows.Close()
	}
	h.render(w, "inventory_scan", d)
}

func (h *Handler) manufacturerOptions(ctx context.Context) []partnerOption {
	return h.partnerSimpleOptions(ctx, `SELECT id::text, name FROM business_partners WHERE active AND kind IN ('manufacturer', 'both') ORDER BY name`)
}

// buildDeliveryLines: Vorschlag je Position – Zugang zum besten Treffer,
// sonst Neuanlage; Positionen ohne Menge werden übersprungen.
func (h *Handler) buildDeliveryLines(ctx context.Context, d *deliveryResult, supplierID string) []deliveryLine {
	var lines []deliveryLine
	for i, it := range d.Items {
		l := deliveryLine{Index: i, Item: it, Qty: formatQty(it.Quantity), NewUnit: it.Unit}
		codes := normCodes(it.SupplierArticleNo, it.ManufacturerPartNo, it.EAN)
		l.Candidates = h.matchParts(ctx, codes, searchWords(it.Description), supplierID)
		l.NewNumber = strings.TrimSpace(it.ManufacturerPartNo)
		if l.NewNumber == "" {
			l.NewNumber = strings.TrimSpace(it.SupplierArticleNo)
		}
		l.NewName = strings.TrimSpace(it.Description)
		switch {
		case it.Quantity <= 0:
			l.Action = "skip"
		case len(l.Candidates) > 0 && l.Candidates[0].Score >= 85:
			l.Action, l.PartID, l.StorageID = "book", l.Candidates[0].ID, l.Candidates[0].LastStorageID
		case l.NewName != "" || l.NewNumber != "":
			l.Action = "new"
		default:
			l.Action = "skip"
		}
		lines = append(lines, l)
	}
	return lines
}

// InventoryScanDetail: GET /inventory/scan/{id}
func (h *Handler) InventoryScanDetail(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		http.Redirect(w, r, "/inventory", http.StatusFound)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	s, files, result, supplier, err := h.loadScan(ctx, id)
	if err != nil {
		http.Redirect(w, r, withNotice("/inventory/scan", "err", "Scan nicht gefunden"), http.StatusSeeOther)
		return
	}
	q := r.URL.Query()
	d := InventoryScanDetailData{
		BaseData: h.baseData(r, "inventory-scan", "Scan-Ergebnis", "Ersatzteile"),
		Message:  q.Get("msg"), Err: q.Get("err"), Scan: s, Files: files,
	}
	if s.Status == "done" {
		switch s.Kind {
		case "delivery":
			var dr deliveryResult
			if json.Unmarshal(result, &dr) == nil {
				d.Delivery = &dr
				d.Suppliers = h.supplierOptions(ctx)
				d.SupplierID = supplier
				if d.SupplierID == "" {
					d.SupplierID, d.SupplierLabel = matchSupplier(dr.SupplierName, d.Suppliers)
				}
				d.Storages, _ = h.storagePaths(ctx)
				d.Lines = h.buildDeliveryLines(ctx, &dr, d.SupplierID)
			}
		case "part":
			var pr partResult
			if json.Unmarshal(result, &pr) == nil {
				d.Part = &pr
				codes := normCodes(pr.PartNumber, pr.TypeDesignation, pr.EAN)
				d.PartCandidates = h.matchParts(ctx, codes, searchWords(append(pr.SearchTerms, pr.Name)...), "")
				d.Manufacturers = h.manufacturerOptions(ctx)
				d.ManufacturerHit, _ = matchSupplier(pr.Manufacturer, d.Manufacturers)
				d.NewPart = newPartFromRecognition(pr)
				d.NewPart.ManufacturerID = d.ManufacturerHit
			}
		}
	}
	h.render(w, "inventory_scan_detail", d)
}

// newPartFromRecognition: Vorbelegung „Neu anlegen“ aus der Foto-Erkennung.
func newPartFromRecognition(pr partResult) scanNewPart {
	np := scanNewPart{Name: strings.TrimSpace(pr.Name), Category: strings.TrimSpace(pr.Category), Unit: "Stück"}
	np.ManufacturerPart = strings.TrimSpace(pr.TypeDesignation)
	if np.ManufacturerPart == "" {
		np.ManufacturerPart = strings.TrimSpace(pr.PartNumber)
	}
	np.Number = strings.TrimSpace(pr.PartNumber)
	if np.Number == "" {
		np.Number = np.ManufacturerPart
	}
	var b strings.Builder
	if pr.Manufacturer != "" {
		b.WriteString("Hersteller: " + pr.Manufacturer + "\n")
	}
	for _, s := range pr.Specs {
		if strings.TrimSpace(s.Key) != "" && strings.TrimSpace(s.Value) != "" {
			b.WriteString(s.Key + ": " + s.Value + "\n")
		}
	}
	if pr.EAN != "" {
		b.WriteString("EAN: " + pr.EAN + "\n")
	}
	np.Description = strings.TrimSpace(b.String())
	return np
}

func parseQty(s string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
}

func scanBack(w http.ResponseWriter, r *http.Request, id, msg string, err error) {
	key, text := "msg", msg
	if err != nil {
		key, text = "err", err.Error()
	}
	http.Redirect(w, r, withNotice("/inventory/scan/"+id, key, text), http.StatusSeeOther)
}

// upsertPartSupplier: Lieferant am Teil vermerken und dessen Artikelnummer
// lernen (vorhandene Nummern bleiben) – so findet der nächste Scan das Teil direkt.
func (h *Handler) upsertPartSupplier(ctx context.Context, partID, supplierID, supplierNo, userID string) {
	if supplierID == "" {
		return
	}
	_, err := h.db.Exec(ctx, `INSERT INTO spare_part_suppliers (part_id, partner_id, supplier_part_no, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid)
		ON CONFLICT (part_id, partner_id) DO UPDATE SET
			supplier_part_no = COALESCE(NULLIF(spare_part_suppliers.supplier_part_no, ''), EXCLUDED.supplier_part_no), updated_at = NOW()`,
		partID, supplierID, truncateRunes(strings.TrimSpace(supplierNo), 99), nullID(userID))
	if err != nil {
		componentLog("lager-scan").Warn().Err(err).Str("teil", partID).Msg("lieferant am teil")
	}
}

// InventoryScanApplyWeb: POST /inventory/scan/{id}/apply – Lieferschein buchen.
func (h *Handler) InventoryScanApplyWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	s, _, result, _, err := h.loadScan(ctx, id)
	if err != nil || s.Kind != "delivery" || s.Status != "done" {
		scanBack(w, r, id, "", errors.New("Scan nicht gefunden oder noch nicht erkannt"))
		return
	}
	if s.Applied {
		scanBack(w, r, id, "", errors.New("Dieser Lieferschein wurde schon gebucht"))
		return
	}
	var dr deliveryResult
	_ = json.Unmarshal(result, &dr)
	_ = r.ParseForm()
	uid := getUser(r).ID
	supplierID := strings.TrimSpace(r.FormValue("supplier_id"))
	noteNo := truncateRunes(strings.TrimSpace(r.FormValue("note_no")), 90)
	defStore := strings.TrimSpace(r.FormValue("default_store"))
	n, _ := strconv.Atoi(r.FormValue("lines"))

	type pending struct {
		pos, action, partID, store, number, name, unit, supplierNo, mpn string
		qty                                                             float64
	}
	var todo []pending
	for i := 0; i < n && i < 500; i++ {
		f := func(k string) string { return strings.TrimSpace(r.FormValue(fmt.Sprintf("%s_%d", k, i))) }
		p := pending{action: f("act"), partID: f("part"), store: f("store"), number: f("num"), name: f("name"), unit: f("unit"), supplierNo: f("sno"), mpn: f("mpn"), pos: f("pos")}
		if p.action != "book" && p.action != "new" {
			continue
		}
		if p.pos == "" {
			p.pos = strconv.Itoa(i + 1)
		}
		q, err := parseQty(f("qty"))
		if err != nil || q <= 0 {
			scanBack(w, r, id, "", fmt.Errorf("Position %s: Menge fehlt", p.pos))
			return
		}
		p.qty = q
		if p.store == "" {
			p.store = defStore
		}
		if p.store == "" {
			scanBack(w, r, id, "", fmt.Errorf("Position %s: Lagerort fehlt – oben einen Lagerort für alle wählen", p.pos))
			return
		}
		if p.action == "book" && p.partID == "" {
			scanBack(w, r, id, "", fmt.Errorf("Position %s: Bitte ein Teil auswählen oder „Neu anlegen“ wählen", p.pos))
			return
		}
		if p.action == "new" && (p.number == "" || p.name == "") {
			scanBack(w, r, id, "", fmt.Errorf("Position %s: Für die Neuanlage Teilenummer und Bezeichnung angeben", p.pos))
			return
		}
		todo = append(todo, p)
	}
	if len(todo) == 0 {
		scanBack(w, r, id, "", errors.New("Keine Position zum Buchen ausgewählt"))
		return
	}
	ref := "Lieferschein"
	if noteNo != "" {
		ref += " " + noteNo
	}
	// 1. neue Teile anlegen
	created := 0
	for i := range todo {
		p := &todo[i]
		if p.action != "new" {
			continue
		}
		part, err := h.inv.Create(ctx, &inventory.CreatePartInput{PartNumber: p.number, Name: p.name, ManufacturerPart: p.mpn, Unit: p.unit, MinQty: 1, ReorderQty: 5}, uid)
		if err != nil {
			msg := friendlyPartError(err)
			if created > 0 {
				msg += fmt.Sprintf(" – %d neue Teile wurden schon angelegt, gebucht ist noch nichts", created)
			}
			scanBack(w, r, id, "", fmt.Errorf("Position %s: %s", p.pos, msg))
			return
		}
		created++
		p.partID = part.ID
		h.addHistory(ctx, "spare_part", part.ID, "create", "", "", p.number, "Angelegt aus Lieferschein-Scan ("+ref+")", uid)
	}
	// 2. Lieferanten-Artikelnummern lernen, 3. alles in einer Transaktion buchen
	var moves []*inventory.BookMovementInput
	for _, p := range todo {
		h.upsertPartSupplier(ctx, p.partID, supplierID, p.supplierNo, uid)
		moves = append(moves, &inventory.BookMovementInput{PartID: p.partID, Type: inventory.MovementIn, Qty: p.qty, StorageNodeID: p.store,
			Reference: truncateRunes(ref, 250), Notes: "Wareneingang per Lieferschein-Scan · Pos. " + p.pos})
	}
	if _, err := h.inv.BookMany(ctx, moves, uid); err != nil {
		msg := "Buchen fehlgeschlagen: " + err.Error()
		if created > 0 {
			msg += fmt.Sprintf(" (%d neue Teile sind angelegt – nach der Korrektur erneut „Buchen“, sie werden dann gefunden)", created)
		}
		scanBack(w, r, id, "", errors.New(msg))
		return
	}
	note := fmt.Sprintf("%d Zugänge gebucht", len(todo))
	if created > 0 {
		note += fmt.Sprintf(", davon %d neue Teile", created)
	}
	_, _ = h.db.Exec(ctx, `UPDATE inventory_scans SET applied_at = NOW(), applied_by = $2::uuid, applied_note = $3, supplier_id = $4, note_no = $5 WHERE id = $1::uuid`,
		id, nullID(uid), note, nullID(supplierID), noteNo)
	componentLog("lager-scan").Info().Str("scan", id).Int("zugaenge", len(todo)).Int("neu", created).Msg("lieferschein gebucht")
	scanBack(w, r, id, note, nil)
}

// InventoryScanCreatePartWeb: POST /inventory/scan/{id}/create – erkanntes Bauteil anlegen.
func (h *Handler) InventoryScanCreatePartWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	s, files, _, _, err := h.loadScan(ctx, id)
	if err != nil || s.Kind != "part" {
		scanBack(w, r, id, "", errors.New("Scan nicht gefunden"))
		return
	}
	_ = r.ParseForm()
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	in := &inventory.CreatePartInput{PartNumber: f("part_number"), Name: f("name"), ManufacturerPart: f("manufacturer_part"),
		Category: f("category"), Unit: f("unit"), Description: f("description"), MinQty: 1, ReorderQty: 5}
	if mid := f("manufacturer_id"); mid != "" {
		in.ManufacturerID = &mid
	}
	if in.PartNumber == "" || in.Name == "" {
		scanBack(w, r, id, "", errors.New("Teilenummer und Bezeichnung angeben"))
		return
	}
	uid := getUser(r).ID
	part, err := h.inv.Create(ctx, in, uid)
	if err != nil {
		scanBack(w, r, id, "", errors.New(friendlyPartError(err)))
		return
	}
	h.addHistory(ctx, "spare_part", part.ID, "create", "", "", in.PartNumber, "Angelegt aus Foto-Erkennung", uid)
	if r.FormValue("use_photo") == "1" && len(files) > 0 && strings.HasPrefix(files[0].Type, "image/") {
		if err := h.attachScanPhoto(ctx, part.ID, in.Description, files[0], uid); err != nil {
			componentLog("lager-scan").Warn().Err(err).Str("teil", part.ID).Msg("foto als teilebild")
		}
	}
	_, _ = h.db.Exec(ctx, `UPDATE inventory_scans SET applied_at = NOW(), applied_by = $2::uuid, applied_note = $3 WHERE id = $1::uuid`,
		id, nullID(uid), "Teil "+in.PartNumber+" angelegt")
	partRedirect(w, r, part.ID, "master", "Teil aus Foto-Erkennung angelegt – bitte Stammdaten prüfen und Bestand buchen", nil)
}

// attachScanPhoto: Foto des Scans als Teilebild übernehmen (wie PartImageSetWeb).
func (h *Handler) attachScanPhoto(ctx context.Context, partID, desc string, f scanFile, uid string) error {
	data, err := os.ReadFile(filepath.Join("uploads", filepath.FromSlash(f.Path)))
	if err != nil {
		return err
	}
	ext := filepath.Ext(f.Path)
	rel := filepath.ToSlash(filepath.Join("inventory", partID, "bild-"+randomHex(8)+ext))
	abs := filepath.Join("uploads", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return err
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO attachments (id, ref_type, ref_id, filename, filepath, mimetype, size_bytes, caption, created_by)
		VALUES (pdh_uuid_v7(), 'inventory', $1::uuid, $2, $3, $4, $5, 'Teilebild', $6)`,
		partID, "foto"+ext, rel, f.Type, len(data), nullID(uid)); err != nil {
		_ = os.Remove(abs)
		return err
	}
	_, err = h.db.Exec(ctx, `UPDATE spare_parts SET description = $1, updated_at = NOW() WHERE id = $2::uuid`, joinPartImage(desc, "/uploads/"+rel), partID)
	return err
}
