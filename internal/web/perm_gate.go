package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// Rechte auf Abteilungsebene und Bereichsadministratoren im Web
// (Grundlage: internal/core/rbac/scope.go, Migration 147).
//
//   - recordDepartments: Abteilung(en) eines Datensatzes – über seine Anlage
//     (infrastructure_department) und die Abteilung der zugewiesenen Gruppe.
//   - hasPermRec: Recht für genau diesen Datensatz (Abteilung beachtet).
//   - PermissionGateMiddleware: endgültiges Löschen nur für den
//     Bereichsadministrator, Löschvormerkung über die Schnittstelle nur mit
//     Bearbeiten-Recht des Bereichs.

// recordDeptSource: Tabelle eines Datensatz-Typs und ob er eine Gruppe hat.
var recordDeptSource = map[string]struct {
	table string
	group bool
}{
	"ticket": {"tickets", true}, "fault": {"faults", true}, "maintenance_task": {"maintenance_tasks", true},
	"maintenance_plan": {"maintenance_plans", true}, "task": {"tasks", true}, "project": {"projects", true},
	"it_asset": {"it_assets", false}, "kvp": {"kvp_ideas", false}, "part": {"spare_parts", false},
}

// recordDepartments: Abteilungen eines Datensatzes (leer = keiner Abteilung
// zugeordnet – dann gilt ein abteilungsbegrenztes Recht wie überall).
func (h *Handler) recordDepartments(ctx context.Context, refType, id string) []string {
	if h.db == nil || id == "" {
		return nil
	}
	refType = refAlias(refType)
	q := ""
	if refType == "infrastructure" {
		q = `SELECT department_id::text FROM infrastructure_department WHERE infrastructure_id = $1::uuid`
	} else if src, ok := recordDeptSource[refType]; ok {
		q = `SELECT idp.department_id::text FROM ` + src.table + ` r JOIN infrastructure_department idp ON idp.infrastructure_id = r.infrastructure_id WHERE r.id = $1::uuid`
		if src.group {
			q += ` UNION SELECT g.department_id::text FROM ` + src.table + ` r JOIN user_groups g ON g.id = r.assigned_group_id WHERE r.id = $1::uuid AND g.department_id IS NOT NULL`
		}
	} else {
		return nil
	}
	rows, err := h.db.Query(ctx, q, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil && d != "" {
			out = append(out, d)
		}
	}
	return out
}

// refAlias: Modulnamen aus Pfaden und Feldsätzen auf die Datensatz-Typen.
func refAlias(t string) string {
	switch t {
	case "maintenance":
		return "maintenance_task"
	case "it":
		return "it_asset"
	case "storage", "business_partner":
		return t
	}
	if a, ok := refTypeAlias[t]; ok {
		return a
	}
	return t
}

// hasPermRec: hat die angemeldete Person das Recht für diesen Datensatz?
func (h *Handler) hasPermRec(r *http.Request, perm, refType, id string) bool {
	if perm == "" {
		return true
	}
	if h.rbac == nil || !h.hasPerm(r, perm) {
		return false
	}
	return h.hasPermIn(r, perm, h.recordDepartments(r.Context(), refType, id))
}

// areaAdminPerm: Bereichsadministrator-Recht je Modul (Feldsätze/Stammdaten).
var areaAdminPerm = map[string]string{
	"ticket": "tickets.admin", "fault": "faults.admin", "maintenance_task": "maintenance.admin", "maintenance": "maintenance.admin",
	"task": "tasks.admin", "project": "projects.admin", "kvp": "kvp.admin", "infrastructure": "infrastructure.admin",
	"part": "inventory.admin", "storage": "inventory.admin", "business_partner": "directory.admin", "it_asset": "it.admin",
}

// isAreaAdmin: Bereichsadministrator für diesen Datensatz?
func (h *Handler) isAreaAdmin(r *http.Request, module, id string) bool {
	perm := areaAdminPerm[module]
	return perm != "" && h.hasPermRec(r, perm, module, id)
}

// gateRule: eine Schreib-Route, die ein Recht braucht.
//   - refType "" und keine ID: Anlegen – das Recht muss irgendwo vorhanden sein.
//   - refType "*": Datensatz-Typ steht im Pfad (Gruppe 1, ID in Gruppe 2);
//     perm "edit" bzw. "done" wird je Typ aufgelöst (recordPerms).
//   - involved: Beteiligte (zugewiesen, verantwortlich, angelegt, Gruppe)
//     dürfen auch ohne das Recht – wie im Abschluss-Assistenten.
type gateRule struct {
	methods  string // z. B. "POST" oder "POST PUT DELETE"
	perm     string
	refType  string
	involved bool
	re       *regexp.Regexp
}

