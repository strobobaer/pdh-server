package maintenance

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }

func fmtDay(t time.Time) string { return t.Format("02.01.2006") }

// Intervalle laufen kalendergenau (Monatsende, Schaltjahr), auch „alle N“.
func TestAddInterval(t *testing.T) {
	cases := []struct {
		unit  string
		count int
		from  time.Time
		want  time.Time
	}{
		{UnitDay, 1, day(2026, 10, 6), day(2026, 10, 7)},
		{UnitDay, 14, day(2026, 10, 6), day(2026, 10, 20)},
		{UnitWeek, 2, day(2026, 10, 6), day(2026, 10, 20)},
		{UnitMonth, 1, day(2026, 1, 15), day(2026, 2, 15)},
		{UnitMonth, 6, day(2026, 10, 6), day(2027, 4, 6)},
		{UnitYear, 1, day(2027, 3, 1), day(2028, 3, 1)},
		{"", 0, day(2026, 10, 6), day(2026, 11, 6)}, // Standard: 1 Monat
	}
	for _, c := range cases {
		if got := AddInterval(c.unit, c.count, c.from.Add(15*time.Hour)); !got.Equal(c.want) {
			t.Errorf("alle %d %s ab %s: %s, erwartet %s", c.count, c.unit, fmtDay(c.from), fmtDay(got), fmtDay(c.want))
		}
	}
}

// Ab Durchfuehrung zaehlt das Erledigt-Datum, fester Rhythmus die Faelligkeit –
// stark verspaetet erledigt wird bis heute aufgeholt (kein Rueckstau).
func TestNextDueForModes(t *testing.T) {
	due, done, now := day(2026, 10, 1), day(2026, 10, 9), day(2026, 10, 9)
	if got := nextDueFor(ScheduleFromCompletion, UnitMonth, 1, due, done, now); !got.Equal(day(2026, 11, 9)) {
		t.Errorf("ab Durchführung: %s", fmtDay(got))
	}
	if got := nextDueFor(ScheduleFixed, UnitMonth, 1, due, done, now); !got.Equal(day(2026, 11, 1)) {
		t.Errorf("fester Rhythmus: %s", fmtDay(got))
	}
	if got := nextDueFor(ScheduleFixed, UnitWeek, 1, day(2026, 9, 15), now, now); !got.Equal(day(2026, 10, 13)) {
		t.Errorf("Aufholen: %s", fmtDay(got))
	}
}

func TestPlannedDates(t *testing.T) {
	got := PlannedDates(UnitWeek, 2, day(2026, 10, 6), day(2026, 11, 3), 10)
	if len(got) != 3 || !got[2].Equal(day(2026, 11, 3)) {
		t.Fatalf("14-tägig: %v", got)
	}
	if n := len(PlannedDates(UnitDay, 1, day(2026, 1, 1), day(2026, 12, 31), 50)); n != 50 {
		t.Fatalf("Obergrenze: %d", n)
	}
}

// Altfelder fuer andere Programmteile bleiben sinnvoll gefuellt.
func TestLegacyInterval(t *testing.T) {
	cases := map[[2]string]Interval{
		{UnitDay, "1"}: IntervalDaily, {UnitDay, "14"}: IntervalWeekly, {UnitWeek, "2"}: IntervalWeekly,
		{UnitMonth, "1"}: IntervalMonthly, {UnitMonth, "3"}: IntervalQuarterly, {UnitMonth, "12"}: IntervalYearly, {UnitYear, "1"}: IntervalYearly,
	}
	for k, want := range cases {
		n := 0
		for _, c := range k[1] {
			n = n*10 + int(c-'0')
		}
		if got, _ := LegacyInterval(k[0], n); got != want {
			t.Errorf("%s×%d: %s, erwartet %s", k[0], n, got, want)
		}
	}
	if u, n := UnitFromLegacy(IntervalQuarterly); u != UnitMonth || n != 3 {
		t.Errorf("quartalsweise: %s×%d", u, n)
	}
}

func TestNormalizePlan(t *testing.T) {
	in := &PlanInput{Name: "  Presse  ", InfrastructureID: "x", Interval: IntervalQuarterly,
		Checklists: []PlanChecklistInput{{TemplateID: "a"}, {TemplateID: "b", RhythmUnit: UnitWeek, RhythmCount: 0}}}
	if err := normalizePlan(in); err != nil {
		t.Fatal(err)
	}
	if in.Name != "Presse" || in.IntervalUnit != UnitMonth || in.IntervalCount != 3 || in.ScheduleMode != ScheduleFromCompletion ||
		in.Type != PlanPreventive || in.Priority != PrioMedium || in.Checklists[0].RhythmUnit != RhythmAlways || in.Checklists[1].RhythmCount != 1 {
		t.Fatalf("normalisiert: %+v", in)
	}
	for _, bad := range []*PlanInput{
		{Name: "", InfrastructureID: "x"},
		{Name: "A"},
		{Name: "A", InfrastructureID: "x", IntervalUnit: "hour"},
		{Name: "A", InfrastructureID: "x", LeadDays: 400},
		{Name: "A", InfrastructureID: "x", Checklists: []PlanChecklistInput{{TemplateID: "a", RhythmUnit: "sometimes"}}},
	} {
		if err := normalizePlan(bad); !IsInputError(err) {
			t.Errorf("ungültig nicht erkannt: %+v → %v", bad, err)
		}
	}
}

func TestMeasure(t *testing.T) {
	if v, ok := ParseMeasure("7,5"); !ok || v != 7.5 {
		t.Fatal("Komma")
	}
	min, max := 5.0, 7.0
	if r := MeasureInRange("7,8", &min, &max); r == nil || *r {
		t.Fatal("außerhalb")
	}
	if r := MeasureInRange("6", &min, nil); r == nil || !*r {
		t.Fatal("nur Min")
	}
	if MeasureInRange("6", nil, nil) != nil || MeasureInRange("abc", &min, &max) != nil {
		t.Fatal("ohne Grenzen / keine Zahl")
	}
}
