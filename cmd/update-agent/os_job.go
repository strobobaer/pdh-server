package main

// Betriebssystem-Updates (Teil A, Einzelserver): der Auftrag „Betriebssystem“.
//
// Phasen wie beim PDH-Update (job.go): UpPrepare (Prüfen) → UpCountdown → UpLocked (Sperre,
// Sicherung, Pakete einspielen, ggf. Neustart, prüfen) → UpRelease → UpDone/UpFailed. Die
// Schritte der Pakete führt osAdvance (os_steps.go). Der Auftrag liegt in <drStateDir>/os-job.json
// und läuft nach einem Neustart des Agenten oder des Rechners weiter; er hält die gemeinsame
// Sperre lockOS von startOSJob bis zum Ende (und danach, bis die Einheit von „install“ endet).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

const (
	osMinFreeApt  = int64(1) << 30
	osMinFreeBoot = int64(200) << 20

	osNetPendingMsg = "Eine Netz-Änderung wartet auf Bestätigung – bitte erst bestätigen oder zurücknehmen lassen"
	osAbortLateMsg  = "Die Pakete werden bereits eingespielt – Abbruch nicht mehr möglich"
	osJobLockCommit = "os-update" // Vermerk in der Datenbank-Sperre (statt eines Commits)
)

// osJob: Auftrag auf dem Einzelserver (os-job.json).
type osJob struct {
	Job          ha.UpdateJob `json:"job"`
	CountdownMin int          `json:"countdown_min"`
	LockMaxMin   int          `json:"lock_max_min"`
	Dump         string       `json:"dump"` // Sicherung vor dem Einspielen (pg_dump -Fc)
	OS           osSteps      `json:"os"`
	Source       string       `json:"source"`
}

type osJobRuntime struct {
	mu      sync.Mutex
	job     osJob // Phase leer = kein Auftrag
	saveMu  sync.Mutex
	release func() // gemeinsame Sperre lockOS; mu
}

func osJobFile() string { return filepath.Join(drStateDir(), "os-job.json") }

func (a *updateAgent) osJobOpen() bool {
	a.osj.mu.Lock()
	defer a.osj.mu.Unlock()
	return !jobEnded(a.osj.job.Job)
}

// osNetBusy: wartet eine Netz-Änderung auf Bestätigung oder Rücknahme (net.go)?
func (a *updateAgent) osNetBusy() bool {
	p := a.netReadState()
	return p != nil && (p.State == "pending" || p.MaintOffPending)
}

// osSpaceProblem: reicht der Platz für die Pakete (/var/cache/apt) und den Kernel (/boot)? Leer = ja.
func (a *updateAgent) osSpaceProblem() string {
	if n, known := a.freeSpaceFn()("/var/cache/apt"); known && n < osMinFreeApt {
		return "Zu wenig freier Platz in /var/cache/apt (mindestens 1 GB)"
	}
	if n, known := a.freeSpaceFn()("/boot"); known && n < osMinFreeBoot {
		return "Zu wenig freier Platz in /boot (mindestens 200 MB)"
	}
	return ""
}

// osPreflight: dieselben Hindernisse wie beim Start des Betriebssystem-Auftrags – wartet eine
// Netz-Änderung auf Bestätigung oder fehlt der Platz? Leer = frei. Der PDH-Auftrag mit Haken prüft
// das unmittelbar vor dem Stoppen von pdh.
func (a *updateAgent) osPreflight() string {
	if a.osNetBusy() {
		return osNetPendingMsg
	}
	return a.osSpaceProblem()
}

// osJobPut übernimmt den Stand eines Schritts und speichert ihn; ein inzwischen eingegangener
// Abbruch bleibt erhalten.
func (a *updateAgent) osJobPut(s osJob) error {
	a.osj.saveMu.Lock()
	defer a.osj.saveMu.Unlock()
	a.osj.mu.Lock()
	cur := a.osj.job.Job
	if cur.ID == s.Job.ID && cur.AbortedBy != "" && s.Job.AbortedBy == "" {
		s.Job.AbortedBy = cur.AbortedBy
	}
	s.Job.OSStep = s.OS.Step
	s.Job.Seq = max(cur.Seq, s.Job.Seq) + 1
	a.osj.job = s
	a.osj.mu.Unlock()
	err := a.writeState(osJobFile(), s)
	if err != nil {
		log.Printf("betriebssystem: %s nicht gespeichert: %v", "os-job.json", err)
	}
	return err
}

