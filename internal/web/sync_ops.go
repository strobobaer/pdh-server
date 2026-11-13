package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/extsync"
)

// Abgleich mit Fremdsystemen – Schritte 4 „Test“ und 5 „Betrieb“ des Cockpits
// und die Herkunft am Datensatz. Getestet wird mit den Funktionen aus
// internal/modules/extsync (DryStage, ProbeTriggers, ProbeWrite); scharf
// geschaltet allein über Store.Arm, das die bestandenen Teststufen prüft.

// ── Zugriff auf die Fremddatenbank für Test und Lauf ────────────────────

// syncOpenForeign öffnet die Fremddatenbank einer Verbindung. db ist in Tests
// (syncForeignHook) nil. closeFn ist nie nil; der Fehlertext ist für die Anzeige.
func (h *Handler) syncOpenForeign(ctx context.Context, connectionID string) (f extsync.Foreign, d extsync.Dialect, db *sql.DB, cfg map[string]string, closeFn func(), err error) {
	closeFn = func() {}
	if h.syncForeignHook != nil {
		f, d, cfg, err = h.syncForeignHook(ctx, connectionID)
		return f, d, nil, cfg, closeFn, err
	}
	kind, cfg, err := h.syncConn(ctx, connectionID)
	if err != nil {
		return nil, nil, nil, cfg, closeFn, err
	}
	if ok, why := h.connRouteReady(ctx, cfg); !ok {
		return nil, nil, nil, cfg, closeFn, errors.New("Weg nicht bereit: " + why)
	}
	if d, err = extsync.DialectFor(kind); err != nil {
		return nil, nil, nil, cfg, closeFn, err
	}
	if db, err = syncOpen(ctx, kind, cfg); err != nil {
		return nil, nil, nil, cfg, closeFn, errors.New("Datenbank nicht erreichbar: " + err.Error())
	}
	return extsync.NewSQLForeign(db, d, connQueryTimeout(cfg)), d, db, cfg, func() { _ = db.Close() }, nil
}

// syncConnConfig: Konfiguration der Verbindung (für „Schreiben erlaubt“ und den Takt).
func (h *Handler) syncConnConfig(ctx context.Context, connectionID string) map[string]string {
	if h.syncForeignHook != nil {
		_, _, cfg, _ := h.syncForeignHook(ctx, connectionID)
		return cfg
	}
	if h.syncReaderHook != nil {
		_, _, cfg, _ := h.syncReaderHook(ctx, connectionID)
		return cfg
	}
	_, cfg, _ := h.syncConn(ctx, connectionID)
	return cfg
}

func syncAllowWrite(cfg map[string]string) bool { return cfg["allow_write"] == "true" }

// ── Anzeige ──────────────────────────────────────────────────────────────

func syncTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("02.01.2006 15:04")
}

// syncKeyText: Schlüssel einer Meldung zur Anzeige („17“ oder „A / 4711“).
func syncKeyText(key string) string {
	kv, err := extsync.DecodeKey(key)
	if err != nil {
		return key
	}
	return strings.Join(kv, " / ")
}

func syncFieldLabels(target string) map[string]string {
	out := map[string]string{}
	for _, f := range extsync.Fields(target) {
		out[f.Key] = f.Label
	}
	return out
}

// syncValuesText: Feldwerte in Reihenfolge des Feldkatalogs als „Titel: …; Status: …“.
func syncValuesText(target string, vals map[string]string) string {
	var parts []string
	for _, f := range extsync.Fields(target) {
		v, ok := vals[f.Key]
		if !ok {
			continue
		}
		if l := syncValueLabels[v]; l != "" && f.Kind == "enum" {
			v = l
		}
		if r := []rune(v); len(r) > 120 {
			v = string(r[:120]) + "…"
		}
		if v == "" {
			v = "(leer)"
		}
		parts = append(parts, f.Label+": "+v)
	}
	return strings.Join(parts, " · ")
}

func syncErrTexts(target string, errs []extsync.RunError) []string {
	labels := syncFieldLabels(target)
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		t := e.Message
		if l := labels[e.Field]; l != "" && !strings.Contains(t, l) {
			t = l + ": " + t
		}
		if e.ForeignKey != "" {
			t = "Meldung " + syncKeyText(e.ForeignKey) + " – " + t
		}
		out = append(out, t)
	}
	return out
}

// ── Schritt 4: Test ──────────────────────────────────────────────────────

