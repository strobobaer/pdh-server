package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pdh/internal/modules/maintenance"
)

type GlobalBoardActionInput struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Action        string `json:"action"`
	Comment       string `json:"comment"`
	RFIDUID       string `json:"rfid_uid"`
	UserID        string `json:"user_id"` // handelnde Person aus der Auswahl (global_dashboard_actor.go)
	AssignedTo    string `json:"assigned_to"`
	FollowUpDate  string `json:"follow_up_date"`
	NoPartsNeeded bool   `json:"no_parts_needed"`
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
	// Kommentar freiwillig (der Leitstand fragt nicht mehr danach)
	if len([]rune(in.Comment)) > 1000 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Der Kommentar darf höchstens 1000 Zeichen lang sein")
		return
	}
	if !globalBoardTypeAllowed(in.Type) || !globalBoardActionAllowed(in.Action) || in.ID == "" {
		writeGlobalBoardError(w, http.StatusBadRequest, "Unbekannter Vorgang oder Aktion")
		return
	}

	actor, how, err := h.boardActor(r, in.UserID, in.RFIDUID)
	if err != nil {
		writeGlobalBoardError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if in.Comment == "" {
		in.Comment = "Am Leitstand (" + how + ")"
	}
	if !h.globalBoardRecordActive(r, in.Type, in.ID) {
		writeGlobalBoardError(w, http.StatusConflict, "Der Vorgang ist nicht mehr offen")
		return
	}

	var assignedTo *string
	if in.Action == "accept" {
		// Zuweisung ist momentan noch fuer jeden aktiven Mitarbeiter offen
		// (anders als die RFID-Bedienung des Terminals oben) - nur
		// pruefen, dass es sich um ein existierendes, aktives Konto
		// handelt.
		if err := h.globalBoardWorkerActive(r, in.AssignedTo); err != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, "Bitte einen aktiven Mitarbeiter auswählen")
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
	go h.notifyMicrosoftTeamsBoardAction(in, assignedTo)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) globalBoardRecordActive(r *http.Request, refType, id string) bool {
	var query string
	switch refType {
	case "fault":
		query = `SELECT EXISTS(SELECT 1 FROM faults WHERE id=$1::uuid AND status IN ('detected','analyzing','in_progress','pending'))`
	case "ticket":
		query = `SELECT EXISTS(SELECT 1 FROM tickets WHERE id=$1::uuid AND status IN ('open','in_progress','pending'))`
	case "maintenance":
		query = `SELECT EXISTS(SELECT 1 FROM maintenance_tasks WHERE id=$1::uuid AND status IN ('open','in_progress','pending'))`
	case "task":
		query = `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1::uuid AND status IN ('open','in_progress','pending'))`
	default:
		return false
	}
	var active bool
	return h.db.QueryRow(r.Context(), query, id).Scan(&active) == nil && active
}

// globalBoardWorkerActive prueft, ob id ein existierendes, aktives,
// nicht-System-Konto ist - Grundlage fuer die (aktuell noch fuer jeden
// Mitarbeiter offene) Zuweisung ueber den Leitstand.
func (h *Handler) globalBoardWorkerActive(r *http.Request, id string) error {
	var userID string
	return h.db.QueryRow(r.Context(), `
		SELECT id::text FROM users
		WHERE id=$1::uuid AND active=true AND is_system_user=false`, id).Scan(&userID)
}

