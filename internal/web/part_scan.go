package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/faults"
)

// Ersatzteile per Smartphone erfassen (migrations/128):
//   - Lieferschein fotografieren (mehrere Seiten oder PDF) → Positionen
//     erkennen → je Position Zugang zu einem vorhandenen Teil oder Neuanlage
//     vorschlagen → nach Prüfung gesammelt buchen.
//   - Bauteil fotografieren → Hersteller, Typ, Nummern erkennen → mit dem
//     Bestand abgleichen → Teil öffnen oder vorausgefüllt neu anlegen.
// Die Erkennung (Claude, faults.Copilot.AnalyzeVision) läuft im Hintergrund,
// damit weder Cloudflare (100 s) noch der Server (120 s) die Anfrage abbrechen.

const (
	scanDir          = "uploads/inventory-scans"
	scanMaxFiles     = 8
	scanMaxFileBytes = 20 << 20
	scanMaxTotal     = 40 << 20
	scanTimeout      = 4 * time.Minute
)

// ── Ergebnis der Bilderkennung ─────────────────────────────────

type deliveryItem struct {
	Position           string  `json:"position"`
	SupplierArticleNo  string  `json:"supplier_article_no"`
	ManufacturerPartNo string  `json:"manufacturer_part_no"`
	Manufacturer       string  `json:"manufacturer"`
	Description        string  `json:"description"`
	Quantity           float64 `json:"quantity"`
	OrderedQuantity    float64 `json:"ordered_quantity"`   // bestellt (0 = nicht angegeben)
	BackorderQuantity  float64 `json:"backorder_quantity"` // Rückstand laut Lieferschein
	Unit               string  `json:"unit"`
	EAN                string  `json:"ean"`
}

type deliveryResult struct {
	SupplierName   string         `json:"supplier_name"`
	DeliveryNoteNo string         `json:"delivery_note_no"`
	DeliveryDate   string         `json:"delivery_date"`
	OrderNo        string         `json:"order_no"`
	Items          []deliveryItem `json:"items"`
}

type partSpec struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type partResult struct {
	Recognized      bool       `json:"recognized"`
	Name            string     `json:"name"`
	Manufacturer    string     `json:"manufacturer"`
	PartNumber      string     `json:"part_number"`
	TypeDesignation string     `json:"type_designation"`
	Category        string     `json:"category"`
	Description     string     `json:"description"`
	EAN             string     `json:"ean"`
	Specs           []partSpec `json:"specs"`
	SearchTerms     []string   `json:"search_terms"`
	Confidence      string     `json:"confidence"`
	Notes           string     `json:"notes"`
}

