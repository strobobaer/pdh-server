package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/listsort"
)

// Generische Reiter-Inhalte fuer alle Stammdaten-/Vorgangsseiten, per htmx
// nachgeladen (wie der Feldsatz-Block):
//   {{template "record-links-slot"   (dict "Module" "ticket" "ID" .Ticket.ID)}}
//   {{template "record-history-slot" (dict "Module" "ticket" "ID" .Ticket.ID)}}

// historyRefType: Modul -> ref_type in record_history.
func historyRefType(module string) string {
	switch module {
	case "part":
		return "spare_part"
	}
	return module
}

// chatRefType: Modul -> ref_type in chat_message_links.
func chatRefType(module string) string {
	switch module {
	case "part":
		return "spare_part"
	}
	return module
}

type linkItem struct {
	Label, Sub, URL, Badge, BadgeClass, Icon string
	Frame                                    bool // im PDH-Viewer oeffnen
}

type linkGroup struct {
	Title, Icon string
	Items       []linkItem
	Empty       string
}

// recordViewContext prueft Modul, Berechtigung und Existenz.
func (h *Handler) recordViewContext(w http.ResponseWriter, r *http.Request) (fieldModule, string, bool) {
	m, ok := fieldModuleByKey(chi.URLParam(r, "module"))
	id := chi.URLParam(r, "id")
	if !ok || h.db == nil {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return m, "", false
	}
	if !h.canViewRecord(r, m, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return m, "", false
	}
	var exists bool
	if err := h.db.QueryRow(r.Context(), fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1::uuid)`, m.Table), id).Scan(&exists); err != nil || !exists {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return m, "", false
	}
	return m, id, true
}

func (h *Handler) renderFragment(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = bindLang(t, langOf(data)).ExecuteTemplate(w, name, data)
	}
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">%s</div>`, esc(err.Error()))
	}
}

// historyModule: ein Modul der Datensatzhistorie. Can prüft genau das, was die
// Detailseite des Moduls prüft (auch die Abteilungsebene, wo die Seite sie prüft).
type historyModule struct {
	RefType, Table string
	Can            func(h *Handler, r *http.Request, id string) bool
}

// historyLogin: für Module, deren Detailseite kein Einzelrecht prüft – jeder
// Angemeldete (die Abteilung sperrt DepartmentScopeMiddleware, soweit scopePaths
// das Modul kennt, auf Seite und Historienpfad gleich).
func historyLogin(h *Handler, r *http.Request, id string) bool { return getUser(r).ID != "" }

// historyModuleOf: Tabelle und ref_type eines Moduls aus der Feldsatz-Tabelle. Ein
// unbekannter Schlüssel ist ein Programmfehler und stoppt den Start (nie „offen“).
func historyModuleOf(key string, can func(h *Handler, r *http.Request, id string) bool) historyModule {
	m, ok := fieldModuleByKey(key)
	if !ok {
		panic("historyModules: unbekanntes Modul " + key)
	}
	return historyModule{historyRefType(key), m.Table, can}
}