type syncProbeOption struct{ Key, Label string }

type syncSampleField struct {
	Key, Label, Value string
	Options           []syncOption
}

type syncPreviewRow struct {
	Key, Kind, Values string
	Errors            []string
}

// SyncDryView: Ergebnis des Trockenlaufs (Stufe 1).
type SyncDryView struct {
	Passed                             bool
	Error                              string
	New, Changed, Backfilled, Archived int
	Failed                             int
	Truncated                          bool
	Previews                           []syncPreviewRow
	NeedsOut                           bool
	OutSQL                             string
	OutParams                          []string
	OutValues                          string
	OutTruncated                       []string
	OutCommentSQL                      string
	OutCommentParams                   []string
	OutErrors                          []string
}

// SyncWriteView: Ergebnis des Schreibversuchs (Stufe 2).
type SyncWriteView struct {
	OK, RolledBack   bool
	Error            string
	Warnings         []string
	Statement        string
	Params           []string
	CommentStatement string
	CommentParams    []string
	Values, ReadBack string
	Truncated        []string
	Errors           []string
}

type SyncTestView struct {
	NeedsWrite  bool // das Profil schreibt ins Fremdsystem: Stufe 2 nötig
	AllowWrite  bool
	Missing     int // Pflichtfelder ohne Zuordnung
	DryAt       string
	WriteAt     string
	Rows        []syncProbeOption // Meldungen zur Auswahl für den Test hinaus
	RowsErr     string
	Key         string
	OutFields   []syncSampleField
	Dry         *SyncDryView
	Write       *SyncWriteView
	AskTriggers bool // vor dem Schreibversuch: Auslöser zeigen und nachfragen
	Triggers    []string
}

// syncOutFields: Felder mit hinausgehender Zuordnung (für Probewerte).
func syncOutFields(p *extsync.Profile, maps []extsync.FieldMap, sample map[string]string) []syncSampleField {
	by := map[string]extsync.FieldMap{}
	for _, m := range maps {
		by[m.PDHField] = m
	}
	var out []syncSampleField
	for _, f := range extsync.Fields(p.TargetType) {
		m, ok := by[f.Key]
		if !ok || m.ForeignColumn == "" || m.FixedValue != nil || (m.Direction != extsync.DirOut && m.Direction != extsync.DirBoth) {
			continue
		}
		sf := syncSampleField{Key: f.Key, Label: f.Label, Value: sample[f.Key]}
		if f.Kind == "enum" {
			sf.Options = syncValueOptions(f)
		}
		out = append(out, sf)
	}
	return out
}

func syncSampleFromForm(r *http.Request, target string) map[string]string {
	out := map[string]string{}
	for _, f := range extsync.Fields(target) {
		if v := strings.TrimSpace(r.FormValue("sample_" + f.Key)); v != "" {
			out[f.Key] = v
		}
	}
	return out
}

// syncProbeOptions liest die Meldungen, die für den Test zur Wahl stehen.
func syncProbeOptions(ctx context.Context, f extsync.Foreign, d extsync.Dialect, p *extsync.Profile, maps []extsync.FieldMap) ([]syncProbeOption, error) {
	rows, err := extsync.ProbeRows(ctx, f, d, p, maps, 0)
	if err != nil {
		return nil, err
	}
	out := make([]syncProbeOption, 0, len(rows))
	for _, row := range rows {
		label := syncKeyText(row.Key)
		if t := strings.TrimSpace(row.Values["title"]); t != "" {
			if r := []rune(t); len(r) > 60 {
				t = string(r[:60]) + "…"
			}
			label += " – " + t
		}
		out = append(out, syncProbeOption{Key: row.Key, Label: label})
	}
	return out, nil
}

func syncHasProbeKey(rows []syncProbeOption, key string) bool {
	for _, r := range rows {
		if r.Key == key {
			return true
		}
	}
	return false
}

// syncTestView baut den Schritt Test ohne Ergebnis.
func (h *Handler) syncTestView(ctx context.Context, p *extsync.Profile, maps []extsync.FieldMap, allowWrite, canEdit bool) *SyncTestView {
	tv := &SyncTestView{NeedsWrite: extsync.NeedsWriteTest(maps, p), AllowWrite: allowWrite,
		Missing: len(extsync.MissingRequired(p, maps)), DryAt: syncTime(p.TestDryAt), WriteAt: syncTime(p.TestWriteAt),
		OutFields: syncOutFields(p, maps, nil)}
	if canEdit && tv.NeedsWrite && p.SourceTable != "" {
		f, d, _, _, closeFn, err := h.syncOpenForeign(ctx, p.ConnectionID)
		if err == nil {
			tv.Rows, err = syncProbeOptions(ctx, f, d, p, maps)
		}
		closeFn()
		if err != nil {
			tv.RowsErr = err.Error()
		}
	}
	return tv
}

