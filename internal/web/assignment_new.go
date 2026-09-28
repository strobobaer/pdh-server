package web

import (
	"net/http"

	"pdh/pkg/appsettings"
)

// AssignmentNewPageData speist die modulübergreifende "Neue Zuweisung"-
// Seite: ein zentraler Einstieg, um Tickets, Aufgaben, Wartungen und
// Störungen direkt mit einem zuständigen Mitarbeiter anzulegen, statt
// jedes Modul einzeln aufzurufen. Erstellt wird weiterhin über die
// bestehenden JSON-APIs der jeweiligen Module (/api/v1/tickets/, /tasks/,
// /maintenance/tasks, /faults/) - hier wird nichts dupliziert.
type AssignmentNewPageData struct {
	BaseData
	Users                     []UserOption
	DefaultDueDaysTicket      int
	DefaultDueDaysTask        int
	DefaultDueDaysMaintenance int
	DefaultDueDaysFault       int
}

func (h *Handler) AssignmentNewPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := AssignmentNewPageData{
		BaseData:                  h.baseData(r, "assignments-new", "Neue Zuweisung", "Auftrag anlegen und zuweisen"),
		Users:                     h.userOptions(ctx),
		DefaultDueDaysTicket:      appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTicket, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysTask:        appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTask, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysMaintenance: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysMaintenance, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysFault:       appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysFault, appsettings.DefaultDueDaysFallback),
	}
	h.render(w, "assignment_new", data)
}
