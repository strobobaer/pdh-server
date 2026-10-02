package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Eigene Seite je Import-/Export-Verbindung: Uebersicht, Pruefungen,
// Abfragen (Import), Zuordnungen, Vorlagen und die passenden Werkzeuge.

var connFieldLabels = map[string]string{
	"source_path": "Quellpfad", "sheet_name": "Tabellenblatt", "has_header": "Erste Zeile ist Überschrift", "delimiter": "Trennzeichen",
	"file_path": "Datenbankdatei", "host": "Host", "port": "Port", "database": "Datenbank", "instance_name": "Instanz",
	"username": "Benutzer", "password": "Passwort", "use_tls": "TLS",
	"broker_mode": "Broker", "ext_broker_host": "Broker-Host", "ext_broker_port": "Broker-Port", "ext_username": "Benutzer",
	"ext_password": "Passwort", "ext_use_tls": "TLS", "ext_client_id": "Client-ID", "ext_topic_filter": "Topic-Filter", "ext_qos": "QoS",
	"int_listen_port": "Port (integriert)", "int_websocket_port": "WebSocket-Port", "int_allow_anonymous": "Anonym erlaubt",
	"int_broker_username": "Broker-Benutzer", "int_broker_password": "Broker-Passwort", "int_max_clients": "Max. Clients",
	"int_persistence_enabled": "Speichern", "int_retain_enabled": "Retain", "int_tls_cert_path": "TLS-Zertifikat",
	"int_tls_key_path": "TLS-Schlüssel", "int_topic_filter": "Topic-Filter", "int_qos": "QoS",
	"url": "URL", "method": "Methode", "poll_interval_minutes": "Abruf-Intervall (Min.)",
	"base_url": "Basis-URL", "auth_method": "Anmeldung", "bearer_token": "Bearer-Token", "api_key_header": "API-Key-Header", "api_key_value": "API-Key",
	"unit_id": "Unit-ID", "endpoint_url": "Endpunkt", "auth_mode": "Anmeldung",
	"template_id": "Vorlage", "destination_path": "Zielpfad", "schedule_cron": "Zeitplan (Cron)", "table_name": "Tabelle",
}

var connSecretFields = map[string]bool{
	"password": true, "ext_password": true, "int_broker_password": true, "bearer_token": true, "api_key_value": true,
}

type connConfigRow struct{ Label, Value string }

type connTool struct{ Href, Icon, Label, Help string }

type queryFormField struct {
	Key, Label, Type, Placeholder, Help string
	Options                             []KindOption
}

// queryFormFields: Eingabefelder einer Abfrage je Verbindungstyp.
func queryFormFields(kind string) []queryFormField {
	rowOpt := []KindOption{{"first", "Erste Zeile"}, {"last", "Letzte Zeile"}}
	switch {
	case sqlBrowsableKind(kind):
		return []queryFormField{
			{Key: "sql", Label: "SQL-Abfrage (nur SELECT)", Type: "textarea", Placeholder: "SELECT …", Help: "Nur lesend. Läuft in einer Transaktion, die immer zurückgerollt wird. Höchstens 200 Zeilen."},
			{Key: "row", Label: "Wert für Zuordnungen aus", Type: "select", Options: rowOpt},
		}
	case webBrowsableKind(kind):
		return []queryFormField{
			{Key: "path", Label: "Pfad oder Adresse", Placeholder: "/status", Help: "Relativ zur Adresse der Verbindung, z. B. /api/v1/maschinen/7 – Anmeldung wird übernommen."},
			{Key: "items_path", Label: "Pfad zur Liste (optional)", Placeholder: "data.items", Help: "Zeigt die Antwort eine Liste, wird jeder Eintrag eine Zeile."},
			{Key: "row", Label: "Wert für Zuordnungen aus", Type: "select", Options: rowOpt},
		}
	case kind == "modbus":
		return []queryFormField{
			{Key: "area", Label: "Bereich", Type: "select", Options: modbusRegisterTypes},
			{Key: "start", Label: "Startadresse", Type: "number", Placeholder: "0", Help: "Adresse ab 0 (nicht 40001)."},
			{Key: "count", Label: "Anzahl Werte", Type: "number", Placeholder: "10"},
			{Key: "type", Label: "Datentyp", Type: "select", Options: modbusDataTypes, Help: "32-Bit-Typen belegen je 2 Register."},
		}
	case kind == "opcua":
		return []queryFormField{
			{Key: "nodes", Label: "Knoten (je Zeile einer)", Type: "textarea", Placeholder: "ns=2;s=Linie1.Temperatur", Help: "Knoten-IDs findest du unter Werkzeuge → Namespace durchsuchen."},
		}
	case kind == "excel":
		return []queryFormField{
			{Key: "sheet", Label: "Tabellenblatt (optional)", Placeholder: "leer = wie in der Verbindung"},
			{Key: "row", Label: "Wert für Zuordnungen aus", Type: "select", Options: []KindOption{{"last", "Letzte Zeile"}, {"first", "Erste Zeile"}}},
		}
	case kind == "csv":
		return []queryFormField{
			{Key: "row", Label: "Wert für Zuordnungen aus", Type: "select", Options: []KindOption{{"last", "Letzte Zeile"}, {"first", "Erste Zeile"}}},
		}
	}
	return nil
}

