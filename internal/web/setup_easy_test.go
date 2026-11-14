package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/ruesten"
)

// ── Hilfen ───────────────────────────────────────────────────────────────

// button legt eine Rüsttaste an und ordnet sie der Anlage zu.
func (f *setupFx) button(infra, name string) *ruesten.Button {
	f.t.Helper()
	b := &ruesten.Button{Name: name + f.sfx, ReasonID: f.reason(), Active: true}
	f.must(f.store.SaveButton(f.ctx, b))
	f.t.Cleanup(func() { f.pool.Exec(context.Background(), `DELETE FROM setup_buttons WHERE id = $1`, b.ID) })
	cur, err := f.store.InfraButtons(f.ctx, infra)
	f.must(err)
	ids := []string{b.ID}
	for _, c := range cur {
		if c.InheritedFrom == "" {
			ids = append(ids, c.ID)
		}
	}
	f.must(f.store.SetInfraButtons(f.ctx, infra, ids))
	return b
}

// easyDo ruft einen Easy-Modus-Handler wie ein Gerät ohne Anmeldung auf.
func easyDo(h http.HandlerFunc, path, id string, form url.Values) *httptest.ResponseRecorder {
	return setupDo(h, http.MethodPost, path, id, "", form)
}

// easyHTML rendert die Easy-Seite mit dem Baustein „Rüsten“.
func (f *setupFx) easyHTML(infra, lang, notice string) string {
	f.t.Helper()
	c, err := f.h.tmpl.Clone()
	f.must(err)
	c = bindLang(c, lang)
	_, err = c.ParseFiles(filepath.Join("..", "..", "web", "templates", "easy.gohtml"))
	f.must(err)
	d := EasyPageData{AssetID: infra, AssetName: "Presse", Setup: f.h.setupBarFor(f.ctx, lang, infra, "/e/"+infra+"/setup", "", false, notice)}
	var buf bytes.Buffer
	f.must(c.ExecuteTemplate(&buf, "easy.gohtml", d))
	return buf.String()
}

// rzSection schneidet den Abschnitt „Rüsten“ aus der Seite (der Rest der Easy-Seite enthält auch „Fertig“).
func rzSection(html string) string {
	i := strings.Index(html, `<section class="rz"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(html[i:], "</section>")
	return html[i : i+j]
}

func (f *setupFx) cleanupFaultFor(infra string) {
	f.t.Cleanup(func() {
		c := context.Background()
		f.pool.Exec(c, `DELETE FROM setup_times WHERE infrastructure_id = $1`, infra)
		f.pool.Exec(c, `DELETE FROM record_history WHERE ref_type = 'fault' AND ref_id IN (SELECT id FROM faults WHERE infrastructure_id = $1)`, infra)
		f.pool.Exec(c, `DELETE FROM record_categories WHERE module = 'fault' AND record_id IN (SELECT id FROM faults WHERE infrastructure_id = $1)`, infra)
		f.pool.Exec(c, `DELETE FROM faults WHERE infrastructure_id = $1`, infra)
	})
}

func (f *setupFx) startEasy(infra, buttonID string) *httptest.ResponseRecorder {
	return easyDo(f.h.EasySetupStartWeb, "/e/"+infra+"/setup/start", infra, url.Values{"button_id": {buttonID}})
}

func (f *setupFx) limiterReset(infra string) {
	f.t.Cleanup(func() {
		setupLimiter.mu.Lock()
		delete(setupLimiter.hits, "asset:"+infra)
		setupLimiter.mu.Unlock()
		easyLimit.mu.Lock()
		delete(easyLimit.hits, "asset:"+infra)
		easyLimit.mu.Unlock()
	})
}

// ── Easy-Modus ───────────────────────────────────────────────────────────

func TestEasyWithoutButtonsUnchanged(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("ohne")
	if bar := f.h.setupBarFor(f.ctx, "de", infra, "/e/"+infra+"/setup", "", false, ""); bar.Show {
		t.Fatalf("ohne Rüsttaste darf nichts erscheinen: %+v", bar)
	}
	html := f.easyHTML(infra, "de", "")
	if strings.Contains(html, `class="rz"`) || strings.Contains(html, "/setup/start") {
		t.Error("Easy-Seite ohne Rüsttaste zeigt den Abschnitt „Rüsten“")
	}
	if !strings.Contains(html, "Letzte Meldungen") || !strings.Contains(html, `data-start="fault"`) {
		t.Error("Easy-Seite ohne Rüsttaste hat sich verändert")
	}
}

func TestEasyShowsButtonsTranslated(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("tr")
	b := f.button(infra, "Formatwechsel")
	// Übersetzung steht im Zwischenspeicher – so läuft der Test ohne Dienst
	t.Setenv("PDH_TRANSLATE_PROVIDER", "deepl")
	t.Setenv("PDH_DEEPL_KEY", "test")
	f.must2(f.pool.Exec(f.ctx, `INSERT INTO translation_cache (lang, hash, source, text) VALUES ('en', $1, $2, $3)
		ON CONFLICT (lang, hash) DO UPDATE SET text = EXCLUDED.text`, textHash(b.Name), b.Name, "Format change "+f.sfx))
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM translation_cache WHERE lang = 'en' AND hash = $1`, textHash(b.Name))
	})

	en := f.easyHTML(infra, "en", "")
	for _, want := range []string{">Setting up</h2>", ">Format change " + f.sfx + "</button>", `name="button_id" value="` + b.ID + `"`, `action="/e/` + infra + `/setup/start"`} {
		if !strings.Contains(en, want) {
			t.Errorf("englische Easy-Seite ohne %q", want)
		}
	}
	de := f.easyHTML(infra, "de", "")
	if !strings.Contains(de, ">"+b.Name+"</button>") || !strings.Contains(de, ">Rüsten</h2>") {
		t.Error("deutsche Easy-Seite zeigt Name oder Überschrift nicht")
	}
}

