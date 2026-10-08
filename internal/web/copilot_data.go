package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pdh/internal/modules/faults"
)

// Copilot-Werkzeuge: lesender Zugriff auf die PDH-Daten mit den Rechten der
// fragenden Person (Modulrechte *.view und Abteilungs-Sicht wie in den Listen).
// Es wird nichts geaendert und nichts ausserhalb des PDH abgefragt.

type copilotKind struct {
	Key, Label, Table, ScopeRef, Perm, TitleCol, URL, ActionsTable, ActionsFK, PrioCol string
	Archived                                                                           bool
}

var copilotKinds = []copilotKind{
	{"fault", "Störung", "faults", "fault", "faults.view", "title", "/faults/", "fault_actions", "fault_id", "severity", true},
	{"ticket", "Ticket", "tickets", "ticket", "tickets.view", "title", "/tickets/", "ticket_actions", "ticket_id", "priority", true},
	{"task", "Aufgabe", "tasks", "task", "tasks.view", "title", "/tasks/", "task_actions", "task_id", "priority", true},
	{"maintenance", "Wartungsauftrag", "maintenance_tasks", "maintenance_task", "maintenance.view", "title", "/maintenance/tasks/", "maintenance_task_actions", "task_id", "priority", true},
	{"project", "Projekt", "projects", "project", "", "name", "/projects/", "", "", "", false},
}

func copilotKindByKey(k string) (copilotKind, bool) {
	for _, c := range copilotKinds {
		if c.Key == k {
			return c, true
		}
	}
	return copilotKind{}, false
}

const copilotOpenCond = `r.status::text NOT IN ('resolved','closed','done','completed','skipped','archive')`

type copilotData struct {
	h      *Handler
	userID string
	scope  *deptScope
	can    func(string) bool
}

func (d *copilotData) allowed(k copilotKind) bool { return k.Perm == "" || d.can(k.Perm) }

// where: Grundbedingungen (Archiv, Abteilungs-Sicht) ab Platzhalter n.
func (d *copilotData) where(k copilotKind, n int) (string, []any) {
	w := " WHERE TRUE"
	if k.Archived {
		w += " AND r.archived_at IS NULL"
	}
	s, args := scopeSQL(d.scope, k.ScopeRef, "r", n)
	return w + s, args
}

func jsonOut(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	s := string(b)
	if len(s) > 24000 { // genug fuer die Antwort, ohne das Kontextfenster zu fluten
		s = s[:24000] + `…(gekürzt)`
	}
	return s, nil
}

