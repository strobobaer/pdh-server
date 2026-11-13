package extsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
)

// Hereinkommender Abgleich – Regeln (verbindlich, siehe Entwurf und Entscheidungen R12–R18):
//
//   - Fortschrittsmarke: Eine gelesene Zeile zählt für den Cursor erst, wenn sie
//     bearbeitet ist ODER ihre Verknüpfung dauerhaft einen Fehler trägt (wird über
//     RetryLinks wiederholt). Geht beides nicht (PDH-Datenbank weg, Kontext
//     abgebrochen), endet der Lauf und der Cursor bleibt vor dieser Zeile.
//   - Ausgangswert (Link.WrittenValues), nach JEDER Bearbeitung eines
//     hereinkommenden Felds: seen[f] = Fremdwert; ist der tatsächliche PDH-Wert
//     danach gleich dem Fremdwert, wird written[f] entfernt; hat der Abgleich den
//     PDH-Wert in dieser Bearbeitung gesetzt oder bewusst behalten (Vorgabe, leerer
//     Fremdwert übergangen, closed→resolved, Lösungstext), gilt written[f] = der
//     tatsächliche PDH-Wert; bei einem Widerspruch (PDH gewinnt) bleibt written[f]
//     unverändert. Nie "" nur um einen Eintrag zu haben.
//   - Verknüpfung, Widerspruchsvermerke und Wartelisteneintrag werden in EINER
//     Transaktion gespeichert (Store.SaveInbound); eine Wiederholung schreibt
//     nichts doppelt.
//   - Endzustände (LinkErrRecordGone, LinkErrForeignGone, LinkErrBadKey) werden
//     einmal gemeldet und nicht wiederholt.
//
// Rückwirkende Meldungen (Spec 4.10):
//   - Neu und rückwirkend ist eine Zeile, deren Anlagedatum (Profile.CreatedColumn)
//     vor dem Beginn des heutigen Tages in Inbound.Location liegt (IsBackfill).
//     Ohne Anlagedatum-Spalte, bei NULL oder unlesbarem Wert gilt sie als neu.
//     Eine Datumsspalte ohne Uhrzeit gilt als Kalendertag am Ort des Servers.
//   - Neue Zeilen benachrichtigen wie gewohnt und zählen für die Obergrenze
//     (Anhalten bei zu vielen). Rückwirkende werden still angelegt (CreateOptions.
//     Silent), mit dem fremden Anlagedatum als Anlagezeit; abgeschlossene werden
//     gleich erledigt und archiviert (CreateOptions.Archive). Sie halten das
//     Profil nie an: je Lauf werden höchstens backfillCap() angelegt, die übrigen
//     als Verknüpfung ohne Datensatz mit LinkErrBackfillWaiting vorgemerkt (zählt
//     als bearbeitet für den Cursor, nicht als Fehler) und von späteren Läufen
//     über BackfillLinks angelegt.
//   - Ausgangswert, Vorabeintrag, Wiederholung und Endzustände gelten für
//     rückwirkende Zeilen unverändert.

// ── Zugriff auf die Fremddatenbank ───────────────────────────────────────

// Foreign führt die Befehle des Dialekts aus: im Betrieb *sql.DB, im Test eine
// Attrappe. Der hereinkommende Lauf ruft Exec nie auf.
type Foreign interface {
	Describe(ctx context.Context, schema, table string) (*TableInfo, error)
	Query(ctx context.Context, st Stmt) ([]map[string]any, error)
	Exec(ctx context.Context, st Stmt) (affected int64, err error)
}

// defaultForeignTimeout gilt, wenn NewSQLForeign keine Zeitgrenze bekommt.
const defaultForeignTimeout = 30 * time.Second

type sqlForeign struct {
	db      *sql.DB
	d       Dialect
	timeout time.Duration
}

// NewSQLForeign führt Befehle über db aus; jeder Aufruf hat die Zeitgrenze timeout
// (0 = 30 Sekunden). Zeilen kommen als Map Spaltenname -> Wert.
func NewSQLForeign(db *sql.DB, d Dialect, timeout time.Duration) Foreign {
	if timeout <= 0 {
		timeout = defaultForeignTimeout
	}
	return &sqlForeign{db: db, d: d, timeout: timeout}
}

func (f *sqlForeign) Describe(ctx context.Context, schema, table string) (*TableInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	return f.d.Describe(ctx, f.db, schema, table)
}

