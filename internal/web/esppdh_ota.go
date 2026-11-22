package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/esppdh"
)

// ESPPDH – Firmware übers Netz aufspielen (0.82.24, zu esppdh_build.go). Das PDH schiebt die
// zuletzt gebaute Firmware mit ESPHome (esppdh.Runner.Upload) auf ein Gerät, das schon den
// Update-Baustein hat (einmal per USB geflasht). Derselbe Arbeiter wie beim Bau führt die
// Aufträge der Reihe nach aus (runMu: nie zwei ESPHome-Aufrufe zugleich). Erfolg erst, wenn das
// Gerät nach dem Neustart die Kennung des Baus meldet (Thema info/firmware).
//
//	POST /import/esppdh/{id}/ota         Aufspielen einreihen (esppdh.ota am Gerät)
//	GET  /import/esppdh/{id}/ota/{rid}   Verlauf eines Auftrags als htmx-Teil (Ansehen des Geräts)
//
// Aufgespielt wird nur am aktiven Server, nicht in der Wartung, nur die Firmware des jüngsten
// gelungenen Baus, die noch zur Konfiguration passt, und nur an eine bestätigte Adresse in einem
// für ESPPDH-Geräte freigegebenen Netz (esppdhOTANetOK).

// permESPOTA: Firmware übers Netz aufspielen (Migration 194).
const permESPOTA = "esppdh.ota"

var (
	esppdhOTAWait = 3 * time.Minute // so lange auf die Meldung der neuen Firmware warten
	esppdhOTAStep = 5 * time.Second // … so oft nachsehen
)

var (
	errEsppdhOTAActive = errors.New("Für dieses Gerät wartet oder läuft schon ein Update.")
	errEsppdhNoOTARun  = errors.New("Diesen Auftrag gibt es nicht.")
)

// esppdhUploadFunc spielt eine Firmware auf (esppdh.Runner.Upload; Tests setzen eine Attrappe).
type esppdhUploadFunc func(ctx context.Context, job esppdh.UploadJob, progress func(string)) (string, error)

// esppdhOTANetOK: Darf an diese Adresse aufgespielt werden? Nur in ein Netz der Seite „Netzwerk“,
// das für ESPPDH-Geräte freigegeben ist: das Netz des WLANs mit Haken „Für ESPPDH-Geräte
// nutzbar“, bei Kabel das eingetragene Netz des Geräts – und die Adresse muss darin liegen.
func esppdhOTANetOK(addr, subnet string, wired, wlanReleased, segmentSet bool) (bool, string) {
	switch {
	case wired && !segmentSet:
		return false, "Das Netz am Kabel ist nicht eingetragen – bitte am Gerät ein Netz wählen."
	case !wired && !wlanReleased:
		return false, "Das WLAN ist nicht für ESPPDH-Geräte freigegeben (Seite „Netzwerk“, Haken „Für ESPPDH-Geräte nutzbar“)."
	case subnet == "":
		return false, "Für das Netz des Geräts ist auf der Seite „Netzwerk“ kein Adressbereich eingetragen."
	}
	_, n, err := net.ParseCIDR(subnet)
	ip := net.ParseIP(addr)
	if err != nil || ip == nil || !n.Contains(ip) {
		return false, "Die Adresse " + addr + " liegt in keinem freigegebenen Netz."
	}
	return true, ""
}

// ── Voraussetzungen ──────────────────────────────────────────

