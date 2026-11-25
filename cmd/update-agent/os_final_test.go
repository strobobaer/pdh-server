package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Schlusskorrektur Teil A (Betriebssystem-Updates): F1 – apt hängt nicht am Agenten.

// TestOSHelperExit1: Hilfsprozess, der mit Fehlercode endet.
func TestOSHelperExit1(t *testing.T) {
	if os.Getenv("PDH_OS_HELPER_EXIT") != "1" {
		t.Skip("Hilfsprozess")
	}
	os.Exit(1)
}

// TestOSHelperHolder: startet ein Kind, das die Ausgabe-Kanäle erbt und lange schläft (wie die Einheit
// hinter „systemd-run --pipe“), und schläft selbst.
func TestOSHelperHolder(t *testing.T) {
	if os.Getenv("PDH_OS_HELPER_HOLD") != "1" {
		t.Skip("Hilfsprozess")
	}
	c := exec.Command(os.Args[0], "-test.run=TestOSHelperSleep")
	c.Env = append(os.Environ(), "PDH_OS_HELPER_SLEEP=1", "PDH_OS_HELPER_HOLD=0")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		os.Exit(3)
	}
	time.Sleep(30 * time.Second)
}

func osHolderCmd(string, ...string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=TestOSHelperHolder")
	c.Env = append(os.Environ(), "PDH_OS_HELPER_HOLD=1")
	return c
}

// osOutFileArg: Pfad aus „-p StandardOutput=append:<Pfad>“.
func osOutFileArg(t *testing.T, args []string) string {
	t.Helper()
	for i, x := range args {
		if x == "-p" && i+1 < len(args) && strings.HasPrefix(args[i+1], "StandardOutput=append:") {
			return strings.TrimPrefix(args[i+1], "StandardOutput=append:")
		}
	}
	t.Fatalf("kein -p StandardOutput=append:… in %v", args)
	return ""
}

func TestOSExecUnitBuildsNoPipeCall(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	var args []string
	var cmd *exec.Cmd
	a.osCmd = func(_ string, ar ...string) *exec.Cmd {
		args = ar
		cmd = exec.Command(os.Args[0], "-test.run=NONE")
		return cmd
	}
	_, _ = a.osExec(context.Background(), "check")
	for _, x := range args {
		if x == "--pipe" || x == "-P" {
			t.Fatalf("--pipe koppelt apt an den Agenten: %v", args)
		}
	}
	out := osOutFileArg(t, args)
	if filepath.Dir(out) != a.osOutDir || !strings.HasPrefix(filepath.Base(out), "pdh-os-check-") || !strings.HasSuffix(out, ".out") {
		t.Fatalf("Ausgabedatei %s (Ordner %s)", out, a.osOutDir)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-p StandardError=inherit") || !strings.Contains(joined, "--wait") {
		t.Fatalf("Argumente: %v", args)
	}
	if args[len(args)-2] != osScriptPath || args[len(args)-1] != "check" {
		t.Fatalf("Skript am Ende: %v", args)
	}
	if cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("Ausgabe darf nicht über eine Pipe des Agenten laufen")
	}
	if cmd.WaitDelay <= 0 || cmd.WaitDelay > 30*time.Second {
		t.Fatalf("WaitDelay %v", cmd.WaitDelay)
	}
	if fi, err := os.Stat(a.osOutDir); err != nil || fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("Ordner: %v %v", fi, err)
	}
}

