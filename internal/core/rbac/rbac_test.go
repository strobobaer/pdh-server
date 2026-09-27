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
	s.permCache = map[string]map[string]bool{
		"worker": {"tickets.view": true},
	}
	s.userOverrides = map[string]map[string]bool{
		"user-allow": {"tickets.edit": true},  // Einzelrecht zusaetzlich zur Rolle erlaubt
		"user-deny":  {"tickets.view": false}, // Rollen-Recht fuer diesen Nutzer entzogen
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
