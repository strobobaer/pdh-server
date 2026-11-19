package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"

	"github.com/jackc/pgx/v5/pgxpool"
)

// viewFx: Labor-Fixture für die Ansichtsrechte (Abteilungen A und B, je eine
// Anlage, eine Gruppe in A, Rollen und Benutzer).
type viewFx struct {
	t       *testing.T
	pool    *pgxpool.Pool
	h       *Handler
	rb      *rbac.Service
	sfx     string
	deptA   string
	deptB   string
	infraA  string
	infraB  string
	groupA  string
	creator string // Ersteller aller Datensätze, ohne Rechte und nie beteiligt außer als Ersteller
}

func (f *viewFx) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *viewFx) id(q string, args ...any) string {
	f.t.Helper()
	var s string
	f.must(f.pool.QueryRow(context.Background(), q, args...).Scan(&s))
	return s
}

func (f *viewFx) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), q, args...)
	f.must(err)
}

// role legt eine Rolle an; perms: Recht -> Abteilung ("" = überall).
func (f *viewFx) role(name string, perms map[string]string) string {
	f.t.Helper()
	key := name + f.sfx
	f.exec(`INSERT INTO roles (key, label, level) VALUES ($1, $1, 40)`, key)
	for p, dept := range perms {
		if dept == "" {
			f.exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT r.id, p.id FROM roles r, permissions p WHERE r.key = $1 AND p.key = $2`, key, p)
		} else {
			f.exec(`INSERT INTO role_permissions (role_id, permission_id, department_id) SELECT r.id, p.id, $3::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key = $2`, key, p, dept)
		}
	}
	f.must(f.rb.RefreshCache(context.Background()))
	return key
}

func (f *viewFx) user(name, role string) string {
	f.t.Helper()
	return f.id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'T', 'U', $2, '', '') RETURNING id::text`, name+f.sfx, role)
}

func (f *viewFx) req(uid, role string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	return r.WithContext(context.WithValue(r.Context(), "user", &users.User{ID: uid, Role: users.Role(role)}))
}

func (f *viewFx) project(infra, group string) string {
	f.t.Helper()
	return f.id(`INSERT INTO projects (name, created_by, infrastructure_id, assigned_group_id)
		VALUES ('P', $1::uuid, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid) RETURNING id::text`, f.creator, infra, group)
}

func (f *viewFx) kvp(infra string) string {
	f.t.Helper()
	return f.id(`INSERT INTO kvp_ideas (title, created_by, infrastructure_id)
		VALUES ('K', $1::uuid, NULLIF($2,'')::uuid) RETURNING id::text`, f.creator, infra)
}

func newViewFx(t *testing.T) *viewFx {
	t.Helper()
	pool := dataNetTestPool(t)
	ctx := context.Background()
	f := &viewFx{t: t, pool: pool, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	f.rb = rbac.NewService(rbac.NewRepository(pool))
	f.h = &Handler{db: pool, rbac: f.rb}
	f.deptA = f.id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "VA "+f.sfx)
	f.deptB = f.id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "VB "+f.sfx)
	f.infraA = f.id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "AnlA "+f.sfx, f.deptA)
	f.infraB = f.id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "AnlB "+f.sfx, f.deptB)
	f.groupA = f.id(`INSERT INTO user_groups (name, department_id) VALUES ($1, $2::uuid) RETURNING id::text`, "GrpA "+f.sfx, f.deptA)
	f.must(f.rb.Warm(ctx))
	noRole := f.role("vf_none", nil)
	f.creator = f.user("vf_creator", noRole)
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM kvp_ideas WHERE created_by = $1::uuid OR title = 'K'`, f.creator)
		pool.Exec(c, `DELETE FROM projects WHERE created_by = $1::uuid`, f.creator)
		pool.Exec(c, `DELETE FROM users WHERE username LIKE 'vf\_%' || $1`, f.sfx)
		pool.Exec(c, `DELETE FROM roles WHERE key LIKE 'vf\_%' || $1`, f.sfx)
		pool.Exec(c, `DELETE FROM user_groups WHERE id = $1::uuid`, f.groupA)
		pool.Exec(c, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, f.infraA, f.infraB)
		pool.Exec(c, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, f.deptA, f.deptB)
	})
	return f
}

func TestMigration183GrantsAllRoles(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE
		(key = 'projects.view' AND label = 'Projekte: ansehen' AND category = 'Aufgaben & Projekte')
		OR (key = 'kvp.view' AND label = 'KVP: alle Vorschläge ansehen' AND category = 'KVP')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("Rechte mit Beschriftung und Kategorie: %d, %v", n, err)
	}
	// Rollen der Tests (vf_…) entstehen erst nach der Migration und zählen nicht.
	for _, key := range []string{"projects.view", "kvp.view"} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM roles r WHERE r.key NOT LIKE 'vf\_%'
			AND NOT EXISTS (SELECT 1 FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			                 WHERE rp.role_id = r.id AND p.key = $1 AND rp.department_id IS NULL)`, key).Scan(&n); err != nil || n != 0 {
			t.Errorf("%d Rollen ohne %s (überall): %v", n, key, err)
		}
	}
}

