package web

import (
	"context"
	"net/http"

	"pdh/internal/modules/it"
)

// IT-Assets: Ansehen mit it.view oder it.edit – jeweils für die Abteilung des
// Datensatzes (Anlage des Assets, samt Unterabteilungen), nicht nur global.
// Dieselbe Regel wie hasPermRec (globales Recht = alles, Abteilungsrecht =
// Assets dieser Abteilungen, Assets ohne Abteilung für alle mit dem Recht
// irgendwo, it.admin schließt beides ein). Seiten, Listen, Suche und
// Schnittstelle nutzen nur die Funktionen dieser Datei, damit kein Weg
// weiter oder enger greift als die anderen.

// itViewAnyFor: hat die Person it.view oder it.edit irgendwo (in irgendeiner Abteilung)?
func (h *Handler) itViewAnyFor(r *http.Request, uid, role string) bool {
	return h.hasPermFor(r, uid, role, "it.view") || h.hasPermFor(r, uid, role, "it.edit")
}

// itViewIn: darf die Person IT-Assets mit diesen Abteilungen sehen (leer = keiner Abteilung
// zugeordnet)? Es gilt die Abteilungsebene des Rechts selbst (Rolle und Einzelberechtigung,
// jeweils samt Unterabteilungen), nicht nur die der Rolle.
func (h *Handler) itViewIn(r *http.Request, uid, role string, depts []string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, "it.view") || h.hasPermHook(r, "it.edit")
	}
	if h.rbac == nil {
		return false
	}
	return h.rbac.HasPermissionInDepartment(uid, role, "it.view", depts) || h.rbac.HasPermissionInDepartment(uid, role, "it.edit", depts)
}

// itInvolved: ist die Person an diesem IT-Asset beteiligt (angelegt, verantwortlich, zugewiesen)?
// Beteiligte dürfen ihr Asset auch ohne IT-Recht sehen (wie die Schreib-Sperre sie kennt).
func (h *Handler) itInvolved(ctx context.Context, uid, id string) bool {
	return uid != "" && bareUUIDRe.MatchString(id) && h.involvedIn(ctx, uid, "it_asset", id)
}

// canViewITPage: darf die Person die Seite „IT-Assets“ öffnen (IT-Recht irgendwo oder an einem Asset beteiligt)?
func (h *Handler) canViewITPage(r *http.Request) bool {
	if h.canViewIT(r) {
		return true
	}
	u := getUser(r)
	return u.ID != "" && len(h.involvedIDs(r.Context(), u.ID, "it_asset")) > 0
}

// canViewITRec: darf die angemeldete Person dieses IT-Asset sehen? Mit it.view/it.edit für die
// Abteilung des Assets oder als Beteiligte. Ein Fehler beim Laden der Abteilung gilt nie als
// „keine Abteilung“, sondern als nicht sichtbar.
func (h *Handler) canViewITRec(r *http.Request, id string) bool {
	u := getUser(r)
	if h.canViewIT(r) {
		if h.rbac == nil {
			return h.hasPermHook != nil // nur Tests mit Rechte-Hook: dort gilt das globale Recht
		}
		if h.hasPermHook != nil && u.ID == "" {
			return true
		}
		depts, err := h.viewDepartments(r.Context(), "it_asset", id)
		if err != nil {
			componentLog("rechte").Error().Err(err).Str("typ", "it_asset").Msg("abteilung des datensatzes laden")
		} else if h.itViewIn(r, u.ID, string(u.Role), depts) {
			return true
		}
	}
	return h.itInvolved(r.Context(), u.ID, id)
}

// canViewITRecAs: wie canViewITRec für eine Person, die nicht aus der Web-Anmeldung kommt (Chat).
func (h *Handler) canViewITRecAs(ctx context.Context, uid, id string) bool {
	if uid == "" || (h.users == nil && h.userByIDFn == nil) {
		return false
	}
	u, err := h.userByID(ctx, uid)
	if err != nil || u == nil {
		return false
	}
	return h.canViewITRec((&http.Request{}).WithContext(context.WithValue(ctx, "user", u)), id)
}

