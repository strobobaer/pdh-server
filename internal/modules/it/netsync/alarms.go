package netsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// defaultGrace gilt, wenn Grace keine positive Dauer liefert.
const defaultGrace = 5 * time.Minute

// AlarmAsset beschreibt ein überwachtes Asset für Ticket und Nachricht.
type AlarmAsset struct {
	ID, Name, IP, Uplink string
	InfrastructureID     string // leer, wenn das Asset keiner Anlage zugeordnet ist
	LastSeen             *time.Time
	OfflineSince         time.Time
	TicketID             string // Ticket des letzten Alarms (leer, wenn es keins gibt)
	GraceMinutes         int    // wirksame Karenz in Minuten (Asset, Kategorie oder allgemein)
}

// AlarmSink setzt Alarme in Tickets und Nachrichten um. Alle Aufrufe laufen in
// der Transaktion des Wächters; die gelieferten after-Funktionen (Nachrichten)
// ruft der Wächter erst nach dem Commit auf.
type AlarmSink interface {
	OpenTicket(ctx context.Context, tx pgx.Tx, a AlarmAsset) (ticketID string, after func(), err error)
	TicketOpen(ctx context.Context, tx pgx.Tx, ticketID string) (bool, error)
	Repeat(ctx context.Context, tx pgx.Tx, a AlarmAsset) (after func(), err error)                  // erneut offline, Ticket noch offen
	Recovered(ctx context.Context, tx pgx.Tx, a AlarmAsset, at time.Time) (after func(), err error) // Eintrag „wieder erreichbar“
}

// Watch prüft überwachte Netz-Assets auf Ausfälle.
//
// Je Asset gibt es zwei Zustände: normal und alarm. Ein Asset, das länger als
// die Karenzzeit offline ist, löst den Alarm aus und legt ein Ticket an (oder
// ergänzt das noch offene Ticket des letzten Alarms). Kommt es zurück, geht
// der Zustand auf normal; die Ticket-ID bleibt gemerkt. Unbekannter Zustand
// ändert nichts.
type Watch struct {
	DB    *pgxpool.Pool
	Sink  AlarmSink
	Grace func(ctx context.Context) time.Duration
	// Source beschränkt den Durchlauf auf Assets dieser Quelle (net_source).
	// Leer: alle Quellen (Betrieb); dann werden auch gelöste Assets (ohne
	// Quelle) mit laufendem Alarm still zurückgesetzt. Gedacht für Tests mit
	// eigener Quelle gegen eine geteilte Datenbank.
	Source string
}

func (w *Watch) grace(ctx context.Context) time.Duration {
	if w.Grace != nil {
		if g := w.Grace(ctx); g > 0 {
			return g
		}
	}
	return defaultGrace
}

// Pass prüft alle überwachten Assets sowie alle mit laufendem Alarm. Fehler
// und Abstürze eines Assets werden protokolliert, die übrigen laufen weiter.
func (w *Watch) Pass(ctx context.Context, now time.Time) error {
	grace := w.grace(ctx)
	rows, err := w.DB.Query(ctx, `SELECT id::text FROM it_assets
		 WHERE net_source IS NOT NULL AND net_monitored AND NOT net_detached AND status = 'active'
		   AND ($1 = '' OR net_source = $1)
		UNION
		SELECT s.asset_id::text FROM it_net_alarm_state s JOIN it_assets a ON a.id = s.asset_id
		 WHERE s.state = 'alarm' AND ($1 = '' OR a.net_source = $1)`, w.Source)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.safeOne(ctx, id, now, grace); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Str("asset", id).Msg("it: netzwächter")
		}
	}
	return nil
}

func (w *Watch) safeOne(ctx context.Context, id string, now time.Time, grace time.Duration) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("abgestürzt: %v", r)
		}
	}()
	return w.watchOne(ctx, id, now, grace)
}