func (f *sqlForeign) Query(ctx context.Context, st Stmt) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	rows, err := f.db.QueryContext(ctx, st.SQL, st.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		// Scan in *any kopiert []byte, die Werte bleiben nach Next gültig.
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (f *sqlForeign) Exec(ctx context.Context, st Stmt) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	res, err := f.db.ExecContext(ctx, st.SQL, st.Args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── Hereinkommender Lauf ─────────────────────────────────────────────────

// Inbound übernimmt neue und geänderte Zeilen der Fremdtabelle ins PDH. Die
// Sperre gegen gleichzeitige Läufe eines Profils nutzt den Pool des Store.
type Inbound struct {
	Store      *Store
	Targets    map[string]Target // nach Profile.TargetType
	SystemUser string            // Akteur aller Änderungen
	// Now liefert die Uhrzeit für die Entscheidung "rückwirkend" (nil = time.Now).
	Now func() time.Time
	// Location ist der Ort des Servers für den Tagesbeginn (nil = time.Local).
	Location *time.Location
	pageSize int // 0 = defaultPageSize; Tests setzen kleinere Seiten
	// afterLock ist nur für Tests: läuft, sobald die Sperre gehalten wird.
	afterLock func()
	// testFail ist nur für Tests: Fehler an benannten Schritten einschleusen
	// ("reserve", "linkerror", "commit").
	testFail func(step, fk string) error
}

// RunOptions steuert einen Lauf.
type RunOptions struct {
	// Dry: Probelauf. Liest, schreibt nichts (keine Verknüpfung, kein Lauf,
	// kein Verlauf, kein Zustand) und hält nie an; liefert die Vorschau.
	Dry bool
	// Limit: 0 = Seiten zu 200 Zeilen bis zum Ende. Sonst (Überlastung) genau
	// eine Seite mit höchstens Limit Zeilen; im Probelauf die Größe der Vorschau.
	Limit int
}

// Preview ist eine Zeile der Vorschau eines Probelaufs: bei neuen Zeilen alle
// Werte des anzulegenden Datensatzes, bei geänderten die zu übernehmenden.
type Preview struct {
	ForeignKey string
	Values     map[string]string
	Errors     []RunError
	IsNew      bool
	// Backfill: neue Zeile, die still als rückwirkende Meldung angelegt würde.
	// Archive: davon gleich erledigt und archiviert (Status abgeschlossen).
	Backfill bool
	Archive  bool
}

// IsBackfill sagt, ob created vor dem Beginn des heutigen Tages (Tag von now in
// loc) liegt. Die Nullzeit ist nie rückwirkend; loc nil = time.Local.
func IsBackfill(created time.Time, now time.Time, loc *time.Location) bool {
	if created.IsZero() {
		return false
	}
	if loc == nil {
		loc = time.Local
	}
	n := now.In(loc)
	return created.Before(time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc))
}

// createdTime liest das Anlagedatum einer Fremdzeile. Zeitspalten kommen als
// Zeitpunkt (UTC); eine reine Datumsspalte kommt als Mitternacht UTC und meint
// den Kalendertag am Ort loc – sie wird als dessen Tagesbeginn in loc gelesen.
// NULL und andere Typen: ok = false (die Zeile gilt als neu).
func createdTime(v any, dateOnly bool, loc *time.Location) (time.Time, bool) {
	tm, ok := v.(time.Time)
	if !ok || tm.IsZero() {
		return time.Time{}, false
	}
	if dateOnly {
		if loc == nil {
			loc = time.Local
		}
		u := tm.UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, loc), true
	}
	return tm, true
}

// backfillValue: ist der gelesene Wert der Anlagedatum-Spalte rückwirkend?
func backfillValue(v any, dateOnly bool, now time.Time, loc *time.Location) bool {
	tm, ok := createdTime(v, dateOnly, loc)
	return ok && IsBackfill(tm, now, loc)
}

const (
	defaultPageSize = 200
	// retryLimit: so viele fehlerhafte oder reservierte Verknüpfungen versucht ein Lauf erneut
	// (wartende rückwirkende Meldungen nicht mitgezählt, siehe backfillCap).
	retryLimit = 50
	// lockAcquireTimeout: so lange wartet ein Lauf höchstens auf eine freie Datenbankverbindung.
	lockAcquireTimeout = 5 * time.Second
	// SkippedBusy steht im Lauf, wenn das Profil schon von einem anderen Lauf bearbeitet wird.
	SkippedBusy = "Profil läuft bereits"
	// SkippedNoConn steht im Lauf, wenn keine Datenbankverbindung für die Sperre frei war.
	SkippedNoConn = "Keine freie Datenbankverbindung"
)

// Zustand einer gelesenen Zeile für die Fortschrittsmarke.
const (
	rowOK      = iota // bearbeitet oder mit Fehler an der Verknüpfung festgehalten
	rowPending        // noch nicht (dauerhaft) bearbeitet: hält die Marke auf
)

type rowPos struct {
	cursor string
	state  int
}

// candidate ist eine gelesene Zeile, die (vielleicht) etwas zu tun hat.
type candidate struct {
	pos     int // Index in positions, -1 ohne Marke (none, Wiederholung)
	keyVals []string
	fk      string
	mapped  map[string]string
	errs    []RunError
	mark    string
	// backfill: rückwirkende Meldung (still anlegen); created ist dann ihr Anlagezeitpunkt.
	backfill bool
	created  time.Time
}

type inboundRun struct {
	in        *Inbound
	p         *Profile
	maps      []FieldMap
	fields    []PDHField
	target    Target
	d         Dialect
	t         *TableInfo
	opt       RunOptions
	run       *Run
	previews  []Preview
	positions []rowPos
	pending   []candidate
	done      map[string]bool // in der Wiederholung schon bearbeitete Schlüssel
	newCount  int
	paused    bool
	// Rückwirkende Meldungen: Uhr und Ort des Laufs (einmal festgelegt), Art der
	// Anlagedatum-Spalte und Zahl der in diesem Lauf zum Anlegen vorgemerkten.
	now           time.Time
	loc           *time.Location
	createdDate   bool // Anlagedatum-Spalte ist reines Datum
	backfillCount int
}

// lockKey bildet aus der Profil-ID die Nummer der Postgres-Beratungssperre.
func lockKey(profileID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("extsync-inbound:" + profileID))
	return int64(h.Sum64())
}

