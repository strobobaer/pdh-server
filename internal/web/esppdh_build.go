package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/core/listsort"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – Firmware bauen (zu esppdh.go). Der Knopf „Firmware bauen“ auf der Detailseite reiht
// einen Bau ein; genau ein Arbeiter (StartESPPDH – nur am aktiven Server, nicht im
// Docker-Betrieb) baut die Aufträge der Reihe nach mit ESPHome (esppdh.Runner: niedrige
// Priorität, Zeitlimit, Platzprüfung, Arbeitsordner wird immer gelöscht).
//
//	POST /import/esppdh/{id}/build               Bau einreihen (esppdh.edit am Gerät)
//	GET  /import/esppdh/{id}/build/{bid}         Verlauf eines Laufs als htmx-Teil (Ansehen des Geräts)
//	POST /import/esppdh/{id}/build/{bid}/cancel  wartenden oder laufenden Bau abbrechen (esppdh.edit)
//
// Die Adresse des Brokers wird beim Einreihen festgelegt und am Lauf gespeichert (Migration
// 180) – ohne Namen des Brokers hinge sie sonst von der Adresse ab, unter der das PDH gerade
// aufgerufen ist; Loopback-Adressen lehnt der Knopf ab. Die Firmware liegt verschlüsselt
// (AES-256-GCM mit esppdhKey, esppdh_firmware.go) unter uploads/esppdh/<gerät>/<lauf>.bin.enc (zum ersten Flashen)
// und <lauf>.ota.bin.enc (für Updates über das Netz); je Gerät bleiben die Dateien der letzten
// drei gelungenen Läufe. Der Verlauf steht geschwärzt in esppdh_builds.log (alle 2 s ein
// Zwischenstand) und ist auf der Detailseite zu sehen: Abfrage alle 2 s, solange der Lauf
// wartet oder läuft.

// esppdhBuilder baut eine Firmware (esppdh.Runner; Tests setzen eine Attrappe ein).
type esppdhBuilder interface {
	Build(ctx context.Context, job esppdh.BuildJob, progress func(log string)) esppdh.BuildResult
}

// esppdhQueue: Zustand des einen Arbeiters. Das Übernehmen eines Auftrags (queued → running)
// und das Abbrechen laufen unter mu – so sieht ein Abbruch immer, ob der Lauf hier baut.
type esppdhQueue struct {
	mu        sync.Mutex
	stop      context.CancelFunc // beendet den Arbeiter (StopESPPDH)
	done      chan struct{}      // geschlossen, sobald der Arbeiter beendet ist
	wake      chan struct{}      // neuer Auftrag
	running   string             // Lauf, der gerade baut
	runDev    string             // sein Gerät
	cancelRun context.CancelFunc // bricht den laufenden Bau ab
	byUser    bool               // … weil jemand „Abbrechen“ gedrückt oder das Gerät gelöscht hat
	// runMu: gehalten, solange ESPHome läuft – vom Arbeiter vom Übernehmen eines Auftrags bis zum
	// Ende des Baus (esppdhRunNext) wie von der Probe einer neuen Version (esppdh_update.go). So
	// laufen nie zwei ESPHome-Aufrufe zugleich.
	runMu sync.Mutex
}

const (
	esppdhListRuns = 10               // Läufe auf der Detailseite
	esppdhIdlePoll = 30 * time.Second // ohne Weckruf: so oft nach Aufträgen sehen
	esppdhStopWait = 20 * time.Second // Herunterfahren wartet höchstens so lange auf den Arbeiter
)

// esppdhRunPoll: Baut gerade eine Probe (runMu belegt), sieht der Arbeiter so oft nach, ob er
// wieder darf (Tests verkürzen es).
var esppdhRunPoll = 2 * time.Second

var (
	errEsppdhNoBuild     = errors.New("Diesen Lauf gibt es nicht.")
	errEsppdhBuildActive = errors.New("Für dieses Gerät wartet oder läuft schon ein Bau.")
)

// ── Arbeiter ─────────────────────────────────────────────────

