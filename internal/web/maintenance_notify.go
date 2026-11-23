package web

import (
	"context"
	"fmt"
	"time"

	"pdh/internal/modules/maintenance"
)

// Wartung: nächster Termin nach dem Abschluss und Nachricht, wenn aus einem
// Plan automatisch ein Durchführungsauftrag entsteht (Vorlauf erreicht).

// maintNextTerm: nächster Termin des Plans (nil ohne Plan oder ohne Wartungsdienst).
func (h *Handler) maintNextTerm(ctx context.Context, taskID string) *maintenance.NextTerm {
	if h.maint == nil {
		return nil
	}
	return h.maint.NextTermForTask(ctx, taskID)
}

// nextTermText: „Nächster Termin: 12.03.2027“, ohne Auftrag mit dem Tag, an dem er entsteht.
func nextTermText(lang string, n *maintenance.NextTerm) string {
	if n == nil {
		return ""
	}
	s := tr(lang, "Nächster Termin: %s", n.Day.Format("02.01.2006"))
	if n.TaskID == "" {
		s += " – " + tr(lang, "Auftrag entsteht am %s", n.OrderOn.Format("02.01.2006"))
	}
	return s
}

// maintAssignedText: Nachricht an die Person, der eine Wartung zugewiesen ist.
func maintAssignedText(title string, due time.Time, taskID string) string {
	return fmt.Sprintf("🔧 Dir wurde die Wartung **%s** am %s zugewiesen. [Öffnen](/maintenance/tasks/%s)", title, due.Format("02.01.2006"), taskID)
}

// planTaskRecipients: zugewiesene Person und Mitglieder der zugewiesenen
// Gruppe (aktiv, kein Bot, kein Systembenutzer), ohne den Auslöser.
func (h *Handler) planTaskRecipients(ctx context.Context, taskID, actorID string) []string {
	rows, err := h.db.Query(ctx, `
		SELECT u.id::text FROM maintenance_tasks t
		JOIN users u ON u.id = t.assigned_to OR u.id IN (SELECT gm.user_id FROM user_group_members gm WHERE gm.group_id = t.assigned_group_id)
		WHERE t.id = $1::uuid AND u.active AND NOT u.is_bot AND NOT u.is_system_user AND u.id::text <> $2
		ORDER BY u.id`, taskID, actorID)
	if err != nil {
		componentLog("wartung").Warn().Err(err).Str("auftrag", taskID).Msg("empfänger des neuen auftrags nicht ladbar")
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && (len(out) == 0 || out[len(out)-1] != id) {
			out = append(out, id)
		}
	}
	return out
}

// notifyPlanTaskCreated: Hinweis an die Zuständigen, wenn der Auftrag mit dem
// Vorlauf automatisch entstanden ist (maintenance.OnPlanTaskCreated).
func (h *Handler) notifyPlanTaskCreated(ctx context.Context, taskID, actorID string) {
	if h.db == nil {
		return
	}
	to := h.planTaskRecipients(ctx, taskID, actorID)
	if len(to) == 0 {
		return
	}
	var title string
	var due time.Time
	if err := h.db.QueryRow(ctx, `SELECT title, due_date FROM maintenance_tasks WHERE id = $1::uuid`, taskID).Scan(&title, &due); err != nil {
		return
	}
	h.systemNotify(ctx, to, maintAssignedText(title, due, taskID))
}

// maintDetailNext: nächster Termin auf der Seite eines abgeschlossenen
// Auftrags – der Folgeauftrag, sonst der geplante Termin mit Auftragstag.
func (h *Handler) maintDetailNext(ctx context.Context, d *MaintenanceTaskDetailData, taskID string) {
	n := h.maintNextTerm(ctx, taskID)
	if n == nil {
		return
	}
	if n.TaskID != "" {
		d.NextTaskID, d.NextTaskDue = n.TaskID, n.Day.Format("02.01.2006")
		return
	}
	d.NextDay, d.NextOrderOn = n.Day.Format("02.01.2006"), n.OrderOn.Format("02.01.2006")
}
