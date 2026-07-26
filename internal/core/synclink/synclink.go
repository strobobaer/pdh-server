package synclink

import "context"

// Linker verbindet Stoerungen, Tickets und Aufgaben untereinander, ohne
// dass die Module sich gegenseitig importieren muessen. Jedes Modul
// registriert beim Start (main.go) seine Callback-Funktionen; Linker ruft
// diese bei Statusaenderungen bzw. neuen Massnahmen der jeweils anderen
// Seite auf. Alle Spiegel-Aufrufe schreiben DIREKT in die Datenbank (nicht
// ueber die normale Service-Methode), damit keine Endlosschleife entsteht.
type Linker struct {
	faultSetStatusDirect   func(ctx context.Context, faultID, status, userID string) error
	faultAddActionDirect   func(ctx context.Context, faultID, description, userID string) error
	faultGetLinkedTicketID func(ctx context.Context, faultID string) (string, error)
	faultGetLinkedTaskID   func(ctx context.Context, faultID string) (string, error)

	ticketSetStatusDirect  func(ctx context.Context, ticketID, status, userID string) error
	ticketAddActionDirect  func(ctx context.Context, ticketID, description, userID string) error
	ticketGetLinkedFaultID func(ctx context.Context, ticketID string) (string, error)
	ticketGetLinkedTaskID  func(ctx context.Context, ticketID string) (string, error)

	taskSetStatusDirect   func(ctx context.Context, taskID, status, userID string) error
	taskAddActionDirect   func(ctx context.Context, taskID, description, userID string) error
	taskGetLinkedFaultID  func(ctx context.Context, taskID string) (string, error)
	taskGetLinkedTicketID func(ctx context.Context, taskID string) (string, error)
}

func New() *Linker {
	return &Linker{}
}

// ── Registrierung ──────────────────────────────────────────────

func (l *Linker) RegisterFault(
	setStatusDirect func(ctx context.Context, faultID, status, userID string) error,
	addActionDirect func(ctx context.Context, faultID, description, userID string) error,
	getLinkedTicketID func(ctx context.Context, faultID string) (string, error),
	getLinkedTaskID func(ctx context.Context, faultID string) (string, error),
) {
	l.faultSetStatusDirect = setStatusDirect
	l.faultAddActionDirect = addActionDirect
	l.faultGetLinkedTicketID = getLinkedTicketID
	l.faultGetLinkedTaskID = getLinkedTaskID
}

func (l *Linker) RegisterTicket(
	setStatusDirect func(ctx context.Context, ticketID, status, userID string) error,
	addActionDirect func(ctx context.Context, ticketID, description, userID string) error,
	getLinkedFaultID func(ctx context.Context, ticketID string) (string, error),
	getLinkedTaskID func(ctx context.Context, ticketID string) (string, error),
) {
	l.ticketSetStatusDirect = setStatusDirect
	l.ticketAddActionDirect = addActionDirect
	l.ticketGetLinkedFaultID = getLinkedFaultID
	l.ticketGetLinkedTaskID = getLinkedTaskID
}

func (l *Linker) RegisterTask(
	setStatusDirect func(ctx context.Context, taskID, status, userID string) error,
	addActionDirect func(ctx context.Context, taskID, description, userID string) error,
	getLinkedFaultID func(ctx context.Context, taskID string) (string, error),
	getLinkedTicketID func(ctx context.Context, taskID string) (string, error),
) {
	l.taskSetStatusDirect = setStatusDirect
	l.taskAddActionDirect = addActionDirect
	l.taskGetLinkedFaultID = getLinkedFaultID
	l.taskGetLinkedTicketID = getLinkedTicketID
}

// ── Status-Uebersetzung ────────────────────────────────────────
// Ticket und Aufgabe nutzen das gleiche Vokabular (open/in_progress/
// resolved/closed), daher 1:1 durchreichen. Stoerung hat eigenes
// Vokabular (detected/analyzing/in_progress/resolved/closed).

func faultStatusToOther(status string) string {
	switch status {
	case "detected":
		return "open"
	case "analyzing", "in_progress":
		return "in_progress"
	case "resolved":
		return "resolved"
	case "closed":
		return "closed"
	}
	return ""
}

func otherStatusToFault(status string) string {
	switch status {
	case "open":
		return "detected"
	case "in_progress", "pending":
		return "in_progress"
	case "resolved":
		return "resolved"
	case "closed":
		return "closed"
	}
	return ""
}

func passthroughStatus(status string) string {
	switch status {
	case "open", "in_progress", "resolved", "closed":
		return status
	}
	return ""
}

