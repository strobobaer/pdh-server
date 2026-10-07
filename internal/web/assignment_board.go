package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Zuweisung (Broker-Eingang): alle offenen, nicht zugewiesenen Vorgaenge der
// Arten, fuer die der Benutzer Broker ist, auf einer Seite - jeder Vorgang
// laesst sich direkt einer Person, einer Gruppe oder sich selbst zuweisen.
// Nur fuer Broker (users.broker_*). Der neue Zustaendige bekommt den
// Aenderungshinweis von PDH-System (pdh_system.go).

const assignmentBoardLimit = 200

type AssignmentBoardData struct {
	BaseData
	Groups []unassignedGroup
	Users  []UserOption
	Teams  []groupView
	Total  int
}

// brokerFor: ist der Benutzer Broker fuer diese Art (ref wie in unassignedKinds)?
func (h *Handler) brokerFor(ctx context.Context, userID, ref string) bool {
	flags := h.brokerFlags(ctx, userID)
	for i, def := range unassignedKinds {
		if def.ref == ref {
			return flags[i]
		}
	}
	return false
}

func (h *Handler) isBroker(ctx context.Context, userID string) bool {
	f := h.brokerFlags(ctx, userID)
	return f[0] || f[1] || f[2] || f[3]
}

// AssignmentBoardPage: GET /assignments
func (h *Handler) AssignmentBoardPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	if !h.isBroker(ctx, u.ID) {
		http.Error(w, "Die Zuweisung steht nur Brokern zur Verfügung.", http.StatusForbidden)
		return
	}
	data := AssignmentBoardData{
		BaseData: h.baseData(r, "assignments", "Zuweisung", "Offene Vorgänge ohne Zuweisung"),
		Groups:   h.brokerUnassigned(ctx, r, u.ID, assignmentBoardLimit),
		Users:    h.userOptions(ctx),
		Teams:    h.loadGroups(ctx),
	}
	for _, g := range data.Groups {
		data.Total += len(g.Items) + g.More
	}
	h.render(w, "assignment_board", data)
}

// AssignmentBoardAssign: POST /assignments/{ref}/{id} mit target = "me",
// "u:<Benutzer-ID>" oder "g:<Gruppen-ID>". Zugewiesen wird nur, solange der
// Vorgang noch frei ist - greifen zwei Broker gleichzeitig zu, gewinnt der erste.
func (h *Handler) AssignmentBoardAssign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	ref, id := chi.URLParam(r, "ref"), chi.URLParam(r, "id")
	table, ok := groupTable(ref)
	if !ok || ref == "project" || ref == "maintenance_plan" || !h.brokerFor(ctx, u.ID, ref) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	if s := h.requestScope(r); s != nil && !h.recordInScope(ctx, s, ref, id) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "me" {
		target = "u:" + u.ID
	}
	kind, who, _ := strings.Cut(target, ":")
	if who == "" || (kind != "u" && kind != "g") {
		assignBoardResult(w, false, "Bitte eine Person oder Gruppe auswählen.")
		return
	}
	var label string
	if kind == "u" {
		label = h.chatUserName(ctx, who)
	} else {
		_ = h.db.QueryRow(ctx, `SELECT name FROM user_groups WHERE id = $1::uuid`, who).Scan(&label)
	}
	if label == "" {
		assignBoardResult(w, false, "Person oder Gruppe nicht gefunden.")
		return
	}

	free := `assigned_to IS NULL AND assigned_group_id IS NULL`
	if ref == "task" {
		free = `assigned_group_id IS NULL AND NOT EXISTS (SELECT 1 FROM task_assignees a WHERE a.task_id = tasks.id)`
	}
	var q string
	switch {
	case kind == "g":
		q = fmt.Sprintf(`UPDATE %s SET assigned_group_id = $1::uuid WHERE id = $2::uuid AND %s`, table, free)
	case ref == "task":
		q = `INSERT INTO task_assignees (task_id, user_id) SELECT id, $1::uuid FROM tasks WHERE id = $2::uuid AND ` + free
	default:
		q = fmt.Sprintf(`UPDATE %s SET assigned_to = $1::uuid, updated_at = NOW() WHERE id = $2::uuid AND %s`, table, free)
	}
	tag, err := h.db.Exec(ctx, q, who, id)
	if err != nil {
		componentLog("zuweisung").Error().Err(err).Str("art", ref).Str("id", id).Msg("zuweisen fehlgeschlagen")
		assignBoardResult(w, false, "Zuweisen hat nicht geklappt.")
		return
	}
	if tag.RowsAffected() == 0 {
		assignBoardResult(w, false, "Schon vergeben – jemand anderes hat den Vorgang inzwischen zugewiesen.")
		return
	}
	field, msg := "assigned_to", "Vom Broker zugewiesen"
	if kind == "g" {
		field, msg = "assigned_group_id", "Vom Broker der Gruppe zugewiesen"
	}
	h.addHistory(ctx, ref, id, "people", field, "", who, msg, u.ID)
	assignBoardResult(w, true, "Zugewiesen an "+label)
}

// assignBoardResult: Rueckmeldung, die die Zeile auf der Seite ersetzt.
func assignBoardResult(w http.ResponseWriter, ok bool, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		fmt.Fprintf(w, `<div class="ab-done"><i class="ti ti-circle-check"></i> %s</div>`, esc(msg))
		return
	}
	fmt.Fprintf(w, `<div class="ab-err"><i class="ti ti-alert-circle"></i> %s</div>`, esc(msg))
}
