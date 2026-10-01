package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Personalstamm: Abteilungen (Stammdaten statt Freitext am Benutzer) und
// Gruppen mit Mitgliedern (migrations/084). Pflegen darf, wer Benutzer
// verwalten darf (system.manage_users).

type deptView struct {
	ID, Name, Description, ManagerID, ManagerName string
	ParentID, ParentName                          string
	IsGlobal                                      bool // uebergeordnet: Rollen sehen alle Vorgaenge
	MemberCount, RoleCount                        int
	Depth                                         int // Ebene im Baum (0 = oberste)
}

// Indent: Einrueckung fuer Auswahllisten und Baumansicht.
func (d deptView) Indent() string { return strings.Repeat("— ", d.Depth) }

// sortDeptTree ordnet Abteilungen als Baum (Eltern vor Kindern, je Ebene nach Name).
func sortDeptTree(list []deptView) []deptView {
	byParent := map[string][]deptView{}
	ids := map[string]bool{}
	for _, d := range list {
		ids[d.ID] = true
	}
	for _, d := range list {
		p := d.ParentID
		if !ids[p] {
			p = ""
		}
		byParent[p] = append(byParent[p], d)
	}
	var out []deptView
	seen := map[string]bool{}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, d := range byParent[parent] {
			if seen[d.ID] || depth > 50 {
				continue
			}
			seen[d.ID] = true
			d.Depth = depth
			out = append(out, d)
			walk(d.ID, depth+1)
		}
	}
	walk("", 0)
	for _, d := range list { // Reste (Zyklen) unten anhaengen
		if !seen[d.ID] {
			out = append(out, d)
		}
	}
	return out
}

type groupView struct {
	ID, Name, Description, DepartmentID, DepartmentName, LeadID, LeadName string
	MemberIDs                                                             map[string]bool
	MemberNames                                                           []string
}

// UserGroupRef: Gruppe, in der eine Person Mitglied ist.
type UserGroupRef struct{ ID, Name string }

type OrgUnitsData struct {
	BaseData
	Departments []deptView
	Groups      []groupView
	Users       []UserOption
	Message     string
	Error       string
}

