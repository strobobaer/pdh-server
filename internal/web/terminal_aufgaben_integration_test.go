//go:build integration

// Aufgaben- und Projektfolien des Infobildschirms (Migration 210): Zeitraum und Überfälliges, Abteilungsauswahl,
// Schnittmenge mit den Abteilungen des Terminals, Recht je Abteilung, „ohne Abteilung“ nur bei globalem Recht,
// Jahresband über zwei Kalenderjahre, Seiten.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TerminalWork ./internal/web/
package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type gtWorkSlide struct {
	Kind  string            `json:"kind"`
	Tasks *tasksSlideData   `json:"tasks"`
	Year  *projectsYearData `json:"year"`
}

type workEnv struct {
	*gtEnv
	deptA, deptB, pA, pB, iA, iB string
	today, endWeek               time.Time
}

func newWorkEnv(t *testing.T) *workEnv {
	t.Helper()
	g := newGTEnv(t)
	e := g.e
	w := &workEnv{gtEnv: g}
	w.deptA, w.deptB = e.dept("WA"), e.dept("WB")
	person := func(name string) string {
		u := e.user(name, "employee", false)
		e.exec(`UPDATE users SET first_name = $2, last_name = 'Werk' WHERE id = $1::uuid`, u, name)
		return u
	}
	w.pA, w.pB = person("Anton"), person("Berta")
	w.iA, w.iB = e.infra("WAnlageA ", w.deptA, ""), e.infra("WAnlageB ", w.deptB, "")
	now := time.Now()
	w.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	w.endWeek = taskRangeEnd(now, taskRangeWeek, "")

	task := func(title, infra, status string, due *time.Time, archived bool, assignees ...string) {
		id := e.id(`INSERT INTO tasks (title, status, due_date, infrastructure_id, color, archived_at, created_by)
			VALUES ($1::varchar, $2, $3, NULLIF($4,'')::uuid, CASE WHEN $1::text = 'T-Alt-A' THEN '#336699' ELSE NULL END, CASE WHEN $5 THEN NOW() END, $6::uuid) RETURNING id::text`,
			title, status, due, infra, archived, w.pA)
		for _, a := range assignees {
			e.exec(`INSERT INTO task_assignees (task_id, user_id) VALUES ($1::uuid, $2::uuid)`, id, a)
		}
	}
	d := func(offset int) *time.Time { x := w.today.AddDate(0, 0, offset); return &x }
	at := func(x time.Time) *time.Time { return &x }
	task("T-Alt-A", w.iA, "open", d(-3), false, w.pA, w.pB)                      // überfällig
	task("T-Heute-A", w.iA, "in_progress", d(0), false, w.pB)                    // heute
	task("T-Morgen-B", w.iB, "open", d(1), false)                                // morgen, Abteilung B
	task("T-Naechste-A", w.iA, "pending", at(w.endWeek.AddDate(0, 0, 3)), false) // nächste Woche
	task("T-Woche4-A", w.iA, "open", at(w.endWeek.AddDate(0, 0, 21)), false)     // letzter Tag der 4. Woche
	task("T-Fern-A", w.iA, "open", at(w.endWeek.AddDate(0, 0, 22)), false)       // erste Tag der 5. Woche
	task("T-Erledigt-A", w.iA, "resolved", d(0), false)
	task("T-Zu-A", w.iA, "closed", d(-5), false)
	task("T-Archiv-A", w.iA, "closed", d(0), true) // (Migration 108: offene Aufgaben sind nie archiviert)
	task("T-OhneDatum-A", w.iA, "open", nil, false)
	task("T-OhneAnlage", "", "open", d(0), false) // keine Anlage: ohne Abteilung
	t.Cleanup(func() {
		e.exec(`DELETE FROM task_assignees WHERE task_id IN (SELECT id FROM tasks WHERE created_by = $1::uuid)`, w.pA)
		e.exec(`DELETE FROM tasks WHERE created_by = $1::uuid`, w.pA)
		e.exec(`DELETE FROM projects WHERE created_by = $1::uuid`, w.pA)
	})
	// Das globale Terminal darf überall Aufgaben und Projekte ansehen
	e.grant(g.termG, "tasks.view", "", true)
	e.grant(g.termG, "projects.view", "", true)
	e.warm()
	return w
}

