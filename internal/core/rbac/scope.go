package rbac

import "strings"

// Rechte auf Abteilungsebene und Bereichsadministratoren.
//
// Jedes Recht gilt entweder in allen Abteilungen oder nur in einer Abteilung
// samt Unterabteilungen (role_permissions.department_id bzw.
// user_permissions.department_id, Migration 147). Das Recht
// „<bereich>.admin“ (z. B. faults.admin) schließt alle Rechte des Bereichs
// ein (faults.view, faults.edit, …) – in seiner Abteilung bzw. überall.

// Override: Einzelberechtigung eines Benutzers. Ein Verbot gilt überall,
// eine Erlaubnis in Department ("" = in allen Abteilungen).
type Override struct {
	Granted    bool
	Department string
}

// AreaAdminKey: das Bereichsadministrator-Recht, das permissionKey
// einschließt ("" = keins, z. B. für die Admin-Rechte selbst).
func AreaAdminKey(permissionKey string) string {
	i := strings.IndexByte(permissionKey, '.')
	if i <= 0 || strings.HasSuffix(permissionKey, ".admin") {
		return ""
	}
	return permissionKey[:i] + ".admin"
}

// Scope: wo ein Benutzer ein Recht hat – überall (all) oder in den
// genannten Abteilungen (jeweils samt Unterabteilungen). Eine
// Einzelberechtigung hat Vorrang vor der Rolle; das Bereichsadministrator-
// Recht kommt hinzu. userID "" prüft nur die Rolle.
func (s *Service) Scope(userID, roleKey, permissionKey string) (all bool, departments []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	add := func(key string) {
		if all {
			return
		}
		if o, ok := s.userOverrides[userID][key]; ok && userID != "" {
			if !o.Granted {
				return
			}
			if o.Department == "" {
				all = true
				return
			}
			departments = append(departments, o.Department)
			return
		}
		if dept, ok := s.permCache[roleKey][key]; ok {
			if dept == "" {
				all = true
				return
			}
			departments = append(departments, dept)
		}
	}
	add(permissionKey)
	if admin := AreaAdminKey(permissionKey); admin != "" {
		// ein ausdrückliches Verbot des Einzelrechts gilt auch gegen den Bereichsadmin
		if o, ok := s.userOverrides[userID][permissionKey]; !(ok && userID != "" && !o.Granted) {
			add(admin)
		}
	}
	if all {
		departments = nil
	}
	return all, departments
}

// HasPermissionInDepartment: hat der Benutzer das Recht für einen Datensatz
// der genannten Abteilungen? Datensätze ohne Abteilung gelten – wie bei der
// Sichtbarkeit – für alle, die das Recht irgendwo haben.
func (s *Service) HasPermissionInDepartment(userID, roleKey, permissionKey string, recordDepartments []string) bool {
	all, depts := s.Scope(userID, roleKey, permissionKey)
	if all {
		return true
	}
	if len(depts) == 0 {
		return false
	}
	if len(recordDepartments) == 0 {
		return true
	}
	for _, rd := range recordDepartments {
		if s.inAnyDepartment(rd, depts) {
			return true
		}
	}
	return false
}

// inAnyDepartment: liegt dept in einer der Abteilungen (oder darunter)?
func (s *Service) inAnyDepartment(dept string, roots []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	for d := dept; d != "" && !seen[d]; d = s.deptParent[d] {
		seen[d] = true
		for _, r := range roots {
			if r == d {
				return true
			}
		}
	}
	return false
}
