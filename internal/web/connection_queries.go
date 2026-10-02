package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gopcua/opcua/ua"
	"github.com/rs/zerolog/log"
)

// Abfragen je Import-Verbindung: beliebig viele benannte Abfragen (SQL-SELECT,
// REST-/Web-Endpunkt, Modbus-Registerblock, OPC-UA-Knotenliste, Excel/CSV).
// Jede liefert eine Tabelle (Spalten + Zeilen). Zuordnungen verweisen mit dem
// Quellwert "q:<abfrage-id>:<spalte>" auf eine Spalte; uebernommen wird der
// Wert der gewaehlten Zeile (erste oder letzte).

const (
	queryMaxRows    = 200
	queryMaxColumns = 80
	queryTimeout    = 30 * time.Second
	queryRefPrefix  = "q:"
)

// queryKindSupported: Verbindungstypen mit Abfragen (MQTT ist Push – dort
// entstehen Werte ueber Topics im Sniffer).
func queryKindSupported(kind string) bool {
	switch kind {
	case "sqlite", "mysql", "mssql", "web", "rest_api", "modbus", "opcua", "excel", "csv":
		return true
	}
	return false
}

// querySpecFields: erlaubte Felder je Typ (Whitelist wie bei den Verbindungen).
func querySpecFields(kind string) []string {
	switch {
	case sqlBrowsableKind(kind):
		return []string{"sql", "row"}
	case webBrowsableKind(kind):
		return []string{"path", "items_path", "row"}
	case kind == "modbus":
		return []string{"area", "start", "count", "type"}
	case kind == "opcua":
		return []string{"nodes"}
	case kind == "excel":
		return []string{"sheet", "row"}
	case kind == "csv":
		return []string{"row"}
	}
	return nil
}

type queryResult struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated,omitempty"`
}

// rowFor: Zeile, aus der Zuordnungen ihren Wert nehmen.
func (q queryResult) rowFor(spec map[string]string) []string {
	if len(q.Rows) == 0 {
		return nil
	}
	if spec["row"] == "last" {
		return q.Rows[len(q.Rows)-1]
	}
	return q.Rows[0]
}

// value: Wert einer Spalte in der massgeblichen Zeile.
func (q queryResult) value(spec map[string]string, column string) (string, bool) {
	row := q.rowFor(spec)
	for i, c := range q.Columns {
		if c == column && i < len(row) {
			return row[i], true
		}
	}
	return "", false
}

func queryRef(queryID, column string) string { return queryRefPrefix + queryID + ":" + column }

// parseQueryRef zerlegt "q:<id>:<spalte>" (Spaltennamen duerfen ":" enthalten).
func parseQueryRef(ref string) (queryID, column string, ok bool) {
	rest, found := strings.CutPrefix(ref, queryRefPrefix)
	if !found {
		return "", "", false
	}
	queryID, column, ok = strings.Cut(rest, ":")
	return queryID, column, ok && queryID != "" && column != ""
}

func isQueryRef(ref string) bool { return strings.HasPrefix(ref, queryRefPrefix) }

