package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/maintenance"
)

// Wartungsplaene anlegen, bearbeiten, vormerken und wiederherstellen (Web).
//
// Diese Routen lagen frueher als "Web-save fallbacks" direkt in
// cmd/server/main.go – vor der Web-Oberflaeche und damit OHNE Anmeldung –
// und doppelten die (nie erreichten) Varianten hier. Jetzt gibt es je
// Aktion genau eine Umsetzung hinter authMiddleware; angelegt wird im
// Namen der angemeldeten Person.

// planForm liest das Formular (multipart/form-data von fetch+FormData oder
// urlencoded von htmx).
func planForm(r *http.Request) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.ParseMultipartForm(32 << 20)
	}
	return r.ParseForm()
}

// scheduleModeForm: Berechnungsart des naechsten Termins (Standard: ab Durchfuehrung).
func scheduleModeForm(r *http.Request) string {
	if r.FormValue("schedule_mode") == maintenance.ScheduleFixed {
		return maintenance.ScheduleFixed
	}
	return maintenance.ScheduleFromCompletion
}

// scheduleModeFormOrKeep: beim Bearbeiten ohne Feld bleibt die bisherige Art.
func scheduleModeFormOrKeep(r *http.Request) string {
	if r.Form["schedule_mode"] == nil {
		return ""
	}
	return scheduleModeForm(r)
}

func planIntervalDays(interval string) int {
	switch interval {
	case "daily":
		return 1
	case "weekly":
		return 7
	case "quarterly":
		return 90
	case "yearly":
		return 365
	}
	return 30
}

