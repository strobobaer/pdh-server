package web

import (
	"bytes"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Planungsfolien des Infobildschirms (terminal_plan.go, Migration 208): Zeiträume, Prüfung, Formular,
// Zwischenspeicher. Die Daten je Art stehen in terminal_plan_integration_test.go.

var planKinds = []string{slideKindPlanShifts, slideKindPlanAbsences, slideKindPlanDuties, slideKindPlanMaint}

func TestPlanRangeBounds(t *testing.T) {
	wed := time.Date(2026, 11, 25, 14, 30, 0, 0, time.Local) // Mittwoch
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.Local) }
	for _, c := range []struct {
		rng      string
		n        int
		from, to time.Time
	}{
		{planRangeToday, 0, day(11, 25), day(11, 25)},
		{planRangeWeek, 0, day(11, 23), day(11, 29)},
		{planRangeNextWeek, 0, day(11, 30), day(12, 6)},
		{planRangeDays, 1, day(11, 25), day(11, 25)},
		{planRangeDays, 3, day(11, 25), day(11, 27)},
		{planRangeDays, 31, day(11, 25), day(12, 25)},
		{planRangeDays, 0, day(11, 25), day(12, 1)},  // ungültig: Vorgabe 7
		{planRangeDays, 32, day(11, 25), day(12, 1)}, // ungültig: Vorgabe 7
		{"", 0, day(11, 25), day(11, 25)},
	} {
		from, to := planRangeBounds(wed, c.rng, c.n)
		if !from.Equal(c.from) || !to.Equal(c.to) {
			t.Errorf("%s/%d: %s bis %s, erwartet %s bis %s", c.rng, c.n, from.Format(dayISO), to.Format(dayISO), c.from.Format(dayISO), c.to.Format(dayISO))
		}
	}
	sun := time.Date(2026, 11, 29, 23, 59, 0, 0, time.Local) // Sonntag gehört zur laufenden Woche
	if f, _ := planRangeBounds(sun, planRangeWeek, 0); !f.Equal(day(11, 23)) {
		t.Errorf("Sonntag: Woche beginnt %s", f.Format(dayISO))
	}
	if planRangeLabel(planRangeDays, 5) != "nächste 5 Tage" || planRangeLabel(planRangeNextWeek, 0) != "nächste Woche" || planRangeLabel("", 0) != "heute" {
		t.Error("Zeitraum in Worten")
	}
}

func TestValidatePlanSlide(t *testing.T) {
	dept := "11111111-1111-4111-8111-111111111111"
	for _, kind := range planKinds {
		ok := terminalSlide{Kind: kind, PlanRange: planRangeDays, PlanDays: 31, DepartmentIDs: []string{dept}, Active: true}
		if err := validateTerminalSlide(&ok); err != nil {
			t.Errorf("%s gültig abgelehnt: %v", kind, err)
		}
		def := terminalSlide{Kind: kind} // Vorgaben
		if err := validateTerminalSlide(&def); err != nil || def.PlanRange != planRangeToday || def.PlanDays != planDefaultDays {
			t.Errorf("%s Vorgaben: %v %+v", kind, err, def)
		}
		for name, s := range map[string]terminalSlide{
			"Zeitraum":        {Kind: kind, PlanRange: "monat"},
			"zu wenige Tage":  {Kind: kind, PlanRange: planRangeDays, PlanDays: -1},
			"zu viele Tage":   {Kind: kind, PlanRange: planRangeDays, PlanDays: 32},
			"Abteilung":       {Kind: kind, DepartmentIDs: []string{"keine-uuid"}},
			"zu viele Abtlg.": {Kind: kind, DepartmentIDs: make([]string, planMaxDepts+1)},
		} {
			if err := validateTerminalSlide(&s); err == nil {
				t.Errorf("%s: %s wurde angenommen", kind, name)
			}
		}
	}
	// Bei anderen Zeiträumen als „days“ zählt N nicht; bei anderen Arten sind die Felder leer
	s := terminalSlide{Kind: slideKindPlanShifts, PlanRange: planRangeWeek, PlanDays: 99}
	if err := validateTerminalSlide(&s); err != nil || s.PlanDays != planDefaultDays {
		t.Errorf("Woche mit N: %v %+v", err, s)
	}
	o := terminalSlide{Kind: slideKindText, Title: "x", PlanRange: "unsinn", PlanDays: 99, DepartmentIDs: []string{dept}}
	if err := validateTerminalSlide(&o); err != nil || o.PlanRange != planRangeToday || len(o.DepartmentIDs) != 0 {
		t.Errorf("Textfolie trägt Planungsfelder: %v %+v", err, o)
	}
}

