package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Schrittmaschine der Betriebssystem-Pakete (os_steps.go).

func osStepsStatus(pkgs int, reboot bool) string {
	var list []string
	for i := 0; i < pkgs; i++ {
		list = append(list, `{"name":"p`+string(rune('a'+i))+`","old":"1","new":"2","security":false}`)
	}
	rb := "false"
	if reboot {
		rb = "true"
	}
	return `{"ok":true,"script_version":2,"system":"Debian","kernel":"6.12","checked_at":"2026-11-24T10:00:00Z","upgradable":[` +
		strings.Join(list, ",") + `],"security_count":0,"reboot_required":` + rb + `,"reboot_pkgs":[]}`
}

type osStepsEnv struct {
	a      *updateAgent
	rec    *jobRec
	clk    *jobClock
	health *jobHealth
	fake   *osFake
	boot   atomic.Value // string
	pers   int
	// persErr: schlägt das n-te Speichern (ab 1) fehl? nil = nie
	persErr func(n int) error
}

// osStepsAgent: 3 Pakete; nach „install“ meldet „status“ rebootAfter.
func osStepsAgent(t *testing.T, pkgs int, rebootAfter bool) *osStepsEnv {
	t.Helper()
	a, rec, clk, health := jobTestAgent(t)
	f := &osFake{statusOut: osStepsStatus(pkgs, false), rec: rec}
	f.onInstall = func() {
		f.mu.Lock()
		f.statusOut = osStepsStatus(0, rebootAfter)
		f.mu.Unlock()
	}
	a.osRun = f.run
	e := &osStepsEnv{a: a, rec: rec, clk: clk, health: health, fake: f}
	e.boot.Store("boot-1")
	a.bootID = func() string { return e.boot.Load().(string) }
	// pdh lässt sich stoppen (Standard in jobTestAgent: „pdh-active“ scheitert = gestoppt)
	return e
}

func (e *osStepsEnv) persist() error {
	e.pers++
	e.rec.add("persist", nil)
	if e.persErr != nil {
		return e.persErr(e.pers)
	}
	return nil
}

// run: Schleife mit Obergrenze; Uhr +1 min je Runde; hook je Runde (optional).
func (e *osStepsEnv) run(t *testing.T, st *osSteps, each func(round int)) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if e.a.osAdvance(context.Background(), st, e.persist) {
			return
		}
		e.clk.add(time.Minute)
		if each != nil {
			each(i)
		}
	}
	t.Fatalf("Schrittmaschine nach 100 Runden nicht beendet: %+v / %s", *st, e.rec.all())
}

func TestOSStepsNoPending(t *testing.T) {
	e := osStepsAgent(t, 0, false)
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if st.Step != "done" || st.Failed || !strings.Contains(st.Msg, "Keine Pakete") {
		t.Fatalf("st=%+v", st)
	}
	if strings.Contains(all, "stop-pdh") || strings.Contains(all, "os install") {
		t.Fatalf("Ablauf: %s", all)
	}
}

func TestOSStepsNoReboot(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	assertOrder(t, all, "os check", "os status", "stop-pdh", "persist", "os install", "os status", "start-pdh")
	if strings.Contains(all, "reboot") || st.Failed || st.Rebooted || st.Step != "done" || st.Pending != 3 {
		t.Fatalf("st=%+v ablauf=%s", st, all)
	}
	if !strings.Contains(st.Msg, "Neustart nein") {
		t.Fatalf("Msg=%q", st.Msg)
	}
}

