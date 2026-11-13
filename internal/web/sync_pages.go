package web

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/extsync"
)

// Abgleich mit Fremdsystemen – Seiten: Übersicht aller Profile und das Cockpit
// eines Profils (Schritte Quelle, Ziel, Zuordnungen). Die Seiten zeigen nur an
// und sammeln Eingaben; geprüft wird in internal/modules/extsync (setup.go),
// gespeichert über extsync.Store.

// syncRef: Datensatz-Typ eines Profils für Rechte auf Abteilungsebene.
const syncRef = "sync_profile"

// Rechte des Bereichs (Migration 158).
const (
	permSyncView  = "sync.view"
	permSyncEdit  = "sync.edit"
	permSyncRun   = "sync.run"
	permSyncAdmin = "sync.admin"
)

// syncCan: Recht für ein Profil (id "" = irgendwo vorhanden).
func (h *Handler) syncCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if id == "" {
		return h.hasPerm(r, perm)
	}
	return h.hasPermRec(r, perm, syncRef, id)
}

// ── Zugriff auf den Aufbau der Fremddatenbank ───────────────────────────

// syncReader liest Tabellen, Spalten und Beispielzeilen der Fremddatenbank.
type syncReader interface {
	Tables(ctx context.Context) ([]extsync.TableInfo, error)
	Describe(ctx context.Context, schema, table string) (*extsync.TableInfo, error)
	Query(ctx context.Context, st extsync.Stmt) ([]map[string]any, error)
}

type sqlSyncReader struct {
	db      *sql.DB
	d       extsync.Dialect
	f       extsync.Foreign
	timeout time.Duration
}

func (s *sqlSyncReader) Tables(ctx context.Context) ([]extsync.TableInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.d.Tables(ctx, s.db)
}

func (s *sqlSyncReader) Describe(ctx context.Context, schema, table string) (*extsync.TableInfo, error) {
	return s.f.Describe(ctx, schema, table)
}

func (s *sqlSyncReader) Query(ctx context.Context, st extsync.Stmt) ([]map[string]any, error) {
	return s.f.Query(ctx, st)
}

// syncOpenReader öffnet die Fremddatenbank einer Verbindung zum Lesen des
// Aufbaus. Der Fehlertext ist für die Anzeige gedacht. closeFn ist nie nil.
func (h *Handler) syncOpenReader(ctx context.Context, connectionID string) (rd syncReader, d extsync.Dialect, cfg map[string]string, closeFn func(), err error) {
	closeFn = func() {}
	if h.syncReaderHook != nil {
		rd, d, cfg, err = h.syncReaderHook(ctx, connectionID)
		return rd, d, cfg, closeFn, err
	}
	kind, cfg, err := h.syncConn(ctx, connectionID)
	if err != nil {
		return nil, nil, cfg, closeFn, err
	}
	if ok, why := h.connRouteReady(ctx, cfg); !ok {
		return nil, nil, cfg, closeFn, errors.New("Weg nicht bereit: " + why)
	}
	if d, err = extsync.DialectFor(kind); err != nil {
		return nil, nil, cfg, closeFn, err
	}
	db, err := syncOpen(ctx, kind, cfg)
	if err != nil {
		return nil, nil, cfg, closeFn, errors.New("Datenbank nicht erreichbar: " + err.Error())
	}
	timeout := connQueryTimeout(cfg)
	return &sqlSyncReader{db: db, d: d, f: extsync.NewSQLForeign(db, d, timeout), timeout: timeout}, d, cfg,
		func() { _ = db.Close() }, nil
}

// syncTableKey: Anzeige- und Formularwert einer Tabelle.
func syncTableKey(schema, table string) string {
	if table == "" {
		return ""
	}
	return schema + "." + table
}

// syncFindTable sucht den Formularwert in der gelesenen Tabellenliste (nie
// wird ein Name aus dem Formular zerlegt und ungeprüft verwendet).
func syncFindTable(tables []extsync.TableInfo, key string) (schema, table string, ok bool) {
	for _, t := range tables {
		if syncTableKey(t.Schema, t.Name) == key {
			return t.Schema, t.Name, true
		}
	}
	return "", "", false
}

