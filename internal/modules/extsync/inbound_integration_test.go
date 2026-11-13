package extsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/tickets"
)

// ── Aufbau ───────────────────────────────────────────────────────────────

// inFix ist ein Profil mit Feldzuordnung, Attrappe der Fremdtabelle und echtem
// Ticket- bzw. Störungsdienst auf der Testdatenbank.
type inFix struct {
	t      *testing.T
	sf     *storeFixture
	s      *Store
	p      *Profile
	maps   []FieldMap
	ff     *fakeForeign
	d      Dialect
	in     *Inbound
	tag    string // eindeutiger Zusatz für Titel
	target string
}

var stdTranslatePrio = map[string]string{"hoch": "high", "mittel": "medium", "niedrig": "low"}

func stdMaps(target string) []FieldMap {
	zustand := map[string]string{"offen": "open", "in Arbeit": "in_progress", "erledigt": "resolved", "geschlossen": "closed"}
	level := "priority"
	if target == RefFault {
		zustand = map[string]string{"offen": "detected", "in Arbeit": "in_progress", "erledigt": "resolved"}
		level = "severity"
	}
	return []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirBoth, Position: 0},
		{ForeignColumn: "Beschreibung", PDHField: "description", Direction: DirIn, Position: 1},
		{ForeignColumn: "Prio", PDHField: level, Direction: DirIn, Translate: stdTranslatePrio, Position: 2},
		{ForeignColumn: "Zustand", PDHField: "status", Direction: DirIn, Translate: zustand, Position: 3},
	}
}

type fixOpt struct {
	target    string
	kind      string // rowversion (Standard) | none
	maps      []FieldMap
	keyCols   []string
	info      *TableInfo
	defaults  map[string]string
	maxNew    int
	changeCol string // Zeitstempel-Spalte bei kind timestamp
}

func newInFix(t *testing.T, o fixOpt) *inFix {
	t.Helper()
	sf := newStoreFixture(t)
	ctx := context.Background()
	if o.target == "" {
		o.target = RefTicket
	}
	if o.kind == "" {
		o.kind = changeKindRowVersion
	}
	if o.info == nil {
		o.info = fakeMeldungenInfo(o.kind == changeKindRowVersion)
	}
	if o.keyCols == nil {
		o.keyCols = []string{"Nr"}
	}
	if o.maps == nil {
		o.maps = stdMaps(o.target)
	}
	p := &Profile{
		Name: fmt.Sprintf("extsync-in-%d", time.Now().UnixNano()), ConnectionID: sf.connID, TargetType: o.target,
		SourceSchema: o.info.Schema, SourceTable: o.info.Name, KeyColumns: o.keyCols,
		ChangeKind: o.kind, Defaults: o.defaults, MaxNew: o.maxNew,
	}
	if o.kind == changeKindRowVersion {
		p.ChangeColumn = "RV"
	}
	if o.changeCol != "" {
		p.ChangeColumn = o.changeCol
	}
	must(t, sf.s.SaveProfile(ctx, p, systemUser))
	must(t, sf.s.ReplaceMaps(ctx, p.ID, o.maps))
	f := &inFix{t: t, sf: sf, s: sf.s, ff: newFakeForeign(o.info), d: fakeDialect{},
		tag: fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000), target: o.target, p: p}
	f.reload()
	f.in = &Inbound{Store: sf.s, SystemUser: systemUser, Targets: map[string]Target{
		RefTicket: NewTicketTarget(sf.pool, tickets.NewService(tickets.NewRepository(sf.pool))),
		RefFault:  NewFaultTarget(sf.pool, faults.NewService(faults.NewRepository(sf.pool), nil)),
	}}
	// Aufräumen vor dem Löschen des Profils (Cleanup läuft rückwärts).
	t.Cleanup(func() { cleanupProfileRecords(sf, p.ID) })
	return f
}

// cleanupProfileRecords löscht alle Datensätze, die über das Profil entstanden sind,
// samt Verlauf (record_history hat keinen Fremdschlüssel).
func cleanupProfileRecords(sf *storeFixture, profileID string) {
	c := context.Background()
	for _, tbl := range []string{"tickets", "faults"} {
		rt := map[string]string{"tickets": "ticket", "faults": "fault"}[tbl]
		_, _ = sf.pool.Exec(c, `WITH ids AS (
				SELECT ref_id FROM sync_links WHERE profile_id = $1 AND ref_type = $2 AND ref_id IS NOT NULL
				UNION SELECT ref_id FROM record_history WHERE ref_type = $2 AND action = 'sync_origin' AND message LIKE $1 || '|%')
			DELETE FROM record_history WHERE ref_type = $2 AND ref_id IN (SELECT ref_id FROM ids)`, profileID, rt)
		_, _ = sf.pool.Exec(c, `DELETE FROM `+tbl+` WHERE id IN (
				SELECT ref_id FROM sync_links WHERE profile_id = $1 AND ref_id IS NOT NULL)`, profileID)
	}
	_, _ = sf.pool.Exec(c, `DELETE FROM sync_profiles WHERE id = $1`, profileID)
}

func (f *inFix) reload() {
	f.t.Helper()
	ctx := context.Background()
	cur := f.p.Cursor
	p, err := f.s.GetProfile(ctx, f.p.ID)
	must(f.t, err)
	p.Cursor = cur
	f.p = p
	f.maps, err = f.s.ListMaps(ctx, p.ID)
	must(f.t, err)
}

// row legt eine Standardzeile an.
func (f *inFix) row(nr int64, titel, prio, zustand string) {
	f.ff.put(map[string]any{"Nr": nr, "Titel": titel + " " + f.tag, "Beschreibung": "Text " + titel, "Prio": prio, "Zustand": zustand})
}

func (f *inFix) title(s string) string { return s + " " + f.tag }

// run fährt einen Lauf und übernimmt den Cursor wie der spätere Taktgeber.
func (f *inFix) run(opt RunOptions) *Run {
	f.t.Helper()
	r, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, opt)
	if err != nil {
		f.t.Fatalf("Lauf: %v", err)
	}
	if !opt.Dry {
		f.p.Cursor = r.Cursor
	}
	return r
}

