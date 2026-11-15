package energie

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fillSeries legt Werte von from bis to (einschließlich) im Abstand step ab.
func fillSeries(t *testing.T, pool *pgxpool.Pool, id string, from, to time.Time, step time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO energy_values (meter_id, ts, value)
		SELECT $1::uuid, g, 1 FROM generate_series($2::timestamptz, $3::timestamptz, $4::interval) g
		ON CONFLICT DO NOTHING`, id, from, to, fmt.Sprintf("%d seconds", int(step.Seconds())))
	must(t, err)
}

func (env *expEnv) setState(t *testing.T, last time.Time) {
	t.Helper()
	_, err := env.pool.Exec(context.Background(), `INSERT INTO energy_export_state (connection_id, last_interval_end)
		VALUES ($1::uuid, $2) ON CONFLICT (connection_id) DO UPDATE SET last_interval_end = $2`, env.conn, last)
	must(t, err)
}

// dataLines liefert alle Datenzeilen (ohne Kopfzeile) aller Dateien.
func (env *expEnv) dataLines(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range env.files(t) {
		for _, l := range strings.Split(strings.TrimSuffix(f.content, "\r\n"), "\r\n") {
			if !strings.HasPrefix(l, "Zeitstempel") && l != "" {
				out = append(out, l)
			}
		}
	}
	return out
}

func TestExporterCatchUpSevenDaysMinuteGrid(t *testing.T) {
	env := exportEnv(t)
	env.cfg.Interval, env.cfg.RunEvery = time.Minute, 15*time.Minute
	ctx := context.Background()
	m := env.instant(t)
	fillSeries(t, env.pool, m, e0.AddDate(0, 0, -11), e0, time.Minute)
	env.setState(t, e0.AddDate(0, 0, -10)) // länger her als 7 Tage

	r, err := env.e.Run(ctx, env.cfg)
	must(t, err)
	// (e0−7 d … e0] einschließlich der Grenze: 7 × 1440 + 1 Intervalle
	if r.Intervals != 7*1440+1 || r.Rows != r.Intervals {
		t.Fatalf("Intervalle %d, Zeilen %d", r.Intervals, r.Rows)
	}
	if len(r.Notes) != 1 || !strings.HasPrefix(r.Notes[0], "Zu lange her") {
		t.Errorf("Notizen: %q", r.Notes)
	}
	lines := env.dataLines(t)
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l] {
			t.Fatalf("doppelte Zeile %q", l)
		}
		seen[l] = true
	}
	if len(lines) != r.Intervals {
		t.Errorf("%d Zeilen in den Dateien, erwartet %d", len(lines), r.Intervals)
	}
	if len(env.files(t)) != 8 {
		t.Errorf("%d Tagesdateien, erwartet 8", len(env.files(t)))
	}
	var st time.Time
	must(t, env.pool.QueryRow(ctx, `SELECT last_interval_end FROM energy_export_state WHERE connection_id = $1::uuid`, env.conn).Scan(&st))
	if !st.Equal(e0) {
		t.Errorf("Zustand %v", st)
	}
}

func TestExporterTwoMinuteDelay(t *testing.T) {
	env := exportEnv(t)
	m := env.instant(t)
	putValue(t, env.pool, m, e0.Add(-15*time.Minute), 1)
	putValue(t, env.pool, m, e0, 2)
	env.now = e0.Add(time.Minute) // 10:16: 10:15 ist noch keine 2 min her
	_, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	c := env.files(t)[0].content
	if !strings.Contains(c, "10:00;") || strings.Contains(c, "10:15;") {
		t.Errorf("Inhalt %q", c)
	}
	env.now = e0.Add(2 * time.Minute)
	_, err = env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if !strings.Contains(env.files(t)[0].content, "10:15;") {
		t.Error("10:15 fehlt nach 2 min")
	}
}

type failTarget struct {
	fail bool
	got  map[string]string
}

func (f *failTarget) Label() string { return "Test" }
func (f *failTarget) Put(_ context.Context, name string, b []byte) error {
	if f.fail {
		return errors.New("Netzlaufwerk nicht erreichbar")
	}
	if f.got == nil {
		f.got = map[string]string{}
	}
	f.got[name] = string(b)
	return nil
}
func (f *failTarget) Test(context.Context) (string, error) { return "", nil }

func TestExporterDeliveryFailureRetried(t *testing.T) {
	env := exportEnv(t)
	ctx := context.Background()
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 2)
	tg := &failTarget{fail: true}
	env.cfg.Target = tg
	r, err := env.e.Run(ctx, env.cfg)
	if err == nil || r.Result != "error" || r.Delivered != 0 {
		t.Fatalf("err=%v %+v", err, r)
	}
	var lastErr string
	var dv int
	q := `SELECT last_error, delivered_version FROM energy_export_files WHERE connection_id = $1::uuid`
	must(t, env.pool.QueryRow(ctx, q, env.conn).Scan(&lastErr, &dv))
	if !strings.Contains(lastErr, "nicht erreichbar") || dv != 0 {
		t.Errorf("last_error=%q delivered=%d", lastErr, dv)
	}
	tg.fail = false
	env.now = env.now.Add(15 * time.Minute)
	r, err = env.e.Run(ctx, env.cfg)
	must(t, err)
	if r.Delivered != 1 || r.Result != "ok" {
		t.Fatalf("%+v", r)
	}
	// Nachgelegt wird der neueste Stand mit beiden Zeilen.
	if got := tg.got["efficio_20261115_0000.csv"]; strings.Count(got, "\r\n") != 3 {
		t.Errorf("Ziel: %q", got)
	}
	must(t, env.pool.QueryRow(ctx, q, env.conn).Scan(&lastErr, &dv))
	if lastErr != "" || dv != 2 {
		t.Errorf("last_error=%q delivered=%d", lastErr, dv)
	}
}

func TestExporterNoHeader(t *testing.T) {
	env := exportEnv(t)
	env.cfg.CSV.Header = false
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	_, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if c := env.files(t)[0].content; strings.Contains(c, "Zeitstempel") || strings.Count(c, "\r\n") != 1 {
		t.Errorf("Inhalt %q", c)
	}
}

func TestExporterSelectionInfraAndKeys(t *testing.T) {
	env := exportEnv(t)
	infra2 := newInfra(t, env.pool, "")
	a := env.instant(t)
	b, _ := source(t, env.pool, env.s, infra2, KindInstant, 1, "1", time.Now())
	c, _ := source(t, env.pool, env.s, infra2, KindInstant, 1, "1", time.Now())
	for _, id := range []string{a, b, c} {
		putValue(t, env.pool, id, e0, 1)
	}
	env.cfg.MeterKeys = []string{keyOf(t, env.s, b)}
	_, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	got := env.files(t)[0].content
	if !strings.Contains(got, keyOf(t, env.s, a)) || !strings.Contains(got, keyOf(t, env.s, b)) || strings.Contains(got, keyOf(t, env.s, c)) {
		t.Errorf("Auswahl Anlage + ID: %q", got)
	}
	// nur IDs: nur die genannte Messstelle
	env.cfg.InfraIDs = nil
	env.cfg.FileDays = 0
	env.now = env.now.Add(15 * time.Minute)
	putValue(t, env.pool, b, e0.Add(15*time.Minute), 1)
	putValue(t, env.pool, a, e0.Add(15*time.Minute), 1)
	_, err = env.e.Run(context.Background(), env.cfg)
	must(t, err)
	fs := env.files(t)
	only := fs[len(fs)-1].content
	if strings.Contains(only, keyOf(t, env.s, a)) || !strings.Contains(only, keyOf(t, env.s, b)) {
		t.Errorf("nur IDs: %q", only)
	}
}

func TestExporterInactiveNotExported(t *testing.T) {
	env := exportEnv(t)
	a, b := env.instant(t), env.instant(t)
	putValue(t, env.pool, a, e0, 1)
	putValue(t, env.pool, b, e0, 2)
	_, err := env.pool.Exec(context.Background(), `UPDATE energy_meters SET active = false WHERE id = $1::uuid`, b)
	must(t, err)
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Rows != 1 || r.Missing != 0 || strings.Contains(env.files(t)[0].content, keyOf(t, env.s, b)) {
		t.Errorf("%+v", r)
	}
}

func TestExporterMultiDayFile(t *testing.T) {
	env := exportEnv(t)
	env.cfg.FileDays = 2
	m := env.instant(t)
	day := func(d, h, mi int) time.Time { return time.Date(2026, 11, d, h, mi, 0, 0, time.UTC) }
	for _, ts := range []time.Time{day(15, 10, 15), day(16, 10, 15), day(16, 23, 45), day(17, 0, 0)} {
		putValue(t, env.pool, m, ts, 1)
	}
	for _, now := range []time.Time{day(15, 10, 17), day(16, 10, 17), day(17, 0, 17)} {
		env.now = now
		_, err := env.e.Run(context.Background(), env.cfg)
		must(t, err)
	}
	fs := env.files(t)
	if len(fs) != 2 || fs[0].name != "efficio_20261115_0000.csv" || fs[1].name != "efficio_20261117_0000.csv" {
		t.Fatalf("Dateien: %+v", fs)
	}
	if fs[0].rows != 3 || fs[1].rows != 1 || strings.Count(fs[0].content, "Zeitstempel") != 1 {
		t.Errorf("Zeilen %d/%d, Datei 1: %q", fs[0].rows, fs[1].rows, fs[0].content)
	}
}

func TestExporterMinuteGridAcrossMidnight(t *testing.T) {
	env := exportEnv(t)
	env.cfg.Interval = time.Minute
	m := env.instant(t)
	d15 := time.Date(2026, 11, 15, 23, 46, 0, 0, time.UTC)
	fillSeries(t, env.pool, m, d15, d15.Add(14*time.Minute), time.Minute)
	env.setState(t, time.Date(2026, 11, 15, 23, 45, 0, 0, time.UTC))
	env.now = time.Date(2026, 11, 16, 0, 2, 0, 0, time.UTC)
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Intervals != 15 || r.Rows != 15 {
		t.Fatalf("%+v", r)
	}
	fs := env.files(t)
	if len(fs) != 2 || fs[0].name != "efficio_20261115_0000.csv" || fs[1].name != "efficio_20261116_0000.csv" {
		t.Fatalf("Dateien: %+v", fs)
	}
	if fs[0].rows != 14 || fs[1].rows != 1 || !strings.Contains(fs[1].content, "16.11.2026 00:00;") {
		t.Errorf("Zeilen %d/%d, %q", fs[0].rows, fs[1].rows, fs[1].content)
	}
}

func TestExporterDSTDayThroughExporter(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	env := exportEnv(t)
	env.cfg.CSV.Loc = berlin
	m := env.instant(t)
	start := time.Date(2026, 10, 25, 0, 0, 0, 0, berlin)
	end := time.Date(2026, 10, 26, 0, 0, 0, 0, berlin)
	fillSeries(t, env.pool, m, start, end, 15*time.Minute)
	env.setState(t, time.Date(2026, 10, 24, 23, 45, 0, 0, berlin))
	env.now = end.Add(2 * time.Minute)
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if r.Intervals != 101 {
		t.Fatalf("%+v", r)
	}
	fs := env.files(t)
	if len(fs) != 2 || fs[0].name != "efficio_20261025_0000.csv" || fs[0].rows != 100 {
		t.Fatalf("Dateien: %+v", fs)
	}
	if strings.Count(fs[0].content, "25.10.2026 02:15;") != 2 || !strings.Contains(fs[0].content, "25.10.2026 23:45;") {
		t.Errorf("Inhalt %q", fs[0].content)
	}
	if !strings.Contains(fs[1].content, "26.10.2026 00:00;") {
		t.Errorf("Folgetag %q", fs[1].content)
	}
}

func TestRangeValuesMinuteDelta(t *testing.T) {
	pool, s, _, infra := samplerEnv(t)
	c, _ := source(t, pool, s, infra, KindCounter, 1, "1", time.Now())
	at := func(mi int) time.Time { return time.Date(2026, 11, 15, 10, mi, 0, 0, time.UTC) }
	putValue(t, pool, c, at(0), 5)
	putValue(t, pool, c, at(1), 7)
	putValue(t, pool, c, at(2), 7)
	meters, err := s.List(context.Background(), Filter{InfraID: infra})
	must(t, err)
	steps, err := s.RangeValues(context.Background(), meters, at(0), 3, time.Minute, ModeDelta)
	must(t, err)
	if len(steps[0].Missing) != 1 || len(steps[0].Points) != 0 { // Vorminute fehlt
		t.Errorf("Schritt 0: %+v", steps[0])
	}
	if len(steps[1].Points) != 1 || steps[1].Points[0].Value != 2 || steps[2].Points[0].Value != 0 {
		t.Errorf("Schritte: %+v %+v", steps[1], steps[2])
	}
}

func TestExporterRunOnStandbyRefused(t *testing.T) {
	env := exportEnv(t)
	env.e.Skip = func() bool { return true }
	if _, err := env.e.Run(context.Background(), env.cfg); !errors.Is(err, ErrStandby) {
		t.Errorf("err=%v", err)
	}
	if len(env.files(t)) != 0 {
		t.Error("auf dem Spiegel darf nichts entstehen")
	}
}