func (w *workEnv) docs(term, dev string) []gtWorkSlide {
	w.e.t.Helper()
	rec := w.do("GET", "/terminal/slides.json", w.cookie(term, dev), nil)
	if rec.Code != 200 {
		w.e.t.Fatalf("slides.json: %d %s", rec.Code, rec.Body.String())
	}
	var d struct {
		Slides []gtWorkSlide `json:"slides"`
	}
	w.e.must(json.Unmarshal(rec.Body.Bytes(), &d))
	return d.Slides
}

func (w *workEnv) clear() { w.e.exec(`DELETE FROM terminal_slides`) }

// flatTasks: "überfällig|Titel|Anlage|Zuständige" und "Datum|Titel" der eigenen Aufgaben (Titel T-…); die gemeinsame
// Testdatenbank enthält weitere Aufgaben.
func flatTasks(tk *tasksSlideData) []string {
	if tk == nil {
		return []string{"<keine Folie>"}
	}
	var out []string
	for _, it := range tk.Overdue {
		if strings.HasPrefix(it.Title, "T-") {
			out = append(out, "ÜBERFÄLLIG|"+it.Title+"|"+strings.TrimSpace(strings.Split(it.Plant, " ")[0])+"|"+strings.Join(it.Assignees, "+"))
		}
	}
	for _, wk := range tk.Weeks {
		for _, d := range wk.Days {
			for _, it := range d.Items {
				if strings.HasPrefix(it.Title, "T-") {
					out = append(out, d.Date+"|"+it.Title)
				}
			}
		}
	}
	return out
}

func wantTasks(t *testing.T, what string, tk *tasksSlideData, want ...string) {
	t.Helper()
	got := flatTasks(tk)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n  ist      %q\n  erwartet %q", what, got, want)
	}
}

func TestTerminalWorkTasksRangeAndOverdue(t *testing.T) {
	w := newWorkEnv(t)
	w.clear()
	dA := []string{w.deptA}
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "week", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "2w", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "4w", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "8w", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: w.today.AddDate(0, 0, 1).Format(dayISO), DepartmentIDs: []string{w.deptA, w.deptB}, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: w.today.AddDate(0, 0, -30).Format(dayISO), DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	got := w.docs(w.termG, w.devG)
	if len(got) != 6 {
		t.Fatalf("Folien: %d", len(got))
	}
	day := func(d time.Time) string { return d.Format(dayISO) }
	alt := "ÜBERFÄLLIG|T-Alt-A|WAnlageA|Anton Werk+Berta Werk"
	heute := day(w.today) + "|T-Heute-A"
	naechste := day(w.endWeek.AddDate(0, 0, 3)) + "|T-Naechste-A"
	woche4 := day(w.endWeek.AddDate(0, 0, 21)) + "|T-Woche4-A"
	fern := day(w.endWeek.AddDate(0, 0, 22)) + "|T-Fern-A"
	// erledigte, geschlossene, archivierte und Aufgaben ohne Fälligkeit fehlen überall
	wantTasks(t, "bis Ende der Woche", got[0].Tasks, alt, heute)
	wantTasks(t, "2 Wochen", got[1].Tasks, alt, heute, naechste)
	wantTasks(t, "4 Wochen", got[2].Tasks, alt, heute, naechste, woche4)
	wantTasks(t, "8 Wochen", got[3].Tasks, alt, heute, naechste, woche4, fern)
	wantTasks(t, "bis morgen, beide Abteilungen", got[4].Tasks, alt, heute, day(w.today.AddDate(0, 0, 1))+"|T-Morgen-B")
	// Enddatum vor heute: überfällige Aufgaben erscheinen trotzdem, sonst nichts
	wantTasks(t, "Ende in der Vergangenheit", got[5].Tasks, alt)
	// Angaben je Aufgabe
	var it taskSlideItem
	for _, x := range got[0].Tasks.Overdue {
		if x.Title == "T-Alt-A" {
			it = x
		}
	}
	if !it.Overdue || it.Color != "#336699" || it.Status != "open" || it.StatusLabel != "Offen" || it.Due != w.today.AddDate(0, 0, -3).Format("02.01.") || !strings.HasPrefix(it.Plant, "WAnlageA") {
		t.Errorf("überfällige Aufgabe: %+v", it)
	}
	for _, wk := range got[0].Tasks.Weeks {
		for _, d := range wk.Days {
			for _, x := range d.Items {
				if x.Title == "T-Heute-A" && (x.Overdue || x.StatusLabel != "In Arbeit" || len(x.Assignees) != 1 || x.Assignees[0] != "Berta Werk" || !d.Today) {
					t.Errorf("heutige Aufgabe: %+v today=%v", x, d.Today)
				}
			}
		}
	}
	// Die Version von slides.json hängt nicht von den Aufgaben ab
	v1 := w.do("GET", "/terminal/slides.json", w.cookie(w.termG, w.devG), nil).Body.String()
	w.e.exec(`UPDATE tasks SET title = 'T-Heute-A' WHERE title = 'T-Heute-A' AND created_by = $1::uuid`, w.pA)
	w.e.exec(`UPDATE tasks SET due_date = due_date + 1 WHERE title = 'T-Morgen-B' AND created_by = $1::uuid`, w.pA)
	v2 := w.do("GET", "/terminal/slides.json", w.cookie(w.termG, w.devG), nil).Body.String()
	var a, b struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal([]byte(v1), &a)
	_ = json.Unmarshal([]byte(v2), &b)
	if a.Version == "" || a.Version != b.Version {
		t.Errorf("Version: %q / %q", a.Version, b.Version)
	}
}

