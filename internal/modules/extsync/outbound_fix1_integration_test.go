package extsync

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Tests der Fix-Runde 1 (R31–R34, I2, I4).

// ── C1 / R31: Quelle fest, sobald es Verknüpfungen gibt ──────────────────

func TestSaveProfileSourceLockedWithLinks(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	f.synced(1, "Alt", "hoch", "offen")
	ctx := context.Background()
	var otherConn string
	must(t, f.sf.pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('import', 'mssql', 'extsync-andere-' || $1) RETURNING id`, f.tag).Scan(&otherConn))
	t.Cleanup(func() {
		_, _ = f.sf.pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1`, otherConn)
	})
	changes := map[string]func(p *Profile){
		"Verbindung": func(p *Profile) { p.ConnectionID = otherConn },
		"Schema":     func(p *Profile) { p.SourceSchema = "andere" },
		"Tabelle":    func(p *Profile) { p.SourceTable = "Andere" },
		"Schlüssel":  func(p *Profile) { p.KeyColumns = []string{"Titel"} },
	}
	for name, change := range changes {
		before, err := f.s.GetProfile(ctx, f.p.ID)
		must(t, err)
		p, _ := f.s.GetProfile(ctx, f.p.ID)
		p.Name = "geändert " + name // soll mit abgelehnt werden (eine Änderung)
		change(p)
		if err := f.s.SaveProfile(ctx, p, systemUser); !errors.Is(err, ErrSourceLocked) {
			t.Fatalf("%s: Fehler %v, erwartet ErrSourceLocked", name, err)
		}
		g, err := f.s.GetProfile(ctx, f.p.ID)
		must(t, err)
		if g.ConnectionID != before.ConnectionID || g.SourceSchema != before.SourceSchema || g.SourceTable != before.SourceTable ||
			strings.Join(g.KeyColumns, ",") != strings.Join(before.KeyColumns, ",") || g.Name != before.Name || g.State != before.State {
			t.Fatalf("%s: Profil verändert: %+v", name, g)
		}
	}
	// Alles andere bleibt änderbar.
	p, _ := f.s.GetProfile(ctx, f.p.ID)
	p.Name, p.IntervalMinutes, p.FilterSQL = "neuer Name "+f.tag, 10, "Prio = 'hoch'"
	must(t, f.s.SaveProfile(ctx, p, systemUser))
}

func TestSaveProfileSourceFreeWithoutLinks(t *testing.T) {
	sf := newStoreFixture(t)
	p := sf.profile(t, fmt.Sprintf("frei-%d", time.Now().UnixNano()))
	p.SourceTable, p.KeyColumns = "Andere", []string{"Nr"}
	must(t, sf.s.SaveProfile(context.Background(), p, systemUser))
	g, err := sf.s.GetProfile(context.Background(), p.ID)
	must(t, err)
	if g.SourceTable != "Andere" || g.KeyColumns[0] != "Nr" {
		t.Fatalf("Profil %+v", g)
	}
}