// esppdhOTAFacts: was für das Aufspielen an einem Gerät zählt (esppdhOTACheck sammelt es).
type esppdhOTAFacts struct {
	DeviceID                        string
	Allowed                         bool   // Recht esppdh.ota am Gerät
	Mirror, Maint, Locked, Active   bool   // Active: ein Auftrag wartet oder läuft schon
	Blocked                         string // warum hier nicht gebaut werden kann (esppdhBuildBlocked)
	LatestBuild, LatestWhen         string // jüngster gelungener Bau
	LatestHash, CfgHash             string // … sein Prüfwert und der des Geräts jetzt ("" = nicht feststellbar)
	Current, FileOK                 bool   // … passt noch zur Konfiguration, Datei vorhanden
	BrokerOK                        bool   // Werte des Brokers lesbar
	Reported, InstalledWhen         string // vom Gerät gemeldete Kennung (und wann gebaut)
	Address, AddressNote            string
	AddressOK                       bool
	Wired, WLANReleased, SegmentSet bool
	Subnet, Platform                string
	Run                             *esppdhOTARunView
}

// esppdhOTAReason: warum gerade nicht aufgespielt werden kann ("" = es geht).
func esppdhOTAReason(f esppdhOTAFacts) string {
	switch {
	case f.Mirror:
		return "Aufspielen geht nur am aktiven Server – dies ist der Spiegel."
	case f.Maint:
		return "Während der Wartung wird keine Firmware aufgespielt."
	case f.Blocked != "":
		return f.Blocked
	case f.Locked:
		return "Das Gerät ist gesperrt – erst entsperren."
	case f.Active:
		return errEsppdhOTAActive.Error()
	case f.LatestBuild == "":
		return "Noch keine Firmware gebaut – erst „Firmware bauen“."
	case !f.Current:
		return "Die zuletzt gebaute Firmware passt nicht mehr zur Konfiguration – erst neu bauen."
	case !f.FileOK:
		return "Die Datei der zuletzt gebauten Firmware fehlt – bitte neu bauen."
	}
	return esppdhOTADeviceReason(f)
}

// esppdhOTADeviceReason: Gründe am Gerät selbst – Broker, Update-Baustein, Adresse, Netz (die
// Sammelaktion prüft sie vor dem Bauen; die Firmware baut sie bei Bedarf neu).
func esppdhOTADeviceReason(f esppdhOTAFacts) string {
	switch {
	case f.Locked:
		return "Das Gerät ist gesperrt – erst entsperren."
	case f.Active:
		return errEsppdhOTAActive.Error()
	case !f.BrokerOK:
		return "Der Broker läuft an diesem Server nicht – ob das Gerät erreichbar ist, lässt sich nicht sehen."
	case f.Reported == "":
		return "Das Gerät hat noch keinen Update-Baustein – einmal per USB flashen."
	case !f.AddressOK:
		if f.AddressNote != "" {
			return "Das Gerät meldet keine bestätigte Adresse (" + f.AddressNote + ")."
		}
		return "Das Gerät meldet keine bestätigte Adresse."
	}
	if ok, msg := esppdhOTANetOK(f.Address, f.Subnet, f.Wired, f.WLANReleased, f.SegmentSet); !ok {
		return msg
	}
	return ""
}

// esppdhOTAView: Teil „Firmware übers Netz“ der Karte „Firmware“.
type esppdhOTAView struct {
	Show                     bool // Recht esppdh.ota – sonst keine Karte
	DeviceID                 string
	CanPush                  bool
	Reason                   string
	Address                  string
	Installed, InstalledWhen string
	Latest, LatestWhen       string
	UpToDate                 bool // die zuletzt gebaute Firmware läuft schon
	PortHint                 string
	Run                      *esppdhOTARunView
}

func esppdhOTAViewOf(f esppdhOTAFacts) esppdhOTAView {
	v := esppdhOTAView{Show: f.Allowed, DeviceID: f.DeviceID, Address: f.Address, Installed: f.Reported, InstalledWhen: f.InstalledWhen,
		Latest: f.LatestBuild, LatestWhen: f.LatestWhen, Run: f.Run, PortHint: "3232"}
	if f.Platform == "esp8266" {
		v.PortHint = "8266"
	}
	v.UpToDate = f.Reported != "" && f.Reported == f.LatestBuild
	v.Reason = esppdhOTAReason(f)
	v.CanPush = f.Allowed && v.Reason == ""
	return v
}