type connMappingRow struct {
	ImportMappingView
	QueryName string
	Column    string
}

type ConnectionDetailData struct {
	BaseData
	Direction, ID, Name, Kind, KindLabel string
	Enabled                              bool
	CanWrite, CanExecute                 bool
	Tab, Notice, Err                     string
	Config                               []connConfigRow
	Check                                *checkReport
	CheckAt                              string
	QueriesSupported                     bool
	Queries                              []connQuery
	QueryFields                          []queryFormField
	OpenQuery                            string
	Builtins                             []builtinQueryTemplate
	OwnTemplates                         []ownTemplateView
	Mappings                             []connMappingRow
	ExportMappings                       []ExportMappingView
	Tools                                []connTool
	Background                           []connConfigRow
}

func connectionTools(direction, kind, id string, config map[string]string) []connTool {
	base := "/" + direction + "/connections/" + id + "/"
	var t []connTool
	if direction == "export" {
		t = append(t, connTool{base + "preview", "ti-table-export", "Vorschau & jetzt exportieren", "Felder auswählen, Werte prüfen, Export starten"})
		if kind == "pdf" || kind == "excel" {
			t = append(t, connTool{"/export/templates", "ti-template", "Exportvorlagen", "Kopf, Logo und Layout für PDF/Excel"})
		}
		return append(t, connTool{"/export/mappings", "ti-list-details", "Alle Export-Felder", "Übersicht über alle Verbindungen"})
	}
	switch {
	case kind == "mqtt":
		t = append(t, connTool{base + "sniffer", "ti-antenna", "Sniffer", "Live-Nachrichten ansehen und Topics zuordnen"})
		if config["broker_mode"] == "integrated" {
			t = append(t, connTool{base + "broker", "ti-server-2", "Broker-Status", "Clients und Aktivität des integrierten Brokers"})
		}
	case kind == "excel" || kind == "csv":
		t = append(t, connTool{base + "preview", "ti-table", "Vorschau", "Spalten ansehen und direkt zuordnen"})
	case sqlBrowsableKind(kind):
		t = append(t, connTool{base + "browse", "ti-database-search", "Datenbank durchsuchen", "Tabellen und Spalten ansehen"})
	case webBrowsableKind(kind):
		t = append(t, connTool{base + "response", "ti-api", "Antwort ansehen", "Rohantwort der Adresse, Felder direkt zuordnen"})
	case kind == "modbus":
		t = append(t, connTool{base + "read", "ti-plug", "Register lesen", "Einzelne Register testen"})
	case kind == "opcua":
		t = append(t, connTool{base + "nodes", "ti-sitemap", "Namespace durchsuchen", "Knoten-IDs finden"})
	}
	return append(t, connTool{"/import/mappings", "ti-list-details", "Alle Zuordnungen", "Übersicht über alle Verbindungen"})
}

