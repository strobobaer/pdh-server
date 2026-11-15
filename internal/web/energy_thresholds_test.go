package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/users"
	"pdh/internal/modules/energie"
	"pdh/pkg/database"
)

// thrDo ruft einen Handler mit Pfadparametern und Formularwerten auf.
func thrDo(h http.HandlerFunc, method, path string, params map[string]string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = context.WithValue(ctx, "user", &users.User{ID: meterTestID, FirstName: "Erwin", LastName: "Energie", Role: "admin"})
	w := httptest.NewRecorder()
	h(w, req.WithContext(ctx))
	return w
}

func TestThresholdsPermissions(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	p := map[string]string{"id": meterTestID, "tid": meterTestID}

	// Seite ohne energy.view: Weiterleitung bzw. 403 für htmx
	w := thrDo(h.EnergyThresholdsWeb, "GET", "/energy/meters/"+meterTestID+"/thresholds", p, nil, false)
	if w.Code != http.StatusFound {
		t.Errorf("Ansehen ohne Recht: %d", w.Code)
	}
	if w := thrDo(h.EnergyThresholdsWeb, "GET", "/energy/meters/"+meterTestID+"/thresholds", p, nil, true); w.Code != http.StatusForbidden {
		t.Errorf("Ansehen ohne Recht (htmx): %d", w.Code)
	}
	if len(asked) == 0 || asked[0] != permEnergyView {
		t.Errorf("Ansehen: geprüft %v, erwartet %s", asked, permEnergyView)
	}

	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
		want string
	}{
		{"speichern", h.EnergyThresholdSaveWeb, "/energy/meters/" + meterTestID + "/thresholds/save-web", permEnergyThresholds},
		{"löschen", h.EnergyThresholdDeleteWeb, "/energy/thresholds/" + meterTestID + "/delete-web", permEnergyAdmin},
	} {
		asked = nil
		w := thrDo(c.fn, "POST", c.path, p, url.Values{"name": {"x"}}, true)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[0] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}

	// ungültige IDs: 404 statt Datenbankzugriff
	h.hasPermHook = func(*http.Request, string) bool { return true }
	if w := thrDo(h.EnergyThresholdsWeb, "GET", "/x", map[string]string{"id": "x"}, nil, true); w.Code != http.StatusNotFound {
		t.Errorf("ungültige Messstelle: %d", w.Code)
	}
	if w := thrDo(h.EnergyThresholdDeleteWeb, "POST", "/x", map[string]string{"tid": "x"}, nil, true); w.Code != http.StatusNotFound {
		t.Errorf("ungültiger Schwellwert: %d", w.Code)
	}
}

func TestThresholdGateRules(t *testing.T) {
	cases := []struct {
		method, path, perm, ref string
		hit                     bool
	}{
		{"POST", "/energy/meters/" + meterTestID + "/thresholds/save-web", permEnergyThresholds, meterRef, true},
		{"POST", "/energy/thresholds/" + meterTestID + "/delete-web", permEnergyAdmin, "", true},
		{"GET", "/energy/meters/" + meterTestID + "/thresholds", "", "", false},
	}
	for _, c := range cases {
		_, perm, ref, _, hit := matchGate(c.method, c.path)
		if hit != c.hit || perm != c.perm || ref != c.ref {
			t.Errorf("%s %s: Treffer %v Recht %q Typ %q; erwartet %v %q %q", c.method, c.path, hit, perm, ref, c.hit, c.perm, c.ref)
		}
	}
}

