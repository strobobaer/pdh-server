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
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/users"
	"pdh/internal/modules/ruesten"
)

const setupTestID = "11111111-1111-4111-8111-111111111111"

// setupDo ruft einen Handler mit der Rüstzeit-ID im Pfad auf; uid "" = nicht angemeldet.
func setupDo(h http.HandlerFunc, method, path, id, uid string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	if uid != "" {
		ctx = context.WithValue(ctx, "user", &users.User{ID: uid, FirstName: "Rita", LastName: "Rüst", Role: "admin"})
	}
	w := httptest.NewRecorder()
	h(w, req.WithContext(ctx))
	return w
}

func setupLocation(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Status %d: %s", w.Code, w.Body.String())
	}
	loc, _ := url.QueryUnescape(w.Header().Get("Location"))
	return loc
}

// ── Rechte ───────────────────────────────────────────────────────────────

func TestSetupPagesRequirePermission(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}

	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
		id   string
		want string
	}{
		{"Arbeitsbereich", h.SetupPage, "/setup", "", permSetupView},
		{"Anlegen-Seite", h.SetupNewPage, "/setup/new", "", permSetupEdit},
		{"Detailseite", h.SetupDetailPage, "/setup/" + setupTestID, setupTestID, permSetupView},
	} {
		asked = nil
		w := setupDo(c.fn, "GET", c.path, c.id, "", nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
			t.Errorf("%s ohne Recht: %d %s", c.name, w.Code, w.Header().Get("Location"))
		}
		if len(asked) == 0 || asked[len(asked)-1] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}

	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
		id   string
		want string
	}{
		{"anlegen", h.SetupCreateWeb, "/setup", "", permSetupEdit},
		{"bearbeiten", h.SetupEditWeb, "/setup/" + setupTestID + "/edit-web", setupTestID, permSetupEdit},
		{"beenden", h.SetupFinishWeb, "/setup/" + setupTestID + "/finish-web", setupTestID, permSetupEdit},
		{"löschen", h.SetupDeleteWeb, "/setup/" + setupTestID + "/delete-web", setupTestID, permSetupAdmin},
	} {
		asked = nil
		w := setupDo(c.fn, "POST", c.path, c.id, "", url.Values{})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[len(asked)-1] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}

	// Löschen fragt setup.admin – setup.edit genügt nicht.
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSetupAdmin }
	if w := setupDo(h.SetupDeleteWeb, "POST", "/setup/"+setupTestID+"/delete-web", setupTestID, "", url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Löschen mit setup.edit, ohne setup.admin: Status %d", w.Code)
	}
	// Keine gültige ID: nicht gefunden statt Datenbankzugriff.
	if w := setupDo(h.SetupDeleteWeb, "POST", "/setup/x/delete-web", "x", "", url.Values{}); w.Code != http.StatusNotFound {
		t.Errorf("ungültige ID: Status %d", w.Code)
	}
}

func TestSetupGateRules(t *testing.T) {
	id := setupTestID
	for _, c := range []struct {
		method, path, perm, ref string
		hit                     bool
	}{
		{"POST", "/setup", permSetupEdit, "", true},
		{"POST", "/setup/", permSetupEdit, "", true},
		{"POST", "/setup/" + id + "/edit-web", permSetupEdit, setupRef, true},
		{"POST", "/setup/" + id + "/finish-web", permSetupEdit, setupRef, true},
		{"POST", "/setup/" + id + "/delete-web", permSetupAdmin, setupRef, true},
		{"GET", "/setup/" + id + "/delete-web", "", "", false},
		{"POST", "/setup/" + id + "/unbekannt-web", "", "", false},
	} {
		_, perm, ref, gotID, hit := matchGate(c.method, c.path)
		if hit != c.hit {
			t.Errorf("%s %s: Treffer %v, erwartet %v", c.method, c.path, hit, c.hit)
			continue
		}
		if !hit {
			continue
		}
		if perm != c.perm || ref != c.ref {
			t.Errorf("%s %s: Recht %q / Typ %q, erwartet %q / %q", c.method, c.path, perm, ref, c.perm, c.ref)
		}
		if c.ref != "" && gotID != id {
			t.Errorf("%s %s: ID %q", c.method, c.path, gotID)
		}
	}
}

// ── Verzeichnisse der Vorgänge ───────────────────────────────────────────