// ── Pruefung der Angaben ─────────────────────────────────────

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlStringLit    = regexp.MustCompile(`'(?:[^']|'')*'`)
	opcuaNodeIDRe   = regexp.MustCompile(`^(ns=\d+;|nsu=[^;]+;)?[isgb]=.+$`)
	sqlForbidden    = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE|UPSERT|REPLACE|DROP|ALTER|CREATE|TRUNCATE|RENAME|GRANT|REVOKE|EXEC|EXECUTE|CALL|INTO|ATTACH|DETACH|PRAGMA|VACUUM|LOCK|SET|USE|SHUTDOWN|KILL|BACKUP|RESTORE|DBCC|BULK|OPENROWSET|LOAD_FILE|OUTFILE|DUMPFILE)\b`)
)

// validateSelectSQL: nur lesende Abfragen (SELECT / WITH … SELECT), genau eine
// Anweisung. Zusaetzlich laeuft jede Abfrage in einer Transaktion, die immer
// zurueckgerollt wird.
func validateSelectSQL(q string) error {
	clean := sqlBlockComment.ReplaceAllString(sqlLineComment.ReplaceAllString(q, " "), " ")
	clean = sqlStringLit.ReplaceAllString(clean, "''")
	clean = strings.TrimSpace(clean)
	clean = strings.TrimSpace(strings.TrimSuffix(clean, ";"))
	if clean == "" {
		return errors.New("Bitte eine SQL-Abfrage eingeben")
	}
	upper := strings.ToUpper(clean)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return errors.New("Nur lesende Abfragen sind erlaubt (SELECT oder WITH … SELECT)")
	}
	if strings.Contains(clean, ";") {
		return errors.New("Bitte nur eine Anweisung je Abfrage (kein „;“ in der Mitte)")
	}
	if m := sqlForbidden.FindString(clean); m != "" {
		return fmt.Errorf("„%s“ ist in Abfragen nicht erlaubt – nur lesen", strings.ToUpper(m))
	}
	return nil
}

func parseUint(s string, def, max int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max {
		return 0, fmt.Errorf("Zahl zwischen 0 und %d erwartet", max)
	}
	return n, nil
}

// normalizeQuerySpec prueft und bereinigt die Angaben einer Abfrage.
func normalizeQuerySpec(kind string, in map[string]string) (map[string]string, error) {
	fields := querySpecFields(kind)
	if fields == nil {
		return nil, errors.New("Für diesen Verbindungstyp gibt es keine Abfragen")
	}
	spec := map[string]string{}
	for _, f := range fields {
		spec[f] = strings.TrimSpace(in[f])
	}
	if r := spec["row"]; r != "" && r != "first" && r != "last" {
		return nil, errors.New("Zeile: „first“ oder „last“")
	}
	switch {
	case sqlBrowsableKind(kind):
		spec["sql"] = strings.TrimSpace(in["sql"]) // Zeilenumbrueche behalten
		if err := validateSelectSQL(spec["sql"]); err != nil {
			return nil, err
		}
	case webBrowsableKind(kind):
		if p := spec["path"]; strings.Contains(p, "://") {
			u, err := url.Parse(p)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, errors.New("Pfad: nur http- oder https-Adressen")
			}
		}
	case kind == "modbus":
		switch spec["area"] {
		case "holding", "input", "coil", "discrete":
		case "":
			spec["area"] = "holding"
		default:
			return nil, errors.New("Bereich: holding, input, coil oder discrete")
		}
		if _, err := parseUint(spec["start"], 0, 65535); err != nil {
			return nil, fmt.Errorf("Startadresse: %w", err)
		}
		n, err := parseUint(spec["count"], 1, 100)
		if err != nil || n == 0 {
			return nil, errors.New("Anzahl: 1 bis 100 Werte")
		}
		if spec["type"] == "" {
			spec["type"] = "uint16"
		}
		if spec["area"] == "coil" || spec["area"] == "discrete" {
			spec["type"] = "bool"
		} else if !modbusDataTypeKnown(spec["type"]) {
			return nil, errors.New("Datentyp unbekannt")
		}
	case kind == "opcua":
		var nodes []string
		for _, l := range strings.Split(strings.ReplaceAll(in["nodes"], ",", "\n"), "\n") {
			if l = strings.TrimSpace(l); l == "" {
				continue
			}
			if _, err := ua.ParseNodeID(l); err != nil || !opcuaNodeIDRe.MatchString(l) {
				return nil, fmt.Errorf("Knoten „%s“ ungültig (z. B. ns=2;s=Linie1.Temperatur oder i=2258)", l)
			}
			nodes = append(nodes, l)
		}
		if len(nodes) == 0 || len(nodes) > 100 {
			return nil, errors.New("Bitte 1 bis 100 Knoten angeben, je Zeile einer")
		}
		spec["nodes"] = strings.Join(nodes, "\n")
	}
	return spec, nil
}

func modbusDataTypeKnown(t string) bool {
	for _, o := range modbusDataTypes {
		if o.Value == t {
			return true
		}
	}
	return false
}

// ── Ausfuehren ───────────────────────────────────────────────

// runConnQuery fuehrt eine Abfrage gegen die Verbindung aus.
func runConnQuery(ctx context.Context, kind string, config, spec map[string]string) (queryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	switch {
	case sqlBrowsableKind(kind):
		return runSQLQuery(ctx, kind, config, spec)
	case webBrowsableKind(kind):
		return runWebQuery(ctx, kind, config, spec)
	case kind == "modbus":
		return runModbusQuery(config, spec)
	case kind == "opcua":
		return runOPCUAQuery(ctx, config, spec)
	case kind == "excel" || kind == "csv":
		return runTabularQuery(kind, config, spec)
	}
	return queryResult{}, errors.New("Für diesen Verbindungstyp gibt es keine Abfragen")
}

func runSQLQuery(ctx context.Context, kind string, config, spec map[string]string) (queryResult, error) {
	if err := validateSelectSQL(spec["sql"]); err != nil {
		return queryResult{}, err
	}
	db, _, err := openSQLConnection(kind, config)
	if err != nil {
		return queryResult{}, err
	}
	defer db.Close()
	// immer zurueckrollen – selbst wenn die Pruefung etwas uebersehen haette
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: kind == "mysql"})
	if err != nil {
		return queryResult{}, fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, strings.TrimSuffix(strings.TrimSpace(spec["sql"]), ";"))
	if err != nil {
		return queryResult{}, fmt.Errorf("Abfrage fehlgeschlagen: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return queryResult{}, err
	}
	res := queryResult{Columns: cols}
	for rows.Next() {
		if len(res.Rows) >= queryMaxRows {
			res.Truncated = true
			break
		}
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return queryResult{}, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = sqlValueToString(v)
		}
		res.Rows = append(res.Rows, row)
	}
	return res, rows.Err()
}

// queryTargetURL: Pfad relativ zur Basisadresse oder vollstaendige Adresse.
func queryTargetURL(base, path string) (string, error) {
	if path == "" {
		return base, nil
	}
	b, err := url.Parse(base)
	if err != nil || b.Host == "" {
		return "", errors.New("Basisadresse der Verbindung fehlt oder ist ungültig")
	}
	if strings.Contains(path, "://") {
		return path, nil
	}
	if strings.HasPrefix(path, "?") {
		b.RawQuery = strings.TrimPrefix(path, "?")
		return b.String(), nil
	}
	rel, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("Pfad ungültig: %w", err)
	}
	if !strings.HasPrefix(path, "/") {
		// relativ zum Basispfad ("api" + "status" → "api/status")
		b.Path = strings.TrimSuffix(b.Path, "/") + "/"
	}
	return b.ResolveReference(rel).String(), nil
}

func runWebQuery(ctx context.Context, kind string, config, spec map[string]string) (queryResult, error) {
	req, err := buildImportRequest(kind, config)
	if err != nil {
		return queryResult{}, err
	}
	target, err := queryTargetURL(req.URL.String(), spec["path"])
	if err != nil {
		return queryResult{}, err
	}
	if req.URL, err = url.Parse(target); err != nil {
		return queryResult{}, err
	}
	req.Host = req.URL.Host
	req = req.WithContext(ctx)
	resp, err := (&http.Client{Timeout: webFetchTimeout}).Do(req)
	if err != nil {
		return queryResult{}, fmt.Errorf("Anfrage fehlgeschlagen: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes))
	if err != nil {
		return queryResult{}, fmt.Errorf("Antwort konnte nicht gelesen werden: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return queryResult{}, fmt.Errorf("HTTP %d von %s", resp.StatusCode, target)
	}
	return jsonToQueryResult(body, spec["items_path"])
}

// jsonToQueryResult: Liste von Objekten → je Element eine Zeile, sonst eine
// Zeile mit allen Werten (flache Pfade wie "data.temperatur").
func jsonToQueryResult(body []byte, itemsPath string) (queryResult, error) {
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		text := strings.TrimSpace(string(body))
		if len([]rune(text)) > 2000 {
			text = string([]rune(text)[:2000])
		}
		return queryResult{Columns: []string{"text"}, Rows: [][]string{{text}}}, nil
	}
	if itemsPath = strings.TrimSpace(itemsPath); itemsPath != "" {
		v, ok := jsonAtPath(data, itemsPath)
		if !ok {
			return queryResult{}, fmt.Errorf("Pfad „%s“ nicht in der Antwort gefunden", itemsPath)
		}
		data = v
	}
	var items []interface{}
	if arr, ok := data.([]interface{}); ok {
		items = arr
	} else {
		items = []interface{}{data}
	}
	res := queryResult{}
	colSet := map[string]bool{}
	var flat []map[string]string
	for _, it := range items {
		if len(flat) >= queryMaxRows {
			res.Truncated = true
			break
		}
		m := flattenJSONMap(it)
		if len(m) == 1 {
			if v, ok := m[""]; ok { // Liste aus einfachen Werten
				m = map[string]string{"wert": v}
			}
		}
		flat = append(flat, m)
		for k := range m {
			colSet[k] = true
		}
	}
	for k := range colSet {
		res.Columns = append(res.Columns, k)
	}
	sort.Strings(res.Columns)
	if len(res.Columns) > queryMaxColumns {
		res.Columns = res.Columns[:queryMaxColumns]
		res.Truncated = true
	}
	for _, m := range flat {
		row := make([]string, len(res.Columns))
		for i, c := range res.Columns {
			row[i] = m[c]
		}
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}

var jsonIndexRe = regexp.MustCompile(`^(.*)\[(\d+)\]$`)

// jsonAtPath folgt einem Pfad wie "data.items" oder "result[0].werte".
func jsonAtPath(data interface{}, path string) (interface{}, bool) {
	cur := data
	for _, part := range strings.Split(path, ".") {
		var idx []int
		for {
			m := jsonIndexRe.FindStringSubmatch(part)
			if m == nil {
				break
			}
			n, _ := strconv.Atoi(m[2])
			idx = append([]int{n}, idx...)
			part = m[1]
		}
		if part != "" {
			obj, ok := cur.(map[string]interface{})
			if !ok {
				return nil, false
			}
			if cur, ok = obj[part]; !ok {
				return nil, false
			}
		}
		for _, n := range idx {
			arr, ok := cur.([]interface{})
			if !ok || n >= len(arr) {
				return nil, false
			}
			cur = arr[n]
		}
	}
	return cur, true
}

func runModbusQuery(config, spec map[string]string) (queryResult, error) {
	start, _ := parseUint(spec["start"], 0, 65535)
	count, _ := parseUint(spec["count"], 1, 100)
	area, typ := spec["area"], spec["type"]
	if area == "" {
		area = "holding"
	}
	if area == "coil" || area == "discrete" {
		typ = "bool"
	}
	step := 1
	if area == "holding" || area == "input" {
		step = int(modbusRegisterCount(typ))
	}
	handler, client, err := openModbusClient(config)
	if err != nil {
		return queryResult{}, err
	}
	defer handler.Close()
	res := queryResult{Rows: [][]string{{}}}
	failed := 0
	var firstErr error
	for i := 0; i < count; i++ {
		addr := start + i*step
		if addr > 65535 {
			break
		}
		reading := modbusReading{RegisterType: area, Address: uint16(addr), DataType: typ}
		res.Columns = append(res.Columns, fmt.Sprintf("%s:%d:%s", area, addr, typ))
		v, err := readModbusValue(client, reading)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			v = "Fehler: " + err.Error()
		}
		res.Rows[0] = append(res.Rows[0], v)
	}
	if failed == len(res.Columns) && firstErr != nil {
		return queryResult{}, fmt.Errorf("Lesen fehlgeschlagen: %w", firstErr)
	}
	return res, nil
}

func runOPCUAQuery(ctx context.Context, config, spec map[string]string) (queryResult, error) {
	client, err := openOPCUAClient(ctx, config)
	if err != nil {
		return queryResult{}, err
	}
	defer client.Close(ctx)
	res := queryResult{Rows: [][]string{{}}}
	failed := 0
	var firstErr error
	for _, n := range strings.Split(spec["nodes"], "\n") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		res.Columns = append(res.Columns, n)
		id, err := ua.ParseNodeID(n)
		var val string
		if err == nil {
			var v *ua.Variant
			if v, err = client.Node(id).Value(ctx); err == nil && v != nil {
				val = fmt.Sprintf("%v", v.Value())
			}
		}
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			val = "Fehler: " + err.Error()
		}
		res.Rows[0] = append(res.Rows[0], val)
	}
	if failed == len(res.Columns) && firstErr != nil {
		return queryResult{}, fmt.Errorf("Lesen fehlgeschlagen: %w", firstErr)
	}
	return res, nil
}

func runTabularQuery(kind string, config, spec map[string]string) (queryResult, error) {
	var cols []ExcelColumn
	var rows [][]string
	var err error
	hasHeader := config["has_header"] == "true"
	if kind == "csv" {
		cols, rows, err = readCSVTable(config["source_path"], config["delimiter"], hasHeader, 0)
	} else {
		cols, rows, err = readExcelTable(config["source_path"], firstNonEmpty(spec["sheet"], config["sheet_name"]), hasHeader, 0)
	}
	if err != nil {
		return queryResult{}, err
	}
	res := queryResult{}
	for _, c := range cols {
		res.Columns = append(res.Columns, c.Label)
	}
	// "last": die letzten Zeilen zeigen, damit die massgebliche dabei ist
	if len(rows) > queryMaxRows {
		res.Truncated = true
		if spec["row"] == "last" {
			rows = rows[len(rows)-queryMaxRows:]
		} else {
			rows = rows[:queryMaxRows]
		}
	}
	for _, r := range rows {
		row := make([]string, len(cols))
		copy(row, r)
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}

// ── Datenbank ────────────────────────────────────────────────

type connQuery struct {
	ID              string
	ConnectionID    string
	Name            string
	Spec            map[string]string
	IntervalMinutes int
	Enabled         bool
	Sort            int
	LastRunAt       string
	LastOK          *bool
	LastMessage     string
	LastRows        int
	LastResult      *queryResult
	SpecJSON        string // fuer das Bearbeiten-Formular
}

// Ran / Succeeded: fuer die Anzeige (letzte Ausfuehrung).
func (q connQuery) Ran() bool       { return q.LastOK != nil }
func (q connQuery) Succeeded() bool { return q.LastOK != nil && *q.LastOK }

func (h *Handler) loadConnQueries(ctx context.Context, connectionID string) ([]connQuery, error) {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, connection_id::text, name, spec, interval_minutes, enabled, sort,
		       COALESCE(to_char(last_run_at, 'DD.MM.YYYY HH24:MI:SS'), ''), last_ok, last_message, last_rows, last_result
		FROM connection_queries WHERE connection_id = $1 ORDER BY sort, created_at`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []connQuery
	for rows.Next() {
		var q connQuery
		var spec, result []byte
		if err := rows.Scan(&q.ID, &q.ConnectionID, &q.Name, &spec, &q.IntervalMinutes, &q.Enabled, &q.Sort,
			&q.LastRunAt, &q.LastOK, &q.LastMessage, &q.LastRows, &result); err != nil {
			return nil, err
		}
		q.Spec = map[string]string{}
		_ = json.Unmarshal(spec, &q.Spec)
		q.SpecJSON = string(spec)
		if len(result) > 0 {
			var r queryResult
			if json.Unmarshal(result, &r) == nil {
				q.LastResult = &r
			}
		}
		list = append(list, q)
	}
	return list, rows.Err()
}

