package energie

import (
	"context"
	"testing"
	"time"
)

// TestSamplerSkipsPausedMeters: Messstellen einer laufenden Neurechnung legt
// der Minutentakt nicht ab; ist der Lauf fertig, rechnet er sie wieder.
func TestSamplerSkipsPausedMeters(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	r, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0.Add(30*time.Second))
	v, _ := virtual(t, s, infra, KindCounter, "["+keyOf(t, s, r)+"]")

	var runID string
	must(t, pool.QueryRow(ctx, `INSERT INTO energy_recalc_runs (meter_id, meter_ids, meter_keys, from_date, days_total, state)
		VALUES ($1::uuid, ARRAY[$1::uuid], ARRAY['V'], '2026-11-13', 3, 'running') RETURNING id::text`, v).Scan(&runID))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM energy_recalc_runs WHERE id = $1::uuid`, runID)
	})

	run(t, sm, m0)
	wantValue(t, pool, r, m0, 100)
	wantNoValue(t, pool, v, m0)

	_, err := pool.Exec(ctx, `UPDATE energy_recalc_runs SET state = 'done' WHERE id = $1::uuid`, runID)
	must(t, err)
	run(t, sm, m0.Add(time.Minute))
	if _, ok := valueAt(t, pool, v, m0.Add(2*time.Minute)); !ok {
		t.Error("nach dem Lauf legt der Minutentakt V nicht wieder ab")
	}
}