func TestEasyStartFinish(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("sf")
	f.limiterReset(infra)
	a, b := f.button(infra, "Formatwechsel"), f.button(infra, "Reinigung")

	w := f.startEasy(infra, a.ID)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/e/"+infra {
		t.Fatalf("Start: %d %s", w.Code, w.Header().Get("Location"))
	}
	run, err := f.store.Running(f.ctx, infra)
	f.must(err)
	if run == nil || run.ButtonID != a.ID || run.Origin != ruesten.OriginEasy || run.CreatedBy != pdhSystemUserID {
		t.Fatalf("Rüstzeit nach Start: %+v", run)
	}
	html := rzSection(f.easyHTML(infra, "de", ""))
	if !strings.Contains(html, "Rüsten: "+a.Name+" seit "+setupSince(run.StartedAt)) || !strings.Contains(html, "Fertig") {
		t.Errorf("laufendes Rüsten nicht angezeigt:\n%s", html)
	}
	if !strings.Contains(html, `class="rz-btn cur" name="button_id" value="`+a.ID+`"`) {
		t.Error("laufende Rüsttaste nicht hervorgehoben")
	}
	if !strings.Contains(html, `value="`+a.ID+`" aria-current="true" disabled>`) || strings.Contains(html, `value="`+b.ID+`" aria-current`) {
		t.Error("nur die laufende Rüsttaste darf nicht antippbar sein")
	}
	// Dieselbe Taste noch einmal (auch ohne Schaltfläche, z. B. alte Seite): ohne Wirkung.
	if w := f.startEasy(infra, a.ID); !strings.Contains(w.Header().Get("Location"), "setup=same") {
		t.Errorf("gleiche Taste: %s", w.Header().Get("Location"))
	}
	if run1, _ := f.store.Running(f.ctx, infra); run1 == nil || run1.ID != run.ID {
		t.Fatalf("gleiche Taste darf nichts ändern: %+v", run1)
	}

	// Wechsel auf eine andere Taste beendet die laufende
	if w := f.startEasy(infra, b.ID); w.Code != http.StatusSeeOther {
		t.Fatalf("Wechsel: %d", w.Code)
	}
	if run2, _ := f.store.Running(f.ctx, infra); run2 == nil || run2.ButtonID != b.ID || run2.ID == run.ID {
		t.Fatalf("nach Wechsel: %+v", run2)
	}
	if old := f.entry(run.ID); old.Status != ruesten.StatusDone {
		t.Errorf("alte Rüstzeit: %s", old.Status)
	}

	// Fertig mit Notiz
	w = easyDo(f.h.EasySetupFinishWeb, "/e/"+infra+"/setup/finish", infra, url.Values{"note": {"Düse gewechselt"}})
	if w.Code != http.StatusSeeOther || strings.Contains(w.Header().Get("Location"), "setup=") {
		t.Fatalf("Fertig: %d %s", w.Code, w.Header().Get("Location"))
	}
	cur, _ := f.store.Running(f.ctx, infra)
	if cur != nil {
		t.Fatal("Rüstzeit läuft noch")
	}
	if html := rzSection(f.easyHTML(infra, "de", "")); strings.Contains(html, "Fertig") || strings.Contains(html, "seit ") {
		t.Error("nach „Fertig“ steht noch ein laufendes Rüsten da")
	}

	// zweites „Fertig“: freundlicher Hinweis
	w = easyDo(f.h.EasySetupFinishWeb, "/e/"+infra+"/setup/finish", infra, url.Values{})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "setup=none") {
		t.Errorf("zweites Fertig: %s", loc)
	}
	if html := f.easyHTML(infra, "de", "none"); !strings.Contains(html, "Hier läuft gerade kein Rüsten.") {
		t.Error("Hinweis fehlt")
	}

	// eine Rüsttaste einer anderen Anlage wird nicht angenommen
	other := f.infra("fremd")
	ob := f.button(other, "Fremd")
	if w := f.startEasy(infra, ob.ID); !strings.Contains(w.Header().Get("Location"), "setup=gone") {
		t.Errorf("fremde Taste: %s", w.Header().Get("Location"))
	}
}

