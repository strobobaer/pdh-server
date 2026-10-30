package main

// Update auf dem Einzelserver (docs/superpowers/specs/2026-10-30-verbund-update-design.md §5.3):
// Der Agent führt den ganzen Auftrag selbst aus – im Hintergrund bauen,
// Countdown, Sperre, Sicherung vor dem Update, Neustart mit X, prüfen, bei
// Fehler Datenbank und alten Stand zurück. Phasen und Anzeige wie der
// Auftrag von pdh-ha (ha.UpdateJob, Art „alle nacheinander“ → „Neustart“).
// Der Auftrag liegt in <drStateDir>/update-job.json und läuft nach einem
// Neustart des Agenten (am Ende von --finish bzw. --rollback) in derselben
// Phase weiter. Host-Befehle: job_host.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
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

// Quellen eines Auftrags (wie pdh-ha).
const (
	srcButton = "knopf"
	srcWindow = "zeitplan"
)

const (
	jobVerifyWait = 10 * time.Minute // Prüffrist der neuen bzw. alten Version
	jobPoll       = 3 * time.Second
	jobSelf       = "self" // Schlüssel des Alt-Commits in UpdateJob.Old
)

// Schritte im Zurücksetzen (singleJob.RbStep).
const (
	rbStop    = "stop"
	rbRestore = "restore"
	rbScript  = "script"
	rbVerify  = "verify"
)

// singleJob: Auftrag auf dem Einzelserver (update-job.json).
type singleJob struct {
	Job          ha.UpdateJob `json:"job"`
	CountdownMin int          `json:"countdown_min"`
	LockMaxMin   int          `json:"lock_max_min"`
	Dump         string       `json:"dump"`      // Sicherung vor dem Update (pg_dump -Fc)
	Finishing    bool         `json:"finishing"` // --finish gestartet: Programm und Datenbank evtl. schon neu
	RbStep       string       `json:"rb_step"`   // nächster Schritt im Zurücksetzen
	RbAt         time.Time    `json:"rb_at"`     // Beginn der Prüfung nach dem Zurücksetzen
}

type jobRuntime struct {
	mu      sync.Mutex
	job     singleJob // Phase leer = kein Auftrag
	looping bool
	saveMu  sync.Mutex
}

// jobNotReady: Prüfung „alles grün“ nicht bestanden (HTTP 409).
type jobNotReady struct {
	reason string
	hints  []string
}

func (e jobNotReady) Error() string { return e.reason }

func singleJobFile() string { return filepath.Join(drStateDir(), "update-job.json") }

func jobEnded(j ha.UpdateJob) bool {
	return j.Phase == ha.UpNone || j.Phase == ha.UpDone || j.Phase == ha.UpFailed
}

func (a *updateAgent) singleJobActive() bool {
	a.sj.mu.Lock()
	defer a.sj.mu.Unlock()
	return !jobEnded(a.sj.job.Job)
}

// loadSingleJob: beim Start des Agenten; ein offener Auftrag läuft weiter.
func (a *updateAgent) loadSingleJob() {
	b, err := os.ReadFile(singleJobFile())
	if err != nil {
		return
	}
	var s singleJob
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("update: update-job.json nicht lesbar: %v", err)
		return
	}
	a.sj.mu.Lock()
	a.sj.job = s
	a.sj.mu.Unlock()
	if !jobEnded(s.Job) {
		log.Printf("update: Auftrag %s wird in Phase %s fortgesetzt", s.Job.ID, s.Job.Phase)
		a.jobKick()
	}
}

// jobPut übernimmt den Stand eines Schritts und speichert ihn. Ein
// inzwischen eingegangener Abbruch bleibt erhalten.
func (a *updateAgent) jobPut(s singleJob) {
	a.sj.saveMu.Lock()
	defer a.sj.saveMu.Unlock()
	a.sj.mu.Lock()
	cur := a.sj.job.Job
	if cur.ID == s.Job.ID && cur.AbortedBy != "" && s.Job.AbortedBy == "" {
		s.Job.AbortedBy = cur.AbortedBy
	}
	s.Job.Seq = max(cur.Seq, s.Job.Seq) + 1
	a.sj.job = s
	a.sj.mu.Unlock()
	if err := writeStateJSON(singleJobFile(), s); err != nil {
		log.Printf("update: update-job.json nicht gespeichert: %v", err)
	}
}

