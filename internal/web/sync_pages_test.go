package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/extsync"
)

// ── Attrappe der Fremddatenbank ──────────────────────────────────────────

type fakeSyncReader struct {
	tables []*extsync.TableInfo
	rows   []map[string]any
}

func (f *fakeSyncReader) Tables(context.Context) ([]extsync.TableInfo, error) {
	out := make([]extsync.TableInfo, 0, len(f.tables))
	for _, t := range f.tables {
		out = append(out, extsync.TableInfo{Schema: t.Schema, Name: t.Name})
	}
	return out, nil
}

func (f *fakeSyncReader) Describe(_ context.Context, schema, table string) (*extsync.TableInfo, error) {
	for _, t := range f.tables {
		if t.Schema == schema && t.Name == table {
			return t, nil
		}
	}
	return nil, extsync.ErrNotFound
}

func (f *fakeSyncReader) Query(context.Context, extsync.Stmt) ([]map[string]any, error) {
	return f.rows, nil
}

func syncTestTables() []*extsync.TableInfo {
	return []*extsync.TableInfo{
		{Schema: "dbo", Name: "Meldungen", UniqueKeys: [][]string{{"Nr"}}, Columns: []extsync.Column{
			{Name: "Nr", Type: "int", IsKey: true, IsIdentity: true},
			{Name: "Titel", Type: "nvarchar", MaxLen: 200},
			{Name: "Prio", Type: "nvarchar", MaxLen: 10, Nullable: true},
			{Name: "Zustand", Type: "nvarchar", MaxLen: 20},
			{Name: "Angelegt", Type: "datetime2"},
			{Name: "RV", Type: "timestamp", IsRowVersion: true},
		}},
		{Schema: "dbo", Name: "MeldungKommentare", UniqueKeys: [][]string{{"Id"}}, Columns: []extsync.Column{
			{Name: "Id", Type: "int", IsKey: true, IsIdentity: true},
			{Name: "MeldungNr", Type: "int"},
			{Name: "Text", Type: "nvarchar"},
			{Name: "Verfasser", Type: "nvarchar", MaxLen: 100, Nullable: true},
		}},
	}
}

// syncTestHandler: Handler mit Testdatenbank, allen Rechten und Attrappe der
// Fremddatenbank; allowWrite ist der Schalter „Schreiben erlaubt“ der Verbindung.
func syncTestHandler(t *testing.T, allowWrite bool) (*Handler, string) {
	t.Helper()
	pool := connTestPool(t)
	h := &Handler{db: pool, jwtSecret: "t", tmpl: loadTestTemplates(t), hasPermHook: func(*http.Request, string) bool { return true }}
	sealed, _ := sealConnConfig(h.connKey(), map[string]string{"host": "h", "password": "pw"})
	connID := insertConnRow(t, pool, sealed)
	dialect, err := extsync.DialectFor("mssql")
	if err != nil {
		t.Fatal(err)
	}
	h.syncReaderHook = func(context.Context, string) (syncReader, extsync.Dialect, map[string]string, error) {
		cfg := map[string]string{"allow_write": "false"}
		if allowWrite {
			cfg["allow_write"] = "true"
		}
		return &fakeSyncReader{tables: syncTestTables(), rows: []map[string]any{{"Nr": int64(1), "Titel": "Lager heiß", "RV": []byte{0, 0, 0, 0, 0, 0, 0, 9}}}}, dialect, cfg, nil
	}
	return h, connID
}

// syncDo ruft einen Handler mit der Profil-ID im Pfad auf.
func syncDo(h http.HandlerFunc, method, path, id string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func syncLocation(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Status %d: %s", w.Code, w.Body.String())
	}
	loc, _ := url.QueryUnescape(w.Header().Get("Location"))
	return loc
}

// ── Rechte ───────────────────────────────────────────────────────────────