func TestOSExecReadsStatusFromOutputFile(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	var path string
	a.osCmd = func(_ string, ar ...string) *exec.Cmd {
		path = osOutFileArg(t, ar)
		if err := os.WriteFile(path, []byte(osStatusJSON+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return exec.Command(os.Args[0], "-test.run=NONE")
	}
	st, err := a.osStatus(context.Background())
	if err != nil || len(st.Upgradable) != 2 || st.SecurityCount != 1 || !st.RebootRequired {
		t.Fatalf("Status aus der Datei: %+v %v", st, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Ausgabedatei nach dem Lesen nicht aufgeräumt: %v", err)
	}
}

func TestOSExecErrorKeepsLastLinesFromFile(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	a.osCmd = func(_ string, ar ...string) *exec.Cmd {
		p := osOutFileArg(t, ar)
		if err := os.WriteFile(p, []byte("eins\nzwei\ndrei\nvier\nE: Paket kaputt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c := exec.Command(os.Args[0], "-test.run=TestOSHelperExit1")
		c.Env = append(os.Environ(), "PDH_OS_HELPER_EXIT=1")
		return c
	}
	out, err := a.osExec(context.Background(), "install")
	if err == nil {
		t.Fatal("Fehlercode muss ankommen")
	}
	if got := osLastLines(string(out), 3); got != "drei / vier / E: Paket kaputt" {
		t.Fatalf("letzte Zeilen: %q", got)
	}
}

// Ein Kind hält die Ausgabe offen (wie die Einheit hinter --pipe): der Schritt darf nach der Frist nicht hängen.
func TestOSExecTimeoutNotBlockedByStubbornClient(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	a.osCmd = osHolderCmd
	a.osUnitActive = func(context.Context, string) bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() { _, err := a.osExec(ctx, "install"); done <- err }()
	select {
	case err := <-done:
		var ur *osUnitRunning
		if !errors.As(err, &ur) {
			t.Fatalf("Fehler: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("osExec hängt nach der Frist (%v)", time.Since(start))
	}
}

func TestOSExecCleansOldOutputFiles(t *testing.T) {
	a, _ := osTestAgent(t)
	a.osRun = nil
	a.osCmd = func(string, ...string) *exec.Cmd { return exec.Command(os.Args[0], "-test.run=NONE") }
	old := filepath.Join(a.osOutDir, "pdh-os-install-1-1.out")
	young := filepath.Join(a.osOutDir, "pdh-os-install-2-2.out")
	other := filepath.Join(a.osOutDir, "notiz.txt")
	for _, p := range []string{old, young, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_, _ = a.osExec(context.Background(), "check")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("alte Ausgabedatei nicht aufgeräumt")
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatal("junge Datei (evtl. laufende Einheit) darf nicht entfernt werden")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("fremde Datei darf nicht entfernt werden")
	}
}

func TestOSWaitUnitRemovesOutputFile(t *testing.T) {
	a, _ := osTestAgent(t)
	a.sleep = func(time.Duration) { time.Sleep(2 * time.Millisecond) }
	p := a.osOutFile("pdh-os-install-9-9")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var active atomic.Bool
	active.Store(true)
	a.osUnitActive = func(context.Context, string) bool { return active.Load() }
	rel, _ := a.lockAcquire(lockOS)
	a.osWaitUnit("pdh-os-install-9-9", rel)
	time.Sleep(30 * time.Millisecond)
	if _, err := os.Stat(p); err != nil {
		t.Fatal("Datei muss bleiben, solange die Einheit läuft")
	}
	active.Store(false)
	osEventually(t, "Datei weg", func() bool { _, err := os.Stat(p); return os.IsNotExist(err) })
}

// ── F2: Speicherfehler vor install und vor dem Neustart ──────────────────────

func TestOSStepsPersistFailsBeforeInstall(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	e.persErr = func(n int) error { return errors.New("Platte voll") }
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if strings.Contains(all, "os install") || strings.Contains(all, "reboot") {
		t.Fatalf("kein install ohne gesicherten Stand: %s", all)
	}
	if !st.Failed || !strings.Contains(st.Msg, "Zustand konnte nicht gespeichert werden") || st.Step != "done" {
		t.Fatalf("st=%+v", st)
	}
	assertOrder(t, all, "stop-pdh", "persist", "start-pdh") // pdh läuft danach wieder
}

func TestOSStepsPersistFailsBeforeReboot(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	e.persErr = func(n int) error {
		if n >= 2 { // 1 = vor install, 2 = vor dem Neustart
			return errors.New("Platte voll")
		}
		return nil
	}
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if strings.Count(all, "os install") != 1 || strings.Contains(all, "reboot") {
		t.Fatalf("kein Neustart ohne gesicherten Stand: %s", all)
	}
	if !st.Failed || !strings.Contains(st.Msg, "kein Neustart") || !strings.Contains(st.Msg, "3 Pakete eingespielt") || st.Step != "done" {
		t.Fatalf("st=%+v", st)
	}
	assertOrder(t, all, "os install", "persist", "start-pdh")
}

func osFailWrite(match func(v any) bool) func(string, any) error {
	return func(path string, v any) error {
		if match(v) {
			return errors.New("Platte voll")
		}
		return writeStateJSON(path, v)
	}
}

// Betriebssystem-Auftrag: scheitert das Speichern des Schritts „boot“, gibt es keinen Neustart.
func TestOSJobNoRebootWhenBootStateNotSaved(t *testing.T) {
	e := osJobEnv(t, 3, true)
	a := e.a
	a.stateWrite = osFailWrite(func(v any) bool { j, ok := v.(osJob); return ok && j.OS.Step == "boot" })
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runOSJob(t, a, e.clk, 60)
	all := e.rec.all()
	if strings.Contains(all, "reboot") {
		t.Fatalf("Neustart trotz nicht gespeichertem Stand: %s", all)
	}
	v := a.jobView()
	if v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "kein Neustart") {
		t.Fatalf("Ende: %+v", v)
	}
}

// Betriebssystem-Auftrag: scheitert das Speichern des Schritts „install“, wird nichts eingespielt.
func TestOSJobNoInstallWhenInstallStateNotSaved(t *testing.T) {
	e := osJobEnv(t, 3, false)
	a := e.a
	a.stateWrite = osFailWrite(func(v any) bool { j, ok := v.(osJob); return ok && j.OS.Step == "install" })
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runOSJob(t, a, e.clk, 60)
	all := e.rec.all()
	if strings.Contains(all, "os install") {
		t.Fatalf("install trotz nicht gespeichertem Stand: %s", all)
	}
	assertOrder(t, all, "stop-pdh", "start-pdh") // pdh wieder an
	v := a.jobView()
	if v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "nicht gespeichert") {
		t.Fatalf("Ende: %+v", v)
	}
}

// PDH-Auftrag mit Haken: derselbe Schutz vor dem Neustart.
func TestSingleJobWithOSNoRebootWhenBootStateNotSaved(t *testing.T) {
	e := pdhOSEnv(t, 3, true)
	a := e.a
	a.stateWrite = osFailWrite(func(v any) bool { j, ok := v.(singleJob); return ok && j.OS.Step == "boot" })
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 80)
	if all := e.rec.all(); strings.Contains(all, "reboot") {
		t.Fatalf("Neustart trotz nicht gespeichertem Stand: %s", all)
	}
	if v := a.jobView(); v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "kein Neustart") {
		t.Fatalf("Ende: %+v", v)
	}
}

// ── F3: neue Aufträge nehmen die Sperre exklusiv ─────────────────────────────

// pdhEndedWithUnit: PDH-Auftrag mit Haken ist beendet, die Einheit der Pakete läuft noch (Sperre bleibt).
func pdhEndedWithUnit(t *testing.T) (*osStepsEnv, *atomic.Bool) {
	t.Helper()
	e := pdhOSEnv(t, 3, false)
	a := e.a
	active := &atomic.Bool{}
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
	if a.lockHolder() != lockPDH {
		t.Fatalf("Vorbedingung: Sperre muss bleiben, Besitzer %q", a.lockHolder())
	}
	return e, active
}

func TestNewPDHJobRefusedWhileUnitOfEndedJobRuns(t *testing.T) {
	e, active := pdhEndedWithUnit(t)
	a := e.a
	for _, src := range []string{srcButton, srcWindow} {
		if _, err := a.startSingleJob(context.Background(), src, true); err == nil {
			t.Fatalf("neuer PDH-Auftrag (%s) neben der laufenden Paket-Einheit", src)
		}
	}
	if a.singleJobActive() {
		t.Fatal("kein Auftrag darf angelegt sein")
	}
	rec := httptest.NewRecorder()
	a.updateHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update", nil))
	if rec.Code != http.StatusLocked {
		t.Fatalf("POST /v1/update: %d %s", rec.Code, rec.Body.String())
	}
	a.mu.RLock()
	running := a.status.Running
	a.mu.RUnlock()
	if running {
		t.Fatal("Update darf nicht laufen")
	}
	// nach dem Ende der Einheit geht es wieder
	active.Store(false)
	osEventually(t, "Sperre frei", func() bool { return a.lockHolder() == "" })
	if _, err := a.startSingleJob(context.Background(), srcButton, true); err != nil {
		t.Fatalf("nach der Einheit: %v", err)
	}
}

// Der geschachtelte Aufruf innerhalb desselben Auftrags (jobScript) darf die Sperre weiter mitnutzen.
func TestJobScriptStillNestedInsideJob(t *testing.T) {
	a, _ := osTestAgent(t)
	a.updateRun = func([]string) {
		a.mu.Lock()
		a.status.Running, a.status.Success = false, true
		a.mu.Unlock()
	}
	rel, busy := a.lockTryExclusive(lockPDH) // wie startSingleJob
	if busy != "" {
		t.Fatal(busy)
	}
	defer rel()
	if err := a.jobScript(context.Background(), "--finish"); err != nil {
		t.Fatalf("jobScript im Auftrag: %v", err)
	}
}

// ── F4: kombinierter Auftrag prüft Netz und Platz, bevor er pdh stoppt ───────

func TestSingleJobWithOSSkippedBeforeStop(t *testing.T) {
	cases := []struct {
		name, want string
		setup      func(e *osStepsEnv)
	}{
		{"Netz-Änderung wartet", "Eine Netz-Änderung wartet auf Bestätigung", func(e *osStepsEnv) {
			netWriteState(t, e.a, "state=pending\nid=x\n")
		}},
		{"Wartung noch an", "Eine Netz-Änderung wartet auf Bestätigung", func(e *osStepsEnv) {
			netWriteState(t, e.a, "state=confirmed\nid=x\nmaint=1\nmaint_off=pending\n")
		}},
		{"Platz apt", "Zu wenig freier Platz in /var/cache/apt", func(e *osStepsEnv) {
			e.a.freeSpace = func(p string) (int64, bool) {
				if p == "/var/cache/apt" {
					return 100 << 20, true
				}
				return 10 << 30, true
			}
		}},
		{"Platz boot", "Zu wenig freier Platz in /boot", func(e *osStepsEnv) {
			e.a.freeSpace = func(p string) (int64, bool) {
				if p == "/boot" {
					return 50 << 20, true
				}
				return 10 << 30, true
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := pdhOSEnv(t, 3, true)
			if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
				t.Fatal(err)
			}
			c.setup(e) // erst nach dem Start: die Lage kann sich bis zum Einspielen ändern
			runPDHJob(t, e, 80)
			rest := afterFinish(e.rec.all())
			if strings.Contains(rest, "stop-pdh") || strings.Contains(rest, "os install") || strings.Contains(rest, "reboot") {
				t.Fatalf("pdh gestoppt bzw. eingespielt trotz Hindernis: %s", e.rec.all())
			}
			v := e.a.jobView()
			if v.Phase != string(ha.UpDone) || !strings.Contains(v.Result, "; Betriebssystem nicht aktualisiert: "+c.want) {
				t.Fatalf("Ende: %+v", v)
			}
			lr := e.a.osState().LastRun
			if lr == nil || lr.OK || !strings.Contains(lr.Message, c.want) {
				t.Fatalf("last_run: %+v", lr)
			}
		})
	}
}

// ── F11: Neustart auch ohne offene Pakete ────────────────────────────────────

// osRunWithReboot: Schritte, bis fertig; sobald „reboot“ gelaufen ist, meldet der Rechner eine neue boot_id.
func osRunWithReboot(t *testing.T, e *osStepsEnv, st *osSteps) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if e.a.osAdvance(context.Background(), st, e.persist) {
			return
		}
		if strings.Contains(e.rec.all(), "reboot") {
			e.boot.Store("boot-2")
		}
		e.clk.add(time.Minute)
	}
	t.Fatalf("nicht beendet: %+v / %s", *st, e.rec.all())
}

func TestOSStepsRebootOnlyWithoutPackages(t *testing.T) {
	e := osStepsAgent(t, 0, false)
	e.fake.statusOut = osStepsStatus(0, true) // kein Paket offen, aber „Neustart nötig“
	var st osSteps
	osRunWithReboot(t, e, &st)
	all := e.rec.all()
	if strings.Contains(all, "os install") || strings.Count(all, "reboot") != 1 {
		t.Fatalf("Ablauf: %s", all)
	}
	assertOrder(t, all, "os check", "os status", "stop-pdh", "persist", "reboot", "start-pdh")
	if st.Step != "done" || st.Failed || !st.Rebooted || st.Pending != 0 || !strings.Contains(st.Msg, "Neustart ja") {
		t.Fatalf("st=%+v", st)
	}
}

func TestOSStepsNoPackagesNoRebootStillNothingToDo(t *testing.T) {
	e := osStepsAgent(t, 0, false)
	var st osSteps
	e.run(t, &st, nil)
	if st.Failed || !strings.Contains(st.Msg, "Keine Pakete") || strings.Contains(e.rec.all(), "stop-pdh") {
		t.Fatalf("st=%+v %s", st, e.rec.all())
	}
}

func TestOSJobRebootOnlyWithoutPackages(t *testing.T) {
	e := osJobEnv(t, 0, false)
	e.fake.statusOut = osStepsStatus(0, true)
	a := e.a
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatalf("Neustart ohne Pakete muss gehen: %v", err)
	}
	for i := 0; i < 60; i++ {
		if a.osJobStep(context.Background()) {
			break
		}
		if strings.Contains(e.rec.all(), "reboot") {
			e.boot.Store("boot-2")
		}
		e.clk.add(time.Minute)
	}
	all := e.rec.all()
	if strings.Contains(all, "os install") || strings.Count(all, "reboot") != 1 {
		t.Fatalf("Ablauf: %s", all)
	}
	assertOrder(t, all, "psql-lock", "stop-pdh", "pg_dump", "reboot", "start-pdh", "psql-unlock")
	if v := a.jobView(); v.Phase != string(ha.UpDone) {
		t.Fatalf("Ende: %+v", v)
	}
	if lr := a.osState().LastRun; lr == nil || !lr.Rebooted || !lr.OK || lr.Packages != 0 {
		t.Fatalf("last_run: %+v", lr)
	}
}