// ── Anzeigehilfen ────────────────────────────────────────────────────────

type syncOption struct {
	Value, Label string
	Disabled     bool
}

var syncStateLabels = map[string][2]string{ // Zustand -> Bezeichnung, Farbe
	extsync.StateDraft:  {"Entwurf", "var(--muted)"},
	extsync.StateTested: {"Getestet", "var(--accent)"},
	extsync.StateArmed:  {"Scharf", "var(--green)"},
	extsync.StateHalted: {"Angehalten", "var(--red)"},
}

func syncStateLabel(state string) (label, color string) {
	if l, ok := syncStateLabels[state]; ok {
		return l[0], l[1]
	}
	return state, "var(--muted)"
}

var syncTargetLabels = map[string]string{extsync.RefTicket: "Ticket", extsync.RefFault: "Störmeldung"}

// syncValueLabels: Bezeichnungen der Aufzählungswerte im PDH.
var syncValueLabels = map[string]string{
	"low": "Niedrig", "medium": "Mittel", "high": "Hoch", "critical": "Kritisch",
	"open": "Offen", "detected": "Erkannt", "analyzing": "In Analyse", "in_progress": "In Arbeit",
	"pending": "Wartend", "resolved": "Erledigt", "closed": "Geschlossen",
}

func syncValueOptions(f extsync.PDHField) []syncOption {
	out := make([]syncOption, 0, len(f.Values))
	for _, v := range f.Values {
		l := syncValueLabels[v]
		if l == "" {
			l = v
		}
		out = append(out, syncOption{Value: v, Label: l})
	}
	return out
}

var syncDirOptions = []syncOption{
	{Value: extsync.DirIn, Label: "herein (Fremdsystem → PDH)"},
	{Value: extsync.DirBoth, Label: "beides"},
	{Value: extsync.DirOut, Label: "hinaus (PDH → Fremdsystem)"},
}

// syncTimeZones: Auswahl der Zeitzone des Fremdsystems; ein anderer gespeicherter
// IANA-Name bleibt in der Liste erhalten.
func syncTimeZones(current string) []syncOption {
	out := []syncOption{
		{Value: "", Label: "wie der PDH-Server"},
		{Value: "UTC", Label: "UTC (Weltzeit)"},
		{Value: "Europe/Berlin", Label: "Europe/Berlin"},
		{Value: "Europe/London", Label: "Europe/London"},
		{Value: "America/New_York", Label: "America/New_York"},
		{Value: "Asia/Shanghai", Label: "Asia/Shanghai"},
	}
	for _, o := range out {
		if o.Value == current {
			return out
		}
	}
	return append(out, syncOption{Value: current, Label: current})
}

