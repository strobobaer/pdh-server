package extsync

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests zu R26 (Spec 4.11): Zeitzone des Fremdsystems je Profil.

// wallUTC: so liefert der Dialekt einen gespeicherten datetime-Wert (Wanduhr als UTC beschriftet).
func wallUTC(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

func (f *inFix) createdAt(id string) time.Time {
	f.t.Helper()
	var c time.Time
	must(f.t, f.sf.pool.QueryRow(context.Background(), `SELECT created_at FROM `+f.tbl()+` WHERE id = $1`, id).Scan(&c))
	return c
}

func TestTimeZoneCreatedDate(t *testing.T) {
	wall := wallUTC(2026, 11, 12, 23, 30)
	cases := []struct {
		name, zone   string
		armDay       time.Time
		backfill     bool
		wantCreated  time.Time
		checkCreated bool
	}{
		// 12.11. 23:30 Berliner Zeit = 22:30 UTC: vor dem Stichtag 13.11. (23:00 UTC).
		{"Berlin", "Europe/Berlin", bt(2026, 11, 13, 8, 0), true, wallUTC(2026, 11, 12, 22, 30), true},
		// Ortszeit des Servers (Inbound.Location = Berlin): wie Berlin.
		{"Server", "server", bt(2026, 11, 13, 8, 0), true, wallUTC(2026, 11, 12, 22, 30), true},
		// Als UTC gemeint: 23:30 UTC ist in Berlin schon der 13.11. – neu.
		{"UTC neu", "UTC", bt(2026, 11, 13, 8, 0), false, time.Time{}, false},
		// Als UTC gemeint, Stichtag 14.11.: rückwirkend mit Anlagezeit 23:30 UTC.
		{"UTC rückwirkend", "UTC", bt(2026, 11, 14, 8, 0), true, wallUTC(2026, 11, 12, 23, 30), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{timeZone: c.zone})
			f.armAt(c.armDay)
			f.at(c.armDay.Add(time.Hour))
			f.rowAt(1, "Spät", "hoch", "offen", wall)
			r := f.run(RunOptions{})
			if c.backfill && (r.Backfilled != 1 || r.New != 0) || !c.backfill && (r.New != 1 || r.Backfilled != 0) {
				t.Fatalf("Lauf: %+v", r)
			}
			id, _, _, _, _ := f.rec(k(1))
			if c.checkCreated && !f.createdAt(id).Equal(c.wantCreated) {
				t.Fatalf("created_at %v, erwartet %v", f.createdAt(id).UTC(), c.wantCreated)
			}
		})
	}
}

func dueMaps(target string) []FieldMap {
	return append(stdMaps(target), FieldMap{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirIn, Position: 4})
}

func TestTimeZoneDueDate(t *testing.T) {
	f := newInFix(t, fixOpt{timeZone: "Europe/Berlin", maps: dueMaps(RefTicket)})
	wall := wallUTC(2027, 3, 4, 10, 0) // Winterzeit: 10:00 Berlin = 09:00 UTC
	f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Termin"), "Prio": "hoch", "Zustand": "offen", "Faellig": wall})
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	d := f.due("tickets", id)
	if d == nil || !d.Equal(wallUTC(2027, 3, 4, 9, 0)) {
		t.Fatalf("Fälligkeit %v", d)
	}
	if l := f.link(k(1)); l.SeenValues["due_date"] != "2027-03-04T09:00:00Z" {
		t.Fatalf("gesehen: %+v", l.SeenValues)
	}
	// Rückweg für das Zurückschreiben: wieder die Wanduhr des Fremdsystems.
	if back := InstantToForeignWall(*d, mustLoc(t, "Europe/Berlin")); !back.Equal(wall) {
		t.Fatalf("Rückweg %v", back)
	}
	// Unverändert gelesen: keine Änderung, kein Verlauf.
	f.p.Cursor = ""
	if r := f.run(RunOptions{}); r.Changed != 0 || len(f.history(id, "sync")) != 0 {
		t.Fatalf("zweiter Lauf: %+v", r)
	}
}

