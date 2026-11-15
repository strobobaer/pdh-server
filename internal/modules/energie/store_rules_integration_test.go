package energie

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSaveVirtualRejectsCycleAndUnknown(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	infra := newInfra(t, pool, "")
	g, a, b := uniq("G"), uniq("A"), uniq("B")
	save(t, s, &Meter{Key: g, InfraID: infra})
	save(t, s, &Meter{Key: a, InfraID: infra, Formula: "[" + g + "]*2"})
	save(t, s, &Meter{Key: b, InfraID: infra, Formula: "[" + a + "]+1"})

	var cyc *CycleError
	_, err := s.Save(ctx, &Meter{Key: a, InfraID: infra, Kind: KindCounter, Formula: "[" + b + "]"}, "")
	if !errors.As(err, &cyc) {
		t.Errorf("Zirkel: %v", err)
	}
	_, err = s.Save(ctx, &Meter{Key: a, InfraID: infra, Kind: KindCounter, Formula: "[" + a + "]"}, "")
	if !errors.As(err, &cyc) {
		t.Errorf("Selbstbezug: %v", err)
	}
	var unk *UnknownRefError
	_, err = s.Save(ctx, &Meter{Key: uniq("U"), InfraID: infra, Kind: KindCounter, Formula: "[gibt-es-nicht]"}, "")
	if !errors.As(err, &unk) {
		t.Errorf("unbekannt: %v", err)
	}
	var fe *FormulaError
	_, err = s.Save(ctx, &Meter{Key: uniq("F"), InfraID: infra, Kind: KindCounter, Formula: "1 +"}, "")
	if !errors.As(err, &fe) {
		t.Errorf("Schreibweise: %v", err)
	}
	// Eine Formel leert die Quelle.
	_, mapID := newMapping(t, pool, infra)
	v := &Meter{Key: uniq("V"), InfraID: infra, Kind: KindCounter, SourceID: mapID, Formula: "[" + g + "]"}
	save(t, s, v)
	got, err := s.Get(ctx, v.ID)
	if err != nil || got.SourceID != "" || !got.Virtual() {
		t.Errorf("Quelle nicht geleert: %+v %v", got, err)
	}
}

func TestSaveRejectsKindMix(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	infra := newInfra(t, pool, "")
	c, co, i1 := uniq("C"), uniq("CO"), uniq("I")
	save(t, s, &Meter{Key: c, InfraID: infra, Kind: KindCounter})
	save(t, s, &Meter{Key: co, InfraID: infra, Kind: KindConsumption})
	save(t, s, &Meter{Key: i1, InfraID: infra, Kind: KindInstant})
	ctx := context.Background()
	for _, tc := range []struct {
		kind, formula string
		ok            bool
	}{
		{KindInstant, "[" + c + "]", false},
		{KindCounter, "[" + i1 + "]", false},
		{KindConsumption, "[" + i1 + "]+1", false},
		{KindCounter, "[" + c + "]+[" + co + "]", true},
		{KindInstant, "[" + i1 + "]*2", true},
	} {
		_, err := s.Save(ctx, &Meter{Key: uniq("M"), InfraID: infra, Kind: tc.kind, Formula: tc.formula}, "")
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, ErrKindMix) {
			t.Errorf("%s %s: %v", tc.kind, tc.formula, err)
		}
	}
}

func TestSaveRejectsChangeInUse(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	infra := newInfra(t, pool, "")
	a, c := uniq("A"), uniq("C")
	src := &Meter{Key: a, InfraID: infra, Kind: KindCounter}
	save(t, s, src)
	save(t, s, &Meter{Key: c, InfraID: infra, Formula: "[" + a + "]"})

	var in *InUseError
	_, err := s.Save(ctx, &Meter{ID: src.ID, Key: uniq("NEU"), InfraID: infra, Kind: KindCounter}, "")
	if !errors.As(err, &in) || len(in.Users) != 1 || in.Users[0] != c {
		t.Fatalf("ID ändern: %v", err)
	}
	if !strings.Contains(err.Error(), "wird verwendet in [") {
		t.Errorf("Text: %q", err.Error())
	}
	_, err = s.Save(ctx, &Meter{ID: src.ID, Key: a, InfraID: infra, Kind: KindConsumption}, "")
	if !errors.As(err, &in) {
		t.Fatalf("Art ändern: %v", err)
	}
	// Name darf sich ändern.
	if _, err = s.Save(ctx, &Meter{ID: src.ID, Key: a, Name: "Neu", InfraID: infra, Kind: KindCounter}, ""); err != nil {
		t.Fatalf("Name ändern: %v", err)
	}
}