// syncConnOptions: Verbindungen, die der Abgleich nutzen kann (SQL Server).
func (h *Handler) syncConnOptions(ctx context.Context) []syncOption {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, name, direction, enabled FROM import_export_connections
		WHERE kind = 'mssql' ORDER BY lower(name), direction`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []syncOption
	for rows.Next() {
		var id, name, dir string
		var enabled bool
		if rows.Scan(&id, &name, &dir, &enabled) != nil {
			continue
		}
		label := name + map[string]string{"import": " (Import)", "export": " (Export)"}[dir]
		if !enabled {
			label += " – deaktiviert"
		}
		out = append(out, syncOption{Value: id, Label: label})
	}
	return out
}

func syncOptionLabel(opts []syncOption, value string) string {
	for _, o := range opts {
		if o.Value == value {
			return o.Label
		}
	}
	return ""
}

// syncDeptOptions: Abteilungen, in denen die Person das Recht hat; global
// ("alle Abteilungen") nur, wenn sie es überall hat.
func (h *Handler) syncDeptOptions(r *http.Request, perm string) (opts []syncOption, allowGlobal bool) {
	if h.db == nil {
		return nil, true
	}
	allowGlobal = true
	limited := h.hasPermHook == nil && h.rbac != nil
	if limited {
		u := getUser(r)
		allowGlobal, _ = h.rbac.Scope(u.ID, string(u.Role), perm)
	}
	rows, err := h.db.Query(r.Context(), `SELECT id::text, name FROM departments WHERE active ORDER BY lower(name)`)
	if err != nil {
		return nil, allowGlobal
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) != nil {
			continue
		}
		if !allowGlobal && limited && !h.hasPermIn(r, perm, []string{id}) {
			continue
		}
		opts = append(opts, syncOption{Value: id, Label: name})
	}
	return opts, allowGlobal
}

func syncBack(id, tab, notice, errMsg string) string {
	q := url.Values{}
	if tab != "" {
		q.Set("tab", tab)
	}
	if notice != "" {
		q.Set("notice", notice)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	path := "/sync"
	if id != "" {
		path += "/" + id
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// ── Übersicht ────────────────────────────────────────────────────────────

type SyncListRow struct {
	ID, Name, Connection, Target    string
	State, StateLabel, StateColor   string
	StateNote                       string
	Open                            int
	LastRun, LastResult, LastReason string
	LastBad                         bool
}

type SyncPageData struct {
	BaseData
	Profiles    []SyncListRow
	Connections []syncOption
	Departments []syncOption
	AllowGlobal bool
	CanEdit     bool
	Notice, Err string
}

// syncRunSummary fasst einen Lauf in einem Satz zusammen.
func syncRunSummary(run extsync.Run) (text string, bad bool) {
	if run.SkippedReason != "" {
		return run.SkippedReason, !strings.HasPrefix(run.SkippedReason, extsync.SkippedOverload)
	}
	var parts []string
	add := func(n int, one string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, one))
		}
	}
	add(run.New, "neu")
	add(run.Backfilled, "rückwirkend")
	add(run.Changed, "geändert")
	add(run.Written, "zurückgeschrieben")
	add(run.Failed, "fehlgeschlagen")
	if len(parts) == 0 {
		return "keine Änderungen", false
	}
	return strings.Join(parts, ", "), run.Failed > 0
}

// SyncPage: GET /sync – Übersicht aller Profile.
func (h *Handler) SyncPage(w http.ResponseWriter, r *http.Request) {
	if !h.syncCan(r, permSyncView, "") {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	d := SyncPageData{
		BaseData: h.baseData(r, "sync", "Abgleich", "Abgleich mit Fremdsystemen"),
		CanEdit:  h.syncCan(r, permSyncEdit, ""),
		Notice:   r.URL.Query().Get("notice"), Err: r.URL.Query().Get("err"),
	}
	d.Connections = h.syncConnOptions(ctx)
	if d.CanEdit {
		d.Departments, d.AllowGlobal = h.syncDeptOptions(r, permSyncEdit)
	}
	store := h.syncStore()
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		d.Err = "Profile konnten nicht geladen werden"
	}
	for _, p := range profiles {
		if !h.syncCan(r, permSyncView, p.ID) {
			continue
		}
		row := SyncListRow{ID: p.ID, Name: p.Name, Target: syncTargetLabels[p.TargetType], State: p.State, StateNote: p.StateNote,
			Connection: syncOptionLabel(d.Connections, p.ConnectionID)}
		row.StateLabel, row.StateColor = syncStateLabel(p.State)
		row.Open, _ = store.OpenOutboxCount(ctx, p.ID)
		if runs, err := store.ListRuns(ctx, p.ID, 1); err == nil && len(runs) > 0 {
			at := runs[0].StartedAt
			if runs[0].FinishedAt != nil {
				at = *runs[0].FinishedAt
			}
			row.LastRun = at.Local().Format("02.01.2006 15:04")
			row.LastResult, row.LastBad = syncRunSummary(runs[0])
		}
		d.Profiles = append(d.Profiles, row)
	}
	sort.SliceStable(d.Profiles, func(i, j int) bool { return strings.ToLower(d.Profiles[i].Name) < strings.ToLower(d.Profiles[j].Name) })
	h.render(w, "sync", d)
}

// ── Cockpit ──────────────────────────────────────────────────────────────

type syncColumnRow struct {
	Name, Type, Note string
	Key              bool
}

// SyncColumnsView: Spalten der gewählten Tabelle mit Schlüssel,
// Änderungserkennung und Anlagedatum (Baustein „sync-columns“).
type SyncColumnsView struct {
	Table    string
	Columns  []syncColumnRow
	Changes  []syncOption
	Change   string
	TimeCols []string
	Created  string
	KeyHint  string
	Err      string
	ReadOnly bool
}

type SyncSourceView struct {
	Tables    []string
	Table     string
	Cols      *SyncColumnsView
	TimeZones []syncOption
}

type syncDefaultRow struct {
	Key, Label, Value string
	Options           []syncOption // leer = Freitext
	Required          bool
}

type SyncTargetView struct {
	Types       []syncOption
	Departments []syncOption
	AllowGlobal bool
	Department  string
	Defaults    []syncDefaultRow
}

type syncPair struct{ Foreign, PDH string }

type syncMapRow struct {
	Key, Label   string
	Required     bool
	Missing      bool // Pflichtfeld ohne Zuordnung und ohne Vorgabe
	Column       string
	Direction    string
	Fixed        string
	Values       []syncOption // Aufzählungswerte (leer = Freitext)
	Pairs        []syncPair
	OutLocked    string // Grund, warum hinaus/beides gesperrt ist
	ColumnExists bool
}

type SyncMapsView struct {
	Columns     []string
	Rows        []syncMapRow
	Dirs        []syncOption
	Tables      []string
	Comments    extsync.CommentTable
	HasComments bool
	CommentsOff string // Grund, warum Kommentare nicht möglich sind
	Missing     int
}

type SyncProfileData struct {
	BaseData
	P                      *extsync.Profile
	StateLabel, StateColor string
	ConnName, TargetLabel  string
	Tab, Notice, Err       string
	CanEdit, CanDelete     bool
	CanRun                 bool   // scharf schalten, anhalten, erneut versuchen (sync.run)
	MissingRequired        int    // Pflichtfelder ohne Zuordnung (Markierung am Schritt 3)
	Locked                 bool   // Verknüpfungen vorhanden: Quelle und Zielart sind fest
	ReadErr                string // Fremddatenbank nicht lesbar
	AllowWrite             bool
	Connections            []syncOption
	Source                 *SyncSourceView
	Target                 *SyncTargetView
	Maps                   *SyncMapsView
	Test                   *SyncTestView
	Ops                    *SyncOpsView
}

func syncColumnNote(c extsync.Column) string {
	var n []string
	if c.IsKey {
		n = append(n, "Primärschlüssel")
	}
	if c.IsRowVersion {
		n = append(n, "rowversion")
	}
	if c.IsIdentity {
		n = append(n, "fortlaufend")
	}
	if c.IsComputed {
		n = append(n, "berechnet")
	}
	if c.Nullable {
		n = append(n, "darf leer sein")
	}
	return strings.Join(n, ", ")
}

func syncColumnType(c extsync.Column) string {
	if c.MaxLen > 0 {
		return fmt.Sprintf("%s(%d)", c.Type, c.MaxLen)
	}
	return c.Type
}

// syncColumnsView baut den Spaltenblock. keys/change/created sind die
// gespeicherten Werte; fehlen sie, gilt die Vorgabe (Primärschlüssel, beste
// Änderungserkennung, kein Anlagedatum).
func syncColumnsView(d extsync.Dialect, t *extsync.TableInfo, keys []string, changeKind, changeCol, created string, fresh bool) *SyncColumnsView {
	v := &SyncColumnsView{Table: syncTableKey(t.Schema, t.Name), TimeCols: extsync.TimeColumns(d, t), Created: created}
	if fresh || len(keys) == 0 {
		keys = nil
		if len(t.UniqueKeys) > 0 {
			keys = t.UniqueKeys[0]
		}
	}
	isKey := map[string]bool{}
	for _, k := range keys {
		isKey[k] = true
	}
	for _, c := range t.Columns {
		v.Columns = append(v.Columns, syncColumnRow{Name: c.Name, Type: syncColumnType(c), Note: syncColumnNote(c), Key: isKey[c.Name]})
	}
	if len(t.UniqueKeys) == 0 {
		v.KeyHint = "Diese Tabelle hat weder Primärschlüssel noch eindeutigen Index – ohne eindeutigen Schlüssel ist kein Abgleich möglich."
	} else {
		var ks []string
		for _, k := range t.UniqueKeys {
			ks = append(ks, strings.Join(k, " + "))
		}
		v.KeyHint = "Eindeutige Schlüssel dieser Tabelle: " + strings.Join(ks, "; ")
	}
	opts := extsync.ChangeOptions(d, t)
	for _, o := range opts {
		label := "ohne Änderungsspalte (jede Zeile wird verglichen)"
		switch o.Kind {
		case "rowversion":
			label = o.Column + " (rowversion – empfohlen)"
		case "timestamp":
			label = o.Column + " (Änderungszeit)"
		}
		v.Changes = append(v.Changes, syncOption{Value: o.Kind + ":" + o.Column, Label: label})
	}
	v.Change = changeKind + ":" + changeCol
	if fresh || changeKind == "" {
		v.Change = opts[0].Kind + ":" + opts[0].Column
	}
	return v
}

// syncLocked: gibt es Verknüpfungen? Dann sind Quelle und Zielart fest.
func (h *Handler) syncLocked(ctx context.Context, profileID string) bool {
	var linked bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sync_links WHERE profile_id = $1)`, profileID).Scan(&linked)
	return linked
}