// R28: Eine Anlagedatum-Spalte vom Typ date entscheidet "rückwirkend" weiter als
// Kalendertag (unabhängig von der Zone); die Anlagezeit ist Mitternacht dieses
// Tages am Ort des Fremdsystems (vorher: am Ort des Servers).
func TestTimeZoneDateCreatedColumnMidnightInZone(t *testing.T) {
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Tokyo"} {
		t.Run(zone, func(t *testing.T) {
			f := newBackfillFix(t, fixOpt{info: backfillInfo("date"), timeZone: zone})
			f.armAt(bt(2026, 11, 13, 8, 0))
			f.at(bt(2026, 11, 13, 9, 0))
			f.rowAt(1, "Datum heute", "hoch", "offen", wallUTC(2026, 11, 13, 0, 0))
			f.rowAt(2, "Datum gestern", "hoch", "offen", wallUTC(2026, 11, 12, 0, 0))
			r := f.run(RunOptions{})
			if r.New != 1 || r.Backfilled != 1 {
				t.Fatalf("Lauf: %+v", r)
			}
			id2, _, _, _, _ := f.rec(k(2))
			want := time.Date(2026, 11, 12, 0, 0, 0, 0, mustLoc(t, zone))
			if c := f.createdAt(id2); !c.Equal(want) {
				t.Fatalf("created_at %v, erwartet %v", c.UTC(), want.UTC())
			}
		})
	}
}

// dateDueFix: Profil mit Datumsspalte "FaelligTag" (date) für due_date, beides.
func dateDueFix(t *testing.T, zone string) *outFix {
	info := fakeMeldungenInfo(true)
	info.Columns = append(info.Columns, Column{Name: "FaelligTag", Type: "date", Nullable: true})
	maps := append(stdMaps(RefTicket), FieldMap{ForeignColumn: "FaelligTag", PDHField: "due_date", Direction: DirBoth, Position: 4})
	return newOutFix(t, fixOpt{info: info, maps: maps, timeZone: zone})
}

// R28: date-Spalte eines Zeitfelds = Mitternacht am Ort des Fremdsystems; das
// Zurückschreiben liefert denselben Kalendertag; hin und zurück ist ein Echo.
func TestTimeZoneDateColumnMidnightInZone(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			loc := mustLoc(t, zone)
			f := dateDueFix(t, zone)
			f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Tag"), "Prio": "hoch", "Zustand": "offen",
				"FaelligTag": wallUTC(2026, 11, 20, 0, 0)})
			f.run(RunOptions{})
			id, _, _, _, _ := f.rec(k(1))
			if d := f.due("tickets", id); d == nil || !d.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, loc)) {
				t.Fatalf("Fälligkeit %v, erwartet Mitternacht in %s", d, zone)
			}
			// PDH ändert auf den 25.11. (Mitternacht am Ort): hinaus geht der 25.11.
			f.userUpdate(id, "due_date = $2", time.Date(2026, 11, 25, 0, 0, 0, 0, loc))
			f.processAll(true)
			ups := f.updates()
			if len(ups) != 1 || !ups[0].set["FaelligTag"].(time.Time).Equal(wallUTC(2026, 11, 25, 0, 0)) {
				t.Fatalf("Befehle %+v", ups)
			}
			before := f.outbox()
			r := f.run(RunOptions{})
			if r.Changed != 0 || r.Failed != 0 || len(f.history(id, "sync")) != 0 || f.outbox() != before {
				t.Fatalf("kein Echo: %+v, Verlauf %+v", r, f.history(id, "sync"))
			}
			if d := f.due("tickets", id); !d.Equal(time.Date(2026, 11, 25, 0, 0, 0, 0, loc)) {
				t.Fatalf("Fälligkeit nach Echo %v", d)
			}
			if l := f.link(k(1)); len(l.WrittenValues) != 0 {
				t.Fatalf("written nicht aufgeräumt: %+v", l.WrittenValues)
			}
		})
	}
}