func TestSyncPagesRequirePermission(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}

	w := syncDo(h.SyncPage, "GET", "/sync", "", nil, false)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("Übersicht ohne sync.view: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = syncDo(h.SyncProfilePage, "GET", "/sync/"+id, id, nil, false)
	if w.Code != http.StatusFound {
		t.Errorf("Cockpit ohne sync.view: %d", w.Code)
	}
	cases := []struct {
		name string
		fn   http.HandlerFunc
		meth string
		path string
		want string
	}{
		{"anlegen", h.SyncNewWeb, "POST", "/sync/new-web", permSyncEdit},
		{"Quelle", h.SyncSourceWeb, "POST", "/sync/" + id + "/source-web", permSyncEdit},
		{"Ziel", h.SyncTargetWeb, "POST", "/sync/" + id + "/target-web", permSyncEdit},
		{"Zuordnungen", h.SyncMapsWeb, "POST", "/sync/" + id + "/maps-web", permSyncEdit},
		{"löschen", h.SyncDeleteWeb, "POST", "/sync/" + id + "/delete-web", permSyncAdmin},
		{"Tabellen", h.SyncTablesFragment, "GET", "/sync/" + id + "/tables", permSyncEdit},
		{"Spalten", h.SyncColumnsFragment, "GET", "/sync/" + id + "/columns", permSyncEdit},
		{"Beispielzeilen", h.SyncSampleFragment, "GET", "/sync/" + id + "/sample", permSyncEdit},
	}
	for _, c := range cases {
		asked = nil
		w := syncDo(c.fn, c.meth, c.path, id, url.Values{}, c.meth == "GET")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[len(asked)-1] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}
	// Löschen fragt sync.admin – sync.edit genügt nicht.
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permSyncAdmin }
	if w := syncDo(h.SyncDeleteWeb, "POST", "/sync/"+id+"/delete-web", id, url.Values{}, false); w.Code != http.StatusForbidden {
		t.Errorf("Löschen mit sync.edit, ohne sync.admin: Status %d", w.Code)
	}
	// Keine gültige ID: nicht gefunden statt Datenbankzugriff.
	if w := syncDo(h.SyncDeleteWeb, "POST", "/sync/x/delete-web", "x", url.Values{}, false); w.Code != http.StatusNotFound {
		t.Errorf("ungültige ID: Status %d", w.Code)
	}
}