// Run fährt einen hereinkommenden Lauf für das Profil p. Ergebnis ist der Lauf
// mit Zählern und neuer Fortschrittsmarke (Run.Cursor, vom Aufrufer auch nach
// einem Fehler zu speichern). Ein Fehler einer Zeile betrifft nur sie; ein Fehler
// des ganzen Laufs (Fremddatenbank nicht erreichbar, Einstellung falsch, PDH-
// Datenbank weg, Kontext abgebrochen) schließt den Lauf mit dem Grund ab und
// kommt als err zurück. Läuft das Profil schon (SkippedBusy) oder ist binnen
// lockAcquireTimeout keine Datenbankverbindung frei (SkippedNoConn), geschieht
// nichts: kein Eintrag in sync_runs, err == nil. Kein Absturz verlässt Run.
//
// Voraussetzung: Ein Lauf (nicht Probelauf) hält für seine ganze Dauer eine
// eigene Verbindung des Pools (Sperre) und braucht weitere für Store, Ziel und
// QuickResolve. Der Aufrufer startet höchstens zwei Läufe gleichzeitig (R19),
// sonst können die Läufe den Pool erschöpfen.
func (in *Inbound) Run(ctx context.Context, p *Profile, maps []FieldMap, f Foreign, d Dialect, opt RunOptions) (run *Run, previews []Preview, err error) {
	if p == nil {
		return nil, nil, errors.New("abgleich: profil fehlt")
	}
	start := p.Cursor
	if p.ChangeKind == changeKindNone || p.ChangeKind == "" {
		start = ""
	}
	run = &Run{ProfileID: p.ID, Direction: DirIn, StartedAt: time.Now(), Cursor: start}
	skip := func(reason string) (*Run, []Preview, error) {
		run.SkippedReason = reason
		now := time.Now()
		run.FinishedAt = &now
		return run, nil, nil
	}
	if !opt.Dry {
		// Sitzungssperre je Profil für den ganzen Lauf (eigene Verbindung des Pools).
		actx, cancel := context.WithTimeout(ctx, lockAcquireTimeout)
		conn, lerr := in.Store.db.Acquire(actx)
		timedOut := errors.Is(actx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		if lerr != nil {
			if timedOut {
				return skip(SkippedNoConn)
			}
			return run, nil, fmt.Errorf("abgleich: sperre holen: %w", lerr)
		}
		defer conn.Release()
		key := lockKey(p.ID)
		var got bool
		if lerr := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); lerr != nil {
			return run, nil, fmt.Errorf("abgleich: sperre holen: %w", lerr)
		}
		if !got {
			return skip(SkippedBusy)
		}
		defer func() {
			// Auch nach Absturz und abgebrochenem Kontext freigeben.
			if _, uerr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, key); uerr != nil {
				// Ohne Freigabe darf die Verbindung nicht in den Pool zurück (Sperre bliebe).
				_ = conn.Conn().Close(context.WithoutCancel(ctx))
			}
		}()
		if in.afterLock != nil {
			in.afterLock()
		}
		id, serr := in.Store.StartRun(ctx, p.ID, DirIn)
		if serr != nil {
			return run, nil, serr
		}
		run.ID = id
	}
	r := &inboundRun{in: in, p: p, maps: maps, d: d, opt: opt, run: run, done: map[string]bool{},
		now: time.Now(), loc: in.Location}
	if in.Now != nil {
		r.now = in.Now()
	}
	if r.loc == nil {
		r.loc = time.Local
	}
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("abgleich: interner Fehler im Lauf: %v", rec)
			run.Cursor = start
			previews = nil
		}
		if err != nil && run.SkippedReason == "" {
			run.SkippedReason = err.Error()
		}
		now := time.Now()
		run.FinishedAt = &now
		if run.ID != "" {
			// Auch bei abgebrochenem Kontext den Lauf abschließen.
			if ferr := in.Store.FinishRun(context.WithoutCancel(ctx), run); ferr != nil && err == nil {
				err = ferr
			}
		}
	}()
	err = r.exec(ctx, f)
	if !opt.Dry {
		run.Cursor = r.cursor(start)
	}
	return run, r.previews, err
}

// cursor: Marke der letzten Zeile vor der ersten nicht (dauerhaft) bearbeiteten
// Zeile; ohne Änderungsspalte leer.
func (r *inboundRun) cursor(start string) string {
	if r.p.ChangeKind == changeKindNone || r.p.ChangeKind == "" {
		return ""
	}
	cur := start
	for _, ps := range r.positions {
		if ps.state != rowOK {
			break
		}
		cur = ps.cursor
	}
	return cur
}

func (r *inboundRun) hook(step, fk string) error {
	if r.in.testFail != nil {
		return r.in.testFail(step, fk)
	}
	return nil
}

func stopped(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Lauf abgebrochen: %w", err)
	}
	return nil
}

func (r *inboundRun) exec(ctx context.Context, f Foreign) error {
	p := r.p
	r.fields = Fields(p.TargetType)
	if r.fields == nil {
		return fmt.Errorf("Ziel %q ist unbekannt", p.TargetType)
	}
	r.target = r.in.Targets[p.TargetType]
	if r.target == nil {
		return fmt.Errorf("Ziel %q ist nicht eingerichtet", p.TargetType)
	}
	if !r.opt.Dry && r.in.SystemUser == "" {
		return errors.New("Systembenutzer fehlt")
	}
	t, err := f.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		return fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	r.t = t
	cols := inboundColumns(r.maps)
	if p.CreatedColumn != "" {
		col, ok := findColumn(t, p.CreatedColumn)
		if !ok {
			return fmt.Errorf("Anlagedatum-Spalte ‚%s‘ gibt es in der Tabelle nicht", p.CreatedColumn)
		}
		isTime, dateOnly := r.d.TimeColumn(*col)
		if !isTime {
			return errors.New("Anlagedatum-Spalte ist keine Datumsspalte")
		}
		r.createdDate = dateOnly
		// Mitgelesen, aber kein zugeordnetes Feld: nie in seen_values oder RowMark.
		if !containsStr(cols, p.CreatedColumn) {
			cols = append(cols, p.CreatedColumn)
		}
	}
	if !r.opt.Dry {
		if err := r.retry(ctx, f, cols); err != nil {
			return err
		}
		if err := r.retryBackfill(ctx, f, cols); err != nil {
			return err
		}
	}

	pageSize := r.in.pageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if r.opt.Limit > 0 {
		pageSize = r.opt.Limit
	}
	onePage := r.opt.Limit > 0 && !r.opt.Dry
	cursor := r.run.Cursor
	for !r.paused {
		st, err := r.d.SelectRows(t, cols, p.KeyColumns, p.ChangeColumn, p.ChangeKind, cursor, p.FilterSQL, p.WindowDays, pageSize)
		if err != nil {
			return err
		}
		rows, err := f.Query(ctx, st)
		if err != nil {
			return fmt.Errorf("Fremddatenbank lesen: %w", err)
		}
		for _, row := range rows {
			if err := stopped(ctx); err != nil {
				return err
			}
			if err := r.readRow(ctx, row); err != nil {
				return err
			}
			if r.paused {
				break
			}
		}
		if r.paused || len(rows) < pageSize || onePage || (r.opt.Dry && len(r.previews) >= r.previewLimit()) {
			break
		}
		if cursor, err = r.d.NextCursor(t, p.KeyColumns, p.ChangeColumn, p.ChangeKind, rows[len(rows)-1]); err != nil {
			return err
		}
	}
	if r.opt.Dry {
		return nil
	}
	if r.paused {
		msg := fmt.Sprintf("Zu viele neue Meldungen: mindestens %d (Obergrenze %d)", r.newCount, r.maxNew())
		r.run.SkippedReason = msg
		return r.in.Store.SetState(ctx, p.ID, StateHalted, msg)
	}
	// Erst jetzt steht fest, dass die Obergrenze hält: neue Zeilen anlegen.
	for _, c := range r.pending {
		if err := stopped(ctx); err != nil {
			return err
		}
		if err := r.processNew(ctx, c); err != nil {
			return err
		}
		r.setState(c.pos, rowOK)
	}
	return nil
}

