package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScopeTarget(t *testing.T) {
	id := "0f8a2b3c-1d2e-4f50-8a9b-0c1d2e3f4a5b"
	for path, want := range map[string]string{
		"/tickets/" + id:                                         "ticket",
		"/tickets/" + id + "/status-web":                         "ticket",
		"/faults/" + id:                                          "fault",
		"/maintenance/tasks/" + id + "/edit-web":                 "maintenance_task",
		"/maintenance/plans/" + id + "/edit-web":                 "maintenance_plan",
		"/maintenance/tasks/" + id + "/steps":                    "maintenance_task",
		"/complete/maintenance/" + id + "/steps/x":               "maintenance_task",
		"/tasks/" + id:                                           "task",
		"/projects/" + id:                                        "project",
		"/it/" + id + "/edit-web":                                "it_asset",
		"/records/ticket/" + id + "/people":                      "ticket",
		"/records/meta/fault/" + id:                              "fault",
		"/records/history/maintenance_task/" + id:                "maintenance_task",
		"/complete/maintenance/" + id:                            "maintenance_task",
		"/attachments/ticket/" + id:                              "ticket",
	} {
		rt, gotID, ok := scopeTarget(path)
		if !ok || rt != want || gotID != id {
			t.Errorf("%s -> %q %q %v, want %s", path, rt, gotID, ok, want)
		}
	}
	for _, path := range []string{"/tickets", "/tickets/new", "/users/" + id, "/records/user/" + id, "/attachments/maint_check_item/" + id} {
		if _, _, ok := scopeTarget(path); ok {
			t.Errorf("%s darf keine Prüfung auslösen", path)
		}
	}
}

func TestFilterJSONList(t *testing.T) {
	allowed := map[string]bool{"a": true}
	got := string(filterJSONList([]byte(`[{"id":"a","x":1},{"id":"b"}]`), allowed))
	if got != `[{"id":"a","x":1}]` {
		t.Errorf("Array: %s", got)
	}
	var obj map[string]json.RawMessage
	out := filterJSONList([]byte(`{"success":true,"data":[{"id":"b"},{"id":"a"}]}`), allowed)
	if err := json.Unmarshal(out, &obj); err != nil || string(obj["data"]) != `[{"id":"a"}]` || string(obj["success"]) != "true" {
		t.Errorf("Objekt: %s", out)
	}
	if string(filterJSONList([]byte(`{"error":"x"}`), allowed)) != `{"error":"x"}` {
		t.Error("andere Antworten müssen unverändert bleiben")
	}
	if string(filterJSONList([]byte(`[{"id":"b"}]`), nil)) != `[{"id":"b"}]` {
		t.Error("ohne Einschränkung unverändert")
	}
}

func TestSortDeptTree(t *testing.T) {
	list := []deptView{
		{ID: "e", Name: "Elektro", ParentID: "i"},
		{ID: "g", Name: "GL"},
		{ID: "i", Name: "Instandhaltung", ParentID: "g"},
		{ID: "m", Name: "Mechanik", ParentID: "i"},
		{ID: "x", Name: "Waise", ParentID: "fehlt"},
	}
	var got []string
	for _, d := range sortDeptTree(list) {
		got = append(got, strings.Repeat(">", d.Depth)+d.ID)
	}
	if strings.Join(got, ",") != "g,>i,>>e,>>m,x" {
		t.Errorf("Baum: %v", got)
	}
}

func TestOrgChartDepartmentsRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	child := &orgDeptNode{deptView: deptView{ID: "e", Name: "Elektro", Depth: 1, MemberCount: 4}, Roles: []string{"Elektriker"}}
	top := &orgDeptNode{deptView: deptView{ID: "i", Name: "Instandhaltung", IsGlobal: true, ManagerName: "Eva"}, Children: []*orgDeptNode{child}}
	out := renderPage(t, tmpl, "orgchart", OrgChartPageData{
		Tiers:       []OrgChartTier{{Level: 10, Roles: []OrgChartRole{{ID: "r1", Label: "Elektriker", Dept: "Elektro"}}}},
		Departments: []*orgDeptNode{top},
	})
	for _, want := range []string{"Instandhaltung", "übergeordnet", "Leitung: Eva", "Elektro", "4 Mitarbeitende", "Elektriker", "od-kids", `<i class="ti ti-building"></i> Elektro`} {
		if !strings.Contains(out, want) {
			t.Errorf("Organigramm enthält %q nicht", want)
		}
	}
}
