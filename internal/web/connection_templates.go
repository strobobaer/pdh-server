package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Vorlagen fuer Verbindungen:
//   - eingebaute Abfrage-Vorlagen je Verbindungstyp (Ausgangspunkt zum
//     Anpassen, Platzhalter in <spitzen Klammern>)
//   - eigene Vorlagen: die Abfragen und Zuordnungen einer Verbindung
//     speichern und auf eine andere Verbindung desselben Typs uebertragen

type builtinQueryTemplate struct {
	Name     string
	Desc     string
	Spec     map[string]string
	Interval int
}

// SpecJSON fuer das Formular (data-Attribut).
func (t builtinQueryTemplate) SpecJSON() string {
	b, _ := json.Marshal(map[string]interface{}{"name": t.Name, "spec": t.Spec, "interval": t.Interval})
	return string(b)
}

func sqlLimit1(kind, cols, from, order string) string {
	if kind == "mssql" {
		return "SELECT TOP 1 " + cols + "\nFROM " + from + "\nORDER BY " + order
	}
	return "SELECT " + cols + "\nFROM " + from + "\nORDER BY " + order + "\nLIMIT 1"
}

func sqlToday(kind, col string) string {
	switch kind {
	case "mssql":
		return col + " >= CAST(GETDATE() AS date)"
	case "sqlite":
		return col + " >= date('now', 'localtime')"
	}
	return col + " >= CURDATE()"
}

// sqlReadTemplates: Vorlagen zum Auslesen einer unbekannten Datenbank – welche Tabellen es
// gibt, welche Spalten, die ersten Zeilen und wie groß die Tabellen sind. Nur von Hand
// (kein Abrufrhythmus); sie stehen vor den Vorlagen für Messwerte.
func sqlReadTemplates(kind string) []builtinQueryTemplate {
	var tables, columns, rows string
	switch kind {
	case "mssql":
		tables = "SELECT TABLE_SCHEMA AS schema_name, TABLE_NAME AS tabelle, TABLE_TYPE AS art\nFROM INFORMATION_SCHEMA.TABLES\nORDER BY TABLE_SCHEMA, TABLE_NAME"
		columns = "SELECT COLUMN_NAME AS spalte, DATA_TYPE AS typ, CHARACTER_MAXIMUM_LENGTH AS laenge, IS_NULLABLE AS leer_erlaubt\nFROM INFORMATION_SCHEMA.COLUMNS\nWHERE TABLE_SCHEMA = '<schema>' AND TABLE_NAME = '<tabellenname>'\nORDER BY ORDINAL_POSITION"
		rows = "SELECT s.name AS schema_name, t.name AS tabelle, SUM(p.rows) AS zeilen\nFROM sys.tables t\nJOIN sys.schemas s ON s.schema_id = t.schema_id\nJOIN sys.partitions p ON p.object_id = t.object_id AND p.index_id IN (0, 1)\nGROUP BY s.name, t.name\nORDER BY zeilen DESC"
	case "mysql":
		tables = "SELECT table_name AS tabelle, table_type AS art\nFROM information_schema.tables\nWHERE table_schema = DATABASE()\nORDER BY table_name"
		columns = "SELECT column_name AS spalte, data_type AS typ, character_maximum_length AS laenge, is_nullable AS leer_erlaubt\nFROM information_schema.columns\nWHERE table_schema = DATABASE() AND table_name = '<tabellenname>'\nORDER BY ordinal_position"
		rows = "SELECT table_name AS tabelle, table_rows AS zeilen_ungefaehr\nFROM information_schema.tables\nWHERE table_schema = DATABASE()\nORDER BY table_rows DESC"
	case "sqlite":
		tables = "SELECT name AS tabelle, type AS art\nFROM sqlite_master\nWHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\'\nORDER BY name"
		columns = "SELECT name AS spalte, type AS typ, \"notnull\" AS pflicht\nFROM pragma_table_info('<tabellenname>')"
	default:
		return nil
	}
	view := "SELECT TOP 20 *\nFROM <tabelle>"
	if kind != "mssql" {
		view = "SELECT *\nFROM <tabelle>\nLIMIT 20"
	}
	out := []builtinQueryTemplate{
		{Name: "Alle Tabellen und Sichten", Desc: "Was es in der Datenbank gibt – zum Erkunden. Danach wieder löschen.",
			Spec: map[string]string{"sql": tables, "row": "first"}},
		{Name: "Spalten einer Tabelle", Desc: "Spaltennamen und Typen einer Tabelle aus der Liste oben.",
			Spec: map[string]string{"sql": columns, "row": "first"}},
		{Name: "Tabelle ansehen (20 Zeilen)", Desc: "Die ersten 20 Zeilen einer Tabelle oder Sicht – bei SQL Server mit Schema, z. B. dbo.Artikel.",
			Spec: map[string]string{"sql": view, "row": "first"}},
	}
	if rows != "" {
		out = append(out, builtinQueryTemplate{Name: "Zeilen je Tabelle", Desc: "Größte Tabellen zuerst – zeigt, wo die Daten liegen.",
			Spec: map[string]string{"sql": rows, "row": "first"}})
	}
	return out
}

