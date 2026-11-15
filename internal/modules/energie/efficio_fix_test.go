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
