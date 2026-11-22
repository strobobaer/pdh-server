package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ESPPDH – Sammelaktion „Alle veralteten Geräte aktualisieren“ (0.82.24, zu esppdh_ota.go).
// Nimmt jedes Gerät, dessen Konfiguration sich seit dem letzten gelungenen Bau geändert hat oder
// das nicht die Kennung des letzten gelungenen Baus meldet (esppdhOutdated; eine neue
// ESPHome-Version allein zählt nicht), und spielt nacheinander auf – bei Bedarf wird vorher neu
// gebaut. Geräte ohne Update-Baustein, ohne bestätigte Adresse oder außerhalb eines
// freigegebenen Netzes werden mit Grund übersprungen; scheitert eines, geht es mit dem nächsten
// weiter. Abbrechen: das laufende Gerät wird fertig, der Rest abgebrochen. Nur wer esppdh.ota
// überall hat; eine Sammelaktion zur Zeit, nur am aktiven Server. Jedes Gerät bekommt einen
// Eintrag in esppdh_ota_runs mit derselben batch_id (die Übersicht zeigt den letzten Stand).
//
//	POST /import/esppdh/ota-all                  Bestätigung mit der Liste (htmx)
//	POST /import/esppdh/ota-all/start            Sammelaktion starten
//	POST /import/esppdh/ota-all/{batch}/cancel   abbrechen

// esppdhBatchPoll: so oft sieht die Sammelaktion nach, ob Bau oder Update fertig ist.
var esppdhBatchPoll = 2 * time.Second

// esppdhOutdated: Konfiguration seit dem letzten gelungenen Bau geändert, oder das Gerät meldet
// nicht dessen Kennung.
func esppdhOutdated(configHash, builtHash, reported, lastOK string) bool {
	return configHash == "" || configHash != builtHash || reported != lastOK
}

type esppdhBatchItem struct {
	DevID, Name, Reason string // Reason: warum es übersprungen wird ("" = es wird aufgespielt)
	NeedsBuild          bool
}

type esppdhBatchResult struct {
	DevID, Status, Note string
}

// esppdhBatchDeps: was die Sammelaktion braucht (Tests ersetzen alles). note trägt ein
// Ergebnis ein, das kein Update erzeugt hat (übersprungen, Bau gescheitert, abgebrochen).
type esppdhBatchDeps struct {
	list     func(ctx context.Context) []esppdhBatchItem
	build    func(ctx context.Context, devID string) (buildID string, err error)
	push     func(ctx context.Context, devID, buildID string) string
	note     func(ctx context.Context, devID, status, note string)
	canceled func() bool
}

const esppdhBatchCanceledMsg = "Sammelaktion abgebrochen."

// esppdhBatchRun arbeitet die Liste der Reihe nach ab.
func esppdhBatchRun(ctx context.Context, d esppdhBatchDeps) []esppdhBatchResult {
	items := d.list(ctx)
	out := make([]esppdhBatchResult, 0, len(items))
	stopped := false
	for _, it := range items {
		stopped = stopped || d.canceled() || ctx.Err() != nil
		switch {
		case stopped:
			d.note(ctx, it.DevID, "canceled", esppdhBatchCanceledMsg)
			out = append(out, esppdhBatchResult{it.DevID, "canceled", esppdhBatchCanceledMsg})
			continue
		case it.Reason != "":
			d.note(ctx, it.DevID, "skipped", it.Reason)
			out = append(out, esppdhBatchResult{it.DevID, "skipped", it.Reason})
			continue
		}
		bid := ""
		if it.NeedsBuild {
			b, err := d.build(ctx, it.DevID)
			if err != nil {
				msg := "Nicht aufgespielt: " + err.Error()
				d.note(ctx, it.DevID, "failed", msg)
				out = append(out, esppdhBatchResult{it.DevID, "failed", msg})
				continue
			}
			bid = b
		}
		out = append(out, esppdhBatchResult{DevID: it.DevID, Status: d.push(ctx, it.DevID, bid)})
	}
	return out
}

// ── Zustand ──────────────────────────────────────────────────

// esppdhBatchState: die laufende Sammelaktion an diesem Server.
type esppdhBatchState struct {
	mu      sync.Mutex
	id      string          // "" = keine
	cancel  bool            // „Abbrechen“ gedrückt
	pending map[string]bool // Geräte, die noch drankommen
}

func (h *Handler) esppdhBatchCanceled() bool {
	h.esppdhB.mu.Lock()
	defer h.esppdhB.mu.Unlock()
	return h.esppdhB.cancel
}

