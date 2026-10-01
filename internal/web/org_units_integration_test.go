//go:build integration

// Integrationstest gegen eine echte PostgreSQL mit allen Migrationen.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration ./internal/web/
package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/internal/modules/it"
	"pdh/internal/modules/projects"
)

func TestOrgUnitsProjectsITIntegration(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{db: pool}
	sfx := time.Now().Format("150405.000000")

	var u1, u2, depID, grpID, roleID string
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, department)
		VALUES ('a'||$1::text, 'a'||$1::text||'@x', 'x', 'Anna', 'Org', 'Lager '||$1::text) RETURNING id::text`, sfx).Scan(&u1))
	must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name)
		VALUES ('b'||$1::text, 'b'||$1::text||'@x', 'x', 'Bert', 'Org') RETURNING id::text`, sfx).Scan(&u2))
	must(pool.QueryRow(ctx, `SELECT department_id::text FROM users WHERE id=$1::uuid`, u1).Scan(&depID))
	must(pool.QueryRow(ctx, `INSERT INTO user_groups (name, department_id, lead_id) VALUES ('Gruppe '||$1::text, $2::uuid, $3::uuid) RETURNING id::text`, sfx, depID, u1).Scan(&grpID))
	_, err = pool.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid), ($1::uuid, $3::uuid)`, grpID, u1, u2)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO roles (key, label) VALUES ('r'||replace($1::text,'.',''), 'Rolle') RETURNING id::text`, sfx).Scan(&roleID))
	_, err = pool.Exec(ctx, `UPDATE roles SET department_id=$1::uuid WHERE id=$2::uuid`, depID, roleID)
	must(err)

	var dep *deptView
	for _, d := range h.loadDepartments(ctx) {
		if d.ID == depID {
			d := d
			dep = &d
		}
	}
	if dep == nil || dep.MemberCount != 1 || dep.RoleCount != 1 {
		t.Fatalf("Abteilung: %+v", dep)
	}
	var grp *groupView
	for _, g := range h.loadGroups(ctx) {
		if g.ID == grpID {
			g := g
			grp = &g
		}
	}
	if grp == nil || len(grp.MemberNames) != 2 || !grp.MemberIDs[u2] || grp.LeadName != "Anna Org" || grp.DepartmentName != dep.Name {
		t.Fatalf("Gruppe: %+v", grp)
	}
	if gs := h.userGroups(ctx, u2); len(gs) != 1 || gs[0].ID != grpID {
		t.Fatalf("Gruppen der Person: %+v", gs)
	}
	if h.roleDepartments(ctx)[roleID] != depID {
		t.Fatal("Rollen-Abteilung fehlt")
	}

	// Projekt mit beiden Zustaendigkeiten + Beteiligte-Abfrage
	prepo := projects.NewRepository(pool)
	p := &projects.Project{Name: "Projekt " + sfx, ResponsibleTo: &u1, AssignedTo: &u2, CreatedBy: u1}
	must(prepo.Create(ctx, p))
	got, err := prepo.GetByID(ctx, p.ID)
	must(err)
	if got.AssigneeName != "Bert Org" || got.ResponsibleName != "Anna Org" {
		t.Fatalf("Projekt: %+v", got)
	}
	people := h.recordPeople(ctx, "project", p.ID)
	if people.AssignedID != u2 || people.ResponsibleID != u1 {
		t.Fatalf("recordPeople(project): %+v", people)
	}

	// IT-Asset: Verantwortlich
	irepo := it.NewRepository(pool)
	a := &it.Asset{Name: "PC " + sfx, Type: it.TypeWorkstation, CreatedBy: u1}
	must(irepo.Create(ctx, a))
	must(irepo.UpdateDetails(ctx, a.ID, &it.UpdateDetailsInput{Name: a.Name, Type: it.TypeWorkstation, AssignedTo: &u2, ResponsibleTo: &u1}))
	ga, err := irepo.GetByID(ctx, a.ID)
	must(err)
	if ga.ResponsibleName != "Anna Org" || ga.AssigneeName != "Bert Org" {
		t.Fatalf("IT-Asset: %+v", ga)
	}

	// Gruppen-Zuweisung: Ticket an Gruppe -> "Mir zugewiesen" fuer Mitglied u2,
	// Aenderungshinweis an u2, Beteiligte zeigen die Gruppe
	var ticketID string
	must(pool.QueryRow(ctx, `INSERT INTO tickets (title, description, priority, status, created_by, assigned_group_id)
		VALUES ('Ticket '||$1::text, '', 'medium', 'open', $2::uuid, $3::uuid) RETURNING id::text`, sfx, u1, grpID).Scan(&ticketID))
	list, err := loadListMine(h, ctx, u2, WidgetInstance{}, func(string) bool { return true })
	must(err)
	found := false
	for _, it := range list.([]listItem) {
		found = found || strings.HasSuffix(it.URL, ticketID)
	}
	if !found {
		t.Fatalf("Ticket der Gruppe fehlt unter Mir zugewiesen: %+v", list)
	}
	if _, err := loadStatMyTasks(h, ctx, u2, WidgetInstance{}, nil); err != nil {
		t.Fatalf("Meine Aufgaben: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET change_notifications = true WHERE id = $1::uuid`, u2); err != nil {
		t.Fatal(err)
	}
	rec := h.changeRecipients(ctx, "ticket", ticketID)
	hasU2 := false
	for _, id := range rec {
		hasU2 = hasU2 || id == u2
	}
	if !hasU2 {
		t.Fatalf("Gruppenmitglied fehlt bei den Hinweis-Empfängern: %v", rec)
	}
	if gid := h.recordGroupID(ctx, "tickets", ticketID); gid != grpID {
		t.Fatalf("recordGroupID: %q", gid)
	}
	if opts := h.groupOptionsHTML(ctx, grpID); !strings.Contains(opts, `value="`+grpID+`" selected`) {
		t.Fatalf("Gruppenauswahl: %s", opts)
	}
}