func TestOSJobNothingToDoStillRefused(t *testing.T) {
	e := osJobEnv(t, 0, false)
	if _, err := e.a.startOSJob(context.Background(), srcButton, false); err == nil || !strings.Contains(err.Error(), "Keine Pakete") {
		t.Fatalf("Fehler: %v", err)
	}
}

func TestSingleJobWithOSRebootOnly(t *testing.T) {
	e := pdhOSEnv(t, 0, false)
	e.fake.statusOut = osStepsStatus(0, true)
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		if e.a.jobStep(context.Background()) {
			break
		}
		if strings.Contains(afterFinish(e.rec.all()), "reboot") {
			e.boot.Store("boot-2")
		}
		e.clk.add(time.Minute)
	}
	// PDH-Auftrag mit Haken: ohne neu eingespielte Pakete nie ein Neustart (nur der Knopf „Betriebssystem“)
	rest := afterFinish(e.rec.all())
	if strings.Contains(rest, "os install") || strings.Contains(rest, "reboot") || strings.Contains(rest, "stop-pdh") {
		t.Fatalf("Ablauf: %s", e.rec.all())
	}
	if v := e.a.jobView(); v.Phase != string(ha.UpDone) {
		t.Fatalf("Ende: %+v", v)
	}
}

// Fenster (Zeitplan): der Betriebssystem-Auftrag startet ohne Pakete nicht, auch nicht wegen „Neustart nötig“.
func TestOSJobWindowRebootOnlyRefused(t *testing.T) {
	e := osJobEnv(t, 0, false)
	e.fake.statusOut = osStepsStatus(0, true)
	if _, err := e.a.startOSJob(context.Background(), srcWindow, false); err == nil || !strings.Contains(err.Error(), "Keine Pakete") {
		t.Fatalf("Fehler: %v", err)
	}
	if strings.Contains(e.rec.all(), "reboot") {
		t.Fatalf("Ablauf: %s", e.rec.all())
	}
}

