package extsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests der Fix-Runde 1 zu Task 8b: fester Stichtag je Profil (R24),
// vollständige Zählung im Probelauf, n_archived (R25).

func bt(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, bfBerlin)
}

// at stellt die Uhr des Laufs.
func (f *inFix) at(now time.Time) { f.in.Now = func() time.Time { return now } }

// armAt schaltet das Profil am Tag von now scharf (Stichtag nur beim ersten Mal).
func (f *inFix) armAt(now time.Time) {
	f.t.Helper()
	must(f.t, f.s.Arm(context.Background(), f.p.ID, DayStart(now, bfBerlin)))
	f.reload()
}

func TestArmSetsNotifyFromOnce(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	ctx := context.Background()
	d1 := DayStart(bt(2026, 11, 12, 10, 0), bfBerlin)
	f.armAt(bt(2026, 11, 12, 10, 0))
	if f.p.State != StateArmed || f.p.NotifyFrom == nil || !f.p.NotifyFrom.Equal(d1) {
		t.Fatalf("nach dem ersten Scharfschalten: %s %v", f.p.State, f.p.NotifyFrom)
	}
	// Zurück auf Entwurf und erneut scharf: der Stichtag bleibt.
	must(t, f.s.SetState(ctx, f.p.ID, StateDraft, ""))
	f.armAt(bt(2026, 11, 14, 9, 0))
	if f.p.State != StateArmed || !f.p.NotifyFrom.Equal(d1) {
		t.Fatalf("nach erneutem Scharfschalten: %s %v", f.p.State, f.p.NotifyFrom)
	}
	// Andere Anlagedatum-Spalte (setzt auf Entwurf zurück): Stichtag bleibt.
	f.p.CreatedColumn = "Faellig"
	must(t, f.s.SaveProfile(ctx, f.p, systemUser))
	f.reload()
	if f.p.State != StateDraft || f.p.NotifyFrom == nil || !f.p.NotifyFrom.Equal(d1) {
		t.Fatalf("nach Änderung der Spalte: %s %v", f.p.State, f.p.NotifyFrom)
	}
	f.p.CreatedColumn = "Angelegt"
	must(t, f.s.SaveProfile(ctx, f.p, systemUser))
	f.armAt(bt(2026, 11, 15, 9, 0))
	if !f.p.NotifyFrom.Equal(d1) {
		t.Fatalf("Stichtag verschoben: %v", f.p.NotifyFrom)
	}
	// Eine Meldung vom 13.11. (nach dem ersten, vor dem letzten Scharfschalten) bleibt neu.
	f.at(bt(2026, 11, 16, 8, 0))
	f.rowAt(1, "Seit Start", "hoch", "offen", bt(2026, 11, 13, 7, 0))
	if r := f.run(RunOptions{}); r.New != 1 || r.Backfilled != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	if err := f.s.Arm(ctx, "00000000-0000-4000-8000-0000000000aa", d1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbekanntes Profil: %v", err)
	}
}

func TestBackfillCutoffIsArmingDay(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.armAt(bt(2026, 11, 12, 10, 0))
	// Kurz nach Mitternacht: die Meldung von gestern 23:58 ist neu (seit dem Scharfschalten).
	f.at(bt(2026, 11, 13, 0, 3))
	f.rowAt(1, "Spät", "hoch", "offen", bt(2026, 11, 12, 23, 58))
	f.rowAt(2, "Vorher", "hoch", "offen", bt(2026, 11, 11, 10, 0))
	r := f.run(RunOptions{})
	if r.New != 1 || r.Backfilled != 1 {
		t.Fatalf("Lauf: %+v", r)
	}
	id1, _, _, _, _ := f.rec(k(1))
	id2, _, _, _, _ := f.rec(k(2))
	if f.queue(id1, "created") != 1 || f.queue(id2, "") != 0 {
		t.Fatalf("Benachrichtigungen: %d / %d", f.queue(id1, "created"), f.queue(id2, ""))
	}
	// Viel später: vor dem Stichtag bleibt still, danach ist neu.
	f.at(bt(2027, 1, 5, 9, 0))
	f.rowAt(3, "Uralt", "hoch", "offen", bt(2026, 11, 11, 23, 59))
	f.rowAt(4, "Gestern", "hoch", "offen", bt(2027, 1, 4, 9, 0))
	r = f.run(RunOptions{})
	if r.New != 1 || r.Backfilled != 1 {
		t.Fatalf("späterer Lauf: %+v", r)
	}
	id4, _, _, _, _ := f.rec(k(4))
	if f.queue(id4, "created") != 1 {
		t.Fatal("Meldung nach dem Stichtag ohne Benachrichtigung")
	}
}

