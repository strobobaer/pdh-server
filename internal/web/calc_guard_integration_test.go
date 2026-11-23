//go:build integration

// Schutz der Eingangswerte berechneter Werte gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestUsedInput|TestSupersededInput|TestBuiltinConnection|TestCalcSourceLabel' ./internal/web/
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/calc"
)

// sumValue legt „Summe“ = A + B an (Eingangswerte an der CSV-Verbindung).
func (e *calcWebEnv) sumValue(t *testing.T) (calc.SaveInput, string) {
	t.Helper()
	in := calc.SaveInput{Name: "Summe " + e.sfx, InfraID: e.assetA, Formula: "A + B",
		Inputs: []calc.InputRef{{Short: "A", MappingID: e.a}, {Short: "B", MappingID: e.b}}}
	res, err := e.st.Save(context.Background(), in, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	in.ID = res.ID
	return in, res.ID
}

func calcPostForm(handle http.HandlerFunc, uid, path string, form url.Values, params map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	c := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	w := httptest.NewRecorder()
	handle(w, r.WithContext(context.WithValue(c, "user", &users.User{ID: uid, Role: "admin"})))
	return w
}

func (e *calcWebEnv) exists(t *testing.T, table, id string) bool {
	t.Helper()
	var ok bool
	if err := e.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1::uuid)`, id).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func location(w *httptest.ResponseRecorder) string {
	u, _ := url.QueryUnescape(w.Header().Get("Location"))
	return u
}

func TestUsedInputNotDeletableByHand(t *testing.T) {
	e := newCalcWebEnv(t)
	e.sumValue(t)
	h := e.handler(func(string) bool { return true })
	p := map[string]string{"id": e.imp, "mappingId": e.a}
	w := calcPostForm(h.ImportMappingDeleteWeb, e.uid, "/x", url.Values{"back": {"page"}}, p)
	if w.Code != http.StatusSeeOther || !strings.Contains(location(w), "wird als Eingangswert gebraucht von „Summe "+e.sfx+"“") {
		t.Errorf("von Hand (Seite): %d %s", w.Code, location(w))
	}
	if w := calcPostForm(h.ImportMappingDeleteWeb, e.uid, "/x", url.Values{}, p); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Summe "+e.sfx) {
		t.Errorf("von Hand: %d %s", w.Code, w.Body.String())
	}
	if !e.exists(t, "import_mappings", e.a) {
		t.Fatal("der genutzte Eingangswert wurde gelöscht")
	}
}

func TestUsedInputNotDeletableWithConnection(t *testing.T) {
	e := newCalcWebEnv(t)
	e.sumValue(t)
	h := e.handler(func(string) bool { return true })
	w := calcPostForm(h.ImportConnectionDeleteWeb, e.uid, "/x", url.Values{}, map[string]string{"id": e.imp})
	if w.Code != http.StatusSeeOther || !strings.Contains(location(w), "Summe "+e.sfx) || !strings.Contains(location(w), "Eingangswerte") {
		t.Errorf("Verbindung löschen: %d %s", w.Code, location(w))
	}
	if !e.exists(t, "import_export_connections", e.imp) || !e.exists(t, "import_mappings", e.a) {
		t.Fatal("Verbindung oder Eingangswert wurden gelöscht")
	}
}

func TestUsedInputNotDeletableWithQuery(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	qid := e.id(t, `INSERT INTO connection_queries (connection_id, name) VALUES ($1::uuid, 'Zählerstände') RETURNING id::text`, e.imp)
	col := e.id(t, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by, last_value, last_received_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, '', $5::uuid, '7', NOW()) RETURNING id::text`, e.imp, queryRef(qid, "stand"), e.assetA, "Stand "+e.sfx, e.uid)
	if _, err := e.st.Save(ctx, calc.SaveInput{Name: "Doppelt " + e.sfx, InfraID: e.assetA, Formula: "S * 2",
		Inputs: []calc.InputRef{{Short: "S", MappingID: col}}}, e.uid); err != nil {
		t.Fatal(err)
	}
	h := e.handler(func(string) bool { return true })
	w := calcPostForm(h.ConnectionQueryDeleteWeb, e.uid, "/x", url.Values{"with_mappings": {"1"}}, map[string]string{"id": e.imp, "qid": qid})
	if w.Code != http.StatusSeeOther || !strings.Contains(location(w), "Doppelt "+e.sfx) {
		t.Errorf("Abfrage samt Zuordnungen löschen: %d %s", w.Code, location(w))
	}
	if !e.exists(t, "connection_queries", qid) || !e.exists(t, "import_mappings", col) {
		t.Fatal("Abfrage oder Eingangswert wurden gelöscht")
	}
	// ohne „samt Zuordnungen“ bleibt die Zuordnung ohnehin stehen – die Abfrage darf gehen
	w = calcPostForm(h.ConnectionQueryDeleteWeb, e.uid, "/x", url.Values{}, map[string]string{"id": e.imp, "qid": qid})
	if w.Code != http.StatusSeeOther || e.exists(t, "connection_queries", qid) || !e.exists(t, "import_mappings", col) {
		t.Errorf("Abfrage allein löschen: %d %s", w.Code, location(w))
	}
}

