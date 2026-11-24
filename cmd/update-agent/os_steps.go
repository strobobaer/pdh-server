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

// failAfterInstall: Fehler nach erfolgreichem Einspielen; das Paketergebnis bleibt stehen.
func (st *osSteps) failAfterInstall(msg string) {
	st.Failed = true
	st.Msg = fmt.Sprintf("%d Pakete eingespielt; %s", st.Pending, msg)
}

// osInstallWait: weiter auf die Einheit von „install“ warten (false) – höchstens
// osInstallTimeout plus 15 Minuten; danach Fehler und pdh wird wieder gestartet. Die Einheit
// bleibt in st.Unit vermerkt (die Sperre bleibt, bis sie endet).
func (a *updateAgent) osInstallWait(st *osSteps, persist func()) bool {
	if st.StepAt.IsZero() {
		// gesicherter Stand ohne Beginn (ältere Datei): ab jetzt zählen, nicht sofort abbrechen
		st.StepAt = a.now()
		persist()
		return false
	}
	if a.now().Sub(st.StepAt) >= osInstallTimeout+15*time.Minute {
		st.fail("Einspielen dauert zu lange – Einheit " + st.Unit + " läuft noch")
		st.Step = "start"
	}
	return false
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
				st.Unit = ur.Unit // die Sperre bleibt, bis die Einheit endet
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
			var ur *osUnitRunning
			if errors.As(err, &ur) {
				st.Unit = ur.Unit
			}
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
			return a.osInstallWait(st, persist)
		}
		// frischer Lauf: pdh darf nicht laufen (nach einem Neustart des Rechners startet es von selbst wieder)
		if a.pdhActive(ctx) {
			if err := a.jobStopPDH(ctx); err != nil {
				st.fail("pdh nicht gestoppt: " + err.Error())
				st.Step = "start"
				return false
			}
		}
		st.Unit = a.osNewUnit("install")
		st.StepAt = a.now() // Beginn der Frist für das Warten auf die Einheit
		persist()
		out, err := a.osExecUnit(ctx, "install", st.Unit)
		if err != nil {
			var ur *osUnitRunning
			if errors.As(err, &ur) {
				return a.osInstallWait(st, persist) // läuft noch: Step bleibt „install“
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
			st.failAfterInstall("Neustart nicht geprüft: Status nicht lesbar: " + err.Error())
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
			st.failAfterInstall("Neustart nicht möglich: boot_id nicht lesbar")
			st.Step = "start"
			return false
		}
		st.BootID, st.StepAt, st.Step = id, a.now(), "boot"
		persist() // vor dem Neustart: danach gibt es den Agenten nicht mehr
		if err := a.jobHost(ctx, jobRebootScript, nil); err != nil {
			st.failAfterInstall("Neustart fehlgeschlagen: " + err.Error())
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
			st.failAfterInstall("Neustart hat nicht stattgefunden – bitte Konsole prüfen")
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
