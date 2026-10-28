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

// gateRule: eine Schreib-Route, die ein Recht für den Datensatz braucht.
type gateRule struct {
	method, perm, refType string
	re                    *regexp.Regexp // Gruppe 1 = Datensatz-ID
}

var gateRules = []gateRule{
	// endgültig löschen: nur Bereichsadministrator
	{http.MethodDelete, "tasks.admin", "task", regexp.MustCompile(`^/api/v1/tasks/` + uuidRe + `/?$`)},
	{http.MethodDelete, "projects.admin", "project", regexp.MustCompile(`^/api/v1/projects/` + uuidRe + `/?$`)},
	{http.MethodPost, "maintenance.admin", "maintenance_task", regexp.MustCompile(`^/maintenance/tasks/` + uuidRe + `/delete-web$`)},
	{http.MethodPost, "kvp.admin", "kvp", regexp.MustCompile(`^/kvp/` + uuidRe + `/delete$`)},
	// Löschvormerkung von Stammdaten über die Schnittstelle: Bearbeiten-Recht
	{http.MethodDelete, "inventory.edit", "part", regexp.MustCompile(`^(?:/api/v1)?/inventory/` + uuidRe + `/?$`)},
	{http.MethodDelete, "inventory.edit", "storage", regexp.MustCompile(`^(?:/api/v1)?/storage/` + uuidRe + `/?$`)},
	{http.MethodDelete, "directory.edit", "business_partner", regexp.MustCompile(`^(?:/api/v1)?/directory/` + uuidRe + `/?$`)},
	{http.MethodDelete, "infrastructure.edit", "infrastructure", regexp.MustCompile(`^(?:/api/v1)?/infrastructure/` + uuidRe + `/?$`)},
}

// PermissionGateMiddleware wird im Wurzel-Router vor ChangeTrackingMiddleware
// eingehängt (main.go), damit auch die Löschvormerkung geprüft wird.
func (h *Handler) PermissionGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || h.rbac == nil {
			next.ServeHTTP(w, r)
			return
		}
		for _, g := range gateRules {
			if g.method != r.Method {
				continue
			}
			m := g.re.FindStringSubmatch(r.URL.Path)
			if m == nil {
				continue
			}
			uid := h.requestActor(r)
			if uid == "" || h.users == nil {
				break // ohne Anmeldung lehnt die Route selbst ab
			}
			u, err := h.users.GetByID(r.Context(), uid)
			if err != nil || u == nil {
				break
			}
			ok := h.rbac.HasPermissionForUser(u.ID, string(u.Role), g.perm) &&
				h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), g.perm, h.recordDepartments(r.Context(), g.refType, m[1]))
			if !ok {
				msg := "Keine Berechtigung: dafür brauchst du das Recht „" + h.permLabel(r, g.perm) + "“"
				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"success":false,"error":"keine berechtigung: ` + g.perm + `"}`))
					return
				}
				http.Error(w, msg, http.StatusForbidden)
				return
			}
			break
		}
		next.ServeHTTP(w, r)
	})
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
