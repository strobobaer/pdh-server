package extsync

import (
	"testing"
	"time"
)

func TestForeignWallToInstant(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	ny := mustLoc(t, "America/New_York")
	wall := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, time.UTC) }
	cases := []struct {
		name string
		wall time.Time
		loc  *time.Location
		want time.Time
	}{
		{"UTC", wall(2026, 7, 15, 10, 0), time.UTC, wall(2026, 7, 15, 10, 0)},
		{"Berlin Sommer", wall(2026, 7, 15, 10, 0), berlin, wall(2026, 7, 15, 8, 0)},
		{"Berlin Winter", wall(2026, 1, 15, 10, 0), berlin, wall(2026, 1, 15, 9, 0)},
		{"Berlin Vortag", wall(2026, 11, 12, 23, 30), berlin, wall(2026, 11, 12, 22, 30)},
		{"New York", wall(2026, 11, 13, 10, 0), ny, wall(2026, 11, 13, 15, 0)},
		// Umstellungstage: Verhalten von time.Date, hier festgeschrieben.
		// 29.03. 02:30 gibt es nicht: gilt als 03:30 MESZ (um die Lücke verschoben).
		{"29.03. Lücke", wall(2026, 3, 29, 2, 30), berlin, wall(2026, 3, 29, 1, 30)},
		{"29.03. 03:00", wall(2026, 3, 29, 3, 0), berlin, wall(2026, 3, 29, 1, 0)},
		// 25.10. 02:30 gibt es zweimal: gilt als die SPÄTERE (02:30 MEZ = 01:30 UTC).
		{"25.10. doppelt", wall(2026, 10, 25, 2, 30), berlin, wall(2026, 10, 25, 1, 30)},
		{"25.10. 01:59", wall(2026, 10, 25, 1, 59), berlin, wall(2026, 10, 24, 23, 59)},
	}
	for _, c := range cases {
		got := ForeignWallToInstant(c.wall, c.loc)
		if !got.Equal(c.want) {
			t.Errorf("%s: %v, erwartet %v", c.name, got.UTC(), c.want)
		}
		// Rückweg: die Wanduhr im Fremdsystem (als UTC beschriftet), außer in der Lücke.
		if c.name != "29.03. Lücke" {
			back := InstantToForeignWall(got, c.loc)
			if !back.Equal(c.wall) || back.Location() != time.UTC {
				t.Errorf("%s: Rückweg %v, erwartet %v", c.name, back, c.wall)
			}
		}
	}
	// Lücke: der Rückweg liefert die gültige Wanduhr 03:30.
	if back := InstantToForeignWall(wall(2026, 3, 29, 1, 30), berlin); !back.Equal(wall(2026, 3, 29, 3, 30)) {
		t.Errorf("Lücke Rückweg: %v", back)
	}
	// Erste 02:30 am 25.10. (MESZ = 00:30 UTC): Rückweg 02:30, Hinweg die spätere.
	if back := InstantToForeignWall(wall(2026, 10, 25, 0, 30), berlin); !back.Equal(wall(2026, 10, 25, 2, 30)) {
		t.Errorf("doppelte Stunde Rückweg: %v", back)
	}
	// Nanosekunden bleiben erhalten.
	n := time.Date(2026, 7, 15, 10, 0, 0, 123456700, time.UTC)
	if got := ForeignWallToInstant(n, berlin); got.Nanosecond() != 123456700 {
		t.Errorf("Nanosekunden: %v", got)
	}
}

// Zonenlose Datums-/Zeitspalten und date-Spalten (R28: Mitternacht am Ort des
// Fremdsystems) werden umgerechnet; alles andere (datetimeoffset, time, Text,
// unbekannte Spalte) bleibt unberührt.
func TestWallColumns(t *testing.T) {
	info := &TableInfo{Schema: "dbo", Name: "T", Columns: []Column{
		{Name: "Nr", Type: "int", IsKey: true},
		{Name: "DT", Type: "datetime"},
		{Name: "DT2", Type: "datetime2"},
		{Name: "SDT", Type: "smalldatetime"},
		{Name: "D", Type: "date"},
		{Name: "DTO", Type: "datetimeoffset"},
		{Name: "Zeit", Type: "time"},
		{Name: "Text", Type: "nvarchar"},
	}}
	var maps []FieldMap
	for i, c := range []string{"DT", "DT2", "SDT", "D", "DTO", "Zeit", "Text", "Fehlt"} {
		maps = append(maps, FieldMap{ForeignColumn: c, PDHField: "due_date", Direction: DirIn, Position: i})
	}
	// Nicht hereinkommend, fester Wert und Nicht-Zeitfeld zählen nie.
	maps = append(maps,
		FieldMap{ForeignColumn: "DT", PDHField: "title", Direction: DirIn},
		FieldMap{ForeignColumn: "DT2", PDHField: "due_date", Direction: DirOut})
	fields := Fields(RefTicket)
	for _, d := range []Dialect{&mssqlDialect{}, fakeDialect{}} {
		got := wallColumns(info, d, maps, fields)
		want := []string{"DT", "DT2", "SDT", "D"}
		if len(got) != len(want) {
			t.Fatalf("%T: %v, erwartet %v", d, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%T: %v, erwartet %v", d, got, want)
			}
		}
	}
}

func TestForeignLocation(t *testing.T) {
	server := mustLoc(t, "America/New_York")
	for _, c := range []struct {
		zone, want string
	}{
		{"", "America/New_York"},
		{"UTC", "UTC"},
		{"Europe/Berlin", "Europe/Berlin"},
	} {
		p := &Profile{TimeZone: c.zone}
		loc, err := p.ForeignLocation(server)
		if err != nil || loc.String() != c.want {
			t.Errorf("%q: %v %v, erwartet %s", c.zone, loc, err, c.want)
		}
	}
	p := &Profile{TimeZone: "Mars/Olymp"}
	if _, err := p.ForeignLocation(server); err == nil || err.Error() != "Zeitzone ‚Mars/Olymp‘ ist unbekannt" {
		t.Fatalf("unbekannte Zone: %v", err)
	}
	// Ohne Serverort gilt time.Local (keine Panik).
	if loc, err := (&Profile{}).ForeignLocation(nil); err != nil || loc == nil {
		t.Fatalf("ohne Serverort: %v %v", loc, err)
	}
}
