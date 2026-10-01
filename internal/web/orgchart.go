package web

import (
	"context"
	"net/http"
)

// OrgChartPageData zeigt die Rollen-Hierarchie als klassisches
// Organigramm: eine Stufe (Tier) pro Rangstufe, ranghöchste zuerst,
// darunter jeweils die zugeordneten aktiven Benutzer. Gleichrangige
// Rollen stehen nebeneinander in derselben Stufe. Die eigentliche
// Rollenzuweisung läuft weiterhin über UserRoleWeb
// (/users/{id}/role-web, per Drag&Drop), das bereits die Rang-Prüfung
// durchsetzt - das Organigramm ist nur eine zweite, visuelle Oberfläche
// dafür.
type OrgChartPageData struct {
	BaseData
	Tiers       []OrgChartTier
	Departments []*orgDeptNode // Abteilungsbaum (oberste Ebene)
}

// orgDeptNode: Abteilung im Organigramm mit Unterabteilungen.
type orgDeptNode struct {
	deptView
	Roles    []string
	Children []*orgDeptNode
}

// orgDeptTree baut aus den Abteilungen (Baumreihenfolge) die verschachtelte Struktur.
func (h *Handler) orgDeptTree(ctx context.Context) []*orgDeptNode {
	roles := map[string][]string{}
	if rows, err := h.db.Query(ctx, `SELECT department_id::text, label FROM roles WHERE department_id IS NOT NULL ORDER BY level DESC, label`); err == nil {
		for rows.Next() {
			var d, l string
			if rows.Scan(&d, &l) == nil {
				roles[d] = append(roles[d], l)
			}
		}
		rows.Close()
	}
	nodes := map[string]*orgDeptNode{}
	var top []*orgDeptNode
	for _, d := range h.loadDepartments(ctx) {
		n := &orgDeptNode{deptView: d, Roles: roles[d.ID]}
		nodes[d.ID] = n
		if p, ok := nodes[d.ParentID]; ok && d.Depth > 0 {
			p.Children = append(p.Children, n)
		} else {
			top = append(top, n)
		}
	}
	return top
}

// OrgChartTier fasst alle Rollen mit derselben Rangstufe zu einer Ebene
// des Organigramms zusammen.
type OrgChartTier struct {
	Level int
	Roles []OrgChartRole
}

type OrgChartRole struct {
	ID        string
	Key       string
	Label     string
	Level     int
	CanAssign bool   // Rang des angemeldeten Benutzers reicht, um hierher zuzuweisen
	Dept      string // Abteilung der Rolle
	Users     []OrgChartUser
}

type OrgChartUser struct {
	ID        string
	FullName  string
	Initials  string
	AvatarBg  string
	CanManage bool // Rang des angemeldeten Benutzers reicht, um diesen Benutzer zu verschieben
}

func (h *Handler) OrgChartPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	actorRoleKey := string(getUser(r).Role)

	roles, _ := h.rbac.ListRoles(ctx) // bereits sortiert: level DESC, label
	allUsers, _ := h.users.List(ctx)

	deptNames := map[string]string{}
	for _, d := range h.loadDepartments(ctx) {
		deptNames[d.ID] = d.Name
	}
	roleDept := h.roleDepartments(ctx)
	var tiers []OrgChartTier
	for _, ro := range roles {
		role := OrgChartRole{
			ID: ro.ID, Key: ro.Key, Label: ro.Label, Level: ro.Level,
			CanAssign: h.rbac.Outranks(actorRoleKey, ro.Key),
			Dept:      deptNames[roleDept[ro.ID]],
		}
		for _, u := range allUsers {
			if !u.Active || string(u.Role) != ro.Key {
				continue
			}
			initials, bg := avatarFor(u.FirstName, u.LastName)
			role.Users = append(role.Users, OrgChartUser{
				ID: u.ID, FullName: u.FirstName + " " + u.LastName,
				Initials: initials, AvatarBg: bg,
				CanManage: h.rbac.Outranks(actorRoleKey, ro.Key),
			})
		}
		if n := len(tiers); n > 0 && tiers[n-1].Level == ro.Level {
			tiers[n-1].Roles = append(tiers[n-1].Roles, role)
		} else {
			tiers = append(tiers, OrgChartTier{Level: ro.Level, Roles: []OrgChartRole{role}})
		}
	}

	data := OrgChartPageData{
		BaseData:    h.baseData(r, "orgchart", "Organigramm", "Rollen & Zuweisung"),
		Tiers:       tiers,
		Departments: h.orgDeptTree(ctx),
	}
	h.render(w, "orgchart", data)
}