// recordPerms: Bearbeiten- und Abschluss-Recht je Datensatz-Typ.
var recordPerms = map[string][2]string{
	"ticket": {"tickets.edit", "tickets.resolve"}, "fault": {"faults.edit", "faults.resolve"},
	"task": {"tasks.edit", "tasks.edit"}, "project": {"projects.edit", "projects.edit"},
	"maintenance_task": {"maintenance.edit", "maintenance.complete"}, "it_asset": {"it.edit", "it.edit"},
}

func gr(methods, perm, refType string, involved bool, pattern string) gateRule {
	return gateRule{methods, perm, refType, involved, regexp.MustCompile(pattern)}
}

var gID = `/` + uuidRe

const (
	gAny  = `(?:/.*)?$`
	gEnd  = `/?$`
	apiV1 = `^/api/v1`
	wPost = "POST"
	wAll  = "POST PUT PATCH DELETE"
)

var gateRules = []gateRule{
	// ── endgültig löschen: nur Bereichsadministrator ──
	gr("DELETE", "tasks.admin", "task", false, apiV1+`/tasks`+gID+gEnd),
	gr("DELETE", "projects.admin", "project", false, apiV1+`/projects`+gID+gEnd),
	gr(wPost, "maintenance.admin", "maintenance_task", false, `^/maintenance/tasks`+gID+`/delete-web$`),
	gr(wPost, "kvp.admin", "kvp", false, `^/kvp`+gID+`/delete$`),
	gr("DELETE", "maintenance.admin", "", false, apiV1+`/maintenance/checklists`+gID+gEnd),
	// ── Löschvormerkung von Stammdaten über die Schnittstelle ──
	gr("DELETE", "inventory.edit", "part", false, `^(?:/api/v1)?/inventory`+gID+gEnd),
	gr("DELETE", "inventory.edit", "storage", false, `^(?:/api/v1)?/storage`+gID+gEnd),
	gr("DELETE", "directory.edit", "business_partner", false, `^(?:/api/v1)?/directory`+gID+gEnd),
	gr("DELETE", "infrastructure.edit", "infrastructure", false, `^(?:/api/v1)?/infrastructure`+gID+gEnd),

	// ── Störungen (Melden bleibt für alle offen) ──
	gr(wPost, "faults.resolve", "fault", true, `^(?:/api/v1)?/faults`+gID+`/resolve$`),
	gr(wPost, "faults.edit", "fault", true, `^/faults`+gID+`/(?:analyze|infrastructure-web)$`),
	gr(wAll, "faults.edit", "fault", true, apiV1+`/faults`+gID+`/(?:analyze|cost-center|due-date|actions|pending-parts)`+gAny),
	// ── Tickets (Melden und Kommentieren bleiben offen) ──
	gr(wPost, "tickets.resolve", "ticket", true, `^/tickets`+gID+`/resolve-web$`),
	gr(wPost, "tickets.resolve", "ticket", true, apiV1+`/tickets`+gID+`/resolve$`),
	gr(wPost, "tickets.edit", "ticket", true, `^/tickets`+gID+`/(?:status-web|infrastructure-web)$`),
	gr(wAll, "tickets.edit", "ticket", true, apiV1+`/tickets`+gID+`/(?:status|cost-center|due-date|actions|pending-parts)`+gAny),
	// ── Wartung ──
	gr(wPost, "maintenance.edit", "", false, `^/maintenance/(?:generate|plans`+gID+`/duplicate-web)$`),
	gr(wPost, "maintenance.edit", "maintenance_task", true, `^/maintenance/tasks`+gID+`/(?:start-web|edit-web)$`),
	gr(wAll, "maintenance.edit", "", false, apiV1+`/maintenance/(?:plans|checklists|checklist-items)(?:/.*)?$`),
	gr(wPost, "maintenance.edit", "", false, apiV1+`/maintenance/tasks(?:/generate)?/?$`),
	gr(wPost, "maintenance.complete", "maintenance_task", true, apiV1+`/maintenance/tasks`+gID+`/complete$`),
	gr(wAll, "maintenance.edit", "maintenance_task", true, apiV1+`/maintenance/tasks`+gID+`/(?:due-date|start|steps|checklists|actions|pending-parts)`+gAny),
	// ── Aufgaben und Projekte ──
	gr(wPost, "tasks.edit", "", false, apiV1+`/tasks/?$`),
	gr(wAll, "tasks.edit", "task", true, apiV1+`/tasks`+gID+`/(?:edit|status|resolve|actions|pending-parts)`+gAny),
	gr(wPost, "projects.edit", "", false, apiV1+`/projects/?$`),
	gr(wPost, "projects.edit", "project", true, apiV1+`/projects`+gID+`/edit$`),
	// ── IT ──
	gr(wPost, "it.edit", "", false, `^(?:/api/v1)?/it/?$`),
	gr(wAll, "it.edit", "it_asset", true, `^(?:/api/v1)?/it`+gID+`/(?:edit-web|status-web|status)$`),
	// ── Lager und Lagerorte (Stammdaten) ──
	gr(wPost, "inventory.create", "", false, `^(?:/api/v1)?/inventory/?$`),
	gr(wPost, "inventory.edit", "", false, apiV1+`/inventory/(?:fields|field-sets)(?:/.*)?$`),
	gr(wPost, "inventory.edit", "part", false, apiV1+`/inventory`+gID+`(?:/field-sets?|/fields)?/?$`),
	gr(wPost, "inventory.book", "", false, apiV1+`/inventory/book/?$`),
	gr(wPost, "inventory.book", "", false, apiV1+`/inventory/stocktake(?:/.*)?$`),
	gr(wPost, "inventory.create", "", false, `^(?:/api/v1)?/storage(?:/?|`+gID+`/(?:children|children-web))$`),
	gr(wPost, "inventory.edit", "storage", false, apiV1+`/storage`+gID+gEnd),
	// ── Hersteller & Lieferanten über die Schnittstelle ──
	gr(wPost, "directory.edit", "", false, apiV1+`/directory/?$`),
	gr(wPost, "directory.edit", "business_partner", false, apiV1+`/directory`+gID+gEnd),
	// ── Beteiligte, Gruppe, Archiv an beliebigen Vorgängen ──
	gr(wPost, "edit", "*", true, `^/records/([a-z_]+)`+gID+`/(?:people|group|parties)(?:/.*)?$`),
	gr(wPost, "done", "*", true, `^/records/([a-z_]+)`+gID+`/archive$`),
}