func objSchema(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	sort.Strings(req)
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

var deliverySchema = objSchema(map[string]any{
	"supplier_name":    strProp("Firmenname des Lieferanten (Absender), leer wenn nicht lesbar"),
	"delivery_note_no": strProp("Lieferschein-Nummer, leer wenn nicht vorhanden"),
	"delivery_date":    strProp("Lieferdatum als JJJJ-MM-TT, leer wenn nicht lesbar"),
	"order_no":         strProp("Bestell- bzw. Auftragsnummer des Kunden, leer wenn nicht vorhanden"),
	"items": map[string]any{"type": "array", "items": objSchema(map[string]any{
		"position":             strProp("Positionsnummer auf dem Lieferschein"),
		"supplier_article_no":  strProp("Artikelnummer des Lieferanten genau wie gedruckt"),
		"manufacturer_part_no": strProp("Hersteller-Artikelnummer bzw. Typbezeichnung, falls getrennt angegeben"),
		"manufacturer":         strProp("Hersteller, falls angegeben"),
		"description":          strProp("Artikelbezeichnung"),
		"quantity":             map[string]any{"type": "number", "description": "gelieferte Menge (nicht bestellt, nicht Rückstand)"},
		"ordered_quantity":     map[string]any{"type": "number", "description": "bestellte Menge, falls auf dem Lieferschein angegeben, sonst 0"},
		"backorder_quantity":   map[string]any{"type": "number", "description": "Rückstand bzw. noch offene Menge, falls angegeben, sonst 0"},
		"unit":                 strProp("Einheit wie Stück, m, kg, Pack – leer wenn nicht angegeben"),
		"ean":                  strProp("EAN/GTIN, falls angegeben"),
	})},
})

var partSchema = objSchema(map[string]any{
	"recognized":       map[string]any{"type": "boolean", "description": "true, wenn ein Bauteil zu erkennen ist"},
	"name":             strProp("kurze deutsche Bezeichnung, z. B. „Rillenkugellager 6205-2RS“"),
	"manufacturer":     strProp("Hersteller, nur wenn sichtbar oder eindeutig erkennbar"),
	"part_number":      strProp("Bestell- bzw. Artikelnummer genau wie aufgedruckt, sonst leer"),
	"type_designation": strProp("Typbezeichnung genau wie aufgedruckt, sonst leer"),
	"category":         strProp("Kategorie, z. B. Lager, Motor, Sensor, Ventil, Dichtung, Riemen, Elektrik"),
	"description":      strProp("ein bis zwei Sätze, woran das Teil erkannt wurde"),
	"ean":              strProp("EAN/GTIN vom Etikett, sonst leer"),
	"specs": map[string]any{"type": "array", "items": objSchema(map[string]any{
		"key": strProp("Merkmal, z. B. Spannung, Leistung, Innendurchmesser"), "value": strProp("Wert mit Einheit"),
	})},
	"search_terms": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "2–5 Suchbegriffe für den Abgleich mit dem Lagerbestand"},
	"confidence":   map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
	"notes":        strProp("Hinweise, z. B. was zur sicheren Bestimmung fehlt (weiteres Foto vom Typenschild)"),
})

const scanSystem = `Du erfasst Waren für das Ersatzteillager eines Industriebetriebs (Instandhaltung).
Übernimm Nummern und Bezeichnungen zeichengenau wie gedruckt – keine Nummern erfinden oder ergänzen.
Was nicht lesbar ist, bleibt leer. Antworte ausschließlich im vorgegebenen JSON-Format.`

const deliveryPrompt = `Die Bilder bzw. das PDF zeigen einen Lieferschein (eine oder mehrere Seiten).
Lies Kopf (Lieferant, Lieferschein-Nr., Datum, Bestell-Nr.) und alle gelieferten Positionen.
Je Position: Artikelnummer des Lieferanten, Hersteller-Nummer/Typ (falls getrennt), Bezeichnung, gelieferte Menge, Einheit, EAN –
und falls angegeben die bestellte Menge und den Rückstand (offene bzw. nachzuliefernde Menge).
Keine Summen-, Versand-, Verpackungs- oder Textzeilen als Position. Positionen, die über mehrere Seiten gehen, nur einmal.`

const partPrompt = `Das Foto zeigt ein Bauteil bzw. Ersatzteil, eventuell mit Typenschild, Etikett oder Verpackung.
Bestimme, was es ist: Bezeichnung, Hersteller, aufgedruckte Artikel- bzw. Typnummer, Kategorie und wichtige technische Merkmale.
Nummern nur übernehmen, wenn sie im Bild lesbar sind. Gib Suchbegriffe an, unter denen das Teil im Lager stehen könnte.`

// ── Abgleich mit dem Bestand ───────────────────────────────────

var codeCleanRe = regexp.MustCompile(`[^a-z0-9]`)

// normCode: Nummern vergleichbar machen („6205-2RS“ = „6205 2rs“).
func normCode(s string) string { return codeCleanRe.ReplaceAllString(strings.ToLower(s), "") }

