package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const legacyBaselineVersion = 10

type migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

func RunMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	migrations, err := loadMigrations(dir)
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return nil
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			name       TEXT NOT NULL,
			checksum   TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("schema_migrations anlegen: %w", err)
	}

	applied, err := appliedMigrations(ctx, pool)
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		complete, err := legacySchemaComplete(ctx, pool)
		if err != nil {
			return err
		}
		if complete {
			if err := baselineLegacyMigrations(ctx, pool, migrations); err != nil {
				return err
			}
			applied, err = appliedMigrations(ctx, pool)
			if err != nil {
				return err
			}
		}
	}

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := runMigration(ctx, pool, m); err != nil {
			return err
		}
	}

	return nil
}

func loadMigrations(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("migrationen lesen: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		version, err := migrationVersion(name)
		if err != nil {
			return nil, err
		}

		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("migration %s lesen: %w", name, err)
		}

		sum := sha256.Sum256(content)
		migrations = append(migrations, migration{
			Version:  version,
			Name:     name,
			SQL:      string(content),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("ungueltiger migrationsname %q", name)
	}

	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("ungueltige migrationsversion %q: %w", name, err)
	}

	return version, nil
}

func appliedMigrations(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("angewendete migrationen lesen: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}

	return applied, rows.Err()
}

func legacySchemaComplete(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	requiredTables := []string{
		"users",
		"faults",
		"shift_models",
		"shift_definitions",
		"shift_assignments",
		"time_entries",
		"infrastructure",
		"maintenance_tasks",
		"spare_parts",
		"attachments",
		"warehouses",
		"it_assets",
	}

	for _, table := range requiredTables {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&exists)
		if err != nil {
			return false, fmt.Errorf("legacy-tabelle %s pruefen: %w", table, err)
		}
		if !exists {
			return false, nil
		}
	}

	return true, nil
}

func baselineLegacyMigrations(ctx context.Context, pool *pgxpool.Pool, migrations []migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("legacy-baseline starten: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, m := range migrations {
		if m.Version > legacyBaselineVersion {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
			ON CONFLICT (version) DO NOTHING`,
			m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("legacy-baseline %s: %w", m.Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("legacy-baseline speichern: %w", err)
	}
	return nil
}

func runMigration(ctx context.Context, pool *pgxpool.Pool, m migration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migration %s starten: %w", m.Name, err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("migration %s ausfuehren: %w", m.Name, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO schema_migrations (version, name, checksum)
		VALUES ($1, $2, $3)`,
		m.Version, m.Name, m.Checksum); err != nil {
		return fmt.Errorf("migration %s protokollieren: %w", m.Name, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration %s speichern: %w", m.Name, err)
	}
	return nil
}
