package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"rsc.io/qr"
)

// Lagerplatz-Etiketten fuer Ersatzteile: Lagerplatz, Bezeichnung,
// Mindestmenge, Teilenummer und QR-Code auf den Artikel im PDH.
// Druckseite ohne PDH-Rahmen, fuer Etikettendrucker (Rolle) und A4-Boegen.

type labelSize struct {
	Key, Label string
	W, H       float64 // Etikett in mm
	Sheet      bool    // A4-Bogen statt Rolle
	Cols, Rows int
	MarginTop  float64
	MarginLeft float64
	GapX, GapY float64
	Compact    bool // kleine Etiketten: weniger Zeilen
}

var labelSizes = []labelSize{
	{Key: "62x29", Label: "Brother QL 62 × 29 mm", W: 62, H: 29, Compact: true},
	{Key: "62x50", Label: "Brother QL 62 × 50 mm", W: 62, H: 50},
	{Key: "54x25", Label: "Dymo 54 × 25 mm (11352)", W: 54, H: 25, Compact: true},
	{Key: "57x32", Label: "Dymo 57 × 32 mm (11354)", W: 57, H: 32, Compact: true},
	{Key: "89x36", Label: "Dymo 89 × 36 mm (99012)", W: 89, H: 36},
	{Key: "100x50", Label: "Thermo 100 × 50 mm", W: 100, H: 50},
	{Key: "a4-70x37", Label: "A4-Bogen 3 × 8 (70 × 37 mm)", W: 70, H: 37, Sheet: true, Cols: 3, Rows: 8, MarginTop: 0.5},
	{Key: "a4-105x42", Label: "A4-Bogen 2 × 7 (105 × 42,3 mm)", W: 105, H: 42.3, Sheet: true, Cols: 2, Rows: 7, MarginTop: 0.4},
	{Key: "a4-70x42", Label: "A4-Bogen 3 × 7 (70 × 42,3 mm)", W: 70, H: 42.3, Sheet: true, Cols: 3, Rows: 7, MarginTop: 0.4},
}

func labelSizeByKey(k string) labelSize {
	for _, s := range labelSizes {
		if s.Key == k {
			return s
		}
	}
	return labelSizes[0]
}

// qrSVG rendert einen QR-Code (Fehlerkorrektur M) als SVG mit Ruhezone.
func qrSVG(text string) (template.HTML, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	n := code.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n, n, n)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; {
			if !code.Black(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < code.Size && code.Black(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x+quiet, y+quiet, run, run)
			x += run
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String()), nil // nur selbst erzeugtes Markup
}