// esppdhOTACheck sammelt die Voraussetzungen (ohne Recht und ohne letzten Auftrag).
func (h *Handler) esppdhOTACheck(ctx context.Context, d esppdhDevice) esppdhOTAFacts {
	f := esppdhOTAFacts{DeviceID: d.ID, Mirror: h.Mirror().On, Maint: h.MaintLocked(), Locked: d.Locked,
		Wired: d.Wired, SegmentSet: d.SegmentID != "", Subnet: d.NetSubnet, Platform: esppdh.PlatformOf(d.BoardKey, d.CustomBoard)}
	f.Blocked = h.esppdhBuildBlocked(ctx)
	if h.db == nil {
		return f
	}
	if !d.Wired && meterUUIDRe.MatchString(d.WLANID) {
		_ = h.db.QueryRow(ctx, `SELECT esppdh_ok FROM net_wlans WHERE id = $1::uuid`, d.WLANID).Scan(&f.WLANReleased)
	}
	var host, hash string
	var created time.Time
	err := h.db.QueryRow(ctx, `SELECT id::text, broker_host, config_hash, created_at FROM esppdh_builds
		WHERE device_id = $1::uuid AND status = 'ok' ORDER BY created_at DESC, id DESC LIMIT 1`, d.ID).Scan(&f.LatestBuild, &host, &hash, &created)
	if err == nil {
		f.LatestWhen, f.LatestHash = created.Local().Format("02.01.2006 15:04"), hash
		f.CfgHash, _ = h.esppdhConfigHashFor(ctx, d, host)
		f.Current = f.CfgHash != "" && f.CfgHash == hash
		_, serr := os.Stat(filepath.Join(esppdhFirmwareRoot, d.ID, esppdhFirmwareName(f.LatestBuild, esppdhOTA)))
		f.FileOK = serr == nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		componentLog("esppdh").Warn().Err(err).Str("gerät", d.Slug).Msg("letzten bau lesen")
	}
	_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM esppdh_ota_runs WHERE device_id = $1::uuid AND status IN ('queued', 'running'))`, d.ID).Scan(&f.Active)
	vv := h.esppdhValues(ctx, []esppdhDevice{d}, time.Now())[0]
	f.BrokerOK, f.Reported, f.Address, f.AddressOK, f.AddressNote = vv.Available, vv.Firmware, vv.Address, vv.AddressOK, vv.AddressNote
	if meterUUIDRe.MatchString(f.Reported) {
		var at time.Time
		if h.db.QueryRow(ctx, `SELECT created_at FROM esppdh_builds WHERE id = $1::uuid AND device_id = $2::uuid`, f.Reported, d.ID).Scan(&at) == nil {
			f.InstalledWhen = at.Local().Format("02.01.2006 15:04")
		}
	}
	return f
}

// esppdhOTAState füllt den Teil „Firmware übers Netz“ für die Geräteseite (leer ohne Recht).
func (h *Handler) esppdhOTAState(ctx context.Context, r *http.Request, d esppdhDevice) esppdhOTAView {
	if !h.esppdhCan(r, permESPOTA, d.ID) {
		return esppdhOTAView{}
	}
	f := h.esppdhOTACheck(ctx, d)
	f.Allowed = true
	if run, err := h.esppdhOTARunOf(ctx, d.ID, ""); err == nil {
		f.Run = &run
	}
	return esppdhOTAViewOf(f)
}

// ── Ablauf ───────────────────────────────────────────────────

// esppdhOTADeps: was ein Lauf braucht (Tests ersetzen alles).
type esppdhOTADeps struct {
	upload   esppdhUploadFunc
	progress func(string)                 // Zwischenstand des Aufspielens (geschwärzt)
	reported func(context.Context) string // vom Gerät gemeldete Firmware-Kennung
	sleep    func(time.Duration)
}

// esppdhOTARun spielt auf und wartet danach bis wait (alle esppdhOTAStep), dass das Gerät die
// Kennung want meldet: dann „ok“, sonst „unconfirmed“. Scheitert das Aufspielen: „failed“.
func esppdhOTARun(ctx context.Context, d esppdhOTADeps, job esppdh.UploadJob, want string, wait time.Duration) (status, log string) {
	progress := d.progress
	if progress == nil {
		progress = func(string) {}
	}
	log, err := d.upload(ctx, job, progress)
	log = strings.TrimRight(log, "\n")
	if err != nil {
		return "failed", esppdhLogLine(log, "Aufspielen fehlgeschlagen: "+err.Error())
	}
	if ctx.Err() != nil {
		return "failed", esppdhLogLine(log, "PDH wurde während des Aufspielens beendet – bitte das Gerät prüfen.")
	}
	log = esppdhLogLine(log, "Aufgespielt. Warte, bis das Gerät nach dem Neustart die neue Firmware meldet …")
	n := int(wait / esppdhOTAStep)
	for i := 0; ; i++ {
		if d.reported(ctx) == want {
			return "ok", esppdhLogLine(log, "Das Gerät meldet die neue Firmware.")
		}
		if i >= n || ctx.Err() != nil {
			break
		}
		d.sleep(esppdhOTAStep)
	}
	return "unconfirmed", esppdhLogLine(log, fmt.Sprintf("Aufgespielt, aber das Gerät meldet die neue Firmware nach %d min nicht – "+
		"bitte am Gerät nachsehen (Strom, Netz, Sicherheitsmodus).", int(wait/time.Minute)))
}

func esppdhLogLine(log, line string) string {
	if log == "" {
		return line
	}
	return log + "\n" + line
}

// esppdhOTAClaim: ein übernommener Auftrag.
type esppdhOTAClaim struct {
	ID, DeviceID, BuildID string
}

// esppdhOTANext übernimmt den ältesten wartenden Auftrag und spielt auf. false: keiner da (oder
// gerade nicht erlaubt – in der Wartung bleiben Aufträge wartend).
func (h *Handler) esppdhOTANext(ctx context.Context, up esppdhUploadFunc) bool {
	if h.db == nil || h.MaintLocked() {
		return false
	}
	switch msg, err := h.esppdhWorkerBlocked(ctx); {
	case err != nil:
		return false
	case msg != "":
		h.esppdhCancelQueued(ctx, "Nicht ausgeführt: "+msg)
		return false
	}
	if !h.esppdhLockRun(ctx) {
		return false
	}
	defer h.esppdhQ.runMu.Unlock()
	var c esppdhOTAClaim
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := h.db.QueryRow(cctx, `UPDATE esppdh_ota_runs SET status = 'running', started_at = now()
		WHERE id = (SELECT id FROM esppdh_ota_runs WHERE status = 'queued' ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id::text, device_id::text, COALESCE(build_id::text, '')`).Scan(&c.ID, &c.DeviceID, &c.BuildID)
	cancel()
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			componentLog("esppdh").Error().Err(err).Msg("update übernehmen")
		}
		return false
	}
	status, log := h.esppdhOTAExec(ctx, up, c)
	h.esppdhOTAFinish(c.ID, status, log)
	componentLog("esppdh").Info().Str("auftrag", c.ID).Str("gerät", c.DeviceID).Str("zustand", status).Msg("update übers netz beendet")
	return true
}

// esppdhOTAExec prüft die Voraussetzungen erneut und spielt auf (der Aufrufer hält runMu).
func (h *Handler) esppdhOTAExec(ctx context.Context, up esppdhUploadFunc, c esppdhOTAClaim) (status, log string) {
	lg := componentLog("esppdh")
	defer func() {
		if p := recover(); p != nil {
			lg.Error().Interface("panic", p).Str("auftrag", c.ID).Msg("update abgestürzt")
			status, log = "failed", "Interner Fehler beim Aufspielen – Näheres steht im Server-Protokoll."
		}
	}()
	d, err := h.esppdhLoad(ctx, c.DeviceID)
	if err != nil {
		return "failed", "Das Gerät ließ sich nicht lesen."
	}
	f := h.esppdhOTACheck(ctx, d)
	f.Active = false // das ist dieser Auftrag
	if c.BuildID != "" && c.BuildID != f.LatestBuild {
		return "failed", "Inzwischen gibt es einen neueren Bau – bitte erneut aufspielen."
	}
	if reason := esppdhOTAReason(f); reason != "" {
		return "failed", "Nicht aufgespielt: " + reason
	}
	var bc esppdhClaim
	if err := h.db.QueryRow(ctx, `SELECT id::text, device_id::text, broker_host, broker_port, broker_tls FROM esppdh_builds WHERE id = $1::uuid`,
		f.LatestBuild).Scan(&bc.ID, &bc.DeviceID, &bc.Host, &bc.Port, &bc.TLS); err != nil {
		return "failed", "Der Bau ließ sich nicht lesen."
	}
	job, _, err := h.esppdhJobFor(ctx, bc)
	if err != nil {
		return "failed", "Nicht aufgespielt: " + err.Error()
	}
	fw, err := h.esppdhFirmwareOTA(f.LatestBuild)
	if err != nil {
		lg.Error().Err(err).Str("auftrag", c.ID).Msg("firmware lesen")
		return "failed", "Die Firmware-Datei ließ sich nicht lesen – bitte neu bauen."
	}
	_, _ = h.db.Exec(ctx, `UPDATE esppdh_ota_runs SET build_id = $2::uuid, address = $3 WHERE id = $1::uuid`, c.ID, f.LatestBuild, f.Address)
	uj := esppdh.UploadJob{ID: c.ID, Slug: d.Slug, Address: f.Address, Config: job.Config, Secrets: job.Secrets, Firmware: fw, SecretValues: job.SecretValues}
	deps := esppdhOTADeps{
		upload:   up,
		progress: func(l string) { h.esppdhOTASaveLog(ctx, c.ID, esppdh.Redact(l, job.SecretValues)) },
		reported: func(rctx context.Context) string { return h.esppdhValues(rctx, []esppdhDevice{d}, time.Now())[0].Firmware },
		sleep: func(w time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(w):
			}
		},
	}
	lg.Info().Str("auftrag", c.ID).Str("gerät", d.Slug).Str("adresse", f.Address).Msg("update übers netz beginnt")
	status, log = esppdhOTARun(ctx, deps, uj, f.LatestBuild, esppdhOTAWait)
	return status, esppdh.Redact(log, job.SecretValues)
}

func (h *Handler) esppdhOTASaveLog(ctx context.Context, id, log string) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := h.db.Exec(sctx, `UPDATE esppdh_ota_runs SET log = $2 WHERE id = $1::uuid AND status = 'running'`, id, log); err != nil && ctx.Err() == nil {
		componentLog("esppdh").Warn().Err(err).Str("auftrag", id).Msg("zwischenstand speichern")
	}
}

// esppdhOTAFailRunning: Beim Start gelten Updates, die beim letzten Beenden noch liefen, als
// gescheitert (StartESPPDH).
func (h *Handler) esppdhOTAFailRunning(ctx context.Context) {
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tag, err := h.db.Exec(sctx, `UPDATE esppdh_ota_runs SET status = 'failed', finished_at = now(),
			log = CASE WHEN log = '' THEN $1 ELSE log || chr(10) || $1 END
		WHERE status = 'running'`, "PDH wurde während des Updates neu gestartet – bitte das Gerät prüfen und erneut aufspielen.")
	switch {
	case err != nil:
		componentLog("esppdh").Error().Err(err).Msg("abgebrochene updates beenden")
	case tag.RowsAffected() > 0:
		componentLog("esppdh").Warn().Int64("anzahl", tag.RowsAffected()).Msg("updates vom letzten start als gescheitert markiert")
	}
}

// esppdhOTACancelQueued bricht wartende Updates mit Hinweis ab (esppdhCancelQueued).
func (h *Handler) esppdhOTACancelQueued(ctx context.Context, note string) {
	tag, err := h.db.Exec(ctx, `UPDATE esppdh_ota_runs SET status = 'canceled', finished_at = now(),
			log = CASE WHEN log = '' THEN $1 ELSE log || chr(10) || $1 END
		WHERE status = 'queued'`, note)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			componentLog("esppdh").Warn().Err(err).Msg("wartende updates abbrechen")
		}
	case tag.RowsAffected() > 0:
		componentLog("esppdh").Warn().Int64("anzahl", tag.RowsAffected()).Str("grund", note).Msg("wartende updates abgebrochen")
	}
}

// esppdhOTAFinish trägt das Ende eines Auftrags ein (eigener Kontext – auch beim Herunterfahren).
func (h *Handler) esppdhOTAFinish(id, status, log string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.db.Exec(ctx, `UPDATE esppdh_ota_runs SET status = $2, log = $3, finished_at = now()
		WHERE id = $1::uuid AND status = 'running'`, id, status, log); err != nil {
		componentLog("esppdh").Error().Err(err).Str("auftrag", id).Str("zustand", status).Msg("ende eintragen")
	}
}

// ── Anzeige ──────────────────────────────────────────────────

// esppdhOTARunView: ein Auftrag mit Verlauf.
type esppdhOTARunView struct {
	ID, DeviceID, Status, Address, BatchID, By, Log string
	Created                                         time.Time
	Started, Finished                               *time.Time
}

func (o esppdhOTARunView) Active() bool { return o.Status == "queued" || o.Status == "running" }

func (o esppdhOTARunView) StatusLabel() string {
	switch o.Status {
	case "queued":
		return "wartet"
	case "running":
		return "läuft"
	case "ok":
		return "aufgespielt"
	case "unconfirmed":
		return "aufgespielt, meldet sich nicht"
	case "failed":
		return "fehlgeschlagen"
	case "canceled":
		return "abgebrochen"
	case "skipped":
		return "übersprungen"
	}
	return o.Status
}

func (o esppdhOTARunView) Badge() string {
	switch o.Status {
	case "running":
		return "b-blue"
	case "ok":
		return "b-green"
	case "failed":
		return "b-red"
	case "unconfirmed", "canceled":
		return "b-amber"
	}
	return "b-gray"
}

func (o esppdhOTARunView) When() string { return o.Created.Local().Format("02.01.2006 15:04") }

// LastLine: letzte nicht leere Zeile des Verlaufs (Ergebnis), höchstens 160 Zeichen.
func (o esppdhOTARunView) LastLine() string {
	lines := strings.Split(strings.TrimSpace(o.Log), "\n")
	l := []rune(strings.TrimSpace(lines[len(lines)-1]))
	if len(l) > 160 {
		return string(l[:160]) + "…"
	}
	return string(l)
}

const esppdhOTARunSelect = `SELECT o.id::text, o.device_id::text, o.status, o.address, COALESCE(o.batch_id::text, ''),
		COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''), o.log, o.created_at, o.started_at, o.finished_at
	FROM esppdh_ota_runs o LEFT JOIN users u ON u.id = o.requested_by`

func esppdhScanOTARun(row pgx.Row) (esppdhOTARunView, error) {
	var o esppdhOTARunView
	err := row.Scan(&o.ID, &o.DeviceID, &o.Status, &o.Address, &o.BatchID, &o.By, &o.Log, &o.Created, &o.Started, &o.Finished)
	return o, err
}

// esppdhOTARunOf: ein Auftrag des Geräts (rid "" = der jüngste); errEsppdhNoOTARun, wenn es ihn
// für dieses Gerät nicht gibt.
func (h *Handler) esppdhOTARunOf(ctx context.Context, devID, rid string) (esppdhOTARunView, error) {
	if h.db == nil || !meterUUIDRe.MatchString(devID) || (rid != "" && !meterUUIDRe.MatchString(rid)) {
		return esppdhOTARunView{}, errEsppdhNoOTARun
	}
	var o esppdhOTARunView
	var err error
	if rid == "" {
		o, err = esppdhScanOTARun(h.db.QueryRow(ctx, esppdhOTARunSelect+` WHERE o.device_id = $1::uuid ORDER BY o.created_at DESC, o.id DESC LIMIT 1`, devID))
	} else {
		o, err = esppdhScanOTARun(h.db.QueryRow(ctx, esppdhOTARunSelect+` WHERE o.id = $1::uuid AND o.device_id = $2::uuid`, rid, devID))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return esppdhOTARunView{}, errEsppdhNoOTARun
	}
	return o, err
}

// ── Knöpfe ───────────────────────────────────────────────────

// EsppdhOTAPushWeb: POST /import/esppdh/{id}/ota – Aufspielen einreihen.
func (h *Handler) EsppdhOTAPushWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.esppdhRecordAction(w, r, permESPOTA)
	if !ok {
		return
	}
	ctx := r.Context()
	d, err := h.esppdhLoad(ctx, id)
	if err != nil {
		h.esppdhLoadFailed(w, r, err)
		return
	}
	f := h.esppdhOTACheck(ctx, d)
	f.Allowed = true
	if reason := esppdhOTAReason(f); reason != "" {
		http.Error(w, reason, http.StatusConflict)
		return
	}
	by := getUser(r).ID
	if !meterUUIDRe.MatchString(by) {
		by = ""
	}
	var rid string
	err = h.db.QueryRow(ctx, `INSERT INTO esppdh_ota_runs (device_id, build_id, status, address, requested_by)
		VALUES ($1::uuid, $2::uuid, 'queued', $3, $4::uuid) RETURNING id::text`, id, f.LatestBuild, f.Address, nullIfEmpty(by)).Scan(&rid)
	switch {
	case networkDup(err):
		http.Error(w, errEsppdhOTAActive.Error(), http.StatusConflict)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("gerät", d.Slug).Msg("update einreihen")
		http.Error(w, "Das Update ließ sich nicht einreihen. Näheres steht im Server-Protokoll.", http.StatusInternalServerError)
		return
	}
	h.esppdhWake()
	componentLog("esppdh").Info().Str("benutzer", getUser(r).ID).Str("gerät", d.Slug).Str("auftrag", rid).
		Str("bau", f.LatestBuild).Str("adresse", f.Address).Msg("update übers netz eingereiht")
	http.Redirect(w, r, "/import/esppdh/"+id+"?n=ota#esp-ota", http.StatusSeeOther)
}

// EsppdhOTAPartWeb: GET /import/esppdh/{id}/ota/{rid} – Verlauf eines Auftrags als htmx-Teil.
// Endet er während der Abfrage alle 2 s (?poll=1), lädt die Seite einmal neu.
func (h *Handler) EsppdhOTAPartWeb(w http.ResponseWriter, r *http.Request) {
	id, rid := chi.URLParam(r, "id"), strings.ToLower(chi.URLParam(r, "rid"))
	if !meterUUIDRe.MatchString(id) || !meterUUIDRe.MatchString(rid) {
		http.NotFound(w, r)
		return
	}
	if !h.esppdhCanSee(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	o, err := h.esppdhOTARunOf(r.Context(), id, rid)
	switch {
	case errors.Is(err, errEsppdhNoOTARun):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		componentLog("esppdh").Error().Err(err).Str("auftrag", rid).Msg("update lesen")
		http.Error(w, "Der Auftrag ließ sich nicht lesen.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !o.Active() && r.URL.Query().Get("poll") == "1" && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
	}
	h.renderFragment(w, "esppdh-ota-run", o)
}