func (a *updateAgent) jobView() ha.UpdateView {
	a.sj.mu.Lock()
	s := a.sj.job
	a.sj.mu.Unlock()
	return s.Job.View(a.now(), time.Duration(s.LockMaxMin)*time.Minute, a.windowSnapshot().Faulty)
}

// startSingleJob legt den Auftrag an (Ziel: Stand des Update-Zweigs).
// force (nur Knopf): trotz nicht grün.
func (a *updateAgent) startSingleJob(ctx context.Context, source string, force bool) ([]string, error) {
	if a.mode != "systemd" {
		return nil, errors.New(dockerNotAvailMsg)
	}
	if _, cluster, err := a.haCluster(ctx); err != nil {
		return nil, fmt.Errorf("pdh-ha nicht erreichbar: %w", err)
	} else if cluster {
		return nil, errors.New("Im Verbund startet pdh-ha das Update")
	}
	if a.singleJobActive() {
		return nil, errors.New("Ein Update-Auftrag läuft bereits")
	}
	a.mu.RLock()
	running := a.status.Running
	a.mu.RUnlock()
	if running {
		return nil, errors.New("Ein Update läuft bereits")
	}
	target, old, err := a.updateTarget(ctx)
	if err != nil {
		return nil, err
	}
	ws := a.windowSnapshot()
	if source == srcWindow && commitIn(ws.Faulty, target) {
		return nil, jobNotReady{reason: "Neue Version " + shortSHA(target) + " ist als fehlerhaft gemerkt"}
	}
	var hints []string
	configured, baseOK, _ := a.drBackupReport()
	free, known := a.freeSpaceFn()(a.repoDir)
	if !known {
		free, hints = math.MaxInt64, append(hints, "Freier Platz nicht geprüft")
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
	oldVersion, _ := a.git(ctx, "show", old+":VERSION")
	win := ws.Window
	if win.Validate() != nil {
		win = ha.DefaultWindow()
	}
	now := a.now()
	s := singleJob{
		Job: ha.UpdateJob{ID: "einzel-" + now.UTC().Format("20060102T150405Z"), Kind: ha.UpdateSequential, Source: source,
			Target: target, OldVersion: oldVersion, Old: map[string]string{jobSelf: old},
			Phase: ha.UpPrepare, PhaseAt: now, StartedAt: now},
		CountdownMin: win.CountdownMin, LockMaxMin: win.LockMaxMin,
		Dump: filepath.Join(drStateDir(), "vor-update-"+target+".dump"),
	}
	a.sj.mu.Lock()
	if !jobEnded(a.sj.job.Job) {
		a.sj.mu.Unlock()
		return nil, errors.New("Ein Update-Auftrag läuft bereits")
	}
	a.sj.job = singleJob{}
	a.sj.mu.Unlock()
	jobRemoveOldDumps()
	a.jobPut(s)
	a.jobKick()
	return hints, nil
}

// jobKick startet die Schleife des Auftrags (höchstens eine).
func (a *updateAgent) jobKick() {
	if a.jobManual {
		return
	}
	a.sj.mu.Lock()
	if a.sj.looping {
		a.sj.mu.Unlock()
		return
	}
	a.sj.looping = true
	a.sj.mu.Unlock()
	go func() {
		for {
			if a.jobStep(context.Background()) {
				a.sj.mu.Lock()
				if jobEnded(a.sj.job.Job) {
					a.sj.looping = false
					a.sj.mu.Unlock()
					return
				}
				a.sj.mu.Unlock()
			}
			time.Sleep(jobPoll)
		}
	}()
}

// jobStep führt den Auftrag einen Schritt weiter; true = beendet.
func (a *updateAgent) jobStep(ctx context.Context) bool {
	a.sj.mu.Lock()
	s := a.sj.job
	a.sj.mu.Unlock()
	if jobEnded(s.Job) {
		return true
	}
	orig := s
	j := &s.Job
	now := a.now()
	switch j.Phase {
	case ha.UpPrepare:
		a.jobPrepare(ctx, &s)
	case ha.UpCountdown:
		switch {
		case j.AbortedBy != "":
			a.jobUndoCheckout(ctx, &s)
			a.jobFinish(&s, ha.UpFailed, "Abgebrochen von "+j.AbortedBy, false)
		case !now.Before(j.CountdownUntil):
			a.jobEnter(&s, ha.UpLocked)
			j.LockedAt = now
		}
	case ha.UpLocked:
		a.jobLocked(ctx, &s)
	case ha.UpVerifyNew:
		lockMax := time.Duration(s.LockMaxMin) * time.Minute
		herr := error(nil)
		if j.AbortedBy == "" {
			herr = a.pdhHealthy(ctx, j.TargetVersion, j.Target)
		}
		switch {
		case j.AbortedBy != "":
			a.jobRollback(&s, "Abgebrochen von "+j.AbortedBy, false)
		case herr == nil:
			a.jobEnter(&s, ha.UpRelease)
		case now.Sub(j.LockedAt) >= lockMax:
			a.jobRollback(&s, "Höchstdauer der Sperre überschritten", true)
		case now.Sub(j.PhaseAt) >= jobVerifyWait:
			a.jobRollback(&s, "Neue Version antwortet nicht: "+herr.Error(), true)
		}
	case ha.UpRelease:
		a.jobUnlock(ctx, &s)
		a.jobFinish(&s, ha.UpDone, jobName(*j)+" abgeschlossen", false)
	case ha.UpRollback:
		a.jobRollbackStep(ctx, &s)
	default:
		a.jobFinish(&s, ha.UpFailed, "Unbekannte Phase "+string(j.Phase), false)
	}
	if !reflect.DeepEqual(s, orig) {
		a.jobPut(s) // beim Warten (Countdown, Prüfen) nicht in jedem Takt schreiben
	}
	return jobEnded(s.Job)
}

// jobPrepare: im Hintergrund bauen (laufendes pdh unverändert), dann Countdown.
func (a *updateAgent) jobPrepare(ctx context.Context, s *singleJob) {
	j := &s.Job
	if j.AbortedBy != "" {
		a.jobFinish(s, ha.UpFailed, "Abgebrochen von "+j.AbortedBy, false)
		return
	}
	if err := a.jobScript(ctx, "--commit", j.Target, "--no-restart"); err != nil {
		// Nur ein Fehler beim Bauen spricht gegen X. Abruf/Netz, Ziel nicht im
		// Zweig oder ein anderer Lauf nicht – der Checkout steht dann noch auf Alt.
		head, herr := a.git(ctx, "rev-parse", "HEAD")
		switch {
		case errors.Is(err, errUpdateBusy):
			a.jobFinish(s, ha.UpFailed, err.Error(), false)
		case herr == nil && head == j.Old[jobSelf]:
			a.jobFinish(s, ha.UpFailed, "Neue Version nicht abgerufen: "+err.Error(), false)
		default:
			a.jobUndoCheckout(ctx, s)
			a.jobFinish(s, ha.UpFailed, "Neue Version konnte nicht gebaut werden: "+err.Error(), herr == nil)
		}
		return
	}
	j.TargetVersion, _ = a.git(ctx, "show", j.Target+":VERSION")
	a.jobEnter(s, ha.UpCountdown)
	j.CountdownUntil = j.PhaseAt.Add(time.Duration(s.CountdownMin) * time.Minute)
}

// jobLocked: Sperre in der Datenbank, Sicherung vor dem Update, Neustart mit X.
func (a *updateAgent) jobLocked(ctx context.Context, s *singleJob) {
	j := &s.Job
	if s.Finishing {
		// Der Agent wurde am Ende von --finish neu gestartet: prüfen entscheidet.
		a.jobEnter(s, ha.UpVerifyNew)
		return
	}
	if j.AbortedBy != "" {
		a.jobAbandon(ctx, s, "Abgebrochen von "+j.AbortedBy)
		return
	}
	db, err := a.jobDBEnv()
	if err == nil {
		db["PDH_UPD_COMMIT"] = j.Target
		db["PDH_UPD_NOT_BEFORE"] = strconv.FormatInt(j.LockedAt.Unix(), 10) // Unix-Sekunden (R16)
		err = a.jobHost(ctx, jobLockScript, db)
	}
	if err != nil {
		a.jobAbandon(ctx, s, "Sperre nicht gesetzt: "+err.Error())
		return
	}
	db["PDH_UPD_DUMP"] = s.Dump
	if err := a.jobHost(ctx, jobDumpScript, db); err != nil {
		a.jobAbandon(ctx, s, "Sicherung vor dem Update fehlgeschlagen: "+err.Error())
		return
	}
	s.Finishing = true
	a.jobPut(*s) // vor --finish speichern: der Agent startet danach neu
	if err := a.jobScript(ctx, "--finish"); err != nil {
		if errors.Is(err, errUpdateBusy) {
			s.Finishing = false // nichts eingesetzt
			a.jobAbandon(ctx, s, err.Error())
			return
		}
		a.jobRollback(s, "Neue Version konnte nicht eingesetzt werden: "+err.Error(), true)
		return
	}
	a.jobEnter(s, ha.UpVerifyNew)
}

// jobAbandon: Fehler in der Sperre vor dem Neustart – Datenbank unverändert,
// nur Checkout zurück; X nicht fehlerhaft.
func (a *updateAgent) jobAbandon(ctx context.Context, s *singleJob, reason string) {
	a.jobUndoCheckout(ctx, s)
	a.jobUnlock(ctx, s)
	a.jobFinish(s, ha.UpFailed, reason, false)
}

// jobRollback: Zurücksetzen beginnen (Datenbank aus der Sicherung, alter Stand).
func (a *updateAgent) jobRollback(s *singleJob, reason string, faulty bool) {
	a.jobEnter(s, ha.UpRollback)
	s.Job.Result, s.Job.MarkFaulty = reason, faulty
	s.RbStep, s.RbAt = rbStop, time.Time{}
	if !s.Finishing {
		s.RbStep = rbScript
	}
}

// jobRollbackStep: pdh stoppen, Datenbank zurückspielen, alten Stand
// einspielen (startet pdh und den Agenten neu), prüfen, Freigabe.
func (a *updateAgent) jobRollbackStep(ctx context.Context, s *singleJob) {
	j := &s.Job
	old := j.Old[jobSelf]
	if !s.Finishing {
		a.jobAbandon(ctx, s, j.Result)
		return
	}
	for {
		switch s.RbStep {
		case rbStop:
			if err := a.jobHost(ctx, jobStopScript, nil); err != nil {
				log.Printf("update: pdh nicht gestoppt: %v", err)
			}
			s.RbStep = rbRestore
			if a.pdhActive(ctx) {
				a.sleepFn()(5 * time.Second)
				if a.pdhActive(ctx) {
					// Nie unter einem laufenden pdh die Datenbank tauschen.
					const msg = "pdh ließ sich nicht stoppen – Datenbank nicht zurückgespielt"
					log.Printf("update: ALARM: %s", msg)
					j.Result += "; " + msg
					s.RbStep = rbScript
				}
			}
		case rbRestore:
			if err := a.jobRestoreDB(ctx, s); err != nil {
				j.Result += "; Datenbank nicht zurückgespielt: " + err.Error()
			}
			s.RbStep = rbScript
		case rbScript:
			s.RbStep, s.RbAt = rbVerify, time.Time{}
			a.jobPut(*s) // der Agent startet am Ende von --rollback neu
			if err := a.jobScript(ctx, "--rollback", old); err != nil {
				j.Result += "; alter Stand nicht eingespielt: " + err.Error()
			}
			s.RbAt = a.now()
		default: // rbVerify
			now := a.now()
			if s.RbAt.IsZero() {
				s.RbAt = now
			}
			err := a.pdhHealthy(ctx, j.OldVersion, old)
			if err != nil && now.Sub(s.RbAt) < jobVerifyWait {
				return
			}
			if err != nil {
				j.Result += "; Zurücksetzen nicht abgeschlossen (" + err.Error() + ") – bitte von Hand prüfen"
			}
			a.jobUnlock(ctx, s)
			a.jobFinish(s, ha.UpFailed, j.Result, j.MarkFaulty)
			return
		}
		a.jobPut(*s)
	}
}

// jobUndoCheckout: Checkout und gebaute Dateien zurück auf den alten Stand,
// ohne Neustart (das laufende pdh ist noch der alte Stand).
func (a *updateAgent) jobUndoCheckout(ctx context.Context, s *singleJob) {
	if err := a.jobScript(ctx, "--rollback", s.Job.Old[jobSelf], "--no-restart"); err != nil {
		log.Printf("update: Checkout nicht zurückgedreht: %v", err)
	}
}

// jobUnlock: Sperr-Vermerk in der Datenbank entfernen (nur nach der Sperre).
func (a *updateAgent) jobUnlock(ctx context.Context, s *singleJob) {
	if s.Job.LockedAt.IsZero() {
		return
	}
	db, err := a.jobDBEnv()
	if err == nil {
		err = a.jobHost(ctx, jobUnlockScript, db)
	}
	if err != nil {
		log.Printf("update: Sperr-Vermerk nicht entfernt: %v", err)
	}
}

func (a *updateAgent) jobEnter(s *singleJob, p ha.UpdatePhase) {
	s.Job.Phase, s.Job.PhaseAt = p, a.now()
}

// jobFinish beendet den Auftrag (Freigabe) und trägt das Ergebnis und ggf.
// den fehlerhaften Stand ins Fenster ein.
func (a *updateAgent) jobFinish(s *singleJob, p ha.UpdatePhase, result string, faulty bool) {
	j := &s.Job
	now := a.now()
	locked := j.Locked()
	if locked {
		j.ReleasedAt = now
	}
	j.Phase, j.PhaseAt, j.Result, j.MarkFaulty = p, now, result, faulty
	summary := result
	switch {
	case p == ha.UpDone:
	case !j.LockedAt.IsZero():
		summary = jobName(*j) + " zurückgesetzt: " + result
	default:
		summary = jobName(*j) + " nicht eingespielt: " + result
	}
	a.windowUpdate(func(ws *windowState) {
		ws.LastResult = summary
		if faulty && !commitIn(ws.Faulty, j.Target) {
			ws.Faulty = append(ws.Faulty, j.Target)
		}
	})
	log.Printf("update: %s", summary)
}

func jobName(j ha.UpdateJob) string {
	if j.TargetVersion != "" {
		return "Update auf " + j.TargetVersion
	}
	return "Update auf " + shortSHA(j.Target)
}

// abortSingleJob: Abbruch durch den Administrator (vor der Sperre: Abbruch,
// in der Sperre: Zurücksetzen; X nicht fehlerhaft).
func (a *updateAgent) abortSingleJob(by string) error {
	by = strings.TrimSpace(by)
	if r := []rune(by); len(r) > 100 {
		by = string(r[:100])
	}
	if by == "" {
		by = "Administrator"
	}
	a.sj.saveMu.Lock()
	defer a.sj.saveMu.Unlock()
	a.sj.mu.Lock()
	j := &a.sj.job.Job
	switch {
	case jobEnded(*j):
		a.sj.mu.Unlock()
		return errors.New("Kein Update-Auftrag aktiv")
	case j.Phase == ha.UpRollback || j.Phase == ha.UpRelease:
		a.sj.mu.Unlock()
		return errors.New("Das Update wird bereits beendet bzw. zurückgesetzt")
	case j.AbortedBy != "":
		a.sj.mu.Unlock()
		return nil
	}
	// nur das Feld am aktuellen Auftrag – nie eine ältere Kopie zurückschreiben
	j.AbortedBy = by
	j.Seq++
	s := a.sj.job
	a.sj.mu.Unlock()
	if err := writeStateJSON(singleJobFile(), s); err != nil {
		log.Printf("update: update-job.json nicht gespeichert: %v", err)
	}
	log.Printf("update: Abbruch durch %s", by)
	return nil
}

// ── Schnittstelle ─────────────────────────────────────────────────────────

// jobHandler: GET /v1/update/job → {"ok":true, …ha.UpdateView}
func (a *updateAgent) jobHandler(w http.ResponseWriter, _ *http.Request) {
	if refuseDocker(a, w) {
		return
	}
	drJSON(w, struct {
		OK bool `json:"ok"`
		ha.UpdateView
	}{true, a.jobView()})
}

// jobStartHandler: POST /v1/update/job/start {"force":bool} (Knopf).
func (a *updateAgent) jobStartHandler(w http.ResponseWriter, r *http.Request) {
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
	hints, err := a.startSingleJob(r.Context(), srcButton, req.Force)
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

// jobAbortHandler: POST /v1/update/job/abort {"by":"Name"}
func (a *updateAgent) jobAbortHandler(w http.ResponseWriter, r *http.Request) {
	if refuseDocker(a, w) {
		return
	}
	var req struct {
		By string `json:"by"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		drRefuse(w, "ungültige Anfrage")
		return
	}
	if err := a.abortSingleJob(req.By); err != nil {
		drRefuse(w, err.Error())
		return
	}
	drJSON(w, map[string]any{"ok": true})
}
