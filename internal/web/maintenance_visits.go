package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/maintenance"
	"pdh/pkg/appsettings"
)

// Wartung: Jahresplan (geplante Termine) und Wartungsregeln.
//
//   POST /maintenance/plans/{id}/yearplan  Jahresplan eines Plans erzeugen/aktualisieren
//   POST /maintenance/yearplan             Jahresplan für alle aktiven Pläne (eigene Abteilungen)
//   POST /maintenance/rules                Vorlauf des Durchführungsauftrags (Arbeitstage)

const errMaintLater = "Wartung läuft – bitte später erneut versuchen"

// canMaintRules: Wartungsregeln gelten für alle Pläne – das Recht
// maintenance.admin muss deshalb überall gelten, nicht nur in einer Abteilung.
func (h *Handler) canMaintRules(r *http.Request) bool {
	u := getUser(r)
	if h.rbac == nil || u == nil {
		return false
	}
	all, _ := h.rbac.Scope(u.ID, string(u.Role), "maintenance.admin")
	return all
}

func maintPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func yearPlanFragment(w http.ResponseWriter, ok bool, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !ok {
		fmt.Fprintf(w, `<span style="color:var(--red);font-size:12px">%s</span>`, esc(text))
		return
	}
	fmt.Fprintf(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> %s</span>`, esc(text))
}

// MaintenanceYearPlanWeb: POST /maintenance/plans/{id}/yearplan.
func (h *Handler) MaintenanceYearPlanWeb(w http.ResponseWriter, r *http.Request) {
	if h.MaintLocked() {
		http.Error(w, errMaintLater, http.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "id")
	if !h.hasPermRec(r, "maintenance.edit", "maintenance_plan", id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	n, err := h.maint.GenerateYearPlan(r.Context(), id, getUser(r).ID)
	if err != nil {
		yearPlanFragment(w, false, "Fehler: "+err.Error())
		return
	}
	if n == 0 {
		yearPlanFragment(w, true, "Jahresplan ist aktuell")
		return
	}
	yearPlanFragment(w, true, maintPlural(n, "Termin", "Termine")+" geplant")
}

// yearPlanFor erzeugt den Jahresplan für die Pläne, an denen die Person
// maintenance.edit hat; die übrigen werden übersprungen und gezählt.
func (h *Handler) yearPlanFor(r *http.Request, planIDs []string) (plans, visits, skipped int) {
	uid := getUser(r).ID
	for _, id := range planIDs {
		if !h.hasPermRec(r, "maintenance.edit", "maintenance_plan", id) {
			skipped++
			continue
		}
		n, err := h.maint.GenerateYearPlan(r.Context(), id, uid)
		if err != nil {
			componentLog("wartung").Warn().Err(err).Str("plan", id).Msg("jahresplan nicht erzeugt")
			continue
		}
		plans++
		visits += n
	}
	return plans, visits, skipped
}

func yearPlanAllText(plans, visits, skipped int) string {
	s := "Jahresplan für " + maintPlural(plans, "Plan", "Pläne") + " erzeugt (" + maintPlural(visits, "Termin", "Termine") + ")"
	if skipped > 0 {
		s += " · " + maintPlural(skipped, "Plan", "Pläne") + " anderer Abteilungen übersprungen"
	}
	return s
}

// MaintenanceYearPlanAllWeb: POST /maintenance/yearplan – alle aktiven Pläne.
func (h *Handler) MaintenanceYearPlanAllWeb(w http.ResponseWriter, r *http.Request) {
	if h.MaintLocked() {
		http.Error(w, errMaintLater, http.StatusServiceUnavailable)
		return
	}
	list, err := h.maint.Repo().ListPlans(r.Context(), "", false)
	if err != nil {
		yearPlanFragment(w, false, "Fehler: "+err.Error())
		return
	}
	var ids []string
	for _, p := range list {
		if p.Active {
			ids = append(ids, p.ID)
		}
	}
	yearPlanFragment(w, true, yearPlanAllText(h.yearPlanFor(r, ids)))
}

// MaintenanceRulesWeb: POST /maintenance/rules – Vorlauf in Arbeitstagen.
func (h *Handler) MaintenanceRulesWeb(w http.ResponseWriter, r *http.Request) {
	if h.MaintLocked() {
		http.Error(w, errMaintLater, http.StatusServiceUnavailable)
		return
	}
	if !h.canMaintRules(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	back := "/maintenance?tab=rules&notice="
	n, err := strconv.Atoi(r.FormValue("lead_workdays"))
	if err != nil || n < 0 || n > 60 {
		http.Redirect(w, r, back+url.QueryEscape("Der Vorlauf muss zwischen 0 und 60 Arbeitstagen liegen"), http.StatusSeeOther)
		return
	}
	if err := h.setUpdateSetting(r.Context(), appsettings.KeyMaintenanceLeadWorkdays, strconv.Itoa(n)); err != nil {
		http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, back+url.QueryEscape("Vorlauf gespeichert"), http.StatusSeeOther)
}

// ── Geplante Termine im Kalender: verschieben, besetzen, zurück zum Plan ──

// visitGuard: Termin laden und prüfen – Wartungssperre, vorhanden, Recht am Plan
// (maintenance.edit in seiner Abteilung). Beteiligt sein reicht nicht.
func (h *Handler) visitGuard(w http.ResponseWriter, r *http.Request) (*maintenance.Visit, bool) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Termin ungültig"})
		return nil, false
	}
	if h.MaintLocked() {
		completionJSON(w, http.StatusServiceUnavailable, map[string]any{"error": errMaintLater})
		return nil, false
	}
	v, err := h.maint.GetVisit(r.Context(), id)
	if err != nil {
		if errors.Is(err, maintenance.ErrVisitNotFound) {
			completionJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		} else {
			completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		}
		return nil, false
	}
	if !h.hasPermRec(r, "maintenance.edit", "maintenance_plan", v.PlanID) {
		completionJSON(w, http.StatusForbidden, map[string]any{"error": "Keine Berechtigung, diesen Termin zu ändern."})
		return nil, false
	}
	return v, true
}

// visitDone: Antwort nach einer Änderung – Verlauf am Plan, Auftrag anlegen,
// falls der (neue) Termin schon im Vorlauf liegt.
func (h *Handler) visitDone(w http.ResponseWriter, r *http.Request, v *maintenance.Visit, err error, field, oldV, newV, what string) {
	if err != nil {
		if maintenance.IsInputError(err) {
			completionJSON(w, http.StatusBadRequest, map[string]any{"error": maintenance.InputMessage(err)})
			return
		}
		if errors.Is(err, maintenance.ErrVisitNotFound) {
			completionJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": friendlyDBError(err)})
		return
	}
	ctx := r.Context()
	uid := getUser(r).ID
	h.addHistory(ctx, "maintenance_plan", v.PlanID, "update", field, oldV, newV, "Wartungstermin im Kalender "+what, uid)
	if err := h.maint.EnsurePlanTask(ctx, v.PlanID, uid); err != nil {
		componentLog("wartung").Warn().Err(err).Str("plan", v.PlanID).Msg("auftrag nach änderung des termins")
	}
	completionJSON(w, http.StatusOK, map[string]any{"success": true})
}

func decodeVisitJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(into); err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültige Daten"})
		return false
	}
	return true
}

// MaintenanceVisitMoveWeb: POST /maintenance/visits/{id}/move {to_date}.
func (h *Handler) MaintenanceVisitMoveWeb(w http.ResponseWriter, r *http.Request) {
	v, ok := h.visitGuard(w, r)
	if !ok {
		return
	}
	var in struct {
		ToDate string `json:"to_date"`
	}
	if !decodeVisitJSON(w, r, &in) {
		return
	}
	to, err := time.ParseInLocation(dayISO, in.ToDate, time.Local)
	if err != nil {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Datum ungültig"})
		return
	}
	err = h.maint.MoveVisit(r.Context(), v.ID, to, getUser(r).ID)
	h.visitDone(w, r, v, err, "Termin", v.Day.Format("02.01.2006"), to.Format("02.01.2006"), "verschoben")
}

// MaintenanceVisitStaffWeb: POST /maintenance/visits/{id}/staff {assigned_to, group_id} (leer = niemand).
func (h *Handler) MaintenanceVisitStaffWeb(w http.ResponseWriter, r *http.Request) {
	v, ok := h.visitGuard(w, r)
	if !ok {
		return
	}
	var in struct {
		AssignedTo string `json:"assigned_to"`
		GroupID    string `json:"group_id"`
	}
	if !decodeVisitJSON(w, r, &in) {
		return
	}
	opt := func(s string) (*string, bool) {
		if s == "" {
			return nil, true
		}
		return &s, uuidInPathRe.MatchString(s)
	}
	person, ok1 := opt(in.AssignedTo)
	group, ok2 := opt(in.GroupID)
	if !ok1 || !ok2 {
		completionJSON(w, http.StatusBadRequest, map[string]any{"error": "Person oder Gruppe ungültig"})
		return
	}
	err := h.maint.StaffVisit(r.Context(), v.ID, person, group, getUser(r).ID)
	h.visitDone(w, r, v, err, "Besetzung", derefOr(v.AssignedTo, "")+" "+derefOr(v.GroupID, ""), in.AssignedTo+" "+in.GroupID, "besetzt")
}

// MaintenanceVisitResetWeb: POST /maintenance/visits/{id}/reset – Tag und Besetzung aus dem Plan.
func (h *Handler) MaintenanceVisitResetWeb(w http.ResponseWriter, r *http.Request) {
	v, ok := h.visitGuard(w, r)
	if !ok {
		return
	}
	err := h.maint.ResetVisit(r.Context(), v.ID, getUser(r).ID)
	h.visitDone(w, r, v, err, "Termin", v.Day.Format("02.01.2006"), v.CalcDay.Format("02.01.2006"), "zurückgesetzt")
}