// StartESPPDH startet den Arbeiter, der eingereihte Bauten nacheinander baut. Zuerst – auf
// jedem Server, auch am Spiegel und im Docker-Betrieb – löscht es Arbeitsordner eines Laufs,
// den ein Absturz unterbrochen hat: Sie enthalten secrets.yaml und den erzeugten Quelltext mit
// den Kennwörtern (nach einer Umschaltung kommt der abgestürzte Server als Spiegel zurück). Das
// schreibt nichts in die Datenbank. Den Arbeiter gibt es nicht am Spiegel (dort wird nicht
// gebaut), nicht im Docker-Betrieb und nicht ohne Datenbank. Läufe, die beim letzten Beenden
// noch liefen, gelten als gescheitert.
func (h *Handler) StartESPPDH(ctx context.Context) {
	lg := componentLog("esppdh")
	h.StopESPPDH() // ein Arbeiter je Server: einen früheren zuerst beenden (dann baut nichts mehr)
	if err := esppdh.CleanupBuilds(); err != nil {
		lg.Error().Err(err).Msg("alte arbeitsordner löschen")
	}
	if h.db == nil || h.Mirror().On || inDocker() {
		lg.Info().Bool("spiegel", h.Mirror().On).Bool("docker", inDocker()).Msg("bauen nicht gestartet")
		return
	}
	runner := h.esppdhRunner
	if runner == nil {
		runner = esppdh.NewRunner()
	}
	wctx, stop := context.WithCancel(ctx)
	done, wake := make(chan struct{}), make(chan struct{}, 1)
	q := &h.esppdhQ
	q.mu.Lock()
	q.stop, q.done, q.wake = stop, done, wake
	q.mu.Unlock()

	sctx, cancel := context.WithTimeout(wctx, 10*time.Second)
	tag, err := h.db.Exec(sctx, `UPDATE esppdh_builds SET status = 'failed', finished_at = now(),
			log = CASE WHEN log = '' THEN $1 ELSE log || E'\n' || $1 END
		WHERE status = 'running'`, "PDH wurde während des Baus neu gestartet – bitte neu bauen.")
	cancel()
	switch {
	case err != nil:
		lg.Error().Err(err).Msg("abgebrochene läufe beenden")
	case tag.RowsAffected() > 0:
		lg.Warn().Int64("anzahl", tag.RowsAffected()).Msg("läufe vom letzten start als gescheitert markiert")
	}
	h.esppdhOTAFailRunning(wctx)
	up := h.esppdhUpload
	if up == nil {
		up = esppdh.NewRunner().Upload
	}
	go h.esppdhWorker(wctx, runner, up, wake, done)
	lg.Info().Msg("bauen gestartet")
}

// StopESPPDH beendet den Arbeiter (Herunterfahren): Ein laufender Bau wird abgebrochen und als
// gescheitert eingetragen. Wartet, bis der Arbeiter fertig ist (höchstens esppdhStopWait).
func (h *Handler) StopESPPDH() {
	q := &h.esppdhQ
	q.mu.Lock()
	stop, done := q.stop, q.done
	q.stop, q.done, q.wake = nil, nil, nil
	q.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	select {
	case <-done:
	case <-time.After(esppdhStopWait):
		componentLog("esppdh").Warn().Msg("arbeiter endet nicht rechtzeitig")
	}
}

// esppdhWake weckt den Arbeiter (neuer Auftrag).
func (h *Handler) esppdhWake() {
	h.esppdhQ.mu.Lock()
	w := h.esppdhQ.wake
	h.esppdhQ.mu.Unlock()
	if w != nil {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

func (h *Handler) esppdhWorker(ctx context.Context, runner esppdhBuilder, up esppdhUploadFunc, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for ctx.Err() == nil {
		// zuerst bauen, dann aufspielen (esppdh_ota.go) – beides nacheinander unter runMu
		if h.esppdhRunNext(ctx, runner) || h.esppdhOTANext(ctx, up) {
			continue
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-time.After(esppdhIdlePoll):
		}
	}
}

// esppdhClaim: ein übernommener Auftrag mit der beim Einreihen festgelegten Broker-Adresse.
type esppdhClaim struct {
	ID, DeviceID, Host string
	Port               int
	TLS                bool
}

// esppdhRunNext übernimmt den ältesten wartenden Auftrag und baut ihn. false: keiner da. Vorher
// prüft es erneut, ob hier gebaut werden darf – ist ESPPDH inzwischen aus oder fehlt die
// Umgebung, werden wartende Aufträge mit Hinweis abgebrochen statt gestartet.
func (h *Handler) esppdhRunNext(ctx context.Context, runner esppdhBuilder) bool {
	switch msg, err := h.esppdhWorkerBlocked(ctx); {
	case err != nil:
		if ctx.Err() == nil {
			componentLog("esppdh").Warn().Err(err).Msg("schalter lesen – später erneut")
		}
		return false
	case msg != "":
		h.esppdhCancelQueued(ctx, "Nicht gebaut: "+msg)
		return false
	}
	// erst runMu, dann übernehmen: Baut gerade eine Probe, bleibt der Auftrag „wartet“ und lässt
	// sich sofort abbrechen
	if !h.esppdhLockRun(ctx) {
		return false
	}
	defer h.esppdhQ.runMu.Unlock()
	q := &h.esppdhQ
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var c esppdhClaim
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	q.mu.Lock()
	err := h.db.QueryRow(cctx, `UPDATE esppdh_builds SET status = 'running', started_at = now()
		WHERE id = (SELECT id FROM esppdh_builds WHERE status = 'queued' ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id::text, device_id::text, broker_host, broker_port, broker_tls`).Scan(&c.ID, &c.DeviceID, &c.Host, &c.Port, &c.TLS)
	if err == nil {
		q.running, q.runDev, q.cancelRun, q.byUser = c.ID, c.DeviceID, cancel, false
	}
	q.mu.Unlock()
	ccancel()
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			componentLog("esppdh").Error().Err(err).Msg("auftrag übernehmen")
		}
		return false
	}
	defer func() {
		q.mu.Lock()
		q.running, q.runDev, q.cancelRun, q.byUser = "", "", nil, false
		q.mu.Unlock()
	}()
	h.esppdhRunBuild(ctx, bctx, runner, c)
	return true
}