// executeConnQuery fuehrt eine Abfrage aus, speichert das Ergebnis und
// uebernimmt die Werte in alle Zuordnungen dieser Abfrage.
func (h *Handler) executeConnQuery(ctx context.Context, kind string, config map[string]string, q connQuery) (queryResult, int, error) {
	res, err := runConnQuery(ctx, kind, config, q.Spec)
	if err != nil {
		_, _ = h.db.Exec(ctx, `UPDATE connection_queries SET last_run_at = NOW(), last_ok = false, last_message = $1 WHERE id = $2`, err.Error(), q.ID)
		return res, 0, err
	}
	msg := fmt.Sprintf("%d Zeile(n), %d Spalte(n)", len(res.Rows), len(res.Columns))
	if res.Truncated {
		msg += " – gekürzt"
	}
	stored, _ := json.Marshal(res)
	_, _ = h.db.Exec(ctx, `UPDATE connection_queries SET last_run_at = NOW(), last_ok = true, last_message = $1, last_rows = $2, last_result = $3 WHERE id = $4`,
		msg, len(res.Rows), stored, q.ID)
	updated := 0
	rows, err := h.db.Query(ctx, `SELECT id::text, source_ref FROM import_mappings WHERE connection_id = $1 AND source_ref LIKE $2`, q.ConnectionID, queryRefPrefix+q.ID+":%")
	if err != nil {
		return res, 0, nil
	}
	type mp struct{ id, col string }
	var list []mp
	for rows.Next() {
		var id, ref string
		if rows.Scan(&id, &ref) == nil {
			if _, col, ok := parseQueryRef(ref); ok {
				list = append(list, mp{id, col})
			}
		}
	}
	rows.Close()
	for _, m := range list {
		v, ok := res.value(q.Spec, m.col)
		if !ok {
			continue
		}
		if _, err := h.db.Exec(ctx, `UPDATE import_mappings SET last_value = $1, last_received_at = NOW() WHERE id = $2`, v, m.id); err == nil {
			updated++
		}
	}
	return res, updated, nil
}

