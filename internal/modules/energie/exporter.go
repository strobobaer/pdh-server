package energie

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
)

const (
	exporterInterval   = 60 * time.Second
	defaultInterval    = time.Minute
	defaultRunEvery    = 15 * time.Minute
	defaultRetention   = 30
	maxRunMessageRunes = 4000
)

// ErrStandby: auf einem Server im Bereitschaftsbetrieb (Spiegel) wird nicht exportiert.
var ErrStandby = errors.New("dieser Server ist nicht der aktive Server – hier kann nicht exportiert werden")

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
	Store       *Store
	Configs     func(ctx context.Context) ([]ExportConfig, error)
	Skip        func() bool
	Delay       time.Duration // Wartezeit nach Intervallende (2 min)
	MaxCatchUp  time.Duration // längstes Nachholen (7 × 24 h)
	DeliverWait time.Duration // Warten auf einen anderen Lauf, der gerade zustellt (5 s)

	mu      sync.Mutex
	running map[string]bool // Verbindungen mit laufendem Exportlauf im Takt
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
	if c.CSV.TimeBase == "" {
		c.CSV.TimeBase = TimeBaseLocal
	}
	return c
}

// Run exportiert alle fälligen Intervalle einer Verbindung und stellt die
// Dateien zu. Das Ergebnis steht im Protokoll (auch „nichts zu tun“).
func (e *Exporter) Run(ctx context.Context, c ExportConfig) (RunResult, error) {
	if e.Skip != nil && e.Skip() {
		return RunResult{}, ErrStandby
	}
	return e.run(ctx, c, true)
}