func syncDryView(p *extsync.Profile, res extsync.DryStageResult) *SyncDryView {
	v := &SyncDryView{Passed: res.Passed, Error: res.Error, NeedsOut: res.NeedsOutbound}
	if run := res.Inbound.Run; run != nil {
		v.New, v.Changed, v.Backfilled, v.Archived, v.Failed, v.Truncated = run.New, run.Changed, run.Backfilled, run.Archived, run.Failed, run.CountTruncated
		if v.Error == "" && run.SkippedReason != "" {
			v.Error = run.SkippedReason
		}
	}
	for _, pv := range res.Inbound.Previews {
		kind := "geändert"
		switch {
		case pv.IsNew && pv.Archive:
			kind = "rückwirkend, gleich erledigt und archiviert"
		case pv.IsNew && pv.Backfill:
			kind = "rückwirkend (still, ohne Benachrichtigung)"
		case pv.IsNew:
			kind = "neu"
		}
		v.Previews = append(v.Previews, syncPreviewRow{Key: syncKeyText(pv.ForeignKey), Kind: kind,
			Values: syncValuesText(p.TargetType, pv.Values), Errors: syncErrTexts(p.TargetType, pv.Errors)})
	}
	if o := res.Outbound; o != nil {
		v.OutSQL, v.OutParams, v.OutTruncated = o.SQL, o.Params, o.Truncated
		v.OutValues = syncValuesText(p.TargetType, o.Values)
		v.OutCommentSQL, v.OutCommentParams = o.CommentSQL, o.CommentParams
		v.OutErrors = syncErrTexts(p.TargetType, o.Errors)
	}
	return v
}

// syncRunDry fährt Stufe 1 (schreibt nirgends) und hält das Ergebnis am Profil fest.
func (h *Handler) syncRunDry(ctx context.Context, p *extsync.Profile, key string, sample map[string]string) (*SyncDryView, error) {
	store := h.syncStore()
	maps, err := store.ListMaps(ctx, p.ID)
	if err != nil {
		return nil, errors.New("Zuordnungen konnten nicht geladen werden")
	}
	f, d, _, _, closeFn, err := h.syncOpenForeign(ctx, p.ConnectionID)
	defer closeFn()
	if err != nil {
		return nil, err
	}
	if key != "" {
		rows, rerr := syncProbeOptions(ctx, f, d, p, maps)
		if rerr != nil {
			return nil, rerr
		}
		if !syncHasProbeKey(rows, key) {
			return nil, errors.New("Diese Meldung steht nicht zur Auswahl – bitte eine aus der Liste wählen")
		}
	}
	res := extsync.DryStage(ctx, h.syncInbound(), p, maps, f, d, key, sample)
	if err := store.MarkTest(ctx, p.ID, extsync.TestStageDry, res.Passed); err != nil {
		return nil, errors.New("Ergebnis des Trockenlaufs konnte nicht gespeichert werden")
	}
	return syncDryView(p, res), nil
}