// connectionRow: Typ, Konfiguration, Status einer Verbindung.
func (h *Handler) connectionRow(ctx context.Context, id, direction string) (name, kind string, enabled bool, config map[string]string, err error) {
	var cfg []byte
	err = h.db.QueryRow(ctx, `SELECT name, kind, enabled, config FROM import_export_connections WHERE id = $1 AND direction = $2`, id, direction).
		Scan(&name, &kind, &enabled, &cfg)
	config = map[string]string{}
	_ = json.Unmarshal(cfg, &config)
	return
}

// runAllConnQueries fuehrt alle aktiven Abfragen einer Verbindung aus.
func (h *Handler) runAllConnQueries(ctx context.Context, connectionID string) (ok, failed, updated int) {
	_, kind, _, config, err := h.connectionRow(ctx, connectionID, "import")
	if err != nil {
		return
	}
	qs, _ := h.loadConnQueries(ctx, connectionID)
	for _, q := range qs {
		if !q.Enabled {
			continue
		}
		_, n, err := h.executeConnQuery(ctx, kind, config, q)
		if err != nil {
			failed++
			continue
		}
		ok++
		updated += n
	}
	return
}

// ── Hintergrund-Abruf je Abfrage ─────────────────────────────

func queryPollID(queryID string) string { return "query:" + queryID }

