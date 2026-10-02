package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Pruefungen je Verbindung: Konfiguration, Erreichbarkeit (DNS/TCP), TLS,
// Anmeldung/Protokoll, Abfragen, Zuordnungen und Hintergrund-Jobs. Das
// Ergebnis wird an der Verbindung gespeichert (last_check) und auf der
// Verbindungsseite angezeigt.

type checkResult struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | info
	Detail string `json:"detail"`
	Ms     int64  `json:"ms,omitempty"`
}

type checkReport struct {
	At    string        `json:"at"`
	Items []checkResult `json:"items"`
	OK    int           `json:"ok"`
	Warn  int           `json:"warn"`
	Fail  int           `json:"fail"`
}

func (r *checkReport) add(group, name, status, detail string, ms int64) {
	r.Items = append(r.Items, checkResult{Group: group, Name: name, Status: status, Detail: detail, Ms: ms})
	switch status {
	case "ok":
		r.OK++
	case "warn":
		r.Warn++
	case "fail":
		r.Fail++
	}
}

// Overall: schlechtester Zustand.
func (r *checkReport) Overall() string {
	switch {
	case r == nil:
		return ""
	case r.Fail > 0:
		return "fail"
	case r.Warn > 0:
		return "warn"
	}
	return "ok"
}

// Pflichtfelder je Typ (Anzeige-Bezeichnung in Klammern).
var connRequired = map[string]map[string][][2]string{
	"import": {
		"excel":    {{"source_path", "Quellpfad"}},
		"csv":      {{"source_path", "Quellpfad"}},
		"sqlite":   {{"file_path", "Datenbankdatei"}},
		"mysql":    {{"host", "Host"}, {"database", "Datenbank"}, {"username", "Benutzer"}},
		"mssql":    {{"host", "Host"}, {"database", "Datenbank"}, {"username", "Benutzer"}},
		"web":      {{"url", "URL"}},
		"rest_api": {{"base_url", "Basis-URL"}},
		"modbus":   {{"host", "Host"}},
		"opcua":    {{"endpoint_url", "Endpunkt"}},
	},
	"export": {
		"pdf":    {{"template_id", "Vorlage"}, {"destination_path", "Zielpfad"}},
		"excel":  {{"template_id", "Vorlage"}, {"destination_path", "Zielpfad"}},
		"csv":    {{"destination_path", "Zielpfad"}},
		"sqlite": {{"file_path", "Datenbankdatei"}, {"table_name", "Tabelle"}},
		"mysql":  {{"host", "Host"}, {"database", "Datenbank"}, {"username", "Benutzer"}, {"table_name", "Tabelle"}},
		"mssql":  {{"host", "Host"}, {"database", "Datenbank"}, {"username", "Benutzer"}, {"table_name", "Tabelle"}},
		"opcua":  {{"endpoint_url", "Endpunkt"}},
		"modbus": {{"host", "Host"}},
	},
}

// netTarget: Host und Port, die das PDH erreichen muss (ok=false: kein Netz).
func netTarget(kind string, config map[string]string) (host, port string, useTLS, ok bool) {
	get := func(k string) string { return strings.TrimSpace(config[k]) }
	switch kind {
	case "mysql":
		return get("host"), firstNonEmpty(get("port"), "3306"), config["use_tls"] == "true", get("host") != ""
	case "mssql":
		if get("instance_name") != "" && get("port") == "" {
			return get("host"), "", false, false // benannte Instanz: Port ueber SQL Browser
		}
		return get("host"), firstNonEmpty(get("port"), "1433"), config["use_tls"] == "true", get("host") != ""
	case "modbus":
		return get("host"), firstNonEmpty(get("port"), "502"), false, get("host") != ""
	case "opcua":
		u, err := url.Parse(get("endpoint_url"))
		if err != nil || u.Hostname() == "" {
			return "", "", false, false
		}
		return u.Hostname(), firstNonEmpty(u.Port(), "4840"), false, true
	case "web", "rest_api":
		raw := get("url")
		if kind == "rest_api" {
			raw = get("base_url")
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", "", false, false
		}
		def := "80"
		if u.Scheme == "https" {
			def = "443"
		}
		return u.Hostname(), firstNonEmpty(u.Port(), def), u.Scheme == "https", true
	case "mqtt":
		if config["broker_mode"] == "integrated" {
			return "", "", false, false
		}
		def := "1883"
		if config["ext_use_tls"] == "true" {
			def = "8883"
		}
		return get("ext_broker_host"), firstNonEmpty(get("ext_broker_port"), def), config["ext_use_tls"] == "true", get("ext_broker_host") != ""
	}
	return "", "", false, false
}