// historyModules: erlaubte Module von /records/history/{module}/{id}. Eigene Tabelle
// (nicht fieldModules), weil dort für Projekt, KVP, Infrastruktur und Lager kein
// Ansichtsrecht steht und die Historie sonst jedem Angemeldeten offen stünde.
var historyModules = map[string]historyModule{
	// Störung (FaultDetail, handler.go), Ticket (TicketDetail), Aufgabe (TaskDetail, tasks_handler.go),
	// Wartung (MaintenanceTaskDetail, maintenance_web.go), Projekt (ProjectDetail, projects_handler.go),
	// KVP (KVPDetailPage, kvp.go), Ersatzteil (PartDetailPage, parts.go): die Seiten verlangen nur die
	// Anmeldung (wer nur „lösen“, „buchen“ o. Ä. darf, sieht die Historie weiter).
	"fault":            historyModuleOf("fault", historyLogin),
	"ticket":           historyModuleOf("ticket", historyLogin),
	"task":             historyModuleOf("task", historyLogin),
	"maintenance_task": historyModuleOf("maintenance_task", historyLogin),
	"project":          historyModuleOf("project", historyLogin),
	"kvp":              historyModuleOf("kvp", historyLogin),
	"part":             historyModuleOf("part", historyLogin),
	// Rüstzeit: SetupDetailPage (setup_pages.go) – permSetupView in der Abteilung der Rüstzeit
	"setup": historyModuleOf("setup", func(h *Handler, r *http.Request, id string) bool {
		return h.setupCan(r, permSetupView, id)
	}),
	// Benutzer: UserDetailPage (user_detail.go) – eigenes Konto, Benutzerverwaltung oder unterstellte Person
	"user": historyModuleOf("user", func(h *Handler, r *http.Request, id string) bool {
		if getUser(r).ID == id || h.canManageUsers(r) {
			return true
		}
		if h.users == nil {
			return false
		}
		sub, _ := h.users.IsSubordinate(r.Context(), getUser(r).ID, id)
		return sub
	}),
	// Anlage: InfraDetail (infra_web.go) – infrastructure.view oder .edit
	"infrastructure": historyModuleOf("infrastructure", func(h *Handler, r *http.Request, id string) bool { return h.canViewInfra(r) }),
	// Lagerort: StorageDetailPage (storage_detail.go) – inventory.view oder .edit
	"storage": historyModuleOf("storage", func(h *Handler, r *http.Request, id string) bool {
		return h.hasPerm(r, "inventory.view") || h.hasPerm(r, "inventory.edit")
	}),
	// Partner: PartnerDetail (partners.go) – directory.view oder .edit
	"business_partner": historyModuleOf("business_partner", func(h *Handler, r *http.Request, id string) bool { return h.canViewPartners(r) }),
	// Bestellung: PurchaseDetailPage (purchases.go) – procurement.view in der Abteilung der Bestellung
	"purchase_order": {refPurchase, "purchase_orders", func(h *Handler, r *http.Request, id string) bool {
		return h.hasPermRec(r, permPurchaseView, refPurchase, id)
	}},
}

const historyStep = 30 // Zeilen (bzw. Gruppen) je Schritt von „Weitere laden“

// historyEntry: eine Zeile der Historie; Count ab 2 = zusammengefasste Gruppe
// („N× Beschriftung – zuletzt …“) mit den Einzeleinträgen in Rows.
type historyEntry struct {
	Row   historyRow
	Label string
	Count int
	Last  string
	Rows  []historyRow
}

// historyRow: ein Eintrag; Title ist seine Meldung, Label die feste Beschriftung
// seiner Art (Vorgang + Feld) – sie gilt für Gruppen und die Sortierart „Nach Art“.
type historyRow struct {
	HistoryView
	Title string
	Label string
}

// historyActionLabels / historyFieldLabels: feste deutsche Beschriftung je Vorgang
// und Feld, unabhängig vom Freitext der einzelnen Meldung.
var historyActionLabels = map[string]string{
	"create": "Angelegt", "update": "Geändert", "status": "Status geändert", "people": "Zuständigkeit geändert",
	"category": "Kategorie geändert", "archive": "Archiviert", "work": "Arbeitsschritt erfasst",
	"document": "Dokument abgelegt", "explorer_write": "Testweise geschrieben",
}

var historyFieldLabels = map[string]string{
	"due_date": "Fälligkeit", "assigned_to": "Zugewiesener Mitarbeiter", "responsible_to": "Verantwortlicher",
	"assigned_group_id": "Zugewiesene Gruppe", "title": "Titel", "assessment": "Bewertung", "plan": "Plan",
	"check_result": "Wirksamkeit", "act": "Ergebnis", "action": "Maßnahme", "file": "Datei",
	"nextcloud_user_id": "Nextcloud-Konto", "must_change_password": "Kennwortwechsel", "infrastructure_id": "Anlage",
}

