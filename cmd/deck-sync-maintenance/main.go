package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"pdh/internal/integrations/nextcloud"
)

type taskRow struct {
	ID          string
	Title       string
	Description string
	Priority    string
	DueDate     time.Time
	Checklist   []string
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dsn := databaseURLFromEnv()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("database connect failed")
	}
	defer db.Close()

	if err := ensureMarkerTable(ctx, db); err != nil {
		log.Fatal().Err(err).Msg("deck marker table failed")
	}

	limit := 25
	if v := strings.TrimSpace(os.Getenv("PDH_DECK_SYNC_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	tasks, err := loadTasks(ctx, db, limit)
	if err != nil {
		log.Fatal().Err(err).Msg("load maintenance tasks failed")
	}
	log.Info().Int("count", len(tasks)).Msg("maintenance deck sync candidates loaded")

	deck := nextcloud.DeckClientFromEnv()
	if !deck.Enabled() {
		log.Warn().Msg("nextcloud deck disabled or incomplete")
		return
	}

	for _, task := range tasks {
		card, err := deck.CreateMaintenanceCard(ctx, nextcloud.DeckCardInput{
			RefType:     "maintenance_task",
			RefID:       task.ID,
			Title:       "Wartung: " + task.Title,
			Description: task.Description,
			Priority:    task.Priority,
			DueDate:     task.DueDate.Format(time.RFC3339),
			Checklist:   task.Checklist,
		})
		if err != nil {
			_ = markError(ctx, db, task.ID, err.Error())
			log.Error().Err(err).Str("maintenance_task_id", task.ID).Str("title", task.Title).Msg("nextcloud deck maintenance card create failed")
			continue
		}
		if card != nil {
			_ = markSynced(ctx, db, task.ID, card.ID)
			log.Info().Str("maintenance_task_id", task.ID).Int("deck_card_id", card.ID).Msg("nextcloud deck maintenance card created")
		}
	}
}

func databaseURLFromEnv() string {
	if url := strings.TrimSpace(os.Getenv("DATABASE_URL")); url != "" {
		return url
	}
	host := envDefault("DB_HOST", envDefault("PDH_DATABASE_HOST", "127.0.0.1"))
	port := envDefault("DB_PORT", envDefault("PDH_DATABASE_PORT", "5432"))
	name := envDefault("DB_NAME", envDefault("PDH_DATABASE_NAME", "pdh"))
	user := envDefault("DB_USER", envDefault("PDH_DATABASE_USER", "pdh"))
	pass := envDefault("DB_PASSWORD", envDefault("PDH_DATABASE_PASSWORD", ""))
	if pass == "" {
		return fmt.Sprintf("postgres://%s@%s:%s/%s?sslmode=disable", user, host, port, name)
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, name)
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func ensureMarkerTable(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS deck_sync_markers (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			ref_type VARCHAR(50) NOT NULL,
			ref_id UUID NOT NULL,
			destination VARCHAR(50) NOT NULL DEFAULT 'nextcloud_deck',
			external_id VARCHAR(100),
			external_url TEXT,
			synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_error TEXT,
			UNIQUE(ref_type, ref_id, destination)
		)`)
	return err
}

func loadTasks(ctx context.Context, db *pgxpool.Pool, limit int) ([]taskRow, error) {
	rows, err := db.Query(ctx, `
		SELECT mt.id::text,
		       mt.title,
		       COALESCE(mt.description,''),
		       mt.priority,
		       mt.due_date,
		       COALESCE(json_agg(ci.label ORDER BY ci.sort_order) FILTER (WHERE ci.id IS NOT NULL), '[]'::json)
		FROM maintenance_tasks mt
		LEFT JOIN maintenance_plans mp ON mt.plan_id = mp.id
		LEFT JOIN checklist_items ci ON mp.checklist_id = ci.checklist_id
		LEFT JOIN deck_sync_markers dsm
		  ON dsm.ref_type='maintenance_task'
		 AND dsm.ref_id=mt.id
		 AND dsm.destination='nextcloud_deck'
		 AND dsm.last_error IS NULL
		WHERE dsm.id IS NULL
		  AND mt.status IN ('open','in_progress')
		GROUP BY mt.id, mt.title, mt.description, mt.priority, mt.due_date, mt.created_at
		ORDER BY mt.created_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []taskRow
	for rows.Next() {
		var t taskRow
		var checklistJSON []byte
		if err := rows.Scan(&t.ID, &t.Title, &t.Description, &t.Priority, &t.DueDate, &checklistJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(checklistJSON, &t.Checklist)
		out = append(out, t)
	}
	return out, rows.Err()
}

func markSynced(ctx context.Context, db *pgxpool.Pool, taskID string, cardID int) error {
	_, err := db.Exec(ctx, `
		INSERT INTO deck_sync_markers (ref_type, ref_id, destination, external_id, synced_at)
		VALUES ('maintenance_task', $1, 'nextcloud_deck', $2, NOW())
		ON CONFLICT (ref_type, ref_id, destination) DO UPDATE SET
			external_id=EXCLUDED.external_id,
			synced_at=NOW(),
			last_error=NULL`, taskID, strconv.Itoa(cardID))
	return err
}

func markError(ctx context.Context, db *pgxpool.Pool, taskID, message string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO deck_sync_markers (ref_type, ref_id, destination, last_error, synced_at)
		VALUES ('maintenance_task', $1, 'nextcloud_deck', $2, NOW())
		ON CONFLICT (ref_type, ref_id, destination) DO UPDATE SET
			last_error=EXCLUDED.last_error,
			synced_at=NOW()`, taskID, message)
	return err
}