func (f *inFix) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	must(f.t, f.sf.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (f *inFix) links() int {
	return f.count(`SELECT count(*) FROM sync_links WHERE profile_id = $1`, f.p.ID)
}

func (f *inFix) records() int {
	tbl := map[string]string{RefTicket: "tickets", RefFault: "faults"}[f.target]
	return f.count(`SELECT count(*) FROM `+tbl+` WHERE title LIKE '%' || $1`, " "+f.tag)
}

func (f *inFix) outbox() int {
	return f.count(`SELECT count(*) FROM sync_outbox o JOIN sync_links l ON l.id = o.link_id WHERE l.profile_id = $1`, f.p.ID)
}

func (f *inFix) runsCount() int {
	return f.count(`SELECT count(*) FROM sync_runs WHERE profile_id = $1`, f.p.ID)
}

func (f *inFix) link(key string) *Link {
	f.t.Helper()
	l, err := f.s.LinkByKey(context.Background(), f.p.ID, key)
	must(f.t, err)
	return l
}

// rec liest Titel, Beschreibung, Stufe und Status des verknüpften Datensatzes.
func (f *inFix) rec(key string) (id, title, desc, level, status string) {
	f.t.Helper()
	l := f.link(key)
	if l.RefID == nil {
		f.t.Fatalf("Verknüpfung %s ohne Datensatz", key)
	}
	tbl, lv := "tickets", "priority"
	if f.target == RefFault {
		tbl, lv = "faults", "severity"
	}
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT id::text, title, COALESCE(description,''), `+lv+`::text, status::text
		FROM `+tbl+` WHERE id = $1`, *l.RefID).Scan(&id, &title, &desc, &level, &status))
	return
}

type histRow struct{ action, field, oldV, newV, msg string }

func (f *inFix) history(refID, action string) []histRow {
	f.t.Helper()
	rows, err := f.sf.pool.Query(context.Background(), `SELECT action, COALESCE(field_name,''), COALESCE(old_value,''),
		COALESCE(new_value,''), COALESCE(message,'') FROM record_history WHERE ref_id = $1 AND ($2 = '' OR action = $2)
		ORDER BY created_at, id`, refID, action)
	must(f.t, err)
	defer rows.Close()
	var out []histRow
	for rows.Next() {
		var h histRow
		must(f.t, rows.Scan(&h.action, &h.field, &h.oldV, &h.newV, &h.msg))
		out = append(out, h)
	}
	return out
}

// pdhUpdate ändert den Datensatz wie ein Benutzer, aber ohne Wartelisteneintrag
// (damit nur die Einträge des Abgleichs gezählt werden).
func (f *inFix) pdhUpdate(id, set string, args ...any) {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.sf.pool.Begin(ctx)
	must(f.t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `SET LOCAL pdh.sync_origin = '1'`)
	must(f.t, err)
	tbl := map[string]string{RefTicket: "tickets", RefFault: "faults"}[f.target]
	_, err = tx.Exec(ctx, `UPDATE `+tbl+` SET `+set+` WHERE id = $1`, append([]any{id}, args...)...)
	must(f.t, err)
	must(f.t, tx.Commit(ctx))
}

func k(n int) string { return fmt.Sprintf(`["%d"]`, n) }

// ── Tests ────────────────────────────────────────────────────────────────

func TestInboundCreatesOnce(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Pumpe", "hoch", "offen")
	f.row(2, "Band", "mittel", "in Arbeit")
	f.row(3, "Lampe", "niedrig", "offen")
	r1 := f.run(RunOptions{})
	if r1.New != 3 || r1.Failed != 0 {
		t.Fatalf("erster Lauf: %+v", r1)
	}
	r2 := f.run(RunOptions{})
	if r2.New != 0 || r2.Changed != 0 {
		t.Fatalf("zweiter Lauf: %+v", r2)
	}
	// Auch ohne gespeicherten Cursor (alles neu gelesen): nichts doppelt.
	f.p.Cursor = ""
	r3 := f.run(RunOptions{})
	if r3.New != 0 || r3.Changed != 0 {
		t.Fatalf("dritter Lauf ohne Cursor: %+v", r3)
	}
	if n := f.records(); n != 3 {
		t.Fatalf("%d Tickets, erwartet 3", n)
	}
	if n := f.links(); n != 3 {
		t.Fatalf("%d Verknüpfungen, erwartet 3", n)
	}
	id, title, desc, prio, status := f.rec(k(2))
	if title != f.title("Band") || desc != "Text Band" || prio != "medium" || status != "in_progress" {
		t.Fatalf("Ticket: %q %q %q %q", title, desc, prio, status)
	}
	// Herkunft im Verlauf, vom Systembenutzer.
	var origin, by string
	must(t, f.sf.pool.QueryRow(context.Background(), `SELECT message, created_by::text FROM record_history
		WHERE ref_id = $1 AND action = 'sync_origin'`, id).Scan(&origin, &by))
	if !strings.HasPrefix(origin, f.p.ID+`|["2"]|Aus `+f.p.Name+", Nummer 2") || by != systemUser {
		t.Fatalf("Herkunft: %q von %s", origin, by)
	}
	var createdBy string
	must(t, f.sf.pool.QueryRow(context.Background(), `SELECT created_by::text FROM tickets WHERE id = $1`, id).Scan(&createdBy))
	if createdBy != systemUser {
		t.Fatalf("angelegt von %s", createdBy)
	}
	runs, err := f.s.ListRuns(context.Background(), f.p.ID, 10)
	must(t, err)
	if len(runs) != 3 || runs[2].New != 3 || runs[2].FinishedAt == nil || runs[2].Direction != DirIn {
		t.Fatalf("Läufe: %+v", runs)
	}
	if f.ff.execs != 0 {
		t.Fatalf("%d Schreibbefehle an die Fremddatenbank", f.ff.execs)
	}
}

func TestInboundChangeApplied(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	f.ff.set(1, "Titel", f.title("Neu"))
	r := f.run(RunOptions{})
	if r.Changed != 1 || r.New != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	id, title, _, _, _ := f.rec(k(1))
	if title != f.title("Neu") {
		t.Fatalf("Titel %q", title)
	}
	h := f.history(id, "sync")
	if len(h) != 1 || h[0].field != "title" || h[0].oldV != f.title("Alt") || h[0].newV != f.title("Neu") {
		t.Fatalf("Verlauf: %+v", h)
	}
	if n := f.outbox(); n != 0 {
		t.Fatalf("%d Wartelisteneinträge, erwartet 0", n)
	}
	if l := f.link(k(1)); l.SeenValues["title"] != f.title("Neu") {
		t.Fatalf("gesehen: %+v", l.SeenValues)
	}
}

func TestInboundEchoIgnored(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	// PDH hat "Zurück" geschrieben und ins Fremdsystem übertragen.
	f.pdhUpdate(id, "title = $2", f.title("Zurück"))
	must(t, f.s.UpdateLinkWritten(context.Background(), f.link(k(1)).ID, map[string]string{"title": f.title("Zurück")}))
	f.ff.set(1, "Titel", f.title("Zurück"))
	r := f.run(RunOptions{})
	if r.Changed != 0 || r.Failed != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	if h := f.history(id, "sync"); len(h) != 0 {
		t.Fatalf("Verlauf bei Echo: %+v", h)
	}
	if n := f.outbox(); n != 0 {
		t.Fatalf("%d Wartelisteneinträge bei Echo", n)
	}
	if l := f.link(k(1)); l.SeenValues["title"] != f.title("Zurück") {
		t.Fatalf("gesehen nicht nachgeführt: %+v", l.SeenValues)
	}
}

func TestInboundConflictPDHWins(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	f.pdhUpdate(id, "title = $2", f.title("PDH"))
	f.ff.set(1, "Titel", f.title("Fremd"))
	r := f.run(RunOptions{})
	if r.Changed != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	_, title, _, _, _ := f.rec(k(1))
	if title != f.title("PDH") {
		t.Fatalf("Titel %q, PDH muss gewinnen", title)
	}
	h := f.history(id, "sync")
	if len(h) != 1 || !strings.Contains(h[0].msg, "Wert im Fremdsystem ‚"+f.title("Fremd")+"‘ nicht übernommen, PDH-Wert bleibt") {
		t.Fatalf("Verlauf: %+v", h)
	}
	if n := f.outbox(); n != 1 {
		t.Fatalf("%d Wartelisteneinträge, erwartet 1", n)
	}
	var kind, v string
	must(t, f.sf.pool.QueryRow(context.Background(), `SELECT o.kind, o.payload->'fields'->>'title' FROM sync_outbox o
		JOIN sync_links l ON l.id = o.link_id WHERE l.profile_id = $1`, f.p.ID).Scan(&kind, &v))
	if kind != KindUpdate || v != f.title("PDH") {
		t.Fatalf("Eintrag %s %q", kind, v)
	}
}

func TestInboundBadRowDoesNotStopRun(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.row(2, "Zwei", "dringend", "offen")
	f.row(3, "Drei", "mittel", "offen")
	r := f.run(RunOptions{})
	if r.New != 2 || r.Failed != 1 || len(r.Errors) != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	e := r.Errors[0]
	if e.ForeignKey != k(2) || e.Field != "priority" || !strings.Contains(e.Message, "dringend") || !strings.Contains(e.Message, "Priorität") {
		t.Fatalf("Fehler: %+v", e)
	}
	runs, _ := f.s.ListRuns(context.Background(), f.p.ID, 1)
	if runs[0].Failed != 1 || len(runs[0].Errors) != 1 || runs[0].Errors[0].ForeignKey != k(2) {
		t.Fatalf("gespeicherter Lauf: %+v", runs[0])
	}
	// Nächster Lauf versucht Zeile 2 erneut (scheitert wieder).
	r = f.run(RunOptions{})
	if r.Failed != 1 || r.New != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	// Im Fremdsystem korrigiert: wird angelegt.
	f.ff.set(2, "Prio", "hoch")
	r = f.run(RunOptions{})
	if r.New != 1 || r.Failed != 0 {
		t.Fatalf("dritter Lauf: %+v", r)
	}
	if n := f.records(); n != 3 {
		t.Fatalf("%d Tickets", n)
	}
}

func TestInboundMaxNewPauses(t *testing.T) {
	f := newInFix(t, fixOpt{maxNew: 2})
	f.row(1, "A", "hoch", "offen")
	f.row(2, "B", "hoch", "offen")
	f.row(3, "C", "hoch", "offen")
	start := f.p.Cursor
	r := f.run(RunOptions{})
	if r.New != 0 || !strings.Contains(r.SkippedReason, "Zu viele neue Meldungen") || !strings.Contains(r.SkippedReason, "Obergrenze 2") {
		t.Fatalf("Lauf: %+v", r)
	}
	if r.Cursor != start {
		t.Fatalf("Cursor bewegt: %q", r.Cursor)
	}
	if n := f.records(); n != 0 {
		t.Fatalf("%d Tickets trotz Obergrenze", n)
	}
	if n := f.links(); n != 0 {
		t.Fatalf("%d Verknüpfungen trotz Obergrenze", n)
	}
	g, err := f.s.GetProfile(context.Background(), f.p.ID)
	must(t, err)
	if g.State != StateHalted || !strings.Contains(g.StateNote, "Zu viele neue Meldungen") {
		t.Fatalf("Zustand %q %q", g.State, g.StateNote)
	}
	runs, _ := f.s.ListRuns(context.Background(), f.p.ID, 1)
	if len(runs) != 1 || runs[0].SkippedReason == "" || runs[0].FinishedAt == nil {
		t.Fatalf("Lauf gespeichert: %+v", runs)
	}
}

func TestInboundCrashBetweenCreateAndAttach(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Absturz", "hoch", "offen")
	ctx := context.Background()
	// Stand nach einem Absturz: Verknüpfung reserviert, Ticket samt Herkunft da, ref_id leer.
	l, created, err := f.s.ReserveLink(ctx, f.p.ID, k(1), RefTicket)
	must(t, err)
	if !created {
		t.Fatal("nicht reserviert")
	}
	tg := f.in.Targets[RefTicket]
	id, err := tg.Create(ctx, map[string]string{"title": f.title("Absturz"), "priority": "high"}, systemUser,
		f.p.ID+"|"+k(1)+"|Aus "+f.p.Name+", Nummer 1")
	must(t, err)
	r := f.run(RunOptions{})
	if r.New != 0 || r.Failed != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	if n := f.records(); n != 1 {
		t.Fatalf("%d Tickets, erwartet 1", n)
	}
	g := f.link(k(1))
	if g.ID != l.ID || g.RefID == nil || *g.RefID != id {
		t.Fatalf("Verknüpfung: %+v", g)
	}
	// Zweiter Lauf: weiterhin eins.
	f.p.Cursor = ""
	f.run(RunOptions{})
	if n := f.records(); n != 1 {
		t.Fatalf("%d Tickets nach zweitem Lauf", n)
	}
}

func TestInboundDryWritesNothing(t *testing.T) {
	f := newInFix(t, fixOpt{maxNew: 1})
	ctx := context.Background()
	// Eine Zeile schon verknüpft, dann geändert, plus zwei neue (über der Obergrenze 1).
	f.row(1, "Alt", "hoch", "offen")
	f.run(RunOptions{})
	f.ff.set(1, "Titel", f.title("Neu"))
	f.row(2, "Zwei", "hoch", "offen")
	f.row(3, "Drei", "niedrig", "erledigt")
	cur := f.p.Cursor
	tickets0, links0, runs0 := f.records(), f.links(), f.runsCount()
	hist0 := f.count(`SELECT count(*) FROM record_history WHERE ref_id = $1`, *f.link(k(1)).RefID)
	r, prev, err := f.in.Run(ctx, f.p, f.maps, f.ff, f.d, RunOptions{Dry: true})
	must(t, err)
	if r.New != 2 || r.Changed != 1 || r.SkippedReason != "" || r.Cursor != cur {
		t.Fatalf("Probelauf: %+v", r)
	}
	if len(prev) != 3 {
		t.Fatalf("Vorschau: %+v", prev)
	}
	byKey := map[string]Preview{}
	for _, p := range prev {
		byKey[p.ForeignKey] = p
	}
	if p := byKey[k(1)]; p.IsNew || p.Values["title"] != f.title("Neu") || len(p.Values) != 1 {
		t.Fatalf("Vorschau geändert: %+v", p)
	}
	if p := byKey[k(3)]; !p.IsNew || p.Values["status"] != "resolved" || p.Values["priority"] != "low" {
		t.Fatalf("Vorschau neu: %+v", p)
	}
	if f.records() != tickets0 || f.links() != links0 || f.runsCount() != runs0 || f.outbox() != 0 {
		t.Fatal("Probelauf hat geschrieben")
	}
	if n := f.count(`SELECT count(*) FROM record_history WHERE ref_id = $1`, *f.link(k(1)).RefID); n != hist0 {
		t.Fatal("Probelauf hat Verlauf geschrieben")
	}
	_, title, _, _, _ := f.rec(k(1))
	if title != f.title("Alt") {
		t.Fatalf("Titel geändert: %q", title)
	}
	g, _ := f.s.GetProfile(ctx, f.p.ID)
	if g.State == StateHalted {
		t.Fatal("Probelauf hat angehalten")
	}
	// Begrenzung der Vorschau.
	_, prev, err = f.in.Run(ctx, f.p, f.maps, f.ff, f.d, RunOptions{Dry: true, Limit: 2})
	must(t, err)
	if len(prev) != 2 {
		t.Fatalf("Vorschau mit Grenze 2: %d", len(prev))
	}
}

func TestInboundFaultTarget(t *testing.T) {
	f := newInFix(t, fixOpt{target: RefFault})
	f.row(1, "Störung", "hoch", "erledigt")
	f.row(2, "Offen", "niedrig", "offen")
	r := f.run(RunOptions{})
	if r.New != 2 || r.Failed != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	id, title, _, sev, status := f.rec(k(1))
	if title != f.title("Störung") || sev != "high" || status != "resolved" {
		t.Fatalf("Störmeldung: %q %q %q", title, sev, status)
	}
	var resolution string
	var archived bool
	must(t, f.sf.pool.QueryRow(context.Background(), `SELECT COALESCE(resolution,''), archived_at IS NOT NULL FROM faults WHERE id = $1`, id).Scan(&resolution, &archived))
	if resolution != "Im Fremdsystem erledigt" || !archived {
		t.Fatalf("Lösung %q, archiviert %v", resolution, archived)
	}
	if h := f.history(id, "resolved"); len(h) != 1 {
		t.Fatalf("QuickResolve-Verlauf: %+v", h)
	}
	if n := f.outbox(); n != 0 {
		t.Fatalf("Anlegen erzeugt %d Wartelisteneinträge", n)
	}
	// Später im Fremdsystem erledigt: QuickResolve am verknüpften Datensatz.
	id2, _, _, _, st2 := f.rec(k(2))
	if st2 != "detected" {
		t.Fatalf("Status %q", st2)
	}
	f.ff.set(2, "Zustand", "erledigt")
	r = f.run(RunOptions{})
	if r.Changed != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	if _, _, _, _, st2 = f.rec(k(2)); st2 != "resolved" {
		t.Fatalf("Status %q", st2)
	}
	// Verlauf: der vom Abgleich festgelegte Lösungstext (R22) und der Status.
	h := f.history(id2, "sync")
	byField := map[string]histRow{}
	for _, x := range h {
		byField[x.field] = x
	}
	if len(h) != 2 || byField["status"].oldV != "detected" || byField["status"].newV != "resolved" ||
		byField["resolution"].newV != resolvedText {
		t.Fatalf("Verlauf: %+v", h)
	}
	// QuickResolve nutzt eine eigene Verbindung: der Auslöser stellt einen Eintrag
	// ein. Er ist unschädlich, weil der Fremdwert schon als gesehen gilt (die
	// Warteliste erledigt ihn ohne Befehl, Task 9).
	l := f.link(k(2))
	if l.SeenValues["status"] != "resolved" {
		t.Fatalf("gesehen: %+v", l.SeenValues)
	}
	var payloadStatus string
	must(t, f.sf.pool.QueryRow(context.Background(), `SELECT COALESCE(payload->'fields'->>'status','') FROM sync_outbox WHERE link_id = $1`, l.ID).Scan(&payloadStatus))
	if payloadStatus != l.SeenValues["status"] {
		t.Fatalf("Eintrag %q weicht vom gesehenen Fremdwert ab", payloadStatus)
	}
	// Nächster Lauf (alles neu gelesen) schreibt nichts erneut.
	f.p.Cursor = ""
	r = f.run(RunOptions{})
	if r.Changed != 0 || len(f.history(id2, "sync")) != 2 {
		t.Fatalf("erneut geschrieben: %+v", r)
	}
}

func TestInboundCompositeKey(t *testing.T) {
	info := &TableInfo{Schema: "dbo", Name: "MeldungenGuid", Columns: []Column{
		{Name: "Id", Type: "uniqueidentifier", IsKey: true},
		{Name: "Werk", Type: "nvarchar", IsKey: true},
		{Name: "Titel", Type: "nvarchar"},
		{Name: "RV", Type: "timestamp", IsRowVersion: true},
	}}
	f := newInFix(t, fixOpt{info: info, keyCols: []string{"Id", "Werk"},
		maps: []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}})
	g := "3F2504E0-4F89-11D3-9A0C-0305E82C3301"
	f.ff.put(map[string]any{"Id": g, "Werk": "W1", "Titel": f.title("Sensor")})
	f.ff.put(map[string]any{"Id": g, "Werk": "W2", "Titel": f.title("Filter")})
	r := f.run(RunOptions{})
	if r.New != 2 {
		t.Fatalf("Lauf: %+v", r)
	}
	key := EncodeKey([]string{strings.ToLower(g), "W2"})
	_, title, _, _, _ := f.rec(key)
	if title != f.title("Filter") {
		t.Fatalf("Titel %q", title)
	}
	f.p.Cursor = ""
	if r = f.run(RunOptions{}); r.New != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	if n := f.records(); n != 2 {
		t.Fatalf("%d Tickets", n)
	}
}

func TestInboundPagingReadsAllPages(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.in.pageSize = 2
	for i := int64(1); i <= 5; i++ {
		f.row(i, fmt.Sprintf("Seite %d", i), "mittel", "offen")
	}
	r := f.run(RunOptions{})
	if r.New != 5 {
		t.Fatalf("Lauf: %+v", r)
	}
	if q := f.ff.queryCount(); q != 3 {
		t.Fatalf("%d Abfragen, erwartet 3 Seiten", q)
	}
	if n := f.records(); n != 5 {
		t.Fatalf("%d Tickets", n)
	}
	// Cursor steht hinter der letzten Zeile: nächster Lauf liest nichts Neues.
	if r = f.run(RunOptions{}); r.New != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
}

func TestInboundNoneKindDetectsChangeByMark(t *testing.T) {
	f := newInFix(t, fixOpt{kind: changeKindNone})
	f.in.pageSize = 2
	f.row(1, "Eins", "hoch", "offen")
	f.row(2, "Zwei", "hoch", "offen")
	f.row(3, "Drei", "hoch", "offen")
	r := f.run(RunOptions{})
	if r.New != 3 || r.Cursor != "" {
		t.Fatalf("Lauf: %+v", r)
	}
	q0 := f.ff.queryCount()
	r = f.run(RunOptions{})
	if r.New != 0 || r.Changed != 0 {
		t.Fatalf("ohne Änderung: %+v", r)
	}
	if q := f.ff.queryCount() - q0; q != 2 {
		t.Fatalf("%d Abfragen, erwartet ganze Tabelle in 2 Seiten", q)
	}
	f.ff.set(2, "Titel", f.title("Zwei neu"))
	r = f.run(RunOptions{})
	if r.Changed != 1 || r.New != 0 {
		t.Fatalf("mit Änderung: %+v", r)
	}
	id, title, _, _, _ := f.rec(k(2))
	if title != f.title("Zwei neu") || len(f.history(id, "sync")) != 1 {
		t.Fatalf("Titel %q", title)
	}
}

// failingTarget lässt Create für einen Titel scheitern.
type failingTarget struct {
	Target
	failTitle string
}

func (ft *failingTarget) Create(ctx context.Context, values map[string]string, user, origin string) (string, error) {
	if values["title"] == ft.failTitle {
		return "", errors.New("Datenbank meldet Fehler")
	}
	return ft.Target.Create(ctx, values, user, origin)
}

func TestInboundFailedRowIsRetriedNextRun(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.row(2, "Zwei", "hoch", "offen")
	f.row(3, "Drei", "hoch", "offen")
	real := f.in.Targets[RefTicket]
	f.in.Targets[RefTicket] = &failingTarget{Target: real, failTitle: f.title("Zwei")}
	r := f.run(RunOptions{})
	if r.New != 2 || r.Failed != 1 || r.Errors[0].ForeignKey != k(2) || !strings.Contains(r.Errors[0].Message, "Datenbank meldet Fehler") {
		t.Fatalf("Lauf: %+v", r)
	}
	// Der Cursor steht hinter der letzten gelesenen Zeile 3: die fehlerhafte
	// Zeile hält ihn nicht auf.
	if r.Cursor != f.cursorAfter(3) {
		t.Fatalf("Cursor %q, erwartet hinter Zeile 3", r.Cursor)
	}
	// Die reservierte Verknüpfung trägt den Fehler.
	if l := f.link(k(2)); l.RefID != nil || !strings.Contains(l.LastError, "Datenbank meldet Fehler") {
		t.Fatalf("Verknüpfung: %+v", l)
	}
	if n := f.count(`SELECT count(*) FROM tickets WHERE title = $1`, f.title("Zwei")); n != 0 {
		t.Fatalf("%d Tickets für die fehlerhafte Zeile", n)
	}
	// Nächster Lauf: Zeile 2 wird einzeln über SelectOne wiederholt.
	f.in.Targets[RefTicket] = real
	ones := f.ff.oneCount()
	r = f.run(RunOptions{})
	if r.New != 1 || r.Failed != 0 || f.ff.oneCount()-ones != 1 {
		t.Fatalf("Wiederholung: %+v, %d Einzelabfragen", r, f.ff.oneCount()-ones)
	}
	if n := f.records(); n != 3 {
		t.Fatalf("%d Tickets", n)
	}
	if l := f.link(k(2)); l.LastError != "" || l.RefID == nil {
		t.Fatalf("Verknüpfung nach Wiederholung: %+v", l)
	}
	// Danach nichts mehr zu wiederholen, nichts doppelt.
	ones = f.ff.oneCount()
	r = f.run(RunOptions{})
	if r.New != 0 || f.ff.oneCount() != ones || f.records() != 3 {
		t.Fatalf("dritter Lauf: %+v", r)
	}
}

// cursorAfter liefert den Cursor des Dialekts für die Zeile mit Nr = nr.
func (f *inFix) cursorAfter(nr int64) string {
	f.t.Helper()
	for _, x := range f.ff.rows {
		if x["Nr"] == nr {
			c, err := f.d.NextCursor(f.ff.info, f.p.KeyColumns, f.p.ChangeColumn, f.p.ChangeKind, x)
			must(f.t, err)
			return c
		}
	}
	f.t.Fatalf("Zeile %d fehlt", nr)
	return ""
}

func TestInboundBadRowDoesNotBlockOverloadPaging(t *testing.T) {
	f := newInFix(t, fixOpt{})
	for i := int64(1); i <= 60; i++ {
		prio := "mittel"
		if i == 3 {
			prio = "dringend" // dauerhaft nicht übersetzbar
		}
		f.row(i, fmt.Sprintf("Seite %d", i), prio, "offen")
	}
	r := f.run(RunOptions{Limit: 50})
	if r.New != 49 || r.Failed != 1 || r.Cursor != f.cursorAfter(50) {
		t.Fatalf("erster Lauf: %+v", r)
	}
	r = f.run(RunOptions{Limit: 50})
	// Zeile 3 wird wiederholt (scheitert wieder), die nächste Seite wird gelesen.
	if r.New != 10 || r.Failed != 1 || r.Errors[0].ForeignKey != k(3) || r.Cursor != f.cursorAfter(60) {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
	if n := f.records(); n != 59 {
		t.Fatalf("%d Tickets", n)
	}
	if l := f.link(k(3)); l.RefID != nil || !strings.Contains(l.LastError, "dringend") {
		t.Fatalf("Verknüpfung: %+v", l)
	}
}

func TestInboundCreateRollsBackWithoutOrigin(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Abbruch", "hoch", "offen")
	rt := f.in.Targets[RefTicket].(*recordTarget)
	var insertedID string
	rt.afterInsert = func(id string) error {
		insertedID = id
		return errors.New("Absturz nach dem Einfügen")
	}
	r := f.run(RunOptions{})
	if r.New != 0 || r.Failed != 1 || insertedID == "" {
		t.Fatalf("Lauf: %+v (eingefügt %q)", r, insertedID)
	}
	// Das eingefügte Ticket ist mit der Transaktion verschwunden, ohne Herkunft.
	if n := f.count(`SELECT count(*) FROM tickets WHERE id = $1 OR title = $2`, insertedID, f.title("Abbruch")); n != 0 {
		t.Fatalf("%d Tickets nach Abbruch vor dem Commit", n)
	}
	if n := f.count(`SELECT count(*) FROM record_history WHERE ref_id = $1`, insertedID); n != 0 {
		t.Fatalf("%d Verlaufseinträge nach Abbruch", n)
	}
	if l := f.link(k(1)); l.RefID != nil || !strings.Contains(l.LastError, "Absturz nach dem Einfügen") {
		t.Fatalf("Verknüpfung: %+v", l)
	}
	// Nächster Lauf legt genau eins an.
	rt.afterInsert = nil
	r = f.run(RunOptions{})
	if r.New != 1 || r.Failed != 0 {
		t.Fatalf("Wiederholung: %+v", r)
	}
	f.p.Cursor = ""
	f.run(RunOptions{})
	if n := f.records(); n != 1 {
		t.Fatalf("%d Tickets, erwartet 1", n)
	}
}

func TestInboundConcurrentRunsSameProfile(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.row(2, "Zwei", "hoch", "offen")
	f.row(3, "Drei", "hoch", "offen")
	gate := make(chan struct{})
	f.ff.gate = gate
	f.ff.entered = make(chan struct{}, 1)
	ctx := context.Background()
	type res struct {
		r   *Run
		err error
	}
	first := make(chan res, 1)
	p1 := *f.p
	go func() {
		r, _, err := f.in.Run(ctx, &p1, f.maps, f.ff, f.d, RunOptions{})
		first <- res{r, err}
	}()
	<-f.ff.entered // erster Lauf hält die Sperre und wartet in der Abfrage
	p2 := *f.p
	r2, _, err := f.in.Run(ctx, &p2, f.maps, f.ff, f.d, RunOptions{})
	must(t, err)
	if r2.SkippedReason != SkippedBusy || r2.New != 0 || r2.ID != "" {
		t.Fatalf("zweiter Lauf: %+v", r2)
	}
	close(gate)
	r1 := <-first
	must(t, r1.err)
	if r1.r.New != 3 {
		t.Fatalf("erster Lauf: %+v", r1.r)
	}
	if n := f.records(); n != 3 {
		t.Fatalf("%d Tickets, erwartet 3", n)
	}
	if n := f.runsCount(); n != 1 {
		t.Fatalf("%d Läufe gespeichert, erwartet 1", n)
	}
	// Sperre wieder frei: ein weiterer Lauf arbeitet normal.
	f.p.Cursor = r1.r.Cursor
	if r := f.run(RunOptions{}); r.SkippedReason != "" {
		t.Fatalf("Sperre nicht freigegeben: %+v", r)
	}
}

func TestInboundDefaultsForUnmappedFields(t *testing.T) {
	f := newInFix(t, fixOpt{
		maps:     []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}},
		defaults: map[string]string{"priority": "high", "description": "Aus dem Fremdsystem", "status": "in_progress"},
	})
	f.row(1, "Vorgabe", "niedrig", "offen")
	r := f.run(RunOptions{})
	if r.New != 1 || r.Failed != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	_, _, desc, prio, status := f.rec(k(1))
	if prio != "high" || desc != "Aus dem Fremdsystem" || status != "in_progress" {
		t.Fatalf("Vorgaben: %q %q %q", desc, prio, status)
	}
	// Gesehen nur für zugeordnete Felder.
	if l := f.link(k(1)); len(l.SeenValues) != 1 || l.SeenValues["title"] != f.title("Vorgabe") {
		t.Fatalf("gesehen: %+v", l.SeenValues)
	}
}

func TestInboundRequiredUnmappedFieldFailsRow(t *testing.T) {
	f := newInFix(t, fixOpt{maps: []FieldMap{{ForeignColumn: "Beschreibung", PDHField: "description", Direction: DirIn}}})
	f.row(1, "Ohne Titel", "hoch", "offen")
	f.row(2, "Auch ohne", "hoch", "offen")
	r := f.run(RunOptions{})
	if r.New != 0 || r.Failed != 2 || len(r.Errors) != 2 {
		t.Fatalf("Lauf: %+v", r)
	}
	if r.Errors[0].Message != "Pflichtfeld ‚Titel‘ ist leer" || r.Errors[0].Field != "title" || r.Errors[0].ForeignKey != k(1) {
		t.Fatalf("Fehler: %+v", r.Errors[0])
	}
	// Kein Datensatz; die Zeilen stehen als reservierte Verknüpfung mit Fehler
	// zur Wiederholung bereit.
	if f.links() != 2 || f.records() != 0 {
		t.Fatalf("%d Verknüpfungen, %d Tickets", f.links(), f.records())
	}
	if l := f.link(k(1)); l.RefID != nil || l.LastError != "Pflichtfeld ‚Titel‘ ist leer" {
		t.Fatalf("Verknüpfung: %+v", l)
	}
}

func TestInboundLinkedRecordDeleted(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Weg", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	_, err := f.sf.pool.Exec(context.Background(), `DELETE FROM tickets WHERE id = $1`, id)
	must(t, err)
	f.ff.set(1, "Titel", f.title("Weg neu"))
	r := f.run(RunOptions{})
	if r.Failed != 1 || r.New != 0 || r.Errors[0].Message != "Datensatz im PDH nicht mehr vorhanden" || r.Errors[0].ForeignKey != k(1) {
		t.Fatalf("Lauf: %+v", r)
	}
	if l := f.link(k(1)); l.LastError != "Datensatz im PDH nicht mehr vorhanden" {
		t.Fatalf("Verknüpfung: %+v", l)
	}
	if n := f.records(); n != 0 {
		t.Fatalf("%d Tickets neu angelegt", n)
	}
}

func TestInboundFirstSyncOverwriteIsInHistory(t *testing.T) {
	f := newInFix(t, fixOpt{maps: []FieldMap{{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn}}})
	f.row(1, "Erst", "hoch", "offen")
	f.run(RunOptions{})
	id, _, _, prio, _ := f.rec(k(1))
	if prio != "medium" {
		t.Fatalf("Ausgangspriorität %q", prio)
	}
	// Später wird die Priorität zugeordnet; die Fremdzeile selbst ist unverändert.
	must(t, f.s.ReplaceMaps(context.Background(), f.p.ID, []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn, Position: 0},
		{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn, Translate: stdTranslatePrio, Position: 1},
	}))
	f.reload()
	f.p.Cursor = ""
	r := f.run(RunOptions{})
	if r.Changed != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	if _, _, _, prio, _ = f.rec(k(1)); prio != "high" {
		t.Fatalf("Priorität %q", prio)
	}
	h := f.history(id, "sync")
	if len(h) != 1 || h[0].field != "priority" || h[0].oldV != "medium" || h[0].newV != "high" ||
		h[0].msg != "Wert aus dem Fremdsystem übernommen (erster Abgleich)" {
		t.Fatalf("Verlauf: %+v", h)
	}
}

func TestInboundOverloadLimitReadsOnePage(t *testing.T) {
	f := newInFix(t, fixOpt{})
	for i := int64(1); i <= 60; i++ {
		f.row(i, fmt.Sprintf("Last %d", i), "mittel", "offen")
	}
	r := f.run(RunOptions{Limit: 50})
	if r.New != 50 || f.ff.queryCount() != 1 {
		t.Fatalf("Lauf: %+v, %d Abfragen", r, f.ff.queryCount())
	}
	r = f.run(RunOptions{Limit: 50})
	if r.New != 10 || f.ff.queryCount() != 2 {
		t.Fatalf("zweiter Lauf: %+v, %d Abfragen", r, f.ff.queryCount())
	}
	if n := f.records(); n != 60 {
		t.Fatalf("%d Tickets", n)
	}
}

func TestInboundWholeRunErrorFinishesRunRow(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.ff.failQuery[1] = errors.New("Verbindung getrennt")
	start := f.p.Cursor
	r, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "Verbindung getrennt") {
		t.Fatalf("Fehler erwartet, bekam %v", err)
	}
	if r == nil || r.Cursor != start {
		t.Fatalf("Lauf: %+v", r)
	}
	runs, _ := f.s.ListRuns(context.Background(), f.p.ID, 10)
	if len(runs) != 1 || runs[0].FinishedAt == nil || !strings.Contains(runs[0].SkippedReason, "Verbindung getrennt") {
		t.Fatalf("Lauf gespeichert: %+v", runs)
	}
	if f.records() != 0 || f.links() != 0 {
		t.Fatal("trotz Fehler geschrieben")
	}
	// Konfigurationsfehler: Schlüssel nicht eindeutig.
	f.p.KeyColumns = []string{"Titel"}
	_, _, err = f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "eindeutig") {
		t.Fatalf("Konfigurationsfehler erwartet, bekam %v", err)
	}
	runs, _ = f.s.ListRuns(context.Background(), f.p.ID, 10)
	if len(runs) != 2 || runs[0].FinishedAt == nil || !strings.Contains(runs[0].SkippedReason, "eindeutig") {
		t.Fatalf("Lauf gespeichert: %+v", runs)
	}
	g, _ := f.s.GetProfile(context.Background(), f.p.ID)
	if g.State != StateDraft {
		t.Fatalf("Zustand verändert: %s", g.State)
	}
}

// panicTarget löst beim Lesen einen Absturz aus.
type panicTarget struct{ Target }

func (panicTarget) Read(context.Context, string) (map[string]string, error) { panic("kaputt") }

func TestInboundPanicIsRecovered(t *testing.T) {
	f := newInFix(t, fixOpt{})
	f.row(1, "Eins", "hoch", "offen")
	f.run(RunOptions{})
	f.ff.set(1, "Titel", f.title("Zwei"))
	f.in.Targets[RefTicket] = panicTarget{f.in.Targets[RefTicket]}
	_, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "kaputt") {
		t.Fatalf("Fehler erwartet, bekam %v", err)
	}
	runs, _ := f.s.ListRuns(context.Background(), f.p.ID, 1)
	if len(runs) != 1 || runs[0].FinishedAt == nil {
		t.Fatalf("Lauf nicht abgeschlossen: %+v", runs)
	}
}

// Ende-zu-Ende gegen das Labor-SQL-Server (nur lesend): echter Dialekt und NewSQLForeign.
func TestInboundLabEndToEnd(t *testing.T) {
	db := openLab(t)
	sf := newStoreFixture(t)
	ctx := context.Background()
	d, err := DialectFor("mssql")
	must(t, err)
	p := &Profile{Name: fmt.Sprintf("extsync-lab-%d", time.Now().UnixNano()), ConnectionID: sf.connID, TargetType: RefTicket,
		SourceSchema: "dbo", SourceTable: "Meldungen", KeyColumns: []string{"MeldungNr"},
		ChangeColumn: "RV", ChangeKind: changeKindRowVersion}
	must(t, sf.s.SaveProfile(ctx, p, systemUser))
	t.Cleanup(func() { cleanupProfileRecords(sf, p.ID) })
	must(t, sf.s.ReplaceMaps(ctx, p.ID, []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn, Position: 0},
		{ForeignColumn: "Beschreibung", PDHField: "description", Direction: DirIn, Position: 1},
		{ForeignColumn: "Prio", PDHField: "priority", Direction: DirIn, Translate: stdTranslatePrio, Position: 2},
		{ForeignColumn: "Zustand", PDHField: "status", Direction: DirIn, Position: 3,
			Translate: map[string]string{"offen": "open", "in Arbeit": "in_progress", "erledigt": "resolved"}},
	}))
	maps, err := sf.s.ListMaps(ctx, p.ID)
	must(t, err)
	p, err = sf.s.GetProfile(ctx, p.ID)
	must(t, err)
	var labRows int
	must(t, db.QueryRowContext(ctx, `SELECT count(*) FROM dbo.Meldungen`).Scan(&labRows))

	in := &Inbound{Store: sf.s, SystemUser: systemUser, Targets: map[string]Target{
		RefTicket: NewTicketTarget(sf.pool, tickets.NewService(tickets.NewRepository(sf.pool))),
	}}
	fr := NewSQLForeign(db, d, 30*time.Second)
	r1, _, err := in.Run(ctx, p, maps, fr, d, RunOptions{})
	must(t, err)
	t.Logf("erster Lauf: neu=%d geändert=%d fehler=%d cursor=%s", r1.New, r1.Changed, r1.Failed, r1.Cursor)
	if r1.New != labRows || r1.Failed != 0 {
		t.Fatalf("erster Lauf: %+v (Laborzeilen %d)", r1, labRows)
	}
	p.Cursor = r1.Cursor
	r2, _, err := in.Run(ctx, p, maps, fr, d, RunOptions{})
	must(t, err)
	t.Logf("zweiter Lauf: neu=%d geändert=%d fehler=%d", r2.New, r2.Changed, r2.Failed)
	if r2.New != 0 {
		t.Fatalf("zweiter Lauf legt an: %+v", r2)
	}
	var n int
	must(t, sf.pool.QueryRow(ctx, `SELECT count(*) FROM sync_links l JOIN tickets t ON t.id = l.ref_id WHERE l.profile_id = $1`, p.ID).Scan(&n))
	if n != labRows {
		t.Fatalf("%d Tickets, erwartet %d", n, labRows)
	}
	var st string
	must(t, sf.pool.QueryRow(ctx, `SELECT t.status::text FROM sync_links l JOIN tickets t ON t.id = l.ref_id
		WHERE l.profile_id = $1 AND t.title = 'Druckluft pfeift'`, p.ID).Scan(&st))
	if st != "resolved" {
		t.Fatalf("Status 'erledigt' -> %q", st)
	}
}
