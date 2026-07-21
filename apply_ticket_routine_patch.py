#!/usr/bin/env python3
"""
Patcht internal/modules/tickets/tickets.go:
1. Ticket-Struct: Resolution/RootCause/NoPartsNeeded-Felder ergänzt
2. Service.UpdateStatus(): lehnt direkten Wechsel zu "resolved"/"closed" ab
3. Routes(): neue Endpunkte für Maßnahmen-Verlauf + Ersatzteil-Merkliste
4. Import "fmt" ergänzt

Aufruf:
    python3 apply_ticket_routine_patch.py internal/modules/tickets/tickets.go
"""
import sys

REPLACEMENTS = [
    (
        "Ticket struct: Resolution/RootCause/NoPartsNeeded ergaenzen",
        '''	// Kostenstelle (eigenständig, unabhängig von der Infrastruktur)
	CostCenterID     *string `json:"cost_center_id,omitempty"`
	CostCenterNumber string  `json:"cost_center_number,omitempty"`
	CostCenterName   string  `json:"cost_center_name,omitempty"`
}''',
        '''	// Kostenstelle (eigenständig, unabhängig von der Infrastruktur)
	CostCenterID     *string `json:"cost_center_id,omitempty"`
	CostCenterNumber string  `json:"cost_center_number,omitempty"`
	CostCenterName   string  `json:"cost_center_name,omitempty"`

	// Pflichtangaben beim Schließen: Maßnahmen-Verlauf + Ersatzteilverwendung
	Resolution    *string `json:"resolution,omitempty"`
	RootCause     *string `json:"root_cause,omitempty"`
	NoPartsNeeded bool    `json:"no_parts_needed,omitempty"`
}''',
    ),
    (
        "UpdateStatus: direkten Abschluss ueber die Schnellauswahl blockieren",
        '''func (s *Service) UpdateStatus(ctx context.Context, id string, status Status, userID string) error {
	err := s.repo.UpdateStatus(ctx, id, status, userID)
	if err == nil && eventBus != nil && status == StatusResolved {
		eventBus.Publish("ticket.resolved", map[string]interface{}{
			"id": id, "resolved_by": userID,
		})
	}
	return err
}''',
        '''func (s *Service) UpdateStatus(ctx context.Context, id string, status Status, userID string) error {
	if status == StatusResolved || status == StatusClosed {
		return fmt.Errorf(`bitte das Ticket über "Ticket lösen" abschließen, nicht über die Status-Auswahl`)
	}
	return s.repo.UpdateStatus(ctx, id, status, userID)
}''',
    ),
    (
        "Import fmt ergaenzen",
        '''import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"pdh/internal/core/addins"
	"pdh/internal/integrations/nextcloud"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)''',
        '''import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"pdh/internal/core/addins"
	"pdh/internal/integrations/nextcloud"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)''',
    ),
    (
        "Routes(): neue Endpunkte ergaenzen",
        '''	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/status", h.UpdateStatus)
	r.Post("/{id}/cost-center", h.UpdateCostCenter)
	r.Post("/{id}/comments", h.AddComment)

	return r
}''',
        '''	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/status", h.UpdateStatus)
	r.Post("/{id}/cost-center", h.UpdateCostCenter)
	r.Post("/{id}/comments", h.AddComment)
	r.Post("/{id}/resolve", h.Resolve)
	r.Post("/{id}/actions", h.AddAction)
	r.Get("/{id}/actions", h.GetActions)
	r.Delete("/{id}/actions/{actionID}", h.DeleteAction)
	r.Get("/{id}/parts-usage", h.GetPartsUsage)
	r.Post("/{id}/pending-parts", h.AddPendingPart)
	r.Get("/{id}/pending-parts", h.GetPendingParts)
	r.Delete("/{id}/pending-parts/{partItemID}", h.DeletePendingPart)

	return r
}''',
    ),
]


def main():
    if len(sys.argv) != 2:
        print("Aufruf: python3 apply_ticket_routine_patch.py internal/modules/tickets/tickets.go")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()

    changed = content
    for label, old, new in REPLACEMENTS:
        count = changed.count(old)
        if count == 0:
            print(f"FEHLER: Block '{label}' wurde nicht gefunden. Nichts geändert.")
            sys.exit(1)
        if count > 1:
            print(f"FEHLER: Block '{label}' wurde {count}x gefunden (erwartet 1x). Nichts geändert.")
            sys.exit(1)
        changed = changed.replace(old, new, 1)

    with open(path, "w", encoding="utf-8") as f:
        f.write(changed)
    print(f"OK: {path} gepatcht ({len(REPLACEMENTS)} Stellen ersetzt).")


if __name__ == "__main__":
    main()