// publicBaseURL: PDH_PUBLIC_URL, sonst aus der Anfrage (Proxy-Header beachten).
func (h *Handler) publicBaseURL(r *http.Request) string {
	if u := strings.TrimRight(strings.TrimSpace(h.mailCfg.PublicURL), "/"); u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

type labelItem struct {
	PartID, PartNumber, Name, Category, Unit, MinQty string
	Location, LocationLeaf                           string
	URL                                              string
	QR                                               template.HTML
	// Anlagen-/IT-Etiketten (asset_info.go): Nummer ohne "Nr."-Praefix,
	// rechts unten ein Hinweis statt der Mindestmenge
	Asset            bool
	NumKey, RightKey string
	Right            string
}

// numText: Nummer unten links (Teile-Nr. bzw. Serien-/Inventarnummer).
func (it labelItem) numText() string {
	if it.Asset {
		return it.PartNumber
	}
	return "Nr. " + it.PartNumber
}

// rightText: unten rechts – Mindestmenge bzw. Hinweis auf Anlagen-Etiketten.
func (it labelItem) rightText() string {
	if it.Asset {
		return it.Right
	}
	return strings.TrimSpace("Min " + it.MinQty + " " + it.Unit)
}

// headText: oberste Zeile – Lagerplatz bzw. Name der Anlage.
func (it labelItem) headText() string {
	return firstNonEmpty(it.LocationLeaf, "ohne Lagerplatz")
}

type LabelsPageData struct {
	Size    labelSize
	Sizes   []labelSize
	Labels  []labelItem
	Pages   [][]*labelItem // Seiten (A4) bzw. einzelne Etiketten (Rolle)
	Copies  int
	Start   int
	ShowCat bool
	Params  []struct{ Key, Value string } // Auswahl fuer das Einstellungsformular
	Title   string
	Logo    string // Drucklogo (Erscheinungsbild: "Logo auf Etiketten")
	Error   string
	// direkt drucken (printers.go): eingerichtete, aktive Drucker
	Printers []labelPrintOption
	// Formular- und Druckadresse (Ersatzteile: /inventory/labels, Anlagen: /a/labels)
	Action, PrintURL string
	Assets           bool // Anlagen-/IT-Etiketten: keine Kategorie-Auswahl

}

// PartLabelsPage erzeugt die Druckansicht. Auswahl (kombinierbar):
//
//	part=ID (mehrfach)  - alle Lagerplaetze dieser Teile (loc=NODE: nur dieser Platz)
//	node=ID             - alle Teile an diesem Lagerplatz inkl. Unterplaetze
//	list=1&cat=&q=&status= - alle Teile des aktuellen Listenfilters
func (h *Handler) PartLabelsPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, "inventory.view") && !h.hasPerm(r, "inventory.edit") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	data := LabelsPageData{
		Size: labelSizeByKey(q.Get("size")), Sizes: labelSizes,
		Copies: clampInt(q.Get("copies"), 1, 1, 50), Start: clampInt(q.Get("start"), 0, 0, 40),
		ShowCat: q.Get("cat_line") != "0",
		Title:   "Lagerplatz-Etiketten",
		Logo:    h.labelLogo(),
	}
	for _, k := range []string{"part", "loc", "node", "list", "cat", "q", "status"} {
		for _, v := range q[k] {
			data.Params = append(data.Params, struct{ Key, Value string }{k, v})
		}
	}
	items, err := h.loadLabelItems(ctx, q)
	if err != nil {
		data.Error = err.Error()
	}
	base := h.publicBaseURL(r)
	for i := range items {
		items[i].URL = base + "/inventory/" + items[i].PartID
	}
	h.renderLabelsPage(w, r, data, items)
}

// renderLabelsPage: Druckansicht (QR-Codes, Kopien, A4-Seiten) fuer fertige Etiketten.
func (h *Handler) renderLabelsPage(w http.ResponseWriter, r *http.Request, data LabelsPageData, items []labelItem) {
	ctx := r.Context()
	if data.Action == "" {
		data.Action, data.PrintURL = "/inventory/labels", "/inventory/labels/print"
	}
	if h.hasPerm(r, "printers.use") {
		data.Printers = h.usablePrinters(ctx)
	}
	for i := range items {
		if svg, err := qrSVG(items[i].URL); err == nil {
			items[i].QR = svg
		}
	}
	// Kopien und (bei A4) uebersprungene Felder bereits benutzter Boegen
	var flat []*labelItem
	if data.Size.Sheet {
		for i := 0; i < data.Start; i++ {
			flat = append(flat, nil)
		}
	}
	for i := range items {
		for c := 0; c < data.Copies; c++ {
			flat = append(flat, &items[i])
		}
	}
	data.Labels = items
	per := 1
	if data.Size.Sheet {
		per = data.Size.Cols * data.Size.Rows
	}
	for i := 0; i < len(flat); i += per {
		end := i + per
		if end > len(flat) {
			end = len(flat)
		}
		data.Pages = append(data.Pages, flat[i:end])
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		_, err = t.ParseFiles("web/templates/labels.gohtml")
	}
	if err == nil {
		err = t.ExecuteTemplate(w, "labels-page", data)
	}
	if err != nil {
		http.Error(w, "Template-Fehler: "+err.Error(), http.StatusInternalServerError)
	}
}