// assetSubtreeIDs: Anlagen, deren Name passt, samt allen Unteranlagen.
func (d *copilotData) assetSubtreeIDs(ctx context.Context, name string) []string {
	rows, err := d.h.db.Query(ctx, `
		WITH RECURSIVE sub AS (
			SELECT id FROM infrastructure WHERE name ILIKE $1 AND hidden_at IS NULL
			UNION SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id
		) SELECT id::text FROM sub LIMIT 500`, "%"+strings.TrimSpace(name)+"%")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

const assetPathSQL = `(WITH RECURSIVE up AS (
	SELECT a.id, a.parent_id, a.name::text AS path FROM infrastructure a WHERE a.id = %s
	UNION ALL SELECT p.id, p.parent_id, p.name || ' › ' || up.path FROM infrastructure p JOIN up ON p.id = up.parent_id
) SELECT path FROM up WHERE parent_id IS NULL LIMIT 1)`

func assetPathExpr(col string) string { return fmt.Sprintf(assetPathSQL, col) }

// ── Werkzeuge ────────────────────────────────────────────────

func copilotTools() []faults.CopilotTool {
	kinds := []any{"fault", "ticket", "task", "maintenance", "project", "asset", "part"}
	work := []any{"fault", "ticket", "task", "maintenance", "project"}
	return []faults.CopilotTool{
		{Name: "search", Description: "Volltextsuche im PDH über Störungen (fault), Tickets, Aufgaben (task), Wartungsaufträge (maintenance), Projekte, Anlagen (asset) und Ersatzteile (part). Sucht in Titel, Beschreibung, Lösung und Ursache. Alle Wörter müssen vorkommen. Für ähnliche frühere Fälle, Lösungen und Fundstellen.",
			Properties: map[string]any{
				"query": map[string]any{"type": "string", "description": "Suchbegriffe, z. B. 'Hydraulik Presse 3'"},
				"types": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": kinds}, "description": "Nur diese Arten; leer = alle"},
				"limit": map[string]any{"type": "integer", "description": "höchstens so viele Treffer je Art (Standard 10, max. 30)"},
			}, Required: []string{"query"}},
		{Name: "get_record", Description: "Details eines Datensatzes: Beschreibung, Status, Priorität, Anlage mit Pfad, Beteiligte, Termine, Lösung/Ursache und die letzten Maßnahmen bzw. bei Ersatzteilen Bestand je Lagerplatz und Reservierungen, bei Anlagen Stammdaten.",
			Properties: map[string]any{
				"type": map[string]any{"type": "string", "enum": kinds},
				"id":   map[string]any{"type": "string", "description": "ID (UUID) aus einem anderen Werkzeug"},
			}, Required: []string{"type", "id"}},
		{Name: "list_records", Description: "Liste von Vorgängen mit Filtern – z. B. offene Störungen einer Anlage, überfällige Aufgaben, meine Tickets, was in den letzten 30 Tagen angelegt wurde.",
			Properties: map[string]any{
				"type":       map[string]any{"type": "string", "enum": work},
				"status":     map[string]any{"type": "string", "enum": []any{"open", "closed", "all"}, "description": "Standard open"},
				"asset":      map[string]any{"type": "string", "description": "Name (oder Teil davon) einer Anlage – Unteranlagen zählen mit"},
				"since_days": map[string]any{"type": "integer", "description": "nur in den letzten N Tagen angelegt"},
				"mine":       map[string]any{"type": "boolean", "description": "nur, wo die fragende Person zugewiesen oder verantwortlich ist"},
				"overdue":    map[string]any{"type": "boolean", "description": "nur überfällige (Fälligkeit überschritten, nicht erledigt)"},
				"limit":      map[string]any{"type": "integer", "description": "Standard 20, max. 50"},
			}, Required: []string{"type"}},
		{Name: "asset_overview", Description: "Überblick über eine Anlage: Pfad, Stammdaten, Unteranlagen, offene Störungen/Tickets/Wartungen, letzte gelöste Störungen mit Lösung, anstehende Wartungen, aktuelle Messwerte aus dem Import und eingebaute Ersatzteile.",
			Properties: map[string]any{"asset": map[string]any{"type": "string", "description": "Name, Teil des Namens oder ID der Anlage"}}, Required: []string{"asset"}},
		{Name: "part_stock", Description: "Ersatzteile mit Bestand: frei, reserviert, Mindestbestand, Lagerplätze, Hersteller-Teilenummer. Für Fragen wie 'Haben wir noch …?' oder 'Wo liegt …?'.",
			Properties: map[string]any{
				"query":     map[string]any{"type": "string", "description": "Name, Teilenummer oder Hersteller-Teilenummer; leer mit below_min = alle unter Mindestbestand"},
				"below_min": map[string]any{"type": "boolean", "description": "nur Teile unter Mindestbestand"},
			}},
		{Name: "statistics", Description: "Zahlen: Anzahl Vorgänge je Anlage, Status, Monat oder Schweregrad/Priorität im Zeitraum, dazu bei Störungen die mittlere Dauer bis zur Lösung.",
			Properties: map[string]any{
				"type":       map[string]any{"type": "string", "enum": []any{"fault", "ticket", "task", "maintenance"}},
				"group_by":   map[string]any{"type": "string", "enum": []any{"asset", "status", "month", "priority"}},
				"since_days": map[string]any{"type": "integer", "description": "Zeitraum in Tagen, Standard 90"},
				"asset":      map[string]any{"type": "string", "description": "optional: nur diese Anlage (mit Unteranlagen)"},
			}, Required: []string{"type", "group_by"}},
		{Name: "import_values", Description: "Aktuelle Messwerte und Zählerstände aus den Import-Verbindungen (z. B. Temperatur, Stückzahl, Störcode einer Maschine) mit Zeitpunkt.",
			Properties: map[string]any{"query": map[string]any{"type": "string", "description": "Bezeichnung des Werts oder Name der Anlage; leer = alle"}}},
		{Name: "due_maintenance", Description: "Wartungsaufträge, die in den nächsten N Tagen fällig oder bereits überfällig sind.",
			Properties: map[string]any{"days": map[string]any{"type": "integer", "description": "Standard 14"}, "asset": map[string]any{"type": "string"}}},
	}
}

func (d *copilotData) exec(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	str := func(k string) string { s, _ := in[k].(string); return strings.TrimSpace(s) }
	num := func(k string, def, lo, hi int) int {
		if f, ok := in[k].(float64); ok {
			n := int(f)
			if n < lo {
				return lo
			}
			if n > hi {
				return hi
			}
			return n
		}
		return def
	}
	flag := func(k string) bool { b, _ := in[k].(bool); return b }
	switch name {
	case "search":
		var types []string
		if arr, ok := in["types"].([]any); ok {
			for _, t := range arr {
				if s, ok := t.(string); ok {
					types = append(types, s)
				}
			}
		}
		return d.search(ctx, str("query"), types, num("limit", 10, 1, 30))
	case "get_record":
		return d.getRecord(ctx, str("type"), str("id"))
	case "list_records":
		return d.listRecords(ctx, str("type"), firstNonEmpty(str("status"), "open"), str("asset"), num("since_days", 0, 0, 3650), flag("mine"), flag("overdue"), num("limit", 20, 1, 50))
	case "asset_overview":
		return d.assetOverview(ctx, str("asset"))
	case "part_stock":
		return d.partStock(ctx, str("query"), flag("below_min"))
	case "statistics":
		return d.statistics(ctx, str("type"), str("group_by"), num("since_days", 90, 1, 3650), str("asset"))
	case "import_values":
		return d.importValues(ctx, str("query"))
	case "due_maintenance":
		return d.dueMaintenance(ctx, num("days", 14, 0, 365), str("asset"))
	}
	return "", fmt.Errorf("unbekanntes Werkzeug %s", name)
}

func words(q string) []string {
	var out []string
	for _, w := range strings.Fields(q) {
		if w = strings.Trim(w, `"'.,;:!?()`); len([]rune(w)) >= 2 {
			out = append(out, w)
		}
		if len(out) == 8 {
			break
		}
	}
	return out
}

// likeAll: "<expr> ILIKE $n AND <expr> ILIKE $n+1 …" fuer jedes Wort.
func likeAll(expr string, ws []string, n int) (string, []any) {
	var parts []string
	var args []any
	for i, w := range ws {
		parts = append(parts, fmt.Sprintf("%s ILIKE $%d", expr, n+i+1))
		args = append(args, "%"+w+"%")
	}
	return strings.Join(parts, " AND "), args
}

func (d *copilotData) search(ctx context.Context, q string, types []string, limit int) (string, error) {
	ws := words(q)
	if len(ws) == 0 {
		return "", errors.New("Bitte Suchbegriffe angeben")
	}
	want := func(t string) bool {
		if len(types) == 0 {
			return true
		}
		for _, x := range types {
			if x == t {
				return true
			}
		}
		return false
	}
	type hit struct {
		Type, ID, Title, Status, Asset, Date, Snippet, URL string
	}
	var hits []hit
	for _, k := range copilotKinds {
		if !want(k.Key) || !d.allowed(k) {
			continue
		}
		text := "coalesce(r." + k.TitleCol + ",'') || ' ' || coalesce(r.description,'')"
		if k.Key == "fault" || k.Key == "ticket" || k.Key == "task" {
			text += " || ' ' || coalesce(r.resolution,'') || ' ' || coalesce(r.root_cause,'')"
		}
		if k.Key == "fault" {
			text += " || ' ' || coalesce(array_to_string(r.symptoms, ' '),'')"
		}
		if k.Key == "maintenance" {
			text += " || ' ' || coalesce(r.notes,'')"
		}
		text = "(" + text + " || ' ' || coalesce(i.name,''))"
		cond, args := likeAll(text, ws, 0)
		w, wargs := d.where(k, len(args))
		args = append(args, wargs...)
		snippet := "coalesce(r.description,'')"
		if k.Key == "fault" || k.Key == "ticket" || k.Key == "task" {
			snippet = "coalesce(NULLIF(r.resolution,''), r.description, '')"
		}
		rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT r.id::text, r.%s, r.status::text, coalesce(i.name,''), to_char(r.created_at,'DD.MM.YYYY'), left(%s, 240)
			FROM %s r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id %s AND %s ORDER BY r.created_at DESC LIMIT %d`,
			k.TitleCol, snippet, k.Table, w, cond, limit), args...)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var x hit
			if rows.Scan(&x.ID, &x.Title, &x.Status, &x.Asset, &x.Date, &x.Snippet) == nil {
				x.Type, x.URL = k.Key, k.URL+x.ID
				x.Status = statusLabel(x.Status)
				hits = append(hits, x)
			}
		}
		rows.Close()
	}
	if want("asset") {
		cond, args := likeAll("(name || ' ' || coalesce(description,'') || ' ' || coalesce(manufacturer,'') || ' ' || coalesce(model,'') || ' ' || coalesce(serial_no,''))", ws, 0)
		rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT id::text, name, coalesce(type::text,''), %s FROM infrastructure WHERE hidden_at IS NULL AND %s ORDER BY name LIMIT %d`, assetPathExpr("infrastructure.id"), cond, limit), args...)
		if err == nil {
			for rows.Next() {
				var x hit
				var typ string
				if rows.Scan(&x.ID, &x.Title, &typ, &x.Asset) == nil {
					x.Type, x.Status, x.URL = "asset", typ, "/infrastructure/"+x.ID
					hits = append(hits, x)
				}
			}
			rows.Close()
		}
	}
	if want("part") && d.can("inventory.view") {
		cond, args := likeAll("(name || ' ' || part_number || ' ' || coalesce(manufacturer_part,'') || ' ' || coalesce(description,'') || ' ' || coalesce(category,''))", ws, 0)
		rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT id::text, part_number || ' ' || name, stock_qty::text || ' ' || unit FROM spare_parts WHERE active AND hidden_at IS NULL AND %s ORDER BY name LIMIT %d`, cond, limit), args...)
		if err == nil {
			for rows.Next() {
				var x hit
				if rows.Scan(&x.ID, &x.Title, &x.Status) == nil {
					x.Type, x.URL, x.Status = "part", "/inventory/"+x.ID, "Bestand "+x.Status
					hits = append(hits, x)
				}
			}
			rows.Close()
		}
	}
	return jsonOut(map[string]any{"treffer": len(hits), "ergebnisse": hits})
}

func (d *copilotData) getRecord(ctx context.Context, typ, id string) (string, error) {
	if !uuidInPathRe.MatchString(id) {
		return "", errors.New("ungültige ID")
	}
	switch typ {
	case "asset":
		var name, kind, desc, loc, serial, man, model, path string
		err := d.h.db.QueryRow(ctx, `SELECT name, coalesce(type::text,''), coalesce(description,''), coalesce(location,''), coalesce(serial_no,''), coalesce(manufacturer,''), coalesce(model,''), `+assetPathExpr("$1::uuid")+`
			FROM infrastructure WHERE id = $1::uuid`, id).Scan(&name, &kind, &desc, &loc, &serial, &man, &model, &path)
		if err != nil {
			return "", errors.New("Anlage nicht gefunden")
		}
		return jsonOut(map[string]any{"name": name, "pfad": path, "typ": kind, "beschreibung": desc, "standort": loc, "seriennummer": serial, "hersteller": man, "modell": model, "url": "/infrastructure/" + id})
	case "part":
		if !d.can("inventory.view") {
			return "", errors.New("keine Berechtigung für Ersatzteile")
		}
		return d.partStock(ctx, id, false)
	}
	k, ok := copilotKindByKey(typ)
	if !ok || !d.allowed(k) {
		return "", errors.New("keine Berechtigung oder unbekannte Art")
	}
	if !d.h.recordInScope(ctx, d.scope, k.ScopeRef, id) {
		return "", errors.New("dieser Vorgang ist für die fragende Person nicht sichtbar")
	}
	extra := ", '' , ''"
	if k.Key == "fault" || k.Key == "ticket" || k.Key == "task" {
		extra = ", coalesce(r.resolution,''), coalesce(r.root_cause,'')"
	}
	prio := "''"
	if k.PrioCol != "" {
		prio = "coalesce(r." + k.PrioCol + "::text,'')"
	}
	due := "''"
	if k.Key != "project" {
		due = "coalesce(to_char(r.due_date,'DD.MM.YYYY'),'')"
	}
	var title, desc, status, pr, asset, created, assigned, responsible, creator, dueS, res, cause string
	q := fmt.Sprintf(`SELECT r.%s, coalesce(r.description,''), r.status::text, %s, coalesce(%s,''), to_char(r.created_at,'DD.MM.YYYY HH24:MI'),
		coalesce((SELECT first_name||' '||last_name FROM users WHERE id = r.assigned_to),''),
		coalesce((SELECT first_name||' '||last_name FROM users WHERE id = r.responsible_to),''),
		coalesce((SELECT first_name||' '||last_name FROM users WHERE id = r.created_by),''), %s %s
		FROM %s r WHERE r.id = $1::uuid`, k.TitleCol, prio, assetPathExpr("r.infrastructure_id"), due, extra, k.Table)
	if k.Key == "task" { // Aufgaben: Zugewiesene stehen in task_assignees
		q = strings.Replace(q, "(SELECT first_name||' '||last_name FROM users WHERE id = r.assigned_to)",
			"(SELECT string_agg(u.first_name||' '||u.last_name, ', ') FROM task_assignees ta JOIN users u ON u.id = ta.user_id WHERE ta.task_id = r.id)", 1)
	}
	if err := d.h.db.QueryRow(ctx, q, id).Scan(&title, &desc, &status, &pr, &asset, &created, &assigned, &responsible, &creator, &dueS, &res, &cause); err != nil {
		return "", fmt.Errorf("nicht gefunden: %w", err)
	}
	out := map[string]any{"art": k.Label, "titel": title, "beschreibung": desc, "status": statusLabel(status), "prioritaet": pr, "anlage": asset,
		"angelegt": created + " von " + creator, "zugewiesen": assigned, "verantwortlich": responsible, "faellig": dueS, "url": k.URL + id}
	if res != "" {
		out["loesung"] = res
	}
	if cause != "" {
		out["ursache"] = cause
	}
	// Störung ↔ Ticket: aus einer Störung erstelltes Ticket ist derselbe Vorgang
	var link string
	switch k.Key {
	case "fault":
		_ = d.h.db.QueryRow(ctx, `SELECT coalesce(linked_ticket_id::text,'') FROM faults WHERE id = $1::uuid`, id).Scan(&link)
		if link != "" {
			out["gleicher_vorgang"] = "Ticket /tickets/" + link
		}
	case "ticket":
		_ = d.h.db.QueryRow(ctx, `SELECT coalesce(linked_fault_id::text,'') FROM tickets WHERE id = $1::uuid`, id).Scan(&link)
		if link != "" {
			out["gleicher_vorgang"] = "Störung /faults/" + link
		}
	}
	if k.ActionsTable != "" {
		rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT to_char(a.created_at,'DD.MM.YYYY HH24:MI'), coalesce(u.first_name||' '||u.last_name,''), a.description
			FROM %s a LEFT JOIN users u ON u.id = a.created_by WHERE a.%s = $1::uuid ORDER BY a.created_at DESC LIMIT 15`, k.ActionsTable, k.ActionsFK), id)
		if err == nil {
			var acts []string
			for rows.Next() {
				var at, who, txt string
				if rows.Scan(&at, &who, &txt) == nil {
					acts = append(acts, at+" "+who+": "+txt)
				}
			}
			rows.Close()
			out["massnahmen"] = acts
		}
	}
	if k.Key != "project" && d.can("inventory.view") {
		rows, err := d.h.db.Query(ctx, `SELECT p.part_number||' '||p.name, r.qty::text FROM spare_part_reservations r JOIN spare_parts p ON p.id = r.part_id WHERE r.kind = $1 AND r.ref_id = $2::uuid`, k.ScopeRef, id)
		if err == nil {
			var parts []string
			for rows.Next() {
				var n, q string
				if rows.Scan(&n, &q) == nil {
					parts = append(parts, q+" × "+n)
				}
			}
			rows.Close()
			if len(parts) > 0 {
				out["reservierte_teile"] = parts
			}
		}
	}
	return jsonOut(out)
}

