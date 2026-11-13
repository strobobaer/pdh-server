package extsync

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEncodeKeyRoundTrip(t *testing.T) {
	cases := [][]string{
		{"17"},
		{"a b", "x|y"},
		{"3f2504e0-4f89-11d3-9a0c-0305e82c3301"},
		{"mit \"Anführung\"", "und,Komma", ""},
	}
	for _, in := range cases {
		key := EncodeKey(in)
		out, err := DecodeKey(key)
		if err != nil {
			t.Fatalf("DecodeKey(%q): %v", key, err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Fatalf("Rundlauf %q: %v != %v", key, out, in)
		}
	}
	if got := EncodeKey([]string{"17"}); got != `["17"]` {
		t.Fatalf("EncodeKey = %s", got)
	}
	for _, bad := range []string{"17", `{"a":1}`, `[1,2]`, ``, `["a",null]`} {
		if _, err := DecodeKey(bad); err == nil {
			t.Fatalf("DecodeKey(%q) hätte scheitern müssen", bad)
		}
	}
}

func TestFieldsCatalog(t *testing.T) {
	tk := Fields("ticket")
	fl := Fields("fault")
	has := func(fs []PDHField, k string) *PDHField {
		for i := range fs {
			if fs[i].Key == k {
				return &fs[i]
			}
		}
		return nil
	}
	if f := has(tk, "title"); f == nil || !f.Required || f.MaxLen != 255 {
		t.Fatalf("title: %+v", f)
	}
	if has(tk, "priority") == nil || has(tk, "severity") != nil {
		t.Fatal("Ticket: priority ja, severity nein")
	}
	if has(fl, "severity") == nil || has(fl, "priority") != nil {
		t.Fatal("Störmeldung: severity ja, priority nein")
	}
	if f := has(fl, "status"); f == nil || len(f.Values) != 6 {
		t.Fatalf("Störmeldung status: %+v", f)
	}
	if f := has(tk, "status"); f == nil || len(f.Values) != 5 {
		t.Fatalf("Ticket status: %+v", f)
	}
	if f := has(tk, "due_date"); f == nil || f.Kind != "time" {
		t.Fatalf("due_date: %+v", f)
	}
	if Fields("unbekannt") != nil {
		t.Fatal("unbekannter Typ liefert Felder")
	}
}

// storeFixture legt eine Verbindung an und räumt sie samt Profilen auf.
type storeFixture struct {
	pool   *pgxpool.Pool
	s      *Store
	connID string
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	pool := testPool(t)
	f := &storeFixture{pool: pool, s: NewStore(pool)}
	name := fmt.Sprintf("extsync-store-%d", time.Now().UnixNano())
	must(t, pool.QueryRow(context.Background(), `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('import', 'mssql', $1) RETURNING id`, name).Scan(&f.connID))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM sync_profiles WHERE connection_id = $1`, f.connID)
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE id = $1`, f.connID)
	})
	return f
}

func (f *storeFixture) profile(t *testing.T, name string) *Profile {
	t.Helper()
	p := &Profile{
		Name: name, ConnectionID: f.connID, TargetType: "ticket",
		SourceSchema: "dbo", SourceTable: "Tickets", KeyColumns: []string{"Id"},
		ChangeColumn: "Geaendert", ChangeKind: "timestamp",
		Defaults: map[string]string{"priority": "medium"},
	}
	must(t, f.s.SaveProfile(context.Background(), p, systemUser))
	if p.ID == "" {
		t.Fatal("ID nicht gesetzt")
	}
	return p
}

func (f *storeFixture) arm(t *testing.T, id string) {
	t.Helper()
	must(t, f.s.SetState(context.Background(), id, "scharf", ""))
}

