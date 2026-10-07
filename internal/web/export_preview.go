package web

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-pdf/fpdf"
	"github.com/goburrow/modbus"
	"github.com/gopcua/opcua/ua"
	"github.com/rs/zerolog/log"
	"github.com/xuri/excelize/v2"
)

// Export-Vorschau: das Gegenstueck zu den Import-Vorschauseiten. Statt
// eine externe Quelle zu erkunden, zeigt diese Seite den System-weiten
// Pool bereits importierter Werte (import_mappings) zur Auswahl an -
// "Fuer Export markieren" legt eine export_mappings-Zeile an. Was
// "field_name" dabei bedeutet, haengt vom Konnektortyp ab (siehe
// exportFieldNameLabel): Spalten-/Feldname bei Excel/PDF/SQL, Ziel-NodeID
// bei OPC UA, "registertyp:adresse:datentyp" bei Modbus (gleiche Notation
// wie auf der Import-Seite). "Jetzt exportieren" schreibt die aktuellen
// Werte aller Zuordnungen dieser Verbindung ins Ziel.

func exportableKind(kind string) bool {
	switch kind {
	case "excel", "csv", "pdf", "sqlite", "mysql", "mssql", "opcua", "modbus":
		return true
	default:
		return false
	}
}

func exportKindLabel(kind string) string {
	switch kind {
	case "pdf":
		return "PDF"
	case "excel":
		return "Excel"
	case "csv":
		return "CSV"
	case "sqlite":
		return "SQLite"
	case "mysql":
		return "MySQL"
	case "mssql":
		return "MSSQL"
	case "opcua":
		return "OPC UA"
	case "modbus":
		return "Modbus TCP"
	default:
		return kind
	}
}

// exportFieldNameLabel beschreibt, was im "Zielfeldname"-Feld des
// Markieren-Dialogs erwartet wird - je nach Konnektortyp ein freier
// Name, eine NodeID oder eine Registeradresse.
func exportFieldNameLabel(kind string) string {
	switch kind {
	case "opcua":
		return "Ziel-NodeID"
	case "modbus":
		return "Zielregister"
	default:
		return "Zielfeldname"
	}
}

type ExportPreviewPageData struct {
	BaseData
	ConnectionID     string
	ConnectionName   string
	Applicable       bool
	CanWrite         bool
	Kind             string
	KindLabel        string
	FieldNameLabel   string
	DestinationLabel string
	ScheduleCron     string
	RegisterTypes    []KindOption
	DataTypes        []KindOption
	AvailableSources []ImportMappingView
	Mappings         []ExportMappingView
	Notice           string
}

func exportDestinationLabel(kind string, config map[string]string) string {
	switch kind {
	case "excel", "csv", "pdf":
		return strings.TrimSpace(config["destination_path"])
	case "sqlite":
		return strings.TrimSpace(config["file_path"]) + " · Tabelle " + strings.TrimSpace(config["table_name"])
	case "mysql":
		host := strings.TrimSpace(config["host"]) + ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "3306") + "/" + strings.TrimSpace(config["database"])
		return host + " · Tabelle " + strings.TrimSpace(config["table_name"])
	case "mssql":
		host := strings.TrimSpace(config["host"])
		if instance := strings.TrimSpace(config["instance_name"]); instance != "" {
			host += `\` + instance
		} else {
			host += ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "1433")
		}
		return host + "/" + strings.TrimSpace(config["database"]) + " · Tabelle " + strings.TrimSpace(config["table_name"])
	case "opcua":
		return strings.TrimSpace(config["endpoint_url"])
	case "modbus":
		return strings.TrimSpace(config["host"]) + ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "502")
	default:
		return ""
	}
}

func (h *Handler) ExportPreviewPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "export") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	var name, kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT name, kind, config FROM import_export_connections WHERE id=$1 AND direction='export'`, id).
		Scan(&name, &kind, &configBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	data := ExportPreviewPageData{
		BaseData:         h.baseData(r, "export", "Export-Vorschau", name),
		ConnectionID:     id,
		ConnectionName:   name,
		Applicable:       exportableKind(kind),
		CanWrite:         h.canConnectionWrite(r, "export"),
		Kind:             kind,
		KindLabel:        exportKindLabel(kind),
		FieldNameLabel:   exportFieldNameLabel(kind),
		DestinationLabel: exportDestinationLabel(kind, config),
		ScheduleCron:     strings.TrimSpace(config["schedule_cron"]),
		RegisterTypes:    modbusWritableRegisterTypes,
		DataTypes:        modbusDataTypes,
		Notice:           r.URL.Query().Get("notice"),
	}
	if !data.Applicable {
		h.render(w, "export_preview", data)
		return
	}
	if sources, err := h.availableExportSources(r.Context()); err == nil {
		data.AvailableSources = sources
	}
	if mappings, err := h.exportMappings(r.Context(), id); err == nil {
		data.Mappings = mappings
	}
	h.render(w, "export_preview", data)
}

// ExportRunWeb schreibt die aktuellen Werte aller Zuordnungen dieser
// Verbindung in die konfigurierte Zieldatei und merkt sich den
// Exportzeitpunkt je Zuordnung.
func (h *Handler) ExportRunWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "export") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/export/connections/" + id + "/preview"

	count, _, err := h.runExport(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Export fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d Feld(er) exportiert", count)), http.StatusSeeOther)
}

