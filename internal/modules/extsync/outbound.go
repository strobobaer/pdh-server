package extsync

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Hinausgehender Abgleich – Regeln (verbindlich, Entwurf 4.3, 4.4, 4.11, R17, R23):
//
//   - Geschrieben wird AUSSCHLIESSLICH über Dialect.UpdateRow (eine Zeile der
//     Meldungstabelle über den Schlüssel der Verknüpfung) und Dialect.InsertChild
//     (Kommentar in die Nebentabelle). Foreign.Exec bekommt nie einen anderen
//     Befehl. Schlüsselwerte kommen nur aus der Verknüpfung (gelesene Zeile).
//   - Ein Eintrag "update" schreibt die Felder aus payload.fields, die eine
//     Zuordnung hinaus/beides haben (ohne festen Wert, mit Fremdspalte), mit dem
//     AKTUELLEN PDH-Wert (Target.Read), nie dem Wert im Eintrag. Ausgelassen wird
//     ein Feld, dessen aktueller Wert gleich seen[f] ist (das Fremdsystem hat ihn)
//     oder gleich written[f] (schon geschrieben bzw. vom Abgleich selbst gesetzter
//     Ausgangswert, R17: Vorgaben und Umsetzungen des Abgleichs gehen nie hinaus).
//     Bleibt nichts, ist der Eintrag ohne Befehl erledigt.
//   - Reihenfolge je Eintrag: Fremdbefehl → written[f] feldweise (Zeilensperre,
//     Store.UpdateLinkWritten) → erledigt (Aufrufer). Jeder Fehler lässt den
//     Eintrag wiederholbar; ein UPDATE mit aktuellen Werten ist wiederholbar.
//   - seen_values und seen_mark schreibt nur der hereinkommende Lauf. last_error
//     der Verknüpfung (Wiederholungsmarke des hereinkommenden Laufs) setzt der
//     Ausgang allein für den Endzustand LinkErrForeignGone (Store.MarkLinkForeignGone);
//     eigene Fehler stehen am Eintrag (sync_outbox.last_error/attempts).
//   - Zeitwerte (Entwurf 4.11): zonenlose Datums-/Zeitspalte → Wanduhr am Ort
//     des Profils (InstantToForeignWall); reine Datumsspalte → Kalendertag am Ort
//     des Profils; alles andere (datetimeoffset, Text) → kanonischer Text (UTC).
//   - Im PDH-Verlauf stehen nur Endzustände und übergangene Einträge, keine
//     Routinemeldungen.

// ErrRowGone: der Änderungsbefehl traf keine Zeile – die Meldung ist im
// Fremdsystem verschwunden. Der Eintrag ist erledigt (nicht wiederholt).
var ErrRowGone = errors.New("Meldung im Fremdsystem nicht mehr vorhanden")

// ErrWriteNotAllowed: an der Verbindung ist "Schreiben erlaubt" aus. Der Eintrag
// bleibt und wird später wiederholt.
var ErrWriteNotAllowed = errors.New("Schreiben ist an der Verbindung nicht erlaubt")

// Aktion im PDH-Verlauf für Meldungen des Hinausschreibens.
const actionSyncOut = "sync_out"

// Texte im Verlauf bzw. als Spur am erledigten Eintrag.
const (
	noteOutRowGone      = "Änderung konnte nicht ins Fremdsystem geschrieben werden: Meldung dort nicht mehr vorhanden"
	noteOutCompositeKey = "Kommentare werden bei zusammengesetztem Schlüssel nicht übertragen"
	noteOutMaybeLost    = "Kommentar wurde möglicherweise nicht übertragen (Abbruch während des Schreibens)"
	noteOutBadItem      = "Eintrag der Warteliste ist unlesbar und wurde übergangen"
)

