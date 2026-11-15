package energie

import (
	"context"
	"math"
	"strings"
	"testing"
)

func thresholdMeter(t *testing.T) (*Store, string) {
	t.Helper()
	pool := testPool(t)
	s := NewStore(pool)
	m := &Meter{Key: uniq("TH"), Name: "Strom", InfraID: newInfra(t, pool, ""), Unit: "kW", Kind: KindInstant}
	save(t, s, m)
	list, err := s.List(context.Background(), Filter{Query: m.Key})
	if err != nil || len(list) != 1 {
		t.Fatalf("Messstelle: %v %d", err, len(list))
	}
	return s, list[0].ID
}

func okThreshold(meter string) *Threshold {
	return &Threshold{MeterID: meter, Name: "Grenze", Condition: CondAbove, Limit: 10,
		WindowMinutes: 5, ResetMinutes: 15, Priority: "medium", Active: true}
}

func TestSaveThresholdValidation(t *testing.T) {
	s, meter := thresholdMeter(t)
	ctx := context.Background()
	u, g := "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	cases := []struct {
		name string
		mod  func(*Threshold)
		msg  string
	}{
		{"Name leer", func(x *Threshold) { x.Name = "  " }, "Namen"},
		{"Name zu lang", func(x *Threshold) { x.Name = strings.Repeat("a", 101) }, "100 Zeichen"},
		{"Bedingung", func(x *Threshold) { x.Condition = "x" }, "über"},
		{"Grenzwert", func(x *Threshold) { x.Limit = math.NaN() }, "Grenzwert"},
		{"Zeitraum 0", func(x *Threshold) { x.WindowMinutes = 0 }, "Zeitraum"},
		{"Zeitraum 1441", func(x *Threshold) { x.WindowMinutes = 1441 }, "Zeitraum"},
		{"Rückschaltung 0", func(x *Threshold) { x.ResetMinutes = 0 }, "Rückschaltzeit"},
		{"Priorität", func(x *Threshold) { x.Priority = "egal" }, "Priorität"},
		{"Benutzer und Gruppe", func(x *Threshold) { x.ResponsibleUserID, x.ResponsibleGroupID = &u, &g }, "Benutzer oder eine Gruppe"},
		{"Tag zu lang", func(x *Threshold) { x.Tag = strings.Repeat("a", 51) }, "Schlagwort"},
	}
	for _, c := range cases {
		x := okThreshold(meter)
		c.mod(x)
		err := s.SaveThreshold(ctx, x)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: Fehler %v, erwartet mit %q", c.name, err, c.msg)
		}
	}

	x := okThreshold(meter)
	if err := s.SaveThreshold(ctx, x); err != nil || x.ID == "" {
		t.Fatalf("anlegen: %v", err)
	}
	x.Name, x.Limit, x.Active = "Neu", 12.5, false
	if err := s.SaveThreshold(ctx, x); err != nil {
		t.Fatalf("ändern: %v", err)
	}
	got, err := s.Threshold(ctx, x.ID)
	if err != nil || got.Name != "Neu" || got.Limit != 12.5 || got.Active || got.State != "normal" ||
		got.MeterKey == "" || got.Unit != "kW" || got.Kind != KindInstant {
		t.Fatalf("lesen: %+v %v", got, err)
	}
	if all, _ := s.Thresholds(ctx, ""); containsTh(all, x.ID) {
		t.Error("inaktiver Schwellwert darf nicht in der Wächterliste stehen")
	}
	if own, _ := s.Thresholds(ctx, meter); !containsTh(own, x.ID) {
		t.Error("UI-Liste muss auch inaktive enthalten")
	}
	if err := s.DeleteThreshold(ctx, x.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Threshold(ctx, x.ID); err != ErrNotFound {
		t.Errorf("nach Löschen: %v", err)
	}
}

func containsTh(l []Threshold, id string) bool {
	for _, t := range l {
		if t.ID == id {
			return true
		}
	}
	return false
}

func TestThresholdCounts(t *testing.T) {
	s, meter := thresholdMeter(t)
	ctx := context.Background()
	a, b := okThreshold(meter), okThreshold(meter)
	b.Name = "Zweite"
	for _, x := range []*Threshold{a, b} {
		if err := s.SaveThreshold(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO energy_threshold_state (threshold_id, state) VALUES ($1, 'alarm')`, b.ID); err != nil {
		t.Fatal(err)
	}
	c, err := s.ThresholdCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := c[meter]; got.Total != 2 || got.Alarms != 1 {
		t.Errorf("Zahlen = %+v, erwartet 2/1", got)
	}
}
