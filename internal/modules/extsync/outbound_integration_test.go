package extsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/tickets"
)

// ── Aufbau ───────────────────────────────────────────────────────────────

// outFix ist ein inFix mit Hinausschreiben. Alle Befehle an die Attrappe gehen
// über recDialect; am Ende jedes Tests wird geprüft, dass Exec ausschließlich
// Befehle von UpdateRow/InsertChild bekam und nie in die Meldungstabelle einfügte.
type outFix struct {
	*inFix
	rd  *recDialect
	out *Outbound
}

func newOutFix(t *testing.T, o fixOpt) *outFix {
	t.Helper()
	f := newInFix(t, o)
	rd := &recDialect{Dialect: fakeDialect{}}
	f.d = rd
	of := &outFix{inFix: f, rd: rd, out: &Outbound{Store: f.s, Targets: f.in.Targets, SystemUser: systemUser,
		Location: time.UTC, UserName: func(context.Context, string) string { return "Erika Muster" }}}
	t.Cleanup(func() { assertOnlyDialectWrites(t, of.ff, of.rd) })
	return of
}

// assertOnlyDialectWrites: jeder Exec-Aufruf war ein Befehl von UpdateRow oder
// InsertChild; ein Einfügen traf nie die Meldungstabelle.
func assertOnlyDialectWrites(t *testing.T, ff *fakeForeign, rd *recDialect) {
	t.Helper()
	for i, st := range ff.execStmts() {
		if !rd.wasProduced(st) {
			t.Errorf("Exec %d: Befehl %q stammt nicht von UpdateRow/InsertChild: %+v", i, st.SQL, st.Args)
		}
		if len(st.Args) != 1 {
			t.Errorf("Exec %d: Befehl %q ohne Befehlsdaten der Attrappe", i, st.SQL)
			continue
		}
		if in, ok := st.Args[0].(fakeInsert); ok && in.table == fakeTableName(ff.info) {
			t.Errorf("Exec %d: Einfügen in die Meldungstabelle", i)
		}
	}
}

// openItems liefert die offenen Wartelisteneinträge des Profils nach ID.
func (f *outFix) openItems() []OutboxItem {
	f.t.Helper()
	rows, err := f.sf.pool.Query(context.Background(), `SELECT o.id, o.link_id::text, o.kind, o.payload, o.attempts, o.last_error
		FROM sync_outbox o JOIN sync_links l ON l.id = o.link_id
		WHERE l.profile_id = $1 AND o.done_at IS NULL ORDER BY o.id`, f.p.ID)
	must(f.t, err)
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		must(f.t, rows.Scan(&it.ID, &it.LinkID, &it.Kind, &it.Payload, &it.Attempts, &it.LastError))
		out = append(out, it)
	}
	return out
}

func (f *outFix) linkByID(id string) *Link {
	f.t.Helper()
	l, err := f.s.LinkByID(context.Background(), id)
	must(f.t, err)
	return l
}

// nextMine: Store.NextOutbox, beschränkt auf die Verknüpfungen dieses Profils
// (das Labor ist geteilt; fremde Einträge werden nur gelesen, nie bearbeitet).
func (f *outFix) nextMine() []OutboxItem {
	f.t.Helper()
	all, err := f.s.NextOutbox(context.Background(), 10000)
	must(f.t, err)
	var out []OutboxItem
	for _, it := range all {
		if f.linkByID(it.LinkID).ProfileID == f.p.ID {
			out = append(out, it)
		}
	}
	return out
}

// histOut liefert die Verlaufseinträge des Hinausschreibens am Datensatz.
func (f *outFix) histOut(id string) []histRow { return f.history(id, actionSyncOut) }

