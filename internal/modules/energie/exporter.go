package energie

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

const (
	exporterInterval   = 60 * time.Second
	defaultInterval    = time.Minute
	defaultRunEvery    = 15 * time.Minute
	defaultRetention   = 30
	maxRunMessageRunes = 4000
)

// Target ist ein Ziel für fertige Dateien.
type Target interface {
	Label() string
	Put(ctx context.Context, name string, content []byte) error
	Test(ctx context.Context) (string, error)
}

// PickupTarget: die Dateien bleiben im PDH und werden dort abgeholt.
type PickupTarget struct{}

func (PickupTarget) Label() string                             { return "Zum Abholen im PDH" }
func (PickupTarget) Put(context.Context, string, []byte) error { return nil }
func (PickupTarget) Test(context.Context) (string, error) {
	return "Die Dateien liegen im PDH zum Abholen bereit.", nil
}

// ExportConfig ist die Einrichtung eines Efficio-Exports.
type ExportConfig struct {
	ConnectionID, Prefix, Mode string
	Interval                   time.Duration // Werteraster: Abstand der Zeilen (Vorgabe 1 min)
	RunEvery                   time.Duration // Lauf-Takt: wie oft die Datei ergänzt wird (Vorgabe 15 min)
	FileDays, RetentionDays    int
	CSV                        CSVOptions
	InfraIDs, MeterKeys        []string // leer: alle Messstellen mit „exportieren“
	Target                     Target
}

// RunResult ist das Ergebnis eines Exportlaufs.
type RunResult struct {
	Intervals, Rows, Missing, Delivered int
	Files, Notes                        []string
	Result                              string // ok | partial | error | nothing
}

// Exporter schreibt die Intervallwerte als CSV in Dateien und stellt sie zu.
type Exporter struct {
	Store      *Store
	Configs    func(ctx context.Context) ([]ExportConfig, error)
	Skip       func() bool
	Delay      time.Duration // Wartezeit nach Intervallende (2 min)
	MaxCatchUp time.Duration // längstes Nachholen (7 × 24 h)
}

func (e *Exporter) delay() time.Duration {
	if e.Delay > 0 {
		return e.Delay
	}
	return 2 * time.Minute
}

func (e *Exporter) maxCatchUp() time.Duration {
	if e.MaxCatchUp > 0 {
		return e.MaxCatchUp
	}
	return 7 * 24 * time.Hour
}

func (c ExportConfig) normalized() ExportConfig {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.RunEvery <= 0 {
		c.RunEvery = defaultRunEvery
	}
	if c.Interval > c.RunEvery { // das Raster ist nie gröber als der Lauf-Takt
		c.Interval = c.RunEvery
	}
	if c.Mode != ModeDelta {
		c.Mode = ModeEnd
	}
	if c.Prefix == "" {
		c.Prefix = "efficio"
	}
	if c.RetentionDays <= 0 {
		c.RetentionDays = defaultRetention
	}
	if c.Target == nil {
		c.Target = PickupTarget{}
	}
	if c.CSV.Loc == nil {
		c.CSV.Loc = time.Local
	}
	return c
}

// Run exportiert alle fälligen Intervalle einer Verbindung und stellt die
// Dateien zu. Das Ergebnis steht im Protokoll (auch „nichts zu tun“).
func (e *Exporter) Run(ctx context.Context, c ExportConfig) (RunResult, error) {
	return e.run(ctx, c, true)
}

func (e *Exporter) run(ctx context.Context, c ExportConfig, logNothing bool) (res RunResult, err error) {
	c = c.normalized()
	started := e.Store.now()
	defer func() {
		if err != nil {
			res.Result = "error"
		}
		if res.Result == "nothing" && !logNothing {
			return
		}
		if lerr := e.logRun(ctx, c, started, res, err); lerr != nil && err == nil {
			err = lerr
		}
		if perr := e.purge(ctx, c); perr != nil && err == nil {
			err = perr
		}
	}()
	if err = e.generate(ctx, c, &res); err != nil {
		return res, err
	}
	derr := e.deliver(ctx, c, &res)
	switch {
	case derr != nil:
		err = derr
	case res.Intervals == 0 && res.Delivered == 0:
		res.Result = "nothing"
	case res.Missing > 0:
		res.Result = "partial"
	default:
		res.Result = "ok"
	}
	return res, err
}

// meters liefert die Messstellen dieses Exports.
func (e *Exporter) meters(ctx context.Context, c ExportConfig) ([]Meter, error) {
	yes := true
	all, err := e.Store.List(ctx, Filter{Export: &yes, Active: &yes})
	if err != nil {
		return nil, err
	}
	if len(c.InfraIDs) == 0 && len(c.MeterKeys) == 0 {
		return all, nil
	}
	allowed := map[string]bool{}
	for _, k := range c.MeterKeys {
		allowed[strings.TrimSpace(k)] = true
	}
	for _, id := range c.InfraIDs {
		sub, err := e.Store.List(ctx, Filter{InfraID: id, Export: &yes, Active: &yes})
		if err != nil {
			return nil, err
		}
		for _, m := range sub {
			allowed[m.Key] = true
		}
	}
	var out []Meter
	for _, m := range all {
		if allowed[m.Key] {
			out = append(out, m)
		}
	}
	return out, nil
}