func (h *Handler) applyGlobalBoardAction(r *http.Request, in GlobalBoardActionInput, actorID string, followUpDate *time.Time) error {
	ctx := r.Context()
	switch in.Action {
	case "accept":
		if in.Type == "task" {
			// Aufgaben erlauben mehrere Zugewiesene (task_assignees) -
			// "Annehmen" fuegt den annehmenden Mitarbeiter hinzu statt eine
			// bestehende Zuweisung zu ersetzen.
			result, err := h.db.Exec(ctx, `UPDATE tasks SET status='in_progress',updated_at=NOW() WHERE id=$1::uuid AND status IN ('open','in_progress','pending')`, in.ID)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return fmt.Errorf("Der Vorgang wurde zwischenzeitlich geändert")
			}
			_, err = h.db.Exec(ctx, `INSERT INTO task_assignees (task_id, user_id) VALUES ($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, in.ID, in.AssignedTo)
			return err
		}
		if in.Type == "maintenance" {
			return h.maint.Accept(ctx, in.ID, in.AssignedTo)
		}
		var query string
		switch in.Type {
		case "fault":
			query = `UPDATE faults SET assigned_to=$1::uuid,status='in_progress',updated_at=NOW() WHERE id=$2::uuid AND status IN ('detected','analyzing','in_progress','pending')`
		case "ticket":
			query = `UPDATE tickets SET assigned_to=$1::uuid,status='in_progress',updated_at=NOW() WHERE id=$2::uuid AND status IN ('open','in_progress','pending')`
		}
		result, err := h.db.Exec(ctx, query, in.AssignedTo, in.ID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("Der Vorgang wurde zwischenzeitlich geändert")
		}
	case "wait":
		// Wiedervorlage: Termin verschieben und Status „Wartet“ setzen
		var err error
		switch in.Type {
		case "fault":
			err = h.faults.UpdateDueDate(ctx, in.ID, followUpDate)
		case "ticket":
			err = h.tickets.UpdateDueDate(ctx, in.ID, followUpDate)
		case "maintenance":
			return h.setBoardWaiting(r, in.Type, in.ID, actorID, *followUpDate)
		case "task":
			_, err = h.db.Exec(ctx, `UPDATE tasks SET due_date=$1::date,updated_at=NOW() WHERE id=$2::uuid AND status IN ('open','in_progress','pending')`, followUpDate.Format("2006-01-02"), in.ID)
		}
		if err != nil {
			return err
		}
		return h.setBoardWaiting(r, in.Type, in.ID, actorID, *followUpDate)
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
			// uebersprungen: der Plan bekommt sofort seinen naechsten Termin
			if _, err := h.maint.Skip(ctx, in.ID, actorID); err != nil {
				if maintenance.IsInputError(err) {
					return fmt.Errorf("%s", maintenance.InputMessage(err))
				}
				return err
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

// setBoardWaiting: nach „Warten“ am Leitstand steht der Vorgang auf „Wartet“
// (pending) – in allen Listen, nicht nur am Leitstand. Annehmen, „Geht noch
// weiter“ oder Starten setzen ihn wieder auf „In Arbeit“.
func (h *Handler) setBoardWaiting(r *http.Request, refType, id, actorID string, until time.Time) error {
	if refType == "maintenance" { // Wartung: Termin + Status ueber das Wartungsmodul
		if err := h.maint.Wait(r.Context(), id, until); err != nil {
			return err
		}
		h.addHistory(r.Context(), "maintenance_task", id, "status", "status", "", "pending", "Wartet bis "+until.Format("02.01.2006")+" (Leitstand)", actorID)
		return nil
	}
	table := map[string]string{"fault": "faults", "ticket": "tickets", "task": "tasks"}[refType]
	if table == "" {
		return fmt.Errorf("Unbekannter Vorgang")
	}
	ctx := r.Context()
	var old string
	if err := h.db.QueryRow(ctx, "SELECT status::text FROM "+table+" WHERE id = $1::uuid", id).Scan(&old); err != nil {
		return err
	}
	if old != "pending" {
		if _, err := h.db.Exec(ctx, "UPDATE "+table+" SET status = 'pending', updated_at = NOW() WHERE id = $1::uuid", id); err != nil {
			return err
		}
	}
	h.addHistory(ctx, refType, id, "status", "status", old, "pending", "Wartet bis "+until.Format("02.01.2006")+" (Leitstand)", actorID)
	return nil
}
