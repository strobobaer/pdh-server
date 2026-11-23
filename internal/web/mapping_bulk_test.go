package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestImportMappingNormalize(t *testing.T) {
	for _, c := range []struct {
		in   importMappingCreateInput
		want string
	}{
		{importMappingCreateInput{SourceRef: "  ", InfrastructureID: "a", Name: "Strom"}, "Ungültiger Quellwert"},
		{importMappingCreateInput{SourceRef: "t", InfrastructureID: " ", Name: "Strom"}, "Bitte eine Infrastruktur auswählen"},
		{importMappingCreateInput{SourceRef: "t", InfrastructureID: "a", Name: "x"}, "Bezeichnung muss 2 bis 150 Zeichen lang sein"},
		{importMappingCreateInput{SourceRef: "t", InfrastructureID: "a", Name: "Strom", Note: strings.Repeat("n", 501)}, "Zuordnungsnotiz darf höchstens 500 Zeichen lang sein"},
		{importMappingCreateInput{SourceRef: "t", InfrastructureID: "a", Name: "Strom", HeartbeatSeconds: 90000}, "Heartbeat: 0 bis 86400 Sekunden (0 = aus)"},
	} {
		in := c.in
		if err := in.normalize(); err == nil || err.Error() != c.want {
			t.Errorf("%+v: %v, erwartet %q", c.in, err, c.want)
		}
	}
	in := importMappingCreateInput{SourceRef: " halle/1 ", InfrastructureID: " a ", Name: " Strom ", Note: " A "}
	if err := in.normalize(); err != nil || in.SourceRef != "halle/1" || in.InfrastructureID != "a" || in.Name != "Strom" || in.Note != "A" {
		t.Errorf("gültig und getrimmt: %+v, %v", in, err)
	}
	ex := exportMappingCreateInput{ImportMappingID: " m ", FieldName: "x"}
	if err := ex.normalize(); err == nil || err.Error() != "Zielfeldname muss 2 bis 150 Zeichen lang sein" {
		t.Errorf("Export: %v", err)
	}
	ex = exportMappingCreateInput{ImportMappingID: " ", FieldName: "Strom"}
	if err := ex.normalize(); err == nil || err.Error() != "Bitte einen Wert auswählen" {
		t.Errorf("Export ohne Wert: %v", err)
	}
}

func bulkReq(t *testing.T, dir string, n int) *http.Request {
	t.Helper()
	items := make([]map[string]string, n)
	for i := range items {
		items[i] = map[string]string{"source_ref": "t", "infrastructure_id": "a", "name": "Strom", "import_mapping_id": "m", "field_name": "Strom"}
	}
	body, _ := json.Marshal(map[string]any{"items": items})
	r := httptest.NewRequest("POST", "/"+dir+"/connections/c1/mappings/bulk", strings.NewReader(string(body)))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", "c1")
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	return r.WithContext(context.WithValue(ctx, "user", &users.User{ID: "u1", Role: "admin"}))
}

func TestBulkRouteLimits(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	for _, dir := range []string{"import", "export"} {
		handle := h.ImportMappingBulkWeb
		if dir == "export" {
			handle = h.ExportMappingBulkWeb
		}
		w := httptest.NewRecorder()
		handle(w, bulkReq(t, dir, 501))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Höchstens 500 Zuordnungen auf einmal") {
			t.Errorf("%s 501: %d %s", dir, w.Code, w.Body.String())
		}
		h.mlock.locked = true
		w = httptest.NewRecorder()
		handle(w, bulkReq(t, dir, 1))
		h.mlock.locked = false
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Wartung läuft") {
			t.Errorf("%s Wartung: %d %s", dir, w.Code, w.Body.String())
		}
	}
	denied := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	w := httptest.NewRecorder()
	denied.ImportMappingBulkWeb(w, bulkReq(t, "import", 1))
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
}