func (d *copilotData) listRecords(ctx context.Context, typ, status, asset string, sinceDays int, mine, overdue bool, limit int) (string, error) {
	k, ok := copilotKindByKey(typ)
	if !ok || !d.allowed(k) {
		return "", errors.New("keine Berechtigung oder unbekannte Art")
	}
	w, args := d.where(k, 0)
	switch status {
	case "open":
		w += " AND " + copilotOpenCond
	case "closed":
		w += " AND NOT " + copilotOpenCond
	}
	if asset != "" {
		ids := d.assetSubtreeIDs(ctx, asset)
		if len(ids) == 0 {
			return jsonOut(map[string]any{"hinweis": "keine Anlage mit diesem Namen gefunden", "ergebnisse": []any{}})
		}
		args = append(args, ids)
		w += fmt.Sprintf(" AND r.infrastructure_id::text = ANY($%d)", len(args))
	}
	if sinceDays > 0 {
		args = append(args, time.Now().AddDate(0, 0, -sinceDays))
		w += fmt.Sprintf(" AND r.created_at >= $%d", len(args))
	}
	if mine {
		args = append(args, d.userID)
		n := len(args)
		cond := fmt.Sprintf("r.responsible_to = $%d::uuid", n)
		if k.Key == "task" {
			cond += fmt.Sprintf(" OR EXISTS (SELECT 1 FROM task_assignees ta WHERE ta.task_id = r.id AND ta.user_id = $%d::uuid)", n)
		} else {
			cond += fmt.Sprintf(" OR r.assigned_to = $%d::uuid", n)
		}
		w += " AND (" + cond + ")"
	}
	dueCol := "r.due_date"
	if k.Key == "project" {
		dueCol = "r.end_date"
	}
	if overdue {
		w += " AND " + dueCol + " < CURRENT_DATE AND " + copilotOpenCond
	}
	prio := "''"
	if k.PrioCol != "" {
		prio = "coalesce(r." + k.PrioCol + "::text,'')"
	}
	var total int
	_ = d.h.db.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s r %s`, k.Table, w), args...).Scan(&total)
	rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT r.id::text, r.%s, r.status::text, %s, coalesce(i.name,''), to_char(r.created_at,'DD.MM.YYYY'), coalesce(to_char(%s,'DD.MM.YYYY'),'')
		FROM %s r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id %s ORDER BY r.created_at DESC LIMIT %d`,
		k.TitleCol, prio, dueCol, k.Table, strings.Replace(w, " WHERE ", " WHERE ", 1), limit), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type item struct{ ID, Titel, Status, Prioritaet, Anlage, Angelegt, Faellig, URL string }
	var list []item
	for rows.Next() {
		var x item
		if rows.Scan(&x.ID, &x.Titel, &x.Status, &x.Prioritaet, &x.Anlage, &x.Angelegt, &x.Faellig) == nil {
			x.Status, x.URL = statusLabel(x.Status), k.URL+x.ID
			list = append(list, x)
		}
	}
	return jsonOut(map[string]any{"art": k.Label, "gesamt": total, "gezeigt": len(list), "ergebnisse": list})
}