func TestTerminalWorkTasksDepartmentIntersectionAndPermission(t *testing.T) {
	w := newWorkEnv(t)
	w.clear()
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: w.today.AddDate(0, 0, 1).Format(dayISO), DurationSeconds: 10, Active: true})                                            // alle
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: w.today.AddDate(0, 0, 1).Format(dayISO), DepartmentIDs: []string{w.deptA, w.deptB}, DurationSeconds: 10, Active: true}) // A+B
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: w.today.AddDate(0, 0, 1).Format(dayISO), DepartmentIDs: []string{w.deptB}, DurationSeconds: 10, Active: true})          // nur B
	tm := w.today.AddDate(0, 0, 1).Format(dayISO)
	hd := w.today.Format(dayISO)
	a := "ÜBERFÄLLIG|T-Alt-A|WAnlageA|Anton Werk+Berta Werk"
	h := hd + "|T-Heute-A"
	b := tm + "|T-Morgen-B"
	o := hd + "|T-OhneAnlage"
	check := func(what string, got []gtWorkSlide, w0, w1, w2 []string) {
		t.Helper()
		if len(got) != 3 {
			t.Fatalf("%s: %d Folien", what, len(got))
		}
		wantTasks(t, what+" Folie alle", got[0].Tasks, w0...)
		wantTasks(t, what+" Folie A+B", got[1].Tasks, w1...)
		wantTasks(t, what+" Folie B", got[2].Tasks, w2...)
	}
	// globales Recht, keine Terminalgrenze: ohne Auswahl auch Aufgaben ohne Anlage; mit Auswahl nur der Abteilungen
	check("ohne Terminalgrenze", w.docs(w.termG, w.devG), []string{a, h, o, b}, []string{a, h, b}, []string{b})
	// Terminal auf Abteilung A begrenzt: Schnittmenge, Aufgaben ohne Anlage entfallen
	w.e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid] WHERE user_id = $1::uuid`, w.termG, w.deptA)
	check("Terminal nur A", w.docs(w.termG, w.devG), []string{a, h}, []string{a, h}, nil)
	// Terminal auf A und B
	w.e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid, $3::uuid] WHERE user_id = $1::uuid`, w.termG, w.deptA, w.deptB)
	check("Terminal A+B", w.docs(w.termG, w.devG), []string{a, h, b}, []string{a, h, b}, []string{b})
	w.e.exec(`UPDATE terminal_settings SET abteilungen = NULL WHERE user_id = $1::uuid`, w.termG)

	// Recht nur in B: Aufgaben der Abteilung A und ohne Anlage fehlen
	w.e.grant(w.termNoTickets, "tasks.view", w.deptB, true)
	w.e.warm()
	check("Recht nur B", w.docs(w.termNoTickets, w.devNT), []string{b}, []string{b}, []string{b})
	// Recht nur in A
	w.e.exec(`UPDATE user_permissions SET department_id = $2::uuid WHERE user_id = $1::uuid AND department_id = $3::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'tasks.view')`, w.termNoTickets, w.deptA, w.deptB)
	w.e.warm()
	check("Recht nur A", w.docs(w.termNoTickets, w.devNT), []string{a, h}, []string{a, h}, nil)
	// Verbot: gar nichts
	w.e.exec(`UPDATE user_permissions SET granted = false, department_id = NULL WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'tasks.view')`, w.termNoTickets)
	w.e.warm()
	check("ohne Recht", w.docs(w.termNoTickets, w.devNT), nil, nil, nil)
	// Das Projektrecht ersetzt das Aufgabenrecht nicht
	w.clear()
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "week", DepartmentIDs: []string{w.deptA}, DurationSeconds: 10, Active: true})
	w.e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, w.termNoTickets)
	w.e.grant(w.termNoTickets, "projects.view", "", true)
	w.e.warm()
	if got := w.docs(w.termNoTickets, w.devNT); len(got) != 1 || len(flatTasks(got[0].Tasks)) != 0 {
		t.Errorf("Projektrecht zeigt Aufgaben: %v", flatTasks(got[0].Tasks))
	}
}