func TestSetupInRecordRegistries(t *testing.T) {
	mi, ok := recordModules[setupRef]
	if !ok || mi.Table != "setup_times" || mi.Path != "/setup/" || mi.Icon == "" || !strings.Contains(mi.TitleExpr, "RZ-") {
		t.Errorf("recordModules[setup]: %+v", mi)
	}
	found := false
	for _, m := range recordModuleOrder {
		found = found || m == setupRef
	}
	if !found {
		t.Error("recordModuleOrder ohne setup")
	}
	fm, ok := fieldModuleByKey(setupRef)
	if !ok || fm.Table != "setup_times" || fm.EditPerm != permSetupEdit || fm.ViewPerm != permSetupView {
		t.Errorf("fieldModules[setup]: %+v", fm)
	}
	if areaAdminPerm[setupRef] != "setup.admin" {
		t.Errorf("Bereichsadministrator: %q", areaAdminPerm[setupRef])
	}
	if src, ok := recordDeptSource[setupRef]; !ok || src.table != "setup_times" || src.group {
		t.Errorf("recordDeptSource[setup]: %+v", src)
	}
	if st, ok := scopeTables[setupRef]; !ok || st.table != "setup_times" ||
		!strings.Contains(st.people, "created_by") || !strings.Contains(st.people, "responsible_to") || !strings.Contains(st.people, "assigned_to") {
		t.Errorf("scopeTables[setup]: %+v", st)
	}
	if rt, id, ok := scopeTarget("/setup/" + setupTestID); !ok || rt != setupRef || id != setupTestID {
		t.Errorf("scopeTarget: %q %q %v", rt, id, ok)
	}
	if rt, id, ok := scopeTarget("/records/fields/setup/" + setupTestID); !ok || rt != setupRef || id != setupTestID {
		t.Errorf("scopeTarget Feldsätze: %q %q %v", rt, id, ok)
	}
	if driveModuleFolders[setupRef] != "Rüstzeiten" {
		t.Errorf("Ablageordner: %q", driveModuleFolders[setupRef])
	}
	if linkRecordKey("/setup/"+setupTestID) != setupRef {
		t.Error("linkRecordKey")
	}
	if recordIcon(setupRef) == "" {
		t.Error("Symbol fehlt")
	}
	def, ok := chatLinkTypeByPath("setup")
	if !ok || def.table != "setup_times" || def.refType != setupRef {
		t.Errorf("chatLinkTypes[setup]: %+v", def)
	}
	if got := chatExtractLinks("siehe https://pdh.example/setup/" + setupTestID + " bitte"); len(got) != 1 || got[0][0] != "setup" {
		t.Errorf("Chat-Verweis: %v", got)
	}
	nav := false
	for _, d := range navDefs {
		nav = nav || (d.Key == "setup" && d.Href == "/setup")
	}
	if !nav {
		t.Error("Navigationseintrag fehlt")
	}
	quick := false
	if qa, err := loadQuickActions(nil, context.Background(), "", WidgetInstance{}, func(string) bool { return true }); err == nil {
		for _, a := range qa.([]quickAction) {
			quick = quick || (a.Name == "Rüstzeit erfassen" && a.URL == "/setup/new")
		}
	}
	if !quick {
		t.Error("Schnellaktion „Rüstzeit erfassen“ fehlt")
	}
	if k := workKinds[setupRef]; k.DoURL("abc") != "/setup/abc/finish-web" {
		t.Errorf("Beenden-Adresse: %q", k.DoURL("abc"))
	}
	if infraHistoryModuleExists(setupRef) == false {
		t.Error("Anlagen-Historie ohne Filter „Rüstzeiten“")
	}
}

func infraHistoryModuleExists(key string) bool {
	_, ok := infraHistoryModuleByKey(key)
	return ok
}

// ── Detailseite ──────────────────────────────────────────────────────────

func setupTestEntry(status string, now time.Time) *ruesten.Entry {
	fault := "22222222-2222-4222-8222-222222222222"
	resp, lead := "u1", "u2"
	e := &ruesten.Entry{ID: setupTestID, Number: 7, InfraID: "i1", InfraName: "Presse 3", StartedAt: now.Add(-3 * time.Hour),
		ReasonID: "r1", ReasonName: "Produktumstellung", ButtonName: "Formatwechsel", Origin: ruesten.OriginManual, Status: status,
		Note: "Düse getauscht", MaxMinutes: 120, GraceMinutes: 15, ResponsibleID: &resp, ShiftLeaderID: &lead,
		ResponsibleName: "Vera Verantwortlich", ShiftLeaderName: "Sven Schicht"}
	switch status {
	case ruesten.StatusDone:
		end := now.Add(-2 * time.Hour)
		e.EndedAt = &end
	case ruesten.StatusFault:
		end := now.Add(-45 * time.Minute)
		e.EndedAt = &end
		e.FaultID = &fault
	}
	return e
}

