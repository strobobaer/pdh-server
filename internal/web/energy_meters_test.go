package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
	"pdh/internal/modules/energie"
	"pdh/pkg/database"
)

const meterTestID = "11111111-1111-4111-8111-111111111111"

// meterDo ruft einen Handler mit der Messstellen-ID im Pfad auf; uid "" = nicht angemeldet.
func meterDo(h http.HandlerFunc, method, path, id, uid string, body any) *httptest.ResponseRecorder {
	return meterDoAs(h, method, path, id, uid, "admin", body)
}

// meterDoAs wie meterDo, aber mit der Rolle des angemeldeten Benutzers.
func meterDoAs(h http.HandlerFunc, method, path, id, uid string, role users.Role, body any) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	if uid != "" {
		ctx = context.WithValue(ctx, "user", &users.User{ID: uid, FirstName: "Erwin", LastName: "Energie", Role: role})
	}
	w := httptest.NewRecorder()
	h(w, req.WithContext(ctx))
	return w
}

func meterErr(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("keine JSON-Antwort (%d): %s", w.Code, w.Body.String())
	}
	return out["error"]
}

// ── Rechte ───────────────────────────────────────────────────────────────

func TestEnergyPagesRequirePermission(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}

	w := meterDo(h.EnergyMetersPage, "GET", "/energy/meters", "", "", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("Seite ohne Recht: %d %s", w.Code, w.Header().Get("Location"))
	}
	if len(asked) == 0 || asked[len(asked)-1] != permEnergyView {
		t.Errorf("Seite: geprüft %v, erwartet %s", asked, permEnergyView)
	}

	body := map[string]any{"key": "X", "infrastructure_id": meterTestID}
	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
		body any
		want string
	}{
		{"anlegen", h.EnergyMeterSaveWeb, "/energy/meters/save-web", body, permEnergyEdit},
		{"ändern", h.EnergyMeterSaveWeb, "/energy/meters/save-web", map[string]any{"id": meterTestID, "key": "X", "infrastructure_id": meterTestID}, permEnergyEdit},
		{"löschen", h.EnergyMeterDeleteWeb, "/energy/meters/" + meterTestID + "/delete-web", nil, permEnergyAdmin},
	} {
		asked = nil
		w := meterDo(c.fn, "POST", c.path, meterTestID, "", c.body)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[0] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}

	// ungültige Anlage und ungültige ID: Fehler statt Datenbankzugriff
	h.hasPermHook = func(*http.Request, string) bool { return true }
	if w := meterDo(h.EnergyMeterSaveWeb, "POST", "/energy/meters/save-web", "", "", map[string]any{"key": "X", "infrastructure_id": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("ohne Anlage: Status %d", w.Code)
	}
	if w := meterDo(h.EnergyMeterDeleteWeb, "POST", "/energy/meters/x/delete-web", "x", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("ungültige ID: Status %d", w.Code)
	}
}

func TestEnergyGateRules(t *testing.T) {
	cases := []struct {
		method, path, perm, ref string
		hit                     bool
	}{
		{"POST", "/energy/meters/" + meterTestID + "/delete-web", permEnergyAdmin, meterRef, true},
		{"POST", "/energy/meters/save-web", permEnergyEdit, "", true},
		{"GET", "/energy/meters/" + meterTestID + "/delete-web", "", "", false},
		{"GET", "/energy/meters", "", "", false},
	}
	for _, c := range cases {
		_, perm, ref, _, hit := matchGate(c.method, c.path)
		if hit != c.hit || perm != c.perm || ref != c.ref {
			t.Errorf("%s %s: Treffer %v Recht %q Typ %q; erwartet %v %q %q", c.method, c.path, hit, perm, ref, c.hit, c.perm, c.ref)
		}
	}
}

func TestEnergyInRegistries(t *testing.T) {
	if src, ok := recordDeptSource[meterRef]; !ok || src.table != "energy_meters" || src.group {
		t.Errorf("recordDeptSource[meter] = %+v", src)
	}
	if areaAdminPerm["meter"] != "energy.admin" {
		t.Errorf("areaAdminPerm[meter] = %q", areaAdminPerm["meter"])
	}
	if meterRef != "meter" || permEnergyView != "energy.view" || permEnergyEdit != "energy.edit" || permEnergyAdmin != "energy.admin" {
		t.Error("Schlüssel von Datensatz-Typ und Rechten weichen von der Vorgabe ab")
	}
	var found bool
	for _, d := range navDefs {
		if d.Href == "/energy/meters" {
			found = true
			if d.Icon != "ti-gauge" || d.Label != "Messstellen" || d.group != "data" {
				t.Errorf("Navigationseintrag: %+v in %q", d.navItem, d.group)
			}
			if d.show(&BaseData{}) {
				t.Error("ohne Recht darf der Eintrag nicht erscheinen")
			}
			if !d.show(&BaseData{CanEnergy: true}) {
				t.Error("mit Recht muss der Eintrag erscheinen")
			}
		}
	}
	if !found {
		t.Error("Navigationseintrag Messstellen fehlt")
	}
}

