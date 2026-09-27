package web

import "net/http"

// OrgChartPageData zeigt die Rollen-Hierarchie als Spalten (ranghöchste
// zuerst) mit den ihr zugeordneten aktiven Benutzern darunter - zum
// schnellen Überblick und um Benutzer per Drag&Drop zwischen Rollen zu
// verschieben. Die eigentliche Rollenzuweisung läuft weiterhin über
// UserRoleWeb (/users/{id}/role-web), das bereits die Rang-Prüfung
// durchsetzt - das Organigramm ist nur eine zweite, visuelle Oberfläche
// dafür.
type OrgChartPageData struct {
	BaseData
	Columns []OrgChartColumn
}

type OrgChartColumn struct {
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

	roles, _ := h.rbac.ListRoles(ctx)
	allUsers, _ := h.users.List(ctx)

	columns := make([]OrgChartColumn, 0, len(roles))
	for _, ro := range roles {
		col := OrgChartColumn{
			ID: ro.ID, Key: ro.Key, Label: ro.Label, Level: ro.Level,
			CanAssign: h.rbac.Outranks(actorRoleKey, ro.Key),
		}
		for _, u := range allUsers {
			if !u.Active || string(u.Role) != ro.Key {
				continue
			}
			initials, bg := avatarFor(u.FirstName, u.LastName)
			col.Users = append(col.Users, OrgChartUser{
				ID: u.ID, FullName: u.FirstName + " " + u.LastName,
				Initials: initials, AvatarBg: bg,
				CanManage: h.rbac.Outranks(actorRoleKey, ro.Key),
			})
		}
		columns = append(columns, col)
	}

	data := OrgChartPageData{
		BaseData: h.baseData(r, "orgchart", "Organigramm", "Rollen & Zuweisung"),
		Columns:  columns,
	}
	h.render(w, "orgchart", data)
}