// Outbound schreibt Einträge der Warteliste ins Fremdsystem.
type Outbound struct {
	Store      *Store
	Targets    map[string]Target // nach Profile.TargetType
	SystemUser string            // Akteur der Verlaufseinträge
	// UserName liefert den Anzeigenamen eines Benutzers für die Verfasserspalte
	// (nil oder "" = "PDH").
	UserName func(ctx context.Context, id string) string
	// Location ist der Ort des Servers (nil = time.Local); er gilt, wenn das
	// Profil keine eigene Zeitzone hat.
	Location *time.Location
	// Now liefert die Uhrzeit für Kommentare ohne lesbare Zeit (nil = time.Now).
	Now func() time.Time
	// testHook ist nur für Tests: läuft an benannten Schritten ("written" vor dem
	// Speichern der Ausgangswerte, "sent" nach dem Einfügen eines Kommentars);
	// ein Fehler wirkt wie ein Abbruch an dieser Stelle.
	testHook func(step string) error
}

// OutResult ist das Ergebnis eines Eintrags. Ist err von Process nil, gilt
// Done = true: der Aufrufer erledigt den Eintrag (Store.OutboxDoneNote mit
// Note). Mit err != nil wird er wiederholt (Store.OutboxFailed, Backoff).
type OutResult struct {
	Done    bool
	Written int    // geschriebene Felder bzw. 1 für einen Kommentar
	Note    string // Spur am erledigten Eintrag (leer = gewöhnlich erledigt)
}

// Backoff liefert die Pause nach dem n-ten Fehlversuch eines Eintrags (n ab 1):
// 5 s, 15 s, 1 min, 5 min, danach 15 min.
func Backoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return 5 * time.Second
	case attempts == 2:
		return 15 * time.Second
	case attempts == 3:
		return time.Minute
	case attempts == 4:
		return 5 * time.Minute
	}
	return 15 * time.Minute
}

func (o *Outbound) loc() *time.Location {
	if o.Location == nil {
		return time.Local
	}
	return o.Location
}

func (o *Outbound) hook(step string) error {
	if o.testHook != nil {
		return o.testHook(step)
	}
	return nil
}

func done(note string) (OutResult, error) { return OutResult{Done: true, Note: note}, nil }

// Process bearbeitet genau einen Eintrag der Verknüpfung link (Profil p,
// Zuordnung maps) über f und d. allowWrite ist "Schreiben erlaubt" der
// Verbindung. Kein Absturz verlässt Process.
func (o *Outbound) Process(ctx context.Context, item OutboxItem, link *Link, p *Profile, maps []FieldMap, f Foreign, d Dialect, allowWrite bool) (res OutResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			res, err = OutResult{}, fmt.Errorf("abgleich: interner Fehler beim Hinausschreiben: %v", rec)
		}
	}()
	switch {
	case o == nil || o.Store == nil:
		return OutResult{}, errors.New("abgleich: speicher fehlt")
	case p == nil || link == nil:
		return OutResult{}, errors.New("abgleich: profil oder verknüpfung fehlt")
	case item.LinkID != link.ID || link.ProfileID != p.ID:
		return OutResult{}, errors.New("abgleich: eintrag, verknüpfung und profil passen nicht zusammen")
	case f == nil || d == nil:
		return OutResult{}, errors.New("abgleich: verbindung zum fremdsystem fehlt")
	}
	if link.LastError == LinkErrForeignGone {
		// Endzustand, schon gemeldet: die Zeile nicht weiter anschreiben. Der
		// hereinkommende Lauf löst die Marke, wenn die Zeile wieder auftaucht.
		return done(LinkErrForeignGone)
	}
	switch item.Kind {
	case KindUpdate:
		return o.update(ctx, item, link, p, maps, f, d, allowWrite)
	case KindComment:
		return o.comment(ctx, item, link, p, f, d, allowWrite)
	}
	return o.badItem(ctx, link, p, fmt.Sprintf("Art ‚%s‘ ist unbekannt", item.Kind))
}