// ── Filter und Meldungen ─────────────────────────────────────────────────

func TestMeterFilterFromQuery(t *testing.T) {
	const infra, conn = "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	f := meterFilterFromQuery(url.Values{
		"infra": {infra}, "virtual": {"1"}, "export": {"0"}, "active": {"1"}, "unit": {" kWh "}, "kind": {"counter"},
		"conn": {conn}, "q": {" Linie 1 "}, "stale": {"1"}, "sort": {"last"}, "dir": {"desc"},
	})
	if f.InfraID != infra || f.ConnectionID != conn || f.Unit != "kWh" || f.Kind != "counter" || f.Query != "Linie 1" ||
		!f.Stale || f.Sort != "last" || !f.Desc {
		t.Errorf("Filter: %+v", f)
	}
	if f.Virtual == nil || !*f.Virtual || f.Export == nil || *f.Export || f.Active == nil || !*f.Active {
		t.Errorf("Ja/Nein-Felder: %v %v %v", f.Virtual, f.Export, f.Active)
	}

	e := meterFilterFromQuery(url.Values{})
	if e.Virtual != nil || e.Export != nil || e.Active != nil || e.InfraID != "" || e.Stale || e.Desc || e.Sort != "key" {
		t.Errorf("leer: %+v", e)
	}
	for _, sort := range []string{"gibtesnicht", "m.meter_key; DROP TABLE x", "KEY"} {
		if got := meterFilterFromQuery(url.Values{"sort": {sort}}).Sort; got != "key" {
			t.Errorf("Sortierung %q -> %q, erwartet key", sort, got)
		}
	}
	for _, s := range []string{"key", "name", "infra", "unit", "kind", "last"} {
		if got := meterFilterFromQuery(url.Values{"sort": {s}}).Sort; got != s {
			t.Errorf("Sortierung %q -> %q", s, got)
		}
	}
	// Ungültiges wird ignoriert statt in die Abfrage zu gelangen
	bad := meterFilterFromQuery(url.Values{"infra": {"x'; --"}, "conn": {"abc"}, "kind": {"irgendwas"}, "virtual": {"vielleicht"}})
	if bad.InfraID != "" || bad.ConnectionID != "" || bad.Kind != "" || bad.Virtual != nil {
		t.Errorf("ungültige Werte: %+v", bad)
	}
}

func TestMeterSortURL(t *testing.T) {
	q := url.Values{"q": {"Linie"}, "export": {"1"}}
	u := meterSortURL(q, "name")
	if !strings.HasPrefix(u, "/energy/meters?") || !strings.Contains(u, "sort=name") || !strings.Contains(u, "q=Linie") || !strings.Contains(u, "export=1") || strings.Contains(u, "dir=") {
		t.Errorf("erste Wahl: %s", u)
	}
	q.Set("sort", "name")
	if u := meterSortURL(q, "name"); !strings.Contains(u, "dir=desc") {
		t.Errorf("zweiter Klick dreht um: %s", u)
	}
	q.Set("dir", "desc")
	if u := meterSortURL(q, "name"); strings.Contains(u, "dir=") {
		t.Errorf("dritter Klick wieder aufsteigend: %s", u)
	}
	if u := meterSortURL(q, "unit"); !strings.Contains(u, "sort=unit") || strings.Contains(u, "dir=") {
		t.Errorf("andere Spalte beginnt aufsteigend: %s", u)
	}
}

func TestMeterNumber(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 1: "1", 12.5: "12,5", 1234.56789: "1234,5679", -3.1: "-3,1", 100: "100", -0.00001: "0"} {
		if got := meterNumber(in); got != want {
			t.Errorf("meterNumber(%v) = %q, erwartet %q", in, got, want)
		}
	}
}