func connectionBackPath(direction, id, tab, notice, errMsg string) string {
	p := "/" + direction + "/connections/" + id + "?tab=" + url.QueryEscape(tab)
	if notice != "" {
		p += "&notice=" + url.QueryEscape(notice)
	}
	if errMsg != "" {
		p += "&err=" + url.QueryEscape(errMsg)
	}
	return p
}

func (h *Handler) ImportConnectionPage(w http.ResponseWriter, r *http.Request) {
	h.connectionDetailPage(w, r, "import")
}
func (h *Handler) ExportConnectionPage(w http.ResponseWriter, r *http.Request) {
	h.connectionDetailPage(w, r, "export")
}

func (h *Handler) connectionDetailPage(w http.ResponseWriter, r *http.Request, direction string) {
	if !h.canConnectionRead(r, direction) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	name, kind, enabled, config, err := h.connectionRow(ctx, id, direction)
	if err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	page := direction
	d := ConnectionDetailData{
		BaseData:  h.baseData(r, page, name, "Verbindung"),
		Direction: direction, ID: id, Name: name, Kind: kind, KindLabel: connectionKindLabel(direction, kind), Enabled: enabled,
		CanWrite: h.canConnectionWrite(r, direction), CanExecute: h.canConnectionExecute(r, direction),
		Tab: r.URL.Query().Get("tab"), Notice: r.URL.Query().Get("notice"), Err: r.URL.Query().Get("err"),
		OpenQuery: r.URL.Query().Get("q"),
	}
	if d.Tab == "" {
		d.Tab = "overview"
	}
	fields, _ := connectionKindFields(direction, kind)
	for _, f := range fields {
		v := strings.TrimSpace(config[f])
		if v == "" {
			continue
		}
		if kind == "mqtt" && ((config["broker_mode"] == "integrated" && strings.HasPrefix(f, "ext_")) || (config["broker_mode"] != "integrated" && strings.HasPrefix(f, "int_"))) {
			continue
		}
		switch {
		case connSecretFields[f]:
			v = "•••••• (gesetzt)"
		case booleanConfigFields[f]:
			v = map[string]string{"true": "ja", "false": "nein"}[v]
		case f == "template_id":
			tpl := h.resolveExportTemplate(ctx, kind, v)
			if tpl.Name != "" {
				v = tpl.Name
			}
		}
		label := connFieldLabels[f]
		if label == "" {
			label = f
		}
		d.Config = append(d.Config, connConfigRow{label, v})
	}
	var lastCheck []byte
	_ = h.db.QueryRow(ctx, `SELECT last_check, COALESCE(to_char(last_check_at, 'DD.MM.YYYY HH24:MI'), '') FROM import_export_connections WHERE id = $1`, id).Scan(&lastCheck, &d.CheckAt)
	if len(lastCheck) > 0 {
		var rep checkReport
		if json.Unmarshal(lastCheck, &rep) == nil {
			d.Check = &rep
		}
	}
	d.Tools = connectionTools(direction, kind, id, config)
	if direction == "import" {
		d.QueriesSupported = queryKindSupported(kind)
		d.Queries, _ = h.loadConnQueries(ctx, id)
		d.QueryFields = queryFormFields(kind)
		d.Builtins = builtinQueryTemplates(kind)
		qnames := map[string]string{}
		for _, q := range d.Queries {
			qnames[q.ID] = q.Name
		}
		if ms, err := h.importMappings(ctx, id); err == nil {
			for _, m := range ms {
				row := connMappingRow{ImportMappingView: m}
				if qid, col, ok := parseQueryRef(m.SourceRef); ok {
					row.QueryName, row.Column = qnames[qid], col
					if row.QueryName == "" {
						row.QueryName = "(gelöschte Abfrage)"
					}
				}
				d.Mappings = append(d.Mappings, row)
			}
			sort.SliceStable(d.Mappings, func(i, j int) bool { return d.Mappings[i].QueryName < d.Mappings[j].QueryName })
		}
		if m, _ := strconv.Atoi(config["poll_interval_minutes"]); m > 0 && webBrowsableKind(kind) {
			st := "läuft nicht"
			if h.importPoll != nil && h.importPoll.Running(id) {
				st = "läuft"
			}
			d.Background = append(d.Background, connConfigRow{"Abruf der Verbindung", "alle " + strconv.Itoa(m) + " Min. – " + st})
		}
		for _, q := range d.Queries {
			if q.IntervalMinutes > 0 && q.Enabled {
				st := "läuft nicht"
				if h.importPoll != nil && h.importPoll.Running(queryPollID(q.ID)) {
					st = "läuft"
				}
				d.Background = append(d.Background, connConfigRow{"Abfrage „" + q.Name + "“", "alle " + strconv.Itoa(q.IntervalMinutes) + " Min. – " + st})
			}
		}
		if kind == "mqtt" && h.mqttImport != nil {
			st := "kein Abonnement"
			if h.mqttImport.IsRunning(id) {
				st = "abonniert"
			}
			d.Background = append(d.Background, connConfigRow{"MQTT-Empfang", st})
		}
	} else {
		d.ExportMappings, _ = h.exportMappings(ctx, id)
		if h.exportCron != nil {
			if next, ok := h.exportCron.Next(id); ok {
				d.Background = append(d.Background, connConfigRow{"Nächster Export", next.Format("02.01.2006 15:04")})
			}
		}
	}
	d.OwnTemplates, _ = h.ownTemplates(ctx, direction, kind)
	h.render(w, "connection_detail", d)
}