func (e *Exporter) run(ctx context.Context, c ExportConfig, logNothing bool) (res RunResult, err error) {
	c = c.normalized()
	started := e.Store.now()
	// Während einer Neurechnung bleibt jede Datei, wie sie ist: danach erzeugt
	// die Neurechnung die nicht verschickten neu, und der Export macht weiter.
	if _, active, aerr := activeRun(ctx, e.Store.DB); aerr != nil {
		return res, aerr
	} else if active {
		res.Result = "nothing"
		res.Notes = []string{"Neurechnung läuft – der Export folgt, sobald sie fertig ist."}
		if logNothing { // nur der von Hand gestartete Lauf steht im Protokoll
			err = e.logRun(ctx, c, started, res, nil)
		}
		return res, err
	}
	defer func() {
		if err != nil {
			res.Result = "error"
		}
		if res.Result == "nothing" && !logNothing && len(res.Notes) == 0 {
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

// maxGapNotes begrenzt die Zahl der Notizen zu Lücken je Lauf.
const maxGapNotes = 50

type gap struct {
	key         string
	start, last time.Time
	n           int
}

// gapNotes fasst fehlende Werte je Messstelle zu zusammenhängenden Lücken zusammen.
type gapNotes struct {
	interval time.Duration
	loc      *time.Location
	open     map[string]*gap
	done     []*gap
}

func newGapNotes(interval time.Duration, loc *time.Location) *gapNotes {
	return &gapNotes{interval: interval, loc: loc, open: map[string]*gap{}}
}

func (g *gapNotes) add(s Step) {
	for _, k := range s.Missing {
		o := g.open[k]
		if o != nil && o.last.Equal(s.End.Add(-g.interval)) {
			o.last = s.End
			o.n++
			continue
		}
		if o != nil {
			g.done = append(g.done, o)
		}
		g.open[k] = &gap{key: k, start: s.End, last: s.End, n: 1}
	}
}

func (g *gapNotes) flush() []string {
	for k, o := range g.open {
		g.done = append(g.done, o)
		delete(g.open, k)
	}
	sort.Slice(g.done, func(i, j int) bool {
		if !g.done[i].start.Equal(g.done[j].start) {
			return g.done[i].start.Before(g.done[j].start)
		}
		return g.done[i].key < g.done[j].key
	})
	var out []string
	for i, o := range g.done {
		if i == maxGapNotes {
			out = append(out, fmt.Sprintf("… und %d weitere", len(g.done)-maxGapNotes))
			break
		}
		out = append(out, g.format(o))
	}
	g.done = nil
	return out
}

func (g *gapNotes) format(o *gap) string {
	s, l := o.start.In(g.loc), o.last.In(g.loc)
	if o.n == 1 {
		return fmt.Sprintf("[%s] ohne Wert %s", o.key, s.Format(timeLayoutDMY))
	}
	mins := int(time.Duration(o.n) * g.interval / time.Minute)
	if s.YearDay() == l.YearDay() && s.Year() == l.Year() {
		return fmt.Sprintf("[%s] ohne Wert %s–%s (%d Minuten)", o.key, s.Format(timeLayoutDMY), l.Format("15:04"), mins)
	}
	return fmt.Sprintf("[%s] ohne Wert %s – %s (%d Minuten)", o.key, s.Format(timeLayoutDMY), l.Format(timeLayoutDMY), mins)
}

// generate legt alle fälligen Intervalle in die Dateien, in Blöcken von
// höchstens einem Ortstag. Jeder Block ist eine eigene Transaktion unter
// Sperre des Zustands und schreibt seinen Fortschritt fest. Zwei gleichzeitige
// Läufe wechseln sich blockweise ab; jedes Intervall kommt genau einmal in
// eine Datei.
func (e *Exporter) generate(ctx context.Context, c ExportConfig, res *RunResult) error {
	meters, err := e.meters(ctx, c)
	if err != nil {
		return err
	}
	tmax := e.Store.now().Add(-e.delay()).Truncate(c.RunEvery)
	gaps := newGapNotes(c.Interval, c.CSV.loc())
	defer func() { res.Notes = append(res.Notes, gaps.flush()...) }()
	for {
		more, err := e.block(ctx, c, meters, tmax, res, gaps)
		if err != nil || !more {
			return err
		}
	}
}

// block verarbeitet den nächsten Block; false: es ist nichts mehr fällig.
func (e *Exporter) block(ctx context.Context, c ExportConfig, meters []Meter, tmax time.Time, res *RunResult, gaps *gapNotes) (bool, error) {
	loc := c.CSV.loc() // Tagesgrenzen und Dateinamen im gewählten Zeitbezug
	tx, err := e.Store.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Fehlt der Zustand, beginnt der Export mit dem letzten abgeschlossenen Lauf-Takt.
	if _, err := tx.Exec(ctx, `INSERT INTO energy_export_state (connection_id, last_interval_end)
		VALUES ($1::uuid, $2) ON CONFLICT (connection_id) DO NOTHING`, c.ConnectionID, tmax.Add(-c.RunEvery)); err != nil {
		return false, err
	}
	var last time.Time
	if err := tx.QueryRow(ctx, `SELECT last_interval_end FROM energy_export_state
		WHERE connection_id = $1::uuid FOR UPDATE`, c.ConnectionID).Scan(&last); err != nil {
		return false, err
	}
	first := last.Truncate(c.Interval).Add(c.Interval) // last liegt auf dem Lauf-Takt
	if earliest := tmax.Add(-e.maxCatchUp()); first.Before(earliest) {
		skipped := first
		first = earliest.Truncate(c.Interval)
		if first.Before(earliest) {
			first = first.Add(c.Interval)
		}
		res.Notes = append(res.Notes, fmt.Sprintf("Zu lange her: Intervalle von %s bis %s wurden nicht mehr nachgeholt",
			skipped.In(loc).Format(timeLayoutDMY), first.Add(-c.Interval).In(loc).Format(timeLayoutDMY)))
	}
	if first.After(tmax) {
		return false, nil
	}

	// Ein Block reicht bis Ortsmitternacht (die Zeile um 00:00 gehört schon zum Folgetag).
	_, dayEnd := FilePeriod(first, 1, loc)
	n := int((dayEnd.Sub(first)-1)/c.Interval) + 1
	if m := int(tmax.Sub(first)/c.Interval) + 1; m < n {
		n = m
	}
	steps, err := e.Store.RangeValues(ctx, meters, first, n, c.Interval, c.Mode)
	if err != nil {
		return false, err
	}
	var buf []byte
	rows := 0
	for _, s := range steps {
		res.Intervals++
		res.Rows += len(s.Points)
		res.Missing += len(s.Missing)
		gaps.add(s)
		if len(s.Points) == 0 {
			continue
		}
		data, err := FormatRows(s.Points, c.CSV)
		if err != nil {
			return false, err
		}
		buf = append(buf, data...)
		rows += len(s.Points)
	}

	if rows > 0 {
		// Die Datei für den ganzen Block: eine schon begonnene (auch mehrtägig)
		// mit demselben Zeitbezug wird weitergeführt, sonst entsteht eine neue.
		// Nach einem Wechsel des Zeitbezugs enthält so keine Datei gemischte
		// Zeitstempel.
		tb := c.CSV.TimeBase
		var name string
		var ps, pe time.Time
		var qerr error
		if c.FileDays <= 0 {
			ps, pe = tmax, tmax // eine Datei je Lauf, Name aus dem letzten Lauf-Takt
			name = FileName(c.Prefix, tmax, loc)
			qerr = tx.QueryRow(ctx, `SELECT name FROM energy_export_files
				WHERE connection_id = $1::uuid AND name = $2 AND time_base = $3`,
				c.ConnectionID, name, tb).Scan(&name)
		} else {
			qerr = tx.QueryRow(ctx, `SELECT name, period_start, period_end FROM energy_export_files
				WHERE connection_id = $1::uuid AND period_start <= $2 AND period_end > $2 AND time_base = $3
				ORDER BY period_start DESC LIMIT 1`, c.ConnectionID, first, tb).Scan(&name, &ps, &pe)
		}
		existing := qerr == nil
		if errors.Is(qerr, pgx.ErrNoRows) {
			if c.FileDays > 0 {
				ps, pe = FilePeriod(first, c.FileDays, loc)
				name = FileName(c.Prefix, ps, loc)
			}
			if name, err = freeName(ctx, tx, c.ConnectionID, name, tb); err != nil {
				return false, err
			}
		} else if qerr != nil {
			return false, qerr
		}
		// first_end/last_end: erstes und letztes Intervallende in der Datei
		// (für das Neuerzeugen nach einer Neurechnung).
		if existing {
			_, err = tx.Exec(ctx, `UPDATE energy_export_files
				SET content = content || $3, rows = rows + $4, version = version + 1, last_end = $5, updated_at = now()
				WHERE connection_id = $1::uuid AND name = $2`, c.ConnectionID, name, buf, rows, steps[n-1].End)
		} else {
			content := buf
			if c.CSV.Header {
				content = joinCSV(HeaderLine(c.CSV), buf)
			}
			_, err = tx.Exec(ctx, `INSERT INTO energy_export_files (connection_id, name, period_start, period_end, content, rows, time_base, first_end, last_end)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)`, c.ConnectionID, name, ps, pe, content, rows, tb, steps[0].End, steps[n-1].End)
		}
		if err != nil {
			return false, err
		}
		if !slices.Contains(res.Files, name) {
			res.Files = append(res.Files, name)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE energy_export_state SET last_interval_end = $2 WHERE connection_id = $1::uuid`,
		c.ConnectionID, steps[n-1].End); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// freeName liefert name, oder – wenn es den Namen schon gibt (Datei mit
// anderem Zeitbezug) – name mit angehängtem Zeitbezug und ggf. Zähler.
func freeName(ctx context.Context, tx pgx.Tx, connID, name, timeBase string) (string, error) {
	base := strings.TrimSuffix(name, ".csv")
	for k := 0; k < 100; k++ {
		cand := name
		switch {
		case k == 1:
			cand = base + "_" + timeBase + ".csv"
		case k > 1:
			cand = fmt.Sprintf("%s_%s%d.csv", base, timeBase, k)
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM energy_export_files
			WHERE connection_id = $1::uuid AND name = $2)`, connID, cand).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return cand, nil
		}
	}
	return "", fmt.Errorf("kein freier Dateiname für %s", name)
}

// querier: Pool oder einzelne Verbindung.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type pendingFile struct{ id, name string }

// pendingFiles nennt die Dateien einer Verbindung, deren Stand noch nicht
// zugestellt ist, älteste zuerst (ohne Inhalt).
func pendingFiles(ctx context.Context, q querier, connID string) ([]pendingFile, error) {
	rows, err := q.Query(ctx, `SELECT id::text, name FROM energy_export_files
		WHERE connection_id = $1::uuid AND version > delivered_version ORDER BY created_at, name`, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingFile
	for rows.Next() {
		var p pendingFile
		if err := rows.Scan(&p.id, &p.name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// markDelivered hält fest, dass version zugestellt ist – nur, wenn noch keine
// neuere als zugestellt gilt.
func markDelivered(ctx context.Context, q querier, id string, version int) error {
	_, err := q.Exec(ctx, `UPDATE energy_export_files
		SET delivered_version = $2, delivered_at = now(), last_error = ''
		WHERE id = $1::uuid AND delivered_version < $2`, id, version)
	return err
}

func (e *Exporter) deliverWait() time.Duration {
	if e.DeliverWait > 0 {
		return e.DeliverWait
	}
	return 5 * time.Second
}

// deliver stellt alle Dateien zu, deren Stand noch nicht zugestellt ist.
//
// Während des Ablegens ist keine Transaktion offen. Die Reihenfolge der
// Versionen sichert eine Advisory-Sperre je Verbindung auf Sitzungsebene, auf
// einer eigenen Verbindung außerhalb des Pools: ein zweiter Lauf wartet
// höchstens deliverWait und überlässt die Zustellung sonst dem nächsten Lauf.
// So überschreibt nie eine ältere Version eine neuere. Der Zustellstand wird
// je Datei sofort festgeschrieben. Scheitert eine Datei, bricht die
// Zustellung ab (das Ziel ist vermutlich nicht erreichbar); der Fehler bleibt
// an der Datei, der nächste Lauf versucht es erneut.
func (e *Exporter) deliver(ctx context.Context, c ExportConfig, res *RunResult) error {
	todo, err := pendingFiles(ctx, e.Store.DB, c.ConnectionID)
	if err != nil || len(todo) == 0 {
		return err
	}
	conn, err := pgx.ConnectConfig(ctx, e.Store.DB.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx)) // Sitzungsende gibt die Sperre frei
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", e.deliverWait().Milliseconds())); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, "energy_export_deliver:"+c.ConnectionID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
			res.Notes = append(res.Notes, "Ein anderer Lauf stellt gerade zu; die Dateien folgen im nächsten Lauf.")
			return nil
		}
		return err
	}
	// Nach dem Warten neu lesen: ein anderer Lauf kann inzwischen zugestellt haben.
	if todo, err = pendingFiles(ctx, conn, c.ConnectionID); err != nil {
		return err
	}
	for i, p := range todo {
		var content []byte
		var version int
		err := conn.QueryRow(ctx, `SELECT content, version FROM energy_export_files WHERE id = $1::uuid`, p.id).Scan(&content, &version)
		if errors.Is(err, pgx.ErrNoRows) { // inzwischen aufgeräumt
			continue
		}
		if err != nil {
			return err
		}
		if perr := c.Target.Put(ctx, p.name, content); perr != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("Zustellung von %s fehlgeschlagen: %v", p.name, perr))
			if rest := len(todo) - i - 1; rest > 0 {
				res.Notes = append(res.Notes, fmt.Sprintf("Zustellung abgebrochen: %d weitere Datei(en) folgen im nächsten Lauf.", rest))
			}
			if _, err := conn.Exec(ctx, `UPDATE energy_export_files SET last_error = $2 WHERE id = $1::uuid`,
				p.id, perr.Error()); err != nil {
				return err
			}
			return fmt.Errorf("%s: %w", p.name, perr)
		}
		if err := markDelivered(ctx, conn, p.id, version); err != nil {
			return err
		}
		res.Delivered++
	}
	return nil
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

// Pass führt alle eingerichteten Exporte einmal aus und wartet auf alle.
// Fehler eines Exports stoppen die anderen nicht. Läufe ohne neues Intervall
// werden nicht protokolliert.
func (e *Exporter) Pass(ctx context.Context) error { return e.pass(ctx, true) }

// begin meldet einen Lauf für die Verbindung an; false: es läuft schon einer.
func (e *Exporter) begin(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running == nil {
		e.running = map[string]bool{}
	}
	if e.running[id] {
		return false
	}
	e.running[id] = true
	return true
}

func (e *Exporter) end(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.running, id)
}

// pass startet je Export einen eigenen Lauf, damit ein langsames oder
// ausgefallenes Ziel die anderen Exporte nicht aufhält. Ist der Lauf einer
// Verbindung aus dem letzten Takt noch nicht fertig, wird sie diesmal
// ausgelassen. wait: auf alle Läufe warten und ihre Fehler liefern; sonst
// werden Fehler protokolliert.
func (e *Exporter) pass(ctx context.Context, wait bool) error {
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
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	fail := func(err error) {
		if !wait {
			if ctx.Err() == nil {
				log.Error().Err(err).Msg("energie: export")
			}
			return
		}
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	for _, c := range cfgs {
		if !e.begin(c.ConnectionID) {
			continue
		}
		wg.Add(1)
		go func(c ExportConfig) {
			defer wg.Done()
			defer e.end(c.ConnectionID)
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Str("export", c.ConnectionID).Msg("energie: export abgestürzt")
					fail(fmt.Errorf("export %s: abgestürzt: %v", c.ConnectionID, r))
				}
			}()
			if _, err := e.run(ctx, c, false); err != nil {
				fail(fmt.Errorf("export %s: %w", c.ConnectionID, err))
			}
		}(c)
	}
	if !wait {
		return nil
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Start führt im Takt von 60 s die Exporte aus, bis ctx endet. Jeder Export
// läuft für sich; eine Panik wird protokolliert, die Schleife läuft weiter.
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
	if err := e.pass(ctx, false); err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("energie: export")
	}
}
