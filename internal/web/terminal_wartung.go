package web

// Wartungen am Anlagenterminal (0.82.28): Nach der Kartenanmeldung zeigt die Kachel „Wartungen“ der angemeldeten
// Person die offenen Wartungsaufträge der Anlage des Terminals und ihrer Unteranlagen. Gezeigt wird, was die
// Person selbst sehen darf (Recht maintenance.view, je Abteilung des Auftrags). Zum Bearbeiten öffnet „PDH öffnen“
// (terminal_pdh.go) den Auftrag im normalen PDH – angemeldet als die Person, mit ihren eigenen Rechten
// (maintenance.edit, maintenance.complete …). Ohne Kartenanmeldung gibt es keine Kachel und keine Liste.

import (
	"net/http"
	"strings"
	"time"

	"pdh/internal/core/users"
)

const (
	terminalMaintShow = 50  // so viele Aufträge zeigt die Liste höchstens
	terminalMaintScan = 400 // so viele offene Aufträge werden höchstens auf Sichtbarkeit geprüft
)

type terminalMaintItem struct {
	ID, Title, Path, Due, Status string
	Overdue                      bool
}

// terminalMaintPage: Daten der Seite /terminal/wartung.
type terminalMaintPage struct {
	LoggedIn bool
	Person   string
	Items    []terminalMaintItem
	Total    int // sichtbare offene Aufträge (unter den geprüften)
}

// terminalMaintTile: Kachel nur mit Kartenanmeldung und nur, wenn die Person Wartungen irgendwo ansehen darf.
func (h *Handler) terminalMaintTile(person *users.User) bool {
	return person != nil && h.rbac != nil && h.rbac.HasPermissionForUser(person.ID, string(person.Role), "maintenance.view")
}

// TerminalWartungPage: GET /terminal/wartung
func (h *Handler) TerminalWartungPage(w http.ResponseWriter, r *http.Request) {
	p, uid, ok := h.terminalSetupGate(w, r, "wartung")
	if !ok {
		return
	}
	var person *users.User
	if h.db != nil && h.terminalDeviceSession(r) { // Kartenrechte nur in der Gerätesitzung
		var err error
		if person, err = h.cardSessionPerson(r.Context(), uid); err != nil {
			componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		}
	}
	p.SetupCard = person != nil
	m := terminalMaintPage{}
	if h.terminalMaintTile(person) {
		m.LoggedIn, m.Person = true, strings.TrimSpace(person.FirstName+" "+person.LastName)
		tree := h.terminalPlantTree(r, p.InfraID, p.Name)
		ids := make([]string, 0, len(tree.Nodes))
		for id := range tree.Nodes {
			ids = append(ids, id)
		}
		rows, err := h.db.Query(r.Context(), `SELECT id::text, title, status::text, due_date, infrastructure_id::text
			FROM maintenance_tasks WHERE infrastructure_id = ANY($1::uuid[]) AND archived_at IS NULL AND status IN ('open','in_progress','pending')
			ORDER BY due_date, lower(title) LIMIT $2`, ids, terminalMaintScan)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("wartungen lesen")
			http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
		type row struct {
			it    terminalMaintItem
			due   time.Time
			infra string
		}
		var all []row
		for rows.Next() {
			var x row
			if rows.Scan(&x.it.ID, &x.it.Title, &x.it.Status, &x.due, &x.infra) == nil {
				all = append(all, x)
			}
		}
		rows.Close()
		today := localDayStart(time.Now())
		for _, x := range all {
			it, due, infra := x.it, x.due, x.infra
			if !h.cardPersonCan(r.Context(), person, "maintenance.view", "maintenance_task", it.ID) {
				continue // nur, was die Person selbst sehen darf
			}
			m.Total++
			if len(m.Items) >= terminalMaintShow {
				continue
			}
			it.Due, it.Overdue = deDate(due), due.Before(today)
			it.Status = statusLabel(it.Status)
			it.Path = terminalPlantLabel(tree, infra)
			m.Items = append(m.Items, it)
		}
	}
	p.Maint = m
	h.renderTerminal(w, p)
}

// More: sichtbare offene Aufträge, die die Liste nicht zeigt.
func (m terminalMaintPage) More() int { return max(0, m.Total-len(m.Items)) }