func checkNetwork(ctx context.Context, rep *checkReport, host, port string, useTLS bool) bool {
	start := time.Now()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	addrs, err := net.DefaultResolver.LookupHost(dctx, host)
	cancel()
	if err != nil {
		rep.add("Erreichbarkeit", "Name auflösen ("+host+")", "fail", "DNS: "+err.Error()+" – Schreibweise prüfen oder IP-Adresse eintragen.", time.Since(start).Milliseconds())
		return false
	}
	rep.add("Erreichbarkeit", "Name auflösen ("+host+")", "ok", strings.Join(addrs, ", "), time.Since(start).Milliseconds())
	start = time.Now()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		rep.add("Erreichbarkeit", "Port "+port+" erreichbar", "fail", err.Error()+" – läuft der Dienst, ist der Port richtig, lässt die Firewall die Verbindung vom PDH-Server zu?", time.Since(start).Milliseconds())
		return false
	}
	conn.Close()
	rep.add("Erreichbarkeit", "Port "+port+" erreichbar", "ok", "TCP-Verbindung in "+strconv.FormatInt(time.Since(start).Milliseconds(), 10)+" ms", time.Since(start).Milliseconds())
	if useTLS {
		start = time.Now()
		tconn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", net.JoinHostPort(host, port), &tls.Config{ServerName: host})
		if err != nil {
			rep.add("Erreichbarkeit", "TLS-Zertifikat", "warn", err.Error()+" – mit selbst signierten Zertifikaten kann die Verbindung trotzdem klappen; sonst Zertifikat prüfen.", time.Since(start).Milliseconds())
			return true
		}
		cert := tconn.ConnectionState().PeerCertificates[0]
		tconn.Close()
		days := int(time.Until(cert.NotAfter).Hours() / 24)
		status, detail := "ok", fmt.Sprintf("gültig bis %s (noch %d Tage), ausgestellt für %s", cert.NotAfter.Format("02.01.2006"), days, cert.Subject.CommonName)
		if days < 30 {
			status, detail = "warn", detail+" – läuft bald ab"
		}
		rep.add("Erreichbarkeit", "TLS-Zertifikat", status, detail, time.Since(start).Milliseconds())
	}
	return true
}

func checkFile(rep *checkReport, label, path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		rep.add("Erreichbarkeit", label, "fail", "nicht erreichbar: "+err.Error()+" – der Pfad muss vom PDH-Server aus sichtbar sein (bei Docker: eingebundenes Laufwerk).", 0)
		return false
	}
	if st.IsDir() {
		rep.add("Erreichbarkeit", label, "fail", path+" ist ein Ordner, keine Datei", 0)
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		rep.add("Erreichbarkeit", label, "fail", "nicht lesbar: "+err.Error(), 0)
		return false
	}
	f.Close()
	age := time.Since(st.ModTime())
	status := "ok"
	detail := fmt.Sprintf("%s, %s, zuletzt geändert %s", path, humanBytes(st.Size()), st.ModTime().Format("02.01.2006 15:04"))
	if age > 7*24*time.Hour {
		status, detail = "warn", detail+" – seit über einer Woche unverändert, kommt noch Nachschub?"
	}
	rep.add("Erreichbarkeit", label, status, detail, 0)
	return true
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d Byte", n)
}

// checkWritableDir: Zielordner vorhanden und beschreibbar (Testdatei).
func checkWritableDir(rep *checkReport, path string) {
	dir := filepath.Dir(path)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		rep.add("Ziel", "Zielordner "+dir, "fail", "Ordner nicht vorhanden oder nicht erreichbar", 0)
		return
	}
	f, err := os.CreateTemp(dir, ".pdh-pruefung-*")
	if err != nil {
		rep.add("Ziel", "Zielordner "+dir, "fail", "nicht beschreibbar: "+err.Error(), 0)
		return
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	rep.add("Ziel", "Zielordner "+dir, "ok", "vorhanden und beschreibbar", 0)
}

