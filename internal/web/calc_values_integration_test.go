//go:build integration

// Seite „Berechnete Werte“ gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run TestCalc ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
	"pdh/internal/modules/calc"
)

// calcEnv: mappingEnv mit zwei Quellen A und B an Anlage A (CSV-Verbindung)
// und Aufräumen der berechneten Werte an beiden Anlagen.
type calcWebEnv struct {
	*mappingEnv
	a, b string
	st   *calc.Store
}

func newCalcWebEnv(t *testing.T) *calcWebEnv {
	t.Helper()
	e := &calcWebEnv{mappingEnv: newMappingEnv(t)}
	e.st = e.h.calcStore()
	t.Cleanup(func() { // läuft vor dem Aufräumen der Anlagen
		c := context.Background()
		_, _ = e.pool.Exec(c, `DELETE FROM copilot_proposals WHERE user_id = $1::uuid`, e.uid)
		_, _ = e.pool.Exec(c, `DELETE FROM import_mappings WHERE infrastructure_id = ANY($1::uuid[])`, []string{e.assetA, e.assetB})
	})
	e.a = e.source(t, e.assetA, "Leistung A", "12,4")
	e.b = e.source(t, e.assetA, "Leistung B", "11.9")
	return e
}

// source legt eine Zuordnung mit Wert von jetzt an der CSV-Verbindung an.
func (e *calcWebEnv) source(t *testing.T, asset, name, value string) string {
	t.Helper()
	return e.id(t, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by, last_value, last_received_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, '', $5::uuid, $6, NOW()) RETURNING id::text`,
		e.imp, "spalte-"+name, asset, name+" "+e.sfx, e.uid, value)
}

// handler: alle Rechte; an der Anlage arbeiten darf man nur, wo allow es sagt.
func (e *calcWebEnv) handler(allow func(infra string) bool) *Handler {
	return &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true },
		assetGuardHook: func(_ *http.Request, _ string, infra string) bool { return allow(infra) }}
}

func calcDo(handle http.HandlerFunc, uid, method, path, body string, params map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == "POST" && strings.HasPrefix(strings.TrimSpace(body), "{") {
		r.Header.Set("Content-Type", "application/json")
	}
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	c := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
	w := httptest.NewRecorder()
	handle(w, r.WithContext(context.WithValue(c, "user", &users.User{ID: uid, Role: "admin"})))
	return w
}

func (e *calcWebEnv) body(t *testing.T, over map[string]any) string {
	t.Helper()
	m := map[string]any{"name": "Summe " + e.sfx, "infrastructure_id": e.assetA, "unit": "kW", "decimals": 1, "max_age_minutes": 10,
		"formula": "A + B", "inputs": []map[string]string{{"short": "A", "mapping_id": e.a}, {"short": "B", "mapping_id": e.b}}}
	for k, v := range over {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *calcWebEnv) calcCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM calc_values cv JOIN import_mappings m ON m.id = cv.mapping_id
		WHERE m.infrastructure_id = ANY($1::uuid[])`, []string{e.assetA, e.assetB}).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCalcSaveWeb(t *testing.T) {
	e := newCalcWebEnv(t)
	all := e.handler(func(string) bool { return true })
	denied := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return false }}

	if w := calcDo(denied.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, nil), nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Schreibrecht: %d", w.Code)
	}
	if w := calcDo(all.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"formula": "A +"}), nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Formel, Zeichen") {
		t.Errorf("Schreibfehler: %d %s", w.Code, w.Body.String())
	}
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if w := calcDo(all.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"valid_from": yesterday}), nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "heute oder später") {
		t.Errorf("gestern: %d %s", w.Code, w.Body.String())
	}
	if w := calcDo(all.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"id": "00000000-0000-7000-8000-000000000009"}), nil); w.Code != http.StatusNotFound {
		t.Errorf("unbekannter Wert: %d %s", w.Code, w.Body.String())
	}
	if n := e.calcCount(t); n != 0 {
		t.Fatalf("abgelehnte Anfragen haben gespeichert: %d", n)
	}

	w := calcDo(all.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, nil), nil)
	var res struct{ Status, ID, Version string }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK || res.Status != "ok" || res.Version != "created" || res.ID == "" {
		t.Fatalf("anlegen: %d %s", w.Code, w.Body.String())
	}
	v, err := e.st.Get(context.Background(), res.ID)
	if err != nil || v.Unit != "kW" || v.Decimals != 1 || v.MaxAgeMinutes == nil || *v.MaxAgeMinutes != 10 || v.Current() == nil || v.Current().Formula != "A + B" {
		t.Fatalf("gespeicherter Wert: %+v %v", v, err)
	}
	// Höchstalter bewusst leer: null
	w = calcDo(all.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"id": res.ID, "max_age_minutes": nil}), nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":"unchanged"`) {
		t.Errorf("Höchstalter leeren: %d %s", w.Code, w.Body.String())
	}
	if v, _ := e.st.Get(context.Background(), res.ID); v == nil || v.MaxAgeMinutes != nil {
		t.Errorf("Höchstalter sollte leer sein: %+v", v)
	}
}