// reconcileQueryPolls startet/stoppt die Intervalle aller Abfragen einer
// Verbindung passend zu ihrem Zustand.
func (h *Handler) reconcileQueryPolls(ctx context.Context, connectionID string) {
	if h.importPoll == nil || h.db == nil {
		return
	}
	_, kind, enabled, config, err := h.connectionRow(ctx, connectionID, "import")
	qs, _ := h.loadConnQueries(ctx, connectionID)
	want := map[string]bool{}
	if err == nil && enabled {
		for _, q := range qs {
			if q.Enabled && q.IntervalMinutes > 0 {
				want[q.ID] = true
			}
		}
	}
	for _, q := range qs {
		pid := queryPollID(q.ID)
		h.importPoll.Stop(pid)
		if !want[q.ID] {
			continue
		}
		q, kind, config := q, kind, config
		h.importPoll.Start(pid, time.Duration(q.IntervalMinutes)*time.Minute, func() {
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, n, err := h.executeConnQuery(cctx, kind, config, q); err != nil {
				log.Warn().Err(err).Str("abfrage", q.ID).Msg("automatische abfrage fehlgeschlagen")
			} else {
				log.Debug().Str("abfrage", q.ID).Int("werte", n).Msg("automatische abfrage")
			}
		})
	}
}

// StartEnabledQueryPolls: beim Serverstart alle Abfrage-Intervalle aufbauen.
func (h *Handler) StartEnabledQueryPolls(ctx context.Context) {
	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT q.connection_id::text FROM connection_queries q
		JOIN import_export_connections c ON c.id = q.connection_id
		WHERE c.enabled AND q.enabled AND q.interval_minutes > 0`)
	if err != nil {
		log.Error().Err(err).Msg("abfrage-intervalle konnten beim start nicht geladen werden")
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		h.reconcileQueryPolls(ctx, id)
	}
}