// runConnectionChecks fuehrt alle Pruefungen aus und speichert das Ergebnis.
func (h *Handler) runConnectionChecks(ctx context.Context, id, direction string) (*checkReport, error) {
	_, kind, enabled, config, err := h.connectionRow(ctx, id, direction)
	if err != nil {
		return nil, errors.New("Verbindung nicht gefunden")
	}
	rep := &checkReport{At: time.Now().Format("02.01.2006 15:04:05")}

	// 1 Konfiguration
	missing := []string{}
	for _, f := range connRequired[direction][kind] {
		if strings.TrimSpace(config[f[0]]) == "" {
			missing = append(missing, f[1])
		}
	}
	if kind == "mqtt" && direction == "import" {
		if config["broker_mode"] == "integrated" {
			if strings.TrimSpace(config["int_listen_port"]) == "" {
				missing = append(missing, "Port des integrierten Brokers")
			}
		} else if strings.TrimSpace(config["ext_broker_host"]) == "" {
			missing = append(missing, "Broker-Host")
		}
	}
	if kind == "rest_api" {
		switch config["auth_method"] {
		case "basic":
			if config["username"] == "" {
				missing = append(missing, "Benutzer (Basic Auth)")
			}
		case "bearer":
			if config["bearer_token"] == "" {
				missing = append(missing, "Bearer-Token")
			}
		case "api_key":
			if config["api_key_header"] == "" || config["api_key_value"] == "" {
				missing = append(missing, "API-Key-Header und -Wert")
			}
		}
	}
	if kind == "opcua" && config["auth_mode"] == "username" && config["username"] == "" {
		missing = append(missing, "Benutzer (OPC UA)")
	}
	if len(missing) > 0 {
		rep.add("Konfiguration", "Pflichtangaben", "fail", "Es fehlt: "+strings.Join(missing, ", "), 0)
	} else {
		rep.add("Konfiguration", "Pflichtangaben", "ok", "vollständig", 0)
	}
	if !enabled {
		rep.add("Konfiguration", "Status", "warn", "Verbindung ist deaktiviert – es läuft nichts automatisch.", 0)
	} else {
		rep.add("Konfiguration", "Status", "ok", "aktiv", 0)
	}
	if (kind == "mysql" || kind == "mssql") && config["use_tls"] != "true" {
		rep.add("Konfiguration", "Verschlüsselung", "warn", "TLS ist aus – Zugangsdaten und Werte gehen unverschlüsselt durchs Netz.", 0)
	}
	if kind == "web" || kind == "rest_api" {
		if u, err := url.Parse(firstNonEmpty(config["url"], config["base_url"])); err == nil && u.Scheme == "http" && config["auth_method"] != "" && config["auth_method"] != "none" {
			rep.add("Konfiguration", "Verschlüsselung", "warn", "Anmeldedaten über http statt https.", 0)
		}
	}
	if s := strings.TrimSpace(config["schedule_cron"]); s != "" {
		rep.add("Konfiguration", "Zeitplan", "ok", "Cron „"+s+"“ ist gültig", 0)
	}
	if len(missing) > 0 {
		h.storeCheck(ctx, id, rep)
		return rep, nil
	}

	// 2 Erreichbarkeit
	reachable := true
	if host, port, useTLS, ok := netTarget(kind, config); ok {
		reachable = checkNetwork(ctx, rep, host, port, useTLS)
	} else if kind == "mssql" && config["instance_name"] != "" {
		rep.add("Erreichbarkeit", "Benannte Instanz", "info", "Port wird über den SQL-Server-Browser (UDP 1434) ermittelt – keine Portprüfung möglich.", 0)
	}
	switch {
	case direction == "import" && (kind == "excel" || kind == "csv"):
		reachable = checkFile(rep, "Quelldatei", config["source_path"])
	case kind == "sqlite":
		if direction == "import" {
			reachable = checkFile(rep, "Datenbankdatei", config["file_path"])
		} else {
			checkWritableDir(rep, config["file_path"])
		}
	case direction == "export" && (kind == "pdf" || kind == "excel" || kind == "csv"):
		checkWritableDir(rep, config["destination_path"])
	}

	// 3 Anmeldung & Protokoll
	if reachable {
		h.checkProtocol(ctx, rep, direction, kind, config, id)
	}

	// 4 Abfragen, 5 Zuordnungen, 6 Hintergrund
	if direction == "import" {
		h.checkQueriesAndMappings(ctx, rep, id, kind, config, reachable)
	} else {
		h.checkExportMappings(ctx, rep, id, kind, config)
	}
	h.checkBackground(ctx, rep, id, direction, kind, enabled, config)
	h.storeCheck(ctx, id, rep)
	return rep, nil
}