// Eine gleichzeitig angelegte Verknüpfung kann die Sperre nicht umgehen.
func TestSaveProfileSourceLockConcurrentLink(t *testing.T) {
	sf := newStoreFixture(t)
	ctx := context.Background()
	p := sf.profile(t, fmt.Sprintf("gleichzeitig-%d", time.Now().UnixNano()))
	tx, err := sf.pool.Begin(ctx)
	must(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `INSERT INTO sync_links (profile_id, foreign_key, ref_type) VALUES ($1, '["1"]', 'ticket')`, p.ID)
	must(t, err)
	done := make(chan error, 1)
	go func() {
		q := *p
		q.SourceTable = "Andere"
		done <- sf.s.SaveProfile(ctx, &q, systemUser)
	}()
	select {
	case err := <-done:
		t.Fatalf("Speichern wartete nicht auf die offene Verknüpfung: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	must(t, tx.Commit(ctx))
	if err := <-done; !errors.Is(err, ErrSourceLocked) {
		t.Fatalf("Fehler %v, erwartet ErrSourceLocked", err)
	}
	g, _ := sf.s.GetProfile(ctx, p.ID)
	if g.SourceTable != "Tickets" {
		t.Fatalf("Tabelle %q", g.SourceTable)
	}
}

// Abwehr in der Tiefe: Schlüssel der Verknüpfung passt nicht zu den Schlüsselspalten.
func TestOutboundKeyArityMismatchFailsClosed(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.withComments(stdCommentTable, kommentareInfo())
	user := f.testUser()
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.addComment(id, user, "Kommentar", time.Now())
	for _, it := range f.openItems() {
		lk := f.linkByID(it.LinkID)
		lk.ForeignKey = `["1","W1"]` // wie nach einem Wechsel der Schlüsselspalten
		res, err := f.out.Process(context.Background(), it, lk, f.p, f.maps, f.ff, f.d, true)
		if err != nil || !res.Done || res.Note != noteOutKeyMismatch {
			t.Fatalf("%s: %+v %v", it.Kind, res, err)
		}
	}
	if n := len(f.ff.execStmts()); n != 0 {
		t.Fatalf("%d Befehle", n)
	}
	if h := f.histOut(id); len(h) != 2 || h[0].msg != noteOutKeyMismatch {
		t.Fatalf("Verlauf %+v", h)
	}
}

// ── I1 / R32: vor dem Ändern die Fremdzeile lesen ────────────────────────

// Benutzer setzt B, B geht hinaus, Benutzer setzt zurück auf A: A muss hinaus.
func TestOutboundRevertIsWrittenBack(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "A", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("B"))
	f.processAll(true)
	f.userUpdate(id, "title = $2", f.title("A"))
	f.processAll(true)
	if got := f.ff.get(1)["Titel"]; got != f.title("A") {
		t.Fatalf("Fremdtitel %v, erwartet die Rücknahme", got)
	}
	if n := len(f.openItems()); n != 0 {
		t.Fatalf("%d offen", n)
	}
	// Danach Echo.
	r := f.run(RunOptions{})
	if _, title, _, _, _ := f.rec(k(1)); r.Changed != 0 || title != f.title("A") {
		t.Fatalf("Lauf %+v, Titel %q", r, title)
	}
}

// Derselbe Wert zweimal: der zweite Eintrag findet ihn schon im Fremdsystem (a).
func TestOutboundSameValueTwiceSkippedByForeignRead(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.processAll(true)
	f.run(RunOptions{}) // Echo: written[title] aufgeräumt
	if _, ok := f.link(k(1)).WrittenValues["title"]; ok {
		t.Fatal("written nicht aufgeräumt – Test prüft sonst (b)")
	}
	f.enqueue(f.link(k(1)).ID, KindUpdate, map[string]any{"fields": map[string]any{"title": "egal"}})
	ones := f.ff.oneCount()
	f.processAll(true)
	if n := len(f.updates()); n != 1 || f.ff.oneCount() == ones || len(f.openItems()) != 0 {
		t.Fatalf("%d Befehle, Lesen %d -> %d, offen %d", n, ones, f.ff.oneCount(), len(f.openItems()))
	}
}

// Verschwindet die Zeile zwischen Lesen und Schreiben, trifft der Befehl null
// Zeilen: derselbe Endzustand wie beim Lesen.
func TestOutboundRowGoneBetweenReadAndWrite(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	f.ff.beforeExec = func(Stmt) { f.ff.del(1) }
	res, err := f.process(f.openItems()[0], true)
	if err != nil || !res.Done || res.Note != ErrRowGone.Error() || len(f.ff.execStmts()) != 1 {
		t.Fatalf("Ergebnis %+v %v", res, err)
	}
	if l := f.link(k(1)); l.LastError != LinkErrForeignGone || len(f.histOut(id)) != 1 {
		t.Fatalf("Verknüpfung %+v", l)
	}
}

// Vorabeintrag des hereinkommenden Laufs, dessen Schreiben nie geschah, danach
// eine Benutzeränderung: die Benutzeränderung geht hinaus.
func TestOutboundDanglingWriteAheadThenUserEdit(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	must(t, f.s.SaveInbound(context.Background(), f.link(k(1)).ID, InboundUpdate{WrittenSet: map[string]string{"title": "Vorab"}}))
	f.userUpdate(id, "title = $2", f.title("Benutzer"))
	f.processAll(true)
	if got := f.ff.get(1)["Titel"]; got != f.title("Benutzer") {
		t.Fatalf("Fremdtitel %v", got)
	}
}

// Widerspruch (PDH gewinnt) aus dem hereinkommenden Lauf: der PDH-Wert geht
// hinaus, danach Echo.
func TestOutboundConflictKeepPDHWrittenThenEcho(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.pdhUpdate(id, "title = $2", f.title("PDH"))
	f.ff.set(1, "Titel", f.title("Fremd"))
	f.run(RunOptions{})
	if n := len(f.openItems()); n != 1 {
		t.Fatalf("%d Einträge aus dem Widerspruch", n)
	}
	f.processAll(true)
	if got := f.ff.get(1)["Titel"]; got != f.title("PDH") {
		t.Fatalf("Fremdtitel %v", got)
	}
	hist, box := len(f.history(id, "sync")), f.outbox()
	r := f.run(RunOptions{})
	_, title, _, _, _ := f.rec(k(1))
	if r.Changed != 0 || title != f.title("PDH") || len(f.history(id, "sync")) != hist || f.outbox() != box {
		t.Fatalf("kein Echo: %+v, Titel %q", r, title)
	}
}

// ── I2: gekürzte Werte gegen den gesehenen Wert falten, gleiche Normalform ─

func TestInboundFoldTruncatedAgainstSeen(t *testing.T) {
	info := fakeMeldungenInfo(true)
	info.Columns[1].MaxLen = 30
	f := newOutFix(t, fixOpt{info: info})
	id := f.synced(1, "Alt", "hoch", "offen")
	long := f.title(strings.Repeat("Lang", 12))
	f.userUpdate(id, "title = $2", long)
	f.processAll(true)
	f.run(RunOptions{}) // Echo: gesehen = langer Wert
	// Benutzer ändert erneut (Hinausschreiben verzögert), im Fremdsystem ändert sich eine andere Spalte.
	long2 := f.title(strings.Repeat("Neu", 15))
	f.pdhUpdate(id, "title = $2", long2)
	f.ff.set(1, "Prio", "niedrig")
	box := f.outbox()
	r := f.run(RunOptions{})
	for _, h := range f.history(id, "sync") {
		if strings.Contains(h.msg, "nicht übernommen") {
			t.Fatalf("erfundener Widerspruch: %+v", h)
		}
	}
	_, title, _, level, _ := f.rec(k(1))
	if r.Failed != 0 || title != long2 || level != "low" || f.outbox() != box {
		t.Fatalf("Lauf %+v, Titel %q, Stufe %q, Warteliste %d -> %d", r, title, level, box, f.outbox())
	}
}

func TestOutboundTruncateTrimsWhitespace(t *testing.T) {
	info := fakeMeldungenInfo(true)
	info.Columns[1].MaxLen = 30
	f := newOutFix(t, fixOpt{info: info})
	id := f.synced(1, "Alt", "hoch", "offen")
	long := "   " + f.title(strings.Repeat("Größe", 10)) + "  "
	f.userUpdate(id, "title = $2", long)
	f.processAll(true)
	want := string([]rune(strings.TrimSpace(long))[:30])
	if got := f.ff.get(1)["Titel"]; got != want {
		t.Fatalf("Fremdtitel %q, erwartet %q", got, want)
	}
	box := f.outbox()
	r := f.run(RunOptions{})
	_, title, _, _, _ := f.rec(k(1))
	if r.Changed != 0 || title != long || f.outbox() != box {
		t.Fatalf("kein Echo: %+v, Titel %q", r, title)
	}
}

// ── I3 / R33: Kürzen nach dem Maß der Datenbankart ───────────────────────

func TestMssqlTruncateText(t *testing.T) {
	d := &mssqlDialect{}
	nv := func(n int) Column { return Column{Name: "T", Type: "nvarchar", MaxLen: n} }
	cases := []struct {
		name string
		c    Column
		in   string
		want string
	}{
		{"Emoji an der Schnittstelle", nv(5), "abcd😀x", "abcd"},
		{"Emoji passt genau", nv(5), "abc😀", "abc😀"},
		{"kombinierendes Zeichen bleibt beim Grundzeichen", nv(5), "abcdéf", "abcd"},
		{"kombinierend passt", nv(6), "abcdéf", "abcdé"},
		{"nchar", Column{Type: "nchar", MaxLen: 3}, "äöüß", "äöü"},
		{"varchar zählt Zeichen", Column{Type: "varchar", MaxLen: 3}, "äöüß", "äöü"},
		{"ohne Höchstlänge", nv(0), "beliebig lang", "beliebig lang"},
		{"keine Textspalte", Column{Type: "int", MaxLen: 2}, "12345", "12345"},
		{"kurz genug", nv(10), "kurz", "kurz"},
	}
	for _, c := range cases {
		got := d.TruncateText(c.c, c.in)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("%s: %q, erwartet %q", c.name, got, c.want)
		}
		if got2 := (fakeDialect{}).TruncateText(c.c, c.in); got2 != got {
			t.Errorf("%s: Attrappe %q, Dialekt %q", c.name, got2, got)
		}
	}
}

