package web

import "testing"

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
