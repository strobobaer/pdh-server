package web

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Aufgaben- und Projektfolien des Infobildschirms (terminal_aufgaben.go, Migration 210): Zeiträume, Gruppierung,
// Jahresband, Seiten, Prüfung, Formular, Zwischenspeicher. Die Daten aus der Datenbank stehen in
// terminal_aufgaben_integration_test.go.

var aufgabenKinds = []string{slideKindTasks, slideKindProjectsYear}

func dayAt(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }

func TestTaskRangeEnd(t *testing.T) {
	wed := time.Date(2026, 11, 25, 14, 30, 0, 0, time.Local) // Mittwoch, KW 48 (23.11. bis 29.11.)
	for _, c := range []struct {
		rng, until string
		want       time.Time
		label      string
	}{
		{taskRangeWeek, "", dayAt(2026, 11, 29), "diese Woche"},
		{taskRange2W, "", dayAt(2026, 12, 6), "bis Ende KW 49"},
		{taskRange4W, "", dayAt(2026, 12, 20), "bis Ende KW 51"},
		{taskRange8W, "", dayAt(2027, 1, 17), "bis Ende KW 2"},
		{taskRange12W, "", dayAt(2027, 2, 14), "bis Ende KW 6"},
		{taskRangeDate, "2026-12-31", dayAt(2026, 12, 31), "bis 31.12.2026"},
		{taskRangeDate, "kaputt", dayAt(2026, 11, 29), "diese Woche"}, // unbrauchbares Datum: Ende der Woche
		{"", "", dayAt(2026, 11, 29), "diese Woche"},
	} {
		if got := taskRangeEnd(wed, c.rng, c.until); !got.Equal(c.want) {
			t.Errorf("%s/%s: Ende %s statt %s", c.rng, c.until, got.Format(dayISO), c.want.Format(dayISO))
		}
		if got := taskRangeLabel(wed, c.rng, c.until); got != c.label {
			t.Errorf("%s/%s: Beschriftung %q statt %q", c.rng, c.until, got, c.label)
		}
	}
	// Sonntag gehört noch zu dieser Woche, Montag beginnt die nächste
	sun := time.Date(2026, 11, 29, 23, 0, 0, 0, time.Local)
	if got := taskRangeEnd(sun, taskRangeWeek, ""); !got.Equal(dayAt(2026, 11, 29)) {
		t.Errorf("Sonntag: %s", got.Format(dayISO))
	}
	mon := time.Date(2026, 11, 30, 0, 5, 0, 0, time.Local)
	if got := taskRangeEnd(mon, taskRangeWeek, ""); !got.Equal(dayAt(2026, 12, 6)) {
		t.Errorf("Montag: %s", got.Format(dayISO))
	}
}

func mkTask(due time.Time, title string) taskRow {
	return taskRow{Due: due, Item: taskSlideItem{Title: title, Assignees: []string{}, Status: "open", StatusLabel: "Offen"}}
}