// Aufgaben ohne Anlage erscheinen nur, wenn das Recht überall gilt, nicht bei einem Recht nur in einer Abteilung.
func TestTerminalWorkTasksNoDeptNeedsGlobalRight(t *testing.T) {
	w := newWorkEnv(t)
	w.clear()
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "week", DurationSeconds: 10, Active: true})
	has := func(term, dev string) bool {
		return strings.Contains(strings.Join(flatTasks(w.docs(term, dev)[0].Tasks), ","), "T-OhneAnlage")
	}
	if !has(w.termG, w.devG) {
		t.Error("globales Recht: Aufgabe ohne Anlage fehlt")
	}
	w.e.grant(w.termNoTickets, "tasks.view", w.deptA, true)
	w.e.warm()
	if has(w.termNoTickets, w.devNT) {
		t.Error("Recht nur in Abteilung A: Aufgabe ohne Anlage erscheint")
	}
}

func (w *workEnv) project(name, status, infra string, start, end *time.Time) {
	w.e.exec(`INSERT INTO projects (name, status, start_date, end_date, infrastructure_id, created_by) VALUES ($1, $2, $3, $4, NULLIF($5,'')::uuid, $6::uuid)`,
		name, status, start, end, infra, w.pA)
}

func flatBars(y *projectsYearData) []string {
	if y == nil {
		return []string{"<keine Folie>"}
	}
	var out []string
	for _, p := range y.Pages {
		for _, b := range p.Bars {
			if strings.HasPrefix(b.Name, "PJ-") {
				out = append(out, b.Name)
			}
		}
	}
	return out
}

