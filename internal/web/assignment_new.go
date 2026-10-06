package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pdh/pkg/appsettings"
)

// AssignmentNewPageData speist die modulübergreifende "Neue Zuweisung"-
// Seite: ein zentraler Einstieg, um Tickets, Aufgaben, Wartungen und
// Störungen direkt mit einem zuständigen Mitarbeiter anzulegen, statt
// jedes Modul einzeln aufzurufen. Erstellt wird weiterhin über die
// bestehenden JSON-APIs der jeweiligen Module (/api/v1/tickets/, /tasks/,
// /maintenance/tasks, /faults/) - hier wird nichts dupliziert.
type AssignmentNewPageData struct {
	BaseData
	Users                     []UserOption
	Groups                    []groupView // zuweisbare Gruppen (migrations/085)
	DefaultDueDaysTicket      int
	DefaultDueDaysTask        int
	DefaultDueDaysMaintenance int
	DefaultDueDaysFault       int
	// Vorbelegung von der QR-Infoseite (/a/<id>): ?type=&infra=&it=&back=
	PresetType               string
	PresetInfraID, InfraPath string
	PresetITID, ITName       string
	Back                     string // nach dem Anlegen hierher statt in die Liste
	// Aufgaben und Wartungen legen hier nur Administratoren und Manager an
	CanCreateRestricted bool
	// Broker: offene, nicht zugewiesene Vorgaenge ihrer Arten als Schnelleinstieg
	Unassigned []unassignedGroup
}

type unassignedItem struct {
	Title, Href, Priority, PrioLabel, Infra, Age string
}

type unassignedGroup struct {
	Kind, Label, Icon, ListURL string
	Items                      []unassignedItem
	More                       int
}

// unassignedKinds: Art → Broker-Spalte, Tabelle und Darstellung (Reihenfolge der Anzeige).
var unassignedKinds = []struct {
	kind, column, ref, label, icon, href, list, query string
}{
	{"fault", "broker_faults", "fault", "Störungen", "ti-alert-triangle", "/faults/", "/faults?unassigned=1",
		`SELECT f.id::text, f.title, f.severity::text, COALESCE(i.name,''), f.created_at FROM faults f
		LEFT JOIN infrastructure i ON i.id = f.infrastructure_id
		WHERE f.assigned_to IS NULL AND f.assigned_group_id IS NULL AND f.status NOT IN ('resolved','closed') ORDER BY f.created_at DESC`},
	{"ticket", "broker_tickets", "ticket", "Tickets", "ti-ticket", "/tickets/", "/tickets?unassigned=1",
		`SELECT t.id::text, t.title, t.priority::text, COALESCE(i.name,''), t.created_at FROM tickets t
		LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
		WHERE t.assigned_to IS NULL AND t.assigned_group_id IS NULL AND t.status NOT IN ('resolved','closed') ORDER BY t.created_at DESC`},
	{"task", "broker_tasks", "task", "Aufgaben", "ti-list-check", "/tasks/", "/tasks",
		`SELECT t.id::text, t.title, t.priority, COALESCE(i.name,''), t.created_at FROM tasks t
		LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
		WHERE t.assigned_group_id IS NULL AND t.status NOT IN ('resolved','closed')
		AND NOT EXISTS (SELECT 1 FROM task_assignees a WHERE a.task_id = t.id) ORDER BY t.created_at DESC`},
	{"maintenance", "broker_maintenance", "maintenance_task", "Wartungen", "ti-tool", "/maintenance/tasks/", "/maintenance",
		`SELECT m.id::text, m.title, m.priority::text, COALESCE(i.name,''), m.created_at FROM maintenance_tasks m
		LEFT JOIN infrastructure i ON i.id = m.infrastructure_id
		WHERE m.assigned_to IS NULL AND m.assigned_group_id IS NULL AND m.status IN ('open','in_progress','pending')
		AND m.due_date - make_interval(days => COALESCE((SELECT p.lead_days FROM maintenance_plans p WHERE p.id = m.plan_id), 0)) < NOW() + INTERVAL '1 day'
		ORDER BY m.due_date`},
}

const unassignedPerKind = 15