func (h *Handler) canRunConnection(r *http.Request, direction string) bool {
	return h.canConnectionWrite(r, direction) || h.canConnectionExecute(r, direction)
}

// ── Pruefungen ───────────────────────────────────────────────

func (h *Handler) ImportConnectionChecksWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionChecksWeb(w, r, "import")
}
func (h *Handler) ExportConnectionChecksWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionChecksWeb(w, r, "export")
}

func (h *Handler) connectionChecksWeb(w http.ResponseWriter, r *http.Request, direction string) {
	id := chi.URLParam(r, "id")
	if !h.canRunConnection(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	rep, err := h.runConnectionChecks(ctx, id, direction)
	if err != nil {
		http.Redirect(w, r, connectionBackPath(direction, id, "checks", "", err.Error()), http.StatusSeeOther)
		return
	}
	msg := "Prüfung abgeschlossen: " + strconv.Itoa(rep.OK) + " in Ordnung"
	if rep.Warn > 0 {
		msg += ", " + strconv.Itoa(rep.Warn) + " Hinweis(e)"
	}
	if rep.Fail > 0 {
		msg += ", " + strconv.Itoa(rep.Fail) + " Fehler"
	}
	http.Redirect(w, r, connectionBackPath(direction, id, "checks", msg, ""), http.StatusSeeOther)
}

// ── Abfragen ─────────────────────────────────────────────────

// ConnectionQuerySaveWeb: POST /import/connections/{id}/queries – anlegen
// oder (mit query_id) aendern.
func (h *Handler) ConnectionQuerySaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_, kind, _, _, err := h.connectionRow(ctx, id, "import")
	if err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	back := func(notice, errMsg, qid string) {
		p := connectionBackPath("import", id, "queries", notice, errMsg)
		if qid != "" {
			p += "&q=" + url.QueryEscape(qid)
		}
		http.Redirect(w, r, p, http.StatusSeeOther)
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if l := len([]rune(name)); l < 2 || l > 150 {
		back("", "Name der Abfrage: 2 bis 150 Zeichen", "")
		return
	}
	in := map[string]string{}
	for _, f := range querySpecFields(kind) {
		in[f] = r.FormValue("s_" + f)
	}
	spec, err := normalizeQuerySpec(kind, in)
	if err != nil {
		back("", err.Error(), "")
		return
	}
	interval, err := parseUint(r.FormValue("interval_minutes"), 0, 1440)
	if err != nil {
		back("", "Intervall: 0 bis 1440 Minuten", "")
		return
	}
	enabled := r.FormValue("enabled") == "on"
	b, _ := json.Marshal(spec)
	qid := strings.TrimSpace(r.FormValue("query_id"))
	if qid != "" {
		tag, err := h.db.Exec(ctx, `UPDATE connection_queries SET name = $1, spec = $2, interval_minutes = $3, enabled = $4, updated_at = NOW()
			WHERE id::text = $5 AND connection_id = $6`, name, b, interval, enabled, qid, id)
		if err != nil || tag.RowsAffected() == 0 {
			back("", "Abfrage konnte nicht gespeichert werden", "")
			return
		}
	} else if err := h.db.QueryRow(ctx, `
		INSERT INTO connection_queries (connection_id, name, spec, interval_minutes, enabled, sort, created_by)
		VALUES ($1, $2, $3, $4, $5, (SELECT COALESCE(MAX(sort), 0) + 1 FROM connection_queries WHERE connection_id = $1), $6) RETURNING id::text`,
		id, name, b, interval, enabled, nullID(getUser(r).ID)).Scan(&qid); err != nil {
		back("", "Abfrage konnte nicht angelegt werden", "")
		return
	}
	h.reconcileQueryPolls(ctx, id)
	// gleich ausprobieren, damit Ergebnis und Spalten zum Zuordnen da sind
	if r.FormValue("run") == "1" && h.canRunConnection(r, "import") {
		qs, _ := h.loadConnQueries(ctx, id)
		_, _, _, config, _ := h.connectionRow(ctx, id, "import")
		for _, q := range qs {
			if q.ID == qid {
				if _, _, err := h.executeConnQuery(ctx, kind, config, q); err != nil {
					back("Abfrage gespeichert", "Ausführen fehlgeschlagen: "+err.Error(), qid)
					return
				}
			}
		}
		back("Abfrage gespeichert und ausgeführt", "", qid)
		return
	}
	back("Abfrage gespeichert", "", qid)
}

func (h *Handler) ConnectionQueryDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id, qid := chi.URLParam(r, "id"), chi.URLParam(r, "qid")
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if h.importPoll != nil {
		h.importPoll.Stop(queryPollID(qid))
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM import_mappings WHERE connection_id = $1 AND source_ref LIKE $2`, id, queryRefPrefix+qid+":%").Scan(&n)
	if r.FormValue("with_mappings") == "1" {
		_, _ = h.db.Exec(ctx, `DELETE FROM import_mappings WHERE connection_id = $1 AND source_ref LIKE $2`, id, queryRefPrefix+qid+":%")
		n = 0
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM connection_queries WHERE id::text = $1 AND connection_id = $2`, qid, id); err != nil {
		http.Redirect(w, r, connectionBackPath("import", id, "queries", "", "Abfrage konnte nicht gelöscht werden"), http.StatusSeeOther)
		return
	}
	msg := "Abfrage gelöscht"
	if n > 0 {
		msg += " – " + strconv.Itoa(n) + " Zuordnung(en) zeigen jetzt ins Leere, bitte unter Zuordnungen löschen"
	}
	http.Redirect(w, r, connectionBackPath("import", id, "queries", msg, ""), http.StatusSeeOther)
}

