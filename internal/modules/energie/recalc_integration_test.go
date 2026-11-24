package energie

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// recEnv: Zähler R1 (+1 je Minute) und R2 (+2 je Minute) mit Minutenwerten,
// V = [R1] und W = [V]*2 mit alten Werten (überall 1). Die ersten Fassungen
// von V und W gelten ab dem 1.10.2026.
type recEnv struct {
	pool             *pgxpool.Pool
	s                *Store
	infra            string
	r1, r2, v, w     string
	k1, k2, vk, wk   string
	r1Map, r2Map     string
	seriesFrom, last time.Time
}

func recalcEnv(t *testing.T, now time.Time, loc *time.Location, seriesFrom time.Time) *recEnv {
	t.Helper()
	pool, s, _, infra := samplerEnv(t)
	s.Loc = loc
	s.Now = func() time.Time { return now }
	e := &recEnv{pool: pool, s: s, infra: infra, seriesFrom: seriesFrom, last: now.Truncate(time.Minute).Add(-time.Minute)}
	e.r1, e.r1Map = source(t, pool, s, infra, KindCounter, 1, "0", seriesFrom)
	e.r2, e.r2Map = source(t, pool, s, infra, KindCounter, 1, "0", seriesFrom)
	e.k1, e.k2 = keyOf(t, s, e.r1), keyOf(t, s, e.r2)
	e.v, e.vk = virtual(t, s, infra, KindCounter, "["+e.k1+"]")
	e.w, e.wk = virtual(t, s, infra, KindCounter, "["+e.vk+"]*2")
	ctx := context.Background()
	for id, rate := range map[string]float64{e.r1: 1, e.r2: 2} {
		_, err := pool.Exec(ctx, `INSERT INTO energy_values (meter_id, ts, value)
			SELECT $1::uuid, g, $4 * extract(epoch FROM g - $2::timestamptz) / 60
			  FROM generate_series($2::timestamptz + interval '1 minute', $3::timestamptz, interval '1 minute') g`,
			id, seriesFrom, e.last, rate)
		must(t, err)
		_, err = pool.Exec(ctx, `UPDATE energy_meters SET last_at = $2 WHERE id = $1::uuid`, id, e.last)
		must(t, err)
	}
	fillSeries(t, pool, e.v, seriesFrom.Add(time.Minute), e.last, time.Minute)
	fillSeries(t, pool, e.w, seriesFrom.Add(time.Minute), e.last, time.Minute)
	_, err := pool.Exec(ctx, `UPDATE energy_formula_versions SET valid_from = '2026-10-01' WHERE meter_id = ANY($1::uuid[])`, []string{e.v, e.w})
	must(t, err)
	return e
}

// minutes seit from (für die erwarteten Werte).
func minutesSince(ts, from time.Time) float64 { return ts.Sub(from).Minutes() }

func (e *recEnv) saveRecalc(t *testing.T, formula string, from time.Time) string {
	t.Helper()
	res, err := e.s.SaveVersion(context.Background(), VersionInput{MeterID: e.v, Formula: formula, ValidFrom: from, Recalc: true}, "")
	must(t, err)
	if res.RunID == "" {
		t.Fatal("kein Lauf angelegt")
	}
	return res.RunID
}

func (e *recEnv) runState(t *testing.T, id string) (state, msg string, doneUntil *time.Time, written int64) {
	t.Helper()
	must(t, e.pool.QueryRow(context.Background(), `SELECT state, message, done_until, values_written FROM energy_recalc_runs WHERE id = $1::uuid`, id).
		Scan(&state, &msg, &doneUntil, &written))
	return
}

func (e *recEnv) passUntilDone(t *testing.T, rc *Recalculator, id string) {
	t.Helper()
	for i := 0; i < 5; i++ {
		if err := rc.Pass(context.Background()); err != nil {
			t.Fatalf("Pass: %v", err)
		}
		if st, _, _, _ := e.runState(t, id); st == "done" {
			return
		}
	}
	st, msg, _, _ := e.runState(t, id)
	t.Fatalf("Lauf nicht fertig: %s %s", st, msg)
}