// esppdhLockRun wartet auf runMu (siehe esppdhRunPoll). false: ctx endete vorher (Herunterfahren).
func (h *Handler) esppdhLockRun(ctx context.Context) bool {
	for {
		if h.esppdhQ.runMu.TryLock() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(esppdhRunPoll):
		}
	}
}

// esppdhRunBuild baut einen übernommenen Auftrag und trägt das Ergebnis ein (der Aufrufer hält
// runMu). ctx endet beim Herunterfahren, bctx zusätzlich beim Abbrechen.
func (h *Handler) esppdhRunBuild(ctx, bctx context.Context, runner esppdhBuilder, c esppdhClaim) {
	lg := componentLog("esppdh")
	var job esppdh.BuildJob
	var res esppdh.BuildResult
	defer func() {
		if p := recover(); p != nil {
			lg.Error().Interface("panic", p).Str("lauf", c.ID).Msg("bau abgestürzt")
			h.esppdhFinish(c.ID, "failed", esppdh.Redact(res.Log+"\nInterner Fehler beim Bau – Näheres steht im Server-Protokoll.", job.SecretValues), res.Version)
		}
	}()
	job, hash, err := h.esppdhJobFor(bctx, c)
	if err != nil {
		lg.Warn().Str("lauf", c.ID).Str("fehler", err.Error()).Msg("bau nicht möglich")
		h.esppdhFinish(c.ID, "failed", err.Error(), "")
		h.esppdhPrune(c.DeviceID, false)
		return
	}
	_, _ = h.db.Exec(bctx, `UPDATE esppdh_builds SET config_hash = $2 WHERE id = $1::uuid`, c.ID, hash)
	lg.Info().Str("lauf", c.ID).Str("gerät", job.Slug).Msg("bau beginnt")
	res = runner.Build(bctx, job, func(log string) { h.esppdhSaveLog(bctx, c.ID, log) })

	h.esppdhQ.mu.Lock()
	byUser := h.esppdhQ.byUser
	h.esppdhQ.mu.Unlock()
	log := strings.TrimRight(res.Log, "\n")
	switch {
	case byUser:
		h.esppdhFinish(c.ID, "canceled", esppdh.Redact(log+"\nVon Hand abgebrochen.", job.SecretValues), res.Version)
	case ctx.Err() != nil:
		h.esppdhFinish(c.ID, "failed", esppdh.Redact(log+"\nPDH wurde während des Baus beendet – bitte neu bauen.", job.SecretValues), res.Version)
	case !res.OK:
		h.esppdhFinish(c.ID, "failed", esppdh.Redact(log, job.SecretValues), res.Version)
	default:
		if err := h.esppdhStoreFirmware(c.DeviceID, c.ID, res.Factory, res.OTA); err != nil {
			lg.Error().Err(err).Str("lauf", c.ID).Msg("firmware speichern")
			h.esppdhRemoveFirmware(c.DeviceID, c.ID)
			h.esppdhFinish(c.ID, "failed", esppdh.Redact(log+"\nDie Firmware ließ sich nicht speichern. Näheres steht im Server-Protokoll.", job.SecretValues), res.Version)
			break
		}
		if err := h.esppdhFinishOK(c, hash, res, esppdh.Redact(log, job.SecretValues)); err != nil {
			// Gerät gelöscht oder Lauf inzwischen beendet: keine Firmware ohne Lauf liegen lassen
			h.esppdhRemoveFirmware(c.DeviceID, c.ID)
			if !errors.Is(err, errEsppdhNoBuild) {
				lg.Error().Err(err).Str("lauf", c.ID).Msg("ergebnis eintragen")
				h.esppdhFinish(c.ID, "failed", esppdh.Redact(log+"\nDas Ergebnis ließ sich nicht eintragen. Näheres steht im Server-Protokoll.", job.SecretValues), res.Version)
			}
			break
		}
		h.esppdhPrune(c.DeviceID, true)
		lg.Info().Str("lauf", c.ID).Str("gerät", job.Slug).Int("größe", len(res.Factory)).Msg("firmware gebaut")
		return
	}
	h.esppdhPrune(c.DeviceID, false)
	lg.Info().Str("lauf", c.ID).Str("gerät", job.Slug).Bool("von_hand", byUser).Msg("bau ohne firmware beendet")
}