// rowGone: der Befehl traf keine Zeile. Erledigt, Verknüpfung bleibt, nichts
// wird angelegt; Marke und Verlaufseintrag genau einmal (eine Transaktion).
func (o *Outbound) rowGone(ctx context.Context, link *Link, p *Profile) (OutResult, error) {
	var n *HistoryNote
	if link.RefID != nil {
		n = &HistoryNote{RefType: p.TargetType, RefID: *link.RefID, Action: actionSyncOut,
			Message: noteOutRowGone, CreatedBy: o.SystemUser}
	}
	if _, err := o.Store.MarkLinkForeignGone(ctx, link.ID, n); err != nil {
		return OutResult{}, err
	}
	return done(ErrRowGone.Error())
}

// badItem erledigt einen unlesbaren Eintrag: er würde die Verknüpfung sonst für
// immer aufhalten. Spur: Verlaufseintrag am Datensatz und last_error am Eintrag.
func (o *Outbound) badItem(ctx context.Context, link *Link, p *Profile, detail string) (OutResult, error) {
	msg := noteOutBadItem + ": " + detail
	if err := o.note(ctx, link, p, msg); err != nil {
		return OutResult{}, err
	}
	return done(msg)
}

// note schreibt einen Verlaufseintrag am verknüpften Datensatz (falls vorhanden).
func (o *Outbound) note(ctx context.Context, link *Link, p *Profile, msg string) error {
	t := o.Targets[p.TargetType]
	if t == nil || link.RefID == nil {
		return nil
	}
	return t.Note(ctx, *link.RefID, actionSyncOut, "", "", "", msg, o.SystemUser)
}

// outFields: PDH-Felder aus dem Eintrag, die hinausgehen dürfen (Zuordnung
// hinaus/beides, kein fester Wert, mit Fremdspalte, im Feldkatalog).
func outFields(maps []FieldMap, fields []PDHField, payload map[string]json.RawMessage) []string {
	idx := fieldIndex(fields)
	var out []string
	for _, m := range maps {
		if (m.Direction != DirOut && m.Direction != DirBoth) || m.FixedValue != nil || m.ForeignColumn == "" {
			continue
		}
		if _, ok := payload[m.PDHField]; !ok {
			continue
		}
		if _, ok := idx[m.PDHField]; !ok || containsStr(out, m.PDHField) {
			continue
		}
		out = append(out, m.PDHField)
	}
	sort.Strings(out)
	return out
}