// historyLabel: stabile Beschriftung zu (Vorgang, Feld), z. B. „Status geändert“
// oder „Geändert · Fälligkeit“. Felder, die der Vorgang schon nennt, entfallen.
func historyLabel(action, field string) string {
	l, ok := historyActionLabels[action]
	if !ok {
		l = action
	}
	if field == "" || field == action || field == "status" || field == "category" || field == "archived_at" {
		return l
	}
	if f, ok := historyFieldLabels[field]; ok {
		field = f
	}
	return l + " · " + field
}

// historyPage: Daten des Kärtchens (Vorlage „record-history“).
type historyPage struct {
	Lang    string
	Bar     SortBar
	Entries []historyEntry
	Cap     int  // Grenze der berücksichtigten Einträge
	Capped  bool // es gab mehr, nur die neuesten zählen
	MoreURL string
}

func (p historyPage) Language() string { return p.Lang }

// RecordHistoryWeb liefert die Datensatzhistorie eines Moduls als Kärtchen mit
// Sortier-Leiste und „Weitere laden“. Das Recht wird vor allem anderen geprüft.
func (h *Handler) RecordHistoryWeb(w http.ResponseWriter, r *http.Request) {
	m, ok := historyModules[chi.URLParam(r, "module")]
	id := chi.URLParam(r, "id")
	if !ok || h.db == nil {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return
	}
	if !m.Can(h, r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var exists bool
	if !uuidInPathRe.MatchString(id) || h.db.QueryRow(r.Context(), fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1::uuid)`, m.Table), id).Scan(&exists) != nil || !exists {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return
	}
	mode := h.sortMode(r, "history")
	n := historyStep
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = min(v, historyCap)
	}
	rows, capped := h.recordHistoryAll(r.Context(), m.RefType, id)
	items := make([]listsort.Item, len(rows))
	hrows := make([]historyRow, len(rows))
	for i, hv := range rows {
		hr := historyRow{HistoryView: hv, Title: hv.Message, Label: historyLabel(hv.Action, hv.FieldName)}
		if hr.Title == "" {
			hr.Title = hr.Label
		}
		hrows[i] = hr
		// gleich = gleicher Vorgang und gleiches Feld; die Beschriftung steht vorn, damit
		// „Gleiches zusammen“ nach dem sortiert, was angezeigt wird
		items[i] = listsort.Item{At: hv.At, Key: hr.Label + "\x00" + hv.Action + "\x00" + hv.FieldName, Label: hr.Label, Person: hv.UserName, Kind: hr.Label}
	}
	var entries []historyEntry
	if mode.Grouped() {
		for _, g := range listsort.Groups(items, mode) {
			e := historyEntry{Row: hrows[g.Idx[0]], Label: hrows[g.Idx[0]].Label, Count: g.Count, Last: g.Last.Format("02.01.2006")}
			if g.Count > 1 {
				for _, i := range g.Idx {
					e.Rows = append(e.Rows, hrows[i])
				}
			}
			entries = append(entries, e)
		}
	} else {
		for _, i := range listsort.Order(items, mode) {
			entries = append(entries, historyEntry{Row: hrows[i], Count: 1})
		}
	}
	p := historyPage{Lang: h.requestLang(r), Cap: historyCap, Capped: capped}
	base := r.URL.Path + "?ls_history=" + string(mode)
	if len(entries) > n {
		entries = entries[:n]
		p.MoreURL = base + "&n=" + strconv.Itoa(n+historyStep)
	}
	p.Entries = entries
	p.Bar = h.sortBar(r, "history", mode, base, "#rec-history") // Sortieren beginnt wieder bei 30
	h.renderFragment(w, "record-history", p)
}

// RecordLinksWeb liefert alle Verknuepfungen eines Datensatzes.
func (h *Handler) RecordLinksWeb(w http.ResponseWriter, r *http.Request) {
	m, id, ok := h.recordViewContext(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var groups []linkGroup
	add := func(g linkGroup) {
		if len(g.Items) > 0 || g.Empty != "" {
			groups = append(groups, g)
		}
	}
	switch m.Key {
	case "ticket", "fault", "task", "project", "maintenance_task", "part", setupRef:
		add(h.linkInfra(ctx, m, id))
	}
	switch m.Key {
	case "ticket", "fault", "task", "maintenance_task":
		if h.canViewIT(r) {
			add(h.linkITAsset(ctx, m, id))
		}
	}
	add(h.linkRecords(ctx, m.Key, id))
	switch m.Key {
	case "ticket", "fault", "task", "maintenance_task":
		add(h.linkParts(ctx, m.Key, id))
	}
	add(h.linkPartners(ctx, m.Key, id))
	if m.Key == "infrastructure" {
		add(h.linkInfraParts(ctx, id))
	}
	if m.Key == "storage" {
		add(h.linkStorageParts(ctx, id))
	}
	add(h.linkChat(ctx, m.Key, id, getUser(r).ID))
	h.renderFragment(w, "record-links", groups)
}

func (h *Handler) queryLinks(ctx context.Context, sql string, args ...interface{}) []linkItem {
	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []linkItem
	for rows.Next() {
		var it linkItem
		var status string
		if rows.Scan(&it.Label, &it.Sub, &it.URL, &status, &it.Icon) == nil {
			if status != "" {
				it.Badge, it.BadgeClass = statusLabel(status), statusClass(status)
			}
			it.Frame = it.URL != ""
			if key := linkRecordKey(it.URL); key != "" {
				it.Icon = recordIcon(key) // Symbol-Tabelle statt fester Klasse im SQL
			}
			list = append(list, it)
		}
	}
	return list
}

func (h *Handler) linkInfra(ctx context.Context, m fieldModule, id string) linkGroup {
	g := linkGroup{Title: "Anlage", Icon: "ti-hierarchy-2", Empty: "Keiner Anlage zugeordnet"}
	g.Items = h.queryLinks(ctx, fmt.Sprintf(`
		WITH RECURSIVE up AS (
			SELECT i.id, i.parent_id, i.name::text AS path FROM infrastructure i
			WHERE i.id = (SELECT infrastructure_id FROM %s WHERE id = $1::uuid)
			UNION ALL SELECT p.id, p.parent_id, p.name || ' › ' || up.path FROM infrastructure p JOIN up ON p.id = up.parent_id
		)
		SELECT i.name, (SELECT path FROM up WHERE parent_id IS NULL), '/infrastructure/' || i.id, '', 'ti-hierarchy-2'
		FROM infrastructure i WHERE i.id = (SELECT infrastructure_id FROM %s WHERE id = $1::uuid)`, m.Table, m.Table), id)
	return g
}

// linkITAsset: zugeordnetes IT-Asset (QR-Infoseite, migrations/096).
func (h *Handler) linkITAsset(ctx context.Context, m fieldModule, id string) linkGroup {
	g := linkGroup{Title: "IT-Asset", Icon: "ti-device-desktop"}
	g.Items = h.queryLinks(ctx, fmt.Sprintf(`SELECT a.name, COALESCE(NULLIF(a.hostname, ''), (SELECT c.name FROM it_asset_categories c WHERE c.key = a.type), a.type), '/it/' || a.id, '', 'ti-device-desktop'
		FROM it_assets a WHERE a.id = (SELECT it_asset_id FROM %s WHERE id = $1::uuid)`, m.Table), id)
	return g
}

// linkRecords: direkt verknuepfte Vorgaenge (Stoerung/Ticket/Aufgabe/Projekt/Wartung).
func (h *Handler) linkRecords(ctx context.Context, module, id string) linkGroup {
	g := linkGroup{Title: "Verknüpfte Vorgänge", Icon: "ti-link"}
	const fault = `SELECT 'Störung: ' || f.title, to_char(f.created_at, 'DD.MM.YYYY'), '/faults/' || f.id, f.status::text, 'ti-alert-triangle' FROM faults f`
	const ticket = `SELECT 'Ticket: ' || t.title, to_char(t.created_at, 'DD.MM.YYYY'), '/tickets/' || t.id, t.status::text, 'ti-ticket' FROM tickets t`
	const task = `SELECT 'Aufgabe: ' || a.title, COALESCE('fällig ' || to_char(a.due_date, 'DD.MM.YYYY'), ''), '/tasks/' || a.id, a.status, 'ti-checkbox' FROM tasks a`
	const project = `SELECT 'Projekt: ' || p.name, COALESCE(to_char(p.start_date, 'DD.MM.YYYY') || ' – ' || to_char(p.end_date, 'DD.MM.YYYY'), ''), '/projects/' || p.id, p.status, 'ti-briefcase' FROM projects p`
	// Rüstzeit: Titel, Status als Wort der Rüstzeit (nicht der gemeinsamen Tabelle statusLabel)
	const setupRow = `SELECT 'Rüstzeit: ' || ` + setupTitleSQL + `, CASE s.status WHEN 'running' THEN 'läuft seit ' || to_char(s.started_at, 'DD.MM.YYYY HH24:MI')
			WHEN 'fault' THEN 'Störung durch Rüsten' ELSE to_char(s.started_at, 'DD.MM.YYYY HH24:MI') END, '/setup/' || s.id, '', 'ti-arrows-exchange-2' FROM setup_times s`
	var q string
	switch module {
	case "ticket":
		q = fault + ` WHERE f.id = (SELECT linked_fault_id FROM tickets WHERE id = $1::uuid) OR f.linked_ticket_id = $1::uuid
			UNION ` + task + ` WHERE a.id = (SELECT linked_task_id FROM tickets WHERE id = $1::uuid) OR a.linked_ticket_id = $1::uuid`
	case "fault":
		q = ticket + ` WHERE t.id = (SELECT linked_ticket_id FROM faults WHERE id = $1::uuid) OR t.linked_fault_id = $1::uuid
			UNION ` + task + ` WHERE a.id = (SELECT linked_task_id FROM faults WHERE id = $1::uuid) OR a.linked_fault_id = $1::uuid
			UNION ` + setupRow + ` WHERE s.fault_id = $1::uuid`
	case setupRef:
		q = fault + ` WHERE f.id = (SELECT fault_id FROM setup_times WHERE id = $1::uuid)`
	case "task":
		q = project + ` WHERE p.id = (SELECT project_id FROM tasks WHERE id = $1::uuid)
			UNION ` + fault + ` WHERE f.id = (SELECT linked_fault_id FROM tasks WHERE id = $1::uuid) OR f.linked_task_id = $1::uuid
			UNION ` + ticket + ` WHERE t.id = (SELECT linked_ticket_id FROM tasks WHERE id = $1::uuid) OR t.linked_task_id = $1::uuid`
	case "project":
		g.Title = "Aufgaben im Projekt"
		q = task + ` WHERE a.project_id = $1::uuid`
	case "maintenance_task":
		q = `SELECT 'Wartungsplan: ' || mp.name, mp.interval_type::text, '', '', 'ti-calendar-repeat' FROM maintenance_plans mp
			WHERE mp.id = (SELECT plan_id FROM maintenance_tasks WHERE id = $1::uuid)`
	case "infrastructure":
		g.Title = "Offene Vorgänge an der Anlage"
		q = fault + ` WHERE f.infrastructure_id = $1::uuid AND f.status NOT IN ('resolved', 'closed')
			UNION ALL ` + ticket + ` WHERE t.infrastructure_id = $1::uuid AND t.status NOT IN ('resolved', 'closed')
			UNION ALL ` + task + ` WHERE a.infrastructure_id = $1::uuid AND a.status NOT IN ('resolved', 'closed')
			UNION ALL SELECT 'Wartung: ' || m.title, 'fällig ' || to_char(m.due_date, 'DD.MM.YYYY'), '/maintenance/tasks/' || m.id, m.status::text, 'ti-tool'
			FROM maintenance_tasks m WHERE m.infrastructure_id = $1::uuid AND m.status IN ('open', 'in_progress', 'pending')
			UNION ALL ` + setupRow + ` WHERE s.infrastructure_id = $1::uuid AND s.status = 'running' AND s.archived_at IS NULL`
	default:
		return g
	}
	g.Items = h.queryLinks(ctx, `SELECT * FROM (`+q+`) x LIMIT 100`, id)
	return g
}

// linkParts: vorgemerkte und gebuchte Ersatzteile eines Vorgangs.
func (h *Handler) linkParts(ctx context.Context, module, id string) linkGroup {
	col := map[string]string{"ticket": "ticket_id", "fault": "fault_id", "task": "task_id", "maintenance_task": "maintenance_task_id"}[module]
	pending := map[string]string{"ticket": "ticket_pending_parts", "fault": "fault_pending_parts", "task": "task_pending_parts", "maintenance_task": "maintenance_task_pending_parts"}[module]
	pendingCol := col
	if module == "maintenance_task" {
		pendingCol = "task_id"
	}
	g := linkGroup{Title: "Ersatzteile", Icon: "ti-package"}
	g.Items = h.queryLinks(ctx, fmt.Sprintf(`
		SELECT sp.part_number || ' · ' || sp.name,
		       'gebucht: ' || rtrim(to_char(SUM(CASE WHEN sm.type = 'out' THEN sm.qty ELSE -sm.qty END), 'FM999999990.###'), '.') || ' ' || sp.unit,
		       '/inventory/' || sp.id, '', 'ti-package'
		FROM stock_movements sm JOIN spare_parts sp ON sp.id = sm.part_id
		WHERE sm.%s = $1::uuid AND sm.type IN ('out', 'in')
		GROUP BY sp.id, sp.part_number, sp.name, sp.unit
		UNION ALL
		SELECT sp.part_number || ' · ' || sp.name, CASE WHEN pp.reserved THEN 'reserviert: ' ELSE 'vorgemerkt: ' END || rtrim(to_char(pp.qty, 'FM999999990.###'), '.') || ' ' || sp.unit,
		       '/inventory/' || sp.id, '', 'ti-bookmark'
		FROM %s pp JOIN spare_parts sp ON sp.id = pp.part_id WHERE pp.%s = $1::uuid`, col, pending, pendingCol), id)
	return g
}

func (h *Handler) linkPartners(ctx context.Context, module, id string) linkGroup {
	g := linkGroup{Title: "Hersteller, Lieferanten & externe Firmen", Icon: "ti-building-factory-2"}
	var q string
	switch module {
	case "infrastructure":
		q = `SELECT bp.name, r.label, '/directory/' || bp.id, '', 'ti-building-factory-2'
			FROM infrastructure i CROSS JOIN LATERAL (VALUES ('Hersteller', i.manufacturer_id), ('Lieferant', i.supplier_id), ('Servicepartner', i.service_partner_id)) r(label, pid)
			JOIN business_partners bp ON bp.id = r.pid WHERE i.id = $1::uuid`
	case "part":
		q = `SELECT bp.name, 'Hersteller', '/directory/' || bp.id, '', 'ti-building-factory-2' FROM spare_parts sp JOIN business_partners bp ON bp.id = sp.manufacturer_id WHERE sp.id = $1::uuid
			UNION ALL SELECT bp.name, CASE WHEN s.preferred THEN 'Hauptlieferant' ELSE 'Lieferant' END, '/directory/' || bp.id, '', 'ti-truck'
			FROM spare_part_suppliers s JOIN business_partners bp ON bp.id = s.partner_id WHERE s.part_id = $1::uuid`
	case "ticket", "fault", "task", "project", "maintenance_task":
		q = `SELECT rp.name, CASE rp.role WHEN 'responsible' THEN 'Verantwortlich' ELSE 'Zuständig' END || CASE WHEN rp.contact <> '' THEN ' · ' || rp.contact ELSE '' END,
			       CASE WHEN rp.partner_id IS NOT NULL THEN '/directory/' || rp.partner_id ELSE '' END, '', CASE WHEN rp.kind = 'person' THEN 'ti-user-share' ELSE 'ti-building' END
			FROM record_external_parties rp WHERE rp.ref_type = '` + module + `' AND rp.ref_id = $1::uuid`
	default:
		return g
	}
	g.Items = h.queryLinks(ctx, q, id)
	return g
}

func (h *Handler) linkInfraParts(ctx context.Context, id string) linkGroup {
	g := linkGroup{Title: "Ersatzteile für diese Anlage", Icon: "ti-package"}
	g.Items = h.queryLinks(ctx, `
		SELECT sp.part_number || ' · ' || sp.name, 'Bestand ' || rtrim(to_char(sp.stock_qty, 'FM999999990.###'), '.') || ' ' || sp.unit,
		       '/inventory/' || sp.id, '', 'ti-package'
		FROM spare_parts sp WHERE sp.infrastructure_id = $1::uuid AND sp.active ORDER BY sp.name LIMIT 200`, id)
	return g
}

func (h *Handler) linkStorageParts(ctx context.Context, id string) linkGroup {
	g := linkGroup{Title: "Ersatzteile an diesem Lagerplatz (inkl. Unterplätze)", Icon: "ti-package", Empty: "Keine Ersatzteile eingelagert"}
	g.Items = h.queryLinks(ctx, `
		WITH RECURSIVE sub AS (SELECT id FROM storage_nodes WHERE id = $1::uuid
		                       UNION ALL SELECT n.id FROM storage_nodes n JOIN sub ON n.parent_id = sub.id)
		SELECT sp.part_number || ' · ' || sp.name, rtrim(to_char(SUM(st.qty), 'FM999999990.###'), '.') || ' ' || sp.unit,
		       '/inventory/' || sp.id, '', 'ti-package'
		FROM spare_part_stock st JOIN spare_parts sp ON sp.id = st.part_id
		WHERE st.storage_node_id IN (SELECT id FROM sub)
		GROUP BY sp.id, sp.part_number, sp.name, sp.unit ORDER BY sp.name LIMIT 500`, id)
	return g
}

// linkChat: Chat-Nachrichten, die den Datensatz verlinken - nur aus
// Unterhaltungen, in denen der Benutzer Mitglied ist.
func (h *Handler) linkChat(ctx context.Context, module, id, userID string) linkGroup {
	g := linkGroup{Title: "Im Chat erwähnt", Icon: "ti-messages"}
	rows, err := h.db.Query(ctx, `
		SELECT m.body, m.created_at, c.id::text, c.kind, c.name, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')
		FROM chat_message_links l
		JOIN chat_messages m ON m.id = l.message_id AND m.deleted_at IS NULL
		JOIN chat_conversations c ON c.id = m.conversation_id
		JOIN chat_members cm ON cm.conversation_id = c.id AND cm.user_id = $3::uuid AND cm.left_at IS NULL
		LEFT JOIN users u ON u.id = m.user_id
		WHERE l.ref_type = $1 AND l.ref_id = $2::uuid
		ORDER BY m.created_at DESC LIMIT 30`, chatRefType(module), id, userID)
	if err != nil {
		return g
	}
	defer rows.Close()
	for rows.Next() {
		var body, convID, kind, name, author string
		var at time.Time
		if rows.Scan(&body, &at, &convID, &kind, &name, &author) != nil {
			continue
		}
		if len([]rune(body)) > 120 {
			body = string([]rune(body)[:120]) + "…"
		}
		where := name
		switch {
		case kind == "direct":
			where = "Direktnachricht"
		case kind == "channel":
			where = "# " + name
		case where == "":
			where = "Gruppenchat"
		}
		g.Items = append(g.Items, linkItem{
			Label: strings.TrimSpace(body), Sub: author + " · " + where + " · " + at.Local().Format("02.01.2006 15:04"),
			URL: "/chat#c=" + convID, Icon: "ti-message",
		})
	}
	return g
}

// canViewRecord: Lesezugriff auf generische Reiter (Feldsaetze, Historie,
// Verknuepfungen). Das eigene Benutzerkonto darf jede Person lesen.
func (h *Handler) canViewRecord(r *http.Request, m fieldModule, id string) bool {
	if m.Key == "user" && id == getUser(r).ID {
		return true
	}
	return h.hasPerm(r, m.ViewPerm) || h.hasPerm(r, m.EditPerm)
}
