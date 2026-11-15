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

// run legt die Minute minute ab: der Lauf kommt wie im Betrieb 5 s nach ihrem Ende.
func run(t *testing.T, sm *Sampler, minute time.Time) (stored, missing int) {
	t.Helper()
	stored, missing, err := sm.Pass(context.Background(), minute.Add(time.Minute+samplerOffset))
	must(t, err)
	return stored, missing
}

// source legt eine Messstelle mit Quelle an; raw und at bestimmen die letzte Lesung.
func source(t *testing.T, pool *pgxpool.Pool, s *Store, infra, kind string, factor float64, raw string, at time.Time) (meterID, mapID string) {
	t.Helper()
	_, mapID = newMapping(t, pool, infra)
	setReading(t, pool, mapID, raw, at)
	m := &Meter{Key: uniq("R"), InfraID: infra, Kind: kind, SourceID: mapID, Factor: factor}
	save(t, s, m)
	return m.ID, mapID
}

// consumer legt eine Verbrauchs-Messstelle an, deren Quelle noch keine Lesung hat.
func consumer(t *testing.T, pool *pgxpool.Pool, s *Store, infra string) (meterID, mapID string) {
	t.Helper()
	_, mapID = newMapping(t, pool, infra)
	m := &Meter{Key: uniq("C"), InfraID: infra, Kind: KindConsumption, SourceID: mapID, Factor: 1}
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

func wantNoValue(t *testing.T, pool *pgxpool.Pool, id string, ts time.Time) {
	t.Helper()
	if v, ok := valueAt(t, pool, id, ts); ok {
		t.Errorf("Wert %s: %v, erwartet kein Wert (auch keine 0)", ts.Format("15:04"), v)
	}
}

func TestNextTick(t *testing.T) {
	for _, c := range []struct{ now, want time.Time }{
		{m0, m0.Add(5 * time.Second)},
		{m0.Add(4 * time.Second), m0.Add(5 * time.Second)},
		{m0.Add(5 * time.Second), m0.Add(time.Minute + 5*time.Second)},
		{m0.Add(59 * time.Second), m0.Add(time.Minute + 5*time.Second)},
	} {
		if got := nextTick(c.now); !got.Equal(c.want) {
			t.Errorf("nextTick(%s) = %s, erwartet %s", c.now.Format("15:04:05"), got.Format("15:04:05"), c.want.Format("15:04:05"))
		}
	}
}

func TestSamplerRealValues(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	end := m0.Add(time.Minute)
	c, _ := source(t, pool, s, infra, KindCounter, 0.001, "12345", m0.Add(30*time.Second))
	i, _ := source(t, pool, s, infra, KindInstant, 1, "7,5", end.Add(-15*time.Minute)) // genau an der Frist
	stale, _ := source(t, pool, s, infra, KindInstant, 1, "5", end.Add(-15*time.Minute-time.Second))
	bad, _ := source(t, pool, s, infra, KindInstant, 1, "n/a", m0.Add(-time.Minute))

	stored, missing := run(t, sm, m0)
	if stored != 2 || missing != 0 {
		t.Errorf("stored=%d missing=%d", stored, missing)
	}
	wantValue(t, pool, c, m0, 12.345)
	wantValue(t, pool, i, m0, 7.5)
	wantNoValue(t, pool, stale, m0)
	wantNoValue(t, pool, bad, m0)
	got, err := s.Get(ctx, c)
	must(t, err)
	if got.LastValue == nil || math.Abs(*got.LastValue-12.345) > 1e-9 || got.LastAt == nil || !got.LastAt.Equal(m0) {
		t.Errorf("letzter Wert: %+v", got)
	}
}

// Eine Lesung gehört zu der Minute, in der sie eintraf – gleich, in welcher
// Sekunde sie kam und wann der Lauf läuft.
func TestSamplerConsumptionSecondInMinute(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	secs := []time.Duration{0, time.Second, 30 * time.Second, 59*time.Second + 999*time.Millisecond}
	ids := make([]string, len(secs))
	for k, sec := range secs {
		var mapID string
		ids[k], mapID = consumer(t, pool, s, infra)
		setReading(t, pool, mapID, "5", m0.Add(sec))
	}
	run(t, sm, m0)
	run(t, sm, m0.Add(time.Minute))
	for k := range secs {
		wantValue(t, pool, ids[k], m0, 5)
		wantNoValue(t, pool, ids[k], m0.Add(time.Minute))
	}
}

// Die Quelle liefert jede Minute kurz vor dem Lauf (bei :02, Lauf bei :05):
// jede Lesung zählt genau einmal in ihrer Minute, keine wird zur 0.
func TestSamplerConsumptionReadingJustBeforeRun(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	id, mapID := consumer(t, pool, s, infra)
	for k := 0; k <= 5; k++ {
		mk := m0.Add(time.Duration(k) * time.Minute)
		if k < 5 {
			setReading(t, pool, mapID, "5", mk.Add(2*time.Second))
		}
		_, _, err := sm.Pass(ctx, mk.Add(samplerOffset)) // legt Minute k−1 ab
		must(t, err)
	}
	for k := 0; k < 5; k++ {
		wantValue(t, pool, id, m0.Add(time.Duration(k)*time.Minute), 5)
	}
	if n := countValues(t, pool, id); n != 5 {
		t.Errorf("%d Minutenwerte, erwartet 5", n)
	}
}

// Eine Lesung genau an der Minutengrenze, also im Lauf der Folgeminute, zählt
// in der Folgeminute – nicht doppelt und nicht gar nicht.
func TestSamplerConsumptionAtMinuteBoundary(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	a, aMap := consumer(t, pool, s, infra)
	b, bMap := consumer(t, pool, s, infra)
	setReading(t, pool, aMap, "5", m1)                  // genau auf der Grenze
	setReading(t, pool, bMap, "7", m1.Add(time.Second)) // während des Laufs für Minute 0
	run(t, sm, m0)
	wantNoValue(t, pool, a, m0)
	wantNoValue(t, pool, b, m0)
	run(t, sm, m1)
	_, _, err := sm.Pass(ctx, m1.Add(time.Minute+samplerOffset)) // derselbe Lauf noch einmal
	must(t, err)
	run(t, sm, m2)
	wantValue(t, pool, a, m1, 5)
	wantValue(t, pool, b, m1, 7)
	wantNoValue(t, pool, a, m2)
	if n := countValues(t, pool, a) + countValues(t, pool, b); n != 2 {
		t.Errorf("%d Minutenwerte, erwartet 2", n)
	}
}

func TestSamplerMultipleReadingsPerMinute(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	m1 := m0.Add(time.Minute)
	c, cMap := consumer(t, pool, s, infra)
	for k, v := range []string{"2", "3", "4"} {
		setReading(t, pool, cMap, v, m0.Add(time.Duration(10+20*k)*time.Second))
	}
	_, zMap := newMapping(t, pool, infra)
	z := &Meter{Key: uniq("Z"), InfraID: infra, Kind: KindCounter, SourceID: zMap}
	save(t, s, z)
	setReading(t, pool, zMap, "100", m0.Add(10*time.Second))
	setReading(t, pool, zMap, "105", m0.Add(40*time.Second))
	setReading(t, pool, zMap, "110", m1.Add(2*time.Second)) // kommt vor dem Lauf für Minute 0
	run(t, sm, m0)
	run(t, sm, m1)
	wantValue(t, pool, c, m0, 9)
	wantNoValue(t, pool, c, m1)
	wantValue(t, pool, z.ID, m0, 105) // letzte Lesung der Minute, nicht die spätere
	wantValue(t, pool, z.ID, m1, 110)
}

// Zwei Verbrauchs-Messstellen an derselben Quelle bekommen beide den Wert.
func TestSamplerConsumptionSharedSource(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	a, mapID := consumer(t, pool, s, infra)
	b := &Meter{Key: uniq("C"), InfraID: infra, Kind: KindConsumption, SourceID: mapID, Factor: 2}
	save(t, s, b)
	setReading(t, pool, mapID, "3", m0.Add(20*time.Second))
	run(t, sm, m0)
	wantValue(t, pool, a, m0, 3)
	wantValue(t, pool, b.ID, m0, 6)
}

// Spät festgeschriebene Lesung (Eingang in Minute 0, sichtbar erst nach deren
// Lauf) geht nicht verloren: sie zählt in der nächsten abgelegten Minute.
func TestSamplerConsumptionLateReading(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	m1 := m0.Add(time.Minute)
	id, mapID := consumer(t, pool, s, infra)
	setReading(t, pool, mapID, "5", m0.Add(10*time.Second))
	run(t, sm, m0)
	setReading(t, pool, mapID, "3", m0.Add(50*time.Second)) // spät
	setReading(t, pool, mapID, "1", m1.Add(10*time.Second))
	run(t, sm, m1)
	wantValue(t, pool, id, m0, 5)
	wantValue(t, pool, id, m1, 4)
}

// Verpasste Minuten holt der nächste Lauf nach, jede Lesung in ihrer Minute.
func TestSamplerCatchUp(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	id, mapID := consumer(t, pool, s, infra)
	setReading(t, pool, mapID, "1", m0.Add(10*time.Second))
	run(t, sm, m0)
	setReading(t, pool, mapID, "2", m0.Add(time.Minute+10*time.Second))
	setReading(t, pool, mapID, "3", m0.Add(2*time.Minute+10*time.Second))
	run(t, sm, m0.Add(3*time.Minute)) // Läufe für Minute 1 und 2 fielen aus
	wantValue(t, pool, id, m0, 1)
	wantValue(t, pool, id, m0.Add(time.Minute), 2)
	wantValue(t, pool, id, m0.Add(2*time.Minute), 3)
	wantNoValue(t, pool, id, m0.Add(3*time.Minute))
}

// Nicht mehr gebrauchte Lesungen werden gelöscht, die letzte vor dem Fenster bleibt.
func TestSamplerPurgesReadings(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	ctx := context.Background()
	_, mapID := newMapping(t, pool, infra)
	save(t, s, &Meter{Key: uniq("Z"), InfraID: infra, Kind: KindCounter, SourceID: mapID})
	old := m0.Add(-time.Hour)
	for k := 0; k < 3; k++ {
		setReading(t, pool, mapID, "1", old.Add(time.Duration(k)*time.Minute))
	}
	setReading(t, pool, mapID, "2", m0.Add(10*time.Second))
	run(t, sm, m0)
	var n int
	var lastOld time.Time
	must(t, pool.QueryRow(ctx, `SELECT count(*), max(received_at) FILTER (WHERE received_at < $2)
		FROM energy_readings WHERE mapping_id = $1::uuid`, mapID, m0).Scan(&n, &lastOld))
	if n != 2 || !lastOld.Equal(old.Add(2*time.Minute)) {
		t.Errorf("%d Lesungen, letzte alte %v", n, lastOld)
	}
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
	m1 := m0.Add(time.Minute)
	run(t, sm, m0)
	setReading(t, pool, gMap, "1010", m1.Add(30*time.Second))
	setReading(t, pool, aMap, "503", m1.Add(30*time.Second))
	stored, missing := run(t, sm, m1)
	if stored != 3 || missing != 0 {
		t.Errorf("stored=%d missing=%d", stored, missing)
	}
	wantValue(t, pool, v, m0, 0)
	wantValue(t, pool, v, m1, 7)
}

func TestSamplerVirtualMissingRef(t *testing.T) {
	pool, sm, _, aMap, g, _, v := counterPair(t)
	setReading(t, pool, aMap, "500", m0.Add(-16*time.Minute)) // Quelle A veraltet
	stored, missing := run(t, sm, m0)
	if stored != 1 || missing != 1 {
		t.Errorf("stored=%d missing=%d", stored, missing)
	}
	wantValue(t, pool, g, m0, 1000)
	wantNoValue(t, pool, v, m0)
}

func TestSamplerCounterResetNoNegative(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	m1 := m0.Add(time.Minute)
	run(t, sm, m0)
	setReading(t, pool, gMap, "5", m1.Add(30*time.Second))
	setReading(t, pool, aMap, "501", m1.Add(30*time.Second))
	if _, missing := run(t, sm, m1); missing != 1 {
		t.Errorf("missing=%d", missing)
	}
	wantNoValue(t, pool, v, m1)
	wantValue(t, pool, v, m0, 0)
}

func TestSamplerTwoPassesSameMinute(t *testing.T) {
	pool, sm, gMap, aMap, g, _, v := counterPair(t)
	m1 := m0.Add(time.Minute)
	run(t, sm, m0)
	setReading(t, pool, gMap, "1010", m1.Add(30*time.Second))
	setReading(t, pool, aMap, "503", m1.Add(30*time.Second))
	run(t, sm, m1)
	setReading(t, pool, gMap, "1100", m1.Add(40*time.Second)) // ändert den schon abgelegten Wert nicht
	if stored, missing := run(t, sm, m1); stored != 0 || missing != 0 {
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
	stored, missing, err := sm.Pass(context.Background(), m0.Add(time.Minute+samplerOffset))
	must(t, err)
	if stored != 0 || missing != 0 || countValues(t, pool, id) != 0 {
		t.Errorf("Skip: stored=%d missing=%d", stored, missing)
	}
}

func TestSamplerVirtualNegativeFormulaKeepsBase(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	run(t, sm, m0)
	setReading(t, pool, gMap, "1003", m1.Add(30*time.Second)) // G +3, A +5: Formelwert -2
	setReading(t, pool, aMap, "505", m1.Add(30*time.Second))
	if _, missing := run(t, sm, m1); missing != 1 {
		t.Errorf("missing=%d", missing)
	}
	wantNoValue(t, pool, v, m1)
	setReading(t, pool, gMap, "1020", m2.Add(30*time.Second)) // seit Basis m0: G +20, A +5
	setReading(t, pool, aMap, "505", m2.Add(30*time.Second))
	run(t, sm, m2)
	wantValue(t, pool, v, m2, 15)
}

func TestSamplerVirtualMissingRefCatchesUp(t *testing.T) {
	pool, sm, gMap, aMap, _, _, v := counterPair(t)
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	run(t, sm, m0)
	setReading(t, pool, gMap, "1010", m1.Add(30*time.Second))
	setReading(t, pool, aMap, "503", m0.Add(-20*time.Minute)) // A fehlt in Minute 1
	if _, missing := run(t, sm, m1); missing != 1 {
		t.Errorf("missing=%d", missing)
	}
	setReading(t, pool, gMap, "1020", m2.Add(30*time.Second))
	setReading(t, pool, aMap, "503", m2.Add(30*time.Second))
	run(t, sm, m2)
	// Zuwachs seit Minute 0: G +20, A +3, ohne Drift.
	wantValue(t, pool, v, m2, 17)
}

func TestSamplerVirtualConsumptionAndInstant(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	i1, _ := source(t, pool, s, infra, KindInstant, 1, "4", m0.Add(20*time.Second))
	i2, _ := source(t, pool, s, infra, KindInstant, 1, "6", m0.Add(20*time.Second))
	c1, c1Map := consumer(t, pool, s, infra)
	c2, c2Map := consumer(t, pool, s, infra)
	setReading(t, pool, c1Map, "5", m0.Add(20*time.Second))
	setReading(t, pool, c2Map, "2", m0.Add(20*time.Second))
	vi, _ := virtual(t, s, infra, KindInstant, "["+keyOf(t, s, i1)+"]+["+keyOf(t, s, i2)+"]")
	vc, _ := virtual(t, s, infra, KindConsumption, "["+keyOf(t, s, c1)+"]+["+keyOf(t, s, c2)+"]")
	m1 := m0.Add(time.Minute)
	run(t, sm, m0)
	run(t, sm, m1)
	wantValue(t, pool, vi, m0, 10)
	wantValue(t, pool, vi, m1, 10)
	wantValue(t, pool, vc, m0, 7)
	wantNoValue(t, pool, vc, m1) // ohne neue Lesung kein Verbrauch – und keine 0
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
	stored, _, err := sm.Pass(ctx, m0.Add(time.Minute+samplerOffset))
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
	m1, m2, m3 := m0.Add(time.Minute), m0.Add(2*time.Minute), m0.Add(3*time.Minute)
	run(t, sm, m0)
	setReading(t, pool, gMap, "1010", m1.Add(30*time.Second))
	setReading(t, pool, aMap, "503", m1.Add(30*time.Second))
	run(t, sm, m1)
	wantValue(t, pool, v, m1, 7)
	// Zählertausch an G: neuer Zähler beginnt bei 5.
	setReading(t, pool, gMap, "5", m2.Add(30*time.Second))
	setReading(t, pool, aMap, "504", m2.Add(30*time.Second))
	run(t, sm, m2)
	wantNoValue(t, pool, v, m2)
	setReading(t, pool, gMap, "15", m3.Add(30*time.Second)) // G +10, A +1 seit Minute 2
	setReading(t, pool, aMap, "505", m3.Add(30*time.Second))
	run(t, sm, m3)
	wantValue(t, pool, v, m3, 7+9)
}

func TestSamplerNewCounterRefRecoversAfterOneMinute(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	g, _ := source(t, pool, s, infra, KindCounter, 1, "1000", m0.Add(-5*time.Second))
	gk := keyOf(t, s, g)
	v := &Meter{Key: uniq("V"), InfraID: infra, Kind: KindCounter, Formula: "[" + gk + "]"}
	save(t, s, v)
	run(t, sm, m0)
	wantValue(t, pool, v.ID, m0, 0)

	// Zähler A kommt neu in die Formel; zur Basis (Minute 0) hat er keinen Wert.
	s.Now = func() time.Time { return m1.Add(20 * time.Second) }
	a, aMap := source(t, pool, s, infra, KindCounter, 1, "500", m1.Add(10*time.Second))
	v.Formula = "[" + gk + "]-[" + keyOf(t, s, a) + "]"
	save(t, s, v)
	setReading(t, pool, gMap(t, pool, g), "1010", m1.Add(30*time.Second))
	run(t, sm, m1)
	wantNoValue(t, pool, v.ID, m1)
	setReading(t, pool, gMap(t, pool, g), "1020", m2.Add(30*time.Second))
	setReading(t, pool, aMap, "503", m2.Add(30*time.Second))
	run(t, sm, m2)
	wantValue(t, pool, v.ID, m2, 10-3) // G +10, A +3 seit Minute 1
}

// Neue Quelle einer Zählermessstelle: die berechneten Messstellen, die sie
// direkt oder indirekt nutzen, buchen den Sprung nicht als Verbrauch.
func TestSamplerSourceChangeRebases(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	m1, m2, m3 := m0.Add(time.Minute), m0.Add(2*time.Minute), m0.Add(3*time.Minute)
	gID, _ := source(t, pool, s, infra, KindCounter, 1, "1000", m0.Add(10*time.Second))
	vID, vKey := virtual(t, s, infra, KindCounter, "["+keyOf(t, s, gID)+"]")
	wID, _ := virtual(t, s, infra, KindCounter, "["+vKey+"]*1")
	run(t, sm, m0)

	// Während Minute 1 bekommt G eine andere Quelle mit viel größerem Stand.
	s.Now = func() time.Time { return m1.Add(30 * time.Second) }
	_, newMap := newMapping(t, pool, infra)
	g, err := s.Get(context.Background(), gID)
	must(t, err)
	g.SourceID = newMap
	save(t, s, g)
	setReading(t, pool, newMap, "50000", m1.Add(40*time.Second))
	run(t, sm, m1)
	wantValue(t, pool, gID, m1, 50000)
	wantNoValue(t, pool, vID, m1)
	wantNoValue(t, pool, wID, m1)

	setReading(t, pool, newMap, "50010", m2.Add(30*time.Second))
	run(t, sm, m2)
	wantValue(t, pool, vID, m2, 10)
	setReading(t, pool, newMap, "50020", m3.Add(30*time.Second))
	run(t, sm, m3)
	wantValue(t, pool, vID, m3, 20)
	if w, ok := valueAt(t, pool, wID, m3); !ok || w > 20 {
		t.Errorf("indirekt: W %v (vorhanden %v), erwartet höchstens 20 ohne Sprung", w, ok)
	}
}

// Neuer Faktor einer Zählermessstelle (Wh → kWh o. ä.): kein Sprung im berechneten Zähler.
func TestSamplerFactorChangeRebases(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	gID, gm := source(t, pool, s, infra, KindCounter, 1, "1000", m0.Add(10*time.Second))
	vID, _ := virtual(t, s, infra, KindCounter, "["+keyOf(t, s, gID)+"]")
	run(t, sm, m0)
	s.Now = func() time.Time { return m1.Add(30 * time.Second) }
	g, err := s.Get(context.Background(), gID)
	must(t, err)
	g.Factor = 10
	save(t, s, g)
	setReading(t, pool, gm, "1010", m1.Add(40*time.Second))
	run(t, sm, m1)
	wantNoValue(t, pool, vID, m1)
	setReading(t, pool, gm, "1011", m2.Add(30*time.Second))
	run(t, sm, m2)
	wantValue(t, pool, vID, m2, 10)
}

// Geänderte Formel: die Minute der Änderung hat keinen Wert, danach rechnet
// die Messstelle mit der neuen Formel von ihrem letzten Stand weiter.
func TestSamplerFormulaChangeRebases(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	m1, m2 := m0.Add(time.Minute), m0.Add(2*time.Minute)
	gID, gm := source(t, pool, s, infra, KindCounter, 1, "1000", m0.Add(10*time.Second))
	gk := keyOf(t, s, gID)
	v := &Meter{Key: uniq("V"), InfraID: infra, Kind: KindCounter, Formula: "[" + gk + "]"}
	save(t, s, v)
	run(t, sm, m0)
	s.Now = func() time.Time { return m1.Add(30 * time.Second) }
	v.Formula = "[" + gk + "]*2"
	save(t, s, v)
	setReading(t, pool, gm, "1010", m1.Add(40*time.Second))
	run(t, sm, m1)
	wantNoValue(t, pool, v.ID, m1)
	setReading(t, pool, gm, "1015", m2.Add(30*time.Second))
	run(t, sm, m2)
	wantValue(t, pool, v.ID, m2, 10) // 0 + (1015 − 1010) × 2
}

// gMap liefert die Zuordnung einer Messstelle.
func gMap(t *testing.T, pool *pgxpool.Pool, meterID string) string {
	t.Helper()
	var id string
	must(t, pool.QueryRow(context.Background(), `SELECT import_mapping_id::text FROM energy_meters WHERE id = $1::uuid`, meterID).Scan(&id))
	return id
}