func (o *Outbound) update(ctx context.Context, item OutboxItem, link *Link, p *Profile, maps []FieldMap, f Foreign, d Dialect, allowWrite bool) (OutResult, error) {
	var pl struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(item.Payload, &pl); err != nil || pl.Fields == nil {
		return o.badItem(ctx, link, p, "Felder fehlen")
	}
	fields := Fields(p.TargetType)
	if fields == nil {
		return OutResult{}, fmt.Errorf("Ziel %q ist unbekannt", p.TargetType)
	}
	cand := outFields(maps, fields, pl.Fields)
	if len(cand) == 0 {
		return done("")
	}
	if link.RefID == nil {
		return done("")
	}
	target := o.Targets[p.TargetType]
	if target == nil {
		return OutResult{}, fmt.Errorf("Ziel %q ist nicht eingerichtet", p.TargetType)
	}
	cur, err := target.Read(ctx, *link.RefID)
	if errors.Is(err, ErrNotFound) {
		return done(LinkErrRecordGone) // nichts mehr zu schreiben
	}
	if err != nil {
		return OutResult{}, err
	}
	vals := map[string]string{}
	for _, k := range cand {
		v := strings.TrimSpace(cur[k])
		if s, ok := link.SeenValues[k]; ok && strings.TrimSpace(s) == v {
			continue // das Fremdsystem hat den Wert
		}
		if w, ok := link.WrittenValues[k]; ok && strings.TrimSpace(w) == v {
			continue // schon geschrieben oder Ausgangswert des Abgleichs (R17)
		}
		// R30: Bei Feldern nur "hinaus" ist PDH die Quelle; auch Vorgaben, die der
		// Abgleich beim Anlegen gesetzt hat, gehen dann rechtmäßig ins Fremdsystem.
		vals[k] = cur[k]
	}
	if len(vals) == 0 {
		return done("")
	}
	if !allowWrite {
		return OutResult{}, ErrWriteNotAllowed
	}
	fLoc, err := p.ForeignLocation(o.loc())
	if err != nil {
		return OutResult{}, err
	}
	t, err := f.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		return OutResult{}, fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	set, errs := ToForeign(maps, fields, vals)
	if len(errs) > 0 {
		return OutResult{}, errors.New(joinErrs(errs))
	}
	for col, v := range set {
		if tm, ok := v.(time.Time); ok {
			set[col] = foreignTime(t, col, d, tm, fLoc)
		}
	}
	cut := truncateForColumns(t, maps, vals, set)
	kv, err := DecodeKey(link.ForeignKey)
	if err != nil {
		return done(LinkErrBadKey)
	}
	st, err := d.UpdateRow(t, p.KeyColumns, kv, set)
	if err != nil {
		return OutResult{}, err
	}
	n, err := f.Exec(ctx, st)
	if err != nil {
		return OutResult{}, fmt.Errorf("Fremddatenbank schreiben: %w", err)
	}
	if n == 0 {
		return o.rowGone(ctx, link, p)
	}
	if err := o.noteTruncated(ctx, link, p, fields, vals, cut); err != nil {
		return OutResult{}, err
	}
	if err := o.hook("written"); err != nil {
		return OutResult{}, err
	}
	// Ausgangswert je geschriebenem Feld in PDH-Form, feldweise.
	if err := o.Store.UpdateLinkWritten(ctx, link.ID, vals); err != nil {
		return OutResult{}, err
	}
	return OutResult{Done: true, Written: len(vals)}, nil
}

// truncateForColumns kürzt Texte in set auf die Höchstlänge der Fremdspalte (in
// Zeichen, R29) und liefert je gekürztem PDH-Feld die Länge. Andere Werte, die
// nicht passen (z. B. Zahl außerhalb des Bereichs), lehnt der Server ab.
//
// Regel für Echo und Wiederholung: written[f] hält den VOLLEN PDH-Wert (die
// Auslassregel "aktueller Wert == written" greift weiter); der hereinkommende
// Lauf wertet die gekürzte Form des aktuellen PDH-Werts als denselben Wert
// (foldTruncated). Ein written mit dem gekürzten Wert ginge nicht: eine spätere
// echte Änderung im Fremdsystem wäre dann ein Widerspruch statt einer Übernahme.
func truncateForColumns(t *TableInfo, maps []FieldMap, vals map[string]string, set map[string]any) map[string]int {
	cut := map[string]int{}
	for _, m := range maps {
		if (m.Direction != DirOut && m.Direction != DirBoth) || m.FixedValue != nil || m.ForeignColumn == "" {
			continue
		}
		if _, ok := vals[m.PDHField]; !ok {
			continue
		}
		s, ok := set[m.ForeignColumn].(string)
		n := columnMaxLen(t, m.ForeignColumn)
		if !ok || n <= 0 || utf8.RuneCountInString(s) <= n {
			continue
		}
		set[m.ForeignColumn] = truncateRunes(s, n)
		cut[m.PDHField] = n
	}
	return cut
}

