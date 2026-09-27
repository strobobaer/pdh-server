package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-pdf/fpdf"
	"github.com/rs/zerolog/log"
	"github.com/xuri/excelize/v2"
)

// Export-Vorschau: das Gegenstueck zu den Import-Vorschauseiten. Statt
// eine externe Quelle zu erkunden, zeigt diese Seite den System-weiten
// Pool bereits importierter Werte (import_mappings) zur Auswahl an -
// "Fuer Export markieren" legt eine export_mappings-Zeile mit freiem
// Zielfeldnamen an. "Jetzt exportieren" schreibt die aktuellen Werte
// aller Zuordnungen dieser Verbindung in die konfigurierte Zieldatei
// (Excel oder PDF).

func exportableKind(kind string) bool {
	return kind == "excel" || kind == "pdf"
}

func exportKindLabel(kind string) string {
	if kind == "pdf" {
		return "PDF"
	}
	return "Excel"
}

type ExportPreviewPageData struct {
	BaseData
	ConnectionID     string
	ConnectionName   string
	Applicable       bool
	CanWrite         bool
	KindLabel        string
	DestinationPath  string
	AvailableSources []ImportMappingView
	Mappings         []ExportMappingView
	Notice           string
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
		BaseData:        h.baseData(r, "export", "Export-Vorschau", name),
		ConnectionID:    id,
		ConnectionName:  name,
		Applicable:      exportableKind(kind),
		CanWrite:        h.canConnectionWrite(r, "export"),
		KindLabel:       exportKindLabel(kind),
		DestinationPath: strings.TrimSpace(config["destination_path"]),
		Notice:          r.URL.Query().Get("notice"),
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

	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='export'`, id).
		Scan(&kind, &configBytes); err != nil || !exportableKind(kind) {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	mappings, err := h.exportMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}
	if len(mappings) == 0 {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Keine Zuordnungen zum Exportieren vorhanden"), http.StatusSeeOther)
		return
	}
	destPath := strings.TrimSpace(config["destination_path"])
	if destPath == "" {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Kein Zielpfad konfiguriert"), http.StatusSeeOther)
		return
	}

	var writeErr error
	switch kind {
	case "excel":
		writeErr = writeExcelExport(destPath, strings.TrimSpace(config["sheet_name"]), mappings)
	case "pdf":
		writeErr = writePDFExport(destPath, strings.TrimSpace(config["template_name"]), mappings)
	}
	if writeErr != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Export fehlgeschlagen: "+writeErr.Error()), http.StatusSeeOther)
		return
	}

	ids := make([]string, len(mappings))
	for i, m := range mappings {
		ids[i] = m.ID
	}
	if _, err := h.db.Exec(r.Context(),
		`UPDATE export_mappings SET last_exported_at=NOW() WHERE id::text = ANY($1)`, ids); err != nil {
		log.Error().Err(err).Str("connection_id", id).Msg("export: exportzeitpunkt konnte nicht gespeichert werden")
	}

	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d Feld(er) nach %s exportiert", len(mappings), destPath)), http.StatusSeeOther)
}

func writeExcelExport(path, sheetName string, mappings []ExportMappingView) error {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("Zielverzeichnis konnte nicht angelegt werden: %w", err)
	}
	return f.SaveAs(path)
}

func writePDFExport(path, title string, mappings []ExportMappingView) error {
	if title == "" {
		title = "Export"
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")
	pdf.AddPage()
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