// retry versucht Verknüpfungen erneut, die reserviert (ohne Datensatz, z. B.
// nach einem Abbruch) oder mit Fehler stehen: die Zeile wird einzeln gelesen
// und wie eine Zeile einer Seite behandelt. Reservierte zählen als neu.
func (r *inboundRun) retry(ctx context.Context, f Foreign, cols []string) error {
	links, err := r.in.Store.RetryLinks(ctx, r.p.ID, retryLimit)
	if err != nil {
		return err
	}
	return r.retryLinks(ctx, f, cols, links)
}

// retryBackfill legt wartende rückwirkende Meldungen an (LinkErrBackfillWaiting),
// höchstens so viele, wie in diesem Lauf noch unter backfillCap passen. Eigener
// Durchgang neben retry: die Grenze 50 der Fehler-Wiederholung gilt hier nicht.
func (r *inboundRun) retryBackfill(ctx context.Context, f Foreign, cols []string) error {
	room := r.backfillCap() - r.backfillCount
	if room <= 0 || r.paused {
		return nil
	}
	links, err := r.in.Store.BackfillLinks(ctx, r.p.ID, room)
	if err != nil {
		return err
	}
	return r.retryLinks(ctx, f, cols, links)
}

// retryLinks liest jede Verknüpfung einzeln und behandelt sie wie eine Zeile.
func (r *inboundRun) retryLinks(ctx context.Context, f Foreign, cols []string, links []Link) error {
	p := r.p
	if len(links) == 0 {
		return nil
	}
	one := cols
	if p.ChangeColumn != "" && p.ChangeKind != changeKindNone {
		one = append(append([]string{}, cols...), p.ChangeColumn) // für die Änderungsmarke
	}
	for i := range links {
		l := &links[i]
		if r.paused {
			return nil
		}
		if err := stopped(ctx); err != nil {
			return err
		}
		kv, err := DecodeKey(l.ForeignKey)
		if err != nil {
			if err := r.terminal(ctx, l.ID, l.ForeignKey, nil, LinkErrBadKey); err != nil {
				return err
			}
			continue
		}
		st, err := r.d.SelectOne(r.t, one, p.KeyColumns, kv)
		if err != nil {
			return err
		}
		rows, err := f.Query(ctx, st)
		if err != nil {
			return fmt.Errorf("Fremddatenbank lesen: %w", err)
		}
		r.done[l.ForeignKey] = true
		if len(rows) == 0 {
			if err := r.terminal(ctx, l.ID, l.ForeignKey, nil, LinkErrForeignGone); err != nil {
				return err
			}
			continue
		}
		if err := r.handleRow(ctx, rows[0], -1, l, true); err != nil {
			return err
		}
	}
	return nil
}

func (r *inboundRun) maxNew() int {
	if r.p.MaxNew <= 0 {
		return DefaultMaxNew
	}
	return r.p.MaxNew
}

// backfillCap: so viele rückwirkende Meldungen legt ein Lauf höchstens an –
// die Obergrenze des Profils, bei Überlastung (RunOptions.Limit) höchstens Limit.
func (r *inboundRun) backfillCap() int {
	c := r.maxNew()
	if r.opt.Limit > 0 && r.opt.Limit < c {
		c = r.opt.Limit
	}
	return c
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (r *inboundRun) previewLimit() int {
	if r.opt.Limit > 0 {
		return r.opt.Limit
	}
	return defaultPageSize
}

// inboundColumns: die Fremdspalten, die der Lauf liest (Schlüssel und
// Änderungsspalte ergänzt der Dialekt).
func inboundColumns(maps []FieldMap) []string {
	seen := map[string]bool{}
	var cols []string
	for _, m := range maps {
		if m.FixedValue != nil || m.ForeignColumn == "" || (m.Direction != DirIn && m.Direction != DirBoth) {
			continue
		}
		if !seen[m.ForeignColumn] {
			seen[m.ForeignColumn] = true
			cols = append(cols, m.ForeignColumn)
		}
	}
	return cols
}

// inboundFields: PDH-Felder, die Decide abgleicht (herein/beides, kein fester Wert).
func (r *inboundRun) inboundFields() []string {
	var out []string
	for _, m := range r.maps {
		if m.FixedValue == nil && m.PDHField != "" && (m.Direction == DirIn || m.Direction == DirBoth) {
			out = append(out, m.PDHField)
		}
	}
	return out
}

func (r *inboundRun) setState(pos, state int) {
	if pos >= 0 {
		r.positions[pos].state = state
	}
}

// fail zählt die Zeile als gescheitert und nimmt ihre Fehler in den Lauf.
func (r *inboundRun) fail(fk string, errs ...RunError) {
	r.run.Failed++
	for _, e := range errs {
		e.ForeignKey = fk
		r.run.Errors = append(r.run.Errors, e)
	}
}

func joinErrs(errs []RunError) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Message
	}
	return strings.Join(parts, "; ")
}