// watchOne prüft ein Asset in einer Transaktion mit gesperrter Zustandszeile.
func (w *Watch) watchOne(ctx context.Context, id string, now time.Time, grace time.Duration) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Zustandszeile anlegen, falls sie fehlt – nur solange das Asset besteht.
	if _, err := tx.Exec(ctx, `INSERT INTO it_net_alarm_state (asset_id)
		SELECT id FROM it_assets WHERE id = $1::uuid ON CONFLICT (asset_id) DO NOTHING`, id); err != nil {
		return err
	}
	var state string
	var offlineSince *time.Time
	var ticket *string
	err = tx.QueryRow(ctx, `SELECT state, offline_since, ticket_id::text FROM it_net_alarm_state
		WHERE asset_id = $1::uuid FOR UPDATE`, id).Scan(&state, &offlineSince, &ticket)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // inzwischen gelöscht
	}
	if err != nil {
		return err
	}

	// Maßgeblich ist der in der Transaktion gelesene Stand des Assets.
	var a AlarmAsset
	var netState string
	var stateSince *time.Time
	var watched bool
	var ownGrace *int
	// Karenz: Wert am Asset, sonst der Kategorie, sonst (NULL) der allgemeine.
	err = tx.QueryRow(ctx, `SELECT a.id::text, a.name, COALESCE(a.ip_address, ''), a.net_uplink, COALESCE(a.infrastructure_id::text, ''),
		       a.net_last_seen, a.net_state, a.net_state_since,
		       (a.net_source IS NOT NULL AND a.net_monitored AND NOT a.net_detached AND a.status = 'active'),
		       COALESCE(a.net_grace_minutes, c.grace_minutes)
		  FROM it_assets a LEFT JOIN it_asset_categories c ON c.key = a.type
		 WHERE a.id = $1::uuid`, id).Scan(&a.ID, &a.Name, &a.IP, &a.Uplink, &a.InfrastructureID,
		&a.LastSeen, &netState, &stateSince, &watched, &ownGrace)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if ticket != nil {
		a.TicketID = *ticket
	}
	if ownGrace != nil {
		grace = time.Duration(*ownGrace) * time.Minute
	}
	a.GraceMinutes = max(1, int(grace/time.Minute))

	var afters []func()
	add := func(f func(), err error) error {
		if err != nil {
			return err
		}
		if f != nil {
			afters = append(afters, f)
		}
		return nil
	}

	switch {
	case !watched:
		if state != "alarm" {
			return nil
		}
		// Nicht mehr überwacht: Alarm endet still, die Ticket-ID bleibt.
		if _, err := tx.Exec(ctx, `UPDATE it_net_alarm_state SET state = 'normal', offline_since = NULL WHERE asset_id = $1::uuid`, id); err != nil {
			return err
		}

	case netState == "offline" && state == "normal":
		if stateSince == nil || now.Sub(*stateSince) < grace {
			return nil
		}
		a.OfflineSince = *stateSince
		open := false
		if ticket != nil {
			if open, err = w.Sink.TicketOpen(ctx, tx, *ticket); err != nil {
				return err
			}
		}
		newTicket := ticket
		if open {
			if err := add(w.Sink.Repeat(ctx, tx, a)); err != nil {
				return err
			}
		} else {
			tid, after, err := w.Sink.OpenTicket(ctx, tx, a)
			if err != nil {
				return err
			}
			newTicket = &tid
			if err := add(after, nil); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE it_net_alarm_state SET state = 'alarm', offline_since = $2, ticket_id = $3::uuid
			WHERE asset_id = $1::uuid`, id, *stateSince, newTicket); err != nil {
			return err
		}

	case netState == "online" && state == "alarm":
		if offlineSince != nil {
			a.OfflineSince = *offlineSince
		}
		at := now
		if stateSince != nil {
			at = *stateSince
		}
		if err := add(w.Sink.Recovered(ctx, tx, a, at)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE it_net_alarm_state SET state = 'normal', offline_since = NULL WHERE asset_id = $1::uuid`, id); err != nil {
			return err
		}

	default:
		return nil
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, f := range afters {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Msg("it: nachlauf nach netzalarm abgestürzt")
				}
			}()
			f()
		}()
	}
	return nil
}