// runExport fuehrt einen Export tatsaechlich aus - gemeinsame Kernlogik
// fuer den manuellen "Jetzt exportieren"-Knopf (ExportRunWeb) und den
// automatischen Zeitplan (reconcileExportSchedule). Liefert die Anzahl
// exportierter Felder.
func (h *Handler) runExport(ctx context.Context, id string) (int, string, error) {
	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(ctx,
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='export'`, id).
		Scan(&kind, &configBytes); err != nil || !exportableKind(kind) {
		return 0, "", fmt.Errorf("Verbindung nicht gefunden")
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	mappings, err := h.exportMappings(ctx, id)
	if err != nil {
		return 0, "", fmt.Errorf("Zuordnungen konnten nicht geladen werden")
	}
	if len(mappings) == 0 {
		return 0, "", fmt.Errorf("keine Zuordnungen zum Exportieren vorhanden")
	}

	destLabel := exportDestinationLabel(kind, config)
	var writeErr error
	switch kind {
	case "excel":
		destPath := strings.TrimSpace(config["destination_path"])
		if destPath == "" {
			writeErr = fmt.Errorf("kein Zielpfad konfiguriert")
		} else {
			tpl := h.resolveExportTemplate(ctx, "excel", config["template_id"])
			writeErr = writeExcelExport(destPath, tpl.SheetName, mappings, h.exportLogoFile())
		}
	case "pdf":
		destPath := strings.TrimSpace(config["destination_path"])
		if destPath == "" {
			writeErr = fmt.Errorf("kein Zielpfad konfiguriert")
		} else {
			tpl := h.resolveExportTemplate(ctx, "pdf", config["template_id"])
			writeErr = writePDFExport(destPath, tpl.Title, tpl.Orientation, mappings, h.exportLogoFile())
		}
	case "csv":
		destPath := strings.TrimSpace(config["destination_path"])
		if destPath == "" {
			writeErr = fmt.Errorf("kein Zielpfad konfiguriert")
		} else {
			writeErr = writeCSVExport(destPath, strings.TrimSpace(config["delimiter"]), mappings)
		}
	case "sqlite", "mysql", "mssql":
		writeErr = writeSQLExport(kind, config, mappings)
	case "opcua":
		writeErr = writeOPCUAExport(ctx, config, mappings)
	case "modbus":
		writeErr = writeModbusExport(config, mappings)
	}
	if writeErr != nil {
		return 0, destLabel, writeErr
	}

	ids := make([]string, len(mappings))
	for i, m := range mappings {
		ids[i] = m.ID
	}
	if _, err := h.db.Exec(ctx,
		`UPDATE export_mappings SET last_exported_at=NOW() WHERE id::text = ANY($1)`, ids); err != nil {
		log.Error().Err(err).Str("connection_id", id).Msg("export: exportzeitpunkt konnte nicht gespeichert werden")
	}
	return len(mappings), destLabel, nil
}

// reconcileExportSchedule gleicht den automatischen Export-Zeitplan
// (schedule_cron) mit dem Soll-Zustand ab - aufgerufen nach jedem
// Anlegen/Bearbeiten/Aktivieren-Deaktivieren einer Verbindung. Ersetzt
// immer zuerst einen bestehenden Eintrag (Stop-dann-neu-Aufbau, wie bei
// den anderen Hintergrund-Manager dieser Session).
func (h *Handler) reconcileExportSchedule(id, direction string, enabled bool, config map[string]string) {
	if direction != "export" {
		return
	}
	spec := strings.TrimSpace(config["schedule_cron"])
	if !enabled || spec == "" {
		h.exportCron.Unschedule(id)
		return
	}
	if err := h.exportCron.Schedule(id, spec, func() { h.runExportScheduled(id) }); err != nil {
		log.Error().Err(err).Str("connection_id", id).Str("spec", spec).Msg("export-zeitplan ungültig")
	}
}

// StartEnabledExportSchedules baut beim Serverstart die Zeitplaene aller
// bereits aktivierten Export-Verbindungen mit Zeitplan wieder auf.
func (h *Handler) StartEnabledExportSchedules(ctx context.Context) {
	rows, err := h.db.Query(ctx,
		`SELECT id::text, config FROM import_export_connections WHERE direction='export' AND enabled=true`)
	if err != nil {
		log.Error().Err(err).Msg("export-zeitplaene konnten beim start nicht geladen werden")
		return
	}
	defer rows.Close()
	type pending struct {
		id     string
		config map[string]string
	}
	var items []pending
	for rows.Next() {
		var id string
		var configBytes []byte
		if err := rows.Scan(&id, &configBytes); err != nil {
			continue
		}
		config := map[string]string{}
		_ = json.Unmarshal(configBytes, &config)
		items = append(items, pending{id: id, config: config})
	}
	rows.Close()
	for _, it := range items {
		h.reconcileExportSchedule(it.id, "export", true, it.config)
	}
}

// StopExportSchedules beendet den Cron-Runner - beim geordneten
// Herunterfahren des PDH-Prozesses aufgerufen.
func (h *Handler) StopExportSchedules() {
	h.exportCron.Stop()
}

// runExportScheduled ist der Cron-Callback - Fehler landen nur im Log,
// da hier (anders als beim manuellen Knopf) niemand eine HTTP-Antwort
// entgegennimmt.
func (h *Handler) runExportScheduled(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	count, destLabel, err := h.runExport(ctx, id)
	if err != nil {
		log.Error().Err(err).Str("connection_id", id).Msg("automatischer export fehlgeschlagen")
		return
	}
	log.Info().Str("connection_id", id).Int("count", count).Str("destination", destLabel).Msg("automatischer export erfolgreich")
}

// resolveExportTemplate laedt die gewaehlte Vorlage - ohne Auswahl (oder
// bei geloeschter Vorlage) greifen dieselben Standardwerte wie vor der
// Vorlagenverwaltung, damit bestehende Verbindungen ohne template_id
// weiterhin funktionieren.
func (h *Handler) resolveExportTemplate(ctx context.Context, kind, templateID string) ExportTemplateView {
	templateID = strings.TrimSpace(templateID)
	if templateID != "" {
		if tpl, ok := h.exportTemplateByID(ctx, templateID); ok && tpl.Kind == kind {
			return tpl
		}
	}
	return ExportTemplateView{Kind: kind, Title: "Export", SheetName: "Export", Orientation: "P"}
}

// logo: optionaler Bildpfad (Erscheinungsbild -> Logo fuer Exporte); das
// Logo schwebt rechts neben der Tabelle, die Zellen bleiben unveraendert.
func writeExcelExport(path, sheetName string, mappings []ExportMappingView, logo string) error {
	path = resolveDataPath(path) // relative Pfade: globaler Datenspeicher (drives.go)
	if sheetName == "" {
		sheetName = "Export"
	}
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", sheetName); err != nil {
		return err
	}
	_ = f.SetCellValue(sheetName, "A1", "Feld")
	_ = f.SetCellValue(sheetName, "B1", "Wert")
	_ = f.SetCellValue(sheetName, "C1", "Quelle")
	_ = f.SetCellValue(sheetName, "D1", "Zuletzt aktualisiert")
	for i, m := range mappings {
		row := i + 2
		_ = f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), m.FieldName)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), m.SourceValue)
		_ = f.SetCellValue(sheetName, fmt.Sprintf("C%d", row), strings.TrimSpace(m.InfrastructureName+" · "+m.SourceName))
		_ = f.SetCellValue(sheetName, fmt.Sprintf("D%d", row), m.SourceReceivedAt)
	}
	if logo != "" {
		scale := 1.0
		if w, h := imageSize(logo); h > 0 {
			scale = 48.0 / float64(h) // ca. 48 px hoch
			if float64(w)*scale > 220 {
				scale = 220.0 / float64(w)
			}
		}
		// Ein fehlerhaftes Logo verhindert den Export nicht.
		_ = f.AddPicture(sheetName, "F1", logo, &excelize.GraphicOptions{ScaleX: scale, ScaleY: scale, Positioning: "oneCell", AltText: "Logo"})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("Zielverzeichnis konnte nicht angelegt werden: %w", err)
	}
	return f.SaveAs(path)
}

// writeCSVExport schreibt dieselben vier Spalten (Feld, Wert, Quelle,
// Zuletzt aktualisiert) wie writeExcelExport, nur als CSV-Datei -
// deutsche CSV-Dateien nutzen haeufig Semikolon als Trennzeichen (Komma
// ist dort das Dezimaltrennzeichen), daher konfigurierbar statt hart
// codiert.
func writeCSVExport(path, delimiter string, mappings []ExportMappingView) error {
	path = resolveDataPath(path) // relative Pfade: globaler Datenspeicher (drives.go)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("Zielverzeichnis konnte nicht angelegt werden: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("Zieldatei konnte nicht angelegt werden: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	w.Comma = csvDelimiterRune(delimiter)
	if err := w.Write([]string{"Feld", "Wert", "Quelle", "Zuletzt aktualisiert"}); err != nil {
		return err
	}
	for _, m := range mappings {
		row := []string{m.FieldName, m.SourceValue, strings.TrimSpace(m.InfrastructureName + " · " + m.SourceName), m.SourceReceivedAt}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writePDFExport(path, title, orientation string, mappings []ExportMappingView, logo string) error {
	if title == "" {
		title = "Export"
	}
	if orientation != "L" {
		orientation = "P"
	}
	pdf := fpdf.New(orientation, "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")
	pdf.AddPage()
	if logo != "" {
		// Logo oben rechts, 14 mm hoch (Breite proportional, max. 60 mm)
		pageW, _ := pdf.GetPageSize()
		_, _, right, _ := pdf.GetMargins()
		wmm, hmm := 0.0, 14.0
		if w, h := imageSize(logo); h > 0 {
			wmm = float64(w) * hmm / float64(h)
			if wmm > 60 {
				wmm, hmm = 60, 60*float64(h)/float64(w)
			}
		}
		if wmm > 0 {
			pdf.ImageOptions(logo, pageW-right-wmm, 8, wmm, hmm, false, fpdf.ImageOptions{ReadDpi: false}, 0, "")
			if pdf.Error() != nil {
				pdf.ClearError() // Logo nicht lesbar -> ohne Logo weiter
			}
		}
	}
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(0, 10, tr(title), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(0, 6, "Erstellt: "+time.Now().Format("02.01.2006 15:04:05"), "", 1, "L", false, 0, "")
	pdf.Ln(4)

	widths := []float64{45, 35, 70, 40}
	headers := []string{"Feld", "Wert", "Quelle", "Aktualisiert"}
	pdf.SetFont("Helvetica", "B", 10)
	for i, header := range headers {
		pdf.CellFormat(widths[i], 8, tr(header), "1", 0, "L", false, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetFont("Helvetica", "", 9)
	for _, m := range mappings {
		pdf.CellFormat(widths[0], 7, tr(m.FieldName), "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[1], 7, tr(m.SourceValue), "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[2], 7, tr(strings.TrimSpace(m.InfrastructureName+" · "+m.SourceName)), "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[3], 7, tr(m.SourceReceivedAt), "1", 0, "L", false, 0, "")
		pdf.Ln(-1)
	}
	if err := pdf.Error(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("Zielverzeichnis konnte nicht angelegt werden: %w", err)
	}
	return pdf.OutputFileAndClose(path)
}

// writeSQLExport haengt fuer jede Zuordnung eine Zeile an eine feste
// Log-Tabelle (field_name, value, source, exported_at) an - bewusst ein
// einfaches, immer gleiches Schema statt dynamischer Spalten je
// Zielfeldname, damit ein Export nie an einem Typkonflikt einer
// bestehenden Spalte scheitert. Funktioniert identisch fuer SQLite und
// MySQL (siehe sql_preview.go fuer die gemeinsame Verbindungs-/
// Quoting-Logik).
func writeSQLExport(kind string, config map[string]string, mappings []ExportMappingView) error {
	var db *sql.DB
	var err error
	switch kind {
	case "sqlite":
		db, err = openSqliteDBWritable(strings.TrimSpace(config["file_path"]))
	case "mysql":
		db, err = openMysqlDB(config)
	case "mssql":
		db, err = openMssqlDB(config)
	default:
		return fmt.Errorf("nicht unterstützter Verbindungstyp")
	}
	if err != nil {
		return err
	}
	defer db.Close()

	table := strings.TrimSpace(config["table_name"])
	if table == "" {
		return fmt.Errorf("keine Zieltabelle konfiguriert")
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (field_name TEXT, value TEXT, source TEXT, exported_at TIMESTAMP)`, quoteIdent(kind, table))
	if _, err := db.Exec(createSQL); err != nil {
		return fmt.Errorf("Zieltabelle konnte nicht angelegt werden: %w", err)
	}

	insertSQL := fmt.Sprintf(`INSERT INTO %s (field_name, value, source, exported_at) VALUES (?, ?, ?, ?)`, quoteIdent(kind, table))
	now := time.Now()
	for _, m := range mappings {
		source := strings.TrimSpace(m.InfrastructureName + " · " + m.SourceName)
		if _, err := db.Exec(insertSQL, m.FieldName, m.SourceValue, source, now); err != nil {
			return fmt.Errorf("Zeile für '%s' konnte nicht geschrieben werden: %w", m.FieldName, err)
		}
	}
	return nil
}

