package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"pdh/internal/modules/maintenance"
)

// Wartungsprotokoll (PDF) – entsteht bei jedem Abschluss eines
// Wartungsauftrags (maintenance.OnTaskCompleted) und wird als Dokument an
// der Anlage abgelegt (Reiter „Dokumente“). Inhalt: Logo, Datum, wer
// ausgefuehrt hat, Checkliste mit Werten/Bewertung/Fotos, Massnahmen,
// verwendete Ersatzteile und Bemerkung.

// maintProtocol: alle Daten fuer ein Protokoll.
type maintProtocol struct {
	TaskID, Title, TypeLabel, PlanName string
	InfraID, InfraPath                 string
	DueDate                            time.Time
	CompletedAt                        time.Time
	DurationMin                        int
	Notes                              string
	Executor                           string
	Participants                       []string
	Checklist                          []*maintenance.TaskChecklistItem
	Actions                            []maintProtocolLine
	Parts                              []maintProtocolLine
	Company                            string
	Logo                               string // Dateipfad oder leer
}

type maintProtocolLine struct{ Text, Meta string }

var maintTypeLabels = map[string]string{"preventive": "Vorbeugende Wartung", "inspection": "Inspektion", "calibration": "Kalibrierung", "cleaning": "Reinigung"}

// saveMaintenanceProtocol: Hook nach dem Abschluss – Protokoll (PDF) an der
// Anlage ablegen und die automatische Rueckmeldung schicken. Fehler nur
// protokollieren, der Abschluss selbst ist dann schon gespeichert.
func (h *Handler) saveMaintenanceProtocol(ctx context.Context, taskID, userID string) {
	p, err := h.loadMaintProtocol(ctx, taskID, userID)
	if err != nil {
		componentLog("wartungsprotokoll").Warn().Err(err).Str("task", taskID).Msg("daten fuer protokoll nicht ladbar")
		return
	}
	url := h.storeMaintProtocol(ctx, p, taskID, userID)
	h.notifyMaintenanceDone(ctx, p, taskID, userID, url)
}

// storeMaintProtocol erzeugt das PDF und legt es ab; liefert die Adresse (leer bei Fehler).
func (h *Handler) storeMaintProtocol(ctx context.Context, p *maintProtocol, taskID, userID string) string {
	log := componentLog("wartungsprotokoll").With().Str("task", taskID).Logger()
	data, err := renderMaintProtocolPDF(p)
	if err != nil {
		log.Warn().Err(err).Msg("pdf nicht erzeugt")
		return ""
	}
	refType, refID := "infrastructure", p.InfraID
	if refID == "" { // Auftrag ohne Anlage: Protokoll am Auftrag
		refType, refID = "maintenance_task", taskID
	}
	date := p.CompletedAt.Format("2006-01-02")
	rel := filepath.Join(refType, refID, "wartungsprotokoll-"+date+"-"+randomHex(4)+".pdf")
	abs := filepath.Join("uploads", rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		log.Warn().Err(err).Msg("ordner nicht anlegbar")
		return ""
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("datei nicht schreibbar")
		return ""
	}
	name := "Wartungsprotokoll " + date + " " + safeFileName(p.Title) + ".pdf"
	if _, err := h.db.Exec(ctx, `INSERT INTO attachments (id, ref_type, ref_id, filename, filepath, mimetype, size_bytes, caption, created_by)
		VALUES (gen_random_uuid(), $1, $2::uuid, $3, $4, 'application/pdf', $5, $6, NULLIF($7,'')::uuid)`,
		refType, refID, name, filepath.ToSlash(rel), len(data), "Wartungsprotokoll: "+p.Title, userID); err != nil {
		_ = os.Remove(abs)
		log.Warn().Err(err).Msg("anhang nicht gespeichert")
		return ""
	}
	h.addHistory(ctx, "maintenance", taskID, "document", "", "", "", "Wartungsprotokoll (PDF) an der Anlage abgelegt", userID)
	log.Info().Str("datei", rel).Msg("wartungsprotokoll gespeichert")
	return "/uploads/" + filepath.ToSlash(rel)
}

