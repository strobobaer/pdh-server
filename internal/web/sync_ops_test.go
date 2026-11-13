package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/extsync"
)

// ── Attrappe der Fremddatenbank für Test und Lauf ────────────────────────

// fakeSyncForeign liefert für jede Abfrage dieselben Zeilen der Meldungstabelle
// und zählt Schreibbefehle (die im Test nie vorkommen dürfen).
type fakeSyncForeign struct {
	tables []*extsync.TableInfo
	rows   []map[string]any
	execs  int
}

func (f *fakeSyncForeign) Describe(_ context.Context, schema, table string) (*extsync.TableInfo, error) {
	for _, t := range f.tables {
		if t.Schema == schema && t.Name == table {
			return t, nil
		}
	}
	return nil, extsync.ErrNotFound
}

func (f *fakeSyncForeign) Query(context.Context, extsync.Stmt) ([]map[string]any, error) {
	return f.rows, nil
}

func (f *fakeSyncForeign) Exec(context.Context, extsync.Stmt) (int64, error) {
	f.execs++
	return 0, fmt.Errorf("attrappe: Schreiben nicht vorgesehen")
}

// syncOpsHandler: wie syncTestHandler, zusätzlich mit Fremddatenbank für Test
// und Lauf. triggers sind die Auslöser der Meldungstabelle.
func syncOpsHandler(t *testing.T, allowWrite *bool, triggers ...string) (*Handler, string, *fakeSyncForeign) {
	t.Helper()
	h, connID := syncTestHandler(t, true)
	dialect, _ := extsync.DialectFor("mssql")
	tables := syncTestTables()
	tables[0].Triggers = triggers
	ff := &fakeSyncForeign{tables: tables, rows: []map[string]any{
		{"Nr": int64(17), "Titel": "Lager heiß", "Prio": "hoch", "Zustand": "offen", "RV": []byte{0, 0, 0, 0, 0, 0, 0, 9}},
	}}
	cfg := func() map[string]string {
		if *allowWrite {
			return map[string]string{"allow_write": "true"}
		}
		return map[string]string{"allow_write": "false"}
	}
	h.syncReaderHook = func(context.Context, string) (syncReader, extsync.Dialect, map[string]string, error) {
		return &fakeSyncReader{tables: tables}, dialect, cfg(), nil
	}
	h.syncForeignHook = func(context.Context, string) (extsync.Foreign, extsync.Dialect, map[string]string, error) {
		return ff, dialect, cfg(), nil
	}
	return h, connID, ff
}

// syncOpsProfile legt ein Profil mit Quelle und Zuordnung an (dir = Richtung des Titels).
func syncOpsProfile(t *testing.T, h *Handler, connID, dir string) string {
	t.Helper()
	id := syncWithSource(t, h, connID)
	loc := syncLocation(t, syncDo(h.SyncMapsWeb, "POST", "/sync/"+id+"/maps-web", id,
		url.Values{"col_title": {"Titel"}, "dir_title": {dir}}, false))
	if !strings.Contains(loc, "Zuordnungen gespeichert") {
		t.Fatalf("Zuordnungen: %s", loc)
	}
	return id
}

func syncMark(t *testing.T, h *Handler, id string, dry, write bool) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `UPDATE sync_profiles SET state = 'getestet',
		test_dry_at = CASE WHEN $2 THEN now() END, test_write_at = CASE WHEN $3 THEN now() END WHERE id = $1`, id, dry, write); err != nil {
		t.Fatal(err)
	}
}

func syncPost(t *testing.T, fn http.HandlerFunc, path, id string, form url.Values) string {
	t.Helper()
	return syncLocation(t, syncDo(fn, "POST", path, id, form, false))
}

// ── Scharf schalten ──────────────────────────────────────────────────────