func TestOSStepsReboot(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	var st osSteps
	rebootSeen := false
	rounds := 0
	for i := 0; i < 100 && !(st.Step == "done"); i++ {
		done := e.a.osAdvance(context.Background(), &st, e.persist)
		if strings.Contains(e.rec.all(), "reboot") {
			rebootSeen = true
		}
		if done {
			break
		}
		if rebootSeen && st.Step != "boot" && st.Step != "done" && !st.Rebooted {
			t.Fatalf("nach dem Neustart-Befehl Step=%q ohne neue boot_id", st.Step)
		}
		if rebootSeen {
			rounds++
			if rounds == 3 { // Rechner ist neu gestartet
				e.boot.Store("boot-2")
			}
		}
		e.clk.add(time.Minute)
	}
	all := e.rec.all()
	if st.Step != "done" || !st.Rebooted || st.Failed {
		t.Fatalf("st=%+v ablauf=%s", st, all)
	}
	if strings.Count(all, "reboot") != 1 || strings.Count(all, "os install") != 1 {
		t.Fatalf("Ablauf: %s", all)
	}
	assertOrder(t, all, "persist", "os install", "persist", "reboot", "start-pdh")
	if st.BootID != "boot-1" {
		t.Fatalf("BootID=%q", st.BootID)
	}
}

func TestOSStepsBootNeverChanges(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	var st osSteps
	e.run(t, &st, nil)
	if !st.Failed || !strings.Contains(st.Msg, "Neustart hat nicht stattgefunden") || st.Step != "done" || st.Rebooted {
		t.Fatalf("st=%+v", st)
	}
	assertOrder(t, e.rec.all(), "reboot", "start-pdh")
}

func TestOSStepsBootIDUnreadable(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	e.boot.Store("")
	var st osSteps
	e.run(t, &st, nil)
	if !st.Failed || !strings.Contains(st.Msg, "boot_id nicht lesbar") || strings.Contains(e.rec.all(), "reboot") {
		t.Fatalf("st=%+v ablauf=%s", st, e.rec.all())
	}
	assertOrder(t, e.rec.all(), "os install", "start-pdh")
}

func TestOSStepsVerifyTimeout(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	e.health.setCode(503)
	var st osSteps
	e.run(t, &st, nil)
	if !st.Failed || !strings.HasPrefix(st.Msg, "Server antwortet nach dem Neustart nicht") || st.Step != "done" {
		t.Fatalf("st=%+v", st)
	}
}

func TestOSStepsInstallFails(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	e.fake.installErr = errors.New("exit status 100")
	e.fake.installOut = "Setting up x\nE: Sub-process /usr/bin/dpkg returned an error code (1)\n"
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if !st.Failed || !strings.Contains(st.Msg, "E: Sub-process /usr/bin/dpkg returned an error code (1)") || st.Step != "done" {
		t.Fatalf("st=%+v", st)
	}
	if strings.Contains(all, "reboot") {
		t.Fatalf("Neustart trotz Fehler: %s", all)
	}
	assertOrder(t, all, "os install", "start-pdh")
}

func TestOSStepsStopFails(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	e.rec.mu.Lock()
	e.rec.fail = map[string]bool{} // „pdh-active“ gelingt = pdh läuft weiter
	e.rec.mu.Unlock()
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if !st.Failed || strings.Contains(all, "os install") || st.Step != "done" {
		t.Fatalf("st=%+v ablauf=%s", st, all)
	}
	assertOrder(t, all, "stop-pdh", "start-pdh")
}

func TestOSStepsInstallStillRunning(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	var active atomic.Bool
	active.Store(true)
	e.a.osUnitActive = func(context.Context, string) bool { return active.Load() }
	calls := 0
	inner := e.fake.run
	e.a.osRun = func(ctx context.Context, action string) ([]byte, error) {
		if action == "install" {
			calls++
			if calls == 1 {
				e.rec.add("os install", nil)
				return nil, &osUnitRunning{Unit: "pdh-os-install-x", Action: "install"}
			}
		}
		return inner(ctx, action)
	}
	var st osSteps
	for i := 0; i < 8; i++ { // bis install gerufen wurde (3 Runden), dann: Einheit läuft, Step bleibt „install“
		if e.a.osAdvance(context.Background(), &st, e.persist) {
			t.Fatalf("fertig: %+v", st)
		}
		if i < 2 {
			continue
		}
		if st.Step != "install" || st.Unit == "" {
			t.Fatalf("Runde %d: %+v", i, st)
		}
	}
	if strings.Contains(e.rec.all(), "start-pdh") || strings.Count(e.rec.all(), "os install") != 1 {
		t.Fatalf("Ablauf: %s", e.rec.all())
	}
	active.Store(false)
	e.run(t, &st, nil)
	if strings.Count(e.rec.all(), "os install") != 2 || st.Step != "done" || st.Failed {
		t.Fatalf("st=%+v ablauf=%s", st, e.rec.all())
	}
	assertOrder(t, e.rec.all(), "os install", "os install", "start-pdh")
}

