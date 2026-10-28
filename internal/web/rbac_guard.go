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