func TestBackfillOutageDayRowsAreNew(t *testing.T) {
	f := newBackfillFix(t, fixOpt{maxNew: 2})
	f.armAt(bt(2026, 11, 10, 8, 0))
	// Ausfall am 11. und 12.: erst am 13. läuft der Abgleich wieder.
	f.at(bt(2026, 11, 13, 8, 0))
	f.rowAt(1, "Ausfall 1", "hoch", "offen", bt(2026, 11, 11, 9, 0))
	f.rowAt(2, "Ausfall 2", "hoch", "offen", bt(2026, 11, 12, 9, 0))
	r := f.run(RunOptions{})
	if r.New != 2 || r.Backfilled != 0 {
		t.Fatalf("Lauf: %+v", r)
	}
	id, _, _, _, _ := f.rec(k(1))
	if f.queue(id, "created") != 1 {
		t.Fatal("Meldung des Ausfalltags ohne Benachrichtigung")
	}
	// Sie zählen für die Obergrenze.
	for i := int64(3); i <= 5; i++ {
		f.rowAt(i, fmt.Sprintf("Ausfall %d", i), "hoch", "offen", bt(2026, 11, 12, 10, 0))
	}
	r = f.run(RunOptions{})
	if !strings.Contains(r.SkippedReason, "Zu viele neue Meldungen") || r.New != 0 || r.Backfilled != 0 {
		t.Fatalf("Obergrenze: %+v", r)
	}
}

func TestBackfillPausedThenResumedStaysNew(t *testing.T) {
	f := newBackfillFix(t, fixOpt{maxNew: 2})
	ctx := context.Background()
	f.armAt(bt(2026, 11, 11, 7, 0))
	f.at(bt(2026, 11, 11, 12, 0))
	for i := int64(1); i <= 3; i++ {
		f.rowAt(i, fmt.Sprintf("Viele %d", i), "hoch", "offen", bt(2026, 11, 11, 9, 0))
	}
	if r := f.run(RunOptions{}); !strings.Contains(r.SkippedReason, "Zu viele neue Meldungen") {
		t.Fatalf("Tag 1: %+v", r)
	}
	// Tag 2, wieder scharf: weiterhin neu, die Obergrenze greift erneut.
	f.at(bt(2026, 11, 12, 9, 0))
	f.armAt(bt(2026, 11, 12, 9, 0))
	if r := f.run(RunOptions{}); !strings.Contains(r.SkippedReason, "Zu viele neue Meldungen") || r.Backfilled != 0 {
		t.Fatalf("Tag 2: %+v", r)
	}
	// Obergrenze erhöht: alle drei werden mit Benachrichtigung angelegt.
	f.p.MaxNew = 5
	must(t, f.s.SaveProfile(ctx, f.p, systemUser))
	f.reload()
	r := f.run(RunOptions{})
	if r.New != 3 || r.Backfilled != 0 {
		t.Fatalf("nach Erhöhung: %+v", r)
	}
	for i := 1; i <= 3; i++ {
		id, _, _, _, _ := f.rec(k(i))
		if f.queue(id, "created") != 1 {
			t.Fatalf("Zeile %d ohne Benachrichtigung", i)
		}
	}
}

func TestBackfillFailedNewRetriedLaterNotifies(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.armAt(bt(2026, 11, 11, 7, 0))
	f.at(bt(2026, 11, 11, 12, 0))
	f.rowAt(1, "Scheitert", "hoch", "offen", bt(2026, 11, 11, 10, 0))
	real := f.in.Targets[RefTicket]
	f.in.Targets[RefTicket] = &failingTarget{Target: real, failTitle: f.title("Scheitert")}
	if r := f.run(RunOptions{}); r.Failed != 1 {
		t.Fatalf("Tag 1: %+v", r)
	}
	f.in.Targets[RefTicket] = real
	f.at(bt(2026, 11, 13, 9, 0))
	r := f.run(RunOptions{})
	if r.New != 1 || r.Backfilled != 0 || r.Failed != 0 {
		t.Fatalf("Tag 3: %+v", r)
	}
	id, _, _, _, _ := f.rec(k(1))
	if f.queue(id, "created") != 1 {
		t.Fatal("Wiederholung ohne Benachrichtigung")
	}
}

