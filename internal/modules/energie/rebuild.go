package energie

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Gründe, aus denen eine Datei nach einer Neurechnung bleibt, wie sie ist.
const (
	keepSent     = "schon verschickt, nicht geändert"
	keepOld      = "vor dem Update erzeugt, nicht geändert"
	keepSettings = "Einstellung des Exports geändert, nicht geändert"
)

// Rebuild erzeugt nach einer Neurechnung die Dateien des Exports c neu, die
// Intervalle nach from enthalten und von denen noch nichts verschickt ist –
// nur, wenn der Export eine der Messstellen keys enthält. Verschickt ist eine
// Datei, sobald eine Fassung zugestellt wurde; beim Ziel „Zum Abholen im PDH“,
// sobald sie einmal heruntergeladen wurde. dry: nur sagen, was geschähe.
func (e *Exporter) Rebuild(ctx context.Context, c ExportConfig, from time.Time, keys map[string]bool, dry bool) (rebuilt, kept []FileImpact, err error) {
	c = c.normalized()
	meters, err := e.meters(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	hit := false
	for _, m := range meters {
		if keys[m.Key] {
			hit = true
			break
		}
	}
	if !hit {
		return nil, nil, nil
	}
	tx, err := e.Store.DB.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	// Dieselbe Sperre wie ein Exportblock; ohne Zustand gibt es keine Dateien.
	var last time.Time
	err = tx.QueryRow(ctx, `SELECT last_interval_end FROM energy_export_state
		WHERE connection_id = $1::uuid FOR UPDATE`, c.ConnectionID).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !dry {
		// Eine laufende Zustellung erst fertig werden lassen (gleiche Sperre wie deliver).
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "energy_export_deliver:"+c.ConnectionID); err != nil {
			return nil, nil, err
		}
	}
	var conn string
	if err := tx.QueryRow(ctx, `SELECT name FROM import_export_connections WHERE id = $1::uuid`, c.ConnectionID).Scan(&conn); err != nil {
		return nil, nil, err
	}

	type file struct {
		id, name, timeBase string
		firstEnd, lastEnd  *time.Time
		delivered          int
		fetched            bool
	}
	rows, err := tx.Query(ctx, `SELECT id::text, name, time_base, first_end, last_end, delivered_version, fetched_at IS NOT NULL
		FROM energy_export_files WHERE connection_id = $1::uuid AND (last_end IS NULL OR last_end > $2)
		  AND period_end > $2 ORDER BY name`, c.ConnectionID, from)
	if err != nil {
		return nil, nil, err
	}
	var files []file
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.id, &f.name, &f.timeBase, &f.firstEnd, &f.lastEnd, &f.delivered, &f.fetched); err != nil {
			rows.Close()
			return nil, nil, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	_, pickup := c.Target.(PickupTarget)
	for _, f := range files {
		fi := FileImpact{Connection: conn, Name: f.name}
		sent := f.fetched || (!pickup && f.delivered > 0)
		switch {
		case sent:
			fi.Reason = keepSent
			kept = append(kept, fi)
			continue
		case f.firstEnd == nil || f.lastEnd == nil:
			fi.Reason = keepOld
			kept = append(kept, fi)
			continue
		case f.timeBase != c.CSV.TimeBase || !f.firstEnd.Equal(f.firstEnd.Truncate(c.Interval)) ||
			f.lastEnd.Sub(*f.firstEnd)%c.Interval != 0 || f.lastEnd.Before(*f.firstEnd):
			fi.Reason = keepSettings
			kept = append(kept, fi)
			continue
		}
		rebuilt = append(rebuilt, fi)
		if dry {
			continue
		}
		n := int(f.lastEnd.Sub(*f.firstEnd)/c.Interval) + 1
		steps, err := e.Store.RangeValues(ctx, meters, *f.firstEnd, n, c.Interval, c.Mode)
		if err != nil {
			return nil, nil, err
		}
		var buf []byte
		count := 0
		for _, s := range steps {
			if len(s.Points) == 0 {
				continue
			}
			data, err := FormatRows(s.Points, c.CSV)
			if err != nil {
				return nil, nil, err
			}
			buf = append(buf, data...)
			count += len(s.Points)
		}
		content := buf
		if c.CSV.Header {
			content = joinCSV(HeaderLine(c.CSV), buf)
		}
		if _, err := tx.Exec(ctx, `UPDATE energy_export_files SET content = $2, rows = $3, version = version + 1,
				last_error = '', updated_at = now() WHERE id = $1::uuid`, f.id, content, count); err != nil {
			return nil, nil, err
		}
	}
	if dry {
		return rebuilt, kept, nil
	}
	return rebuilt, kept, tx.Commit(ctx)
}