func TestMeterSaveMessage(t *testing.T) {
	_, perr := energie.ParseFormula("[A] +")
	cases := []struct {
		err    error
		want   string
		status int
	}{
		{&energie.CycleError{Path: []string{"A", "B", "A"}}, "Zirkelbezug: [A] → [B] → [A]", 400},
		{&energie.UnknownRefError{Key: "X"}, "Unbekannte Messstelle [X]", 400},
		{energie.ErrKeyTaken, "Diese Messstellen-ID ist bereits vergeben", 409},
		{energie.ErrKindMix, "Momentanwerte lassen sich nur mit Momentanwerten verrechnen", 400},
		{energie.ErrBadKey, "Die Messstellen-ID muss 1 bis 64 Zeichen lang sein", 400},
		{energie.ErrNoInfra, "Bitte eine Anlage wählen", 400},
		{energie.ErrNotFound, "Messstelle nicht gefunden", 404},
		{perr, "Formel, Zeichen", 400},
		{energie.ErrFormulaScope, "Bei Zählerständen und Verbräuchen sind in der Formel nur", 400},
		{&energie.InUseError{Users: []string{"A", "B"}}, "wird verwendet in [A], [B]", 409},
		{&pgconn.PgError{Code: "23503"}, "gibt es nicht mehr", 400},
		{errors.New("connection refused: 10.0.0.1"), "konnte nicht gespeichert werden", 500},
	}
	for _, c := range cases {
		got := meterSaveMessage(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("%v: Meldung %q enthält %q nicht", c.err, got, c.want)
		}
		if st := meterSaveStatus(c.err); st != c.status {
			t.Errorf("%v: Status %d, erwartet %d", c.err, st, c.status)
		}
	}
	if got := meterSaveMessage(errors.New("connection refused: 10.0.0.1")); strings.Contains(got, "10.0.0.1") {
		t.Errorf("interne Fehler gehören nicht in die Meldung: %q", got)
	}
}

// ── Darstellung ──────────────────────────────────────────────────────────

func meterTestData(canEdit bool) EnergyMetersData {
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	v1, v2 := 1234.5, 80.0
	rows := []energie.Meter{
		{ID: "m1", Key: "ZAEHLER-1", Name: "Zählerstand Linie 1", InfraID: "i1", InfraPath: "Halle 1 / Linie 1",
			SourceID: "s1", SourceLabel: "Temperatur Halle", ConnectionName: "PFC200", Unit: "kWh", Kind: energie.KindCounter,
			Factor: 1, Export: true, Active: true, LastValue: &v1, LastAt: &now},
		{ID: "m2", Key: "REST", Name: "Rest Halle", InfraID: "i1", InfraPath: "Halle 1",
			Formula: "[GESAMT] - [ZAEHLER-1]", Unit: "kWh", Kind: energie.KindCounter, Factor: 1, Export: true, Active: true, LastValue: &v2, LastAt: &old},
	}
	d := EnergyMetersData{
		Total: 2, CanCreate: canEdit, Units: []string{"kWh"},
		Sources: []MeterSourceOption{{ID: "s1", Label: "PFC200 · Temperatur Halle (Halle 1)"}},
		Conns:   []MeterConnOption{{ID: "c1", Name: "PFC200"}},
		Sort:    "key", SortURL: map[string]string{"key": "/energy/meters?sort=key&dir=desc", "name": "/energy/meters?sort=name"}, SortMark: map[string]string{"key": "▲"},
	}
	for _, m := range rows {
		r := newMeterRow(m, now)
		r.CanEdit, r.CanDelete = canEdit, canEdit
		if m.ID == "m1" {
			r.ThrText, r.ThrAlarms = thresholdCountText(energie.ThresholdCount{Total: 2, Alarms: 1})
		}
		d.Rows = append(d.Rows, r)
	}
	return d
}

func TestMeterTableRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	html := renderPage(t, tmpl, "energy_meters", meterTestData(true))
	for _, want := range []string{
		"ZAEHLER-1", "Zählerstand Linie 1", "Halle 1 / Linie 1", "PFC200 · Temperatur Halle", "1234,5 kWh",
		`<span class="badge b-blue">berechnet</span>`, `<code class="em-formula">[GESAMT] - [ZAEHLER-1]</code>`,
		"veraltet", "Neue Messstelle", `data-infra-picker`, `name="infra"`, `action="/energy/meters"`,
		"Exportieren", "Letzter Wert", `id="em-edit"`, `/energy/meters/save-web`,
		"Schwellwerte", `id="em-thr-row-m1"`, `hx-get="/energy/meters/m1/thresholds"`, "2 · 1 Alarm", `name="alarm"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	// Die Formel steht in der Spalte „Quelle“ der berechneten Zeile, nicht in einer eigenen Spalte.
	i := strings.Index(html, `data-id="m2"`)
	if i < 0 || !strings.Contains(html[i:i+1200], `data-label="Quelle" class="em-src"`) ||
		strings.Index(html[i:], "[GESAMT] - [ZAEHLER-1]") > strings.Index(html[i:], `data-label="Einheit"`) {
		t.Error("die Formel gehört in die Spalte Quelle")
	}
}

func TestMeterTableReadOnly(t *testing.T) {
	tmpl := loadTestTemplates(t)
	html := renderPage(t, tmpl, "energy_meters", meterTestData(false))
	if !strings.Contains(html, "ZAEHLER-1") || !strings.Contains(html, "berechnet") {
		t.Error("Ansehen muss funktionieren")
	}
	for _, bad := range []string{`aria-label="Bearbeiten"`, "Neue Messstelle", `id="em-edit"`, "emEdit(", "emDelete(", "Löschen"} {
		if strings.Contains(html, bad) {
			t.Errorf("ohne energy.edit darf %q nicht erscheinen", bad)
		}
	}
	// leere Tabelle
	d := meterTestData(false)
	d.Rows, d.Total = nil, 0
	if html := renderPage(t, tmpl, "energy_meters", d); !strings.Contains(html, "Noch keine Messstelle angelegt.") {
		t.Error("Leerzustand fehlt")
	}
	d.FilterOn = true
	if html := renderPage(t, tmpl, "energy_meters", d); !strings.Contains(html, "passt zu den Filtern") {
		t.Error("Leerzustand mit Filter fehlt")
	}
}

// ── Abteilung ────────────────────────────────────────────────────────────

func TestMeterDeptScope(t *testing.T) {
	st, ok := scopeTables[meterRef]
	if !ok || st.table != "energy_meters" || !strings.Contains(st.people, "r.created_by = $1::uuid") {
		t.Fatalf("scopeTables[meter] = %+v", st)
	}
	cond, args := scopeSQL(&deptScope{UserID: "u1", Departments: []string{"d1"}}, meterRef, "m", 0)
	if !strings.Contains(cond, "m.infrastructure_id") || !strings.Contains(cond, "m.created_by = $1::uuid") || len(args) != 2 {
		t.Errorf("scopeSQL: %q %v", cond, args)
	}
	if rt, id, ok := scopeTarget("/energy/meters/" + meterTestID + "/delete-web"); !ok || rt != meterRef || id != meterTestID {
		t.Errorf("scopeTarget: %q %q %v", rt, id, ok)
	}
	if _, _, ok := scopeTarget("/energy/meters"); ok {
		t.Error("die Liste braucht keine Datensatzprüfung")
	}
}

// ── Datenbank: Speichern, Rechte nach Abteilung ──────────────────────────

func TestMeterSaveWeb(t *testing.T) {
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
	must(database.RunMigrations(ctx, pool, "../../migrations"))
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	werk := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Werk "+sfx)
	buero := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "Büro "+sfx)
	infraW := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "Presse "+sfx, werk)
	infraB := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "Drucker "+sfx, buero)
	role := "energie" + sfx
	_, err = pool.Exec(ctx, `INSERT INTO roles (key, label, level) VALUES ($1, 'Energie', 40)`, role)
	must(err)
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'E', 'E', $2, '', '') RETURNING id::text`, "e"+sfx, role)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM energy_meters WHERE infrastructure_id IN ($1::uuid, $2::uuid)`, infraW, infraB)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, user)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE key = $1`, role)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, infraW, infraB)
		_, _ = pool.Exec(ctx, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, werk, buero)
	}()
	// Messstellen pflegen nur im Werk, löschen nirgends
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_id, department_id)
		SELECT r.id, p.id, $2::uuid FROM roles r, permissions p WHERE r.key = $1 AND p.key = 'energy.edit'`, role, werk)
	must(err)
	rb := rbac.NewService(rbac.NewRepository(pool))
	must(rb.Warm(ctx))
	h := &Handler{db: pool, rbac: rb}
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return meterDoAs(h.EnergyMeterSaveWeb, "POST", "/energy/meters/save-web", "", user, users.Role(role), body)
	}
	keyA, keyB, keyF := "A-"+sfx, "B-"+sfx, "F-"+sfx

	// neue Zeile mit Anlage legt an
	w := post(map[string]any{"key": keyA, "name": "Zähler A", "infrastructure_id": infraW, "unit": "kWh", "kind": "counter", "factor": 1, "export": true, "active": true})
	if w.Code != http.StatusOK {
		t.Fatalf("anlegen: %d %s", w.Code, w.Body.String())
	}
	var ok struct {
		ID      string `json:"id"`
		Created bool   `json:"created"`
	}
	must(json.Unmarshal(w.Body.Bytes(), &ok))
	idA := ok.ID
	if idA == "" || !ok.Created {
		t.Fatalf("Antwort: %s", w.Body.String())
	}
	m, err := energie.NewStore(pool).Get(ctx, idA)
	must(err)
	if m.Key != keyA || m.InfraID != infraW || m.Unit != "kWh" || m.Factor != 1 || !m.Export || !m.Active {
		t.Errorf("gespeichert: %+v", m)
	}

	// berechnete Messstelle B = [A]; danach A = [B] ergibt einen Zirkel
	w = post(map[string]any{"key": keyB, "infrastructure_id": infraW, "formula": "[" + keyA + "]", "kind": "counter", "factor": 1, "export": true, "active": true})
	if w.Code != http.StatusOK {
		t.Fatalf("berechnet anlegen: %d %s", w.Code, w.Body.String())
	}
	w = post(map[string]any{"id": idA, "key": keyA, "infrastructure_id": infraW, "formula": "[" + keyB + "]", "kind": "counter", "factor": 1, "export": true, "active": true})
	if w.Code != http.StatusBadRequest {
		t.Errorf("Zirkel: Status %d %s", w.Code, w.Body.String())
	}
	if got, want := meterErr(t, w), fmt.Sprintf("Zirkelbezug: [%s] → [%s] → [%s]", keyA, keyB, keyA); got != want {
		t.Errorf("Zirkel: Meldung %q, erwartet %q", got, want)
	}
	// unbekannte ID
	w = post(map[string]any{"key": keyF, "infrastructure_id": infraW, "formula": "[GIBTS-NICHT-" + sfx + "]", "kind": "counter", "factor": 1})
	if w.Code != http.StatusBadRequest || !strings.Contains(meterErr(t, w), "Unbekannte Messstelle [GIBTS-NICHT-") {
		t.Errorf("unbekannte ID: %d %s", w.Code, w.Body.String())
	}
	// A steht in der Formel von B: Umbenennen ist nicht möglich und sagt, wo
	w = post(map[string]any{"id": idA, "key": keyA + "-neu", "infrastructure_id": infraW, "kind": "counter", "factor": 1, "export": true, "active": true})
	if w.Code != http.StatusConflict || !strings.Contains(meterErr(t, w), "wird verwendet in ["+keyB+"]") {
		t.Errorf("verwendet: %d %s", w.Code, w.Body.String())
	}

	// fremde Abteilung: anlegen und ändern verboten
	w = post(map[string]any{"key": "X-" + sfx, "infrastructure_id": infraB, "kind": "counter", "factor": 1})
	if w.Code != http.StatusForbidden {
		t.Errorf("anlegen im Büro: %d %s", w.Code, w.Body.String())
	}
	idB := id(`INSERT INTO energy_meters (meter_key, name, infrastructure_id, kind) VALUES ($1, 'Büro', $2::uuid, 'counter') RETURNING id::text`, "BUERO-"+sfx, infraB)
	w = post(map[string]any{"id": idB, "key": "BUERO-" + sfx, "infrastructure_id": infraB, "kind": "counter", "factor": 1})
	if w.Code != http.StatusForbidden {
		t.Errorf("ändern im Büro: %d %s", w.Code, w.Body.String())
	}
	// eine Messstelle des Werks lässt sich nicht ins Büro verschieben
	w = post(map[string]any{"id": idA, "key": keyA, "infrastructure_id": infraB, "kind": "counter", "factor": 1})
	if w.Code != http.StatusForbidden {
		t.Errorf("ins Büro verschieben: %d %s", w.Code, w.Body.String())
	}

	// löschen: nur Bereichsadministrator – die Rolle hat energy.edit, aber nicht energy.admin
	if w := meterDoAs(h.EnergyMeterDeleteWeb, "POST", "/energy/meters/"+idA+"/delete-web", idA, user, users.Role(role), nil); w.Code != http.StatusForbidden {
		t.Errorf("löschen ohne energy.admin: %d", w.Code)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM energy_meters WHERE id = $1::uuid`, idA).Scan(&n))
	if n != 1 {
		t.Error("die Messstelle darf nicht gelöscht sein")
	}

	// Abteilungsfilter der Liste: Werk sichtbar, Büro nicht
	allowed := h.allowedIDsFor(ctx, &deptScope{UserID: user, Departments: []string{werk}}, meterRef)
	if !allowed[idA] || allowed[idB] {
		t.Errorf("Abteilungsfilter: %v", allowed)
	}
}