func (h *Handler) loadDepartments(ctx context.Context) []deptView {
	rows, err := h.db.Query(ctx, `
		SELECT d.id::text, d.name, d.description, COALESCE(d.manager_id::text, ''),
		       COALESCE(m.first_name || ' ' || m.last_name, ''),
		       COALESCE(d.parent_id::text, ''), COALESCE(p.name, ''), d.is_global,
		       (SELECT COUNT(*) FROM users u WHERE u.department_id = d.id AND u.active),
		       (SELECT COUNT(*) FROM roles r WHERE r.department_id = d.id)
		  FROM departments d
		  LEFT JOIN users m ON m.id = d.manager_id
		  LEFT JOIN departments p ON p.id = d.parent_id
		 WHERE d.active
		 ORDER BY lower(d.name)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []deptView
	for rows.Next() {
		var d deptView
		if rows.Scan(&d.ID, &d.Name, &d.Description, &d.ManagerID, &d.ManagerName, &d.ParentID, &d.ParentName, &d.IsGlobal, &d.MemberCount, &d.RoleCount) == nil {
			out = append(out, d)
		}
	}
	return sortDeptTree(out)
}

// departmentNames: Auswahlliste fuer das Abteilungsfeld am Benutzer.
func (h *Handler) departmentNames(ctx context.Context) []string {
	var out []string
	for _, d := range h.loadDepartments(ctx) {
		out = append(out, d.Name)
	}
	return out
}

func (h *Handler) loadGroups(ctx context.Context) []groupView {
	rows, err := h.db.Query(ctx, `
		SELECT g.id::text, g.name, g.description, COALESCE(g.department_id::text, ''), COALESCE(d.name, ''),
		       COALESCE(g.lead_id::text, ''), COALESCE(l.first_name || ' ' || l.last_name, '')
		  FROM user_groups g
		  LEFT JOIN departments d ON d.id = g.department_id
		  LEFT JOIN users l ON l.id = g.lead_id
		 WHERE g.active
		 ORDER BY lower(g.name)`)
	if err != nil {
		return nil
	}
	var out []groupView
	idx := map[string]int{}
	for rows.Next() {
		g := groupView{MemberIDs: map[string]bool{}}
		if rows.Scan(&g.ID, &g.Name, &g.Description, &g.DepartmentID, &g.DepartmentName, &g.LeadID, &g.LeadName) == nil {
			idx[g.ID] = len(out)
			out = append(out, g)
		}
	}
	rows.Close()
	mrows, err := h.db.Query(ctx, `
		SELECT m.group_id::text, u.id::text, u.first_name || ' ' || u.last_name
		  FROM user_group_members m JOIN users u ON u.id = m.user_id
		 WHERE u.active
		 ORDER BY u.last_name, u.first_name`)
	if err != nil {
		return out
	}
	defer mrows.Close()
	for mrows.Next() {
		var gid, uid, name string
		if mrows.Scan(&gid, &uid, &name) != nil {
			continue
		}
		if i, ok := idx[gid]; ok {
			out[i].MemberIDs[uid] = true
			out[i].MemberNames = append(out[i].MemberNames, name)
		}
	}
	return out
}

// userGroups: Gruppen einer Person (Benutzer-Detailseite).
func (h *Handler) userGroups(ctx context.Context, userID string) []UserGroupRef {
	rows, err := h.db.Query(ctx, `
		SELECT g.id::text, g.name FROM user_group_members m JOIN user_groups g ON g.id = m.group_id
		 WHERE m.user_id = $1::uuid AND g.active ORDER BY lower(g.name)`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UserGroupRef
	for rows.Next() {
		var g UserGroupRef
		if rows.Scan(&g.ID, &g.Name) == nil {
			out = append(out, g)
		}
	}
	return out
}

// OrgUnitsPage: GET /admin/org-units – Abteilungen & Gruppen.
func (h *Handler) OrgUnitsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	h.render(w, "org_units", OrgUnitsData{
		BaseData:    h.baseData(r, "org_units", "Abteilungen & Gruppen", "Personalstamm"),
		Departments: h.loadDepartments(ctx),
		Groups:      h.loadGroups(ctx),
		Users:       h.userOptions(ctx),
		Message:     r.URL.Query().Get("msg"),
		Error:       r.URL.Query().Get("err"),
	})
}

func orgRedirect(w http.ResponseWriter, r *http.Request, anchor, msg string, err error) {
	target := "/admin/org-units?"
	if err != nil {
		s := err.Error()
		if strings.Contains(s, "idx_departments_name") || strings.Contains(s, "idx_user_groups_name") {
			s = "Dieser Name ist bereits vergeben"
		}
		target += "err=" + url.QueryEscape(s)
	} else {
		target += "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target+"#"+anchor, http.StatusSeeOther)
}

// DepartmentSaveWeb: POST /admin/departments – anlegen oder (mit id) aendern.
func (h *Handler) DepartmentSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 100 {
		orgRedirect(w, r, "departments", "", errors.New("Name ist Pflicht (höchstens 100 Zeichen)"))
		return
	}
	desc := strings.TrimSpace(r.FormValue("description"))
	manager := nullID(r.FormValue("manager_id"))
	parent := strings.TrimSpace(r.FormValue("parent_id"))
	global := r.FormValue("is_global") != ""
	var err error
	if id := strings.TrimSpace(r.FormValue("id")); id != "" {
		// keine Zyklen: die uebergeordnete Abteilung darf nicht darunter liegen
		var cycle bool
		if parent != "" {
			_ = h.db.QueryRow(r.Context(), `
				WITH RECURSIVE up AS (
					SELECT id, parent_id, 0 AS depth FROM departments WHERE id = $1::uuid
					UNION ALL SELECT d.id, d.parent_id, up.depth + 1 FROM up JOIN departments d ON d.id = up.parent_id WHERE up.depth < 50)
				SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, parent, id).Scan(&cycle)
		}
		if cycle {
			orgRedirect(w, r, "departments", "", errors.New("Eine Abteilung kann nicht unter sich selbst oder einer ihrer Unterabteilungen liegen"))
			return
		}
		_, err = h.db.Exec(r.Context(), `UPDATE departments SET name=$1, description=$2, manager_id=$3, parent_id=$4, is_global=$5 WHERE id=$6::uuid`,
			name, desc, manager, nullID(parent), global, id)
	} else {
		// eine frueher geloeschte Abteilung gleichen Namens wird wiederbelebt
		_, err = h.db.Exec(r.Context(), `
			INSERT INTO departments (name, description, manager_id, parent_id, is_global) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT ((lower(name))) DO UPDATE SET active = true, description = EXCLUDED.description, manager_id = EXCLUDED.manager_id,
			       parent_id = EXCLUDED.parent_id, is_global = EXCLUDED.is_global`,
			name, desc, manager, nullID(parent), global)
	}
	orgRedirect(w, r, "departments", "Abteilung gespeichert", err)
}