// Zeitraum und Überfälliges: vor heute = überfällig (oben, gekennzeichnet), bis einschließlich Ende = Woche/Tag,
// danach entfällt die Aufgabe.
func TestGroupTaskRowsRangeAndOverdue(t *testing.T) {
	today := dayAt(2026, 11, 25) // Mittwoch
	to := taskRangeEnd(today, taskRange2W, "")
	rows := []taskRow{
		mkTask(dayAt(2026, 11, 10), "alt"),         // überfällig
		mkTask(dayAt(2026, 11, 24), "gestern"),     // überfällig, aber in dieser Woche
		mkTask(dayAt(2026, 11, 25), "heute"),       // nicht überfällig
		mkTask(dayAt(2026, 11, 25), "heute zwei"),  // gleicher Tag
		mkTask(dayAt(2026, 11, 29), "sonntag"),     // letzter Tag dieser Woche
		mkTask(dayAt(2026, 11, 30), "montag"),      // nächste Woche
		mkTask(dayAt(2026, 12, 6), "ende"),         // letzter Tag des Zeitraums
		mkTask(dayAt(2026, 12, 7), "zu spät"),      // außerhalb
		mkTask(dayAt(2027, 3, 1), "weit entfernt"), // außerhalb
	}
	g := groupTaskRows(rows, today, to)
	var od []string
	for _, it := range g.Overdue {
		if !it.Overdue {
			t.Errorf("%s nicht als überfällig gekennzeichnet", it.Title)
		}
		od = append(od, it.Title+"@"+it.Due)
	}
	if strings.Join(od, ",") != "alt@10.11.,gestern@24.11." {
		t.Errorf("überfällig: %v", od)
	}
	var got []string
	for _, w := range g.Weeks {
		for _, d := range w.Days {
			for _, it := range d.Items {
				if it.Overdue {
					t.Errorf("%s fälschlich überfällig", it.Title)
				}
				got = append(got, w.Label+"|"+d.Label+"|"+it.Title)
			}
		}
	}
	want := []string{
		"KW 48 · 23.11.–29.11.|Mi 25.11.|heute", "KW 48 · 23.11.–29.11.|Mi 25.11.|heute zwei", "KW 48 · 23.11.–29.11.|So 29.11.|sonntag",
		"KW 49 · 30.11.–06.12.|Mo 30.11.|montag", "KW 49 · 30.11.–06.12.|So 06.12.|ende",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Wochen:\n%s\nstatt\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !g.Weeks[0].Days[0].Today || g.Weeks[0].Days[1].Today {
		t.Error("heute nicht markiert")
	}
	// Enddatum mitten in der Woche
	g = groupTaskRows(rows, today, dayAt(2026, 11, 25))
	if len(g.Weeks) != 1 || len(g.Weeks[0].Days) != 1 || len(g.Weeks[0].Days[0].Items) != 2 || len(g.Overdue) != 2 {
		t.Errorf("bis heute: %+v", g)
	}
	// Nichts: leere Listen statt null
	g = groupTaskRows(nil, today, to)
	if g.Overdue == nil || g.Weeks == nil {
		t.Error("leere Listen sind null")
	}
}

func TestGroupTaskRowsCaps(t *testing.T) {
	today := dayAt(2026, 11, 25)
	var rows []taskRow
	for i := 0; i < taskMaxOverdue+5; i++ {
		rows = append(rows, mkTask(dayAt(2026, 10, 1), fmt.Sprintf("alt%d", i)))
	}
	for i := 0; i < taskMaxItems+7; i++ {
		rows = append(rows, mkTask(dayAt(2026, 11, 26), fmt.Sprintf("neu%d", i)))
	}
	g := groupTaskRows(rows, today, dayAt(2026, 12, 31))
	n := 0
	for _, w := range g.Weeks {
		for _, d := range w.Days {
			n += len(d.Items)
		}
	}
	if len(g.Overdue) != taskMaxOverdue || g.OverdueMore != 5 || n != taskMaxItems || g.More != 7 {
		t.Errorf("Grenzen: %d überfällig (+%d), %d Aufgaben (+%d)", len(g.Overdue), g.OverdueMore, n, g.More)
	}
}

func TestValidateWorkSlide(t *testing.T) {
	ok := func(s terminalSlide) terminalSlide {
		t.Helper()
		if err := validateTerminalSlide(&s); err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
		return s
	}
	bad := func(what, want string, s terminalSlide) {
		t.Helper()
		if err := validateTerminalSlide(&s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v statt %q", what, err, want)
		}
	}
	s := ok(terminalSlide{Kind: slideKindTasks})
	if s.TaskRange != taskRangeWeek || s.YearMode != yearModeCurrent || s.OnlyActive || s.TaskUntil != "" {
		t.Errorf("Vorgaben Aufgaben: %+v", s)
	}
	for _, r := range []string{"week", "2w", "4w", "8w", "12w"} {
		ok(terminalSlide{Kind: slideKindTasks, TaskRange: r, TaskUntil: "2026-01-01"})
	}
	if s := ok(terminalSlide{Kind: slideKindTasks, TaskRange: "4w", TaskUntil: "2026-01-01"}); s.TaskUntil != "" {
		t.Error("Datum bleibt bei Wochen stehen")
	}
	if s := ok(terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: " 2026-12-31 "}); s.TaskUntil != "2026-12-31" {
		t.Errorf("Datum: %q", s.TaskUntil)
	}
	bad("Zeitraum", "Ungültiger Zeitraum", terminalSlide{Kind: slideKindTasks, TaskRange: "14d"})
	bad("Datum fehlt", "Enddatum", terminalSlide{Kind: slideKindTasks, TaskRange: "date"})
	bad("Datum falsch", "Enddatum", terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: "31.31.2026"})
	bad("Datum Jahr", "Enddatum", terminalSlide{Kind: slideKindTasks, TaskRange: "date", TaskUntil: "1999-01-01"})
	bad("Jahresband", "Ungültiger Jahreszeitraum", terminalSlide{Kind: slideKindProjectsYear, YearMode: "decade"})
	bad("Abteilung", "Ungültige Abteilung", terminalSlide{Kind: slideKindTasks, DepartmentIDs: []string{"x"}})
	bad("Abteilung Jahr", "Ungültige Abteilung", terminalSlide{Kind: slideKindProjectsYear, DepartmentIDs: []string{"x"}})
	bad("Abteilungen viele", "höchstens 100", terminalSlide{Kind: slideKindProjectsYear, DepartmentIDs: make([]string, planMaxDepts+1)})
	d := "11111111-1111-4111-8111-111111111111"
	if s := ok(terminalSlide{Kind: slideKindProjectsYear, YearMode: "rolling", OnlyActive: true, DepartmentIDs: []string{d}}); s.YearMode != "rolling" || !s.OnlyActive || len(s.DepartmentIDs) != 1 {
		t.Errorf("Projektfolie: %+v", s)
	}
	// andere Arten tragen keine Felder der neuen Arten
	for _, k := range []string{slideKindText, slideKindWebsite} {
		s := ok(terminalSlide{Kind: k, Body: "x", URL: "https://example.org", TaskRange: "date", TaskUntil: "2026-12-31", YearMode: "next", OnlyActive: true, DepartmentIDs: []string{d}})
		if s.TaskRange != taskRangeWeek || s.TaskUntil != "" || s.YearMode != yearModeCurrent || s.OnlyActive || len(s.DepartmentIDs) != 0 {
			t.Errorf("%s trägt Felder: %+v", k, s)
		}
	}
	// die Aufgabenfolie trägt keine Projektfelder und umgekehrt; Planungsfolien behalten ihre Abteilungen
	if s := ok(terminalSlide{Kind: slideKindTasks, YearMode: "next", OnlyActive: true}); s.YearMode != yearModeCurrent || s.OnlyActive {
		t.Errorf("Aufgabenfolie mit Projektfeldern: %+v", s)
	}
	if s := ok(terminalSlide{Kind: slideKindProjectsYear, TaskRange: "date", TaskUntil: "2026-12-31"}); s.TaskRange != taskRangeWeek || s.TaskUntil != "" {
		t.Errorf("Projektfolie mit Aufgabenfeldern: %+v", s)
	}
	if s := ok(terminalSlide{Kind: slideKindPlanShifts, DepartmentIDs: []string{d}}); len(s.DepartmentIDs) != 1 {
		t.Errorf("Planungsfolie verliert Abteilungen: %+v", s)
	}
}

