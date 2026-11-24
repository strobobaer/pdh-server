package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

// PDH-Auftrag mit Haken „Betriebssystem mit einspielen“ und Wartungsfenster mit
// Sicherheitsupdates (job.go, window.go).

// osStatusSec: Status mit pkgs Paketen, davon sec Sicherheitspakete.
func osStatusSec(pkgs, sec int, reboot bool) string {
	var list []string
	for i := 0; i < pkgs; i++ {
		list = append(list, fmt.Sprintf(`{"name":"p%d","old":"1","new":"2","security":%t}`, i, i < sec))
	}
	return fmt.Sprintf(`{"ok":true,"script_version":2,"system":"Debian","kernel":"6.12","checked_at":"2026-11-24T10:00:00Z",`+
		`"upgradable":[%s],"security_count":%d,"reboot_required":%t,"reboot_pkgs":[]}`, strings.Join(list, ","), sec, reboot)
}

// pdhOSEnv: Einzelserver mit neuer PDH-Version, Haken an, pkgs Paketen.
func pdhOSEnv(t *testing.T, pkgs int, rebootAfter bool) *osStepsEnv {
	t.Helper()
	e := osJobEnv(t, pkgs, rebootAfter)
	e.health.set("0.77.9")
	e.a.osUpdateState(func(st *osState) { st.WithPDHUpdate = true })
	return e
}

func runPDHJob(t *testing.T, e *osStepsEnv, max int) {
	t.Helper()
	runJob(t, e.a, e.clk, max)
}

func afterFinish(all string) string {
	_, rest, _ := strings.Cut(all, "script --finish")
	return rest
}

func TestSingleJobWithOS(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	a := e.a
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 60)
	all := e.rec.all()
	assertOrder(t, all, "script --commit "+jobNewSHA+" --no-restart", "script --finish", "os check", "os status",
		"stop-pdh", "os install", "start-pdh", "psql-unlock")
	if strings.Contains(all, "script --rollback") {
		t.Fatalf("Zurücksetzen: %s", all)
	}
	v := a.jobView()
	if v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "Betriebssystem: 3 Pakete, Neustart nein") {
		t.Fatalf("Ende: %+v", v)
	}
	if lr := a.windowSnapshot().LastResult; !strings.Contains(lr, "Betriebssystem: 3 Pakete, Neustart nein") {
		t.Fatalf("LastResult: %q", lr)
	}
	if a.lockHolder() != "" {
		t.Fatalf("Sperre nicht frei: %s", a.lockHolder())
	}
}

func TestSingleJobWithOSReboot(t *testing.T) {
	e := pdhOSEnv(t, 3, true)
	a := e.a
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		a.jobStep(context.Background())
		e.clk.add(time.Minute)
		a.sj.mu.Lock()
		step := a.sj.job.OS.Step
		a.sj.mu.Unlock()
		if step == "boot" {
			break
		}
	}
	v := a.jobView()
	if !v.Active || v.Phase != string(ha.UpRelease) || v.Step != 4 || v.Text != "Server startet neu" {
		t.Fatalf("vor dem Neustart: %+v", v)
	}
	// Agent und Rechner starten neu
	b := &updateAgent{mode: "systemd", repoDir: a.repoDir, token: a.token, netStateDir: a.netStateDir}
	b.dr.Config = a.dr.Config
	b.windowDefaults()
	b.wireJobFakes(e.rec, e.clk)
	b.osRun = e.fake.run
	b.bootID = a.bootID
	e.boot.Store("boot-2")
	b.loadSingleJob()
	runJob(t, b, e.clk, 40)
	all := e.rec.all()
	parts := strings.Split(all, " | ")
	count := func(l string) (n int) {
		for _, p := range parts {
			if p == l {
				n++
			}
		}
		return
	}
	if count("os install") != 1 || count("reboot") != 1 {
		t.Fatalf("Ablauf: %s", all)
	}
	assertOrder(t, all, "os install", "reboot", "start-pdh", "psql-unlock")
	if v := b.jobView(); v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "Neustart ja") {
		t.Fatalf("Ende: %+v", v)
	}
}

func TestSingleJobWithOSNoPending(t *testing.T) {
	e := pdhOSEnv(t, 0, false)
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 60)
	rest := afterFinish(e.rec.all())
	if !strings.Contains(rest, "os check") || strings.Contains(rest, "stop-pdh") || strings.Contains(rest, "os install") {
		t.Fatalf("Ablauf nach --finish: %s", rest)
	}
	v := e.a.jobView()
	if v.Phase != string(ha.UpDone) || strings.Contains(v.Result, "Betriebssystem") || v.Result != "Update auf 0.77.9 abgeschlossen" {
		t.Fatalf("Ende: %+v", v)
	}
}