// Die Planungsfolien stehen nicht im Zwischenspeicher des Geräts und entfallen offline wie Website-Folien.
func TestPlanKindsNotCacheable(t *testing.T) {
	src := []byte(readStatic(t, "../../web/static/terminal.js"))
	kinds := regexp.MustCompile(`var CACHEABLE_KINDS = [^\n]*`).Find(src)
	if kinds == nil {
		t.Fatal("CACHEABLE_KINDS nicht gefunden")
	}
	if strings.Contains(string(kinds), "plan") {
		t.Errorf("Planungsfolien im Zwischenspeicher: %s", kinds)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node fehlt")
	}
	var fns []string
	for _, name := range []string{"showableIndex", "cachePayload"} {
		m := regexp.MustCompile(`(?s)\n  function ` + name + `\(.*?\n  \}\n`).Find(src)
		if m == nil {
			t.Fatalf("Funktion %s nicht gefunden", name)
		}
		fns = append(fns, string(m))
	}
	harness := string(kinds) + "\n" + strings.Join(fns, "\n") + `
const out = [];
const eq = (got, want, what) => { const g = JSON.stringify(got), w = JSON.stringify(want); if (g !== w) out.push(what + ': ' + g + ' statt ' + w); };
const txt = {id: 't', kind: 'text'};
['plan_shifts', 'plan_absences', 'plan_duties', 'plan_maint'].forEach(function (k) {
  const p = {id: k, kind: k};
  eq(showableIndex([txt, p, txt], 1, false), 1, k + ' online');
  eq(showableIndex([txt, p, txt], 1, true), 2, k + ' offline übersprungen');
  eq(showableIndex([p, p], 0, true), -1, k + ' nur Planung, offline');
  eq(cachePayload({version: 'v', slides: [txt, p]}, {}).slides.map(s => s.id), ['t'], k + ' nicht im Zwischenspeicher');
});
console.log(out.join('\n'));
`
	cmd := exec.Command(node, "-e", harness)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, e.String())
	}
	if s := strings.TrimSpace(o.String()); s != "" {
		t.Error(s)
	}
}

// Der Player setzt Planungsdaten nur über textContent ein und prüft die Farbe; es gibt keine Art/Grund-Felder.
func TestPlanPlayerSafe(t *testing.T) {
	js := readStatic(t, "../../web/static/terminal.js")
	i := strings.Index(js, "var drawPlan = function")
	j := strings.Index(js, "var draw = function")
	if i < 0 || j < i {
		t.Fatal("drawPlan nicht gefunden")
	}
	body := js[i:j]
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(body, bad) {
			t.Errorf("drawPlan nutzt %s", bad)
		}
	}
	if !strings.Contains(body, "/^#[0-9a-fA-F]{6}$/.test(g.color)") {
		t.Error("Gruppenfarbe wird nicht geprüft")
	}
}

// Abwesenheiten: die Abfrage liest weder Art noch Notiz; ins JSON gelangt nur „abwesend“.
func TestPlanAbsenceQueryReadsNoReason(t *testing.T) {
	src := readStatic(t, "terminal_plan.go")
	i := strings.Index(src, "func (h *Handler) planAbsenceRows")
	j := strings.Index(src, "func (h *Handler) planDutyRows")
	if i < 0 || j < i {
		t.Fatal("planAbsenceRows nicht gefunden")
	}
	body := src[i:j]
	for _, bad := range []string{"a.type", "a.note", "a.decision_note", "absenceTypeLabels", "type::text", "view_all"} {
		if strings.Contains(body, bad) {
			t.Errorf("planAbsenceRows greift auf %q zu", bad)
		}
	}
	if absentLabelPlain != "abwesend" {
		t.Error("Beschriftung")
	}
}

func TestPlanFormTemplate(t *testing.T) {
	form := readStatic(t, "../../web/templates/terminal_slide_form.gohtml")
	for _, want := range []string{`name="plan_range"`, `name="plan_days"`, `name="department_ids"`, `value="plan_shifts"`, `value="plan_absences"`,
		`value="plan_duties"`, `value="plan_maint"`, `min="1" max="31"`} {
		if !strings.Contains(form, want) {
			t.Errorf("Formular enthält %q nicht", want)
		}
	}
}

// ── mit Datenbank: Formular der Verwaltungsseite ──