func wantAt(t *testing.T, pool *pgxpool.Pool, id, name string, ts time.Time, want float64) {
	t.Helper()
	got, ok := valueAt(t, pool, id, ts)
	if !ok || math.Abs(got-want) > 1e-6 {
		t.Errorf("%s %s: %v (vorhanden %v), erwartet %v", name, ts.Format("02.01. 15:04"), got, ok, want)
	}
}

func wantNoneAt(t *testing.T, pool *pgxpool.Pool, id, name string, ts time.Time) {
	t.Helper()
	if v, ok := valueAt(t, pool, id, ts); ok {
		t.Errorf("%s %s: %v, erwartet kein Wert", name, ts.Format("02.01. 15:04"), v)
	}
}

var recNow = time.Date(2026, 11, 15, 10, 17, 0, 0, time.UTC)

func TestRecalcCounterMatchesRule(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 11, 13))
	e.passUntilDone(t, NewRecalculator(e.s, nil, nil), id)

	d13 := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	wantAt(t, e.pool, e.v, "V", d13, 1) // gehört zum 12.11.
	wantAt(t, e.pool, e.w, "W", d13, 1)
	wantAt(t, e.pool, e.v, "V", d13.Add(-12*time.Hour), 1)
	for _, ts := range []time.Time{d13.Add(time.Minute), d13.Add(13 * time.Hour), d13.Add(30 * time.Hour), e.last} {
		m := minutesSince(ts, d13)
		wantAt(t, e.pool, e.v, "V", ts, 1+3*m)
		wantAt(t, e.pool, e.w, "W", ts, 1+6*m)
	}
	wantAt(t, e.pool, e.r1, "R1", e.last, minutesSince(e.last, e.seriesFrom))
	wantAt(t, e.pool, e.r2, "R2", e.last, 2*minutesSince(e.last, e.seriesFrom))
	m, err := e.s.Get(context.Background(), e.v)
	must(t, err)
	if m.LastAt == nil || !m.LastAt.Equal(e.last) {
		t.Errorf("last_at von V: %v, erwartet %s", m.LastAt, e.last)
	}
	if _, _, _, written := e.runState(t, id); written == 0 {
		t.Error("values_written ist 0")
	}
	if p, err := pausedMeters(context.Background(), e.pool); err != nil || len(p) != 0 {
		t.Errorf("nach dem Lauf angehalten: %v %v", p, err)
	}
}

func TestRecalcResetAndGap(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	swap := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	// Zählertausch: ab 08:00 beginnt R2 wieder bei 5 (+2 je Minute).
	_, err := e.pool.Exec(ctx, `UPDATE energy_values SET value = 5 + 2 * extract(epoch FROM ts - $2::timestamptz) / 60
		WHERE meter_id = $1::uuid AND ts >= $2`, e.r2, swap)
	must(t, err)
	gap := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	_, err = e.pool.Exec(ctx, `DELETE FROM energy_values WHERE meter_id = $1::uuid AND ts >= $2 AND ts <= $3`, e.r2, gap, gap.Add(4*time.Minute))
	must(t, err)

	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 11, 13))
	e.passUntilDone(t, NewRecalculator(e.s, nil, nil), id)

	before, ok := valueAt(t, e.pool, e.v, swap.Add(-time.Minute))
	if !ok {
		t.Fatal("V hat vor dem Tausch keinen Wert")
	}
	wantNoneAt(t, e.pool, e.v, "V", swap)
	wantAt(t, e.pool, e.v, "V", swap.Add(time.Minute), before+3) // nur die Minute nach dem Tausch
	for i := 0; i < 5; i++ {
		wantNoneAt(t, e.pool, e.v, "V", gap.Add(time.Duration(i)*time.Minute))
	}
	at859, _ := valueAt(t, e.pool, e.v, gap.Add(-time.Minute))
	wantAt(t, e.pool, e.v, "V", gap.Add(5*time.Minute), at859+18) // 6 Minuten × 3, nachgeholt
}