func (h *Handler) esppdhBatchDone(devID string) {
	h.esppdhB.mu.Lock()
	delete(h.esppdhB.pending, devID)
	h.esppdhB.mu.Unlock()
}

// esppdhCanBatch: esppdh.ota muss überall gelten – die Sammelaktion betrifft alle Geräte.
func (h *Handler) esppdhCanBatch(r *http.Request) bool {
	if h.esppdhOTAAllHook != nil {
		return h.esppdhOTAAllHook(r)
	}
	u := getUser(r)
	if h.rbac == nil || u == nil {
		return false
	}
	all, _ := h.rbac.Scope(u.ID, string(u.Role), permESPOTA)
	return all
}

// esppdhBatchBlocked: warum hier gerade keine Sammelaktion laufen kann ("" = es geht).
func (h *Handler) esppdhBatchBlocked(ctx context.Context) string {
	f := esppdhOTAFacts{Mirror: h.Mirror().On, Maint: h.MaintLocked(), Blocked: h.esppdhBuildBlocked(ctx)}
	switch {
	case f.Mirror:
		return "Aufspielen geht nur am aktiven Server – dies ist der Spiegel."
	case f.Maint:
		return "Während der Wartung wird keine Firmware aufgespielt."
	case f.Blocked != "":
		return f.Blocked
	}
	h.esppdhQ.mu.Lock()
	running := h.esppdhQ.ctx != nil
	h.esppdhQ.mu.Unlock()
	if !running {
		return "Der Arbeiter für Bauen und Aufspielen läuft an diesem Server nicht."
	}
	return ""
}

// esppdhBatchList: die veralteten Geräte, mit Grund, falls eines übersprungen wird.
func (h *Handler) esppdhBatchList(ctx context.Context) []esppdhBatchItem {
	list, err := h.esppdhList(ctx)
	if err != nil {
		componentLog("esppdh").Error().Err(err).Msg("geräte für die sammelaktion lesen")
		return nil
	}
	var out []esppdhBatchItem
	for _, x := range list {
		d, err := h.esppdhLoad(ctx, x.ID)
		if err != nil {
			continue
		}
		f := h.esppdhOTACheck(ctx, d)
		if !esppdhOutdated(f.CfgHash, f.LatestHash, f.Reported, f.LatestBuild) {
			continue
		}
		out = append(out, esppdhBatchItem{DevID: d.ID, Name: d.Name, Reason: esppdhOTADeviceReason(f),
			NeedsBuild: !f.Current || !f.FileOK || f.LatestBuild == ""})
	}
	return out
}

// ── Ablauf am Server ─────────────────────────────────────────

// esppdhBatchGo führt die Sammelaktion im Hintergrund aus (bis der Arbeiter endet).
func (h *Handler) esppdhBatchGo(ctx context.Context, batch string, items []esppdhBatchItem, by, reqHost string) {
	lg := componentLog("esppdh")
	defer func() {
		if p := recover(); p != nil {
			lg.Error().Interface("panic", p).Str("sammelaktion", batch).Msg("sammelaktion abgestürzt")
		}
		h.esppdhB.mu.Lock()
		h.esppdhB.id, h.esppdhB.cancel, h.esppdhB.pending = "", false, nil
		h.esppdhB.mu.Unlock()
	}()
	deps := esppdhBatchDeps{
		list:     func(context.Context) []esppdhBatchItem { return items },
		build:    func(ctx context.Context, devID string) (string, error) { return h.esppdhBatchBuild(ctx, devID, by, reqHost) },
		push:     func(ctx context.Context, devID, bid string) string { return h.esppdhBatchPush(ctx, batch, devID, bid, by) },
		note:     func(_ context.Context, devID, status, note string) { h.esppdhBatchNote(batch, devID, status, note, by) },
		canceled: h.esppdhBatchCanceled,
	}
	res := esppdhBatchRun(ctx, deps)
	n := map[string]int{}
	for _, r := range res {
		n[r.Status]++
	}
	lg.Info().Str("sammelaktion", batch).Int("geräte", len(res)).Int("aufgespielt", n["ok"]).Int("unbestätigt", n["unconfirmed"]).
		Int("fehlgeschlagen", n["failed"]).Int("übersprungen", n["skipped"]).Int("abgebrochen", n["canceled"]).Msg("sammelaktion beendet")
}

