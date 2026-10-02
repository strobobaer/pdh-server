package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTimelineStyleSanitizeAndCSS(t *testing.T) {
	s := TimelineStyle{Width: 99, Brightness: 80, BarHeight: 3, DueDays: -2, Running: "gelb", Done: "#22C55E", DoneFill: "", Due: "#ef4444", Overdue: "#ff1f1f;}body{x", Unassigned: "#a855f7"}
	s.sanitize()
	d := defaultTimelineStyle
	if s.Width != d.Width || s.Brightness != d.Brightness || s.BarHeight != d.BarHeight || s.DueDays != d.DueDays {
		t.Errorf("Zahlen nicht begrenzt: %+v", s)
	}
	if s.Running != d.Running || s.Done != "#22c55e" || s.DoneFill != d.DoneFill || s.Overdue != d.Overdue {
		t.Errorf("Farben nicht geprüft: %+v", s)
	}
	css := string(s.CSS())
	for _, want := range []string{"--tl-w:2px", "--tl-running:#facc15", "--tl-unassigned:#a855f7", ".gantt .bar-wrapper.tl-overdue .bar", "@keyframes tl-flash",
		".gantt .bar-wrapper.tl-done .bar,.gantt .bar-wrapper.tl-done .bar-progress{fill:var(--tl-done-fill)!important}", "prefers-reduced-motion", ".gantt-selected"} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS enthält %q nicht", want)
		}
	}
	if strings.Contains(css, "body{x") {
		t.Error("CSS-Einschleusung")
	}
	// Helligkeit wirkt nur auf die Raender, nicht auf die Ausgrau-Farbe
	s.Brightness = 50
	css = string(s.CSS())
	if !strings.Contains(css, "--tl-running:"+mixHex("#facc15", "#ffffff", .5)) || !strings.Contains(css, "--tl-done-fill:#6b7280") {
		t.Errorf("Helligkeit: %s", css[:200])
	}
	s.Brightness = -50
	if !strings.Contains(string(s.CSS()), "--tl-due:"+mixHex("#ef4444", "#000000", .5)) {
		t.Error("dunkler Rand fehlt")
	}
	if (TimelineStyle{}).Safe() != defaultTimelineStyle {
		t.Error("leere Einstellung ergibt nicht den Standard")
	}
}

func TestTimelinePagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{EnvFile: ".env", FileWritable: true}
	d.Brand = Branding{AppName: "PDH", Timeline: TimelineStyle{Width: 3, Brightness: 10, BarHeight: 28, DueDays: 2, Running: "#ffee00", Done: "#00ff00", DoneFill: "#777777", Due: "#ff0000", Overdue: "#ff0000", Unassigned: "#aa00ff"}}
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{`action="/admin/branding/timeline"`, `name="running" value="#ffee00"`, `name="unassigned" value="#aa00ff"`, `name="done_fill" value="#777777"`,
		`name="width" min="0.5" max="6" step="0.5" value="3"`, `name="bar_height" min="12" max="48" step="2" value="28"`, `<option value="2" selected>2 Tage vorher</option>`,
		"window.PDH_TL = {dueDays:  2 , barHeight:  28 }", "--tl-w:3px"} {
		if !strings.Contains(out, want) {
			t.Errorf("Einstellungen enthalten %q nicht", want)
		}
	}
	checkScripts(t, "server_config", out)

	dash := renderPage(t, tmpl, "dashboard", DashboardData{GanttItems: []GanttItem{{ID: "a", RefType: "ticket", Title: "T", StartISO: "2026-10-01", EndISO: "2026-10-09", IsRunning: true, IsUnassigned: true, Color: "#4b9fc4"}}})
	for _, want := range []string{"running: true, unassigned: true", "pdhTimelineApply(wrapper, t)", "bar_height: PDH_TL.barHeight", `class="tl-legend"`} {
		if !strings.Contains(dash, want) {
			t.Errorf("Dashboard enthält %q nicht", want)
		}
	}
	checkScripts(t, "dashboard", dash)
	proj := renderPage(t, tmpl, "project_detail", ProjectDetailData{Tasks: []TaskView{{ID: "x", Title: "A", Status: "in_progress", StartDateISO: "2026-10-01", DueDateISO: "2026-10-05"}}})
	if !strings.Contains(proj, "running: true") || !strings.Contains(proj, "unassigned: true") || !strings.Contains(proj, "bar_height: PDH_TL.barHeight") {
		t.Error("Projekt-Zeitstrahl ohne Zustände")
	}
	checkScripts(t, "project", proj)

	// Zustandslogik im Browser-Skript: Vorrang und Faellig-Vorlauf
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node fehlt")
	}
	m := regexp.MustCompile(`(?s)<script>\s*(window\.PDH_TL = .*?)</script>`).FindStringSubmatch(dash)
	if m == nil {
		t.Fatal("Zeitstrahl-Skript nicht gefunden")
	}
	harness := "var window = globalThis;\n" + m[1] + `
const iso = n => { const d = new Date(); d.setHours(0,0,0,0); d.setDate(d.getDate() + n); return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0'); };
const S = pdhTimelineState, out = [];
const eq = (got, want, what) => { if(got !== want) out.push(what + ': ' + got + ' statt ' + want); };
window.PDH_TL.dueDays = 1;
eq(S({done: true, end: iso(-5)}), 'done', 'beendet vor überfällig');
eq(S({end: iso(-1), unassigned: true}), 'overdue', 'überfällig vor nicht zugewiesen');
eq(S({end: iso(0)}), 'due', 'heute fällig');
eq(S({end: iso(1)}), 'due', 'morgen fällig (Vorlauf 1)');
eq(S({end: iso(2), running: true}), 'running', 'übermorgen läuft');
eq(S({end: iso(5), unassigned: true, running: true}), 'unassigned', 'nicht zugewiesen vor laufend');
eq(S({end: iso(-3), provisional: true}), '', 'geschätzter Termin nie überfällig');
window.PDH_TL.dueDays = 0;
eq(S({end: iso(1)}), '', 'Vorlauf 0: morgen noch nicht fällig');
if(out.length){ console.log(out.join('\n')); process.exit(1); }
`
	f := filepath.Join(t.TempDir(), "tl.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Errorf("Zustandslogik: %s", b)
	}
}
