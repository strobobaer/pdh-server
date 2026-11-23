package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

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