// Schrittmaschine mit Sperre für den reinen Neustart: 0 Pakete + „Neustart nötig“ ⇒ nichts tun.
func TestOSStepsNoBareReboot(t *testing.T) {
	e := osStepsAgent(t, 0, false)
	e.fake.statusOut = osStepsStatus(0, true)
	st := osSteps{NoBareReboot: true}
	e.run(t, &st, nil)
	all := e.rec.all()
	if st.Failed || st.Step != "done" || strings.Contains(all, "reboot") || strings.Contains(all, "stop-pdh") {
		t.Fatalf("st=%+v ablauf=%s", st, all)
	}
}

// ── F12: kombinierter Auftrag – eindeutiges Ergebnis ─────────────────────────

func TestSingleJobWithOSResultTexts(t *testing.T) {
	// Erfolg
	e := pdhOSEnv(t, 3, false)
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 60)
	if v := e.a.jobView(); v.Result != "Update auf 0.77.9 abgeschlossen; Betriebssystem: 3 Pakete, Neustart nein" {
		t.Fatalf("Erfolg: %q", v.Result)
	}

	// Einspielen scheitert: abgeschlossen, aber deutliche Warnung (kein „nicht aktualisiert“ nach „eingespielt“)
	e = pdhOSEnv(t, 3, false)
	e.fake.installErr = errors.New("exit status 100")
	e.fake.installOut = "E: Paket kaputt\n"
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runPDHJob(t, e, 60)
	v := e.a.jobView()
	want := "Update auf 0.77.9 abgeschlossen. Achtung: Betriebssystem-Teil fehlgeschlagen – Einspielen fehlgeschlagen: E: Paket kaputt"
	if v.Phase != string(ha.UpDone) || v.Result != want || len(e.a.windowSnapshot().Faulty) != 0 {
		t.Fatalf("Einspielen: %+v", v)
	}
	if strings.Contains(e.rec.all(), "script --rollback") {
		t.Fatal("kein Zurücksetzen")
	}

	// Server antwortet nach dem Neustart nicht
	e = pdhOSEnv(t, 3, true)
	if _, err := e.a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if e.a.jobStep(context.Background()) {
			break
		}
		if strings.Contains(afterFinish(e.rec.all()), "reboot") {
			e.boot.Store("boot-2")
			e.health.setCode(503)
		}
		e.clk.add(time.Minute)
	}
	v = e.a.jobView()
	if v.Phase != string(ha.UpDone) || !strings.HasPrefix(v.Result, "Update auf 0.77.9 abgeschlossen. Achtung: Betriebssystem-Teil fehlgeschlagen – Server antwortet nach dem Neustart nicht") ||
		strings.Contains(v.Result, "nicht aktualisiert") {
		t.Fatalf("Server tot: %+v", v)
	}
	if lr := e.a.osState().LastRun; lr == nil || lr.OK {
		t.Fatalf("last_run: %+v", lr)
	}
}