func TestSyncGateRules(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	cases := []struct {
		method, path, perm, ref string
		hit                     bool
	}{
		{"POST", "/sync/new-web", permSyncEdit, "", true},
		{"POST", "/sync/" + id + "/source-web", permSyncEdit, syncRef, true},
		{"POST", "/sync/" + id + "/target-web", permSyncEdit, syncRef, true},
		{"POST", "/sync/" + id + "/maps-web", permSyncEdit, syncRef, true},
		{"POST", "/sync/" + id + "/delete-web", permSyncAdmin, syncRef, true},
		{"GET", "/sync/" + id + "/delete-web", "", "", false},
		{"POST", "/sync/" + id + "/unbekannt-web", "", "", false},
	}
	for _, c := range cases {
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
	if areaAdminPerm[syncRef] != permSyncAdmin {
		t.Errorf("Bereichsadministrator des Abgleichs: %q", areaAdminPerm[syncRef])
	}
}

// ── Aktionen ─────────────────────────────────────────────────────────────

func syncNewProfile(t *testing.T, h *Handler, connID string) string {
	t.Helper()
	w := syncDo(h.SyncNewWeb, "POST", "/sync/new-web", "", url.Values{
		"name": {"zz-sync-seite"}, "connection_id": {connID}, "target_type": {"ticket"}}, false)
	loc := syncLocation(t, w)
	if !strings.HasPrefix(loc, "/sync/") || !strings.Contains(loc, "tab=source") {
		t.Fatalf("nach dem Anlegen: %s", loc)
	}
	id := strings.SplitN(strings.TrimPrefix(loc, "/sync/"), "?", 2)[0]
	t.Cleanup(func() { _, _ = h.db.Exec(context.Background(), `DELETE FROM sync_profiles WHERE id = $1`, id) })
	return id
}

func syncGet(t *testing.T, h *Handler, id string) *extsync.Profile {
	t.Helper()
	p, err := h.syncStore().GetProfile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSyncNewValidatesInput(t *testing.T) {
	h, connID := syncTestHandler(t, true)
	for name, form := range map[string]url.Values{
		"Name zu kurz":          {"name": {"x"}, "connection_id": {connID}, "target_type": {"ticket"}},
		"unbekannte Verbindung": {"name": {"zz-sync-seite"}, "connection_id": {"00000000-0000-4000-8000-000000000000"}, "target_type": {"ticket"}},
		"unbekanntes Ziel":      {"name": {"zz-sync-seite"}, "connection_id": {connID}, "target_type": {"auftrag"}},
	} {
		loc := syncLocation(t, syncDo(h.SyncNewWeb, "POST", "/sync/new-web", "", form, false))
		if !strings.HasPrefix(loc, "/sync?") || !strings.Contains(loc, "err=") {
			t.Errorf("%s: %s", name, loc)
		}
	}
	id := syncNewProfile(t, h, connID)
	if p := syncGet(t, h, id); p.State != extsync.StateDraft || p.TargetType != extsync.RefTicket || p.ConnectionID != connID {
		t.Fatalf("neues Profil: %+v", p)
	}
}

func TestSyncSourceRejectsUnknownTable(t *testing.T) {
	h, connID := syncTestHandler(t, true)
	id := syncNewProfile(t, h, connID)
	post := func(form url.Values) string {
		t.Helper()
		return syncLocation(t, syncDo(h.SyncSourceWeb, "POST", "/sync/"+id+"/source-web", id, form, false))
	}
	// Tabelle, die es in der Datenbank nicht gibt – auch nicht mit eingeschleustem Text.
	for _, table := range []string{"dbo.Gibtsnicht", "dbo.Meldungen; DROP TABLE x", "Meldungen"} {
		loc := post(url.Values{"table": {table}, "key": {"Nr"}, "change": {"rowversion:RV"}})
		if !strings.Contains(loc, "gibt es in der Datenbank nicht") {
			t.Errorf("Tabelle %q: %s", table, loc)
		}
	}
	if p := syncGet(t, h, id); p.SourceTable != "" {
		t.Fatalf("abgelehnte Tabelle wurde gespeichert: %+v", p)
	}
	// Spalten müssen im gelesenen Aufbau stehen und passen.
	for name, form := range map[string]url.Values{
		"unbekannter Schlüssel":       {"table": {"dbo.Meldungen"}, "key": {"Gibtsnicht"}, "change": {"none:"}},
		"Schlüssel nicht eindeutig":   {"table": {"dbo.Meldungen"}, "key": {"Titel"}, "change": {"none:"}},
		"kein Schlüssel":              {"table": {"dbo.Meldungen"}, "change": {"none:"}},
		"Änderungsspalte falsch":      {"table": {"dbo.Meldungen"}, "key": {"Nr"}, "change": {"rowversion:Titel"}},
		"Anlagedatum keine Zeit":      {"table": {"dbo.Meldungen"}, "key": {"Nr"}, "change": {"none:"}, "created": {"Titel"}},
		"Zeitzone unbekannt":          {"table": {"dbo.Meldungen"}, "key": {"Nr"}, "change": {"none:"}, "time_zone": {"Mond/Krater"}},
		"unbekannte Art der Änderung": {"table": {"dbo.Meldungen"}, "key": {"Nr"}, "change": {"magie:RV"}},
	} {
		if loc := post(form); !strings.Contains(loc, "err=") {
			t.Errorf("%s: angenommen (%s)", name, loc)
		}
	}
	if p := syncGet(t, h, id); p.SourceTable != "" {
		t.Fatalf("ungültige Quelle wurde gespeichert: %+v", p)
	}
	// Gültig.
	loc := post(url.Values{"name": {"zz-sync-seite neu"}, "table": {"dbo.Meldungen"}, "key": {"Nr"}, "change": {"rowversion:RV"},
		"created": {"Angelegt"}, "time_zone": {"Europe/Berlin"}})
	if !strings.Contains(loc, "notice=") {
		t.Fatalf("gültige Quelle abgelehnt: %s", loc)
	}
	p := syncGet(t, h, id)
	if p.Name != "zz-sync-seite neu" || p.SourceSchema != "dbo" || p.SourceTable != "Meldungen" || len(p.KeyColumns) != 1 || p.KeyColumns[0] != "Nr" ||
		p.ChangeKind != "rowversion" || p.ChangeColumn != "RV" || p.CreatedColumn != "Angelegt" || p.TimeZone != "Europe/Berlin" {
		t.Fatalf("gespeicherte Quelle: %+v", p)
	}
}

func TestSyncTargetSave(t *testing.T) {
	h, connID := syncTestHandler(t, true)
	id := syncNewProfile(t, h, connID)
	post := func(form url.Values) string {
		t.Helper()
		return syncLocation(t, syncDo(h.SyncTargetWeb, "POST", "/sync/"+id+"/target-web", id, form, false))
	}
	if loc := post(url.Values{"target_type": {"ticket"}, "filter_sql": {"1=1; DELETE FROM Meldungen"}}); !strings.Contains(loc, "Filter") {
		t.Errorf("verbotener Filter angenommen: %s", loc)
	}
	if loc := post(url.Values{"target_type": {"ticket"}, "def_priority": {"superwichtig"}}); !strings.Contains(loc, "ungültig") {
		t.Errorf("ungültiger Vorgabewert angenommen: %s", loc)
	}
	loc := post(url.Values{"target_type": {"fault"}, "def_severity": {"HIGH"}, "def_title": {"Meldung aus dem ERP"},
		"filter_sql": {"Zustand <> 'storniert'"}, "window_days": {"99999"}, "max_new": {"0"}})
	if !strings.Contains(loc, "notice=") {
		t.Fatalf("gültiges Ziel abgelehnt: %s", loc)
	}
	p := syncGet(t, h, id)
	if p.TargetType != extsync.RefFault || p.Defaults["severity"] != "high" || p.Defaults["title"] != "Meldung aus dem ERP" ||
		p.FilterSQL != "Zustand <> 'storniert'" || p.WindowDays != 3650 || p.MaxNew != 1 {
		t.Fatalf("gespeichertes Ziel: %+v", p)
	}
}

func syncWithSource(t *testing.T, h *Handler, connID string) string {
	t.Helper()
	id := syncNewProfile(t, h, connID)
	loc := syncLocation(t, syncDo(h.SyncSourceWeb, "POST", "/sync/"+id+"/source-web", id,
		url.Values{"table": {"dbo.Meldungen"}, "key": {"Nr"}, "change": {"rowversion:RV"}}, false))
	if !strings.Contains(loc, "notice=") {
		t.Fatalf("Quelle: %s", loc)
	}
	return id
}

func TestSyncMapsSave(t *testing.T) {
	h, connID := syncTestHandler(t, true)
	id := syncWithSource(t, h, connID)
	post := func(form url.Values) string {
		t.Helper()
		return syncLocation(t, syncDo(h.SyncMapsWeb, "POST", "/sync/"+id+"/maps-web", id, form, false))
	}
	if loc := post(url.Values{"col_title": {"Gibtsnicht"}, "dir_title": {"herein"}}); !strings.Contains(loc, "gibt es in der Tabelle nicht") {
		t.Errorf("unbekannte Spalte angenommen: %s", loc)
	}
	if loc := post(url.Values{"col_title": {"Nr"}, "dir_title": {"beides"}}); !strings.Contains(loc, "Schlüsselspalte") {
		t.Errorf("Zurückschreiben in die Schlüsselspalte angenommen: %s", loc)
	}
	if loc := post(url.Values{"col_title": {"Titel"}, "dir_title": {"herein"}, "ct_table": {"dbo.Gibtsnicht"}}); !strings.Contains(loc, "gibt es in der Datenbank nicht") {
		t.Errorf("unbekannte Kommentartabelle angenommen: %s", loc)
	}
	loc := post(url.Values{
		"col_title": {"Titel"}, "dir_title": {"beides"},
		"col_priority": {"Prio"}, "dir_priority": {"herein"},
		"trf_priority": {"hoch", "", "mittel"}, "trp_priority": {"high", "low", "medium"},
		"fixed_status": {"open"}, "dir_status": {"herein"},
		"ct_table": {"dbo.MeldungKommentare"}, "ct_fk": {"MeldungNr"}, "ct_text": {"Text"}, "ct_author": {"Verfasser"},
	})
	if !strings.Contains(loc, "Zuordnungen gespeichert") || strings.Contains(loc, "Pflichtfeld") {
		t.Fatalf("gültige Zuordnung: %s", loc)
	}
	maps, err := h.syncStore().ListMaps(context.Background(), id)
	if err != nil || len(maps) != 3 {
		t.Fatalf("Zuordnungen: %+v %v", maps, err)
	}
	by := map[string]extsync.FieldMap{}
	for _, m := range maps {
		by[m.PDHField] = m
	}
	if m := by["title"]; m.ForeignColumn != "Titel" || m.Direction != extsync.DirBoth {
		t.Errorf("Titel: %+v", m)
	}
	if m := by["priority"]; len(m.Translate) != 2 || m.Translate["hoch"] != "high" || m.Translate["mittel"] != "medium" {
		t.Errorf("Übersetzung: %+v", m.Translate)
	}
	if m := by["status"]; m.FixedValue == nil || *m.FixedValue != "open" || m.ForeignColumn != "" {
		t.Errorf("fester Wert: %+v", m)
	}
	p := syncGet(t, h, id)
	if p.CommentTable == nil || p.CommentTable.Table != "MeldungKommentare" || p.CommentTable.FKColumn != "MeldungNr" || p.CommentTable.AuthorColumn != "Verfasser" {
		t.Fatalf("Kommentartabelle: %+v", p.CommentTable)
	}
	// Ohne Titel: gespeichert, aber mit Hinweis auf das fehlende Pflichtfeld.
	if loc := post(url.Values{"col_priority": {"Prio"}, "dir_priority": {"herein"}}); !strings.Contains(loc, "Pflichtfeld: Titel") {
		t.Errorf("Hinweis auf fehlendes Pflichtfeld: %s", loc)
	}
	if p := syncGet(t, h, id); p.CommentTable != nil {
		t.Errorf("abgewählte Kommentartabelle blieb: %+v", p.CommentTable)
	}
}

// Ohne „Schreiben erlaubt“ an der Verbindung wird keine Richtung hinaus/beides
// und keine Kommentartabelle gespeichert, und die Seite sperrt die Auswahl.
func TestSyncMapsLocksOutboundWithoutWrite(t *testing.T) {
	h, connID := syncTestHandler(t, false)
	id := syncWithSource(t, h, connID)
	post := func(form url.Values) string {
		t.Helper()
		return syncLocation(t, syncDo(h.SyncMapsWeb, "POST", "/sync/"+id+"/maps-web", id, form, false))
	}
	for _, dir := range []string{"beides", "hinaus"} {
		if loc := post(url.Values{"col_title": {"Titel"}, "dir_title": {dir}}); !strings.Contains(loc, "Schreiben ist an der Verbindung nicht erlaubt") {
			t.Errorf("Richtung %s ohne Schreiben angenommen: %s", dir, loc)
		}
	}
	if loc := post(url.Values{"col_title": {"Titel"}, "dir_title": {"herein"}, "ct_table": {"dbo.MeldungKommentare"}, "ct_fk": {"MeldungNr"}, "ct_text": {"Text"}}); !strings.Contains(loc, "Kommentare: Schreiben") {
		t.Errorf("Kommentartabelle ohne Schreiben angenommen: %s", loc)
	}
	if maps, _ := h.syncStore().ListMaps(context.Background(), id); len(maps) != 0 {
		t.Fatalf("abgelehnte Zuordnung gespeichert: %+v", maps)
	}
	if loc := post(url.Values{"col_title": {"Titel"}, "dir_title": {"herein"}}); !strings.Contains(loc, "Zuordnungen gespeichert") {
		t.Fatalf("herein ohne Schreiben abgelehnt: %s", loc)
	}

	p := syncGet(t, h, id)
	maps, _ := h.syncStore().ListMaps(context.Background(), id)
	tables := syncTestTables()
	d := SyncProfileData{P: p, Tab: "maps", CanEdit: true, AllowWrite: false, TargetLabel: "Ticket",
		Maps: syncMapsView(p, maps, tables[0], []extsync.TableInfo{*tables[0], *tables[1]}, false)}
	out := renderPage(t, loadTestTemplates(t), "sync_profile", d)
	for _, want := range []string{
		"Schreiben ist an der Verbindung nicht erlaubt",
		`<option value="beides"  disabled>`,
		`<option value="hinaus"  disabled>`,
		"Zurückschreiben gesperrt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite ohne %q", want)
		}
	}
	if strings.Contains(out, `<option value="herein" selected disabled>`) {
		t.Error("Richtung herein darf nicht gesperrt sein")
	}
}