// esppdhQueueIdle: wartet und läuft kein Bau? Ist das nicht lesbar, gilt die Schlange als belegt.
func (h *Handler) esppdhQueueIdle(ctx context.Context) bool {
	if h.db == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var busy bool
	err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM esppdh_builds WHERE status IN ('queued', 'running'))`).Scan(&busy)
	return err == nil && !busy
}

// esppdhJobFor setzt den Auftrag aus dem Gerät zusammen, wie es jetzt gespeichert ist, für die
// beim Einreihen festgelegte Broker-Adresse. Haben sich die Einstellungen des Brokers seitdem
// geändert, baut das PDH nicht (die Seite zeigt die neue Adresse; neu bauen).
func (h *Handler) esppdhJobFor(ctx context.Context, c esppdhClaim) (esppdh.BuildJob, string, error) {
	d, err := h.esppdhLoad(ctx, c.DeviceID)
	if err != nil {
		if errors.Is(err, errEsppdhNotFound) {
			return esppdh.BuildJob{}, "", err
		}
		return esppdh.BuildJob{}, "", errors.New("Das Gerät ließ sich nicht lesen. Näheres steht im Server-Protokoll.")
	}
	mqttPW, apPW, err := h.esppdhSecrets(d)
	if err != nil {
		return esppdh.BuildJob{}, "", err
	}
	res, msgs := h.esppdhResolve(ctx, d, mqttPW, apPW, c.Host)
	if len(msgs) == 0 {
		msgs = esppdh.ValidateDevice(res.Dev)
	}
	if len(msgs) > 0 {
		return esppdh.BuildJob{}, "", errors.New("Die Firmware lässt sich nicht bauen: " + strings.Join(msgs, " "))
	}
	if b := res.Broker; b.Host != c.Host || b.Port != c.Port || b.TLS != c.TLS {
		return esppdh.BuildJob{}, "", fmt.Errorf("Die Einstellungen des Brokers haben sich seit dem Einreihen geändert (jetzt %s) – bitte neu bauen.",
			esppdhAddr(b.Host, b.Port, b.TLS))
	}
	res.Dev.FirmwareID = c.ID // das Gerät meldet die Kennung seines Baus (Thema info/firmware)
	cfg, sec, err := esppdh.Generate(res.Dev)
	if err != nil {
		return esppdh.BuildJob{}, "", err
	}
	return esppdh.BuildJob{ID: c.ID, Slug: d.Slug, Config: cfg, Secrets: sec, SecretValues: res.Dev.SecretValues()}, esppdh.ConfigHash(res.Dev), nil
}

// esppdhSaveLog schreibt einen Zwischenstand (schon geschwärzt) in den Lauf.
func (h *Handler) esppdhSaveLog(ctx context.Context, id, log string) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := h.db.Exec(ctx, `UPDATE esppdh_builds SET log = $2 WHERE id = $1::uuid AND status = 'running'`, id, log); err != nil && ctx.Err() == nil {
		componentLog("esppdh").Warn().Err(err).Str("lauf", id).Msg("zwischenstand speichern")
	}
}

// esppdhFinish trägt das Ende eines Laufs ohne Firmware ein (auch beim Herunterfahren – deshalb
// ein eigener Kontext).
func (h *Handler) esppdhFinish(id, status, log, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.db.Exec(ctx, `UPDATE esppdh_builds SET status = $2, log = $3, esphome_version = $4, finished_at = now()
		WHERE id = $1::uuid AND status = 'running'`, id, status, log, version); err != nil {
		componentLog("esppdh").Error().Err(err).Str("lauf", id).Str("zustand", status).Msg("ende eintragen")
	}
}

// esppdhFinishOK trägt einen gelungenen Lauf ein und setzt built_hash des Geräts – in einem
// Schritt. errEsppdhNoBuild: der Lauf ist nicht mehr „running“ (Gerät gelöscht).
func (h *Handler) esppdhFinishOK(c esppdhClaim, hash string, res esppdh.BuildResult, log string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sum := sha256.Sum256(res.Factory)
	tag, err := tx.Exec(ctx, `UPDATE esppdh_builds SET status = 'ok', log = $2, esphome_version = $3, firmware_sha256 = $4,
			firmware_size = $5, config_hash = $6, finished_at = now()
		WHERE id = $1::uuid AND status = 'running'`, c.ID, log, res.Version, hex.EncodeToString(sum[:]), len(res.Factory), hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errEsppdhNoBuild
	}
	if _, err := tx.Exec(ctx, `UPDATE esppdh_devices SET built_hash = $2 WHERE id = $1::uuid`, c.DeviceID, hash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ── Einreihen und Abbrechen ──────────────────────────────────

// esppdhLoopbackHost: Adresse, unter der ein Gerät nur sich selbst erreicht (oder keine).
func esppdhLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

func esppdhLoopbackMsg(host string) string {
	return "Die Firmware würde den Broker unter „" + host + "“ suchen – unter dieser Adresse erreicht das Gerät den Broker nicht. " +
		"Bitte am Broker einen Namen eintragen oder das PDH über seine Adresse im Netz öffnen (nicht über localhost) und dann bauen."
}

// esppdhAddr: „host:port (TLS)“ für Meldungen.
func esppdhAddr(host string, port int, tls bool) string {
	s := net.JoinHostPort(host, strconv.Itoa(port))
	if tls {
		return s + " (TLS)"
	}
	return s + " (ohne TLS)"
}

// esppdhBuildBlocked: warum an diesem Server gerade nicht gebaut werden kann ("" = es geht).
func (h *Handler) esppdhBuildBlocked(ctx context.Context) string {
	if h.Mirror().On {
		return esppdhMsgMirror
	}
	switch st := h.esppdhStateOf(ctx); {
	case st.Docker:
		return errEsppdhDocker.Error() + "."
	case !st.Enabled:
		return esppdhMsgOff
	case st.EnvMissing:
		return esppdhMsgNoEnv
	}
	return ""
}

const (
	esppdhMsgMirror = "Firmware bauen geht nur am aktiven Server – dies ist der Spiegel."
	esppdhMsgOff    = "ESPPDH ist ausgeschaltet – einschalten unter Server-Einstellungen → ESPPDH."
	esppdhMsgNoEnv  = "Die Umgebung zum Bauen (ESPHome) ist an diesem Server noch nicht eingerichtet – Server-Einstellungen → ESPPDH → „Umgebung einrichten“."
)

// esppdhWorkerBlocked: wie esppdhBuildBlocked, aber für den Arbeiter – ein Lesefehler der
// Datenbank ist hier kein „ausgeschaltet“ (sonst würde eine kurze Störung alle wartenden
// Aufträge abbrechen), sondern ein Fehler: dann später erneut versuchen.
func (h *Handler) esppdhWorkerBlocked(ctx context.Context) (string, error) {
	switch {
	case h.Mirror().On:
		return esppdhMsgMirror, nil
	case inDocker():
		return errEsppdhDocker.Error() + ".", nil
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var on string
	if err := h.db.QueryRow(sctx, `SELECT value FROM app_settings WHERE key = $1`, esppdhSetEnabled).Scan(&on); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	switch {
	case on != "1":
		return esppdhMsgOff, nil
	case !esppdh.EnvPresent():
		return esppdhMsgNoEnv, nil
	}
	return "", nil
}

// esppdhCancelQueued bricht alle wartenden Aufträge mit Hinweis ab (nicht am Spiegel – dort ist
// die Datenbank nur lesbar, und gebaut wird ohnehin nicht).
func (h *Handler) esppdhCancelQueued(ctx context.Context, note string) {
	if h.Mirror().On || inDocker() {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h.esppdhOTACancelQueued(sctx, note) // wartende Updates übers Netz ebenso
	tag, err := h.db.Exec(sctx, `UPDATE esppdh_builds SET status = 'canceled', finished_at = now(),
			log = CASE WHEN log = '' THEN $1 ELSE log || E'\n' || $1 END
		WHERE status = 'queued'`, note)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			componentLog("esppdh").Warn().Err(err).Msg("wartende aufträge abbrechen")
		}
	case tag.RowsAffected() > 0:
		componentLog("esppdh").Warn().Int64("anzahl", tag.RowsAffected()).Str("grund", note).Msg("wartende aufträge abgebrochen")
	}
}

// EsppdhBuildWeb: POST /import/esppdh/{id}/build – Bau einreihen. Das Gerät muss so, wie es
// gespeichert ist, baubar sein; die Broker-Adresse wird hier festgelegt.
func (h *Handler) EsppdhBuildWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return
	}
	ctx := r.Context()
	if msg := h.esppdhBuildBlocked(ctx); msg != "" {
		http.Error(w, msg, http.StatusConflict)
		return
	}
	d, err := h.esppdhLoad(ctx, id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	mqttPW, apPW, err := h.esppdhSecrets(d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	res, msgs := h.esppdhResolve(ctx, d, mqttPW, apPW, r.Host)
	if len(msgs) == 0 {
		msgs = esppdh.ValidateDevice(res.Dev)
	}
	if len(msgs) > 0 {
		http.Error(w, "Die Firmware lässt sich noch nicht bauen: "+strings.Join(msgs, " "), http.StatusConflict)
		return
	}
	if esppdhLoopbackHost(res.Broker.Host) {
		http.Error(w, esppdhLoopbackMsg(res.Broker.Host), http.StatusConflict)
		return
	}
	by := getUser(r).ID
	if !meterUUIDRe.MatchString(by) {
		by = ""
	}
	var bid string
	err = h.db.QueryRow(ctx, `INSERT INTO esppdh_builds (device_id, status, config_hash, requested_by, broker_host, broker_port, broker_tls)
		VALUES ($1::uuid, 'queued', $2, $3::uuid, $4, $5, $6) RETURNING id::text`,
		id, esppdh.ConfigHash(res.Dev), nullIfEmpty(by), res.Broker.Host, res.Broker.Port, res.Broker.TLS).Scan(&bid)
	switch {
	case networkDup(err):
		http.Error(w, errEsppdhBuildActive.Error(), http.StatusConflict)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("gerät", d.Slug).Msg("bau einreihen")
		http.Error(w, "Der Bau ließ sich nicht einreihen. Näheres steht im Server-Protokoll.", http.StatusInternalServerError)
		return
	}
	h.esppdhWake()
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Str("lauf", bid).
		Str("broker", esppdhAddr(res.Broker.Host, res.Broker.Port, res.Broker.TLS)).Msg("bau eingereiht")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n=build#esp-firmware", http.StatusSeeOther)
}

// EsppdhBuildCancelWeb: POST /import/esppdh/{id}/build/{bid}/cancel – wartenden oder laufenden
// Bau abbrechen.
func (h *Handler) EsppdhBuildCancelWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPEdit)
	if !ok {
		return
	}
	bid := strings.ToLower(chi.URLParam(r, "bid"))
	if !meterUUIDRe.MatchString(bid) || h.db == nil {
		http.NotFound(w, r)
		return
	}
	if h.Mirror().On {
		http.Error(w, mirrorReadOnlyMsg, http.StatusConflict)
		return
	}
	switch err := h.esppdhCancelBuild(r.Context(), id, bid); {
	case errors.Is(err, errEsppdhNoBuild):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("lauf", bid).Msg("bau abbrechen")
		http.Error(w, "Der Bau ließ sich nicht abbrechen. Näheres steht im Server-Protokoll.", http.StatusInternalServerError)
		return
	}
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("lauf", bid).Msg("bau abgebrochen")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n=canceled#esp-firmware", http.StatusSeeOther)
}

// esppdhCancelBuild bricht einen Lauf des Geräts ab: Baut ihn dieser Arbeiter, bricht er ab
// und trägt „canceled“ ein; sonst (wartend, oder „running“ ohne Arbeiter nach einem Absturz)
// steht „canceled“ sofort in der Datenbank. Fertige Läufe bleiben, wie sie sind.
func (h *Handler) esppdhCancelBuild(ctx context.Context, devID, bid string) error {
	q := &h.esppdhQ
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running == bid && q.runDev == devID && q.cancelRun != nil {
		q.byUser = true
		q.cancelRun()
		return nil
	}
	tag, err := h.db.Exec(ctx, `UPDATE esppdh_builds SET status = 'canceled', finished_at = now(),
			log = CASE WHEN log = '' THEN $3 ELSE log || E'\n' || $3 END
		WHERE id = $1::uuid AND device_id = $2::uuid AND status IN ('queued', 'running')`, bid, devID, "Von Hand abgebrochen.")
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM esppdh_builds WHERE id = $1::uuid AND device_id = $2::uuid)`, bid, devID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errEsppdhNoBuild
	}
	return nil
}