func (h *Handler) storeCheck(ctx context.Context, id string, rep *checkReport) {
	b, _ := json.Marshal(rep)
	_, _ = h.db.Exec(ctx, `UPDATE import_export_connections SET last_check = $1, last_check_at = NOW() WHERE id = $2`, b, id)
}

func (h *Handler) checkProtocol(ctx context.Context, rep *checkReport, direction, kind string, config map[string]string, id string) {
	start := time.Now()
	ms := func() int64 { return time.Since(start).Milliseconds() }
	switch {
	case sqlBrowsableKind(kind):
		db, label, err := openSQLConnection(kind, config)
		if err != nil {
			rep.add("Anmeldung", "Datenbank öffnen", "fail", err.Error(), ms())
			return
		}
		defer db.Close()
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := db.PingContext(pctx); err != nil {
			rep.add("Anmeldung", "Anmeldung an "+label, "fail", err.Error()+" – Benutzer, Passwort und Rechte prüfen.", ms())
			return
		}
		rep.add("Anmeldung", "Anmeldung an "+label, "ok", "angemeldet", ms())
		tables, err := listSQLTables(db, kind)
		if err != nil {
			rep.add("Anmeldung", "Tabellen lesen", "warn", err.Error(), ms())
			return
		}
		if direction == "export" {
			t := strings.TrimSpace(config["table_name"])
			found := false
			for _, x := range tables {
				if strings.EqualFold(x, t) {
					found = true
				}
			}
			if found {
				rep.add("Ziel", "Zieltabelle „"+t+"“", "ok", "vorhanden", ms())
			} else {
				rep.add("Ziel", "Zieltabelle „"+t+"“", "warn", "nicht gefunden – wird sie beim Export angelegt? Sonst Namen prüfen.", ms())
			}
			return
		}
		rep.add("Anmeldung", "Tabellen lesen", "ok", fmt.Sprintf("%d Tabelle(n) sichtbar", len(tables)), ms())
	case webBrowsableKind(kind):
		body, status, err := fetchImportResponse(kind, config)
		switch {
		case err != nil:
			rep.add("Anmeldung", "Abruf", "fail", err.Error(), ms())
		case status == 401 || status == 403:
			rep.add("Anmeldung", "Abruf", "fail", fmt.Sprintf("HTTP %d – Anmeldung abgelehnt, Zugangsdaten prüfen.", status), ms())
		case status < 200 || status > 299:
			rep.add("Anmeldung", "Abruf", "fail", fmt.Sprintf("HTTP %d", status), ms())
		default:
			rep.add("Anmeldung", "Abruf", "ok", fmt.Sprintf("HTTP %d, %s", status, humanBytes(int64(len(body)))), ms())
			var v interface{}
			if json.Unmarshal(body, &v) != nil {
				rep.add("Anmeldung", "Antwortformat", "warn", "keine JSON-Antwort – Werte nur als Text verfügbar", 0)
			} else {
				rep.add("Anmeldung", "Antwortformat", "ok", fmt.Sprintf("JSON mit %d Wert(en)", len(flattenJSONMap(v))), 0)
			}
		}
	case kind == "modbus":
		handler, _, err := openModbusClient(config)
		if err != nil {
			rep.add("Anmeldung", "Modbus-Verbindung", "fail", err.Error(), ms())
			return
		}
		handler.Close()
		rep.add("Anmeldung", "Modbus-Verbindung", "ok", "Unit-ID "+firstNonEmpty(config["unit_id"], "1"), ms())
	case kind == "opcua":
		client, err := openOPCUAClient(ctx, config)
		if err != nil {
			rep.add("Anmeldung", "OPC-UA-Sitzung", "fail", err.Error(), ms())
			return
		}
		defer client.Close(ctx)
		rep.add("Anmeldung", "OPC-UA-Sitzung", "ok", "Sitzung aufgebaut", ms())
		res, err := runOPCUAQuery(ctx, config, map[string]string{"nodes": "i=2259"})
		if err == nil && len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
			state := res.Rows[0][0]
			status := "ok"
			if state != "0" {
				status = "warn"
			}
			rep.add("Anmeldung", "Serverzustand", status, "ServerState = "+state+" (0 = läuft)", ms())
		}
	case direction == "import" && (kind == "excel" || kind == "csv"):
		cols, rows, err := readTabularPreview(kind, config["source_path"], config["sheet_name"], config["delimiter"], config["has_header"] == "true")
		if err != nil {
			rep.add("Anmeldung", "Inhalt lesen", "fail", err.Error(), ms())
			return
		}
		rep.add("Anmeldung", "Inhalt lesen", "ok", fmt.Sprintf("%d Spalte(n), Vorschau %d Zeile(n)", len(cols), len(rows)), ms())
	case kind == "mqtt":
		if config["broker_mode"] == "integrated" {
			if h.mqttBrokers != nil && h.mqttBrokers.IsRunning(id) {
				rep.add("Anmeldung", "Integrierter Broker", "ok", "läuft auf Port "+config["int_listen_port"], 0)
			} else {
				detail := "läuft nicht"
				if h.mqttBrokers != nil {
					if e := h.mqttBrokers.LastError(id); e != "" {
						detail += ": " + e
					}
				}
				rep.add("Anmeldung", "Integrierter Broker", "fail", detail, 0)
			}
		}
	}
}

