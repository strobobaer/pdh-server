package energie

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var e0 = time.Date(2026, 11, 15, 10, 15, 0, 0, time.UTC) // ein Intervallende

// putValue legt einen Minutenwert direkt ab.
func putValue(t *testing.T, pool *pgxpool.Pool, id string, ts time.Time, v float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO energy_values (meter_id, ts, value) VALUES ($1::uuid, $2, $3)
		ON CONFLICT (meter_id, ts) DO UPDATE SET value = EXCLUDED.value`, id, ts, v)
	must(t, err)
}

func TestIntervalValuesModes(t *testing.T) {
	pool, s, _, infra := samplerEnv(t)
	ctx := context.Background()
	c, _ := source(t, pool, s, infra, KindCounter, 1, "1", time.Now())
	cons, _ := source(t, pool, s, infra, KindConsumption, 1, "1", time.Now())
	inst, _ := source(t, pool, s, infra, KindInstant, 1, "1", time.Now())
	at := func(h, m int) time.Time { return time.Date(2026, 11, 15, h, m, 0, 0, time.UTC) }

	putValue(t, pool, c, at(10, 0), 100)
	putValue(t, pool, c, at(10, 14), 110)
	putValue(t, pool, c, at(10, 15), 115)
	putValue(t, pool, c, at(10, 16), 999)  // nach dem Ende
	putValue(t, pool, cons, at(10, 0), 50) // gehört zum Vorintervall
	putValue(t, pool, cons, at(10, 1), 1)
	putValue(t, pool, cons, at(10, 2), 2)
	putValue(t, pool, cons, at(10, 15), 3)
	putValue(t, pool, inst, at(10, 10), 7)
	putValue(t, pool, inst, at(10, 15), 9)

	meters, err := s.List(ctx, Filter{InfraID: infra})
	must(t, err)
	byKey := map[string]float64{}
	pts, missing, err := s.IntervalValues(ctx, meters, e0, 15*time.Minute, ModeEnd)
	must(t, err)
	for _, p := range pts {
		byKey[p.Key] = p.Value
		if !p.At.Equal(e0) {
			t.Errorf("Zeitpunkt %v", p.At)
		}
	}
	if len(missing) != 0 || len(pts) != 3 {
		t.Fatalf("pts=%v missing=%v", pts, missing)
	}
	if byKey[keyOf(t, s, c)] != 115 || byKey[keyOf(t, s, cons)] != 6 || byKey[keyOf(t, s, inst)] != 9 {
		t.Errorf("end: %v", byKey)
	}

	pts, _, err = s.IntervalValues(ctx, meters, e0, 15*time.Minute, ModeDelta)
	must(t, err)
	byKey = map[string]float64{}
	for _, p := range pts {
		byKey[p.Key] = p.Value
	}
	if byKey[keyOf(t, s, c)] != 15 || byKey[keyOf(t, s, cons)] != 6 || byKey[keyOf(t, s, inst)] != 9 {
		t.Errorf("delta: %v", byKey)
	}
}

func TestIntervalDeltaNegativeMissing(t *testing.T) {
	pool, s, _, infra := samplerEnv(t)
	ctx := context.Background()
	c, _ := source(t, pool, s, infra, KindCounter, 1, "1", time.Now())
	putValue(t, pool, c, e0.Add(-15*time.Minute), 500)
	putValue(t, pool, c, e0, 3) // Zählertausch: Wert fällt zurück
	meters, err := s.List(ctx, Filter{InfraID: infra})
	must(t, err)
	pts, missing, err := s.IntervalValues(ctx, meters, e0, 15*time.Minute, ModeDelta)
	must(t, err)
	if len(pts) != 0 || len(missing) != 1 || missing[0] != keyOf(t, s, c) {
		t.Errorf("pts=%v missing=%v", pts, missing)
	}
}

// ── Exporter ───────────────────────────────────────────────────────────

type expEnv struct {
	pool  *pgxpool.Pool
	s     *Store
	e     *Exporter
	infra string
	conn  string
	now   time.Time
	cfg   ExportConfig
}

func exportEnv(t *testing.T) *expEnv {
	t.Helper()
	pool, s, _, infra := samplerEnv(t)
	env := &expEnv{pool: pool, s: s, infra: infra, now: time.Date(2026, 11, 15, 10, 17, 0, 0, time.UTC)}
	s.Now = func() time.Time { return env.now }
	must(t, pool.QueryRow(context.Background(), `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('export', 'efficio', $1) RETURNING id::text`, uniq("efficio-test")).Scan(&env.conn))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1`, env.conn)
	})
	env.cfg = ExportConfig{
		ConnectionID: env.conn, Prefix: "efficio", Mode: ModeEnd, Interval: 15 * time.Minute,
		FileDays: 1, RetentionDays: 30, InfraIDs: []string{infra}, Target: PickupTarget{},
		CSV: CSVOptions{TimeFormat: "dmy", DecimalComma: true, Header: true, Encoding: "utf-8", Loc: time.UTC},
	}
	env.e = &Exporter{Store: s, Delay: 2 * time.Minute, MaxCatchUp: 7 * 24 * time.Hour,
		Configs: func(context.Context) ([]ExportConfig, error) { return []ExportConfig{env.cfg}, nil }}
	return env
}