// esppdhBatchBuild baut ein Gerät neu und wartet auf das Ende des Baus.
func (h *Handler) esppdhBatchBuild(ctx context.Context, devID, by, reqHost string) (string, error) {
	d, err := h.esppdhLoad(ctx, devID)
	if err != nil {
		return "", errors.New("Das Gerät ließ sich nicht lesen.")
	}
	bid, conflict, err := h.esppdhQueueBuild(ctx, d, by, reqHost)
	switch {
	case conflict != "":
		return "", errors.New(conflict)
	case err != nil:
		return "", errors.New("Der Bau ließ sich nicht einreihen.")
	}
	if st := h.esppdhBatchWait(ctx, `SELECT status FROM esppdh_builds WHERE id = $1::uuid`, bid); st != "ok" {
		return "", errors.New("Der Bau ist nicht gelungen – der Verlauf steht auf der Geräteseite.")
	}
	return bid, nil
}

// esppdhBatchPush reiht das Update eines Geräts ein und wartet auf sein Ende.
func (h *Handler) esppdhBatchPush(ctx context.Context, batch, devID, bid, by string) string {
	defer h.esppdhBatchDone(devID)
	d, err := h.esppdhLoad(ctx, devID)
	if err != nil {
		h.esppdhBatchNote(batch, devID, "failed", "Das Gerät ließ sich nicht lesen.", by)
		return "failed"
	}
	f := h.esppdhOTACheck(ctx, d)
	if bid != "" && bid != f.LatestBuild {
		h.esppdhBatchNote(batch, devID, "failed", "Nicht aufgespielt: inzwischen gibt es einen neueren Bau.", by)
		return "failed"
	}
	if reason := esppdhOTAReason(f); reason != "" {
		h.esppdhBatchNote(batch, devID, "failed", "Nicht aufgespielt: "+reason, by)
		return "failed"
	}
	var rid string
	err = h.db.QueryRow(ctx, `INSERT INTO esppdh_ota_runs (device_id, build_id, batch_id, status, address, requested_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'queued', $4, $5::uuid) RETURNING id::text`, devID, f.LatestBuild, batch, f.Address, nullIfEmpty(by)).Scan(&rid)
	if err != nil {
		msg := "Das Update ließ sich nicht einreihen."
		if networkDup(err) {
			msg = errEsppdhOTAActive.Error()
		}
		h.esppdhBatchNote(batch, devID, "failed", msg, by)
		return "failed"
	}
	h.esppdhWake()
	return h.esppdhBatchWait(ctx, `SELECT status FROM esppdh_ota_runs WHERE id = $1::uuid`, rid)
}

// esppdhBatchWait fragt den Zustand eines Laufs ab, bis er nicht mehr wartet oder läuft.
func (h *Handler) esppdhBatchWait(ctx context.Context, q, id string) string {
	for {
		var st string
		if err := h.db.QueryRow(ctx, q, id).Scan(&st); err != nil {
			if ctx.Err() != nil {
				return "canceled"
			}
			return "failed"
		}
		if st != "queued" && st != "running" {
			return st
		}
		select {
		case <-ctx.Done():
			return "canceled"
		case <-time.After(esppdhBatchPoll):
		}
	}
}

