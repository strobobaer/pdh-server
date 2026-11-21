//go:build integration

// „Meine Vorgänge“ der PDH-App gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run AppWork ./internal/web/
package web

import (
	"context"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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
	e.exec(`INSERT INTO faults (title, severity, status, infrastructure_id, assigned_to, created_by)
		VALUES ($1, 'high', 'in_progress', $2::uuid, $3::uuid, $4::uuid)`, e.title("Stoerung"), e.infra, e.uid, e.other)
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
		e.exec(`INSERT INTO faults (title, severity, status, infrastructure_id, assigned_to, created_by) VALUES ($1, 'low', 'detected', $2::uuid, $3::uuid, $4::uuid)`,
			e.title("ZugewiesenFremd"), infraB, e.uid, e.other)
		e.warm()
		appDeviceCache.forget(e.devID)
		e.wantOnly("Stoerung", "Ticket", "Aufgabe", "Wartung", "ZugewiesenFremd")
	})
}