// noteTruncated vermerkt eine Kürzung am Datensatz – einmal je Verknüpfung und
// Feld und gekürztem Inhalt: nur wenn die gekürzte Form vom zuletzt
// geschriebenen Wert (written) abweicht.
func (o *Outbound) noteTruncated(ctx context.Context, link *Link, p *Profile, fields []PDHField, vals map[string]string, cut map[string]int) error {
	t := o.Targets[p.TargetType]
	if len(cut) == 0 || t == nil || link.RefID == nil {
		return nil
	}
	idx := fieldIndex(fields)
	keys := make([]string, 0, len(cut))
	for k := range cut {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := cut[k]
		short := truncateRunes(vals[k], n)
		if prev, ok := link.WrittenValues[k]; ok && truncateRunes(prev, n) == short {
			continue
		}
		msg := fmt.Sprintf("Wert für ‚%s‘ wurde beim Schreiben ins Fremdsystem auf %d Zeichen gekürzt", idx[k].Label, n)
		if err := t.Note(ctx, *link.RefID, actionSync, k, vals[k], short, msg, o.SystemUser); err != nil {
			return err
		}
	}
	return nil
}

// commentPayload ist der Eintrag "comment" des Auslösers pdh_sync_enqueue.
type commentPayload struct {
	Text      *string `json:"text"`
	UserID    *string `json:"user_id"`
	CreatedAt *string `json:"created_at"`
}

// comment hängt einen PDH-Kommentar an die Nebentabelle (Fremdschlüssel = der
// EINZIGE Schlüsselwert der Verknüpfung). Kommentartabellen, die auf einen
// zusammengesetzten Schlüssel verweisen, unterstützt dieses Paket nicht: der
// Kommentar wird dann mit Hinweis übergangen statt mit falschem Verweis.
//
// Höchstens einmal: Ein INSERT ist im Fremdsystem nicht wiederholbar. Deshalb
// wird VOR dem Befehl dauerhaft gemerkt, dass gesendet wird (OutboxBeginSend).
// Findet ein späterer Versuch die Marke, ist der Ausgang offen (Absturz oder
// Abbruch während des Schreibens): der Eintrag wird mit Hinweis erledigt –
// lieber ein Kommentar sichtbar verloren als doppelt im Fremdsystem. Nur wenn
// der Server den Befehl eindeutig abgelehnt hat, wird die Marke gelöscht und
// später erneut versucht.
func (o *Outbound) comment(ctx context.Context, item OutboxItem, link *Link, p *Profile, f Foreign, d Dialect, allowWrite bool) (OutResult, error) {
	var pl commentPayload
	if err := json.Unmarshal(item.Payload, &pl); err != nil || pl.Text == nil {
		return o.badItem(ctx, link, p, "Kommentartext fehlt")
	}
	ct := p.CommentTable
	if ct == nil || ct.Table == "" || ct.FKColumn == "" || ct.TextColumn == "" {
		return done("")
	}
	if pl.UserID != nil && o.SystemUser != "" && *pl.UserID == o.SystemUser {
		return done("") // vom Abgleich selbst
	}
	if len(p.KeyColumns) != 1 {
		if err := o.note(ctx, link, p, noteOutCompositeKey); err != nil {
			return OutResult{}, err
		}
		return done(noteOutCompositeKey)
	}
	kv, err := DecodeKey(link.ForeignKey)
	if err != nil || len(kv) != 1 {
		return done(LinkErrBadKey)
	}
	if !allowWrite {
		return OutResult{}, ErrWriteNotAllowed
	}
	fLoc, err := p.ForeignLocation(o.loc())
	if err != nil {
		return OutResult{}, err
	}
	src, err := f.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		return OutResult{}, fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	child, err := f.Describe(ctx, ct.Schema, ct.Table)
	if err != nil {
		return OutResult{}, fmt.Errorf("Aufbau der Kommentartabelle %s.%s lesen: %w", ct.Schema, ct.Table, err)
	}
	// Nie an eine verschwundene Meldung anhängen (nur lesend geprüft).
	sel, err := d.SelectOne(src, nil, p.KeyColumns, kv)
	if err != nil {
		return OutResult{}, err
	}
	rows, err := f.Query(ctx, sel)
	if err != nil {
		return OutResult{}, fmt.Errorf("Fremddatenbank lesen: %w", err)
	}
	if len(rows) == 0 {
		return o.rowGone(ctx, link, p)
	}
	values := map[string]any{
		ct.FKColumn:   kv[0],
		ct.TextColumn: truncateRunes(*pl.Text, columnMaxLen(child, ct.TextColumn)),
	}
	if ct.AuthorColumn != "" {
		name := ""
		if o.UserName != nil && pl.UserID != nil {
			name = strings.TrimSpace(o.UserName(ctx, *pl.UserID))
		}
		if name == "" {
			name = "PDH"
		}
		values[ct.AuthorColumn] = truncateRunes(name, columnMaxLen(child, ct.AuthorColumn))
	}
	if ct.TimeColumn != "" {
		at := o.now()
		if pl.CreatedAt != nil {
			if tm, err := time.Parse(time.RFC3339Nano, *pl.CreatedAt); err == nil {
				at = tm
			}
		}
		values[ct.TimeColumn] = foreignTime(child, ct.TimeColumn, d, at, fLoc)
	}
	st, err := d.InsertChild(src, child, values)
	if err != nil {
		return OutResult{}, err
	}
	already, err := o.Store.OutboxBeginSend(ctx, item.ID)
	if err != nil {
		return OutResult{}, err
	}
	if already {
		if err := o.note(ctx, link, p, noteOutMaybeLost); err != nil {
			return OutResult{}, err
		}
		return done(noteOutMaybeLost)
	}
	n, err := f.Exec(ctx, st)
	if err == nil && n == 0 {
		err = errors.New("Kommentar wurde nicht eingefügt")
	}
	if err != nil {
		if !uncertainExec(ctx, err) {
			// Eindeutig abgelehnt: nichts eingefügt, später erneut versuchen.
			if cerr := o.Store.OutboxClearSend(context.WithoutCancel(ctx), item.ID); cerr != nil {
				return OutResult{}, fmt.Errorf("Fremddatenbank schreiben: %w (Marke nicht gelöscht: %v)", err, cerr)
			}
		}
		return OutResult{}, fmt.Errorf("Fremddatenbank schreiben: %w", err)
	}
	if err := o.hook("sent"); err != nil {
		return OutResult{}, err
	}
	return OutResult{Done: true, Written: 1}, nil
}