func TestTerminalWorkProjectsYear(t *testing.T) {
	w := newWorkEnv(t)
	w.clear()
	yr := w.today.Year()
	at := func(y int, m time.Month, d int) *time.Time {
		x := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
		return &x
	}
	w.project("PJ-Laufend", "active", w.iA, at(yr, 1, 10), at(yr, 12, 31))
	w.project("PJ-Planung-Naechstes", "planning", w.iA, at(yr, 12, 31), at(yr+1, 1, 15))
	w.project("PJ-Ueber-Jahreswechsel", "active", w.iA, at(yr, 12, 31), at(yr+1, 12, 31)) // reicht über jedes 12-Monats-Band hinaus
	w.project("PJ-Fertig", "completed", w.iA, at(yr, 2, 1), at(yr, 3, 1))
	w.project("PJ-Alt", "active", w.iA, at(yr-3, 1, 1), at(yr-2, 1, 1))
	w.project("PJ-OhneDatum", "active", w.iA, nil, nil)
	w.project("PJ-NurEnde", "paused", w.iA, nil, at(yr, 6, 30))
	w.project("PJ-B", "active", w.iB, at(yr, 4, 1), at(yr, 4, 30))
	w.project("PJ-OhneAnlage", "active", "", at(yr, 4, 1), at(yr, 4, 30))
	dA, dB := []string{w.deptA}, []string{w.deptB}
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "next", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "rolling", DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", OnlyActive: true, DepartmentIDs: dA, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: dB, DurationSeconds: 10, Active: true})
	got := w.docs(w.termG, w.devG)
	if len(got) != 5 {
		t.Fatalf("Folien: %d", len(got))
	}
	same := func(what string, y *projectsYearData, want ...string) {
		t.Helper()
		if g := flatBars(y); strings.Join(g, ",") != strings.Join(want, ",") {
			t.Errorf("%s: %v statt %v", what, g, want)
		}
	}
	// Sortiert nach Start; ohne Datum fehlt, nur ein Ende zählt als Eintagesbalken; das Projekt bis Ende Dezember des Folgejahres liegt in beiden Jahren
	same("aktuelles Jahr", got[0].Year, "PJ-Laufend", "PJ-Fertig", "PJ-NurEnde", "PJ-Planung-Naechstes", "PJ-Ueber-Jahreswechsel")
	same("nächstes Jahr", got[1].Year, "PJ-Planung-Naechstes", "PJ-Ueber-Jahreswechsel")
	same("nur aktive", got[3].Year, "PJ-Laufend", "PJ-Ueber-Jahreswechsel")
	same("Abteilung B", got[4].Year, "PJ-B")
	// 12 Monate ab heute: Projekte bis Jahresende und über den Jahreswechsel; nicht die alten, undatierten, anderer Abteilungen
	roll := strings.Join(flatBars(got[2].Year), ",")
	for _, want := range []string{"PJ-Laufend", "PJ-Planung-Naechstes", "PJ-Ueber-Jahreswechsel"} {
		if !strings.Contains(roll, want) {
			t.Errorf("12 Monate ab heute: %s fehlt in %s", want, roll)
		}
	}
	for _, not := range []string{"PJ-Alt", "PJ-OhneDatum", "PJ-B", "PJ-OhneAnlage"} {
		if strings.Contains(roll, not) {
			t.Errorf("12 Monate ab heute: %s ist dabei: %s", not, roll)
		}
	}
	if y := got[0].Year; y.Label != fmt.Sprint(yr) || len(y.Months) != 12 || y.Today == nil || *y.Today <= 0 || *y.Today >= 100 || len(y.Legend) < 2 {
		t.Errorf("Jahresband: %+v", y)
	}
	if y := got[1].Year; y.Today != nil {
		t.Errorf("Heute-Linie im nächsten Jahr: %v", *y.Today)
	}
	// 12 Monate ab heute: zwei Kalenderjahre, das Projekt ist rechts angeschnitten
	y := got[2].Year
	if y.From != w.today.Format(dayISO) || len(y.Months) < 12 || y.Today == nil {
		t.Errorf("rollendes Band: from %s, %d Monate", y.From, len(y.Months))
	}
	for _, p := range y.Pages {
		for _, b := range p.Bars {
			if b.Name == "PJ-Ueber-Jahreswechsel" && (b.ClipL || !b.ClipR || b.L < 0 || b.L+b.W < 99.9 || b.Color != projectStatusColors["active"] || b.StatusLabel != "Aktiv") {
				t.Errorf("Balken über das Band: %+v", b)
			}
		}
	}
	// Ohne Auswahl und mit globalem Recht auch Projekte ohne Anlage
	w.clear()
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DurationSeconds: 10, Active: true})
	if g := flatBars(w.docs(w.termG, w.devG)[0].Year); !strings.Contains(strings.Join(g, ","), "PJ-OhneAnlage") || !strings.Contains(strings.Join(g, ","), "PJ-B") {
		t.Errorf("alle Abteilungen: %v", g)
	}
}