func TestSyncDelete(t *testing.T) {
	h, connID := syncTestHandler(t, true)
	id := syncWithSource(t, h, connID)
	loc := syncLocation(t, syncDo(h.SyncDeleteWeb, "POST", "/sync/"+id+"/delete-web", id, url.Values{}, false))
	if !strings.HasPrefix(loc, "/sync?") || !strings.Contains(loc, "gelöscht") {
		t.Fatalf("nach dem Löschen: %s", loc)
	}
	if _, err := h.syncStore().GetProfile(context.Background(), id); err == nil {
		t.Fatal("Profil nicht gelöscht")
	}
}

// ── Bausteine und Vorlagen ───────────────────────────────────────────────

func TestSyncFragments(t *testing.T) {
	h, connID := syncTestHandler(t, true)
	id := syncNewProfile(t, h, connID)
	body := func(fn http.HandlerFunc, path string) string {
		t.Helper()
		w := syncDo(fn, "GET", path, id, nil, true)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: Status %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	out := body(h.SyncTablesFragment, "/sync/"+id+"/tables")
	for _, want := range []string{`<option value="dbo.Meldungen"`, `<option value="dbo.MeldungKommentare"`, `hx-get="/sync/` + id + `/columns"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Tabellen ohne %q", want)
		}
	}
	out = body(h.SyncColumnsFragment, "/sync/"+id+"/columns?table=dbo.Meldungen")
	for _, want := range []string{
		`name="key" value="Nr" aria-label="Schlüsselspalte Nr" checked`, // Primärschlüssel vorgeschlagen
		`<option value="rowversion:RV" selected>`,                       // beste Änderungserkennung vorgeschlagen
		`<option value="none:"`,
		`<option value="Angelegt"`,
		"Eindeutige Schlüssel dieser Tabelle: Nr",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Spalten ohne %q", want)
		}
	}
	if strings.Contains(out, `value="Titel" aria-label="Schlüsselspalte Titel" checked`) {
		t.Error("Titel als Schlüssel vorgeschlagen")
	}
	if out := body(h.SyncColumnsFragment, "/sync/"+id+"/columns?table=dbo.Gibtsnicht"); !strings.Contains(out, "gibt es in der Datenbank nicht") {
		t.Errorf("unbekannte Tabelle: %s", out)
	}
	out = body(h.SyncSampleFragment, "/sync/"+id+"/sample?table=dbo.Meldungen")
	for _, want := range []string{"Lager heiß", "0x0000000000000009", "<th>Titel</th>", "nichts gespeichert"} {
		if !strings.Contains(out, want) {
			t.Errorf("Beispielzeilen ohne %q", want)
		}
	}
	if out := body(h.SyncSampleFragment, "/sync/"+id+"/sample?table=x"); !strings.Contains(out, "gibt es in der Datenbank nicht") {
		t.Errorf("Beispielzeilen einer unbekannten Tabelle: %s", out)
	}
	// Nach den Bausteinen muss die Vorlagensammlung weiter kopierbar sein – sonst
	// liefert der Server danach für jede Seite einen Fehler.
	if _, err := h.tmpl.Clone(); err != nil {
		t.Fatalf("Vorlagen nach den Bausteinen nicht mehr nutzbar: %v", err)
	}
}

func TestSyncMapsMarksMissingRequired(t *testing.T) {
	tables := syncTestTables()
	all := []extsync.TableInfo{*tables[0], *tables[1]}
	p := &extsync.Profile{ID: "p1", Name: "ERP", TargetType: extsync.RefTicket, SourceSchema: "dbo", SourceTable: "Meldungen", KeyColumns: []string{"Nr"}}
	tmpl := loadTestTemplates(t)
	render := func(maps []extsync.FieldMap) string {
		return renderPage(t, tmpl, "sync_profile", SyncProfileData{P: p, Tab: "maps", CanEdit: true, AllowWrite: true, TargetLabel: "Ticket",
			Maps: syncMapsView(p, maps, tables[0], all, true)})
	}
	out := render([]extsync.FieldMap{{PDHField: "priority", ForeignColumn: "Prio", Direction: extsync.DirIn}})
	if strings.Count(out, "Pflichtfeld ohne Zuordnung") != 1 || !strings.Contains(out, "sy-map sy-map-missing") {
		t.Errorf("fehlendes Pflichtfeld Titel nicht (genau einmal) markiert")
	}
	out = render([]extsync.FieldMap{{PDHField: "title", ForeignColumn: "Titel", Direction: extsync.DirBoth}})
	if strings.Contains(out, "Pflichtfeld ohne Zuordnung") || strings.Contains(out, "sy-map sy-map-missing") {
		t.Error("zugeordnetes Pflichtfeld als fehlend markiert")
	}
	if !strings.Contains(out, `<option value="Titel" selected>`) || !strings.Contains(out, `<option value="beides" selected >`) {
		t.Error("gespeicherte Zuordnung nicht vorgewählt")
	}
	// Vorgabewert für den Titel: nichts fehlt.
	p.Defaults = map[string]string{"title": "Meldung"}
	if out := render(nil); strings.Contains(out, "Pflichtfeld ohne Zuordnung") {
		t.Error("Pflichtfeld mit Vorgabewert als fehlend markiert")
	}
}

func TestSyncTemplatesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	tables := syncTestTables()
	all := []extsync.TableInfo{*tables[0], *tables[1]}
	dialect, _ := extsync.DialectFor("mssql")
	conns := []syncOption{{Value: "c1", Label: "ERP (Import)"}}

	// Übersicht: leer, ohne Verbindung, gefüllt.
	out := renderPage(t, tmpl, "sync", SyncPageData{})
	if !strings.Contains(out, "Noch kein Profil") || strings.Contains(out, "Neues Profil") {
		t.Error("leere Übersicht ohne Bearbeiten-Recht")
	}
	out = renderPage(t, tmpl, "sync", SyncPageData{CanEdit: true})
	if !strings.Contains(out, "noch keine SQL-Server-Verbindung") {
		t.Error("Hinweis auf fehlende Verbindung fehlt")
	}
	out = renderPage(t, tmpl, "sync", SyncPageData{CanEdit: true, AllowGlobal: true, Connections: conns, Notice: "gespeichert",
		Profiles: []SyncListRow{{ID: "p1", Name: "ERP-Meldungen", Connection: "ERP (Import)", Target: "Ticket", StateLabel: "Scharf",
			StateColor: "var(--green)", Open: 3, LastRun: "13.11.2026 10:00", LastResult: "2 neu", StateNote: "läuft"}}})
	for _, want := range []string{`href="/sync/p1"`, "ERP-Meldungen", "Scharf", "2 neu", "<b>3</b>", `action="/sync/new-web"`, "– alle Abteilungen –", "gespeichert"} {
		if !strings.Contains(out, want) {
			t.Errorf("Übersicht ohne %q", want)
		}
	}

	empty := &extsync.Profile{ID: "p0", Name: "Neu", TargetType: extsync.RefTicket, ConnectionID: "c1", MaxNew: 200}
	full := &extsync.Profile{ID: "p1", Name: "ERP-Meldungen", TargetType: extsync.RefFault, ConnectionID: "c1",
		SourceSchema: "dbo", SourceTable: "Meldungen", KeyColumns: []string{"Nr"}, ChangeKind: "rowversion", ChangeColumn: "RV",
		CreatedColumn: "Angelegt", TimeZone: "Asia/Tokyo", FilterSQL: "Prio = 'hoch'", WindowDays: 30, MaxNew: 50,
		Defaults:     map[string]string{"severity": "high"},
		CommentTable: &extsync.CommentTable{Schema: "dbo", Table: "MeldungKommentare", FKColumn: "MeldungNr", TextColumn: "Text"}}
	maps := []extsync.FieldMap{
		{PDHField: "title", ForeignColumn: "Titel", Direction: extsync.DirBoth},
		{PDHField: "severity", ForeignColumn: "Prio", Direction: extsync.DirIn, Translate: map[string]string{"hoch": "high"}},
		{PDHField: "description", ForeignColumn: "Entfernt", Direction: extsync.DirIn},
	}
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	req := httptest.NewRequest("GET", "/sync/p1", nil)
	for _, edit := range []bool{true, false} {
		for _, p := range []*extsync.Profile{empty, full} {
			for _, tab := range []string{"source", "target", "maps"} {
				d := SyncProfileData{P: p, Tab: tab, CanEdit: edit, CanDelete: edit, AllowWrite: true, StateLabel: "Entwurf", StateColor: "var(--muted)",
					ConnName: "ERP (Import)", TargetLabel: syncTargetLabels[p.TargetType], Connections: conns}
				var table *extsync.TableInfo
				if p.SourceTable != "" && edit {
					table = tables[0]
				}
				switch tab {
				case "source":
					d.Source = &SyncSourceView{Table: syncTableKey(p.SourceSchema, p.SourceTable), TimeZones: syncTimeZones(p.TimeZone),
						Tables: []string{"dbo.Meldungen", "dbo.MeldungKommentare"}}
					if table != nil {
						d.Source.Cols = syncColumnsView(dialect, table, p.KeyColumns, p.ChangeKind, p.ChangeColumn, p.CreatedColumn, false)
					}
				case "target":
					d.Target = h.syncTargetView(req, p)
				case "maps":
					var m []extsync.FieldMap
					if p == full {
						m = maps
					}
					d.Maps = syncMapsView(p, m, table, all, true)
				}
				out := renderPage(t, tmpl, "sync_profile", d)
				if !strings.Contains(out, p.Name) || !strings.Contains(out, `href="?tab=maps"`) {
					t.Errorf("Cockpit %s/%s (bearbeiten=%v): Kopf fehlt", p.Name, tab, edit)
				}
				if got := strings.Contains(out, "/delete-web"); got != edit {
					t.Errorf("Cockpit %s/%s: Löschknopf sichtbar=%v, erwartet %v", p.Name, tab, got, edit)
				}
				if !edit && strings.Contains(out, "speichern</button>") {
					t.Errorf("Cockpit %s/%s ohne Bearbeiten-Recht zeigt einen Speichern-Knopf", p.Name, tab)
				}
			}
		}
	}

	// Gefülltes Profil, Bearbeiter: Einzelheiten je Schritt.
	src := SyncProfileData{P: full, Tab: "source", CanEdit: true, Locked: true, Connections: conns, TargetLabel: "Störmeldung",
		Source: &SyncSourceView{Table: "dbo.Meldungen", Tables: []string{"dbo.Meldungen"}, TimeZones: syncTimeZones(full.TimeZone)}}
	src.Source.Cols = syncColumnsView(dialect, tables[0], full.KeyColumns, full.ChangeKind, full.ChangeColumn, full.CreatedColumn, false)
	src.Source.Cols.ReadOnly = true
	out = renderPage(t, tmpl, "sync_profile", src)
	for _, want := range []string{
		"bereits Meldungen verknüpft",                              // Quelle fest
		`<input type="hidden" name="table" value="dbo.Meldungen">`, // gesperrte Auswahl wird trotzdem gesendet
		`<input type="hidden" name="key" value="Nr">`,
		`<option value="Asia/Tokyo" selected>`, // gespeicherte Zeitzone bleibt in der Liste
		`<option value="Angelegt" selected>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Quelle (fest) ohne %q", want)
		}
	}
	mp := SyncProfileData{P: full, Tab: "maps", CanEdit: true, AllowWrite: true, TargetLabel: "Störmeldung", Maps: syncMapsView(full, maps, tables[0], all, true)}
	out = renderPage(t, tmpl, "sync_profile", mp)
	for _, want := range []string{
		"Schweregrad",
		`<option value="Entfernt" selected>Entfernt (nicht mehr vorhanden)</option>`, // Spalte im Fremdsystem verschwunden
		`value="hoch"`,
		`<option value="dbo.MeldungKommentare" selected>`,
		`value="MeldungNr"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Zuordnungen ohne %q", want)
		}
	}
	if strings.Contains(out, `<option value="dbo.Meldungen"`) {
		t.Error("die Meldungstabelle steht als Kommentartabelle zur Wahl")
	}
	// Fremddatenbank nicht lesbar: Hinweis, Speichern gesperrt.
	mp.ReadErr = "Datenbank nicht erreichbar: Zeitüberschreitung"
	mp.Maps = syncMapsView(full, maps, nil, nil, true)
	out = renderPage(t, tmpl, "sync_profile", mp)
	if !strings.Contains(out, "gerade nicht lesbar") || !strings.Contains(out, `type="submit" disabled`) || !strings.Contains(out, `<option value="Titel" selected>`) {
		t.Error("Zuordnungen ohne Verbindung: Hinweis, gesperrtes Speichern oder gespeicherte Spalte fehlt")
	}
}

func TestSyncRunSummary(t *testing.T) {
	cases := []struct {
		run  extsync.Run
		want string
		bad  bool
	}{
		{extsync.Run{}, "keine Änderungen", false},
		{extsync.Run{New: 2, Changed: 1}, "2 neu, 1 geändert", false},
		{extsync.Run{Written: 3, Failed: 1}, "3 zurückgeschrieben, 1 fehlgeschlagen", true},
		{extsync.Run{SkippedReason: "Weg nicht bereit: Tunnel steht nicht"}, "Weg nicht bereit: Tunnel steht nicht", true},
		{extsync.Run{SkippedReason: extsync.SkippedOverload + " (Prozessor)"}, "Warteposition wegen hoher Serverlast (Prozessor)", false},
	}
	for _, c := range cases {
		if got, bad := syncRunSummary(c.run); got != c.want || bad != c.bad {
			t.Errorf("%+v: %q/%v, erwartet %q/%v", c.run, got, bad, c.want, c.bad)
		}
	}
}
