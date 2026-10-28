package web

import "testing"

// Welche Schreib-Route braucht welches Recht (perm_gate.go, gateRules).
func TestPermissionGateRules(t *testing.T) {
	const id = "01a18200-0000-7000-8000-000000000001"
	cases := []struct{ method, path, perm, refType string }{
		{"DELETE", "/api/v1/tasks/" + id, "tasks.admin", "task"},
		{"DELETE", "/api/v1/projects/" + id, "projects.admin", "project"},
		{"POST", "/maintenance/tasks/" + id + "/delete-web", "maintenance.admin", "maintenance_task"},
		{"POST", "/kvp/" + id + "/delete", "kvp.admin", "kvp"},
		{"DELETE", "/api/v1/inventory/" + id, "inventory.edit", "part"},
		{"POST", "/faults/" + id + "/resolve", "faults.resolve", "fault"},
		{"POST", "/api/v1/faults/" + id + "/resolve", "faults.resolve", "fault"},
		{"POST", "/faults/" + id + "/analyze", "faults.edit", "fault"},
		{"DELETE", "/api/v1/faults/" + id + "/actions/" + id, "faults.edit", "fault"},
		{"POST", "/tickets/" + id + "/status-web", "tickets.edit", "ticket"},
		{"POST", "/tickets/" + id + "/resolve-web", "tickets.resolve", "ticket"},
		{"POST", "/api/v1/maintenance/tasks/" + id + "/complete", "maintenance.complete", "maintenance_task"},
		{"PUT", "/api/v1/maintenance/plans/" + id, "maintenance.edit", ""},
		{"POST", "/api/v1/tasks", "tasks.edit", ""},
		{"POST", "/api/v1/tasks/" + id + "/status", "tasks.edit", "task"},
		{"POST", "/api/v1/projects/", "projects.edit", ""},
		{"POST", "/it", "it.edit", ""},
		{"PUT", "/api/v1/it/" + id + "/status", "it.edit", "it_asset"},
		{"POST", "/inventory", "inventory.create", ""},
		{"POST", "/api/v1/inventory/stocktake/" + id + "/count", "inventory.book", ""},
		{"POST", "/storage/" + id + "/children-web", "inventory.create", ""},
		{"POST", "/records/fault/" + id + "/people", "faults.edit", "fault"},
		{"POST", "/records/maintenance/" + id + "/archive", "maintenance.complete", "maintenance_task"},
	}
	for _, c := range cases {
		_, perm, refType, _, ok := matchGate(c.method, c.path)
		if !ok || perm != c.perm || refType != c.refType {
			t.Errorf("%s %s: Recht %q (%q), erwartet %q (%q)", c.method, c.path, perm, refType, c.perm, c.refType)
		}
	}
	// offen: Melden, Kommentare, Chat, Zeiterfassung, Lesen
	for _, c := range [][2]string{{"POST", "/faults"}, {"POST", "/api/v1/faults/"}, {"POST", "/tickets"}, {"POST", "/tickets/" + id + "/comment"},
		{"POST", "/api/v1/faults/" + id + "/chat"}, {"POST", "/faults/" + id + "/time/start"}, {"GET", "/api/v1/tasks/" + id}} {
		if _, perm, _, _, ok := matchGate(c[0], c[1]); ok {
			t.Errorf("%s %s sollte offen sein, verlangt %q", c[0], c[1], perm)
		}
	}
}