func TestThresholdStateText(t *testing.T) {
	at := time.Date(2026, 11, 15, 14, 5, 0, 0, time.Local)
	cases := []struct {
		t    energie.Threshold
		want string
	}{
		{energie.Threshold{State: "normal"}, "normal"},
		{energie.Threshold{State: "alarm", AlarmSince: &at}, "Alarm seit 15.11. 14:05"},
		{energie.Threshold{State: "alarm", AlarmSince: &at, ClearSince: &at}, "zurückschaltend seit 15.11. 14:05"},
	}
	for _, c := range cases {
		if got := thresholdStateText(c.t); got != c.want {
			t.Errorf("Zustand %+v: %q, erwartet %q", c.t, got, c.want)
		}
	}
	for c, want := range map[energie.ThresholdCount]string{{}: "keine", {Total: 2}: "2", {Total: 2, Alarms: 1}: "2 · 1 Alarm", {Total: 3, Alarms: 2}: "3 · 2 Alarme"} {
		if got, _ := thresholdCountText(c); got != want {
			t.Errorf("Spalte %+v: %q, erwartet %q", c, got, want)
		}
	}
}

func TestMeterTableThresholdColumn(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := meterTestData(true)
	d.Filter.Alarm, d.FilterOn = true, true
	html := renderPage(t, tmpl, "energy_meters", d)
	for _, want := range []string{
		"<th>Schwellwerte</th>", "2 · 1 Alarm", "has-alarm", `hx-get="/energy/meters/m1/thresholds"`,
		`name="alarm" value="1" checked`, "Nur mit offenem Alarm",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	// ohne Filter nicht angehakt
	html = renderPage(t, tmpl, "energy_meters", meterTestData(true))
	if strings.Contains(html, `name="alarm" value="1" checked`) {
		t.Error("Filter „mit offenem Alarm“ darf ohne alarm=1 nicht angehakt sein")
	}
	// Filter aus der Adresse
	if f := meterFilterFromQuery(url.Values{"alarm": {"1"}}); !f.Alarm {
		t.Error("alarm=1 wird nicht gelesen")
	}
	if f := meterFilterFromQuery(url.Values{"alarm": {"0"}}); f.Alarm {
		t.Error("alarm=0 darf nicht filtern")
	}
}

func TestThresholdsFragmentRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	if _, err := tmpl.ParseFiles("../../web/templates/widgets/energy_thresholds.gohtml"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 14, 5, 0, 0, time.Local)
	tid := "44444444-4444-4444-8444-444444444444"
	d := ThresholdsData{
		MeterID: "m1", MeterName: "Druckluft", Unit: "bar", CanSave: true, CanDelete: true, ShowForm: true,
		Form:       newThresholdForm(),
		Priorities: thresholdPriorities, Tags: []string{"Druckluft"},
		Users:  []ThresholdChoice{{"u1", "Eva Muster"}},
		Groups: []ThresholdChoice{{"g1", "Instandhaltung"}},
		Rows: []ThresholdRow{{
			Threshold: energie.Threshold{ID: "t1", Name: "zu hoch", Active: true, State: "alarm", AlarmSince: &at, TicketID: &tid},
			CondText:  "über", LimitText: "8,5 bar", WindowText: "5 min", ResetText: "15 min", PriorityText: "Hoch",
			ResponsibleText: "Eva Muster", StateText: "Alarm seit 15.11. 14:05", Alarm: true,
		}},
	}
	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "energy-thresholds", d); err != nil {
		t.Fatal(err)
	}
	html := sb.String()
	for _, want := range []string{
		"zu hoch", "über 8,5 bar", "Alarm seit 15.11. 14:05", `href="/tickets/` + tid + `"`, "badge b-red",
		`hx-post="/energy/meters/m1/thresholds/save-web"`, `value="user:u1"`, `value="group:g1"`, `<option value="Druckluft">`,
		`hx-post="/energy/thresholds/t1/delete-web"`, "Rückschaltzeit",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Fragment enthält %q nicht", want)
		}
	}
	// ohne Recht: keine Knöpfe, kein Formular
	d.CanSave, d.CanDelete = false, false
	sb.Reset()
	if err := tmpl.ExecuteTemplate(&sb, "energy-thresholds", d); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"save-web", "delete-web", "Neuer Schwellwert"} {
		if strings.Contains(sb.String(), bad) {
			t.Errorf("ohne Recht darf %q nicht erscheinen", bad)
		}
	}
}