// Band „ab heute 12 Monate“ überspannt zwei Kalenderjahre; „aktuell“ und „nächstes“ sind Kalenderjahre.
func TestYearBandBounds(t *testing.T) {
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.Local)
	for _, c := range []struct {
		mode     string
		from, to time.Time
		label    string
	}{
		{yearModeCurrent, dayAt(2026, 1, 1), dayAt(2026, 12, 31), "2026"},
		{yearModeNext, dayAt(2027, 1, 1), dayAt(2027, 12, 31), "2027"},
		{yearModeRolling, dayAt(2026, 11, 23), dayAt(2027, 11, 22), "23.11.2026 – 22.11.2027"},
		{"", dayAt(2026, 1, 1), dayAt(2026, 12, 31), "2026"},
	} {
		f, to := yearBandBounds(now, c.mode)
		if !f.Equal(c.from) || !to.Equal(c.to) {
			t.Errorf("%s: %s bis %s", c.mode, f.Format(dayISO), to.Format(dayISO))
		}
		if got := yearModeLabel(now, c.mode); got != c.label {
			t.Errorf("%s: Beschriftung %q", c.mode, got)
		}
	}
	// Schaltjahr: 12 Monate ab 29.02.
	f, to := yearBandBounds(time.Date(2028, 2, 29, 9, 0, 0, 0, time.Local), yearModeRolling)
	if !f.Equal(dayAt(2028, 2, 29)) || !to.Equal(dayAt(2029, 2, 28)) {
		t.Errorf("Schaltjahr: %s bis %s", f.Format(dayISO), to.Format(dayISO))
	}
}