// builtinQueryTemplates: Vorlagen je Verbindungstyp.
func builtinQueryTemplates(kind string) []builtinQueryTemplate {
	switch {
	case sqlBrowsableKind(kind):
		return append(sqlReadTemplates(kind), []builtinQueryTemplate{
			{Name: "Letzter Datensatz", Desc: "Neueste Zeile einer Tabelle – z. B. aktueller Zählerstand oder letzter Messwert.",
				Spec: map[string]string{"sql": sqlLimit1(kind, "*", "<tabelle>", "<zeitstempel-spalte> DESC"), "row": "first"}, Interval: 5},
			{Name: "Ausgewählte Messwerte", Desc: "Nur bestimmte Spalten des neuesten Eintrags einer Maschine.",
				Spec: map[string]string{"sql": sqlLimit1(kind, "<spalte1>, <spalte2>", "<tabelle>\nWHERE <maschinen-spalte> = '<maschine>'", "<zeitstempel-spalte> DESC"), "row": "first"}, Interval: 5},
			{Name: "Summe heute", Desc: "Tagessumme, z. B. Stückzahl oder Verbrauch seit Mitternacht.",
				Spec: map[string]string{"sql": "SELECT SUM(<wert-spalte>) AS summe_heute\nFROM <tabelle>\nWHERE " + sqlToday(kind, "<zeitstempel-spalte>"), "row": "first"}, Interval: 15},
			{Name: "Anzahl offener Meldungen", Desc: "Zählt Datensätze mit einem bestimmten Status.",
				Spec: map[string]string{"sql": "SELECT COUNT(*) AS anzahl\nFROM <tabelle>\nWHERE <status-spalte> = '<offen>'", "row": "first"}, Interval: 15},
			{Name: "Kennzahlen je Gruppe", Desc: "Mehrere Zeilen, z. B. je Linie – zugeordnet wird die erste Zeile.",
				Spec: map[string]string{"sql": "SELECT <gruppen-spalte>, COUNT(*) AS anzahl, MAX(<zeitstempel-spalte>) AS zuletzt\nFROM <tabelle>\nGROUP BY <gruppen-spalte>\nORDER BY <gruppen-spalte>", "row": "first"}},
		}...)
	case kind == "rest_api":
		return []builtinQueryTemplate{
			{Name: "Status-Endpunkt", Desc: "Einzelnes Objekt mit Werten, z. B. GET /status.", Spec: map[string]string{"path": "/status", "row": "first"}, Interval: 5},
			{Name: "Liste von Einträgen", Desc: "Antwort mit einer Liste unter „data“ – je Eintrag eine Zeile.", Spec: map[string]string{"path": "/<ressource>", "items_path": "data", "row": "first"}, Interval: 15},
			{Name: "Einzelner Datensatz", Desc: "Ein bestimmter Eintrag über seine Kennung.", Spec: map[string]string{"path": "/<ressource>/<id>", "row": "first"}, Interval: 15},
			{Name: "Gefilterte Abfrage", Desc: "Mit Suchparametern in der Adresse.", Spec: map[string]string{"path": "/<ressource>?status=<offen>&limit=1", "items_path": "data", "row": "first"}, Interval: 15},
		}
	case kind == "web":
		return []builtinQueryTemplate{
			{Name: "Ganze Antwort", Desc: "Die eingestellte Adresse – alle Werte als eine Zeile.", Spec: map[string]string{"row": "first"}, Interval: 15},
			{Name: "Liste aus der Antwort", Desc: "Liste unter einem Pfad, je Eintrag eine Zeile.", Spec: map[string]string{"items_path": "<pfad.zur.liste>", "row": "first"}, Interval: 15},
		}
	case kind == "modbus":
		return []builtinQueryTemplate{
			{Name: "Holding-Register 0–9", Desc: "Zehn 16-Bit-Werte ab Adresse 0.", Spec: map[string]string{"area": "holding", "start": "0", "count": "10", "type": "uint16"}, Interval: 1},
			{Name: "Messwerte Float32", Desc: "Gleitkommawerte (je 2 Register) ab Adresse 100.", Spec: map[string]string{"area": "holding", "start": "100", "count": "8", "type": "float32"}, Interval: 1},
			{Name: "Eingangsregister", Desc: "Nur lesbare Werte, z. B. Sensoren.", Spec: map[string]string{"area": "input", "start": "0", "count": "10", "type": "int16"}, Interval: 1},
			{Name: "Zustände (Coils)", Desc: "16 Schaltzustände ab Adresse 0, z. B. Störung/Betrieb.", Spec: map[string]string{"area": "coil", "start": "0", "count": "16", "type": "bool"}, Interval: 1},
		}
	case kind == "opcua":
		return []builtinQueryTemplate{
			{Name: "Serverstatus", Desc: "Zustand und aktuelle Uhrzeit des OPC-UA-Servers – gut zum Prüfen.", Spec: map[string]string{"nodes": "i=2259\ni=2258"}, Interval: 5},
			{Name: "Maschinenwerte", Desc: "Eigene Knoten, je Zeile einer – IDs aus dem Explorer.", Spec: map[string]string{"nodes": "ns=2;s=<Maschine>.<Temperatur>\nns=2;s=<Maschine>.<Drehzahl>\nns=2;s=<Maschine>.<Stoerung>"}, Interval: 1},
		}
	case kind == "excel":
		return []builtinQueryTemplate{
			{Name: "Letzte Zeile", Desc: "Neuester Eintrag einer fortlaufenden Liste.", Spec: map[string]string{"row": "last"}, Interval: 15},
			{Name: "Erste Zeile", Desc: "Kopfwerte oder eine Tabelle mit nur einer Datenzeile.", Spec: map[string]string{"row": "first"}, Interval: 15},
			{Name: "Anderes Tabellenblatt", Desc: "Zweites Blatt derselben Datei, letzte Zeile.", Spec: map[string]string{"sheet": "<Blattname>", "row": "last"}, Interval: 15},
		}
	case kind == "csv":
		return []builtinQueryTemplate{
			{Name: "Letzte Zeile", Desc: "Neuester Eintrag, z. B. eine Protokolldatei der Maschine.", Spec: map[string]string{"row": "last"}, Interval: 5},
			{Name: "Erste Zeile", Desc: "Datei mit nur einer Datenzeile.", Spec: map[string]string{"row": "first"}, Interval: 15},
		}
	}
	return nil
}

