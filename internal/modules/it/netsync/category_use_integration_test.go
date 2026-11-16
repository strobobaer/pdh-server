package netsync

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// mkCategory legt eine eigene Kategorie an (Schlüssel eindeutig) und räumt sie
// nach den Assets des Tests wieder weg.
func (f *fx) mkCategory(monitor, active bool, grace *int) string {
	f.t.Helper()
	key := fmt.Sprintf("t-%d", rand.Int63())
	ctx := context.Background()
	_, err := f.s.DB.Exec(ctx, `INSERT INTO it_asset_categories (key, name, active, monitor_default, grace_minutes)
		VALUES ($1, $1, $2, $3, $4)`, key, active, monitor, grace)
	must(f.t, err)
	f.t.Cleanup(func() {
		// Assets zuerst (Fremdschlüssel), dann die Kategorie.
		_, _ = f.s.DB.Exec(context.Background(), `DELETE FROM it_assets WHERE type = $1`, key)
		_, _ = f.s.DB.Exec(context.Background(), `DELETE FROM it_asset_categories WHERE key = $1`, key)
	})
	return key
}

func TestAdoptUsesCategoryMonitorDefault(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	off := f.mkCategory(false, true, nil)
	on := f.mkCategory(true, true, nil)
	_, err := f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{
		{ExternalID: "a", MAC: "aa:bb:cc:00:30:01", Name: "A", Kind: "wired"},
		{ExternalID: "b", MAC: "aa:bb:cc:00:30:02", Name: "B", Kind: "wired"},
		{ExternalID: "c", MAC: "aa:bb:cc:00:30:03", Name: "C", Kind: "wired"},
	}}, t0)
	must(t, err)
	// Vorhandenes Asset ohne Quelle gleicher MAC: Verknüpfungszweig.
	_, err = f.s.DB.Exec(ctx, `INSERT INTO it_assets (name, mac_address, created_by) VALUES ($1, 'AA-BB-CC-00-30-03', $2)`, "hand-"+f.src, f.user)
	must(t, err)

	monitored := func(id string) bool {
		var m bool
		must(t, f.s.DB.QueryRow(ctx, `SELECT net_monitored FROM it_assets WHERE id = $1::uuid`, id).Scan(&m))
		return m
	}
	ida, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:30:01", off, f.user, t0)
	must(t, err)
	idb, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:30:02", on, f.user, t0)
	must(t, err)
	idc, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:30:03", off, f.user, t0)
	must(t, err)
	if monitored(ida) || !monitored(idb) || monitored(idc) {
		t.Errorf("überwacht: neu/aus=%v neu/an=%v verknüpft/aus=%v", monitored(ida), monitored(idb), monitored(idc))
	}
}

func TestAdoptRejectsBadCategory(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	hidden := f.mkCategory(true, false, nil)
	_, err := f.s.Apply(ctx, f.src, Snapshot{Clients: []Client{{MAC: "aa:bb:cc:00:31:01", Name: "X", Kind: "wired"}}}, t0)
	must(t, err)
	for _, typ := range []string{"gibt-es-nicht", hidden, "network", ""} {
		if _, err := f.s.Adopt(ctx, f.src, "aa:bb:cc:00:31:01", typ, f.user, t0); !errors.Is(err, ErrBadType) {
			t.Errorf("Typ %q: erwartet ErrBadType, bekam %v", typ, err)
		}
	}
	if n := f.count(`SELECT count(*) FROM it_net_candidates WHERE source=$1`, f.src); n != 1 {
		t.Errorf("Vorschlag muss bleiben: %d", n)
	}
}

// setCat ordnet das Asset einer Kategorie zu und setzt dessen eigene Karenz.
func setCat(t *testing.T, f *fx, id, cat string, grace *int) {
	t.Helper()
	_, err := f.s.DB.Exec(context.Background(), `UPDATE it_assets SET type = $2, net_grace_minutes = $3 WHERE id = $1::uuid`, id, cat, grace)
	must(t, err)
}

func TestWatchGraceFromCategory(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	ten := 10
	id := f.mk("a", "offline", t0)
	setCat(t, f, id, f.mkCategory(true, true, &ten), nil)

	// Kategorie-Karenz 10 min: bei 6 min noch kein Alarm, bei 10 min Alarm.
	must(t, w.Pass(ctx, t0.Add(6*time.Minute)))
	if len(sink.opened) != 0 {
		t.Fatal("bei 6 min darf noch kein Alarm kommen")
	}
	must(t, w.Pass(ctx, t0.Add(10*time.Minute)))
	if len(sink.opened) != 1 || sink.opened[0].GraceMinutes != 10 {
		t.Fatalf("Alarm bei 10 min erwartet, GraceMinutes 10: %+v", sink.opened)
	}
}

func TestWatchAssetGraceBeatsCategory(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	ten, two := 10, 2
	id := f.mk("a", "offline", t0)
	setCat(t, f, id, f.mkCategory(true, true, &ten), &two)
	must(t, w.Pass(ctx, t0.Add(1*time.Minute)))
	if len(sink.opened) != 0 {
		t.Fatal("bei 1 min noch kein Alarm")
	}
	must(t, w.Pass(ctx, t0.Add(2*time.Minute)))
	if len(sink.opened) != 1 || sink.opened[0].GraceMinutes != 2 {
		t.Fatalf("Asset-Karenz 2 min muss gelten: %+v", sink.opened)
	}
}

func TestWatchClearedAssetGraceFallsBackAndKeepsAlarm(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	ten, two := 10, 2
	cat := f.mkCategory(true, true, &ten)
	id := f.mk("a", "offline", t0)
	setCat(t, f, id, cat, &two)
	must(t, w.Pass(ctx, t0.Add(2*time.Minute)))
	if len(sink.opened) != 1 {
		t.Fatal("Alarm bei 2 min erwartet")
	}
	// Karenz am Asset geleert (jetzt gilt die Kategorie, 10 min): laufender Alarm bleibt.
	setCat(t, f, id, cat, nil)
	must(t, w.Pass(ctx, t0.Add(3*time.Minute)))
	if st, _, _ := f.alarmState(id); st != "alarm" {
		t.Errorf("Alarm muss bleiben, Zustand %s", st)
	}
	if len(sink.opened) != 1 || len(sink.repeated) != 0 || len(sink.recov) != 0 {
		t.Errorf("keine weiteren Aktionen: opened=%d repeated=%d recov=%d", len(sink.opened), len(sink.repeated), len(sink.recov))
	}
}

func TestWatchGeneralGraceWithoutCategoryGrace(t *testing.T) {
	f, sink, w := newWatch(t)
	ctx := context.Background()
	id := f.mk("a", "offline", t0)
	setCat(t, f, id, f.mkCategory(true, true, nil), nil)
	must(t, w.Pass(ctx, t0.Add(4*time.Minute)))
	if len(sink.opened) != 0 {
		t.Fatal("bei 4 min noch kein Alarm")
	}
	must(t, w.Pass(ctx, t0.Add(5*time.Minute)))
	if len(sink.opened) != 1 || sink.opened[0].GraceMinutes != 5 {
		t.Fatalf("allgemeine Karenz 5 min erwartet: %+v", sink.opened)
	}
}
