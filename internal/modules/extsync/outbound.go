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
)

// Hinausgehender Abgleich – Regeln (verbindlich, Entwurf 4.3, 4.4, 4.11, R17, R23, R29–R34):
//
//   - Geschrieben wird AUSSCHLIESSLICH über Dialect.UpdateRow (eine Zeile der
//     Meldungstabelle über den Schlüssel der Verknüpfung) und Dialect.InsertChild
//     (Kommentar in die Nebentabelle). Foreign.Exec bekommt nie einen anderen
//     Befehl. Schlüsselwerte kommen nur aus der Verknüpfung (gelesene Zeile).
//     Abwehr in der Tiefe zu R31: passt die Zahl der Schlüsselwerte nicht zu den
//     Schlüsselspalten des Profils, wird nichts geschrieben.
//   - Ein Eintrag "update" schreibt die Felder aus payload.fields, die eine
//     Zuordnung hinaus/beides haben (ohne festen Wert, mit Fremdspalte), mit dem
//     AKTUELLEN PDH-Wert (Target.Read), nie dem Wert im Eintrag. Ausgelassen wird
//     ein Feld (R32), wenn (b) es written[f] gibt und der aktuelle Wert gleich ist
//     (schon geschrieben bzw. vom Abgleich gesetzter Ausgangswert, R17), oder (a)
//     die Fremdzeile – vorher gelesen und wie herein in PDH-Form gebracht
//     (foreignPDH) – den Wert schon hat, auch in gekürzter Form (truncatedForm).
//     Bleibt nichts, ist der Eintrag ohne Befehl erledigt.
//   - Reihenfolge je Eintrag: Fremdbefehl → written[f] feldweise (Zeilensperre,
//     Store.UpdateLinkWritten) → Kürzungsvermerk → erledigt (Aufrufer). Jeder
//     Fehler lässt den Eintrag wiederholbar; ein UPDATE ist wiederholbar.
//   - seen_values und seen_mark schreibt nur der hereinkommende Lauf. last_error
//     der Verknüpfung (Wiederholungsmarke des hereinkommenden Laufs) setzt der
//     Ausgang allein für den Endzustand LinkErrForeignGone (Store.MarkLinkForeignGone);
//     eigene Fehler stehen am Eintrag (sync_outbox.last_error/attempts), hängende
//     werden einmal am Datensatz gemeldet (ReportStuck).
//   - Zeitwerte (Entwurf 4.11, R28): zonenlose Datums-/Zeitspalte → Wanduhr am
//     Ort des Profils (InstantToForeignWall); reine Datumsspalte → Kalendertag am
//     Ort des Profils; alles andere (datetimeoffset, Text) → kanonischer Text (UTC).
//   - Texte werden nach dem Maß des Dialekts gekürzt (Dialect.TruncateText, R33).
//   - Im PDH-Verlauf stehen nur Endzustände, Kürzungen, hängende und übergangene
//     Einträge, keine Routinemeldungen.

// ErrRowGone: die Meldung ist im Fremdsystem verschwunden. Der Eintrag ist
// erledigt (nicht wiederholt).
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
	noteOutKeyMismatch  = "Schlüssel der Verknüpfung passt nicht zu den Schlüsselspalten des Profils – nichts geschrieben"
	noteOutStuckPrefix  = "Änderung konnte bisher nicht ins Fremdsystem geschrieben werden: "
)

// StuckAfterAttempts: nach so vielen Fehlversuchen gilt ein Eintrag als hängend
// und wird einmal am Datensatz gemeldet (R34).
const StuckAfterAttempts = 5

// stuckDetailMax: so viele Zeichen des Fehlertexts stehen im Vermerk.
const stuckDetailMax = 300

// commentInsertTimeout: Zeitgrenze des Einfügens eines Kommentars; es läuft ohne
// den Abbruch des Aufrufers, damit ein Halt es nicht mitten im Senden trifft.
const commentInsertTimeout = 30 * time.Second

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

