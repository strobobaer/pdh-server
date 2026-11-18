package extsync

import (
	"testing"
	"time"
	_ "time/tzdata" // Zeitzonen auch ohne Systemdatenbank (Windows)
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestDayStart(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"Winter", time.Date(2026, 1, 15, 0, 30, 0, 0, berlin), time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC)},
		{"Sommer", time.Date(2026, 7, 15, 10, 0, 0, 0, berlin), time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)},
		// Umstellungstage: Tagesbeginn liegt noch in der alten Zeit.
		{"29.03. (MEZ→MESZ)", time.Date(2026, 3, 29, 12, 0, 0, 0, berlin), time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC)},
		{"25.10. (MESZ→MEZ)", time.Date(2026, 10, 25, 12, 0, 0, 0, berlin), time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)},
		{"now in UTC", time.Date(2026, 7, 14, 22, 30, 0, 0, time.UTC), time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := DayStart(c.now, berlin); !got.Equal(c.want) {
			t.Errorf("%s: DayStart = %v, erwartet %v", c.name, got.UTC(), c.want)
		}
	}
	_ = DayStart(time.Now(), nil) // nil = time.Local, keine Panik
}

func TestIsBackfill(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	day := func(y int, m time.Month, d int) time.Time {
		return DayStart(time.Date(y, m, d, 12, 0, 0, 0, berlin), berlin)
	}
	cases := []struct {
		name    string
		created time.Time
		cutoff  time.Time
		want    bool
	}{
		{"Vortag 23:59", time.Date(2026, 11, 12, 23, 59, 0, 0, berlin), day(2026, 11, 13), true},
		{"Stichtag 00:00", time.Date(2026, 11, 13, 0, 0, 0, 0, berlin), day(2026, 11, 13), false},
		{"nach dem Stichtag", time.Date(2026, 11, 20, 8, 0, 0, 0, berlin), day(2026, 11, 13), false},
		{"Nullzeit", time.Time{}, day(2026, 11, 13), false},
		{"Winter kurz vor Mitternacht", time.Date(2026, 1, 14, 22, 59, 59, 0, time.UTC), day(2026, 1, 15), true},
		{"Winter Mitternacht", time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC), day(2026, 1, 15), false},
		{"Sommer kurz vor Mitternacht", time.Date(2026, 7, 14, 21, 59, 59, 0, time.UTC), day(2026, 7, 15), true},
		{"Sommer Mitternacht", time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC), day(2026, 7, 15), false},
		{"Mitternacht UTC (02:00 Berlin)", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), day(2026, 7, 15), false},
		{"29.03. kurz davor", time.Date(2026, 3, 28, 22, 59, 59, 0, time.UTC), day(2026, 3, 29), true},
		{"29.03. Tagesbeginn", time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC), day(2026, 3, 29), false},
		{"25.10. kurz davor", time.Date(2026, 10, 24, 21, 59, 59, 0, time.UTC), day(2026, 10, 25), true},
		{"25.10. Tagesbeginn", time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC), day(2026, 10, 25), false},
	}
	for _, c := range cases {
		if got := IsBackfill(c.created, false, c.cutoff, berlin); got != c.want {
			t.Errorf("%s: IsBackfill = %v, erwartet %v", c.name, got, c.want)
		}
	}
}

// Eine reine Datumsspalte (ohne Zeit und Zone) kommt als Mitternacht UTC an und
// wird als Kalendertag mit dem Kalendertag des Stichtags am Ort verglichen.
func TestBackfillDateValueIsCalendarDate(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	cutoff := DayStart(time.Date(2026, 11, 13, 9, 0, 0, 0, ny), ny)
	today := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	// Als Zeitpunkt läge "13.11. 00:00 UTC" in New York noch am Vortag.
	if !IsBackfill(today, false, cutoff, ny) {
		t.Fatal("Voraussetzung: als Zeitpunkt wäre der Stichtag rückwirkend")
	}
	if IsBackfill(today, true, cutoff, ny) {
		t.Fatal("Datum des Stichtags gilt als rückwirkend")
	}
	if !IsBackfill(yesterday, true, cutoff, ny) {
		t.Fatal("Datum vor dem Stichtag gilt nicht als rückwirkend")
	}
	if IsBackfill(time.Time{}, true, cutoff, ny) {
		t.Fatal("Nullzeit gilt als rückwirkend")
	}
	// Anlagezeit einer Datumsspalte: Tagesbeginn des Kalendertags am Ort.
	if tm, ok := createdTime(yesterday, true, ny); !ok || !tm.Equal(time.Date(2026, 11, 12, 0, 0, 0, 0, ny)) {
		t.Fatalf("Anlagezeit %v", tm)
	}
	if _, ok := createdTime(nil, false, ny); ok {
		t.Fatal("NULL liefert eine Anlagezeit")
	}
	if _, ok := createdTime("2026-01-01", false, ny); ok {
		t.Fatal("unbekannter Typ liefert eine Anlagezeit")
	}
}

func TestDialectTimeColumn(t *testing.T) {
	d := &mssqlDialect{}
	for _, c := range []struct {
		col            Column
		isTime, dateOn bool
	}{
		{Column{Name: "a", Type: "datetime"}, true, false},
		{Column{Name: "a", Type: "datetime2"}, true, false},
		{Column{Name: "a", Type: "SmallDateTime"}, true, false},
		{Column{Name: "a", Type: "date"}, true, true},
		{Column{Name: "a", Type: "nvarchar"}, false, false},
		{Column{Name: "a", Type: "datetimeoffset"}, false, false},
		{Column{Name: "a", Type: "timestamp", IsRowVersion: true}, false, false},
	} {
		isTime, dateOnly := d.TimeColumn(c.col)
		if isTime != c.isTime || dateOnly != c.dateOn {
			t.Errorf("%s: %v/%v, erwartet %v/%v", c.col.Type, isTime, dateOnly, c.isTime, c.dateOn)
		}
	}
}