// notifyMaintenanceDone: automatische Rueckmeldung nach dem Abschluss an
// Verantwortliche, Zugewiesene und den Ersteller (nicht an die ausfuehrende
// Person) – mit Ergebnis, Abweichungen, Protokoll und naechstem Termin.
func (h *Handler) notifyMaintenanceDone(ctx context.Context, p *maintProtocol, taskID, userID, pdfURL string) {
	rows, err := h.db.Query(ctx, `SELECT DISTINCT u.id::text FROM maintenance_tasks mt
		LEFT JOIN maintenance_plans mp ON mp.id = mt.plan_id
		JOIN users u ON u.id IN (mt.responsible_to, mt.assigned_to, mt.created_by, mp.responsible_to)
		WHERE mt.id = $1::uuid AND u.active AND NOT u.is_bot AND NOT u.is_system_user
		  AND u.id <> COALESCE(NULLIF($2, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`, taskID, userID)
	if err != nil {
		componentLog("wartung").Warn().Err(err).Str("task", taskID).Msg("rueckmeldung: empfaenger nicht ladbar")
		return
	}
	var to []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			to = append(to, id)
		}
	}
	rows.Close()
	if len(to) == 0 {
		return
	}
	h.systemNotify(ctx, to, maintDoneMessage(p, taskID, pdfURL, h.maintNextDue(ctx, taskID)))
}

// maintDoneMessage: Text der Rueckmeldung.
func maintDoneMessage(p *maintProtocol, taskID, pdfURL string, next *time.Time) string {
	var b strings.Builder
	b.WriteString("✅ Wartung erledigt: „" + p.Title + "“")
	if p.InfraPath != "" {
		b.WriteString(" · " + p.InfraPath)
	}
	b.WriteString("\nErledigt")
	if p.Executor != "" {
		b.WriteString(" von " + p.Executor)
	}
	b.WriteString(" am " + p.CompletedAt.Format("02.01.2006, 15:04 Uhr"))
	if len(p.Participants) > 0 {
		b.WriteString(" (mit " + strings.Join(p.Participants, ", ") + ")")
	}
	if n := len(p.Checklist); n > 0 {
		var out, open []string
		for _, it := range p.Checklist {
			switch {
			case it.InRange != nil && !*it.InRange:
				out = append(out, strings.TrimSpace(it.Label+" "+it.Value+" "+it.Unit))
			case it.ItemType == "checkbox" && !it.Done:
				open = append(open, it.Label)
			}
		}
		b.WriteString(fmt.Sprintf("\nCheckliste: %d Punkte", n))
		if len(out) == 0 && len(open) == 0 {
			b.WriteString(" – alles in Ordnung")
		}
		if len(out) > 0 {
			b.WriteString("\n⚠ Außerhalb von Min/Max: " + strings.Join(out, "; "))
		}
		if len(open) > 0 {
			b.WriteString("\n⚠ Nicht erledigt: " + strings.Join(open, "; "))
		}
	}
	if strings.TrimSpace(p.Notes) != "" {
		b.WriteString("\nBemerkung: " + strings.TrimSpace(p.Notes))
	}
	if next != nil {
		b.WriteString("\n📅 Nächster Termin: " + next.Local().Format("02.01.2006"))
	}
	if pdfURL != "" {
		b.WriteString("\nProtokoll: " + pdfURL)
	}
	b.WriteString("\nAuftrag: /maintenance/tasks/" + taskID)
	return b.String()
}

var protocolNameChars = regexp.MustCompile(`[^\p{L}\p{N} ._-]+`)