// ShouldReportStuck: Der Aufrufer meldet einen hängenden Eintrag (ReportStuck)
// genau dann, wenn die Zahl der Fehlversuche NACH diesem Fehlschlag
// StuckAfterAttempts erreicht (nur bei genau diesem Wert).
func ShouldReportStuck(attemptsAfterThisFailure int) bool {
	return attemptsAfterThisFailure == StuckAfterAttempts
}

// ReportStuck meldet einen hängenden Eintrag einmal am PDH-Datensatz (R34):
// Verlaufseintrag (Aktion "sync") "Änderung konnte bisher nicht ins
// Fremdsystem geschrieben werden: <Fehler, höchstens 300 Zeichen>". Ein zweiter
// Aufruf für denselben Eintrag schreibt nichts (sync_outbox.stuck_reported_at;
// Marke und Verlaufseintrag in einer Transaktion). last_error der Verknüpfung
// bleibt unberührt; der Eintrag bleibt offen und wird weiter versucht.
func (o *Outbound) ReportStuck(ctx context.Context, item OutboxItem, link *Link, p *Profile, cause error) error {
	if o == nil || o.Store == nil || link == nil || p == nil || item.LinkID != link.ID {
		return errors.New("abgleich: eintrag, verknüpfung oder profil fehlt")
	}
	detail := "unbekannter Fehler"
	if cause != nil {
		detail = truncateRunes(cause.Error(), stuckDetailMax)
	}
	var n *HistoryNote
	if link.RefID != nil {
		n = &HistoryNote{RefType: p.TargetType, RefID: *link.RefID, Action: actionSync,
			Message: noteOutStuckPrefix + detail, CreatedBy: o.SystemUser}
	}
	_, err := o.Store.MarkOutboxStuck(ctx, item.ID, n)
	return err
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
	if item.Kind != KindUpdate && item.Kind != KindComment {
		return o.badItem(ctx, link, p, fmt.Sprintf("Art ‚%s‘ ist unbekannt", item.Kind))
	}
	kv, err := DecodeKey(link.ForeignKey)
	if err != nil {
		return done(LinkErrBadKey)
	}
	// Abwehr in der Tiefe (R31): Die Quelle eines Profils mit Verknüpfungen ist
	// fest (SaveProfile). Passt der Schlüssel trotzdem nicht zu den
	// Schlüsselspalten, könnte er eine fremde Zeile treffen: nichts schreiben.
	if len(kv) != len(p.KeyColumns) {
		if err := o.note(ctx, link, p, noteOutKeyMismatch); err != nil {
			return OutResult{}, err
		}
		return done(noteOutKeyMismatch)
	}
	if item.Kind == KindUpdate {
		return o.update(ctx, item, link, p, maps, f, d, kv, allowWrite)
	}
	return o.comment(ctx, item, link, p, f, d, kv, allowWrite)
}

// rowGone: die Meldung ist im Fremdsystem verschwunden. Erledigt, Verknüpfung
// bleibt, nichts wird angelegt; Marke und Verlaufseintrag genau einmal (eine
// Transaktion).
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

// outMaps: Zuordnungen, die hinausgehen dürfen (hinaus/beides, kein fester
// Wert, mit Fremdspalte, im Feldkatalog), je PDH-Feld höchstens eine.
func outMaps(maps []FieldMap, fields []PDHField) map[string]FieldMap {
	idx := fieldIndex(fields)
	out := map[string]FieldMap{}
	for _, m := range maps {
		if (m.Direction != DirOut && m.Direction != DirBoth) || m.FixedValue != nil || m.ForeignColumn == "" {
			continue
		}
		if _, ok := idx[m.PDHField]; !ok {
			continue
		}
		if _, dup := out[m.PDHField]; !dup {
			out[m.PDHField] = m
		}
	}
	return out
}