func (h *Handler) checkQueriesAndMappings(ctx context.Context, rep *checkReport, id, kind string, config map[string]string, reachable bool) {
	qs, _ := h.loadConnQueries(ctx, id)
	known := map[string]connQuery{}
	results := map[string]*queryResult{}
	if queryKindSupported(kind) {
		if len(qs) == 0 {
			rep.add("Abfragen", "Abfragen", "info", "Noch keine Abfragen – unter „Abfragen“ anlegen oder eine Vorlage übernehmen.", 0)
		}
		for _, q := range qs {
			known[q.ID] = q
			if !q.Enabled {
				rep.add("Abfragen", q.Name, "info", "deaktiviert", 0)
				continue
			}
			if !reachable {
				rep.add("Abfragen", q.Name, "fail", "übersprungen – Verbindung nicht erreichbar", 0)
				continue
			}
			start := time.Now()
			res, n, err := h.executeConnQuery(ctx, kind, config, q)
			if err != nil {
				rep.add("Abfragen", q.Name, "fail", err.Error(), time.Since(start).Milliseconds())
				continue
			}
			r := res
			results[q.ID] = &r
			status := "ok"
			detail := fmt.Sprintf("%d Zeile(n), %d Spalte(n), %d Zuordnung(en) aktualisiert", len(res.Rows), len(res.Columns), n)
			if len(res.Rows) == 0 {
				status, detail = "warn", "liefert keine Zeilen – Zuordnungen bekommen keinen Wert"
			}
			rep.add("Abfragen", q.Name, status, detail, time.Since(start).Milliseconds())
		}
	}

	ms, err := h.importMappings(ctx, id)
	if err != nil {
		return
	}
	if len(ms) == 0 {
		rep.add("Zuordnungen", "Zuordnungen", "info", "Noch keine Zuordnungen – Werte werden erst übernommen, wenn sie einer Anlage zugeordnet sind.", 0)
		return
	}
	empty, stale := 0, 0
	var broken, missingCol []string
	for _, m := range ms {
		if qid, col, ok := parseQueryRef(m.SourceRef); ok {
			q, exists := known[qid]
			if !exists {
				broken = append(broken, m.Name)
				continue
			}
			if res := results[qid]; res != nil {
				if _, ok := res.value(q.Spec, col); !ok {
					missingCol = append(missingCol, m.Name+" ("+col+")")
				}
			}
		}
		if m.LastValue == "" && m.LastReceivedAt == "" {
			empty++
			continue
		}
		if t, err := time.ParseInLocation("02.01.2006 15:04:05", m.LastReceivedAt, time.Local); err == nil && time.Since(t) > 24*time.Hour {
			stale++
		}
	}
	rep.add("Zuordnungen", "Anzahl", "ok", fmt.Sprintf("%d Zuordnung(en)", len(ms)), 0)
	if len(broken) > 0 {
		rep.add("Zuordnungen", "Verweis auf gelöschte Abfrage", "fail", strings.Join(broken, ", ")+" – Zuordnung löschen oder neu anlegen.", 0)
	}
	if len(missingCol) > 0 {
		rep.add("Zuordnungen", "Spalte fehlt im Ergebnis", "warn", strings.Join(missingCol, ", "), 0)
	}
	if empty > 0 {
		rep.add("Zuordnungen", "Ohne Wert", "warn", fmt.Sprintf("%d Zuordnung(en) haben noch nie einen Wert bekommen", empty), 0)
	}
	if stale > 0 {
		rep.add("Zuordnungen", "Veraltete Werte", "warn", fmt.Sprintf("%d Zuordnung(en) seit über 24 Stunden ohne neuen Wert", stale), 0)
	}
}