func TestSetupDetailView(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local)
	run := newSetupDetail(setupTestEntry(ruesten.StatusRunning, now), now)
	if run.Code != "RZ-0007" || run.StatusText != "Rüsten" || !run.Overdue || run.TimesLocked || run.FaultURL != "" {
		t.Errorf("laufend: %+v", run)
	}
	if run.StartValue != "2026-11-14T09:00" || run.EndValue != "" || run.Duration != "3 Std. 0 Min." {
		t.Errorf("laufend Zeiten: %q %q %q", run.StartValue, run.EndValue, run.Duration)
	}
	e := setupTestEntry(ruesten.StatusRunning, now)
	e.StartedAt = now.Add(-30 * time.Minute)
	if v := newSetupDetail(e, now); v.Overdue {
		t.Error("innerhalb der Höchstdauer ist nicht überschritten")
	}
	done := newSetupDetail(setupTestEntry(ruesten.StatusDone, now), now)
	if done.StatusText != "Beendet" || done.Overdue || done.EndValue != "2026-11-14T10:00" || done.Duration != "1 Std. 0 Min." {
		t.Errorf("beendet: %+v", done)
	}
	fault := newSetupDetail(setupTestEntry(ruesten.StatusFault, now), now)
	if fault.StatusText != "Störung durch Rüsten" || !fault.TimesLocked || fault.FaultURL != "/faults/22222222-2222-4222-8222-222222222222" || fault.Overdue {
		t.Errorf("Störung: %+v", fault)
	}
}

func TestSetupDetailRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	now := time.Now()
	reasons := []ruesten.Reason{{ID: "r1", Name: "Produktumstellung", Active: true}, {ID: "r2", Name: "Reinigung", Active: true}}
	build := func(status string, edit, del bool) SetupDetailData {
		d := newSetupDetail(setupTestEntry(status, now), now)
		d.Reasons, d.CanEdit, d.CanDelete = reasons, edit, del
		return d
	}
	must := func(html string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(html, w) {
				t.Errorf("Seite enthält %q nicht", w)
			}
		}
	}
	not := func(html string, nots ...string) {
		t.Helper()
		for _, w := range nots {
			if strings.Contains(html, w) {
				t.Errorf("Seite darf %q nicht enthalten", w)
			}
		}
	}

	run := renderPage(t, tmpl, "setup_detail", build(ruesten.StatusRunning, true, true))
	must(run, "RZ-0007", "Formatwechsel", "Presse 3", "Rüsten", "Höchstdauer überschritten", "Vera Verantwortlich", "Sven Schicht",
		`action="/setup/`+setupTestID+`/edit-web"`, `action="/setup/`+setupTestID+`/finish-web"`, `action="/setup/`+setupTestID+`/delete-web"`,
		`value="`+newSetupDetail(setupTestEntry(ruesten.StatusRunning, now), now).StartValue+`"`, "/records/fields/setup/", "/records/links/setup/", "/records/history/setup/", "/records/meta/setup/", `data-attach-ref="setup:`+setupTestID+`"`, "Düse getauscht")
	not(run, "Störmeldung ansehen")
	if strings.Contains(run, `name="started_at"`) && strings.Contains(run[strings.Index(run, `name="started_at"`)-80:strings.Index(run, `name="started_at"`)+80], "disabled") {
		t.Error("laufend: Beginn darf nicht gesperrt sein")
	}

	done := renderPage(t, tmpl, "setup_detail", build(ruesten.StatusDone, true, false))
	must(done, "Beendet", `action="/setup/`+setupTestID+`/edit-web"`)
	not(done, "finish-web", "delete-web", "Höchstdauer überschritten")

	fault := renderPage(t, tmpl, "setup_detail", build(ruesten.StatusFault, true, false))
	must(fault, "Störung durch Rüsten", `href="/faults/22222222-2222-4222-8222-222222222222"`, "Störmeldung ansehen")
	not(fault, "finish-web")
	i := strings.Index(fault, `name="started_at"`)
	if i < 0 || !strings.Contains(fault[i:i+200], "disabled") {
		t.Error("Störung: Beginn muss gesperrt sein")
	}

	// ohne Rechte: nur ansehen
	ro := renderPage(t, tmpl, "setup_detail", build(ruesten.StatusRunning, false, false))
	must(ro, "RZ-0007", "Presse 3")
	not(ro, "edit-web", "finish-web", "delete-web", "data-mod=\"setup\"")
}

func TestSetupNewRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := SetupDetailData{IsNew: true, CanEdit: true, Reasons: []ruesten.Reason{{ID: "r1", Name: "Produktumstellung", Active: true}},
		StartValue: "2026-11-14T08:00", InfraID: "i1", InfraName: "Presse 3", Err: "An dieser Anlage läuft bereits eine Rüstzeit."}
	html := renderPage(t, tmpl, "setup_detail", d)
	for _, want := range []string{`action="/setup"`, `name="infrastructure_id"`, `name="reason_id"`, `name="started_at"`, `name="ended_at"`,
		`name="duration_min"`, `name="note"`, "Produktumstellung", "läuft bereits", `value="2026-11-14T08:00"`} {
		if !strings.Contains(html, want) {
			t.Errorf("Anlegen-Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(html, "/records/history/") {
		t.Error("Anlegen-Seite ohne Verlauf")
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

type setupFx struct {
	t     *testing.T
	pool  *pgxpool.Pool
	ctx   context.Context
	h     *Handler
	store *ruesten.Store
	sfx   string
	n     int
}

func newSetupFx(t *testing.T) *setupFx {
	t.Helper()
	pool := workTestPool(t)
	f := &setupFx{t: t, pool: pool, ctx: context.Background(), store: ruesten.NewStore(pool),
		sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	f.h = &Handler{db: pool, jwtSecret: "t", tmpl: loadTestTemplates(t), hasPermHook: func(*http.Request, string) bool { return true }}
	return f
}

func (f *setupFx) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *setupFx) infra(name string) string {
	f.t.Helper()
	f.n++
	var id string
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'device') RETURNING id::text`,
		"RZW-"+f.sfx+"-"+name).Scan(&id))
	f.t.Cleanup(func() {
		c := context.Background()
		f.pool.Exec(c, `DELETE FROM record_history WHERE ref_type = 'setup' AND ref_id IN (SELECT id FROM setup_times WHERE infrastructure_id = $1)`, id)
		f.pool.Exec(c, `DELETE FROM record_categories WHERE module = 'setup' AND record_id IN (SELECT id FROM setup_times WHERE infrastructure_id = $1)`, id)
		f.pool.Exec(c, `DELETE FROM setup_times WHERE infrastructure_id = $1`, id)
		f.pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1`, id)
	})
	return id
}