func (o *Outbound) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// columnMaxLen: Höchstlänge der Spalte in Zeichen (0 = unbegrenzt/unbekannt).
func columnMaxLen(t *TableInfo, col string) int {
	if c, ok := findColumn(t, col); ok && c.MaxLen > 0 {
		return c.MaxLen
	}
	return 0
}

// uncertainExec: Ist nach diesem Fehler offen, ob der Befehl ausgeführt wurde?
// Abbruch, Zeitgrenze und abgerissene Verbindung: ja (der Server kann den
// Befehl schon ausgeführt haben). Sonst hat der Server ihn abgelehnt.
func uncertainExec(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// foreignTime wandelt einen Zeitpunkt für die Spalte col: zonenlose Datums-/
// Zeitspalte → Wanduhr am Ort loc; reine Datumsspalte → Kalendertag am Ort loc
// (als Mitternacht UTC, wie der Dialekt Datumswerte erwartet); sonst (u. a.
// datetimeoffset, Text) der kanonische Text in UTC – unverändert, und für
// datetimeoffset als ISO 8601 mit Z eindeutig. Fehlt die Spalte, bleibt der
// Wert (der Dialekt lehnt die Spalte ab).
func foreignTime(t *TableInfo, col string, d Dialect, tm time.Time, loc *time.Location) any {
	c, ok := findColumn(t, col)
	if !ok {
		return tm
	}
	isTime, dateOnly := d.TimeColumn(*c)
	switch {
	case isTime && dateOnly:
		w := tm.In(loc)
		return time.Date(w.Year(), w.Month(), w.Day(), 0, 0, 0, 0, time.UTC)
	case isTime:
		return InstantToForeignWall(tm, loc)
	}
	return canonTime(tm)
}