// ── F6: Wartungsfenster wartet auf kurze Sperren ─────────────────────────────

// windowSleeps: zählt die Pausen des Fensters; nach n Pausen läuft hook einmal (z. B. Sperre frei).
func windowSleeps(e *osStepsEnv, n int, hook func()) *int {
	count := 0
	e.a.sleep = func(d time.Duration) {
		count++
		if count == n && hook != nil {
			hook()
		}
	}
	return &count
}

func TestWindowWaitsForShortLockPDH(t *testing.T) {
	e := windowOSAgent(t, 2, 2)
	rel, busy := e.a.lockTryExclusive(lockESPHome)
	if busy != "" {
		t.Fatal(busy)
	}
	n := windowSleeps(e, 3, rel)
	w := ha.DefaultWindow()
	w.Install = true
	res, started := e.a.windowSingle(context.Background(), w)
	if !started || res != "Update gestartet" || *n != 3 {
		t.Fatalf("Ergebnis: %q %v nach %d Pausen", res, started, *n)
	}
}

func TestWindowWaitsForShortLockOS(t *testing.T) {
	e := windowOSAgent(t, 2, 2)
	e.a.osUpdateState(func(st *osState) { st.WithPDHUpdate = true })
	sameAsHEAD(e)
	rel, _ := e.a.lockTryExclusive(lockNet)
	n := windowSleeps(e, 2, rel)
	res, started := e.a.windowOS(context.Background())
	if !started || !strings.Contains(res, "Betriebssystem-Update gestartet") || *n != 2 {
		t.Fatalf("Ergebnis: %q %v nach %d Pausen", res, started, *n)
	}
	// auch eine laufende Betriebssystem-Prüfung zählt als kurz
	e = windowOSAgent(t, 2, 2)
	rel, _ = e.a.lockTryExclusive(lockOSCheck)
	n = windowSleeps(e, 1, rel)
	if res, started = e.a.windowOS(context.Background()); !started || *n != 1 {
		t.Fatalf("Prüfung: %q %v %d", res, started, *n)
	}
}

