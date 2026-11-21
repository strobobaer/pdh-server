package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// „Meine Vorgänge“: offene Arbeit eines Benutzers über Störungen, Tickets,
// Aufgaben, Wartungen (und fürs Startseiten-Widget Rüstzeiten). Gemeinsame
// Grundlage des Widgets „Meine Aufgaben“ (loadListMine) und der PDH-App
// (GET /api/app/v1/work).

// myWorkRow: eine Zeile aus myOpenWork. Number ist leer, wo die Art keine
// eigene Nummer hat (nur Rüstzeiten haben „RZ-…“).
type myWorkRow struct {
	Type, ID, Number, Title, Status, Priority, AssetID, AssetName string
	Due                                                           *time.Time // nur Datum (UTC-Mitternacht)
	UpdatedAt                                                     time.Time
}

// appWorkLimit: so viele Vorgänge liefert die App höchstens.
const appWorkLimit = 100

// myWorkKind: Alias der Tabelle und Bereichsart der Abteilungsbegrenzung je Vorgangsart.
var myWorkKind = map[string]struct{ alias, scopeRef string }{
	"ticket":      {"r", "ticket"},
	"fault":       {"r", "fault"},
	"task":        {"t", "task"}, // myTasksWhere nennt die Tabelle t
	"maintenance": {"r", "maintenance_task"},
	setupRef:      {"s", setupRef},
}

// myOpenWorkSQL: je Art der SELECT (Spalten: k, id, number, title, status,
// priority, asset_id, asset_name, due, updated_at). cond ist die
// Abteilungsbedingung (" AND …", Platzhalter $2/$3) oder leer.
func myOpenWorkSQL(kind, cond string) string {
	const cols = `, COALESCE(i.id::text, ''), COALESCE(i.name, '')`
	switch kind {
	case "ticket":
		return `SELECT 'ticket', r.id::text, ''::text, r.title::text, r.status::text, r.priority::text` + cols + `, r.due_date::date, r.updated_at
			FROM tickets r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE (r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs + `)
			  AND r.status IN ('open','in_progress','pending') AND r.archived_at IS NULL` + cond
	case "fault":
		return `SELECT 'fault', r.id::text, ''::text, r.title::text, r.status::text, r.severity::text` + cols + `, r.due_date::date, r.updated_at
			FROM faults r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE (r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs + `)
			  AND r.status IN ('detected','analyzing','in_progress','pending') AND r.archived_at IS NULL` + cond
	case "task":
		return `SELECT 'task', t.id::text, ''::text, t.title::text, t.status::text, t.priority::text` + cols + `, t.due_date::date, t.updated_at
			FROM tasks t LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
			WHERE ` + myTasksWhere + cond
	case "maintenance":
		return `SELECT 'maintenance', r.id::text, ''::text, r.title::text, r.status::text, r.priority::text` + cols + `, r.due_date::date, r.updated_at
			FROM maintenance_tasks r LEFT JOIN infrastructure i ON i.id = r.infrastructure_id
			WHERE (r.assigned_to = $1::uuid OR r.responsible_to = $1::uuid OR r.assigned_group_id IN ` + myGroupIDs + `)
			  AND r.status::text IN ('open','in_progress','pending') AND r.archived_at IS NULL` + cond
	case setupRef:
		// laufende Rüstzeiten beim Anlagenverantwortlichen und beim Schichtführer (beendete nicht)
		return `SELECT 'setup', s.id::text, 'RZ-' || lpad(s.number::text, greatest(4, length(s.number::text)), '0'), (` + setupTitleSQL + `)::text, s.status::text, ''::text` + cols + `, NULL::date, s.updated_at
			FROM setup_times s LEFT JOIN infrastructure i ON i.id = s.infrastructure_id
			WHERE (s.responsible_to = $1::uuid OR s.assigned_to = $1::uuid) AND s.status = 'running' AND s.archived_at IS NULL` + cond
	}
	return ""
}

