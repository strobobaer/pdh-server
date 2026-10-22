package web

import (
	"strings"
	"testing"
	"time"
)

func TestEasterAndHolidays(t *testing.T) {
	for y, want := range map[int]string{2024: "2024-03-31", 2025: "2025-04-20", 2026: "2026-04-05", 2027: "2027-03-28"} {
		if got := easterSunday(y).Format(dayISO); got != want {
			t.Errorf("Ostern %d: %s, erwartet %s", y, got, want)
		}
	}
	by := germanHolidays(2026, "BY")
	for d, name := range map[string]string{"2026-01-06": "Heilige Drei Könige", "2026-06-04": "Fronleichnam", "2026-11-01": "Allerheiligen", "2026-04-03": "Karfreitag"} {
		if by[d] != name {
			t.Errorf("BY %s: %q, erwartet %q", d, by[d], name)
		}
	}
	if _, ok := by["2026-10-31"]; ok {
		t.Error("Reformationstag ist in Bayern kein Feiertag")
	}
	sn := germanHolidays(2026, "SN")
	if sn["2026-11-18"] != "Buß- und Bettag" {
		t.Errorf("Buß- und Bettag 2026: %v", sn["2026-11-18"])
	}
	if len(germanHolidays(2026, "")) != 9 {
		t.Errorf("bundesweit 9 Feiertage, sind %d", len(germanHolidays(2026, "")))
	}
}

func TestMondayOf(t *testing.T) {
	sun := time.Date(2026, 10, 25, 15, 0, 0, 0, time.Local) // Sonntag
	if got := mondayOf(sun).Format(dayISO); got != "2026-10-19" {
		t.Errorf("Montag zu Sonntag: %s", got)
	}
	mon := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)
	if got := mondayOf(mon).Format(dayISO); got != "2026-10-19" {
		t.Errorf("Montag zu Montag: %s", got)
	}
}

func TestShiftTemplatesConsistent(t *testing.T) {
	for _, tp := range shiftTemplates {
		shorts := map[string]bool{}
		for _, s := range tp.Shifts {
			if !hhmmRe.MatchString(s.Start) || !hhmmRe.MatchString(s.End) || !colorRe.MatchString(s.Color) {
				t.Errorf("%s: Schicht %s ungültig", tp.Key, s.Short)
			}
			shorts[s.Short] = true
		}
		used := map[string]bool{}
		for _, p := range tp.Pattern {
			if p != "" && !shorts[p] {
				t.Errorf("%s: Muster nutzt unbekannte Schicht %q", tp.Key, p)
			}
			used[p] = true
		}
		for s := range shorts {
			if !used[s] {
				t.Errorf("%s: Schicht %s kommt im Muster nicht vor", tp.Key, s)
			}
		}
		if tp.Crews > 1 && tp.Crews*tp.CrewStep != len(tp.Pattern) {
			t.Errorf("%s: %d Mannschaften × %d Tage Versatz decken den Zyklus von %d Tagen nicht genau ab", tp.Key, tp.Crews, tp.CrewStep, len(tp.Pattern))
		}
		if templateByKey(tp.Key) == nil {
			t.Errorf("%s nicht auffindbar", tp.Key)
		}
	}
}

// Kontischicht: zu jedem Zeitpunkt ist jede Schicht genau einmal besetzt.
func TestKontiCoversEveryShift(t *testing.T) {
	for _, key := range []string{"konti4", "konti5", "drei"} {
		tp := templateByKey(key)
		n := len(tp.Pattern)
		for day := 0; day < n; day++ {
			count := map[string]int{}
			for crew := 0; crew < tp.Crews; crew++ {
				off := crew * tp.CrewStep
				count[tp.Pattern[((day-off)%n+n)%n]]++
			}
			for _, s := range tp.Shifts {
				weekday := day % 7
				if key == "drei" && weekday >= 5 {
					continue // Mo–Fr-Modell
				}
				if count[s.Short] != 1 {
					t.Errorf("%s Tag %d: Schicht %s %d-mal besetzt", key, day, s.Short, count[s.Short])
				}
			}
		}
	}
}

func testPlannerData(tab string) PlannerData {
	d := PlannerData{Tab: tab, CanPlan: true, CanApprove: true, CanViewAll: true, CanRequest: true, CanMaintView: true, CanMaintEdit: true}
	return d
}

func TestPlannerShiftsRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := testPlannerData("shifts")
	d.Week, d.ThisWeek, d.WeekLabel, d.ModelID = "2026-10-19", "2026-10-19", "KW 43", "m1"
	d.Models = []plannerModel{{ID: "m1", Name: "3-Schicht", Shifts: 3, Active: true}, {ID: "m2", Name: "Tagschicht", Shifts: 1, Active: true}}
	for i := 0; i < 7; i++ {
		day := mondayOf(time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)).AddDate(0, 0, i)
		pd := plannerDay{Date: day.Format(dayISO), Label: day.Format("02.01."), Short: weekdayShort[day.Weekday()]}
		if i == 5 {
			pd.Weekend = true
		}
		d.Days = append(d.Days, pd)
	}
	d.Days[1].Holiday = "Testfeiertag"
	row := plannerRow{Shift: plannerShift{ID: "s1", Name: "Frühschicht", Short: "F", Color: "#10B981", Time: "06:00–14:00", MinStaff: 2}, Cells: make([]plannerCell, 7)}
	row.Cells[0] = plannerCell{People: []plannerPerson{{ID: "u1", Name: "Anna Muster"}}, Under: true}
	row.Cells[2] = plannerCell{People: []plannerPerson{{ID: "u1", Name: "Anna Muster"}, {ID: "u2", Name: "Ben Beispiel", Draft: true}}}
	d.Rows = []plannerRow{row}
	d.Users = []plannerUser{{ID: "u1", Name: "Anna Muster"}, {ID: "u2", Name: "Ben Beispiel"}}
	d.DayInfo = map[string]plannerDayInfo{"2026-10-19": {Shift: map[string]string{"u1": "F"}, ShiftID: map[string]string{"u1": "s1"}, Absent: map[string]string{"u2": "Urlaub"}}}
	d.DraftCount = 1
	out := renderPage(t, tmpl, "planner", d)
	for _, want := range []string{"Frühschicht", "Anna Muster", `data-people="u1,u2"`, `data-shift="s1"`, "Testfeiertag", "1/2", "pl-cell click under",
		"pl-chip draft", "model=m2", "/planner/shifts/copy-week", "Vorwoche übernehmen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Schichtplan enthält %q nicht", want)
		}
	}
	// ohne Planungsrecht: nur ansehen
	d.CanPlan = false
	out = renderPage(t, tmpl, "planner", d)
	if strings.Contains(out, "pl-cell click") || strings.Contains(out, `id="pl-pick"`) || strings.Contains(out, "tab=models") {
		t.Error("ohne Recht „Schichten planen“ darf nichts bearbeitbar sein")
	}
}

func TestPlannerOtherTabsRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := testPlannerData("absences")
	d.Abs = &absenceTabData{Year: 2026, PrevYear: 2025, NextYear: 2027, Today: "2026-10-21", MonthLabel: "Oktober 2026", Month: "2026-10",
		Me:      entitlementRow{Days: 30, Carry: 2, Taken: 10, Pending: 3, Rest: 22},
		Types:   []struct{ Key, Label string }{{"vacation", "Urlaub"}},
		Mine:    []absenceRow{{ID: "a1", TypeLabel: "Urlaub", From: "01.11.2026", To: "05.11.2026", Days: "5", StatusLabel: "beantragt", StatusClass: "b-amber"}},
		Pending: []absenceRow{{ID: "a2", UserName: "Ben Beispiel", TypeLabel: "Urlaub", From: "02.11.2026", To: "03.11.2026", Days: "2", Conflicts: []string{"2 Schicht(en) eingeteilt"}}},
		Days:    []plannerDay{{Date: "2026-10-01", Label: "1", Short: "Do"}},
		Cal:     []absenceCalRow{{UserID: "u2", Name: "Ben Beispiel", Cells: []absenceCalCell{{Class: "ab-vacation", Title: "Urlaub", Short: "U"}}}},
		Entitlements: []entitlementRow{{UserID: "u2", Name: "Ben Beispiel", Days: 30, DaysIn: "30", CarryIn: "0", Rest: -1}},
		Users:        []UserOption{{ID: "u2", Name: "Ben Beispiel"}}}
	out := renderPage(t, tmpl, "planner", d)
	for _, want := range []string{"/planner/absences/a2/decide", "2 Schicht(en) eingeteilt", "/planner/absences/a1/cancel", "ab-vacation", `action="/planner/entitlements"`, "year=2025", "remove_shifts"} {
		if !strings.Contains(out, want) {
			t.Errorf("Urlaub enthält %q nicht", want)
		}
	}

	d = testPlannerData("maintenance")
	md := maintDay{plannerDay: plannerDay{Date: "2026-10-21", Label: "21"}, InMonth: true, OnDuty: 3, Minutes: 90,
		Items: []maintItem{{ID: "t1", Kind: "maintenance", Title: "Ölwechsel Presse", Date: "2026-10-21", Assignee: "Anna", AssigneeID: "u1", Warn: "Anna ist abwesend (Urlaub)", WarnClass: "b-red", URL: "/maintenance/tasks/t1"}}}
	d.Maint = &maintTabData{MonthLabel: "Oktober 2026", Month: "2026-10", Weeks: [][]maintDay{{md}}, Avail: map[string][]UserOption{"2026-10-21": {{ID: "u2", Name: "Ben"}}},
		Users: []UserOption{{ID: "u1", Name: "Anna"}}, Warnings: 1}
	out = renderPage(t, tmpl, "planner", d)
	for _, want := range []string{"Ölwechsel Presse", "Anna ist abwesend", `data-id="t1"`, "1,5 h", "mt-dlg"} {
		if !strings.Contains(out, want) {
			t.Errorf("Wartungsplanung enthält %q nicht", want)
		}
	}

	d = testPlannerData("models")
	p := setupPattern{ID: "p1", Name: "Rhythmus", Cycle: 2, Crews: 2, CrewStep: 1, Days: []string{"s1", ""}}
	p.Slots = p.slots()
	d.Setup = &setupTabData{Templates: shiftTemplates, NextMonday: "2026-10-26",
		Models: []setupModel{{plannerModel: plannerModel{ID: "m1", Name: "3-Schicht", Active: true},
			Shifts:   []setupShift{{ID: "s1", Name: "Früh", Short: "F", Start: "06:00", End: "14:00", Color: "#10B981", Active: true}},
			Patterns: []setupPattern{p}}},
		Plans: []setupPlan{{ID: "pl1", Name: "Plan KW 44", Status: "draft", Count: 12}},
		Users: []plannerUser{{ID: "u1", Name: "Anna Muster"}}}
	out = renderPage(t, tmpl, "planner", d)
	for _, want := range []string{"/planner/templates/konti4/apply", "4-Schicht Kontischicht", "/planner/shift-defs/s1", "/planner/patterns/p1",
		`name="day_0"`, "Tag 2 · Di", "/planner/plans/generate", `name="crew_u1"`, "/planner/plans/pl1/publish", "F F S S N N – –"} {
		if !strings.Contains(out, want) {
			t.Errorf("Modelle enthalten %q nicht", want)
		}
	}

	d = testPlannerData("settings")
	d.Setup = &setupTabData{States: germanStates, State: "BY", VacationDef: "30", Year: 2026,
		Holidays: []setupClosure{{Day: "2026-06-04", Label: "Do 04.06.", Name: "Fronleichnam"}},
		Closures: []setupClosure{{Day: "2026-12-28", Label: "Mo 28.12.2026", Name: "Betriebsurlaub"}}}
	out = renderPage(t, tmpl, "planner", d)
	for _, want := range []string{`value="BY" selected`, "Fronleichnam", "/planner/closures/2026-12-28/delete", `action="/planner/settings"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Einstellungen enthalten %q nicht", want)
		}
	}
}

func TestCalendarRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	abs := &calChip{Kind: "absence", Short: "Urlaub", Tip: "Urlaub", Color: "#10B981", URL: "/planner?tab=absences"}
	sh := &calChip{Kind: "shift", Short: "F", Color: "#10B981", URL: "/planner?tab=shifts"}
	row := calRow{UserID: "u1", Name: "Anna Muster", Conflicts: 1, Cells: []calCell{
		{Date: "2026-10-22", Absence: abs, Shift: sh, Conflict: "Urlaub – aber Schicht F",
			Items: []calChip{{Kind: "maintenance", Title: "Ölwechsel", URL: "/maintenance/tasks/t1", Color: "#C99A3C", Icon: "ti-tool"}}},
		{Date: "2026-10-23", Warn: "Fällig an einem Tag ohne Schicht",
			Items: []calChip{{Kind: "project", Title: "Umbau Linie 2", URL: "/projects/p1", Color: "#64748B", Icon: "ti-timeline", SpanStart: true}}},
	}}
	d := CalendarData{Kinds: calKinds, Ranges: calRanges, Range: 14, From: "2026-10-22", Today: "2026-10-22", Label: "22.10. – 04.11.2026",
		Show: map[string]bool{"absence": true, "shift": true, "maintenance": true, "project": true}, Counts: map[string]int{"maintenance": 1},
		Days: []plannerDay{{Date: "2026-10-22", Label: "22.10.", Short: "Do", Today: true}, {Date: "2026-10-23", Label: "23.10.", Short: "Fr", Holiday: "Testtag"}},
		Rows: []calRow{row}, Conflicts: 1, Warns: 1, OnlyBusy: true}
	out := renderPage(t, tmpl, "calendar", d)
	for _, want := range []string{"Anna Muster", " clash", " warn", "Ölwechsel", "Umbau Linie 2", "/maintenance/tasks/t1", "1 Überschneidung", "Testtag",
		`name="show" value="ticket"`, `value="14" selected`} {
		if !strings.Contains(out, want) {
			t.Errorf("Kalender enthält %q nicht", want)
		}
	}
}