func TestCanViewRecDepartments(t *testing.T) {
	f := newViewFx(t)
	role := f.role("vf_a", map[string]string{permProjectsView: f.deptA, permKVPView: f.deptA})
	uid := f.user("vf_a", role)
	r := f.req(uid, role)
	pA, pB, pNone := f.project(f.infraA, ""), f.project(f.infraB, ""), f.project("", "")
	pGrp := f.project("", f.groupA)
	for name, c := range map[string]struct {
		id   string
		want bool
	}{"Anlage A": {pA, true}, "Anlage B": {pB, false}, "ohne Anlage": {pNone, true}, "Gruppe aus A": {pGrp, true}} {
		if got := f.h.canViewRec(r, "project", c.id); got != c.want {
			t.Errorf("Projekt %s: %v, erwartet %v", name, got, c.want)
		}
	}
	kA, kB, kNone := f.kvp(f.infraA), f.kvp(f.infraB), f.kvp("")
	for name, c := range map[string]struct {
		id   string
		want bool
	}{"Anlage A": {kA, true}, "Anlage B": {kB, false}, "ohne Anlage": {kNone, true}} {
		if got := f.h.canViewRec(r, "kvp", c.id); got != c.want {
			t.Errorf("KVP %s: %v, erwartet %v", name, got, c.want)
		}
	}
	if !f.h.mayViewAny(r, "project") || !f.h.mayViewAny(r, "kvp") {
		t.Error("Recht in Abteilung A zählt für mayViewAny")
	}
}