func TestYearBandMonthsRollingTwoYears(t *testing.T) {
	from, to := dayAt(2026, 11, 23), dayAt(2027, 11, 22)
	m := yearBandMonths(from, to)
	if len(m) != 13 {
		t.Fatalf("Monate: %d", len(m))
	}
	if m[0].Label != "Nov 2026" || m[1].Label != "Dez" || m[2].Label != "Jan 2027" || m[12].Label != "Nov" {
		t.Errorf("Beschriftung: %v", []string{m[0].Label, m[1].Label, m[2].Label, m[12].Label})
	}
	sum := 0.0
	for i, x := range m {
		sum += x.W
		if i > 0 && (x.L < m[i-1].L+m[i-1].W-0.001 || x.L > m[i-1].L+m[i-1].W+0.001) {
			t.Errorf("Lücke vor %s: %.3f nach %.3f", x.Label, x.L, m[i-1].L+m[i-1].W)
		}
	}
	if m[0].L != 0 || sum < 99.99 || sum > 100.01 {
		t.Errorf("Summe der Breiten %.3f, Beginn %.3f", sum, m[0].L)
	}
	// angeschnittene Monate sind schmaler als volle
	if !(m[0].W < m[1].W) || !(m[12].W < m[1].W) {
		t.Errorf("Anschnitt: %.2f %.2f %.2f", m[0].W, m[1].W, m[12].W)
	}
	// Kalenderjahr: zwölf Spalten
	cur := yearBandMonths(dayAt(2026, 1, 1), dayAt(2026, 12, 31))
	if len(cur) != 12 || cur[0].Label != "Jan 2026" || cur[1].Label != "Feb" || cur[11].Label != "Dez" {
		t.Errorf("Kalenderjahr: %+v", cur)
	}
}

