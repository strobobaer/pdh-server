package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestValidateCompletion(t *testing.T) {
	ok := func(in completionRequest, parts, booked int, running bool) error {
		return validateCompletion(&in, parts, booked, running)
	}
	base := completionRequest{Comment: "Filter getauscht", Minutes: 30, Finish: true, NoParts: true}
	if err := ok(base, 0, 0, false); err != nil {
		t.Fatalf("gültiger Abschluss abgelehnt: %v", err)
	}
	cases := []struct {
		name    string
		mod     func(*completionRequest)
		parts   int
		booked  int
		running bool
		want    string
	}{
		{"ohne Kommentar", func(r *completionRequest) { r.Comment = "  " }, 0, 0, false, "Kommentar"},
		{"ohne Material", func(r *completionRequest) { r.NoParts = false }, 0, 0, false, "Material"},
		{"ohne Zeit", func(r *completionRequest) { r.Minutes = 0 }, 0, 0, false, "Arbeitszeit"},
		{"Kollegen ohne gemeinsame Zeit", func(r *completionRequest) { r.Minutes = 0; r.Colleagues = []string{"x"} }, 0, 60, false, "gemeinsame Arbeitszeit"},
		{"zu lange", func(r *completionRequest) { r.Minutes = 25 * 60 }, 0, 0, false, "24 Stunden"},
	}
	for _, c := range cases {
		in := base
		c.mod(&in)
		err := ok(in, c.parts, c.booked, c.running)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: erwartet Fehler mit %q, bekommen %v", c.name, c.want, err)
		}
	}
	// erlaubte Varianten
	allowed := []struct {
		name    string
		mod     func(*completionRequest)
		parts   int
		booked  int
		running bool
	}{
		{"Material vorgemerkt", func(r *completionRequest) { r.NoParts = false }, 2, 0, false},
		{"Zeit schon erfasst", func(r *completionRequest) { r.Minutes = 0 }, 0, 45, false},
		{"Timer läuft, mit Kollegen", func(r *completionRequest) { r.Minutes = 0; r.Colleagues = []string{"x"} }, 0, 0, true},
		{"geht noch weiter ohne Material", func(r *completionRequest) { r.Finish = false; r.NoParts = false }, 0, 0, false},
	}
	for _, c := range allowed {
		in := base
		c.mod(&in)
		if err := ok(in, c.parts, c.booked, c.running); err != nil {
			t.Errorf("%s: unerwartet abgelehnt: %v", c.name, err)
		}
	}
}

func TestDeptMatches(t *testing.T) {
	want := []string{"Instandhaltung", "Elektro"}
	for dept, exp := range map[string]bool{"Instandhaltung": true, "Elektrowerkstatt": true, "ELEKTRO": true, "Verwaltung": false, "": false} {
		if deptMatches(dept, want) != exp {
			t.Errorf("%q: erwartet %v", dept, exp)
		}
	}
}

func TestCompletionKindsComplete(t *testing.T) {
	for typ, k := range completionKinds {
		if k.Type != typ || k.Table == "" || !strings.HasPrefix(k.PartsAPI, "/api/v1/") || k.EditPerm == "" || k.DonePerm == "" || k.RefType == "" {
			t.Errorf("%s unvollständig: %+v", typ, k)
		}
	}
}

func TestWizardInLayout(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "user_detail", UserDetailData{User: UserView{ID: "u1"}})
	for _, want := range []string{`id="cw"`, "window.pdhComplete", "Wer war dabei?", "Kein Material verwendet", "Geht noch weiter", "cwSubmit(true)", `data-min="60"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Assistent: %q fehlt", want)
		}
	}
}

// Kein gruener Haken darf einen Vorgang mehr ohne Assistent abschliessen.
func TestNoBypassCompletion(t *testing.T) {
	root := filepath.Join("..", "..", "web", "templates")
	read := func(n string) string {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for file, want := range map[string]string{
		"ticket_detail.gohtml":      "pdhComplete('ticket'",
		"fault_detail.gohtml":       "pdhComplete('fault'",
		"task_detail.gohtml":        "pdhComplete('task'",
		"maintenance_detail.gohtml": "pdhComplete('maintenance'",
		"tickets.gohtml":            `data-complete="ticket:`,
		"faults.gohtml":             `data-complete="fault:`,
	} {
		s := read(file)
		if !strings.Contains(s, want) {
			t.Errorf("%s: %q fehlt", file, want)
		}
		for _, bad := range []string{"/archive\"", "resolve-web\"", `hx-post="/faults/{{.Fault.ID}}/resolve"`, "complete-web',{method"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: Abschluss ohne Assistent (%s)", file, bad)
			}
		}
	}
	if !regexp.MustCompile(`action === 'done' && item\.dataset\.complete`).MatchString(read("base.gohtml")) {
		t.Error("„Auswahl erledigen“ öffnet den Assistenten nicht")
	}
}

func TestTimePagePending(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := TimePageData{
		Entries:        []TimeEntryView{{ID: "e1", Description: "Ticket: Presse", DurationStr: "1h 30min", Pending: true, PendingFrom: "Max Muster", Mine: true}, {ID: "e2", Description: "Eigen", DurationStr: "20min"}},
		PendingEntries: []TimeEntryView{{ID: "e1", Description: "Ticket: Presse", DurationStr: "1h 30min", Pending: true, PendingFrom: "Max Muster", Mine: true}},
	}
	out := renderPage(t, tmpl, "timetracking", d)
	for _, want := range []string{`id="te-confirm-card"`, "Zu bestätigen (1)", "eingetragen von Max Muster", `class="te-pending"`, "confirmTimeEntry('e1')", "unbestätigt"} {
		if !strings.Contains(out, want) {
			t.Errorf("Zeiterfassung: %q fehlt", want)
		}
	}
	if strings.Count(out, "confirmTimeEntry('e2')") != 0 {
		t.Error("bestätigter Eintrag mit Bestätigen-Knopf")
	}
}