// syncProfile lädt das Profil der Anfrage und prüft das Recht. ok = false:
// die Antwort ist schon geschrieben.
func (h *Handler) syncProfile(w http.ResponseWriter, r *http.Request, perm string) (*extsync.Profile, bool) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return nil, false
	}
	if !h.syncCan(r, perm, id) {
		if r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" {
			http.Redirect(w, r, "/", http.StatusFound)
		} else {
			http.Error(w, "keine berechtigung", http.StatusForbidden)
		}
		return nil, false
	}
	p, err := h.syncStore().GetProfile(r.Context(), id)
	if err != nil {
		if errors.Is(err, extsync.ErrNotFound) {
			http.Error(w, "Profil nicht gefunden", http.StatusNotFound)
		} else {
			http.Error(w, "Profil konnte nicht geladen werden", http.StatusInternalServerError)
		}
		return nil, false
	}
	return p, true
}

// SyncProfilePage: GET /sync/{id} – Cockpit eines Profils.
func (h *Handler) SyncProfilePage(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncView)
	if !ok {
		return
	}
	d := h.syncProfileData(r, p, r.URL.Query().Get("tab"))
	d.Notice, d.Err = r.URL.Query().Get("notice"), r.URL.Query().Get("err")
	h.render(w, "sync_profile", d)
}

