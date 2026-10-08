package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsGlobalBoardDepartment(t *testing.T) {
	tests := []struct {
		department string
		want       bool
	}{
		{department: "Instandhaltung", want: true},
		{department: "Werkstatt Instandhaltung", want: true},
		{department: "IT", want: true},
		{department: "IT-Service", want: true},
		{department: "Informationstechnik", want: true},
		{department: "Produktion", want: false},
		{department: "Qualität", want: false},
		{department: "", want: false},
	}
	for _, test := range tests {
		t.Run(test.department, func(t *testing.T) {
			if got := isGlobalBoardDepartment(test.department); got != test.want {
				t.Errorf("isGlobalBoardDepartment(%q) = %t, want %t", test.department, got, test.want)
			}
		})
	}
}

func TestGlobalBoardActionAndTypeAllowLists(t *testing.T) {
	for _, action := range []string{"accept", "done", "discard", "wait"} {
		if !globalBoardActionAllowed(action) {
			t.Errorf("expected action %q to be allowed", action)
		}
	}
	for _, action := range []string{"", "delete", "resolve"} {
		if globalBoardActionAllowed(action) {
			t.Errorf("expected action %q to be rejected", action)
		}
	}
	for _, refType := range []string{"fault", "ticket", "maintenance", "task"} {
		if !globalBoardTypeAllowed(refType) {
			t.Errorf("expected type %q to be allowed", refType)
		}
	}
	for _, refType := range []string{"", "user", "project"} {
		if globalBoardTypeAllowed(refType) {
			t.Errorf("expected type %q to be rejected", refType)
		}
	}
}

func TestValidateGlobalParts(t *testing.T) {
	tests := []struct {
		name          string
		noPartsNeeded bool
		pendingParts  int
		wantError     bool
	}{
		{name: "no parts confirmed", noPartsNeeded: true, pendingParts: 0},
		{name: "parts are pending", pendingParts: 2},
		{name: "no confirmation and no parts", wantError: true},
		{name: "confirmation conflicts with pending parts", noPartsNeeded: true, pendingParts: 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateGlobalParts(test.noPartsNeeded, test.pendingParts)
			if (err != nil) != test.wantError {
				t.Fatalf("validateGlobalParts() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

// Zeitstrahl verschieben: öffentliche Tafel nur ansehen, abgelaufene Anmeldung
// führt zur Anmeldung statt „kein token“.
func TestTimelineMoveNeedsLogin(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	if _, err := c.ParseFiles(filepath.Join("..", "..", "web", "templates", "global_dashboard.gohtml")); err != nil {
		t.Fatal(err)
	}
	render := func(loggedIn bool) string {
		var b strings.Builder
		if err := c.ExecuteTemplate(&b, "global_dashboard.gohtml", GlobalDashboardPageData{LoggedIn: loggedIn}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	anon := render(false)
	checkScripts(t, "leitstand", anon)
	if !strings.Contains(anon, `class="gantt-ro"`) || !strings.Contains(anon, "Termine verschieben geht nach der Anmeldung") {
		t.Error("öffentliche Tafel: Zeitstrahl muss nur lesbar sein")
	}
	if strings.Contains(render(true), `class="gantt-ro"`) {
		t.Error("angemeldet: Zeitstrahl verschiebbar")
	}
	for _, f := range []string{"dashboard.gohtml", "project_detail.gohtml", "global_dashboard.gohtml"} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "web", "templates", f))
		if !strings.Contains(string(b), "res.status === 401") || !strings.Contains(string(b), "/login?next=") {
			t.Errorf("%s: abgelaufene Anmeldung beim Verschieben nicht behandelt", f)
		}
	}
}

// Zuweisung zeigt alle Arten; zuweisen nur als Broker der Art oder Admin.
func TestAssignmentBoardShowsAllKinds(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := AssignmentBoardData{Users: []UserOption{{ID: "u1", Name: "Eva"}}, Groups: []unassignedGroup{
		{Kind: "ticket", Ref: "ticket", Label: "Tickets", CanAssign: true, Items: []unassignedItem{{ID: "t1", Ref: "ticket", Title: "Tür klemmt"}}},
		{Kind: "task", Ref: "task", Label: "Aufgaben", Brokers: "Max Muster", Items: []unassignedItem{{ID: "a1", Ref: "task", Title: "Regal aufbauen"}}},
		{Kind: "maintenance", Ref: "maintenance_task", Label: "Wartungen"},
	}}
	out := renderPage(t, tmpl, "assignment_board", d)
	for _, want := range []string{`hx-post="/assignments/ticket/t1"`, "Regal aufbauen", "weist zu: Max Muster", "Wartungen", "Alles zugewiesen"} {
		if !strings.Contains(out, want) {
			t.Errorf("Zuweisung ohne %q", want)
		}
	}
	if strings.Contains(out, `hx-post="/assignments/task/a1"`) {
		t.Error("ohne Broker-Recht darf keine Zuweisung angeboten werden")
	}
}
