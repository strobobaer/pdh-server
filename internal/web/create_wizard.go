package web

import (
	"net/http"
	"strings"

	"pdh/pkg/appsettings"
)

// Erstellungs-Assistent (widgets/create_wizard.gohtml): legt Tickets,
// Stoerungen, Aufgaben und Wartungsauftraege Schritt fuer Schritt an – im
// gleichen Stil wie der Abschluss-Assistent. Angelegt wird ueber die
// bestehenden JSON-APIs der Module; hier gibt es nur die Auswahllisten und
// die Suche nach aehnlichen geloesten Stoerungen.

type createPerson struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
}

type createGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Members    int    `json:"members"`
}

// CreateOptionsWeb: GET /create/options – Personen, Gruppen, Standard-Fristen.
func (h *Handler) CreateOptionsWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := struct {
		Me      string         `json:"me"`
		People  []createPerson `json:"people"`
		Groups  []createGroup  `json:"groups"`
		DueDays map[string]int `json:"due_days"`
	}{Me: getUser(r).ID, People: []createPerson{}, Groups: []createGroup{}, DueDays: map[string]int{}}
	if rows, err := h.db.Query(ctx, `
		SELECT id::text, first_name || ' ' || last_name, COALESCE(department, '')
		  FROM users WHERE active AND NOT is_system_user AND NOT is_bot
		 ORDER BY last_name, first_name`); err == nil {
		for rows.Next() {
			var p createPerson
			if rows.Scan(&p.ID, &p.Name, &p.Department) == nil {
				out.People = append(out.People, p)
			}
		}
		rows.Close()
	}
	for _, g := range h.loadGroups(ctx) {
		out.Groups = append(out.Groups, createGroup{ID: g.ID, Name: g.Name, Department: g.DepartmentName, Members: len(g.MemberNames)})
	}
	for k, key := range map[string]string{"ticket": appsettings.KeyDefaultDueDaysTicket, "task": appsettings.KeyDefaultDueDaysTask,
		"maintenance": appsettings.KeyDefaultDueDaysMaintenance, "fault": appsettings.KeyDefaultDueDaysFault} {
		out.DueDays[k] = appsettings.GetInt(ctx, h.db, key, appsettings.DefaultDueDaysFallback)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateSimilarWeb: GET /create/similar?title=&description=&infra= – aehnliche
// geloeste Stoerungen, solange die neue Meldung noch erfasst wird.
func (h *Handler) CreateSimilarWeb(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	title, desc := strings.TrimSpace(q.Get("title")), strings.TrimSpace(q.Get("description"))
	type item struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Resolution string `json:"resolution"`
		Percent    int    `json:"percent"`
	}
	out := []item{}
	if len([]rune(title+desc)) >= 4 && h.faults != nil {
		var infra *string
		if v := strings.TrimSpace(q.Get("infra")); v != "" {
			infra = &v
		}
		if list, err := h.faults.SimilarForText(r.Context(), title, desc, infra, 3); err == nil {
			s := h.requestScope(r)
			for _, f := range list {
				if s != nil && !h.recordInScope(r.Context(), s, "fault", f.ID) {
					continue
				}
				out = append(out, item{ID: f.ID, Title: f.Title, Resolution: f.Resolution, Percent: int(f.Similarity*100 + 0.5)})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