func TestWindowGivesUpAfter15MinutesNamingHolder(t *testing.T) {
	e := windowOSAgent(t, 2, 2)
	rel, _ := e.a.lockTryExclusive(lockNet)
	defer rel()
	n := windowSleeps(e, -1, nil)
	w := ha.DefaultWindow()
	w.Install = true
	res, started := e.a.windowSingle(context.Background(), w)
	if started || !strings.Contains(res, lockNet) || !strings.Contains(res, "15 Minuten") || *n != 30 {
		t.Fatalf("Ergebnis: %q %v nach %d Pausen", res, started, *n)
	}
}

func TestWindowDoesNotWaitForRunningJob(t *testing.T) {
	e := windowOSAgent(t, 2, 2)
	if _, err := e.a.startOSJob(context.Background(), srcButton, false); err != nil { // läuft: lockOS
		t.Fatal(err)
	}
	n := windowSleeps(e, -1, nil)
	w := ha.DefaultWindow()
	w.Install = true
	res, started := e.a.windowSingle(context.Background(), w)
	if started || *n != 0 || !strings.Contains(res, "Betriebssystem-Update läuft bereits") {
		t.Fatalf("Ergebnis: %q %v nach %d Pausen", res, started, *n)
	}
	// ebenso ein laufendes PDH-Update
	e2 := windowOSAgent(t, 2, 2)
	rel, _ := e2.a.lockTryExclusive(lockPDH)
	defer rel()
	n = windowSleeps(e2, -1, nil)
	if res, started = e2.a.windowSingle(context.Background(), w); started || *n != 0 || !strings.Contains(res, lockBusyMsg(lockPDH)) {
		t.Fatalf("PDH: %q %v %d", res, started, *n)
	}
}

