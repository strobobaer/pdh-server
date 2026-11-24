package energie

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ymd(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

// versionEnv: Ortszeit UTC, „heute“ ist der 15.11.2026 (m0).
func versionEnv(t *testing.T) (*pgxpool.Pool, *Store, string) {
	t.Helper()
	pool, s, _, infra := samplerEnv(t)
	s.Loc = time.UTC
	s.Now = func() time.Time { return m0 }
	return pool, s, infra
}

func formulaOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var f string
	must(t, pool.QueryRow(context.Background(), `SELECT formula FROM energy_meters WHERE id = $1::uuid`, id).Scan(&f))
	return f
}

func versionsOf(t *testing.T, s *Store, id string) []Version {
	t.Helper()
	vs, err := s.Versions(context.Background(), id)
	must(t, err)
	return vs
}

func TestVersionPlannedTakesEffectAtMidnight(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	b, _ := source(t, pool, s, infra, KindCounter, 1, "50", m0)
	ak, bk := keyOf(t, s, a), keyOf(t, s, b)
	v, _ := virtual(t, s, infra, KindCounter, "["+ak+"]")
	_, err := pool.Exec(ctx, `UPDATE energy_meters SET base_at = $2 WHERE id = $1::uuid`, v, m0)
	must(t, err)

	res, err := s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]+[" + bk + "]", ValidFrom: ymd(2026, 11, 16)}, "")
	must(t, err)
	if res.Version.Status != StatusPlanned || res.RunID != "" {
		t.Fatalf("Ergebnis %+v, erwartet geplant ohne Lauf", res)
	}
	_, err = s.ApplyVersions(ctx, time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC))
	must(t, err)
	if f := formulaOf(t, pool, v); f != "["+ak+"]" {
		t.Errorf("vor Mitternacht: %q", f)
	}
	_, err = s.ApplyVersions(ctx, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	must(t, err)
	if f := formulaOf(t, pool, v); f != "["+ak+"]+["+bk+"]" {
		t.Errorf("ab Mitternacht: %q", f)
	}
	var base time.Time
	must(t, pool.QueryRow(ctx, `SELECT base_at FROM energy_meters WHERE id = $1::uuid`, v).Scan(&base))
	if !base.Equal(m0) {
		t.Errorf("base_at verschoben auf %s", base)
	}
}

func TestVersionOnePlannedPerDay(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	ak := keyOf(t, s, a)
	v, _ := virtual(t, s, infra, KindCounter, "["+ak+"]")
	first, err := s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*2", ValidFrom: ymd(2026, 12, 1)}, "")
	must(t, err)
	_, err = s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*3", ValidFrom: ymd(2026, 12, 1)}, "")
	var pe *PlannedExistsError
	if !errors.As(err, &pe) || !pe.Date.Equal(ymd(2026, 12, 1)) {
		t.Fatalf("zweite geplante Fassung am selben Tag: %v", err)
	}
	if _, err := s.SaveVersion(ctx, VersionInput{MeterID: v, VersionID: first.Version.ID, Formula: "[" + ak + "]*3", ValidFrom: ymd(2026, 12, 1)}, ""); err != nil {
		t.Fatalf("geplante Fassung ändern: %v", err)
	}
	vs := versionsOf(t, s, v)
	if len(vs) != 2 || vs[0].Formula != "["+ak+"]*3" || vs[0].Status != StatusPlanned {
		t.Errorf("Fassungen: %+v", vs)
	}
}

func TestVersionOnlyPlannedEditable(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	ak := keyOf(t, s, a)
	v, _ := virtual(t, s, infra, KindCounter, "["+ak+"]")
	vs := versionsOf(t, s, v)
	if len(vs) != 1 || vs[0].Status != StatusCurrent {
		t.Fatalf("Fassungen nach dem Anlegen: %+v", vs)
	}
	_, err := s.SaveVersion(ctx, VersionInput{MeterID: v, VersionID: vs[0].ID, Formula: "[" + ak + "]*2", ValidFrom: ymd(2026, 12, 1)}, "")
	if !errors.Is(err, ErrVersionFixed) {
		t.Errorf("geltende Fassung ändern: %v", err)
	}
	if err := s.DeleteVersion(ctx, v, vs[0].ID); !errors.Is(err, ErrVersionFixed) {
		t.Errorf("geltende Fassung löschen: %v", err)
	}
	planned, err := s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*2", ValidFrom: ymd(2026, 12, 1)}, "")
	must(t, err)
	must(t, s.DeleteVersion(ctx, v, planned.Version.ID))
	if n := len(versionsOf(t, s, v)); n != 1 {
		t.Errorf("nach dem Löschen der geplanten: %d Fassungen", n)
	}
}