func TestEasyOverrunHint(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("ue")
	f.limiterReset(infra)
	a := f.button(infra, "Umstellung")
	f.startEasy(infra, a.ID)
	run, _ := f.store.Running(f.ctx, infra)
	f.must2(f.pool.Exec(f.ctx, `UPDATE setup_times SET max_minutes = 10, grace_minutes = 5, started_at = now() - interval '5 minutes' WHERE id = $1`, run.ID))
	if html := f.easyHTML(infra, "de", ""); strings.Contains(html, "Höchstdauer überschritten") {
		t.Error("Hinweis zu früh")
	}
	f.must2(f.pool.Exec(f.ctx, `UPDATE setup_times SET started_at = now() - interval '12 minutes' WHERE id = $1`, run.ID))
	html := f.easyHTML(infra, "de", "")
	if !strings.Contains(html, "Höchstdauer überschritten") || !strings.Contains(html, "Fertig") {
		t.Error("Hinweis „Höchstdauer überschritten“ fehlt oder „Fertig“ verschwunden")
	}
}

func TestEasyFaultState(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("st")
	f.limiterReset(infra)
	f.cleanupFaultFor(infra)
	a := f.button(infra, "Umstellung")
	f.startEasy(infra, a.ID)
	run, _ := f.store.Running(f.ctx, infra)
	if got := f.h.setupPlantState(f.ctx, infra); got != "running" {
		t.Fatalf("Zustand beim Rüsten: %q", got)
	}
	var faultID string
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO faults (title, infrastructure_id, created_by) VALUES ('Störung durch Rüsten: x', $1, $2) RETURNING id::text`,
		infra, pdhSystemUserID).Scan(&faultID))
	f.must2(f.pool.Exec(f.ctx, `UPDATE setup_times SET status = 'fault', ended_at = now() + interval '1 millisecond', fault_id = $2 WHERE id = $1`, run.ID, faultID))

	if got := f.h.setupPlantState(f.ctx, infra); got != "fault" {
		t.Fatalf("Zustand mit offener Störmeldung: %q", got)
	}
	html := rzSection(f.easyHTML(infra, "de", ""))
	if !strings.Contains(html, "Störung durch Rüsten – Instandhaltung ist informiert") {
		t.Error("Hinweis auf die Störung fehlt")
	}
	if strings.Contains(html, "Fertig") || strings.Contains(html, "/setup/finish") {
		t.Error("bei Störung darf es kein „Fertig“ geben")
	}
	if !strings.Contains(html, "/setup/start") {
		t.Error("ein neuer Rüstvorgang muss trotzdem möglich sein")
	}
	// Störmeldung erledigt → Zustand weg
	f.must2(f.pool.Exec(f.ctx, `UPDATE faults SET status = 'resolved' WHERE id = $1`, faultID))
	if got := f.h.setupPlantState(f.ctx, infra); got != "" {
		t.Errorf("Zustand nach Erledigung: %q", got)
	}
	if html := f.easyHTML(infra, "de", ""); strings.Contains(html, "Instandhaltung ist informiert") {
		t.Error("Hinweis bleibt trotz erledigter Störmeldung")
	}
}

func TestSetupLimiterSeparate(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("lim")
	f.limiterReset(infra)
	a, b := f.button(infra, "A"), f.button(infra, "B")
	for i := 0; i < setupLimitMax; i++ {
		btn := a
		if i%2 == 1 {
			btn = b
		}
		if loc := f.startEasy(infra, btn.ID).Header().Get("Location"); strings.Contains(loc, "setup=") {
			t.Fatalf("Vorgang %d abgewiesen: %s", i+1, loc)
		}
	}
	w := f.startEasy(infra, a.ID)
	if !strings.Contains(w.Header().Get("Location"), "setup=rate") {
		t.Fatalf("31. Vorgang nicht abgewiesen: %s", w.Header().Get("Location"))
	}
	if html := f.easyHTML(infra, "de", "rate"); !strings.Contains(html, "Zu viele Vorgänge an dieser Anlage") {
		t.Error("Hinweis zur Grenze fehlt")
	}
	// Die Meldungen der Anlage sind davon unabhängig.
	if !easyLimit.allow("asset:"+infra, 30, time.Hour, time.Now()) {
		t.Error("„Störung +“ an derselben Anlage wird durch das Rüsten gebremst")
	}
}

func TestEasySetupNoNotification(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("nn")
	f.limiterReset(infra)
	f.cleanupFaultFor(infra)
	a := f.button(infra, "Umstellung")
	f.startEasy(infra, a.ID)
	f.startEasy(infra, f.button(infra, "Zweite").ID)
	easyDo(f.h.EasySetupFinishWeb, "/e/"+infra+"/setup/finish", infra, url.Values{})
	var faultsN, ticketsN int
	f.must(f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM faults WHERE infrastructure_id = $1), (SELECT count(*) FROM tickets WHERE infrastructure_id = $1)`, infra).Scan(&faultsN, &ticketsN))
	if faultsN != 0 || ticketsN != 0 {
		t.Errorf("Rüsten im Easy-Modus legt Störung/Ticket an: %d / %d", faultsN, ticketsN)
	}
}