// Datenbank: Speichern über das Formular, Fehlermeldungen, Löschen.
func TestThresholdSaveWeb(t *testing.T) {
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
	infra := id(`INSERT INTO infrastructure (name, type) VALUES ($1, 'line') RETURNING id::text`, "Presse "+sfx)
	meter := id(`INSERT INTO energy_meters (meter_key, name, infrastructure_id, kind, unit) VALUES ($1, 'Druckluft', $2::uuid, 'instant', 'bar') RETURNING id::text`, "DL-"+sfx, infra)
	other := id(`INSERT INTO energy_meters (meter_key, name, infrastructure_id, kind, unit) VALUES ($1, 'Anderer', $2::uuid, 'instant', 'bar') RETURNING id::text`, "AN-"+sfx, infra)
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Eva', 'Muster', 'admin', '', '') RETURNING id::text`, "t"+sfx)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM energy_meters WHERE id IN ($1::uuid, $2::uuid)`, meter, other)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, user)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
	}()
	h := &Handler{db: pool, tmpl: loadTestTemplates(t), hasPermHook: func(*http.Request, string) bool { return true }}
	if _, err := h.tmpl.ParseFiles("../../web/templates/widgets/energy_thresholds.gohtml"); err != nil {
		t.Fatal(err)
	}
	save := func(mid string, f url.Values) *httptest.ResponseRecorder {
		return thrDo(h.EnergyThresholdSaveWeb, "POST", "/energy/meters/"+mid+"/thresholds/save-web", map[string]string{"id": mid}, f, true)
	}
	valid := func() url.Values {
		return url.Values{"name": {"zu hoch"}, "condition": {"above"}, "limit": {"8,5"}, "window_minutes": {"5"},
			"reset_minutes": {"15"}, "priority": {"high"}, "tag": {"Druckluft"}, "active": {"1"}}
	}

	// ungültiger Zeitraum: Meldung, Eingaben bleiben im Formular, nichts gespeichert
	f := valid()
	f.Set("window_minutes", "0")
	w := save(meter, f)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Der Zeitraum muss zwischen 1 und 1440 Minuten liegen.") ||
		!strings.Contains(w.Body.String(), `value="zu hoch"`) {
		t.Fatalf("ungültiger Zeitraum: %d %s", w.Code, w.Body.String())
	}
	// Grenzwert keine Zahl
	f = valid()
	f.Set("limit", "viel")
	if w := save(meter, f); !strings.Contains(w.Body.String(), "Der Grenzwert muss eine Zahl sein.") {
		t.Errorf("Grenzwert: %s", w.Body.String())
	}
	// unbekannter Benutzer: deutsche Meldung statt Datenbankfehler
	f = valid()
	f.Set("responsible", "user:55555555-5555-4555-8555-555555555555")
	if w := save(meter, f); !strings.Contains(w.Body.String(), "Die gewählte Person oder Gruppe gibt es nicht.") {
		t.Errorf("unbekannter Benutzer: %s", w.Body.String())
	}
	st := energie.NewStore(pool)
	if list, _ := st.Thresholds(ctx, meter); len(list) != 0 {
		t.Fatalf("Fehler dürfen nichts anlegen: %+v", list)
	}

	// Erfolg: legt an, Fragment zeigt Liste und Meldung
	f = valid()
	f.Set("responsible", "user:"+user)
	w = save(meter, f)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Schwellwert gespeichert.") || !strings.Contains(w.Body.String(), "über 8,5 bar") {
		t.Fatalf("anlegen: %d %s", w.Code, w.Body.String())
	}
	if b := w.Body.String(); !strings.Contains(b, `id="em-thr-lbl-`+meter+`" hx-swap-oob="true"`) || !strings.Contains(b, `em-thr-lbl">1</span>`) {
		t.Errorf("Speichern frischt den Zählerknopf nicht auf: %s", b)
	}
	list, err := st.Thresholds(ctx, meter)
	must(err)
	if len(list) != 1 {
		t.Fatalf("angelegt: %+v", list)
	}
	th := list[0]
	if th.Limit != 8.5 || th.WindowMinutes != 5 || th.ResetMinutes != 15 || th.Priority != "high" || th.Tag != "Druckluft" ||
		th.ResponsibleUserID == nil || *th.ResponsibleUserID != user || !th.Active {
		t.Errorf("gespeichert: %+v", th)
	}

	// ändern über tid; ein Schwellwert einer anderen Messstelle lässt sich so nicht ändern
	f = valid()
	f.Set("tid", th.ID)
	f.Set("name", "noch zu hoch")
	f.Del("active")
	if w := save(meter, f); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "noch zu hoch") {
		t.Fatalf("ändern: %d %s", w.Code, w.Body.String())
	}
	got, err := st.Threshold(ctx, th.ID)
	must(err)
	if got.Name != "noch zu hoch" || got.Active {
		t.Errorf("geändert: %+v", got)
	}
	if w := save(other, f); w.Code != http.StatusNotFound {
		t.Errorf("fremde Messstelle: %d", w.Code)
	}

	// Fragment mit Formular zum Bearbeiten
	edit := thrDo(h.EnergyThresholdsWeb, "GET", "/energy/meters/"+meter+"/thresholds?edit="+th.ID, map[string]string{"id": meter}, nil, true)
	if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), `value="noch zu hoch"`) {
		t.Errorf("Bearbeiten-Fragment: %d %s", edit.Code, edit.Body.String())
	}

	// Zähler für die Übersicht und Filter „mit Alarm“
	counts, err := st.ThresholdCounts(ctx)
	must(err)
	if counts[meter].Total != 1 || counts[meter].Alarms != 0 {
		t.Errorf("Zähler: %+v", counts[meter])
	}
	_, err = pool.Exec(ctx, `INSERT INTO energy_threshold_state (threshold_id, state, alarm_since) VALUES ($1::uuid, 'alarm', now())`, th.ID)
	must(err)
	ms, err := st.List(ctx, energie.Filter{Alarm: true, AllowedIDs: map[string]bool{meter: true, other: true}})
	must(err)
	if len(ms) != 1 || ms[0].ID != meter {
		t.Errorf("Filter mit Alarm: %+v", ms)
	}

	// Verantwortlicher ist inzwischen inaktiv: Name mit Hinweis statt leerem Text
	_, err = pool.Exec(ctx, `UPDATE energy_thresholds SET responsible_user_id = $2::uuid WHERE id = $1::uuid`, th.ID, user)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1::uuid`, user)
	must(err)
	if w := thrDo(h.EnergyThresholdsWeb, "GET", "/energy/meters/"+meter+"/thresholds", map[string]string{"id": meter}, nil, true); !strings.Contains(w.Body.String(), "Eva Muster (inaktiv)") {
		t.Errorf("inaktiver Verantwortlicher: %s", w.Body.String())
	}

	// löschen
	w = thrDo(h.EnergyThresholdDeleteWeb, "POST", "/energy/thresholds/"+th.ID+"/delete-web", map[string]string{"tid": th.ID}, nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Schwellwert gelöscht.") {
		t.Fatalf("löschen: %d %s", w.Code, w.Body.String())
	}
	if b := w.Body.String(); !strings.Contains(b, `id="em-thr-lbl-`+meter+`" hx-swap-oob="true"`) || !strings.Contains(b, `em-thr-lbl">keine</span>`) {
		t.Errorf("Löschen frischt den Zählerknopf nicht auf: %s", b)
	}
	if list, _ := st.Thresholds(ctx, meter); len(list) != 0 {
		t.Errorf("nach dem Löschen: %+v", list)
	}
	if w := thrDo(h.EnergyThresholdDeleteWeb, "POST", "/energy/thresholds/"+th.ID+"/delete-web", map[string]string{"tid": th.ID}, nil, true); w.Code != http.StatusNotFound {
		t.Errorf("erneut löschen: %d", w.Code)
	}
}