// myOpenWork liefert die offenen Vorgänge von uid der angegebenen Arten
// ("ticket", "fault", "task", "maintenance", setupRef), höchstens limit,
// sortiert nach Fälligkeit (leer zuletzt), Priorität, zuletzt geändert zuerst.
// Es gilt die Abteilungsbegrenzung des Benutzers; Rechte je Art prüft der Aufrufer.
func (h *Handler) myOpenWork(ctx context.Context, uid string, kinds []string, limit int) ([]myWorkRow, error) {
	scope := h.scopeForUserID(ctx, uid)
	args := []any{uid}
	var parts []string
	for _, k := range kinds {
		mk := myWorkKind[k]
		cond, a := scopeSQL(scope, mk.scopeRef, mk.alias, 1)
		if a != nil {
			args = []any{uid, a[0], a[1]} // $2/$3 sind für jede Art gleich
		}
		if q := myOpenWorkSQL(k, cond); q != "" {
			parts = append(parts, q)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	rows, err := h.db.Query(ctx, `SELECT * FROM (`+strings.Join(parts, "\nUNION ALL\n")+`) x(k, id, number, title, status, priority, asset_id, asset_name, due, updated_at)
		ORDER BY (due IS NULL), due,
		  CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
		  updated_at DESC LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []myWorkRow
	for rows.Next() {
		var w myWorkRow
		if err := rows.Scan(&w.Type, &w.ID, &w.Number, &w.Title, &w.Status, &w.Priority, &w.AssetID, &w.AssetName, &w.Due, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// AppWorkAPI: GET /api/app/v1/work – „Meine Vorgänge“ der App. Arten =
// allowed_types des Geräts ∩ Ansichtsrecht des Benutzers (wie das Widget).
func (h *Handler) AppWorkAPI(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	dev := appDevice(r)
	if dev == nil || !h.hasPerm(r, permAppUse) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	var kinds []string
	for _, k := range []string{"fault", "ticket", "task", "maintenance"} {
		if appKindAllowed(dev, k) && h.hasPerm(r, appViewPerm[k]) {
			kinds = append(kinds, k)
		}
	}
	rows, err := h.myOpenWork(r.Context(), u.ID, kinds, appWorkLimit)
	if err != nil {
		componentLog("app").Error().Err(err).Msg("meine vorgänge")
		appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "app_unavailable")
		return
	}
	items := appWorkItems(rows)
	terminalJSON(w, http.StatusOK, map[string]any{"items": items})
}

// appViewPerm: Ansichtsrecht je Vorgangsart (leer = keines; Aufgaben sieht, wer
// nicht durch Abteilung oder Beteiligung ausgeschlossen ist). Gemeinsam für die
// Liste und die Einzelansicht.
var appViewPerm = map[string]string{"fault": "faults.view", "ticket": "tickets.view", "task": "", "maintenance": "maintenance.view"}

// appKindAllowed: darf das Gerät diese Vorgangsart (allowed_types)?
func appKindAllowed(dev *appDeviceCtx, kind string) bool {
	if dev == nil {
		return false
	}
	for _, t := range dev.AllowedTypes {
		if t == kind {
			return true
		}
	}
	return false
}

// appWorkAccess: Ergebnis der Zugriffsprüfung auf einen Vorgang.
type appWorkAccess int

const (
	appWorkForbidden appWorkAccess = iota // Art, Recht oder Abteilung verbieten es (403)
	appWorkNotFound                       // Art oder Vorgang gibt es nicht (404)
	appWorkOK                             // sichtbar; die can-Flags gelten
)

// appWorkPerms: was die App an einem Vorgang sehen und tun darf (Benutzer ∩ Gerät,
// nie mehr als das Web). Außer bei Access == appWorkOK sind alle Flags false.
//
//	Start   – Arbeit beginnen (Bearbeiten-Recht oder beteiligt, nicht abgeschlossen)
//	Action  – Maßnahme eintragen (canWriteAction und die Art/der Status nimmt Maßnahmen an)
//	Comment – Kommentar schreiben (nur Ticket, nicht abgeschlossen)
//	Step    – Checklistenschritt erfassen (nur Wartung, Bearbeiten-Recht oder beteiligt, offen)
//	Finish  – abschließen (canFinish)
type appWorkPerms struct {
	Access                               appWorkAccess
	Start, Action, Comment, Step, Finish bool
	Closed                               bool // abgeschlossen oder archiviert (nur bei appWorkOK)
}

// See: Vorgang darf angezeigt werden.
func (p appWorkPerms) See() bool { return p.Access == appWorkOK }

// appWorkCan prüft, ob das App-Gerät samt Benutzer den Vorgang sehen darf, und
// bestimmt die erlaubten Aktionen nach den Regeln des Webs. r muss durch AppAuth
// gelaufen sein. Ein Fehler heißt: Datenbank nicht erreichbar (nichts freigeben).
func (h *Handler) appWorkCan(r *http.Request, kind, id string) (appWorkPerms, error) {
	k, ok := completionKinds[kind]
	if !ok { // „setup“ und alles Unbekannte gibt es in der App-Einzelansicht nicht
		return appWorkPerms{Access: appWorkNotFound}, nil
	}
	dev := appDevice(r)
	if dev == nil || !h.hasPerm(r, permAppUse) || !appKindAllowed(dev, kind) {
		return appWorkPerms{Access: appWorkForbidden}, nil
	}
	if !bareUUIDRe.MatchString(id) || h.db == nil {
		return appWorkPerms{Access: appWorkNotFound}, nil
	}
	ctx := r.Context()
	var status string
	var archived bool
	switch err := h.db.QueryRow(ctx, `SELECT status::text, archived_at IS NOT NULL FROM `+k.Table+` WHERE id = $1::uuid`, id).Scan(&status, &archived); {
	case errors.Is(err, pgx.ErrNoRows):
		return appWorkPerms{Access: appWorkNotFound}, nil
	case err != nil:
		return appWorkPerms{}, err
	}
	ref := myWorkKind[kind].scopeRef // Bereichsart der Rechte („maintenance_task“)
	u := getUser(r)
	if u == nil || u.ID == "" {
		return appWorkPerms{Access: appWorkForbidden}, nil
	}
	// Ansichtsrecht (in der Abteilung des Vorgangs) oder Beteiligung, und Abteilungssperre
	if !h.hasPermRec(r, appViewPerm[kind], ref, id) && !h.recordInvolved(ctx, u.ID, ref, id) {
		return appWorkPerms{Access: appWorkForbidden}, nil
	}
	if !h.recordInScope(ctx, h.requestScope(r), ref, id) {
		return appWorkPerms{Access: appWorkForbidden}, nil
	}
	p := appWorkPerms{Access: appWorkOK, Closed: closedStatus[status] || archived}
	// Abgeschlossen: nur ansehen. Betrachter schreiben in der App nie (strenger als
	// die Web-Wege Maßnahme/Kommentar, die Beteiligte ohne Rollenprüfung zulassen).
	if p.Closed || u.Role == "viewer" {
		return p, nil
	}
	involved := h.completionInvolved(r, k, id)
	p.Start = h.hasPermRec(r, k.EditPerm, ref, id) || involved
	if am, ok := actionModules[ref]; ok && am.Add != nil && am.Input(status) {
		p.Action = h.canWriteAction(r, ref, id)
	}
	p.Comment = kind == "ticket" && h.tickets != nil
	p.Step = kind == "maintenance" && (h.hasPermRec(r, k.EditPerm, ref, id) || involved)
	p.Finish = h.canFinish(r, k, id)
	return p, nil
}

// appWorkDetail: Felder der Einzelansicht, art-übergreifend.
type appWorkDetail struct {
	Title, Description, Status, Priority, AssetID, AssetName string
	Due                                                      string
	Actions                                                  []map[string]any
	Comments                                                 []map[string]any // nur Ticket
	Steps                                                    []map[string]any // nur Wartung
}

func appTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (h *Handler) appAssetName(ctx context.Context, id string) string {
	if id == "" || !bareUUIDRe.MatchString(id) {
		return ""
	}
	var name string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(name, '') FROM infrastructure WHERE id = $1::uuid`, id).Scan(&name)
	return name
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// loadAppWorkDetail liest den Vorgang über den Dienst seines Moduls. Jeder
// Lesefehler (auch von Maßnahmen, Kommentaren, Schritten) wird zurückgegeben –
// lieber „vorübergehend nicht verfügbar“ als stillschweigend leere Listen.
func (h *Handler) loadAppWorkDetail(ctx context.Context, kind, id string) (*appWorkDetail, error) {
	d := &appWorkDetail{}
	actions := func(n int, at func(i int) (string, string, time.Time)) {
		d.Actions = make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			text, by, t := at(i)
			d.Actions = append(d.Actions, map[string]any{"text": text, "by": by, "at": appTime(t)})
		}
	}
	switch kind {
	case "fault":
		if h.faults == nil {
			return nil, errNoService
		}
		f, err := h.faults.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Title, d.Description, d.Status, d.Priority = f.Title, f.Description, string(f.Status), string(f.Severity)
		d.AssetID = derefStr(f.InfrastructureID)
		as, err := h.faults.GetActions(ctx, id)
		if err != nil {
			return nil, err
		}
		actions(len(as), func(i int) (string, string, time.Time) {
			return as[i].Description, as[i].CreatedByName, as[i].CreatedAt
		})
	case "ticket":
		if h.tickets == nil {
			return nil, errNoService
		}
		t, err := h.tickets.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Title, d.Description, d.Status, d.Priority = t.Title, t.Description, string(t.Status), string(t.Priority)
		d.AssetID = derefStr(t.InfrastructureID)
		as, err := h.tickets.GetActions(ctx, id)
		if err != nil {
			return nil, err
		}
		actions(len(as), func(i int) (string, string, time.Time) {
			return as[i].Description, as[i].CreatedByName, as[i].CreatedAt
		})
		cs, err := h.tickets.ListComments(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Comments = []map[string]any{}
		for _, c := range cs {
			d.Comments = append(d.Comments, map[string]any{"text": c.Text, "by": c.UserName, "at": appTime(c.CreatedAt)})
		}
	case "task":
		if h.tasks == nil {
			return nil, errNoService
		}
		t, err := h.tasks.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Title, d.Description, d.Status, d.Priority = t.Title, t.Description, string(t.Status), string(t.Priority)
		d.AssetID = derefStr(t.InfrastructureID)
		as, err := h.tasks.GetActions(ctx, id)
		if err != nil {
			return nil, err
		}
		actions(len(as), func(i int) (string, string, time.Time) {
			return as[i].Description, as[i].CreatedByName, as[i].CreatedAt
		})
	case "maintenance":
		if h.maint == nil {
			return nil, errNoService
		}
		t, err := h.maint.GetTaskByID(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Title, d.Description, d.Status, d.Priority = t.Title, t.Description, string(t.Status), string(t.Priority)
		d.AssetID = t.InfrastructureID
		as, err := h.maint.GetActions(ctx, id)
		if err != nil {
			return nil, err
		}
		actions(len(as), func(i int) (string, string, time.Time) {
			return as[i].Description, as[i].CreatedByName, as[i].CreatedAt
		})
		ss, err := h.maint.Steps(ctx, id)
		if err != nil {
			return nil, err
		}
		d.Steps = []map[string]any{}
		for _, s := range ss {
			d.Steps = append(d.Steps, map[string]any{"id": s.ID, "label": s.Label, "kind": s.ItemType, "value": s.Value,
				"done": s.Done, "required": s.Required, "min": s.MinValue, "max": s.MaxValue, "in_range": s.InRange})
		}
	default:
		return nil, pgx.ErrNoRows
	}
	if d.Actions == nil {
		d.Actions = []map[string]any{}
	}
	// Fälligkeit genau wie in der Liste (myOpenWorkSQL: due_date::date in der Zeitzone der Datenbank)
	var due *time.Time
	if err := h.db.QueryRow(ctx, `SELECT due_date::date FROM `+completionKinds[kind].Table+` WHERE id = $1::uuid`, id).Scan(&due); err != nil {
		return nil, err
	}
	if due != nil {
		d.Due = due.Format("2006-01-02")
	}
	d.AssetName = h.appAssetName(ctx, d.AssetID)
	return d, nil
}

// appWorkDenied antwortet auf einen nicht sichtbaren Vorgang (404 bzw. 403);
// false heißt: sichtbar, weiter.
func appWorkDenied(w http.ResponseWriter, p appWorkPerms) bool {
	switch p.Access {
	case appWorkOK:
		return false
	case appWorkNotFound:
		appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
	default:
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
	}
	return true
}

// appUnavailable: 503 app_unavailable (vorübergehend; die App versucht es später erneut).
func appUnavailable(w http.ResponseWriter, err error, kind, id, what string) {
	componentLog("app").Error().Err(err).Str("art", kind).Str("vorgang", id).Msg(what)
	appJSONErr(w, http.StatusServiceUnavailable, "app_unavailable", "app_unavailable")
}

// AppWorkDetailAPI: GET /api/app/v1/work/{type}/{id} – ein Vorgang mit den
// Aktionen, die dieses Gerät und dieser Benutzer daran dürfen.
func (h *Handler) AppWorkDetailAPI(w http.ResponseWriter, r *http.Request) {
	kind, id := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	p, err := h.appWorkCan(r, kind, id)
	if err != nil {
		appUnavailable(w, err, kind, id, "vorgang prüfen")
		return
	}
	if appWorkDenied(w, p) {
		return
	}
	h.appWorkRespond(w, r, kind, id, p)
}

// appWorkRespond schreibt die Einzelansicht (gemeinsam für GET und alle
// schreibenden Wege, die danach den aktuellen Stand liefern).
func (h *Handler) appWorkRespond(w http.ResponseWriter, r *http.Request, kind, id string, p appWorkPerms) {
	d, err := h.loadAppWorkDetail(r.Context(), kind, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
			return
		}
		appUnavailable(w, err, kind, id, "vorgang laden")
		return
	}
	hist := []map[string]any{}
	for _, x := range h.queryHistory(r.Context(), historyRef(kind), id, 20) {
		hist = append(hist, map[string]any{"action": x.Action, "by": x.UserName, "at": appTime(x.At), "message": x.Message})
	}
	out := map[string]any{
		"type": kind, "id": id, "number": "", "title": d.Title, "description": d.Description, "status": d.Status, "priority": d.Priority,
		"asset": map[string]string{"id": d.AssetID, "name": d.AssetName}, "due": nil,
		"actions": d.Actions, "history": hist,
		"can": map[string]bool{"start": p.Start, "action": p.Action, "comment": p.Comment, "step": p.Step, "finish": p.Finish},
	}
	if d.Due != "" {
		out["due"] = d.Due
	}
	if d.Comments != nil {
		out["comments"] = d.Comments
	}
	if d.Steps != nil {
		out["steps"] = d.Steps
	}
	terminalJSON(w, http.StatusOK, out)
}