// syncRunWrite fährt Stufe 2. Hat eine betroffene Tabelle Auslöser und ist
// noch nicht bestätigt (confirmed), kommt nur die Liste der Auslöser zurück
// (ask = true) – geschrieben wird dann nichts.
func (h *Handler) syncRunWrite(ctx context.Context, p *extsync.Profile, key string, sample map[string]string, confirmed bool) (triggers []string, ask bool, res *SyncWriteView, err error) {
	store := h.syncStore()
	maps, err := store.ListMaps(ctx, p.ID)
	if err != nil {
		return nil, false, nil, errors.New("Zuordnungen konnten nicht geladen werden")
	}
	if !extsync.NeedsWriteTest(maps, p) {
		return nil, false, nil, errors.New(extsync.ProbeNothingToWrite)
	}
	if key == "" {
		return nil, false, nil, errors.New("Bitte eine Meldung für den Schreibversuch wählen")
	}
	f, d, db, cfg, closeFn, err := h.syncOpenForeign(ctx, p.ConnectionID)
	defer closeFn()
	if err != nil {
		return nil, false, nil, err
	}
	if !syncAllowWrite(cfg) {
		return nil, false, nil, extsync.ErrWriteNotAllowed
	}
	rows, err := syncProbeOptions(ctx, f, d, p, maps)
	if err != nil {
		return nil, false, nil, err
	}
	if !syncHasProbeKey(rows, key) {
		return nil, false, nil, errors.New("Diese Meldung steht nicht zur Auswahl – bitte eine aus der Liste wählen")
	}
	if triggers, err = extsync.ProbeTriggers(ctx, f, p); err != nil {
		return nil, false, nil, err
	}
	if len(triggers) > 0 && !confirmed {
		return triggers, true, nil, nil
	}
	wp := extsync.ProbeWrite(ctx, db, d, p, maps, key, sample, true, connQueryTimeout(cfg))
	if err := store.MarkTest(ctx, p.ID, extsync.TestStageWrite, wp.OK); err != nil {
		return triggers, false, nil, errors.New("Ergebnis des Schreibversuchs konnte nicht gespeichert werden")
	}
	return triggers, false, &SyncWriteView{OK: wp.OK, RolledBack: wp.RolledBack, Error: wp.Error, Warnings: wp.Warnings,
		Statement: wp.Statement, Params: wp.Params, CommentStatement: wp.CommentStatement, CommentParams: wp.CommentParams,
		Values: syncValuesText(p.TargetType, wp.Values), ReadBack: syncValuesText(p.TargetType, wp.ReadBack),
		Truncated: wp.Truncated, Errors: syncErrTexts(p.TargetType, wp.Errors)}, nil
}

// syncRenderTest zeigt den Schritt Test mit Ergebnis (das Profil wird neu
// geladen, weil der Test den Zustand ändern kann).
func (h *Handler) syncRenderTest(w http.ResponseWriter, r *http.Request, id, key string, sample map[string]string, fill func(d *SyncProfileData)) {
	p, err := h.syncStore().GetProfile(r.Context(), id)
	if err != nil {
		http.Error(w, "Profil nicht gefunden", http.StatusNotFound)
		return
	}
	d := h.syncProfileData(r, p, "test")
	d.Test.Key = key
	maps, _ := h.syncStore().ListMaps(r.Context(), p.ID)
	d.Test.OutFields = syncOutFields(p, maps, sample)
	fill(&d)
	h.render(w, "sync_profile", d)
}

// SyncTestDryWeb: POST /sync/{id}/test-dry-web – Stufe 1, Trockenlauf.
func (h *Handler) SyncTestDryWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	_ = r.ParseForm()
	key, sample := r.FormValue("key"), syncSampleFromForm(r, p.TargetType)
	res, err := h.syncRunDry(r.Context(), p, key, sample)
	h.syncRenderTest(w, r, p.ID, key, sample, func(d *SyncProfileData) {
		if err != nil {
			d.Err = "Trockenlauf nicht möglich: " + err.Error()
			return
		}
		d.Test.Dry = res
	})
}

// SyncTestWriteWeb: POST /sync/{id}/test-write-web – Stufe 2, Schreibversuch mit Rücknahme.
func (h *Handler) SyncTestWriteWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	_ = r.ParseForm()
	key, sample := r.FormValue("key"), syncSampleFromForm(r, p.TargetType)
	triggers, ask, res, err := h.syncRunWrite(r.Context(), p, key, sample, r.FormValue("confirm") == "1")
	h.syncRenderTest(w, r, p.ID, key, sample, func(d *SyncProfileData) {
		d.Test.Triggers, d.Test.AskTriggers, d.Test.Write = triggers, ask, res
		if err != nil {
			d.Err = "Schreibversuch nicht möglich: " + err.Error()
		}
	})
}

// ── Schritt 5: Betrieb ───────────────────────────────────────────────────

type syncOutboxRow struct {
	ID                  int64
	Key, RefID, RefPath string
	Kind                string
	Attempts            int
	LastError, NextTry  string
	Since               string
}

type syncRunRow struct {
	When, Until, Direction, Summary string
	Bad, Waiting                    bool
	Errors                          []string
}

// syncArmConfirm: Rückfrage vor dem ersten Scharfschalten mit den Zahlen des Trockenlaufs.
type syncArmConfirm struct {
	New, Backfilled, Archived, Changed, Failed int
	Truncated, TooMany                         bool
	MaxNew                                     int
	Error                                      string
}