// writeState schreibt eine Zustandsdatei (Test-Haken stateWrite).
func (a *updateAgent) writeState(path string, v any) error {
	if a.stateWrite != nil {
		return a.stateWrite(path, v)
	}
	return writeStateJSON(path, v)
}

// loadOSJob: beim Start des Agenten; ein offener Auftrag hält die Sperre wieder und läuft weiter.
func (a *updateAgent) loadOSJob() {
	b, err := os.ReadFile(osJobFile())
	if err != nil {
		return
	}
	var s osJob
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("betriebssystem: os-job.json nicht lesbar: %v", err)
		return
	}
	a.osj.mu.Lock()
	a.osj.job = s
	a.osj.mu.Unlock()
	if jobEnded(s.Job) {
		// Einheit von „install“ läuft noch (z. B. nach „dauert zu lange“): Sperre halten, bis sie endet
		if s.OS.Unit != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			active := a.osUnitIsActive(ctx, s.OS.Unit)
			cancel()
			if active {
				if release, busy := a.lockTryExclusive(lockOS); busy == "" {
					a.osWaitUnit(s.OS.Unit, release)
				}
			}
		}
		return
	}
	log.Printf("betriebssystem: Auftrag %s wird in Phase %s fortgesetzt", s.Job.ID, s.Job.Phase)
	release, busy := a.lockTryExclusive(lockOS)
	if busy != "" {
		log.Printf("betriebssystem: Sperre gehört schon %s", busy)
	}
	a.osj.mu.Lock()
	a.osj.release = release
	a.osj.mu.Unlock()
	a.jobKick()
}

// osJobPreferred: gehört die Anzeige dem Betriebssystem-Auftrag (offen, sonst der jüngere)?
func (a *updateAgent) osJobPreferred() bool {
	a.osj.mu.Lock()
	o := a.osj.job.Job
	a.osj.mu.Unlock()
	if o.ID == "" {
		return false
	}
	if !jobEnded(o) {
		return true
	}
	a.sj.mu.Lock()
	p := a.sj.job.Job
	a.sj.mu.Unlock()
	return p.ID == "" || (jobEnded(p) && o.PhaseAt.After(p.PhaseAt))
}

func (a *updateAgent) osJobView() ha.UpdateView {
	a.osj.mu.Lock()
	s := a.osj.job
	a.osj.mu.Unlock()
	return s.Job.View(a.now(), time.Duration(s.LockMaxMin)*time.Minute, a.windowSnapshot().Faulty)
}

