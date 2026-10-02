package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"rsc.io/qr"
)

// Netzwerkdrucker: IPP/IPPS (Port 631, Standard bei allen aktuellen
// Buero-Druckern, "AirPrint"/"Mopria") oder RAW/JetDirect (Port 9100).
// Gedruckt wird PDF; IPP liefert ausserdem Zustand, Toner und Formate.

// ── IPP (RFC 8010/8011), nur was das PDH braucht ─────────────

const (
	ippOpPrintJob         = 0x0002
	ippOpGetPrinterAttrs  = 0x000B
	ippTagOperation       = 0x01
	ippTagJob             = 0x02
	ippTagEnd             = 0x03
	ippTagInteger         = 0x21
	ippTagBoolean         = 0x22
	ippTagEnum            = 0x23
	ippTagText            = 0x41
	ippTagName            = 0x42
	ippTagKeyword         = 0x44
	ippTagURI             = 0x45
	ippTagCharset         = 0x47
	ippTagNaturalLanguage = 0x48
	ippTagMimeMediaType   = 0x49
)

type ippValue struct {
	Tag byte
	Raw []byte
}

func (v ippValue) String() string { return string(v.Raw) }

func (v ippValue) Int() int {
	if len(v.Raw) == 4 {
		return int(int32(binary.BigEndian.Uint32(v.Raw)))
	}
	if len(v.Raw) == 1 {
		return int(v.Raw[0])
	}
	return 0
}

type ippWriter struct{ bytes.Buffer }

func (w *ippWriter) attr(tag byte, name string, value []byte) {
	w.WriteByte(tag)
	_ = binary.Write(w, binary.BigEndian, uint16(len(name)))
	w.WriteString(name)
	_ = binary.Write(w, binary.BigEndian, uint16(len(value)))
	w.Write(value)
}

func (w *ippWriter) str(tag byte, name, value string) { w.attr(tag, name, []byte(value)) }

func (w *ippWriter) integer(tag byte, name string, n int) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(int32(n)))
	w.attr(tag, name, b)
}

func ippHeader(op uint16, reqID uint32, printerURI, user string) *ippWriter {
	w := &ippWriter{}
	w.Write([]byte{0x02, 0x00}) // IPP 2.0
	_ = binary.Write(w, binary.BigEndian, op)
	_ = binary.Write(w, binary.BigEndian, reqID)
	w.WriteByte(ippTagOperation)
	w.str(ippTagCharset, "attributes-charset", "utf-8")
	w.str(ippTagNaturalLanguage, "attributes-natural-language", "de")
	w.str(ippTagURI, "printer-uri", printerURI)
	w.str(ippTagName, "requesting-user-name", user)
	return w
}

// parseIPPResponse liefert Statuscode und alle Attribute (Name → Werte).
func parseIPPResponse(b []byte) (uint16, map[string][]ippValue, error) {
	if len(b) < 9 {
		return 0, nil, errors.New("IPP-Antwort zu kurz")
	}
	status := binary.BigEndian.Uint16(b[2:4])
	attrs := map[string][]ippValue{}
	p, last := 8, ""
	for p < len(b) {
		tag := b[p]
		p++
		if tag == ippTagEnd {
			break
		}
		if tag < 0x10 { // Beginn einer Attributgruppe
			continue
		}
		if p+2 > len(b) {
			return status, attrs, errors.New("IPP-Antwort beschädigt")
		}
		nl := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+nl+2 > len(b) {
			return status, attrs, errors.New("IPP-Antwort beschädigt")
		}
		name := string(b[p : p+nl])
		p += nl
		vl := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+vl > len(b) {
			return status, attrs, errors.New("IPP-Antwort beschädigt")
		}
		if name == "" { // weiterer Wert desselben Attributs
			name = last
		}
		attrs[name] = append(attrs[name], ippValue{Tag: tag, Raw: append([]byte(nil), b[p:p+vl]...)})
		last = name
		p += vl
	}
	return status, attrs, nil
}

type ippTarget struct {
	HTTPURL, PrinterURI string
	Insecure            bool
}

func ippTargetFrom(config map[string]string) ippTarget {
	host := strings.TrimSpace(config["host"])
	port := firstNonEmpty(strings.TrimSpace(config["port"]), "631")
	path := firstNonEmpty(strings.TrimSpace(config["ipp_path"]), "/ipp/print")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	hp := host + ":" + port
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") { // IPv6
		hp = "[" + host + "]:" + port
	}
	if config["protocol"] == "ipps" {
		return ippTarget{"https://" + hp + path, "ipps://" + hp + path, config["tls_insecure"] == "true"}
	}
	return ippTarget{"http://" + hp + path, "ipp://" + hp + path, false}
}