// MaintenancePlanCreateWeb: POST /maintenance/plans
func (h *Handler) MaintenancePlanCreateWeb(w http.ResponseWriter, r *http.Request) {
	if err := planForm(r); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
	if name == "" || infraID == "" {
		http.Error(w, "Name und Infrastruktur sind Pflicht", http.StatusBadRequest)
		return
	}
	if s := h.requestScope(r); s != nil && !h.infraInScope(r, s, infraID) {
		http.Error(w, "Kein Zugriff: Diese Anlage gehört zu einer anderen Abteilung.", http.StatusForbidden)
		return
	}
	interval := r.FormValue("interval")
	firstDue := strings.TrimSpace(r.FormValue("first_due_at"))
	if firstDue == "" {
		firstDue = time.Now().Format("2006-01-02")
	}
	u := getUser(r)
	var planID string
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO maintenance_plans
		  (id, name, description, type, infrastructure_id, interval_type, interval_days,
		   estimated_min, priority, assigned_to, active, next_due_at, created_by, cost_center_id, responsible_to, assigned_group_id, schedule_mode)
		VALUES (gen_random_uuid(), $1, $2, $3::maintenance_type, $4::uuid, $5::maintenance_interval, $6,
			0, $7::maintenance_priority, NULLIF($8,'')::uuid, true, $9::date, $10::uuid, NULLIF($11,'')::uuid, NULLIF($12,'')::uuid,
			NULLIF(NULLIF($13,''),$14)::uuid, $15)
		RETURNING id::text`,
		name, strings.TrimSpace(r.FormValue("description")), r.FormValue("type"), infraID, interval, planIntervalDays(interval),
		r.FormValue("priority"), strings.TrimSpace(r.FormValue("assigned_to")), firstDue, u.ID,
		strings.TrimSpace(r.FormValue("cost_center_id")), strings.TrimSpace(r.FormValue("responsible_to")),
		strings.TrimSpace(r.FormValue("assigned_group_id")), groupKeep, scheduleModeForm(r),
	).Scan(&planID)
	if err != nil {
		componentLog("wartung").Error().Err(err).Msg("wartungsplan anlegen fehlgeschlagen")
		http.Error(w, "Wartungsplan konnte nicht angelegt werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	componentLog("wartung").Info().Str("plan_id", planID).Str("user", u.ID).Msg("wartungsplan angelegt")
	// erster Auftrag sofort – nicht erst nach „Aufträge generieren“
	if err := h.maint.EnsurePlanTask(r.Context(), planID, u.ID); err != nil {
		componentLog("wartung").Warn().Err(err).Str("plan_id", planID).Msg("erster auftrag nicht angelegt")
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/maintenance")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
}

// infraInScope: Anlage liegt in der Abteilungs-Sicht der Person.
func (h *Handler) infraInScope(r *http.Request, s *deptScope, infraID string) bool {
	var ok bool
	err := h.db.QueryRow(r.Context(), `SELECT NOT EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = $1::uuid)
		OR EXISTS (SELECT 1 FROM infrastructure_department x WHERE x.infrastructure_id = $1::uuid AND x.department_id::text = ANY($2))`,
		infraID, s.Departments).Scan(&ok)
	return err == nil && ok
}

// MaintenancePlanEditWeb: POST /maintenance/plans/{id}/edit-web
func (h *Handler) MaintenancePlanEditWeb(w http.ResponseWriter, r *http.Request) {
	if err := planForm(r); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	planID := chi.URLParam(r, "id")
	name := strings.TrimSpace(r.FormValue("name"))
	infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
	if name == "" || infraID == "" {
		http.Error(w, "Name und Infrastruktur sind Pflicht", http.StatusBadRequest)
		return
	}
	if s := h.requestScope(r); s != nil && !h.infraInScope(r, s, infraID) {
		http.Error(w, "Kein Zugriff: Diese Anlage gehört zu einer anderen Abteilung.", http.StatusForbidden)
		return
	}
	intervalDays, _ := strconv.Atoi(r.FormValue("interval_days"))
	estimatedMin, _ := strconv.Atoi(r.FormValue("estimated_min"))
	defaultDurationMin, _ := strconv.Atoi(r.FormValue("default_duration_min"))
	if defaultDurationMin > 0 {
		estimatedMin = defaultDurationMin
	}
	templateIDs := r.Form["checklist_template_ids"]
	if len(templateIDs) == 0 {
		templateIDs = r.Form["checklist_template_id"]
	}
	_, err := h.db.Exec(r.Context(), `
		UPDATE maintenance_plans
		SET name=$1,
		    description=COALESCE(NULLIF($2,''), description),
		    type=$3::maintenance_type,
		    infrastructure_id=$4::uuid,
		    interval_type=$5::maintenance_interval,
		    interval_days=CASE WHEN $6 > 0 THEN $6 ELSE interval_days END,
		    estimated_min=CASE WHEN $7 > 0 THEN $7 ELSE estimated_min END,
		    default_duration_min=$8,
		    priority=$9::maintenance_priority,
		    next_due_at=CASE WHEN NULLIF($10,'') IS NULL THEN next_due_at ELSE $10::date END,
		    cost_center_id=NULLIF($11,'')::uuid,
		    assigned_to=CASE WHEN $13 THEN NULLIF($14,'')::uuid ELSE assigned_to END,
		    responsible_to=CASE WHEN $15 THEN NULLIF($16,'')::uuid ELSE responsible_to END,
		    assigned_group_id=CASE WHEN $17 THEN NULLIF($18,'')::uuid ELSE assigned_group_id END,
		    schedule_mode=CASE WHEN $19 = '' THEN schedule_mode ELSE $19 END,
		    active=true
		WHERE id=$12`,
		name, strings.TrimSpace(r.FormValue("description")), r.FormValue("type"), infraID, r.FormValue("interval"),
		intervalDays, estimatedMin, defaultDurationMin, r.FormValue("priority"),
		strings.TrimSpace(r.FormValue("next_due_at")), strings.TrimSpace(r.FormValue("cost_center_id")), planID,
		r.Form["assigned_to"] != nil, strings.TrimSpace(r.FormValue("assigned_to")),
		r.Form["responsible_to"] != nil, strings.TrimSpace(r.FormValue("responsible_to")),
		r.Form["assigned_group_id"] != nil && r.FormValue("assigned_group_id") != groupKeep, strings.TrimSpace(r.FormValue("assigned_group_id")),
		scheduleModeFormOrKeep(r),
	)
	if err != nil {
		componentLog("wartung").Error().Err(err).Str("plan_id", planID).Msg("wartungsplan speichern fehlgeschlagen")
		http.Error(w, "Wartungsplan konnte nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := maintenance.NewRepository(h.db).AssignChecklistTemplatesToPlan(r.Context(), planID, templateIDs, defaultDurationMin); err != nil {
		http.Error(w, "Checklisten konnten nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// neuer Termin am Plan: der noch nicht begonnene Auftrag zieht mit
	if strings.TrimSpace(r.FormValue("next_due_at")) != "" {
		_, _ = h.db.Exec(r.Context(), `UPDATE maintenance_tasks mt SET due_date = mp.next_due_at
			FROM maintenance_plans mp WHERE mp.id = $1::uuid AND mt.plan_id = mp.id AND mt.status = 'open'`, planID)
	}
	if err := h.maint.EnsurePlanTask(r.Context(), planID, getUser(r).ID); err != nil {
		componentLog("wartung").Warn().Err(err).Str("plan_id", planID).Msg("auftrag zum plan nicht angelegt")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Gespeichert</span>`))
}

// MaintenancePlansRestoreAllWeb: POST /maintenance/plans/restore-all-web
func (h *Handler) MaintenancePlansRestoreAllWeb(w http.ResponseWriter, r *http.Request) {
	cmd, err := h.db.Exec(r.Context(), `UPDATE maintenance_plans SET active=true WHERE active=false`)
	if err != nil {
		http.Error(w, "Wartungspläne konnten nicht wiederhergestellt werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	componentLog("wartung").Info().Int64("anzahl", cmd.RowsAffected()).Str("user", getUser(r).ID).Msg("wartungsplaene wiederhergestellt")
	_, _ = h.maint.GenerateTasks(r.Context(), getUser(r).ID) // wiederhergestellte Plaene bekommen ihren Auftrag
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/maintenance")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
}

// MaintenancePlanDeleteWeb: DELETE /maintenance/plans/{id}/delete-web (deaktivieren/vormerken)
func (h *Handler) MaintenancePlanDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if _, err := h.db.Exec(r.Context(), `UPDATE maintenance_plans SET active=false WHERE id=$1`, chi.URLParam(r, "id")); err != nil {
		http.Error(w, "Wartungsplan konnte nicht vorgemerkt werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