func TestEasyDisabledInfraPageStillWorks(t *testing.T) {
	f := newSetupFx(t)
	infra, uid := f.infra("aus"), f.user()
	f.limiterReset(infra)
	a := f.button(infra, "Umstellung")
	var old *string
	_ = f.pool.QueryRow(f.ctx, `SELECT value FROM app_settings WHERE key = $1`, KeyEasyMode).Scan(&old)
	f.must(f.h.setUpdateSetting(f.ctx, KeyEasyMode, "0"))
	t.Cleanup(func() {
		if old == nil {
			f.pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, KeyEasyMode)
		} else {
			f.h.setUpdateSetting(context.Background(), KeyEasyMode, *old)
		}
	})

	if w := f.startEasy(infra, a.ID); w.Code != http.StatusNotFound {
		t.Errorf("Easy-Modus aus: Start über /e/ = %d", w.Code)
	}
	if run, _ := f.store.Running(f.ctx, infra); run != nil {
		t.Fatal("Easy-Modus aus, aber es läuft eine Rüstzeit")
	}
	w := setupDo(f.h.InfraSetupStartWeb, http.MethodPost, "/infrastructure/"+infra+"/setup/start", infra, uid, url.Values{"button_id": {a.ID}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/a/"+infra {
		t.Fatalf("Anlagenseite: %d %s", w.Code, w.Header().Get("Location"))
	}
	if run, _ := f.store.Running(f.ctx, infra); run == nil {
		t.Error("über die Anlagenseite lässt sich nicht rüsten")
	}
}

