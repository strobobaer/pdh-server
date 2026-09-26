package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	coreusers "pdh/internal/core/users"
	"pdh/internal/modules/maintenance"
)

type GlobalBoardActionInput struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Action        string `json:"action"`
	Comment       string `json:"comment"`
	RFIDUID       string `json:"rfid_uid"`
	AssignedTo    string `json:"assigned_to"`
	FollowUpDate  string `json:"follow_up_date"`
	NoPartsNeeded bool  `json:"no_parts_needed"`
}

func (h *Handler) GlobalDashboardAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in GlobalBoardActionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Aktion")
		return
	}
	in.Comment = strings.TrimSpace(in.Comment)
	in.RFIDUID = strings.TrimSpace(in.RFIDUID)
	in.ID = strings.TrimSpace(in.ID)
	if len([]rune(in.Comment)) < 8 || len([]rune(in.Comment)) > 1000 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Der Kommentar muss 8 bis 1000 Zeichen lang sein")
		return
	}
	if in.RFIDUID == "" {
		writeGlobalBoardError(w, http.StatusUnauthorized, "Bitte RFID-Karte zur Bestätigung scannen")
		return
	}
	if !globalBoardTypeAllowed(in.Type) || !globalBoardActionAllowed(in.Action) || in.ID == "" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Unbekannter Vorgang oder Aktion")
		return
	}

	_, actor, err := h.users.LoginByRFID(r.Context(), in.RFIDUID)
	if err != nil || actor == nil || !isGlobalBoardDepartment(actor.Department) || actor.Role == coreusers.RoleViewer {
		writeGlobalBoardError(w, http.StatusUnauthorized, "RFID-Karte gehört keinem aktiven Mitarbeiter aus Instandhaltung oder IT")
		return
	}
	if !h.globalBoardRecordActive(r, in.Type, in.ID) {
		writeGlobalBoardError(w, http.StatusConflict, "Der Vorgang ist nicht mehr offen")
		return
	}

	var assignedTo *string
	if in.Action == "accept" {
		department, role, err := h.globalBoardWorkerDepartment(r, in.AssignedTo)
		if err != nil || !isGlobalBoardDepartment(department) || role == string(coreusers.RoleViewer) {
			writeGlobalBoardError(w, http.StatusBadRequest, "Bitte einen aktiven Mitarbeiter aus Instandhaltung oder IT auswählen")
			return
		}
		assignedTo = &in.AssignedTo
	}

	var followUpDate *time.Time
	if in.Action == "wait" {
		date, err := time.Parse("2006-01-02", in.FollowUpDate)
		if err != nil || date.Before(time.Now().Truncate(24*time.Hour)) {
			writeGlobalBoardError(w, http.StatusBadRequest, "Bitte einen heutigen oder zukünftigen Wiedervorlagetermin angeben")
			return
		}
		followUpDate = &date
	}

	if err := h.applyGlobalBoardAction(r, in, actor.ID, followUpDate); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := h.db.Exec(r.Context(), `
		INSERT INTO global_dashboard_actions
			(ref_type, ref_id, action, comment, action_user_id, assigned_to, follow_up_date)
		VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6::uuid, $7::date)`,
		in.Type, in.ID, in.Action, in.Comment, actor.ID, assignedTo, followUpDate); err != nil {
		writeGlobalBoardError(w, http.StatusInternalServerError, "Aktion wurde ausgeführt, konnte aber nicht protokolliert werden")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) globalBoardRecordActive(r *http.Request, refType, id string) bool {
	var query string
	switch refType {
	case "fault":
		query = `SELECT EXISTS(SELECT 1 FROM faults WHERE id=$1::uuid AND status IN ('detected','analyzing','in_progress'))`
	case "ticket":
		query = `SELECT EXISTS(SELECT 1 FROM tickets WHERE id=$1::uuid AND status IN ('open','in_progress','pending'))`
	case "maintenance":
		query = `SELECT EXISTS(SELECT 1 FROM maintenance_tasks WHERE id=$1::uuid AND status IN ('open','in_progress'))`
	case "task":
		query = `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1::uuid AND status IN ('open','in_progress'))`
	default:
		return false
	}
	var active bool
	return h.db.QueryRow(r.Context(), query, id).Scan(&active) == nil && active
}

func (h *Handler) globalBoardWorkerDepartment(r *http.Request, id string) (string, string, error) {
	var department, role string
	err := h.db.QueryRow(r.Context(), `
		SELECT department, role FROM users
		WHERE id=$1::uuid AND active=true AND is_system_user=false`, id).Scan(&department, &role)
	return department, role, err
}

