package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	_ "modernc.org/sqlite"
)

// SQLite-Vorschau: dasselbe Muster wie bei Excel (Vorschau der Quelle ->
// Spalte markieren -> Infrastruktur-Picker -> freie Bezeichnung/Notiz),
// nur dass eine SQLite-Datei mehrere Tabellen enthalten kann - deshalb
// zusaetzlich eine Tabellenauswahl, und der Quellwert einer Zuordnung
// wird als "Tabelle.Spalte" gespeichert statt nur als Spaltenname.
// Reiner Go-Treiber (modernc.org/sqlite), damit CGO_ENABLED=0 (siehe
// Dockerfile) weiterhin funktioniert.

const sqlitePreviewMaxRows = 20

func openSqlite(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("kein Dateipfad konfiguriert")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("Datei konnte nicht geöffnet werden: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("Datei nicht erreichbar oder keine gültige SQLite-Datenbank: %w", err)
	}
	return db, nil
}

func listSqliteTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func readSqlitePreview(path, table string) ([]string, [][]string, error) {
	db, err := openSqlite(path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	if table == "" {
		return nil, nil, fmt.Errorf("keine Tabelle ausgewählt")
	}
	rows, err := db.Query(fmt.Sprintf(`SELECT * FROM %q LIMIT %d`, table, sqlitePreviewMaxRows))
	if err != nil {
		return nil, nil, fmt.Errorf("Tabelle '%s' konnte nicht gelesen werden: %w", table, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	dataRows := make([][]string, 0)
	values := make([]interface{}, len(columns))
	pointers := make([]interface{}, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(columns))
		for i, v := range values {
			row[i] = sqliteValueToString(v)
		}
		dataRows = append(dataRows, row)
	}
	return columns, dataRows, rows.Err()
}

func sqliteValueToString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// readSqliteLastValues liefert je angefragter Spalte den Wert der
// zuletzt eingefuegten Zeile (hoechste rowid) einer Tabelle - fuer
// "Jetzt aktualisieren", analog zur "letzten Zeile" bei Excel.
func readSqliteLastRow(db *sql.DB, table string, columns []string) (map[string]string, error) {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = fmt.Sprintf("%q", c)
	}
	query := fmt.Sprintf(`SELECT %s FROM %q ORDER BY rowid DESC LIMIT 1`, strings.Join(quoted, ", "), table)
	row := db.QueryRow(query)
	values := make([]interface{}, len(columns))
	pointers := make([]interface{}, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := row.Scan(pointers...); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(columns))
	for i, c := range columns {
		out[c] = sqliteValueToString(values[i])
	}
	return out, nil
}

type SqlitePreviewPageData struct {
	BaseData
	ConnectionID   string
	ConnectionName string
	Applicable     bool
	CanWrite       bool
	SourcePath     string
	Tables         []string
	SelectedTable  string
	Error          string
	Columns        []string
	Rows           [][]string
	Mappings       []ImportMappingView
	Notice         string
}

func (h *Handler) SqlitePreviewPage(w http.ResponseWriter, r *http.Request) {
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

	data := SqlitePreviewPageData{
		BaseData:       h.baseData(r, "import", "SQLite-Vorschau", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     kind == "sqlite",
		CanWrite:       h.canConnectionWrite(r, "import"),
		SourcePath:     strings.TrimSpace(config["file_path"]),
		SelectedTable:  strings.TrimSpace(r.URL.Query().Get("table")),
		Notice:         r.URL.Query().Get("notice"),
	}
	if !data.Applicable {
		h.render(w, "sqlite_preview", data)
		return
	}

	db, err := openSqlite(data.SourcePath)
	if err != nil {
		data.Error = err.Error()
		h.render(w, "sqlite_preview", data)
		return
	}
	defer db.Close()

	tables, err := listSqliteTables(db)
	if err != nil {
		data.Error = "Tabellen konnten nicht gelesen werden: " + err.Error()
		h.render(w, "sqlite_preview", data)
		return
	}
	data.Tables = tables
	if data.SelectedTable == "" && len(tables) > 0 {
		data.SelectedTable = tables[0]
	}
	if data.SelectedTable != "" {
		cols, rows, err := readSqlitePreview(data.SourcePath, data.SelectedTable)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Columns = cols
			data.Rows = rows
		}
	}
	if mappings, err := h.importMappings(r.Context(), id); err == nil {
		data.Mappings = mappings
	}
	h.render(w, "sqlite_preview", data)
}

// SqliteRefreshValuesWeb liest fuer jede Zuordnung (Quellwert
// "Tabelle.Spalte") den Wert der zuletzt eingefuegten Zeile neu ein -
// Tabellen werden dabei gruppiert, um pro Tabelle nur eine Abfrage zu
// stellen statt eine je Zuordnung.
func (h *Handler) SqliteRefreshValuesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	base := "/import/connections/" + id + "/browse"

	var kind string
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, config FROM import_export_connections WHERE id=$1 AND direction='import'`, id).
		Scan(&kind, &configBytes); err != nil || kind != "sqlite" {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)
	path := strings.TrimSpace(config["file_path"])

	db, err := openSqlite(path)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Aktualisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	defer db.Close()

	mappings, err := h.importMappings(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Zuordnungen konnten nicht geladen werden"), http.StatusSeeOther)
		return
	}

	byTable := map[string][]ImportMappingView{}
	for _, mp := range mappings {
		table, _, ok := strings.Cut(mp.SourceRef, ".")
		if !ok {
			continue
		}
		byTable[table] = append(byTable[table], mp)
	}

	updated := 0
	for table, tableMappings := range byTable {
		columns := make([]string, len(tableMappings))
		for i, mp := range tableMappings {
			_, col, _ := strings.Cut(mp.SourceRef, ".")
			columns[i] = col
		}
		values, err := readSqliteLastRow(db, table, columns)
		if err != nil {
			log.Error().Err(err).Str("connection_id", id).Str("table", table).Msg("sqlite-werte konnten nicht gelesen werden")
			continue
		}
		for i, mp := range tableMappings {
			value, ok := values[columns[i]]
			if !ok {
				continue
			}
			if _, err := h.db.Exec(r.Context(), `
				UPDATE import_mappings SET last_value=$1, last_received_at=NOW() WHERE id=$2`, value, mp.ID); err != nil {
				log.Error().Err(err).Str("mapping_id", mp.ID).Msg("sqlite-wert konnte nicht gespeichert werden")
				continue
			}
			updated++
		}
	}
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