func TestOSStepsResumeWaitsForUnit(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	var active atomic.Bool
	active.Store(true)
	e.a.osUnitActive = func(_ context.Context, u string) bool { return u == "pdh-os-install-alt" && active.Load() }
	st := osSteps{Step: "install", Pending: 3, Unit: "pdh-os-install-alt", StepAt: e.clk.now()}
	for i := 0; i < 3; i++ {
		if e.a.osAdvance(context.Background(), &st, e.persist) {
			t.Fatalf("fertig: %+v", st)
		}
	}
	if strings.Contains(e.rec.all(), "os install") || strings.Contains(e.rec.all(), "start-pdh") || st.Step != "install" {
		t.Fatalf("st=%+v ablauf=%s", st, e.rec.all())
	}
	active.Store(false)
	e.run(t, &st, nil)
	if strings.Count(e.rec.all(), "os install") != 1 || st.Step != "done" || st.Failed || st.Unit != "" {
		t.Fatalf("st=%+v ablauf=%s", st, e.rec.all())
	}
}

func TestOSStepsInstalling(t *testing.T) {
	if !(osSteps{Step: "install"}).installing() || (osSteps{Step: "stop"}).installing() {
		t.Fatal("installing()")
	}
}

func TestOSStepsInstallWaitBounded(t *testing.T) {
	e := osStepsAgent(t, 3, false)
	e.a.osUnitActive = func(context.Context, string) bool { return true }
	inner := e.fake.run
	e.a.osRun = func(ctx context.Context, action string) ([]byte, error) {
		if action == "install" {
			e.rec.add("os install", nil)
			return nil, &osUnitRunning{Unit: "pdh-os-install-x", Action: "install"}
		}
		return inner(ctx, action)
	}
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if !st.Failed || !strings.Contains(st.Msg, "dauert zu lange") || st.Unit == "" || st.Step != "done" {
		t.Fatalf("st=%+v", st)
	}
	if strings.Count(all, "os install") != 1 {
		t.Fatalf("zweites install: %s", all)
	}
	assertOrder(t, all, "os install", "start-pdh")
}

func TestOSStepsStatusUnreadableAfterInstall(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	e.fake.onInstall = func() {
		e.fake.mu.Lock()
		e.fake.statusOut = "kaputt"
		e.fake.mu.Unlock()
	}
	var st osSteps
	e.run(t, &st, nil)
	all := e.rec.all()
	if !st.Failed || !strings.Contains(st.Msg, "3 Pakete eingespielt") || strings.Contains(all, "reboot") {
		t.Fatalf("st=%+v ablauf=%s", st, all)
	}
	assertOrder(t, all, "os install", "start-pdh")
}

func TestOSStepsRebootCommandFails(t *testing.T) {
	e := osStepsAgent(t, 3, true)
	e.rec.mu.Lock()
	e.rec.fail["reboot"] = true
	e.rec.mu.Unlock()
	var st osSteps
	e.run(t, &st, nil)
	if !st.Failed || !strings.Contains(st.Msg, "Neustart fehlgeschlagen") || strings.Contains(st.Msg, "nicht stattgefunden") || !strings.Contains(st.Msg, "3 Pakete eingespielt") {
		t.Fatalf("st=%+v", st)
	}
	assertOrder(t, e.rec.all(), "reboot", "start-pdh")
}