func TestSupersededInputDeletable(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	in, id := e.sumValue(t)
	time.Sleep(5 * time.Millisecond) // die neue Fassung ist die zuletzt angelegte
	in.Formula, in.Inputs = "B * 2", []calc.InputRef{{Short: "B", MappingID: e.b}}
	if _, err := e.st.Save(ctx, in, e.uid); err != nil {
		t.Fatal(err)
	}
	h := e.handler(func(string) bool { return true })
	w := calcPostForm(h.ImportMappingDeleteWeb, e.uid, "/x", url.Values{"back": {"page"}}, map[string]string{"id": e.imp, "mappingId": e.a})
	if w.Code != http.StatusSeeOther || !strings.Contains(location(w), "Zuordnung gelöscht") || e.exists(t, "import_mappings", e.a) {
		t.Fatalf("abgelöster Eingangswert: %d %s", w.Code, location(w))
	}
	v, err := e.st.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	old := v.Versions[len(v.Versions)-1]
	if old.Status != calc.StatusSuperseded || !old.Inputs[0].Gone || old.Inputs[0].Label != "Presse A "+e.sfx+" › Leistung A "+e.sfx {
		t.Errorf("abgelöste Fassung behält die Bezeichnung: %+v", old)
	}
}

func TestBuiltinConnectionProtected(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	conn, err := e.st.ConnectionID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h := e.handler(func(string) bool { return true })
	p := map[string]string{"id": conn}
	state := func() (string, bool) {
		var name string
		var enabled bool
		if err := e.pool.QueryRow(ctx, `SELECT name, enabled FROM import_export_connections WHERE id = $1::uuid`, conn).Scan(&name, &enabled); err != nil {
			t.Fatalf("die eingebaute Verbindung fehlt: %v", err)
		}
		return name, enabled
	}
	if w := calcPostForm(h.ImportConnectionEditWeb, e.uid, "/x", url.Values{"name": {"Umbenannt"}}, p); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCalcBuiltin) {
		t.Errorf("bearbeiten: %d %s", w.Code, w.Body.String())
	}
	if w := calcPostForm(h.ImportConnectionToggleWeb, e.uid, "/x", url.Values{}, p); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCalcBuiltin) {
		t.Errorf("abschalten: %d %s", w.Code, w.Body.String())
	}
	if w := calcPostForm(h.ImportConnectionDeleteWeb, e.uid, "/x", url.Values{}, p); w.Code != http.StatusSeeOther || !strings.Contains(location(w), errCalcBuiltin) {
		t.Errorf("löschen: %d %s", w.Code, location(w))
	}
	if name, enabled := state(); name != "Berechnete Werte" || !enabled {
		t.Errorf("eingebaute Verbindung verändert: %q %v", name, enabled)
	}
	if w := calcPostForm(h.ImportConnectionCreateWeb, e.uid, "/x", url.Values{"kind": {calc.Kind}, "name": {"Zweite " + e.sfx}}, nil); !strings.Contains(location(w), "Unbekannter Verbindungstyp") {
		t.Errorf("zweite anlegen: %d %s", w.Code, location(w))
	}
	// gewöhnliche Zuordnungen gehören nicht an die eingebaute Verbindung
	body := `{"source_ref":"x","infrastructure_id":"` + e.assetA + `","name":"Fremd"}`
	if w := calcDo(h.ImportMappingCreateWeb, e.uid, "POST", "/x", body, p); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCalcOwnPage) {
		t.Errorf("Zuordnung von Hand: %d %s", w.Code, w.Body.String())
	}
	if w := calcDo(h.ImportMappingBulkWeb, e.uid, "POST", "/x", `{"items":[`+body+`]}`, p); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCalcOwnPage) {
		t.Errorf("Sammel-Anlage: %d %s", w.Code, w.Body.String())
	}
	if w := calcDo(h.ImportSuggestionsWeb, e.uid, "GET", "/x", "", p); w.Code != http.StatusBadRequest {
		t.Errorf("Vorschläge: %d", w.Code)
	}
	var foreign int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM import_mappings WHERE connection_id = $1::uuid AND name = 'Fremd'`, conn).Scan(&foreign); err != nil || foreign != 0 {
		t.Errorf("an der eingebauten Verbindung wurde eine gewöhnliche Zuordnung angelegt: %d %v", foreign, err)
	}
}

func TestCalcSourceLabel(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	_, id := e.sumValue(t)
	sources, err := e.h.availableExportSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sources {
		if strings.Contains(s.SourceRef, "calc:") {
			t.Fatalf("rohe Kennung in den Export-Quellen: %q", s.SourceRef)
		}
		if s.ID == id {
			found = s.SourceRef == "berechnet: A + B"
		}
	}
	if !found {
		t.Error("Export-Quellen: „berechnet: A + B“ fehlt")
	}
	all, err := e.h.allImportMappings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, m := range all {
		if strings.Contains(m.SourceRef, "calc:") {
			t.Fatalf("rohe Kennung in der Zuordnungs-Übersicht: %q", m.SourceRef)
		}
		if m.ID == id {
			found = m.SourceRef == "berechnet: A + B" && m.ConnectionKindLabel == "Berechnete Werte"
		}
	}
	if !found {
		t.Error("Übersicht: „berechnet: A + B“ fehlt")
	}
	// lange Formeln werden gekürzt
	long := "A" + strings.Repeat(" + A", 60)
	if _, err := e.st.Save(ctx, calc.SaveInput{Name: "Lang " + e.sfx, InfraID: e.assetA, Formula: long, Inputs: []calc.InputRef{{Short: "A", MappingID: e.a}}}, e.uid); err != nil {
		t.Fatal(err)
	}
	for _, label := range e.h.calcSourceLabels(ctx) {
		if n := len([]rune(label)); n > len([]rune("berechnet: "))+121 {
			t.Errorf("Bezeichnung zu lang (%d Zeichen)", n)
		}
	}
}