// startOSJob legt den Auftrag an. force (nur Knopf): trotz „nicht grün“ (Platz für Pakete und
// /boot sind nie übergehbar). Fehler jobNotReady = HTTP 409.
func (a *updateAgent) startOSJob(ctx context.Context, source string, force bool) ([]string, error) {
	if ok, reason, _ := a.osAvailable(ctx); !ok {
		return nil, errors.New(reason)
	}
	if a.osJobOpen() {
		return nil, errors.New("Ein Betriebssystem-Update läuft bereits")
	}
	a.mu.RLock()
	running := a.status.Running
	a.mu.RUnlock()
	if running || a.singleJobActive() {
		return nil, errors.New("Ein PDH-Update läuft bereits")
	}
	// gemeinsame Sperre bis zum Ende des Auftrags; nie von zwei Starts geteilt
	release, busy := a.lockTryExclusive(lockOS)
	switch busy {
	case "":
	case lockPDH:
		return nil, &lockBusyError{Owner: lockPDH, Msg: "Ein PDH-Update läuft bereits"}
	default:
		return nil, newLockBusy(busy)
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	if a.osNetBusy() {
		return nil, errors.New(osNetPendingMsg)
	}
	ss, err := a.osStatus(ctx)
	if err != nil {
		return nil, err
	}
	if len(ss.Upgradable) == 0 && !ss.RebootRequired {
		return nil, errors.New("Keine Pakete zu aktualisieren")
	}
	if msg := a.osSpaceProblem(); msg != "" {
		return nil, errors.New(msg)
	}
	var hints []string
	configured, baseOK, _ := a.drBackupReport()
	free, known := a.freeSpaceFn()(a.repoDir)
	if !known {
		free, hints = int64(1)<<62, append(hints, "Freier Platz nicht geprüft")
	}
	ok, reason, h := ha.UpdateReady(ha.ReadyObs{Mode: ha.Single, BackupConfigured: configured, LastBaseOK: baseOK, FreeSelf: free})
	hints = append(hints, h...)
	if !ok && !force {
		return nil, jobNotReady{reason: reason, hints: hints}
	}
	if !ok {
		hints = append(hints, "Trotz „"+reason+"“ gestartet")
	}
	if n, known := a.freeSpaceFn()(drStateDir()); known && n < ha.UpdateMinFree {
		msg := "Weniger als 2 GB frei für die Sicherung vor dem Update (" + drStateDir() + ")"
		if !force {
			return nil, jobNotReady{reason: msg, hints: hints}
		}
		hints = append(hints, "Trotz „"+msg+"“ gestartet")
	}
	ws := a.windowSnapshot()
	win := ws.Window
	if win.Validate() != nil {
		win = ha.DefaultWindow()
	}
	now := a.now()
	s := osJob{
		Job: ha.UpdateJob{ID: "os-" + now.UTC().Format("20060102T150405Z"), Kind: ha.UpdateOS, Source: source,
			Phase: ha.UpPrepare, PhaseAt: now, StartedAt: now},
		CountdownMin: win.CountdownMin, LockMaxMin: win.LockMaxMin, Source: source,
		Dump: filepath.Join(drStateDir(), "vor-update-os.dump"),
	}
	a.osj.mu.Lock()
	if !jobEnded(a.osj.job.Job) {
		a.osj.mu.Unlock()
		return nil, errors.New("Ein Betriebssystem-Update läuft bereits")
	}
	a.osj.job = osJob{}
	if a.osj.release != nil {
		a.osj.release() // Rest eines früheren Auftrags
	}
	a.osj.release, held = release, false
	a.osj.mu.Unlock()
	jobRemoveOldDumps()
	a.osJobPut(s)
	a.jobKick()
	return hints, nil
}

// osJobStep führt den Auftrag einen Schritt weiter; true = beendet.
func (a *updateAgent) osJobStep(ctx context.Context) bool {
	a.osj.mu.Lock()
	s := a.osj.job
	a.osj.mu.Unlock()
	if jobEnded(s.Job) {
		return true
	}
	orig := s
	j := &s.Job
	now := a.now()
	switch j.Phase {
	case ha.UpPrepare:
		a.osJobPrepare(ctx, &s)
	case ha.UpCountdown:
		switch {
		case j.AbortedBy != "":
			a.osJobFinish(&s, ha.UpFailed, "Abgebrochen von "+j.AbortedBy, false)
		case !now.Before(j.CountdownUntil):
			j.Phase, j.PhaseAt, j.LockedAt = ha.UpLocked, now, now
		}
	case ha.UpLocked:
		a.osJobLocked(ctx, &s)
	case ha.UpRelease:
		a.osJobUnlock(ctx, &s)
		if s.OS.Failed {
			a.osJobFinish(&s, ha.UpFailed, s.OS.Msg, true)
		} else {
			a.osJobFinish(&s, ha.UpDone, s.OS.Msg, true)
		}
	default:
		a.osJobFinish(&s, ha.UpFailed, "Unbekannte Phase "+string(j.Phase), true)
	}
	if ctx.Err() == nil || jobEnded(s.Job) {
		if !reflect.DeepEqual(s, orig) {
			a.osJobPut(s)
		}
	}
	if jobEnded(s.Job) {
		a.osJobReleaseLock(s.OS.Unit)
		return true
	}
	return false
}

// osJobPrepare: Pakete prüfen (check, Anzahl), dann Countdown.
func (a *updateAgent) osJobPrepare(ctx context.Context, s *osJob) {
	j := &s.Job
	if j.AbortedBy != "" {
		a.osJobFinish(s, ha.UpFailed, "Abgebrochen von "+j.AbortedBy, false)
		return
	}
	persist := func() error { return a.osJobPut(*s) }
	a.osAdvance(ctx, &s.OS, persist)
	if ctx.Err() != nil {
		return
	}
	if s.OS.Step == "done" {
		if s.OS.Failed {
			a.osJobFinish(s, ha.UpFailed, s.OS.Msg, true)
		} else { // nichts zu tun: Ziel erreicht
			a.osJobFinish(s, ha.UpDone, s.OS.Msg, true)
		}
		return
	}
	j.Phase, j.PhaseAt = ha.UpCountdown, a.now()
	j.CountdownUntil = j.PhaseAt.Add(time.Duration(s.CountdownMin) * time.Minute)
}

// osJobLocked: Sperre in der Datenbank, pdh stoppen, Sicherung, dann die Schritte der Pakete.
func (a *updateAgent) osJobLocked(ctx context.Context, s *osJob) {
	j := &s.Job
	switch s.OS.Step {
	case "", "stop":
		if j.AbortedBy != "" {
			a.osJobAbandon(ctx, s, "Abgebrochen von "+j.AbortedBy)
			return
		}
		db, err := a.jobDBEnv()
		if err == nil {
			db["PDH_UPD_COMMIT"] = osJobLockCommit
			db["PDH_UPD_NOT_BEFORE"] = strconv.FormatInt(j.LockedAt.Unix(), 10)
			err = a.jobHost(ctx, jobLockScript, db)
		}
		if err != nil {
			a.osJobAbandon(ctx, s, "Sperre nicht gesetzt: "+err.Error())
			return
		}
		db["PDH_UPD_DUMP"] = s.Dump
		// pdh vor der Sicherung stoppen: sonst gingen Schreibvorgänge zwischen Sicherung und Einspielen verloren
		if err := a.jobStopPDH(ctx); err != nil {
			a.osJobAbandon(ctx, s, "pdh vor der Sicherung nicht gestoppt: "+err.Error())
			return
		}
		if err := a.jobHost(ctx, jobDumpScript, db); err != nil {
			a.osJobAbandon(ctx, s, "Sicherung vor dem Update fehlgeschlagen: "+err.Error())
			return
		}
		if h := a.osBeforeInstall; h != nil {
			h()
		}
		aborted, err := a.osJobBeginInstall(s)
		switch {
		case aborted != "":
			a.osJobAbandon(ctx, s, "Abgebrochen von "+aborted)
		case err != nil:
			// ohne gesicherten Stand nichts einspielen: ein Neustart des Agenten würde die Einheit übersehen
			a.osJobAbandon(ctx, s, osNotSavedMsg+" – nichts eingespielt: "+err.Error())
		}
		return
	}
	before := s.OS.Step
	persist := func() error { return a.osJobPut(*s) }
	done := a.osAdvance(ctx, &s.OS, persist) // wartet der Neustart auf eine Netz-Änderung, steht der Grund in OS.Hold
	j.Result = s.OS.Hold
	if done {
		j.Phase, j.PhaseAt = ha.UpRelease, a.now()
		return
	}
	if ctx.Err() != nil && before == "install" {
		// Der Agent wird beendet: das ist kein Fehler beim Einspielen. Der Stand bleibt „install“
		// und wird nach dem Neustart des Agenten wieder aufgenommen (loadOSJob).
		a.osj.mu.Lock()
		*s = a.osj.job
		a.osj.mu.Unlock()
	}
}

// osJobBeginInstall schaltet auf „install“ um und entscheidet dabei unteilbar über einen
// Abbruch (abortOSJob nimmt dieselbe Sperre): entweder gewinnt der Abbruch (Rückgabe: Name,
// es wird nicht eingespielt) oder er wird danach mit „Abbruch nicht mehr möglich“ abgelehnt.
func (a *updateAgent) osJobBeginInstall(s *osJob) (abortedBy string, err error) {
	a.osj.saveMu.Lock()
	defer a.osj.saveMu.Unlock()
	a.osj.mu.Lock()
	cur := a.osj.job
	if cur.Job.ID == s.Job.ID && cur.Job.AbortedBy != "" {
		a.osj.mu.Unlock()
		s.Job.AbortedBy = cur.Job.AbortedBy
		return cur.Job.AbortedBy, nil
	}
	s.OS.Step = "install" // pdh steht schon
	s.Job.OSStep = "install"
	s.Job.Seq = max(cur.Job.Seq, s.Job.Seq) + 1
	a.osj.job = *s
	a.osj.mu.Unlock()
	if err := a.writeState(osJobFile(), *s); err != nil {
		log.Printf("betriebssystem: os-job.json nicht gespeichert: %v", err)
		return "", err
	}
	return "", nil
}

// osJobAbandon: Fehler vor dem Einspielen – Datenbank unverändert, Sperr-Vermerk weg, pdh wieder an.
func (a *updateAgent) osJobAbandon(ctx context.Context, s *osJob, reason string) {
	if ctx.Err() != nil {
		return // Agent wird beendet: nach dem Neustart noch einmal von vorn
	}
	a.osJobUnlock(ctx, s)
	if err := a.jobHost(ctx, jobStartScript, nil); err != nil {
		log.Printf("betriebssystem: ALARM: pdh nicht wieder gestartet: %v", err)
		reason += "; pdh nicht wieder gestartet – bitte von Hand prüfen"
	}
	s.OS.Step = "done"
	s.OS.fail(reason)
	a.osJobFinish(s, ha.UpFailed, reason, !strings.HasPrefix(reason, "Abgebrochen von "))
}

// osJobUnlock: Sperr-Vermerk in der Datenbank entfernen (nur nach der Sperre).
func (a *updateAgent) osJobUnlock(ctx context.Context, s *osJob) {
	sj := singleJob{Job: s.Job}
	a.jobUnlock(ctx, &sj)
}

// osJobFinish beendet den Auftrag und trägt das Ergebnis (bei record) als „letzter Lauf“ ein.
func (a *updateAgent) osJobFinish(s *osJob, p ha.UpdatePhase, result string, record bool) {
	j := &s.Job
	now := a.now()
	if j.Locked() {
		j.ReleasedAt = now
	}
	j.Phase, j.PhaseAt, j.Result = p, now, result
	j.OSStep = s.OS.Step
	if record {
		a.osRecordLastRun(now, s.OS, p == ha.UpDone, result)
	}
	log.Printf("betriebssystem: %s", fmt.Sprintf("%s: %s", p, result))
}

// osRecordLastRun trägt den letzten Lauf der Pakete ein (Betriebssystem-Auftrag und PDH-Auftrag mit Haken).
func (a *updateAgent) osRecordLastRun(now time.Time, st osSteps, ok bool, msg string) {
	lr := osLastRun{At: now.UTC().Format(time.RFC3339), Packages: st.Pending, Rebooted: st.Rebooted, OK: ok, Message: msg}
	a.osUpdateState(func(s *osState) { s.LastRun = &lr })
}

// osJobReleaseLock gibt die Sperre frei (einmal). Läuft die Einheit von „install“ noch (z. B.
// nach „dauert zu lange“), erst wenn sie beendet ist.
func (a *updateAgent) osJobReleaseLock(unit string) {
	a.osj.mu.Lock()
	r := a.osj.release
	a.osj.release = nil
	a.osj.mu.Unlock()
	if r != nil {
		a.osReleaseAfterUnit(r, unit)
	}
}

// osReleaseAfterUnit gibt die Sperre r frei – läuft die Einheit unit noch, erst wenn sie beendet ist
// (gilt für den Betriebssystem-Auftrag und den PDH-Auftrag mit Haken).
func (a *updateAgent) osReleaseAfterUnit(r func(), unit string) {
	if unit != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		active := a.osUnitIsActive(ctx, unit)
		cancel()
		if active {
			a.osWaitUnit(unit, r)
			return
		}
	}
	r()
}