// ── Anzeige ──────────────────────────────────────────────────

// esppdhRunView: ein Lauf für die Detailseite (Log nur beim jüngsten bzw. im htmx-Teil).
type esppdhRunView struct {
	ID, DeviceID, Status, Log, Version, By string
	BrokerHost                             string
	BrokerPort                             int
	BrokerTLS                              bool
	Size                                   int64
	Created                                time.Time
	Started, Finished                      *time.Time
	CanCancel                              bool
}

// Active: wartet oder läuft – dann fragt die Seite alle 2 s nach.
func (b esppdhRunView) Active() bool { return b.Status == "queued" || b.Status == "running" }

func (b esppdhRunView) StatusLabel() string {
	switch b.Status {
	case "queued":
		return "wartet"
	case "running":
		return "läuft"
	case "ok":
		return "fertig"
	case "failed":
		return "fehlgeschlagen"
	case "canceled":
		return "abgebrochen"
	}
	return b.Status
}

func (b esppdhRunView) Badge() string {
	switch b.Status {
	case "running":
		return "b-blue"
	case "ok":
		return "b-green"
	case "failed":
		return "b-red"
	case "canceled":
		return "b-amber"
	}
	return "b-gray"
}

// When: Zeitpunkt des Einreihens, z. B. „18.11.2026 14:05“.
func (b esppdhRunView) When() string { return b.Created.Local().Format("02.01.2006 15:04") }