func (h *Handler) ConnectionQueryRunWeb(w http.ResponseWriter, r *http.Request) {
	id, qid := chi.URLParam(r, "id"), chi.URLParam(r, "qid")
	if !h.canRunConnection(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_, kind, _, config, err := h.connectionRow(ctx, id, "import")
	if err != nil {
		http.Error(w, "Verbindung nicht gefunden", http.StatusNotFound)
		return
	}
	qs, _ := h.loadConnQueries(ctx, id)
	for _, q := range qs {
		if q.ID != qid {
			continue
		}
		res, n, err := h.executeConnQuery(ctx, kind, config, q)
		p := ""
		if err != nil {
			p = connectionBackPath("import", id, "queries", "", "„"+q.Name+"“: "+err.Error())
		} else {
			p = connectionBackPath("import", id, "queries", "„"+q.Name+"“: "+strconv.Itoa(len(res.Rows))+" Zeile(n), "+strconv.Itoa(n)+" Zuordnung(en) aktualisiert", "")
		}
		http.Redirect(w, r, p+"&q="+url.QueryEscape(qid), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, connectionBackPath("import", id, "queries", "", "Abfrage nicht gefunden"), http.StatusSeeOther)
}

func (h *Handler) ConnectionQueriesRunAllWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canRunConnection(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ok, failed, updated := h.runAllConnQueries(r.Context(), id)
	msg := strconv.Itoa(ok) + " Abfrage(n) ausgeführt, " + strconv.Itoa(updated) + " Zuordnung(en) aktualisiert"
	errMsg := ""
	if failed > 0 {
		errMsg = strconv.Itoa(failed) + " Abfrage(n) fehlgeschlagen – Details in der Liste"
	}
	http.Redirect(w, r, connectionBackPath("import", id, r.FormValue("tab"), msg, errMsg), http.StatusSeeOther)
}

// ── Vorlagen ─────────────────────────────────────────────────

func (h *Handler) ImportConnectionTemplateSaveWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionTemplateSaveWeb(w, r, "import")
}
func (h *Handler) ExportConnectionTemplateSaveWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionTemplateSaveWeb(w, r, "export")
}
func (h *Handler) ImportConnectionTemplateApplyWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionTemplateApplyWeb(w, r, "import")
}
func (h *Handler) ExportConnectionTemplateApplyWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionTemplateApplyWeb(w, r, "export")
}
func (h *Handler) ImportConnectionTemplateDeleteWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionTemplateDeleteWeb(w, r, "import")
}
func (h *Handler) ExportConnectionTemplateDeleteWeb(w http.ResponseWriter, r *http.Request) {
	h.connectionTemplateDeleteWeb(w, r, "export")
}

