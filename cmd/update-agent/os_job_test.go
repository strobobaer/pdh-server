package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

// Auftrag „Betriebssystem“ für den Einzelserver (os_job.go).

// osJobEnv: Einzelserver mit 4 Paketen, Sicherung eingerichtet und frisch, Netz ohne offene Änderung.
func osJobEnv(t *testing.T, pkgs int, rebootAfter bool) *osStepsEnv {
	t.Helper()
	e := osStepsAgent(t, pkgs, rebootAfter)
	e.a.token = strings.Repeat("x", 32)
	e.a.netStateDir = t.TempDir()
	e.a.dr.Config.Enabled = true
	e.a.dr.LastBase = e.clk.now()
	return e
}

// runOSJob: Schritte, bis der Auftrag endet (Uhr je Schritt +1 min).
func runOSJob(t *testing.T, a *updateAgent, clk *jobClock, max int) {
	t.Helper()
	for i := 0; i < max; i++ {
		if a.osJobStep(context.Background()) {
			return
		}
		clk.add(time.Minute)
	}
	t.Fatalf("Auftrag nach %d Schritten nicht beendet: %+v", max, a.jobView())
}

func osJobRestarted(e *osStepsEnv) *updateAgent {
	b := &updateAgent{mode: "systemd", repoDir: e.a.repoDir, token: e.a.token, netStateDir: e.a.netStateDir}
	b.dr.Config = e.a.dr.Config
	b.windowDefaults()
	b.wireJobFakes(e.rec, e.clk)
	b.osRun = e.fake.run
	b.bootID = e.a.bootID
	return b
}

func osWriteJob(t *testing.T, e *osStepsEnv, j osJob) {
	t.Helper()
	if err := writeStateJSON(osJobFile(), j); err != nil {
		t.Fatal(err)
	}
}

func osLockedJob(e *osStepsEnv, st osSteps) osJob {
	now := e.clk.now()
	return osJob{Job: ha.UpdateJob{ID: "os-x", Kind: ha.UpdateOS, Source: srcButton, Phase: ha.UpLocked, PhaseAt: now,
		StartedAt: now, LockedAt: now, OSStep: st.Step}, LockMaxMin: 60, CountdownMin: 2,
		Dump: filepath.Join(drStateDir(), "vor-update-os.dump"), OS: st}
}

func TestOSJobHappyPath(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	v := a.jobView()
	if !v.Active || v.Kind != "os" || v.Phase != string(ha.UpPrepare) || !a.singleJobActive() {
		t.Fatalf("Start: %+v", v)
	}
	runOSJob(t, a, e.clk, 40)
	assertOrder(t, e.rec.all(), "os check", "psql-lock", "stop-pdh", "pg_dump", "os install", "start-pdh", "psql-unlock")
	if got := e.rec.env["psql-lock"]["PDH_UPD_COMMIT"]; got != "os-update" {
		t.Fatalf("Sperr-Vermerk: %q", got)
	}
	v = a.jobView()
	want := []string{"Abmelden", "Pakete einspielen", "Neustart", "Prüfen", "Freigabe"}
	if v.Active || v.Phase != string(ha.UpDone) || v.Kind != "os" || strings.Join(v.Steps, "|") != strings.Join(want, "|") {
		t.Fatalf("Ende: %+v", v)
	}
	lr := a.osState().LastRun
	if lr == nil || lr.Packages != 4 || lr.Rebooted || !lr.OK {
		t.Fatalf("last_run: %+v", lr)
	}
	if rel, busy := a.lockAcquire(lockPDH); busy != "" {
		t.Fatalf("Sperre nicht frei: %s", busy)
	} else {
		rel()
	}
	if len(a.windowSnapshot().Faulty) != 0 {
		t.Fatal("Fenster darf nichts merken")
	}
}