func (o *Outbound) update(ctx context.Context, item OutboxItem, link *Link, p *Profile, maps []FieldMap, f Foreign, d Dialect, kv []string, allowWrite bool) (OutResult, error) {
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
	out := outMaps(maps, fields)
	var cand []string
	for k := range pl.Fields {
		if _, ok := out[k]; ok {
			cand = append(cand, k)
		}
	}
	sort.Strings(cand)
	if len(cand) == 0 || link.RefID == nil {
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
	// (b) Ausgangswert: schon geschrieben oder vom Abgleich selbst gesetzt (R17).
	// Bei Feldern nur "hinaus" ist PDH die Quelle (R30): auch Vorgaben, die der
	// Abgleich beim Anlegen gesetzt hat, gehen dann rechtmäßig ins Fremdsystem.
	var rest []string
	for _, k := range cand {
		if w, ok := link.WrittenValues[k]; ok && strings.TrimSpace(w) == strings.TrimSpace(cur[k]) {
			continue
		}
		rest = append(rest, k)
	}
	if len(rest) == 0 {
		return done("")
	}
	fLoc, err := p.ForeignLocation(o.loc())
	if err != nil {
		return OutResult{}, err
	}
	t, err := f.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		return OutResult{}, fmt.Errorf("Aufbau der Tabelle %s.%s lesen: %w", p.SourceSchema, p.SourceTable, err)
	}
	// (a) Fremdzeile lesen und wie herein in PDH-Form bringen (R32).
	readMaps, cols := outReadMaps(out, rest)
	sel, err := d.SelectOne(t, cols, p.KeyColumns, kv)
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
	// Nicht umsetzbare Fremdwerte (Fehler) fehlen in fv und gelten als verschieden.
	fv, _ := foreignPDH(readMaps, fields, rows[0], wallColumns(t, d, readMaps, fields), fLoc, p.Defaults)
	vals := map[string]string{}
	for _, k := range rest {
		if x, ok := fv[k]; ok {
			tx := strings.TrimSpace(x)
			if tx == strings.TrimSpace(cur[k]) {
				continue
			}
			if c, ok := findColumn(t, out[k].ForeignColumn); ok && len(out[k].Translate) == 0 && truncatedForm(d, *c, cur[k]) == tx {
				continue // das Fremdsystem hält die gekürzte Form
			}
		}
		vals[k] = cur[k]
	}
	if len(vals) == 0 {
		return done("")
	}
	if !allowWrite {
		return OutResult{}, ErrWriteNotAllowed
	}
	st, cut, errs, err := buildOutUpdate(t, d, p.KeyColumns, kv, maps, fields, vals, fLoc)
	if len(errs) > 0 {
		return OutResult{}, errors.New(joinErrs(errs))
	}
	if err != nil {
		return OutResult{}, err
	}
	n, err := f.Exec(ctx, st)
	if err != nil {
		return OutResult{}, fmt.Errorf("Fremddatenbank schreiben: %w", err)
	}
	if n == 0 {
		return o.rowGone(ctx, link, p) // zwischen Lesen und Schreiben verschwunden
	}
	if err := o.hook("written"); err != nil {
		return OutResult{}, err
	}
	// Ausgangswert je geschriebenem Feld in PDH-Form, feldweise.
	if err := o.Store.UpdateLinkWritten(ctx, link.ID, vals); err != nil {
		return OutResult{}, err
	}
	// Erst nach dem Speichern: eine Wiederholung lässt das Feld dann über (b)
	// aus und vermerkt nichts doppelt.
	if err := o.noteTruncated(ctx, link, p, fields, d, vals, cut); err != nil {
		return OutResult{}, err
	}
	return OutResult{Done: true, Written: len(vals)}, nil
}

// outReadMaps: die Zuordnungen der Felder keys (aus outMaps) zum Lesen der
// Fremdzeile in PDH-Form (Richtung herein, damit ToPDH und wallColumns sie
// nehmen) und die zu lesenden Spalten. Ausgang und Schreibversuch lesen so.
func outReadMaps(out map[string]FieldMap, keys []string) ([]FieldMap, []string) {
	var readMaps []FieldMap
	var cols []string
	for _, k := range keys {
		m, ok := out[k]
		if !ok {
			continue
		}
		m.Direction = DirIn // nur zum Lesen
		readMaps = append(readMaps, m)
		if !containsStr(cols, m.ForeignColumn) {
			cols = append(cols, m.ForeignColumn)
		}
	}
	return readMaps, cols
}