type SyncOpsView struct {
	Interval      int
	Effective     string // wirksamer Takt in Worten
	MinInterval   int
	MaxInterval   int
	NextRun       string
	Open          int
	Entries       []syncOutboxRow
	Runs          []syncRunRow
	ArmBlock      string // warum Scharfschalten gerade nicht geht ("" = möglich)
	Armed, Halted bool
	FirstArm      bool // noch nie scharf: vor dem Scharfschalten wird nachgefragt
	RunnerOn      bool
	Confirm       *syncArmConfirm
}

var syncDirLabels = map[string]string{extsync.DirIn: "herein", extsync.DirOut: "hinaus", "test": "Test"}

func syncRunRows(target string, runs []extsync.Run) []syncRunRow {
	out := make([]syncRunRow, 0, len(runs))
	for _, run := range runs {
		row := syncRunRow{When: run.StartedAt.Local().Format("02.01.2006 15:04"), Direction: syncDirLabels[run.Direction]}
		if run.FinishedAt != nil && run.FinishedAt.Sub(run.StartedAt) >= time.Minute {
			row.Until = run.FinishedAt.Local().Format("02.01. 15:04")
		}
		row.Summary, row.Bad = syncRunSummary(run)
		row.Waiting = strings.HasPrefix(run.SkippedReason, extsync.SkippedOverload)
		row.Errors = syncErrTexts(target, run.Errors)
		out = append(out, row)
	}
	return out
}

// syncArmBlock: der Grund, warum das Profil nicht scharf geschaltet werden kann.
func syncArmBlock(p *extsync.Profile, maps []extsync.FieldMap, allowWrite bool) string {
	needsWrite := extsync.NeedsWriteTest(maps, p)
	switch {
	case p.State == extsync.StateArmed:
		return ""
	case needsWrite && !allowWrite:
		return extsync.ErrWriteNotAllowed.Error()
	case p.TestDryAt == nil:
		return (&extsync.NotTestedError{Stage: extsync.TestStageDry}).Error()
	case needsWrite && p.TestWriteAt == nil:
		return (&extsync.NotTestedError{Stage: extsync.TestStageWrite}).Error()
	}
	return ""
}

func (h *Handler) syncOpsView(ctx context.Context, p *extsync.Profile, maps []extsync.FieldMap, allowWrite bool) *SyncOpsView {
	store := h.syncStore()
	cfg := h.syncConnConfig(ctx, p.ConnectionID)
	ov := &SyncOpsView{Interval: p.IntervalMinutes, MinInterval: extsync.MinIntervalMinutes, MaxInterval: extsync.MaxIntervalMinutes,
		NextRun: syncTime(p.NextRunAt), Armed: p.State == extsync.StateArmed, Halted: p.State == extsync.StateHalted,
		FirstArm: p.NotifyFrom == nil, RunnerOn: h.syncRunner != nil, ArmBlock: syncArmBlock(p, maps, allowWrite)}
	minutes := int(connPollInterval(cfg, p.IntervalMinutes) / time.Minute)
	ov.Effective = fmt.Sprintf("alle %d Minuten", minutes)
	if parseConnRoute(cfg).ViaVPN {
		ov.Effective = fmt.Sprintf("über VPN: alle %d Minuten (Einstellung der Verbindung)", minutes)
	}
	ov.Open, _ = store.OpenOutboxCount(ctx, p.ID)
	if entries, err := store.OpenOutbox(ctx, p.ID, 50); err == nil {
		for _, e := range entries {
			row := syncOutboxRow{ID: e.ID, Key: syncKeyText(e.ForeignKey), Attempts: e.Attempts, LastError: e.LastError,
				Since: e.CreatedAt.Local().Format("02.01. 15:04"), Kind: map[string]string{extsync.KindUpdate: "Änderung", extsync.KindComment: "Kommentar"}[e.Kind]}
			if e.Attempts > 0 {
				row.NextTry = e.NextTryAt.Local().Format("02.01. 15:04")
			}
			if e.RefID != nil {
				row.RefID = *e.RefID
				row.RefPath = map[string]string{extsync.RefTicket: "/tickets/", extsync.RefFault: "/faults/"}[p.TargetType] + *e.RefID
			}
			ov.Entries = append(ov.Entries, row)
		}
	}
	if runs, err := store.ListRuns(ctx, p.ID, 20); err == nil {
		ov.Runs = syncRunRows(p.TargetType, runs)
	}
	return ov
}