func (f *setupFx) user() string {
	f.t.Helper()
	f.n++
	var id string
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, phone)
		VALUES ('rzw'||$1::text, 'rzw'||$1::text||'@example.invalid', 'x', 'Rita', 'Rüst', '') RETURNING id::text`,
		f.sfx+string(rune('a'+f.n))).Scan(&id))
	f.t.Cleanup(func() {
		c := context.Background()
		f.pool.Exec(c, `DELETE FROM record_history WHERE created_by = $1`, id)
		f.pool.Exec(c, `UPDATE infrastructure SET responsible_to = NULL WHERE responsible_to = $1`, id)
		f.pool.Exec(c, `DELETE FROM setup_times WHERE created_by = $1 OR responsible_to = $1 OR assigned_to = $1`, id)
		f.pool.Exec(c, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func (f *setupFx) reason() string {
	f.t.Helper()
	var id string
	f.must(f.pool.QueryRow(f.ctx, `SELECT id::text FROM setup_reasons WHERE key = 'changeover'`).Scan(&id))
	return id
}

func localForm(ts time.Time) string { return ts.Format("2006-01-02T15:04") }

func (f *setupFx) entry(id string) *ruesten.Entry {
	f.t.Helper()
	e, err := f.store.Get(f.ctx, id)
	f.must(err)
	return e
}

func setupIDFromLocation(loc string) string {
	return strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/setup/")
}

func TestSetupCreateFinishEdit(t *testing.T) {
	f := newSetupFx(t)
	infra, reason, uid := f.infra("a"), f.reason(), f.user()
	h := f.h
	start := time.Now().Add(-90 * time.Minute).Truncate(time.Minute)

	// Anlegen ohne Ende: läuft; die Uhrzeit des Formulars gilt als Ortszeit.
	w := setupDo(h.SetupCreateWeb, "POST", "/setup", "", uid, url.Values{
		"infrastructure_id": {infra}, "reason_id": {reason}, "started_at": {localForm(start)}, "note": {"Düse"}})
	loc := setupLocation(t, w)
	if !strings.HasPrefix(loc, "/setup/") {
		t.Fatalf("Weiterleitung %q", loc)
	}
	id := setupIDFromLocation(loc)
	e := f.entry(id)
	if e.Status != ruesten.StatusRunning || e.EndedAt != nil || e.Origin != ruesten.OriginManual || e.Note != "Düse" || e.CreatedBy != uid {
		t.Fatalf("angelegt: %+v", e)
	}
	wantStart, _ := time.ParseInLocation("2006-01-02T15:04", localForm(start), time.Local)
	if !e.StartedAt.Equal(wantStart) {
		t.Errorf("Beginn %v, erwartet %v (Ortszeit)", e.StartedAt, wantStart)
	}

	// Zweite laufende Rüstzeit an derselben Anlage wird abgewiesen.
	w = setupDo(h.SetupCreateWeb, "POST", "/setup", "", uid, url.Values{
		"infrastructure_id": {infra}, "reason_id": {reason}, "started_at": {localForm(start)}})
	if loc := setupLocation(t, w); !strings.HasPrefix(loc, "/setup/new?") || !strings.Contains(loc, "läuft bereits") {
		t.Errorf("doppelt laufend: %q", loc)
	}
	// Beginn in der Zukunft
	other := f.infra("b")
	w = setupDo(h.SetupCreateWeb, "POST", "/setup", "", uid, url.Values{
		"infrastructure_id": {other}, "reason_id": {reason}, "started_at": {localForm(time.Now().Add(2 * time.Hour))}})
	if loc := setupLocation(t, w); !strings.Contains(loc, "Zukunft") {
		t.Errorf("Zukunft: %q", loc)
	}
	// Pflichtangaben
	w = setupDo(h.SetupCreateWeb, "POST", "/setup", "", uid, url.Values{"started_at": {localForm(start)}})
	if loc := setupLocation(t, w); !strings.HasPrefix(loc, "/setup/new?") || !strings.Contains(loc, "err=") {
		t.Errorf("ohne Anlage: %q", loc)
	}

	// Bearbeiten: Ende setzen beendet die Rüstzeit, jede Änderung steht im Verlauf.
	end := start.Add(40 * time.Minute)
	w = setupDo(h.SetupEditWeb, "POST", "/setup/"+id+"/edit-web", id, uid, url.Values{
		"reason_id": {reason}, "started_at": {localForm(start)}, "ended_at": {localForm(end)}, "note": {"Düse und Filter"}})
	setupLocation(t, w)
	e = f.entry(id)
	if e.Status != ruesten.StatusDone || e.EndedAt == nil || e.Note != "Düse und Filter" {
		t.Fatalf("bearbeitet: %+v", e)
	}
	var hist int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'setup' AND ref_id = $1::uuid AND action = 'updated'`, id).Scan(&hist))
	if hist < 2 {
		t.Errorf("Verlauf: %d Änderungen", hist)
	}
	// Ende vor Beginn
	w = setupDo(h.SetupEditWeb, "POST", "/setup/"+id+"/edit-web", id, uid, url.Values{
		"reason_id": {reason}, "started_at": {localForm(start)}, "ended_at": {localForm(start.Add(-time.Hour))}})
	if loc := setupLocation(t, w); !strings.Contains(loc, "err=") || !strings.HasPrefix(loc, "/setup/"+id) {
		t.Errorf("Ende vor Beginn: %q", loc)
	}

	// Leeres Ende bei einer beendeten Rüstzeit: Fehlermeldung, nichts ändert sich, nie wieder geöffnet.
	w = setupDo(h.SetupEditWeb, "POST", "/setup/"+id+"/edit-web", id, uid, url.Values{
		"reason_id": {reason}, "started_at": {localForm(start)}, "ended_at": {""}, "note": {"anders"}})
	if loc := setupLocation(t, w); !strings.Contains(loc, "err=") || !strings.Contains(loc, "Ende") {
		t.Errorf("leeres Ende: %q", loc)
	}
	if e := f.entry(id); e.Status != ruesten.StatusDone || e.EndedAt == nil || e.Note != "Düse und Filter" {
		t.Errorf("leeres Ende darf nichts ändern: %+v", e)
	}

	// Beenden einer laufenden Rüstzeit (mit Vermerk); zweites Beenden wird abgewiesen.
	w = setupDo(h.SetupCreateWeb, "POST", "/setup", "", uid, url.Values{
		"infrastructure_id": {other}, "reason_id": {reason}, "started_at": {localForm(time.Now().Add(-20 * time.Minute))}})
	id2 := setupIDFromLocation(setupLocation(t, w))
	w = setupDo(h.SetupFinishWeb, "POST", "/setup/"+id2+"/finish-web", id2, uid, url.Values{"note": {"fertig"}})
	if loc := setupLocation(t, w); !strings.HasPrefix(loc, "/setup/"+id2) || strings.Contains(loc, "err=") {
		t.Errorf("Beenden: %q", loc)
	}
	if e := f.entry(id2); e.Status != ruesten.StatusDone || e.EndedAt == nil || e.Note != "fertig" {
		t.Errorf("beendet: %+v", e)
	}
	w = setupDo(h.SetupFinishWeb, "POST", "/setup/"+id2+"/finish-web", id2, uid, url.Values{})
	if loc := setupLocation(t, w); !strings.Contains(loc, "err=") {
		t.Errorf("zweites Beenden: %q", loc)
	}

	// Störung: Beginn und Ende bleiben gesperrt, der Vermerk ist änderbar.
	third := f.infra("c")
	e3, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: third, ReasonID: reason, StartedAt: start.Add(-3 * time.Hour),
		EndedAt: ptrTime(start.Add(-time.Hour))}, uid)
	f.must(err)
	_, err = f.pool.Exec(f.ctx, `UPDATE setup_times SET status = 'fault' WHERE id = $1::uuid`, e3.ID)
	f.must(err)
	w = setupDo(h.SetupEditWeb, "POST", "/setup/"+e3.ID+"/edit-web", e3.ID, uid, url.Values{
		"reason_id": {reason}, "started_at": {localForm(start)}, "ended_at": {localForm(start.Add(time.Hour))}, "note": {"Vermerk"}})
	setupLocation(t, w)
	got := f.entry(e3.ID)
	if got.Note != "Vermerk" || !got.StartedAt.Equal(e3.StartedAt) || got.EndedAt == nil || !got.EndedAt.Equal(*e3.EndedAt) || got.Status != ruesten.StatusFault {
		t.Errorf("Störung gesperrt: %+v (vorher %+v)", got, e3)
	}
	// Beenden einer Rüstzeit mit Störmeldung: nicht möglich
	w = setupDo(h.SetupFinishWeb, "POST", "/setup/"+e3.ID+"/finish-web", e3.ID, uid, url.Values{})
	if loc := setupLocation(t, w); !strings.Contains(loc, "err=") {
		t.Errorf("Beenden bei Störung: %q", loc)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestSetupDelete(t *testing.T) {
	f := newSetupFx(t)
	infra, reason, uid := f.infra("d"), f.reason(), f.user()
	e, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: reason, StartedAt: time.Now().Add(-time.Hour)}, uid)
	f.must(err)
	var cat string
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO categories (name, color, icon, sort_order) VALUES ('zz-rz-'||$1::text, '#112233', 'ti-tag', 99) RETURNING id::text`, f.sfx).Scan(&cat))
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM record_categories WHERE category_id = $1::uuid`, cat)
		f.pool.Exec(context.Background(), `DELETE FROM categories WHERE id = $1::uuid`, cat)
	})
	_, err = f.pool.Exec(f.ctx, `INSERT INTO record_categories (module, record_id, category_id) VALUES ('setup', $1::uuid, $2::uuid)`, e.ID, cat)
	f.must(err)

	f.h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSetupAdmin }
	if w := setupDo(f.h.SetupDeleteWeb, "POST", "/setup/"+e.ID+"/delete-web", e.ID, uid, url.Values{}); w.Code != http.StatusForbidden {
		t.Fatalf("ohne Bereichsadministrator: %d", w.Code)
	}
	f.h.hasPermHook = func(*http.Request, string) bool { return true }
	w := setupDo(f.h.SetupDeleteWeb, "POST", "/setup/"+e.ID+"/delete-web", e.ID, uid, url.Values{})
	if loc := setupLocation(t, w); !strings.HasPrefix(loc, "/setup") || strings.HasPrefix(loc, "/setup/"+e.ID) {
		t.Errorf("nach dem Löschen: %q", loc)
	}
	for _, q := range []string{
		`SELECT count(*) FROM setup_times WHERE id = $1::uuid`,
		`SELECT count(*) FROM record_history WHERE ref_type = 'setup' AND ref_id = $1::uuid`,
		`SELECT count(*) FROM record_categories WHERE module = 'setup' AND record_id = $1::uuid`,
		`SELECT count(*) FROM record_field_values WHERE module = 'setup' AND record_id = $1::uuid`,
	} {
		var n int
		f.must(f.pool.QueryRow(f.ctx, q, e.ID).Scan(&n))
		if n != 0 {
			t.Errorf("nach dem Löschen übrig (%d): %s", n, q)
		}
	}
	if w := setupDo(f.h.SetupDeleteWeb, "POST", "/setup/"+e.ID+"/delete-web", e.ID, uid, url.Values{}); w.Code != http.StatusNotFound {
		t.Errorf("zweites Löschen: %d", w.Code)
	}
}