// writeOPCUAExport schreibt fuer jede Zuordnung den Quellwert auf die
// als Zielfeldname hinterlegte NodeID eines bestehenden, beschreibbaren
// Knotens. Der aktuelle Werttyp des Zielknotens wird vorher gelesen, um
// den (immer als Text gespeicherten) Quellwert typgerecht umzuwandeln -
// ein OPC-UA-Server lehnt sonst meist Typkonflikte ab.
func writeOPCUAExport(ctx context.Context, config map[string]string, mappings []ExportMappingView) error {
	client, err := openOPCUAClient(ctx, config)
	if err != nil {
		return err
	}
	defer client.Close(ctx)

	for _, m := range mappings {
		nodeID, err := ua.ParseNodeID(strings.TrimSpace(m.FieldName))
		if err != nil {
			return fmt.Errorf("ungültige NodeID '%s': %w", m.FieldName, err)
		}
		node := client.Node(nodeID)
		current, err := node.Value(ctx)
		if err != nil {
			return fmt.Errorf("Zielknoten '%s' konnte nicht gelesen werden: %w", m.FieldName, err)
		}
		variant, err := coerceOPCUAVariant(current, m.SourceValue)
		if err != nil {
			return fmt.Errorf("Wert für '%s' konnte nicht umgewandelt werden: %w", m.FieldName, err)
		}
		resp, err := client.Write(ctx, &ua.WriteRequest{
			NodesToWrite: []*ua.WriteValue{
				{
					NodeID:      nodeID,
					AttributeID: ua.AttributeIDValue,
					Value: &ua.DataValue{
						EncodingMask: ua.DataValueValue,
						Value:        variant,
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("Schreiben nach '%s' fehlgeschlagen: %w", m.FieldName, err)
		}
		if len(resp.Results) == 0 || resp.Results[0] != ua.StatusOK {
			return fmt.Errorf("Schreiben nach '%s' vom Server abgelehnt", m.FieldName)
		}
	}
	return nil
}

// coerceOPCUAVariant wandelt den (als Text gespeicherten) Quellwert in
// denselben Go-Typ um, den der Zielknoten aktuell traegt - einfache,
// best-effort Typanpassung fuer die gaengigsten Faelle (Zahl/Bool/Text).
func coerceOPCUAVariant(current *ua.Variant, value string) (*ua.Variant, error) {
	switch current.Value().(type) {
	case float32:
		f, err := strconv.ParseFloat(value, 32)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(float32(f))
	case float64:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(f)
	case int16:
		n, err := strconv.ParseInt(value, 10, 16)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(int16(n))
	case int32:
		n, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(int32(n))
	case int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(n)
	case uint16:
		n, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(uint16(n))
	case uint32:
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(uint32(n))
	case bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(b)
	default:
		return ua.NewVariant(value)
	}
}

// writeModbusExport schreibt fuer jede Zuordnung den Quellwert auf das
// als Zielfeldname hinterlegte Register ("registertyp:adresse:datentyp",
// gleiche Notation wie auf der Import-Seite). Nur Coils und Holding
// Register sind beschreibbar.
func writeModbusExport(config map[string]string, mappings []ExportMappingView) error {
	handler, client, err := openModbusClient(config)
	if err != nil {
		return err
	}
	defer handler.Close()

	for _, m := range mappings {
		reading, err := parseModbusReading(m.FieldName)
		if err != nil {
			return fmt.Errorf("ungültiges Zielregister '%s': %w", m.FieldName, err)
		}
		if err := writeModbusValue(client, reading, m.SourceValue); err != nil {
			return fmt.Errorf("Schreiben nach '%s' fehlgeschlagen: %w", m.FieldName, err)
		}
	}
	return nil
}

func writeModbusValue(client modbus.Client, reading modbusReading, value string) error {
	switch reading.RegisterType {
	case "coil":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("ungültiger Bool-Wert: %w", err)
		}
		v := uint16(0)
		if b {
			v = 0xFF00
		}
		_, err = client.WriteSingleCoil(reading.Address, v)
		return err
	case "holding":
		switch reading.DataType {
		case "uint16":
			n, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				return fmt.Errorf("ungültiger Wert: %w", err)
			}
			_, err = client.WriteSingleRegister(reading.Address, uint16(n))
			return err
		case "int16":
			n, err := strconv.ParseInt(value, 10, 16)
			if err != nil {
				return fmt.Errorf("ungültiger Wert: %w", err)
			}
			_, err = client.WriteSingleRegister(reading.Address, uint16(int16(n)))
			return err
		case "uint32":
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return fmt.Errorf("ungültiger Wert: %w", err)
			}
			data := make([]byte, 4)
			binary.BigEndian.PutUint32(data, uint32(n))
			_, err = client.WriteMultipleRegisters(reading.Address, 2, data)
			return err
		case "int32":
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return fmt.Errorf("ungültiger Wert: %w", err)
			}
			data := make([]byte, 4)
			binary.BigEndian.PutUint32(data, uint32(int32(n)))
			_, err = client.WriteMultipleRegisters(reading.Address, 2, data)
			return err
		case "float32":
			f, err := strconv.ParseFloat(value, 32)
			if err != nil {
				return fmt.Errorf("ungültiger Wert: %w", err)
			}
			data := make([]byte, 4)
			binary.BigEndian.PutUint32(data, math.Float32bits(float32(f)))
			_, err = client.WriteMultipleRegisters(reading.Address, 2, data)
			return err
		default:
			return fmt.Errorf("unbekannter Datentyp")
		}
	default:
		return fmt.Errorf("registertyp '%s' ist nicht beschreibbar (nur coil/holding)", reading.RegisterType)
	}
}
