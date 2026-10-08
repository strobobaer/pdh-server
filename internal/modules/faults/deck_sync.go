package faults

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"pdh/internal/integrations/nextcloud"
)

func (s *Service) syncFaultAsync(f *Fault) {
	deck := nextcloud.DeckClientFromEnv()
	if deck.Enabled() {
		input := nextcloud.DeckCardInput{
			RefType:     "fault",
			RefID:       f.ID,
			Title:       "Stoerung: " + f.Title,
			Description: faultDeckDescription(f),
			Priority:    string(f.Severity),
		}
		go func(faultID, title string, cardInput nextcloud.DeckCardInput) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			card, err := deck.CreateFaultCard(ctx, cardInput)
			if err != nil {
				log.Error().Err(err).Str("fault_id", faultID).Str("title", title).Msg("nextcloud deck fault card create failed")
				return
			}
			if card != nil {
				log.Info().Str("fault_id", faultID).Int("deck_card_id", card.ID).Msg("nextcloud deck fault card created")
			}
		}(f.ID, f.Title, input)
	} else {
		log.Debug().Str("fault_id", f.ID).Str("title", f.Title).Msg("nextcloud deck fault sync disabled")
	}

	if autoTicketEnabled() {
		// ohne Zuweisung erst vormerken – das Ticket entsteht nach der Zuweisung
		// (StartPendingTicketWatcher); so landet kein Ticket ohne Zuständige
		if !faultAssigned(f) {
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := s.repo.MarkTicketPending(ctx, id); err != nil {
					log.Error().Err(err).Str("fault_id", id).Msg("ticket from fault: vormerken fehlgeschlagen")
					return
				}
				log.Info().Str("fault_id", id).Msg("ticket from fault: wartet auf zuweisung")
			}(f.ID)
			return
		}
		priority := severityToTicketPriority(f.Severity)
		go func(fault *Fault) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ticketID, err := s.repo.CreateTicketFromFault(ctx, fault, priority)
			if err != nil {
				log.Error().Err(err).Str("fault_id", fault.ID).Str("title", fault.Title).Msg("ticket from fault create failed")
				return
			}
			log.Info().Str("fault_id", fault.ID).Str("ticket_id", ticketID).Msg("ticket from fault created")
		}(cloneFault(f))
	}
}

// autoTicketEnabled: Ticket aus jeder neuen Störung (Server-Einstellung).
func autoTicketEnabled() bool {
	return truthyEnv("PDH_FAULT_CREATE_TICKET") || truthyEnv("PDH_FAULT_CREATE_TICKET_ENABLED")
}

// faultAssigned: Störung ist einer Person zugewiesen (eine Gruppe prüft die
// Datenbank im Hintergrundlauf – sie wird erst nach dem Anlegen gesetzt).
func faultAssigned(f *Fault) bool { return f.AssignedTo != nil && *f.AssignedTo != "" }

// StartPendingTicketWatcher legt vorgemerkte Tickets an, sobald die Störung
// zugewiesen ist (Person oder Gruppe) – alle 15 Sekunden.
func (s *Service) StartPendingTicketWatcher(ctx context.Context) {
	if !autoTicketEnabled() {
		return
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				if n, err := s.CreatePendingTickets(runCtx); err != nil {
					log.Error().Err(err).Msg("ticket from fault: vorgemerkte anlegen fehlgeschlagen")
				} else if n > 0 {
					log.Info().Int("tickets", n).Msg("ticket from fault: nach zuweisung angelegt")
				}
				cancel()
			}
		}
	}()
}

// CreatePendingTickets: vorgemerkte Störungen mit Zuweisung bekommen ihr Ticket;
// erledigte oder bereits verknüpfte verlieren die Markierung.
func (s *Service) CreatePendingTickets(ctx context.Context) (int, error) {
	if _, err := s.repo.db.Exec(ctx, `UPDATE faults SET ticket_pending = false
		WHERE ticket_pending AND (status::text IN ('resolved', 'closed') OR archived_at IS NOT NULL OR linked_ticket_id IS NOT NULL)`); err != nil {
		return 0, err
	}
	rows, err := s.repo.db.Query(ctx, `SELECT id::text FROM faults
		WHERE ticket_pending AND (assigned_to IS NOT NULL OR assigned_group_id IS NOT NULL)`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		// atomar übernehmen (auch bei mehreren Instanzen nur ein Ticket)
		tag, err := s.repo.db.Exec(ctx, `UPDATE faults SET ticket_pending = false WHERE id = $1::uuid AND ticket_pending`, id)
		if err != nil || tag.RowsAffected() == 0 {
			continue
		}
		f, err := s.repo.GetByID(ctx, id)
		if err != nil {
			continue
		}
		ticketID, err := s.repo.CreateTicketFromFault(ctx, f, severityToTicketPriority(f.Severity))
		if err != nil {
			_, _ = s.repo.db.Exec(ctx, `UPDATE faults SET ticket_pending = true WHERE id = $1::uuid`, id) // nächster Lauf
			return n, err
		}
		log.Info().Str("fault_id", id).Str("ticket_id", ticketID).Msg("ticket from fault created after assignment")
		n++
	}
	return n, nil
}

func faultDeckDescription(f *Fault) string {
	desc := strings.TrimSpace(f.Description)
	if len(f.Symptoms) > 0 {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "Symptoms:\n"
		for _, symptom := range f.Symptoms {
			if strings.TrimSpace(symptom) != "" {
				desc += "- " + strings.TrimSpace(symptom) + "\n"
			}
		}
	}
	return desc
}

func severityToTicketPriority(sev Severity) string {
	switch sev {
	case SeverityCritical:
		return "critical"
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	default:
		return "low"
	}
}

func truthyEnv(key string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func cloneFault(f *Fault) *Fault {
	if f == nil {
		return nil
	}
	c := *f
	if f.Symptoms != nil {
		c.Symptoms = append([]string(nil), f.Symptoms...)
	}
	return &c
}
