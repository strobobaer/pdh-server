package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"pdh/internal/modules/faults"
)

// Copilot auf einer Verbindungsseite (Import/Export): zusätzliche Werkzeuge
// zum Lesen der Verbindung, ihrer Quellwerte, des Anlagenbaums und der
// vorhandenen Zuordnungen – und propose_mappings, das eine Änderungsliste als
// Vorschlag ablegt (copilot_proposals.go). Angelegt wird hier nichts; das
// macht erst „Übernehmen“ in der Seitenleiste. Kennwörter und Schlüssel der
// Verbindung gehen nie an den Copilot (redactSecrets).

// copilotConnRe: Feld conn von /copilot/ask – "import:<uuid>" oder "export:<uuid>".
var copilotConnRe = regexp.MustCompile(`^(import|export):([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// copilotConn ist die Verbindung, auf deren Seite gefragt wird.
type copilotConn struct {
	Direction, ID, Kind, Name string
	Enabled                   bool
	Config                    map[string]string // entschlüsselt – nur zum Lesen der Quellen, nie ausgeben
	CanWrite                  bool
	Allow                     assetGuard
	Proposal                  *mappingProposal // Ergebnis von propose_mappings
}

func copilotConnTools(direction string, canWrite bool) []faults.CopilotTool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	limit := func(max int) map[string]any {
		return map[string]any{"type": "integer", "description": fmt.Sprintf("höchstens so viele Zeilen (max. %d)", max)}
	}
	tools := []faults.CopilotTool{
		{Name: "connection_info", Description: "Die Verbindung dieser Seite: Art, Einstellungen (ohne Kennwörter), Abfragen mit letztem Ergebnis und die letzte Prüfung. Für Fragen wie „warum kommt nichts an?“.",
			Properties: map[string]any{}},
		{Name: "asset_tree", Description: "Anlagenbaum mit ID und Pfad („Werk › Halle 2 › Presse 3“). Die ID wird für propose_mappings gebraucht.",
			Properties: map[string]any{"query": str("Wörter aus Name oder Pfad; leer = alle"), "limit": limit(300)}},
		{Name: "mappings", Description: "Die Zuordnungen, die es an dieser Verbindung schon gibt (mit ID, Anlage, Bezeichnung, letztem Wert).",
			Properties: map[string]any{}},
	}
	if direction == "import" {
		tools = append(tools, faults.CopilotTool{Name: "source_values", Description: "Quellwerte der Verbindung, die noch nicht zugeordnet sind: Themen, Spalten, Knoten oder Abfragespalten mit Kennung (ref), lesbarem Namen und Beispielwert.",
			Properties: map[string]any{"query": str("Wörter aus dem Namen; leer = alle"), "limit": limit(maxBulkMappings)}})
	} else {
		tools = append(tools, faults.CopilotTool{Name: "export_sources", Description: "Importwerte, die als Feld in diesen Export aufgenommen werden können (ID, Anlage, Bezeichnung, letzter Wert, schon exportiert ja/nein).",
			Properties: map[string]any{"query": str("Wörter aus Anlage oder Bezeichnung; leer = alle"), "limit": limit(maxBulkMappings)}})
	}
	if canWrite {
		rowProps := map[string]any{
			"op":                map[string]any{"type": "string", "enum": []any{proposalOpAdd, proposalOpChange, proposalOpKeep, proposalOpDelete}, "description": "add = neu, change = ändern, keep = bleibt, delete = nur nennen (löschen geht nur von Hand)"},
			"source_ref":        str("Import, add: Kennung des Quellwerts (ref aus source_values; MQTT-Thema, Modbus z. B. holding:1:float32, OPC-UA-Knoten)"),
			"import_mapping_id": str("Export, add: ID des Importwerts aus export_sources"),
			"mapping_id":        str("change/keep/delete: ID der vorhandenen Zuordnung aus mappings"),
			"infrastructure_id": str("Import: ID der Anlage aus asset_tree"),
			"name":              str("Import: Bezeichnung des Werts (2 bis 150 Zeichen)"),
			"note":              str("Notiz, z. B. „Einheit °C“ (höchstens 500 Zeichen)"),
			"field_name":        str("Export: Name des Zielfelds (2 bis 150 Zeichen)"),
			"reason":            str("kurze Begründung für die Person"),
			"source_url":        str("Link zur Vorlage aus dem Internet, falls die Zeile daher stammt"),
		}
		tools = append(tools, faults.CopilotTool{Name: "propose_mappings", Description: "Legt eine Änderungsliste als Vorschlag ab. Es wird nichts angelegt – die Person sieht die Liste in der Seitenleiste und drückt „Übernehmen“. Das Ergebnis nennt, welche Zeilen das PDH nicht übernehmen kann und warum.",
			Properties: map[string]any{
				"summary": str("ein Satz, was der Vorschlag tut"),
				"rows":    map[string]any{"type": "array", "description": "höchstens 500 Zeilen", "items": map[string]any{"type": "object", "properties": rowProps, "required": []string{"op"}}},
			}, Required: []string{"summary", "rows"}})
	}
	return tools
}

// redactSecrets ersetzt jedes Geheimnis der Verbindung im Text – auch wenn es
// in einer Fehlermeldung oder Adresse steckt.
func redactSecrets(text string, config map[string]string) string {
	var secrets []string
	for k, v := range config {
		if connSecretFields[strings.TrimSuffix(k, "_enc")] && len(strings.TrimSpace(v)) >= 4 {
			secrets = append(secrets, v, strings.TrimSpace(v))
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, "••••")
	}
	return text
}

// secretLikeKey: Schlüssel einer Abfrage, deren Wert nicht gezeigt wird.
func secretLikeKey(k string) bool {
	k = strings.ToLower(k)
	for _, part := range []string{"pass", "token", "secret", "key", "auth"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// connectionInfoText beschreibt die Verbindung für den Copilot – ohne
// Kennwörter: Geheimfelder erscheinen nur als „gesetzt“ oder „leer“.
func connectionInfoText(name, kind string, enabled bool, config map[string]string, check *checkReport, queries []connQuery) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Verbindung: %s\nArt: %s\nAktiv: %s\n\nEinstellungen:\n", name, kind, map[bool]string{true: "ja", false: "nein"}[enabled])
	keys := make([]string, 0, len(config))
	secretSet := map[string]bool{}
	for k, v := range config {
		base := strings.TrimSuffix(k, "_enc")
		if connSecretFields[base] {
			secretSet[base] = secretSet[base] || strings.TrimSpace(v) != ""
			continue
		}
		if strings.TrimSpace(v) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %s\n", firstNonEmpty(connFieldLabels[k], k), config[k])
	}
	secretKeys := make([]string, 0, len(secretSet))
	for k := range secretSet {
		secretKeys = append(secretKeys, k)
	}
	sort.Strings(secretKeys)
	for _, k := range secretKeys {
		fmt.Fprintf(&b, "- %s: %s\n", firstNonEmpty(connFieldLabels[k], k), map[bool]string{true: "gesetzt", false: "leer"}[secretSet[k]])
	}
	if len(queries) > 0 {
		b.WriteString("\nAbfragen:\n")
		for _, q := range queries {
			state := "noch nicht gelaufen"
			if q.Ran() {
				state = map[bool]string{true: "zuletzt in Ordnung", false: "zuletzt fehlgeschlagen"}[q.Succeeded()]
			}
			fmt.Fprintf(&b, "- %s (ID %s, alle %d min, %s, %d Zeilen", q.Name, q.ID, q.IntervalMinutes, state, q.LastRows)
			if q.LastRunAt != "" {
				b.WriteString(", " + q.LastRunAt)
			}
			b.WriteString(")")
			if !q.Enabled {
				b.WriteString(" – ausgeschaltet")
			}
			if q.LastMessage != "" {
				b.WriteString(" – Meldung: " + q.LastMessage)
			}
			b.WriteString("\n")
			specKeys := make([]string, 0, len(q.Spec))
			for k, v := range q.Spec {
				if strings.TrimSpace(v) != "" && !secretLikeKey(k) {
					specKeys = append(specKeys, k)
				}
			}
			sort.Strings(specKeys)
			for _, k := range specKeys {
				fmt.Fprintf(&b, "  %s: %s\n", k, shortText(q.Spec[k], 400))
			}
		}
	}
	if check != nil {
		fmt.Fprintf(&b, "\nLetzte Prüfung (%s): %d in Ordnung, %d Warnungen, %d Fehler\n", check.At, check.OK, check.Warn, check.Fail)
		for _, it := range check.Items {
			if it.Status == "ok" {
				continue
			}
			fmt.Fprintf(&b, "- [%s] %s › %s: %s\n", it.Status, it.Group, it.Name, it.Detail)
		}
	} else {
		b.WriteString("\nLetzte Prüfung: noch keine (Knopf „Verbindung prüfen“ auf der Seite).\n")
	}
	return redactSecrets(b.String(), config)
}

func shortText(s string, max int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// safeHTTPURL lässt nur http(s)-Adressen durch (Links in der Änderungsliste).
func safeHTTPURL(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if len(s) > 500 || strings.ContainsAny(s, " \t\r\n\"'<>") || !(strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "http://")) {
		return ""
	}
	return s
}

// proposalRowsFromInput liest die Zeilen aus der Eingabe von propose_mappings.
// Gültigkeit und „übernommen“ setzt nur das PDH, nie der Copilot.
func proposalRowsFromInput(in map[string]any) ([]proposalRow, error) {
	arr, _ := in["rows"].([]any)
	if len(arr) > maxBulkMappings {
		return nil, errors.New(errProposalTooLarge)
	}
	rows := make([]proposalRow, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
		reason := str("reason")
		if r := []rune(reason); len(r) > 300 {
			reason = string(r[:299]) + "…"
		}
		rows = append(rows, proposalRow{
			Op: strings.ToLower(str("op")), SourceRef: str("source_ref"), ImportMappingID: str("import_mapping_id"), MappingID: str("mapping_id"),
			InfrastructureID: str("infrastructure_id"), Name: str("name"), Note: str("note"), FieldName: str("field_name"),
			Reason: reason, SourceURL: safeHTTPURL(str("source_url")),
		})
	}
	if len(rows) == 0 {
		return nil, errors.New("Der Vorschlag enthält keine Zeilen")
	}
	return rows, nil
}

// matchWords: alle Wörter der Frage kommen im Text vor (leer = alles).
func matchWords(text string, ws []string) bool {
	text = strings.ToLower(text)
	for _, w := range ws {
		if !strings.Contains(text, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

// exec führt ein Werkzeug der Verbindungsseite aus. Zweiter Wert: das
// Werkzeug gehört hierher.
func (c *copilotConn) exec(ctx context.Context, d *copilotData, name string, in map[string]any) (string, bool, error) {
	out, known, err := c.run(ctx, d, name, in)
	if err != nil {
		err = errors.New(redactSecrets(err.Error(), c.Config))
	}
	return redactSecrets(out, c.Config), known, err
}

func (c *copilotConn) run(ctx context.Context, d *copilotData, name string, in map[string]any) (string, bool, error) {
	h := d.h
	query, _ := in["query"].(string)
	ws := strings.Fields(query)
	limit := func(def, max int) int {
		if f, ok := in["limit"].(float64); ok && int(f) >= 1 {
			if int(f) > max {
				return max
			}
			return int(f)
		}
		return def
	}
	switch name {
	case "connection_info":
		var check *checkReport
		var raw []byte
		if h.db.QueryRow(ctx, `SELECT last_check FROM import_export_connections WHERE id = $1::uuid`, c.ID).Scan(&raw) == nil && len(raw) > 0 {
			var rep checkReport
			if json.Unmarshal(raw, &rep) == nil {
				check = &rep
			}
		}
		var queries []connQuery
		if c.Direction == "import" {
			queries, _ = h.loadConnQueries(ctx, c.ID)
		}
		return connectionInfoText(c.Name, c.Kind, c.Enabled, c.Config, check, queries), true, nil

	case "asset_tree":
		assets, err := h.assetList(ctx)
		if err != nil {
			return "", true, err
		}
		type row struct {
			ID   string `json:"id"`
			Pfad string `json:"pfad"`
		}
		max := limit(100, 300)
		list := []row{}
		more := 0
		for _, a := range assets {
			if !matchWords(a.Path, ws) {
				continue
			}
			if len(list) >= max {
				more++
				continue
			}
			list = append(list, row{a.ID, a.Path})
		}
		out, err := jsonOut(map[string]any{"anlagen": list, "weitere": more})
		return out, true, err

	case "mappings":
		if c.Direction == "import" {
			ms, err := h.importMappings(ctx, c.ID)
			if err != nil {
				return "", true, err
			}
			type row struct {
				ID       string `json:"mapping_id"`
				Ref      string `json:"source_ref"`
				AnlageID string `json:"infrastructure_id"`
				Anlage   string `json:"anlage"`
				Name     string `json:"name"`
				Notiz    string `json:"note,omitempty"`
				Wert     string `json:"letzter_wert,omitempty"`
				Zeit     string `json:"empfangen,omitempty"`
			}
			list := make([]row, 0, len(ms))
			for _, m := range ms {
				list = append(list, row{m.ID, m.SourceRef, m.InfrastructureID, m.InfrastructureName, m.Name, m.Note, shortSample(m.LastValue), m.LastReceivedAt})
			}
			out, err := jsonOut(map[string]any{"zuordnungen": list})
			return out, true, err
		}
		ms, err := h.exportMappings(ctx, c.ID)
		if err != nil {
			return "", true, err
		}
		type row struct {
			ID      string `json:"mapping_id"`
			WertID  string `json:"import_mapping_id"`
			Anlage  string `json:"anlage"`
			Wert    string `json:"wert"`
			Feld    string `json:"field_name"`
			Notiz   string `json:"note,omitempty"`
			Zuletzt string `json:"zuletzt_exportiert,omitempty"`
		}
		list := make([]row, 0, len(ms))
		for _, m := range ms {
			list = append(list, row{m.ID, m.ImportMappingID, m.InfrastructureName, m.SourceName, m.FieldName, m.Note, m.LastExportedAt})
		}
		out, err := jsonOut(map[string]any{"felder": list})
		return out, true, err

	case "source_values":
		if c.Direction != "import" {
			return "", true, errors.New("Quellwerte gibt es nur bei Import-Verbindungen – für den Export export_sources nutzen")
		}
		set := h.mappingSources(ctx, c.ID, c.Kind, c.Config)
		type row struct {
			Ref      string `json:"ref"`
			Name     string `json:"name"`
			Beispiel string `json:"beispiel,omitempty"`
		}
		max := limit(200, maxBulkMappings)
		list := []row{}
		more := 0
		for _, s := range set.Sources {
			if !matchWords(s.Label+" "+s.Ref, ws) {
				continue
			}
			if len(list) >= max {
				more++
				continue
			}
			list = append(list, row{s.Ref, s.Label, s.Sample})
		}
		out, err := jsonOut(map[string]any{"quellwerte": list, "weitere": more, "hinweise": set.Hints,
			"erklaerung": "Nur Quellwerte, die noch nicht zugeordnet sind. Schon Zugeordnetes zeigt mappings."})
		return out, true, err

	case "export_sources":
		if c.Direction != "export" {
			return "", true, errors.New("Importwerte zum Exportieren gibt es nur bei Export-Verbindungen")
		}
		sources, err := h.availableExportSources(ctx)
		if err != nil {
			return "", true, err
		}
		existing, err := h.exportMappings(ctx, c.ID)
		if err != nil {
			return "", true, err
		}
		exported := map[string]bool{}
		for _, e := range existing {
			exported[e.ImportMappingID] = true
		}
		type row struct {
			ID         string `json:"import_mapping_id"`
			Anlage     string `json:"anlage"`
			Name       string `json:"name"`
			Notiz      string `json:"note,omitempty"`
			Wert       string `json:"letzter_wert,omitempty"`
			Verbindung string `json:"verbindung,omitempty"`
			Exportiert bool   `json:"schon_feld"`
		}
		max := limit(200, maxBulkMappings)
		list := []row{}
		more := 0
		for _, s := range sources {
			if !matchWords(s.InfrastructureName+" "+s.Name+" "+s.Note, ws) {
				continue
			}
			if len(list) >= max {
				more++
				continue
			}
			list = append(list, row{s.ID, s.InfrastructureName, s.Name, s.Note, shortSample(s.LastValue), s.ConnectionName, exported[s.ID]})
		}
		out, err := jsonOut(map[string]any{"importwerte": list, "weitere": more})
		return out, true, err

	case "propose_mappings":
		if !c.CanWrite {
			return "", true, errors.New("Die Person darf an dieser Verbindung nichts ändern – nur erklären, was zu tun wäre")
		}
		rows, err := proposalRowsFromInput(in)
		if err != nil {
			return "", true, err
		}
		summary, _ := in["summary"].(string)
		p := &mappingProposal{UserID: d.userID, ConnectionID: c.ID, Direction: c.Direction, Summary: shortText(summary, 300), Rows: rows}
		if err := h.checkProposalRows(ctx, p, c.Allow); err != nil {
			return "", true, err
		}
		if err := h.saveProposal(ctx, p); err != nil {
			return "", true, errors.New("Der Vorschlag konnte nicht gespeichert werden")
		}
		c.Proposal = p
		type problem struct {
			Zeile  int    `json:"zeile"`
			Grund  string `json:"grund"`
			Quelle string `json:"quelle,omitempty"`
		}
		ready := 0
		problems := []problem{}
		for i, r := range p.Rows {
			switch {
			case r.open():
				ready++
			case r.Problem != "":
				problems = append(problems, problem{i, r.Problem, firstNonEmpty(r.SourceRef, r.MappingID, r.ImportMappingID)})
			}
		}
		out, err := jsonOut(map[string]any{
			"gespeichert": true, "uebernehmbar": ready, "nicht_uebernehmbar": problems,
			"hinweis": "Noch ist nichts angelegt. Die Person sieht die Änderungsliste unter dieser Antwort und drückt „Übernehmen“.",
		})
		return out, true, err
	}
	return "", false, nil
}

// copilotConnPrompt: Regeln für den Copilot auf einer Verbindungsseite.
func copilotConnPrompt(c *copilotConn, web bool) string {
	what := map[string]string{"import": "Import-Verbindung", "export": "Export-Verbindung"}[c.Direction]
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nDie Person ist auf der Seite der %s „%s“ (Art %s). Fragen ohne andere Angabe meinen diese Verbindung.\n", what, c.Name, c.Kind)
	b.WriteString("Zusätzliche Regeln für diese Seite:\n")
	b.WriteString("- Lies mit connection_info, mappings, asset_tree")
	if c.Direction == "import" {
		b.WriteString(" und source_values")
	} else {
		b.WriteString(" und export_sources")
	}
	b.WriteString(", bevor du etwas vorschlägst. Erfinde keine Quellwerte, Anlagen oder IDs.\n")
	if c.CanWrite {
		b.WriteString("- Zuordnungen legst du nie selbst an. Änderungen gehen nur über propose_mappings: eine Zeile je Zuordnung mit kurzer Begründung. Danach fasst du in zwei, drei Sätzen zusammen und verweist auf den Knopf „Übernehmen“ in der Änderungsliste unter deiner Antwort.\n")
		b.WriteString("- Nennt propose_mappings Zeilen, die nicht übernommen werden können, sag der Person den Grund.\n")
		b.WriteString("- Löschen kannst du nur nennen (op delete) – gelöscht wird von Hand auf der Seite.\n")
	} else {
		b.WriteString("- Die Person darf an dieser Verbindung nichts ändern. Erkläre, was zu tun wäre; anlegen muss es jemand mit dem Recht zum Bearbeiten der Verbindungen.\n")
	}
	b.WriteString("- Ist etwas mehrdeutig (zwei passende Anlagen, unklare Einheit), frag zurück, statt zu raten.\n")
	b.WriteString("- Kennwörter, Schlüssel und Tokens erfragst und nennst du nie. Fehlt ein Kennwort, verweise auf „Verbindung bearbeiten“.\n")
	if web {
		b.WriteString("- Für Vorlagen (z. B. Registertabelle eines Zählers, Themen eines Geräts) darfst du hier die Websuche nutzen. Nenne die Quelle als Link und trage sie bei den Zeilen in source_url ein. Angaben aus dem Internet sind Vorschläge – die Person prüft sie vor dem Übernehmen.\n")
	}
	return b.String()
}

// proposalView: Änderungsliste für die Seitenleiste (base.gohtml, copRenderProposal).
func proposalView(p *mappingProposal) map[string]any {
	rows := make([]map[string]any, 0, len(p.Rows))
	for _, r := range p.Rows {
		rows = append(rows, map[string]any{
			"op": r.Op, "label": firstNonEmpty(r.Label, r.SourceRef, r.MappingID), "asset": r.AssetPath, "name": r.Name, "note": r.Note,
			"field": r.FieldName, "reason": r.Reason, "url": safeHTTPURL(r.SourceURL), "valid": r.Valid, "problem": r.Problem, "applied": r.Applied,
		})
	}
	return map[string]any{"id": p.ID, "direction": p.Direction, "summary": p.Summary, "rows": rows}
}