func TestArmRequiresTests(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "beides")
	arm := func() string {
		return syncPost(t, h.SyncArmWeb, "/sync/"+id+"/arm-web", id, url.Values{"confirm": {"1"}})
	}
	if loc := arm(); !strings.Contains(loc, "Erst den Trockenlauf bestehen") || !strings.Contains(loc, "tab=ops") {
		t.Fatalf("ohne Trockenlauf: %s", loc)
	}
	syncMark(t, h, id, true, false)
	if loc := arm(); !strings.Contains(loc, "Erst den Schreibversuch bestehen") {
		t.Fatalf("ohne Schreibversuch: %s", loc)
	}
	if p := syncGet(t, h, id); p.State == extsync.StateArmed || p.NotifyFrom != nil {
		t.Fatalf("trotz fehlender Tests scharf: %+v", p)
	}
	syncMark(t, h, id, true, true)
	if loc := arm(); !strings.Contains(loc, "scharf geschaltet") {
		t.Fatalf("mit beiden Stufen: %s", loc)
	}
	p := syncGet(t, h, id)
	if p.State != extsync.StateArmed || p.NotifyFrom == nil {
		t.Fatalf("nach dem Scharfschalten: %+v", p)
	}
	// Anhalten und Fortsetzen; der Stichtag bleibt.
	first := *p.NotifyFrom
	if loc := syncPost(t, h.SyncPauseWeb, "/sync/"+id+"/pause-web", id, url.Values{}); !strings.Contains(loc, "angehalten") {
		t.Fatalf("anhalten: %s", loc)
	}
	if p := syncGet(t, h, id); p.State != extsync.StateHalted || p.StateNote != "Von Hand angehalten" {
		t.Fatalf("angehalten: %+v", p)
	}
	if loc := syncPost(t, h.SyncPauseWeb, "/sync/"+id+"/pause-web", id, url.Values{}); !strings.Contains(loc, "Nur ein scharfes Profil") {
		t.Fatalf("zweimal anhalten: %s", loc)
	}
	// Fortsetzen fragt nicht noch einmal nach (kein erster Lauf mehr).
	if loc := syncPost(t, h.SyncArmWeb, "/sync/"+id+"/arm-web", id, url.Values{}); !strings.Contains(loc, "scharf geschaltet") {
		t.Fatalf("fortsetzen: %s", loc)
	}
	if p := syncGet(t, h, id); p.State != extsync.StateArmed || !p.NotifyFrom.Equal(first) {
		t.Fatalf("nach dem Fortsetzen: %+v", p)
	}
}

// Ohne hinausgehende Zuordnung genügt der Trockenlauf; er läuft über die
// Oberfläche, schreibt nirgends und macht aus dem Entwurf „getestet“.
func TestArmSkipsWriteTestWithoutOutbound(t *testing.T) {
	allow := true
	h, connID, ff := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "herein")
	res, err := h.syncRunDry(context.Background(), syncGet(t, h, id), "", nil)
	if err != nil || res == nil || !res.Passed || res.New != 1 || res.Failed != 0 || res.NeedsOut {
		t.Fatalf("Trockenlauf: %+v %v", res, err)
	}
	if len(res.Previews) != 1 || res.Previews[0].Key != "17" || res.Previews[0].Kind != "neu" || !strings.Contains(res.Previews[0].Values, "Titel: Lager heiß") {
		t.Fatalf("Vorschau: %+v", res.Previews)
	}
	if ff.execs != 0 {
		t.Fatal("der Trockenlauf hat geschrieben")
	}
	var links, tickets int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM sync_links WHERE profile_id = $1`, id).Scan(&links)
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM tickets WHERE title = 'Lager heiß'`).Scan(&tickets)
	if links != 0 || tickets != 0 {
		t.Fatalf("der Trockenlauf hat im PDH etwas angelegt: %d Verknüpfungen, %d Tickets", links, tickets)
	}
	p := syncGet(t, h, id)
	if p.State != extsync.StateTested || p.TestDryAt == nil || p.TestWriteAt != nil {
		t.Fatalf("nach dem Trockenlauf: %+v", p)
	}
	if loc := syncPost(t, h.SyncArmWeb, "/sync/"+id+"/arm-web", id, url.Values{"confirm": {"1"}}); !strings.Contains(loc, "scharf geschaltet") {
		t.Fatalf("scharf schalten ohne Stufe 2: %s", loc)
	}
	// Ein Schreibversuch ist für dieses Profil nicht vorgesehen.
	if _, _, _, err := h.syncRunWrite(context.Background(), p, `["17"]`, nil, true); err == nil || err.Error() != extsync.ProbeNothingToWrite {
		t.Fatalf("Schreibversuch ohne hinausgehende Zuordnung: %v", err)
	}
}