func safeFileName(s string) string {
	s = strings.TrimSpace(protocolNameChars.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > 60 {
		s = strings.TrimSpace(string(r[:60]))
	}
	if s == "" {
		s = "Auftrag"
	}
	return s
}

func (h *Handler) loadMaintProtocol(ctx context.Context, taskID, userID string) (*maintProtocol, error) {
	p := &maintProtocol{TaskID: taskID}
	var typ string
	var completed *time.Time
	if err := h.db.QueryRow(ctx, `SELECT mt.title, mt.type::text, COALESCE(mp.name,''), COALESCE(mt.infrastructure_id::text,''),
			mt.due_date, mt.completed_at, COALESCE(mt.duration_min,0), COALESCE(mt.notes,'')
		FROM maintenance_tasks mt LEFT JOIN maintenance_plans mp ON mp.id = mt.plan_id WHERE mt.id = $1::uuid`, taskID).
		Scan(&p.Title, &typ, &p.PlanName, &p.InfraID, &p.DueDate, &completed, &p.DurationMin, &p.Notes); err != nil {
		return nil, err
	}
	p.TypeLabel = maintTypeLabels[typ]
	if p.TypeLabel == "" {
		p.TypeLabel = typ
	}
	p.CompletedAt = time.Now()
	if completed != nil {
		p.CompletedAt = completed.Local()
	}
	if p.InfraID != "" {
		// Pfad der Anlage im Baum (Gebaeude › Linie › Anlage)
		_ = h.db.QueryRow(ctx, `WITH RECURSIVE up AS (
				SELECT id, parent_id, name, 0 AS depth FROM infrastructure WHERE id = $1::uuid
				UNION ALL SELECT i.id, i.parent_id, i.name, up.depth + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id WHERE up.depth < 20)
			SELECT string_agg(name, ' › ' ORDER BY depth DESC) FROM up`, p.InfraID).Scan(&p.InfraPath)
	}
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(TRIM(first_name || ' ' || last_name), '') FROM users WHERE id = NULLIF($1,'')::uuid`, userID).Scan(&p.Executor)
	if rows, err := h.db.Query(ctx, `SELECT DISTINCT TRIM(u.first_name || ' ' || u.last_name) FROM time_entries te JOIN users u ON u.id = te.user_id
		WHERE te.ref_type = 'maintenance' AND te.ref_id = $1::uuid AND te.user_id <> COALESCE(NULLIF($2,'')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY 1`, taskID, userID); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil && n != "" {
				p.Participants = append(p.Participants, n)
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT a.description, a.created_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')
		FROM maintenance_task_actions a LEFT JOIN users u ON u.id = a.created_by WHERE a.task_id = $1::uuid ORDER BY a.created_at`, taskID); err == nil {
		for rows.Next() {
			var l maintProtocolLine
			var at time.Time
			if rows.Scan(&l.Text, &at, &l.Meta) == nil {
				l.Meta = strings.TrimSpace(at.Local().Format("02.01.2006 15:04") + " · " + l.Meta)
				p.Actions = append(p.Actions, l)
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT COALESCE(sp.part_number || ' · ', '') || sp.name, SUM(sm.qty)::text
		FROM stock_movements sm JOIN spare_parts sp ON sp.id = sm.part_id
		WHERE sm.maintenance_task_id = $1::uuid AND sm.type = 'out' GROUP BY sp.part_number, sp.name ORDER BY sp.name`, taskID); err == nil {
		for rows.Next() {
			var l maintProtocolLine
			if rows.Scan(&l.Text, &l.Meta) == nil {
				p.Parts = append(p.Parts, l)
			}
		}
		rows.Close()
	}
	items, err := maintenance.NewRepository(h.db).TaskChecklistResults(ctx, taskID)
	if err != nil {
		return nil, err
	}
	p.Checklist = items
	b := h.branding()
	p.Company = b.AppName
	p.Logo = h.exportLogoFile()
	return p, nil
}

// uploadPath: /uploads/... → Dateipfad (nur innerhalb von uploads)
func uploadPath(url string) string {
	rel := strings.TrimPrefix(url, "/uploads/")
	if rel == url || strings.Contains(rel, "..") {
		return ""
	}
	return filepath.Join("uploads", filepath.FromSlash(rel))
}

func fmtMeasure(v float64) string {
	return strings.Replace(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), "."), ".", ",", 1)
}

// pdfLineCount: Zeilen, die MultiCell fuer einen (bereits nach cp1252
// uebersetzten) Text braucht – SplitText kann mit cp1252-Bytes nicht umgehen.
func pdfLineCount(pdf *fpdf.Fpdf, text string, width float64) int {
	total := 0
	for _, para := range strings.Split(text, "\n") {
		lines, cur := 1, 0.0
		space := pdf.GetStringWidth(" ")
		for _, word := range strings.Fields(para) {
			w := pdf.GetStringWidth(word)
			switch {
			case cur == 0:
				cur = w
			case cur+space+w <= width:
				cur += space + w
			default:
				lines++
				cur = w
			}
			for cur > width { // ueberlanges Wort wird umbrochen
				lines++
				cur -= width
			}
		}
		total += lines
	}
	return total
}

// renderMaintProtocolPDF baut das Protokoll (A4 hoch).
func renderMaintProtocolPDF(p *maintProtocol) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")
	const left, right, top = 15.0, 15.0, 12.0
	pdf.SetMargins(left, top, right)
	pdf.SetAutoPageBreak(true, 18)
	pageW, pageH := pdf.GetPageSize()
	contentW := pageW - left - right
	created := time.Now()
	pdf.AliasNbPages("{nb}")

	pdf.SetHeaderFunc(func() {
		y := top
		logoH := 0.0
		if p.Logo != "" {
			if w, h := imageSize(p.Logo); h > 0 {
				hmm := 14.0
				wmm := float64(w) * hmm / float64(h)
				if wmm > 50 {
					wmm, hmm = 50, 50*float64(h)/float64(w)
				}
				pdf.ImageOptions(p.Logo, left, y, wmm, hmm, false, fpdf.ImageOptions{ReadDpi: false}, 0, "")
				if pdf.Error() != nil {
					pdf.ClearError()
				} else {
					logoH = hmm
				}
			}
		}
		pdf.SetXY(left, y)
		pdf.SetFont("Helvetica", "B", 16)
		pdf.SetTextColor(20, 20, 20)
		pdf.CellFormat(contentW, 8, tr("Wartungsprotokoll"), "", 1, "R", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(90, 90, 90)
		sub := p.CompletedAt.Format("02.01.2006")
		if p.Company != "" {
			sub = p.Company + " · " + sub
		}
		pdf.CellFormat(contentW, 5, tr(sub), "", 1, "R", false, 0, "")
		lineY := y + 15
		if logoH+2 > 15 {
			lineY = y + logoH + 2
		}
		pdf.SetDrawColor(200, 200, 200)
		pdf.Line(left, lineY, pageW-right, lineY)
		pdf.SetY(lineY + 4)
		pdf.SetTextColor(20, 20, 20)
	})
	pdf.SetFooterFunc(func() {
		pdf.SetY(-13)
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(contentW/2, 5, tr("Erstellt "+created.Format("02.01.2006 15:04")+" · PDH"), "", 0, "L", false, 0, "")
		pdf.CellFormat(contentW/2, 5, tr(fmt.Sprintf("Seite %d von {nb}", pdf.PageNo())), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()

	section := func(title string) {
		if pdf.GetY() > pageH-40 {
			pdf.AddPage()
		}
		pdf.Ln(3)
		pdf.SetFont("Helvetica", "B", 11)
		pdf.SetTextColor(20, 20, 20)
		pdf.CellFormat(contentW, 7, tr(title), "B", 1, "L", false, 0, "")
		pdf.Ln(2)
	}

	// ── Kopfdaten ──
	pdf.SetFont("Helvetica", "B", 13)
	pdf.MultiCell(contentW, 6, tr(p.Title), "", "L", false)
	pdf.Ln(2)
	kv := [][2]string{
		{"Anlage", p.InfraPath},
		{"Wartungsplan", p.PlanName},
		{"Art", p.TypeLabel},
		{"Fällig am", p.DueDate.Format("02.01.2006")},
		{"Ausgeführt am", p.CompletedAt.Format("02.01.2006, 15:04 Uhr")},
		{"Ausführende(r)", p.Executor},
		{"Beteiligt", strings.Join(p.Participants, ", ")},
	}
	if p.DurationMin > 0 {
		kv = append(kv, [2]string{"Dauer", fmt.Sprintf("%d:%02d h", p.DurationMin/60, p.DurationMin%60)})
	}
	for _, row := range kv {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		pdf.SetFont("Helvetica", "", 9.5)
		pdf.SetTextColor(100, 100, 100)
		pdf.CellFormat(38, 6, tr(row[0]), "", 0, "L", false, 0, "")
		pdf.SetTextColor(20, 20, 20)
		pdf.SetFont("Helvetica", "B", 9.5)
		pdf.MultiCell(contentW-38, 6, tr(row[1]), "", "L", false)
	}

	// ── Checkliste ──
	section("Checkliste")
	if len(p.Checklist) == 0 {
		pdf.SetFont("Helvetica", "I", 9.5)
		pdf.CellFormat(contentW, 6, tr("Für diesen Auftrag wurden keine Checklistenpunkte erfasst."), "", 1, "L", false, 0, "")
	} else {
		widths := []float64{9, 71, 40, 38, contentW - 158}
		heads := []string{"Nr.", "Prüfpunkt", "Ergebnis", "Vorgabe", "Bewertung"}
		header := func() {
			pdf.SetFont("Helvetica", "B", 9)
			pdf.SetFillColor(238, 241, 245)
			pdf.SetTextColor(40, 40, 40)
			for i, hd := range heads {
				pdf.CellFormat(widths[i], 7, tr(hd), "1", 0, "L", true, 0, "")
			}
			pdf.Ln(-1)
		}
		header()
		for n, it := range p.Checklist {
			result, rating := "", ""
			ok := true
			switch it.ItemType {
			case "checkbox":
				result = "nicht erledigt"
				if it.Done {
					result = "erledigt"
				}
				ok = it.Done
				rating = map[bool]string{true: "i. O.", false: "offen"}[it.Done]
			case "number":
				result = strings.TrimSpace(it.Value + " " + it.Unit)
				if it.Value == "" {
					result = "–"
				}
				if it.InRange != nil {
					ok = *it.InRange
					rating = map[bool]string{true: "im Bereich", false: "außerhalb"}[ok]
				}
			default:
				result = it.Value
				if result == "" {
					result = "–"
				}
			}
			var spec []string
			if it.ItemType == "number" {
				if it.TargetValue != nil {
					spec = append(spec, "Soll "+fmtMeasure(*it.TargetValue))
				}
				if it.MinValue != nil || it.MaxValue != nil {
					lo, hi := "…", "…"
					if it.MinValue != nil {
						lo = fmtMeasure(*it.MinValue)
					}
					if it.MaxValue != nil {
						hi = fmtMeasure(*it.MaxValue)
					}
					spec = append(spec, lo+" – "+hi+" "+it.Unit)
				}
			}
			label := it.Label
			if it.CheckedBy != "" && it.CheckedAt != nil {
				label += "\n" + it.CheckedBy + ", " + it.CheckedAt.Local().Format("02.01. 15:04")
			}
			cells := []string{fmt.Sprintf("%d", n+1), label, result, strings.Join(spec, "\n"), rating}
			// Zeilenhoehe nach dem laengsten Feld
			pdf.SetFont("Helvetica", "", 9)
			lines := 1
			for i, c := range cells {
				if l := pdfLineCount(pdf, tr(c), widths[i]-2); l > lines {
					lines = l
				}
			}
			rowH := float64(lines)*4.6 + 2
			if pdf.GetY()+rowH > pageH-20 {
				pdf.AddPage()
				header()
			}
			x, y := pdf.GetX(), pdf.GetY()
			for i, c := range cells {
				pdf.Rect(x, y, widths[i], rowH, "D")
				pdf.SetXY(x+1, y+1)
				pdf.SetTextColor(20, 20, 20)
				pdf.SetFont("Helvetica", "", 9)
				if i == 4 && rating != "" {
					pdf.SetFont("Helvetica", "B", 9)
					if ok {
						pdf.SetTextColor(5, 120, 70)
					} else {
						pdf.SetTextColor(190, 30, 30)
					}
				}
				pdf.MultiCell(widths[i]-2, 4.6, tr(c), "", "L", false)
				x += widths[i]
			}
			pdf.SetXY(left, y+rowH)
			// Fotos zur Dokumentation
			if len(it.DocImages) > 0 {
				const imgH = 32.0
				if pdf.GetY()+imgH+4 > pageH-20 {
					pdf.AddPage()
				}
				ix, iy := left+9, pdf.GetY()+2
				for _, img := range it.DocImages {
					path := uploadPath(img.URL)
					w, h := imageSize(path)
					if path == "" || h == 0 {
						continue
					}
					iw := float64(w) * imgH / float64(h)
					if iw > 60 {
						iw = 60
					}
					if ix+iw > pageW-right {
						break
					}
					pdf.ImageOptions(path, ix, iy, iw, 0, false, fpdf.ImageOptions{ReadDpi: false}, 0, "")
					if pdf.Error() != nil {
						pdf.ClearError()
						continue
					}
					ix += iw + 3
				}
				pdf.SetXY(left, iy+imgH+2)
			}
		}
	}

	// ── Massnahmen, Ersatzteile, Bemerkung ──
	if len(p.Actions) > 0 {
		section("Durchgeführte Maßnahmen")
		for _, a := range p.Actions {
			pdf.SetFont("Helvetica", "", 9.5)
			pdf.SetTextColor(20, 20, 20)
			pdf.MultiCell(contentW, 5, tr("• "+a.Text), "", "L", false)
			pdf.SetFont("Helvetica", "", 8)
			pdf.SetTextColor(110, 110, 110)
			pdf.CellFormat(contentW, 4.5, tr("   "+a.Meta), "", 1, "L", false, 0, "")
		}
	}
	section("Verwendete Ersatzteile")
	pdf.SetFont("Helvetica", "", 9.5)
	pdf.SetTextColor(20, 20, 20)
	if len(p.Parts) == 0 {
		pdf.CellFormat(contentW, 6, tr("Keine Ersatzteile verwendet."), "", 1, "L", false, 0, "")
	}
	for _, l := range p.Parts {
		pdf.CellFormat(contentW-30, 6, tr(l.Text), "B", 0, "L", false, 0, "")
		pdf.CellFormat(30, 6, tr(strings.TrimRight(strings.TrimRight(l.Meta, "0"), ".")+" Stk."), "B", 1, "R", false, 0, "")
	}
	if strings.TrimSpace(p.Notes) != "" {
		section("Bemerkung")
		pdf.SetFont("Helvetica", "", 9.5)
		pdf.MultiCell(contentW, 5, tr(p.Notes), "", "L", false)
	}

	// ── Bestaetigung ──
	if pdf.GetY() > pageH-45 {
		pdf.AddPage()
	}
	pdf.Ln(10)
	pdf.SetDrawColor(150, 150, 150)
	y := pdf.GetY() + 8
	pdf.Line(left, y, left+75, y)
	pdf.Line(pageW-right-75, y, pageW-right, y)
	pdf.SetXY(left, y+1)
	pdf.SetFont("Helvetica", "", 8.5)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(75, 5, tr("Datum: "+p.CompletedAt.Format("02.01.2006")), "", 0, "L", false, 0, "")
	pdf.SetX(pageW - right - 75)
	pdf.CellFormat(75, 5, tr("Ausgeführt: "+p.Executor), "", 1, "L", false, 0, "")

	if err := pdf.Error(); err != nil {
		return nil, err
	}
	var buf strings.Builder
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// RegisterMaintenanceProtocol haengt das Protokoll an jeden Wartungsabschluss (main.go).
func (h *Handler) RegisterMaintenanceProtocol() {
	maintenance.OnTaskCompleted(h.saveMaintenanceProtocol)
}