// ── T5: Sperrmeldung bei laufender Prüfung ───────────────────────────────────

func TestLockMessageForRunningCheck(t *testing.T) {
	if got := lockBusyMsg(lockOSCheck); got != "Eine Betriebssystem-Prüfung läuft gerade – bitte kurz warten" {
		t.Fatalf("Meldung: %q", got)
	}
	a, _ := osTestAgent(t)
	gate := make(chan struct{})
	started := make(chan struct{})
	a.osRun = func(_ context.Context, action string) ([]byte, error) {
		if action == "check" {
			close(started)
			<-gate
			return []byte("ok\n"), nil
		}
		return []byte(osStatusJSON), nil
	}
	done := make(chan struct{})
	go func() { osServe(t, a, http.MethodPost, "/v1/os/check", "", true); close(done) }()
	<-started
	if h := a.lockHolder(); h != lockOSCheck {
		t.Fatalf("Besitzer %q", h)
	}
	// ein PDH-Update (Knopf oder Fenster) sieht die Prüfung, nicht „Betriebssystem-Update“
	if _, err := a.startSingleJob(context.Background(), srcWindow, false); err == nil || err.Error() != lockBusyMsg(lockOSCheck) {
		t.Fatalf("Meldung beim Start: %v", err)
	}
	close(gate)
	<-done
}