func TestArmRefusedWithoutAllowWrite(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "beides")
	syncMark(t, h, id, true, true)
	allow = false // Schalter an der Verbindung nachträglich aus
	loc := syncPost(t, h.SyncArmWeb, "/sync/"+id+"/arm-web", id, url.Values{"confirm": {"1"}})
	if !strings.Contains(loc, "Schreiben ist an der Verbindung nicht erlaubt") {
		t.Fatalf("ohne Schreiben erlaubt: %s", loc)
	}
	if p := syncGet(t, h, id); p.State == extsync.StateArmed {
		t.Fatal("trotz gesperrtem Schreiben scharf")
	}
	if _, _, _, err := h.syncRunWrite(context.Background(), syncGet(t, h, id), `["17"]`, nil, true); err != extsync.ErrWriteNotAllowed {
		t.Fatalf("Schreibversuch ohne Schreiben erlaubt: %v", err)
	}
}

func TestArmNeedsRunPermission(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	var asked []string
	// Bearbeiten erlaubt, Betrieb nicht.
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return p != permSyncRun }}
	for name, fn := range map[string]http.HandlerFunc{
		"arm": h.SyncArmWeb, "pause": h.SyncPauseWeb, "run": h.SyncRunWeb, "retry": h.SyncRetryWeb,
	} {
		asked = nil
		if w := syncDo(fn, "POST", "/sync/"+id+"/"+name+"-web", id, url.Values{}, false); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne sync.run: Status %d", name, w.Code)
		}
		if len(asked) != 1 || asked[0] != permSyncRun {
			t.Errorf("%s: geprüft %v", name, asked)
		}
	}
	// Testen und Takt brauchen sync.edit, die Läufe sync.view.
	h.hasPermHook = func(*http.Request, string) bool { return false }
	for name, fn := range map[string]http.HandlerFunc{"test-dry": h.SyncTestDryWeb, "test-write": h.SyncTestWriteWeb, "interval": h.SyncIntervalWeb} {
		if w := syncDo(fn, "POST", "/sync/"+id+"/"+name+"-web", id, url.Values{}, false); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", name, w.Code)
		}
	}
	if w := syncDo(h.SyncRunsFragment, "GET", "/sync/"+id+"/runs", id, nil, true); w.Code != http.StatusForbidden {
		t.Errorf("Läufe ohne sync.view: Status %d", w.Code)
	}
	for _, c := range []struct{ path, perm string }{
		{"/sync/" + id + "/arm-web", permSyncRun}, {"/sync/" + id + "/pause-web", permSyncRun}, {"/sync/" + id + "/run-web", permSyncRun},
		{"/sync/" + id + "/outbox/42/retry-web", permSyncRun},
		{"/sync/" + id + "/test-dry-web", permSyncEdit}, {"/sync/" + id + "/test-write-web", permSyncEdit}, {"/sync/" + id + "/interval-web", permSyncEdit},
	} {
		_, perm, ref, gotID, hit := matchGate("POST", c.path)
		if !hit || perm != c.perm || ref != syncRef || gotID != id {
			t.Errorf("Rechte-Tor %s: Treffer=%v Recht=%q Typ=%q ID=%q", c.path, hit, perm, ref, gotID)
		}
	}
	if _, _, _, _, hit := matchGate("POST", "/sync/"+id+"/outbox/x/retry-web"); hit {
		t.Error("Rechte-Tor trifft einen Eintrag ohne Nummer")
	}
}

// ── Schreibversuch ───────────────────────────────────────────────────────