// rowFailed hält den Fehler dauerhaft an der Verknüpfung fest (wird wiederholt)
// und zählt die Zeile. Lässt sich der Fehler nicht speichern, endet der Lauf
// (der Cursor bleibt dann vor der Zeile).
func (r *inboundRun) rowFailed(ctx context.Context, linkID, fk string, errs ...RunError) error {
	err := r.hook("linkerror", fk)
	if err == nil {
		err = r.in.Store.SetLinkError(ctx, linkID, joinErrs(errs))
	}
	if err != nil {
		return fmt.Errorf("Fehler der Zeile %s nicht gespeichert (%s): %w", fk, joinErrs(errs), err)
	}
	r.fail(fk, errs...)
	return nil
}

// terminal setzt einen Endzustand (einmal gemeldet, nicht wiederholt). mark wird
// mitgespeichert, damit Lesen ohne Änderungsspalte die Zeile überspringt.
func (r *inboundRun) terminal(ctx context.Context, linkID, fk string, mark *string, msg string) error {
	err := r.hook("linkerror", fk)
	if err == nil {
		err = r.in.Store.SaveInbound(ctx, linkID, InboundUpdate{SeenMark: mark, LastError: &msg})
	}
	if err != nil {
		return fmt.Errorf("Fehler der Zeile %s nicht gespeichert (%s): %w", fk, msg, err)
	}
	r.fail(fk, RunError{Message: msg})
	return nil
}

// readRow ordnet eine Zeile einer Seite ein. Fehler hier beenden den Lauf.
func (r *inboundRun) readRow(ctx context.Context, row map[string]any) error {
	p := r.p
	pos := -1
	if p.ChangeKind != changeKindNone && p.ChangeKind != "" {
		cur, err := r.d.NextCursor(r.t, p.KeyColumns, p.ChangeColumn, p.ChangeKind, row)
		if err != nil {
			return err
		}
		// Zählt erst, wenn die Zeile dauerhaft bearbeitet ist (handleRow).
		r.positions = append(r.positions, rowPos{cursor: cur, state: rowPending})
		pos = len(r.positions) - 1
	}
	return r.handleRow(ctx, row, pos, nil, false)
}

// handleRow bearbeitet eine Zeile (aus einer Seite oder aus der Wiederholung).
// link ist bei der Wiederholung schon bekannt; retried erzwingt den Abgleich
// auch bei unveränderter Marke (damit ein Fehler am Link verschwindet). Ein
// Fehler beendet den Lauf; sonst ist die Zeile bearbeitet, festgehalten oder
// (neu) zum Anlegen vorgemerkt.
func (r *inboundRun) handleRow(ctx context.Context, row map[string]any, pos int, link *Link, retried bool) error {
	p := r.p
	kv, err := r.d.KeyValues(r.t, p.KeyColumns, row)
	if err != nil {
		return err
	}
	c := candidate{pos: pos, keyVals: kv, fk: EncodeKey(kv)}
	if !retried && r.done[c.fk] {
		r.setState(pos, rowOK) // schon in der Wiederholung bearbeitet
		return nil
	}
	c.mapped, c.errs = ToPDH(r.maps, r.fields, row, p.Defaults)
	if v, ok := c.mapped["status"]; ok {
		c.mapped["status"] = normalizeStatus(v)
	}
	var changeVal any
	if p.ChangeColumn != "" {
		changeVal = row[p.ChangeColumn]
	}
	c.mark = RowMark(p.ChangeKind, changeVal, c.mapped)
	if p.CreatedColumn != "" {
		if tm, ok := createdTime(row[p.CreatedColumn], r.createdDate, r.loc); ok && IsBackfill(tm, r.now, r.loc) {
			c.backfill, c.created = true, tm
		}
	}

	if link == nil {
		link, err = r.in.Store.LinkByKey(ctx, p.ID, c.fk)
		if errors.Is(err, ErrNotFound) {
			link = nil
		} else if err != nil {
			return err
		}
	}
	if link != nil && link.RefID != nil {
		unchanged := link.SeenMark == c.mark
		switch {
		case !retried && unchanged && link.LastError == LinkErrRecordGone:
			// Endzustand, schon gemeldet: erst eine neue Änderung meldet erneut.
			r.setState(pos, rowOK)
			return nil
		case !retried && unchanged && r.seenComplete(link) && link.LastError != LinkErrForeignGone:
			r.setState(pos, rowOK) // unverändert
			return nil
		}
		if r.opt.Dry {
			return r.previewChanged(ctx, c, link)
		}
		if err := r.processChanged(ctx, c, link); err != nil {
			return err
		}
		r.setState(pos, rowOK)
		return nil
	}
	// Neu (oder reserviert ohne Datensatz).
	if r.opt.Dry {
		r.previewNew(c)
		return nil
	}
	if c.backfill {
		// Rückwirkend: zählt nicht für das Anhalten; über der Grenze je Lauf vormerken.
		r.backfillCount++
		if r.backfillCount > r.backfillCap() {
			return r.park(ctx, c, link)
		}
		r.pending = append(r.pending, c) // Zustand bleibt rowPending bis zum Anlegen
		return nil
	}
	r.newCount++
	if r.newCount > r.maxNew() {
		r.paused = true
	}
	r.pending = append(r.pending, c) // Zustand bleibt rowPending bis zum Anlegen
	return nil
}