// syncBase: Seitenrahmen des Abgleichs (ohne Rechtedienst – nur in Tests – leer).
func (h *Handler) syncBase(r *http.Request, title string) BaseData {
	if h.rbac == nil {
		var none BaseData // nur in Tests: ohne Rechtedienst gibt es keinen Seitenkopf
		return none
	}
	return h.baseData(r, "sync", title, "Abgleich")
}

// syncTabs: Schritte des Cockpits; unbekannt = Quelle.
var syncTabs = map[string]bool{"source": true, "target": true, "maps": true, "test": true, "ops": true}

// syncProfileData baut die Daten des Cockpits für einen Schritt. Die
// Aktionen der Schritte Test und Betrieb ergänzen ihr Ergebnis und rendern
// dieselbe Seite.
func (h *Handler) syncProfileData(r *http.Request, p *extsync.Profile, tab string) SyncProfileData {
	ctx := r.Context()
	d := SyncProfileData{
		BaseData: h.syncBase(r, p.Name),
		P:        p, TargetLabel: syncTargetLabels[p.TargetType], Tab: tab,
		CanEdit: h.syncCan(r, permSyncEdit, p.ID), CanDelete: h.syncCan(r, permSyncAdmin, p.ID),
		CanRun:      h.syncCan(r, permSyncRun, p.ID),
		Locked:      h.syncLocked(ctx, p.ID),
		Connections: h.syncConnOptions(ctx),
	}
	d.StateLabel, d.StateColor = syncStateLabel(p.State)
	d.ConnName = syncOptionLabel(d.Connections, p.ConnectionID)
	if !syncTabs[d.Tab] {
		d.Tab = "source"
	}
	maps, _ := h.syncStore().ListMaps(ctx, p.ID)
	d.MissingRequired = len(extsync.MissingRequired(p, maps))

	// Aufbau der Fremddatenbank nur lesen, wo er gebraucht wird, und nur für Bearbeiter.
	var table *extsync.TableInfo
	var tables []extsync.TableInfo
	var dialect extsync.Dialect
	if d.CanEdit && (d.Tab == "source" || d.Tab == "maps") {
		rd, dl, cfg, closeFn, err := h.syncOpenReader(ctx, p.ConnectionID)
		d.AllowWrite = cfg["allow_write"] == "true"
		if err != nil {
			d.ReadErr = err.Error()
		} else {
			dialect = dl
			if tables, err = rd.Tables(ctx); err != nil {
				d.ReadErr = "Tabellen nicht lesbar: " + err.Error()
			}
			if p.SourceTable != "" && d.ReadErr == "" {
				if table, err = rd.Describe(ctx, p.SourceSchema, p.SourceTable); err != nil {
					d.ReadErr = fmt.Sprintf("Tabelle %s nicht lesbar: %v", syncTableKey(p.SourceSchema, p.SourceTable), err)
				}
			}
		}
		closeFn()
	} else {
		d.AllowWrite = syncAllowWrite(h.syncConnConfig(ctx, p.ConnectionID))
	}

	switch d.Tab {
	case "source":
		sv := &SyncSourceView{Table: syncTableKey(p.SourceSchema, p.SourceTable), TimeZones: syncTimeZones(p.TimeZone)}
		for _, t := range tables {
			sv.Tables = append(sv.Tables, syncTableKey(t.Schema, t.Name))
		}
		if table != nil {
			sv.Cols = syncColumnsView(dialect, table, p.KeyColumns, p.ChangeKind, p.ChangeColumn, p.CreatedColumn, false)
			sv.Cols.ReadOnly = d.Locked
		}
		d.Source = sv
	case "target":
		d.Target = h.syncTargetView(r, p)
	case "maps":
		d.Maps = syncMapsView(p, maps, table, tables, d.AllowWrite)
	case "test":
		d.Test = h.syncTestView(ctx, p, maps, d.AllowWrite, d.CanEdit)
	case "ops":
		d.Ops = h.syncOpsView(ctx, p, maps, d.AllowWrite)
	}
	return d
}