// ── Aufrufe von Fault ────────────────────────────────────────

func (l *Linker) OnFaultStatusChanged(ctx context.Context, faultID, status, userID string) {
	mapped := faultStatusToOther(status)
	if mapped == "" {
		return
	}
	if l.faultGetLinkedTicketID != nil && l.ticketSetStatusDirect != nil {
		if id, err := l.faultGetLinkedTicketID(ctx, faultID); err == nil && id != "" {
			_ = l.ticketSetStatusDirect(ctx, id, mapped, userID)
		}
	}
	if l.faultGetLinkedTaskID != nil && l.taskSetStatusDirect != nil {
		if id, err := l.faultGetLinkedTaskID(ctx, faultID); err == nil && id != "" {
			_ = l.taskSetStatusDirect(ctx, id, mapped, userID)
		}
	}
}

func (l *Linker) OnFaultActionAdded(ctx context.Context, faultID, description, userID string) {
	if l.faultGetLinkedTicketID != nil && l.ticketAddActionDirect != nil {
		if id, err := l.faultGetLinkedTicketID(ctx, faultID); err == nil && id != "" {
			_ = l.ticketAddActionDirect(ctx, id, description, userID)
		}
	}
	if l.faultGetLinkedTaskID != nil && l.taskAddActionDirect != nil {
		if id, err := l.faultGetLinkedTaskID(ctx, faultID); err == nil && id != "" {
			_ = l.taskAddActionDirect(ctx, id, description, userID)
		}
	}
}

// ── Aufrufe von Ticket ───────────────────────────────────────

func (l *Linker) OnTicketStatusChanged(ctx context.Context, ticketID, status, userID string) {
	if l.ticketGetLinkedFaultID != nil && l.faultSetStatusDirect != nil {
		if mapped := otherStatusToFault(status); mapped != "" {
			if id, err := l.ticketGetLinkedFaultID(ctx, ticketID); err == nil && id != "" {
				_ = l.faultSetStatusDirect(ctx, id, mapped, userID)
			}
		}
	}
	if l.ticketGetLinkedTaskID != nil && l.taskSetStatusDirect != nil {
		if mapped := passthroughStatus(status); mapped != "" {
			if id, err := l.ticketGetLinkedTaskID(ctx, ticketID); err == nil && id != "" {
				_ = l.taskSetStatusDirect(ctx, id, mapped, userID)
			}
		}
	}
}

func (l *Linker) OnTicketActionAdded(ctx context.Context, ticketID, description, userID string) {
	if l.ticketGetLinkedFaultID != nil && l.faultAddActionDirect != nil {
		if id, err := l.ticketGetLinkedFaultID(ctx, ticketID); err == nil && id != "" {
			_ = l.faultAddActionDirect(ctx, id, description, userID)
		}
	}
	if l.ticketGetLinkedTaskID != nil && l.taskAddActionDirect != nil {
		if id, err := l.ticketGetLinkedTaskID(ctx, ticketID); err == nil && id != "" {
			_ = l.taskAddActionDirect(ctx, id, description, userID)
		}
	}
}

// ── Aufrufe von Task ─────────────────────────────────────────

func (l *Linker) OnTaskStatusChanged(ctx context.Context, taskID, status, userID string) {
	if l.taskGetLinkedFaultID != nil && l.faultSetStatusDirect != nil {
		if mapped := otherStatusToFault(status); mapped != "" {
			if id, err := l.taskGetLinkedFaultID(ctx, taskID); err == nil && id != "" {
				_ = l.faultSetStatusDirect(ctx, id, mapped, userID)
			}
		}
	}
	if l.taskGetLinkedTicketID != nil && l.ticketSetStatusDirect != nil {
		if mapped := passthroughStatus(status); mapped != "" {
			if id, err := l.taskGetLinkedTicketID(ctx, taskID); err == nil && id != "" {
				_ = l.ticketSetStatusDirect(ctx, id, mapped, userID)
			}
		}
	}
}

func (l *Linker) OnTaskActionAdded(ctx context.Context, taskID, description, userID string) {
	if l.taskGetLinkedFaultID != nil && l.faultAddActionDirect != nil {
		if id, err := l.taskGetLinkedFaultID(ctx, taskID); err == nil && id != "" {
			_ = l.faultAddActionDirect(ctx, id, description, userID)
		}
	}
	if l.taskGetLinkedTicketID != nil && l.ticketAddActionDirect != nil {
		if id, err := l.taskGetLinkedTicketID(ctx, taskID); err == nil && id != "" {
			_ = l.ticketAddActionDirect(ctx, id, description, userID)
		}
	}
}
