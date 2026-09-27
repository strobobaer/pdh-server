package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	mysqldriver "github.com/go-sql-driver/mysql"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/rs/zerolog/log"
	_ "modernc.org/sqlite"
)

// SQL-Datenbank-Vorschau: gemeinsame Tabellen/Spalten-Vorschau fuer alle
// relationalen Import-Konnektoren (SQLite, MySQL, MSSQL - jeweils nur ein
// zusaetzlicher Dialekt-Zweig in den Funktionen unten, keine eigene
// Seite je Datenbanktyp). Eine Datenbank kann mehrere Tabellen enthalten,
// deshalb zusaetzlich eine Tabellenauswahl (anders als bei Excel mit
// fester Sheet-Konfiguration). Der Quellwert einer Zuordnung wird als
// "Tabelle.Spalte" gespeichert.

const sqlPreviewMaxRows = 20

func sqlBrowsableKind(kind string) bool {
	switch kind {
	case "sqlite", "mysql", "mssql":
		return true
	default:
		return false
	}
}

func sqlKindLabel(kind string) string {
	switch kind {
	case "sqlite":
		return "SQLite"
	case "mysql":
		return "MySQL"
	case "mssql":
		return "MSSQL"
	default:
		return kind
	}
}

func openSQLConnection(kind string, config map[string]string) (*sql.DB, string, error) {
	switch kind {
	case "sqlite":
		path := strings.TrimSpace(config["file_path"])
		db, err := openSqliteDB(path)
		return db, path, err
	case "mysql":
		db, err := openMysqlDB(config)
		label := strings.TrimSpace(config["host"]) + ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "3306") + "/" + strings.TrimSpace(config["database"])
		return db, label, err
	case "mssql":
		db, err := openMssqlDB(config)
		host := strings.TrimSpace(config["host"])
		if instance := strings.TrimSpace(config["instance_name"]); instance != "" {
			host += `\` + instance
		} else {
			host += ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "1433")
		}
		label := host + "/" + strings.TrimSpace(config["database"])
		return db, label, err
	default:
		return nil, "", fmt.Errorf("nicht unterstützter Verbindungstyp")
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func openSqliteDB(path string) (*sql.DB, error) {
	if path == "" {
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

// openSqliteDBWritable oeffnet (und legt bei Bedarf an) eine SQLite-
// Datei beschreibbar - fuer den Export, anders als openSqliteDB (lesend,
// fuer Import-Vorschau/Browsing), das bewusst read-only oeffnet und eine
// noch nicht existierende Datei daher nicht anlegen wuerde.
func openSqliteDBWritable(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("kein Dateipfad konfiguriert")
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, fmt.Errorf("Datei konnte nicht geöffnet werden: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("Datei nicht erreichbar oder keine gültige SQLite-Datenbank: %w", err)
	}
	return db, nil
}

func openMysqlDB(config map[string]string) (*sql.DB, error) {
	host := strings.TrimSpace(config["host"])
	if host == "" {
		return nil, fmt.Errorf("kein Host konfiguriert")
	}
	port := firstNonEmpty(strings.TrimSpace(config["port"]), "3306")

	cfg := mysqldriver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = host + ":" + port
	cfg.DBName = strings.TrimSpace(config["database"])
	cfg.User = config["username"]
	cfg.Passwd = config["password"]
	cfg.ParseTime = true
	if config["use_tls"] == "true" {
		cfg.TLSConfig = "true"
	}

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return db, nil
}

func openMssqlDB(config map[string]string) (*sql.DB, error) {
	host := strings.TrimSpace(config["host"])
	if host == "" {
		return nil, fmt.Errorf("kein Host konfiguriert")
	}
	dsn := url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(config["username"], config["password"]),
	}
	if instance := strings.TrimSpace(config["instance_name"]); instance != "" {
		// Mit Instanzname ohne Port verbinden - der Treiber ermittelt den
		// tatsaechlichen Port ueber den SQL Server Browser Dienst (UDP 1434).
		dsn.Host = host
		dsn.Path = "/" + instance
	} else {
		dsn.Host = host + ":" + firstNonEmpty(strings.TrimSpace(config["port"]), "1433")
	}

	query := url.Values{}
	if db := strings.TrimSpace(config["database"]); db != "" {
		query.Set("database", db)
	}
	if config["use_tls"] == "true" {
		query.Set("encrypt", "true")
	} else {
		query.Set("encrypt", "disable")
	}
	dsn.RawQuery = query.Encode()

	db, err := sql.Open("sqlserver", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return db, nil
}

func quoteIdent(kind, name string) string {
	switch kind {
	case "mysql":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case "mssql":
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	default: // sqlite
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

func listSQLTables(db *sql.DB, kind string) ([]string, error) {
	var query string
	switch kind {
	case "sqlite":
		query = `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	case "mysql":
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name`
	case "mssql":
		query = `SELECT table_name FROM information_schema.tables WHERE table_type = 'BASE TABLE' ORDER BY table_name`
	default:
		return nil, fmt.Errorf("nicht unterstützt")
	}
	rows, err := db.Query(query)
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

func sqlValueToString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func readSQLPreview(db *sql.DB, kind, table string) ([]string, [][]string, error) {
	if table == "" {
		return nil, nil, fmt.Errorf("keine Tabelle ausgewählt")
	}
	query := fmt.Sprintf(`SELECT * FROM %s LIMIT %d`, quoteIdent(kind, table), sqlPreviewMaxRows)
	rows, err := db.Query(query)
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
			row[i] = sqlValueToString(v)
		}
		dataRows = append(dataRows, row)
	}
	return columns, dataRows, rows.Err()
}

// findOrderColumn liefert eine Spalte, nach der "die zuletzt eingefuegte
// Zeile" bestimmt werden kann - bei SQLite immer die eingebaute rowid,
// bei MySQL der (einzelne) Primaerschluessel. Ohne eindeutigen Kandidaten
// ein klarer Fehler statt eine zufaellige Zeile zurueckzugeben.
func singlePrimaryKeyColumn(rows *sql.Rows) (string, error) {
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(cols) != 1 {
		return "", fmt.Errorf("keine eindeutige Sortierspalte gefunden (Primärschlüssel mit genau einer Spalte erforderlich)")
	}
	return cols[0], nil
}

func findOrderColumn(db *sql.DB, kind, table string) (column string, quoted bool, err error) {
	switch kind {
	case "sqlite":
		return "rowid", false, nil
	case "mysql":
		rows, err := db.Query(`
			SELECT column_name FROM information_schema.key_column_usage
			WHERE table_schema = DATABASE() AND table_name = ? AND constraint_name = 'PRIMARY'
			ORDER BY ordinal_position`, table)
		if err != nil {
			return "", false, err
		}
		col, err := singlePrimaryKeyColumn(rows)
		if err != nil {
			return "", false, err
		}
		return col, true, nil
	case "mssql":
		rows, err := db.Query(`
			SELECT ku.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku
			  ON tc.constraint_name = ku.constraint_name AND tc.table_schema = ku.table_schema
			WHERE tc.table_name = ? AND tc.constraint_type = 'PRIMARY KEY'
			ORDER BY ku.ordinal_position`, table)
		if err != nil {
			return "", false, err
		}
		col, err := singlePrimaryKeyColumn(rows)
		if err != nil {
			return "", false, err
		}
		return col, true, nil
	default:
		return "", false, fmt.Errorf("nicht unterstützt")
	}
}

func readSQLLastRow(db *sql.DB, kind, table string, columns []string) (map[string]string, error) {
	orderColumn, quoted, err := findOrderColumn(db, kind, table)
	if err != nil {
		return nil, fmt.Errorf("Tabelle '%s': %w", table, err)
	}
	orderExpr := orderColumn
	if quoted {
		orderExpr = quoteIdent(kind, orderColumn)
	}
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = quoteIdent(kind, c)
	}
	query := fmt.Sprintf(`SELECT %s FROM %s ORDER BY %s DESC LIMIT 1`,
		strings.Join(quotedCols, ", "), quoteIdent(kind, table), orderExpr)
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
		out[c] = sqlValueToString(values[i])
	}
	return out, nil
}

type SQLPreviewPageData struct {
	BaseData
	ConnectionID   string
	ConnectionName string
	Applicable     bool
	CanWrite       bool
	KindLabel      string
	SourceLabel    string
	Tables         []string
	SelectedTable  string
	Error          string
	Columns        []string
	Rows           [][]string
	Mappings       []ImportMappingView
	Notice         string
}

func (h *Handler) SQLBrowsePage(w http.ResponseWriter, r *http.Request) {
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

	data := SQLPreviewPageData{
		BaseData:       h.baseData(r, "import", "Datenbank-Vorschau", name),
		ConnectionID:   id,
		ConnectionName: name,
		Applicable:     sqlBrowsableKind(kind),
		CanWrite:       h.canConnectionWrite(r, "import"),
		KindLabel:      sqlKindLabel(kind),
		SelectedTable:  strings.TrimSpace(r.URL.Query().Get("table")),
		Notice:         r.URL.Query().Get("notice"),
	}
	if !data.Applicable {
		h.render(w, "sql_browse", data)
		return
	}

	db, sourceLabel, err := openSQLConnection(kind, config)
	data.SourceLabel = sourceLabel
	if err != nil {
		data.Error = err.Error()
		h.render(w, "sql_browse", data)
		return
	}
	defer db.Close()

	tables, err := listSQLTables(db, kind)
	if err != nil {
		data.Error = "Tabellen konnten nicht gelesen werden: " + err.Error()
		h.render(w, "sql_browse", data)
		return
	}
	data.Tables = tables
	if data.SelectedTable == "" && len(tables) > 0 {
		data.SelectedTable = tables[0]
	}
	if data.SelectedTable != "" {
		cols, rows, err := readSQLPreview(db, kind, data.SelectedTable)
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
	h.render(w, "sql_browse", data)
}

// SQLRefreshValuesWeb liest fuer jede Zuordnung (Quellwert
// "Tabelle.Spalte") den Wert der zuletzt eingefuegten Zeile neu ein -
// Tabellen werden dabei gruppiert, um pro Tabelle nur eine Abfrage zu
// stellen statt eine je Zuordnung.
func (h *Handler) SQLRefreshValuesWeb(w http.ResponseWriter, r *http.Request) {
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
		Scan(&kind, &configBytes); err != nil || !sqlBrowsableKind(kind) {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	config := map[string]string{}
	_ = json.Unmarshal(configBytes, &config)

	db, _, err := openSQLConnection(kind, config)
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
		values, err := readSQLLastRow(db, kind, table, columns)
		if err != nil {
			log.Error().Err(err).Str("connection_id", id).Str("table", table).Msg("sql-werte konnten nicht gelesen werden")
			continue
		}
		for i, mp := range tableMappings {
			value, ok := values[columns[i]]
			if !ok {
				continue
			}
			if _, err := h.db.Exec(r.Context(), `
				UPDATE import_mappings SET last_value=$1, last_received_at=NOW() WHERE id=$2`, value, mp.ID); err != nil {
				log.Error().Err(err).Str("mapping_id", mp.ID).Msg("sql-wert konnte nicht gespeichert werden")
				continue
			}
			updated++
		}
	}
	http.Redirect(w, r, base+"?notice="+url.QueryEscape(fmt.Sprintf("%d von %d Wert(en) aktualisiert", updated, len(mappings))), http.StatusSeeOther)
}
