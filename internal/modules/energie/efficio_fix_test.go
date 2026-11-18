package energie

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWindows1252UnmappableBecomesQuestionMark(t *testing.T) {
	o := CSVOptions{TimeFormat: "dmy", Encoding: "windows-1252", Loc: time.UTC}
	b, err := FormatRows([]Point{{Key: "Zä日x", At: time.Date(2026, 11, 15, 10, 15, 0, 0, time.UTC), Value: 1}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Z\xe4?x") {
		t.Errorf("got %q", b)
	}
}

func TestGapNotesMergeAndCap(t *testing.T) {
	g := newGapNotes(time.Minute, time.UTC)
	t0 := time.Date(2026, 11, 15, 10, 1, 0, 0, time.UTC)
	for k := 0; k < 150; k++ {
		g.add(Step{End: t0.Add(time.Duration(k) * time.Minute), Missing: []string{"A"}})
	}
	notes := g.flush()
	if len(notes) != 1 || notes[0] != "[A] ohne Wert 15.11.2026 10:01–12:30 (150 Minuten)" {
		t.Errorf("Notizen: %q", notes)
	}
	g = newGapNotes(time.Minute, time.UTC)
	for k := 0; k < 80; k++ { // 80 Messstellen mit je einer Lücke
		g.add(Step{End: t0, Missing: []string{fmt.Sprintf("M%02d", k)}})
	}
	notes = g.flush()
	if len(notes) != maxGapNotes+1 || notes[maxGapNotes] != "… und 30 weitere" {
		t.Errorf("%d Notizen, letzte %q", len(notes), notes[len(notes)-1])
	}
}

func TestFormatRowsTimeBase(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	at := time.Date(2026, 10, 25, 0, 15, 0, 0, time.UTC) // 02:15 Sommerzeit
	pts := []Point{{Key: "A", At: at, Value: 1}}
	for _, c := range []struct{ tf, base, want string }{
		{"dmy", TimeBaseLocal, "25.10.2026 02:15;A;1\r\n"},
		{"dmy", TimeBaseOffset, "25.10.2026 02:15 +02:00;A;1\r\n"},
		{"iso", TimeBaseOffset, "2026-10-25 02:15+02:00;A;1\r\n"},
		{"dmy", TimeBaseUTC, "25.10.2026 00:15;A;1\r\n"},
		{"iso", TimeBaseUTC, "2026-10-25 00:15;A;1\r\n"},
	} {
		b, err := FormatRows(pts, CSVOptions{TimeFormat: c.tf, TimeBase: c.base, Loc: berlin})
		if err != nil || string(b) != c.want {
			t.Errorf("%s/%s: %q, erwartet %q (%v)", c.tf, c.base, b, c.want, err)
		}
	}
	for _, v := range []string{"", TimeBaseLocal, TimeBaseUTC, TimeBaseOffset} {
		if CheckTimeBase(v) != nil {
			t.Errorf("%q abgewiesen", v)
		}
	}
	if CheckTimeBase("cet") == nil {
		t.Error("ungültiger Zeitbezug angenommen")
	}
}

func TestSettingsTimeBase(t *testing.T) {
	s := DefaultSettings()
	if s.TimeBase != TimeBaseLocal || s.Validate() != nil {
		t.Fatalf("Vorgabe: %+v", s)
	}
	s.TimeBase = "cet"
	if s.Validate() == nil {
		t.Error("ungültiger Zeitbezug angenommen")
	}
}
