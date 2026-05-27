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

	if truthyEnv("PDH_FAULT_CREATE_TICKET") || truthyEnv("PDH_FAULT_CREATE_TICKET_ENABLED") {
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