func ippDo(ctx context.Context, t ippTarget, body io.Reader) (uint16, map[string][]ippValue, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.HTTPURL, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/ipp")
	client := &http.Client{Timeout: 60 * time.Second}
	if t.Insecure {
		// Drucker haben fast immer selbst signierte Zertifikate
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Drucker nicht erreichbar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("Drucker antwortet mit HTTP %d – IPP-Pfad prüfen (meist /ipp/print)", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return parseIPPResponse(b)
}

type ippPrinterInfo struct {
	State           int // 3 bereit, 4 druckt, 5 angehalten
	Reasons         []string
	MakeModel, Name string
	Accepting       bool
	Formats         []string
	Markers         []string // "Schwarz 45 %"
	QueuedJobs      int
}

func ippGetPrinter(ctx context.Context, t ippTarget) (ippPrinterInfo, error) {
	w := ippHeader(ippOpGetPrinterAttrs, 1, t.PrinterURI, "pdh")
	for i, a := range []string{"printer-state", "printer-state-reasons", "printer-make-and-model", "printer-name", "printer-is-accepting-jobs",
		"document-format-supported", "marker-names", "marker-levels", "queued-job-count"} {
		name := "requested-attributes"
		if i > 0 {
			name = ""
		}
		w.str(ippTagKeyword, name, a)
	}
	w.WriteByte(ippTagEnd)
	status, attrs, err := ippDo(ctx, t, bytes.NewReader(w.Bytes()))
	if err != nil {
		return ippPrinterInfo{}, err
	}
	if status > 0x00FF {
		return ippPrinterInfo{}, fmt.Errorf("IPP-Status 0x%04X", status)
	}
	info := ippPrinterInfo{Accepting: true}
	if v := attrs["printer-state"]; len(v) > 0 {
		info.State = v[0].Int()
	}
	for _, v := range attrs["printer-state-reasons"] {
		if s := v.String(); s != "none" {
			info.Reasons = append(info.Reasons, s)
		}
	}
	if v := attrs["printer-make-and-model"]; len(v) > 0 {
		info.MakeModel = v[0].String()
	}
	if v := attrs["printer-name"]; len(v) > 0 {
		info.Name = v[0].String()
	}
	if v := attrs["printer-is-accepting-jobs"]; len(v) > 0 {
		info.Accepting = v[0].Int() == 1
	}
	for _, v := range attrs["document-format-supported"] {
		info.Formats = append(info.Formats, v.String())
	}
	names, levels := attrs["marker-names"], attrs["marker-levels"]
	for i := range names {
		if i < len(levels) {
			l := levels[i].Int()
			if l >= 0 {
				info.Markers = append(info.Markers, fmt.Sprintf("%s %d %%", names[i].String(), l))
			}
		}
	}
	if v := attrs["queued-job-count"]; len(v) > 0 {
		info.QueuedJobs = v[0].Int()
	}
	return info, nil
}

// ippStateReasonsDE: verstaendliche Texte fuer haeufige Meldungen.
var ippStateReasonsDE = map[string]string{
	"media-empty": "Papier leer", "media-jam": "Papierstau", "media-needed": "Papier einlegen", "toner-low": "Toner fast leer",
	"toner-empty": "Toner leer", "marker-supply-low": "Verbrauchsmaterial fast leer", "marker-supply-empty": "Verbrauchsmaterial leer",
	"door-open": "Klappe offen", "cover-open": "Abdeckung offen", "paused": "angehalten", "offline": "offline", "input-tray-missing": "Fach fehlt",
	"output-area-full": "Ablage voll", "spool-area-full": "Speicher voll",
}

func ippReasonText(r string) string {
	base := r
	for _, suf := range []string{"-report", "-warning", "-error"} {
		base = strings.TrimSuffix(base, suf)
	}
	if t, ok := ippStateReasonsDE[base]; ok {
		return t
	}
	return r
}

func ippPrintPDF(ctx context.Context, t ippTarget, jobName string, pdf []byte, copies int) error {
	w := ippHeader(ippOpPrintJob, 2, t.PrinterURI, "pdh")
	w.str(ippTagName, "job-name", jobName)
	w.str(ippTagMimeMediaType, "document-format", "application/pdf")
	if copies > 1 {
		w.WriteByte(ippTagJob)
		w.integer(ippTagInteger, "copies", copies)
	}
	w.WriteByte(ippTagEnd)
	status, attrs, err := ippDo(ctx, t, io.MultiReader(bytes.NewReader(w.Bytes()), bytes.NewReader(pdf)))
	if err != nil {
		return err
	}
	if status > 0x00FF {
		msg := ""
		if v := attrs["status-message"]; len(v) > 0 {
			msg = " – " + v[0].String()
		}
		return fmt.Errorf("Druckauftrag abgelehnt (IPP-Status 0x%04X%s)", status, msg)
	}
	return nil
}

// sendToNetworkPrinter: PDF per IPP oder RAW 9100.
func sendToNetworkPrinter(ctx context.Context, config map[string]string, jobName string, pdf []byte, copies int) error {
	if config["protocol"] == "raw" {
		port := firstNonEmpty(strings.TrimSpace(config["port"]), "9100")
		for i := 0; i < max(1, copies); i++ {
			if err := rawSend(ctx, strings.TrimSpace(config["host"]), port, pdf); err != nil {
				return err
			}
		}
		return nil
	}
	return ippPrintPDF(ctx, ippTargetFrom(config), jobName, pdf, copies)
}

// ── PDF ──────────────────────────────────────────────────────

func paperSize(config map[string]string) string {
	switch config["paper"] {
	case "A5", "Letter":
		return config["paper"]
	}
	return "A4"
}

func newPDF(orientation, unit, size string, w, h float64) *fpdf.Fpdf {
	var p *fpdf.Fpdf
	if size == "" {
		p = fpdf.NewCustom(&fpdf.InitType{OrientationStr: orientation, UnitStr: unit, Size: fpdf.SizeType{Wd: w, Ht: h}})
	} else {
		p = fpdf.New(orientation, unit, size, "")
	}
	p.SetAutoPageBreak(false, 0)
	p.SetMargins(0, 0, 0)
	return p
}

// pdfText: fpdf-Kernschriften kennen nur Latin-1 (Umlaute ja, „–“ nein).
func pdfText(p *fpdf.Fpdf, s string) string {
	s = strings.NewReplacer("–", "-", "—", "-", "„", "\"", "“", "\"", "”", "\"", "‚", "'", "‘", "'", "’", "'", "…", "...", "›", ">", "×", "x", "•", "-").Replace(s)
	return p.UnicodeTranslatorFromDescriptor("")(s)
}

func pdfQR(p *fpdf.Fpdf, text string, x, y, size float64) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return
	}
	n := float64(code.Size + 2)
	mod := size / n
	p.SetFillColor(0, 0, 0)
	for yy := 0; yy < code.Size; yy++ {
		for xx := 0; xx < code.Size; xx++ {
			if code.Black(xx, yy) {
				p.Rect(x+float64(xx+1)*mod, y+float64(yy+1)*mod, mod, mod, "F")
			}
		}
	}
}

