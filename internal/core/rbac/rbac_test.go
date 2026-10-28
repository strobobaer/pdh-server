package rbac

import "testing"

func newHierarchyService() *Service {
	return &Service{
		roleLevels:   map[string]int{"admin": 100, "manager": 70, "technician": 50, "worker": 30, "viewer": 10},
		maxRoleLevel: 100,
	}
}

func TestOutranksStrictlyLowerRoleAllowed(t *testing.T) {
	s := newHierarchyService()
	if !s.Outranks("manager", "worker") {
		t.Error("manager sollte worker (echt niedrigerer Rang) verwalten dürfen")
	}
	if !s.Outranks("manager", "viewer") {
		t.Error("manager sollte viewer (echt niedrigerer Rang) verwalten dürfen")
	}
}

func TestOutranksEqualOrHigherRoleDeniedForNonTop(t *testing.T) {
	s := newHierarchyService()
	if s.Outranks("manager", "manager") {
		t.Error("manager sollte sich selbst/Gleichrangige nicht verwalten dürfen")
	}
	if s.Outranks("manager", "admin") {
		t.Error("manager sollte admin (ranghöher) nicht verwalten dürfen")
	}
	if s.Outranks("technician", "manager") {
		t.Error("technician sollte manager (ranghöher) nicht verwalten dürfen")
	}
}

func TestOutranksTopRoleBypassesEqualRankCheck(t *testing.T) {
	s := newHierarchyService()
	if !s.Outranks("admin", "admin") {
		t.Error("die ranghöchste Rolle sollte auch Gleichrangige (andere admins) verwalten dürfen")
	}
	if !s.Outranks("admin", "worker") {
		t.Error("die ranghöchste Rolle sollte alle niedrigeren Rollen verwalten dürfen")
	}
}

func TestOutranksUnknownRoleDeniedByDefault(t *testing.T) {
	s := newHierarchyService()
	if s.Outranks("ghost", "worker") {
		t.Error("eine unbekannte Rolle (Rang 0) sollte niemanden verwalten dürfen")
	}
}

func TestRoleLevelUnknownRoleIsZero(t *testing.T) {
	s := newHierarchyService()
	if got := s.RoleLevel("ghost"); got != 0 {
		t.Errorf("RoleLevel(unbekannt) = %d, want 0", got)
	}
	if got := s.RoleLevel("manager"); got != 70 {
		t.Errorf("RoleLevel(manager) = %d, want 70", got)
	}
}

func TestMaxRoleLevel(t *testing.T) {
	s := newHierarchyService()
	if got := s.MaxRoleLevel(); got != 100 {
		t.Errorf("MaxRoleLevel() = %d, want 100", got)
	}
}

func newOverrideService() *Service {
	s := newHierarchyService()
	s.permCache = map[string]map[string]string{
		"worker": {"tickets.view": ""},
	}
	s.userOverrides = map[string]map[string]Override{
		"user-allow": {"tickets.edit": {Granted: true}},  // Einzelrecht zusaetzlich zur Rolle erlaubt
		"user-deny":  {"tickets.view": {Granted: false}}, // Rollen-Recht fuer diesen Nutzer entzogen
	}
	return s
}

func TestHasPermissionForUserFallsBackToRoleWithoutOverride(t *testing.T) {
	s := newOverrideService()
	if !s.HasPermissionForUser("user-none", "worker", "tickets.view") {
		t.Error("ohne Override sollte das Rollen-Recht gelten")
	}
	if s.HasPermissionForUser("user-none", "worker", "tickets.edit") {
		t.Error("ohne Rollen-Recht und ohne Override sollte kein Zugriff bestehen")
	}
}

func TestHasPermissionForUserAllowOverrideGrantsBeyondRole(t *testing.T) {
	s := newOverrideService()
	if !s.HasPermissionForUser("user-allow", "worker", "tickets.edit") {
		t.Error("Allow-Override sollte ein Recht gewaehren, das die Rolle nicht hat")
	}
}

func TestHasPermissionForUserDenyOverrideRevokesRolePermission(t *testing.T) {
	s := newOverrideService()
	if s.HasPermissionForUser("user-deny", "worker", "tickets.view") {
		t.Error("Deny-Override sollte ein von der Rolle gewaehrtes Recht entziehen")
	}
}

// Rechte auf Abteilungsebene und Bereichsadministrator (scope.go).
func newScopeService() *Service {
	return &Service{
		permCache: map[string]map[string]string{
			"meister": {"faults.view": "", "faults.edit": "halle1"},
			"bereich": {"faults.admin": "werk"},
		},
		userOverrides: map[string]map[string]Override{
			"u-deny":  {"faults.edit": {Granted: false}},
			"u-allow": {"tickets.edit": {Granted: true, Department: "halle2"}},
		},
		deptParent: map[string]string{"halle1": "werk", "halle2": "werk", "werk": "", "buero": ""},
	}
}

func TestScopeDepartmentGrant(t *testing.T) {
	s := newScopeService()
	if !s.HasPermissionForUser("x", "meister", "faults.edit") {
		t.Error("abteilungsbegrenztes Recht zählt als vorhanden")
	}
	if !s.HasPermissionInDepartment("x", "meister", "faults.edit", []string{"halle1"}) {
		t.Error("Recht in der eigenen Abteilung")
	}
	if s.HasPermissionInDepartment("x", "meister", "faults.edit", []string{"halle2"}) {
		t.Error("Recht gilt nicht in fremder Abteilung")
	}
	if !s.HasPermissionInDepartment("x", "meister", "faults.edit", nil) {
		t.Error("Datensatz ohne Abteilung: wer das Recht irgendwo hat, darf")
	}
	if !s.HasPermissionInDepartment("x", "meister", "faults.view", []string{"buero"}) {
		t.Error("Recht ohne Abteilung gilt überall")
	}
}

func TestScopeAreaAdmin(t *testing.T) {
	s := newScopeService()
	if !s.HasPermissionInDepartment("x", "bereich", "faults.resolve", []string{"halle2"}) {
		t.Error("Bereichsadmin des Werks darf in Unterabteilung")
	}
	if s.HasPermissionInDepartment("x", "bereich", "faults.resolve", []string{"buero"}) {
		t.Error("Bereichsadmin des Werks darf nicht im Büro")
	}
	if s.HasPermissionForUser("x", "bereich", "tickets.edit") {
		t.Error("Bereichsadmin Störungen hat keine Ticket-Rechte")
	}
	if AreaAdminKey("faults.admin") != "" || AreaAdminKey("faults.edit") != "faults.admin" {
		t.Error("AreaAdminKey")
	}
}

func TestScopeUserOverrides(t *testing.T) {
	s := newScopeService()
	if s.HasPermissionForUser("u-deny", "meister", "faults.edit") || s.HasPermissionForUser("u-deny", "bereich", "faults.edit") {
		t.Error("Einzelverbot gilt auch gegen Rolle und Bereichsadmin")
	}
	if !s.HasPermissionInDepartment("u-allow", "", "tickets.edit", []string{"halle2"}) || s.HasPermissionInDepartment("u-allow", "", "tickets.edit", []string{"halle1"}) {
		t.Error("Einzelerlaubnis mit Abteilung")
	}
}