func TestSaveProfileDefaultsAndRoundTrip(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := &Profile{Name: "rt", ConnectionID: f.connID, TargetType: "fault", SourceTable: "T", KeyColumns: []string{"A", "B"},
		CommentTable: &CommentTable{Schema: "dbo", Table: "K", FKColumn: "Id", TextColumn: "Text"}}
	must(t, f.s.SaveProfile(ctx, p, systemUser))
	g, err := f.s.GetProfile(ctx, p.ID)
	must(t, err)
	if g.IntervalMinutes != 5 || g.MaxNew != 200 || g.State != "entwurf" || g.ChangeKind != "none" {
		t.Fatalf("Vorgaben: %+v", g)
	}
	if !reflect.DeepEqual(g.KeyColumns, []string{"A", "B"}) || g.CommentTable == nil || g.CommentTable.Table != "K" {
		t.Fatalf("Rundlauf: %+v", g)
	}
	if g.Defaults == nil {
		t.Fatal("Defaults nil")
	}
	if _, err := f.s.GetProfile(ctx, "00000000-0000-4000-8000-0000000000aa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("erwartet ErrNotFound, bekam %v", err)
	}
	list, err := f.s.ListProfiles(ctx)
	must(t, err)
	found := false
	for _, x := range list {
		if x.ID == p.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("ListProfiles ohne Profil")
	}
	must(t, f.s.DeleteProfile(ctx, p.ID))
	if _, err := f.s.GetProfile(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nach Löschen: %v", err)
	}
}

func TestSaveProfileResetsState(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "reset")
	must(t, f.s.SetState(ctx, p.ID, "scharf", ""))
	_, err := f.pool.Exec(ctx, `UPDATE sync_profiles SET test_dry_at = now(), test_write_at = now() WHERE id = $1`, p.ID)
	must(t, err)

	// Nur Name, Takt und Höchstzahl: Zustand bleibt.
	g, err := f.s.GetProfile(ctx, p.ID)
	must(t, err)
	g.Name = "reset-neu"
	g.IntervalMinutes = 10
	g.MaxNew = 50
	must(t, f.s.SaveProfile(ctx, g, systemUser))
	g, err = f.s.GetProfile(ctx, p.ID)
	must(t, err)
	if g.State != "scharf" || g.TestDryAt == nil || g.TestWriteAt == nil || g.Name != "reset-neu" || g.IntervalMinutes != 10 {
		t.Fatalf("Zustand zu Unrecht verändert: %+v", g)
	}

	// Änderungen an Quelle/Ziel setzen zurück.
	mut := map[string]func(*Profile){
		"Tabelle":   func(p *Profile) { p.SourceTable = "Andere" },
		"Schlüssel": func(p *Profile) { p.KeyColumns = []string{"Id", "Nr"} },
		"Filter":    func(p *Profile) { p.FilterSQL = "Aktiv = 1" },
		"Fenster":   func(p *Profile) { p.WindowDays = 30 },
		"Vorgaben":  func(p *Profile) { p.Defaults = map[string]string{"priority": "high"} },
		"Ziel":      func(p *Profile) { p.TargetType = "fault"; p.Defaults = map[string]string{} },
		"Kommentar": func(p *Profile) {
			p.CommentTable = &CommentTable{Table: "K", FKColumn: "Id", TextColumn: "T"}
		},
		"Änderungsspalte": func(p *Profile) { p.ChangeColumn = "Andere" },
		"Anlagedatum":     func(p *Profile) { p.CreatedColumn = "Angelegt" },
	}
	for name, fn := range mut {
		g, err := f.s.GetProfile(ctx, p.ID)
		must(t, err)
		must(t, f.s.SetState(ctx, p.ID, "scharf", ""))
		_, err = f.pool.Exec(ctx, `UPDATE sync_profiles SET test_dry_at = now(), test_write_at = now() WHERE id = $1`, p.ID)
		must(t, err)
		g.State = "scharf"
		fn(g)
		must(t, f.s.SaveProfile(ctx, g, systemUser))
		h, err := f.s.GetProfile(ctx, p.ID)
		must(t, err)
		if h.State != "entwurf" || h.TestDryAt != nil || h.TestWriteAt != nil {
			t.Fatalf("%s: kein Zurücksetzen: %+v", name, h)
		}
		// zurück auf Ausgangswerte für den nächsten Fall
		h.SourceTable, h.KeyColumns, h.FilterSQL, h.WindowDays = "Tickets", []string{"Id"}, "", 0
		h.Defaults, h.TargetType, h.CommentTable, h.ChangeColumn = map[string]string{"priority": "medium"}, "ticket", nil, "Geaendert"
		h.CreatedColumn = ""
		must(t, f.s.SaveProfile(ctx, h, systemUser))
	}
}

