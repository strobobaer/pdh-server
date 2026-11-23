package maintenance

import (
	"strings"
	"testing"
	"time"
)

func ptr(n int) *int { return &n }

// Ältere Aufrufe nennen den Vorlauf noch in Kalendertagen (lead_days); er wird
// wie in Migration 212 in Arbeitstage umgerechnet: ceil(Tage × 5 / 7), höchstens 60.
func TestNormalizeLeadAltform(t *testing.T) {
	base := func(in PlanInput) *PlanInput {
		in.Name, in.InfrastructureID = "Presse", "x"
		return &in
	}
	for _, c := range []struct {
		in   *PlanInput
		want *int
	}{
		{base(PlanInput{LeadDays: 14}), ptr(10)},
		{base(PlanInput{LeadDays: 1}), ptr(1)},
		{base(PlanInput{LeadDays: 365}), ptr(60)},
		{base(PlanInput{LeadDays: 14, LeadWorkdays: ptr(3)}), ptr(3)},
		{base(PlanInput{LeadWorkdays: ptr(0)}), ptr(0)},
		{base(PlanInput{}), nil},
	} {
		if err := normalizePlan(c.in); err != nil {
			t.Fatalf("%+v: %v", c.in, err)
		}
		switch {
		case c.want == nil && c.in.LeadWorkdays != nil:
			t.Errorf("ohne Angabe gilt die Vorgabe (nil), bekam %d", *c.in.LeadWorkdays)
		case c.want != nil && (c.in.LeadWorkdays == nil || *c.in.LeadWorkdays != *c.want):
			t.Errorf("LeadDays %d: %v, erwartet %d", c.in.LeadDays, c.in.LeadWorkdays, *c.want)
		}
	}
	for _, bad := range []int{61, -1} {
		err := normalizePlan(base(PlanInput{LeadWorkdays: ptr(bad)}))
		if !IsInputError(err) || !strings.HasSuffix(err.Error(), "Vorlauf: 0 bis 60 Arbeitstage") {
			t.Errorf("%d: %v", bad, err)
		}
	}
}

func TestCivil(t *testing.T) {
	got := civil(time.Date(2027, 3, 28, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2027, 3, 28, 0, 0, 0, 0, time.Local); !got.Equal(want) || got.Location() != time.Local {
		t.Errorf("civil: %v, erwartet %v", got, want)
	}
}