func (env *expEnv) instant(t *testing.T) string {
	id, _ := source(t, env.pool, env.s, env.infra, KindInstant, 1, "1", time.Now())
	return id
}

type fileInfo struct {
	name    string
	content string
	version int
	rows    int
}

func (env *expEnv) files(t *testing.T) []fileInfo {
	t.Helper()
	rows, err := env.pool.Query(context.Background(), `SELECT name, convert_from(content, 'UTF8'), version, rows
		FROM energy_export_files WHERE connection_id = $1::uuid ORDER BY name`, env.conn)
	must(t, err)
	defer rows.Close()
	var out []fileInfo
	for rows.Next() {
		var f fileInfo
		must(t, rows.Scan(&f.name, &f.content, &f.version, &f.rows))
		out = append(out, f)
	}
	return out
}

func TestExporterAppendsToDayFile(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1.5)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 2.5)

	r, err := env.e.Run(ctx, env.cfg)
	must(t, err)
	if r.Intervals != 1 || r.Rows != 1 || r.Result != "ok" || r.Delivered != 1 {
		t.Fatalf("erster Lauf: %+v", r)
	}
	env.now = env.now.Add(15 * time.Minute)
	r, err = env.e.Run(ctx, env.cfg)
	must(t, err)
	if r.Intervals != 1 || r.Rows != 1 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	fs := env.files(t)
	if len(fs) != 1 {
		t.Fatalf("%d Dateien", len(fs))
	}
	f := fs[0]
	key := keyOf(t, env.s, m)
	want := "Zeitstempel;Messstellen-ID;Messwert\r\n15.11.2026 10:15;" + key + ";1,5\r\n15.11.2026 10:30;" + key + ";2,5\r\n"
	if f.name != "efficio_20261115_0000.csv" || f.content != want || f.version != 2 || f.rows != 2 {
		t.Errorf("Datei: %+v", f)
	}
	if strings.Count(f.content, "Zeitstempel") != 1 {
		t.Error("Kopfzeile nicht genau einmal")
	}
	var delivered int
	must(t, env.pool.QueryRow(ctx, `SELECT delivered_version FROM energy_export_files WHERE connection_id = $1::uuid`, env.conn).Scan(&delivered))
	if delivered != 2 {
		t.Errorf("delivered_version %d", delivered)
	}
	var runs int
	must(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM energy_export_runs WHERE connection_id = $1::uuid AND result = 'ok'`, env.conn).Scan(&runs))
	if runs != 2 {
		t.Errorf("%d Protokollzeilen", runs)
	}
}

func TestExporterFileDaysZero(t *testing.T) {
	env := exportEnv(t)
	env.cfg.FileDays = 0
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 2)
	_, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	env.now = env.now.Add(15 * time.Minute)
	_, err = env.e.Run(context.Background(), env.cfg)
	must(t, err)
	fs := env.files(t)
	if len(fs) != 2 || fs[0].name != "efficio_20261115_1015.csv" || fs[1].name != "efficio_20261115_1030.csv" {
		t.Fatalf("Dateien: %+v", fs)
	}
	for _, f := range fs {
		if f.version != 1 || strings.Count(f.content, "\r\n") != 2 {
			t.Errorf("Datei %+v", f)
		}
	}
}

func TestExporterMissingRowAndNote(t *testing.T) {
	env := exportEnv(t)
	a, b := env.instant(t), env.instant(t)
	putValue(t, env.pool, a, e0, 4)
	_ = b // kein Wert
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Result != "partial" || r.Rows != 1 || r.Missing != 1 {
		t.Fatalf("%+v", r)
	}
	if len(r.Notes) != 1 || r.Notes[0] != "["+keyOf(t, env.s, b)+"] ohne Wert 15.11.2026 10:15" {
		t.Errorf("Notizen: %q", r.Notes)
	}
	fs := env.files(t)
	if len(fs) != 1 || strings.Contains(fs[0].content, keyOf(t, env.s, b)) {
		t.Errorf("Datei: %+v", fs)
	}
}

func TestExporterOnlyExportFlag(t *testing.T) {
	env := exportEnv(t)
	a, b := env.instant(t), env.instant(t)
	putValue(t, env.pool, a, e0, 1)
	putValue(t, env.pool, b, e0, 2)
	_, err := env.pool.Exec(context.Background(), `UPDATE energy_meters SET export = false WHERE id = $1::uuid`, b)
	must(t, err)
	// Rohwert der Import-Zuordnung steht nur dort, nie in der Datei.
	_, err = env.pool.Exec(context.Background(), `UPDATE import_mappings SET last_value = '4711', last_received_at = now()
		WHERE id = (SELECT import_mapping_id FROM energy_meters WHERE id = $1::uuid)`, a)
	must(t, err)
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Rows != 1 || r.Missing != 0 {
		t.Fatalf("%+v", r)
	}
	c := env.files(t)[0].content
	if strings.Contains(c, keyOf(t, env.s, b)) || strings.Contains(c, "4711") || !strings.Contains(c, keyOf(t, env.s, a)) {
		t.Errorf("Inhalt %q", c)
	}
}

func TestExporterCatchUpNoDuplicates(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	m := env.instant(t)
	tmax := e0
	for k := 0; k < 12; k++ {
		putValue(t, env.pool, m, tmax.Add(-time.Duration(k)*15*time.Minute), float64(k))
	}
	_, err := env.pool.Exec(ctx, `INSERT INTO energy_export_state (connection_id, last_interval_end) VALUES ($1::uuid, $2)`,
		env.conn, tmax.Add(-3*time.Hour))
	must(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := env.e.Run(ctx, env.cfg)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total += r.Intervals
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 12 {
		t.Errorf("Intervalle gesamt %d, erwartet 12", total)
	}
	fs := env.files(t)
	if len(fs) != 1 {
		t.Fatalf("%d Dateien", len(fs))
	}
	lines := strings.Split(strings.TrimSuffix(fs[0].content, "\r\n"), "\r\n")
	seen := map[string]bool{}
	for _, l := range lines[1:] {
		if seen[l] {
			t.Errorf("doppelte Zeile %q", l)
		}
		seen[l] = true
	}
	if len(lines) != 13 {
		t.Errorf("%d Zeilen, erwartet 13 (Kopf + 12)", len(lines))
	}
}

func TestExporterRetention(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	old := env.now.AddDate(0, 0, -31)
	_, err := env.pool.Exec(ctx, `INSERT INTO energy_export_files (connection_id, name, period_start, period_end, content)
		VALUES ($1::uuid, 'alt.csv', $2, $3, ''::bytea), ($1::uuid, 'neu.csv', $4, $5, ''::bytea)`,
		env.conn, old.Add(-24*time.Hour), old, env.now.AddDate(0, 0, -5), env.now.AddDate(0, 0, -4))
	must(t, err)
	_, err = env.pool.Exec(ctx, `INSERT INTO energy_export_runs (connection_id, started_at, result) VALUES ($1::uuid, $2, 'ok')`, env.conn, old)
	must(t, err)
	_, err = env.e.Run(ctx, env.cfg)
	must(t, err)
	fs := env.files(t)
	if len(fs) != 1 || fs[0].name != "neu.csv" {
		t.Errorf("Dateien: %+v", fs)
	}
	var n int
	must(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM energy_export_runs WHERE connection_id = $1::uuid AND started_at < $2`, env.conn, env.now.AddDate(0, 0, -30)).Scan(&n))
	if n != 0 {
		t.Errorf("%d alte Protokollzeilen", n)
	}
}