func (h *Handler) applyGlobalBoardAction(r *http.Request, in GlobalBoardActionInput, actorID string, followUpDate *time.Time) error {
	ctx := r.Context()
	switch in.Action {
	case "accept":
		var query string
		switch in.Type {
		case "fault":
			query = `UPDATE faults SET assigned_to=$1::uuid,status='in_progress',updated_at=NOW() WHERE id=$2::uuid AND status IN ('detected','analyzing','in_progress')`
		case "ticket":
			query = `UPDATE tickets SET assigned_to=$1::uuid,status='in_progress',updated_at=NOW() WHERE id=$2::uuid AND status IN ('open','in_progress','pending')`
		case "maintenance":
			query = `UPDATE maintenance_tasks SET assigned_to=$1::uuid,status='in_progress' WHERE id=$2::uuid AND status IN ('open','in_progress')`
		case "task":
			query = `UPDATE tasks SET assigned_to=$1::uuid,status='in_progress',updated_at=NOW() WHERE id=$2::uuid AND status IN ('open','in_progress')`
		}
		result, err := h.db.Exec(ctx, query, in.AssignedTo, in.ID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("Der Vorgang wurde zwischenzeitlich geändert")
		}
	case "wait":
		switch in.Type {
		case "fault":
			return h.faults.UpdateDueDate(ctx, in.ID, followUpDate)
		case "ticket":
			return h.tickets.UpdateDueDate(ctx, in.ID, followUpDate)
		case "maintenance":
			return h.maint.UpdateDueDate(ctx, in.ID, *followUpDate)
		case "task":
			_, err := h.db.Exec(ctx, `UPDATE tasks SET due_date=$1::date,updated_at=NOW() WHERE id=$2::uuid AND status IN ('open','in_progress')`, followUpDate.Format("2006-01-02"), in.ID)
			return err
		}
	case "done":
		switch in.Type {
		case "fault":
			parts, err := h.faults.GetPendingParts(ctx, in.ID)
			if err != nil {
				return err
			}
			if err := validateGlobalParts(in.NoPartsNeeded, len(parts)); err != nil {
				return err
			}
			if _, err := h.faults.AddAction(ctx, in.ID, in.Comment, actorID); err != nil {
				return err
			}
			return h.faults.Resolve(ctx, in.ID, in.Comment, in.Comment, actorID, in.NoPartsNeeded)
		case "ticket":
			parts, err := h.tickets.GetPendingParts(ctx, in.ID)
			if err != nil {
				return err
			}
			if err := validateGlobalParts(in.NoPartsNeeded, len(parts)); err != nil {
				return err
			}
			if _, err := h.tickets.AddAction(ctx, in.ID, in.Comment, actorID); err != nil {
				return err
			}
			return h.tickets.Resolve(ctx, in.ID, in.Comment, in.Comment, actorID, in.NoPartsNeeded)
		case "maintenance":
			parts, err := h.maint.GetPendingParts(ctx, in.ID)
			if err != nil {
				return err
			}
			if err := validateGlobalParts(in.NoPartsNeeded, len(parts)); err != nil {
				return err
			}
			if _, err := h.maint.AddAction(ctx, in.ID, in.Comment, actorID); err != nil {
				return err
			}
			return h.maint.CompleteTaskValidated(ctx, in.ID, actorID, &maintenance.CompleteTaskInput{Notes: in.Comment}, in.NoPartsNeeded)
		case "task":
			parts, err := h.tasks.GetPendingParts(ctx, in.ID)
			if err != nil {
				return err
			}
			if err := validateGlobalParts(in.NoPartsNeeded, len(parts)); err != nil {
				return err
			}
			if _, err := h.tasks.AddAction(ctx, in.ID, in.Comment, actorID); err != nil {
				return err
			}
			return h.tasks.Resolve(ctx, in.ID, in.Comment, in.Comment, actorID, in.NoPartsNeeded)
		}
	case "discard":
		switch in.Type {
		case "fault":
			return h.faults.QuickResolve(ctx, in.ID, "Verworfen: "+in.Comment, "", actorID)
		case "ticket":
			return h.tickets.QuickResolve(ctx, in.ID, "Verworfen: "+in.Comment, "", actorID)
		case "maintenance":
			result, err := h.db.Exec(ctx, `UPDATE maintenance_tasks SET status='skipped' WHERE id=$1::uuid AND status IN ('open','in_progress')`, in.ID)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return fmt.Errorf("Der Vorgang wurde zwischenzeitlich geändert")
			}
		case "task":
			return h.tasks.Discard(ctx, in.ID, actorID)
		}
	}
	return nil
}

func globalBoardTypeAllowed(refType string) bool {
	switch refType {
	case "fault", "ticket", "maintenance", "task":
		return true
	default:
		return false
	}
}

func globalBoardActionAllowed(action string) bool {
	switch action {
	case "accept", "done", "discard", "wait":
		return true
	default:
		return false
	}
}

func validateGlobalParts(noPartsNeeded bool, pendingParts int) error {
	if pendingParts == 0 && !noPartsNeeded {
		return fmt.Errorf("Bitte ‚Keine Ersatzteile benötigt‘ bestätigen oder vorgemerkte Teile erfassen")
	}
	if pendingParts > 0 && noPartsNeeded {
		return fmt.Errorf("Vorgemerkte Ersatzteile vorhanden; die Bestätigung ‚Keine Ersatzteile benötigt‘ ist nicht möglich")
	}
	return nil
}

func writeGlobalBoardError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