func TestDeleteInUse(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	infra := newInfra(t, pool, "")
	a, c := uniq("A"), uniq("C")
	src := &Meter{Key: a, InfraID: infra}
	virt := &Meter{Key: c, InfraID: infra, Formula: "[" + a + "]"}
	save(t, s, src)
	save(t, s, virt)
	var in *InUseError
	if err := s.Delete(ctx, src.ID); !errors.As(err, &in) || in.Users[0] != c {
		t.Fatalf("Löschen: %v", err)
	}
	if err := s.Delete(ctx, virt.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, src.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("zweimal: %v", err)
	}
}

func TestListFilters(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	s.Now = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	top := newInfra(t, pool, "")
	sub := newInfra(t, pool, top)
	other := newInfra(t, pool, "")
	conn, mapID := newMapping(t, pool, top)

	tag := uniq("LF")
	mk := func(key string, m Meter) string {
		m.Key = tag + "-" + key
		if m.Name == "" {
			m.Name = "Name " + key
		}
		save(t, s, &m)
		return m.ID
	}
	idA := mk("A", Meter{InfraID: top, Unit: "kWh", Kind: KindCounter, SourceID: mapID})
	idB := mk("B", Meter{InfraID: sub, Unit: "m3", Kind: KindInstant})
	idC := mk("C", Meter{InfraID: other, Unit: "kWh", Kind: KindCounter, Formula: "[" + tag + "-A]"})
	idD := mk("D", Meter{InfraID: top, Unit: "kWh", Kind: KindConsumption, Name: "Sonderling"})
	for _, q := range []struct {
		sql string
		a   []any
	}{
		{`UPDATE energy_meters SET export = false WHERE id = $1::uuid`, []any{idD}},
		{`UPDATE energy_meters SET active = false WHERE id = $1::uuid`, []any{idB}},
		{`UPDATE energy_meters SET last_at = $2, last_value = 1 WHERE id = $1::uuid`, []any{idA, s.Now().Add(-time.Minute)}},
		{`UPDATE energy_meters SET last_at = $2, last_value = 2 WHERE id = $1::uuid`, []any{idB, s.Now().Add(-time.Hour)}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.a...)
		must(t, err)
	}

	yes, no := true, false
	run := func(name string, f Filter, want string) {
		t.Helper()
		if f.Query == "" {
			f.Query = tag // nur die eigenen Messstellen
		}
		list, err := s.List(ctx, f)
		must(t, err)
		var got []string
		for _, m := range list {
			got = append(got, strings.TrimPrefix(m.Key, tag+"-"))
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s: %v, erwartet %s", name, got, want)
		}
	}
	run("alle", Filter{}, "A,B,C,D")
	run("Anlage mit Unteranlagen", Filter{InfraID: top}, "A,B,D")
	run("Unteranlage", Filter{InfraID: sub}, "B")
	run("virtuell", Filter{Virtual: &yes}, "C")
	run("echt", Filter{Virtual: &no}, "A,B,D")
	run("exportieren", Filter{Export: &yes}, "A,B,C")
	run("nicht exportieren", Filter{Export: &no}, "D")
	run("inaktiv", Filter{Active: &no}, "B")
	run("Einheit", Filter{Unit: "m3"}, "B")
	run("Art", Filter{Kind: KindConsumption}, "D")
	run("Verbindung", Filter{ConnectionID: conn}, "A")
	run("Freitext", Filter{Query: "sonderling"}, "D")
	run("veraltet", Filter{Stale: true}, "B,C,D")
	run("zuletzt absteigend", Filter{Sort: "last", Desc: true}, "A,B,C,D")
	run("Name absteigend", Filter{Sort: "name", Desc: true}, "D,C,B,A")
	run("erlaubt", Filter{AllowedIDs: map[string]bool{idA: true, idC: true}}, "A,C")
	run("nichts erlaubt", Filter{AllowedIDs: map[string]bool{}}, "")

	got, err := s.Get(ctx, idA)
	must(t, err)
	if got.InfraPath == "" || got.ConnectionID != conn || got.SourceLabel != "Quelle" {
		t.Errorf("Get: %+v", got)
	}
	units, err := s.Units(ctx)
	must(t, err)
	if !strings.Contains(strings.Join(units, ","), "m3") {
		t.Errorf("Einheiten: %v", units)
	}
}
