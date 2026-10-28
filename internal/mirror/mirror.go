// Package mirror erkennt, ob das PDH auf einem PostgreSQL-Hot-Standby („Spiegel“)
// läuft, und überwacht den Modus (Spezifikation Ausfallschutz Teil 4, §2).
package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Mode ist der Betriebsmodus der Datenbank.
type Mode int

const (
	Normal   Mode = iota // beschreibbare Datenbank
	ReadOnly             // Spiegel: Standby oder schreibgeschützt
)

func (m Mode) String() string {
	if m == ReadOnly {
		return "spiegel"
	}
	return "normal"
}

// Prober ist, was von der Datenbank gebraucht wird (erfüllt *pgxpool.Pool).
type Prober interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Detect fragt den Modus ab: Standby oder schreibgeschützte Transaktion = ReadOnly.
// Bei Fehler wird Normal mit err geliefert – Aufrufer prüfen zuerst err.
func Detect(ctx context.Context, p Prober) (Mode, error) {
	var rec, ro bool
	err := p.QueryRow(ctx, `SELECT pg_is_in_recovery(), current_setting('transaction_read_only') = 'on'`).Scan(&rec, &ro)
	if err != nil {
		return Normal, err
	}
	if rec || ro {
		return ReadOnly, nil
	}
	return Normal, nil
}

// Watch prüft alle every den Modus und blockiert bis ctx endet oder onChange
// gerufen wurde (genau einmal, dann Rückkehr): bei abweichendem Modus oder wenn
// Detect länger als lost durchgehend fehlschlägt. lost <= 0 heißt: Datenbank-
// fehler beenden Watch nie, nur ein Moduswechsel. Jede Abfrage ist auf every
// begrenzt; eine hängende Abfrage zählt als Fehler. every <= 0 wird auf 1 s gesetzt.
func Watch(ctx context.Context, p Prober, start Mode, every, lost time.Duration, onChange func(reason string)) {
	if every <= 0 {
		every = time.Second
	}
	var failSince time.Time
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, every)
		m, err := Detect(pctx, p)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if failSince.IsZero() {
				failSince = time.Now()
			}
			if lost > 0 && time.Since(failSince) >= lost {
				onChange(fmt.Sprintf("Datenbank seit %s nicht erreichbar: %v", lost, err))
				return
			}
			continue
		}
		failSince = time.Time{}
		if m != start {
			onChange(fmt.Sprintf("Modus gewechselt: %s → %s", start, m))
			return
		}
	}
}

// ReplayLag liefert den Rückstand des Spiegels und den Zeitpunkt der zuletzt
// eingespielten Transaktion. Ohne eingespielte Transaktion (NULL): at.IsZero().
func ReplayLag(ctx context.Context, p Prober) (lag time.Duration, at time.Time, err error) {
	var iv pgtype.Interval
	var ts pgtype.Timestamptz
	err = p.QueryRow(ctx, `SELECT now() - pg_last_xact_replay_timestamp(), pg_last_xact_replay_timestamp()`).Scan(&iv, &ts)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !iv.Valid || !ts.Valid {
		return 0, time.Time{}, nil
	}
	lag = time.Duration(iv.Microseconds)*time.Microsecond +
		time.Duration(iv.Days)*24*time.Hour + time.Duration(iv.Months)*30*24*time.Hour
	return lag, ts.Time, nil
}

// currentSQL: Empfänger streamt, letzte Meldung des aktiven Servers frisch,
// alles Empfangene eingespielt. 60 s statt 30 s: bei Ruhe fragt der Spiegel
// erst nach wal_receiver_timeout/2 (30 s) nach – 30 s würde ständig kippen.
// Ohne pg_read_all_stats sind status/last_msg_receipt_time NULL; dann reicht
// ein laufender Empfänger (beendet sich nach wal_receiver_timeout ohne Kontakt).
// Kein Empfänger (keine Zeile) oder keine LSN → false.
const currentSQL = `SELECT COALESCE((
	SELECT CASE WHEN r.status IS NULL THEN true
		ELSE r.status = 'streaming' AND r.last_msg_receipt_time > now() - interval '60 seconds' END
	FROM pg_stat_wal_receiver r LIMIT 1
) AND pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn(), false)`

// Current: Spiegel ist auf dem Stand des aktiven Servers (nichts unterwegs).
// Bei ruhendem aktivem Server wächst ReplayLag, Current bleibt true.
func Current(ctx context.Context, p Prober) (bool, error) {
	var ok bool
	if err := p.QueryRow(ctx, currentSQL).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}