// DepartmentDeleteWeb: Abteilung entfernen; Mitarbeitende, Rollen und Gruppen
// verlieren nur die Zuordnung.
func (h *Handler) DepartmentDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM departments WHERE id = $1::uuid`, chi.URLParam(r, "id"))
	orgRedirect(w, r, "departments", "Abteilung entfernt", err)
}

// GroupSaveWeb: POST /admin/groups – anlegen oder aendern, inkl. Mitglieder.
func (h *Handler) GroupSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len([]rune(name)) > 100 {
		orgRedirect(w, r, "groups", "", errors.New("Name ist Pflicht (höchstens 100 Zeichen)"))
		return
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		orgRedirect(w, r, "groups", "", err)
		return
	}
	defer tx.Rollback(ctx)
	id := strings.TrimSpace(r.FormValue("id"))
	desc := strings.TrimSpace(r.FormValue("description"))
	if id != "" {
		_, err = tx.Exec(ctx, `UPDATE user_groups SET name=$1, description=$2, department_id=$3, lead_id=$4 WHERE id=$5::uuid`,
			name, desc, nullID(r.FormValue("department_id")), nullID(r.FormValue("lead_id")), id)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO user_groups (name, description, department_id, lead_id) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			name, desc, nullID(r.FormValue("department_id")), nullID(r.FormValue("lead_id"))).Scan(&id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM user_group_members WHERE group_id = $1::uuid`, id)
	}
	for _, uid := range r.Form["member_ids"] {
		if err != nil {
			break
		}
		if uid = strings.TrimSpace(uid); uid != "" {
			_, err = tx.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, uid)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	orgRedirect(w, r, "groups", "Gruppe gespeichert", err)
}

func (h *Handler) GroupDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM user_groups WHERE id = $1::uuid`, chi.URLParam(r, "id"))
	orgRedirect(w, r, "groups", "Gruppe entfernt", err)
}

// UserGroupsSaveWeb: POST /users/{id}/groups – Gruppen einer Person setzen
// (Benutzer-Detailseite, Reiter Stammdaten).
func (h *Handler) UserGroupsSaveWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u, err := h.users.GetByID(ctx, id)
	if err != nil || u == nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if _, editMaster, _, _ := h.userAccess(r, id, string(u.Role)); !editMaster {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		userRedirect(w, r, id, "master", "", err)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `DELETE FROM user_group_members WHERE user_id = $1::uuid`, id)
	for _, gid := range r.Form["group_ids"] {
		if err != nil {
			break
		}
		if gid = strings.TrimSpace(gid); gid != "" {
			_, err = tx.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, gid, id)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err == nil {
		h.addHistory(ctx, "user", id, "update", "Gruppen", "", "", "Gruppen geändert", getUser(r).ID)
	}
	userRedirect(w, r, id, "master", "Gruppen gespeichert", err)
}

// RoleDepartmentWeb: POST /admin/roles/{id}/department-web – Abteilung einer Rolle.
func (h *Handler) RoleDepartmentWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	role, err := h.rbac.GetRoleByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Rolle nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.outranksRole(r, role.Key) {
		http.Error(w, "keine berechtigung, diese Rolle zu bearbeiten", http.StatusForbidden)
		return
	}
	r.ParseForm()
	if _, err := h.db.Exec(r.Context(), `UPDATE roles SET department_id = $1 WHERE id = $2::uuid`, nullID(r.FormValue("department_id")), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// roleDepartments: Rolle-ID -> Abteilungs-ID (Rollenseite).
func (h *Handler) roleDepartments(ctx context.Context) map[string]string {
	out := map[string]string{}
	rows, err := h.db.Query(ctx, `SELECT id::text, COALESCE(department_id::text, '') FROM roles`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, dep string
		if rows.Scan(&id, &dep) == nil {
			out[id] = dep
		}
	}
	return out
}
