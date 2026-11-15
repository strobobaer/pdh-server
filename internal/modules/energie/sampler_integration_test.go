package energie

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var m0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

// samplerEnv räumt Reste früherer Läufe weg, damit Zählungen stimmen.
func samplerEnv(t *testing.T) (*pgxpool.Pool, *Store, *Sampler, string) {
	t.Helper()
	pool := testPool(t)
	_, err := pool.Exec(context.Background(), `DELETE FROM energy_meters WHERE infrastructure_id IN
		(SELECT id FROM infrastructure WHERE name LIKE 'EnergieTest-%')`)
	must(t, err)
	s := NewStore(pool)
	return pool, s, &Sampler{Store: s}, newInfra(t, pool, "")
}

// source legt eine Messstelle mit Quelle an; raw und ago bestimmen die letzte Lesung.
func source(t *testing.T, pool *pgxpool.Pool, s *Store, infra, kind string, factor float64, raw string, at time.Time) (meterID, mapID string) {
	t.Helper()
	_, mapID = newMapping(t, pool, infra)
	setReading(t, pool, mapID, raw, at)
	m := &Meter{Key: uniq("R"), InfraID: infra, Kind: kind, SourceID: mapID, Factor: factor}
	save(t, s, m)
	return m.ID, mapID
}

func setReading(t *testing.T, pool *pgxpool.Pool, mapID, raw string, at time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE import_mappings SET last_value = $2, last_received_at = $3 WHERE id = $1::uuid`, mapID, raw, at)
	must(t, err)
}

func virtual(t *testing.T, s *Store, infra, kind, formula string) (id, key string) {
	t.Helper()
	m := &Meter{Key: uniq("V"), InfraID: infra, Kind: kind, Formula: formula}
	save(t, s, m)
	return m.ID, m.Key
}

func keyOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	m, err := s.Get(context.Background(), id)
	must(t, err)
	return m.Key
}

func valueAt(t *testing.T, pool *pgxpool.Pool, id string, ts time.Time) (float64, bool) {
	t.Helper()
	var v float64
	err := pool.QueryRow(context.Background(), `SELECT value FROM energy_values WHERE meter_id = $1::uuid AND ts = $2`, id, ts).Scan(&v)
	return v, err == nil
}

func countValues(t *testing.T, pool *pgxpool.Pool, id string) int {
	t.Helper()
	var n int
	must(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_values WHERE meter_id = $1::uuid`, id).Scan(&n))
	return n
}

func wantValue(t *testing.T, pool *pgxpool.Pool, id string, ts time.Time, want float64) {
	t.Helper()
	got, ok := valueAt(t, pool, id, ts)
	if !ok || math.Abs(got-want) > 1e-9 {
		t.Errorf("Wert %s: %v (vorhanden %v), erwartet %v", ts.Format("15:04"), got, ok, want)
	}
}

func TestSamplerRealValues(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	c, _ := source(t, pool, s, infra, KindCounter, 0.001, "12345", m0.Add(-30*time.Second))
	i, _ := source(t, pool, s, infra, KindInstant, 1, "7,5", m0.Add(-14*time.Minute))
	stale, _ := source(t, pool, s, infra, KindInstant, 1, "5", m0.Add(-16*time.Minute))
	bad, _ := source(t, pool, s, infra, KindInstant, 1, "n/a", m0.Add(-time.Minute))

	stored, missing, err := sm.Pass(ctx, m0.Add(20*time.Second)) // auf die Minute abgeschnitten
	must(t, err)
	if stored != 2 || missing != 0 {
		t.Errorf("stored=%d missing=%d", stored, missing)
	}
	wantValue(t, pool, c, m0, 12.345)
	wantValue(t, pool, i, m0, 7.5)
	for _, id := range []string{stale, bad} {
		if _, ok := valueAt(t, pool, id, m0); ok {
			t.Errorf("veraltet/unlesbar darf keinen Wert haben (%s)", id)
		}
	}
	got, err := s.Get(ctx, c)
	must(t, err)
	if got.LastValue == nil || math.Abs(*got.LastValue-12.345) > 1e-9 || got.LastAt == nil || !got.LastAt.Equal(m0) {
		t.Errorf("letzter Wert: %+v", got)
	}
}

func TestSamplerConsumptionCountsOnce(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	id, _ := source(t, pool, s, infra, KindConsumption, 1, "5", m0.Add(-10*time.Second))
	for k := 0; k < 3; k++ {
		_, _, err := sm.Pass(ctx, m0.Add(time.Duration(k)*time.Minute))
		must(t, err)
	}
	wantValue(t, pool, id, m0, 5)
	wantValue(t, pool, id, m0.Add(time.Minute), 0)
	wantValue(t, pool, id, m0.Add(2*time.Minute), 0)
}

// counterPair: zwei Zähler und eine berechnete Messstelle [G]-[A].
func counterPair(t *testing.T) (pool *pgxpool.Pool, sm *Sampler, gMap, aMap, g, a, v string) {
	pool, s, sm, infra := samplerEnv(t)
	g, gMap = source(t, pool, s, infra, KindCounter, 1, "1000", m0.Add(-5*time.Second))
	a, aMap = source(t, pool, s, infra, KindCounter, 1, "500", m0.Add(-5*time.Second))
	v, _ = virtual(t, s, infra, KindCounter, "["+keyOf(t, s, g)+"]-["+keyOf(t, s, a)+"]")
	return
}