func TestTerminalSlidesWebPlanKinds(t *testing.T) {
	e := newSlidesEnv(t)
	d1, d2 := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	for _, kind := range planKinds {
		form := url.Values{"kind": {kind}, "title": {"Plan " + kind}, "plan_range": {"days"}, "plan_days": {"5"}, "department_ids": {d1, d2, d1}, "active": {"1"}}
		wantRedirect(t, e.do("POST", "/terminal-slides", []byte(form.Encode()), "application/x-www-form-urlencoded"), "created")
	}
	got := e.slides(t)
	if len(got) != 4 {
		t.Fatalf("Folien: %d", len(got))
	}
	for i, s := range got {
		if s.Kind != planKinds[i] || s.PlanRange != "days" || s.PlanDays != 5 || len(s.DepartmentIDs) != 2 || s.DepartmentIDs[0] != d1 || s.DepartmentIDs[1] != d2 || !s.Active {
			t.Errorf("%s: %+v", planKinds[i], s)
		}
	}
	// Vorgaben: ohne Angaben heute und alle Abteilungen
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": slideKindPlanShifts, "active": "1"}, nil), "created")
	if s := e.slides(t)[4]; s.PlanRange != "today" || s.PlanDays != 7 || len(s.DepartmentIDs) != 0 {
		t.Errorf("Vorgaben: %+v", s)
	}
	// Liste und Formulare rendern; die Liste nennt Zeitraum und Abteilungen
	rec := e.do("GET", "/terminal-slides", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "nächste 5 Tage · 2 Abteilungen") || !strings.Contains(rec.Body.String(), "heute · alle Abteilungen") {
		t.Errorf("Liste: %d", rec.Code)
	}
	for _, k := range planKinds {
		if rec := e.do("GET", "/terminal-slides/new?kind="+k, nil, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="plan_range"`) {
			t.Errorf("neu %s: %d", k, rec.Code)
		}
	}
	rec = e.do("GET", "/terminal-slides/"+got[0].ID, nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<option value="days" selected>`) || !strings.Contains(rec.Body.String(), `value="5"`) {
		t.Errorf("Ändern-Formular: %d", rec.Code)
	}
	// Ändern auf anderen Zeitraum; der Rest bleibt gültig
	wantRedirect(t, e.post("/terminal-slides/"+got[0].ID, map[string]string{"kind": slideKindPlanShifts, "plan_range": "next_week", "plan_days": "40", "active": "1"}, nil), "saved")
	if s := e.slides(t)[0]; s.PlanRange != "next_week" || s.PlanDays != 7 || len(s.DepartmentIDs) != 0 {
		t.Errorf("geändert: %+v", s)
	}
}

func TestTerminalSlidesWebPlanInvalidInput(t *testing.T) {
	e := newSlidesEnv(t)
	for name, c := range map[string]struct {
		fields map[string]string
		want   string
	}{
		"Zeitraum unbekannt": {map[string]string{"kind": slideKindPlanShifts, "plan_range": "monat"}, "Ungültiger Zeitraum"},
		"N null":             {map[string]string{"kind": slideKindPlanAbsences, "plan_range": "days", "plan_days": "0"}, "von 1 bis 31"},
		"N 32":               {map[string]string{"kind": slideKindPlanDuties, "plan_range": "days", "plan_days": "32"}, "von 1 bis 31"},
		"N negativ":          {map[string]string{"kind": slideKindPlanMaint, "plan_range": "days", "plan_days": "-3"}, "von 1 bis 31"},
		"N Text":             {map[string]string{"kind": slideKindPlanMaint, "plan_range": "days", "plan_days": "viele"}, "von 1 bis 31"},
		"Abteilung":          {map[string]string{"kind": slideKindPlanShifts, "department_ids": "keine-uuid"}, "Ungültige Abteilung"},
		"Abteilungen viele":  {map[string]string{"kind": slideKindPlanShifts, "department_ids": manyUUIDs(planMaxDepts + 1)}, "höchstens 100 Abteilungen"},
		"Dauer":              {map[string]string{"kind": slideKindPlanShifts, "duration": "4"}, "zwischen 5 und 600"},
	} {
		rec := e.post("/terminal-slides", c.fields, nil)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.want) || !strings.Contains(rec.Body.String(), `name="plan_range"`) {
			t.Errorf("%s: Status %d, Meldung %q fehlt", name, rec.Code, c.want)
		}
	}
	// Grenzen gehen durch: N = 1 und N = 31
	for _, n := range []string{"1", "31"} {
		wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": slideKindPlanShifts, "plan_range": "days", "plan_days": n}, nil), "created")
	}
	if got := e.slides(t); len(got) != 2 || got[0].PlanDays != 1 || got[1].PlanDays != 31 {
		t.Errorf("Grenzen: %+v", got)
	}
}