func TestVersionPastNeedsRecalc(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	ak := keyOf(t, s, a)
	v, vk := virtual(t, s, infra, KindCounter, "["+ak+"]")
	w, wk := virtual(t, s, infra, KindCounter, "["+vk+"]*2")
	putValue(t, pool, v, time.Date(2026, 11, 13, 0, 1, 0, 0, time.UTC), 1)
	putValue(t, pool, w, time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), 1) // älter, zählt nicht für V
	// V gibt es schon länger: ihre erste Fassung gilt ab dem 1.11.
	_, err := pool.Exec(ctx, `UPDATE energy_formula_versions SET valid_from = '2026-11-01' WHERE meter_id = $1::uuid`, v)
	must(t, err)

	_, err = s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*3", ValidFrom: ymd(2026, 11, 14)}, "")
	if !errors.Is(err, ErrPastDate) {
		t.Errorf("gestern ohne Neurechnung: %v", err)
	}
	_, err = s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*3", ValidFrom: ymd(2026, 11, 12), Recalc: true}, "")
	var te *TooEarlyError
	if !errors.As(err, &te) || !te.First.Equal(ymd(2026, 11, 13)) {
		t.Errorf("vor dem ältesten Wert: %v", err)
	}
	res, err := s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*3", ValidFrom: ymd(2026, 11, 13), Recalc: true}, "")
	must(t, err)
	if res.RunID == "" {
		t.Fatal("kein Lauf angelegt")
	}
	var state string
	var keys []string
	var days int
	must(t, pool.QueryRow(ctx, `SELECT state, meter_keys, days_total FROM energy_recalc_runs WHERE id = $1::uuid`, res.RunID).Scan(&state, &keys, &days))
	if state != "running" || len(keys) != 2 || keys[0] != vk || keys[1] != wk || days != 3 {
		t.Errorf("Lauf: %s %v %d Tage", state, keys, days)
	}
	if f := formulaOf(t, pool, v); f != "["+ak+"]*3" {
		t.Errorf("geltende Formel nach der Neurechnung-Fassung: %q", f)
	}
}

func TestVersionBusy(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	ak := keyOf(t, s, a)
	v, vk := virtual(t, s, infra, KindCounter, "["+ak+"]")
	w, wk := virtual(t, s, infra, KindCounter, "["+ak+"]*2")
	putValue(t, pool, v, time.Date(2026, 11, 13, 0, 1, 0, 0, time.UTC), 1)
	_, err := pool.Exec(ctx, `INSERT INTO energy_recalc_runs (meter_id, meter_ids, meter_keys, from_date, days_total, state)
		VALUES ($1::uuid, ARRAY[$1::uuid], ARRAY[$2], '2026-11-13', 3, 'running')`, w, wk)
	must(t, err)

	_, err = s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]*3", ValidFrom: ymd(2026, 11, 14), Recalc: true}, "")
	if !errors.Is(err, ErrRecalcBusy) {
		t.Errorf("zweite Neurechnung: %v", err)
	}
	m := &Meter{ID: w, Key: wk, InfraID: infra, Kind: KindCounter, Formula: "[" + ak + "]*4", Active: true, Export: true}
	if _, err := s.Save(ctx, m, ""); !errors.Is(err, ErrRecalcBusy) {
		t.Errorf("Formel einer Messstelle im Lauf ändern: %v", err)
	}
	if err := s.Delete(ctx, w); !errors.Is(err, ErrRecalcBusy) {
		t.Errorf("Messstelle im Lauf löschen: %v", err)
	}
	// Ein Bezug einer Messstelle im Lauf bleibt ebenfalls, wie er ist (W2).
	ma, err := s.Get(ctx, a)
	must(t, err)
	ma.Factor = 2
	if _, err := s.Save(ctx, ma, ""); !errors.Is(err, ErrRecalcBusy) {
		t.Errorf("Bezug einer Messstelle im Lauf ändern: %v", err)
	}
	// Eine Messstelle außerhalb des Laufs bleibt bearbeitbar.
	m2 := &Meter{ID: v, Key: vk, InfraID: infra, Kind: KindCounter, Formula: "[" + ak + "]*5", Active: true, Export: true}
	if _, err := s.Save(ctx, m2, ""); err != nil {
		t.Errorf("Messstelle außerhalb des Laufs: %v", err)
	}
}