// park merkt eine rückwirkende Meldung über der Grenze je Lauf für einen
// späteren Lauf vor (Verknüpfung ohne Datensatz, LinkErrBackfillWaiting). Die
// Zeile gilt damit als bearbeitet (Cursor) und nicht als fehlgeschlagen. Gibt es
// schon eine Verknüpfung ohne Datensatz, bleibt sie (sie wird ohnehin wiederholt).
func (r *inboundRun) park(ctx context.Context, c candidate, link *Link) error {
	if link == nil {
		err := r.hook("park", c.fk)
		if err == nil {
			_, err = r.in.Store.ParkLink(ctx, r.p.ID, c.fk, r.p.TargetType)
		}
		if err != nil {
			return fmt.Errorf("Rückwirkende Meldung %s nicht vorgemerkt: %w", c.fk, err)
		}
	}
	r.setState(c.pos, rowOK)
	return nil
}

// seenComplete: Für jedes hereinkommende Feld gibt es einen gesehenen Wert.
// Fehlt einer (Feld später zugeordnet), wird die Zeile abgeglichen, auch wenn
// sich ihre Marke nicht geändert hat.
func (r *inboundRun) seenComplete(l *Link) bool {
	for _, k := range r.inboundFields() {
		if _, ok := l.SeenValues[k]; !ok {
			return false
		}
	}
	return true
}

// complete ergänzt Vorgaben für Katalogfelder ohne Zuordnung und prüft Pflichtfelder.
func (r *inboundRun) complete(mapped map[string]string) (map[string]string, []RunError) {
	values := make(map[string]string, len(mapped))
	for k, v := range mapped {
		values[k] = v
	}
	inMapped := map[string]bool{}
	for _, m := range r.maps {
		if m.FixedValue != nil || m.Direction == DirIn || m.Direction == DirBoth {
			inMapped[m.PDHField] = true
		}
	}
	var errs []RunError
	for _, f := range r.fields {
		if !inMapped[f.Key] {
			if d := strings.TrimSpace(r.p.Defaults[f.Key]); d != "" {
				v, e := finishPDHValue(f, d)
				if e != nil {
					errs = append(errs, *e)
					continue
				}
				if f.Key == "status" {
					v = normalizeStatus(v)
				}
				values[f.Key] = v
			}
		}
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			errs = append(errs, RunError{Field: f.Key, Message: fmt.Sprintf("Pflichtfeld ‚%s‘ ist leer", f.Label)})
		}
	}
	return values, errs
}

// ── Ausgangswerte (R17, R22, R23) ────────────────────────────────────────
//
// Werte, die der Abgleich abweichend vom Fremdwert ins PDH setzt, werden VOR
// dem Schreiben festgelegt (planApply/planCreate) und vorab als Ausgangswert
// gespeichert (Vorabeintrag mit LinkErrInterrupted). So stimmt die Einordnung
// durch Decide (p == w) auch nach einem Teilfehler. Ein Vorabeintrag, dessen
// Schreiben nie geschah, schadet nicht: der nächste Fremdwert gewinnt über
// p == s. Nie wird ein Unterschied zweier Lesevorgänge als Ausgangswert
// genommen (sonst würde eine Benutzeränderung geschluckt) – außer beim Anlegen,
// wo es vorher keinen Datensatz gab (Vorgaben des Dienstes wie die Fälligkeit).

// keepsWhenEmpty: Felder, die das Ziel bei leerem Wert unverändert lässt.
func keepsWhenEmpty(k string) bool {
	return k == "title" || k == "status" || k == "priority" || k == "severity"
}

func (r *inboundRun) levelKey() string {
	for _, f := range r.fields {
		if f.Key == "priority" || f.Key == "severity" {
			return f.Key
		}
	}
	return "priority"
}

func defaultStatusOf(targetType string) string {
	if targetType == RefFault {
		return "detected"
	}
	return "open"
}

// planApply: was ans Ziel geht (vals) und welcher PDH-Wert je Feld danach
// beabsichtigt ist (intended). Beim Abschließen ohne Lösung legt der Abgleich
// den Lösungstext selbst fest (das Ziel erfindet keine Werte).
func (r *inboundRun) planApply(apply, current map[string]string) (vals, intended, notes map[string]string) {
	vals = make(map[string]string, len(apply)+1)
	for k, v := range apply {
		vals[k] = v
	}
	notes = map[string]string{}
	newStatus := current["status"]
	if v := strings.TrimSpace(vals["status"]); v != "" {
		newStatus = v
	}
	if isFinalStatus(newStatus) && !isFinalStatus(current["status"]) {
		res := current["resolution"]
		if v, ok := vals["resolution"]; ok {
			res = v
		}
		if strings.TrimSpace(res) == "" {
			vals["resolution"] = resolvedText
			notes["resolution"] = noteResolvedText
		}
	}
	intended = make(map[string]string, len(vals))
	for k, v := range vals {
		v = strings.TrimSpace(v)
		switch {
		case v == "" && keepsWhenEmpty(k):
			intended[k] = current[k] // leer: PDH-Wert bleibt
		case k == "status" && isFinalStatus(v) && isFinalStatus(current[k]):
			intended[k] = current[k]
		default:
			intended[k] = v
		}
	}
	return vals, intended, notes
}

// planCreate: Werte für das Anlegen samt den Vorgaben, die der Abgleich selbst setzt.
func (r *inboundRun) planCreate(values map[string]string) (vals, intended map[string]string) {
	vals = make(map[string]string, len(values)+2)
	for k, v := range values {
		vals[k] = v
	}
	lk := r.levelKey()
	if strings.TrimSpace(vals[lk]) == "" {
		vals[lk] = "medium"
	}
	if isFinalStatus(vals["status"]) && strings.TrimSpace(vals["resolution"]) == "" {
		vals["resolution"] = resolvedText
	}
	intended = make(map[string]string, len(vals)+1)
	for k, v := range vals {
		intended[k] = v
	}
	if strings.TrimSpace(vals["status"]) == "" {
		intended["status"] = defaultStatusOf(r.p.TargetType)
	}
	return vals, intended
}