type fileAcc struct {
	name       string
	start, end time.Time
	existing   bool
	buf        []byte
	rows       int
}

// generate legt alle fälligen Intervalle unter Sperre des Zustands in die
// Dateien. Zwei gleichzeitige Läufe warten aufeinander; jedes Intervall
// kommt genau einmal in eine Datei.
func (e *Exporter) generate(ctx context.Context, c ExportConfig, res *RunResult) error {
	meters, err := e.meters(ctx, c)
	if err != nil {
		return err
	}
	now := e.Store.now()
	tmax := now.Add(-e.delay()).Truncate(c.RunEvery)
	loc := c.CSV.Loc

	tx, err := e.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Fehlt der Zustand, beginnt der Export mit dem letzten abgeschlossenen Intervall.
	if _, err := tx.Exec(ctx, `INSERT INTO energy_export_state (connection_id, last_interval_end)
		VALUES ($1::uuid, $2) ON CONFLICT (connection_id) DO NOTHING`, c.ConnectionID, tmax.Add(-c.RunEvery)); err != nil {
		return err
	}
	var last time.Time
	if err := tx.QueryRow(ctx, `SELECT last_interval_end FROM energy_export_state
		WHERE connection_id = $1::uuid FOR UPDATE`, c.ConnectionID).Scan(&last); err != nil {
		return err
	}

	first := last.Truncate(c.Interval).Add(c.Interval) // last liegt auf dem Lauf-Takt
	if earliest := tmax.Add(-e.maxCatchUp()); first.Before(earliest) {
		skipped := first
		first = earliest.Truncate(c.Interval)
		if first.Before(earliest) {
			first = first.Add(c.Interval)
		}
		res.Notes = append(res.Notes, fmt.Sprintf("Zu lange her: Intervalle bis %s wurden nicht mehr nachgeholt (ab %s)",
			first.In(loc).Format(timeLayoutDMY), skipped.In(loc).Format(timeLayoutDMY)))
	}
	if first.After(tmax) {
		return nil
	}

	var accs []*fileAcc
	byName := map[string]*fileAcc{}
	acc := func(T time.Time) (*fileAcc, error) {
		if c.FileDays <= 0 {
			// eine Datei je Lauf, Name aus dem letzten Intervallende
			name := FileName(c.Prefix, tmax, loc)
			a := byName[name]
			if a == nil {
				a = &fileAcc{name: name, start: tmax, end: tmax}
				byName[name], accs = a, append(accs, a)
			}
			return a, nil
		}
		// Eine schon begonnene Datei, die T abdeckt (auch mehrtägig), wird weitergeführt.
		var name string
		var ps, pe time.Time
		err := tx.QueryRow(ctx, `SELECT name, period_start, period_end FROM energy_export_files
			WHERE connection_id = $1::uuid AND period_start <= $2 AND period_end > $2
			ORDER BY period_start DESC LIMIT 1`, c.ConnectionID, T).Scan(&name, &ps, &pe)
		existing := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			ps, pe = FilePeriod(T, c.FileDays, loc)
			name = FileName(c.Prefix, ps, loc)
		} else if err != nil {
			return nil, err
		}
		a := byName[name]
		if a == nil {
			a = &fileAcc{name: name, start: ps, end: pe, existing: existing}
			byName[name], accs = a, append(accs, a)
		}
		return a, nil
	}

	for T := first; !T.After(tmax); T = T.Add(c.Interval) {
		pts, missing, err := e.Store.IntervalValues(ctx, meters, T, c.Interval, c.Mode)
		if err != nil {
			return err
		}
		res.Intervals++
		res.Rows += len(pts)
		res.Missing += len(missing)
		if len(missing) > 0 {
			ids := make([]string, len(missing))
			for i, k := range missing {
				ids[i] = "[" + k + "]"
			}
			res.Notes = append(res.Notes, fmt.Sprintf("%s: kein Wert für %s",
				T.In(loc).Format(timeLayoutDMY), strings.Join(ids, ", ")))
		}
		if len(pts) == 0 {
			continue
		}
		data, err := FormatRows(pts, c.CSV)
		if err != nil {
			return err
		}
		a, err := acc(T)
		if err != nil {
			return err
		}
		a.buf = append(a.buf, data...)
		a.rows += len(pts)
	}

	for _, a := range accs {
		if a.existing {
			_, err = tx.Exec(ctx, `UPDATE energy_export_files
				SET content = content || $3, rows = rows + $4, version = version + 1, updated_at = now()
				WHERE connection_id = $1::uuid AND name = $2`, c.ConnectionID, a.name, a.buf, a.rows)
		} else {
			content := a.buf
			if c.CSV.Header {
				content = joinCSV(HeaderLine(c.CSV), a.buf)
			}
			_, err = tx.Exec(ctx, `INSERT INTO energy_export_files (connection_id, name, period_start, period_end, content, rows)
				VALUES ($1::uuid, $2, $3, $4, $5, $6)`, c.ConnectionID, a.name, a.start, a.end, content, a.rows)
		}
		if err != nil {
			return err
		}
		res.Files = append(res.Files, a.name)
	}
	if _, err := tx.Exec(ctx, `UPDATE energy_export_state SET last_interval_end = $2 WHERE connection_id = $1::uuid`,
		c.ConnectionID, tmax); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deliver stellt alle Dateien zu, deren Stand noch nicht zugestellt ist.