// Review Focus 5: Person mit dem Recht nur an Anlage A.
func TestCalcSaveAssetGuard(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	onlyA := e.handler(func(infra string) bool { return infra == e.assetA })

	w := calcDo(onlyA.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, nil), nil)
	var res struct{ ID string }
	if _ = json.Unmarshal(w.Body.Bytes(), &res); w.Code != http.StatusOK || res.ID == "" {
		t.Fatalf("an eigener Anlage anlegen: %d %s", w.Code, w.Body.String())
	}
	// an fremder Anlage anlegen
	if w := calcDo(onlyA.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"name": "Fremd " + e.sfx, "infrastructure_id": e.assetB}), nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), errAssetDenied) {
		t.Errorf("an fremder Anlage anlegen: %d %s", w.Code, w.Body.String())
	}
	// eigenen Wert nach B verschieben
	if w := calcDo(onlyA.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"id": res.ID, "infrastructure_id": e.assetB}), nil); w.Code != http.StatusForbidden {
		t.Errorf("nach B verschieben: %d %s", w.Code, w.Body.String())
	}
	if v, _ := e.st.Get(ctx, res.ID); v == nil || v.InfraID != e.assetA {
		t.Errorf("Anlage hat sich geändert: %+v", v)
	}

	// Wert an B (von jemand anderem angelegt) mit einer geplanten Fassung
	foreign, err := e.st.Save(ctx, calc.SaveInput{Name: "Fremd " + e.sfx, InfraID: e.assetB, Formula: "A", Inputs: []calc.InputRef{{Short: "A", MappingID: e.a}}}, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := e.st.Save(ctx, calc.SaveInput{ID: foreign.ID, Name: "Fremd " + e.sfx, InfraID: e.assetB, Formula: "A * 2",
		ValidFrom: localDate(time.Now()).AddDate(0, 0, 7), Inputs: []calc.InputRef{{Short: "A", MappingID: e.a}}}, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	// ändern – auch mit Verschieben an die eigene Anlage
	for _, infra := range []string{e.assetB, e.assetA} {
		if w := calcDo(onlyA.CalcSaveWeb, e.uid, "POST", "/import/calc/values", e.body(t, map[string]any{"id": foreign.ID, "infrastructure_id": infra, "name": "Gekapert " + e.sfx}), nil); w.Code != http.StatusForbidden {
			t.Errorf("fremden Wert ändern (Anlage %s): %d %s", infra, w.Code, w.Body.String())
		}
	}
	p := map[string]string{"id": foreign.ID, "vid": planned.VersionID}
	if w := calcDo(onlyA.CalcVersionDeleteWeb, e.uid, "POST", "/import/calc/values/"+foreign.ID+"/versions/"+planned.VersionID+"/delete", "", p); w.Code != http.StatusForbidden {
		t.Errorf("fremde geplante Fassung löschen: %d", w.Code)
	}
	if w := calcDo(onlyA.CalcDeleteWeb, e.uid, "POST", "/import/calc/values/"+foreign.ID+"/delete", "", p); w.Code != http.StatusForbidden {
		t.Errorf("fremden Wert löschen: %d", w.Code)
	}
	v, err := e.st.Get(ctx, foreign.ID)
	if err != nil || v.Name != "Fremd "+e.sfx || v.InfraID != e.assetB || len(v.Versions) != 2 {
		t.Errorf("fremder Wert wurde verändert: %+v %v", v, err)
	}

	// mit dem Recht an beiden Anlagen geht alles
	all := e.handler(func(string) bool { return true })
	if w := calcDo(all.CalcVersionDeleteWeb, e.uid, "POST", "/x", "", p); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Errorf("geplante Fassung löschen: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := calcDo(all.CalcDeleteWeb, e.uid, "POST", "/x", "", p); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Errorf("Wert löschen: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, err := e.st.Get(ctx, foreign.ID); err == nil {
		t.Error("der Wert ist nicht gelöscht")
	}
	all.mlock.locked = true
	if w := calcDo(all.CalcDeleteWeb, e.uid, "POST", "/x", "", map[string]string{"id": res.ID}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("Löschen in der Wartung: %d", w.Code)
	}
}

func TestCalcCheckWeb(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	h := e.handler(func(string) bool { return true })
	check := func(over map[string]any) (calcCheckResult, int) {
		w := calcDo(h.CalcCheckWeb, e.uid, "POST", "/import/calc/check", e.body(t, over), nil)
		var res calcCheckResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res, w.Code
	}
	if res, code := check(nil); code != http.StatusOK || !res.OK || res.Probe != "24,3" || res.ProbeReason != "" || len(res.Hints) != 0 {
		t.Errorf("gültige Formel: %d %+v", code, res)
	}
	if res, _ := check(map[string]any{"formula": "A + C"}); res.OK || !strings.Contains(res.Error, "„C“") || res.Pos != 0 {
		t.Errorf("unbekannter Name: %+v", res)
	}
	if res, _ := check(map[string]any{"formula": "A +"}); res.OK || res.Pos < 1 || !strings.Contains(res.Error, "Formel, Zeichen") {
		t.Errorf("Schreibfehler: %+v", res)
	}
	if res, _ := check(map[string]any{"formula": "A * 2"}); !res.OK || len(res.Hints) != 1 || res.Hints[0] != "B wird in der Formel nicht benutzt" {
		t.Errorf("nicht benutzter Eingangswert: %+v", res)
	}
	if res, _ := check(map[string]any{"formula": "A / (B - B)"}); !res.OK || res.Probe != "" || res.ProbeReason != "Teilen durch 0" {
		t.Errorf("Probe ohne Wert: %+v", res)
	}
	if n := e.calcCount(t); n != 0 {
		t.Fatalf("die Prüfung hat gespeichert: %d", n)
	}
	// Kreisbezug: Eins nutzt Zwei, Zwei soll Eins nutzen
	one, err := e.st.Save(ctx, calc.SaveInput{Name: "Eins " + e.sfx, InfraID: e.assetA, Formula: "A", Inputs: []calc.InputRef{{Short: "A", MappingID: e.a}}}, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	two, err := e.st.Save(ctx, calc.SaveInput{Name: "Zwei " + e.sfx, InfraID: e.assetA, Formula: "E * 2", Inputs: []calc.InputRef{{Short: "E", MappingID: one.ID}}}, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := check(map[string]any{"id": one.ID, "formula": "Z + 1", "inputs": []map[string]string{{"short": "Z", "mapping_id": two.ID}}})
	if res.OK || !strings.HasPrefix(res.Error, "Kreisbezug: ") || !strings.Contains(res.Error, "Eins "+e.sfx) || !strings.Contains(res.Error, "Zwei "+e.sfx) || strings.Contains(res.Error, one.ID) {
		t.Errorf("Kreisbezug: %+v", res)
	}
}

func TestCalcInputsWeb(t *testing.T) {
	e := newCalcWebEnv(t)
	h := e.handler(func(string) bool { return true })
	w := calcDo(h.CalcInputsWeb, e.uid, "GET", "/import/calc/inputs?q=leistung+"+e.sfx, "", nil)
	var list []calcCandidateView
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || len(list) != 2 {
		t.Fatalf("Suche: %d %s", w.Code, w.Body.String())
	}
	if c := list[0]; c.ID != e.a || c.Name != "Leistung A "+e.sfx || c.Path != "Presse A "+e.sfx || c.Connection != "Imp "+e.sfx || c.Value != "12,4" || !strings.HasPrefix(c.Age, "vor ") || c.Calc {
		t.Errorf("Treffer: %+v", c)
	}
	denied := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return false }}
	if w := calcDo(denied.CalcInputsWeb, e.uid, "GET", "/import/calc/inputs", "", nil); w.Code != http.StatusForbidden {
		t.Errorf("ohne Leserecht: %d", w.Code)
	}
}

func TestCalcRedirectAndForm(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	h := e.handler(func(string) bool { return true })
	conn, err := e.st.ConnectionID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w := calcDo(h.CalcRedirectWeb, e.uid, "GET", "/import/calc", "", nil); w.Code != http.StatusFound || w.Header().Get("Location") != "/import/connections/"+conn {
		t.Errorf("Weiterleitung: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Formular aus dem Speicher: geltende Fassung, und die geplante zum Ändern
	in := calc.SaveInput{Name: "Summe " + e.sfx, InfraID: e.assetA, Unit: "kW", Decimals: 1, Formula: "A + B",
		Inputs: []calc.InputRef{{Short: "A", MappingID: e.a}, {Short: "B", MappingID: e.b}}}
	res, err := e.st.Save(ctx, in, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	in.ID, in.Formula, in.ValidFrom = res.ID, "A + B + 1", localDate(time.Now()).AddDate(0, 0, 7)
	planned, err := e.st.Save(ctx, in, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.st.Get(ctx, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := calcFormFor(v, "", localDate(now), now)
	if f.ID != res.ID || f.VersionID != "" || f.Formula != "A + B" || f.MaxAge != "" || f.ValidFrom != f.Today || len(f.Inputs) != 2 || len(f.Versions) != 2 {
		t.Fatalf("Formular: %+v", f)
	}
	if in := f.Inputs[0]; in.Short != "A" || in.Value != "12,4" || in.Label != "Presse A "+e.sfx+" › Leistung A "+e.sfx || !strings.HasPrefix(in.Age, "vor ") {
		t.Errorf("Eingangswert: %+v", in)
	}
	if ver := f.Versions[0]; ver.Status != calc.StatusPlanned || !ver.Editable || ver.By != "Z O" || f.Versions[1].Editable {
		t.Errorf("Fassungen: %+v", f.Versions)
	}
	f = calcFormFor(v, planned.VersionID, localDate(now), now)
	if f.VersionID != planned.VersionID || f.Formula != "A + B + 1" || f.ValidFrom != in.ValidFrom.Format("2006-01-02") {
		t.Errorf("geplante Fassung im Formular: %+v", f)
	}
}