func TestSamplerVirtualCounter(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	ctx := context.Background()
	m1 := m0.Add(time.Minute)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	setReading(t, pool, gMap, "1010", m1.Add(-5*time.Second))
	setReading(t, pool, aMap, "503", m1.Add(-5*time.Second))
	stored, missing, err := sm.Pass(ctx, m1)
	must(t, err)
	if stored != 3 || missing != 0 {
		t.Errorf("stored=%d missing=%d", stored, missing)
	}
	wantValue(t, pool, v, m0, 0)
	wantValue(t, pool, v, m1, 7)
}

func TestSamplerVirtualMissingRef(t *testing.T) {
	pool, sm, _, aMap, g, _, v := counterPair(t)
	setReading(t, pool, aMap, "500", m0.Add(-16*time.Minute)) // Quelle A veraltet
	stored, missing, err := sm.Pass(context.Background(), m0)
	must(t, err)
	if stored != 1 || missing != 1 {
		t.Errorf("stored=%d missing=%d", stored, missing)
	}
	wantValue(t, pool, g, m0, 1000)
	if _, ok := valueAt(t, pool, v, m0); ok {
		t.Error("berechnete Messstelle ohne Bezug darf keinen Wert (auch keine 0) haben")
	}
}

func TestSamplerCounterResetNoNegative(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	ctx := context.Background()
	m1 := m0.Add(time.Minute)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	setReading(t, pool, gMap, "5", m1.Add(-5*time.Second))
	setReading(t, pool, aMap, "501", m1.Add(-5*time.Second))
	_, missing, err := sm.Pass(ctx, m1)
	must(t, err)
	if missing != 1 {
		t.Errorf("missing=%d", missing)
	}
	if _, ok := valueAt(t, pool, v, m1); ok {
		t.Error("nach Zählerrücksetzung darf kein Wert entstehen")
	}
	wantValue(t, pool, v, m0, 0)
}

func TestSamplerTwoPassesSameMinute(t *testing.T) {
	pool, sm, gMap, aMap, g, _, v := counterPair(t)
	ctx := context.Background()
	m1 := m0.Add(time.Minute)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	setReading(t, pool, gMap, "1010", m1.Add(-5*time.Second))
	setReading(t, pool, aMap, "503", m1.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m1)
	must(t, err)
	setReading(t, pool, gMap, "1100", m1.Add(-4*time.Second)) // ändert den schon abgelegten Wert nicht
	stored, missing, err := sm.Pass(ctx, m1)
	must(t, err)
	if stored != 0 || missing != 0 {
		t.Errorf("zweiter Lauf: stored=%d missing=%d", stored, missing)
	}
	if n := countValues(t, pool, v); n != 2 {
		t.Errorf("Zeilen virtuell: %d", n)
	}
	wantValue(t, pool, v, m1, 7)
	wantValue(t, pool, g, m1, 1010)
}

func TestSamplerSkip(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	id, _ := source(t, pool, s, infra, KindInstant, 1, "3", m0)
	sm.Skip = func() bool { return true }
	stored, missing, err := sm.Pass(context.Background(), m0)
	must(t, err)
	if stored != 0 || missing != 0 || countValues(t, pool, id) != 0 {
		t.Errorf("Skip: stored=%d missing=%d", stored, missing)
	}
}

func TestSamplerVirtualNegativeFormulaKeepsBase(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	ctx := context.Background()
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	setReading(t, pool, gMap, "1003", m1.Add(-5*time.Second)) // G +3, A +5: Formelwert -2
	setReading(t, pool, aMap, "505", m1.Add(-5*time.Second))
	_, missing, err := sm.Pass(ctx, m1)
	must(t, err)
	if missing != 1 {
		t.Errorf("missing=%d", missing)
	}
	if _, ok := valueAt(t, pool, v, m1); ok {
		t.Error("negativer Formelwert darf keinen Wert ergeben")
	}
	setReading(t, pool, gMap, "1020", m2.Add(-5*time.Second)) // seit Basis m0: G +20, A +5
	setReading(t, pool, aMap, "505", m2.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m2)
	must(t, err)
	wantValue(t, pool, v, m2, 15)
}

func TestSamplerVirtualMissingRefCatchesUp(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	ctx := context.Background()
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	setReading(t, pool, gMap, "1010", m1.Add(-5*time.Second))
	setReading(t, pool, aMap, "503", m0.Add(-20*time.Minute)) // A fehlt in Minute 1
	_, missing, err := sm.Pass(ctx, m1)
	must(t, err)
	if missing != 1 {
		t.Errorf("missing=%d", missing)
	}
	setReading(t, pool, gMap, "1020", m2.Add(-5*time.Second))
	setReading(t, pool, aMap, "503", m2.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m2)
	must(t, err)
	// Zuwachs seit Minute 0: G +20, A +3, ohne Drift.
	wantValue(t, pool, v, m2, 17)
}

