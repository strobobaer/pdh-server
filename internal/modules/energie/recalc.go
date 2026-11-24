package energie

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Rückwirkende Neurechnung. Eine Fassung mit einem Datum bis heute legt einen
// Lauf an (energy_recalc_runs). Der Lauf rechnet die Minutenwerte der
// Messstelle und aller, die auf ihr aufbauen, Tag für Tag neu – jeder Tag in
// einer eigenen Transaktion, mit denselben Regeln wie der Minutentakt
// (evalMinute). Bricht er ab, steht jeder Tag entweder ganz alt oder ganz neu
// da, und der nächste Durchlauf macht am ersten nicht geschriebenen Tag weiter.
//
// Solange der Lauf läuft, lässt der Minutentakt seine Messstellen aus. Der
// Schlussschritt rechnet unter derselben Sperre wie der Minutentakt
// (minuteLock) bis zur zuletzt abgelegten Minute, stellt Stand und Basis der
// Messstellen darauf und gibt sie frei (exporting). Danach erzeugt der Export
// die noch nicht verschickten Efficio-Dateien neu (done).

const (
	recalcInterval    = 30 * time.Second
	recalcMaxAttempts = 5
)

// Zustände eines Laufs.
const (
	RunRunning   = "running"
	RunExporting = "exporting"
	RunDone      = "done"
	RunFailed    = "failed"
)

// RecalcRun ist ein Lauf der Neurechnung. Progress in Prozent.
type RecalcRun struct {
	ID, MeterID, State, Message, CreatedBy string
	MeterKeys, FilesRebuilt, FilesKept     []string
	From                                   time.Time
	DoneUntil                              *time.Time
	DaysTotal, Progress                    int
	ValuesWritten, Missing                 int64
	CreatedAt                              time.Time
	FinishedAt                             *time.Time

	meterIDs []string
}

// RecalcPreview: was „Neu rechnen“ bewirken würde. NextFrom: ab diesem Tag
// gilt eine spätere Fassung (die neue gilt bis zum Tag davor).
type RecalcPreview struct {
	From, To      time.Time
	Days          int
	Meters        []string
	NextFrom      *time.Time
	Rebuild, Keep []FileImpact
}

// FileImpact: eine Efficio-Datei und was mit ihr geschieht.
type FileImpact struct {
	Connection, Name, Reason string
}

// Recalculator arbeitet die Läufe ab. Skip wahr: nichts tun (Spiegel, Wartung).
type Recalculator struct {
	Store       *Store
	Exporter    *Exporter
	Skip        func() bool
	MaxAttempts int // 0 = 5

	nudge    chan struct{}
	afterDay func(day time.Time) // nur für Tests: nach jedem geschriebenen Tag
}

func NewRecalculator(s *Store, ex *Exporter, skip func() bool) *Recalculator {
	return &Recalculator{Store: s, Exporter: ex, Skip: skip, nudge: make(chan struct{}, 1)}
}

// Nudge stößt den nächsten Durchlauf sofort an (blockiert nie).
func (rc *Recalculator) Nudge() {
	if rc == nil || rc.nudge == nil {
		return
	}
	select {
	case rc.nudge <- struct{}{}:
	default:
	}
}

func (rc *Recalculator) skip() bool { return rc.Skip != nil && rc.Skip() }

// Start arbeitet sofort, dann alle 30 s und nach jedem Anstoß, bis ctx endet.
func (rc *Recalculator) Start(ctx context.Context) {
	for {
		rc.safePass(ctx)
		t := time.NewTimer(recalcInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-rc.nudge:
			t.Stop()
		}
	}
}

func (rc *Recalculator) safePass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("energie: neurechnung abgestürzt")
		}
	}()
	if err := rc.Pass(ctx); err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("energie: neurechnung")
	}
}

const runCols = `id::text, COALESCE(meter_id::text, ''), meter_ids::text[], meter_keys, from_date, done_until,
	days_total, state, message, values_written, missing, files_rebuilt, files_kept, created_by_name, created_at, finished_at`

