package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/scheduler"
)

// ── Rechte ───────────────────────────────────────────────────
//
// import.read/export.read steuern die Sichtbarkeit der Seite und des
// Nav-Eintrags, import.write/export.write das Anlegen/Bearbeiten/Loeschen
// von Verbindungen. import.execute/export.execute sind bereits als
// Berechtigung angelegt (Matrix), werden aber erst scharf geschaltet,
// sobald die jeweiligen Konnektoren echte Daten uebertragen - siehe
// Kommentar in migrations/056_import_export.up.sql.

func (h *Handler) canConnectionRead(r *http.Request, direction string) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), direction+".read")
}

func (h *Handler) canConnectionWrite(r *http.Request, direction string) bool {
	if h.rbac == nil { // nur in Tests mit hasPermHook
		return h.hasPermHook != nil && h.hasPerm(r, direction+".write")
	}
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), direction+".write")
}

// ── Konnektortypen ───────────────────────────────────────────
//
// Je Konnektortyp die zulaessigen Konfigurationsfelder (Whitelist, damit
// nur bekannte Schluessel in der JSONB-Config landen) sowie welche davon
// Checkboxen sind (true/false statt Freitext).

type KindOption struct {
	Value string
	Label string
}

var importKindOptions = []KindOption{
	{"excel", "Excel-Datei"},
	{"csv", "CSV-Datei"},
	{"sqlite", "SQLite"},
	{"mysql", "MySQL"},
	{"mssql", "MSSQL"},
	{"mqtt", "MQTT"},
	{"web", "Web"},
	{"rest_api", "REST-API"},
	{"modbus", "Modbus TCP"},
	{"opcua", "OPC UA"},
}

var exportKindOptions = []KindOption{
	{"pdf", "PDF"},
	{"excel", "Excel"},
	{"csv", "CSV"},
	{"sqlite", "SQLite"},
	{"mysql", "MySQL"},
	{"mssql", "MSSQL"},
	{"opcua", "OPC UA"},
	{"modbus", "Modbus TCP"},
}

var importKindFields = map[string][]string{
	"excel":  {"source_path", "sheet_name", "has_header"},
	"csv":    {"source_path", "delimiter", "has_header"},
	"sqlite": {"file_path"},
	"mysql":  {"host", "port", "database", "username", "password", "use_tls"},
	"mssql":  {"host", "port", "database", "instance_name", "username", "password", "use_tls", "route", "vpn_poll_minutes", "vpn_timeout_seconds", "allow_write", "tls_fingerprint"},
	"mqtt": {
		"broker_mode",
		// externer Broker
		"ext_broker_host", "ext_broker_port", "ext_username", "ext_password", "ext_use_tls", "ext_client_id", "ext_topic_filter", "ext_qos",
		// integrierter Broker (voller Einstellungsumfang)
		"int_listen_port", "int_websocket_port", "int_allow_anonymous", "int_broker_username", "int_broker_password",
		"int_max_clients", "int_persistence_enabled", "int_retain_enabled", "int_tls_cert_path", "int_tls_key_path",
		"int_topic_filter", "int_qos",
	},
	"web":      {"url", "method", "poll_interval_minutes"},
	"rest_api": {"base_url", "auth_method", "username", "password", "bearer_token", "api_key_header", "api_key_value", "poll_interval_minutes"},
	"modbus":   {"host", "port", "unit_id"},
	"opcua":    {"endpoint_url", "auth_mode", "username", "password"},
}

var exportKindFields = map[string][]string{
	"pdf":    {"template_id", "destination_path", "schedule_cron"},
	"excel":  {"template_id", "destination_path", "schedule_cron"},
	"csv":    {"destination_path", "delimiter", "schedule_cron"},
	"sqlite": {"file_path", "table_name", "schedule_cron"},
	"mysql":  {"host", "port", "database", "username", "password", "use_tls", "table_name", "schedule_cron"},
	"mssql":  {"host", "port", "database", "instance_name", "username", "password", "use_tls", "route", "vpn_poll_minutes", "vpn_timeout_seconds", "allow_write", "tls_fingerprint", "table_name", "schedule_cron"},
	"opcua":  {"endpoint_url", "auth_mode", "username", "password", "schedule_cron"},
	"modbus": {"host", "port", "unit_id", "schedule_cron"},
}

var booleanConfigFields = map[string]bool{
	"has_header": true, "use_tls": true, "ext_use_tls": true,
	"int_allow_anonymous": true, "int_persistence_enabled": true, "int_retain_enabled": true,
	"allow_write": true,
}

