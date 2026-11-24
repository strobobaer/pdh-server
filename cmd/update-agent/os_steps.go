package main

// Betriebssystem-Updates (Teil A): Schrittmaschine des Auftrags „Pakete einspielen“.
//
//	"" → stop → install → (reboot → boot)? → start → verify → done
//
// osAdvance führt höchstens einen Schritt aus und ist nach einem Neustart des Agenten oder
// des Rechners wieder aufnehmbar: der Zustand (osSteps) wird vor install und vor dem Neustart
// über persist gesichert. apt, das Skript und der Neustart laufen nur über systemd-run bzw.
// jobHost, nie im Agenten selbst.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const osBootWait = 20 * time.Minute // Warten auf das Hochfahren nach dem Neustart

// osSteps: Zustand des Auftrags (wird als JSON gesichert).
type osSteps struct {
	Step     string    `json:"step"`
	Pending  int       `json:"pending"` // Pakete vor dem Einspielen
	BootID   string    `json:"boot_id"`
	StepAt   time.Time `json:"step_at"`
	Rebooted bool      `json:"rebooted"`
	Failed   bool      `json:"failed"`
	Msg      string    `json:"msg"`  // Fehlertext bzw. Ergebnis („3 Pakete, Neustart ja“)
	Unit     string    `json:"unit"` // Name der Einheit von „install“ (Wiederaufnahme)
}

func (st osSteps) installing() bool { return st.Step == "install" }

// fail merkt einen Fehler: ein bisheriges Ergebnis („3 Pakete …“) entfällt, der erste
// Fehlertext bleibt vorn, spätere werden angehängt.
func (st *osSteps) fail(msg string) {
	if !st.Failed {
		st.Msg = ""
	}
	st.Failed = true
	if st.Msg == "" {
		st.Msg = msg
	} else if !strings.Contains(st.Msg, msg) {
		st.Msg += "; " + msg
	}
}

// osAdvance führt höchstens einen Schritt aus. true = beendet (Step=="done"; bei Failed ist
// pdh wieder gestartet).
func (a *updateAgent) osAdvance(ctx context.Context, st *osSteps, persist func()) (done bool) {
	switch st.Step {
	case "done":
		return true

	case "":
		if out, err := a.osExec(ctx, "check"); err != nil {
			a.osClearCache()
			var ur *osUnitRunning
			if errors.As(err, &ur) {
				st.fail(err.Error())
			} else if msg := osLastLines(string(out), 3); msg != "" {
				st.fail("Prüfen fehlgeschlagen: " + msg)
			} else {
				st.fail("Prüfen fehlgeschlagen: " + err.Error())
			}
			st.Step = "done"
			return true
		}
		a.osClearCache()
		ss, err := a.osStatus(ctx)
		if err != nil {
			st.fail("Status nicht lesbar: " + err.Error())
			st.Step = "done"
			return true
		}
		st.Pending = len(ss.Upgradable)
		if st.Pending == 0 {
			st.Msg, st.Step = "Keine Pakete zu aktualisieren", "done"
			return true
		}
		st.Step = "stop"

	case "stop":
		if err := a.jobStopPDH(ctx); err != nil {
			st.fail("pdh nicht gestoppt: " + err.Error())
			st.Step = "start"
			return false
		}
		st.Step = "install"

	case "install":
		// Einheit läuft noch (Agent oder Rechner wurde zwischendurch neu gestartet): warten,
		// nie ein zweites apt daneben starten
		if st.Unit != "" && a.osUnitIsActive(ctx, st.Unit) {
			return false
		}
		st.Unit = a.osNewUnit("install")
		persist()
		out, err := a.osExecUnit(ctx, "install", st.Unit)
		if err != nil {
			var ur *osUnitRunning
			if errors.As(err, &ur) {
				return false // läuft noch: Step bleibt „install“
			}
			msg := osLastLines(string(out), 3)
			if msg == "" {
				msg = err.Error()
			}
			st.Unit = ""
			st.fail("Einspielen fehlgeschlagen: " + msg)
			st.Step = "start"
			return false
		}
		st.Unit = ""
		a.osClearCache()
		ss, err := a.osStatus(ctx)
		if err != nil {
			st.fail("Status nach dem Einspielen nicht lesbar: " + err.Error())
			st.Step = "start"
			return false
		}
		reboot := "nein"
		st.Step = "start"
		if ss.RebootRequired {
			reboot, st.Step = "ja", "reboot"
		}
		st.Msg = fmt.Sprintf("%d Pakete, Neustart %s", st.Pending, reboot)

	case "reboot":
		id := a.osBootID()
		if id == "" {
			st.fail("boot_id nicht lesbar – kein Neustart")
			st.Step = "start"
			return false
		}
		st.BootID, st.StepAt, st.Step = id, a.now(), "boot"
		persist() // vor dem Neustart: danach gibt es den Agenten nicht mehr
		if err := a.jobHost(ctx, jobRebootScript, nil); err != nil {
			st.fail("Neustart fehlgeschlagen: " + err.Error())
			st.Step = "start"
		}

	case "boot":
		cur := a.osBootID()
		switch {
		case cur != "" && cur != st.BootID:
			st.Rebooted, st.Step = true, "start"
		case a.now().Sub(st.StepAt) < osBootWait:
			return false
		default:
			st.fail("Neustart hat nicht stattgefunden – bitte Konsole prüfen")
			st.Step = "start"
		}

	case "start":
		if err := a.jobHost(ctx, jobStartScript, nil); err != nil {
			st.fail("pdh nicht gestartet: " + err.Error())
		}
		st.Step, st.StepAt = "verify", a.now()

	case "verify":
		err := a.pdhHealthy(ctx, "", "")
		switch {
		case err == nil:
			st.Step = "done"
		case a.now().Sub(st.StepAt) < jobVerifyWait:
			return false
		default:
			st.fail("Server antwortet nach dem Neustart nicht: " + err.Error())
			st.Step = "done"
		}

	default:
		st.fail("Unbekannter Schritt " + st.Step)
		st.Step = "start"
	}
	return st.Step == "done"
}
