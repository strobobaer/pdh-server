package faults

import "testing"

// Ticket aus Störung erst nach der Zuweisung (migrations/111, deck_sync.go).
func TestAutoTicketNeedsAssignment(t *testing.T) {
	t.Setenv("PDH_FAULT_CREATE_TICKET", "")
	t.Setenv("PDH_FAULT_CREATE_TICKET_ENABLED", "")
	if autoTicketEnabled() {
		t.Error("ohne Einstellung kein automatisches Ticket")
	}
	t.Setenv("PDH_FAULT_CREATE_TICKET", "true")
	if !autoTicketEnabled() {
		t.Error("Einstellung wird nicht erkannt")
	}
	empty, someone := "", "u1"
	for _, c := range []struct {
		f    *Fault
		want bool
	}{
		{&Fault{}, false},
		{&Fault{AssignedTo: &empty}, false},
		{&Fault{ResponsibleTo: &someone}, false}, // nur verantwortlich ist keine Zuweisung
		{&Fault{AssignedTo: &someone}, true},
	} {
		if got := faultAssigned(c.f); got != c.want {
			t.Errorf("%+v: %v, erwartet %v", c.f, got, c.want)
		}
	}
}
