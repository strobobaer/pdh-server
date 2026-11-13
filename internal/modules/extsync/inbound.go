package extsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

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

// Inbound übernimmt neue und geänderte Zeilen der Fremdtabelle ins PDH.
type Inbound struct {
	Store      *Store
	Targets    map[string]Target // nach Profile.TargetType
	SystemUser string            // Akteur aller Änderungen
	pageSize   int               // 0 = defaultPageSize; Tests setzen kleinere Seiten
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
}

const defaultPageSize = 200

// Meldungen im Lauf.
const msgRecordGone = "Datensatz im PDH nicht mehr vorhanden"

// Zustand einer gelesenen Zeile für die Fortschrittsmarke.
const (
	rowOK        = iota
	rowFailed    // scheitert: Marke bleibt davor, die Zeile kommt wieder
	rowPending   // neue Zeile, noch nicht angelegt
	rowFailedEnd // scheitert dauerhaft (Datensatz gelöscht): hält die Marke nicht auf
)

type rowPos struct {
	cursor string
	state  int
}

// candidate ist eine gelesene Zeile, die (vielleicht) etwas zu tun hat.
type candidate struct {
	pos     int // Index in positions, -1 ohne Marke (none)
	keyVals []string
	fk      string
	mapped  map[string]string
	errs    []RunError
	mark    string
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
	newCount  int
	paused    bool
}