func TestSamplerVirtualConsumptionAndInstant(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	i1, _ := source(t, pool, s, infra, KindInstant, 1, "4", m0.Add(-5*time.Second))
	i2, _ := source(t, pool, s, infra, KindInstant, 1, "6", m0.Add(-5*time.Second))
	c1, _ := source(t, pool, s, infra, KindConsumption, 1, "5", m0.Add(-5*time.Second))
	c2, _ := source(t, pool, s, infra, KindConsumption, 1, "2", m0.Add(-5*time.Second))
	vi, _ := virtual(t, s, infra, KindInstant, "["+keyOf(t, s, i1)+"]+["+keyOf(t, s, i2)+"]")
	vc, _ := virtual(t, s, infra, KindConsumption, "["+keyOf(t, s, c1)+"]+["+keyOf(t, s, c2)+"]")
	m1 := m0.Add(time.Minute)
	for _, m := range []time.Time{m0, m1} {
		_, _, err := sm.Pass(ctx, m)
		must(t, err)
	}
	wantValue(t, pool, vi, m0, 10)
	wantValue(t, pool, vi, m1, 10)
	wantValue(t, pool, vc, m0, 7)
	wantValue(t, pool, vc, m1, 0)
}

func TestSamplerEvalOrderErrorStillStoresRealValues(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	r, _ := source(t, pool, s, infra, KindInstant, 1, "3", m0.Add(-5*time.Second))
	_, ka := virtual(t, s, infra, KindInstant, "["+keyOf(t, s, r)+"]")
	_, kb := virtual(t, s, infra, KindInstant, "["+ka+"]")
	// Zirkel an Save vorbei in die Datenbank schreiben.
	_, err := pool.Exec(ctx, `UPDATE energy_meters SET formula = $2 WHERE meter_key = $1`, ka, "["+kb+"]")
	must(t, err)
	stored, _, err := sm.Pass(ctx, m0)
	var cyc *CycleError
	if !errors.As(err, &cyc) {
		t.Fatalf("erwartet CycleError, erhalten %v", err)
	}
	if stored != 1 {
		t.Errorf("stored=%d", stored)
	}
	wantValue(t, pool, r, m0, 3)
}

func TestSamplerCounterSwapRecovers(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	ctx := context.Background()
	m1, m2, m3 := m0.Add(time.Minute), m0.Add(2*time.Minute), m0.Add(3*time.Minute)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	setReading(t, pool, gMap, "1010", m1.Add(-5*time.Second))
	setReading(t, pool, aMap, "503", m1.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m1)
	must(t, err)
	wantValue(t, pool, v, m1, 7)
	// Zählertausch an G: neuer Zähler beginnt bei 5.
	setReading(t, pool, gMap, "5", m2.Add(-5*time.Second))
	setReading(t, pool, aMap, "504", m2.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m2)
	must(t, err)
	if _, ok := valueAt(t, pool, v, m2); ok {
		t.Error("erste Minute nach dem Tausch darf keinen Wert haben")
	}
	setReading(t, pool, gMap, "15", m3.Add(-5*time.Second)) // G +10, A +1 seit Minute 2
	setReading(t, pool, aMap, "505", m3.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m3)
	must(t, err)
	wantValue(t, pool, v, m3, 7+9)
}

func TestSamplerNewCounterRefRecoversAfterOneMinute(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	g, _ := source(t, pool, s, infra, KindCounter, 1, "1000", m0.Add(-5*time.Second))
	gk := keyOf(t, s, g)
	v := &Meter{Key: uniq("V"), InfraID: infra, Kind: KindCounter, Formula: "[" + gk + "]"}
	save(t, s, v)
	_, _, err := sm.Pass(ctx, m0)
	must(t, err)
	wantValue(t, pool, v.ID, m0, 0)

	// Zähler A kommt neu in die Formel; zur Basis (Minute 0) hat er keinen Wert.
	a, aMap := source(t, pool, s, infra, KindCounter, 1, "500", m1.Add(-5*time.Second))
	v.Formula = "[" + gk + "]-[" + keyOf(t, s, a) + "]"
	save(t, s, v)
	setReading(t, pool, gMap(t, pool, g), "1010", m1.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m1)
	must(t, err)
	if _, ok := valueAt(t, pool, v.ID, m1); ok {
		t.Error("Minute der Aufnahme darf keinen Wert haben")
	}
	setReading(t, pool, gMap(t, pool, g), "1020", m2.Add(-5*time.Second))
	setReading(t, pool, aMap, "503", m2.Add(-5*time.Second))
	_, _, err = sm.Pass(ctx, m2)
	must(t, err)
	wantValue(t, pool, v.ID, m2, 10-3) // G +10, A +3 seit Minute 1
}

// gMap liefert die Zuordnung einer Messstelle.
func gMap(t *testing.T, pool *pgxpool.Pool, meterID string) string {
	t.Helper()
	var id string
	must(t, pool.QueryRow(context.Background(), `SELECT import_mapping_id::text FROM energy_meters WHERE id = $1::uuid`, meterID).Scan(&id))
	return id
}
