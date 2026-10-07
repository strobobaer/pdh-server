package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdh/internal/core/rbac"
)

func TestOrgUnitsPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := OrgUnitsData{
		Departments: []deptView{{ID: "d1", Name: "Elektro", ManagerID: "u1", ManagerName: "Eva", MemberCount: 3, RoleCount: 1}},
		Groups: []groupView{{ID: "g1", Name: "Ersthelfer", DepartmentID: "d1", DepartmentName: "Elektro",
			MemberIDs: map[string]bool{"u2": true}, MemberNames: []string{"Max Muster"}}},
		Users: []UserOption{{ID: "u1", Name: "Eva"}, {ID: "u2", Name: "Max Muster"}},
	}
	out := renderPage(t, tmpl, "org_units", d)
	for _, want := range []string{"Elektro", "Ersthelfer", "Max Muster", `/admin/groups/g1/delete`, `value="u2" checked`, `name="member_ids"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Abteilungen & Gruppen enthält %q nicht", want)
		}
	}
	// leere Seite: Formulare zum Anlegen offen
	if out = renderPage(t, tmpl, "org_units", OrgUnitsData{}); !strings.Contains(out, "Noch keine Gruppen") {
		t.Error("leere Gruppenliste fehlt")
	}
}

func TestUserDetailGroupsAndDepartment(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{User: UserView{ID: "u1", Username: "eva", Department: "Alt"}, Master: userMaster{Language: "de"},
		CanEditMaster: true, Departments: []string{"Elektro"},
		Groups:    []UserGroupRef{{ID: "g1", Name: "Ersthelfer"}},
		AllGroups: []groupView{{ID: "g1", Name: "Ersthelfer"}, {ID: "g2", Name: "Frühschicht"}}}
	out := renderPage(t, tmpl, "user_detail", d)
	for _, want := range []string{`<option value="Alt" selected>Alt</option>`, `value="g1" checked`, "/users/u1/groups"} {
		if !strings.Contains(out, want) {
			t.Errorf("Benutzer-Detail enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `value="g2" checked`) {
		t.Error("Gruppe g2 fälschlich angehakt")
	}
}

// Alle Seiten-Templates muessen sich zusammen mit base und widgets parsen lassen
// (sie werden sonst erst beim Aufruf geparst und fallen erst dann auf).
func TestAllPageTemplatesParse(t *testing.T) {
	root := filepath.Join("..", "..", "web", "templates")
	files, err := filepath.Glob(filepath.Join(root, "*.gohtml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("keine Templates gefunden: %v", err)
	}
	base := loadTestTemplates(t)
	for _, f := range files {
		name := filepath.Base(f)
		if name == "base.gohtml" || name == "login.gohtml" {
			continue
		}
		c, err := base.Clone()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(f); err != nil {
			continue
		}
		if _, err := c.ParseFiles(f); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRolesPageDepartmentSelect(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := RolesPageData{
		Roles: []RoleColumn{
			{Role: &rbac.Role{ID: "r1", Key: "elektriker", Label: "Elektriker", Level: 10}, CanManage: true},
			{Role: &rbac.Role{ID: "r2", Key: "admin", Label: "Admin", Level: 100, IsBuiltin: true}},
		},
		Matrix:          map[string]map[string]bool{},
		Departments:     []deptView{{ID: "d1", Name: "Elektro"}},
		RoleDepartments: map[string]string{"r1": "d1", "r2": "d1"},
	}
	out := renderPage(t, tmpl, "roles", d)
	if !strings.Contains(out, `saveRoleDepartment(this,&#39;r1&#39;)`) && !strings.Contains(out, `saveRoleDepartment(this,'r1')`) {
		t.Error("Abteilungsauswahl für verwaltbare Rolle fehlt")
	}
	if !strings.Contains(out, `<option value="d1" selected>Elektro</option>`) {
		t.Error("zugeordnete Abteilung nicht ausgewählt")
	}
	if strings.Contains(out, `saveRoleDepartment(this,'r2')`) || strings.Contains(out, `saveRoleDepartment(this,&#39;r2&#39;)`) {
		t.Error("nicht verwaltbare Rolle darf keine Auswahl haben")
	}
}

func TestAssignmentNewOffersGroups(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "assignment_new", AssignmentNewPageData{
		Users:  []UserOption{{ID: "u1", Name: "Eva"}},
		Groups: []groupView{{ID: "g1", Name: "Elektro Früh", DepartmentName: "Elektro", MemberNames: []string{"Eva", "Max"}}},
	})
	for _, want := range []string{`<optgroup label="Personen">`, `<option value="u1">Eva</option>`, `<optgroup label="Gruppen">`, `value="g:g1"`, "Elektro Früh (Elektro) · 2 Mitglieder"} {
		if !strings.Contains(out, want) {
			t.Errorf("Neu anlegen enthält %q nicht", want)
		}
	}
}

func TestCreateWizardInBase(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "tickets", TicketsPageData{})
	for _, want := range []string{`id="crw"`, `data-type="maintenance"`, `id="crw-infra-id"`, `window.pdhCreate`, `pdhCreate(&#39;ticket&#39;)`} {
		if !strings.Contains(out, want) {
			t.Errorf("Erstellungs-Assistent: %q fehlt", want)
		}
	}
}