func TestSetupDeptScope(t *testing.T) {
	f := newSetupFx(t)
	reason, creator, other := f.reason(), f.user(), f.user()
	var dep, dep2 string
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO departments (name) VALUES ('zz-rz-a-'||$1::text) RETURNING id::text`, f.sfx).Scan(&dep))
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO departments (name) VALUES ('zz-rz-b-'||$1::text) RETURNING id::text`, f.sfx).Scan(&dep2))
	infra := f.infra("e")
	t.Cleanup(func() {
		c := context.Background()
		f.pool.Exec(c, `DELETE FROM record_history WHERE ref_type = 'setup' AND ref_id IN (SELECT id FROM setup_times WHERE infrastructure_id = $1)`, infra)
		f.pool.Exec(c, `DELETE FROM setup_times WHERE infrastructure_id = $1`, infra)
		f.pool.Exec(c, `UPDATE infrastructure SET department_id = NULL WHERE id = $1`, infra)
		f.pool.Exec(c, `DELETE FROM departments WHERE id IN ($1, $2)`, dep, dep2)
	})
	_, err := f.pool.Exec(f.ctx, `UPDATE infrastructure SET department_id = $2::uuid WHERE id = $1::uuid`, infra, dep)
	f.must(err)
	e, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: reason, StartedAt: time.Now().Add(-time.Hour)}, creator)
	f.must(err)

	if got := f.h.recordDepartments(f.ctx, setupRef, e.ID); len(got) != 1 || got[0] != dep {
		t.Errorf("Abteilung über die Anlage: %v", got)
	}
	if !f.h.recordInScope(f.ctx, &deptScope{UserID: other, Departments: []string{dep}}, setupRef, e.ID) {
		t.Error("eigene Abteilung muss sichtbar sein")
	}
	if f.h.recordInScope(f.ctx, &deptScope{UserID: other, Departments: []string{dep2}}, setupRef, e.ID) {
		t.Error("fremde Abteilung darf nicht sichtbar sein")
	}
	if !f.h.recordInScope(f.ctx, &deptScope{UserID: creator, Departments: []string{dep2}}, setupRef, e.ID) {
		t.Error("Ersteller bleibt beteiligt und sieht die Rüstzeit")
	}
	ids := f.h.allowedIDsFor(f.ctx, &deptScope{UserID: other, Departments: []string{dep2}}, setupRef)
	if ids[e.ID] {
		t.Error("Liste: fremde Abteilung darf nicht auftauchen")
	}
}

