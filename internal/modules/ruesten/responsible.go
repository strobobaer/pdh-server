package ruesten

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// shiftLeaderSQL sucht den Schichtführer, der zum Zeitpunkt $1 (Datum) / $2
// (Uhrzeit) Dienst hat. Eine Nachtschicht über Mitternacht (Ende vor Beginn)
// gehört zu dem Tag, an dem sie beginnt: nach Mitternacht zählt also die
// Zuweisung vom Vortag. Zuweisungen mit Plan gelten nur bei veröffentlichtem
// Plan, Zuweisungen ohne Plan (Altbestand) immer – wie im Schichtplan.
const shiftLeaderSQL = `
SELECT u.id::text
  FROM shift_assignments sa
  JOIN shift_definitions sd ON sd.id = sa.shift_id
  JOIN users u ON u.id = sa.user_id
  LEFT JOIN shift_plans sp ON sp.id = sa.plan_id
 WHERE u.shift_leader AND u.active
   AND (sa.plan_id IS NULL OR sp.status = 'published')
   AND (
        (sa.date = $1::text::date AND (
            (sd.start_time < sd.end_time AND $2::text::time >= sd.start_time AND $2::text::time < sd.end_time)
         OR (sd.start_time >= sd.end_time AND $2::text::time >= sd.start_time)))
     OR (sa.date = $1::text::date - 1 AND sd.start_time >= sd.end_time AND $2::text::time < sd.end_time)
   )
 ORDER BY u.last_name, u.id
 LIMIT 1`

// Responsibles liefert den Anlagenverantwortlichen (vererbt über die
// Anlagenhierarchie, Sicht infrastructure_responsible) und den Schichtführer,
// der zum Zeitpunkt at Dienst hat. Fehlt eine Person, ist der Wert nil.
func (s *Store) Responsibles(ctx context.Context, infraID string, at time.Time) (responsible, shiftLeader *string, err error) {
	var id string
	switch err = s.DB.QueryRow(ctx, `SELECT responsible_to::text FROM infrastructure_responsible WHERE infrastructure_id = $1`, infraID).Scan(&id); {
	case err == nil:
		responsible = &id
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, nil, err
	}
	var lid string
	switch err = s.DB.QueryRow(ctx, shiftLeaderSQL, at.Format("2006-01-02"), at.Format("15:04:05")).Scan(&lid); {
	case err == nil:
		shiftLeader = &lid
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, nil, err
	}
	return responsible, shiftLeader, nil
}