func (f *outFix) itemByID(id int64) (attempts int, lastError string, done bool) {
	f.t.Helper()
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT attempts, last_error, done_at IS NOT NULL FROM sync_outbox WHERE id = $1`,
		id).Scan(&attempts, &lastError, &done))
	return
}

// process bearbeitet einen Eintrag wie der spätere Taktgeber: erledigt oder Fehlversuch.
func (f *outFix) process(it OutboxItem, allowWrite bool) (OutResult, error) {
	f.t.Helper()
	res, err := f.out.Process(context.Background(), it, f.linkByID(it.LinkID), f.p, f.maps, f.ff, f.d, allowWrite)
	ctx := context.Background()
	switch {
	case err != nil:
		must(f.t, f.s.OutboxFailed(ctx, it.ID, err.Error(), Backoff(it.Attempts+1)))
	case res.Done:
		must(f.t, f.s.OutboxDoneNote(ctx, it.ID, res.Note))
	}
	return res, err
}

// processAll bearbeitet alle offenen Einträge der Reihe nach (je Verknüpfung
// höchstens bis zum ersten Fehler).
func (f *outFix) processAll(allowWrite bool) {
	f.t.Helper()
	blocked := map[string]bool{}
	for _, it := range f.openItems() {
		if blocked[it.LinkID] {
			continue
		}
		if _, err := f.process(it, allowWrite); err != nil {
			blocked[it.LinkID] = true
		}
	}
}

// userUpdate ändert den Datensatz wie ein Benutzer: der Auslöser stellt einen Eintrag ein.
func (f *outFix) userUpdate(id, set string, args ...any) {
	f.t.Helper()
	tbl := map[string]string{RefTicket: "tickets", RefFault: "faults"}[f.target]
	_, err := f.sf.pool.Exec(context.Background(), `UPDATE `+tbl+` SET `+set+` WHERE id = $1`, append([]any{id}, args...)...)
	must(f.t, err)
}

func (f *outFix) enqueue(linkID, kind string, payload any) {
	f.t.Helper()
	must(f.t, f.s.Enqueue(context.Background(), linkID, kind, payload))
}

// updates liefert die ausgeführten Änderungsbefehle.
func (f *outFix) updates() []fakeUpdate {
	var out []fakeUpdate
	for _, st := range f.ff.execStmts() {
		if len(st.Args) != 1 {
			continue
		}
		if u, ok := st.Args[0].(fakeUpdate); ok {
			out = append(out, u)
		}
	}
	return out
}

// synced legt Zeile nr an, übernimmt sie und liefert die ID des Datensatzes.
func (f *outFix) synced(nr int64, titel, prio, zustand string) string {
	f.t.Helper()
	f.row(nr, titel, prio, zustand)
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(int(nr)))
	return id
}

// ── Wartezeit ────────────────────────────────────────────────────────────

func TestBackoffCap(t *testing.T) {
	want := map[int]time.Duration{0: 5 * time.Second, 1: 5 * time.Second, 2: 15 * time.Second, 3: time.Minute,
		4: 5 * time.Minute, 5: 15 * time.Minute, 6: 15 * time.Minute, 50: 15 * time.Minute, 1 << 30: 15 * time.Minute}
	for n, w := range want {
		if got := Backoff(n); got != w {
			t.Errorf("Backoff(%d) = %v, erwartet %v", n, got, w)
		}
	}
}

// ── Ändern ───────────────────────────────────────────────────────────────

func TestOutboundUpdateUsesCurrentValue(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu1"))
	f.userUpdate(id, "title = $2", f.title("Neu2"))
	items := f.openItems()
	if len(items) != 2 {
		t.Fatalf("%d Einträge, erwartet 2", len(items))
	}
	res, err := f.process(items[0], true)
	must(t, err)
	if !res.Done || res.Written != 1 {
		t.Fatalf("Ergebnis %+v", res)
	}
	if got := f.ff.get(1)["Titel"]; got != f.title("Neu2") {
		t.Fatalf("Fremdtitel %v, erwartet den aktuellen PDH-Wert", got)
	}
	if w := f.link(k(1)).WrittenValues["title"]; w != f.title("Neu2") {
		t.Fatalf("written[title] = %q", w)
	}
	// Der zweite Eintrag hat nichts mehr zu tun: schon geschrieben.
	res, err = f.process(items[1], true)
	must(t, err)
	if !res.Done || res.Written != 0 || len(f.ff.execStmts()) != 1 {
		t.Fatalf("zweiter Eintrag: %+v, %d Befehle", res, len(f.ff.execStmts()))
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge", n)
	}
}

// Abbruch nach dem Fremdbefehl, vor dem Speichern der Ausgangswerte: der Eintrag
// bleibt, die Wiederholung schreibt dieselben Werte erneut (UPDATE ist wiederholbar).
func TestOutboundCrashAfterWriteRewrites(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.out.testHook = func(step string) error {
		if step == "written" {
			return errors.New("Absturz")
		}
		return nil
	}
	f.processAll(true)
	f.out.testHook = nil
	it := f.openItems()
	if len(it) != 1 || it[0].Attempts != 1 || f.ff.get(1)["Titel"] != f.title("Neu") {
		t.Fatalf("nach Abbruch: %+v", it)
	}
	if _, ok := f.link(k(1)).WrittenValues["title"]; ok {
		t.Fatal("written trotz Abbruch gesetzt")
	}
	must(t, f.s.OutboxRetryNow(context.Background(), it[0].ID))
	f.processAll(true)
	ups := f.updates()
	if len(ups) != 2 || ups[1].set["Titel"] != f.title("Neu") || len(f.openItems()) != 0 {
		t.Fatalf("Wiederholung: %+v, offen %d", ups, len(f.openItems()))
	}
	if w := f.link(k(1)).WrittenValues["title"]; w != f.title("Neu") {
		t.Fatalf("written[title] = %q", w)
	}
}

func TestOutboundOnlyMappedOutFields(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	// Beschreibung und Priorität sind "herein": nie schreiben.
	f.userUpdate(id, "title = $2, description = 'PDH-Text', priority = 'low'", f.title("Neu"))
	f.processAll(true)
	ups := f.updates()
	if len(ups) != 1 {
		t.Fatalf("%d Änderungsbefehle", len(ups))
	}
	if len(ups[0].set) != 1 || ups[0].set["Titel"] != f.title("Neu") {
		t.Fatalf("SET %+v, erwartet nur Titel", ups[0].set)
	}
	if !sameKeys(ups[0].keyVals, []string{"1"}) || !sameKeys(ups[0].keyCols, []string{"Nr"}) {
		t.Fatalf("Schlüssel %v=%v", ups[0].keyCols, ups[0].keyVals)
	}
	row := f.ff.get(1)
	if row["Beschreibung"] != "Text Alt" || row["Prio"] != "hoch" {
		t.Fatalf("hereinkommende Spalten geändert: %+v", row)
	}
}

func TestOutboundSkipsUnchangedAgainstSeen(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Zwischen"))
	f.userUpdate(id, "title = $2", f.title("Alt")) // zurück: Fremdsystem hat den Wert schon
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle, erwartet keiner", n)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge", n)
	}
	if _, ok := f.link(k(1)).WrittenValues["title"]; ok {
		t.Fatal("written[title] gesetzt, obwohl nichts geschrieben wurde")
	}
}

func TestOutboundWriteNotAllowed(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	it := f.openItems()[0]
	res, err := f.process(it, false)
	if !errors.Is(err, ErrWriteNotAllowed) || res.Done {
		t.Fatalf("Ergebnis %+v, Fehler %v", res, err)
	}
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle trotz Schreibverbot", n)
	}
	items := f.openItems()
	if len(items) != 1 || items[0].Attempts != 1 || items[0].LastError != ErrWriteNotAllowed.Error() {
		t.Fatalf("Eintrag %+v", items)
	}
	if f.ff.get(1)["Titel"] != f.title("Alt") {
		t.Fatal("Fremdzeile geändert")
	}
}

func TestOutboundDeletedPDHRecord(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	_, err := f.sf.pool.Exec(context.Background(), `DELETE FROM tickets WHERE id = $1`, id)
	must(t, err)
	res, err := f.process(f.openItems()[0], true)
	must(t, err)
	if !res.Done || res.Written != 0 {
		t.Fatalf("Ergebnis %+v", res)
	}
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle", n)
	}
	if l := f.link(k(1)); l.LastError != "" {
		t.Fatalf("last_error %q", l.LastError)
	}
}

func TestOutboundUnknownPayloadFieldIgnored(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	l := f.link(k(1))
	f.enqueue(l.ID, KindUpdate, map[string]any{"fields": map[string]any{"bogus": "x", "Titel": "y", "priority_x": "z"}})
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle für unbekannte Felder", n)
	}
	f.pdhUpdate(id, "title = $2", f.title("Neu"))
	f.enqueue(l.ID, KindUpdate, map[string]any{"fields": map[string]any{"bogus": "x", "title": "egal"}})
	f.processAll(true)
	ups := f.updates()
	if len(ups) != 1 || len(ups[0].set) != 1 || ups[0].set["Titel"] != f.title("Neu") {
		t.Fatalf("Befehle %+v", ups)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge", n)
	}
}

func TestOutboundWrittenIsFieldwise(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "", "offen") // Prio leer: Abgleich setzt "medium" (Ausgangswert)
	l := f.link(k(1))
	if l.WrittenValues["priority"] != "medium" {
		t.Fatalf("Ausgangswert fehlt: %+v", l.WrittenValues)
	}
	f.userUpdate(id, "title = $2", f.title("Neu"))
	// Während das Hinausschreiben läuft, setzt ein anderer Schreiber ein anderes Feld.
	f.ff.beforeExec = func(Stmt) {
		must(t, f.s.SaveInbound(context.Background(), l.ID, InboundUpdate{WrittenSet: map[string]string{"description": "gleichzeitig"}}))
	}
	f.processAll(true)
	got := f.link(k(1)).WrittenValues
	want := map[string]string{"priority": "medium", "description": "gleichzeitig", "title": f.title("Neu")}
	if len(got) != len(want) {
		t.Fatalf("written %+v, erwartet %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("written %+v, erwartet %+v", got, want)
		}
	}
}

func TestOutboundSkipsSyncSetBaseline(t *testing.T) {
	maps := stdMaps(RefTicket)
	maps[2].Direction = DirBoth // Prio beides
	f := newOutFix(t, fixOpt{maps: maps})
	id := f.synced(1, "Alt", "", "offen")
	l := f.link(k(1))
	if l.WrittenValues["priority"] != "medium" {
		t.Fatalf("Ausgangswert fehlt: %+v", l.WrittenValues)
	}
	// Ein Eintrag mit der vom Abgleich gesetzten Vorgabe geht nie ins Fremdsystem.
	f.enqueue(l.ID, KindUpdate, map[string]any{"fields": map[string]any{"priority": "medium"}})
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle: Vorgabe ins Fremdsystem geschrieben", n)
	}
	if got := f.ff.get(1)["Prio"]; got != "" {
		t.Fatalf("Prio %v", got)
	}
	// Gegenprobe: eine Benutzeränderung geht hinaus (übersetzt).
	f.userUpdate(id, "priority = 'high'")
	f.processAll(true)
	if got := f.ff.get(1)["Prio"]; got != "hoch" {
		t.Fatalf("Prio %v, erwartet hoch", got)
	}
}

// Der Auslöser stellt beim Abschließen über QuickResolve (hereinkommend) einen
// Eintrag ein; er darf nichts ins Fremdsystem schreiben.
func TestOutboundDropsInboundQuickResolveEntry(t *testing.T) {
	info := fakeMeldungenInfo(true)
	info.Columns = append(info.Columns, Column{Name: "Loesung", Type: "nvarchar", Nullable: true})
	maps := stdMaps(RefTicket)
	maps[3].Direction = DirBoth // Zustand beides
	maps = append(maps, FieldMap{ForeignColumn: "Loesung", PDHField: "resolution", Direction: DirBoth, Position: 4})
	f := newOutFix(t, fixOpt{info: info, maps: maps})
	f.synced(1, "Alt", "hoch", "offen")
	f.ff.set(1, "Zustand", "erledigt")
	f.run(RunOptions{})
	_, _, _, _, st := f.rec(k(1))
	if st != "resolved" {
		t.Fatalf("Status %q", st)
	}
	items := f.openItems()
	if len(items) == 0 {
		t.Fatal("kein Eintrag von QuickResolve – der Test prüft nichts")
	}
	var payload struct{ Fields map[string]any }
	must(t, json.Unmarshal(items[0].Payload, &payload))
	if _, ok := payload.Fields["status"]; !ok {
		t.Fatalf("Eintrag ohne Status: %s", items[0].Payload)
	}
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle: %+v", n, f.updates())
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge", n)
	}
}

// Nach dem Hinausschreiben liest der hereinkommende Lauf den eigenen Wert als Echo.
func TestOutboundThenInboundIsEcho(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.processAll(true)
	if f.ff.get(1)["Titel"] != f.title("Neu") {
		t.Fatal("nicht geschrieben")
	}
	before := f.outbox()
	r := f.run(RunOptions{})
	if r.Changed != 0 || r.Failed != 0 {
		t.Fatalf("Lauf %+v", r)
	}
	if h := f.history(id, "sync"); len(h) != 0 {
		t.Fatalf("Verlauf: %+v", h)
	}
	if n := f.outbox(); n != before {
		t.Fatalf("Warteliste %d -> %d", before, n)
	}
	_, title, _, _, _ := f.rec(k(1))
	if title != f.title("Neu") {
		t.Fatalf("PDH-Titel %q", title)
	}
	l := f.link(k(1))
	if _, ok := l.WrittenValues["title"]; ok || l.SeenValues["title"] != f.title("Neu") {
		t.Fatalf("Verknüpfung nach Echo: %+v", l)
	}
}

// ── Fremdzeile verschwunden, Reihenfolge, unlesbare Einträge ─────────────

func TestOutboundRowGone(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.ff.del(1)
	res, err := f.process(f.openItems()[0], true)
	must(t, err)
	if !res.Done || res.Written != 0 || res.Note != ErrRowGone.Error() {
		t.Fatalf("Ergebnis %+v", res)
	}
	stmts := f.ff.execStmts()
	if len(stmts) != 1 || stmts[0].SQL != "update" {
		t.Fatalf("Befehle %+v", stmts)
	}
	if f.ff.get(1) != nil || len(f.ff.rows) != 0 {
		t.Fatal("Zeile wurde angelegt")
	}
	l := f.link(k(1))
	if l.RefID == nil || *l.RefID != id || l.LastError != LinkErrForeignGone {
		t.Fatalf("Verknüpfung %+v", l)
	}
	if h := f.histOut(id); len(h) != 1 || h[0].msg != noteOutRowGone {
		t.Fatalf("Verlauf %+v", h)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge", n)
	}
}

func TestOutboundRowGoneLaterItemsSkip(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu1"))
	f.userUpdate(id, "title = $2", f.title("Neu2"))
	l := f.link(k(1))
	f.enqueue(l.ID, KindComment, map[string]any{"text": "Hallo", "user_id": nil, "created_at": time.Now()})
	f.ff.del(1)
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 1 {
		t.Fatalf("%d Befehle, erwartet genau einen (der die Lücke entdeckt)", n)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge", n)
	}
	if h := f.histOut(id); len(h) != 1 {
		t.Fatalf("Verlauf %+v, erwartet genau einen Eintrag", h)
	}
	// Taucht die Zeile wieder auf, löst der hereinkommende Lauf die Marke und es geht weiter.
	f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Wieder"), "Beschreibung": "Text Alt", "Prio": "hoch", "Zustand": "offen"})
	f.run(RunOptions{})
	if l := f.link(k(1)); l.LastError != "" {
		t.Fatalf("Marke bleibt: %q", l.LastError)
	}
	f.userUpdate(id, "title = $2", f.title("Danach"))
	f.processAll(true)
	if got := f.ff.get(1)["Titel"]; got != f.title("Danach") {
		t.Fatalf("Titel %v", got)
	}
}

func TestOutboundFailureBlocksLaterOfSameLink(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id1 := f.synced(1, "Eins", "hoch", "offen")
	id2 := f.synced(2, "Zwei", "hoch", "offen")
	f.sf.arm(t, f.p.ID)
	f.userUpdate(id1, "title = $2", f.title("Eins-a"))
	f.userUpdate(id1, "title = $2", f.title("Eins-b"))
	f.userUpdate(id2, "title = $2", f.title("Zwei-a"))
	all := f.openItems()
	if len(all) != 3 {
		t.Fatalf("%d Einträge", len(all))
	}
	a1, a2, b1 := all[0], all[1], all[2]
	f.ff.failExec = func(st Stmt) error {
		if u, ok := st.Args[0].(fakeUpdate); ok && u.keyVals[0] == "1" {
			return errors.New("Sperre im Fremdsystem")
		}
		return nil
	}
	next := f.nextMine()
	if len(next) != 2 || next[0].ID != a1.ID || next[1].ID != b1.ID {
		t.Fatalf("NextOutbox %+v", next)
	}
	for _, it := range next {
		_, _ = f.process(it, true)
	}
	if att, msg, done := f.itemByID(a1.ID); att != 1 || done || msg == "" {
		t.Fatalf("a1: %d %q %v", att, msg, done)
	}
	if att, _, done := f.itemByID(a2.ID); att != 0 || done {
		t.Fatalf("a2 angefasst: %d %v", att, done)
	}
	if _, _, done := f.itemByID(b1.ID); !done {
		t.Fatal("b1 nicht erledigt: andere Verknüpfung blockiert")
	}
	if next := f.nextMine(); len(next) != 0 {
		t.Fatalf("vor Ablauf der Pause fällig: %+v", next)
	}
	// a2 überholt a1 nie; nach der Pause kommt a1 wieder und schreibt den aktuellen Wert.
	f.ff.failExec = nil
	must(t, f.s.OutboxRetryNow(context.Background(), a1.ID))
	next = f.nextMine()
	if len(next) != 1 || next[0].ID != a1.ID {
		t.Fatalf("NextOutbox %+v", next)
	}
	_, err := f.process(next[0], true)
	must(t, err)
	next = f.nextMine()
	if len(next) != 1 || next[0].ID != a2.ID {
		t.Fatalf("NextOutbox %+v", next)
	}
	_, err = f.process(next[0], true)
	must(t, err)
	if f.ff.get(1)["Titel"] != f.title("Eins-b") || f.ff.get(2)["Titel"] != f.title("Zwei-a") {
		t.Fatalf("Fremdzeilen %+v / %+v", f.ff.get(1), f.ff.get(2))
	}
}

func TestOutboundMalformedPayloadIsDoneWithNote(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	l := f.link(k(1))
	f.enqueue(l.ID, KindUpdate, map[string]any{"fields": "kein Objekt"})
	f.enqueue(l.ID, KindUpdate, map[string]any{"felder": map[string]any{"title": "x"}})
	f.enqueue(l.ID, KindComment, []any{"kein", "Objekt"})
	f.userUpdate(id, "title = $2", f.title("Neu"))
	items := f.openItems()
	f.processAll(true)
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offene Einträge: unlesbare Einträge halten die Verknüpfung auf", n)
	}
	for _, it := range items[:3] {
		if _, msg, done := f.itemByID(it.ID); !done || !startsWith(msg, noteOutBadItem) {
			t.Fatalf("Eintrag %d: %q %v", it.ID, msg, done)
		}
	}
	if h := f.histOut(id); len(h) != 3 || !startsWith(h[0].msg, noteOutBadItem) {
		t.Fatalf("Verlauf %+v", h)
	}
	if got := f.ff.get(1)["Titel"]; got != f.title("Neu") {
		t.Fatalf("Titel %v: späterer Eintrag nicht geschrieben", got)
	}
	// Eine gefälschte Art schreibt nichts und hält nichts auf.
	res, err := f.out.Process(context.Background(), OutboxItem{ID: -1, LinkID: l.ID, Kind: "delete",
		Payload: json.RawMessage(`{"fields":{"title":"x"}}`)}, f.link(k(1)), f.p, f.maps, f.ff, f.d, true)
	if err != nil || !res.Done || len(f.ff.execStmts()) != 1 {
		t.Fatalf("gefälschte Art: %+v %v, %d Befehle", res, err, len(f.ff.execStmts()))
	}
}

// ── Kommentare ───────────────────────────────────────────────────────────

// kommentareInfo (dialect_mssql_test.go) ist der Aufbau der Nebentabelle wie im Labor.

var stdCommentTable = &CommentTable{Schema: "dbo", Table: "MeldungKommentare", FKColumn: "MeldungNr",
	TextColumn: "Text", AuthorColumn: "Verfasser", TimeColumn: "Zeit"}

// withComments richtet die Kommentartabelle am Profil und in der Attrappe ein.
func (f *outFix) withComments(ct *CommentTable, info *TableInfo) {
	f.t.Helper()
	f.p.CommentTable = ct
	cur := f.p.Cursor
	must(f.t, f.s.SaveProfile(context.Background(), f.p, systemUser))
	f.reload()
	f.p.Cursor = cur
	f.ff.addChild(info)
}

// testUser legt einen Benutzer an (Kommentare des Systembenutzers gehen nicht hinaus).
func (f *outFix) testUser() string {
	f.t.Helper()
	var id string
	must(f.t, f.sf.pool.QueryRow(context.Background(), `INSERT INTO users (username, email, password_hash, first_name, last_name)
		VALUES ('extsync-out-' || $1, 'extsync-out-' || $1 || '@example.invalid', 'x', 'Erika', 'Muster') RETURNING id`,
		f.tag).Scan(&id))
	f.t.Cleanup(func() {
		c := context.Background()
		_, _ = f.sf.pool.Exec(c, `DELETE FROM ticket_comments WHERE user_id = $1`, id)
		_, _ = f.sf.pool.Exec(c, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func (f *outFix) addComment(ticketID, userID, text string, at time.Time) {
	f.t.Helper()
	_, err := f.sf.pool.Exec(context.Background(), `INSERT INTO ticket_comments (ticket_id, user_id, text, created_at)
		VALUES ($1, $2, $3, $4)`, ticketID, userID, text, at)
	must(f.t, err)
}

func TestOutboundComment(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	user := f.testUser()
	var asked string
	f.out.UserName = func(_ context.Context, uid string) string { asked = uid; return "Erika Muster" }
	at := time.Date(2026, 11, 13, 9, 30, 15, 0, time.UTC)

	// Ohne Kommentartabelle: erledigt, nichts geschrieben.
	f.addComment(id, user, "ohne Tabelle", at)
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 0 || len(f.openItems()) != 0 {
		t.Fatalf("ohne Kommentartabelle: %d Befehle, %d offen", n, len(f.openItems()))
	}

	f.withComments(stdCommentTable, kommentareInfo())
	f.addComment(id, user, "Dichtung getauscht", at)
	// Erster Versuch scheitert eindeutig (Fehler des Servers): später erneut, genau einmal eingefügt.
	f.ff.failExec = func(Stmt) error { return errors.New("Einfügen abgelehnt") }
	f.processAll(true)
	f.ff.failExec = nil
	if n := len(f.ff.childRows("dbo", "MeldungKommentare")); n != 0 {
		t.Fatalf("%d Zeilen nach Fehlversuch", n)
	}
	it := f.openItems()
	if len(it) != 1 || it[0].Attempts != 1 {
		t.Fatalf("Eintrag nach Fehlversuch: %+v", it)
	}
	must(t, f.s.OutboxRetryNow(context.Background(), it[0].ID))
	f.processAll(true)
	rows := f.ff.childRows("dbo", "MeldungKommentare")
	if len(rows) != 1 {
		t.Fatalf("%d Kommentarzeilen", len(rows))
	}
	r := rows[0]
	if r["MeldungNr"] != "1" || r["Text"] != "Dichtung getauscht" || r["Verfasser"] != "Erika Muster" || asked != user {
		t.Fatalf("Zeile %+v (gefragt nach %q)", r, asked)
	}
	if tm, ok := r["Zeit"].(time.Time); !ok || !tm.Equal(at) {
		t.Fatalf("Zeit %v", r["Zeit"])
	}
	if _, ok := r["Id"]; ok {
		t.Fatal("Identitätsspalte befüllt")
	}
	// Kommentar des Systembenutzers geht nicht hinaus; Verfasser ohne Namen heißt "PDH".
	f.addComment(id, systemUser, "vom System", at)
	f.out.UserName = func(context.Context, string) string { return "" }
	f.addComment(id, user, "namenlos", at)
	f.processAll(true)
	rows = f.ff.childRows("dbo", "MeldungKommentare")
	if len(rows) != 2 || rows[1]["Text"] != "namenlos" || rows[1]["Verfasser"] != "PDH" {
		t.Fatalf("Zeilen %+v", rows)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offen", n)
	}
	if f.ff.get(1)["Titel"] != f.title("Alt") {
		t.Fatal("Meldungszeile geändert")
	}
}

func TestOutboundCommentCrashWhileSendingIsNotDuplicated(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.withComments(stdCommentTable, kommentareInfo())
	user := f.testUser()

	// Abbruch NACH dem Einfügen, vor dem Erledigen.
	f.addComment(id, user, "eins", time.Now())
	f.out.testHook = func(step string) error {
		if step == "sent" {
			return errors.New("Absturz")
		}
		return nil
	}
	f.processAll(true)
	f.out.testHook = nil
	it := f.openItems()
	if len(it) != 1 || len(f.ff.childRows("dbo", "MeldungKommentare")) != 1 {
		t.Fatalf("nach Abbruch: %+v", it)
	}
	must(t, f.s.OutboxRetryNow(context.Background(), it[0].ID))
	res, err := f.process(f.openItems()[0], true)
	must(t, err)
	if !res.Done || res.Note != noteOutMaybeLost {
		t.Fatalf("Wiederholung %+v", res)
	}
	if n := len(f.ff.childRows("dbo", "MeldungKommentare")); n != 1 {
		t.Fatalf("%d Zeilen: Kommentar doppelt", n)
	}

	// Abbruch zwischen Merken und Einfügen (Prozess beendet): kein blindes Einfügen.
	f.addComment(id, user, "zwei", time.Now())
	it = f.openItems()
	already, err := f.s.OutboxBeginSend(context.Background(), it[0].ID)
	must(t, err)
	if already {
		t.Fatal("frischer Eintrag gilt als gesendet")
	}
	res, err = f.process(it[0], true)
	must(t, err)
	if !res.Done || res.Note != noteOutMaybeLost || len(f.ff.childRows("dbo", "MeldungKommentare")) != 1 {
		t.Fatalf("Ergebnis %+v", res)
	}
	h := f.histOut(id)
	if len(h) != 2 || h[0].msg != noteOutMaybeLost {
		t.Fatalf("Verlauf %+v", h)
	}
	if _, msg, done := f.itemByID(it[0].ID); !done || msg != noteOutMaybeLost {
		t.Fatalf("Spur am Eintrag: %q %v", msg, done)
	}

	// Zeitgrenze beim Einfügen: offen, ob der Server eingefügt hat – kein zweiter Versuch.
	f.addComment(id, user, "drei", time.Now())
	f.ff.failExec = func(Stmt) error { return fmt.Errorf("lesen: %w", context.DeadlineExceeded) }
	f.processAll(true)
	f.ff.failExec = nil
	it = f.openItems()
	if len(it) != 1 {
		t.Fatalf("offen %+v", it)
	}
	must(t, f.s.OutboxRetryNow(context.Background(), it[0].ID))
	execs := len(f.ff.execStmts())
	res, err = f.process(f.openItems()[0], true)
	must(t, err)
	if !res.Done || res.Note != noteOutMaybeLost || len(f.ff.execStmts()) != execs {
		t.Fatalf("nach Zeitgrenze: %+v, %d -> %d Befehle", res, execs, len(f.ff.execStmts()))
	}
}

func TestOutboundCommentCompositeKeySkipped(t *testing.T) {
	info := &TableInfo{Schema: "dbo", Name: "MeldungenGuid", Columns: []Column{
		{Name: "Id", Type: "uniqueidentifier", IsKey: true},
		{Name: "Werk", Type: "nvarchar", IsKey: true},
		{Name: "Titel", Type: "nvarchar"},
		{Name: "RV", Type: "timestamp", IsRowVersion: true},
	}}
	f := newOutFix(t, fixOpt{info: info, keyCols: []string{"Id", "Werk"},
		maps: []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirBoth}}})
	f.ff.put(map[string]any{"Id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "Werk": "W1", "Titel": f.title("Sensor")})
	f.run(RunOptions{})
	key := EncodeKey([]string{"3f2504e0-4f89-11d3-9a0c-0305e82c3301", "W1"})
	id, _, _, _, _ := f.rec(key)
	f.withComments(stdCommentTable, kommentareInfo())
	user := f.testUser()
	f.addComment(id, user, "nicht übertragbar", time.Now())
	f.processAll(true)
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle", n)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offen", n)
	}
	if h := f.histOut(id); len(h) != 1 || h[0].msg != noteOutCompositeKey {
		t.Fatalf("Verlauf %+v", h)
	}
	// Ändern geht bei zusammengesetztem Schlüssel weiter (beide Schlüsselwerte).
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.processAll(true)
	ups := f.updates()
	if len(ups) != 1 || !sameKeys(ups[0].keyVals, []string{"3f2504e0-4f89-11d3-9a0c-0305e82c3301", "W1"}) {
		t.Fatalf("Befehle %+v", ups)
	}
}

func TestOutboundCommentTruncatesToMaxLen(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	info := kommentareInfo()
	info.Columns[2].MaxLen = 10 // Text
	info.Columns[3].MaxLen = 5  // Verfasser
	f.withComments(stdCommentTable, info)
	user := f.testUser()
	f.out.UserName = func(context.Context, string) string { return "Ärger Öztürk" }
	f.addComment(id, user, "Größenänderung über zehn Zeichen", time.Now())
	f.processAll(true)
	rows := f.ff.childRows("dbo", "MeldungKommentare")
	if len(rows) != 1 || rows[0]["Text"] != "Größenände" || rows[0]["Verfasser"] != "Ärger" {
		t.Fatalf("Zeilen %+v", rows)
	}
}

// ── Zeitzone ─────────────────────────────────────────────────────────────

func TestOutboundTimeZoneWall(t *testing.T) {
	due := time.Date(2026, 11, 19, 23, 0, 0, 0, time.UTC) // 20.11.2026 00:00 in Berlin
	cases := []struct {
		col, typ string
		want     any
	}{
		{"Faellig", "datetime2", time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}, // Wanduhr Berlin
		{"FaelligTag", "date", time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)},   // Kalendertag in Berlin
		{"FaelligOff", "datetimeoffset", "2026-11-19T23:00:00Z"},                // Zeitpunkt, unverändert
		{"FaelligText", "nvarchar", "2026-11-19T23:00:00Z"},                     // kanonischer Text
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			info := fakeMeldungenInfo(true)
			info.Columns = append(info.Columns, Column{Name: c.col, Type: c.typ, Nullable: true})
			maps := append(stdMaps(RefTicket), FieldMap{ForeignColumn: c.col, PDHField: "due_date", Direction: DirOut, Position: 9})
			f := newOutFix(t, fixOpt{info: info, maps: maps, timeZone: "Europe/Berlin"})
			id := f.synced(1, "Alt", "hoch", "offen")
			f.userUpdate(id, "due_date = $2", due)
			f.processAll(true)
			ups := f.updates()
			if len(ups) != 1 {
				t.Fatalf("Befehle %+v", ups)
			}
			got := ups[0].set[c.col]
			if gt, ok := got.(time.Time); ok {
				if wt, ok := c.want.(time.Time); !ok || !gt.Equal(wt) || gt.Location() != time.UTC {
					t.Fatalf("%s = %v, erwartet %v", c.col, got, c.want)
				}
			} else if got != c.want {
				t.Fatalf("%s = %#v, erwartet %#v", c.col, got, c.want)
			}
			if w := f.link(k(1)).WrittenValues["due_date"]; w != "2026-11-19T23:00:00Z" {
				t.Fatalf("written[due_date] = %q (PDH-Form erwartet)", w)
			}
		})
	}
	t.Run("Kommentarzeit", func(t *testing.T) {
		f := newOutFix(t, fixOpt{timeZone: "Europe/Berlin"})
		id := f.synced(1, "Alt", "hoch", "offen")
		f.withComments(stdCommentTable, kommentareInfo())
		user := f.testUser()
		f.addComment(id, user, "Sommer", time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
		f.processAll(true)
		rows := f.ff.childRows("dbo", "MeldungKommentare")
		if len(rows) != 1 || !rows[0]["Zeit"].(time.Time).Equal(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("Zeilen %+v", rows)
		}
	})
	t.Run("unbekannte Zone", func(t *testing.T) {
		f := newOutFix(t, fixOpt{timeZone: "Mars/Olympus"})
		f.p.TimeZone = "UTC" // anlegen mit gültiger Zone, dann falsch einstellen
		id := f.synced(1, "Alt", "hoch", "offen")
		f.p.TimeZone = "Mars/Olympus"
		f.userUpdate(id, "title = $2", f.title("Neu"))
		res, err := f.process(f.openItems()[0], true)
		if err == nil || res.Done || len(f.ff.execStmts()) != 0 {
			t.Fatalf("Ergebnis %+v %v, %d Befehle", res, err, len(f.ff.execStmts()))
		}
	})
}

// ── Garantie: nur UpdateRow und InsertChild erreichen Exec ───────────────

// recForeign nimmt Befehle des echten T-SQL-Dialekts entgegen, ohne Datenbank:
// Aufbau wie im Labor, jede Meldung "gibt es", jeder Befehl trifft eine Zeile.
type recForeign struct {
	src, child *TableInfo
	log        []Stmt
}

func (r *recForeign) Describe(_ context.Context, schema, table string) (*TableInfo, error) {
	for _, t := range []*TableInfo{r.src, r.child} {
		if strings.EqualFold(t.Schema, schema) && strings.EqualFold(t.Name, table) {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

func (r *recForeign) Query(context.Context, Stmt) ([]map[string]any, error) {
	return []map[string]any{{"Nr": int64(1)}}, nil
}

func (r *recForeign) Exec(_ context.Context, st Stmt) (int64, error) {
	r.log = append(r.log, st)
	return 1, nil
}

var hostileTexts = []string{
	"'; DROP TABLE [dbo].[Meldungen]; --",
	"]; DELETE FROM [dbo].[Meldungen] WHERE 1=1; --",
	"x'); TRUNCATE TABLE dbo.Meldungen; INSERT INTO dbo.Meldungen (Titel) VALUES ('y",
	"MERGE dbo.Meldungen AS t USING (SELECT 1) s ON 1=1 WHEN MATCHED THEN DELETE;",
	"@p1; EXEC xp_cmdshell 'dir' --",
	"Größe \"Zitat\" [Klammer] `Gravis` ; -- /* */",
}

// Feindliche Einträge: Feldnamen außerhalb des Katalogs, SQL in Werten, ein
// hereinkommendes Feld im Eintrag, eine gefälschte Art. Jeder ausgeführte Befehl
// stammt von UpdateRow/InsertChild, eingefügt wird nur in die Nebentabelle, und
// feindlicher Text steht nur in den Parametern – mit Attrappe UND echtem T-SQL-Dialekt.
func TestOutboundOnlyDialectWritesHostile(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.withComments(stdCommentTable, kommentareInfo())
	l := f.link(k(1))
	ms, err := DialectFor("mssql")
	must(t, err)
	mrd := &recDialect{Dialect: ms}
	rf := &recForeign{src: fakeMeldungenInfo(true), child: kommentareInfo()}
	rnd := rand.New(rand.NewSource(20261113))
	pick := func() string { return hostileTexts[rnd.Intn(len(hostileTexts))] }
	for i := 0; i < 40; i++ {
		evil := fmt.Sprintf("%s #%d", pick(), i)
		f.out.UserName = func(context.Context, string) string { return pick() }
		f.pdhUpdate(id, "title = $2, description = $3", evil, pick())
		fields := map[string]any{pick(): pick(), "description": pick(), "title": pick(), "kind": "delete", "Titel]; DROP": 1}
		f.enqueue(l.ID, KindUpdate, map[string]any{"kind": "delete", "fields": fields})
		f.enqueue(l.ID, KindComment, map[string]any{"text": evil, "user_id": pick(), "created_at": pick(), "kind": "delete"})
		for _, it := range f.openItems() {
			forged := it
			forged.Kind = []string{"delete", "merge", "insert", "UPDATE", ""}[i%5]
			for _, x := range []OutboxItem{forged, it} {
				lk := f.linkByID(x.LinkID)
				// Echter Dialekt: jeder Eintrag wird geschrieben (Verknüpfung ohne gesehenen Stand).
				lk2 := *lk
				lk2.SeenValues, lk2.WrittenValues = map[string]string{}, map[string]string{}
				_, _ = f.out.Process(context.Background(), x, &lk2, f.p, f.maps, rf, mrd, true)
				_ = f.s.OutboxClearSend(context.Background(), x.ID)
			}
			_, _ = f.process(it, true) // Attrappe; erledigt den Eintrag
		}
	}
	if len(rf.log) < 40 || len(f.ff.execStmts()) < 40 {
		t.Fatalf("zu wenige Befehle geprüft: %d / %d", len(rf.log), len(f.ff.execStmts()))
	}
	for i, st := range rf.log {
		if !mrd.wasProduced(st) {
			t.Fatalf("T-SQL %d stammt nicht von UpdateRow/InsertChild: %s", i, st.SQL)
		}
		upd := strings.HasPrefix(st.SQL, "UPDATE TOP (1) [dbo].[Meldungen] SET ")
		ins := strings.HasPrefix(st.SQL, "INSERT INTO [dbo].[MeldungKommentare] (")
		if !upd && !ins {
			t.Fatalf("T-SQL %d: %s", i, st.SQL)
		}
		up := strings.ToUpper(st.SQL)
		for _, bad := range []string{"DELETE", "TRUNCATE", "DROP", "MERGE", "EXEC", ";", "--"} {
			if strings.Contains(up, bad) {
				t.Fatalf("T-SQL %d enthält %s: %s", i, bad, st.SQL)
			}
		}
		if strings.Count(up, "INSERT") > 1 || (upd && strings.Contains(up, "INSERT")) {
			t.Fatalf("T-SQL %d: %s", i, st.SQL)
		}
		if upd && strings.Contains(st.SQL, "[Beschreibung]") {
			t.Fatalf("hereinkommendes Feld geschrieben: %s", st.SQL)
		}
		for _, h := range hostileTexts {
			if strings.Contains(st.SQL, h[:8]) {
				t.Fatalf("feindlicher Text im SQL statt als Parameter: %s", st.SQL)
			}
		}
		foundHostile := false
		for _, a := range st.Args {
			if s, ok := a.(string); ok {
				for _, h := range hostileTexts {
					foundHostile = foundHostile || strings.Contains(s, h)
				}
			}
		}
		if !foundHostile {
			t.Fatalf("T-SQL %d ohne den feindlichen Wert in den Parametern: %+v", i, st.Args)
		}
	}
	// Attrappe: newOutFix prüft am Ende jeden Befehl (assertOnlyDialectWrites).
	for _, st := range f.ff.execStmts() {
		if u, ok := st.Args[0].(fakeUpdate); ok {
			for col := range u.set {
				if col != "Titel" {
					t.Fatalf("Spalte %q geschrieben", col)
				}
			}
		}
	}
}

// ── Labor: echter SQL Server ─────────────────────────────────────────────

// txForeign führt Lesen und Schreiben in EINER Transaktion des Labors aus (am
// Ende zurückgerollt): pdh_sync darf nicht löschen, so bleibt das Labor
// unverändert. Den Aufbau liest der Dialekt über db (Katalog, unverändert).
type txForeign struct {
	db *sql.DB
	tx *sql.Tx
	d  Dialect
}

func (f *txForeign) Describe(ctx context.Context, schema, table string) (*TableInfo, error) {
	return f.d.Describe(ctx, f.db, schema, table)
}

func (f *txForeign) Query(ctx context.Context, st Stmt) ([]map[string]any, error) {
	rows, err := f.tx.QueryContext(ctx, st.SQL, st.Args...)
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
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (f *txForeign) Exec(ctx context.Context, st Stmt) (int64, error) {
	res, err := f.tx.ExecContext(ctx, st.SQL, st.Args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func TestOutboundAgainstLab(t *testing.T) {
	db := openLab(t)
	sf := newStoreFixture(t)
	ctx := context.Background()
	ms, err := DialectFor("mssql")
	must(t, err)
	d := &recDialect{Dialect: ms}

	// Stand des Labors vorher (außerhalb jeder Transaktion).
	var nr int64
	var origTitle string
	var nMeld, nKomm int
	must(t, db.QueryRowContext(ctx, `SELECT TOP (1) MeldungNr, Titel FROM dbo.Meldungen ORDER BY MeldungNr`).Scan(&nr, &origTitle))
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.Meldungen`).Scan(&nMeld))
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.MeldungKommentare`).Scan(&nKomm))
	t.Cleanup(func() {
		// Nach dem Zurückrollen: Labor unverändert? Sonst Titel zurücksetzen und melden.
		var title string
		var m, k int
		c := context.Background()
		_ = db.QueryRowContext(c, `SELECT Titel FROM dbo.Meldungen WHERE MeldungNr = @p1`, nr).Scan(&title)
		_ = db.QueryRowContext(c, `SELECT count(*) FROM dbo.Meldungen`).Scan(&m)
		_ = db.QueryRowContext(c, `SELECT count(*) FROM dbo.MeldungKommentare`).Scan(&k)
		if title != origTitle {
			_, _ = db.ExecContext(c, `UPDATE dbo.Meldungen SET Titel = @p1 WHERE MeldungNr = @p2`, origTitle, nr)
			t.Errorf("Labor: Titel war %q, zurückgesetzt auf %q", title, origTitle)
		}
		if m != nMeld || k != nKomm {
			t.Errorf("Labor verändert: Meldungen %d -> %d, Kommentare %d -> %d", nMeld, m, nKomm, k)
		}
		t.Logf("Labor nach dem Test: Titel %q, Meldungen %d, Kommentare %d (vorher %q, %d, %d)", title, m, k, origTitle, nMeld, nKomm)
	})
	tx, err := db.BeginTx(ctx, nil)
	must(t, err)
	t.Cleanup(func() { _ = tx.Rollback() }) // läuft vor der Prüfung oben
	fr := &txForeign{db: db, tx: tx, d: d}

	p := &Profile{Name: fmt.Sprintf("extsync-out-lab-%d", time.Now().UnixNano()), ConnectionID: sf.connID, TargetType: RefTicket,
		SourceSchema: "dbo", SourceTable: "Meldungen", KeyColumns: []string{"MeldungNr"},
		ChangeColumn: "RV", ChangeKind: changeKindRowVersion, CommentTable: stdCommentTable}
	must(t, sf.s.SaveProfile(ctx, p, systemUser))
	t.Cleanup(func() { cleanupProfileRecords(sf, p.ID) })
	must(t, sf.s.ReplaceMaps(ctx, p.ID, []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirBoth, Position: 0},
		{ForeignColumn: "Beschreibung", PDHField: "description", Direction: DirIn, Position: 1},
	}))
	maps, err := sf.s.ListMaps(ctx, p.ID)
	must(t, err)
	p, err = sf.s.GetProfile(ctx, p.ID)
	must(t, err)
	targets := map[string]Target{RefTicket: NewTicketTarget(sf.pool, tickets.NewService(tickets.NewRepository(sf.pool)))}
	in := &Inbound{Store: sf.s, SystemUser: systemUser, Targets: targets}
	r1, _, err := in.Run(ctx, p, maps, fr, d, RunOptions{})
	must(t, err)
	if r1.New != nMeld {
		t.Fatalf("Übernahme %+v", r1)
	}
	p.Cursor = r1.Cursor
	l, err := sf.s.LinkByKey(ctx, p.ID, EncodeKey([]string{fmt.Sprint(nr)}))
	must(t, err)

	of := &outFix{inFix: &inFix{t: t, sf: sf, s: sf.s, p: p, maps: maps, d: d, target: RefTicket,
		tag: fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)}}
	user := of.testUser()
	newTitle := "Labortest Hinaus " + of.tag
	_, err = sf.pool.Exec(ctx, `UPDATE tickets SET title = $2 WHERE id = $1`, *l.RefID, newTitle)
	must(t, err)
	at := time.Date(2026, 11, 13, 8, 15, 0, 0, time.UTC)
	of.addComment(*l.RefID, user, "Kommentar aus PDH "+of.tag, at)

	out := &Outbound{Store: sf.s, Targets: targets, SystemUser: systemUser, Location: time.UTC,
		UserName: func(context.Context, string) string { return "Erika Muster" }}
	p.TimeZone = "UTC"
	var items []OutboxItem
	rows, err := sf.pool.Query(ctx, `SELECT id, link_id::text, kind, payload, attempts, last_error FROM sync_outbox
		WHERE link_id = $1 AND done_at IS NULL ORDER BY id`, l.ID)
	must(t, err)
	for rows.Next() {
		var it OutboxItem
		must(t, rows.Scan(&it.ID, &it.LinkID, &it.Kind, &it.Payload, &it.Attempts, &it.LastError))
		items = append(items, it)
	}
	rows.Close()
	if len(items) != 2 {
		t.Fatalf("%d Einträge", len(items))
	}
	for _, it := range items {
		lk, err := sf.s.LinkByID(ctx, it.LinkID)
		must(t, err)
		res, err := out.Process(ctx, it, lk, p, maps, fr, d, true)
		must(t, err)
		t.Logf("Eintrag %d (%s): %+v", it.ID, it.Kind, res)
		if !res.Done || res.Written != 1 {
			t.Fatalf("Eintrag %s: %+v", it.Kind, res)
		}
		must(t, sf.s.OutboxDoneNote(ctx, it.ID, res.Note))
	}
	// In der Transaktion: geschrieben, angehängt, keine Meldung mehr oder weniger.
	var title, ktext, kverf string
	var kzeit time.Time
	var m, k int
	must(t, tx.QueryRowContext(ctx, `SELECT Titel FROM dbo.Meldungen WHERE MeldungNr = @p1`, nr).Scan(&title))
	must(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM dbo.Meldungen`).Scan(&m))
	must(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM dbo.MeldungKommentare`).Scan(&k))
	must(t, tx.QueryRowContext(ctx, `SELECT TOP (1) Text, Verfasser, Zeit FROM dbo.MeldungKommentare
		WHERE MeldungNr = @p1 ORDER BY Id DESC`, nr).Scan(&ktext, &kverf, &kzeit))
	t.Logf("im Labor (Transaktion): Titel %q, Meldungen %d, Kommentare %d, neuer Kommentar %q von %q um %v", title, m, k, ktext, kverf, kzeit)
	if title != newTitle || m != nMeld || k != nKomm+1 || ktext != "Kommentar aus PDH "+of.tag || kverf != "Erika Muster" ||
		!kzeit.Equal(at) {
		t.Fatalf("Labor: Titel %q, Meldungen %d/%d, Kommentare %d/%d, %q %q %v", title, m, nMeld, k, nKomm, ktext, kverf, kzeit)
	}
	for _, st := range d.produced {
		if !strings.HasPrefix(st.SQL, "UPDATE TOP (1) [dbo].[Meldungen] SET ") && !strings.HasPrefix(st.SQL, "INSERT INTO [dbo].[MeldungKommentare] (") {
			t.Fatalf("Befehl %s", st.SQL)
		}
		t.Logf("Befehl: %s", st.SQL)
	}
	// Echo gegen den echten Server: der nächste Lauf ändert nichts.
	r2, _, err := in.Run(ctx, p, maps, fr, d, RunOptions{})
	must(t, err)
	var cur string
	must(t, sf.pool.QueryRow(ctx, `SELECT title FROM tickets WHERE id = $1`, *l.RefID).Scan(&cur))
	if r2.Changed != 0 || r2.Failed != 0 || cur != newTitle {
		t.Fatalf("Folgelauf %+v, PDH-Titel %q", r2, cur)
	}
	must(t, tx.Rollback())
}

func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

// Hereinkommender Lauf und Hinausschreiben derselben Verknüpfung verschränkt:
// das Hinausschreiben läuft zwischen Entscheidung und Speichern des
// hereinkommenden Laufs (Haken "commit"). Beide Ausgangswerte bleiben richtig,
// der nächste Lauf erkennt das Echo.
func TestOutboundInterleavingWithInbound(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "", "offen") // written[priority] = medium (Vorgabe)
	f.userUpdate(id, "title = $2", f.title("PDH"))
	f.ff.set(1, "Prio", "hoch") // gleichzeitig im Fremdsystem
	ran := false
	f.in.testFail = func(step, fk string) error {
		if step == "commit" && fk == k(1) && !ran {
			ran = true
			f.processAll(true)
		}
		return nil
	}
	r := f.run(RunOptions{})
	f.in.testFail = nil
	if !ran || r.Failed != 0 {
		t.Fatalf("Lauf %+v, Haken lief: %v", r, ran)
	}
	if got := f.ff.get(1)["Titel"]; got != f.title("PDH") {
		t.Fatalf("Fremdtitel %v", got)
	}
	l := f.link(k(1))
	if l.WrittenValues["title"] != f.title("PDH") {
		t.Fatalf("written des Hinausschreibens verloren: %+v", l.WrittenValues)
	}
	if _, ok := l.WrittenValues["priority"]; ok {
		t.Fatalf("Ausgangswert priority nicht aufgeräumt: %+v", l.WrittenValues)
	}
	_, _, _, level, _ := f.rec(k(1))
	if level != "high" {
		t.Fatalf("Priorität %q", level)
	}
	before := f.outbox()
	r = f.run(RunOptions{})
	if r.Changed != 0 || r.Failed != 0 {
		t.Fatalf("Folgelauf %+v", r)
	}
	_, title, _, _, _ := f.rec(k(1))
	if title != f.title("PDH") || f.outbox() != before {
		t.Fatalf("PDH-Titel %q, Warteliste %d -> %d", title, before, f.outbox())
	}
	for _, h := range f.history(id, "sync") {
		if h.field == "title" {
			t.Fatalf("Titel im Verlauf übernommen: %+v", h)
		}
	}
	if l := f.link(k(1)); len(l.WrittenValues) != 0 || l.SeenValues["title"] != f.title("PDH") {
		t.Fatalf("Verknüpfung %+v", l)
	}
}