// buildOutUpdate baut den Änderungsbefehl für die PDH-Werte vals (PDH-Form)
// genau so, wie der Ausgang schreibt: ToForeign (Übersetzung), Zeitwerte nach
// der Zeitzone des Profils (foreignTime), Texte nach dem Maß des Dialekts
// gekürzt (truncateForColumns), der Befehl allein von Dialect.UpdateRow. cut
// nennt je gekürztem PDH-Feld die Spalte. Fehler der Werte kommen als errs
// (dann kein Befehl). Einzige Stelle, an der ein Änderungsbefehl entsteht: der
// Ausgang und der Schreibversuch im Cockpit rufen beide sie auf.
func buildOutUpdate(t *TableInfo, d Dialect, keyCols, kv []string, maps []FieldMap, fields []PDHField, vals map[string]string, fLoc *time.Location) (st Stmt, cut map[string]Column, errs []RunError, err error) {
	set, errs := ToForeign(maps, fields, vals)
	if len(errs) > 0 {
		return Stmt{}, nil, errs, nil
	}
	for col, v := range set {
		if tm, ok := v.(time.Time); ok {
			set[col] = foreignTime(t, col, d, tm, fLoc)
		}
	}
	cut = truncateForColumns(t, d, maps, vals, set)
	st, err = d.UpdateRow(t, keyCols, kv, set)
	if err != nil {
		return Stmt{}, nil, nil, err
	}
	return st, cut, nil, nil
}

// truncateForColumns kürzt Texte in set nach dem Maß des Dialekts auf die
// Fremdspalte (R29, R33) und liefert je gekürztem PDH-Feld die Spalte. Andere
// Werte, die nicht passen (z. B. Zahl außerhalb des Bereichs), lehnt der Server ab.
//
// Regel für Echo und Wiederholung: written[f] hält den VOLLEN PDH-Wert (die
// Auslassregel (b) greift weiter); der hereinkommende Lauf und das Lesen vor
// dem Ändern werten die gekürzte Form (truncatedForm) als denselben Wert. Ein
// written mit dem gekürzten Wert ginge nicht: eine spätere echte Änderung im
// Fremdsystem wäre dann ein Widerspruch statt einer Übernahme.
func truncateForColumns(t *TableInfo, d Dialect, maps []FieldMap, vals map[string]string, set map[string]any) map[string]Column {
	cut := map[string]Column{}
	for _, m := range maps {
		if (m.Direction != DirOut && m.Direction != DirBoth) || m.FixedValue != nil || m.ForeignColumn == "" {
			continue
		}
		if _, ok := vals[m.PDHField]; !ok {
			continue
		}
		s, ok := set[m.ForeignColumn].(string)
		c, found := findColumn(t, m.ForeignColumn)
		if !ok || !found {
			continue
		}
		if short := d.TruncateText(*c, s); short != s {
			set[m.ForeignColumn] = short
			cut[m.PDHField] = *c
		}
	}
	return cut
}