func TestWriteTestShowsTriggersFirst(t *testing.T) {
	allow := true
	h, connID, ff := syncOpsHandler(t, &allow, "trg_Meldungen_Mail", "trg_Meldungen_Log")
	id := syncOpsProfile(t, h, connID, "beides")
	p := syncGet(t, h, id)
	ctx := context.Background()

	triggers, ask, res, err := h.syncRunWrite(ctx, p, `["17"]`, map[string]string{"title": "Probe"}, false)
	if err != nil || !ask || res != nil || len(triggers) != 2 || triggers[0] != "trg_Meldungen_Mail" {
		t.Fatalf("erst die Auslöser zeigen: %v ask=%v res=%+v %v", triggers, ask, res, err)
	}
	if ff.execs != 0 {
		t.Fatal("vor der Bestätigung wurde geschrieben")
	}
	if p := syncGet(t, h, id); p.TestWriteAt != nil {
		t.Fatal("Stufe 2 gilt ohne Versuch als bestanden")
	}
	// Die Seite zeigt die Namen und den Knopf „Trotzdem versuchen“ mit Meldung und Probewerten.
	maps, _ := h.syncStore().ListMaps(ctx, id)
	tv := &SyncTestView{NeedsWrite: true, AllowWrite: true, Key: `["17"]`, AskTriggers: true, Triggers: triggers,
		OutFields: syncOutFields(p, maps, map[string]string{"title": "Probe"})}
	out := renderPage(t, loadTestTemplates(t), "sync_profile", SyncProfileData{P: p, Tab: "test", CanEdit: true, Test: tv})
	for _, want := range []string{"trg_Meldungen_Mail", "trg_Meldungen_Log", "Trotzdem versuchen", `name="confirm" value="1"`,
		`name="sample_title" value="Probe"`, `name="key" value="[&#34;17&#34;]"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Rückfrage ohne %q", want)
		}
	}
	if strings.Contains(out, "Schreibversuch starten") {
		t.Error("neben der Rückfrage steht der gewöhnliche Startknopf")
	}
	// Eine Meldung, die nicht zur Auswahl steht, wird nie angeschrieben.
	if _, _, _, err := h.syncRunWrite(ctx, p, `["999"]`, nil, true); err == nil || !strings.Contains(err.Error(), "steht nicht zur Auswahl") {
		t.Fatalf("fremder Schlüssel: %v", err)
	}
	if _, _, _, err := h.syncRunWrite(ctx, p, "", nil, true); err == nil {
		t.Fatal("Schreibversuch ohne Meldung angenommen")
	}
	// Bestätigt: der Versuch läuft (hier ohne echte Datenbank: scheitert sauber, nichts gilt als bestanden).
	_, ask, res, err = h.syncRunWrite(ctx, p, `["17"]`, nil, true)
	if err != nil || ask || res == nil || res.OK || res.Error == "" {
		t.Fatalf("bestätigter Versuch: ask=%v res=%+v %v", ask, res, err)
	}
	if p := syncGet(t, h, id); p.TestWriteAt != nil {
		t.Fatal("gescheiterter Schreibversuch gilt als bestanden")
	}
}

// ── Betrieb ──────────────────────────────────────────────────────────────

// syncLinked legt eine Verknüpfung des Profils zu einem (gedachten) Datensatz an.
func syncLinked(t *testing.T, h *Handler, profileID, key, refID string) *extsync.Link {
	t.Helper()
	ctx := context.Background()
	l, _, err := h.syncStore().ReserveLink(ctx, profileID, key, extsync.RefTicket)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.syncStore().AttachLink(ctx, l.ID, refID, "", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRetryMovesItemToNow(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "beides")
	other := syncOpsProfile(t, h, connID, "beides")
	ctx := context.Background()
	store := h.syncStore()
	l := syncLinked(t, h, id, `["17"]`, "22222222-2222-4222-8222-222222222222")
	if err := store.Enqueue(ctx, l.ID, extsync.KindUpdate, map[string]any{"fields": []string{"title"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.OpenOutbox(ctx, id, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Warteliste: %+v %v", entries, err)
	}
	n := entries[0].ID
	if err := store.OutboxFailed(ctx, n, "Sperre im Fremdsystem", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	retry := func(profile string, item int64) string {
		req := httptest.NewRequest("POST", fmt.Sprintf("/sync/%s/outbox/%d/retry-web", profile, item), nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", profile)
		rc.URLParams.Add("n", fmt.Sprint(item))
		w := httptest.NewRecorder()
		h.SyncRetryWeb(w, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc)))
		return syncLocation(t, w)
	}
	due := func() bool {
		var ok bool
		_ = h.db.QueryRow(ctx, `SELECT next_try_at <= now() FROM sync_outbox WHERE id = $1`, n).Scan(&ok)
		return ok
	}
	if due() {
		t.Fatal("Ausgangslage: Eintrag ist schon fällig")
	}
	// Über ein anderes Profil lässt sich der Eintrag nicht anstoßen.
	if loc := retry(other, n); !strings.Contains(loc, "nicht gefunden") || due() {
		t.Fatalf("fremdes Profil: %s (fällig=%v)", loc, due())
	}
	if loc := retry(id, n); !strings.Contains(loc, "sofort erneut versucht") || !due() {
		t.Fatalf("erneut versuchen: %s (fällig=%v)", loc, due())
	}
	// Der Fehlerzähler bleibt (es war ein Fehlversuch); die Seite zeigt den Eintrag mit Knopf.
	p := syncGet(t, h, id)
	maps, _ := store.ListMaps(ctx, id)
	ov := h.syncOpsView(ctx, p, maps, true)
	if ov.Open != 1 || len(ov.Entries) != 1 || ov.Entries[0].Attempts != 1 || ov.Entries[0].Key != "17" || ov.Entries[0].RefPath != "/tickets/22222222-2222-4222-8222-222222222222" {
		t.Fatalf("Betrieb: %+v", ov)
	}
	out := renderPage(t, loadTestTemplates(t), "sync_profile", SyncProfileData{P: p, Tab: "ops", CanEdit: true, CanRun: true, Ops: ov, StateLabel: "Entwurf"})
	for _, want := range []string{"Sperre im Fremdsystem", fmt.Sprintf("/outbox/%d/retry-web", n), "Erneut versuchen", "1 Fehlversuch(e)"} {
		if !strings.Contains(out, want) {
			t.Errorf("Betrieb ohne %q", want)
		}
	}
}

func TestSyncIntervalAndOpsView(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "herein")
	for in, want := range map[string]int{"1": 5, "12": 12, "999": 30, "abc": 5} {
		if loc := syncPost(t, h.SyncIntervalWeb, "/sync/"+id+"/interval-web", id, url.Values{"interval_minutes": {in}}); !strings.Contains(loc, "Takt gespeichert") {
			t.Fatalf("Takt %s: %s", in, loc)
		}
		if p := syncGet(t, h, id); p.IntervalMinutes != want {
			t.Errorf("Takt %s: %d, erwartet %d", in, p.IntervalMinutes, want)
		}
	}
	// Der Takt ändert den Zustand nicht.
	syncMark(t, h, id, true, false)
	syncPost(t, h.SyncIntervalWeb, "/sync/"+id+"/interval-web", id, url.Values{"interval_minutes": {"10"}})
	p := syncGet(t, h, id)
	if p.State != extsync.StateTested || p.TestDryAt == nil {
		t.Fatalf("Takt hat den Test verworfen: %+v", p)
	}
	ctx := context.Background()
	maps, _ := h.syncStore().ListMaps(ctx, id)
	ov := h.syncOpsView(ctx, p, maps, true)
	if ov.Effective != "alle 10 Minuten" || ov.ArmBlock != "" || !ov.FirstArm || ov.RunnerOn || ov.Armed {
		t.Fatalf("Betrieb: %+v", ov)
	}
	// Ohne Taktgeber (Spiegel, Test) geht „Jetzt abgleichen“ nicht.
	if loc := syncPost(t, h.SyncRunWeb, "/sync/"+id+"/run-web", id, url.Values{}); !strings.Contains(loc, "läuft der Abgleich nicht") {
		t.Fatalf("Lauf ohne Taktgeber: %s", loc)
	}
	// Läufe als Baustein.
	if err := h.syncStore().NoteSkip(ctx, id, extsync.DirIn, extsync.SkippedOverload+" (Prozessor)"); err != nil {
		t.Fatal(err)
	}
	w := syncDo(h.SyncRunsFragment, "GET", "/sync/"+id+"/runs", id, nil, true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Warteposition wegen hoher Serverlast (Prozessor)") || !strings.Contains(w.Body.String(), "herein") {
		t.Fatalf("Läufe: %d %s", w.Code, w.Body.String())
	}
}

// ── Herkunft am Datensatz ────────────────────────────────────────────────

func TestOriginShownOnTicket(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "beides")
	const ref = "33333333-3333-4333-8333-333333333333"
	l := syncLinked(t, h, id, `["A","4711"]`, ref)
	req := httptest.NewRequest("GET", "/tickets/"+ref, nil)
	ctx := context.Background()

	v := h.syncOrigin(req, extsync.RefTicket, ref)
	if v == nil || v.ProfileID != id || v.Profile != "zz-sync-seite" || v.Key != "A / 4711" || v.Bad || !strings.Contains(v.Status, "Abgleich ruht") {
		t.Fatalf("Herkunft: %+v", v)
	}
	if h.syncOrigin(req, extsync.RefFault, ref) != nil || h.syncOrigin(req, extsync.RefTicket, "44444444-4444-4444-8444-444444444444") != nil {
		t.Fatal("Herkunft an einem nicht verknüpften Datensatz")
	}
	// Offene Änderung, dann Fehler.
	if err := h.syncStore().Enqueue(ctx, l.ID, extsync.KindUpdate, map[string]any{"fields": []string{"title"}}); err != nil {
		t.Fatal(err)
	}
	if v := h.syncOrigin(req, extsync.RefTicket, ref); v == nil || v.Open != 1 || !strings.Contains(v.Status, "1 Änderung(en) warten") {
		t.Fatalf("offene Änderung: %+v", v)
	}
	if err := h.syncStore().SetLinkError(ctx, l.ID, extsync.LinkErrForeignGone); err != nil {
		t.Fatal(err)
	}
	v = h.syncOrigin(req, extsync.RefTicket, ref)
	if v == nil || !v.Bad || v.Status != "Fehler: "+extsync.LinkErrForeignGone {
		t.Fatalf("Fehler: %+v", v)
	}
	// Ticket- und Störungsseite binden den Baustein ein; er zeigt Profil, Nummer und Stand.
	tmpl := loadTestTemplates(t)
	for _, page := range []struct {
		name string
		data any
	}{
		{"ticket_detail", TicketDetailData{SyncOrigin: v}},
		{"fault_detail", FaultDetailData{SyncOrigin: v}},
	} {
		out := renderPage(t, tmpl, page.name, page.data)
		for _, want := range []string{"Aus dem Fremdsystem", "zz-sync-seite", "A / 4711", "Fehler: " + extsync.LinkErrForeignGone, `href="/sync/` + id + `?tab=ops"`} {
			if !strings.Contains(out, want) {
				t.Errorf("%s ohne %q", page.name, want)
			}
		}
	}
}

func TestOriginHiddenWithoutPermission(t *testing.T) {
	allow := true
	h, connID, _ := syncOpsHandler(t, &allow)
	id := syncOpsProfile(t, h, connID, "herein")
	const ref = "55555555-5555-4555-8555-555555555555"
	syncLinked(t, h, id, `["17"]`, ref)
	req := httptest.NewRequest("GET", "/tickets/"+ref, nil)
	if h.syncOrigin(req, extsync.RefTicket, ref) == nil {
		t.Fatal("Ausgangslage: mit Recht muss die Herkunft sichtbar sein")
	}
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSyncView }
	if v := h.syncOrigin(req, extsync.RefTicket, ref); v != nil {
		t.Fatalf("Herkunft ohne sync.view sichtbar: %+v", v)
	}
	// Ohne Herkunft bleibt die Seite ohne den Baustein.
	tmpl := loadTestTemplates(t)
	for _, page := range []struct {
		name string
		data any
	}{{"ticket_detail", TicketDetailData{}}, {"fault_detail", FaultDetailData{}}} {
		if out := renderPage(t, tmpl, page.name, page.data); strings.Contains(out, "Aus dem Fremdsystem") {
			t.Errorf("%s zeigt eine Herkunft ohne Daten", page.name)
		}
	}
}

// ── Vorlagen der Schritte Test und Betrieb ───────────────────────────────

func TestSyncTestAndOpsTemplatesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	p := &extsync.Profile{ID: "p1", Name: "ERP-Meldungen", TargetType: extsync.RefTicket, SourceSchema: "dbo", SourceTable: "Meldungen",
		KeyColumns: []string{"Nr"}, MaxNew: 200, IntervalMinutes: 5, StateNote: "Zu viele neue Meldungen"}
	render := func(d SyncProfileData) string {
		d.P, d.StateLabel, d.StateColor, d.TargetLabel = p, "Getestet", "var(--accent)", "Ticket"
		return renderPage(t, tmpl, "sync_profile", d)
	}
	rows := []syncProbeOption{{Key: `["17"]`, Label: "17 – Lager heiß"}}
	fields := []syncSampleField{{Key: "title", Label: "Titel"}, {Key: "priority", Label: "Priorität", Options: []syncOption{{Value: "high", Label: "Hoch"}}}}

	// Test: leer (nichts schreibt), mit Auswahl, mit Ergebnissen.
	out := render(SyncProfileData{Tab: "test", CanEdit: true, Test: &SyncTestView{}})
	for _, want := range []string{"Trockenlauf starten", "Stufe 2 ist nicht nötig", "noch nicht bestanden", `href="?tab=ops"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Test (leer) ohne %q", want)
		}
	}
	out = render(SyncProfileData{Tab: "test", CanEdit: true, Test: &SyncTestView{NeedsWrite: true, AllowWrite: true, Missing: 1,
		DryAt: "13.11.2026 10:00", Rows: rows, OutFields: fields, Key: `["17"]`,
		Dry: &SyncDryView{Passed: false, Error: "Fremddatenbank lesen: Zeitüberschreitung", New: 3, Backfilled: 2, Archived: 1, Failed: 1, Truncated: true, NeedsOut: true,
			Previews: []syncPreviewRow{{Key: "17", Kind: "neu", Values: "Titel: Lager heiß", Errors: []string{"Priorität: Wert dringend ist nicht zugeordnet"}}},
			OutSQL:   "UPDATE [dbo].[Meldungen] SET [Titel] = @p1 WHERE [Nr] = @p2", OutParams: []string{"Probe", "17"}, OutValues: "Titel: Probe",
			OutTruncated: []string{"Titel"}, OutCommentSQL: "INSERT INTO [dbo].[MeldungKommentare] ([MeldungNr], [Text]) VALUES (@p1, @p2)", OutErrors: []string{"Titel: zu lang"}},
		Write: &SyncWriteView{OK: true, RolledBack: true, Statement: "UPDATE [dbo].[Meldungen] SET [Titel] = @p1 WHERE [Nr] = @p2", Params: []string{"Probe", "17"},
			ReadBack: "Titel: Probe", Warnings: []string{extsync.WarnCounters}, CommentStatement: "INSERT INTO [dbo].[MeldungKommentare]"}}})
	for _, want := range []string{
		"Zuordnung für ein Pflichtfeld", "bestanden am 13.11.2026 10:00", "17 – Lager heiß", "Probewert Titel", "Probewert Priorität",
		"Trockenlauf nicht bestanden", "Fremddatenbank lesen: Zeitüberschreitung", "mindestens 3", "davon 1 gleich archiviert", "ist nicht zugeordnet",
		"UPDATE [dbo].[Meldungen] SET [Titel] = @p1", "nur gezeigt, nicht gesendet", "Gekürzt auf die Länge der Spalte: Titel", "Titel: zu lang",
		"Schreibversuch bestanden – alles zurückgenommen", "In der Transaktion zurückgelesen: Titel: Probe", extsync.WarnCounters, "Schreibversuch starten",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Test (gefüllt) ohne %q", want)
		}
	}
	// Rücknahme nicht bestätigt: deutlich sagen.
	out = render(SyncProfileData{Tab: "test", CanEdit: true, Test: &SyncTestView{NeedsWrite: true, AllowWrite: true,
		Write: &SyncWriteView{Statement: "UPDATE [dbo].[Meldungen]", Error: extsync.RollbackUnconfirmed}}})
	if !strings.Contains(out, "Schreibversuch nicht bestanden") || !strings.Contains(out, "Bitte die Meldung im Fremdsystem prüfen") {
		t.Error("nicht bestätigte Rücknahme wird nicht deutlich gezeigt")
	}
	// Ohne Bearbeiten-Recht und ohne Schreiben erlaubt: kein Startknopf.
	out = render(SyncProfileData{Tab: "test", Test: &SyncTestView{NeedsWrite: true}})
	if strings.Contains(out, "Trockenlauf starten") || strings.Contains(out, "Schreibversuch starten") || !strings.Contains(out, "Schreiben ist an der Verbindung nicht erlaubt") {
		t.Error("Test ohne Rechte: Startknopf sichtbar oder Hinweis fehlt")
	}

	// Betrieb: gesperrt, bereit, scharf, Rückfrage.
	ops := func(o SyncOpsView, run, edit bool) string {
		o.MinInterval, o.MaxInterval, o.Interval, o.Effective = 5, 30, 5, "alle 5 Minuten"
		return render(SyncProfileData{Tab: "ops", CanRun: run, CanEdit: edit, Ops: &o})
	}
	out = ops(SyncOpsView{ArmBlock: "Erst den Trockenlauf bestehen", FirstArm: true}, true, true)
	if !strings.Contains(out, "Erst den Trockenlauf bestehen") || strings.Contains(out, `action="/sync/p1/arm-web"`) || !strings.Contains(out, "Takt speichern") ||
		!strings.Contains(out, "Zu viele neue Meldungen") || !strings.Contains(out, "läuft der Abgleich nicht") {
		t.Error("Betrieb (gesperrt): Grund, Takt, Hinweis des Profils oder Hinweis auf den Spiegel fehlt, oder der Knopf ist aktiv")
	}
	out = ops(SyncOpsView{FirstArm: true, RunnerOn: true}, true, false)
	if !strings.Contains(out, `action="/sync/p1/arm-web"`) || !strings.Contains(out, "wie viele Meldungen der erste Lauf anlegen würde") || strings.Contains(out, "Takt speichern") {
		t.Error("Betrieb (bereit): Knopf oder Hinweis fehlt, oder der Takt ist ohne Bearbeiten-Recht änderbar")
	}
	out = ops(SyncOpsView{Halted: true, RunnerOn: true}, true, true)
	if !strings.Contains(out, "Fortsetzen (scharf schalten)") {
		t.Error("Betrieb (angehalten): Fortsetzen fehlt")
	}
	out = ops(SyncOpsView{Armed: true, RunnerOn: true, NextRun: "13.11.2026 10:05", Open: 2,
		Runs: []syncRunRow{{When: "13.11.2026 10:00", Until: "13.11. 10:20", Direction: "herein", Summary: "Warteposition wegen hoher Serverlast (Prozessor)", Waiting: true},
			{When: "13.11.2026 09:55", Direction: "hinaus", Summary: "1 fehlgeschlagen", Bad: true, Errors: []string{"Meldung 17 – Sperre"}}}}, true, true)
	for _, want := range []string{"Jetzt abgleichen", "Anhalten", "13.11.2026 10:05", "<b>2</b> Änderung(en)", "bis 13.11. 10:20", "Warteposition wegen hoher Serverlast (Prozessor)", "Meldung 17 – Sperre", `hx-get="/sync/p1/runs"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Betrieb (scharf) ohne %q", want)
		}
	}
	if strings.Contains(out, "läuft der Abgleich nicht") {
		t.Error("Betrieb (scharf): Hinweis auf den Spiegel trotz laufendem Taktgeber")
	}
	out = ops(SyncOpsView{Armed: true, RunnerOn: true}, false, false)
	if strings.Contains(out, "Jetzt abgleichen") || strings.Contains(out, "pause-web") {
		t.Error("Betrieb ohne sync.run zeigt Knöpfe")
	}
	out = ops(SyncOpsView{FirstArm: true, RunnerOn: true, Confirm: &syncArmConfirm{New: 12, Backfilled: 340, Archived: 300, MaxNew: 200}}, true, true)
	for _, want := range []string{"Vor dem ersten Scharfschalten", "<b>12</b> neue Ticket(s)", "<b>340</b> rückwirkend", "davon 300 gleich archiviert", "Ja, scharf schalten", `name="confirm" value="1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Rückfrage ohne %q", want)
		}
	}
	out = ops(SyncOpsView{FirstArm: true, RunnerOn: true, Confirm: &syncArmConfirm{New: 250, TooMany: true, MaxNew: 200}}, true, true)
	if !strings.Contains(out, "mehr als die erlaubten 200") || strings.Contains(out, "Ja, scharf schalten") {
		t.Error("Rückfrage bei zu vielen Meldungen: Hinweis fehlt oder Scharfschalten wird angeboten")
	}
	out = ops(SyncOpsView{FirstArm: true, RunnerOn: true, Confirm: &syncArmConfirm{Error: "Datenbank nicht erreichbar"}}, true, true)
	if !strings.Contains(out, "ließen sich nicht zählen") || strings.Contains(out, "Ja, scharf schalten") {
		t.Error("Rückfrage ohne Zahlen: Hinweis fehlt oder Scharfschalten wird angeboten")
	}
}
