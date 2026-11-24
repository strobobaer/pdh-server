package web

import (
	"bytes"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/stillstand"
)

func wtAt(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, workLocation)
}

func hm(s string) int {
	v, ok := parseHM(s)
	if !ok {
		panic("Uhrzeit " + s)
	}
	return v
}

func shift(weekday int, start, end string, breaks ...string) workShift {
	s := workShift{Name: "S", Weekday: weekday, StartMin: hm(start), EndMin: hm(end)}
	for i := 0; i+1 < len(breaks); i += 2 {
		s.Breaks = append(s.Breaks, workBreak{StartMin: hm(breaks[i]), EndMin: hm(breaks[i+1])})
	}
	return s
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func TestWorkParseAndValidate(t *testing.T) {
	for in, want := range map[string]int{"06:00": 360, "6:05": 365, "23:59": 1439, "00:00": 0, " 14:30 ": 870} {
		if got, ok := parseHM(in); !ok || got != want {
			t.Errorf("parseHM(%q) = %d, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "24:00", "12:60", "abc", "12", "1:2"} {
		if _, ok := parseHM(bad); ok {
			t.Errorf("parseHM(%q) sollte scheitern", bad)
		}
	}
	if validateShiftTimes(hm("06:00"), hm("06:00")) == nil {
		t.Error("gleiche Zeiten erlaubt")
	}
	if validateShiftTimes(hm("22:00"), hm("06:00")) != nil {
		t.Error("Schicht über Mitternacht abgelehnt")
	}
	// Pause: innerhalb der Schicht
	if err := validateBreak(hm("06:00"), hm("14:00"), hm("09:00"), hm("09:30")); err != nil {
		t.Errorf("Pause in Schicht: %v", err)
	}
	if validateBreak(hm("06:00"), hm("14:00"), hm("13:45"), hm("14:15")) == nil {
		t.Error("Pause über das Schichtende erlaubt")
	}
	if validateBreak(hm("06:00"), hm("14:00"), hm("05:30"), hm("06:30")) == nil {
		t.Error("Pause vor Schichtbeginn erlaubt")
	}
	// Nachtschicht: Pause vor und nach Mitternacht
	for _, b := range [][2]string{{"23:30", "00:00"}, {"01:00", "01:30"}, {"23:45", "00:15"}} {
		if err := validateBreak(hm("22:00"), hm("06:00"), hm(b[0]), hm(b[1])); err != nil {
			t.Errorf("Nachtpause %v: %v", b, err)
		}
	}
	if validateBreak(hm("22:00"), hm("06:00"), hm("12:00"), hm("12:30")) == nil {
		t.Error("Mittagspause in Nachtschicht erlaubt")
	}
	if validateBreak(hm("06:00"), hm("14:00"), hm("09:00"), hm("09:00")) == nil {
		t.Error("leere Pause erlaubt")
	}
}

// Schicht mit Pause: die Pause wird aus der Arbeitszeit entfernt.
func TestWorkBreaksRemoved(t *testing.T) {
	p := &workPlan{Defined: true, Shifts: []workShift{shift(1, "06:00", "14:00", "09:00", "09:30", "11:30", "12:00")}}
	// Montag 23.11.2026
	iv := p.intervals(wtAt(2026, 11, 23, 0, 0), wtAt(2026, 11, 24, 0, 0), workLocation)
	if len(iv) != 3 {
		t.Fatalf("erwartet 3 Abschnitte, erhalten %v", iv)
	}
	want := [][2]time.Time{
		{wtAt(2026, 11, 23, 6, 0), wtAt(2026, 11, 23, 9, 0)},
		{wtAt(2026, 11, 23, 9, 30), wtAt(2026, 11, 23, 11, 30)},
		{wtAt(2026, 11, 23, 12, 0), wtAt(2026, 11, 23, 14, 0)},
	}
	for i, w := range want {
		if !iv[i].Start.Equal(w[0]) || !iv[i].End.Equal(w[1]) {
			t.Errorf("Abschnitt %d: %v–%v, erwartet %v–%v", i, iv[i].Start, iv[i].End, w[0], w[1])
		}
	}
	if m := plannedWorkMinutes(iv); !near(m, 7*60) {
		t.Errorf("Arbeitszeit %v Minuten, erwartet 420", m)
	}
	// anderer Wochentag: keine Arbeitszeit
	if iv := p.intervals(wtAt(2026, 11, 24, 0, 0), wtAt(2026, 11, 25, 0, 0), workLocation); len(iv) != 0 {
		t.Errorf("Dienstag: %v", iv)
	}
	// Ausschnitt wird zugeschnitten
	iv = p.intervals(wtAt(2026, 11, 23, 8, 0), wtAt(2026, 11, 23, 10, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 90) {
		t.Errorf("Ausschnitt 08–10 Uhr: %v Minuten, erwartet 90", m)
	}
}

// Nachtschicht über Mitternacht gehört zum Tag des Beginns; Pausen vor und nach Mitternacht.
func TestWorkMidnightShift(t *testing.T) {
	p := &workPlan{Defined: true, Shifts: []workShift{shift(5, "22:00", "06:00", "23:45", "00:15", "03:00", "03:30")}} // Freitag
	// Samstag 24.10.: der Rest der Freitagsschicht reicht herein
	iv := p.intervals(wtAt(2026, 11, 28, 0, 0), wtAt(2026, 11, 29, 0, 0), workLocation) // Samstag 28.11.
	if len(iv) != 2 || !iv[0].Start.Equal(wtAt(2026, 11, 28, 0, 15)) || !iv[0].End.Equal(wtAt(2026, 11, 28, 3, 0)) ||
		!iv[1].Start.Equal(wtAt(2026, 11, 28, 3, 30)) || !iv[1].End.Equal(wtAt(2026, 11, 28, 6, 0)) {
		t.Fatalf("Samstag: %v", iv)
	}
	if m := plannedWorkMinutes(iv); !near(m, 165+150) {
		t.Errorf("Samstag %v Minuten, erwartet 315", m)
	}
	// Freitag 27.11.: Beginn um 22:00 bis 23:45
	iv = p.intervals(wtAt(2026, 11, 27, 0, 0), wtAt(2026, 11, 28, 0, 0), workLocation)
	if len(iv) != 1 || !iv[0].Start.Equal(wtAt(2026, 11, 27, 22, 0)) || !iv[0].End.Equal(wtAt(2026, 11, 27, 23, 45)) {
		t.Fatalf("Freitag: %v", iv)
	}
	// ganze Schicht, vom Freitag bis Samstag: 8 Stunden minus 2 Pausen (30 + 30 Minuten) = 420
	iv = p.intervals(wtAt(2026, 11, 27, 0, 0), wtAt(2026, 11, 29, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 420) {
		t.Errorf("ganze Schicht %v Minuten, erwartet 420", m)
	}
	// Zeitraum beginnt mitten in der Schicht (Samstag 02:00): Vortagsschicht zählt mit
	iv = p.intervals(wtAt(2026, 11, 28, 2, 0), wtAt(2026, 11, 28, 4, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 60+30) {
		t.Errorf("02–04 Uhr: %v Minuten, erwartet 90", m)
	}
	// Sonntag: nichts mehr
	if iv := p.intervals(wtAt(2026, 11, 29, 0, 0), wtAt(2026, 11, 30, 0, 0), workLocation); len(iv) != 0 {
		t.Errorf("Sonntag: %v", iv)
	}
}

// Sommerzeitumstellung: Die Nachtschicht ist am Umstellungstag 7 bzw. 9 Stunden lang, Tagschichten bleiben 8.
func TestWorkDSTDay(t *testing.T) {
	night := &workPlan{Defined: true, Shifts: []workShift{shift(6, "22:00", "06:00")}} // Samstag
	// 28./29.3.2026: Uhr springt 02:00 → 03:00
	iv := night.intervals(wtAt(2026, 3, 28, 0, 0), wtAt(2026, 3, 30, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 7*60) {
		t.Errorf("Nachtschicht Frühjahr: %v Minuten, erwartet 420", m)
	}
	// 24./25.10.2026: Uhr springt 03:00 → 02:00
	iv = night.intervals(wtAt(2026, 10, 24, 0, 0), wtAt(2026, 10, 26, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 9*60) {
		t.Errorf("Nachtschicht Herbst: %v Minuten, erwartet 540", m)
	}
	// Pause nach Mitternacht in der Umstellungsnacht: wirkliche Zeit (3 Uhr gibt es im Frühjahr als erste Stunde nach 1:59)
	withBreak := &workPlan{Defined: true, Shifts: []workShift{shift(6, "22:00", "06:00", "01:00", "01:30")}}
	iv = withBreak.intervals(wtAt(2026, 3, 28, 0, 0), wtAt(2026, 3, 30, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 7*60-30) {
		t.Errorf("Nachtschicht mit Pause Frühjahr: %v Minuten, erwartet 390", m)
	}
	// Tagschicht am Umstellungstag selbst (Sonntag 29.3.): 06–14 Uhr = 8 Stunden
	day := &workPlan{Defined: true, Shifts: []workShift{shift(7, "06:00", "14:00")}}
	iv = day.intervals(wtAt(2026, 3, 29, 0, 0), wtAt(2026, 3, 30, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 8*60) {
		t.Errorf("Tagschicht Umstellungstag: %v Minuten", m)
	}
	// Schicht, die in die fehlende Stunde fällt, bleibt gültig (kein negativer oder doppelter Abschnitt)
	gap := &workPlan{Defined: true, Shifts: []workShift{shift(7, "01:00", "04:00")}}
	iv = gap.intervals(wtAt(2026, 3, 29, 0, 0), wtAt(2026, 3, 30, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 2*60) {
		t.Errorf("Schicht über die Lücke: %v Minuten, erwartet 120", m)
	}
}

// Ausnahmen ersetzen den Wochentag: geschlossen oder mit eigenen Schichten.
func TestWorkExceptions(t *testing.T) {
	p := &workPlan{Defined: true,
		Shifts: []workShift{shift(1, "06:00", "14:00"), shift(2, "06:00", "14:00")},
		Exceptions: []workException{
			{Day: "2026-11-23", Closed: true},
			{Day: "2026-11-24", Shifts: []workShift{shift(0, "08:00", "10:00")}},
		}}
	if iv := p.intervals(wtAt(2026, 11, 23, 0, 0), wtAt(2026, 11, 24, 0, 0), workLocation); len(iv) != 0 {
		t.Errorf("geschlossen: %v", iv)
	}
	iv := p.intervals(wtAt(2026, 11, 24, 0, 0), wtAt(2026, 11, 25, 0, 0), workLocation)
	if m := plannedWorkMinutes(iv); !near(m, 120) {
		t.Errorf("abweichende Zeiten: %v Minuten, erwartet 120", m)
	}
	// ohne Plan: keine Intervalle
	if iv := (&workPlan{}).intervals(wtAt(2026, 11, 23, 0, 0), wtAt(2026, 11, 30, 0, 0), workLocation); len(iv) != 0 {
		t.Errorf("ohne Plan: %v", iv)
	}
}

// Überlappende Schichten werden vereinigt, nichts wird doppelt gezählt.
func TestWorkOverlappingShiftsMerged(t *testing.T) {
	p := &workPlan{Defined: true, Shifts: []workShift{shift(1, "06:00", "14:00"), shift(1, "12:00", "18:00")}}
	iv := p.intervals(wtAt(2026, 11, 23, 0, 0), wtAt(2026, 11, 24, 0, 0), workLocation)
	if len(iv) != 1 || !near(plannedWorkMinutes(iv), 12*60) {
		t.Errorf("vereinigt: %v", iv)
	}
}

// Auswertung der Stillstände: nur Überlappung mit der Arbeitszeit (ohne Pausen und Zeit außerhalb der Schicht).
func TestDowntimeWorkOverlap(t *testing.T) {
	const a, b, c = "a", "b", "c" // c hat keine Arbeitszeiten
	plan := &workPlan{Defined: true, Shifts: []workShift{shift(1, "06:00", "14:00", "09:00", "09:30")}}
	from, to := wtAt(2026, 11, 23, 0, 0), wtAt(2026, 11, 24, 0, 0)
	iv := plan.intervals(from, to, workLocation)
	work := map[string]plantWork{a: {true, iv}, b: {true, iv}, c: {false, nil}}
	ivs := []stillstand.Interval{
		// 05:00–07:00: nur 06–07 in der Arbeitszeit = 60
		{InfraID: a, Reason: "Material fehlt", Start: wtAt(2026, 11, 23, 5, 0), End: wtAt(2026, 11, 23, 7, 0)},
		// 09:00–09:30: genau die Pause: zählt nicht
		{InfraID: a, Reason: "Material fehlt", Start: wtAt(2026, 11, 23, 9, 0), End: wtAt(2026, 11, 23, 9, 30)},
		// 08:30–10:00: 30 vor der Pause + 30 nach der Pause = 60
		{InfraID: b, Reason: "Störung", Start: wtAt(2026, 11, 23, 8, 30), End: wtAt(2026, 11, 23, 10, 0)},
		// 15:00–16:00: nach der Schicht: zählt nicht
		{InfraID: b, Reason: "Störung", Start: wtAt(2026, 11, 23, 15, 0), End: wtAt(2026, 11, 23, 16, 0)},
		// ohne Arbeitszeiten: ganze Dauer
		{InfraID: c, Reason: "Störung", Start: wtAt(2026, 11, 23, 15, 0), End: wtAt(2026, 11, 23, 16, 0)},
	}
	got := aggregateDowntime(ivs, work, true)
	if got.Count != 3 || !near(got.Minutes, 60+60+60) {
		t.Fatalf("Arbeitszeit: Anzahl %d, Minuten %v; erwartet 3 und 180", got.Count, got.Minutes)
	}
	if !near(got.WorkMinutes, 120) || !near(got.Planned, 2*450) {
		t.Errorf("WorkMinutes %v (120), Planned %v (900)", got.WorkMinutes, got.Planned)
	}
	byName := map[string]downtimeAggRow{}
	for _, r := range got.Reasons {
		byName[r.Name] = r
	}
	if r := byName["Material fehlt"]; r.Count != 1 || !near(r.Minutes, 60) {
		t.Errorf("Material fehlt: %+v", r)
	}
	if r := byName["Störung"]; r.Count != 2 || !near(r.Minutes, 120) || !near(r.WorkMinutes, 60) {
		t.Errorf("Störung: %+v", r)
	}
	if len(got.Plants) != 3 {
		t.Errorf("je Anlage und Grund: %+v", got.Plants)
	}
	// Rohsummen: alles zählt mit voller Dauer
	raw := aggregateDowntime(ivs, work, false)
	if raw.Count != 5 || !near(raw.Minutes, 120+30+90+60+60) {
		t.Errorf("Rohsumme: Anzahl %d, Minuten %v; erwartet 5 und 360", raw.Count, raw.Minutes)
	}
	if !near(raw.WorkMinutes, 120) {
		t.Errorf("Rohsumme, davon in Arbeitszeit: %v, erwartet 120", raw.WorkMinutes)
	}
	if s := downtimeShare(120, 900); s != "13,3 %" {
		t.Errorf("Anteil: %q", s)
	}
	if downtimeShare(1, 0) != "–" {
		t.Error("Anteil ohne Arbeitszeit")
	}
}

func TestWorkOverlapMinutes(t *testing.T) {
	iv := []workInterval{{wtAt(2026, 11, 23, 6, 0), wtAt(2026, 11, 23, 9, 0)}, {wtAt(2026, 11, 23, 9, 30), wtAt(2026, 11, 23, 14, 0)}}
	if m := workOverlapMinutes(iv, wtAt(2026, 11, 23, 8, 0), wtAt(2026, 11, 23, 10, 0)); !near(m, 60+30) {
		t.Errorf("%v", m)
	}
	if m := workOverlapMinutes(iv, wtAt(2026, 11, 23, 14, 0), wtAt(2026, 11, 23, 15, 0)); m != 0 {
		t.Errorf("%v", m)
	}
}

// Rechte-Schranke: Arbeitszeiten ändern verlangt Infrastruktur bearbeiten (in der Abteilung der Anlage).
func TestWorkTimesPermission(t *testing.T) {
	const path = "/infrastructure/01a2028a-49f9-7fc1-8d31-5c44d7a7ec2b/work-times-web"
	found := false
	for _, g := range gateRules {
		if strings.Contains(g.methods, "POST") && g.re.MatchString(path) && g.perm == "infrastructure.edit" {
			found = true
		}
	}
	if !found {
		t.Error("keine Schranke infrastructure.edit für work-times-web")
	}
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	w := setupDo(h.InfraWorkTimesWeb, "POST", path, setupTestID, "", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: Status %d", w.Code)
	}
	if len(asked) == 0 || asked[len(asked)-1] != permInfraEdit {
		t.Errorf("geprüft %v, erwartet %s", asked, permInfraEdit)
	}
}

// Darstellung: Reiter „Arbeitszeiten“ und Stillstandsseite mit Anteil an der Arbeitszeit.
func TestWorkTimesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	tab := workInfraTab{Defined: true, Own: true, Count: 1, Week: "7 Std. 30 Min.",
		Days: []workDayView{{Weekday: 1, Name: "Montag", Shifts: []workShiftView{{ID: "s1", Name: "Nacht", Start: "22:00", End: "06:00", Overnight: true, Len: "7 Std.",
			Breaks: []workBreakView{{ID: "b1", Start: "01:00", End: "01:30"}}}}}, {Weekday: 2, Name: "Dienstag"}},
		Exceptions: []workExcView{{ID: "e1", Day: "2026-12-24", Label: "Do, 24.12.2026", Note: "Heiligabend", Closed: true}},
		Models:     []workModelOption{{"m1", "3-Schicht"}}}
	var buf bytes.Buffer
	wt, err := tmpl.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.ExecuteTemplate(&buf, "work-times-tab", map[string]any{"T": tab, "ID": "a1", "CanEdit": true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Nacht", "22:00–06:00", "bis Folgetag", "Pause 01:00–01:30", "Heiligabend", "3-Schicht", `value="break_add"`, `value="exc_add"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Reiter enthält %q nicht", want)
		}
	}
	// geerbt und ohne Recht: keine Bearbeitungsformulare
	buf.Reset()
	ro := workInfraTab{Defined: true, SourceName: "Halle 1", Count: 1, Week: "8 Std.", Days: tab.Days}
	if err := wt.ExecuteTemplate(&buf, "work-times-tab", map[string]any{"T": ro, "ID": "a1", "CanEdit": false}); err != nil {
		t.Fatal(err)
	}
	if s := buf.String(); !strings.Contains(s, "Geerbt von „Halle 1“") || strings.Contains(s, `value="shift_add"`) || strings.Contains(s, `value="own_on"`) {
		t.Errorf("schreibgeschützt/geerbt falsch: %s", s)
	}

	d := DowntimePageData{WorkTime: true, Days: 7, Total: 2, Duration: "2 Std.", Planned: "15 Std.", Share: "13,3 %", NoPlan: 1,
		BasisOpts: []downtimeChoice{{"1", "Nur Arbeitszeit", true}, {"0", "Gesamte Zeit (Rohsummen)", false}},
		Rows:      []downtimeRow{{Name: "Material fehlt", Count: 2, Duration: "2 Std.", Avg: "1 Std.", Pct: 100, Share: "13,3 %"}},
		PlantRows: []downtimePlantRow{{Plant: "Halle / Linie", Reason: "Material fehlt", Count: 2, Duration: "2 Std.", Share: "13,3 %"}}}
	page := renderPage(t, tmpl, "downtime", d)
	for _, want := range []string{"Anteil an der Arbeitszeit", "13,3 %", "Je Anlage und Grund", "Gesamte Zeit (Rohsummen)", "ganzen Dauer"} {
		if !strings.Contains(page, want) {
			t.Errorf("Stillstandsseite enthält %q nicht", want)
		}
	}
	d.WorkTime = false
	if page := renderPage(t, tmpl, "downtime", d); strings.Contains(page, "<th>Anteil an der Arbeitszeit") || strings.Contains(page, "ganzen Dauer") {
		t.Error("Rohsummen zeigen Arbeitszeit-Spalte")
	}
}