func TestOSJobWithReboot(t *testing.T) {
	e := osJobEnv(t, 3, true)
	a := e.a
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		a.osJobStep(context.Background())
		e.clk.add(time.Minute)
		a.osj.mu.Lock()
		step := a.osj.job.OS.Step
		a.osj.mu.Unlock()
		if step == "boot" {
			break
		}
	}
	if v := a.jobView(); !v.Active || v.Step != 2 {
		t.Fatalf("vor dem Neustart: %+v", v)
	}
	e.boot.Store("boot-2")
	b := osJobRestarted(e)
	b.loadOSJob()
	runOSJob(t, b, e.clk, 20)
	if v := b.jobView(); v.Phase != string(ha.UpDone) {
		t.Fatalf("Ende: %+v", v)
	}
	if lr := b.osState().LastRun; lr == nil || !lr.Rebooted || !lr.OK || lr.Packages != 3 {
		t.Fatalf("last_run: %+v", lr)
	}
	all := e.rec.all()
	if strings.Count(all, "os install") != 1 || strings.Count(all, "reboot") != 1 {
		t.Fatalf("Ablauf: %s", all)
	}
}

func TestOSJobResumeMidInstall(t *testing.T) {
	e := osJobEnv(t, 3, false)
	osWriteJob(t, e, osLockedJob(e, osSteps{Step: "install", Pending: 3, StepAt: e.clk.now()}))
	e.a.loadOSJob()
	runOSJob(t, e.a, e.clk, 20)
	all := e.rec.all()
	if strings.Count(all, "os install") != 1 || strings.Count(all, "reboot") > 1 {
		t.Fatalf("Ablauf: %s", all)
	}
	if v := e.a.jobView(); v.Phase != string(ha.UpDone) {
		t.Fatalf("Ende: %+v", v)
	}
}

func TestLockRestoredAfterRestart(t *testing.T) {
	e := osJobEnv(t, 3, false)
	osWriteJob(t, e, osLockedJob(e, osSteps{Step: "install", Pending: 3, StepAt: e.clk.now()}))
	e.a.loadOSJob()
	if _, busy := e.a.lockAcquire(lockPDH); busy != lockOS {
		t.Fatalf("Sperre nach dem Neustart: %q", busy)
	}
}

func TestOSJobInstallFails(t *testing.T) {
	e := osJobEnv(t, 4, false)
	e.fake.installErr = errors.New("exit status 100")
	e.fake.installOut = "E: Paket kaputt\n"
	if _, err := e.a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runOSJob(t, e.a, e.clk, 40)
	v := e.a.jobView()
	if v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "E: Paket kaputt") {
		t.Fatalf("Ende: %+v", v)
	}
	all := e.rec.all()
	if !strings.Contains(all, "start-pdh") || !strings.Contains(all, "psql-unlock") || strings.Contains(all, "reboot") {
		t.Fatalf("Ablauf: %s", all)
	}
	if len(e.a.windowSnapshot().Faulty) != 0 {
		t.Fatal("Fenster darf nichts merken")
	}
	if lr := e.a.osState().LastRun; lr == nil || lr.OK {
		t.Fatalf("last_run: %+v", lr)
	}
	if _, busy := e.a.lockAcquire(lockPDH); busy != "" {
		t.Fatalf("Sperre nicht frei: %s", busy)
	}
}

func TestOSJobRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e *osStepsEnv)
		want  string
		force bool // auch mit force verweigert
	}{
		{"Docker", func(t *testing.T, e *osStepsEnv) { e.a.mode = "docker" }, dockerNotAvailMsg, true},
		{"Verbund", func(t *testing.T, e *osStepsEnv) {
			haInstalled(t)
			haFakeSocket(t, `{"ok":true,"configured":true,"mode":"duo","self":{"id":"a","role":"aktiv"}}`)
		}, "Teil B", true},
		{"keine Pakete", func(t *testing.T, e *osStepsEnv) { e.fake.statusOut = osStepsStatus(0, false) }, "Keine Pakete zu aktualisieren", true},
		{"Platz apt", func(t *testing.T, e *osStepsEnv) {
			e.a.freeSpace = func(p string) (int64, bool) {
				if p == "/var/cache/apt" {
					return 100 << 20, true
				}
				return 10 << 30, true
			}
		}, "/var/cache/apt (mindestens 1 GB)", true},
		{"Platz boot", func(t *testing.T, e *osStepsEnv) {
			e.a.freeSpace = func(p string) (int64, bool) {
				if p == "/boot" {
					return 100 << 20, true
				}
				return 10 << 30, true
			}
		}, "/boot (mindestens 200 MB)", true},
		{"Skript veraltet", func(t *testing.T, e *osStepsEnv) {
			e.fake.statusOut = "Aufruf: pdh-os-update check|install\n"
			e.fake.statusErr = errors.New("exit status 2")
		}, "veraltet", true},
		{"Netz wartet", func(t *testing.T, e *osStepsEnv) {
			netWriteState(t, e.a, "state=pending\nid=x\n")
		}, "Eine Netz-Änderung wartet auf Bestätigung", true},
		{"PDH-Update läuft", func(t *testing.T, e *osStepsEnv) {
			rel, _ := e.a.lockAcquire(lockPDH)
			t.Cleanup(rel)
		}, "PDH-Update", true},
		{"Sicherung fehlt", func(t *testing.T, e *osStepsEnv) { e.a.dr.LastBase = time.Time{} }, "Grundsicherung", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := osJobEnv(t, 4, false)
			c.setup(t, e)
			_, err := e.a.startOSJob(context.Background(), srcButton, false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Fehler: %v (erwartet %q)", err, c.want)
			}
			var nr jobNotReady
			if errors.As(err, &nr) != !c.force {
				t.Fatalf("jobNotReady=%v, erwartet %v: %v", errors.As(err, &nr), !c.force, err)
			}
			if c.force {
				if _, err := e.a.startOSJob(context.Background(), srcButton, true); err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("mit force: %v", err)
				}
			} else if _, err := e.a.startOSJob(context.Background(), srcButton, true); err != nil {
				t.Fatalf("mit force soll starten: %v", err)
			}
			if e.a.osJobOpen() == c.force {
				t.Fatalf("offener Auftrag: %v", e.a.osJobOpen())
			}
		})
	}
	// 409 über die Schnittstelle
	e := osJobEnv(t, 4, false)
	e.a.dr.LastBase = time.Time{}
	code, m := osJobServe(t, e.a, http.MethodPost, "/v1/os/job/start", "")
	if code != http.StatusConflict || m["ok"] != false {
		t.Fatalf("409 erwartet: %d %v", code, m)
	}
	code, m = osJobServe(t, e.a, http.MethodPost, "/v1/os/job/start", `{"force":true}`)
	if code != 200 || m["ok"] != true {
		t.Fatalf("mit force: %d %v", code, m)
	}
}

func TestOSJobRefusedWhileNetPending(t *testing.T) {
	e := osJobEnv(t, 4, false)
	netWriteState(t, e.a, "state=confirmed\nid=x\nmaint=1\nmaint_off=pending\n") // Wartung noch an
	_, err := e.a.startOSJob(context.Background(), srcButton, true)
	if err == nil || !strings.Contains(err.Error(), "Eine Netz-Änderung wartet auf Bestätigung") {
		t.Fatalf("Fehler: %v", err)
	}
	if _, busy := e.a.lockAcquire(lockPDH); busy != "" {
		t.Fatalf("Sperre blieb hängen: %s", busy)
	}
}

