//go:build integration

// Abteilung der Anlage bei „Vorlage anwenden“ und beim Löschen von
// Zuordnungen (Schlussprüfung I4) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'AssetGuard' ./internal/web/
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

func TestAssetGuardTemplateApply(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	onlyA := func(id string) bool { return id == e.assetA }

	payload, _ := json.Marshal(templatePayload{Mappings: []templateMapping{
		{Query: -1, SourceRef: "Strom", Name: "Strom A", InfrastructureID: e.assetA},
		{Query: -1, SourceRef: "Druck", Name: "Druck B", InfrastructureID: e.assetB},
	}})
	tpl := e.id(t, `INSERT INTO connection_templates (direction, kind, name, description, payload) VALUES ('import', 'csv', $1, '', $2) RETURNING id::text`, "Vorlage "+e.sfx, payload)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM connection_templates WHERE id = $1::uuid`, tpl)
	})

	msg, err := e.h.applyConnectionTemplate(ctx, e.imp, "import", tpl, e.uid, onlyA)
	if err != nil || !strings.Contains(msg, "ohne Berechtigung") {
		t.Fatalf("Import: %q %v", msg, err)
	}
	if n := e.count(t, "import_mappings", e.imp); n != 1 {
		t.Errorf("nur die Zuordnung an Anlage A erwartet: %d", n)
	}

	// Export: zwei Import-Werte (A und B) als Felder – nur A ist erlaubt
	mA, _, _ := e.h.insertImportMapping(ctx, e.imp, e.uid, importMappingCreateInput{SourceRef: "Temp", InfrastructureID: e.assetA, Name: "Temp A"})
	mB, _, _ := e.h.insertImportMapping(ctx, e.imp, e.uid, importMappingCreateInput{SourceRef: "Temp", InfrastructureID: e.assetB, Name: "Temp B"})
	payload, _ = json.Marshal(templatePayload{Mappings: []templateMapping{
		{ImportMappingID: mA, FieldName: "Temp_A"}, {ImportMappingID: mB, FieldName: "Temp_B"},
	}})
	etpl := e.id(t, `INSERT INTO connection_templates (direction, kind, name, description, payload) VALUES ('export', 'csv', $1, '', $2) RETURNING id::text`, "Export-Vorlage "+e.sfx, payload)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM connection_templates WHERE id = $1::uuid`, etpl)
	})
	msg, err = e.h.applyConnectionTemplate(ctx, e.exp, "export", etpl, e.uid, onlyA)
	if err != nil || !strings.Contains(msg, "ohne Berechtigung") {
		t.Fatalf("Export: %q %v", msg, err)
	}
	if n := e.count(t, "export_mappings", e.exp); n != 1 {
		t.Errorf("nur das Feld zu Anlage A erwartet: %d", n)
	}
}

func TestAssetGuardMappingDelete(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	mB, _, err := e.h.insertImportMapping(ctx, e.imp, e.uid, importMappingCreateInput{SourceRef: "Druck", InfrastructureID: e.assetB, Name: "Druck B"})
	if err != nil {
		t.Fatal(err)
	}
	xB, _, err := e.h.insertExportMapping(ctx, e.exp, e.uid, exportMappingCreateInput{ImportMappingID: mB, FieldName: "Druck_B"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true }}
	call := func(fn http.HandlerFunc, conn, mapping string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/x", strings.NewReader("back=page"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", conn)
		rc.URLParams.Add("mappingId", mapping)
		c := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
		w := httptest.NewRecorder()
		fn(w, r.WithContext(context.WithValue(c, "user", &users.User{ID: e.uid, Role: "admin"})))
		return w
	}

	// fremde Abteilung: nichts wird gelöscht
	h.assetGuardHook = func(*http.Request, string, string) bool { return false }
	if w := call(h.ExportMappingDeleteWeb, e.exp, xB); w.Code != http.StatusForbidden {
		t.Errorf("Export-Feld fremde Abteilung: %d", w.Code)
	}
	if w := call(h.ImportMappingDeleteWeb, e.imp, mB); w.Code != http.StatusForbidden {
		t.Errorf("Zuordnung fremde Abteilung: %d", w.Code)
	}
	if e.count(t, "export_mappings", e.exp) != 1 || e.count(t, "import_mappings", e.imp) != 1 {
		t.Error("ohne Berechtigung darf nichts gelöscht sein")
	}

	// eigene Abteilung: das Export-Feld wird gelöscht
	h.assetGuardHook = func(_ *http.Request, perm, infra string) bool { return perm == "export.write" && infra == e.assetB }
	if w := call(h.ExportMappingDeleteWeb, e.exp, xB); w.Code != http.StatusSeeOther {
		t.Errorf("Export-Feld eigene Abteilung: %d %s", w.Code, w.Body.String())
	}
	if n := e.count(t, "export_mappings", e.exp); n != 0 {
		t.Errorf("Feld sollte gelöscht sein: %d", n)
	}
}

// Schlussprüfung M1: Ausgeblendete Anlagen werden nicht vorgeschlagen.
func TestAssetListSkipsHidden(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE infrastructure SET hidden_at = NOW() WHERE id = $1::uuid`, e.assetB); err != nil {
		t.Fatal(err)
	}
	list, err := e.h.assetList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range list {
		seen[a.ID] = true
	}
	if !seen[e.assetA] || seen[e.assetB] {
		t.Errorf("sichtbar A erwartet, ausgeblendet B nicht: A=%v B=%v", seen[e.assetA], seen[e.assetB])
	}
}