func TestCanViewRecInvolved(t *testing.T) {
	f := newViewFx(t)
	role := f.role("vf_n", nil) // keinerlei Rechte
	uid := f.user("vf_n", role)
	r := f.req(uid, role)
	if f.h.mayViewAny(r, "project") {
		t.Error("ohne Recht kein mayViewAny")
	}

	set := func(id, col string, v any) {
		f.exec(`UPDATE projects SET `+col+` = $2 WHERE id = $1::uuid`, id, v)
	}
	p := f.project("", "")
	if f.h.canViewRec(r, "project", p) {
		t.Fatal("unbeteiligt ohne Recht darf nicht sehen")
	}
	for _, col := range []string{"created_by", "responsible_to", "assigned_to"} {
		set(p, col, uid)
		if !f.h.canViewRec(r, "project", p) {
			t.Errorf("Projekt: %s sieht den Vorgang", col)
		}
		if col == "created_by" {
			set(p, col, f.creator)
		} else {
			set(p, col, nil)
		}
		if f.h.canViewRec(r, "project", p) {
			t.Errorf("Projekt: nach Entfernen von %s nicht mehr sichtbar", col)
		}
	}
	// Gruppenmitglied
	pg := f.project("", f.groupA)
	if f.h.canViewRec(r, "project", pg) {
		t.Error("Gruppe ohne Mitgliedschaft")
	}
	f.exec(`INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, f.groupA, uid)
	if !f.h.canViewRec(r, "project", pg) {
		t.Error("Gruppenmitglied sieht das Projekt")
	}
	f.exec(`DELETE FROM user_group_members WHERE user_id = $1::uuid`, uid)
	if f.h.canViewRec(r, "project", pg) {
		t.Error("nach Austritt aus der Gruppe nicht mehr")
	}
	if ids := f.h.involvedIDs(context.Background(), uid, "project"); len(ids) != 0 {
		t.Errorf("involvedIDs ohne Beteiligung: %v", ids)
	}

	// KVP
	k := f.kvp("")
	if f.h.canViewRec(r, "kvp", k) {
		t.Fatal("KVP unbeteiligt")
	}
	for _, col := range []string{"submitter_id", "created_by", "reviewer_id", "responsible_to"} {
		f.exec(`UPDATE kvp_ideas SET `+col+` = $2::uuid WHERE id = $1::uuid`, k, uid)
		if !f.h.canViewRec(r, "kvp", k) {
			t.Errorf("KVP: %s sieht den Vorschlag", col)
		}
		f.exec(`UPDATE kvp_ideas SET `+col+` = $2 WHERE id = $1::uuid`, k, nil)
		if f.h.canViewRec(r, "kvp", k) {
			t.Errorf("KVP: nach Entfernen von %s nicht mehr", col)
		}
	}
	f.exec(`INSERT INTO kvp_members (idea_id, user_id) VALUES ($1::uuid, $2::uuid)`, k, uid)
	if !f.h.canViewRec(r, "kvp", k) {
		t.Error("KVP: Mitwirkender")
	}
	f.exec(`DELETE FROM kvp_members WHERE idea_id = $1::uuid`, k)
	f.exec(`INSERT INTO kvp_actions (idea_id, description, responsible_id) VALUES ($1::uuid, 'x', $2::uuid)`, k, uid)
	if !f.h.canViewRec(r, "kvp", k) {
		t.Error("KVP: Maßnahmen-Verantwortlicher")
	}
	if ids := f.h.involvedIDs(context.Background(), uid, "kvp"); !ids[k] {
		t.Error("involvedIDs enthält den Vorschlag")
	}
	f.exec(`DELETE FROM kvp_actions WHERE idea_id = $1::uuid`, k)
	if f.h.canViewRec(r, "kvp", k) {
		t.Error("KVP: nach Entfernen der Maßnahme nicht mehr")
	}
}

func TestCanViewRecEditIsNotView(t *testing.T) {
	f := newViewFx(t)
	role := f.role("vf_e", map[string]string{"projects.edit": "", "kvp.manage": ""})
	r := f.req(f.user("vf_e", role), role)
	if f.h.canViewRec(r, "project", f.project("", "")) {
		t.Error("projects.edit schließt das Ansehen nicht ein")
	}
	if f.h.canViewRec(r, "kvp", f.kvp("")) {
		t.Error("kvp.manage schließt das Ansehen nicht ein")
	}
	if f.h.mayViewAny(r, "project") || f.h.mayViewAny(r, "kvp") {
		t.Error("mayViewAny ohne Ansichtsrecht")
	}
}

func TestCanViewRecAreaAdmin(t *testing.T) {
	f := newViewFx(t)
	role := f.role("vf_adm", map[string]string{"projects.admin": "", "kvp.admin": f.deptA})
	r := f.req(f.user("vf_adm", role), role)
	if !f.h.canViewRec(r, "project", f.project(f.infraB, "")) {
		t.Error("projects.admin sieht Projekte")
	}
	if !f.h.canViewRec(r, "kvp", f.kvp(f.infraA)) || f.h.canViewRec(r, "kvp", f.kvp(f.infraB)) {
		t.Error("kvp.admin gilt nur in Abteilung A")
	}
}

func TestViewFilterMatchesCanViewRec(t *testing.T) {
	f := newViewFx(t)
	role := f.role("vf_f", map[string]string{permProjectsView: f.deptA})
	uid := f.user("vf_f", role)
	r := f.req(uid, role)
	ids := []string{
		f.project(f.infraA, ""), f.project(f.infraB, ""), f.project("", ""),
		f.project("", f.groupA), f.project(f.infraB, ""), f.project(f.infraA, f.groupA),
	}
	f.exec(`UPDATE projects SET responsible_to = $2::uuid WHERE id = $1::uuid`, ids[4], uid) // B, aber beteiligt
	filter := f.h.viewFilter(r, "project")
	for i, id := range ids {
		if got, want := filter(id), f.h.canViewRec(r, "project", id); got != want {
			t.Errorf("Datensatz %d: viewFilter %v, canViewRec %v", i, got, want)
		}
	}
	if filter(ids[1]) || !filter(ids[4]) {
		t.Error("B ohne Beteiligung unsichtbar, B mit Beteiligung sichtbar")
	}

	// Recht überall: keine Datenbankabfrage nötig
	all := f.role("vf_all", map[string]string{permProjectsView: ""})
	ra := f.req(f.user("vf_all", all), all)
	hn := &Handler{rbac: f.rb} // ohne Datenbank
	if !hn.viewFilter(ra, "project")("irgendeine-id") {
		t.Error("Recht überall: immer wahr, auch ohne Datenbank")
	}
	// ohne Recht und ohne Beteiligung: nichts
	none := f.req(f.user("vf_n2", f.role("vf_n2", nil)), "vf_n2"+f.sfx)
	if f.h.viewFilter(none, "project")(ids[0]) {
		t.Error("ohne Recht und Beteiligung unsichtbar")
	}
	// ohne Anmeldung
	if f.h.viewFilter(httptest.NewRequest("GET", "/", nil), "project")(ids[0]) || f.h.canViewRec(httptest.NewRequest("GET", "/", nil), "project", ids[0]) {
		t.Error("Gast sieht nichts")
	}
}

func TestCanViewRecOtherTypes(t *testing.T) {
	f := newViewFx(t)
	role := f.role("vf_o", nil)
	r := f.req(f.user("vf_o", role), role)
	if !f.h.canViewRec(r, "ticket", "00000000-0000-0000-0000-000000000000") {
		t.Error("andere Typen sind nicht betroffen")
	}
	if !f.h.viewFilter(r, "ticket")("x") || !f.h.mayViewAny(r, "ticket") {
		t.Error("andere Typen: Filter und mayViewAny immer wahr")
	}
}
