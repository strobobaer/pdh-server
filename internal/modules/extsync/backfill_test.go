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

func TestIsBackfill(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, berlin)
	cases := []struct {
		name    string
		created time.Time
		now     time.Time
		want    bool
	}{
		{"gestern 23:59", time.Date(2026, 11, 12, 23, 59, 0, 0, berlin), now, true},
		{"heute 00:00", time.Date(2026, 11, 13, 0, 0, 0, 0, berlin), now, false},
		{"heute später", time.Date(2026, 11, 13, 18, 0, 0, 0, berlin), now, false},
		{"Nullzeit", time.Time{}, now, false},
		// Winterzeit (MEZ, UTC+1): Tagesbeginn 15.01. ist 14.01. 23:00 UTC.
		{"Winter kurz vor Mitternacht", time.Date(2026, 1, 14, 22, 59, 59, 0, time.UTC),
			time.Date(2026, 1, 15, 0, 30, 0, 0, berlin), true},
		{"Winter Mitternacht", time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 15, 0, 30, 0, 0, berlin), false},
		// Sommerzeit (MESZ, UTC+2): Tagesbeginn 15.07. ist 14.07. 22:00 UTC.
		{"Sommer kurz vor Mitternacht", time.Date(2026, 7, 14, 21, 59, 59, 0, time.UTC),
			time.Date(2026, 7, 15, 10, 0, 0, 0, berlin), true},
		{"Sommer Mitternacht", time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 15, 10, 0, 0, 0, berlin), false},
		// Mitternacht UTC ist in Berlin schon 02:00 desselben Tags: nicht rückwirkend.
		{"Mitternacht UTC", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 15, 10, 0, 0, 0, berlin), false},
		// "now" in UTC angegeben: maßgeblich ist der Tag in loc.
		{"now in UTC", time.Date(2026, 7, 14, 21, 59, 59, 0, time.UTC),
			time.Date(2026, 7, 14, 22, 30, 0, 0, time.UTC), true},
	}
	for _, c := range cases {
		if got := IsBackfill(c.created, c.now, berlin); got != c.want {
			t.Errorf("%s: IsBackfill = %v, erwartet %v", c.name, got, c.want)
		}
	}
	// Ohne Ort gilt time.Local (keine Panik).
	_ = IsBackfill(time.Now().Add(-48*time.Hour), time.Now(), nil)
}

// Eine reine Datumsspalte (ohne Zeit und Zone) kommt als Mitternacht UTC an und
// wird als Kalendertag am Ort des Servers verstanden.
func TestBackfillDateValueIsCalendarDate(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, ny)
	today := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	// Als Zeitpunkt läge "heute 00:00 UTC" in New York noch am Vortag.
	if !IsBackfill(today, now, ny) {
		t.Fatal("Voraussetzung: als Zeitpunkt wäre heute rückwirkend")
	}
	if backfillValue(today, true, now, ny) {
		t.Fatal("Datum von heute gilt als rückwirkend")
	}
	if !backfillValue(yesterday, true, now, ny) {
		t.Fatal("Datum von gestern gilt nicht als rückwirkend")
	}
	if !backfillValue(today, false, now, ny) {
		t.Fatal("Zeitspalte: Zeitpunkt vor Tagesbeginn muss rückwirkend sein")
	}
	if backfillValue(nil, false, now, ny) || backfillValue(nil, true, now, ny) {
		t.Fatal("NULL gilt als neu")
	}
	if backfillValue("2026-01-01", false, now, ny) {
		t.Fatal("unbekannter Typ gilt als neu")
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