func TestExporterSkip(t *testing.T) {
	env := exportEnv(t)
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	skip := true
	env.e.Skip = func() bool { return skip }
	must(t, env.e.Pass(context.Background()))
	if len(env.files(t)) != 0 {
		t.Fatal("Skip: es darf nichts entstehen")
	}
	skip = false
	must(t, env.e.Pass(context.Background()))
	if len(env.files(t)) != 1 {
		t.Fatal("ohne Skip muss eine Datei entstehen")
	}
	// Ein Lauf ohne neues Intervall wird im Takt nicht protokolliert.
	var before, after int
	q := `SELECT count(*) FROM energy_export_runs WHERE connection_id = $1::uuid`
	must(t, env.pool.QueryRow(context.Background(), q, env.conn).Scan(&before))
	must(t, env.e.Pass(context.Background()))
	must(t, env.pool.QueryRow(context.Background(), q, env.conn).Scan(&after))
	if before != after {
		t.Errorf("Protokoll wuchs von %d auf %d", before, after)
	}
}

func TestExporterMinuteGridPerRun(t *testing.T) {
	env := exportEnv(t)
	env.cfg.Interval, env.cfg.RunEvery = time.Minute, 15*time.Minute
	m := env.instant(t)
	for k := 1; k <= 30; k++ {
		putValue(t, env.pool, m, e0.Add(time.Duration(k-15)*time.Minute), float64(k))
	}
	// Vor dem Lauf-Takt (10:16) passiert nichts über das Vorintervall hinaus.
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Intervals != 15 || r.Rows != 15 {
		t.Fatalf("erster Lauf: %+v", r)
	}
	r, err = env.e.Run(context.Background(), env.cfg) // gleiche Zeit: nichts mehr
	must(t, err)
	if r.Intervals != 0 || r.Result != "nothing" {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	env.now = env.now.Add(15 * time.Minute)
	r, err = env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Intervals != 15 || r.Rows != 15 {
		t.Fatalf("dritter Lauf: %+v", r)
	}
	fs := env.files(t)
	if len(fs) != 1 || fs[0].rows != 30 || strings.Count(fs[0].content, "\r\n") != 31 {
		t.Errorf("Datei: %+v", fs)
	}
	if !strings.Contains(fs[0].content, "15.11.2026 10:01;") || !strings.Contains(fs[0].content, "15.11.2026 10:30;") {
		t.Error("Zeitstempel 10:01 und 10:30 erwartet")
	}
}

// Ohne Werte entsteht keine Datei – auch keine mit Kopfzeile. Kommen später
// Werte, entsteht sie normal; eine vorhandene Datei bleibt bei leerem Lauf unverändert.
func TestExporterNoDataNoFile(t *testing.T) {
	env := exportEnv(t)
	a := env.instant(t) // Messstelle ohne Minutenwert
	ctx := context.Background()
	r, err := env.e.Run(ctx, env.cfg)
	must(t, err)
	if r.Rows != 0 || len(r.Files) != 0 {
		t.Fatalf("Lauf ohne Werte: %+v", r)
	}
	if fs := env.files(t); len(fs) != 0 {
		t.Fatalf("Datei trotz fehlender Werte: %+v", fs)
	}
	putValue(t, env.pool, a, e0, 4)
	// Intervall e0 ist schon als leer abgehakt; ein neues Intervall mit Wert erzeugt die Datei.
	putValue(t, env.pool, a, e0.Add(15*time.Minute), 5)
	env.now = env.now.Add(15 * time.Minute)
	r, err = env.e.Run(ctx, env.cfg)
	must(t, err)
	fs := env.files(t)
	if len(fs) != 1 || fs[0].rows != 1 || !strings.Contains(fs[0].content, "Zeitstempel;Messstellen-ID;Messwert") {
		t.Fatalf("Datei nach Werten: %+v %+v", fs, r)
	}
	ver := fs[0].version
	env.now = env.now.Add(15 * time.Minute) // wieder ein Lauf ohne Werte
	if _, err = env.e.Run(ctx, env.cfg); err != nil {
		t.Fatal(err)
	}
	if fs = env.files(t); len(fs) != 1 || fs[0].version != ver {
		t.Errorf("Datei bei leerem Lauf verändert: %+v", fs)
	}
}
