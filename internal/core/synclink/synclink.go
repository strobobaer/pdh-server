package synclink

import "context"

// Linker verbindet Stoerungen und Tickets bidirektional, ohne dass die
// beiden Module sich gegenseitig importieren muessen. Jedes Modul
// registriert beim Start (main.go) seine Callback-Funktionen; Linker ruft
// diese bei Statusaenderungen bzw. neuen Massnahmen der jeweils anderen
// Seite auf. Alle Spiegel-Aufrufe schreiben DIREKT in die Datenbank (nicht
// ueber die normale Service-Methode), damit keine Endlosschleife entsteht.
type Linker struct {
	faultSetStatusDirect   func(ctx context.Context, faultID, status, userID string) error
	ticketSetStatusDirect  func(ctx context.Context, ticketID, status, userID string) error
	faultAddActionDirect   func(ctx context.Context, faultID, description, userID string) error
	ticketAddActionDirect  func(ctx context.Context, ticketID, description, userID string) error
	faultGetLinkedTicketID func(ctx context.Context, faultID string) (string, error)
	ticketGetLinkedFaultID func(ctx context.Context, ticketID string) (string, error)
}

func New() *Linker {
	return &Linker{}
}

// ── Registrierung durch das faults-Modul ──────────────────────

func (l *Linker) RegisterFault(
	setStatusDirect func(ctx context.Context, faultID, status, userID string) error,
	addActionDirect func(ctx context.Context, faultID, description, userID string) error,
	getLinkedTicketID func(ctx context.Context, faultID string) (string, error),
) {
	l.faultSetStatusDirect = setStatusDirect
	l.faultAddActionDirect = addActionDirect
	l.faultGetLinkedTicketID = getLinkedTicketID
}

// ── Registrierung durch das tickets-Modul ─────────────────────

func (l *Linker) RegisterTicket(
	setStatusDirect func(ctx context.Context, ticketID, status, userID string) error,
	addActionDirect func(ctx context.Context, ticketID, description, userID string) error,
	getLinkedFaultID func(ctx context.Context, ticketID string) (string, error),
) {
	l.ticketSetStatusDirect = setStatusDirect
	l.ticketAddActionDirect = addActionDirect
	l.ticketGetLinkedFaultID = getLinkedFaultID
}

// ── Status-Uebersetzung ────────────────────────────────────────

func faultStatusToTicket(status string) string {
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

func ticketStatusToFault(status string) string {
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

// ── Aufrufe von faults nach tickets ────────────────────────────

func (l *Linker) OnFaultStatusChanged(ctx context.Context, faultID, status, userID string) {
	if l.faultGetLinkedTicketID == nil || l.ticketSetStatusDirect == nil {
		return
	}
	ticketID, err := l.faultGetLinkedTicketID(ctx, faultID)
	if err != nil || ticketID == "" {
		return
	}
	mapped := faultStatusToTicket(status)
	if mapped == "" {
		return
	}
	_ = l.ticketSetStatusDirect(ctx, ticketID, mapped, userID)
}

func (l *Linker) OnFaultActionAdded(ctx context.Context, faultID, description, userID string) {
	if l.faultGetLinkedTicketID == nil || l.ticketAddActionDirect == nil {
		return
	}
	ticketID, err := l.faultGetLinkedTicketID(ctx, faultID)
	if err != nil || ticketID == "" {
		return
	}
	_ = l.ticketAddActionDirect(ctx, ticketID, description, userID)
}

// ── Aufrufe von tickets nach faults ─────────────────────────────

func (l *Linker) OnTicketStatusChanged(ctx context.Context, ticketID, status, userID string) {
	if l.ticketGetLinkedFaultID == nil || l.faultSetStatusDirect == nil {
		return
	}
	faultID, err := l.ticketGetLinkedFaultID(ctx, ticketID)
	if err != nil || faultID == "" {
		return
	}
	mapped := ticketStatusToFault(status)
	if mapped == "" {
		return
	}
	_ = l.faultSetStatusDirect(ctx, faultID, mapped, userID)
}

func (l *Linker) OnTicketActionAdded(ctx context.Context, ticketID, description, userID string) {
	if l.ticketGetLinkedFaultID == nil || l.faultAddActionDirect == nil {
		return
	}
	faultID, err := l.ticketGetLinkedFaultID(ctx, ticketID)
	if err != nil || faultID == "" {
		return
	}
	_ = l.faultAddActionDirect(ctx, faultID, description, userID)
}