var assignPrioLabels = map[string]string{"low": "niedrig", "medium": "mittel", "high": "hoch", "critical": "kritisch"}

// brokerUnassigned: fuer jede Art, fuer die der Benutzer Broker ist, die offenen
// Vorgaenge ohne Zuweisung (Abteilungs-Sichtbarkeit beachtet).
func (h *Handler) brokerUnassigned(ctx context.Context, r *http.Request, userID string) []unassignedGroup {
	var flags [4]bool
	if h.db.QueryRow(ctx, `SELECT broker_faults, broker_tickets, broker_tasks, broker_maintenance FROM users WHERE id = $1::uuid`, userID).
		Scan(&flags[0], &flags[1], &flags[2], &flags[3]) != nil {
		return nil
	}
	now := time.Now()
	var out []unassignedGroup
	for k, def := range unassignedKinds {
		if !flags[k] {
			continue
		}
		scope := h.scopeAllowedIDs(r, def.ref)
		g := unassignedGroup{Kind: def.kind, Label: def.label, Icon: def.icon, ListURL: def.list}
		rows, err := h.db.Query(ctx, def.query)
		if err != nil {
			componentLog("zuweisung").Warn().Err(err).Str("art", def.kind).Msg("nicht zugewiesene nicht ladbar")
			continue
		}
		for rows.Next() {
			var id, title, prio, infra string
			var created time.Time
			if rows.Scan(&id, &title, &prio, &infra, &created) != nil {
				continue
			}
			if scope != nil && !scope[id] {
				continue
			}
			if len(g.Items) >= unassignedPerKind {
				g.More++
				continue
			}
			g.Items = append(g.Items, unassignedItem{Title: title, Href: def.href + id, Priority: prio,
				PrioLabel: assignPrioLabels[prio], Infra: infra, Age: unassignedAge(now.Sub(created))})
		}
		rows.Close()
		out = append(out, g)
	}
	return out
}

// unassignedAge: „seit 5 Min.“, „seit 3 Std.“, „seit 2 Tagen“.
func unassignedAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "seit " + strconv.Itoa(int(d.Minutes())) + " Min."
	case d < 48*time.Hour:
		return "seit " + strconv.Itoa(int(d.Hours())) + " Std."
	}
	return "seit " + strconv.Itoa(int(d.Hours()/24)) + " Tagen"
}

func (h *Handler) AssignmentNewPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := AssignmentNewPageData{
		BaseData:                  h.baseData(r, "assignments-new", "Neue Zuweisung", "Auftrag anlegen und zuweisen"),
		Users:                     h.userOptions(ctx),
		Groups:                    h.loadGroups(ctx),
		DefaultDueDaysTicket:      appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTicket, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysTask:        appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTask, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysMaintenance: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysMaintenance, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysFault:       appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysFault, appsettings.DefaultDueDaysFallback),
	}
	data.CanCreateRestricted = h.isGlobalBoardAdminOrManager(r)
	q := r.URL.Query()
	switch t := q.Get("type"); t {
	case "ticket", "fault":
		data.PresetType = t
	case "task", "maintenance":
		if data.CanCreateRestricted {
			data.PresetType = t
		}
	}
	data.Unassigned = h.brokerUnassigned(ctx, r, getUser(r).ID)
	if id := q.Get("infra"); uuidInPathRe.MatchString(id) && len(id) == 36 {
		if err := h.db.QueryRow(ctx, `SELECT `+assetPathExpr("$1::uuid"), id).Scan(&data.InfraPath); err == nil && data.InfraPath != "" {
			data.PresetInfraID = id
		}
	}
	if id := q.Get("it"); uuidInPathRe.MatchString(id) && len(id) == 36 && h.canViewIT(r) {
		if err := h.db.QueryRow(ctx, `SELECT name FROM it_assets WHERE id = $1::uuid`, id).Scan(&data.ITName); err == nil {
			data.PresetITID = id
		}
	}
	if b := q.Get("back"); strings.HasPrefix(b, "/a/") && loginNext(b) == b {
		data.Back = b
	}
	h.render(w, "assignment_new", data)
}