// findAsset: beste Anlage zu Name/ID.
func (d *copilotData) findAsset(ctx context.Context, q string) (id, name string, err error) {
	if uuidInPathRe.MatchString(q) {
		err = d.h.db.QueryRow(ctx, `SELECT id::text, name FROM infrastructure WHERE id::text = $1`, q).Scan(&id, &name)
		return
	}
	err = d.h.db.QueryRow(ctx, `SELECT id::text, name FROM infrastructure WHERE hidden_at IS NULL AND name ILIKE $1
		ORDER BY (lower(name) = lower($2)) DESC, length(name) LIMIT 1`, "%"+q+"%", q).Scan(&id, &name)
	if err != nil {
		err = errors.New("keine Anlage mit diesem Namen gefunden")
	}
	return
}

func (d *copilotData) assetOverview(ctx context.Context, q string) (string, error) {
	id, _, err := d.findAsset(ctx, strings.TrimSpace(q))
	if err != nil {
		return "", err
	}
	base, err := d.getRecord(ctx, "asset", id)
	if err != nil {
		return "", err
	}
	out := map[string]any{}
	_ = json.Unmarshal([]byte(base), &out)
	rows, err := d.h.db.Query(ctx, `SELECT name FROM infrastructure WHERE parent_id = $1::uuid AND hidden_at IS NULL ORDER BY name LIMIT 40`, id)
	if err == nil {
		var ch []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				ch = append(ch, n)
			}
		}
		rows.Close()
		out["unteranlagen"] = ch
	}
	sub := d.assetSubtreeByID(ctx, id)
	for _, kk := range []string{"fault", "ticket", "maintenance"} {
		k, _ := copilotKindByKey(kk)
		if !d.allowed(k) {
			continue
		}
		w, args := d.where(k, 1)
		args = append([]any{sub}, args...)
		rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT r.id::text, r.%s, r.status::text, to_char(r.created_at,'DD.MM.YYYY') FROM %s r %s AND r.infrastructure_id::text = ANY($1) AND %s ORDER BY r.created_at DESC LIMIT 15`,
			k.TitleCol, k.Table, w, copilotOpenCond), args...)
		if err == nil {
			var open []string
			for rows.Next() {
				var rid, t, s, dt string
				if rows.Scan(&rid, &t, &s, &dt) == nil {
					open = append(open, fmt.Sprintf("%s (%s, seit %s) %s%s", t, statusLabel(s), dt, k.URL, rid))
				}
			}
			rows.Close()
			out["offen_"+kk] = open
		}
	}
	if k, _ := copilotKindByKey("fault"); d.allowed(k) {
		w, args := d.where(k, 1)
		args = append([]any{sub}, args...)
		rows, err := d.h.db.Query(ctx, `SELECT r.title, coalesce(r.resolution,''), coalesce(r.root_cause,''), coalesce(to_char(r.resolved_at,'DD.MM.YYYY'),'') FROM faults r `+w+
			` AND r.infrastructure_id::text = ANY($1) AND NOT `+copilotOpenCond+` ORDER BY r.resolved_at DESC NULLS LAST LIMIT 10`, args...)
		if err == nil {
			var done []string
			for rows.Next() {
				var t, res, cause, dt string
				if rows.Scan(&t, &res, &cause, &dt) == nil {
					done = append(done, fmt.Sprintf("%s: %s – Lösung: %s; Ursache: %s", dt, t, firstNonEmpty(res, "–"), firstNonEmpty(cause, "–")))
				}
			}
			rows.Close()
			out["letzte_geloeste_stoerungen"] = done
		}
	}
	if vals, err := d.importValuesFor(ctx, "", sub); err == nil && len(vals) > 0 {
		out["messwerte"] = vals
	}
	if d.can("inventory.view") {
		rows, err := d.h.db.Query(ctx, `SELECT part_number||' '||name||' (Bestand '||stock_qty::text||' '||unit||')' FROM spare_parts WHERE active AND infrastructure_id::text = ANY($1) ORDER BY name LIMIT 30`, sub)
		if err == nil {
			var parts []string
			for rows.Next() {
				var s string
				if rows.Scan(&s) == nil {
					parts = append(parts, s)
				}
			}
			rows.Close()
			out["eingebaute_ersatzteile"] = parts
		}
	}
	return jsonOut(out)
}

func (d *copilotData) assetSubtreeByID(ctx context.Context, id string) []string {
	rows, err := d.h.db.Query(ctx, `WITH RECURSIVE sub AS (SELECT id FROM infrastructure WHERE id = $1::uuid UNION SELECT i.id FROM infrastructure i JOIN sub ON i.parent_id = sub.id) SELECT id::text FROM sub LIMIT 500`, id)
	if err != nil {
		return []string{id}
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			ids = append(ids, s)
		}
	}
	return ids
}

func (d *copilotData) partStock(ctx context.Context, q string, belowMin bool) (string, error) {
	if !d.can("inventory.view") {
		return "", errors.New("keine Berechtigung für Ersatzteile")
	}
	where := "p.active AND p.hidden_at IS NULL"
	var args []any
	if uuidInPathRe.MatchString(q) {
		args = append(args, q)
		where += " AND p.id::text = $1"
	} else if ws := words(q); len(ws) > 0 {
		cond, a := likeAll("(p.name || ' ' || p.part_number || ' ' || coalesce(p.manufacturer_part,'') || ' ' || coalesce(p.manufacturer,'') || ' ' || coalesce(p.category,''))", ws, 0)
		where += " AND " + cond
		args = append(args, a...)
	} else if !belowMin {
		return "", errors.New("Bitte einen Suchbegriff angeben")
	}
	if belowMin {
		where += " AND p.min_qty > 0 AND p.stock_qty < p.min_qty"
	}
	_, paths := d.h.storagePaths(ctx)
	rows, err := d.h.db.Query(ctx, `SELECT p.id::text, p.part_number, p.name, coalesce(p.manufacturer_part,''), coalesce(p.category,''), p.unit, p.stock_qty::float8, p.min_qty::float8,
		coalesce((SELECT SUM(r.qty) FROM spare_part_reservations r WHERE r.part_id = p.id),0)::float8
		FROM spare_parts p WHERE `+where+` ORDER BY p.name LIMIT 25`, args...)
	if err != nil {
		return "", err
	}
	type part struct {
		ID, Teilenummer, Name, HerstellerNr, Kategorie, Einheit string
		Frei, Mindestbestand, Reserviert                        float64
		Lagerplaetze                                            []string
		URL                                                     string
	}
	var list []part
	for rows.Next() {
		var p part
		if rows.Scan(&p.ID, &p.Teilenummer, &p.Name, &p.HerstellerNr, &p.Kategorie, &p.Einheit, &p.Frei, &p.Mindestbestand, &p.Reserviert) == nil {
			p.URL = "/inventory/" + p.ID
			list = append(list, p)
		}
	}
	rows.Close()
	for i := range list {
		srows, err := d.h.db.Query(ctx, `SELECT storage_node_id::text, qty::float8 FROM spare_part_stock WHERE part_id = $1::uuid AND qty <> 0 ORDER BY qty DESC LIMIT 10`, list[i].ID)
		if err != nil {
			continue
		}
		for srows.Next() {
			var nid string
			var qty float64
			if srows.Scan(&nid, &qty) == nil {
				list[i].Lagerplaetze = append(list[i].Lagerplaetze, fmt.Sprintf("%s: %s", firstNonEmpty(paths[nid], "?"), formatQty(qty)))
			}
		}
		srows.Close()
	}
	return jsonOut(map[string]any{"hinweis": "Frei = verfügbarer Bestand; Reserviert ist bereits abgebucht und für Vorgänge vorgesehen", "teile": list})
}

func (d *copilotData) statistics(ctx context.Context, typ, groupBy string, sinceDays int, asset string) (string, error) {
	k, ok := copilotKindByKey(typ)
	if !ok || !d.allowed(k) || k.Key == "project" {
		return "", errors.New("keine Berechtigung oder unbekannte Art")
	}
	w, args := d.where(k, 0)
	args = append(args, time.Now().AddDate(0, 0, -sinceDays))
	w += fmt.Sprintf(" AND r.created_at >= $%d", len(args))
	if asset != "" {
		ids := d.assetSubtreeIDs(ctx, asset)
		args = append(args, ids)
		w += fmt.Sprintf(" AND r.infrastructure_id::text = ANY($%d)", len(args))
	}
	var group string
	switch groupBy {
	case "asset":
		group = "coalesce(i.name, 'ohne Anlage')"
	case "status":
		group = "r.status::text"
	case "month":
		group = "to_char(r.created_at, 'YYYY-MM')"
	case "priority":
		if k.PrioCol == "" {
			return "", errors.New("keine Priorität bei dieser Art")
		}
		group = "coalesce(r." + k.PrioCol + "::text,'–')"
	default:
		return "", errors.New("group_by: asset, status, month oder priority")
	}
	dur := "NULL::float8"
	if k.Key == "fault" {
		dur = "AVG(EXTRACT(EPOCH FROM (r.resolved_at - r.detected_at)) / 3600) FILTER (WHERE r.resolved_at IS NOT NULL)"
	}
	rows, err := d.h.db.Query(ctx, fmt.Sprintf(`SELECT %s AS g, COUNT(*), %s FROM %s r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id %s GROUP BY g ORDER BY COUNT(*) DESC LIMIT 40`,
		group, dur, k.Table, w), args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type row struct {
		Gruppe           string
		Anzahl           int
		MittlereDauerStd *float64 `json:",omitempty"`
	}
	var list []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.Gruppe, &x.Anzahl, &x.MittlereDauerStd) == nil {
			if groupBy == "status" {
				x.Gruppe = statusLabel(x.Gruppe)
			}
			list = append(list, x)
		}
	}
	return jsonOut(map[string]any{"art": k.Label, "zeitraum_tage": sinceDays, "gruppiert_nach": groupBy, "werte": list})
}

type importValueRow struct{ Bezeichnung, Anlage, Wert, Zeitpunkt, Verbindung string }

func (d *copilotData) importValuesFor(ctx context.Context, q string, assetIDs []string) ([]importValueRow, error) {
	where := "TRUE"
	var args []any
	if ws := words(q); len(ws) > 0 {
		cond, a := likeAll("(m.name || ' ' || coalesce(m.note,'') || ' ' || coalesce(i.name,''))", ws, 0)
		where += " AND " + cond
		args = append(args, a...)
	}
	if assetIDs != nil {
		args = append(args, assetIDs)
		where += fmt.Sprintf(" AND m.infrastructure_id::text = ANY($%d)", len(args))
	}
	rows, err := d.h.db.Query(ctx, `SELECT m.name, coalesce(i.name,''), coalesce(m.last_value,''), coalesce(to_char(m.last_received_at,'DD.MM.YYYY HH24:MI'),'noch kein Wert'), c.name
		FROM import_mappings m JOIN import_export_connections c ON c.id = m.connection_id LEFT JOIN infrastructure i ON i.id = m.infrastructure_id
		WHERE `+where+` ORDER BY i.name, m.name LIMIT 60`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []importValueRow
	for rows.Next() {
		var x importValueRow
		if rows.Scan(&x.Bezeichnung, &x.Anlage, &x.Wert, &x.Zeitpunkt, &x.Verbindung) == nil {
			list = append(list, x)
		}
	}
	return list, rows.Err()
}

func (d *copilotData) importValues(ctx context.Context, q string) (string, error) {
	list, err := d.importValuesFor(ctx, q, nil)
	if err != nil {
		return "", err
	}
	return jsonOut(map[string]any{"werte": list})
}

func (d *copilotData) dueMaintenance(ctx context.Context, days int, asset string) (string, error) {
	k, _ := copilotKindByKey("maintenance")
	if !d.allowed(k) {
		return "", errors.New("keine Berechtigung für Wartungen")
	}
	w, args := d.where(k, 0)
	args = append(args, time.Now().AddDate(0, 0, days))
	w += fmt.Sprintf(" AND r.due_date <= $%d AND %s", len(args), copilotOpenCond)
	if asset != "" {
		args = append(args, d.assetSubtreeIDs(ctx, asset))
		w += fmt.Sprintf(" AND r.infrastructure_id::text = ANY($%d)", len(args))
	}
	rows, err := d.h.db.Query(ctx, `SELECT r.id::text, r.title, coalesce(i.name,''), to_char(r.due_date,'DD.MM.YYYY'), r.due_date < CURRENT_DATE,
		coalesce((SELECT first_name||' '||last_name FROM users WHERE id = r.assigned_to),'')
		FROM maintenance_tasks r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id `+w+` ORDER BY r.due_date LIMIT 40`, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type item struct {
		Titel, Anlage, Faellig string
		Ueberfaellig           bool
		Zugewiesen, URL        string
	}
	var list []item
	for rows.Next() {
		var x item
		var id string
		if rows.Scan(&id, &x.Titel, &x.Anlage, &x.Faellig, &x.Ueberfaellig, &x.Zugewiesen) == nil {
			x.URL = k.URL + id
			list = append(list, x)
		}
	}
	return jsonOut(map[string]any{"tage": days, "wartungen": list})
}

// ── Systemtext und Ablauf ────────────────────────────────────

func copilotSystemPrompt(userName, extra string) string {
	return `Du bist der Instandhaltungs-Copilot im PDH (Prozess Data Hub) eines Industriebetriebs. Du beantwortest Fragen zu den Daten dieses Betriebs:
Störungen, Tickets, Aufgaben, Wartungen, Projekte, Anlagen, Ersatzteile/Bestand und Messwerte aus dem Import.

Regeln:
- Hole Fakten immer mit den Werkzeugen aus dem PDH. Erfinde keine Vorgänge, Bestände, Namen oder Zahlen. Findest du nichts, sag das.
- Du hast keinen Internetzugang. Allgemeines Fachwissen (z. B. wie man eine Hydraulik entlüftet) darfst du nutzen, kennzeichne es dann als allgemeinen Hinweis, nicht als PDH-Daten.
- Du siehst nur, was die fragende Person sehen darf. Ändern kannst du nichts – verweise zum Bearbeiten auf den Vorgang.
- Nenne bei Vorgängen, Anlagen und Teilen den Link (Pfad wie /faults/…) aus den Werkzeug-Ergebnissen, damit man ihn anklicken kann.
- Störungen und Tickets können derselbe Vorgang sein: Aus einer Störung wird oft ein Ticket erstellt (verknüpft, gleicher oder ähnlicher Titel, gleiche Anlage). Zähle solche Paare nicht doppelt, sondern behandle sie als einen Fall und nenne beide Links. Ein Datensatz zeigt das unter "gleicher_vorgang".
- Arbeitssicherheit zuerst (Freischalten, Sichern), wenn es um Arbeiten an Anlagen geht.
- Antworte auf Deutsch, knapp und praxisnah. Kurze Listen statt langer Absätze. Heute ist ` + time.Now().Format("Monday, 02.01.2006") + `.
Fragende Person: ` + userName + `.` + extra
}

var copilotToolLabels = map[string]string{
	"search": "Suche", "get_record": "Datensätze", "list_records": "Listen", "asset_overview": "Anlagen", "part_stock": "Ersatzteile",
	"statistics": "Auswertungen", "import_values": "Messwerte", "due_maintenance": "Wartungen",
}

// askCopilot: Frage mit PDH-Daten. Claude ruft die Werkzeuge selbst; Ollama
// (ohne Werkzeuge) bekommt vorab die Suchtreffer zur Frage.
func (h *Handler) askCopilot(ctx context.Context, d *copilotData, userName, question, faultID string) (string, []string, error) {
	cp := h.faults.Copilot()
	if cp == nil {
		return "", nil, errors.New("Copilot ist nicht eingerichtet")
	}
	extra := ""
	if faultID != "" {
		extra = "\n" + h.faults.FaultContext(ctx, faultID)
	}
	system := copilotSystemPrompt(userName, extra)
	if cp.CanUseTools() {
		answer, used, err := cp.AskWithTools(ctx, system, question, copilotTools(), d.exec)
		var labels []string
		for _, u := range used {
			labels = append(labels, firstNonEmpty(copilotToolLabels[u], u))
		}
		return answer, labels, err
	}
	data, _ := d.search(ctx, question, nil, 5)
	if faultID != "" {
		if rec, err := d.getRecord(ctx, "fault", faultID); err == nil {
			data = rec + "\n" + data
		}
	}
	answer, err := cp.AskWithContext(ctx, system, question, data)
	return answer, []string{"Suche"}, err
}
