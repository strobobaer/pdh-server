package maintenance

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }

// Intervalle laufen kalendergenau (Monatsende, Schaltjahr), nicht in 30/365 Tagen.
func TestNextDueCalendar(t *testing.T) {
	cases := []struct {
		iv   Interval
		days int
		from time.Time
		want time.Time
	}{
		{IntervalDaily, 1, day(2026, 10, 6), day(2026, 10, 7)},
		{IntervalWeekly, 7, day(2026, 10, 6), day(2026, 10, 13)},
		{IntervalMonthly, 30, day(2026, 1, 15), day(2026, 2, 15)},
		{IntervalQuarterly, 90, day(2026, 10, 6), day(2027, 1, 6)},
		{IntervalYearly, 365, day(2027, 3, 1), day(2028, 3, 1)}, // Schaltjahr: nicht 29.02.
		{"", 14, day(2026, 10, 6), day(2026, 10, 20)},
	}
	for _, c := range cases {
		if got := NextDue(c.iv, c.days, c.from.Add(15*time.Hour)); !got.Equal(c.want) {
			t.Errorf("%s ab %s: %s, erwartet %s", c.iv, c.from.Format("02.01.2006"), got.Format("02.01.2006"), c.want.Format("02.01.2006"))
		}
	}
}

// Ab Durchfuehrung zaehlt das Erledigt-Datum, fester Rhythmus die Faelligkeit –
// stark verspaetet erledigt wird bis heute aufgeholt (kein Rueckstau).
func TestNextDueForModes(t *testing.T) {
	due, done, now := day(2026, 10, 1), day(2026, 10, 9), day(2026, 10, 9)
	if got := nextDueFor(ScheduleFromCompletion, IntervalMonthly, 30, due, done, now); !got.Equal(day(2026, 11, 9)) {
		t.Errorf("ab Durchführung: %s", got.Format("02.01.2006"))
	}
	if got := nextDueFor(ScheduleFixed, IntervalMonthly, 30, due, done, now); !got.Equal(day(2026, 11, 1)) {
		t.Errorf("fester Rhythmus: %s", got.Format("02.01.2006"))
	}
	// wöchentlich, drei Wochen zu spät erledigt → nächster Termin ab heute im Rhythmus
	if got := nextDueFor(ScheduleFixed, IntervalWeekly, 7, day(2026, 9, 15), now, now); !got.Equal(day(2026, 10, 13)) {
		t.Errorf("Aufholen: %s", got.Format("02.01.2006"))
	}
}