// deviations: hereinkommende Felder, deren beabsichtigter PDH-Wert vom Fremdwert abweicht.
func (r *inboundRun) deviations(intended, foreign map[string]string) map[string]string {
	dev := map[string]string{}
	for _, k := range r.inboundFields() {
		if iv, ok := intended[k]; ok && strings.TrimSpace(iv) != strings.TrimSpace(foreign[k]) {
			dev[k] = iv
		}
	}
	return dev
}

// finalWritten: feldweise Änderung der Ausgangswerte nach dem Schreiben. set
// sind die vorab festgelegten Abweichungen; gelöscht wird nur, wo der PDH-Wert
// jetzt gleich dem Fremdwert ist und ein bekannter Eintrag (aus dem geladenen
// Stand oder dem Vorabeintrag) besteht – nur wenn er noch unverändert ist.
func (r *inboundRun) finalWritten(snapshot, ahead, set, foreign, actual, keep map[string]string) (map[string]string, map[string]string) {
	outSet := map[string]string{}
	for k, v := range set {
		outSet[k] = v
	}
	del := map[string]string{}
	for _, k := range r.inboundFields() {
		if _, ok := keep[k]; ok {
			continue
		}
		if strings.TrimSpace(actual[k]) != strings.TrimSpace(foreign[k]) {
			continue
		}
		delete(outSet, k)
		if v, ok := ahead[k]; ok {
			del[k] = v
		} else if v, ok := snapshot[k]; ok {
			del[k] = v
		}
	}
	return outSet, del
}

// writeAhead speichert Abweichungen vor dem Schreiben samt Wiederholungsmarke.
func (r *inboundRun) writeAhead(ctx context.Context, linkID string, dev map[string]string) error {
	if len(dev) == 0 {
		return nil
	}
	msg := LinkErrInterrupted
	return r.in.Store.SaveInbound(ctx, linkID, InboundUpdate{WrittenSet: dev, LastError: &msg})
}

func (r *inboundRun) originNote(c candidate) string {
	return fmt.Sprintf("%s|%s|Aus %s, Nummer %s", r.p.ID, c.fk, r.p.Name, strings.Join(c.keyVals, ", "))
}

// processNew legt den Datensatz genau einmal an: reservieren, anlegen samt
// Herkunft (eine Transaktion), anbinden. Scheitert die Zeile, bleibt die
// reservierte Verknüpfung mit Fehler stehen und wird später wiederholt. Ein
// Fehler als Rückgabe beendet den Lauf.
func (r *inboundRun) processNew(ctx context.Context, c candidate) error {
	st := r.in.Store
	err := r.hook("reserve", c.fk)
	var link *Link
	var created bool
	if err == nil {
		link, created, err = st.ReserveLink(ctx, r.p.ID, c.fk, r.p.TargetType)
	}
	if err != nil {
		return fmt.Errorf("Verknüpfung für %s nicht reserviert: %w", c.fk, err)
	}
	if !created {
		if link.RefID != nil {
			return r.processChanged(ctx, c, link)
		}
		// Reserviert, aber nicht angebunden (Abbruch nach dem Commit des Anlegens?).
		id, found, err := r.target.FindOrigin(ctx, r.p.ID, c.fk)
		if err != nil {
			return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
		}
		if found {
			empty := ""
			if err := st.SaveInbound(ctx, link.ID, InboundUpdate{RefID: &id, SeenMark: &empty, Seen: map[string]string{}}); err != nil {
				return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
			}
			link.RefID, link.SeenMark, link.SeenValues = &id, "", map[string]string{}
			return r.processChanged(ctx, c, link)
		}
	}
	errs := c.errs
	var values map[string]string
	if len(errs) == 0 {
		values, errs = r.complete(c.mapped)
	}
	if len(errs) > 0 {
		return r.rowFailed(ctx, link.ID, c.fk, errs...)
	}
	vals, intended := r.planCreate(values)
	ahead := r.deviations(intended, c.mapped)
	if err := r.writeAhead(ctx, link.ID, ahead); err != nil {
		return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
	}
	var opt CreateOptions
	if c.backfill {
		created := c.created
		opt = CreateOptions{Silent: true, CreatedAt: &created, Archive: isFinalStatus(vals["status"])}
	}
	id, err := r.target.Create(ctx, vals, r.in.SystemUser, r.originNote(c), opt)
	if err != nil {
		if id != "" {
			// Datensatz samt Herkunft existiert, eine Nacharbeit fehlte: anbinden ohne
			// gesehenen Stand, damit der nächste Versuch die Felder abgleicht.
			empty := ""
			_ = st.SaveInbound(ctx, link.ID, InboundUpdate{RefID: &id, SeenMark: &empty, Seen: map[string]string{}})
		}
		return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
	}
	// Tatsächliche Werte samt Vorgaben des Dienstes (z. B. Fälligkeit): der
	// Datensatz ist neu, kein Benutzer kann ihn schon geändert haben.
	actual, err := r.target.Read(ctx, id)
	if err != nil {
		return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
	}
	set := r.deviations(actual, c.mapped)
	set, del := r.finalWritten(link.WrittenValues, ahead, set, c.mapped, actual, nil)
	noErr := ""
	err = r.hook("commit", c.fk)
	if err == nil {
		err = st.SaveInbound(ctx, link.ID, InboundUpdate{RefID: &id, SeenMark: &c.mark, Seen: c.mapped,
			WrittenSet: set, WrittenDel: del, LastError: &noErr})
	}
	if err != nil {
		// Der Datensatz hat seinen Herkunftseintrag: die Wiederholung bindet ihn an.
		return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
	}
	switch {
	case !c.backfill:
		r.run.New++
	case opt.Archive:
		r.run.Backfilled++
		r.run.Archived++
	default:
		r.run.Backfilled++
	}
	return nil
}

