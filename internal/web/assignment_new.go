package web

import (
	"net/http"
	"strings"

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
	Groups                    []groupView // zuweisbare Gruppen (migrations/085)
	DefaultDueDaysTicket      int
	DefaultDueDaysTask        int
	DefaultDueDaysMaintenance int
	DefaultDueDaysFault       int
	// Vorbelegung von der QR-Infoseite (/a/<id>): ?type=&infra=&it=&back=
	PresetType               string
	PresetInfraID, InfraPath string
	PresetITID, ITName       string
	Back                     string // nach dem Anlegen hierher statt in die Liste
}

func (h *Handler) AssignmentNewPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := AssignmentNewPageData{
		BaseData:                  h.baseData(r, "assignments-new", "Neue Zuweisung", "Auftrag anlegen und zuweisen"),
		Users:                     h.userOptions(ctx),
		Groups:                    h.loadGroups(ctx),
		DefaultDueDaysTicket:      appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTicket, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysTask:        appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTask, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysMaintenance: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysMaintenance, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysFault:       appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysFault, appsettings.DefaultDueDaysFallback),
	}
	q := r.URL.Query()
	switch t := q.Get("type"); t {
	case "ticket", "task", "maintenance", "fault":
		data.PresetType = t
	}
	if id := q.Get("infra"); uuidInPathRe.MatchString(id) && len(id) == 36 {
		if err := h.db.QueryRow(ctx, `SELECT `+assetPathExpr("$1::uuid"), id).Scan(&data.InfraPath); err == nil && data.InfraPath != "" {
			data.PresetInfraID = id
		}
	}
	if id := q.Get("it"); uuidInPathRe.MatchString(id) && len(id) == 36 && h.canViewIT(r) {
		if err := h.db.QueryRow(ctx, `SELECT name FROM it_assets WHERE id = $1::uuid`, id).Scan(&data.ITName); err == nil {
			data.PresetITID = id
		}
	}
	if b := q.Get("back"); strings.HasPrefix(b, "/a/") && loginNext(b) == b {
		data.Back = b
	}
	h.render(w, "assignment_new", data)
}