func TestProjBarFor(t *testing.T) {
	from, to := dayAt(2026, 11, 23), dayAt(2027, 11, 22) // 365 Tage
	bar := func(s, e time.Time) (projBar, bool) { return projBarFor("P", "active", s, e, from, to) }
	// über den Jahreswechsel
	b, ok := bar(dayAt(2026, 12, 1), dayAt(2027, 1, 31))
	if !ok || b.ClipL || b.ClipR || b.Start != "01.12.2026" || b.End != "31.01.2027" || b.Color != projectStatusColors["active"] {
		t.Fatalf("Balken: %+v", b)
	}
	if want := 8.0 / 365 * 100; b.L < want-0.01 || b.L > want+0.01 {
		t.Errorf("Beginn %.3f statt %.3f", b.L, want)
	}
	if want := 62.0 / 365 * 100; b.W < want-0.01 || b.W > want+0.01 {
		t.Errorf("Breite %.3f statt %.3f", b.W, want)
	}
	// vor dem Band begonnen, nach dem Band endend: angeschnitten, ganze Breite
	b, ok = bar(dayAt(2026, 1, 1), dayAt(2028, 1, 1))
	if !ok || !b.ClipL || !b.ClipR || b.L != 0 || b.W < 99.99 || b.W > 100.01 {
		t.Errorf("angeschnitten: %+v %v", b, ok)
	}
	// außerhalb
	if _, ok := bar(dayAt(2026, 1, 1), dayAt(2026, 11, 22)); ok {
		t.Error("endet vor dem Band")
	}
	if _, ok := bar(dayAt(2027, 11, 23), dayAt(2027, 12, 31)); ok {
		t.Error("beginnt nach dem Band")
	}
	// Ränder zählen mit
	if _, ok := bar(dayAt(2026, 10, 1), dayAt(2026, 11, 23)); !ok {
		t.Error("Ende am ersten Tag")
	}
	if _, ok := bar(dayAt(2027, 11, 22), dayAt(2027, 12, 31)); !ok {
		t.Error("Start am letzten Tag")
	}
	// Mindestbreite, Ende vor Start wird getauscht, rechter Rand bleibt im Band
	b, _ = bar(dayAt(2027, 11, 22), dayAt(2027, 11, 22))
	if b.W < 0.8 || b.L+b.W > 100.0001 {
		t.Errorf("Eintagsbalken: %+v", b)
	}
	b, ok = bar(dayAt(2027, 1, 31), dayAt(2026, 12, 1))
	if !ok || b.Start != "01.12.2026" || b.End != "31.01.2027" {
		t.Errorf("vertauscht: %+v", b)
	}
	// unbekannter Status: graue Standardfarbe
	if b, _ := projBarFor("P", "was-neues", dayAt(2026, 12, 1), dayAt(2026, 12, 2), from, to); !hexColorRe.MatchString(b.Color) {
		t.Errorf("Farbe: %q", b.Color)
	}
	for st, c := range projectStatusColors {
		if !hexColorRe.MatchString(c) {
			t.Errorf("Farbe %s: %q", st, c)
		}
	}
}

func TestPageProjectBars(t *testing.T) {
	mk := func(n int) []projBar {
		b := make([]projBar, n)
		for i := range b {
			b[i].Name = fmt.Sprint(i)
		}
		return b
	}
	for _, c := range []struct{ n, pages, last int }{{0, 0, 0}, {1, 1, 1}, {14, 1, 14}, {15, 2, 1}, {28, 2, 14}, {29, 3, 1}, {140, 10, 14}} {
		p := pageProjectBars(mk(c.n), projYearPageSize)
		if len(p) != c.pages || p == nil {
			t.Errorf("%d Balken: %d Seiten statt %d", c.n, len(p), c.pages)
			continue
		}
		total := 0
		for i, pg := range p {
			if len(pg.Bars) > projYearPageSize {
				t.Errorf("%d Balken: Seite %d hat %d Balken", c.n, i, len(pg.Bars))
			}
			total += len(pg.Bars)
		}
		if total != c.n || (c.pages > 0 && len(p[len(p)-1].Bars) != c.last) {
			t.Errorf("%d Balken: Summe %d, letzte Seite %d", c.n, total, len(p[len(p)-1].Bars))
		}
	}
	// Reihenfolge bleibt, Seiten teilen keinen Speicher zum Anhängen
	p := pageProjectBars(mk(20), 14)
	if p[0].Bars[0].Name != "0" || p[1].Bars[0].Name != "14" || cap(p[0].Bars) != 14 {
		t.Errorf("Reihenfolge: %v %v cap %d", p[0].Bars[0].Name, p[1].Bars[0].Name, cap(p[0].Bars))
	}
	// Seitenzeit: Dauer geteilt durch Seiten, mindestens 4 Sekunden
	for _, c := range []struct{ dur, pages, want int }{{60, 1, 60}, {60, 3, 20}, {30, 10, 4}, {15, 4, 4}, {600, 10, 60}, {15, 0, 15}} {
		if got := projPageSeconds(c.dur, c.pages); got != c.want {
			t.Errorf("Seitenzeit %d s / %d: %d statt %d", c.dur, c.pages, got, c.want)
		}
	}
}