// R28: Verknüpfungen aus der Zeit vor der Regel haben Mitternacht UTC gesehen.
// Ohne Benutzeränderung wird einmal übernommen (richtiger Zeitpunkt), ohne
// Widerspruch und ohne Wartelisteneintrag; danach ist Ruhe.
func TestTimeZoneDateColumnOldSeenAppliedOnce(t *testing.T) {
	f := dateDueFix(t, "Europe/Berlin")
	f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Alt"), "Prio": "hoch", "Zustand": "offen",
		"FaelligTag": wallUTC(2026, 11, 20, 0, 0)})
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	// Stand wie nach der alten Regel: PDH und gesehen = Mitternacht UTC, Marke leer.
	f.pdhUpdate(id, "due_date = $2", wallUTC(2026, 11, 20, 0, 0))
	l := f.link(k(1))
	l.SeenValues["due_date"] = "2026-11-20T00:00:00Z"
	empty := ""
	must(t, f.s.SaveInbound(context.Background(), l.ID, InboundUpdate{SeenMark: &empty, Seen: l.SeenValues}))
	f.p.Cursor = ""
	r := f.run(RunOptions{})
	if r.Changed != 1 || r.Failed != 0 {
		t.Fatalf("Lauf %+v", r)
	}
	if d := f.due("tickets", id); !d.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, mustLoc(t, "Europe/Berlin"))) {
		t.Fatalf("Fälligkeit %v", d)
	}
	h := f.history(id, "sync")
	if len(h) != 1 || h[0].field != "due_date" || strings.Contains(h[0].msg, "nicht übernommen") {
		t.Fatalf("Verlauf %+v", h)
	}
	if n := f.outbox(); n != 0 {
		t.Fatalf("%d Wartelisteneinträge", n)
	}
	f.p.Cursor = ""
	if r := f.run(RunOptions{}); r.Changed != 0 {
		t.Fatalf("zweiter Lauf %+v", r)
	}
}

func TestTimeZoneKeepsCursorAndMark(t *testing.T) {
	info := &TableInfo{Schema: "dbo", Name: "MeldungenZeit", Columns: []Column{
		{Name: "Nr", Type: "int", IsKey: true},
		{Name: "Titel", Type: "nvarchar"},
		{Name: "Faellig", Type: "datetime2", Nullable: true},
		{Name: "Geaendert", Type: "datetime2"},
	}}
	f := newInFix(t, fixOpt{kind: changeKindTimestamp, changeCol: "Geaendert", info: info, timeZone: "Europe/Berlin",
		maps: []FieldMap{
			{ForeignColumn: "Titel", PDHField: "title", Direction: DirIn},
			{ForeignColumn: "Faellig", PDHField: "due_date", Direction: DirIn, Position: 1},
		}})
	f.in.pageSize = 2
	t0 := time.Date(2026, 11, 1, 8, 0, 0, 123456700, time.UTC)
	raw := map[int64]time.Time{}
	for i := int64(1); i <= 5; i++ {
		ts := t0
		if i > 3 {
			ts = t0.Add(time.Duration(i) * time.Second)
		}
		raw[i] = ts
		f.ff.put(map[string]any{"Nr": i, "Titel": f.title(fmt.Sprintf("Zeit %d", i)), "Geaendert": ts, "Faellig": wallUTC(2027, 1, 2, 10, 0)})
	}
	r := f.run(RunOptions{})
	if r.New != 5 || r.Cursor != f.cursorAfter(5) {
		t.Fatalf("erster Lauf: %+v", r)
	}
	q := f.ff.queryCount()
	if r = f.run(RunOptions{}); r.New != 0 || r.Changed != 0 || f.ff.queryCount()-q != 1 {
		t.Fatalf("zweiter Lauf: %+v, %d Abfragen", r, f.ff.queryCount()-q)
	}
	for i, ts := range raw {
		l := f.link(k(int(i)))
		if want := RowMark(changeKindTimestamp, ts, nil); l.SeenMark != want {
			t.Fatalf("Zeile %d: Marke %q, erwartet roher Wert %q", i, l.SeenMark, want)
		}
		if l.SeenValues["due_date"] != "2027-01-02T09:00:00Z" {
			t.Fatalf("Zeile %d: gesehen %+v", i, l.SeenValues)
		}
	}
}