// Run fährt einen hereinkommenden Lauf für das Profil p. Ergebnis ist der Lauf
// mit Zählern und neuer Fortschrittsmarke (Run.Cursor, vom Aufrufer zu
// speichern). Ein Fehler der ganzen Zeile betrifft nur sie; ein Fehler des
// ganzen Laufs (Fremddatenbank nicht erreichbar, Einstellung falsch) schließt
// den Lauf mit dem Grund ab und kommt als err zurück. Kein Absturz verlässt Run.
func (in *Inbound) Run(ctx context.Context, p *Profile, maps []FieldMap, f Foreign, d Dialect, opt RunOptions) (run *Run, previews []Preview, err error) {
	if p == nil {
		return nil, nil, errors.New("abgleich: profil fehlt")
	}
	start := p.Cursor
	if p.ChangeKind == changeKindNone || p.ChangeKind == "" {
		start = ""
	}
	run = &Run{ProfileID: p.ID, Direction: DirIn, StartedAt: time.Now(), Cursor: start}
	if !opt.Dry {
		id, serr := in.Store.StartRun(ctx, p.ID, DirIn)
		if serr != nil {
			return run, nil, serr
		}
		run.ID = id
	}
	r := &inboundRun{in: in, p: p, maps: maps, d: d, opt: opt, run: run}
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

// cursor: Marke der letzten Zeile vor der ersten fehlerhaften oder nicht
// angelegten Zeile (in Lesefolge); ohne Änderungsspalte leer.
func (r *inboundRun) cursor(start string) string {
	if r.p.ChangeKind == changeKindNone || r.p.ChangeKind == "" {
		return ""
	}
	cur := start
	for _, ps := range r.positions {
		if ps.state == rowFailed || ps.state == rowPending {
			break
		}
		cur = ps.cursor
	}
	return cur
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
	if !r.opt.Dry {
		if err := r.adoptOrphans(ctx); err != nil {
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
	cols := inboundColumns(r.maps)
	cursor := r.run.Cursor
	for {
		st, err := r.d.SelectRows(t, cols, p.KeyColumns, p.ChangeColumn, p.ChangeKind, cursor, p.FilterSQL, p.WindowDays, pageSize)
		if err != nil {
			return err
		}
		rows, err := f.Query(ctx, st)
		if err != nil {
			return fmt.Errorf("Fremddatenbank lesen: %w", err)
		}
		for _, row := range rows {
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
		if r.processNew(ctx, c) {
			r.setState(c.pos, rowOK)
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

// adoptOrphans bindet reservierte Verknüpfungen an Datensätze, die vor einem
// Abbruch schon angelegt wurden (Herkunftseintrag). Ohne Fund gilt die Zeile
// beim Lesen wieder als neu.
func (r *inboundRun) adoptOrphans(ctx context.Context) error {
	links, err := r.in.Store.OrphanLinks(ctx, r.p.ID)
	if err != nil {
		return err
	}
	for _, l := range links {
		id, found, err := r.target.FindOrigin(ctx, r.p.ID, l.ForeignKey)
		if err != nil {
			return err
		}
		if found {
			if err := r.in.Store.AttachLink(ctx, l.ID, id, "", map[string]string{}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *inboundRun) setState(pos, state int) {
	if pos >= 0 {
		r.positions[pos].state = state
	}
}

// fail zählt die Zeile als gescheitert und nimmt ihre Fehler in den Lauf.
func (r *inboundRun) fail(c candidate, state int, errs ...RunError) {
	r.run.Failed++
	for _, e := range errs {
		e.ForeignKey = c.fk
		r.run.Errors = append(r.run.Errors, e)
	}
	r.setState(c.pos, state)
}

func joinErrs(errs []RunError) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Message
	}
	return strings.Join(parts, "; ")
}

// readRow ordnet eine gelesene Zeile ein. Fehler hier betreffen den ganzen Lauf.
func (r *inboundRun) readRow(ctx context.Context, row map[string]any) error {
	p := r.p
	kv, err := r.d.KeyValues(r.t, p.KeyColumns, row)
	if err != nil {
		return err
	}
	c := candidate{pos: -1, keyVals: kv, fk: EncodeKey(kv)}
	if p.ChangeKind != changeKindNone && p.ChangeKind != "" {
		cur, err := r.d.NextCursor(r.t, p.KeyColumns, p.ChangeColumn, p.ChangeKind, row)
		if err != nil {
			return err
		}
		r.positions = append(r.positions, rowPos{cursor: cur, state: rowOK})
		c.pos = len(r.positions) - 1
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

	link, err := r.in.Store.LinkByKey(ctx, p.ID, c.fk)
	if errors.Is(err, ErrNotFound) {
		link = nil
	} else if err != nil {
		return err
	}
	if link != nil && link.RefID != nil {
		if link.SeenMark == c.mark && r.seenComplete(link) {
			return nil // unverändert
		}
		if r.opt.Dry {
			return r.previewChanged(ctx, c, link)
		}
		r.processChanged(ctx, c, link)
		return nil
	}
	// Neu (oder reserviert ohne Datensatz).
	r.newCount++
	if r.opt.Dry {
		r.previewNew(c)
		return nil
	}
	if r.newCount > r.maxNew() {
		r.paused = true
	}
	r.setState(c.pos, rowPending)
	r.pending = append(r.pending, c)
	return nil
}

// seenComplete: Für jedes hereinkommende Feld gibt es einen gesehenen Wert.
// Fehlt einer (Feld später zugeordnet), wird die Zeile abgeglichen, auch wenn
// sich ihre Marke nicht geändert hat.
func (r *inboundRun) seenComplete(l *Link) bool {
	for _, m := range r.maps {
		if m.FixedValue != nil || (m.Direction != DirIn && m.Direction != DirBoth) {
			continue
		}
		if _, ok := l.SeenValues[m.PDHField]; !ok {
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

func (r *inboundRun) originNote(c candidate) string {
	return fmt.Sprintf("%s|%s|Aus %s, Nummer %s", r.p.ID, c.fk, r.p.Name, strings.Join(c.keyVals, ", "))
}

// processNew legt den Datensatz genau einmal an: reservieren, anlegen samt
// Herkunft, anbinden. Liefert true bei Erfolg.
func (r *inboundRun) processNew(ctx context.Context, c candidate) bool {
	st := r.in.Store
	if len(c.errs) > 0 {
		r.fail(c, rowFailed, c.errs...)
		return false
	}
	values, errs := r.complete(c.mapped)
	if len(errs) > 0 {
		r.fail(c, rowFailed, errs...)
		return false
	}
	link, created, err := st.ReserveLink(ctx, r.p.ID, c.fk, r.p.TargetType)
	if err != nil {
		r.fail(c, rowFailed, RunError{Message: err.Error()})
		return false
	}
	if !created {
		if link.RefID == nil {
			// Reserviert, aber nicht angebunden (Abbruch eines früheren Laufs).
			id, found, err := r.target.FindOrigin(ctx, r.p.ID, c.fk)
			if err != nil {
				r.fail(c, rowFailed, RunError{Message: err.Error()})
				return false
			}
			if found {
				if err := st.AttachLink(ctx, link.ID, id, "", map[string]string{}); err != nil {
					r.fail(c, rowFailed, RunError{Message: err.Error()})
					return false
				}
				link.RefID, link.SeenMark, link.SeenValues = &id, "", map[string]string{}
				return r.processChanged(ctx, c, link)
			}
		} else {
			return r.processChanged(ctx, c, link)
		}
	}
	id, err := r.target.Create(ctx, values, r.in.SystemUser, r.originNote(c))
	if err != nil {
		msg := err.Error()
		if id != "" {
			// Datensatz samt Herkunft existiert, eine Nacharbeit fehlte: anbinden ohne
			// gesehenen Stand, damit der nächste Lauf die Felder abgleicht.
			_ = st.AttachLink(ctx, link.ID, id, "", map[string]string{})
		}
		_ = st.SetLinkError(ctx, link.ID, msg)
		r.fail(c, rowFailed, RunError{Message: msg})
		return false
	}
	if err := st.AttachLink(ctx, link.ID, id, c.mark, c.mapped); err != nil {
		// Der Datensatz hat seinen Herkunftseintrag: der nächste Lauf bindet ihn an.
		r.fail(c, rowFailed, RunError{Message: err.Error()})
		return false
	}
	r.run.New++
	return true
}

// processChanged gleicht eine verknüpfte Zeile ab. Liefert true bei Erfolg.
func (r *inboundRun) processChanged(ctx context.Context, c candidate, link *Link) bool {
	st := r.in.Store
	user := r.in.SystemUser
	if len(c.errs) > 0 {
		_ = st.SetLinkError(ctx, link.ID, joinErrs(c.errs))
		r.fail(c, rowFailed, c.errs...)
		return false
	}
	id := *link.RefID
	pdhNow, err := r.target.Read(ctx, id)
	if errors.Is(err, ErrNotFound) {
		// Nicht neu anlegen. Die Zeile hält die Marke nicht auf: ein neuer Versuch
		// änderte nichts, solange der Datensatz fehlt.
		_ = st.SetLinkError(ctx, link.ID, msgRecordGone)
		r.fail(c, rowFailedEnd, RunError{Message: msgRecordGone})
		return false
	}
	if err != nil {
		r.fail(c, rowFailed, RunError{Message: err.Error()})
		return false
	}
	dec := Decide(r.maps, link.SeenValues, link.WrittenValues, c.mapped, pdhNow)
	if len(dec.Apply) > 0 {
		notes := map[string]string{}
		for k, v := range dec.Apply {
			if _, seen := link.SeenValues[k]; !seen && pdhNow[k] != "" && pdhNow[k] != v {
				notes[k] = noteFirstSync // überschriebener PDH-Wert steht so im Verlauf
			}
		}
		if err := r.target.Apply(ctx, id, dec.Apply, notes, user); err != nil {
			_ = st.SetLinkError(ctx, link.ID, err.Error())
			r.fail(c, rowFailed, RunError{Message: err.Error()})
			return false
		}
	}
	if len(dec.KeepPDH) > 0 {
		keys := make([]string, 0, len(dec.KeepPDH))
		for k := range dec.KeepPDH {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fields := map[string]string{}
		for _, k := range keys {
			lost := dec.ForeignLost[k]
			msg := fmt.Sprintf("Wert im Fremdsystem ‚%s‘ nicht übernommen, PDH-Wert bleibt", lost)
			if err := r.target.Note(ctx, id, actionSync, k, dec.KeepPDH[k], lost, msg, user); err != nil {
				r.fail(c, rowFailed, RunError{Field: k, Message: err.Error()})
				return false
			}
			fields[k] = dec.KeepPDH[k]
		}
		if err := st.Enqueue(ctx, link.ID, KindUpdate, map[string]any{"fields": fields}); err != nil {
			r.fail(c, rowFailed, RunError{Message: err.Error()})
			return false
		}
	}
	if err := st.UpdateLinkSeen(ctx, link.ID, c.mark, c.mapped); err != nil {
		r.fail(c, rowFailed, RunError{Message: err.Error()})
		return false
	}
	if link.LastError != "" {
		_ = st.SetLinkError(ctx, link.ID, "")
	}
	if len(dec.Apply) > 0 || len(dec.KeepPDH) > 0 {
		r.run.Changed++
	}
	return true
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
	if len(errs) > 0 {
		r.run.Failed++
		r.run.Errors = append(r.run.Errors, withKey(c.fk, errs)...)
	} else {
		r.run.New++
	}
	r.addPreview(Preview{ForeignKey: c.fk, Values: values, Errors: withKey(c.fk, errs), IsNew: true})
}

func (r *inboundRun) previewChanged(ctx context.Context, c candidate, link *Link) error {
	errs := c.errs
	var apply map[string]string
	if len(errs) == 0 {
		pdhNow, err := r.target.Read(ctx, *link.RefID)
		switch {
		case errors.Is(err, ErrNotFound):
			errs = []RunError{{Message: msgRecordGone}}
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