func connectionKindOptions(direction string) []KindOption {
	if direction == "export" {
		return exportKindOptions
	}
	return importKindOptions
}

func connectionKindFields(direction, kind string) ([]string, bool) {
	if direction == "export" {
		f, ok := exportKindFields[kind]
		return f, ok
	}
	f, ok := importKindFields[kind]
	return f, ok
}

func connectionKindLabel(direction, kind string) string {
	for _, opt := range connectionKindOptions(direction) {
		if opt.Value == kind {
			return opt.Label
		}
	}
	return kind
}

// ── Seiten-Daten ─────────────────────────────────────────────

type ConnectionView struct {
	ID                 string
	Kind               string
	KindLabel          string
	Name               string
	Enabled            bool
	ConfigJSON         string
	IsIntegratedBroker bool
	CreatedBy          string
	CreatedAt          string
}

type ConnectionsPageData struct {
	BaseData
	Direction      string
	Connections    []ConnectionView
	Kinds          []KindOption
	CanWrite       bool
	CanExecute     bool
	PDFTemplates   []ExportTemplateView
	ExcelTemplates []ExportTemplateView
	Tunnels        []connTunnelOption // ausgehende VPN-Tunnel für „Weg“ (leer ohne VPN)
	Notice         string
}

func (h *Handler) importExportConnections(ctx context.Context, direction string) ([]ConnectionView, error) {
	rows, err := h.db.Query(ctx, `
		SELECT c.id::text, c.kind, c.name, c.enabled, c.config,
		       COALESCE(u.first_name || ' ' || u.last_name, ''), to_char(c.created_at, 'DD.MM.YYYY HH24:MI')
		FROM import_export_connections c
		LEFT JOIN users u ON u.id = c.created_by
		WHERE c.direction = $1
		ORDER BY c.kind, c.name`, direction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]ConnectionView, 0)
	for rows.Next() {
		var v ConnectionView
		var configBytes []byte
		if err := rows.Scan(&v.ID, &v.Kind, &v.Name, &v.Enabled, &configBytes, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.KindLabel = connectionKindLabel(direction, v.Kind)
		// Kennwörter und verschlüsselte Felder gehen nie in den Browser
		var config map[string]string
		if json.Unmarshal(configBytes, &config) == nil {
			pub, _ := json.Marshal(publicConnConfig(config))
			v.ConfigJSON = string(pub)
			if v.Kind == "mqtt" {
				v.IsIntegratedBroker = config["broker_mode"] == "integrated"
			}
		} else {
			v.ConfigJSON = "{}"
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ── Seiten-Handler ───────────────────────────────────────────

func (h *Handler) ImportPage(w http.ResponseWriter, r *http.Request) {
	h.connectionsPage(w, r, "import")
}
func (h *Handler) ExportPage(w http.ResponseWriter, r *http.Request) {
	h.connectionsPage(w, r, "export")
}

func (h *Handler) connectionsPage(w http.ResponseWriter, r *http.Request, direction string) {
	if !h.canConnectionRead(r, direction) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	conns, err := h.importExportConnections(r.Context(), direction)
	if err != nil {
		http.Error(w, "Verbindungen konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	page, title, ctxTitle := "import", "Import", "Datenimport"
	if direction == "export" {
		page, title, ctxTitle = "export", "Export", "Datenexport"
	}
	data := ConnectionsPageData{
		BaseData:    h.baseData(r, page, title, ctxTitle),
		Direction:   direction,
		Connections: conns,
		Kinds:       connectionKindOptions(direction),
		CanWrite:    h.canConnectionWrite(r, direction),
		CanExecute:  h.canConnectionExecute(r, direction),
		Notice:      r.URL.Query().Get("notice"),
	}
	if data.CanWrite {
		data.Tunnels = h.connTunnelOptions(r.Context())
	}
	if direction == "export" {
		if tpls, err := h.exportTemplates(r.Context(), "pdf"); err == nil {
			data.PDFTemplates = tpls
		}
		if tpls, err := h.exportTemplates(r.Context(), "excel"); err == nil {
			data.ExcelTemplates = tpls
		}
	}
	h.render(w, page, data)
}

// ── Schreibende Aktionen ─────────────────────────────────────

func (h *Handler) ImportConnectionCreateWeb(w http.ResponseWriter, r *http.Request) {
	h.createConnection(w, r, "import")
}
func (h *Handler) ExportConnectionCreateWeb(w http.ResponseWriter, r *http.Request) {
	h.createConnection(w, r, "export")
}
func (h *Handler) ImportConnectionEditWeb(w http.ResponseWriter, r *http.Request) {
	h.editConnection(w, r, "import")
}
func (h *Handler) ExportConnectionEditWeb(w http.ResponseWriter, r *http.Request) {
	h.editConnection(w, r, "export")
}
func (h *Handler) ImportConnectionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	h.deleteConnection(w, r, "import")
}
func (h *Handler) ExportConnectionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	h.deleteConnection(w, r, "export")
}
func (h *Handler) ImportConnectionToggleWeb(w http.ResponseWriter, r *http.Request) {
	h.toggleConnection(w, r, "import")
}
func (h *Handler) ExportConnectionToggleWeb(w http.ResponseWriter, r *http.Request) {
	h.toggleConnection(w, r, "export")
}

func redirectBase(direction string) string {
	if direction == "export" {
		return "/export"
	}
	return "/import"
}

func buildConnectionConfig(r *http.Request, kind string, fields []string) map[string]string {
	config := make(map[string]string, len(fields))
	for _, f := range fields {
		formKey := "f_" + kind + "_" + f
		if booleanConfigFields[f] {
			if r.FormValue(formKey) == "on" {
				config[f] = "true"
			} else {
				config[f] = "false"
			}
			continue
		}
		config[f] = strings.TrimSpace(r.FormValue(formKey))
	}
	if kind == "mssql" {
		normalizeMssqlRouteFields(config)
	}
	return config
}

// normalizeMssqlRouteFields: Weg, Zeiten und Fingerabdruck in feste Form bringen.
// Fehlt das Feld oder ist es ungültig, gilt die Vorgabe (direkt, 5 Min., 60 s,
// kein Fingerabdruck).
func normalizeMssqlRouteFields(config map[string]string) {
	if r := parseConnRoute(config); r.ViaVPN {
		config["route"] = "vpn:" + r.PeerID
	} else {
		config["route"] = "direct"
	}
	config["vpn_poll_minutes"] = strconv.Itoa(clampConnInt(config["vpn_poll_minutes"], connPollVPNDefault, connPollVPNMin, connPollVPNMax))
	config["vpn_timeout_seconds"] = strconv.Itoa(clampConnInt(config["vpn_timeout_seconds"], int(connTimeoutVPN/time.Second), connTimeoutMin, connTimeoutMax))
	config["tls_fingerprint"] = normalizeFingerprint(config["tls_fingerprint"])
}

func (h *Handler) createConnection(w http.ResponseWriter, r *http.Request, direction string) {
	base := redirectBase(direction)
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	fields, ok := connectionKindFields(direction, kind)
	if !ok {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Unbekannter Verbindungstyp"), http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if l := len([]rune(name)); l < 2 || l > 150 {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Name muss 2 bis 150 Zeichen lang sein"), http.StatusSeeOther)
		return
	}
	config := buildConnectionConfig(r, kind, fields)
	if err := scheduler.ValidateCronSpec(config["schedule_cron"]); err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	sealed, err := sealConnConfig(h.connKey(), config)
	if err != nil {
		http.Error(w, "Kennwort konnte nicht verschlüsselt werden", http.StatusInternalServerError)
		return
	}
	configJSON, err := json.Marshal(sealed)
	if err != nil {
		http.Error(w, "Konfiguration ungültig", http.StatusInternalServerError)
		return
	}
	u := getUser(r)
	var id string
	err = h.db.QueryRow(r.Context(), `
		INSERT INTO import_export_connections (direction, kind, name, config, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, direction, kind, name, configJSON, u.ID).Scan(&id)
	if err != nil {
		http.Error(w, "Verbindung konnte nicht angelegt werden", http.StatusInternalServerError)
		return
	}
	h.reconcileMqttBroker(id, direction, kind, true, config)
	h.reconcileExportSchedule(id, direction, true, config)
	h.reconcileImportPoll(id, direction, kind, true, config)
	// gleich auf die eigene Seite der Verbindung: Pruefen, Abfragen, Zuordnen
	next := "checks"
	if direction == "import" && queryKindSupported(kind) {
		next = "queries"
	}
	http.Redirect(w, r, connectionBackPath(direction, id, next, "Verbindung angelegt – jetzt prüfen und Abfragen anlegen", ""), http.StatusSeeOther)
}

func (h *Handler) editConnection(w http.ResponseWriter, r *http.Request, direction string) {
	base := redirectBase(direction)
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	var kind string
	var enabled bool
	var storedBytes []byte
	if err := h.db.QueryRow(r.Context(),
		`SELECT kind, enabled, config FROM import_export_connections WHERE id=$1 AND direction=$2`, id, direction).Scan(&kind, &enabled, &storedBytes); err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	fields, ok := connectionKindFields(direction, kind)
	if !ok {
		http.Error(w, "Unbekannter Verbindungstyp", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if l := len([]rune(name)); l < 2 || l > 150 {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape("Name muss 2 bis 150 Zeichen lang sein"), http.StatusSeeOther)
		return
	}
	config := buildConnectionConfig(r, kind, fields)
	if err := scheduler.ValidateCronSpec(config["schedule_cron"]); err != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	// Leeres Geheimfeld im Formular = gespeicherten Wert behalten
	stored := map[string]string{}
	_ = json.Unmarshal(storedBytes, &stored)
	config = keepStoredSecrets(config, stored, fields)
	sealed, err := sealConnConfig(h.connKey(), config)
	if err != nil {
		http.Error(w, "Kennwort konnte nicht verschlüsselt werden", http.StatusInternalServerError)
		return
	}
	configJSON, err := json.Marshal(sealed)
	if err != nil {
		http.Error(w, "Konfiguration ungültig", http.StatusInternalServerError)
		return
	}
	_, err = h.db.Exec(r.Context(), `
		UPDATE import_export_connections SET name=$1, config=$2, updated_at=NOW()
		WHERE id=$3 AND direction=$4`, name, configJSON, id, direction)
	if err != nil {
		http.Error(w, "Verbindung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	// Dienste brauchen die Zugangsdaten im Klartext (nur im Speicher)
	plain, perr := openConnConfig(h.connKey(), sealed)
	if perr != nil {
		http.Redirect(w, r, base+"?notice="+url.QueryEscape(perr.Error()), http.StatusSeeOther)
		return
	}
	h.reconcileMqttBroker(id, direction, kind, enabled, plain)
	h.reconcileExportSchedule(id, direction, enabled, plain)
	h.reconcileImportPoll(id, direction, kind, enabled, plain)
	if direction == "import" {
		h.reconcileQueryPolls(r.Context(), id)
	}
	if r.FormValue("return_to") == "detail" {
		http.Redirect(w, r, connectionBackPath(direction, id, "overview", "Einstellungen gespeichert", ""), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, base+"?notice="+url.QueryEscape("Verbindung gespeichert"), http.StatusSeeOther)
}

func (h *Handler) deleteConnection(w http.ResponseWriter, r *http.Request, direction string) {
	base := redirectBase(direction)
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if qs, err := h.loadConnQueries(r.Context(), id); err == nil {
		for _, q := range qs {
			h.importPoll.Stop(queryPollID(q.ID))
		}
	}
	if _, err := h.db.Exec(r.Context(),
		`DELETE FROM import_export_connections WHERE id=$1 AND direction=$2`, id, direction); err != nil {
		http.Error(w, "Verbindung konnte nicht gelöscht werden", http.StatusInternalServerError)
		return
	}
	h.mqttImport.Stop(id)
	h.mqttBrokers.Stop(id)
	h.exportCron.Unschedule(id)
	h.importPoll.Stop(id)
	http.Redirect(w, r, base+"?notice="+url.QueryEscape("Verbindung gelöscht"), http.StatusSeeOther)
}

func (h *Handler) toggleConnection(w http.ResponseWriter, r *http.Request, direction string) {
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	var kind string
	var enabled bool
	var configBytes []byte
	if err := h.db.QueryRow(r.Context(), `
		UPDATE import_export_connections SET enabled = NOT enabled, updated_at=NOW()
		WHERE id=$1 AND direction=$2 RETURNING kind, enabled, config`, id, direction).
		Scan(&kind, &enabled, &configBytes); err != nil {
		http.Error(w, "Verbindung konnte nicht geändert werden", http.StatusInternalServerError)
		return
	}
	config, err := h.openConnJSON(configBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	h.reconcileMqttBroker(id, direction, kind, enabled, config)
	h.reconcileExportSchedule(id, direction, enabled, config)
	h.reconcileImportPoll(id, direction, kind, enabled, config)
	if direction == "import" {
		h.reconcileQueryPolls(r.Context(), id)
	}
	w.WriteHeader(http.StatusNoContent)
}