// networkTestPDF: Testseite mit Rahmen, Schrift, Grauverlauf und QR-Code.
func networkTestPDF(printerName, location, url string, config map[string]string) ([]byte, error) {
	p := newPDF("P", "mm", paperSize(config), 0, 0)
	p.AddPage()
	pw, ph := p.GetPageSize()
	p.SetDrawColor(0, 0, 0)
	p.SetLineWidth(0.6)
	p.Rect(10, 10, pw-20, ph-20, "D")
	p.SetFont("Helvetica", "B", 22)
	p.SetXY(20, 22)
	p.CellFormat(pw-40, 10, pdfText(p, "PDH – Testseite"), "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "", 12)
	lines := []string{
		"Drucker: " + printerName,
		"Standort: " + firstNonEmpty(location, "-"),
		"Gedruckt: " + time.Now().Format("02.01.2006 15:04:05"),
		"Papier: " + paperSize(config),
		"",
		"Wenn diese Seite vollständig und scharf gedruckt ist, funktioniert der Drucker mit dem PDH.",
		"Umlaute: ÄÖÜ äöü ß",
	}
	for _, l := range lines {
		p.SetX(20)
		p.MultiCell(pw-40, 7, pdfText(p, l), "", "L", false)
	}
	y := p.GetY() + 8
	for i := 0; i < 10; i++ { // Graustufen
		g := 255 - i*25
		p.SetFillColor(g, g, g)
		p.Rect(20+float64(i)*15, y, 15, 12, "FD")
	}
	y += 20
	p.SetFont("Helvetica", "", 9)
	p.SetXY(20, y)
	p.CellFormat(pw-40, 5, pdfText(p, "Graustufen von weiß nach schwarz – alle zehn Felder sollten unterscheidbar sein."), "", 1, "L", false, 0, "")
	if url != "" {
		pdfQR(p, url, pw-20-40, ph-20-40, 40)
		p.SetXY(20, ph-30)
		p.CellFormat(pw-80, 5, pdfText(p, url), "", 0, "L", false, 0, "")
	}
	var buf bytes.Buffer
	err := p.Output(&buf)
	return buf.Bytes(), err
}

// labelsPDF: Etiketten wie die Druckansicht – A4-Bogen oder Rolle (eine
// Seite je Etikett).
func labelsPDF(items []labelItem, size labelSize, copies, start int, showCat bool) ([]byte, error) {
	var flat []*labelItem
	if size.Sheet {
		for i := 0; i < start; i++ {
			flat = append(flat, nil)
		}
	}
	for i := range items {
		for c := 0; c < max(1, copies); c++ {
			flat = append(flat, &items[i])
		}
	}
	var p *fpdf.Fpdf
	if size.Sheet {
		p = newPDF("P", "mm", "A4", 0, 0)
	} else {
		orient := "P"
		if size.W > size.H {
			orient = "L"
		}
		p = newPDF(orient, "mm", "", size.W, size.H)
	}
	per := 1
	if size.Sheet {
		per = size.Cols * size.Rows
	}
	left := size.MarginLeft
	if size.Sheet && left == 0 {
		left = (210 - float64(size.Cols)*size.W) / 2
	}
	for i, it := range flat {
		if i%per == 0 {
			p.AddPage()
		}
		if it == nil {
			continue
		}
		x, y := 0.0, 0.0
		if size.Sheet {
			k := i % per
			x = left + float64(k%size.Cols)*size.W
			y = size.MarginTop + float64(k/size.Cols)*size.H
		}
		drawLabelPDF(p, *it, x, y, size, showCat)
	}
	if len(flat) == 0 {
		return nil, errors.New("keine Etiketten für diese Auswahl")
	}
	var buf bytes.Buffer
	err := p.Output(&buf)
	return buf.Bytes(), err
}

func drawLabelPDF(p *fpdf.Fpdf, it labelItem, x, y float64, size labelSize, showCat bool) {
	pad := 1.5
	q := size.H - 2*pad
	if it.URL != "" {
		pdfQR(p, it.URL, x+pad, y+pad, q)
	} else {
		q = 0
	}
	tx := x + pad + q + 1.5
	tw := size.W - (tx - x) - pad
	fs := func(compact, normal float64) float64 {
		if size.Compact {
			return compact
		}
		return normal
	}
	loc := firstNonEmpty(it.LocationLeaf, "ohne Lagerplatz")
	p.SetFont("Helvetica", "B", fs(9, 11))
	p.SetXY(tx, y+pad)
	p.CellFormat(tw, fs(3.6, 4.4), pdfText(p, fitPDF(p, loc, tw)), "B", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "B", fs(7, 8.5))
	p.SetXY(tx, p.GetY()+0.6)
	p.MultiCell(tw, fs(2.8, 3.4), pdfText(p, fitPDF(p, it.Name, tw*1.9)), "", "L", false)
	if showCat && it.Category != "" && !size.Compact {
		p.SetFont("Helvetica", "", 5.5)
		p.SetX(tx)
		p.CellFormat(tw, 2.4, pdfText(p, fitPDF(p, it.Category, tw)), "", 1, "L", false, 0, "")
	}
	by := y + size.H - pad - fs(3.2, 3.8)
	p.SetFont("Courier", "B", fs(8, 10))
	p.SetXY(tx, by)
	p.CellFormat(tw*0.6, fs(3.2, 3.8), pdfText(p, fitPDF(p, it.PartNumber, tw*0.6)), "", 0, "L", false, 0, "")
	p.SetFont("Helvetica", "B", fs(7.5, 9))
	p.SetXY(tx+tw*0.6, by)
	p.CellFormat(tw*0.4, fs(3.2, 3.8), pdfText(p, "Min "+it.MinQty+" "+it.Unit), "", 0, "R", false, 0, "")
}

// fitPDF kuerzt Text auf die verfuegbare Breite (mit "…" als "...").
func fitPDF(p *fpdf.Fpdf, s string, w float64) string {
	if p.GetStringWidth(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 1 && p.GetStringWidth(string(r)+"...") > w {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}

func parsePort(s, def string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 && n < 65536 {
		return strconv.Itoa(n)
	}
	return def
}