// PermissionGateMiddleware wird im Wurzel-Router vor ChangeTrackingMiddleware
// eingehängt (main.go): alle Schreib-Routen aus gateRules brauchen ihr Recht –
// für einen Datensatz in seiner Abteilung (Rechte auf Abteilungsebene).
func (h *Handler) PermissionGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || h.rbac == nil {
			next.ServeHTTP(w, r)
			return
		}
		g, perm, refType, id, hit := matchGate(r.Method, r.URL.Path)
		if hit {
			if uid := h.requestActor(r); uid != "" && h.users != nil {
				if u, err := h.users.GetByID(r.Context(), uid); err == nil && u != nil {
					ok := h.rbac.HasPermissionForUser(u.ID, string(u.Role), perm)
					if ok && id != "" {
						ok = h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), perm, h.recordDepartments(r.Context(), refType, id))
					}
					if !ok && g.involved && id != "" {
						ok = h.recordInvolved(r.Context(), u.ID, refType, id)
					}
					if !ok {
						componentLog("rechte").Info().Str("user", u.ID).Str("recht", perm).Str("pfad", r.URL.Path).Msg("schreibzugriff ohne recht abgelehnt")
						if strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Accept"), "json") {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusForbidden)
							_, _ = w.Write([]byte(`{"success":false,"error":"Keine Berechtigung: Recht ` + perm + ` fehlt"}`))
							return
						}
						http.Error(w, "Keine Berechtigung: dafür brauchst du das Recht „"+h.permLabel(r, perm)+"“", http.StatusForbidden)
						return
					}
				}
			}
			// ohne Anmeldung lehnt die Route selbst ab
		}
		next.ServeHTTP(w, r)
	})
}

// matchGate: Regel, nötiges Recht, Datensatz-Typ und -ID für eine Schreib-Route.
func matchGate(method, path string) (gateRule, string, string, string, bool) {
	for _, g := range gateRules {
		if !strings.Contains(" "+g.methods+" ", " "+method+" ") {
			continue
		}
		m := g.re.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		perm, refType, id := g.perm, g.refType, ""
		if refType == "*" {
			refType, id = refAlias(m[1]), m[2]
			p, ok := recordPerms[refType]
			if !ok {
				return g, "", "", "", false // unbekannter Typ: die Route prüft selbst
			}
			perm = p[0]
			if g.perm == "done" {
				perm = p[1]
			}
		} else if refType != "" && len(m) > 1 {
			id = m[1]
		}
		return g, perm, refType, id, true
	}
	return gateRule{}, "", "", "", false
}

// recordInvolved: ist die Person am Datensatz beteiligt (zugewiesen,
// verantwortlich, angelegt oder über ihre Gruppe)?
func (h *Handler) recordInvolved(ctx context.Context, uid, refType, id string) bool {
	st, ok := scopeTables[refType]
	if !ok || h.db == nil {
		return false
	}
	var yes bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+st.table+` r WHERE r.id = $2::uuid AND (`+st.people+`))`, uid, id).Scan(&yes)
	return yes
}

// permLabel: Bezeichnung eines Rechts (für Meldungen).
func (h *Handler) permLabel(r *http.Request, key string) string {
	if perms, err := h.rbac.ListPermissions(r.Context()); err == nil {
		for _, p := range perms {
			if p.Key == key {
				return p.Label
			}
		}
	}
	return key
}