func TestTerminalWorkProjectsDepartmentIntersectionAndPermission(t *testing.T) {
	w := newWorkEnv(t)
	w.clear()
	yr := w.today.Year()
	at := func(m time.Month, d int) *time.Time { x := time.Date(yr, m, d, 0, 0, 0, 0, time.Local); return &x }
	w.project("PJ-A", "active", w.iA, at(1, 5), at(2, 5))
	w.project("PJ-B", "active", w.iB, at(1, 6), at(2, 6))
	w.project("PJ-O", "active", "", at(1, 7), at(2, 7))
	w.slide(terminalSlide{Kind: slideKindProjectsYear, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, DepartmentIDs: []string{w.deptA, w.deptB}, DurationSeconds: 10, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, DepartmentIDs: []string{w.deptB}, DurationSeconds: 10, Active: true})
	check := func(what string, got []gtWorkSlide, w0, w1, w2 string) {
		t.Helper()
		if len(got) != 3 {
			t.Fatalf("%s: %d Folien", what, len(got))
		}
		for i, want := range []string{w0, w1, w2} {
			if g := strings.Join(flatBars(got[i].Year), ","); g != want {
				t.Errorf("%s Folie %d: %q statt %q", what, i, g, want)
			}
		}
	}
	check("ohne Terminalgrenze", w.docs(w.termG, w.devG), "PJ-A,PJ-B,PJ-O", "PJ-A,PJ-B", "PJ-B")
	w.e.exec(`UPDATE terminal_settings SET abteilungen = ARRAY[$2::uuid] WHERE user_id = $1::uuid`, w.termG, w.deptA)
	check("Terminal nur A", w.docs(w.termG, w.devG), "PJ-A", "PJ-A", "")
	w.e.exec(`UPDATE terminal_settings SET abteilungen = NULL WHERE user_id = $1::uuid`, w.termG)
	// Recht Projekte ansehen nur in B; das Aufgabenrecht ersetzt es nicht
	w.e.grant(w.termNoTickets, "projects.view", w.deptB, true)
	w.e.grant(w.termNoTickets, "tasks.view", "", true)
	w.e.warm()
	check("Recht nur B", w.docs(w.termNoTickets, w.devNT), "PJ-B", "PJ-B", "PJ-B")
	w.e.exec(`UPDATE user_permissions SET granted = false, department_id = NULL WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'projects.view')`, w.termNoTickets)
	w.e.warm()
	check("ohne Recht", w.docs(w.termNoTickets, w.devNT), "", "", "")
}