func TestInfraHistoryHasSetup(t *testing.T) {
	f := newSetupFx(t)
	infra, reason, uid := f.infra("h"), f.reason(), f.user()
	now := time.Now()
	done, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: reason, StartedAt: now.Add(-5 * time.Hour), EndedAt: ptrTime(now.Add(-4 * time.Hour))}, uid)
	f.must(err)
	run, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: reason, StartedAt: now.Add(-30 * time.Minute)}, uid)
	f.must(err)

	req := httptest.NewRequest("GET", "/infrastructure/"+infra+"/history", nil)
	load := func(state string) map[string]InfraHistoryEntry {
		t.Helper()
		res, err := f.h.loadInfraHistory(f.ctx, req, infra, infraHistoryFilter{Modules: []string{"setup"}, IncludeChildren: true, State: state}, 50)
		f.must(err)
		out := map[string]InfraHistoryEntry{}
		for _, e := range res.Entries {
			out[e.ID] = e
		}
		return out
	}
	all := load("")
	if len(all) != 2 {
		t.Fatalf("Einträge: %v", all)
	}
	d := all[done.ID]
	if d.Module != "setup" || d.ModuleLabel != "Rüstzeiten" || d.DetailURL != "/setup/"+done.ID || !strings.HasPrefix(d.Title, ruesten.Number(done.Number)+" · ") ||
		d.StatusLabel != "Beendet" || d.ClosedLabel == "" || d.OccurredAt.IsZero() {
		t.Errorf("beendete Rüstzeit: %+v", d)
	}
	if r := all[run.ID]; r.StatusLabel != "Rüsten" || r.ClosedLabel != "" {
		t.Errorf("laufende Rüstzeit: %+v", r)
	}
	if open := load("open"); len(open) != 1 || open[run.ID].ID == "" {
		t.Errorf("offen: %v", open)
	}
	if closed := load("closed"); len(closed) != 1 || closed[done.ID].ID == "" {
		t.Errorf("abgeschlossen: %v", closed)
	}
}

