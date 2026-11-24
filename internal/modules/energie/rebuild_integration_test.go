package energie

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fileRange struct {
	name               string
	firstEnd, lastEnd  *time.Time
	version, delivered int
	content            string
}

func (env *expEnv) fileRanges(t *testing.T) []fileRange {
	t.Helper()
	rows, err := env.pool.Query(context.Background(), `SELECT name, first_end, last_end, version, delivered_version, convert_from(content, 'UTF8')
		FROM energy_export_files WHERE connection_id = $1::uuid ORDER BY name`, env.conn)
	must(t, err)
	defer rows.Close()
	var out []fileRange
	for rows.Next() {
		var f fileRange
		must(t, rows.Scan(&f.name, &f.firstEnd, &f.lastEnd, &f.version, &f.delivered, &f.content))
		out = append(out, f)
	}
	return out
}

// twoRuns exportiert e0 und e0+15 min (Werte 1 und 2) in eine Tagesdatei.
func (env *expEnv) twoRuns(t *testing.T) (meterID, key string) {
	t.Helper()
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 2)
	_, _ = env.e.Run(context.Background(), env.cfg)
	env.now = env.now.Add(15 * time.Minute)
	_, _ = env.e.Run(context.Background(), env.cfg)
	return m, keyOf(t, env.s, m)
}

func TestExporterRecordsFileRange(t *testing.T) {
	env := exportEnv(t)
	env.twoRuns(t)
	fs := env.fileRanges(t)
	if len(fs) != 1 || fs[0].firstEnd == nil || fs[0].lastEnd == nil ||
		!fs[0].firstEnd.Equal(e0) || !fs[0].lastEnd.Equal(e0.Add(15*time.Minute)) {
		t.Fatalf("Dateien: %+v", fs)
	}
}

func TestRebuildUnsentFile(t *testing.T) {
	env := exportEnv(t)
	env.cfg.Target = &failTarget{fail: true}
	m, key := env.twoRuns(t)
	putValue(t, env.pool, m, e0, 5)
	putValue(t, env.pool, m, e0.Add(15*time.Minute), 6)
	before := env.fileRanges(t)[0]

	rebuilt, kept, err := env.e.Rebuild(context.Background(), env.cfg, e0.Add(-time.Hour), map[string]bool{key: true}, false)
	must(t, err)
	if len(rebuilt) != 1 || len(kept) != 0 || rebuilt[0].Name != before.name {
		t.Fatalf("neu %+v, bleibt %+v", rebuilt, kept)
	}
	after := env.fileRanges(t)[0]
	want := "Zeitstempel;Messstellen-ID;Messwert\r\n15.11.2026 10:15;" + key + ";5\r\n15.11.2026 10:30;" + key + ";6\r\n"
	if after.content != want || after.version != before.version+1 {
		t.Errorf("Datei nach dem Neuerzeugen: %q Version %d (vorher %d)", after.content, after.version, before.version)
	}
}

func TestRebuildKeepsDelivered(t *testing.T) {
	env := exportEnv(t)
	env.cfg.Target = &probeTarget{}
	m, key := env.twoRuns(t)
	putValue(t, env.pool, m, e0, 5)
	before := env.fileRanges(t)[0]
	if before.delivered == 0 {
		t.Fatal("Datei wurde nicht zugestellt")
	}
	rebuilt, kept, err := env.e.Rebuild(context.Background(), env.cfg, e0.Add(-time.Hour), map[string]bool{key: true}, false)
	must(t, err)
	if len(rebuilt) != 0 || len(kept) != 1 || !strings.Contains(kept[0].Reason, "schon verschickt") {
		t.Fatalf("neu %+v, bleibt %+v", rebuilt, kept)
	}
	if after := env.fileRanges(t)[0]; after.content != before.content || after.version != before.version {
		t.Error("zugestellte Datei wurde geändert")
	}
}

func TestRebuildPickupFetched(t *testing.T) {
	env := exportEnv(t)
	m, key := env.twoRuns(t)
	putValue(t, env.pool, m, e0, 5)
	keys := map[string]bool{key: true}
	rebuilt, kept, err := env.e.Rebuild(context.Background(), env.cfg, e0.Add(-time.Hour), keys, true)
	must(t, err)
	if len(rebuilt) != 1 || len(kept) != 0 {
		t.Fatalf("nicht abgeholt: neu %+v, bleibt %+v", rebuilt, kept)
	}
	if f := env.fileRanges(t)[0]; strings.Contains(f.content, ";5\r\n") {
		t.Error("Vorschau hat die Datei geändert")
	}
	_, err = env.pool.Exec(context.Background(), `UPDATE energy_export_files SET fetched_at = now() WHERE connection_id = $1::uuid`, env.conn)
	must(t, err)
	rebuilt, kept, err = env.e.Rebuild(context.Background(), env.cfg, e0.Add(-time.Hour), keys, true)
	must(t, err)
	if len(rebuilt) != 0 || len(kept) != 1 || !strings.Contains(kept[0].Reason, "schon verschickt") {
		t.Fatalf("abgeholt: neu %+v, bleibt %+v", rebuilt, kept)
	}
	// Ein Export ohne die Messstellen des Laufs bleibt außen vor.
	rebuilt, kept, err = env.e.Rebuild(context.Background(), env.cfg, e0.Add(-time.Hour), map[string]bool{"gibt-es-nicht": true}, true)
	must(t, err)
	if len(rebuilt)+len(kept) != 0 {
		t.Errorf("fremder Export: neu %+v, bleibt %+v", rebuilt, kept)
	}
}

func TestExporterPausesDuringRecalc(t *testing.T) {
	env := exportEnv(t)
	m := env.instant(t)
	putValue(t, env.pool, m, e0, 1)
	_, err := env.pool.Exec(context.Background(), `INSERT INTO energy_recalc_runs (meter_id, meter_ids, meter_keys, from_date, days_total, state)
		VALUES ($1::uuid, ARRAY[$1::uuid], ARRAY['X'], '2026-11-13', 3, 'exporting')`, m)
	must(t, err)
	r, err := env.e.Run(context.Background(), env.cfg)
	must(t, err)
	if len(env.files(t)) != 0 || r.Result != "nothing" || !strings.Contains(strings.Join(r.Notes, " "), "Neurechnung läuft") {
		t.Errorf("Lauf während der Neurechnung: %+v, %d Dateien", r, len(env.files(t)))
	}
}