func (h *Handler) syncOpsFail(w http.ResponseWriter, r *http.Request, id, msg string) {
	http.Redirect(w, r, syncBack(id, "ops", "", msg), http.StatusSeeOther)
}

// SyncArmWeb: POST /sync/{id}/arm-web – scharf schalten (auch: fortsetzen nach Anhalten).
func (h *Handler) SyncArmWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncRun)
	if !ok {
		return
	}
	_ = r.ParseForm()
	ctx := r.Context()
	store := h.syncStore()
	maps, err := store.ListMaps(ctx, p.ID)
	if err != nil {
		h.syncOpsFail(w, r, p.ID, "Zuordnungen konnten nicht geladen werden")
		return
	}
	allowWrite := syncAllowWrite(h.syncConnConfig(ctx, p.ConnectionID))
	if why := syncArmBlock(p, maps, allowWrite); why != "" {
		h.syncOpsFail(w, r, p.ID, why)
		return
	}
	// Vor dem ersten Scharfschalten: zeigen, was der erste Lauf anlegen würde.
	if p.NotifyFrom == nil && p.State != extsync.StateArmed && r.FormValue("confirm") != "1" {
		conf := &syncArmConfirm{MaxNew: p.MaxNew}
		f, d, _, _, closeFn, oerr := h.syncOpenForeign(ctx, p.ConnectionID)
		if oerr != nil {
			conf.Error = oerr.Error()
		} else {
			res := extsync.DryInbound(ctx, h.syncInbound(), p, maps, f, d)
			conf.Error = res.Error
			if run := res.Run; run != nil {
				conf.New, conf.Backfilled, conf.Archived, conf.Changed, conf.Failed, conf.Truncated = run.New, run.Backfilled, run.Archived, run.Changed, run.Failed, run.CountTruncated
				conf.TooMany = run.New > p.MaxNew
			}
		}
		closeFn()
		d2 := h.syncProfileData(r, p, "ops")
		d2.Ops.Confirm = conf
		h.render(w, "sync_profile", d2)
		return
	}
	if err := store.Arm(ctx, p.ID, extsync.DayStart(time.Now(), time.Local)); err != nil {
		msg := "Scharfschalten fehlgeschlagen"
		if errors.Is(err, extsync.ErrNotTested) {
			msg = err.Error()
		}
		h.syncOpsFail(w, r, p.ID, msg)
		return
	}
	componentLog("abgleich").Info().Str("profil", p.Name).Str("benutzer", getUser(r).ID).Msg("profil scharf geschaltet")
	http.Redirect(w, r, syncBack(p.ID, "ops", "Profil ist scharf geschaltet – der Abgleich läuft im Takt", ""), http.StatusSeeOther)
}

// SyncPauseWeb: POST /sync/{id}/pause-web – anhalten. Warteliste und
// Verknüpfungen bleiben; nach dem Fortsetzen wird nachgeholt.
func (h *Handler) SyncPauseWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncRun)
	if !ok {
		return
	}
	if p.State != extsync.StateArmed {
		h.syncOpsFail(w, r, p.ID, "Nur ein scharfes Profil kann angehalten werden")
		return
	}
	if err := h.syncStore().SetState(r.Context(), p.ID, extsync.StateHalted, "Von Hand angehalten"); err != nil {
		h.syncOpsFail(w, r, p.ID, "Anhalten fehlgeschlagen")
		return
	}
	componentLog("abgleich").Info().Str("profil", p.Name).Str("benutzer", getUser(r).ID).Msg("profil angehalten")
	http.Redirect(w, r, syncBack(p.ID, "ops", "Profil angehalten", ""), http.StatusSeeOther)
}

// SyncRunWeb: POST /sync/{id}/run-web – jetzt abgleichen (ein hereinkommender Lauf).
func (h *Handler) SyncRunWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncRun)
	if !ok {
		return
	}
	if h.syncRunner == nil {
		h.syncOpsFail(w, r, p.ID, "Auf diesem Server läuft der Abgleich nicht (Spiegel)")
		return
	}
	run, err := h.syncRunner.RunNow(r.Context(), p.ID)
	switch {
	case errors.Is(err, extsync.ErrNotArmed), errors.Is(err, extsync.ErrMaintenance):
		h.syncOpsFail(w, r, p.ID, err.Error())
	case err != nil:
		h.syncOpsFail(w, r, p.ID, "Lauf fehlgeschlagen: "+err.Error())
	case run == nil:
		h.syncOpsFail(w, r, p.ID, "Lauf fehlgeschlagen")
	default:
		text, bad := syncRunSummary(*run)
		if bad {
			h.syncOpsFail(w, r, p.ID, "Lauf: "+text)
			return
		}
		http.Redirect(w, r, syncBack(p.ID, "ops", "Lauf beendet: "+text, ""), http.StatusSeeOther)
	}
}