// abortOSJob: Abbruch durch den Administrator, nur bis zum Einspielen der Pakete.
func (a *updateAgent) abortOSJob(by string) error {
	a.osj.saveMu.Lock()
	defer a.osj.saveMu.Unlock()
	a.osj.mu.Lock()
	j := &a.osj.job.Job
	switch {
	case jobEnded(*j):
		a.osj.mu.Unlock()
		return errors.New("Kein Update-Auftrag aktiv")
	case j.Phase == ha.UpRelease:
		a.osj.mu.Unlock()
		return errors.New("Das Update wird bereits beendet")
	case j.Phase == ha.UpLocked && a.osj.job.OS.Step != "" && a.osj.job.OS.Step != "stop":
		a.osj.mu.Unlock()
		return errors.New(osAbortLateMsg)
	case j.AbortedBy != "":
		a.osj.mu.Unlock()
		return nil
	}
	j.AbortedBy = by
	j.Seq++
	s := a.osj.job
	a.osj.mu.Unlock()
	if err := writeStateJSON(osJobFile(), s); err != nil {
		log.Printf("betriebssystem: os-job.json nicht gespeichert: %v", err)
	}
	log.Printf("betriebssystem: Abbruch durch %s", by)
	return nil
}

// osJobStartHandler: POST /v1/os/job/start {"force":bool} (Knopf).
func (a *updateAgent) osJobStartHandler(w http.ResponseWriter, r *http.Request) {
	if refuseDocker(a, w) {
		return
	}
	var req struct {
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		drRefuse(w, "ungültige Anfrage")
		return
	}
	hints, err := a.startOSJob(r.Context(), srcButton, req.Force)
	if hints == nil {
		hints = []string{}
	}
	var nr jobNotReady
	switch {
	case errors.As(err, &nr):
		if nr.hints == nil {
			nr.hints = []string{}
		}
		writeMountJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": nr.reason, "hints": nr.hints})
	case err != nil:
		writeMountJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
	default:
		drJSON(w, map[string]any{"ok": true, "hints": hints})
	}
}
