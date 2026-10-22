package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
)

// Etiketten-Designer (/inventory/labels/designer): je Etikettengröße ein
// Layout aus frei platzierbaren Feldern – Position und Größe in mm,
// Schriftgröße in pt, fett/Festbreite, Ausrichtung, maximale Zeichenzahl und
// Zeilen, Präfix, Unterstrich. Gespeichert in app_settings
// ("label_layout:<Größe>"). Ohne gespeichertes Layout druckt PDH wie bisher.
// Mit Layout zeichnen alle Wege danach: Browser, PDF (Netzwerkdrucker),
// Zebra (ZPL) und Dymo – bei abweichender Medien-Größe maßstäblich.

type labelElement struct {
	Key     string  `json:"key"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	Size    float64 `json:"size"`  // Schrift in pt
	Bold    bool    `json:"bold"`  // fett
	Mono    bool    `json:"mono"`  // Festbreite (Nummern)
	Align   string  `json:"align"` // left|center|right
	Max     int     `json:"max"`   // höchstens so viele Zeichen (0 = egal)
	Lines   int     `json:"lines"` // höchstens so viele Zeilen (1–4)
	Line    bool    `json:"line"`  // Linie unter dem Feld
	Prefix  string  `json:"prefix"`
	Text    string  `json:"text"` // Freitext
	Visible bool    `json:"visible"`
}

type labelLayout struct {
	Size     string         `json:"size"`
	Elements []labelElement `json:"elements"`
}

// labelFields: Felder, die der Designer kennt (Reihenfolge = Liste im Designer).
var labelFields = []struct{ Key, Label, Sample string }{
	{"qr", "QR-Code (Buchung)", ""},
	{"logo", "Logo", ""},
	{"loc", "Lagerplatz", "Fach 3"},
	{"path", "Lagerpfad", "Halle 1 › Regal A › Fach 3"},
	{"name", "Bezeichnung", "Rillenkugellager 6205-2RSH"},
	{"cat", "Kategorie", "Wälzlager"},
	{"pn", "Teilenummer", "4711-6205-2RSH-C3"},
	{"mf", "Hersteller", "SKF"},
	{"mpn", "Hersteller-Teilenr.", "6205-2RSH/C3"},
	{"min", "Mindestmenge", "4 Stück"},
	{"ean", "EAN", "7316571234567"},
	{"text", "Freitext", "Freitext"},
}

func labelFieldLabel(key string) string {
	for _, f := range labelFields {
		if f.Key == key {
			return f.Label
		}
	}
	return key
}

// defaultLabelLayout: entspricht der bisherigen festen Gestaltung.
func defaultLabelLayout(s labelSize) labelLayout {
	pad := 1.5
	q := s.H - 2*pad
	tx := pad + q + 1.5
	tw := s.W - tx - 2
	fs := func(c, n float64) float64 {
		if s.Compact {
			return c
		}
		return n
	}
	locH, nameH, rowH := fs(3.6, 4.4), fs(5.6, 6.8), fs(3.2, 3.8)
	pnY := s.H - pad - rowH - fs(2.6, 3.0)
	el := []labelElement{
		{Key: "qr", X: pad, Y: pad, W: q, H: q, Visible: true},
		{Key: "logo", X: s.W - 13.5, Y: 1.2, W: 12, H: 3.6, Visible: !s.Compact},
		{Key: "loc", X: tx, Y: pad, W: tw, H: locH, Size: fs(9, 11), Bold: true, Lines: 1, Line: true, Visible: true},
		{Key: "path", X: tx, Y: pad + locH + 0.3, W: tw, H: 2.2, Size: 5.5, Lines: 1, Visible: !s.Compact},
		{Key: "name", X: tx, Y: pad + locH + fs(0.6, 2.8), W: tw, H: nameH, Size: fs(7, 8.5), Bold: true, Lines: 2, Visible: true},
		{Key: "cat", X: tx, Y: pad + locH + fs(0.6, 2.8) + nameH + 0.2, W: tw, H: 2.2, Size: 5.5, Lines: 1, Visible: !s.Compact},
		{Key: "pn", X: tx, Y: pnY, W: tw, H: fs(3.0, 3.6), Size: fs(8, 10), Bold: true, Mono: true, Lines: 2, Visible: true},
		{Key: "mf", X: tx, Y: s.H - pad - rowH + 0.4, W: tw * 0.62, H: rowH - 0.4, Size: fs(6, 7), Bold: true, Lines: 1, Visible: true},
		{Key: "min", X: tx + tw*0.62, Y: s.H - pad - rowH + 0.4, W: tw * 0.38, H: rowH - 0.4, Size: fs(6.5, 7.5), Bold: true, Align: "right", Lines: 1, Prefix: "Min ", Visible: true},
		{Key: "mpn", X: tx, Y: pnY - 2.6, W: tw, H: 2.4, Size: 6, Lines: 1, Visible: false},
		{Key: "ean", X: tx, Y: pnY - 2.6, W: tw, H: 2.4, Size: 6, Mono: true, Lines: 1, Visible: false},
		{Key: "text", X: tx, Y: pnY - 2.6, W: tw, H: 2.4, Size: 6, Lines: 1, Text: "Freitext", Visible: false},
	}
	for i := range el {
		el[i] = roundElement(el[i])
	}
	return labelLayout{Size: s.Key, Elements: el}
}

func roundElement(e labelElement) labelElement {
	r := func(v float64) float64 { return math.Round(v*10) / 10 }
	e.X, e.Y, e.W, e.H = r(e.X), r(e.Y), r(e.W), r(e.H)
	if e.Align == "" {
		e.Align = "left"
	}
	if e.Lines < 1 {
		e.Lines = 1
	}
	return e
}

// sanitize: Werte begrenzen, unbekannte Felder verwerfen, fehlende ergänzen.
func (l *labelLayout) sanitize(s labelSize) {
	known := map[string]bool{}
	for _, f := range labelFields {
		known[f.Key] = true
	}
	clamp := func(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
	seen := map[string]bool{}
	var out []labelElement
	for _, e := range l.Elements {
		if !known[e.Key] || seen[e.Key] {
			continue
		}
		seen[e.Key] = true
		e.X, e.Y = clamp(e.X, 0, s.W), clamp(e.Y, 0, s.H)
		e.W, e.H = clamp(e.W, 1, s.W-e.X), clamp(e.H, 1, s.H-e.Y)
		e.Size = clamp(e.Size, 4, 40)
		if e.Align != "center" && e.Align != "right" {
			e.Align = "left"
		}
		if e.Max < 0 || e.Max > 200 {
			e.Max = 0
		}
		if e.Lines < 1 || e.Lines > 4 {
			e.Lines = 1
		}
		e.Prefix = truncateRunes(e.Prefix, 30)
		e.Text = truncateRunes(e.Text, 120)
		out = append(out, roundElement(e))
	}
	for _, d := range defaultLabelLayout(s).Elements {
		if !seen[d.Key] {
			d.Visible = false
			out = append(out, d)
		}
	}
	l.Size, l.Elements = s.Key, out
}

func labelLayoutKey(size string) string { return "label_layout:" + size }

// labelLayoutFor: gespeichertes Layout der Größe (nil = bisherige Gestaltung).
func (h *Handler) labelLayoutFor(ctx context.Context, s labelSize) *labelLayout {
	raw := h.appSetting(ctx, labelLayoutKey(s.Key), "")
	if raw == "" {
		return nil
	}
	var l labelLayout
	if json.Unmarshal([]byte(raw), &l) != nil {
		return nil
	}
	l.sanitize(s)
	return &l
}

// text eines Feldes für ein Etikett (mit Präfix und Kürzung).
func (e labelElement) text(it labelItem) string {
	var v string
	switch e.Key {
	case "loc":
		v = it.headText()
	case "path":
		v = it.Location
	case "name":
		v = it.Name
	case "cat":
		v = it.Category
	case "pn":
		v = it.PartNumber
	case "mf":
		v = it.Manufacturer
	case "mpn":
		v = it.MPN
	case "ean":
		v = it.EAN
	case "min":
		v = strings.TrimSpace(it.MinQty + " " + it.Unit)
	case "text":
		v = e.Text
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if e.Max > 0 {
		if r := []rune(v); len(r) > e.Max {
			v = string(r[:e.Max-1]) + "…"
		}
	}
	return e.Prefix + v
}

// ── Browser-Druck ─────────────────────────────────────────────

type labelPart struct {
	Kind  string // qr|logo|text
	Style template.CSS
	Text  string
}

func (l *labelLayout) parts(it labelItem, logo string) []labelPart {
	var out []labelPart
	for _, e := range l.Elements {
		if !e.Visible {
			continue
		}
		css := fmt.Sprintf("left:%.2fmm;top:%.2fmm;width:%.2fmm;height:%.2fmm;", e.X, e.Y, e.W, e.H)
		switch e.Key {
		case "qr":
			if it.URL != "" {
				out = append(out, labelPart{Kind: "qr", Style: template.CSS(css)})
			}
		case "logo":
			if logo != "" {
				out = append(out, labelPart{Kind: "logo", Style: template.CSS(css)})
			}
		default:
			t := e.text(it)
			if t == "" {
				continue
			}
			css += fmt.Sprintf("font-size:%.1fpt;text-align:%s;-webkit-line-clamp:%d;", e.Size, e.Align, e.Lines)
			if e.Bold {
				css += "font-weight:700;"
			}
			if e.Mono {
				css += "font-family:'Courier New',monospace;"
			}
			if e.Line {
				css += "border-bottom:.3mm solid #000;"
			}
			if e.Lines == 1 {
				css += "white-space:nowrap;text-overflow:ellipsis;display:block;"
			}
			out = append(out, labelPart{Kind: "text", Style: template.CSS(css), Text: t})
		}
	}
	return out
}

// ── PDF (Netzwerkdrucker) ─────────────────────────────────────

func drawLayoutPDF(p *fpdf.Fpdf, l *labelLayout, it labelItem, x0, y0 float64) {
	for _, e := range l.Elements {
		if !e.Visible {
			continue
		}
		x, y := x0+e.X, y0+e.Y
		switch e.Key {
		case "qr":
			if it.URL != "" {
				pdfQR(p, it.URL, x, y, math.Min(e.W, e.H))
			}
			continue
		case "logo":
			continue // Logo nur im Browser-Druck
		}
		t := e.text(it)
		if t == "" {
			continue
		}
		family, style := "Helvetica", ""
		if e.Mono {
			family = "Courier"
		}
		if e.Bold {
			style = "B"
		}
		p.SetFont(family, style, e.Size)
		lh := e.Size * 0.3528 * 1.15 // pt → mm, Zeilenabstand
		align := map[string]string{"left": "L", "center": "C", "right": "R"}[e.Align]
		border := ""
		if e.Line {
			border = "B"
		}
		if e.Lines <= 1 {
			p.SetXY(x, y)
			p.CellFormat(e.W, math.Min(e.H, lh), pdfText(p, fitPDF(p, t, e.W)), border, 0, align, false, 0, "")
			continue
		}
		lines := p.SplitText(t, e.W)
		if len(lines) > e.Lines {
			lines = lines[:e.Lines]
			lines[e.Lines-1] = fitPDF(p, lines[e.Lines-1]+"…", e.W)
		}
		for i, ln := range lines {
			p.SetXY(x, y+float64(i)*lh)
			b := ""
			if i == len(lines)-1 {
				b = border
			}
			p.CellFormat(e.W, lh, pdfText(p, ln), b, 0, align, false, 0, "")
		}
	}
}

// ── Zebra (ZPL) ───────────────────────────────────────────────

func zplLayoutLabel(l *labelLayout, it labelItem, s labelSize, z zebraSettings, copies int) string {
	var b bytes.Buffer
	z.header(&b, copies)
	sx, sy := z.WidthMM/s.W, z.HeightMM/s.H
	dots := func(mm float64) int { return mmToDots(mm, z.DPI) }
	for _, e := range l.Elements {
		if !e.Visible {
			continue
		}
		x, y, w, hgt := dots(e.X*sx), dots(e.Y*sy), dots(e.W*sx), dots(e.H*sy)
		switch e.Key {
		case "qr":
			if it.URL != "" {
				fmt.Fprintf(&b, "^FO%d,%d^BQN,2,%d^FDMA,%s^FS", x, max(0, y-dots(0.7)), qrMagnification(it.URL, min(w, hgt)), zplText(it.URL))
			}
			continue
		case "logo":
			continue
		}
		t := e.text(it)
		if t == "" {
			continue
		}
		fh := int(e.Size * math.Min(sx, sy) * float64(z.DPI) / 72)
		if e.Lines == 1 { // lange Texte kleiner setzen statt abschneiden
			if n := len([]rune(t)); n > 0 {
				if fit := int(float64(w) / (0.55 * float64(n))); fit < fh {
					fh = max(fit, fh/2)
				}
			}
		}
		align := map[string]string{"left": "L", "center": "C", "right": "R"}[e.Align]
		fw := fh
		if e.Bold {
			fw = fh * 11 / 10
		}
		fmt.Fprintf(&b, "^FO%d,%d^A0N,%d,%d^FB%d,%d,0,%s^FD%s^FS", x, y, fh, fw, w, e.Lines, align, zplText(t))
		if e.Line {
			fmt.Fprintf(&b, "^FO%d,%d^GB%d,%d,%d^FS", x, y+hgt, w, 2, 2)
		}
	}
	b.WriteString("^XZ\n")
	return b.String()
}

// ── Dymo ──────────────────────────────────────────────────────

func dymoLayoutLabel(l *labelLayout, it labelItem, s labelSize, t dymoLabelType, paperName string) string {
	var b strings.Builder
	b.WriteString(dymoLabelOpen(t, paperName))
	sx, sy := t.LongMM/s.W, t.ShortMM/s.H
	for _, e := range l.Elements {
		if !e.Visible {
			continue
		}
		x, y, w, hgt := twips(e.X*sx), twips(e.Y*sy), twips(e.W*sx), twips(e.H*sy)
		switch e.Key {
		case "qr":
			if it.URL != "" {
				b.WriteString(dymoQR(it.URL, x, y, min(w, hgt)))
			}
			continue
		case "logo":
			continue
		}
		txt := e.text(it)
		if txt == "" {
			continue
		}
		align := map[string]string{"left": "Left", "center": "Center", "right": "Right"}[e.Align]
		b.WriteString(dymoText(strings.ToUpper(e.Key), txt, e.Size*math.Min(sx, sy), e.Bold, x, y, w, hgt, align))
	}
	b.WriteString(`</DieCutLabel>`)
	return b.String()
}

// ── Designer-Seite ────────────────────────────────────────────

type LabelDesignerData struct {
	BaseData
	Size     labelSize
	Sizes    []labelSize
	Layout   labelLayout
	Custom   bool
	Fields   []struct{ Key, Label, Sample string }
	Sample   map[string]string // Feld → Beispieltext (echtes Teil, falls vorhanden)
	SampleQR template.HTML
	Logo     string
	Message  string
	Error    string
}

func (h *Handler) canLabelDesign(r *http.Request) bool {
	return h.hasPerm(r, "inventory.edit") || h.hasPerm(r, "printers.manage")
}

// LabelDesignerPage: GET /inventory/labels/designer?size=…[&part=…]
func (h *Handler) LabelDesignerPage(w http.ResponseWriter, r *http.Request) {
	if !h.canLabelDesign(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	s := labelSizeByKey(q.Get("size"))
	d := LabelDesignerData{
		BaseData: h.baseData(r, "labels-designer", "Etiketten-Designer", "Ersatzteile"),
		Size:     s, Sizes: labelSizes, Fields: labelFields, Logo: h.labelLogo(),
		Message: q.Get("msg"), Error: q.Get("err"), Sample: map[string]string{},
	}
	if l := h.labelLayoutFor(ctx, s); l != nil {
		d.Layout, d.Custom = *l, true
	} else {
		d.Layout = defaultLabelLayout(s)
	}
	for _, f := range labelFields {
		d.Sample[f.Key] = f.Sample
	}
	// echtes Teil als Beispiel (gewähltes oder eines mit Lagerplatz)
	sel := url.Values{}
	if p := q.Get("part"); p != "" {
		sel.Set("part", p)
	} else {
		var pid string
		if h.db.QueryRow(ctx, `SELECT part_id::text FROM spare_part_stock WHERE qty > 0 ORDER BY updated_at DESC LIMIT 1`).Scan(&pid) == nil {
			sel.Set("part", pid)
		}
	}
	if sel.Get("part") != "" {
		if items, err := h.loadLabelItems(ctx, sel); err == nil && len(items) > 0 {
			it := items[0]
			for _, f := range labelFields {
				if f.Key == "qr" || f.Key == "logo" || f.Key == "text" {
					continue
				}
				if v := (labelElement{Key: f.Key}).text(it); v != "" {
					d.Sample[f.Key] = v
				}
			}
		}
	}
	if svg, err := qrSVG(h.publicBaseURL(r) + "/inventory/00000000-0000-0000-0000-000000000000/booking"); err == nil {
		d.SampleQR = svg
	}
	h.render(w, "label_designer", d)
}

// LabelDesignerSaveWeb: POST /inventory/labels/designer – JSON {size, elements} bzw. {size, reset:true}
func (h *Handler) LabelDesignerSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canLabelDesign(r) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Keine Berechtigung."})
		return
	}
	var in struct {
		Size     string         `json:"size"`
		Reset    bool           `json:"reset"`
		Elements []labelElement `json:"elements"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return
	}
	s := labelSizeByKey(in.Size)
	if s.Key != in.Size {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "unbekannte Etikettengröße"})
		return
	}
	ctx := r.Context()
	if in.Reset {
		_, err := h.db.Exec(ctx, `DELETE FROM app_settings WHERE key = $1`, labelLayoutKey(s.Key))
		if err != nil {
			completionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		componentLog("etiketten").Info().Str("groesse", s.Key).Str("von", getUser(r).ID).Msg("layout zurückgesetzt")
		completionJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	l := labelLayout{Size: s.Key, Elements: in.Elements}
	l.sanitize(s)
	visible := 0
	for _, e := range l.Elements {
		if e.Visible {
			visible++
		}
	}
	if visible == 0 {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Mindestens ein Feld muss sichtbar sein"})
		return
	}
	raw, _ := json.Marshal(l)
	if err := h.setAppSetting(ctx, labelLayoutKey(s.Key), string(raw)); err != nil {
		completionJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	componentLog("etiketten").Info().Str("groesse", s.Key).Int("felder", visible).Str("von", getUser(r).ID).Msg("layout gespeichert")
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}

// StorageTreeOption: Lagerort für die Etiketten-Auswahl (eingerückt nach Ebene).
type storageTreeOption struct {
	ID, Name, Type, Path string
	Depth, Parts         int
	Selected             bool
}

var storageLevelLabels = map[string]string{"lagerort": "Lager", "regal": "Regal", "fach": "Fach", "platz": "Platz"}

// storageTree: alle aktiven Lagerorte als Baum (Tiefensuche) mit der Zahl
// der Etiketten darunter (Teil × Lagerplatz).
func (h *Handler) storageTree(ctx context.Context, selected []string) []storageTreeOption {
	sel := map[string]bool{}
	for _, s := range selected {
		sel[s] = true
	}
	rows, err := h.db.Query(ctx, `
		WITH RECURSIVE t AS (
			SELECT id, parent_id, name::text AS name, type::text AS type, 0 AS depth, ARRAY[lower(name::text)] AS sortkey, name::text AS path
			FROM storage_nodes WHERE parent_id IS NULL AND active
			UNION ALL
			SELECT n.id, n.parent_id, n.name::text, n.type::text, t.depth + 1, t.sortkey || lower(n.name::text), t.path || ' › ' || n.name
			FROM storage_nodes n JOIN t ON n.parent_id = t.id WHERE n.active AND t.depth < 12
		)
		SELECT t.id::text, COALESCE(t.parent_id::text, ''), t.name, t.type, t.path, t.depth,
		       (SELECT COUNT(*) FROM spare_part_stock st JOIN spare_parts sp ON sp.id = st.part_id AND sp.active WHERE st.storage_node_id = t.id)
		FROM t ORDER BY t.sortkey`)
	if err != nil {
		componentLog("etiketten").Warn().Err(err).Msg("lagerbaum")
		return nil
	}
	defer rows.Close()
	var out []storageTreeOption
	parent := map[string]string{}
	idx := map[string]int{}
	for rows.Next() {
		var o storageTreeOption
		var p string
		if rows.Scan(&o.ID, &p, &o.Name, &o.Type, &o.Path, &o.Depth, &o.Parts) == nil {
			o.Selected = sel[o.ID]
			o.Type = firstNonEmpty(storageLevelLabels[o.Type], o.Type)
			parent[o.ID] = p
			idx[o.ID] = len(out)
			out = append(out, o)
		}
	}
	// Etiketten der Unterplätze nach oben aufsummieren
	direct := make([]int, len(out))
	for i := range out {
		direct[i] = out[i].Parts
	}
	for i := range out {
		for p := parent[out[i].ID]; p != ""; p = parent[p] {
			if j, ok := idx[p]; ok {
				out[j].Parts += direct[i]
			} else {
				break
			}
		}
	}
	return out
}

// Einrückung für die Baumanzeige
func (o storageTreeOption) Indent() template.CSS {
	return template.CSS("padding-left:" + strconv.Itoa(4+o.Depth*16) + "px")
}