func (h *Handler) checkExportMappings(ctx context.Context, rep *checkReport, id, kind string, config map[string]string) {
	if kind == "pdf" || kind == "excel" {
		if tid := strings.TrimSpace(config["template_id"]); tid != "" {
			var n int
			_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM export_templates WHERE id::text = $1`, tid).Scan(&n)
			if n == 0 {
				rep.add("Ziel", "Exportvorlage", "fail", "die eingestellte Vorlage gibt es nicht mehr – unter Export → Vorlagen wählen", 0)
			} else {
				rep.add("Ziel", "Exportvorlage", "ok", "vorhanden", 0)
			}
		}
	}
	ms, err := h.exportMappings(ctx, id)
	if err != nil {
		return
	}
	if len(ms) == 0 {
		rep.add("Zuordnungen", "Felder", "warn", "Noch keine Felder – der Export schreibt nichts.", 0)
		return
	}
	empty := 0
	for _, m := range ms {
		if m.SourceValue == "" {
			empty++
		}
	}
	rep.add("Zuordnungen", "Felder", "ok", fmt.Sprintf("%d Feld(er)", len(ms)), 0)
	if empty > 0 {
		rep.add("Zuordnungen", "Quellwerte", "warn", fmt.Sprintf("%d Feld(er) ohne aktuellen Quellwert – die Import-Verbindung prüfen", empty), 0)
	}
}

func (h *Handler) checkBackground(ctx context.Context, rep *checkReport, id, direction, kind string, enabled bool, config map[string]string) {
	if !enabled {
		return
	}
	if direction == "export" {
		if strings.TrimSpace(config["schedule_cron"]) == "" {
			rep.add("Hintergrund", "Zeitplan", "info", "kein Zeitplan – Export nur von Hand", 0)
		} else if h.exportCron != nil {
			if next, ok := h.exportCron.Next(id); ok {
				rep.add("Hintergrund", "Zeitplan", "ok", "nächster Export "+next.Format("02.01.2006 15:04"), 0)
			} else {
				rep.add("Hintergrund", "Zeitplan", "fail", "Zeitplan ist eingestellt, aber nicht aktiv – Verbindung speichern oder Server neu starten", 0)
			}
		}
		return
	}
	if webBrowsableKind(kind) && h.importPoll != nil {
		if m, _ := strconv.Atoi(strings.TrimSpace(config["poll_interval_minutes"])); m > 0 {
			if h.importPoll.Running(id) {
				rep.add("Hintergrund", "Abruf-Intervall", "ok", fmt.Sprintf("alle %d Minute(n)", m), 0)
			} else {
				rep.add("Hintergrund", "Abruf-Intervall", "fail", "eingestellt, läuft aber nicht – Verbindung speichern", 0)
			}
		}
	}
	if kind == "mqtt" && h.mqttImport != nil {
		if h.mqttImport.IsRunning(id) {
			rep.add("Hintergrund", "MQTT-Empfang", "ok", "abonniert", 0)
		} else {
			rep.add("Hintergrund", "MQTT-Empfang", "warn", "kein Abonnement aktiv – gibt es Zuordnungen?", 0)
		}
	}
	qs, _ := h.loadConnQueries(ctx, id)
	want, run := 0, 0
	for _, q := range qs {
		if q.Enabled && q.IntervalMinutes > 0 {
			want++
			if h.importPoll != nil && h.importPoll.Running(queryPollID(q.ID)) {
				run++
			}
		}
	}
	if want > 0 {
		status := "ok"
		if run < want {
			status = "fail"
		}
		rep.add("Hintergrund", "Abfrage-Intervalle", status, fmt.Sprintf("%d von %d laufen", run, want), 0)
	}
}
