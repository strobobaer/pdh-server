//go:build integration

// „Meine Vorgänge“ der PDH-App gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run AppWork ./internal/web/
package web

import (
	"context"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
)

type appWorkEnv struct {
	*appAuthEnv
	infra, infraName string
	other            string // anderer Benutzer (Ersteller der fremden Vorgänge)
	group            string
}

func newAppWorkEnv(t *testing.T) *appWorkEnv {
	e := &appWorkEnv{appAuthEnv: newAppAuthEnv(t)}
	r := chi.NewRouter()
	r.With(e.h.AppAuth).Get("/api/app/v1/work", e.h.AppWorkAPI)
	r.With(e.h.AppAuth).Get("/api/app/v1/work/{type}/{id}", e.h.AppWorkDetailAPI)
	e.h.faults = faults.NewService(faults.NewRepository(e.pool), nil)
	e.h.tickets = tickets.NewService(tickets.NewRepository(e.pool))
	e.h.tasks = tasks.NewService(tasks.NewRepository(e.pool))
	e.h.maint = maintenance.NewService(maintenance.NewRepository(e.pool))
	e.router = r
	for _, p := range []string{"tickets.view", "faults.view", "maintenance.view"} {
		e.grant(e.uid, p, "", true)
	}
	e.warm()
	e.infra = e.atEnv.infra("Presse ", "", "")
	e.infraName = e.id(`SELECT name FROM infrastructure WHERE id = $1::uuid`, e.infra)
	e.other = e.user("other", "worker", false)
	e.group = e.id(`INSERT INTO user_groups (name) VALUES ($1) RETURNING id::text`, "G"+e.sfx)
	e.exec(`INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, e.group, e.uid)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.pool.Exec(bg, `DELETE FROM task_assignees WHERE task_id IN (SELECT id FROM tasks WHERE title LIKE '%'||$1)`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM tasks WHERE title LIKE '%'||$1`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM maintenance_tasks WHERE title LIKE '%'||$1`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM tickets WHERE title LIKE '%'||$1`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM faults WHERE title LIKE '%'||$1`, e.sfx)
		_, _ = e.pool.Exec(bg, `DELETE FROM user_group_members WHERE group_id = $1::uuid`, e.group)
		_, _ = e.pool.Exec(bg, `DELETE FROM user_groups WHERE id = $1::uuid`, e.group)
	})
	return e
}

func (e *appWorkEnv) title(k string) string { return k + " " + e.sfx }

// seed legt je Art einen offenen Vorgang für den Benutzer an (Störung: zugewiesen,
// Ticket: über die Gruppe, Aufgabe: als Beteiligter, Wartung: zugewiesen).
func (e *appWorkEnv) seed() {
	e.exec(`INSERT INTO faults (title, description, severity, status, infrastructure_id, assigned_to, created_by)
		VALUES ($1, 'Beschreibung', 'high', 'in_progress', $2::uuid, $3::uuid, $4::uuid)`, e.title("Stoerung"), e.infra, e.uid, e.other)
	e.exec(`INSERT INTO tickets (title, priority, status, assigned_group_id, created_by, due_date)
		VALUES ($1, 'medium', 'open', $2::uuid, $3::uuid, now() + interval '3 days')`, e.title("Ticket"), e.group, e.other)
	tid := e.id(`INSERT INTO tasks (title, status, created_by) VALUES ($1, 'open', $2::uuid) RETURNING id::text`, e.title("Aufgabe"), e.other)
	e.exec(`INSERT INTO task_assignees (task_id, user_id) VALUES ($1::uuid, $2::uuid)`, tid, e.uid)
	e.exec(`INSERT INTO maintenance_tasks (title, infrastructure_id, priority, status, due_date, assigned_to, created_by)
		VALUES ($1, $2::uuid, 'low', 'open', now() + interval '1 day', $3::uuid, $4::uuid)`, e.title("Wartung"), e.infra, e.uid, e.other)
}

// workList: die Vorgänge dieses Tests in Antwortreihenfolge (Kurzname -> Eintrag).
func (e *appWorkEnv) workList() ([]string, map[string]map[string]any) {
	e.t.Helper()
	rec := e.call("GET", "/api/app/v1/work", nil, e.key, e.token, "1.0.0")
	if rec.Code != 200 {
		e.t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	items, _ := appJSON(e.t, rec)["items"].([]any)
	var order []string
	out := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		title, _ := m["title"].(string)
		if strings.HasSuffix(title, " "+e.sfx) {
			k := strings.TrimSuffix(title, " "+e.sfx)
			order = append(order, k)
			out[k] = m
		}
	}
	return order, out
}

func (e *appWorkEnv) wantOnly(keys ...string) map[string]map[string]any {
	e.t.Helper()
	_, got := e.workList()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
		if got[k] == nil {
			e.t.Errorf("%s fehlt: %v", k, got)
		}
	}
	for k := range got {
		if !want[k] {
			e.t.Errorf("%s dürfte nicht da sein", k)
		}
	}
	return got
}

func TestAppWorkList(t *testing.T) {
	t.Run("alle vier Arten mit Anlage und Typ", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		got := e.wantOnly("Stoerung", "Ticket", "Aufgabe", "Wartung")
		for k, typ := range map[string]string{"Stoerung": "fault", "Ticket": "ticket", "Aufgabe": "task", "Wartung": "maintenance"} {
			if got[k]["type"] != typ {
				t.Errorf("%s: type %v", k, got[k]["type"])
			}
			if got[k]["id"] == "" || got[k]["status"] == "" || got[k]["updated_at"] == nil {
				t.Errorf("%s: %v", k, got[k])
			}
		}
		a := got["Stoerung"]["asset"].(map[string]any)
		if a["id"] != e.infra || a["name"] != e.infraName {
			t.Errorf("anlage: %v", a)
		}
		if got["Stoerung"]["priority"] != "high" || got["Ticket"]["due"] == nil || got["Aufgabe"]["due"] != nil {
			t.Errorf("felder: %v", got)
		}
		if a := got["Aufgabe"]["asset"].(map[string]any); a["id"] != "" || a["name"] != "" {
			t.Errorf("Vorgang ohne Anlage: %v", a)
		}
	})
	t.Run("sortiert nach Fälligkeit (leer zuletzt), dann Priorität", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		order, _ := e.workList()
		// Wartung (morgen), Ticket (in 3 Tagen), ohne Fälligkeit: Störung (hoch) vor Aufgabe (mittel)
		if strings.Join(order, ",") != "Wartung,Ticket,Stoerung,Aufgabe" {
			t.Errorf("reihenfolge: %v", order)
		}
	})
	t.Run("Gerät mit allowed_types fault zeigt nur Störungen", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		e.exec(`UPDATE app_devices SET allowed_types = '{fault}' WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		e.wantOnly("Stoerung")
	})
	t.Run("ohne tickets.view kein Ticket", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		e.exec(`UPDATE user_permissions SET granted = false WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'tickets.view')`, e.uid)
		e.warm()
		e.wantOnly("Stoerung", "Aufgabe", "Wartung")
	})
	t.Run("geschlossene Vorgänge fehlen", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		e.exec(`UPDATE faults SET status = 'closed' WHERE title LIKE '%'||$1`, e.sfx)
		e.exec(`UPDATE tickets SET status = 'resolved' WHERE title LIKE '%'||$1`, e.sfx)
		e.exec(`UPDATE tasks SET status = 'done' WHERE title LIKE '%'||$1`, e.sfx)
		e.exec(`UPDATE maintenance_tasks SET status = 'done' WHERE title LIKE '%'||$1`, e.sfx)
		e.wantOnly()
	})
	t.Run("fremde Vorgänge fehlen", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.exec(`INSERT INTO tickets (title, priority, status, created_by) VALUES ($1, 'low', 'open', $2::uuid)`, e.title("Fremd"), e.other)
		e.wantOnly()
	})
	t.Run("abteilungsbegrenzt: eigene Arbeit bleibt, Fremdes fehlt", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		deptA, deptB := e.dept("DA"), e.dept("DB")
		infraB := e.atEnv.infra("Fremd ", deptB, "")
		role := "appdept" + e.sfx
		e.exec(`INSERT INTO roles (key, label, level, department_id) VALUES ($1, 'AD', 10, $2::uuid)`, role, deptA)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
		e.exec(`UPDATE users SET role = $2 WHERE id = $1::uuid`, e.uid, role)
		// fremde Abteilung, nicht beteiligt: fehlt
		e.exec(`INSERT INTO tickets (title, priority, status, infrastructure_id, created_by) VALUES ($1, 'low', 'open', $2::uuid, $3::uuid)`, e.title("FremdeAbt"), infraB, e.other)
		// fremde Abteilung, aber zugewiesen: Beteiligung zählt wie im Web
		e.exec(`INSERT INTO faults (title, description, severity, status, infrastructure_id, assigned_to, created_by) VALUES ($1, '', 'low', 'detected', $2::uuid, $3::uuid, $4::uuid)`,
			e.title("ZugewiesenFremd"), infraB, e.uid, e.other)
		e.warm()
		appDeviceCache.forget(e.devID)
		e.wantOnly("Stoerung", "Ticket", "Aufgabe", "Wartung", "ZugewiesenFremd")
	})
}