func clampInt(s string, def, min, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

const maxLabels = 1000

func (h *Handler) loadLabelItems(ctx context.Context, q map[string][]string) ([]labelItem, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	_, paths := h.storagePaths(ctx)
	where := []string{"sp.active"}
	var args []interface{}
	add := func(cond string, v interface{}) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	stockCond := "true"
	switch {
	case len(q["part"]) > 0:
		add("sp.id::text = ANY($?::text[])", q["part"])
		if loc := get("loc"); loc != "" {
			args = append(args, loc)
			stockCond = "st.storage_node_id::text = $" + strconv.Itoa(len(args))
		}
	case get("node") != "":
		args = append(args, get("node"))
		stockCond = fmt.Sprintf(`st.storage_node_id IN (
			WITH RECURSIVE sub AS (SELECT id FROM storage_nodes WHERE id::text = $%d
			                       UNION ALL SELECT n.id FROM storage_nodes n JOIN sub ON n.parent_id = sub.id)
			SELECT id FROM sub)`, len(args))
		where = append(where, "st.part_id IS NOT NULL")
	case get("list") == "1":
		switch cat := get("cat"); cat {
		case "":
		case partNoCategory:
			where = append(where, "COALESCE(sp.category, '') = ''")
		default:
			add("sp.category = $?", cat)
		}
		switch get("status") {
		case "low":
			where = append(where, "sp.stock_qty <= sp.min_qty")
		case "critical":
			where = append(where, "sp.stock_qty <= sp.critical_qty")
		case "empty":
			where = append(where, "sp.stock_qty <= 0")
		case "nosupplier":
			where = append(where, "NOT EXISTS (SELECT 1 FROM spare_part_suppliers s WHERE s.part_id = sp.id)")
		}
		if s := get("q"); s != "" {
			add(`(sp.part_number || ' ' || sp.name || ' ' || COALESCE(sp.category, '') || ' ' || COALESCE(sp.manufacturer_part, '')) ILIKE $?`, likePattern(s))
		}
	default:
		return nil, fmt.Errorf("keine Auswahl – Etiketten aus einem Ersatzteil, einem Lagerplatz oder der Ersatzteilliste aufrufen")
	}
	rows, err := h.db.Query(ctx, `
		SELECT sp.id::text, sp.part_number, sp.name, COALESCE(sp.category, ''), sp.unit, sp.min_qty::float8,
		       COALESCE(st.storage_node_id::text, '')
		FROM spare_parts sp
		LEFT JOIN spare_part_stock st ON st.part_id = sp.id AND `+stockCond+`
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY sp.name, st.storage_node_id
		LIMIT `+strconv.Itoa(maxLabels), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []labelItem
	for rows.Next() {
		var it labelItem
		var min float64
		var node string
		if rows.Scan(&it.PartID, &it.PartNumber, &it.Name, &it.Category, &it.Unit, &min, &node) != nil {
			continue
		}
		it.MinQty = formatQty(min)
		if node != "" {
			it.Location = paths[node]
			if parts := strings.Split(it.Location, " › "); len(parts) > 0 {
				it.LocationLeaf = parts[len(parts)-1]
			}
		}
		items = append(items, it)
	}
	// Sortierung nach Lagerplatz, damit die Etiketten in Regal-Reihenfolge kommen
	sortLabelsByLocation(items)
	return items, rows.Err()
}

func sortLabelsByLocation(items []labelItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && labelLess(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func labelLess(a, b labelItem) bool {
	if a.Location != b.Location {
		if a.Location == "" || b.Location == "" {
			return b.Location == ""
		}
		return naturalLess(a.Location, b.Location)
	}
	return strings.ToLower(a.Name) < strings.ToLower(b.Name)
}

// naturalLess vergleicht "Fach 2" < "Fach 10".
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := leadingDigits(a), leadingDigits(b)
		if ad != "" && bd != "" {
			an, _ := strconv.Atoi(ad)
			bn, _ := strconv.Atoi(bd)
			if an != bn {
				return an < bn
			}
			a, b = a[len(ad):], b[len(bd):]
			continue
		}
		ra, rb := strings.ToLower(a[:1]), strings.ToLower(b[:1])
		if ra != rb {
			return ra < rb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