func TestSingleJobWithOSInstallFails(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	e.fake.installErr = errors.New("exit status 100")
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 60)
	v := e.a.jobView()
	if v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "Betriebssystem nicht aktualisiert") ||
		!strings.Contains(v.Result, "Update auf 0.77.9 abgeschlossen") {
		t.Fatalf("Ende: %+v", v)
	}
	if f := e.a.windowSnapshot().Faulty; len(f) != 0 {
		t.Fatalf("fehlerhaft gemerkt: %v", f)
	}
	all := e.rec.all()
	if strings.Contains(all, "script --rollback") || strings.Contains(all, "pg_restore") {
		t.Fatalf("Zurücksetzen: %s", all)
	}
	assertOrder(t, all, "script --finish", "os install", "start-pdh", "psql-unlock")
}

func TestSingleJobWithoutOSUnchanged(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	e.a.osUpdateState(func(st *osState) { st.WithPDHUpdate = false })
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 60)
	if strings.Contains(e.rec.all(), "os ") {
		t.Fatalf("Betriebssystem ohne Haken: %s", e.rec.all())
	}
	if v := e.a.jobView(); v.Result != "Update auf 0.77.9 abgeschlossen" {
		t.Fatalf("Ergebnis: %+v", v)
	}
}

func TestSingleJobWithOSSnapshot(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	e.a.osUpdateState(func(st *osState) { st.WithPDHUpdate = false }) // nach dem Start ausgeschaltet
	runPDHJob(t, e, 60)
	if !strings.Contains(e.rec.all(), "os install") {
		t.Fatalf("Pakete trotz Wert aus WithOS nicht eingespielt: %s", e.rec.all())
	}
}

func TestSingleJobAbortDuringOS(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	a := e.a
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	a.sj.mu.Lock()
	a.sj.job.Job.Phase = ha.UpRelease
	a.sj.job.WithOS = true
	a.sj.job.OS = osSteps{Step: "install", Pending: 3}
	a.sj.mu.Unlock()
	if err := a.abortSingleJob("Anna"); err == nil || !strings.Contains(err.Error(), "bereits beendet bzw. zurückgesetzt") {
		t.Fatalf("Abbruch: %v", err)
	}
}

// Ruling 1: Netz-Änderung wartet – kein Neustart auch im PDH-Auftrag.
func TestSingleJobWithOSRebootWaitsForNet(t *testing.T) {
	e := pdhOSEnv(t, 3, true)
	a := e.a
	netWriteState(t, a, "state=pending\nid=x\n")
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		a.jobStep(context.Background())
		e.clk.add(time.Minute)
		a.sj.mu.Lock()
		step := a.sj.job.OS.Step
		a.sj.mu.Unlock()
		if step == "reboot" {
			break
		}
	}
	for i := 0; i < 10; i++ {
		if a.jobStep(context.Background()) {
			t.Fatal("beendet")
		}
		e.clk.add(time.Minute)
	}
	if strings.Contains(e.rec.all(), "reboot") {
		t.Fatalf("Neustart trotz offener Netz-Änderung: %s", e.rec.all())
	}
	if v := a.jobView(); !strings.Contains(v.Result, "Eine Netz-Änderung wartet auf Bestätigung") {
		t.Fatalf("Anzeige: %+v", v)
	}
	netWriteState(t, a, "state=confirmed\nid=x\nmaint=0\nmaint_off=done\n")
	e.boot.Store("boot-1")
	for i := 0; i < 3; i++ {
		a.jobStep(context.Background())
	}
	if strings.Count(e.rec.all(), "reboot") != 1 {
		t.Fatalf("Neustart nach Bestätigung: %s", e.rec.all())
	}

	// Frist: bleibt die Änderung offen, endet der Auftrag ohne Neustart; das PDH-Update bleibt gültig
	e2 := pdhOSEnv(t, 3, true)
	netWriteState(t, e2.a, "state=pending\nid=x\n")
	if _, err := e2.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e2, 120)
	v := e2.a.jobView()
	all := e2.rec.all()
	if v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "Betriebssystem nicht aktualisiert") ||
		!strings.Contains(v.Result, "Eine Netz-Änderung wartet auf Bestätigung") ||
		strings.Contains(all, "| reboot") || !strings.Contains(all, "start-pdh") || len(e2.a.windowSnapshot().Faulty) != 0 {
		t.Fatalf("Frist: %+v / %s", v, all)
	}
}