// normCodes: brauchbare Nummern (mind. 3 Zeichen, ohne Dubletten).
func normCodes(codes ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range codes {
		n := normCode(c)
		if len(n) >= 3 && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

type partCandidate struct {
	ID, PartNumber, Name, Unit, Reason string
	Stock, MinQty                      float64
	Score                              int
	LastStorageID                      string // Lagerort mit dem meisten Bestand
}

func (c partCandidate) StockText() string { return formatQty(c.Stock) }

// matchParts sucht Teile über Nummern (Teile-, Hersteller-, Lieferanten-
// Artikelnummer) und – wenn das nichts liefert – über Wörter im Namen.
func (h *Handler) matchParts(ctx context.Context, codes []string, words []string, supplierID string) []partCandidate {
	var out []partCandidate
	if len(codes) > 0 {
		rows, err := h.db.Query(ctx, `
			SELECT * FROM (
				SELECT p.id::text, p.part_number, p.name, COALESCE(p.unit, ''), COALESCE(p.stock_qty, 0), COALESCE(p.min_qty, 0),
				       GREATEST(
				         CASE WHEN regexp_replace(lower(p.part_number), '[^a-z0-9]', '', 'g') = ANY($1) THEN 95 ELSE 0 END,
				         CASE WHEN regexp_replace(lower(COALESCE(p.manufacturer_part, '')), '[^a-z0-9]', '', 'g') = ANY($1) THEN 90 ELSE 0 END,
				         CASE WHEN p.ean <> '' AND p.ean = ANY($1) THEN 97 ELSE 0 END,
				         COALESCE((SELECT MAX(CASE WHEN s.partner_id = $2::uuid THEN 100 ELSE 85 END) FROM spare_part_suppliers s
				                   WHERE s.part_id = p.id AND regexp_replace(lower(COALESCE(s.supplier_part_no, '')), '[^a-z0-9]', '', 'g') = ANY($1)), 0)
				       ) AS score,
				       COALESCE((SELECT st.storage_node_id::text FROM spare_part_stock st WHERE st.part_id = p.id ORDER BY st.qty DESC LIMIT 1), '')
				  FROM spare_parts p WHERE p.active AND p.hidden_at IS NULL
			) x WHERE score > 0 ORDER BY score DESC, name LIMIT 5`, codes, nullID(supplierID))
		if err == nil {
			for rows.Next() {
				var c partCandidate
				if rows.Scan(&c.ID, &c.PartNumber, &c.Name, &c.Unit, &c.Stock, &c.MinQty, &c.Score, &c.LastStorageID) == nil {
					c.Reason = map[int]string{100: "Artikelnr. dieses Lieferanten", 97: "EAN", 95: "Teilenummer", 90: "Hersteller-Nr.", 85: "Artikelnr. eines Lieferanten"}[c.Score]
					out = append(out, c)
				}
			}
			rows.Close()
		} else {
			componentLog("lager-scan").Warn().Err(err).Msg("abgleich nummern")
		}
	}
	if len(out) > 0 || len(words) == 0 {
		return out
	}
	// alle Wörter im Namen oder in der Beschreibung (höchstens vier)
	if len(words) > 4 {
		words = words[:4]
	}
	cond := make([]string, len(words))
	args := make([]any, len(words))
	for i, w := range words {
		cond[i] = fmt.Sprintf("(p.name ILIKE $%d OR p.description ILIKE $%d OR p.part_number ILIKE $%d)", i+1, i+1, i+1)
		args[i] = likePattern(w)
	}
	rows, err := h.db.Query(ctx, `SELECT p.id::text, p.part_number, p.name, COALESCE(p.unit, ''), COALESCE(p.stock_qty, 0), COALESCE(p.min_qty, 0),
			COALESCE((SELECT st.storage_node_id::text FROM spare_part_stock st WHERE st.part_id = p.id ORDER BY st.qty DESC LIMIT 1), '')
		FROM spare_parts p WHERE p.active AND p.hidden_at IS NULL AND `+strings.Join(cond, " AND ")+` ORDER BY p.name LIMIT 5`, args...)
	if err != nil {
		componentLog("lager-scan").Warn().Err(err).Msg("abgleich namen")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		c := partCandidate{Score: 40, Reason: "ähnlicher Name"}
		if rows.Scan(&c.ID, &c.PartNumber, &c.Name, &c.Unit, &c.Stock, &c.MinQty, &c.LastStorageID) == nil {
			out = append(out, c)
		}
	}
	return out
}

var wordSplitRe = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}.\-/]*`)

// searchWords: aussagekräftige Wörter aus einer Bezeichnung (ohne Füllwörter, Einheiten).
func searchWords(texts ...string) []string {
	stop := map[string]bool{"und": true, "mit": true, "für": true, "fuer": true, "der": true, "die": true, "das": true, "aus": true,
		"stk": true, "stück": true, "stueck": true, "pack": true, "set": true, "the": true, "and": true, "for": true, "with": true}
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, w := range wordSplitRe.FindAllString(t, -1) {
			lw := strings.ToLower(strings.Trim(w, ".-/"))
			if len([]rune(lw)) < 3 || stop[lw] || seen[lw] {
				continue
			}
			seen[lw] = true
			out = append(out, lw)
		}
	}
	return out
}

var legalFormRe = regexp.MustCompile(`(?i)\b(gmbh|mbh|ag|kg|ohg|gbr|ug|e\.?\s?k\.?|co\.?|ltd\.?|inc\.?|s\.?a\.?|s\.?r\.?l\.?|bv|nv|se|haftungsbeschränkt)\b|&|\+|[.,()]`)

// normCompany: „Würth Industrie Service GmbH & Co. KG“ → „würth industrie service“.
func normCompany(s string) string {
	s = legalFormRe.ReplaceAllString(strings.ToLower(s), " ")
	return strings.Join(strings.Fields(s), " ")
}

// matchSupplier: Lieferant aus dem Verzeichnis zum Namen auf dem Lieferschein.
func matchSupplier(name string, suppliers []partnerOption) (id, label string) {
	n := normCompany(name)
	if len(n) < 3 {
		return "", ""
	}
	best := 0
	for _, s := range suppliers {
		c := normCompany(strings.TrimSuffix(s.Label, " (gesperrt)"))
		if c == "" {
			continue
		}
		score := 0
		switch {
		case c == n:
			score = 3
		case len(c) >= 4 && strings.Contains(n, c), len(n) >= 4 && strings.Contains(c, n):
			score = 2
		case strings.Fields(c)[0] == strings.Fields(n)[0] && len(strings.Fields(c)[0]) >= 4:
			score = 1
		}
		if score > best {
			best, id, label = score, s.Value, s.Label
		}
	}
	return id, label
}

// ── Hochladen und Erkennung im Hintergrund ─────────────────────

type scanFile struct {
	Path string `json:"path"` // relativ zu uploads/
	Name string `json:"name"`
	Type string `json:"type"`
}

// readScanFiles: hochgeladene Fotos/PDFs prüfen und unter uploads/inventory-scans/<id>/ ablegen.
func readScanFiles(scanID string, fhs []*multipart.FileHeader) ([]scanFile, []faults.VisionInput, error) {
	if len(fhs) == 0 {
		return nil, nil, errors.New("Bitte ein Foto aufnehmen oder eine Datei auswählen")
	}
	if len(fhs) > scanMaxFiles {
		return nil, nil, fmt.Errorf("Höchstens %d Seiten auf einmal", scanMaxFiles)
	}
	dir := filepath.Join(scanDir, scanID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	var files []scanFile
	var inputs []faults.VisionInput
	total := 0
	for i, fh := range fhs {
		f, err := fh.Open()
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, scanMaxFileBytes+1))
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(data) > scanMaxFileBytes {
			return nil, nil, fmt.Errorf("„%s“ ist größer als %d MB", fh.Filename, scanMaxFileBytes>>20)
		}
		if total += len(data); total > scanMaxTotal {
			return nil, nil, fmt.Errorf("Zusammen höchstens %d MB", scanMaxTotal>>20)
		}
		mt := http.DetectContentType(data)
		ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "application/pdf": ".pdf"}[mt]
		if ext == "" {
			return nil, nil, fmt.Errorf("„%s“: Format %s wird nicht unterstützt – Foto (JPEG, PNG) oder PDF verwenden", fh.Filename, mt)
		}
		rel := filepath.ToSlash(filepath.Join("inventory-scans", scanID, fmt.Sprintf("seite-%d-%s%s", i+1, randomHex(6), ext)))
		if err := os.WriteFile(filepath.Join("uploads", rel), data, 0o644); err != nil {
			return nil, nil, err
		}
		files = append(files, scanFile{Path: rel, Name: filepath.Base(fh.Filename), Type: mt})
		inputs = append(inputs, faults.VisionInput{MediaType: mt, Data: data})
	}
	return files, inputs, nil
}

// runScan: Erkennung im Hintergrund, Ergebnis in inventory_scans.
func (h *Handler) runScan(scanID, kind string, inputs []faults.VisionInput) {
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	prompt, schema := deliveryPrompt, deliverySchema
	if kind == "part" {
		prompt, schema = partPrompt, partSchema
	}
	var out string
	err := errors.New("Copilot nicht eingerichtet")
	if h.faults != nil && h.faults.Copilot() != nil {
		out, err = h.faults.Copilot().AnalyzeVision(ctx, scanSystem, prompt, inputs, schema, "medium")
	}
	if err == nil && !json.Valid([]byte(out)) {
		err = errors.New("Antwort der Bilderkennung war kein gültiges JSON")
	}
	if err != nil {
		componentLog("lager-scan").Warn().Err(err).Str("scan", scanID).Str("art", kind).Msg("erkennung")
		_, _ = h.db.Exec(ctx, `UPDATE inventory_scans SET status = 'error', error = $2 WHERE id = $1::uuid`, scanID, err.Error())
		return
	}
	var noteNo string
	if kind == "delivery" {
		var d deliveryResult
		if json.Unmarshal([]byte(out), &d) == nil {
			noteNo = truncateRunes(d.DeliveryNoteNo, 90)
		}
	}
	_, _ = h.db.Exec(ctx, `UPDATE inventory_scans SET status = 'done', result = $2::jsonb, note_no = $3 WHERE id = $1::uuid`, scanID, out, noteNo)
	componentLog("lager-scan").Info().Str("scan", scanID).Str("art", kind).Msg("erkannt")
}

// InventoryScanUploadWeb: POST /inventory/scan/{kind} (delivery|part), Dateien in „files“.
func (h *Handler) InventoryScanUploadWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	kind := chi.URLParam(r, "kind")
	if kind != "delivery" && kind != "part" {
		http.NotFound(w, r)
		return
	}
	back := "/inventory/scan?tab=" + kind
	if h.faults == nil || h.faults.Copilot() == nil || !h.faults.Copilot().VisionAvailable() {
		http.Redirect(w, r, withNotice(back, "err", faults.ErrVisionUnavailable.Error()), http.StatusSeeOther)
		return
	}
	// Fotos über das Mobilnetz brauchen länger als das Lese-Limit des Servers (15 s)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(3 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, scanMaxTotal+(2<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Redirect(w, r, withNotice(back, "err", fmt.Sprintf("Upload zu groß oder abgebrochen (höchstens %d MB)", scanMaxTotal>>20)), http.StatusSeeOther)
		return
	}
	fhs := r.MultipartForm.File["files"]
	if kind == "part" && len(fhs) > 3 {
		fhs = fhs[:3]
	}
	ctx := r.Context()
	var id string
	if err := h.db.QueryRow(ctx, `INSERT INTO inventory_scans (kind, created_by) VALUES ($1, $2::uuid) RETURNING id::text`, kind, nullID(getUser(r).ID)).Scan(&id); err != nil {
		http.Redirect(w, r, withNotice(back, "err", friendlyDBError(err)), http.StatusSeeOther)
		return
	}
	files, inputs, err := readScanFiles(id, fhs)
	if err != nil {
		_, _ = h.db.Exec(ctx, `DELETE FROM inventory_scans WHERE id = $1::uuid`, id)
		_ = os.RemoveAll(filepath.Join(scanDir, id))
		http.Redirect(w, r, withNotice(back, "err", err.Error()), http.StatusSeeOther)
		return
	}
	raw, _ := json.Marshal(files)
	_, _ = h.db.Exec(ctx, `UPDATE inventory_scans SET files = $2::jsonb WHERE id = $1::uuid`, id, string(raw))
	go h.runScan(id, kind, inputs)
	http.Redirect(w, r, "/inventory/scan/"+id, http.StatusSeeOther)
}
