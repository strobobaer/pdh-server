package web

import (
	"errors"
	"net/http"
	"sort"

	"pdh/internal/core/rbac"
)

// Rechte nur weitergeben, die man selbst hat: Wer Rollen verwalten darf, aber
// kein Administrator ist, kann einer Rolle oder einer Person nur Rechte
// geben, die er selbst besitzt. Wegnehmen geht immer (im Rahmen des Rangs).

var errPermUnknown = errors.New("unbekannte Berechtigung")

// permKeyByID: Schlüssel einer Berechtigung zur ID.
func (h *Handler) permKeyByID(r *http.Request, id string) (string, error) {
	perms, err := h.rbac.ListPermissions(r.Context())
	if err != nil {
		return "", err
	}
	for _, p := range perms {
		if p.ID == id {
			return p.Key, nil
		}
	}
	return "", errPermUnknown
}

// mayGrant: darf die handelnde Person dieses Recht vergeben?
func (h *Handler) mayGrant(r *http.Request, key string) bool {
	return h.actorIsAdmin(r) || h.hasPerm(r, key)
}

// mayGrantIn: darf die handelnde Person dieses Recht für die Abteilung
// vergeben ("" = alle Abteilungen)? Wer das Recht selbst nur in einer
// Abteilung hat, gibt es auch nur dort (oder darunter) weiter.
func (h *Handler) mayGrantIn(r *http.Request, key, dept string) bool {
	if h.actorIsAdmin(r) {
		return true
	}
	u := getUser(r)
	all, depts := h.rbac.Scope(u.ID, string(u.Role), key)
	if all {
		return true
	}
	return dept != "" && len(depts) > 0 && h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), key, []string{dept})
}

// hasPermIn: hat die angemeldete Person das Recht für einen Datensatz dieser
// Abteilungen (Rechte auf Abteilungsebene, rbac/scope.go)?
func (h *Handler) hasPermIn(r *http.Request, perm string, depts []string) bool {
	if perm == "" {
		return true
	}
	u := getUser(r)
	return h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), perm, depts)
}

// departmentExists: gibt es die (aktive) Abteilung?
func (h *Handler) departmentExists(r *http.Request, id string) bool {
	var ok bool
	_ = h.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM departments WHERE id::text = $1)`, id).Scan(&ok)
	return ok
}

// permCategoryOrder: Reihenfolge der Gruppen in der Matrix – erst die
// tägliche Arbeit, dann Material, Personal, Daten, zuletzt die Verwaltung.
// Unbekannte Kategorien folgen alphabetisch dahinter.
var permCategoryOrder = []string{
	"Störungen", "Tickets", "Wartung", "Aufgaben & Projekte", "KVP", "Leitstand & Dashboards",
	"Infrastruktur & Anlagen", "IT", "Lager", "Beschaffung", "Drucker", "Hersteller & Lieferanten",
	"Schichten & Abwesenheit", "Zeiterfassung", "Personal", "Chat", "Managementsysteme",
	"Daten & Schnittstellen", "VPN", "Stammdaten", "System & Sicherheit",
}

// groupPermissions: Rechte nach Kategorie in fester Reihenfolge, innerhalb
// einer Gruppe nach Anzeigename.
func groupPermissions(perms []*rbac.Permission) []PermissionGroup {
	rank := map[string]int{}
	for i, c := range permCategoryOrder {
		rank[c] = i
	}
	byCat := map[string][]*rbac.Permission{}
	var cats []string
	for _, p := range perms {
		if _, ok := byCat[p.Category]; !ok {
			cats = append(cats, p.Category)
		}
		byCat[p.Category] = append(byCat[p.Category], p)
	}
	sort.SliceStable(cats, func(i, j int) bool {
		ri, iok := rank[cats[i]]
		rj, jok := rank[cats[j]]
		if iok != jok {
			return iok
		}
		if iok {
			return ri < rj
		}
		return cats[i] < cats[j]
	})
	out := make([]PermissionGroup, 0, len(cats))
	for _, c := range cats {
		ps := byCat[c]
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Label < ps[j].Label })
		out = append(out, PermissionGroup{Category: c, Permissions: ps})
	}
	return out
}