func TestOutboundCommentTruncatesUTF16(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	info := kommentareInfo()
	info.Columns[2].MaxLen = 5 // Text nvarchar(5)
	f.withComments(stdCommentTable, info)
	user := f.testUser()
	f.addComment(id, user, "abcd😀xyz", time.Now())
	f.processAll(true)
	rows := f.ff.childRows("dbo", "MeldungKommentare")
	if len(rows) != 1 || rows[0]["Text"] != "abcd" {
		t.Fatalf("Zeilen %+v", rows)
	}
}

// ── I4: Abbruch vor/während des Einfügens eines Kommentars ───────────────

func (f *outFix) sendingSet(id int64) bool {
	f.t.Helper()
	var set bool
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT sending_at IS NOT NULL FROM sync_outbox WHERE id = $1`, id).Scan(&set))
	return set
}

func commentFix(t *testing.T) (*outFix, string) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.withComments(stdCommentTable, kommentareInfo())
	f.addComment(id, f.testUser(), "Kommentar", time.Now())
	return f, id
}

func TestOutboundCommentCancelledBeforeSend(t *testing.T) {
	f, _ := commentFix(t)
	it := f.openItems()[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.out.Process(ctx, it, f.linkByID(it.LinkID), f.p, f.maps, f.ff, f.d, true)
	if err == nil || res.Done {
		t.Fatalf("Ergebnis %+v %v", res, err)
	}
	if f.sendingSet(it.ID) || len(f.ff.execStmts()) != 0 {
		t.Fatalf("Marke %v, %d Befehle", f.sendingSet(it.ID), len(f.ff.execStmts()))
	}
	// Später normal eingefügt.
	f.processAll(true)
	if n := len(f.ff.childRows("dbo", "MeldungKommentare")); n != 1 {
		t.Fatalf("%d Zeilen", n)
	}
}

func TestOutboundCommentInsertNotCancelled(t *testing.T) {
	f, _ := commentFix(t)
	it := f.openItems()[0]
	ctx, cancel := context.WithCancel(context.Background())
	f.ff.beforeExec = func(Stmt) { cancel() } // Halt kommt während des Einfügens
	res, err := f.out.Process(ctx, it, f.linkByID(it.LinkID), f.p, f.maps, f.ff, f.d, true)
	if err != nil || !res.Done || res.Written != 1 {
		t.Fatalf("Ergebnis %+v %v", res, err)
	}
	if len(f.ff.execCtxErrs) != 1 || f.ff.execCtxErrs[0] != nil {
		t.Fatalf("Einfügen lief mit abgebrochenem Kontext: %v", f.ff.execCtxErrs)
	}
}

func TestOutboundCommentBadConnRetried(t *testing.T) {
	f, _ := commentFix(t)
	f.ff.failExec = func(Stmt) error { return fmt.Errorf("senden: %w", driver.ErrBadConn) }
	f.processAll(true)
	f.ff.failExec = nil
	it := f.openItems()
	if len(it) != 1 || f.sendingSet(it[0].ID) {
		t.Fatalf("nach ErrBadConn: %+v, Marke %v", it, len(it) == 1 && f.sendingSet(it[0].ID))
	}
	must(t, f.s.OutboxRetryNow(context.Background(), it[0].ID))
	f.processAll(true)
	if n := len(f.ff.childRows("dbo", "MeldungKommentare")); n != 1 || len(f.openItems()) != 0 {
		t.Fatalf("%d Zeilen, %d offen", n, len(f.openItems()))
	}
}

// ── I5 / R34: hängende Einträge einmal am Datensatz melden ────────────────

func TestShouldReportStuck(t *testing.T) {
	for n, want := range map[int]bool{0: false, 1: false, 4: false, StuckAfterAttempts: true, 6: false, 50: false} {
		if ShouldReportStuck(n) != want {
			t.Errorf("ShouldReportStuck(%d) = %v", n, !want)
		}
	}
	if StuckAfterAttempts != 5 {
		t.Fatalf("StuckAfterAttempts = %d", StuckAfterAttempts)
	}
}

func TestOutboundReportStuckOnce(t *testing.T) {
	f := newOutFix(t, fixOpt{})
	id := f.synced(1, "Alt", "hoch", "offen")
	f.userUpdate(id, "title = $2", f.title("Neu"))
	it := f.openItems()[0]
	l := f.linkByID(it.LinkID)
	cause := errors.New("Konvertierung fehlgeschlagen: " + strings.Repeat("x", 400))
	for i := 0; i < 2; i++ {
		must(t, f.out.ReportStuck(context.Background(), it, l, f.p, cause))
	}
	var notes []histRow
	for _, h := range f.history(id, "sync") {
		if strings.HasPrefix(h.msg, "Änderung konnte bisher nicht ins Fremdsystem geschrieben werden: ") {
			notes = append(notes, h)
		}
	}
	if len(notes) != 1 {
		t.Fatalf("Vermerke %+v", notes)
	}
	detail := strings.TrimPrefix(notes[0].msg, "Änderung konnte bisher nicht ins Fremdsystem geschrieben werden: ")
	if utf8.RuneCountInString(detail) != 300 || !strings.HasPrefix(detail, "Konvertierung fehlgeschlagen: ") {
		t.Fatalf("Fehlertext %d Zeichen: %q", utf8.RuneCountInString(detail), detail)
	}
	if lk := f.link(k(1)); lk.LastError != "" {
		t.Fatalf("last_error der Verknüpfung gesetzt: %q", lk.LastError)
	}
	if n := len(f.openItems()); n != 1 {
		t.Fatalf("Eintrag erledigt: %d offen", n)
	}
}
