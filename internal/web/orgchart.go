package web

import "net/http"

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
	Tiers []OrgChartTier
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
	CanAssign bool // Rang des angemeldeten Benutzers reicht, um hierher zuzuweisen
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

	var tiers []OrgChartTier
	for _, ro := range roles {
		role := OrgChartRole{
			ID: ro.ID, Key: ro.Key, Label: ro.Label, Level: ro.Level,
			CanAssign: h.rbac.Outranks(actorRoleKey, ro.Key),
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
		BaseData: h.baseData(r, "orgchart", "Organigramm", "Rollen & Zuweisung"),
		Tiers:    tiers,
	}
	h.render(w, "orgchart", data)
}