// processChanged gleicht eine verknüpfte Zeile ab. Ein Fehler als Rückgabe
// beendet den Lauf; Zeilenfehler stehen danach an der Verknüpfung.
func (r *inboundRun) processChanged(ctx context.Context, c candidate, link *Link) error {
	st := r.in.Store
	user := r.in.SystemUser
	if len(c.errs) > 0 {
		return r.rowFailed(ctx, link.ID, c.fk, c.errs...)
	}
	id := *link.RefID
	pdhNow, err := r.target.Read(ctx, id)
	if errors.Is(err, ErrNotFound) {
		// Nicht neu anlegen; Endzustand bis zur nächsten Änderung der Fremdzeile.
		return r.terminal(ctx, link.ID, c.fk, &c.mark, LinkErrRecordGone)
	}
	if err != nil {
		return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
	}
	dec := Decide(r.maps, link.SeenValues, link.WrittenValues, c.mapped, pdhNow)
	actual := pdhNow
	var ahead map[string]string
	if len(dec.Apply) > 0 {
		vals, intended, notes := r.planApply(dec.Apply, pdhNow)
		for k, v := range dec.Apply {
			if _, seen := link.SeenValues[k]; !seen && pdhNow[k] != "" && pdhNow[k] != v {
				notes[k] = noteFirstSync // überschriebener PDH-Wert steht so im Verlauf
			}
		}
		ahead = r.deviations(intended, c.mapped)
		if err := r.writeAhead(ctx, link.ID, ahead); err != nil {
			return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
		}
		// Eine Wiederholung nach Teilfehler übernimmt dieselben Werte erneut:
		// Apply schreibt gleiche Werte nicht noch einmal (kein doppelter Verlauf).
		if err := r.target.Apply(ctx, id, vals, notes, user); err != nil {
			return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
		}
		err := r.hook("postread", c.fk)
		if err == nil {
			// Nur für das Aufräumen (PDH gleich Fremdwert), nie als Ausgangswert.
			actual, err = r.target.Read(ctx, id)
		}
		if err != nil {
			return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
		}
	}
	set, del := r.finalWritten(link.WrittenValues, ahead, ahead, c.mapped, actual, dec.KeepPDH)
	noErr := ""
	u := InboundUpdate{SeenMark: &c.mark, Seen: c.mapped, WrittenSet: set, WrittenDel: del, LastError: &noErr}
	if len(dec.KeepPDH) > 0 {
		keys := make([]string, 0, len(dec.KeepPDH))
		for k := range dec.KeepPDH {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		u.Outbox = map[string]string{}
		for _, k := range keys {
			lost := dec.ForeignLost[k]
			u.Notes = append(u.Notes, HistoryNote{RefType: r.p.TargetType, RefID: id, Action: actionSync, Field: k,
				OldValue: dec.KeepPDH[k], NewValue: lost, CreatedBy: user,
				Message: fmt.Sprintf("Wert im Fremdsystem ‚%s‘ nicht übernommen, PDH-Wert bleibt", lost)})
			u.Outbox[k] = dec.KeepPDH[k]
		}
	}
	// Verknüpfung, Vermerke und Wartelisteneintrag gemeinsam (eine Transaktion).
	err = r.hook("commit", c.fk)
	if err == nil {
		err = st.SaveInbound(ctx, link.ID, u)
	}
	if err != nil {
		return r.rowFailed(ctx, link.ID, c.fk, RunError{Message: err.Error()})
	}
	if len(dec.Apply) > 0 || len(dec.KeepPDH) > 0 {
		r.run.Changed++
	}
	return nil
}

func (r *inboundRun) addPreview(pv Preview) {
	if len(r.previews) < r.previewLimit() {
		r.previews = append(r.previews, pv)
	}
}

func withKey(fk string, errs []RunError) []RunError {
	out := make([]RunError, len(errs))
	for i, e := range errs {
		e.ForeignKey = fk
		out[i] = e
	}
	return out
}

func (r *inboundRun) previewNew(c candidate) {
	values, errs := c.mapped, c.errs
	if len(errs) == 0 {
		values, errs = r.complete(c.mapped)
	}
	archive := c.backfill && len(errs) == 0 && isFinalStatus(values["status"])
	switch {
	case len(errs) > 0:
		r.run.Failed++
		r.run.Errors = append(r.run.Errors, withKey(c.fk, errs)...)
	case !c.backfill:
		r.run.New++
	default:
		r.run.Backfilled++
		if archive {
			r.run.Archived++
		}
	}
	r.addPreview(Preview{ForeignKey: c.fk, Values: values, Errors: withKey(c.fk, errs), IsNew: true,
		Backfill: c.backfill, Archive: archive})
}

func (r *inboundRun) previewChanged(ctx context.Context, c candidate, link *Link) error {
	errs := c.errs
	var apply map[string]string
	if len(errs) == 0 {
		pdhNow, err := r.target.Read(ctx, *link.RefID)
		switch {
		case errors.Is(err, ErrNotFound):
			errs = []RunError{{Message: LinkErrRecordGone}}
		case err != nil:
			return err
		default:
			dec := Decide(r.maps, link.SeenValues, link.WrittenValues, c.mapped, pdhNow)
			if len(dec.Apply) == 0 && len(dec.KeepPDH) == 0 {
				return nil
			}
			apply = dec.Apply
		}
	}
	if len(errs) > 0 {
		r.run.Failed++
		r.run.Errors = append(r.run.Errors, withKey(c.fk, errs)...)
	} else {
		r.run.Changed++
	}
	r.addPreview(Preview{ForeignKey: c.fk, Values: apply, Errors: withKey(c.fk, errs), IsNew: false})
	return nil
}