func TestRecalcDSTDay(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	must(t, err)
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	e := recalcEnv(t, now, berlin, time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC))
	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 10, 25))
	e.passUntilDone(t, NewRecalculator(e.s, nil, nil), id)

	var n int
	must(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_values WHERE meter_id = $1::uuid AND ts > $2 AND ts <= $3`,
		e.v, dayStart(ymd(2026, 10, 25), berlin), dayStart(ymd(2026, 10, 26), berlin)).Scan(&n))
	if n != 1500 {
		t.Errorf("V am 25.10. (25 Stunden): %d Werte, erwartet 1500", n)
	}
	start := dayStart(ymd(2026, 10, 25), berlin)
	wantAt(t, e.pool, e.v, "V", start.Add(1500*time.Minute), 1+3*1500)
}

func TestRecalcResume(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 11, 13))
	rc := NewRecalculator(e.s, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	rc.afterDay = func(time.Time) { cancel() }
	if err := rc.Pass(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("erster Pass: %v", err)
	}
	st, _, done, _ := e.runState(t, id)
	if st != "running" || done == nil || !done.Equal(ymd(2026, 11, 13)) {
		t.Fatalf("nach dem Abbruch: %s, bis %v", st, done)
	}
	rc.afterDay = nil
	e.passUntilDone(t, rc, id)
	var n int
	d13 := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	must(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_values WHERE meter_id = $1::uuid AND ts > $2 AND ts <= $3`,
		e.v, d13, d13.Add(24*time.Hour)).Scan(&n))
	if n != 1440 {
		t.Errorf("V am 13.11.: %d Werte, erwartet 1440", n)
	}
	wantAt(t, e.pool, e.v, "V", d13.Add(30*time.Hour), 1+3*30*60)
	if st, msg, _, _ := e.runState(t, id); st != "done" || msg != "" {
		t.Errorf("Abbruch zählt als Fehler: %s %q", st, msg)
	}
}

func TestRecalcHandoverNoJump(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	// Wie im Betrieb: V und W haben eine Basis von vor dem Lauf.
	_, err := e.pool.Exec(context.Background(), `UPDATE energy_meters SET base_at = $2 WHERE id = ANY($1::uuid[])`,
		[]string{e.v, e.w}, time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC))
	must(t, err)
	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 11, 13))
	e.passUntilDone(t, NewRecalculator(e.s, nil, nil), id)

	// Die Quellen liefern in der Minute 10:16–10:17 ihren nächsten Stand.
	r1 := minutesSince(e.last, e.seriesFrom)
	setReading(t, e.pool, e.r1Map, fmt.Sprint(r1+1), e.last.Add(30*time.Second))
	setReading(t, e.pool, e.r2Map, fmt.Sprint(2*r1+2), e.last.Add(30*time.Second))
	vLast, ok := valueAt(t, e.pool, e.v, e.last)
	if !ok {
		t.Fatal("V hat zur Übergabe keinen Wert")
	}
	sm := &Sampler{Store: e.s}
	run(t, sm, e.last)
	wantAt(t, e.pool, e.v, "V", e.last.Add(time.Minute), vLast+3)
}