// noteTruncated vermerkt eine Kürzung am Datensatz – einmal je Verknüpfung,
// Feld und gekürztem Inhalt: nur wenn die gekürzte Form von der des zuvor
// geschriebenen Werts (written) abweicht.
func (o *Outbound) noteTruncated(ctx context.Context, link *Link, p *Profile, fields []PDHField, d Dialect, vals map[string]string, cut map[string]Column) error {
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
		c := cut[k]
		short := truncatedForm(d, c, vals[k])
		if prev, ok := link.WrittenValues[k]; ok && truncatedForm(d, c, prev) == short {
			continue
		}
		msg := fmt.Sprintf("Wert für ‚%s‘ wurde beim Schreiben ins Fremdsystem auf %d Zeichen gekürzt", idx[k].Label, c.MaxLen)
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
// Ist der Aufrufer schon abgebrochen, wird gar nicht erst gemerkt. Das
// Einfügen selbst läuft ohne den Abbruch des Aufrufers (eigene Zeitgrenze), ein
// Halt trifft es also nicht mitten im Senden. Findet ein späterer Versuch die
// Marke, ist der Ausgang offen: der Eintrag wird mit Hinweis erledigt – lieber
// ein Kommentar sichtbar verloren als doppelt im Fremdsystem. Nur wenn sicher
// nichts ausgeführt wurde (Server lehnt ab, Verbindung vor dem Senden
// unbrauchbar), wird die Marke gelöscht und später erneut versucht.
func (o *Outbound) comment(ctx context.Context, item OutboxItem, link *Link, p *Profile, f Foreign, d Dialect, kv []string, allowWrite bool) (OutResult, error) {
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
	name := ""
	if ct.AuthorColumn != "" && o.UserName != nil && pl.UserID != nil {
		name = strings.TrimSpace(o.UserName(ctx, *pl.UserID))
	}
	at := o.now()
	if pl.CreatedAt != nil {
		if tm, err := time.Parse(time.RFC3339Nano, *pl.CreatedAt); err == nil {
			at = tm
		}
	}
	st, err := d.InsertChild(src, child, commentValues(d, child, ct, kv[0], *pl.Text, name, at, fLoc))
	if err != nil {
		return OutResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return OutResult{}, err // abgebrochen: nichts gemerkt, nichts gesendet
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
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commentInsertTimeout)
	n, err := f.Exec(ectx, st)
	cancel()
	if err == nil && n == 0 {
		err = errors.New("Kommentar wurde nicht eingefügt")
	}
	if err != nil {
		if !uncertainExec(err) {
			// Sicher nicht ausgeführt: Marke löschen, später erneut versuchen.
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

// commentValues: die Werte einer Kommentarzeile, wie der Ausgang sie einfügt –
// Fremdschlüssel fk, Text und Verfasser (leer = "PDH") nach dem Maß des
// Dialekts gekürzt, Zeit at nach der Zeitzone des Profils (foreignTime).
// Verfasser- und Zeitspalte nur, wenn die Kommentartabelle sie nennt. Der
// Befehl entsteht allein über Dialect.InsertChild (Ausgang und Schreibversuch).
func commentValues(d Dialect, child *TableInfo, ct *CommentTable, fk, text, author string, at time.Time, fLoc *time.Location) map[string]any {
	values := map[string]any{
		ct.FKColumn:   fk,
		ct.TextColumn: truncateFor(d, child, ct.TextColumn, text),
	}
	if ct.AuthorColumn != "" {
		if author == "" {
			author = "PDH"
		}
		values[ct.AuthorColumn] = truncateFor(d, child, ct.AuthorColumn, author)
	}
	if ct.TimeColumn != "" {
		values[ct.TimeColumn] = foreignTime(child, ct.TimeColumn, d, at, fLoc)
	}
	return values
}

func (o *Outbound) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// truncateFor kürzt s nach dem Maß des Dialekts auf die Spalte col von t.
func truncateFor(d Dialect, t *TableInfo, col, s string) string {
	if c, ok := findColumn(t, col); ok {
		return d.TruncateText(*c, s)
	}
	return s
}

// uncertainExec: Ist nach diesem Fehler offen, ob der Befehl ausgeführt wurde?
// driver.ErrBadConn liefert database/sql nur, wenn der Befehl nicht gesendet
// werden konnte: sicher nicht ausgeführt. Zeitgrenze, Abbruch und eine
// abgerissene Verbindung (EOF, Netzfehler): offen. Sonst hat der Server den
// Befehl abgelehnt (sicher nicht ausgeführt).
func uncertainExec(err error) bool {
	if errors.Is(err, driver.ErrBadConn) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
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