func TestWorkKindsLabelled(t *testing.T) {
	for _, k := range aufgabenKinds {
		if slideKindLabels[k] == "" || !slideIsWork(k) || slideIsPlan(k) {
			t.Errorf("%s", k)
		}
	}
	if slideIsWork(slideKindText) || slideIsWork(slideKindPlanShifts) {
		t.Error("andere Arten gelten als Aufgaben/Projekte")
	}
}

// Die Folien stehen nicht im Zwischenspeicher des Geräts und entfallen offline wie Website-Folien.
func TestWorkKindsNotCacheable(t *testing.T) {
	src := []byte(readStatic(t, "../../web/static/terminal.js"))
	kinds := regexp.MustCompile(`var CACHEABLE_KINDS = [^\n]*`).Find(src)
	if kinds == nil {
		t.Fatal("CACHEABLE_KINDS nicht gefunden")
	}
	for _, k := range aufgabenKinds {
		if strings.Contains(string(kinds), k) {
			t.Errorf("%s im Zwischenspeicher: %s", k, kinds)
		}
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
['tasks', 'projects_year'].forEach(function (k) {
  const p = {id: k, kind: k};
  eq(showableIndex([txt, p, txt], 1, false), 1, k + ' online');
  eq(showableIndex([txt, p, txt], 1, true), 2, k + ' offline übersprungen');
  eq(showableIndex([p, p], 0, true), -1, k + ' nur diese Art, offline');
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

// Der Player setzt die Daten nur über textContent bzw. Textknoten ein, prüft Farben und begrenzt Zahlen; er zeigt
// höchstens 14 Balken je Seite und blättert über einen Zeitgeber, der beim Neuzeichnen abgeräumt wird.
func TestWorkPlayerSafe(t *testing.T) {
	js := readStatic(t, "../../web/static/terminal.js")
	i := strings.Index(js, "var drawTasks = function")
	j := strings.Index(js, "var draw = function")
	if i < 0 || j < i {
		t.Fatal("drawTasks/drawYear nicht gefunden")
	}
	body := js[i:j]
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
		if strings.Contains(body, bad) {
			t.Errorf("Aufgaben-/Projektfolie nutzt %s", bad)
		}
	}
	for _, want := range []string{"/^#[0-9a-fA-F]{6}$/.test(it.color)", "/^#[0-9a-fA-F]{6}$/.test(b.color)", "/^#[0-9a-fA-F]{6}$/.test(l.color)",
		"slice(0, 14)", "pct(b.l)", "pct(b.w)", "reloadTimer = setInterval"} {
		if !strings.Contains(body, want) {
			t.Errorf("fehlt: %s", want)
		}
	}
	if !strings.Contains(js, "if (reloadTimer) { clearInterval(reloadTimer)") {
		t.Error("Zeitgeber wird nicht abgeräumt")
	}
}

func TestWorkFormTemplate(t *testing.T) {
	form := readStatic(t, "../../web/templates/terminal_slide_form.gohtml")
	for _, want := range []string{`name="task_range"`, `name="task_until"`, `name="year_mode"`, `name="only_active"`, `name="department_ids"`,
		`value="tasks"`, `value="projects_year"`, `value="12w"`, `value="rolling"`} {
		if !strings.Contains(form, want) {
			t.Errorf("Formular enthält %q nicht", want)
		}
	}
	if n := strings.Count(form, `name="department_ids"`); n != 1 {
		t.Errorf("Abteilungsfeld %d-mal", n)
	}
}

// ── mit Datenbank: Formular der Verwaltungsseite ──

func TestTerminalSlidesWebWorkKinds(t *testing.T) {
	e := newSlidesEnv(t)
	d1, d2 := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	wantRedirect(t, e.do("POST", "/terminal-slides", []byte(url.Values{"kind": {slideKindTasks}, "title": {"Aufgaben"}, "task_range": {"date"},
		"task_until": {"31.12.2026"}, "department_ids": {d1, d2, d1}, "active": {"1"}}.Encode()), "application/x-www-form-urlencoded"), "created")
	wantRedirect(t, e.do("POST", "/terminal-slides", []byte(url.Values{"kind": {slideKindProjectsYear}, "year_mode": {"rolling"}, "only_active": {"1"},
		"department_ids": {d2}, "active": {"1"}}.Encode()), "application/x-www-form-urlencoded"), "created")
	got := e.slides(t)
	if len(got) != 2 {
		t.Fatalf("Folien: %d", len(got))
	}
	if s := got[0]; s.Kind != slideKindTasks || s.TaskRange != "date" || s.TaskUntil != "2026-12-31" || len(s.DepartmentIDs) != 2 || s.YearMode != "current" || s.OnlyActive {
		t.Errorf("Aufgabenfolie: %+v", s)
	}
	if s := got[1]; s.Kind != slideKindProjectsYear || s.YearMode != "rolling" || !s.OnlyActive || len(s.DepartmentIDs) != 1 || s.TaskRange != "week" || s.TaskUntil != "" {
		t.Errorf("Projektfolie: %+v", s)
	}
	// Vorgaben ohne Angaben
	wantRedirect(t, e.post("/terminal-slides", map[string]string{"kind": slideKindTasks, "active": "1"}, nil), "created")
	if s := e.slides(t)[2]; s.TaskRange != "week" || len(s.DepartmentIDs) != 0 {
		t.Errorf("Vorgaben: %+v", s)
	}
	rec := e.do("GET", "/terminal-slides", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "bis 31.12.2026 · 2 Abteilungen") || !strings.Contains(rec.Body.String(), "· 1 Abteilung · nur aktive") {
		t.Errorf("Liste: %d", rec.Code)
	}
	for _, k := range aufgabenKinds {
		if rec := e.do("GET", "/terminal-slides/new?kind="+k, nil, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="task_range"`) {
			t.Errorf("neu %s: %d", k, rec.Code)
		}
	}
	rec = e.do("GET", "/terminal-slides/"+got[0].ID, nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<option value="date" selected>`) || !strings.Contains(rec.Body.String(), `value="2026-12-31"`) {
		t.Errorf("Ändern-Formular: %d", rec.Code)
	}
}

func TestTerminalSlidesWebWorkInvalidInput(t *testing.T) {
	e := newSlidesEnv(t)
	for name, c := range map[string]struct {
		fields map[string]string
		want   string
	}{
		"Zeitraum unbekannt": {map[string]string{"kind": slideKindTasks, "task_range": "14d"}, "Ungültiger Zeitraum"},
		"Datum fehlt":        {map[string]string{"kind": slideKindTasks, "task_range": "date"}, "Enddatum"},
		"Datum falsch":       {map[string]string{"kind": slideKindTasks, "task_range": "date", "task_until": "morgen"}, "Enddatum"},
		"Jahresband":         {map[string]string{"kind": slideKindProjectsYear, "year_mode": "decade"}, "Ungültiger Jahreszeitraum"},
		"Abteilung":          {map[string]string{"kind": slideKindProjectsYear, "department_ids": "keine-uuid"}, "Ungültige Abteilung"},
		"Abteilungen viele":  {map[string]string{"kind": slideKindTasks, "department_ids": manyUUIDs(planMaxDepts + 1)}, "höchstens 100 Abteilungen"},
		"Dauer":              {map[string]string{"kind": slideKindTasks, "duration": "4"}, "zwischen 5 und 600"},
	} {
		rec := e.post("/terminal-slides", c.fields, nil)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.want) || !strings.Contains(rec.Body.String(), `name="task_range"`) {
			t.Errorf("%s: Status %d, Meldung %q fehlt", name, rec.Code, c.want)
		}
	}
	if got := e.slides(t); len(got) != 0 {
		t.Errorf("fehlerhafte Folien gespeichert: %d", len(got))
	}
}