func (h *Handler) syncTargetView(r *http.Request, p *extsync.Profile) *SyncTargetView {
	tv := &SyncTargetView{Types: []syncOption{
		{Value: extsync.RefTicket, Label: syncTargetLabels[extsync.RefTicket]},
		{Value: extsync.RefFault, Label: syncTargetLabels[extsync.RefFault]},
	}}
	tv.Departments, tv.AllowGlobal = h.syncDeptOptions(r, permSyncEdit)
	if p.DepartmentID != nil {
		tv.Department = *p.DepartmentID
	}
	for _, f := range extsync.Fields(p.TargetType) {
		if f.Kind == "time" || f.Key == "resolution" || f.Key == "root_cause" {
			continue // Vorgaben nur für Titel, Beschreibung, Stufe und Status
		}
		row := syncDefaultRow{Key: f.Key, Label: f.Label, Value: p.Defaults[f.Key], Required: f.Required}
		if f.Kind == "enum" {
			row.Options = syncValueOptions(f)
		}
		tv.Defaults = append(tv.Defaults, row)
	}
	return tv
}

// syncPairSlots: so viele leere Zeilen für neue Übersetzungen zeigt das Formular.
const syncPairSlots = 3

func syncMapsView(p *extsync.Profile, maps []extsync.FieldMap, table *extsync.TableInfo, tables []extsync.TableInfo, allowWrite bool) *SyncMapsView {
	mv := &SyncMapsView{Dirs: syncDirOptions}
	if table != nil {
		for _, c := range table.Columns {
			mv.Columns = append(mv.Columns, c.Name)
		}
	} else {
		// Aufbau nicht gelesen (nur Ansicht oder Datenbank nicht erreichbar): die
		// gespeicherten Spalten anzeigen.
		for _, m := range maps {
			if m.ForeignColumn != "" {
				mv.Columns = append(mv.Columns, m.ForeignColumn)
			}
		}
	}
	for _, t := range tables {
		if table != nil && t.Schema == table.Schema && t.Name == table.Name {
			continue // die Meldungstabelle selbst ist nie Kommentartabelle
		}
		mv.Tables = append(mv.Tables, syncTableKey(t.Schema, t.Name))
	}
	byField := map[string]extsync.FieldMap{}
	for _, m := range maps {
		byField[m.PDHField] = m
	}
	missing := map[string]bool{}
	for _, f := range extsync.MissingRequired(p, maps) {
		missing[f.Key] = true
	}
	mv.Missing = len(missing)
	for _, f := range extsync.Fields(p.TargetType) {
		m := byField[f.Key]
		row := syncMapRow{Key: f.Key, Label: f.Label, Required: f.Required, Missing: missing[f.Key],
			Column: m.ForeignColumn, Direction: m.Direction}
		if row.Direction == "" {
			row.Direction = extsync.DirIn
		}
		if m.FixedValue != nil {
			row.Fixed = *m.FixedValue
		}
		if f.Kind == "enum" {
			row.Values = syncValueOptions(f)
		}
		var keys []string
		for k := range m.Translate {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			row.Pairs = append(row.Pairs, syncPair{Foreign: k, PDH: m.Translate[k]})
		}
		for i := 0; i < syncPairSlots; i++ {
			row.Pairs = append(row.Pairs, syncPair{})
		}
		row.OutLocked = extsync.OutLockReason(table, p, m.ForeignColumn, allowWrite)
		row.ColumnExists = m.ForeignColumn == ""
		for _, c := range mv.Columns {
			if c == m.ForeignColumn {
				row.ColumnExists = true
			}
		}
		mv.Rows = append(mv.Rows, row)
	}
	if p.CommentTable != nil {
		mv.Comments, mv.HasComments = *p.CommentTable, true
	}
	switch {
	case !allowWrite:
		mv.CommentsOff = "Schreiben ist an der Verbindung nicht erlaubt – Kommentare können nicht übertragen werden."
	case len(p.KeyColumns) != 1:
		mv.CommentsOff = "Kommentare gehen nur bei genau einer Schlüsselspalte."
	}
	return mv
}