// Ruling 2: Läuft die Einheit nach dem Ende noch, bleibt die Sperre bis sie endet.
func TestSingleJobWithOSLockUntilUnitEnds(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	a := e.a
	var active atomic.Bool
	active.Store(true)
	a.osUnitActive = func(context.Context, string) bool { return active.Load() }
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	now := e.clk.now()
	a.sj.mu.Lock()
	a.sj.job.Job.Phase, a.sj.job.Job.PhaseAt, a.sj.job.Job.LockedAt = ha.UpRelease, now, now
	a.sj.job.WithOS = true
	a.sj.job.OS = osSteps{Step: "start", Pending: 3, Failed: true, Msg: "3 Pakete eingespielt; x", Unit: "pdh-os-install-1"}
	a.sj.mu.Unlock()
	runPDHJob(t, e, 20)
	if v := a.jobView(); v.Phase != string(ha.UpDone) {
		t.Fatalf("Ende: %+v", v)
	}
	if a.lockHolder() != lockPDH {
		t.Fatalf("Sperre muss bis zum Ende der Einheit bleiben, Besitzer %q", a.lockHolder())
	}
	if _, err := a.startOSJob(context.Background(), srcButton, false); err == nil {
		t.Fatal("zweiter Auftrag neben laufender Einheit")
	}
	active.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for a.lockHolder() != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.lockHolder() != "" {
		t.Fatalf("Sperre nach Ende der Einheit nicht frei: %q", a.lockHolder())
	}
}

// Ruling 3: Wiederaufnahme des PDH-Auftrags im Schritt „install“.
func pdhResumeAgent(e *osStepsEnv, st osSteps) *updateAgent {
	now := e.clk.now()
	s := singleJob{
		Job: ha.UpdateJob{ID: "einzel-x", Kind: ha.UpdateSequential, Source: srcButton, Target: jobNewSHA, TargetVersion: "0.77.9",
			OldVersion: "0.77.8", Old: map[string]string{jobSelf: jobOldSHA}, Phase: ha.UpRelease, PhaseAt: now, StartedAt: now,
			LockedAt: now, OSStep: st.Step},
		CountdownMin: 2, LockMaxMin: 60, Finishing: true, WithOS: true, OS: st,
		Dump: "x.dump",
	}
	_ = writeStateJSON(singleJobFile(), s)
	b := &updateAgent{mode: "systemd", repoDir: e.a.repoDir, token: e.a.token, netStateDir: e.a.netStateDir}
	b.dr.Config = e.a.dr.Config
	b.windowDefaults()
	b.wireJobFakes(e.rec, e.clk)
	b.osRun = e.fake.run
	b.bootID = e.a.bootID
	return b
}

func TestSingleJobWithOSResumeInstallStopsPDH(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	// pdh läuft nach dem Neustart des Rechners wieder; „stop-pdh“ beendet es
	e.rec.mu.Lock()
	e.rec.fail["pdh-active"] = false
	e.rec.mu.Unlock()
	e.rec.on["stop-pdh"] = func() {
		e.rec.mu.Lock()
		e.rec.fail["pdh-active"] = true
		e.rec.mu.Unlock()
	}
	b := pdhResumeAgent(e, osSteps{Step: "install", Pending: 3, StepAt: e.clk.now()})
	b.loadSingleJob()
	runJob(t, b, e.clk, 40)
	assertOrder(t, e.rec.all(), "stop-pdh", "os install", "start-pdh", "psql-unlock")
	if v := b.jobView(); v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "Betriebssystem: 3 Pakete") {
		t.Fatalf("Ende: %+v", v)
	}
}

func TestSingleJobWithOSResumeWaitsForUnit(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	var active atomic.Bool
	active.Store(true)
	b := pdhResumeAgent(e, osSteps{Step: "install", Pending: 3, Unit: "pdh-os-install-1"}) // StepAt null
	b.osUnitActive = func(context.Context, string) bool { return active.Load() }
	b.loadSingleJob()
	for i := 0; i < 5; i++ {
		if b.jobStep(context.Background()) {
			t.Fatal("beendet")
		}
		e.clk.add(time.Minute)
	}
	b.sj.mu.Lock()
	st := b.sj.job.OS
	b.sj.mu.Unlock()
	if st.Step != "install" || st.Failed || st.StepAt.IsZero() || strings.Contains(e.rec.all(), "os install") {
		t.Fatalf("st=%+v / %s", st, e.rec.all())
	}
}

func TestSingleJobWithOSShutdownDuringInstall(t *testing.T) {
	e := pdhOSEnv(t, 3, false)
	ctx, cancel := context.WithCancel(context.Background())
	e.fake.onInstall = cancel
	e.fake.installErr = context.Canceled
	b := pdhResumeAgent(e, osSteps{Step: "install", Pending: 3, StepAt: e.clk.now()})
	b.loadSingleJob()
	if b.jobStep(ctx) {
		t.Fatal("beendet")
	}
	b.sj.mu.Lock()
	st := b.sj.job.OS
	b.sj.mu.Unlock()
	if st.Step != "install" || st.Failed {
		t.Fatalf("Beenden des Agenten ist kein Fehler: %+v", st)
	}
}