// esppdhBatchNote trägt ein Ergebnis ohne Update ein (eigener Kontext – auch nach dem Abbruch).
func (h *Handler) esppdhBatchNote(batch, devID, status, note, by string) {
	h.esppdhBatchDone(devID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !meterUUIDRe.MatchString(by) {
		by = ""
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO esppdh_ota_runs (device_id, batch_id, status, log, requested_by, started_at, finished_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, now(), now())`, devID, batch, status, note, nullIfEmpty(by)); err != nil {
		componentLog("esppdh").Error().Err(err).Str("sammelaktion", batch).Str("gerät", devID).Msg("ergebnis eintragen")
	}
}

// ── Anzeige ──────────────────────────────────────────────────

// esppdhBatchView: Stand der Sammelaktion auf der Übersicht.
type esppdhBatchView struct {
	CanStart bool   // Knopf „Alle veralteten Geräte aktualisieren“
	Running  string // laufende Sammelaktion ("" = keine)
	Pending  int    // Geräte, die noch drankommen
	Last     string // letzte Sammelaktion (Stand je Gerät in der Tabelle)
}

// esppdhBatchOverview: Stand für die Übersicht – je Gerät der Eintrag der letzten Sammelaktion.
func (h *Handler) esppdhBatchOverview(ctx context.Context, r *http.Request) (esppdhBatchView, map[string]*esppdhOTARunView, map[string]bool) {
	var v esppdhBatchView
	if !h.esppdhCanBatch(r) || h.db == nil {
		return v, nil, nil
	}
	h.esppdhB.mu.Lock()
	v.Running, v.Pending = h.esppdhB.id, len(h.esppdhB.pending)
	pending := make(map[string]bool, len(h.esppdhB.pending))
	for k := range h.esppdhB.pending {
		pending[k] = true
	}
	h.esppdhB.mu.Unlock()
	v.CanStart = v.Running == "" && h.esppdhBatchBlocked(ctx) == ""
	_ = h.db.QueryRow(ctx, `SELECT batch_id::text FROM esppdh_ota_runs WHERE batch_id IS NOT NULL ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&v.Last)
	runs := map[string]*esppdhOTARunView{}
	if v.Last != "" {
		rows, err := h.db.Query(ctx, esppdhOTARunSelect+` WHERE o.batch_id = $1::uuid ORDER BY o.created_at, o.id`, v.Last)
		if err == nil {
			for rows.Next() {
				if o, err := esppdhScanOTARun(rows); err == nil {
					runs[o.DeviceID] = &o
				}
			}
			rows.Close()
		}
	}
	return v, runs, pending
}

// esppdhBatchConfirm: Bestätigung mit der Liste (htmx-Teil).
type esppdhBatchConfirm struct {
	Blocked string
	Items   []esppdhBatchItem
}

// EsppdhOTAAllWeb: POST /import/esppdh/ota-all – welche Geräte die Sammelaktion nähme.
func (h *Handler) EsppdhOTAAllWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhCanBatch(r) {
		http.Error(w, "keine berechtigung – das Recht „Firmware übers Netz aufspielen“ muss überall gelten", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	c := esppdhBatchConfirm{Blocked: h.esppdhBatchBlocked(ctx)}
	if c.Blocked == "" {
		c.Items = h.esppdhBatchList(ctx)
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderFragment(w, "esppdh-ota-all-confirm", c)
}

// EsppdhOTAAllStartWeb: POST /import/esppdh/ota-all/start – Sammelaktion starten.
func (h *Handler) EsppdhOTAAllStartWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhCanBatch(r) {
		http.Error(w, "keine berechtigung – das Recht „Firmware übers Netz aufspielen“ muss überall gelten", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if msg := h.esppdhBatchBlocked(ctx); msg != "" {
		http.Error(w, msg, http.StatusConflict)
		return
	}
	items := h.esppdhBatchList(ctx)
	if len(items) == 0 {
		http.Redirect(w, r, "/import/esppdh?n=ota-none", http.StatusSeeOther)
		return
	}
	id, err := uuid.NewV7()
	if err != nil {
		http.Error(w, "Die Sammelaktion ließ sich nicht starten.", http.StatusInternalServerError)
		return
	}
	batch := id.String()
	h.esppdhQ.mu.Lock()
	wctx := h.esppdhQ.ctx
	h.esppdhQ.mu.Unlock()
	b := &h.esppdhB
	b.mu.Lock()
	if b.id != "" {
		b.mu.Unlock()
		http.Error(w, "Es läuft schon eine Sammelaktion.", http.StatusConflict)
		return
	}
	b.id, b.cancel, b.pending = batch, false, map[string]bool{}
	for _, it := range items {
		b.pending[it.DevID] = true
	}
	b.mu.Unlock()
	by := getUser(r).ID
	go h.esppdhBatchGo(wctx, batch, items, by, r.Host)
	componentLog("esppdh").Info().Str("benutzer", by).Str("sammelaktion", batch).Int("geräte", len(items)).Msg("sammelaktion gestartet")
	http.Redirect(w, r, "/import/esppdh?n=ota-all", http.StatusSeeOther)
}

// EsppdhOTAAllCancelWeb: POST /import/esppdh/ota-all/{batch}/cancel – Rest abbrechen.
func (h *Handler) EsppdhOTAAllCancelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.esppdhCanBatch(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	batch := chi.URLParam(r, "batch")
	b := &h.esppdhB
	b.mu.Lock()
	ok := b.id != "" && b.id == batch
	if ok {
		b.cancel = true
	}
	b.mu.Unlock()
	if !ok {
		http.Error(w, "Diese Sammelaktion läuft nicht (mehr).", http.StatusNotFound)
		return
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("sammelaktion", batch).Msg("sammelaktion abgebrochen")
	http.Redirect(w, r, "/import/esppdh?n=ota-all-cancel", http.StatusSeeOther)
}