func TestReserveLinkOnce(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "reserve")
	l1, c1, err := f.s.ReserveLink(ctx, p.ID, `["1"]`, "ticket")
	must(t, err)
	l2, c2, err := f.s.ReserveLink(ctx, p.ID, `["1"]`, "ticket")
	must(t, err)
	if !c1 || c2 || l1.ID != l2.ID {
		t.Fatalf("created %v/%v, IDs %s/%s", c1, c2, l1.ID, l2.ID)
	}
	if l1.RefID != nil {
		t.Fatalf("Link: %+v", l1)
	}

	// Gleichzeitig: genau einer legt an.
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := f.s.ReserveLink(ctx, p.ID, `["2"]`, "ticket")
			if err != nil {
				t.Error(err)
				return
			}
			if c {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("created = %d, erwartet 1", created)
	}
}

func TestLinkLifecycle(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "link")
	var ticketID string
	must(t, f.pool.QueryRow(ctx, `INSERT INTO tickets (title, created_by) VALUES ('extsync-link', $1) RETURNING id`, systemUser).Scan(&ticketID))
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM tickets WHERE id = $1`, ticketID) })

	l, _, err := f.s.ReserveLink(ctx, p.ID, `["9"]`, "ticket")
	must(t, err)
	orph, err := f.s.OrphanLinks(ctx, p.ID)
	must(t, err)
	if len(orph) != 1 || orph[0].ID != l.ID {
		t.Fatalf("Waisen: %+v", orph)
	}
	must(t, f.s.AttachLink(ctx, l.ID, ticketID, "m1", map[string]string{"title": "A"}))
	orph, _ = f.s.OrphanLinks(ctx, p.ID)
	if len(orph) != 0 {
		t.Fatal("Waise bleibt nach Anbinden")
	}
	must(t, f.s.UpdateLinkSeen(ctx, l.ID, "m2", map[string]string{"title": "B"}))
	must(t, f.s.UpdateLinkWritten(ctx, l.ID, map[string]string{"title": "C"}))
	must(t, f.s.SetLinkError(ctx, l.ID, "kaputt"))

	g, err := f.s.LinkByKey(ctx, p.ID, `["9"]`)
	must(t, err)
	if g.RefID == nil || *g.RefID != ticketID || g.SeenMark != "m2" || g.SeenValues["title"] != "B" ||
		g.WrittenValues["title"] != "C" || g.LastError != "kaputt" {
		t.Fatalf("Link: %+v", g)
	}
	if _, err := f.s.LinkByKey(ctx, p.ID, `["404"]`); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LinkByKey: %v", err)
	}
	bl, bp, err := f.s.LinkByRef(ctx, "ticket", ticketID)
	must(t, err)
	if bl.ID != l.ID || bp.ID != p.ID {
		t.Fatal("LinkByRef falsch")
	}
	all, err := f.s.LinksByRef(ctx, "ticket", ticketID)
	must(t, err)
	if len(all) != 1 {
		t.Fatalf("LinksByRef: %d", len(all))
	}
	if _, _, err := f.s.LinkByRef(ctx, "ticket", "00000000-0000-4000-8000-0000000000aa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LinkByRef fehlend: %v", err)
	}
}

// ids sammelt die Eintrags-IDs aus der Liste, die zu den gegebenen Verknüpfungen gehören.
func outboxFor(t *testing.T, s *Store, linkIDs ...string) []OutboxItem {
	t.Helper()
	items, err := s.NextOutbox(context.Background(), 1000)
	must(t, err)
	var out []OutboxItem
	for _, it := range items {
		for _, id := range linkIDs {
			if it.LinkID == id {
				out = append(out, it)
			}
		}
	}
	return out
}

func TestNextOutboxKeepsOrderPerLink(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "outbox")
	f.arm(t, p.ID)
	a, _, _ := f.s.ReserveLink(ctx, p.ID, `["a"]`, "ticket")
	b, _, _ := f.s.ReserveLink(ctx, p.ID, `["b"]`, "ticket")
	must(t, f.s.Enqueue(ctx, a.ID, "update", map[string]any{"fields": map[string]string{"title": "1"}}))
	must(t, f.s.Enqueue(ctx, a.ID, "comment", map[string]string{"text": "2"}))
	must(t, f.s.Enqueue(ctx, b.ID, "update", map[string]any{"fields": map[string]string{"title": "x"}}))

	got := outboxFor(t, f.s, a.ID, b.ID)
	if len(got) != 2 || got[0].LinkID != a.ID || got[1].LinkID != b.ID || got[0].Kind != "update" || got[0].ID >= got[1].ID {
		t.Fatalf("erste Runde: %+v", got)
	}
	if len(got[0].Payload) == 0 {
		t.Fatal("Nutzlast leer")
	}

	// Erster Eintrag von a schlägt fehl, neuer Versuch in der Zukunft.
	must(t, f.s.OutboxFailed(ctx, got[0].ID, "Zeitüberschreitung", time.Hour))
	got = outboxFor(t, f.s, a.ID, b.ID)
	if len(got) != 1 || got[0].LinkID != b.ID {
		t.Fatalf("a darf nicht überholen: %+v", got)
	}
	var attempts int
	var lastErr string
	must(t, f.pool.QueryRow(ctx, `SELECT attempts, last_error FROM sync_outbox WHERE link_id = $1 ORDER BY id LIMIT 1`, a.ID).Scan(&attempts, &lastErr))
	if attempts != 1 || lastErr != "Zeitüberschreitung" {
		t.Fatalf("attempts=%d err=%q", attempts, lastErr)
	}
	n, err := f.s.OpenOutboxCount(ctx, p.ID)
	must(t, err)
	if n != 3 {
		t.Fatalf("offen = %d", n)
	}

	// Sofort wieder versuchen: erster Eintrag von a kommt, der zweite noch nicht.
	var firstA int64
	must(t, f.pool.QueryRow(ctx, `SELECT id FROM sync_outbox WHERE link_id = $1 ORDER BY id LIMIT 1`, a.ID).Scan(&firstA))
	must(t, f.s.OutboxRetryNow(ctx, firstA))
	got = outboxFor(t, f.s, a.ID)
	if len(got) != 1 || got[0].ID != firstA {
		t.Fatalf("nach RetryNow: %+v", got)
	}
	must(t, f.s.OutboxDone(ctx, firstA))
	got = outboxFor(t, f.s, a.ID)
	if len(got) != 1 || got[0].Kind != "comment" {
		t.Fatalf("nach Done: %+v", got)
	}
	n, _ = f.s.OpenOutboxCount(ctx, p.ID)
	if n != 2 {
		t.Fatalf("offen nach Done = %d", n)
	}
}

func TestNextOutboxOnlyArmed(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "armed")
	l, _, _ := f.s.ReserveLink(ctx, p.ID, `["a"]`, "ticket")
	must(t, f.s.Enqueue(ctx, l.ID, "update", map[string]any{"fields": map[string]string{}}))
	for _, st := range []string{"entwurf", "getestet", "angehalten"} {
		must(t, f.s.SetState(ctx, p.ID, st, ""))
		if got := outboxFor(t, f.s, l.ID); len(got) != 0 {
			t.Fatalf("Zustand %s liefert Einträge", st)
		}
	}
	f.arm(t, p.ID)
	if got := outboxFor(t, f.s, l.ID); len(got) != 1 {
		t.Fatalf("scharf liefert %d", len(got))
	}
}

func TestDemoteConnection(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p1 := f.profile(t, "demote1")
	p2 := f.profile(t, "demote2")
	f.arm(t, p1.ID)
	must(t, f.s.SetState(ctx, p2.ID, "getestet", ""))
	_, err := f.pool.Exec(ctx, `UPDATE sync_profiles SET test_dry_at = now(), test_write_at = now() WHERE id = ANY($1)`, []string{p1.ID, p2.ID})
	must(t, err)
	n, err := f.s.DemoteConnection(ctx, f.connID)
	must(t, err)
	if n != 1 {
		t.Fatalf("zurückgestuft: %d", n)
	}
	g, _ := f.s.GetProfile(ctx, p1.ID)
	if g.State != "getestet" || g.StateNote != "Weg der Verbindung geändert" || g.TestDryAt != nil || g.TestWriteAt != nil {
		t.Fatalf("p1: %+v", g)
	}
	h, _ := f.s.GetProfile(ctx, p2.ID)
	if h.State != "getestet" || h.TestDryAt == nil {
		t.Fatalf("p2 darf nicht berührt werden: %+v", h)
	}
}

func TestMapsAndRuns(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "maps")
	f.arm(t, p.ID)
	fixed := "high"
	must(t, f.s.ReplaceMaps(ctx, p.ID, []FieldMap{
		{ForeignColumn: "Titel", PDHField: "title", Direction: "beides", Position: 0},
		{PDHField: "priority", Direction: "herein", Translate: map[string]string{"1": "low"}, FixedValue: &fixed, Position: 1},
	}))
	g, _ := f.s.GetProfile(ctx, p.ID)
	if g.State != "entwurf" {
		t.Fatalf("ReplaceMaps setzt nicht zurück: %s", g.State)
	}
	maps, err := f.s.ListMaps(ctx, p.ID)
	must(t, err)
	if len(maps) != 2 || maps[0].PDHField != "title" || maps[1].Translate["1"] != "low" || maps[1].FixedValue == nil || *maps[1].FixedValue != "high" {
		t.Fatalf("Zuordnung: %+v", maps)
	}
	must(t, f.s.ReplaceMaps(ctx, p.ID, nil))
	if maps, _ = f.s.ListMaps(ctx, p.ID); len(maps) != 0 {
		t.Fatal("nicht geleert")
	}

	id, err := f.s.StartRun(ctx, p.ID, "herein")
	must(t, err)
	must(t, f.s.FinishRun(ctx, &Run{ID: id, New: 2, Changed: 1, Written: 3, Failed: 1, SkippedReason: "x",
		Errors: []RunError{{ForeignKey: `["1"]`, Field: "title", Message: "zu lang"}}}))
	runs, err := f.s.ListRuns(ctx, p.ID, 10)
	must(t, err)
	if len(runs) != 1 || runs[0].FinishedAt == nil || runs[0].New != 2 || runs[0].Written != 3 ||
		len(runs[0].Errors) != 1 || runs[0].Errors[0].Message != "zu lang" {
		t.Fatalf("Lauf: %+v", runs)
	}
}

func TestDueProfilesAndSchedule(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	p := f.profile(t, "due")
	has := func() bool {
		ps, err := f.s.DueProfiles(ctx)
		must(t, err)
		for _, x := range ps {
			if x.ID == p.ID {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("Entwurf ist nicht fällig")
	}
	f.arm(t, p.ID)
	if !has() {
		t.Fatal("scharf ohne next_run_at ist fällig")
	}
	must(t, f.s.SetSchedule(ctx, p.ID, 2, time.Now().Add(time.Hour), "cur"))
	if has() {
		t.Fatal("Zukunft ist nicht fällig")
	}
	g, _ := f.s.GetProfile(ctx, p.ID)
	if g.FailCount != 2 || g.Cursor != "cur" || g.NextRunAt == nil {
		t.Fatalf("Plan: %+v", g)
	}
	must(t, f.s.SetSchedule(ctx, p.ID, 0, time.Now().Add(-time.Minute), "cur"))
	if !has() {
		t.Fatal("Vergangenheit ist fällig")
	}
}