func scanRun(row pgx.Row) (*RecalcRun, error) {
	var r RecalcRun
	var rebuilt, kept string
	if err := row.Scan(&r.ID, &r.MeterID, &r.meterIDs, &r.MeterKeys, &r.From, &r.DoneUntil, &r.DaysTotal, &r.State,
		&r.Message, &r.ValuesWritten, &r.Missing, &rebuilt, &kept, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt); err != nil {
		return nil, err
	}
	r.From = asDay(r.From)
	if r.DoneUntil != nil {
		d := asDay(*r.DoneUntil)
		r.DoneUntil = &d
	}
	r.FilesRebuilt, r.FilesKept = splitLines(rebuilt), splitLines(kept)
	switch r.State {
	case RunDone:
		r.Progress = 100
	case RunExporting:
		r.Progress = 99
	default:
		if r.DoneUntil != nil && r.DaysTotal > 0 {
			r.Progress = (int(r.DoneUntil.Sub(r.From).Hours()/24) + 1) * 100 / r.DaysTotal
		}
		if r.Progress > 99 {
			r.Progress = 99
		}
	}
	return &r, nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// RunningRecalc liefert den Lauf, der gerade läuft oder exportiert (nil = keiner).
func (s *Store) RunningRecalc(ctx context.Context) (*RecalcRun, error) {
	r, err := scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM energy_recalc_runs
		WHERE state IN ('running', 'exporting') LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// RecalcRuns liefert die Läufe, die die Messstelle betrafen, neueste zuerst.
func (s *Store) RecalcRuns(ctx context.Context, meterID string, limit int) ([]RecalcRun, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+runCols+` FROM energy_recalc_runs
		WHERE $1::uuid = ANY(meter_ids) ORDER BY created_at DESC LIMIT $2`, meterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecalcRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Preview prüft eine Fassung wie SaveVersion mit „Neu rechnen“ und sagt, was
// der Lauf beträfe – ohne etwas zu schreiben.
func (rc *Recalculator) Preview(ctx context.Context, in VersionInput) (RecalcPreview, error) {
	in.Recalc = true
	_, info, err := rc.Store.saveVersion(ctx, in, "", true)
	if err != nil {
		return RecalcPreview{}, err
	}
	p := RecalcPreview{From: info.from, To: info.today, Days: info.days, Meters: info.keys, NextFrom: info.nextFrom}
	return p, nil
}

// Pass arbeitet am laufenden Lauf weiter, bis er fertig ist, ctx endet oder
// Skip wahr wird. Ein Fehler zählt als Fehlversuch; ab MaxAttempts endet der
// Lauf als abgebrochen und der Minutentakt rechnet die Messstellen wieder.
func (rc *Recalculator) Pass(ctx context.Context) error {
	if rc.skip() {
		return nil
	}
	run, err := rc.Store.RunningRecalc(ctx)
	if err != nil || run == nil {
		return err
	}
	err = rc.work(ctx, run)
	if err != nil && ctx.Err() == nil {
		if ferr := rc.fail(ctx, run, err); ferr != nil {
			log.Error().Err(ferr).Msg("energie: fehlversuch der neurechnung festhalten")
		}
	}
	return err
}

func (rc *Recalculator) fail(ctx context.Context, run *RecalcRun, cause error) error {
	max := rc.MaxAttempts
	if max <= 0 {
		max = recalcMaxAttempts
	}
	_, err := rc.Store.DB.Exec(ctx, `UPDATE energy_recalc_runs SET attempts = attempts + 1, message = $2,
			state = CASE WHEN attempts + 1 >= $3 THEN 'failed' ELSE state END,
			finished_at = CASE WHEN attempts + 1 >= $3 THEN now() ELSE finished_at END
		WHERE id = $1::uuid`, run.ID, cause.Error(), max)
	return err
}

// recMeter: eine Messstelle, wie die Neurechnung sie braucht.
type recMeter struct {
	id, key, kind, formula string
	vers                   []verRow
}

// formulaOn: die Formel, die am Tag day gilt.
func (m *recMeter) formulaOn(day time.Time) string {
	if i := effectiveIndex(m.vers, day); i >= 0 {
		return m.vers[i].formula
	}
	return m.formula
}

type recData struct {
	run   []*recMeter // Messstellen des Laufs
	inRun map[string]bool
	byKey map[string]*recMeter
}

func (rc *Recalculator) load(ctx context.Context, run *RecalcRun) (*recData, error) {
	d := &recData{inRun: map[string]bool{}, byKey: map[string]*recMeter{}}
	byID := map[string]*recMeter{}
	rows, err := rc.Store.DB.Query(ctx, `SELECT id::text, meter_key, kind, formula FROM energy_meters`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		m := &recMeter{}
		if err := rows.Scan(&m.id, &m.key, &m.kind, &m.formula); err != nil {
			rows.Close()
			return nil, err
		}
		d.byKey[m.key] = m
		byID[m.id] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	vr, err := rc.Store.DB.Query(ctx, `SELECT meter_id::text, id::text, valid_from, formula, created_at
		FROM energy_formula_versions WHERE meter_id = ANY($1::uuid[]) ORDER BY meter_id, valid_from, created_at, id`, run.meterIDs)
	if err != nil {
		return nil, err
	}
	defer vr.Close()
	for vr.Next() {
		var mid string
		var v verRow
		if err := vr.Scan(&mid, &v.id, &v.validFrom, &v.formula, &v.createdAt); err != nil {
			return nil, err
		}
		v.validFrom = asDay(v.validFrom)
		if m := byID[mid]; m != nil {
			m.vers = append(m.vers, v)
		}
	}
	if err := vr.Err(); err != nil {
		return nil, err
	}
	for _, id := range run.meterIDs {
		if m := byID[id]; m != nil {
			d.run = append(d.run, m)
			d.inRun[id] = true
		}
	}
	return d, nil
}

func (rc *Recalculator) work(ctx context.Context, run *RecalcRun) error {
	if run.State == RunRunning {
		s := rc.Store
		loc := s.loc()
		data, err := rc.load(ctx, run)
		if err != nil {
			return err
		}
		next := run.From
		if run.DoneUntil != nil {
			next = run.DoneUntil.AddDate(0, 0, 1)
		}
		// Ganze Tage, an denen der Minutentakt sicher nichts mehr nachholt,
		// je in einer Transaktion; den Rest rechnet der Schlussschritt.
		cutoff := s.now().Add(-samplerCatchUp - lateGrace)
		for !dayStart(next.AddDate(0, 0, 1), loc).After(cutoff) {
			if err := rc.recalcDay(ctx, run, data, next); err != nil {
				return err
			}
			if rc.afterDay != nil {
				rc.afterDay(next)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if rc.skip() {
				return nil
			}
			next = next.AddDate(0, 0, 1)
		}
		if err := rc.finish(ctx, run, data, next); err != nil {
			return err
		}
	}
	return rc.export(ctx, run)
}

// recalcDay rechnet den Tag day neu und vermerkt ihn am Lauf.
func (rc *Recalculator) recalcDay(ctx context.Context, run *RecalcRun, data *recData, day time.Time) error {
	tx, err := rc.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	loc := rc.Store.loc()
	written, missing, _, err := rc.compute(ctx, tx, data, day, dayStart(day, loc), dayStart(day.AddDate(0, 0, 1), loc))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE energy_recalc_runs SET done_until = $2::date,
			values_written = values_written + $3, missing = missing + $4 WHERE id = $1::uuid`,
		run.ID, sqlDay(day), written, missing); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// finish rechnet ab dem Tag next bis zur zuletzt abgelegten Minute E – unter
// minuteLock, damit der Minutentakt nicht gleichzeitig schreibt –, stellt
// Stand und Basis der Messstellen auf das Ende und gibt sie frei.
func (rc *Recalculator) finish(ctx context.Context, run *RecalcRun, data *recData, next time.Time) error {
	s := rc.Store
	loc := s.loc()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockMinute(ctx, tx); err != nil {
		return err
	}
	// E: jüngster Stand der übrigen aktiven Messstellen – dort setzt der
	// Minutentakt fort. Ohne solche: das Ende der letzten vollen Minute.
	now := s.now().Truncate(time.Minute)
	var e *time.Time
	if err := tx.QueryRow(ctx, `SELECT max(last_at) FROM energy_meters
		WHERE active AND NOT (id = ANY($1::uuid[])) AND last_at <= $2`, run.meterIDs, now).Scan(&e); err != nil {
		return err
	}
	end := now
	if e != nil {
		end = *e
	}
	var written, missing int64
	var states map[string]*recState
	for d := next; dayStart(d, loc).Before(end); d = d.AddDate(0, 0, 1) {
		hi := dayStart(d.AddDate(0, 0, 1), loc)
		if hi.After(end) {
			hi = end
		}
		w, m, st, err := rc.compute(ctx, tx, data, d, dayStart(d, loc), hi)
		if err != nil {
			return err
		}
		written, missing, states = written+w, missing+m, st
	}
	if states == nil {
		if states, err = startStates(ctx, tx, data, end); err != nil {
			return err
		}
	}
	for _, m := range data.run {
		st := states[m.id]
		if st == nil {
			continue
		}
		var lastV *float64
		var lastAt, base *time.Time
		if st.hasLast {
			v, ts := st.last.v, st.last.ts
			lastV, lastAt = &v, &ts
		}
		if !st.base.IsZero() {
			b := st.base
			base = &b
		}
		if _, err := tx.Exec(ctx, `UPDATE energy_meters SET last_value = $2, last_at = $3, base_at = $4 WHERE id = $1::uuid`,
			m.id, lastV, lastAt, base); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE energy_recalc_runs SET state = 'exporting', done_until = $2::date,
			values_written = values_written + $3, missing = missing + $4 WHERE id = $1::uuid`,
		run.ID, sqlDay(s.today()), written, missing); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	run.State = RunExporting
	return nil
}

// export erzeugt nach der Neurechnung die noch nicht verschickten
// Efficio-Dateien neu und schließt den Lauf ab.
func (rc *Recalculator) export(ctx context.Context, run *RecalcRun) error {
	var rebuilt, kept []string
	if rc.Exporter != nil {
		r, k, err := rc.rebuildAll(ctx, run.From, run.MeterKeys, false)
		if err != nil {
			return err
		}
		rebuilt, kept = impactLines(r), impactLines(k)
	}
	_, err := rc.Store.DB.Exec(ctx, `UPDATE energy_recalc_runs SET state = 'done', finished_at = now(),
			files_rebuilt = $2, files_kept = $3 WHERE id = $1::uuid AND state = 'exporting'`,
		run.ID, strings.Join(rebuilt, "\n"), strings.Join(kept, "\n"))
	return err
}

// impactLines: je Datei eine Zeile „Name (Verbindung) – Grund“.
func impactLines(l []FileImpact) []string {
	out := make([]string, len(l))
	for i, f := range l {
		out[i] = fmt.Sprintf("%s (%s)", f.Name, f.Connection)
		if f.Reason != "" {
			out[i] += " – " + f.Reason
		}
	}
	return out
}

// rebuildAll: Efficio-Dateien aller Exporte, die eine der Messstellen keys
// enthalten, ab dem Tag from (Task 5 füllt das).
func (rc *Recalculator) rebuildAll(ctx context.Context, from time.Time, keys []string, dry bool) (rebuilt, kept []FileImpact, err error) {
	return nil, nil, nil
}

// recState: Stand einer Laufmessstelle – letzter Wert und Basiszeitpunkt.
type recState struct {
	last    stamped
	hasLast bool
	base    time.Time
}

// startStates liest je Laufmessstelle den letzten Wert bis at; sein
// Zeitpunkt ist die Basis.
func startStates(ctx context.Context, q querier, data *recData, at time.Time) (map[string]*recState, error) {
	ids := make([]string, len(data.run))
	out := map[string]*recState{}
	for i, m := range data.run {
		ids[i] = m.id
		out[m.id] = &recState{}
	}
	rows, err := q.Query(ctx, `
		SELECT u.id::text, v.ts, v.value
		  FROM unnest($1::uuid[]) u(id)
		  JOIN LATERAL (SELECT ts, value FROM energy_values
		                 WHERE meter_id = u.id AND ts <= $2 ORDER BY ts DESC LIMIT 1) v ON true`, ids, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var st stamped
		if err := rows.Scan(&id, &st.ts, &st.v); err != nil {
			return nil, err
		}
		out[id].last, out[id].hasLast, out[id].base = st, true, st.ts
	}
	return out, rows.Err()
}

// compute rechnet die Laufmessstellen für die Minuten mit Ende in (lo, hi]
// mit den Formeln des Tages day neu und ersetzt ihre Werte in diesem Bereich.
// Ausgangslage ist der Stand in der Datenbank (letzter Wert bis lo).
func (rc *Recalculator) compute(ctx context.Context, tx pgx.Tx, data *recData, day, lo, hi time.Time) (written, missing int64, states map[string]*recState, err error) {
	// Formeln des Tages und ihre Reihenfolge.
	formulas := map[string]string{}
	parsed := map[string]*Formula{}
	for _, m := range data.run {
		src := m.formulaOn(day)
		if src == "" {
			continue
		}
		f, err := ParseFormula(src)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("Formel von [%s] am %s: %w", m.key, day.Format("02.01.2006"), err)
		}
		formulas[m.key], parsed[m.key] = src, f
	}
	order, err := EvalOrder(formulas)
	if err != nil {
		return 0, 0, nil, err
	}

	// Werte der Bezüge außerhalb des Laufs in (lo, hi].
	series := map[string]map[int64]float64{}
	var refIDs []string
	idKey := map[string]string{}
	for _, f := range parsed {
		for _, ref := range f.Refs() {
			if r := data.byKey[ref]; r != nil && !data.inRun[r.id] && idKey[r.id] == "" {
				idKey[r.id] = r.key
				refIDs = append(refIDs, r.id)
			}
		}
	}
	for _, m := range data.run {
		series[m.key] = map[int64]float64{}
	}
	if len(refIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT meter_id::text, ts, value FROM energy_values
			WHERE meter_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3`, refIDs, lo, hi)
		if err != nil {
			return 0, 0, nil, err
		}
		for rows.Next() {
			var id string
			var ts time.Time
			var v float64
			if err := rows.Scan(&id, &ts, &v); err != nil {
				rows.Close()
				return 0, 0, nil, err
			}
			k := idKey[id]
			if series[k] == nil {
				series[k] = map[int64]float64{}
			}
			series[k][ts.Unix()] = v
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, 0, nil, err
		}
	}

	states, err = startStates(ctx, tx, data, lo)
	if err != nil {
		return 0, 0, nil, err
	}
	// Werte zur Basis: im Bereich aus series, davor aus der Datenbank.
	type baseKey struct {
		key string
		ts  int64
	}
	baseCache := map[baseKey]*float64{}
	baseVal := func(key string, ts time.Time) (float64, bool, error) {
		if ts.After(lo) {
			v, ok := series[key][ts.Unix()]
			return v, ok, nil
		}
		bk := baseKey{key, ts.Unix()}
		if p, ok := baseCache[bk]; ok {
			if p == nil {
				return 0, false, nil
			}
			return *p, true, nil
		}
		var v float64
		err := tx.QueryRow(ctx, `SELECT value FROM energy_values WHERE meter_id = $1::uuid AND ts = $2`, data.byKey[key].id, ts).Scan(&v)
		if errors.Is(err, pgx.ErrNoRows) {
			baseCache[bk] = nil
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		baseCache[bk] = &v
		return v, true, nil
	}

	var wIDs []string
	var wTS []time.Time
	var wVals []float64
	var qerr error
	for ts := lo.Add(time.Minute); !ts.After(hi); ts = ts.Add(time.Minute) {
		u := ts.Unix()
		for _, key := range order {
			m := data.byKey[key]
			st := states[m.id]
			hasBase := !st.base.IsZero()
			v, ok, rebase := evalMinute(parsed[key], m.kind, func(ref string) (refValue, bool) {
				r := data.byKey[ref]
				if r == nil {
					return refValue{}, false
				}
				now, have := series[ref][u]
				rv := refValue{Kind: r.kind, Now: now, HasNow: have}
				if r.kind == KindCounter && hasBase {
					b, hb, err := baseVal(ref, st.base)
					if err != nil {
						qerr = err
					}
					rv.Base, rv.HasBase = b, hb
				}
				return rv, true
			}, hasBase, st.last.v)
			if qerr != nil {
				return 0, 0, nil, qerr
			}
			switch {
			case ok:
				series[key][u] = v
				st.last, st.hasLast, st.base = stamped{v: v, ts: ts}, true, ts
				wIDs, wTS, wVals = append(wIDs, m.id), append(wTS, ts), append(wVals, v)
			case rebase:
				st.base = ts
				missing++
			default:
				missing++
			}
		}
	}

	ids := make([]string, len(data.run))
	for i, m := range data.run {
		ids[i] = m.id
	}
	if _, err := tx.Exec(ctx, `DELETE FROM energy_values WHERE meter_id = ANY($1::uuid[]) AND ts > $2 AND ts <= $3`, ids, lo, hi); err != nil {
		return 0, 0, nil, err
	}
	if len(wIDs) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO energy_values (meter_id, ts, value)
			SELECT * FROM unnest($1::uuid[], $2::timestamptz[], $3::float8[])`, wIDs, wTS, wVals); err != nil {
			return 0, 0, nil, err
		}
	}
	return int64(len(wIDs)), missing, states, nil
}