// ── Anlagenseite für Angemeldete ─────────────────────────────────────────

func TestInfraPageButtonsForLoggedIn(t *testing.T) {
	f := newSetupFx(t)
	infra, uid := f.infra("an"), f.user()
	f.limiterReset(infra)
	a := f.button(infra, "Umstellung")
	path := "/infrastructure/" + infra + "/setup/"

	w := setupDo(f.h.InfraSetupStartWeb, http.MethodPost, path+"start", infra, uid, url.Values{"button_id": {a.ID}, "back": {"/a/" + infra + "?sub=0"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/a/"+infra+"?sub=0" {
		t.Fatalf("Start: %d %s", w.Code, w.Header().Get("Location"))
	}
	run, _ := f.store.Running(f.ctx, infra)
	if run == nil || run.CreatedBy != uid || run.Origin != ruesten.OriginManual {
		t.Fatalf("Ersteller muss die Person sein: %+v", run)
	}
	bar := f.h.setupBarFor(f.ctx, "de", infra, "/infrastructure/"+infra+"/setup", "/a/"+infra, false, "")
	tmpl := loadTestTemplates(t)
	html := renderPage(t, tmpl, "asset_info", AssetInfoData{Asset: assetInfoView{Kind: "infra", ID: infra, Name: "Presse", TypeLabel: "Gerät"}, Setup: bar, Plant: "running"})
	for _, want := range []string{`action="/infrastructure/` + infra + `/setup/finish"`, `action="/infrastructure/` + infra + `/setup/start"`,
		`name="back" value="/a/` + infra + `"`, "Rüsten: " + a.Name + " seit ", "Fertig"} {
		if !strings.Contains(html, want) {
			t.Errorf("Anlagenseite ohne %q", want)
		}
	}
	if !strings.Contains(html, `b-amber"><i class="ti ti-arrows-exchange-2"></i> Rüsten</span>`) {
		t.Error("Zustand „Rüsten“ in der Kopfzeile fehlt")
	}

	// Fertig durch die Person; Rücksprung auf fremde Adresse wird abgewiesen
	w = setupDo(f.h.InfraSetupFinishWeb, http.MethodPost, path+"finish", infra, uid, url.Values{"note": {"fertig"}, "back": {"https://fremd.example/"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/a/"+infra {
		t.Fatalf("Fertig: %d %s", w.Code, w.Header().Get("Location"))
	}
	if e := f.entry(run.ID); e.Status != ruesten.StatusDone || e.Note != "fertig" {
		t.Errorf("Rüstzeit nach Fertig: %+v", e)
	}

	// ohne „Anlage sehen“ nichts
	h2 := &Handler{db: f.pool, jwtSecret: "t", tmpl: f.h.tmpl, hasPermHook: func(*http.Request, string) bool { return false }}
	w = setupDo(h2.InfraSetupStartWeb, http.MethodPost, path+"start", infra, uid, url.Values{"button_id": {a.ID}})
	if w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
	if r, _ := f.store.Running(f.ctx, infra); r != nil {
		t.Error("ohne Recht wurde gerüstet")
	}
}

func TestInfraDetailShowsSetupState(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := InfraDetailData{Node: InfraNodeView{ID: "n1", Name: "Presse 3"}, Types: infraTypes, SetupState: "fault"}
	html := renderPage(t, tmpl, "infra_detail", d)
	if !strings.Contains(html, `b-red" title="Rüstzeit der Anlage"><i class="ti ti-arrows-exchange-2"></i> Störung durch Rüsten`) {
		t.Error("Detailseite zeigt „Störung durch Rüsten“ nicht")
	}
	d.SetupState = ""
	if strings.Contains(renderPage(t, tmpl, "infra_detail", d), "Rüstzeit der Anlage") {
		t.Error("Kennzeichen ohne Zustand")
	}
}

func TestPlantWatchSetupState(t *testing.T) {
	f := newSetupFx(t)
	infra, quiet := f.infra("pw"), f.infra("pwruhig")
	f.limiterReset(infra)
	f.cleanupFaultFor(infra)
	a := f.button(infra, "Umstellung")
	f.startEasy(infra, a.ID)
	states := f.h.setupPlantStates(f.ctx, []string{infra, quiet})
	if states[infra] != "running" || states[quiet] != "" {
		t.Fatalf("Zustände: %v", states)
	}
	c, err := f.h.tmpl.Clone()
	f.must(err)
	c = bindLang(c, "de")
	var buf bytes.Buffer
	body := watchCards{BoardID: "b", Cards: []watchCard{{ID: infra, Name: "Presse", SetupState: states[infra]}, {ID: quiet, Name: "Ruhig"}}, Total: 2}
	f.must(c.ExecuteTemplate(&buf, "pw-cards", body))
	html := buf.String()
	if n := strings.Count(html, "ti-arrows-exchange-2"); n != 1 || !strings.Contains(html, "</i> Rüsten</a>") {
		t.Errorf("Kennzeichen in der Schnellübersicht: %d mal\n%s", n, html)
	}
}

// ── Überwachung ──────────────────────────────────────────────────────────

// overdueSetup startet eine Rüstzeit, die vor 3 Minuten begann (Höchstdauer 1, Karenz 1).
func (f *setupFx) overdueSetup(infra, buttonID, uid string) *ruesten.Entry {
	f.t.Helper()
	e, err := f.store.Start(f.ctx, infra, buttonID, uid, ruesten.OriginEasy)
	f.must(err)
	f.must2(f.pool.Exec(f.ctx, `UPDATE setup_times SET max_minutes = 1, grace_minutes = 1, started_at = now() - interval '3 minutes' WHERE id = $1`, e.ID))
	return f.entry(e.ID)
}

// smallPoolHandler: Handler und Speicher auf einem Pool mit nur zwei Verbindungen –
// Überwachung (Transaktion) und Störmeldung brauchen je eine; das darf sich nicht blockieren.
func (f *setupFx) smallPoolHandler() (*Handler, *ruesten.Store) {
	f.t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("PDH_TEST_DSN"))
	f.must(err)
	cfg.MaxConns = 2
	p, err := pgxpool.NewWithConfig(f.ctx, cfg)
	f.must(err)
	f.t.Cleanup(p.Close)
	h := &Handler{db: p, jwtSecret: "t", tmpl: f.h.tmpl, hasPermHook: f.h.hasPermHook, faults: faults.NewService(faults.NewRepository(p), nil)}
	return h, ruesten.NewStore(p)
}

func TestMonitorWiringCreatesFault(t *testing.T) {
	f := newSetupFx(t)
	infra, uid := f.infra("mon"), f.user()
	f.cleanupFaultFor(infra)
	a := f.button(infra, "Formatwechsel")
	e := f.overdueSetup(infra, a.ID, uid)

	h, st := f.smallPoolHandler()
	m := &ruesten.Monitor{Store: st, CreateFault: h.createSetupFault, SystemUser: pdhSystemUserID}
	done := make(chan struct{})
	var n int
	var err error
	go func() { n, err = m.Pass(f.ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Überwachung hängt (Verbindungen blockieren sich)")
	}
	f.must(err)
	if n != 1 {
		t.Fatalf("Störmeldungen: %d", n)
	}
	got := f.entry(e.ID)
	if got.Status != ruesten.StatusFault || got.FaultID == nil || got.EndedAt == nil {
		t.Fatalf("Rüstzeit: %+v", got)
	}
	var title, desc, sev, createdBy string
	var stopped bool
	f.must(f.pool.QueryRow(f.ctx, `SELECT title, description, severity::text, plant_stopped, created_by::text FROM faults WHERE id = $1`, *got.FaultID).
		Scan(&title, &desc, &sev, &stopped, &createdBy))
	if title != "Störung durch Rüsten: "+a.Name || sev != "high" || !stopped || createdBy != pdhSystemUserID {
		t.Errorf("Störmeldung: %q %s %v %s", title, sev, stopped, createdBy)
	}
	for _, want := range []string{"Höchstdauer: 1 Minuten", "Karenzzeit: 1 Minuten", "Gestartet von: Rita Rüst", "/setup/" + e.ID, ruesten.Number(e.Number)} {
		if !strings.Contains(desc, want) {
			t.Errorf("Beschreibung ohne %q:\n%s", want, desc)
		}
	}
	var cats, hist int
	f.must(f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM record_categories rc JOIN categories c ON c.id = rc.category_id
			WHERE rc.module = 'fault' AND rc.record_id = $1 AND c.name = 'Störung durch Rüsten'),
		(SELECT count(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1 AND action = 'create' AND message LIKE '%Rüstzeit %')`, *got.FaultID).Scan(&cats, &hist))
	if cats != 1 || hist != 1 {
		t.Errorf("Kategorie %d, Verlauf an der Störmeldung %d", cats, hist)
	}
	var setupHist int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'setup' AND ref_id = $1 AND action = 'fault'`, e.ID).Scan(&setupHist))
	if setupHist != 1 {
		t.Errorf("Verlauf an der Rüstzeit: %d", setupHist)
	}
	// zweiter Durchlauf: nichts mehr
	if n, err := m.Pass(f.ctx); err != nil || n != 0 {
		t.Errorf("zweiter Durchlauf: %d %v", n, err)
	}
}

// Auflage: Die Störmeldung wird nur einmal angelegt, auch wenn der erste Versuch
// hinter dem Anlegen scheitert (Rüstzeit bleibt dann „läuft“) – der nächste
// Durchlauf nutzt die vorhandene Störmeldung.
func TestSetupFaultIdempotent(t *testing.T) {
	f := newSetupFx(t)
	infra, uid := f.infra("idem"), f.user()
	f.cleanupFaultFor(infra)
	a := f.button(infra, "Umstellung")
	e := f.overdueSetup(infra, a.ID, uid)
	h, st := f.smallPoolHandler()

	first, err := h.createSetupFault(f.ctx, *e)
	f.must(err)
	again, err := h.createSetupFault(f.ctx, *e)
	f.must(err)
	if first == "" || again != first {
		t.Fatalf("zweiter Aufruf: %q statt %q", again, first)
	}
	// Die Rüstzeit läuft noch (Fehler nach dem Anlegen) → der Durchlauf übernimmt die vorhandene.
	m := &ruesten.Monitor{Store: st, CreateFault: h.createSetupFault, SystemUser: pdhSystemUserID}
	if n, err := m.Pass(f.ctx); err != nil || n != 1 {
		t.Fatalf("Durchlauf: %d %v", n, err)
	}
	var faultsN, histN int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM faults WHERE infrastructure_id = $1`, infra).Scan(&faultsN))
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM record_history WHERE ref_type = 'fault' AND ref_id = $1 AND action = 'create'`, first).Scan(&histN))
	if got := f.entry(e.ID); faultsN != 1 || got.FaultID == nil || *got.FaultID != first || histN != 1 {
		t.Errorf("Störmeldungen %d, Verknüpfung %v, Verlaufseinträge %d", faultsN, got.FaultID, histN)
	}
}

func TestStartSetupMonitorWithoutServices(t *testing.T) {
	(&Handler{}).StartSetupMonitor(context.Background()) // ohne Datenbank und Störungsdienst: kein Absturz
	h := &Handler{}
	if _, err := h.createSetupFault(context.Background(), ruesten.Entry{}); err == nil {
		t.Error("ohne Störungsdienst muss ein Fehler kommen")
	}
}

func TestSetupBackPath(t *testing.T) {
	for in, want := range map[string]string{
		"/a/x?sub=0":              "/a/x?sub=0",
		"/infrastructure/x?tab=1": "/infrastructure/x?tab=1",
		"https://fremd.example/":  "/a/fb",
		"//fremd.example/":        "/a/fb",
		"/a/x\r\nSet-Cookie: a=b": "/a/fb",
		"/login":                  "/a/fb",
		"":                        "/a/fb",
	} {
		if got := setupBackPath(in, "/a/fb"); got != want {
			t.Errorf("%q -> %q, erwartet %q", in, got, want)
		}
	}
}

// Die Störmeldung wird auch dann wiedergefunden, wenn jemand die Markerzeile aus
// der Beschreibung entfernt hat (Schlüssel im Verlauf, nicht editierbar).
func TestSetupFaultFoundWithoutMarker(t *testing.T) {
	f := newSetupFx(t)
	infra, uid := f.infra("key"), f.user()
	f.cleanupFaultFor(infra)
	a := f.button(infra, "Umstellung")
	e := f.overdueSetup(infra, a.ID, uid)
	h, st := f.smallPoolHandler()

	first, err := h.createSetupFault(f.ctx, *e)
	f.must(err)
	f.must2(f.pool.Exec(f.ctx, `UPDATE faults SET description = 'ohne Marker' WHERE id = $1`, first))
	again, err := h.createSetupFault(f.ctx, *e)
	f.must(err)
	if again != first {
		t.Fatalf("zweite Störmeldung trotz Schlüssel: %q statt %q", again, first)
	}
	m := &ruesten.Monitor{Store: st, CreateFault: h.createSetupFault, SystemUser: pdhSystemUserID}
	if n, err := m.Pass(f.ctx); err != nil || n != 1 {
		t.Fatalf("Durchlauf: %d %v", n, err)
	}
	var faultsN int
	f.must(f.pool.QueryRow(f.ctx, `SELECT count(*) FROM faults WHERE infrastructure_id = $1`, infra).Scan(&faultsN))
	if got := f.entry(e.ID); faultsN != 1 || got.FaultID == nil || *got.FaultID != first {
		t.Errorf("Störmeldungen %d, Verknüpfung %v", faultsN, got.FaultID)
	}
}

// Läuft während einer offenen Störung eine neue Rüstzeit, entscheidet die neueste:
// Kopfzeile, Schnellübersicht und Baustein sagen alle „Rüsten“.
func TestSetupNewestDecidesState(t *testing.T) {
	f := newSetupFx(t)
	infra := f.infra("neu")
	f.limiterReset(infra)
	f.cleanupFaultFor(infra)
	a, b := f.button(infra, "Erste"), f.button(infra, "Zweite")
	f.startEasy(infra, a.ID)
	old, _ := f.store.Running(f.ctx, infra)
	var faultID string
	f.must(f.pool.QueryRow(f.ctx, `INSERT INTO faults (title, infrastructure_id, created_by) VALUES ('Störung durch Rüsten: x', $1, $2) RETURNING id::text`,
		infra, pdhSystemUserID).Scan(&faultID))
	f.must2(f.pool.Exec(f.ctx, `UPDATE setup_times SET status = 'fault', ended_at = now(), fault_id = $2, started_at = now() - interval '2 hours' WHERE id = $1`, old.ID, faultID))
	if got := f.h.setupPlantState(f.ctx, infra); got != "fault" {
		t.Fatalf("nur die Störung: %q", got)
	}
	f.startEasy(infra, b.ID) // neue Rüstzeit bei offener Störmeldung
	if got := f.h.setupPlantState(f.ctx, infra); got != "running" {
		t.Fatalf("neueste Rüstzeit läuft, Zustand: %q", got)
	}
	bar := f.h.setupBarFor(f.ctx, "de", infra, "/e/"+infra+"/setup", "", false, "")
	if !bar.Running || bar.Fault {
		t.Errorf("Baustein widerspricht der Kopfzeile: %+v", bar)
	}
}
