package maintenance

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"pdh/internal/integrations/nextcloud"
)

func (s *Service) syncMaintenanceTaskToDeckAsync(t *MaintenanceTask) {
	deck := nextcloud.DeckClientFromEnv()
	if !deck.Enabled() {
		log.Debug().Str("maintenance_task_id", t.ID).Str("title", t.Title).Msg("nextcloud deck maintenance sync disabled")
		return
	}
	copyTask := *t
	go func(task MaintenanceTask) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if ok, err := s.repo.deckMarkerExists(ctx, "maintenance_task", task.ID); err != nil {
			log.Error().Err(err).Str("maintenance_task_id", task.ID).Msg("deck sync marker check failed")
			return
		} else if ok {
			return
		}

		items, err := s.repo.checklistLabelsForTask(ctx, &task)
		if err != nil {
			log.Error().Err(err).Str("maintenance_task_id", task.ID).Msg("maintenance checklist labels load failed")
		}

		card, err := deck.CreateMaintenanceCard(ctx, nextcloud.DeckCardInput{
			RefType:     "maintenance_task",
			RefID:       task.ID,
			Title:       "Wartung: " + task.Title,
			Description: task.Description,
			Priority:    string(task.Priority),
			DueDate:     task.DueDate.Format(time.RFC3339),
			Checklist:   items,
		})
		if err != nil {
			_ = s.repo.upsertDeckMarkerError(ctx, "maintenance_task", task.ID, err.Error())
			log.Error().Err(err).Str("maintenance_task_id", task.ID).Str("title", task.Title).Msg("nextcloud deck maintenance card create failed")
			return
		}
		if card != nil {
			_ = s.repo.upsertDeckMarker(ctx, "maintenance_task", task.ID, card.ID)
			log.Info().Str("maintenance_task_id", task.ID).Int("deck_card_id", card.ID).Msg("nextcloud deck maintenance card created")
		}
	}(copyTask)
}

func (s *Service) syncOpenMaintenanceTasksToDeckAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		tasks, err := s.repo.ListTasks(ctx, TaskOpen, "")
		if err != nil {
			log.Error().Err(err).Msg("maintenance tasks load for deck sync failed")
			return
		}
		for _, task := range tasks {
			s.syncMaintenanceTaskToDeckAsync(task)
		}
	}()
}

func (r *Repository) deckMarkerExists(ctx context.Context, refType, refID string) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM deck_sync_markers
			WHERE ref_type=$1 AND ref_id=$2 AND destination='nextcloud_deck' AND last_error IS NULL
		)`, refType, refID).Scan(&ok)
	return ok, err
}

func (r *Repository) upsertDeckMarker(ctx context.Context, refType, refID string, deckCardID int) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO deck_sync_markers (ref_type, ref_id, destination, external_id, synced_at)
		VALUES ($1, $2, 'nextcloud_deck', $3, NOW())
		ON CONFLICT (ref_type, ref_id, destination) DO UPDATE SET
		  external_id=EXCLUDED.external_id,
		  synced_at=NOW(),
		  last_error=NULL`, refType, refID, deckCardID)
	return err
}

func (r *Repository) upsertDeckMarkerError(ctx context.Context, refType, refID, message string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO deck_sync_markers (ref_type, ref_id, destination, last_error, synced_at)
		VALUES ($1, $2, 'nextcloud_deck', $3, NOW())
		ON CONFLICT (ref_type, ref_id, destination) DO UPDATE SET
		  last_error=EXCLUDED.last_error,
		  synced_at=NOW()`, refType, refID, message)
	return err
}

func (r *Repository) checklistLabelsForTask(ctx context.Context, t *MaintenanceTask) ([]string, error) {
	if t == nil || t.PlanID == nil {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT ci.label
		FROM maintenance_plans mp
		JOIN checklist_items ci ON ci.checklist_id=mp.checklist_id
		WHERE mp.id=$1
		ORDER BY ci.sort_order`, *t.PlanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err == nil && label != "" {
			labels = append(labels, label)
		}
	}
	return labels, rows.Err()
}