// ── Eigene Vorlagen ──────────────────────────────────────────

type templateQuery struct {
	Name     string            `json:"name"`
	Spec     map[string]string `json:"spec"`
	Interval int               `json:"interval"`
	Enabled  bool              `json:"enabled"`
}

type templateMapping struct {
	Query            int    `json:"query"` // Index in Queries, -1 = Quellwert direkt
	SourceRef        string `json:"source_ref,omitempty"`
	Column           string `json:"column,omitempty"`
	Name             string `json:"name"`
	Note             string `json:"note"`
	InfrastructureID string `json:"infrastructure_id"`
	ImportMappingID  string `json:"import_mapping_id,omitempty"` // Export
	FieldName        string `json:"field_name,omitempty"`        // Export
}

type templatePayload struct {
	Queries  []templateQuery   `json:"queries,omitempty"`
	Mappings []templateMapping `json:"mappings"`
}

type ownTemplateView struct {
	ID, Name, Description, CreatedBy, CreatedAt string
	Queries, Mappings                           int
}

func (h *Handler) ownTemplates(ctx context.Context, direction, kind string) ([]ownTemplateView, error) {
	rows, err := h.db.Query(ctx, `
		SELECT t.id::text, t.name, t.description, t.payload, COALESCE(u.first_name || ' ' || u.last_name, ''), to_char(t.created_at, 'DD.MM.YYYY')
		FROM connection_templates t LEFT JOIN users u ON u.id = t.created_by
		WHERE t.direction = $1 AND t.kind = $2 ORDER BY t.name`, direction, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ownTemplateView
	for rows.Next() {
		var v ownTemplateView
		var payload []byte
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &payload, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		var p templatePayload
		_ = json.Unmarshal(payload, &p)
		v.Queries, v.Mappings = len(p.Queries), len(p.Mappings)
		list = append(list, v)
	}
	return list, rows.Err()
}

// saveConnectionTemplate speichert Abfragen und Zuordnungen als Vorlage.
func (h *Handler) saveConnectionTemplate(ctx context.Context, id, direction, name, desc, userID string) error {
	_, kind, _, _, err := h.connectionRow(ctx, id, direction)
	if err != nil && !isConnSecretErr(err) { // nur Metadaten nötig
		return errors.New("Verbindung nicht gefunden")
	}
	var p templatePayload
	if direction == "import" {
		qs, err := h.loadConnQueries(ctx, id)
		if err != nil {
			return err
		}
		index := map[string]int{}
		for i, q := range qs {
			index[q.ID] = i
			p.Queries = append(p.Queries, templateQuery{Name: q.Name, Spec: q.Spec, Interval: q.IntervalMinutes, Enabled: q.Enabled})
		}
		ms, err := h.importMappings(ctx, id)
		if err != nil {
			return err
		}
		for _, m := range ms {
			tm := templateMapping{Query: -1, Name: m.Name, Note: m.Note, InfrastructureID: m.InfrastructureID}
			if qid, col, ok := parseQueryRef(m.SourceRef); ok {
				i, known := index[qid]
				if !known {
					continue
				}
				tm.Query, tm.Column = i, col
			} else {
				tm.SourceRef = m.SourceRef
			}
			p.Mappings = append(p.Mappings, tm)
		}
	} else {
		ms, err := h.exportMappings(ctx, id)
		if err != nil {
			return err
		}
		for _, m := range ms {
			p.Mappings = append(p.Mappings, templateMapping{Query: -1, ImportMappingID: m.ImportMappingID, FieldName: m.FieldName, Note: m.Note, Name: m.SourceName})
		}
	}
	if len(p.Queries) == 0 && len(p.Mappings) == 0 {
		return errors.New("Die Verbindung hat noch keine Abfragen oder Zuordnungen – nichts zu speichern")
	}
	b, _ := json.Marshal(p)
	_, err = h.db.Exec(ctx, `INSERT INTO connection_templates (direction, kind, name, description, payload, created_by) VALUES ($1, $2, $3, $4, $5, $6)`,
		direction, kind, name, desc, b, nullID(userID))
	return err
}

// applyConnectionTemplate uebertraegt eine Vorlage auf eine Verbindung
// desselben Typs. Bereits vorhandene Abfragen (gleicher Name) und
// Zuordnungen (gleicher Quellwert und Name) werden nicht doppelt angelegt.
// Zuordnungen an Anlagen, an denen die Person nicht arbeiten darf (allow,
// Abteilung der Anlage), werden übersprungen und gezählt.
func (h *Handler) applyConnectionTemplate(ctx context.Context, id, direction, templateID, userID string, allow assetGuard) (string, error) {
	_, kind, _, _, err := h.connectionRow(ctx, id, direction)
	if err != nil && !isConnSecretErr(err) { // nur Metadaten nötig
		return "", errors.New("Verbindung nicht gefunden")
	}
	var tkind string
	var payload []byte
	if err := h.db.QueryRow(ctx, `SELECT kind, payload FROM connection_templates WHERE id = $1 AND direction = $2`, templateID, direction).Scan(&tkind, &payload); err != nil {
		return "", errors.New("Vorlage nicht gefunden")
	}
	if tkind != kind {
		return "", errors.New("Die Vorlage gehört zu einem anderen Verbindungstyp")
	}
	var p templatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	if direction == "export" {
		added, skipped, denied := 0, 0, 0
		for _, m := range p.Mappings {
			asset, err := h.exportSourceAsset(ctx, m.ImportMappingID)
			if err != nil {
				skipped++
				continue
			}
			if !allow(asset) {
				denied++
				continue
			}
			tag, err := h.db.Exec(ctx, `
				INSERT INTO export_mappings (connection_id, import_mapping_id, field_name, note, created_by)
				SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::uuid
				WHERE NOT EXISTS (SELECT 1 FROM export_mappings WHERE connection_id = $1::uuid AND import_mapping_id = $2::uuid AND field_name = $3::text)`,
				id, m.ImportMappingID, m.FieldName, m.Note, nullID(userID))
			if err == nil && tag.RowsAffected() > 0 {
				added++
			}
		}
		msg := fmt.Sprintf("%d Feld(er) übernommen", added)
		if skipped > 0 {
			msg += fmt.Sprintf(", %d übersprungen (Quellwert gibt es nicht mehr)", skipped)
		}
		if denied > 0 {
			msg += fmt.Sprintf(", %d ohne Berechtigung für die Anlage übersprungen", denied)
		}
		return msg, nil
	}

	existing, _ := h.loadConnQueries(ctx, id)
	byName := map[string]string{}
	for _, q := range existing {
		byName[strings.ToLower(q.Name)] = q.ID
	}
	newIDs := make([]string, len(p.Queries))
	qAdded := 0
	for i, q := range p.Queries {
		if qid, ok := byName[strings.ToLower(q.Name)]; ok {
			newIDs[i] = qid
			continue
		}
		spec, err := normalizeQuerySpec(kind, q.Spec)
		if err != nil {
			continue
		}
		b, _ := json.Marshal(spec)
		if err := h.db.QueryRow(ctx, `
			INSERT INTO connection_queries (connection_id, name, spec, interval_minutes, enabled, sort, created_by)
			VALUES ($1, $2, $3, $4, $5, (SELECT COALESCE(MAX(sort), 0) + 1 FROM connection_queries WHERE connection_id = $1), $6) RETURNING id::text`,
			id, q.Name, b, q.Interval, q.Enabled, nullID(userID)).Scan(&newIDs[i]); err == nil {
			qAdded++
		}
	}
	mAdded, mSkipped, mDenied := 0, 0, 0
	for _, m := range p.Mappings {
		if !uuidInPathRe.MatchString(m.InfrastructureID) || !allow(m.InfrastructureID) {
			mDenied++
			continue
		}
		ref := m.SourceRef
		if m.Query >= 0 {
			if m.Query >= len(newIDs) || newIDs[m.Query] == "" {
				mSkipped++
				continue
			}
			ref = queryRef(newIDs[m.Query], m.Column)
		}
		tag, err := h.db.Exec(ctx, `
			INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by)
			SELECT $1::uuid, $2::text, i.id, $4::text, $5::text, $6::uuid FROM infrastructure i
			WHERE i.id::text = $3::text
			  AND NOT EXISTS (SELECT 1 FROM import_mappings WHERE connection_id = $1::uuid AND source_ref = $2::text AND name = $4::text)`,
			id, ref, m.InfrastructureID, m.Name, m.Note, nullID(userID))
		if err != nil || tag.RowsAffected() == 0 {
			mSkipped++
			continue
		}
		mAdded++
	}
	h.reconcileQueryPolls(ctx, id)
	h.reconcileMqttConsumer(id)
	msg := fmt.Sprintf("%d Abfrage(n) und %d Zuordnung(en) übernommen", qAdded, mAdded)
	if mSkipped > 0 {
		msg += fmt.Sprintf(", %d Zuordnung(en) übersprungen (schon vorhanden oder Anlage gelöscht)", mSkipped)
	}
	if mDenied > 0 {
		msg += fmt.Sprintf(", %d ohne Berechtigung für die Anlage übersprungen", mDenied)
	}
	return msg, nil
}