func TestFormulaScopeByKind(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	p1, _ := source(t, pool, s, infra, KindInstant, 1, "3", m0)
	p2, _ := source(t, pool, s, infra, KindInstant, 1, "7", m0)
	k1, k2 := keyOf(t, s, p1), keyOf(t, s, p2)
	max := "MAX([" + k1 + "]; [" + k2 + "])"

	inst := &Meter{Key: uniq("I"), InfraID: infra, Kind: KindInstant, Formula: max, Active: true, Export: true}
	if _, err := s.Save(ctx, inst, ""); err != nil {
		t.Fatalf("Momentanwert mit MAX: %v", err)
	}
	cnt := &Meter{Key: uniq("Z"), InfraID: infra, Kind: KindCounter, Formula: max, Active: true, Export: true}
	if _, err := s.Save(ctx, cnt, ""); !errors.Is(err, ErrFormulaScope) {
		t.Errorf("Zählerstand mit MAX: %v", err)
	}

	j, jk := virtual(t, s, infra, KindInstant, "["+k1+"]")
	_, err := s.SaveVersion(ctx, VersionInput{MeterID: j, Formula: max, ValidFrom: ymd(2026, 12, 1)}, "")
	must(t, err)
	m := &Meter{ID: j, Key: jk, InfraID: infra, Kind: KindCounter, Formula: "[" + k1 + "]", Active: true, Export: true}
	if _, err := s.Save(ctx, m, ""); !errors.Is(err, ErrFormulaScope) {
		t.Errorf("Artwechsel mit geplanter MAX-Fassung: %v", err)
	}
}

func TestVersionPlannedRefBlocksDelete(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	x, _ := source(t, pool, s, infra, KindCounter, 1, "5", m0)
	ak, xk := keyOf(t, s, a), keyOf(t, s, x)
	v, vk := virtual(t, s, infra, KindCounter, "["+ak+"]")
	_, err := s.SaveVersion(ctx, VersionInput{MeterID: v, Formula: "[" + ak + "]+[" + xk + "]", ValidFrom: ymd(2026, 12, 1)}, "")
	must(t, err)
	err = s.Delete(ctx, x)
	var ie *InUseError
	if !errors.As(err, &ie) || len(ie.Users) != 1 || ie.Users[0] != vk {
		t.Errorf("Löschen einer Messstelle aus einer geplanten Fassung: %v", err)
	}
}

func TestSaveFormulaCreatesTodayVersion(t *testing.T) {
	pool, s, infra := versionEnv(t)
	ctx := context.Background()
	a, _ := source(t, pool, s, infra, KindCounter, 1, "100", m0)
	ak := keyOf(t, s, a)
	v, vk := virtual(t, s, infra, KindCounter, "["+ak+"]")
	m := &Meter{ID: v, Key: vk, InfraID: infra, Kind: KindCounter, Formula: "[" + ak + "]*2", Active: true, Export: true}
	if _, err := s.Save(ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	vs := versionsOf(t, s, v)
	if len(vs) != 2 || vs[0].Formula != "["+ak+"]*2" || !vs[0].ValidFrom.Equal(ymd(2026, 11, 15)) ||
		vs[0].Status != StatusCurrent || vs[1].Status != StatusSuperseded {
		t.Errorf("Fassungen: %+v", vs)
	}
	// Gleiche Formel noch einmal speichern legt keine weitere Fassung an.
	if _, err := s.Save(ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(versionsOf(t, s, v)); n != 2 {
		t.Errorf("nach unverändertem Speichern %d Fassungen", n)
	}
	// Umstellen auf eine Quelle verwirft die Fassungen.
	_, mapID := newMapping(t, pool, infra)
	m = &Meter{ID: v, Key: vk, InfraID: infra, Kind: KindCounter, SourceID: mapID, Active: true, Export: true}
	if _, err := s.Save(ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(versionsOf(t, s, v)); n != 0 {
		t.Errorf("nach Umstellen auf Quelle %d Fassungen", n)
	}
}