// Mehr als 14 Projekte: Seiten zu je 14 Balken, Seitenzeit aus der Anzeigedauer.
func TestTerminalWorkProjectsPaging(t *testing.T) {
	w := newWorkEnv(t)
	w.clear()
	yr := w.today.Year()
	for i := 0; i < 31; i++ {
		s := time.Date(yr, 1, 1, 0, 0, 0, 0, time.Local).AddDate(0, 0, i*7)
		e := s.AddDate(0, 0, 20)
		w.project(fmt.Sprintf("PJ-%02d", i), "active", w.iA, &s, &e)
	}
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: []string{w.deptA}, DurationSeconds: 60, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: []string{w.deptB}, DurationSeconds: 60, Active: true})
	got := w.docs(w.termG, w.devG)
	y := got[0].Year
	if len(y.Pages) != 3 || len(y.Pages[0].Bars) != 14 || len(y.Pages[1].Bars) != 14 || len(y.Pages[2].Bars) != 3 || y.Total != 31 || y.More != 0 {
		var n []int
		for _, p := range y.Pages {
			n = append(n, len(p.Bars))
		}
		t.Fatalf("Seiten: %v, gesamt %d", n, y.Total)
	}
	if y.PageSeconds != 20 {
		t.Errorf("Seitenzeit %d s statt 20", y.PageSeconds)
	}
	if y.Pages[0].Bars[0].Name != "PJ-00" || y.Pages[1].Bars[0].Name != "PJ-14" || y.Pages[2].Bars[2].Name != "PJ-30" {
		t.Errorf("Reihenfolge: %s %s %s", y.Pages[0].Bars[0].Name, y.Pages[1].Bars[0].Name, y.Pages[2].Bars[2].Name)
	}
	// 3 Seiten brauchen mindestens 12 s: eine Folie mit 5 s Dauer wird auf 12 s verlängert, 60 s bleiben 60 s
	w.clear()
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: []string{w.deptA}, DurationSeconds: 5, Active: true})
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: []string{w.deptA}, DurationSeconds: 60, Active: true})
	var dd struct {
		Slides []struct {
			Duration int               `json:"duration"`
			Year     *projectsYearData `json:"year"`
		} `json:"slides"`
	}
	w.e.must(json.Unmarshal(w.do("GET", "/terminal/slides.json", w.cookie(w.termG, w.devG), nil).Body.Bytes(), &dd))
	if len(dd.Slides) != 2 || dd.Slides[0].Duration != 12 || dd.Slides[0].Year.PageSeconds != 4 || dd.Slides[1].Duration != 60 || dd.Slides[1].Year.PageSeconds != 20 {
		t.Errorf("Anzeigedauer: %+v", dd.Slides)
	}
	w.clear()
	w.slide(terminalSlide{Kind: slideKindProjectsYear, YearMode: "current", DepartmentIDs: []string{w.deptB}, DurationSeconds: 60, Active: true})
	got = w.docs(w.termG, w.devG)
	// keine Projekte: keine Seite, leere Liste (nicht null) im JSON
	if e := got[0].Year; e == nil || len(e.Pages) != 0 || e.Pages == nil {
		t.Errorf("leer: %+v", e)
	}
	raw := w.do("GET", "/terminal/slides.json", w.cookie(w.termG, w.devG), nil).Body.String()
	if !strings.Contains(raw, `"pages":[]`) {
		t.Error(`leere Seiten nicht als "pages":[] im JSON`)
	}
}

// Viele überfällige Aufgaben verdrängen die des Zeitraums nicht, und beide „+ n weitere“ stammen aus Zählungen.
func TestTerminalWorkTasksSeparateLimits(t *testing.T) {
	w := newWorkEnv(t)
	for i := 0; i < taskMaxOverdue+5; i++ {
		w.e.exec(`INSERT INTO tasks (title, status, due_date, infrastructure_id, created_by) VALUES ($1::varchar, 'open', $2, $3::uuid, $4::uuid)`,
			fmt.Sprintf("T-Viel-%03d", i), w.today.AddDate(0, 0, -10), w.iB, w.pA)
	}
	for i := 0; i < taskMaxItems+3; i++ {
		w.e.exec(`INSERT INTO tasks (title, status, due_date, infrastructure_id, created_by) VALUES ($1::varchar, 'open', $2, $3::uuid, $4::uuid)`,
			fmt.Sprintf("T-Zeit-%03d", i), w.today, w.iB, w.pA)
	}
	w.clear()
	w.slide(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: w.today.Format(dayISO), DepartmentIDs: []string{w.deptB}, DurationSeconds: 10, Active: true})
	tk := w.docs(w.termG, w.devG)[0].Tasks
	if len(tk.Overdue) != taskMaxOverdue || tk.OverdueMore != 5 || taskShown(tk) != taskMaxItems || tk.More != 3 {
		t.Errorf("überfällig %d (+%d), im Zeitraum %d (+%d)", len(tk.Overdue), tk.OverdueMore, taskShown(tk), tk.More)
	}
}