func (h *Handler) connectionTemplateSaveWeb(w http.ResponseWriter, r *http.Request, direction string) {
	id := chi.URLParam(r, "id")
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	desc := strings.TrimSpace(r.FormValue("description"))
	if l := len([]rune(name)); l < 2 || l > 150 || len([]rune(desc)) > 500 {
		http.Redirect(w, r, connectionBackPath(direction, id, "templates", "", "Name: 2 bis 150 Zeichen, Beschreibung höchstens 500"), http.StatusSeeOther)
		return
	}
	if err := h.saveConnectionTemplate(r.Context(), id, direction, name, desc, getUser(r).ID); err != nil {
		http.Redirect(w, r, connectionBackPath(direction, id, "templates", "", err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, connectionBackPath(direction, id, "templates", "Vorlage „"+name+"“ gespeichert", ""), http.StatusSeeOther)
}

func (h *Handler) connectionTemplateApplyWeb(w http.ResponseWriter, r *http.Request, direction string) {
	id, tid := chi.URLParam(r, "id"), chi.URLParam(r, "tid")
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	msg, err := h.applyConnectionTemplate(r.Context(), id, direction, tid, getUser(r).ID)
	if err != nil {
		http.Redirect(w, r, connectionBackPath(direction, id, "templates", "", err.Error()), http.StatusSeeOther)
		return
	}
	tab := "mappings"
	if direction == "import" {
		tab = "queries"
	}
	http.Redirect(w, r, connectionBackPath(direction, id, tab, "Vorlage angewendet: "+msg, ""), http.StatusSeeOther)
}

func (h *Handler) connectionTemplateDeleteWeb(w http.ResponseWriter, r *http.Request, direction string) {
	id, tid := chi.URLParam(r, "id"), chi.URLParam(r, "tid")
	if !h.canConnectionWrite(r, direction) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, _ = h.db.Exec(r.Context(), `DELETE FROM connection_templates WHERE id::text = $1 AND direction = $2`, tid, direction)
	http.Redirect(w, r, connectionBackPath(direction, id, "templates", "Vorlage gelöscht", ""), http.StatusSeeOther)
}