func TestTimeZoneChangeShiftsOnce(t *testing.T) {
	f := newInFix(t, fixOpt{maps: dueMaps(RefTicket)}) // zunächst UTC
	ctx := context.Background()
	f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Termin"), "Prio": "hoch", "Zustand": "offen",
		"Faellig": wallUTC(2027, 3, 4, 10, 0)})
	f.run(RunOptions{})
	id, _, _, _, _ := f.rec(k(1))
	if d := f.due("tickets", id); d == nil || !d.Equal(wallUTC(2027, 3, 4, 10, 0)) {
		t.Fatalf("Fälligkeit %v", d)
	}
	// Einstellung berichtigt: Profil wird Entwurf, jede Zeile wird einmal neu verglichen.
	f.p.TimeZone = "Europe/Berlin"
	must(t, f.s.SaveProfile(ctx, f.p, systemUser))
	f.reload()
	f.p.Cursor = ""
	if f.p.State != StateDraft {
		t.Fatalf("Zustand %q", f.p.State)
	}
	r := f.run(RunOptions{})
	if r.Changed != 1 || r.Failed != 0 {
		t.Fatalf("Lauf nach Umstellung: %+v", r)
	}
	if d := f.due("tickets", id); d == nil || !d.Equal(wallUTC(2027, 3, 4, 9, 0)) {
		t.Fatalf("Fälligkeit nach Umstellung %v", d)
	}
	h := f.history(id, "sync")
	if len(h) != 1 || h[0].field != "due_date" {
		t.Fatalf("Verlauf: %+v", h)
	}
	if f.outbox() != 0 || f.conflictNotes(id) != 0 {
		t.Fatalf("Warteliste %d, Widersprüche %d", f.outbox(), f.conflictNotes(id))
	}
	f.p.Cursor = ""
	if r = f.run(RunOptions{}); r.Changed != 0 {
		t.Fatalf("erneut verschoben: %+v", r)
	}
}

// datetimeoffset trägt seine Zone selbst: der Treiber liefert den richtigen
// Zeitpunkt, die Zeitzone des Profils darf ihn nicht verschieben.
func TestTimeZoneDatetimeOffsetUntouched(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	info := fakeMeldungenInfo(true)
	info.Columns = append(info.Columns, Column{Name: "FaelligDTO", Type: "datetimeoffset", Nullable: true})
	maps := append(stdMaps(RefTicket), FieldMap{ForeignColumn: "FaelligDTO", PDHField: "due_date", Direction: DirIn, Position: 4})
	// 10:00 bei +01:00 = 09:00 UTC.
	inst := time.Date(2027, 3, 4, 10, 0, 0, 0, time.FixedZone("", 3600))
	for _, c := range []struct{ name, zone string }{{"Berlin", "Europe/Berlin"}, {"Server", "server"}} {
		t.Run(c.name, func(t *testing.T) {
			f := newInFix(t, fixOpt{info: info, maps: maps, timeZone: c.zone})
			f.in.Location = berlin // Ortszeit des Servers nicht UTC
			f.ff.put(map[string]any{"Nr": int64(1), "Titel": f.title("Offset"), "Prio": "hoch", "Zustand": "offen", "FaelligDTO": inst})
			f.run(RunOptions{})
			id, _, _, _, _ := f.rec(k(1))
			if d := f.due("tickets", id); d == nil || !d.Equal(inst) {
				t.Fatalf("Fälligkeit %v, erwartet %v", d, inst.UTC())
			}
			if l := f.link(k(1)); l.SeenValues["due_date"] != "2027-03-04T09:00:00Z" {
				t.Fatalf("gesehen: %+v", l.SeenValues)
			}
		})
	}
}

func TestTimeZoneUnknownFailsRun(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Eins", "hoch", "offen", bfYesterday)
	f.p.TimeZone = "Mars/Olymp"
	for _, dry := range []bool{false, true} {
		_, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: dry})
		if err == nil || !strings.Contains(err.Error(), "Zeitzone ‚Mars/Olymp‘ ist unbekannt") {
			t.Fatalf("Probelauf %v: %v", dry, err)
		}
	}
	if f.links() != 0 || f.records() != 0 {
		t.Fatal("trotz unbekannter Zeitzone angelegt")
	}
}