// Took: Dauer des Baus, z. B. „4 min 12 s“ ("" solange er nicht fertig ist).
func (b esppdhRunView) Took() string {
	if b.Started == nil || b.Finished == nil {
		return ""
	}
	d := b.Finished.Sub(*b.Started).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d/time.Second))
	}
	return fmt.Sprintf("%d min %d s", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// Addr: Broker-Adresse, für die die Firmware gebaut ist.
func (b esppdhRunView) Addr() string {
	if b.BrokerHost == "" {
		return ""
	}
	return net.JoinHostPort(b.BrokerHost, strconv.Itoa(b.BrokerPort))
}

// SizeText: Größe der Firmware, z. B. „812 KB“.
func (b esppdhRunView) SizeText() string {
	if b.Size >= 1<<20 {
		return strings.Replace(strconv.FormatFloat(float64(b.Size)/(1<<20), 'f', 1, 64), ".", ",", 1) + " MB"
	}
	return strconv.FormatInt((b.Size+1023)>>10, 10) + " KB"
}

const esppdhRunSelect = `SELECT b.id::text, b.device_id::text, b.status, b.esphome_version, b.broker_host, b.broker_port, b.broker_tls,
		b.firmware_size, b.created_at, b.started_at, b.finished_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')`

func esppdhScanRun(row pgx.Row, extra ...any) (esppdhRunView, error) {
	var b esppdhRunView
	err := row.Scan(append([]any{&b.ID, &b.DeviceID, &b.Status, &b.Version, &b.BrokerHost, &b.BrokerPort, &b.BrokerTLS,
		&b.Size, &b.Created, &b.Started, &b.Finished, &b.By}, extra...)...)
	return b, err
}

// esppdhRuns: die Läufe eines Geräts (ohne Verlauf) in der gewählten Sortierung, höchstens limit.
func (h *Handler) esppdhRuns(ctx context.Context, devID string, limit int, mode listsort.Mode) ([]esppdhRunView, error) {
	rows, err := h.db.Query(ctx, esppdhRunSelect+` FROM esppdh_builds b LEFT JOIN users u ON u.id = b.requested_by
		WHERE b.device_id = $1::uuid ORDER BY `+logOrderSQL(mode, "b.created_at", "NULLIF(TRIM(u.first_name || ' ' || u.last_name), '')", "b.status")+`, b.id DESC LIMIT $2`, devID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []esppdhRunView
	for rows.Next() {
		b, err := esppdhScanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// esppdhRun: ein Lauf des Geräts mit Verlauf; errEsppdhNoBuild, wenn er nicht zu diesem Gerät gehört.
func (h *Handler) esppdhRun(ctx context.Context, devID, bid string) (esppdhRunView, error) {
	if h.db == nil || !meterUUIDRe.MatchString(devID) || !meterUUIDRe.MatchString(bid) {
		return esppdhRunView{}, errEsppdhNoBuild
	}
	var log string
	b, err := esppdhScanRun(h.db.QueryRow(ctx, esppdhRunSelect+`, b.log FROM esppdh_builds b LEFT JOIN users u ON u.id = b.requested_by
		WHERE b.id = $1::uuid AND b.device_id = $2::uuid`, bid, devID), &log)
	if errors.Is(err, pgx.ErrNoRows) {
		return esppdhRunView{}, errEsppdhNoBuild
	}
	b.Log = log
	return b, err
}

// esppdhFirmwareCard füllt die Karte „Firmware“ der Detailseite: die letzten Läufe, der
// jüngste mit Verlauf, Knopf „Firmware bauen“ und den Hinweis, wenn die Konfiguration seit dem
// letzten Bau geändert ist (oder sich das nicht prüfen lässt). res: das gespeicherte Gerät, wie
// es jetzt gebaut würde.
func (h *Handler) esppdhFirmwareCard(ctx context.Context, r *http.Request, pd *ESPPDHDevicePageData, d esppdhDevice, res esppdhResolved) {
	runs, err := h.esppdhRuns(ctx, d.ID, esppdhListRuns, listsort.Newest)
	if err != nil {
		componentLog("esppdh").Error().Err(err).Str("gerät", d.Slug).Msg("läufe lesen")
	}
	pd.Runs = runs
	// Liste „Letzte Läufe“: gewählte Sortierung; die Logik unten (jüngster Lauf, letzter
	// gelungener) bleibt bei „neueste zuerst“.
	mode := h.sortMode(r, "builds")
	pd.RunList = runs
	if mode != listsort.Newest {
		if l, err := h.esppdhRuns(ctx, d.ID, esppdhListRuns, mode); err != nil {
			componentLog("esppdh").Error().Err(err).Str("gerät", d.Slug).Msg("läufe sortiert lesen")
		} else {
			pd.RunList = l
		}
	}
	pd.RunBar = h.sortBar(r, "builds", mode, r.URL.Path, "")
	if len(runs) > 0 {
		pd.LatestRunID = runs[0].ID
	}
	active := false
	var lastOK *esppdhRunView
	for i := range runs {
		active = active || runs[i].Active()
		if lastOK == nil && runs[i].Status == "ok" {
			lastOK = &runs[i]
		}
	}
	if len(runs) > 0 {
		if b, err := h.esppdhRun(ctx, d.ID, runs[0].ID); err == nil {
			b.CanCancel = pd.CanEdit && b.Active() && !h.Mirror().On
			pd.Run = &b
		}
	}
	// geändert seit dem letzten Bau? Gerechnet für die Adresse, für die zuletzt gebaut wurde –
	// ohne Namen des Brokers hängt die Adresse sonst am Aufruf der Seite. d.BuiltHash ist die
	// Prüfsumme des jüngsten gelungenen Laufs (esppdhFinishOK setzt beide in einem Schritt).
	if d.BuiltHash != "" || lastOK != nil {
		host := res.Broker.Host
		if lastOK != nil && lastOK.BrokerHost != "" {
			host = lastOK.BrokerHost
		}
		current, err := h.esppdhFirmwareCurrent(ctx, d, host, d.BuiltHash)
		if err != nil {
			componentLog("esppdh").Warn().Str("gerät", d.Slug).Str("grund", err.Error()).Msg("firmware lässt sich nicht gegen die konfiguration prüfen")
		}
		pd.Changed, pd.ChangedUnknown = err == nil && !current, err != nil
	}
	if res.Broker.Host != "" && esppdhLoopbackHost(res.Broker.Host) {
		pd.Problems = append(pd.Problems, esppdhLoopbackMsg(res.Broker.Host))
	}
	pd.BuildNote = h.esppdhBuildBlocked(ctx)
	if pd.BuildNote == "" && len(pd.Problems) > 0 {
		pd.BuildNote = "Erst die fehlenden Angaben oben ergänzen und speichern – dann lässt sich die Firmware bauen."
	}
	pd.CanBuild = pd.CanEdit && pd.BuildNote == "" && !active
}

// EsppdhBuildPartWeb: GET /import/esppdh/{id}/build/{bid} – Verlauf eines Laufs als htmx-Teil.
// Prüft bei jeder Abfrage das Recht und dass der Lauf zu diesem Gerät gehört. Endet ein Lauf
// während der Abfrage alle 2 s (?poll=1), lädt die Seite einmal neu (Knopf, Liste, Hinweise).
func (h *Handler) EsppdhBuildPartWeb(w http.ResponseWriter, r *http.Request) {
	id, bid := chi.URLParam(r, "id"), strings.ToLower(chi.URLParam(r, "bid"))
	if !meterUUIDRe.MatchString(id) || !meterUUIDRe.MatchString(bid) {
		http.NotFound(w, r)
		return
	}
	if !h.esppdhCanSee(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	b, err := h.esppdhRun(r.Context(), id, bid)
	switch {
	case errors.Is(err, errEsppdhNoBuild):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("lauf", bid).Msg("lauf lesen")
		http.Error(w, "Der Lauf ließ sich nicht lesen.", http.StatusInternalServerError)
		return
	}
	b.CanCancel = b.Active() && !h.Mirror().On && h.esppdhCan(r, permESPEdit, id)
	var buf bytes.Buffer
	tm, err := h.tmpl.Clone()
	if err == nil {
		err = tm.ExecuteTemplate(&buf, "esppdh-build", b)
	}
	if err != nil {
		componentLog("esppdh").Error().Err(err).Msg("verlauf zeichnen")
		http.Error(w, "Der Verlauf ließ sich nicht anzeigen.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !b.Active() && r.URL.Query().Get("poll") == "1" && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
	}
	_, _ = w.Write(buf.Bytes())
}