// ── Bausteine für htmx ──────────────────────────────────────────────────

func (h *Handler) syncFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Immer auf einer Kopie ausführen: eine ausgeführte Vorlagensammlung lässt
	// sich nicht mehr kopieren, danach könnte keine Seite mehr gerendert werden.
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, name, data)
	}
	if err != nil {
		http.Error(w, "Template-Fehler: "+err.Error(), http.StatusInternalServerError)
	}
}

// SyncTablesFragment: GET /sync/{id}/tables – Tabellen der Verbindung als Auswahl.
func (h *Handler) SyncTablesFragment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	connID := p.ConnectionID
	if c := r.URL.Query().Get("connection_id"); c != "" && syncOptionLabel(h.syncConnOptions(r.Context()), c) != "" {
		connID = c
	}
	sv := &SyncSourceView{Table: syncTableKey(p.SourceSchema, p.SourceTable)}
	rd, _, _, closeFn, err := h.syncOpenReader(r.Context(), connID)
	defer closeFn()
	var tables []extsync.TableInfo
	if err == nil {
		tables, err = rd.Tables(r.Context())
	}
	if err != nil {
		h.syncFragment(w, "sync-tables", map[string]any{"ID": p.ID, "Err": err.Error(), "Table": sv.Table})
		return
	}
	for _, t := range tables {
		sv.Tables = append(sv.Tables, syncTableKey(t.Schema, t.Name))
	}
	h.syncFragment(w, "sync-tables", map[string]any{"ID": p.ID, "Tables": sv.Tables, "Table": sv.Table})
}