// ── F7: Verbund-Update (POST /v1/update) mit fremder Sperre ──────────────────

func postUpdateCommit(a *updateAgent) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.updateHandler(rec, httptest.NewRequest(http.MethodPost, "/v1/update", strings.NewReader(`{"commit":"`+jobNewSHA+`","op":"install"}`)))
	return rec
}

func TestUpdateHandlerWaitsForShortLock(t *testing.T) {
	a, _ := osTestAgent(t)
	a.updateRun = func([]string) {
		a.mu.Lock()
		a.status.Running, a.status.Success = false, true
		a.mu.Unlock()
	}
	rel, _ := a.lockTryExclusive(lockESPHome)
	n := 0
	a.sleep = func(time.Duration) {
		n++
		if n == 3 {
			rel()
		}
	}
	rec := postUpdateCommit(a)
	if rec.Code != http.StatusAccepted || n != 3 {
		t.Fatalf("%d %s nach %d Pausen", rec.Code, rec.Body.String(), n)
	}
}

func TestUpdateHandlerForeignLockClearMessage(t *testing.T) {
	a, _ := osTestAgent(t)
	rel, _ := a.lockTryExclusive(lockNet)
	defer rel()
	n := 0
	a.sleep = func(time.Duration) { n++ }
	rec := postUpdateCommit(a)
	if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "Eine Netzwerk-Änderung läuft") || n == 0 {
		t.Fatalf("%d %s nach %d Pausen", rec.Code, rec.Body.String(), n)
	}
	// ein laufendes Betriebssystem-Update wird nicht abgewartet
	rel()
	rel2, busy := a.lockTryExclusive(lockOS)
	if busy != "" {
		t.Fatal(busy)
	}
	defer rel2()
	n = 0
	rec = postUpdateCommit(a)
	if rec.Code != http.StatusLocked || n != 0 || !strings.Contains(rec.Body.String(), "Betriebssystem-Update") {
		t.Fatalf("OS: %d %s nach %d Pausen", rec.Code, rec.Body.String(), n)
	}
}

// Der laufende Lauf selbst bleibt 409 mit Stand und Art (pdh-ha wartet auf sein Ergebnis).
func TestUpdateHandlerRunningStaysConflictWithCommit(t *testing.T) {
	a, _ := osTestAgent(t)
	if _, ok := a.startUpdateNew(jobNewSHA, "install"); !ok {
		t.Fatal("Start")
	}
	rec := postUpdateCommit(a)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), jobNewSHA) || !strings.Contains(rec.Body.String(), `"op":"install"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