func TestRecalcFailsAfterAttempts(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	_, err := e.pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION energy_test_reject() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.meter_id = '%s'::uuid THEN RAISE EXCEPTION 'Testfehler'; END IF; RETURN NEW; END $$`, e.v))
	must(t, err)
	_, err = e.pool.Exec(ctx, `CREATE TRIGGER trg_energy_test_reject BEFORE INSERT ON energy_values
		FOR EACH ROW EXECUTE FUNCTION energy_test_reject()`)
	must(t, err)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = e.pool.Exec(c, `DROP TRIGGER IF EXISTS trg_energy_test_reject ON energy_values`)
		_, _ = e.pool.Exec(c, `DROP FUNCTION IF EXISTS energy_test_reject()`)
	})

	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 11, 13))
	rc := NewRecalculator(e.s, nil, nil)
	rc.MaxAttempts = 2
	for i := 0; i < 2; i++ {
		if err := rc.Pass(ctx); err == nil {
			t.Fatalf("Pass %d ohne Fehler", i+1)
		}
	}
	st, msg, _, _ := e.runState(t, id)
	if st != "failed" || msg == "" {
		t.Errorf("Lauf: %s %q, erwartet failed mit Meldung", st, msg)
	}
	if p, err := pausedMeters(ctx, e.pool); err != nil || len(p) != 0 {
		t.Errorf("nach dem Abbruch angehalten: %v %v", p, err)
	}
}

func TestRecalcRebuildsExport(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	var conn string
	must(t, e.pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('export', 'efficio', $1) RETURNING id::text`, uniq("efficio-test")).Scan(&conn))
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1::uuid`, conn)
	})
	cfg := ExportConfig{ConnectionID: conn, Prefix: "efficio", Mode: ModeEnd, Interval: 15 * time.Minute,
		FileDays: 1, RetentionDays: 30, MeterKeys: []string{e.vk}, Target: PickupTarget{},
		CSV: CSVOptions{TimeFormat: "dmy", DecimalComma: true, Header: true, Encoding: "utf-8", Loc: time.UTC}}
	ex := &Exporter{Store: e.s, Delay: 2 * time.Minute, MaxCatchUp: 7 * 24 * time.Hour,
		Configs: func(context.Context) ([]ExportConfig, error) { return []ExportConfig{cfg}, nil }}
	_, err := e.pool.Exec(ctx, `INSERT INTO energy_export_state (connection_id, last_interval_end) VALUES ($1::uuid, $2)`,
		conn, time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	must(t, err)
	_, err = ex.Run(ctx, cfg)
	must(t, err)

	id := e.saveRecalc(t, "["+e.k1+"]+["+e.k2+"]", ymd(2026, 11, 13))
	e.passUntilDone(t, NewRecalculator(e.s, ex, nil), id)

	var rebuilt, content string
	must(t, e.pool.QueryRow(ctx, `SELECT files_rebuilt FROM energy_recalc_runs WHERE id = $1::uuid`, id).Scan(&rebuilt))
	if !strings.Contains(rebuilt, "efficio_20261115_0000.csv") {
		t.Errorf("neu erzeugt: %q", rebuilt)
	}
	must(t, e.pool.QueryRow(ctx, `SELECT convert_from(content, 'UTF8') FROM energy_export_files WHERE connection_id = $1::uuid`, conn).Scan(&content))
	at := time.Date(2026, 11, 15, 10, 15, 0, 0, time.UTC)
	if want := fmt.Sprintf("15.11.2026 10:15;%s;%d\r\n", e.vk, int(1+3*minutesSince(at, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)))); !strings.Contains(content, want) {
		t.Errorf("Datei enthält %q nicht:\n%s", want, content)
	}
}

func TestRecalcPreview(t *testing.T) {
	e := recalcEnv(t, recNow, time.UTC, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	rc := NewRecalculator(e.s, nil, nil)
	p, err := rc.Preview(context.Background(), VersionInput{MeterID: e.v, Formula: "[" + e.k1 + "]+[" + e.k2 + "]", ValidFrom: ymd(2026, 11, 13), Recalc: true})
	must(t, err)
	if !p.From.Equal(ymd(2026, 11, 13)) || !p.To.Equal(ymd(2026, 11, 15)) || p.Days != 3 ||
		len(p.Meters) != 2 || p.Meters[0] != e.vk || p.Meters[1] != e.wk {
		t.Errorf("Vorschau: %+v", p)
	}
	var runs, vers int
	must(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_recalc_runs WHERE meter_id = $1::uuid`, e.v).Scan(&runs))
	must(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_formula_versions WHERE meter_id = $1::uuid`, e.v).Scan(&vers))
	if runs != 0 || vers != 1 {
		t.Errorf("Vorschau hat geschrieben: %d Läufe, %d Fassungen", runs, vers)
	}
	if f := formulaOf(t, e.pool, e.v); f != "["+e.k1+"]" {
		t.Errorf("Vorschau hat die Formel geändert: %q", f)
	}
}