func TestSetupLinks(t *testing.T) {
	f := newSetupFx(t)
	infra, reason, uid := f.infra("l"), f.reason(), f.user()
	run, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: reason, StartedAt: time.Now().Add(-30 * time.Minute)}, uid)
	f.must(err)

	g := f.h.linkRecords(f.ctx, "infrastructure", infra)
	found := false
	for _, it := range g.Items {
		found = found || it.URL == "/setup/"+run.ID
	}
	if !found {
		t.Errorf("laufende Rüstzeit fehlt bei „Offene Vorgänge an der Anlage“: %+v", g.Items)
	}
	fm, _ := fieldModuleByKey(setupRef)
	if g := f.h.linkInfra(f.ctx, fm, run.ID); len(g.Items) != 1 || g.Items[0].URL != "/infrastructure/"+infra {
		t.Errorf("Anlage: %+v", g.Items)
	}
	if err := f.store.Finish(f.ctx, run.ID, uid, ""); err != nil {
		t.Fatal(err)
	}
	for _, it := range f.h.linkRecords(f.ctx, "infrastructure", infra).Items {
		if it.URL == "/setup/"+run.ID {
			t.Error("beendete Rüstzeit gehört nicht zu den offenen Vorgängen")
		}
	}
}

func TestListMineShowsRunningSetup(t *testing.T) {
	f := newSetupFx(t)
	infra, reason, uid := f.infra("m"), f.reason(), f.user()
	_, err := f.pool.Exec(f.ctx, `UPDATE infrastructure SET responsible_to = $2::uuid WHERE id = $1::uuid`, infra, uid)
	f.must(err)
	run, err := f.store.Create(f.ctx, ruesten.ManualInput{InfraID: infra, ReasonID: reason, StartedAt: time.Now().Add(-30 * time.Minute)}, "")
	f.must(err)
	if run.ResponsibleID == nil || *run.ResponsibleID != uid {
		t.Fatalf("Verantwortlicher nicht übernommen: %+v", run)
	}

	list := func(can func(string) bool) []listItem {
		t.Helper()
		out, err := loadListMine(f.h, f.ctx, uid, WidgetInstance{}, can)
		f.must(err)
		return out.([]listItem)
	}
	has := func(items []listItem) *listItem {
		for i := range items {
			if items[i].URL == "/setup/"+run.ID {
				return &items[i]
			}
		}
		return nil
	}
	it := has(list(func(string) bool { return true }))
	if it == nil {
		t.Fatal("laufende Rüstzeit fehlt unter „Mir zugewiesen“")
	}
	if !strings.Contains(it.Title, ruesten.Number(run.Number)) || !strings.Contains(it.Sub, "Rüstzeit") || !strings.Contains(it.Sub, "Rüsten") {
		t.Errorf("Eintrag: %+v", it)
	}
	if has(list(func(p string) bool { return p != permSetupView })) != nil {
		t.Error("ohne setup.view keine Rüstzeit in der Liste")
	}
	f.must(f.store.Finish(f.ctx, run.ID, uid, ""))
	if has(list(func(string) bool { return true })) != nil {
		t.Error("beendete Rüstzeit gehört nicht mehr in „Mir zugewiesen“")
	}
}
