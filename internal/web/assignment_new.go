package web

import "net/http"

// AssignmentNewPageData speist die modulübergreifende "Neue Zuweisung"-
// Seite: ein zentraler Einstieg, um Tickets, Aufgaben, Wartungen und
// Störungen direkt mit einem zuständigen Mitarbeiter anzulegen, statt
// jedes Modul einzeln aufzurufen. Erstellt wird weiterhin über die
// bestehenden JSON-APIs der jeweiligen Module (/api/v1/tickets/, /tasks/,
// /maintenance/tasks, /faults/) - hier wird nichts dupliziert.
type AssignmentNewPageData struct {
	BaseData
	Users []UserOption
}

func (h *Handler) AssignmentNewPage(w http.ResponseWriter, r *http.Request) {
	data := AssignmentNewPageData{
		BaseData: h.baseData(r, "assignments-new", "Neue Zuweisung", "Auftrag anlegen und zuweisen"),
		Users:    h.userOptions(r.Context()),
	}
	h.render(w, "assignment_new", data)
}