func TestDryRunBeforeArmingUsesToday(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	if f.p.NotifyFrom != nil {
		t.Fatal("Stichtag vor dem Scharfschalten gesetzt")
	}
	f.rowAt(1, "Heute", "hoch", "offen", bfToday)
	f.rowAt(2, "Gestern", "hoch", "offen", bfYesterday)
	r, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: true})
	must(t, err)
	if r.New != 1 || r.Backfilled != 1 {
		t.Fatalf("Probelauf: %+v", r)
	}
	g, err := f.s.GetProfile(context.Background(), f.p.ID)
	must(t, err)
	if g.NotifyFrom != nil {
		t.Fatal("Probelauf hat einen Stichtag gespeichert")
	}
}

// ── I2: Probelauf zählt vollständig ──────────────────────────────────────

func TestDryRunCountsAllRows(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.in.pageSize = 100
	for i := int64(1); i <= 300; i++ {
		st := "offen"
		if i <= 120 {
			st = "erledigt"
		}
		f.rowAt(i, fmt.Sprintf("Alt %d", i), "mittel", st, bfLastMonth)
	}
	for i := int64(301); i <= 450; i++ {
		f.rowAt(i, fmt.Sprintf("Neu %d", i), "mittel", "offen", bfToday)
	}
	r, prev, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: true})
	must(t, err)
	if r.New != 150 || r.Backfilled != 300 || r.Archived != 120 || r.Failed != 0 || r.CountTruncated {
		t.Fatalf("Probelauf: New %d Backfilled %d Archived %d Failed %d gekürzt %v", r.New, r.Backfilled, r.Archived, r.Failed, r.CountTruncated)
	}
	if len(prev) == 0 || len(prev) > defaultPageSize {
		t.Fatalf("%d Vorschauzeilen", len(prev))
	}
	if f.links() != 0 || f.records() != 0 {
		t.Fatal("Probelauf hat geschrieben")
	}
}

func TestDryRunCountCapSetsFlag(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.in.pageSize = 40
	f.in.dryCountCap = 100
	for i := int64(1); i <= 300; i++ {
		f.rowAt(i, fmt.Sprintf("Alt %d", i), "mittel", "offen", bfLastMonth)
	}
	r, _, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: true})
	must(t, err)
	if !r.CountTruncated || r.Backfilled >= 300 || r.Backfilled < 100 {
		t.Fatalf("Probelauf: Backfilled %d gekürzt %v", r.Backfilled, r.CountTruncated)
	}
	// Abgebrochener Kontext: Ergebnis bis dahin, Kennzeichen gesetzt, kein Fehler.
	ctx, cancel := context.WithCancel(context.Background())
	f.in.dryCountCap = 0
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	f.ff.gate, f.ff.entered = gate, entered
	go func() { <-entered; cancel(); close(gate) }()
	r, _, err = f.in.Run(ctx, f.p, f.maps, f.ff, f.d, RunOptions{Dry: true})
	if err != nil || !r.CountTruncated {
		t.Fatalf("abgebrochen: %v, %+v", err, r)
	}
}

func TestDryRunOverloadReadsOnePage(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	for i := int64(1); i <= 80; i++ {
		f.rowAt(i, fmt.Sprintf("Alt %d", i), "mittel", "offen", bfLastMonth)
	}
	q := f.ff.queryCount()
	r, prev, err := f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: true, Limit: 50})
	must(t, err)
	if f.ff.queryCount()-q != 1 || r.Backfilled != 50 || !r.CountTruncated || len(prev) != 50 {
		t.Fatalf("Überlastung: %d Abfragen, %+v, %d Vorschau", f.ff.queryCount()-q, r, len(prev))
	}
	r, _, err = f.in.Run(context.Background(), f.p, f.maps, f.ff, f.d, RunOptions{Dry: true, Limit: 100})
	must(t, err)
	if r.Backfilled != 80 || r.CountTruncated {
		t.Fatalf("Grenze 100: %+v", r)
	}
}

// ── R25: n_archived ──────────────────────────────────────────────────────

func TestRunArchivedPersisted(t *testing.T) {
	f := newBackfillFix(t, fixOpt{})
	f.rowAt(1, "Zu", "hoch", "erledigt", bfLastMonth)
	f.rowAt(2, "Offen", "hoch", "offen", bfLastMonth)
	f.run(RunOptions{})
	runs, err := f.s.ListRuns(context.Background(), f.p.ID, 1)
	must(t, err)
	if runs[0].Archived != 1 || runs[0].Backfilled != 2 {
		t.Fatalf("gespeicherter Lauf: %+v", runs[0])
	}
}