func windowOSAgent(t *testing.T, pkgs, sec int) *osStepsEnv {
	t.Helper()
	e := pdhOSEnv(t, pkgs, false)
	e.fake.statusOut = osStatusSec(pkgs, sec, false)
	return e
}

func sameAsHEAD(e *osStepsEnv) { e.a.gitRun = jobFakeGit(false, jobOldSHA) } // Remote gleich HEAD

func TestWindowSingleOSNoPDHUpdate(t *testing.T) {
	e := windowOSAgent(t, 2, 2)
	sameAsHEAD(e)
	w := ha.DefaultWindow()
	w.Install = true
	res, started := e.a.windowSingle(context.Background(), w)
	if !started || !strings.Contains(res, "Betriebssystem-Update gestartet (2 Sicherheitsupdates)") {
		t.Fatalf("Ergebnis: %q %v", res, started)
	}
	if v := e.a.jobView(); v.Kind != "os" || !v.Active {
		t.Fatalf("Auftrag: %+v", v)
	}
}

func TestWindowSingleOSVariants(t *testing.T) {
	type tc struct {
		name      string
		pkgs, sec int
		check     bool // Haken
		install   bool
		sameHead  bool
		faulty    bool
		wantRes   string // Teil des Ergebnisses
		wantOS    bool   // OS-Auftrag
		wantPDH   bool   // PDH-Auftrag
		wantWithO bool
	}
	cases := []tc{
		{name: "Haken aus", pkgs: 2, sec: 2, install: true, sameHead: true, wantRes: "Keine neue Version im Update-Zweig"},
		{name: "keine Sicherheit", pkgs: 5, sec: 0, check: true, install: true, sameHead: true, wantRes: "Keine Sicherheitsupdates offen"},
		{name: "Einspielen aus", pkgs: 2, sec: 1, check: true, wantRes: "Betriebssystem-Update gestartet (1 Sicherheitsupdates)", wantOS: true},
		{name: "Einspielen aus ohne Haken", pkgs: 2, sec: 1, wantRes: "Nichts zu tun: „Neue Version einspielen“ ist ausgeschaltet"},
		{name: "neue Version", pkgs: 2, sec: 2, check: true, install: true, wantRes: "Update gestartet", wantPDH: true, wantWithO: true},
		{name: "Ziel fehlerhaft", pkgs: 2, sec: 2, check: true, install: true, faulty: true,
			wantRes: "Betriebssystem-Update gestartet (2 Sicherheitsupdates)", wantOS: true},
		{name: "Ziel fehlerhaft ohne Haken", pkgs: 2, sec: 2, install: true, faulty: true, wantRes: "als fehlerhaft gemerkt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := windowOSAgent(t, c.pkgs, c.sec)
			e.a.osUpdateState(func(st *osState) { st.WithPDHUpdate = c.check })
			if c.sameHead {
				sameAsHEAD(e)
			}
			if c.faulty {
				e.a.windowUpdate(func(s *windowState) { s.Faulty = []string{jobNewSHA} })
			}
			w := ha.DefaultWindow()
			w.Install = c.install
			res, started := e.a.windowSingle(context.Background(), w)
			if !strings.Contains(res, c.wantRes) || started != (c.wantOS || c.wantPDH) {
				t.Fatalf("Ergebnis: %q %v", res, started)
			}
			if got := e.a.osJobOpen(); got != c.wantOS {
				t.Fatalf("OS-Auftrag %v, erwartet %v", got, c.wantOS)
			}
			e.a.sj.mu.Lock()
			pdh, withOS := !jobEnded(e.a.sj.job.Job), e.a.sj.job.WithOS
			e.a.sj.mu.Unlock()
			if pdh != c.wantPDH || withOS != c.wantWithO {
				t.Fatalf("PDH-Auftrag %v (WithOS %v), erwartet %v/%v", pdh, withOS, c.wantPDH, c.wantWithO)
			}
		})
	}
}

func TestWindowSingleOSWhileJobActive(t *testing.T) {
	e := windowOSAgent(t, 2, 2)
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	res, started := e.a.windowOS(context.Background())
	if started || !strings.Contains(res, lockBusyMsg(lockPDH)) {
		t.Fatalf("Ergebnis: %q %v", res, started)
	}
	if e.a.osJobOpen() || strings.Contains(e.rec.all(), "os check") {
		t.Fatalf("zweiter Auftrag bzw. Prüfen neben dem PDH-Auftrag: %s", e.rec.all())
	}
}
