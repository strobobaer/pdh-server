package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/maintenance"
)

// Abschluss-Assistent: Jeder gruene Haken zum Fertigsetzen eines Tickets,
// einer Stoerung, Aufgabe oder Wartung fuehrt Schritt fuer Schritt durch
//   1. Material (vorgemerkte Ersatzteile oder "kein Material"),
//   2. Zeit (laufender Timer wird gestoppt, sonst Beginn + Dauer),
//   3. Wer war dabei? (Mitarbeitende der eingestellten Abteilungen),
//   4. Kommentar (was wurde gemacht, ggf. Ursache),
//   5. Fertig – oder "geht noch weiter" (bleibt in Bearbeitung).
// Mitarbeitende bekommen denselben Zeitraum als unbestaetigten (gelben)
// Zeiteintrag, den sie in der Zeiterfassung bestaetigen. Pruefungen laufen
// hier auf dem Server, die Oberflaeche ist in widgets/completion_wizard.gohtml.

const keyCompletionDepartments = "completion.departments"

const defaultCompletionDepartments = "Instandhaltung, Elektro, Mechanik"

type completionKind struct {
	Type, Label, Table, PartsAPI, EditPerm, DonePerm, RefType string
	RootCause                                                 bool
}

var completionKinds = map[string]completionKind{
	"ticket":      {Type: "ticket", Label: "Ticket", Table: "tickets", PartsAPI: "/api/v1/tickets/", EditPerm: "tickets.edit", DonePerm: "tickets.resolve", RefType: "ticket", RootCause: true},
	"fault":       {Type: "fault", Label: "Störung", Table: "faults", PartsAPI: "/api/v1/faults/", EditPerm: "faults.edit", DonePerm: "faults.resolve", RefType: "fault", RootCause: true},
	"task":        {Type: "task", Label: "Aufgabe", Table: "tasks", PartsAPI: "/api/v1/tasks/", EditPerm: "tasks.edit", DonePerm: "tasks.edit", RefType: "task", RootCause: true},
	"maintenance": {Type: "maintenance", Label: "Wartung", Table: "maintenance_tasks", PartsAPI: "/api/v1/maintenance/tasks/", EditPerm: "maintenance.edit", DonePerm: "maintenance.complete", RefType: "maintenance"},
}

// Status, in denen ein Vorgang als abgeschlossen gilt.
var closedStatus = map[string]bool{"resolved": true, "closed": true, "done": true, "skipped": true}

// completionColleague: Auswahl "Wer war dabei?".
type completionColleague struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Suggested  bool   `json:"suggested"` // gehoert zu einer eingestellten Abteilung
}

type completionInfo struct {
	Type, Label, Title, Status string
	PartsURL                   string                `json:"parts_url"`
	RootCause                  bool                  `json:"root_cause"`
	CanFinish                  bool                  `json:"can_finish"`
	BookedMin                  int                   `json:"booked_min"` // schon erfasste eigene Zeit
	RunningSince               string                `json:"running_since,omitempty"`
	RunningMin                 int                   `json:"running_min"`
	SuggestStart               string                `json:"suggest_start"` // YYYY-MM-DDTHH:MM
	Departments                []string              `json:"departments"`
	Colleagues                 []completionColleague `json:"colleagues"`
}