// detail ruft die Einzelansicht auf; liefert Statuscode und Antwort.
func (e *appWorkEnv) detail(kind, id string) (int, map[string]any) {
	e.t.Helper()
	rec := e.call("GET", "/api/app/v1/work/"+kind+"/"+id, nil, e.key, e.token, "1.0.0")
	return rec.Code, appJSON(e.t, rec)
}

func (e *appWorkEnv) idOf(table, k string) string {
	return e.id(`SELECT id::text FROM `+table+` WHERE title = $1`, e.title(k))
}

func (e *appWorkEnv) can(table, k string) map[string]any {
	e.t.Helper()
	kind := map[string]string{"faults": "fault", "tickets": "ticket", "tasks": "task", "maintenance_tasks": "maintenance"}[table]
	code, m := e.detail(kind, e.idOf(table, k))
	if code != 200 {
		e.t.Fatalf("%s: %d %v", kind, code, m)
	}
	return m["can"].(map[string]any)
}

func TestAppWorkDetail(t *testing.T) {
	t.Run("je Art die Felder, Schritte nur Wartung, Kommentare nur Ticket", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		fid, tid, kid, mid := e.idOf("faults", "Stoerung"), e.idOf("tickets", "Ticket"), e.idOf("tasks", "Aufgabe"), e.idOf("maintenance_tasks", "Wartung")
		e.exec(`INSERT INTO ticket_comments (ticket_id, user_id, text) VALUES ($1::uuid, $2::uuid, 'Kommentar eins')`, tid, e.other)
		e.exec(`INSERT INTO fault_actions (fault_id, description, created_by) VALUES ($1::uuid, 'Sicherung getauscht', $2::uuid)`, fid, e.other)
		e.exec(`INSERT INTO maintenance_task_steps (task_id, label, item_type, required, unit, min_value, max_value, sort_order)
			VALUES ($1::uuid, 'Druck prüfen', 'number', true, 'bar', 1, 9, 1)`, mid)
		for kind, id := range map[string]string{"fault": fid, "ticket": tid, "task": kid, "maintenance": mid} {
			code, m := e.detail(kind, id)
			if code != 200 {
				t.Fatalf("%s: %d %v", kind, code, m)
			}
			for _, f := range []string{"type", "id", "number", "title", "description", "status", "priority", "asset", "due", "actions", "history", "can"} {
				if _, ok := m[f]; !ok {
					t.Errorf("%s: Feld %s fehlt", kind, f)
				}
			}
			if m["type"] != kind || m["id"] != id {
				t.Errorf("%s: %v", kind, m)
			}
			if _, ok := m["steps"]; ok != (kind == "maintenance") {
				t.Errorf("%s: steps vorhanden=%v", kind, ok)
			}
			if _, ok := m["comments"]; ok != (kind == "ticket") {
				t.Errorf("%s: comments vorhanden=%v", kind, ok)
			}
		}
		_, m := e.detail("fault", fid)
		if a := m["asset"].(map[string]any); a["id"] != e.infra || a["name"] != e.infraName {
			t.Errorf("anlage: %v", a)
		}
		if acts := m["actions"].([]any); len(acts) != 1 || acts[0].(map[string]any)["text"] != "Sicherung getauscht" {
			t.Errorf("maßnahmen: %v", m["actions"])
		}
		_, m = e.detail("ticket", tid)
		if cs := m["comments"].([]any); len(cs) != 1 || cs[0].(map[string]any)["text"] != "Kommentar eins" {
			t.Errorf("kommentare: %v", m["comments"])
		}
		_, m = e.detail("maintenance", mid)
		st := m["steps"].([]any)
		if len(st) != 1 {
			t.Fatalf("schritte: %v", st)
		}
		s0 := st[0].(map[string]any)
		if s0["label"] != "Druck prüfen" || s0["kind"] != "number" || s0["required"] != true || s0["min"].(float64) != 1 || s0["max"].(float64) != 9 || s0["id"] == "" {
			t.Errorf("schritt: %v", s0)
		}
	})
	t.Run("Gerät ohne ticket: Ticket 403, Störung weiter erlaubt", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		e.exec(`UPDATE app_devices SET allowed_types = '{fault,task,maintenance}' WHERE id = $1::uuid`, e.devID)
		appDeviceCache.forget(e.devID)
		code, m := e.detail("ticket", e.idOf("tickets", "Ticket"))
		if code != 403 || m["code"] != "app_no_permission" {
			t.Errorf("%d %v", code, m)
		}
		if code, _ := e.detail("fault", e.idOf("faults", "Stoerung")); code != 200 {
			t.Errorf("störung %d", code)
		}
	})
	t.Run("unbekannt: 404 app_not_found", func(t *testing.T) {
		e := newAppWorkEnv(t)
		for _, c := range [][2]string{{"fault", "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"}, {"ufo", "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"}, {"fault", "kaputt"}, {"setup", "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"}} {
			code, m := e.detail(c[0], c[1])
			if code != 404 || m["code"] != "app_not_found" {
				t.Errorf("%v: %d %v", c, code, m)
			}
		}
	})
	t.Run("ohne Ansichtsrecht und nicht beteiligt: 403, beteiligt: sichtbar", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.exec(`INSERT INTO tickets (title, priority, status, created_by) VALUES ($1, 'low', 'open', $2::uuid)`, e.title("Fremd"), e.other)
		id := e.idOf("tickets", "Fremd")
		if code, _ := e.detail("ticket", id); code != 200 { // tickets.view vorhanden
			t.Fatalf("mit Recht %d", code)
		}
		e.exec(`UPDATE user_permissions SET granted = false WHERE user_id = $1::uuid AND permission_id = (SELECT id FROM permissions WHERE key = 'tickets.view')`, e.uid)
		e.warm()
		if code, m := e.detail("ticket", id); code != 403 || m["code"] != "app_no_permission" {
			t.Errorf("ohne Recht: %d %v", code, m)
		}
		e.exec(`UPDATE tickets SET assigned_to = $2::uuid WHERE id = $1::uuid`, id, e.uid)
		if code, _ := e.detail("ticket", id); code != 200 {
			t.Errorf("beteiligt: %d", code)
		}
	})
	t.Run("fremde Abteilung: 403, zugewiesen: sichtbar", func(t *testing.T) {
		e := newAppWorkEnv(t)
		deptA, deptB := e.dept("DA"), e.dept("DB")
		infraB := e.atEnv.infra("Fremd ", deptB, "")
		role := "appdept" + e.sfx
		e.exec(`INSERT INTO roles (key, label, level, department_id) VALUES ($1, 'AD', 10, $2::uuid)`, role, deptA)
		t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM roles WHERE key = $1`, role) })
		e.exec(`UPDATE users SET role = $2 WHERE id = $1::uuid`, e.uid, role)
		e.exec(`INSERT INTO faults (title, description, severity, status, infrastructure_id, created_by) VALUES ($1, '', 'low', 'detected', $2::uuid, $3::uuid)`,
			e.title("FremdeAbt"), infraB, e.other)
		e.warm()
		appDeviceCache.forget(e.devID)
		id := e.idOf("faults", "FremdeAbt")
		if code, m := e.detail("fault", id); code != 403 || m["code"] != "app_no_permission" {
			t.Errorf("fremde Abteilung: %d %v", code, m)
		}
		e.exec(`UPDATE faults SET assigned_to = $2::uuid WHERE id = $1::uuid`, id, e.uid)
		if code, _ := e.detail("fault", id); code != 200 {
			t.Errorf("zugewiesen: %d", code)
		}
	})
	t.Run("Betrachter: nicht starten, nicht abschließen", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		e.exec(`UPDATE users SET role = 'viewer' WHERE id = $1::uuid`, e.uid)
		e.warm()
		appDeviceCache.forget(e.devID)
		for table, k := range map[string]string{"faults": "Stoerung", "tickets": "Ticket", "maintenance_tasks": "Wartung"} {
			c := e.can(table, k)
			if c["finish"] != false || c["start"] != false {
				t.Errorf("%s: %v", table, c)
			}
		}
	})
	t.Run("beteiligter Werker ohne faults.edit darf Maßnahme (wie Web)", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		c := e.can("faults", "Stoerung")
		if c["action"] != true || c["start"] != true || c["finish"] != true {
			t.Errorf("störung: %v", c)
		}
		if c["comment"] != false || c["step"] != false {
			t.Errorf("störung kann weder kommentieren noch Schritte: %v", c)
		}
		if c := e.can("tickets", "Ticket"); c["comment"] != true || c["step"] != false {
			t.Errorf("ticket: %v", c)
		}
		// Wartung: Schritte ja, Maßnahmen trägt das Web nur beim Abschließen ein
		if c := e.can("maintenance_tasks", "Wartung"); c["step"] != true || c["action"] != false || c["finish"] != true {
			t.Errorf("wartung: %v", c)
		}
	})
	t.Run("geschlossener Vorgang: alle can false", func(t *testing.T) {
		e := newAppWorkEnv(t)
		e.seed()
		e.exec(`UPDATE faults SET status = 'closed' WHERE title LIKE '%'||$1`, e.sfx)
		e.exec(`UPDATE tickets SET status = 'resolved' WHERE title LIKE '%'||$1`, e.sfx)
		e.exec(`UPDATE tasks SET status = 'closed' WHERE title LIKE '%'||$1`, e.sfx)
		e.exec(`UPDATE maintenance_tasks SET status = 'done' WHERE title LIKE '%'||$1`, e.sfx)
		for table, k := range map[string]string{"faults": "Stoerung", "tickets": "Ticket", "tasks": "Aufgabe", "maintenance_tasks": "Wartung"} {
			for f, v := range e.can(table, k) {
				if v != false {
					t.Errorf("%s: can.%s = %v", table, f, v)
				}
			}
		}
	})
}

func TestAppWorkDetailNotInvolved(t *testing.T) {
	e := newAppWorkEnv(t)
	third := e.user("third", "worker", false)
	// einem Dritten zugewiesen: Ansehen über tickets.view, aber ohne Bearbeiten-Recht keine Aktion außer Kommentar
	e.exec(`INSERT INTO tickets (title, priority, status, assigned_to, created_by) VALUES ($1, 'low', 'open', $2::uuid, $2::uuid)`, e.title("Dritter"), third)
	e.grant(e.uid, "tickets.edit", "", false)
	e.grant(e.uid, "tickets.resolve", "", false)
	e.warm()
	c := e.can("tickets", "Dritter")
	if c["start"] != false || c["action"] != false || c["finish"] != false || c["step"] != false || c["comment"] != true {
		t.Errorf("nicht beteiligt: %v", c)
	}
	// mit tickets.edit und tickets.resolve (global) darf er alles Zutreffende
	e.exec(`UPDATE user_permissions SET granted = true WHERE user_id = $1::uuid AND permission_id IN (SELECT id FROM permissions WHERE key IN ('tickets.edit', 'tickets.resolve'))`, e.uid)
	e.warm()
	c = e.can("tickets", "Dritter")
	if c["start"] != true || c["action"] != true || c["finish"] != true {
		t.Errorf("mit Recht: %v", c)
	}
}