// SyncIntervalWeb: POST /sync/{id}/interval-web – Takt (5 bis 30 Minuten) speichern.
func (h *Handler) SyncIntervalWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	_ = r.ParseForm()
	p.IntervalMinutes = syncFormInt(r, "interval_minutes", extsync.DefaultIntervalMinutes, extsync.MinIntervalMinutes, extsync.MaxIntervalMinutes)
	if err := h.syncStore().SaveProfile(r.Context(), p, getUser(r).ID); err != nil {
		h.syncOpsFail(w, r, p.ID, syncUserMessage(err))
		return
	}
	http.Redirect(w, r, syncBack(p.ID, "ops", fmt.Sprintf("Takt gespeichert: alle %d Minuten", p.IntervalMinutes), ""), http.StatusSeeOther)
}

// SyncRetryWeb: POST /sync/{id}/outbox/{n}/retry-web – einen wartenden Eintrag sofort erneut versuchen.
func (h *Handler) SyncRetryWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncRun)
	if !ok {
		return
	}
	n, err := strconv.ParseInt(chi.URLParam(r, "n"), 10, 64)
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	if err := h.syncStore().OutboxRetryNowIn(r.Context(), p.ID, n); err != nil {
		h.syncOpsFail(w, r, p.ID, "Eintrag nicht gefunden oder schon erledigt")
		return
	}
	if h.syncRunner != nil {
		h.syncRunner.Kick()
	}
	http.Redirect(w, r, syncBack(p.ID, "ops", "Eintrag wird sofort erneut versucht", ""), http.StatusSeeOther)
}

// SyncRunsFragment: GET /sync/{id}/runs – die letzten 20 Läufe (htmx).
func (h *Handler) SyncRunsFragment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncView)
	if !ok {
		return
	}
	runs, err := h.syncStore().ListRuns(r.Context(), p.ID, 20)
	if err != nil {
		http.Error(w, "Läufe konnten nicht geladen werden", http.StatusInternalServerError)
		return
	}
	h.syncFragment(w, "sync-runs", syncRunRows(p.TargetType, runs))
}

// ── Herkunft am Datensatz ────────────────────────────────────────────────

// SyncOriginView: woher ein Ticket oder eine Störmeldung stammt und wie der
// Abgleich steht (Baustein „sync-origin“).
type SyncOriginView struct {
	ProfileID, Profile, Key string
	Status                  string
	Open                    int
	Bad                     bool
}

// syncOrigin liefert die Herkunft eines Datensatzes – nur mit dem Recht
// sync.view für das Profil; sonst und ohne Verknüpfung nil.
func (h *Handler) syncOrigin(r *http.Request, refType, refID string) *SyncOriginView {
	if h.db == nil || refID == "" || !h.syncCan(r, permSyncView, "") {
		return nil
	}
	ctx := r.Context()
	store := h.syncStore()
	links, err := store.LinksByRef(ctx, refType, refID)
	if err != nil || len(links) == 0 {
		return nil
	}
	l := links[0]
	if !h.syncCan(r, permSyncView, l.ProfileID) {
		return nil
	}
	p, err := store.GetProfile(ctx, l.ProfileID)
	if err != nil {
		return nil
	}
	v := &SyncOriginView{ProfileID: p.ID, Profile: p.Name, Key: syncKeyText(l.ForeignKey), Status: "aktuell"}
	v.Open, _ = store.LinkOpenCount(ctx, l.ID)
	switch {
	case l.LastError != "" && l.LastError != extsync.LinkErrBackfillWaiting:
		v.Status, v.Bad = "Fehler: "+l.LastError, true
	case v.Open > 0:
		v.Status = fmt.Sprintf("%d Änderung(en) warten auf das Zurückschreiben", v.Open)
	case p.State != extsync.StateArmed:
		label, _ := syncStateLabel(p.State)
		v.Status = "Abgleich ruht (Profil: " + label + ")"
	}
	return v
}
