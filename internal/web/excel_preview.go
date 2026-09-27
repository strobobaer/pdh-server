package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/xuri/excelize/v2"
)

// Excel-Vorschau: das Excel-Gegenstueck zum MQTT-Sniffer im selben Muster
// (Vorschau der Quelle -> Spalte markieren -> Infrastruktur-Picker ->
// freie Bezeichnung/Notiz). Da Excel-Dateien statisch sind (kein Push wie
// bei MQTT), gibt es hier keinen dauerhaften Hintergrund-Consumer,
// sondern einen manuellen "Jetzt aktualisieren"-Knopf, der die Datei neu
// liest und fuer jede Zuordnung den Wert der letzten Zeile in der
// markierten Spalte uebernimmt (die Datei wird als fortlaufendes Log
// behandelt: neue Zeilen = neue Werte).

const excelPreviewMaxRows = 20

type ExcelColumn struct {
	Index int
	Label string
}

type ExcelPreviewPageData struct {
	BaseData
	ConnectionID   string
	ConnectionName string
	Applicable     bool
	CanWrite       bool
	SourcePath     string
	SheetName      string
	HasHeader      bool
	Error          string
	Columns        []ExcelColumn
	Rows           [][]string
	Mappings       []ImportMappingView
	Notice         string
}

func columnLetter(n int) string {
	letter := ""
	for n >= 0 {
		letter = string(rune('A'+(n%26))) + letter
		n = n/26 - 1
	}
	return letter
}

func readExcelPreview(path, sheet string, hasHeader bool) ([]ExcelColumn, [][]string, error) {
	if path == "" {
		return nil, nil, fmt.Errorf("kein Quellpfad konfiguriert")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("Datei nicht erreichbar: %s", err.Error())
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("Datei konnte nicht gelesen werden: %s", err.Error())
	}
	defer f.Close()

	sheetName := sheet
	if sheetName == "" {
		list := f.GetSheetList()
		if len(list) == 0 {
			return nil, nil, fmt.Errorf("keine Tabellenblätter in der Datei gefunden")
		}
		sheetName = list[0]
	}

	allRows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, nil, fmt.Errorf("Tabellenblatt '%s' konnte nicht gelesen werden: %s", sheetName, err.Error())
	}
	if len(allRows) == 0 {
		return nil, nil, fmt.Errorf("Tabellenblatt '%s' ist leer", sheetName)
	}

	colCount := 0
	for _, row := range allRows {
		if len(row) > colCount {
			colCount = len(row)
		}
	}

	columns := make([]ExcelColumn, colCount)
	dataRows := allRows
	if hasHeader {
		header := allRows[0]
		for i := 0; i < colCount; i++ {
			label := columnLetter(i)
			if i < len(header) && strings.TrimSpace(header[i]) != "" {
				label = strings.TrimSpace(header[i])
			}
			columns[i] = ExcelColumn{Index: i, Label: label}
		}
		if len(allRows) > 1 {
			dataRows = allRows[1:]
		} else {
			dataRows = nil
		}
	} else {
		for i := 0; i < colCount; i++ {
			columns[i] = ExcelColumn{Index: i, Label: columnLetter(i)}
		}
	}

	if len(dataRows) > excelPreviewMaxRows {
		dataRows = dataRows[:excelPreviewMaxRows]
	}
	return columns, dataRows, nil
}

func (h *Handler) ExcelPreviewPage(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	var name, kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT name, kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&name, &kind, &configBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	data := ExcelPreviewPageData{
		BaseData:       h.baseData(r, "import", "Excel-Vorschau", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     kind == "excel",
		CanWrite:       h.canConnectionWrite(r, "import"),
		SourcePath:     strings.TrimSpace(config["source_path"]),
		SheetName:      strings.TrimSpace(config["sheet_name"]),
		HasHeader:      config["has_header"] == "true",
		Notice:         r.URL.Query().Get("notice"),
	}
	if data.Applicable {
		cols, rows, err := readExcelPreview(data.SourcePath, data.SheetName, data.HasHeader)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Columns = cols
			data.Rows = rows
		}
		if mappings, err := h.importMappings(r.Context(), id); err == nil {
			data.Mappings = mappings
		}
	}
	h.render(w, "excel_preview", data)
}

// ExcelRefreshValuesWeb liest die konfigurierte Datei neu ein und
// uebernimmt fuer jede bestehende Zuordnung den Wert der letzten
// Datenzeile in der markierten Spalte (Zuordnung ueber den aktuellen
// Spaltennamen/-buchstaben - verschwindet eine Spalte, bleibt ihre
// Zuordnung unveraendert stehen statt zu crashen).
func (h *Handler) ExcelRefreshValuesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/import/connections/" + id + "/preview"

	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&kind, &configBytes); err != nil || kind != "excel" {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	columns, rows, err := readExcelPreview(strings.TrimSpace(config["source_path"]), strings.TrimSpace(config["sheet_name"]), config["has_header"] == "true")
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	if len(rows) == 0 {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Keine Datenzeilen gefunden"), http.StatusSeeOther)
		return
	}
	lastRow := rows[len(rows)-1]

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}
	labelToIndex := make(map[string]int, len(columns))
	for _, col := range columns {
		labelToIndex[col.Label] = col.Index
	}

	updated := 0
	for _, mp := range mappings {
		idx, ok := labelToIndex[mp.SourceRef]
		if !ok || idx >= len(lastRow) {
			continue
		}
		if _, err := h.db.Exec(r.Context(), `
			UPDATE import_mappings SET last_value=$1, last_received_at=NOW() WHERE id=$2`, lastRow[idx], mp.ID); err != nil {
			log.Error().Err(err).Str("mapping_id", mp.ID).Msg("excel-wert konnte nicht gespeichert werden")
			continue
		}
		updated++
	}
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