// itPermVisibleIDs: die IT-Assets, die das Recht (it.view/it.edit samt Abteilung) zeigt;
// nil = alle. Bei einem Fehler: nichts.
func (h *Handler) itPermVisibleIDs(r *http.Request) map[string]bool {
	if h.rbac == nil {
		return nil // nur Tests mit Rechte-Hook
	}
	u := getUser(r)
	if u == nil || u.ID == "" {
		if h.hasPermHook != nil && h.itViewAnyFor(r, "", "") {
			return nil // nur Tests mit Rechte-Hook ohne Benutzer: das globale Recht gilt
		}
		return map[string]bool{}
	}
	if h.hasPermHook != nil && !h.itViewAnyFor(r, u.ID, string(u.Role)) {
		return map[string]bool{}
	}
	allV, depV := h.rbac.Scope(u.ID, string(u.Role), "it.view")
	allE, depE := h.rbac.Scope(u.ID, string(u.Role), "it.edit")
	if allV || allE {
		return nil
	}
	out := map[string]bool{}
	if len(depV) == 0 && len(depE) == 0 {
		return out
	}
	ctx := r.Context()
	depts, err := h.allRecordDepartments(ctx, "it_asset")
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", "it_asset").Msg("abteilungen der datensätze laden")
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT id::text FROM it_assets`)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", "it_asset").Msg("it-assets laden")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && h.itViewIn(r, u.ID, string(u.Role), depts[id]) {
			out[id] = true
		}
	}
	return out
}

// itVisibleIDs: die IT-Assets, die die angemeldete Person sehen darf – Recht samt Abteilung und
// Abteilungssperre der Rolle, dazu die, an denen sie beteiligt ist; nil = alle. Für Listen, Zähler
// und Exporte; einmal je Anfrage berechnen und weiterreichen.
func (h *Handler) itVisibleIDs(r *http.Request) map[string]bool {
	base, perm := h.scopeAllowedIDs(r, "it_asset"), h.itPermVisibleIDs(r)
	if perm == nil {
		return base
	}
	out := map[string]bool{}
	for id := range perm {
		if base == nil || base[id] {
			out[id] = true
		}
	}
	if u := getUser(r); u.ID != "" {
		for id := range h.involvedIDs(r.Context(), u.ID, "it_asset") {
			out[id] = true
		}
	}
	return out
}

// itStatsOf: die Kennzahlen der IT-Seite für eine Auswahl von Assets (nil = alle gelten).
func itStatsOf(list []*it.Asset, visible map[string]bool) map[string]int {
	s := map[string]int{"total": 0, "active": 0, "servers": 0, "network": 0}
	for _, a := range list {
		if visible != nil && !visible[a.ID] {
			continue
		}
		s["total"]++
		if a.Status == "active" {
			s["active"]++
		}
		switch a.Type {
		case "server":
			s["servers"]++
		case "network":
			s["network"]++
		}
	}
	return s
}

// infraDepartments: Abteilungen einer Anlage (mit Fehler: ein Fehler ist nie „keine Abteilung“).
func (h *Handler) infraDepartments(ctx context.Context, infraID string) ([]string, error) {
	if h.viewDeptErr != nil {
		return nil, h.viewDeptErr
	}
	if h.db == nil {
		return nil, errViewNoDB
	}
	rows, err := h.db.Query(ctx, `SELECT department_id::text FROM infrastructure_department WHERE infrastructure_id = $1::uuid`, infraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		if d != "" {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// mayEditITIn: darf die Person IT-Assets in dieser Anlage anlegen oder dorthin verschieben
// (it.edit für die Abteilung der Ziel-Anlage)? Ohne Anlage ("") ist das Asset für jeden mit
// IT-Recht sichtbar – das darf nur, wer it.edit überall hat.
func (h *Handler) mayEditITIn(r *http.Request, infraID string) bool {
	if !h.hasPerm(r, "it.edit") {
		return false
	}
	if h.rbac == nil {
		return h.hasPermHook != nil
	}
	if infraID == "" {
		if h.hasPermHook != nil {
			return true
		}
		u := getUser(r)
		all, _ := h.rbac.Scope(u.ID, string(u.Role), "it.edit")
		return all
	}
	depts, err := h.infraDepartments(r.Context(), infraID)
	if err != nil {
		componentLog("rechte").Error().Err(err).Str("typ", "infrastructure").Msg("abteilung der anlage laden")
		return false
	}
	return h.hasPermIn(r, "it.edit", depts)
}