func (h *Handler) completionDepartments(ctx context.Context) []string {
	raw := h.appSetting(ctx, keyCompletionDepartments, defaultCompletionDepartments)
	var out []string
	for _, d := range strings.Split(raw, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// deptMatches: Abteilung passt, wenn ein eingestellter Begriff darin vorkommt
// ("Elektro" passt auf "Elektrowerkstatt", Gross/klein egal).
func deptMatches(dept string, wanted []string) bool {
	d := strings.ToLower(dept)
	for _, w := range wanted {
		if w != "" && strings.Contains(d, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

func (h *Handler) completionRecord(ctx context.Context, k completionKind, id string) (title, status string, err error) {
	err = h.db.QueryRow(ctx, fmt.Sprintf(`SELECT title::text, status::text FROM %s WHERE id = $1::uuid`, k.Table), id).Scan(&title, &status)
	if err != nil {
		err = uiError("Vorgang nicht gefunden")
	}
	return
}

func (h *Handler) completionKind(w http.ResponseWriter, r *http.Request) (completionKind, string, bool) {
	k, ok := completionKinds[chi.URLParam(r, "type")]
	id := chi.URLParam(r, "id")
	lang := ctxLang(r.Context())
	if !ok || !uuidInPathRe.MatchString(id) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": tr(lang, "unbekannter Vorgang")})
		return k, "", false
	}
	if !h.canFn(r)(k.EditPerm) {
		completionJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": tr(lang, "Keine Berechtigung, diesen Vorgang zu bearbeiten.")})
		return k, "", false
	}
	return k, id, true
}

func completionJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// CompletionInfoWeb: GET /complete/{type}/{id} – Daten fuer den Assistenten.
func (h *Handler) CompletionInfoWeb(w http.ResponseWriter, r *http.Request) {
	k, id, ok := h.completionKind(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	u := getUser(r)
	lang := ctxLang(ctx)
	title, status, err := h.completionRecord(ctx, k, id)
	if err != nil {
		completionJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": tr(lang, err.Error())})
		return
	}
	info := completionInfo{Type: k.Type, Label: tr(lang, k.Label), Title: title, Status: status, PartsURL: k.PartsAPI + id + "/pending-parts",
		RootCause: k.RootCause, CanFinish: h.canFn(r)(k.DonePerm), Departments: h.completionDepartments(ctx)}
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(SUM(duration_min), 0)::int FROM time_entries
		WHERE user_id = $1::uuid AND ref_type::text = $2 AND ref_id = $3::uuid AND ended_at IS NOT NULL AND NOT pending`, u.ID, k.RefType, id).Scan(&info.BookedMin)
	var running *time.Time
	_ = h.db.QueryRow(ctx, `SELECT started_at FROM time_entries
		WHERE user_id = $1::uuid AND ref_type::text = $2 AND ref_id = $3::uuid AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`, u.ID, k.RefType, id).Scan(&running)
	start := time.Now().Add(-30 * time.Minute)
	if running != nil {
		info.RunningSince = running.Local().Format("02.01. 15:04")
		info.RunningMin = int(time.Since(*running).Minutes())
		start = *running
	}
	info.SuggestStart = start.Local().Format("2006-01-02T15:04")
	info.Colleagues = h.completionColleagues(ctx, u.ID, info.Departments)
	completionJSON(w, http.StatusOK, map[string]any{"success": true, "data": info})
}

func (h *Handler) completionColleagues(ctx context.Context, self string, depts []string) []completionColleague {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, TRIM(first_name || ' ' || last_name), COALESCE(department, '')
		FROM users WHERE active AND NOT is_bot AND NOT is_system_user AND id <> $1::uuid
		ORDER BY department, last_name, first_name`, self)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []completionColleague
	for rows.Next() {
		var c completionColleague
		if rows.Scan(&c.ID, &c.Name, &c.Department) == nil {
			c.Suggested = deptMatches(c.Department, depts)
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Suggested && !out[j].Suggested })
	return out
}

// completionRequest: Eingaben des Assistenten.
type completionRequest struct {
	NoParts    bool     `json:"no_parts"`
	Start      string   `json:"start"`   // YYYY-MM-DDTHH:MM (Ortszeit)
	Minutes    int      `json:"minutes"` // neue Arbeitszeit (0 = keine)
	Colleagues []string `json:"colleagues"`
	Comment    string   `json:"comment"`
	RootCause  string   `json:"root_cause"`
	Finish     bool     `json:"finish"` // false = "geht noch weiter"
}

// validateCompletion prueft die Eingaben ohne Datenbank.
func validateCompletion(in *completionRequest, parts int, booked int, running bool) error {
	in.Comment = strings.TrimSpace(in.Comment)
	if len([]rune(in.Comment)) < 3 {
		return uiError("Bitte kurz beschreiben, was gemacht wurde (Kommentar).")
	}
	if in.Minutes < 0 || in.Minutes > 24*60 {
		return uiError("Die Arbeitszeit muss zwischen 1 Minute und 24 Stunden liegen.")
	}
	if in.Finish && parts == 0 && !in.NoParts {
		return uiError("Bitte das verwendete Material erfassen oder „Kein Material verwendet“ bestätigen.")
	}
	session := running || in.Minutes > 0
	if !session && booked == 0 {
		return uiError("Bitte die Arbeitszeit erfassen.")
	}
	if len(in.Colleagues) > 0 && !session {
		return uiError("Für die Mitarbeitenden bitte die gemeinsame Arbeitszeit angeben.")
	}
	if len(in.Colleagues) > 50 {
		return uiError("Zu viele Mitarbeitende ausgewählt.")
	}
	return nil
}

func (h *Handler) completionPendingParts(ctx context.Context, k completionKind, id string) (int, error) {
	switch k.Type {
	case "ticket":
		p, err := h.tickets.GetPendingParts(ctx, id)
		return len(p), err
	case "fault":
		p, err := h.faults.GetPendingParts(ctx, id)
		return len(p), err
	case "task":
		p, err := h.tasks.GetPendingParts(ctx, id)
		return len(p), err
	case "maintenance":
		p, err := h.maint.GetPendingParts(ctx, id)
		return len(p), err
	}
	return 0, nil
}

func (h *Handler) completionAddAction(ctx context.Context, k completionKind, id, text, uid string) error {
	var err error
	switch k.Type {
	case "ticket":
		_, err = h.tickets.AddAction(ctx, id, text, uid)
	case "fault":
		_, err = h.faults.AddAction(ctx, id, text, uid)
	case "task":
		_, err = h.tasks.AddAction(ctx, id, text, uid)
	case "maintenance":
		_, err = h.maint.AddAction(ctx, id, text, uid)
	}
	return err
}

func (h *Handler) completionFinish(ctx context.Context, k completionKind, id string, in *completionRequest, uid string, sessionMin int) error {
	switch k.Type {
	case "ticket":
		return h.tickets.Resolve(ctx, id, in.Comment, in.RootCause, uid, in.NoParts)
	case "fault":
		return h.faults.Resolve(ctx, id, in.Comment, in.RootCause, uid, in.NoParts)
	case "task":
		return h.tasks.Resolve(ctx, id, in.Comment, in.RootCause, uid, in.NoParts)
	case "maintenance":
		return h.maint.CompleteTaskValidated(ctx, id, uid, &maintenance.CompleteTaskInput{Notes: in.Comment, DurationMin: sessionMin}, in.NoParts)
	}
	return nil
}

// CompletionWeb: POST /complete/{type}/{id} – Assistent ausfuehren.
func (h *Handler) CompletionWeb(w http.ResponseWriter, r *http.Request) {
	k, id, ok := h.completionKind(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	u := getUser(r)
	lang := ctxLang(ctx)
	fail := func(status int, err error) {
		completionJSON(w, status, map[string]any{"success": false, "error": tr(lang, err.Error())})
	}
	var in completionRequest
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err := json.Unmarshal(body, &in); err != nil {
		fail(http.StatusBadRequest, uiError("ungültige Daten"))
		return
	}
	if in.Finish && !h.canFn(r)(k.DonePerm) {
		fail(http.StatusForbidden, uiError("Keine Berechtigung, diesen Vorgang abzuschließen."))
		return
	}
	title, status, err := h.completionRecord(ctx, k, id)
	if err != nil {
		fail(http.StatusNotFound, err)
		return
	}
	if closedStatus[status] {
		fail(http.StatusConflict, uiError("Der Vorgang ist bereits abgeschlossen."))
		return
	}
	parts, err := h.completionPendingParts(ctx, k, id)
	if err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}
	var booked int
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(SUM(duration_min), 0)::int FROM time_entries
		WHERE user_id = $1::uuid AND ref_type::text = $2 AND ref_id = $3::uuid AND ended_at IS NOT NULL AND NOT pending`, u.ID, k.RefType, id).Scan(&booked)
	var runID string
	var runStart time.Time
	_ = h.db.QueryRow(ctx, `SELECT id::text, started_at FROM time_entries
		WHERE user_id = $1::uuid AND ref_type::text = $2 AND ref_id = $3::uuid AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`, u.ID, k.RefType, id).Scan(&runID, &runStart)
	if err := validateCompletion(&in, parts, booked, runID != ""); err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	colleagues, names, err := h.completionValidColleagues(ctx, in.Colleagues, u.ID)
	if err != nil {
		fail(http.StatusBadRequest, err)
		return
	}

	// ── Zeit: laufenden Timer stoppen oder neuen Eintrag anlegen ──
	var start, end time.Time
	desc := k.Label + ": " + title
	switch {
	case runID != "":
		start, end = runStart, time.Now()
		if _, err := h.db.Exec(ctx, `UPDATE time_entries SET ended_at = $1, duration_min = GREATEST(1, EXTRACT(EPOCH FROM ($1 - started_at))::int / 60)
			WHERE id = $2::uuid`, end, runID); err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
	case in.Minutes > 0:
		start = time.Now().Add(-time.Duration(in.Minutes) * time.Minute)
		if t, e := time.ParseInLocation("2006-01-02T15:04", in.Start, time.Local); e == nil {
			start = t
		}
		end = start.Add(time.Duration(in.Minutes) * time.Minute)
		if err := h.insertTimeEntry(ctx, u.ID, k.RefType, id, desc, start, end, ""); err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
	}
	sessionMin := 0
	if !start.IsZero() {
		sessionMin = int(end.Sub(start).Minutes())
	}

	// ── Mitarbeitende: gleicher Zeitraum, unbestaetigt ──
	if len(colleagues) > 0 {
		me := strings.TrimSpace(u.FirstName + " " + u.LastName)
		for _, c := range colleagues {
			if err := h.insertTimeEntry(ctx, c, k.RefType, id, desc+" (eingetragen von "+me+")", start, end, u.ID); err != nil {
				fail(http.StatusInternalServerError, err)
				return
			}
		}
		h.systemNotify(ctx, colleagues, fmt.Sprintf("⏱ %s hat dich bei %s „%s“ als beteiligt eingetragen: %s–%s (%s). Bitte in der Zeiterfassung bestätigen: /time",
			me, k.Label, title, start.Local().Format("02.01. 15:04"), end.Local().Format("15:04"), durationText(sessionMin)))
	}

	// ── Kommentar als Massnahme ──
	action := in.Comment
	if len(names) > 0 {
		action += "\n\nBeteiligt: " + strings.Join(names, ", ")
	}
	if err := h.completionAddAction(ctx, k, id, action, u.ID); err != nil {
		fail(http.StatusInternalServerError, err)
		return
	}

	if !in.Finish {
		_, _ = h.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET status = 'in_progress', updated_at = NOW()
			WHERE id = $1::uuid AND status::text IN ('open','detected','analyzing','pending')`, k.Table), id)
		h.addHistory(ctx, k.Type, id, "work", "", "", "", "Arbeitsschritt erfasst (geht noch weiter)", u.ID)
		completionJSON(w, http.StatusOK, map[string]any{"success": true, "closed": false, "message": tr(lang, "Arbeitsschritt gespeichert – der Vorgang bleibt in Bearbeitung.")})
		return
	}
	if err := h.completionFinish(ctx, k, id, &in, u.ID, sessionMin); err != nil {
		// Zeit, Material-Vormerkung und Kommentar sind gespeichert – nur der Abschluss fehlt
		completionJSON(w, http.StatusOK, map[string]any{"success": true, "closed": false, "warning": tr(lang, "Gespeichert, aber noch nicht abgeschlossen: %s", tr(lang, err.Error()))})
		return
	}
	if boardCompleteFrom(ctx) {
		h.logBoardCompletion(context.WithoutCancel(ctx), k.Type, id, in.Comment, u.ID)
	}
	completionJSON(w, http.StatusOK, map[string]any{"success": true, "closed": true, "message": tr(lang, "%s abgeschlossen.", tr(lang, k.Label))})
}

// completionValidColleagues: nur aktive, echte Benutzer (nicht man selbst).
func (h *Handler) completionValidColleagues(ctx context.Context, ids []string, self string) ([]string, []string, error) {
	var valid, names []string
	seen := map[string]bool{}
	for _, id := range ids {
		if id == self || seen[id] || !uuidInPathRe.MatchString(id) {
			continue
		}
		seen[id] = true
		var name string
		if err := h.db.QueryRow(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid AND active AND NOT is_bot`, id).Scan(&name); err != nil {
			return nil, nil, uiError("Ein ausgewählter Mitarbeiter wurde nicht gefunden.")
		}
		valid = append(valid, id)
		names = append(names, name)
	}
	return valid, names, nil
}

func (h *Handler) insertTimeEntry(ctx context.Context, userID, refType, refID, desc string, start, end time.Time, pendingFrom string) error {
	_, err := h.db.Exec(ctx, `
		INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min, pending, pending_from)
		VALUES (gen_random_uuid(), $1::uuid, $2::time_ref_type, $3::uuid, $4, $5, $6, GREATEST(1, $7::int), $8 <> '', NULLIF($8, '')::uuid)`,
		userID, refType, refID, desc, start, end, int(end.Sub(start).Minutes()), pendingFrom)
	return err
}

// TimeConfirmWeb: POST /time/{id}/confirm – eigenen unbestaetigten Eintrag bestaetigen.
func (h *Handler) TimeConfirmWeb(w http.ResponseWriter, r *http.Request) {
	tag, err := h.db.Exec(r.Context(), `UPDATE time_entries SET pending = false WHERE id = $1::uuid AND user_id = $2::uuid AND pending`,
		chi.URLParam(r, "id"), getUser(r).ID)
	if err != nil || tag.RowsAffected() == 0 {
		completionJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Eintrag nicht gefunden"})
		return
	}
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}

// CompletionSettingsWeb: POST /core/settings/completion (Abteilungen)
func (h *Handler) CompletionSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	var parts []string
	for _, d := range strings.Split(r.FormValue("departments"), ",") {
		if d = strings.TrimSpace(d); d != "" && len([]rune(d)) <= 60 {
			parts = append(parts, d)
		}
	}
	notice := "Abteilungen für den Abschluss-Assistenten gespeichert."
	if err := h.setAppSetting(r.Context(), keyCompletionDepartments, strings.Join(parts, ", ")); err != nil {
		notice = "Fehler: " + err.Error()
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}