func TestOSJobRebootWaitsForNet(t *testing.T) {
	e := osJobEnv(t, 3, true)
	a := e.a
	netWriteState(t, a, "state=pending\nid=x\n")
	osWriteJob(t, e, osLockedJob(e, osSteps{Step: "reboot", Pending: 3, StepAt: e.clk.now(), Msg: "3 Pakete, Neustart ja"}))
	a.loadOSJob()
	for i := 0; i < 10; i++ {
		if a.osJobStep(context.Background()) {
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
	// Änderung erledigt: Neustart
	netWriteState(t, a, "state=confirmed\nid=x\nmaint=0\nmaint_off=done\n")
	e.boot.Store("boot-1")
	for i := 0; i < 3; i++ {
		a.osJobStep(context.Background())
	}
	if strings.Count(e.rec.all(), "reboot") != 1 {
		t.Fatalf("Neustart nach Bestätigung: %s", e.rec.all())
	}
	// Frist: bleibt die Änderung offen, scheitert der Auftrag ohne Neustart
	e2 := osJobEnv(t, 3, true)
	netWriteState(t, e2.a, "state=pending\nid=x\n")
	osWriteJob(t, e2, osLockedJob(e2, osSteps{Step: "reboot", Pending: 3, StepAt: e2.clk.now()}))
	e2.a.loadOSJob()
	runOSJob(t, e2.a, e2.clk, 60)
	v := e2.a.jobView()
	if v.Phase != string(ha.UpFailed) || !strings.Contains(v.Result, "Eine Netz-Änderung wartet auf Bestätigung") ||
		strings.Contains(e2.rec.all(), "reboot") || !strings.Contains(e2.rec.all(), "start-pdh") {
		t.Fatalf("Frist: %+v / %s", v, e2.rec.all())
	}
}

func TestOSJobExcludesPDHJob(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	if _, err := a.startSingleJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.startOSJob(context.Background(), srcButton, false); err == nil || !strings.Contains(err.Error(), "PDH-Update") {
		t.Fatalf("OS neben PDH: %v", err)
	}
	e2 := osJobEnv(t, 4, false)
	if _, err := e2.a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.a.startSingleJob(context.Background(), srcButton, false); err == nil || !strings.Contains(err.Error(), "Betriebssystem-Update") {
		t.Fatalf("PDH neben OS: %v", err)
	}
	if _, err := e2.a.startOSJob(context.Background(), srcButton, false); err == nil || !strings.Contains(err.Error(), "Betriebssystem-Update") {
		t.Fatalf("OS neben OS: %v", err)
	}
}

func TestOSJobAbort(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5 && a.jobView().Phase != string(ha.UpCountdown); i++ {
		a.osJobStep(context.Background())
	}
	if a.jobView().Phase != string(ha.UpCountdown) {
		t.Fatalf("Countdown nicht erreicht: %+v", a.jobView())
	}
	if err := a.abortSingleJob("Anna"); err != nil {
		t.Fatal(err)
	}
	runOSJob(t, a, e.clk, 5)
	v := a.jobView()
	if v.Phase != string(ha.UpFailed) || v.Result != "Abgebrochen von Anna" || strings.Contains(e.rec.all(), "psql-lock") {
		t.Fatalf("Abbruch: %+v / %s", v, e.rec.all())
	}
	if _, busy := a.lockAcquire(lockPDH); busy != "" {
		t.Fatalf("Sperre nicht frei: %s", busy)
	}
	// während des Einspielens nicht mehr
	e2 := osJobEnv(t, 4, false)
	osWriteJob(t, e2, osLockedJob(e2, osSteps{Step: "install", Pending: 4, StepAt: e2.clk.now()}))
	e2.a.loadOSJob()
	if err := e2.a.abortSingleJob("Anna"); err == nil || !strings.Contains(err.Error(), "Die Pakete werden bereits eingespielt") {
		t.Fatalf("Abbruch beim Einspielen: %v", err)
	}
}

func TestOSJobInstallCancelledKeepsStep(t *testing.T) {
	e := osJobEnv(t, 4, false)
	osWriteJob(t, e, osLockedJob(e, osSteps{Step: "install", Pending: 4, StepAt: e.clk.now()}))
	e.a.loadOSJob()
	ctx, cancel := context.WithCancel(context.Background())
	e.a.osRun = func(c context.Context, action string) ([]byte, error) {
		if action == "install" {
			cancel() // Agent wird beendet
			return nil, c.Err()
		}
		return e.fake.run(c, action)
	}
	e.a.osJobStep(ctx)
	b, err := os.ReadFile(osJobFile())
	if err != nil {
		t.Fatal(err)
	}
	var j osJob
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	if j.OS.Step != "install" || j.OS.Failed {
		t.Fatalf("gesichert: %+v", j.OS)
	}
	if a := e.a; a.osj.job.OS.Step != "install" || a.osj.job.OS.Failed {
		t.Fatalf("im Speicher: %+v", a.osj.job.OS)
	}
}

func TestOSJobLockHeldUntilUnitEnds(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	var active atomic.Bool
	active.Store(true)
	a.osUnitActive = func(context.Context, string) bool { return active.Load() }
	st := osSteps{Step: "install", Pending: 4, Unit: "pdh-os-install-x", StepAt: e.clk.now().Add(-time.Hour)}
	osWriteJob(t, e, osLockedJob(e, st))
	a.loadOSJob()
	runOSJob(t, a, e.clk, 20) // „dauert zu lange“
	if v := a.jobView(); v.Phase != string(ha.UpFailed) {
		t.Fatalf("Ende: %+v", v)
	}
	if _, busy := a.lockAcquire(lockPDH); busy != lockOS {
		t.Fatalf("Sperre muss bleiben, solange die Einheit läuft: %q", busy)
	}
	active.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rel, busy := a.lockAcquire(lockPDH); busy == "" {
			rel()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Sperre nach Ende der Einheit nicht frei")
}

func TestOSStepsZeroStepAtWaits(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	e.a.osUnitActive = func(context.Context, string) bool { return true }
	st := osSteps{Step: "install", Pending: 3, Unit: "pdh-os-install-alt"} // StepAt fehlt
	if e.a.osAdvance(context.Background(), &st, e.persist) {
		t.Fatalf("fertig: %+v", st)
	}
	if st.Step != "install" || st.Failed || st.StepAt.IsZero() || e.pers == 0 {
		t.Fatalf("st=%+v persist=%d", st, e.pers)
	}
}

func osJobServe(t *testing.T, a *updateAgent, method, path, body string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/os/job/start", a.authorized(http.HandlerFunc(a.osJobStartHandler)))
	mux.Handle("GET /v1/update/job", a.authorized(http.HandlerFunc(a.jobHandler)))
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+a.token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestOSJobHandlers(t *testing.T) {
	e := osJobEnv(t, 4, false)
	code, m := osJobServe(t, e.a, http.MethodPost, "/v1/os/job/start", "")
	if code != 200 || m["ok"] != true {
		t.Fatalf("Start: %d %v", code, m)
	}
	if h, ok := m["hints"].([]any); !ok || len(h) != 0 {
		t.Fatalf("hints: %v", m["hints"])
	}
	code, m = osJobServe(t, e.a, http.MethodGet, "/v1/update/job", "")
	if code != 200 || m["kind"] != "os" || m["active"] != true {
		t.Fatalf("Auftrag: %d %v", code, m)
	}
}

// ── Korrekturrunde 1 ──────────────────────────────────────────────────────

func osWaitLockFree(t *testing.T, a *updateAgent) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rel, busy := a.lockAcquire(lockPDH); busy == "" {
			rel()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Sperre nach Ende der Einheit nicht frei")
}

func TestOSJobCheckTimeoutHoldsLock(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	var active atomic.Bool
	active.Store(true)
	a.osUnitActive = func(context.Context, string) bool { return active.Load() }
	inner := e.fake.run
	a.osRun = func(ctx context.Context, action string) ([]byte, error) {
		if action == "check" {
			return nil, &osUnitRunning{Unit: "pdh-os-check-x", Action: "check"}
		}
		return inner(ctx, action)
	}
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runOSJob(t, a, e.clk, 10)
	if v := a.jobView(); v.Phase != string(ha.UpFailed) {
		t.Fatalf("Ende: %+v", v)
	}
	if _, busy := a.lockAcquire(lockPDH); busy != lockOS {
		t.Fatalf("Sperre muss bleiben, solange die Prüf-Einheit läuft: %q", busy)
	}
	active.Store(false)
	osWaitLockFree(t, a)
}

func TestOSJobResumeInstallStopsPDH(t *testing.T) {
	e := osJobEnv(t, 3, false)
	e.rec.mu.Lock()
	e.rec.fail["pdh-active"] = false // pdh läuft (nach dem Hochfahren wieder gestartet)
	e.rec.mu.Unlock()
	e.rec.on["stop-pdh"] = func() {
		e.rec.mu.Lock()
		e.rec.fail["pdh-active"] = true
		e.rec.mu.Unlock()
	}
	osWriteJob(t, e, osLockedJob(e, osSteps{Step: "install", Pending: 3, StepAt: e.clk.now()}))
	e.a.loadOSJob()
	runOSJob(t, e.a, e.clk, 20)
	assertOrder(t, e.rec.all(), "stop-pdh", "os install", "start-pdh")
	if v := e.a.jobView(); v.Phase != string(ha.UpDone) {
		t.Fatalf("Ende: %+v", v)
	}
}

func TestLoadOSJobHoldsLockForRunningUnit(t *testing.T) {
	for _, ended := range []bool{false, true} {
		e := osJobEnv(t, 3, false)
		var active atomic.Bool
		active.Store(true)
		e.a.osUnitActive = func(_ context.Context, u string) bool { return u == "pdh-os-install-x" && active.Load() }
		j := osLockedJob(e, osSteps{Step: "install", Pending: 3, Unit: "pdh-os-install-x", StepAt: e.clk.now()})
		if ended {
			j.Job.Phase, j.OS.Step, j.OS.Failed, j.OS.Msg = ha.UpFailed, "done", true, "Einspielen dauert zu lange"
		}
		osWriteJob(t, e, j)
		e.a.loadOSJob()
		if _, busy := e.a.lockAcquire(lockPDH); busy != lockOS {
			t.Fatalf("beendet=%v: Sperre muss gehalten werden: %q", ended, busy)
		}
		active.Store(false)
		if ended {
			osWaitLockFree(t, e.a)
		}
	}
}

func TestOSJobAbortWinsBeforeInstall(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	var abortErr error
	a.osBeforeInstall = func() { abortErr = a.abortSingleJob("Anna") } // Fenster zwischen Sicherung und Umschalten
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	runOSJob(t, a, e.clk, 40)
	v := a.jobView()
	all := e.rec.all()
	installed := strings.Contains(all, "os install")
	if abortErr == nil {
		if installed || v.Phase != string(ha.UpFailed) || v.Result != "Abgebrochen von Anna" || !strings.Contains(all, "start-pdh") {
			t.Fatalf("Abbruch angenommen, aber: %+v / %s", v, all)
		}
	} else if !installed || !strings.Contains(abortErr.Error(), "Die Pakete werden bereits eingespielt") {
		t.Fatalf("Abbruch abgelehnt (%v), aber installed=%v", abortErr, installed)
	}
}

func TestNetApplyNamesOSHolder(t *testing.T) {
	e := osJobEnv(t, 4, false)
	if _, err := e.a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	ans := netCall(t, e.a, http.MethodPost, "/v1/net/apply", `{"action":"fix"}`)
	if ans.OK || !strings.Contains(ans.Error, "Betriebssystem-Update") || strings.Contains(ans.Error, "PDH-Update") {
		t.Fatalf("%+v", ans)
	}
}

func TestOSJobNoPackagesAfterCheckIsDone(t *testing.T) {
	e := osJobEnv(t, 4, false)
	a := e.a
	if _, err := a.startOSJob(context.Background(), srcButton, false); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	e.fake.statusOut = osStepsStatus(0, false) // zwischen Start und Prüfen erledigt
	e.fake.mu.Unlock()
	runOSJob(t, a, e.clk, 10)
	v := a.jobView()
	if v.Phase != string(ha.UpDone) || v.Result != "Keine Pakete zu aktualisieren" {
		t.Fatalf("Ende: %+v", v)
	}
	if lr := a.osState().LastRun; lr == nil || !lr.OK || lr.Packages != 0 {
		t.Fatalf("last_run: %+v", lr)
	}
}