// Scheitert eine, bleibt der Fehler an der Datei; der nächste Lauf legt nach.
func (e *Exporter) deliver(ctx context.Context, c ExportConfig, res *RunResult) error {
	type pending struct {
		id, name string
		content  []byte
		version  int
	}
	rows, err := e.Store.DB.Query(ctx, `SELECT id::text, name, content, version FROM energy_export_files
		WHERE connection_id = $1::uuid AND version > delivered_version ORDER BY created_at, name`, c.ConnectionID)
	if err != nil {
		return err
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.name, &p.content, &p.version); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var errs []error
	for _, p := range todo {
		if perr := c.Target.Put(ctx, p.name, p.content); perr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.name, perr))
			res.Notes = append(res.Notes, fmt.Sprintf("Zustellung von %s fehlgeschlagen: %v", p.name, perr))
			if _, err := e.Store.DB.Exec(context.WithoutCancel(ctx), `UPDATE energy_export_files SET last_error = $2 WHERE id = $1::uuid`,
				p.id, perr.Error()); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if _, err := e.Store.DB.Exec(ctx, `UPDATE energy_export_files
			SET delivered_version = GREATEST(delivered_version, $2), delivered_at = now(), last_error = ''
			WHERE id = $1::uuid`, p.id, p.version); err != nil {
			errs = append(errs, err)
			continue
		}
		res.Delivered++
	}
	return errors.Join(errs...)
}

func (e *Exporter) logRun(ctx context.Context, c ExportConfig, started time.Time, res RunResult, runErr error) error {
	msg := strings.Join(res.Notes, "\n")
	if runErr != nil {
		if msg != "" {
			msg += "\n"
		}
		msg += runErr.Error()
	}
	if utf8.RuneCountInString(msg) > maxRunMessageRunes {
		msg = string([]rune(msg)[:maxRunMessageRunes]) + " …"
	}
	_, err := e.Store.DB.Exec(context.WithoutCancel(ctx), `INSERT INTO energy_export_runs
		(connection_id, started_at, finished_at, intervals, rows, missing, target, files, result, message)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		c.ConnectionID, started, e.Store.now(), res.Intervals, res.Rows, res.Missing,
		c.Target.Label(), strings.Join(res.Files, ", "), res.Result, msg)
	return err
}

// purge löscht Dateien und Protokoll, die älter als die Aufbewahrungszeit sind.
func (e *Exporter) purge(ctx context.Context, c ExportConfig) error {
	cutoff := e.Store.now().AddDate(0, 0, -c.RetentionDays)
	if _, err := e.Store.DB.Exec(ctx, `DELETE FROM energy_export_files
		WHERE connection_id = $1::uuid AND period_end < $2`, c.ConnectionID, cutoff); err != nil {
		return err
	}
	_, err := e.Store.DB.Exec(ctx, `DELETE FROM energy_export_runs
		WHERE connection_id = $1::uuid AND started_at < $2`, c.ConnectionID, cutoff)
	return err
}

// Pass führt alle eingerichteten Exporte einmal aus. Fehler eines Exports
// stoppen die anderen nicht. Läufe ohne neues Intervall werden nicht protokolliert.
func (e *Exporter) Pass(ctx context.Context) error {
	if e.Skip != nil && e.Skip() {
		return nil
	}
	if e.Configs == nil {
		return nil
	}
	cfgs, err := e.Configs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cfgs {
		if _, err := e.run(ctx, c, false); err != nil {
			errs = append(errs, fmt.Errorf("export %s: %w", c.ConnectionID, err))
		}
	}
	return errors.Join(errs...)
}

// Start führt im Takt von 60 s die Exporte aus, bis ctx endet. Eine Panik in
// einem Durchlauf wird protokolliert, die Schleife läuft weiter.
func (e *Exporter) Start(ctx context.Context) {
	t := time.NewTicker(exporterInterval)
	defer t.Stop()
	for {
		e.safePass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (e *Exporter) safePass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("energie: export abgestürzt")
		}
	}()
	if err := e.Pass(ctx); err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("energie: export")
	}
}