// syncDescribeChosen liest den Aufbau der im Formular gewählten Tabelle.
func (h *Handler) syncDescribeChosen(ctx context.Context, connID, key string) (*extsync.TableInfo, extsync.Dialect, syncReader, func(), error) {
	rd, d, _, closeFn, err := h.syncOpenReader(ctx, connID)
	if err != nil {
		return nil, nil, nil, closeFn, err
	}
	tables, err := rd.Tables(ctx)
	if err != nil {
		return nil, nil, nil, closeFn, errors.New("Tabellen nicht lesbar: " + err.Error())
	}
	schema, table, ok := syncFindTable(tables, key)
	if !ok {
		return nil, nil, nil, closeFn, fmt.Errorf("Tabelle ‚%s‘ gibt es in der Datenbank nicht", key)
	}
	t, err := rd.Describe(ctx, schema, table)
	if err != nil {
		return nil, nil, nil, closeFn, fmt.Errorf("Tabelle ‚%s‘ nicht lesbar: %v", key, err)
	}
	return t, d, rd, closeFn, nil
}

// SyncColumnsFragment: GET /sync/{id}/columns?table= – Spalten der gewählten Tabelle.
func (h *Handler) SyncColumnsFragment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	key := r.URL.Query().Get("table")
	if key == "" {
		h.syncFragment(w, "sync-columns", &SyncColumnsView{Err: "Bitte eine Tabelle wählen."})
		return
	}
	t, d, _, closeFn, err := h.syncDescribeChosen(r.Context(), p.ConnectionID, key)
	defer closeFn()
	if err != nil {
		h.syncFragment(w, "sync-columns", &SyncColumnsView{Err: err.Error()})
		return
	}
	same := t.Schema == p.SourceSchema && t.Name == p.SourceTable
	h.syncFragment(w, "sync-columns", syncColumnsView(d, t, p.KeyColumns, p.ChangeKind, p.ChangeColumn, p.CreatedColumn, !same))
}

// syncSampleMax: so viele Beispielzeilen und Spalten zeigt das Cockpit höchstens.
const (
	syncSampleRows = 20
	syncSampleCols = 12
	syncSampleText = 80
)

func syncSampleValue(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return "–"
	case []byte:
		s = "0x" + hex.EncodeToString(x)
	case time.Time:
		s = x.Format("2006-01-02 15:04:05")
	default:
		s = fmt.Sprint(x)
	}
	if r := []rune(s); len(r) > syncSampleText {
		s = string(r[:syncSampleText]) + "…"
	}
	return s
}

// SyncSampleFragment: GET /sync/{id}/sample?table= – die ersten 20 Zeilen.
func (h *Handler) SyncSampleFragment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	fail := func(msg string) { h.syncFragment(w, "sync-sample", map[string]any{"Err": msg}) }
	t, d, rd, closeFn, err := h.syncDescribeChosen(r.Context(), p.ConnectionID, r.URL.Query().Get("table"))
	defer closeFn()
	if err != nil {
		fail(err.Error())
		return
	}
	if len(t.UniqueKeys) == 0 {
		fail("Ohne eindeutigen Schlüssel lassen sich keine Zeilen lesen.")
		return
	}
	var cols []string
	for _, c := range t.Columns {
		if len(cols) < syncSampleCols {
			cols = append(cols, c.Name)
		}
	}
	st, err := d.SelectRows(t, cols, t.UniqueKeys[0], "", "none", "", "", 0, syncSampleRows)
	if err != nil {
		fail(err.Error())
		return
	}
	rows, err := rd.Query(r.Context(), st)
	if err != nil {
		fail("Zeilen nicht lesbar: " + err.Error())
		return
	}
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		line := make([]string, len(cols))
		for i, c := range cols {
			line[i] = syncSampleValue(row[c])
		}
		out = append(out, line)
	}
	h.syncFragment(w, "sync-sample", map[string]any{"Head": cols, "Rows": out, "More": len(t.Columns) > len(cols),
		"Table": syncTableKey(t.Schema, t.Name)})
}
