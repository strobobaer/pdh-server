package main

// pgActor führt die Aktionen des Entscheidungskerns an PostgreSQL und am
// Dienst pdh wirklich aus (Teil 3). Die mitwandernde Adresse setzt der
// Abgleich in access_worker.go (Teil 5), nicht dieser Ausführer.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

// pgPassfile: Passwort von pdh_replica für Standby und pg_rewind (postgres, 0600).
const pgPassfile = "/var/lib/postgresql/.pdh-ha-pgpass"

type pgEnv struct {
	SelfID, PartnerID         string
	PartnerAddrs              []string // Hosts ohne Port
	ReplicaPass               string
	CAFile, CertFile, KeyFile string // PostgreSQL-Kopien (postgres, 0600)
	SlotKeepMB                int64
	Listen                    []string
	DataCurrent               bool          // für promote ohne Handbefehl
	Tick                      time.Duration // Takt: so schnell kommt ein Herzschlag des Partners an
}

type pgActor struct {
	pg  *pgCtl
	svc serviceCtl // pdh
	tun serviceCtl // Cloudflare-Tunnel (nur am aktiven Knoten)
	// Nextcloud (nur am aktiven Knoten); nil = keiner. Ein Fehler hier ist nur ein
	// Alarm und gibt nie einen Fehler der Aktion zurück: Nextcloud löst keine
	// Umschaltung aus und schaltet keine Wartung ein.
	nc serviceCtl
	// nach einer Übernahme (promote), wenn Nextcloud gestartet ist: Prüflauf der
	// Dateien; läuft in einer eigenen Goroutine, nie hier (nil = keiner)
	afterTakeover func()
	ev            *eventLog
	env           func() pgEnv
	pushCfg       func(ctx context.Context) error // Konfiguration an den Partner (nil = keine)
	// letzter Dateiabgleich vor der Übergabe (nil = keine Spiegelung); hat seine
	// eigene Frist (MirrorFinalMax), unabhängig von wait
	mirrorFinal func(ctx context.Context) error
	// Stand des eigenen Tauschs (Amtszeit term der Abgabe); nil = läuft (Tests, Handbefehle)
	phase func(term uint64) swapPhase
	// merkt „Rücknahme verweigert, Partner hat übernommen“ für die Wiederanbindung (R32)
	refused func()
	// R48: die Rolle ist noch aktiv (nil = nicht geprüft: Tests, Handbefehle am aktiven Server)
	stillActive func() bool
	wait        time.Duration // Frist für den Gleichstand beim Abgeben
	poll        time.Duration
	now         func() time.Time
	passfile    string // Pfad der passfile (Tests: Temp-Ordner)

	rewindMem atomic.Bool // pg_rewind begonnen, Ordner noch nicht als Standby gelaufen (mit Datei, R35)

	mu        sync.Mutex
	syncNames string               // gewünschte synchronous_standby_names (Replikation, Task 8)
	mirror    *mirrorNote          // zuletzt gemeldeter Ausgang des Spiegel-Starts (nil = keiner)
	ncAlarmAt map[string]time.Time // letzter Alarm je Art ("start", "stop")
}

// mirrorNote: Ausgang eines Spiegel-Starts ("" = gestartet, sonst Fehler).
type mirrorNote struct {
	err string
	at  time.Time
}

// mirrorRepeat: gleicher Ausgang wird höchstens so oft erneut gemeldet.
const mirrorRepeat = time.Hour

func newPGActor(pg *pgCtl, svc, tun serviceCtl, ev *eventLog, env func() pgEnv) *pgActor {
	return &pgActor{pg: pg, svc: svc, tun: tun, ev: ev, env: env, wait: 60 * time.Second, poll: 500 * time.Millisecond, now: time.Now, passfile: pgPassfile}
}

// standbyConninfo: alle Wege zum aktiven Knoten, Passwort nur aus der passfile.
func standbyConninfo(e pgEnv) string {
	ports := make([]string, len(e.PartnerAddrs))
	for i := range ports {
		ports[i] = fmt.Sprint(pgPort)
	}
	return fmt.Sprintf("host=%s port=%s user=%s application_name=%s sslmode=verify-ca sslrootcert=%s passfile=%s",
		strings.Join(e.PartnerAddrs, ","), strings.Join(ports, ","), replicaUser, slotName(e.SelfID), e.CAFile, pgPassfile)
}

func (a *pgActor) syncTarget() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.syncNames
}

func (a *pgActor) setSyncTarget(s string) {
	a.mu.Lock()
	a.syncNames = s
	a.mu.Unlock()
}

func (a *pgActor) primaryConf(e pgEnv, readOnly bool) pgConf {
	return pgConf{Listen: e.Listen, SyncNames: a.syncTarget(), ReadOnly: readOnly, SlotKeepMB: e.SlotKeepMB,
		CertFile: e.CertFile, KeyFile: e.KeyFile, CAFile: e.CAFile}
}

func (a *pgActor) standbyConf(e pgEnv) pgConf {
	return pgConf{Listen: e.Listen, PrimaryConninfo: standbyConninfo(e), SlotName: slotName(e.SelfID),
		SlotKeepMB: e.SlotKeepMB, CertFile: e.CertFile, KeyFile: e.KeyFile, CAFile: e.CAFile}
}

func (a *pgActor) Do(ctx context.Context, act ha.Action) error {
	switch act.Kind {
	case ha.ActPromote:
		return a.promote(ctx, act)
	case ha.ActDemote:
		return a.demote(ctx, act.Term)
	case ha.ActDemoteFreeze:
		return a.demoteFreeze(ctx, act.Term)
	case ha.ActAbandon:
		return a.abandon(ctx)
	case ha.ActFence:
		return a.fence(ctx)
	case ha.ActUnfence:
		return a.unfence(ctx)
	case ha.ActRejoin:
		return a.rejoin(ctx, act.Term)
	case ha.ActRejoinStranded:
		return a.rejoinStranded(ctx, act.Term)
	case ha.ActRecopyUnsafe:
		return a.recopyUnsafe(ctx)
	case ha.ActVIPTake, ha.ActVIPRelease:
		// Die mitwandernde Adresse folgt allein dem Abgleich (accessWorker);
		// ein Fehler hier darf nie Wartung einschalten.
		return nil
	}
	a.ev.add(ha.Event{Kind: "action", Text: fmt.Sprintf("würde jetzt: %s (Amtszeit %d)", act.Kind, act.Term)})
	return nil
}

// demote: Rollentausch am aktiven Knoten. Erst nur lesend (auch andere
// Programme im selben PostgreSQL schreiben dann nicht mehr), dann auf den
// Gleichstand warten – bis dahin bleibt alles umkehrbar. Erst danach wird
// PostgreSQL gestoppt und Standby des Partners.
func (a *pgActor) demote(ctx context.Context, term uint64) error {
	e := a.env()
	if a.pushCfg != nil {
		if err := a.pushCfg(ctx); err != nil {
			return fmt.Errorf("Konfiguration nicht übertragen: %w", err)
		}
	}
	if err := a.fence(ctx); err != nil {
		return err
	}
	if err := a.finalMirror(ctx); err != nil {
		return a.undoFence(ctx, err, term, true)
	}
	if err := a.waitCaughtUp(ctx, slotName(e.PartnerID)); err != nil {
		return a.undoFence(ctx, err, term, true)
	}
	// vor dem Stoppen noch einmal prüfen (R32.3)
	switch a.phaseOf(term) {
	case phaseWithdrawn:
		return a.undoFence(ctx, swapWithdrawnError{}, term, true)
	case phaseTaken:
		// Der Partner ist (praktisch) hochgestuft: unser Stopp läge hinter dem Trennpunkt,
		// wir könnten der neuen Zeitlinie nicht folgen. Nur lesend bleiben, merken, pg_rewind.
		a.markRefused()
		return swapTakenError{rejoin: true}
	case phaseUnknown:
		return swapWithdrawnError{} // ungewiss (Fenced): nur lesend lassen, nichts tun
	}
	if err := a.pg.ctl(ctx, "stop"); err != nil {
		return err
	}
	if err := a.becomeStandby(ctx, e); err != nil {
		return err
	}
	return a.pg.ctl(ctx, "start")
}

// demoteFreeze: Einfrieren für das Update im Verbund. Wie demote bis zum
// Stopp (nur lesend, Gleichstand abwarten, PostgreSQL stoppen), aber ohne
// standby.signal und ohne Neustart: Die Datenbank bleibt als unveränderter
// Rückfallstand liegen. Das Auftauen übernimmt rejoin (pg_rewind-Probelauf).
func (a *pgActor) demoteFreeze(ctx context.Context, term uint64) error {
	if a.pg.status(ctx).State == "stopped" {
		return nil // schon eingefroren (Wiederholung): nichts zu tun
	}
	if a.pushCfg != nil {
		if err := a.pushCfg(ctx); err != nil {
			return fmt.Errorf("Konfiguration nicht übertragen: %w", err)
		}
	}
	if err := a.fence(ctx); err != nil {
		return err
	}
	if err := a.finalMirror(ctx); err != nil {
		return a.undoFence(ctx, err, term, false)
	}
	e := a.env()
	if err := a.waitCaughtUp(ctx, slotName(e.PartnerID)); err != nil {
		return a.undoFence(ctx, err, term, false)
	}
	// vor dem Stoppen noch einmal prüfen: nur ein zurückgenommener Tausch geht zurück (R32.4)
	if a.phaseOf(term) == phaseWithdrawn {
		return a.undoFence(ctx, swapWithdrawnError{}, term, false)
	}
	return a.stopPG(ctx)
}

// finalMirror: letzter Dateiabgleich nach dem Abschotten (Dienste aus, Datenbank
// nur lesend) – danach kann nichts Neues mehr ankommen. Ohne Spiegelung nichts.
func (a *pgActor) finalMirror(ctx context.Context) error {
	if a.mirrorFinal == nil {
		return nil
	}
	if err := a.mirrorFinal(ctx); err != nil {
		return &finalSyncError{err: err}
	}
	return nil
}

// undoFence nach einem gescheiterten Abgeben: Hat der Step schon zurückgenommen (Rolle
// aktiv), wird wieder eingeschaltet; scheitert das, wird es an den Fehler angehängt. Solange
// der Tausch als laufend gilt, geschieht hier nichts (R42): Der Server ist noch passiv und
// wäre neben einem übernehmenden Partner schreibbar. Den Rückweg geht allein der Step, mit
// gespeicherter und gesendeter Rolle aktiv, und nur mit Sicht auf den Partner (R43).
func (a *pgActor) undoFence(ctx context.Context, err error, term uint64, demote bool) error {
	switch a.settledPhase(ctx, term) {
	case phaseTaken:
		// C1: Der Partner hat übernommen: wieder einschalten gäbe zwei schreibbare
		// Datenbanken und zwei Tunnel. Bei demote merken, damit die Wiederanbindung folgt.
		a.ev.add(ha.Event{Kind: "alarm", Text: "Abschottung bleibt: der Partner hat inzwischen übernommen"})
		if demote {
			a.markRefused()
		}
		return swapTakenError{rejoin: demote} // keine Wartung: der Partner arbeitet (N3)
	case phaseUnknown:
		a.ev.add(ha.Event{Kind: "alarm", Text: "Abschottung bleibt: der Zustand der Rolle ist ungewiss"})
		return err
	case phaseGiving:
		return err
	}
	if uerr := a.unfence(ctx); uerr != nil {
		err = fmt.Errorf("%v; Rücknahme fehlgeschlagen: %v", err, uerr)
	}
	return err
}

// swapPhase: Stand des eigenen Rollentauschs aus Sicht der Abgabe.
type swapPhase int

const (
	phaseGiving    swapPhase = iota // die Übergabe läuft noch (oder unbekannt)
	phaseWithdrawn                  // der Step hat zurückgenommen: wieder aktiv
	phaseTaken                      // der Partner hat übernommen
	phaseUnknown                    // ungewiss (z. B. Fenced), ohne Übernahme
)

func (a *pgActor) phaseOf(term uint64) swapPhase {
	if a.phase == nil {
		return phaseGiving
	}
	return a.phase(term)
}

// settledPhase: Stand des Tauschs vor einer Rücknahme. Übernimmt der Partner, endet sein
// Dateiempfang sofort; der letzte Abgleich scheitert also, bevor sein Herzschlag hier im
// nächsten Takt zählt. Wer dann zurücknähme, machte die Datenbank neben dem neuen aktiven
// Server schreibbar. Darum drei Takte warten (einer darf verloren gehen), solange der Tausch
// noch als laufend gilt.
func (a *pgActor) settledPhase(ctx context.Context, term uint64) swapPhase {
	end := time.Now().Add(3 * a.env().Tick)
	for {
		p := a.phaseOf(term)
		if p != phaseGiving || !time.Now().Before(end) {
			return p
		}
		select {
		case <-ctx.Done():
			return p
		case <-time.After(a.poll):
		}
	}
}

func (a *pgActor) markRefused() {
	if a.refused != nil {
		a.refused()
	}
}

// rejoinStranded (R32, R33): Wiederanbindung nach verweigerter Rücknahme oder per Handbefehl.
// Der Aktor prüft den eigenen Stand frisch: nur ein laufender, nur lesender Primary wird
// angefasst (nie ein eingefrorener Rückfallstand). Schon Standby: erledigt. Jeder andere Stand:
// strandedWaitError, es geschieht nichts. Scheitert die Wiederanbindung, bevor ein echter
// pg_rewind lief, kommt die Datenbank wieder als nur lesender Primary hoch (die Konfiguration
// stammt aus dem Abschotten), sonst hilft danach auch der Handbefehl nicht mehr. Mit der Datei
// aus dem Rewind bleibt sie gestoppt (R35).
func (a *pgActor) rejoinStranded(ctx context.Context, term uint64) error {
	if a.rewindUnsafe() {
		return strandedWaitError{"die Daten sind nach einer abgebrochenen Wiederanbindung unsicher"}
	}
	switch err := strandedStateErr(a.pg.status(ctx)); {
	case errors.Is(err, errAlreadyStandby):
		a.ev.add(ha.Event{Kind: "info", Text: "Wiederanbindung nicht nötig: die Datenbank ist schon Standby"})
		return nil
	case err != nil:
		return err
	}
	err := a.rejoin(ctx, term)
	if err != nil && !a.rewindUnsafe() && a.pg.status(ctx).State == "stopped" {
		if serr := a.pg.ctl(ctx, "start"); serr != nil {
			a.ev.add(ha.Event{Kind: "alarm", Text: "Datenbank nach gescheiterter Wiederanbindung nicht gestartet: " + serr.Error()})
		}
	}
	return err
}

// errAlreadyStandby: die Datenbank folgt schon dem Partner.
var errAlreadyStandby = errors.New("Die Datenbank ist schon Standby – nichts zu tun")

// strandedWaitError: Die Datenbank ist in keinem Stand für die Wiederanbindung. Kein
// Fehlschlag der Aktion: nichts wurde angefasst, keine Wartung, eine Marke bleibt (N2).
type strandedWaitError struct{ why string }

func (e strandedWaitError) Error() string { return "Wiederanbindung nicht möglich: " + e.why }

// strandedStateErr: nil nur für einen laufenden, nur lesenden Primary.
func strandedStateErr(st pgStatus) error {
	switch {
	case st.State == "primary" && st.ReadOnly:
		return nil
	case st.State == "standby":
		return errAlreadyStandby
	case st.State == "primary":
		return strandedWaitError{"die Datenbank ist schreibbar"}
	case st.State == "stopped":
		return strandedWaitError{"die Datenbank ist gestoppt (ein eingefrorener Stand bleibt unberührt)"}
	}
	return strandedWaitError{"der Stand der Datenbank ist nicht lesbar: " + st.Err}
}

// abandon: Rückfallstand verwerfen (Rücktausch ohne Rückweg). Dienste und
// PostgreSQL werden gestoppt, ohne auf den Partner zu warten und ohne
// Standby einzurichten; die Wiedereingliederung entscheidet später rejoin.
func (a *pgActor) abandon(ctx context.Context) error {
	if err := a.stopServices(ctx); err != nil {
		return fmt.Errorf("pdh bzw. Tunnel nicht gestoppt: %w", err)
	}
	return a.stopPG(ctx)
}

// seal (Update „nacheinander“, C3): am aktiven Server abschotten wie beim
// Abgeben (pdh und Tunnel aus, nur lesend, Sitzungen beendet) und warten,
// bis der Spiegel alles eingespielt hat – erst dann darf der Partner
// eingefroren werden. Scheitert der Gleichstand, wird die Abschottung
// zurückgenommen; der Server arbeitet weiter wie vorher.
func (a *pgActor) seal(ctx context.Context) error {
	undo := func(err error) error {
		if uerr := a.unfence(ctx); uerr != nil {
			err = fmt.Errorf("%w; Rücknahme fehlgeschlagen: %v", err, uerr)
		}
		return err
	}
	if err := a.fence(ctx); err != nil {
		return undo(err)
	}
	e := a.env()
	if err := a.waitCaughtUp(ctx, slotName(e.PartnerID)); err != nil {
		if ctx.Err() == nil {
			err = fmt.Errorf("Spiegel hat nicht aufgeholt (Frist %s)", a.wait)
		}
		return undo(err)
	}
	return nil
}

// freeze: Standby einfrieren – PostgreSQL stoppen, Daten und standby.signal
// bleiben unverändert.
func (a *pgActor) freeze(ctx context.Context) error {
	return a.stopPG(ctx)
}

// stopPG stoppt PostgreSQL; ist es schon gestoppt, ist das kein Fehler.
func (a *pgActor) stopPG(ctx context.Context) error {
	if err := a.pg.ctl(ctx, "stop"); err != nil && a.pg.status(ctx).State != "stopped" {
		return err
	}
	return nil
}

// waitCaughtUp wartet, bis der Partner alles eingespielt hat.
func (a *pgActor) waitCaughtUp(ctx context.Context, app string) error {
	deadline := time.Now().Add(a.wait)
	for {
		st := a.pg.status(ctx)
		if st.State == "primary" {
			for _, s := range st.Standbys {
				if s.App == app && s.ReplayLSN >= st.LSN {
					return nil
				}
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("Partner hat nicht aufgeholt (Frist %s) – Rollentausch abgebrochen", a.wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.poll):
		}
	}
}

// frozenPrimary: Datenordner vorhanden und ohne standby.signal – ein
// gestoppter Primary (eingefroren), kein Standby.
func (a *pgActor) frozenPrimary() bool {
	dd := filepath.FromSlash(a.pg.c.DataDir)
	if _, err := os.Stat(filepath.Join(dd, "PG_VERSION")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dd, "standby.signal"))
	return errors.Is(err, os.ErrNotExist)
}

// becomeStandby: standby.signal und Konfiguration für den Partner als Quelle.
func (a *pgActor) becomeStandby(ctx context.Context, e pgEnv) error {
	if err := a.pg.writeOwned(ctx, filepath.Join(filepath.FromSlash(a.pg.c.DataDir), "standby.signal"), nil); err != nil {
		return err
	}
	return a.pg.writeConf(ctx, a.standbyConf(e))
}

func (a *pgActor) promote(ctx context.Context, act ha.Action) error {
	if a.rewindUnsafe() {
		return errDataUnsafe // auch von Hand: dieser Ordner darf nie Primary werden (R35)
	}
	e := a.env()
	// Datenstand zuerst prüfen: ein abgelehntes Hochstufen darf keine Wirkung
	// haben (kein Start des eingefrorenen Clusters).
	if !act.Manual && !e.DataCurrent {
		return errors.New("Datenstand nicht aktuell")
	}
	st := a.pg.status(ctx)
	// Gestoppt (eingefroren, Update im Verbund): erst starten. Ohne
	// standby.signal kommt ein Primary hoch und braucht kein pg_promote.
	fromStopped := st.State == "stopped"
	if fromStopped {
		if err := a.pg.ctl(ctx, "start"); err != nil {
			return fmt.Errorf("Datenbank nicht gestartet: %w", err)
		}
		st = a.pg.status(ctx)
	}
	alreadyPrimary := fromStopped && st.State == "primary"
	// R31: Von Hand darf auch ein laufender, nur lesender Primary übernommen werden
	// (abgeschottet nach einem Tausch, dessen Rücknahme verweigert wurde).
	if act.Manual && st.State == "primary" && st.ReadOnly {
		alreadyPrimary = true
	}
	if st.State != "standby" && !alreadyPrimary {
		return errors.New("Datenbank ist kein Standby")
	}
	if !alreadyPrimary {
		if out, err := a.pg.query(ctx, "SELECT pg_promote(true, 60)"); err != nil || out != "t" {
			return fmt.Errorf("pg_promote fehlgeschlagen: %v %s", err, out)
		}
	}
	if out, err := a.pg.query(ctx, "SELECT pg_is_in_recovery()"); err != nil || out != "f" {
		return fmt.Errorf("Datenbank nach dem Hochstufen nicht schreibbar: %v %s", err, out)
	}
	slot := slotName(e.PartnerID)
	if _, err := a.pg.query(ctx, fmt.Sprintf(
		"SELECT pg_create_physical_replication_slot('%[1]s') WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = '%[1]s')", slot)); err != nil {
		return err
	}
	a.setSyncTarget(slot)
	if err := a.pg.writeConf(ctx, a.primaryConf(e, false)); err != nil {
		return err
	}
	if err := a.pg.ctl(ctx, "reload"); err != nil {
		return err
	}
	// pdh lief als Spiegel: neu starten statt auf seinen Wächter zu warten
	err := errors.Join(a.svc.Restart(ctx), a.tun.Start(ctx))
	// die Datenbank ist schreibbar: jetzt darf Nextcloud laufen
	a.ncStart(ctx)
	if a.afterTakeover != nil {
		a.afterTakeover()
	}
	return err
}

// stopServices: erst den Tunnel, dann Nextcloud, dann pdh. Nextcloud macht nie
// einen Fehler daraus (ncStop meldet nur einen Alarm).
func (a *pgActor) stopServices(ctx context.Context) error {
	a.mirrorReset()
	terr := a.tun.Stop(ctx)
	a.ncStop(ctx)
	return errors.Join(terr, a.svc.Stop(ctx))
}

// ncStart startet Nextcloud (nur nach einem schreibbaren Stand der Datenbank);
// ein Fehler ist ein Alarm, höchstens einmal je Stunde, nie ein Fehler der Aktion.
func (a *pgActor) ncStart(ctx context.Context) {
	if a.nc == nil {
		return
	}
	a.ncNote("start", "Nextcloud nicht gestartet: ", ncTimed(ctx, a.nc.Start))
}

// ncStop stoppt Nextcloud; Fehler wie bei ncStart.
func (a *pgActor) ncStop(ctx context.Context) {
	if a.nc == nil {
		return
	}
	a.ncNote("stop", "Nextcloud nicht gestoppt: ", ncTimed(ctx, a.nc.Stop))
}

// restoreSingle: Einzelbetrieb nach dem Entfernen aus dem Verbund – pdh, Tunnel und
// Nextcloud starten wie früher beim Hochfahren (Reihenfolge wie beim Start).
func (a *pgActor) restoreSingle(ctx context.Context) {
	for _, s := range []serviceCtl{a.svc, a.tun, a.nc} {
		if s == nil {
			continue
		}
		if err := s.SetAutostart(ctx, true); err == nil {
			_ = s.Start(ctx)
		}
	}
}

func (a *pgActor) ncNote(kind, text string, err error) {
	a.mu.Lock()
	if err == nil {
		delete(a.ncAlarmAt, kind)
		a.mu.Unlock()
		return
	}
	now := a.now()
	if at, ok := a.ncAlarmAt[kind]; ok && now.Sub(at) < mirrorRepeat {
		a.mu.Unlock()
		return
	}
	if a.ncAlarmAt == nil {
		a.ncAlarmAt = map[string]time.Time{}
	}
	a.ncAlarmAt[kind] = now
	a.mu.Unlock()
	a.ev.add(ha.Event{Kind: "alarm", Text: text + err.Error()})
}

// mirrorStarted meldet den Ausgang des Spiegel-Starts nur bei Änderung oder
// spätestens nach mirrorRepeat erneut – ein dauerhaft abstürzendes pdh
// füllt sonst alle 30 s das Protokoll.
func (a *pgActor) mirrorStarted(err error) {
	n := mirrorNote{at: a.now()}
	if err != nil {
		n.err = err.Error()
	}
	a.mu.Lock()
	last := a.mirror
	quiet := last != nil && last.err == n.err && n.at.Sub(last.at) < mirrorRepeat
	if !quiet {
		a.mirror = &n
	}
	a.mu.Unlock()
	switch {
	case quiet:
	case err == nil:
		a.ev.add(ha.Event{Kind: "info", Text: "PDH als Spiegel gestartet"})
	default:
		a.ev.add(ha.Event{Kind: "alarm", Text: "PDH als Spiegel nicht gestartet: " + n.err})
	}
}

// mirrorReset: pdh bewusst gestoppt oder aktiv – der nächste Spiegel-Start
// wird wieder gemeldet.
func (a *pgActor) mirrorReset() {
	a.mu.Lock()
	a.mirror = nil
	a.mu.Unlock()
}

// disableAutostart: im Verbund startet pdh-ha pdh und Tunnel, nie systemd
// beim Hochfahren (dort ist PostgreSQL kurz schreibbar).
func (a *pgActor) disableAutostart(ctx context.Context) error {
	err := errors.Join(a.svc.SetAutostart(ctx, false), a.tun.SetAutostart(ctx, false))
	if a.nc != nil {
		a.ncNote("autostart", "Autostart von Nextcloud nicht abgeschaltet: ", a.nc.SetAutostart(ctx, false))
	}
	return err
}

// autostartChecker: Dienste, die melden können, ob ihr Autostart an ist.
type autostartChecker interface {
	AutostartOn(ctx context.Context) bool
}

// keepAutostartOff: Der Autostart bleibt im Verbund aus, auch wenn ihn etwas
// wieder einschaltet (z. B. das Tunnel-Skript aus dem PDH mit
// „systemctl enable --now cloudflared“) – sonst startete cloudflared beim
// Hochfahren auch am passiven Server.
func (a *pgActor) keepAutostartOff(ctx context.Context) {
	for _, s := range []struct {
		name string
		c    serviceCtl
	}{{"pdh", a.svc}, {"Tunnel", a.tun}, {"Nextcloud", a.nc}} {
		if s.c == nil {
			continue
		}
		ac, ok := s.c.(autostartChecker)
		if !ok || !ac.AutostartOn(ctx) {
			continue
		}
		if err := s.c.SetAutostart(ctx, false); err != nil {
			a.ev.add(ha.Event{Kind: "alarm", Text: "Autostart von " + s.name + " nicht abgeschaltet: " + err.Error()})
			continue
		}
		a.ev.add(ha.Event{Kind: "info", Text: "Autostart von " + s.name + " wieder abgeschaltet – im Verbund startet pdh-ha " + s.name + " nur am aktiven Server"})
	}
}

// fence: Tunnel und pdh aus, Datenbank nur lesend, offene Sitzungen beenden. Die
// Replikation (walsender) läuft weiter.
func (a *pgActor) fence(ctx context.Context) error {
	e := a.env()
	if err := a.stopServices(ctx); err != nil {
		return fmt.Errorf("pdh bzw. Tunnel nicht gestoppt: %w", err)
	}
	if st := a.pg.status(ctx); st.State != "primary" {
		return nil // Standby oder gestoppt: schreibt ohnehin nicht
	}
	if err := a.pg.writeConf(ctx, a.primaryConf(e, true)); err != nil {
		return err
	}
	if err := a.pg.ctl(ctx, "reload"); err != nil {
		return err
	}
	_, err := a.pg.query(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()")
	return err
}

// unfence: wieder schreibbar, Dienste an. Ist die Datenbank eingefroren
// (gestoppt, ohne standby.signal – etwa nach demoteFreeze, wenn die
// Übernahme des Partners scheitert), wird sie zuerst wieder gestartet (I2):
// sie ist der unveränderte eigene Stand, der Partner hat nicht übernommen.
func (a *pgActor) unfence(ctx context.Context) error {
	if a.rewindUnsafe() {
		return errDataUnsafe
	}
	e := a.env()
	st := a.pg.status(ctx)
	if st.State == "stopped" && a.frozenPrimary() {
		if err := a.pg.ctl(ctx, "start"); err != nil {
			return fmt.Errorf("Eingefrorene Datenbank nicht gestartet: %w", err)
		}
		st = a.pg.status(ctx)
	}
	if st.State != "primary" {
		return fmt.Errorf("Datenbank ist nicht schreibbar (%s) – Übernahme erzwingen nötig", st.State)
	}
	if !a.roleActive() {
		return unfenceRoleError{}
	}
	if err := a.pg.writeConf(ctx, a.primaryConf(e, false)); err != nil {
		return err
	}
	if !a.roleActive() {
		// noch nicht geladen, aber zurückschreiben: Der nächste reload darf es nicht aufnehmen
		return errors.Join(unfenceRoleError{}, a.fence(ctx))
	}
	if err := a.pg.ctl(ctx, "reload"); err != nil {
		return err
	}
	// R48: Vor jedem Dienst prüfen: Sieht der Takt inzwischen eine höhere Amtszeit, ist die Rolle
	// weg, und der Server schottet sich wieder ab.
	var err error
	for _, s := range []serviceCtl{a.svc, a.tun} {
		if !a.roleActive() {
			return errors.Join(unfenceRoleError{}, a.fence(ctx))
		}
		err = errors.Join(err, s.Start(ctx))
	}
	if !a.roleActive() {
		return errors.Join(unfenceRoleError{}, a.fence(ctx))
	}
	a.ncStart(ctx)
	return err
}

func (a *pgActor) roleActive() bool { return a.stillActive == nil || a.stillActive() }

// unfenceRoleError: Die Rolle ging während der Rücknahme verloren. Kein Fehlschlag mit
// Wartung, die Wiederanbindung folgt der höheren Amtszeit.
type unfenceRoleError struct{}

func (unfenceRoleError) Error() string {
	return "Rücknahme abgebrochen: dieser Server ist nicht mehr aktiv"
}
func (unfenceRoleError) noMaint() {}

// reconcile gleicht Dienste und Datenbank mit der Rolle ab – nur in die
// sichere Richtung: nie hochstufen, Tunnel nie auf einem nicht aktiven
// Knoten, pdh dort nur als Spiegel über einem Standby.
// Am aktiven Knoten mit schreibbarer Datenbank startet er pdh und Tunnel
// (nach einem Neustart bei Beobachtung gibt es kein unfence, das sie startet);
// systemctl start lässt laufende Einheiten unberührt.
func (a *pgActor) reconcile(ctx context.Context, role ha.Role) {
	if role == ha.Active {
		a.mirrorReset()
		if st := a.pg.status(ctx); st.State == "primary" && !st.ReadOnly {
			if err := a.svc.Start(ctx); err != nil {
				a.ev.add(ha.Event{Kind: "alarm", Text: "pdh auf dem aktiven Server nicht gestartet: " + err.Error()})
			}
			if err := a.tun.Start(ctx); err != nil {
				a.ev.add(ha.Event{Kind: "alarm", Text: "Tunnel auf dem aktiven Server nicht gestartet: " + err.Error()})
			}
			a.ncStart(ctx)
		}
		return
	}
	if a.tun.Active(ctx) {
		if err := a.tun.Stop(ctx); err == nil {
			a.ev.add(ha.Event{Kind: "info", Text: "Tunnel auf nicht aktivem Server gestoppt"})
		} else {
			a.ev.add(ha.Event{Kind: "alarm", Text: "Tunnel auf nicht aktivem Server nicht gestoppt: " + err.Error()})
		}
	}
	// Nextcloud läuft nur am aktiven Server; Active umfasst auch einen von einem
	// Installationsskript wieder angelegten Cron-Auftrag
	if a.nc != nil && a.nc.Active(ctx) {
		a.ncStop(ctx)
	}
	e := a.env()
	st := a.pg.status(ctx)
	if a.rewindUnsafe() {
		switch st.State {
		case "standby":
			a.clearRewindMarker() // als Standby hochgekommen: die Daten folgen dem Partner
		case "primary":
			// Nach einem Neustart des Rechners startet das System PostgreSQL von selbst.
			if err := a.pg.ctl(ctx, "stop"); err != nil {
				a.ev.add(ha.Event{Kind: "alarm", Text: "Datenbank mit unsicheren Daten nicht gestoppt: " + err.Error()})
			}
			st = a.pg.status(ctx)
		}
	}
	switch {
	case st.State == "standby":
		if !a.svc.Active(ctx) {
			a.mirrorStarted(a.svc.Start(ctx))
		}
	case st.State == "primary" && !st.ReadOnly:
		if err := a.fence(ctx); err == nil {
			a.ev.add(ha.Event{Kind: "alarm", Text: "Nicht aktiver Server mit schreibbarer Datenbank – abgeschaltet"})
		} else {
			a.ev.add(ha.Event{Kind: "alarm", Text: "Nicht aktiver Server mit schreibbarer Datenbank – Abschalten fehlgeschlagen: " + err.Error()})
		}
		return
	default: // abgeschottet, gestoppt, Fehler: kein Spiegel
		if a.svc.Active(ctx) {
			if err := a.svc.Stop(ctx); err == nil {
				a.mirrorReset()
				a.ev.add(ha.Event{Kind: "info", Text: "pdh auf nicht aktivem Server gestoppt"})
			} else {
				a.ev.add(ha.Event{Kind: "alarm", Text: "pdh auf nicht aktivem Server nicht gestoppt: " + err.Error()})
			}
		}
		return
	}
	if len(e.PartnerAddrs) == 0 {
		return
	}
	want := renderPGConf(a.standbyConf(e))
	name := filepath.Join(filepath.FromSlash(a.pg.c.ConfDir), "conf.d", "pdh-ha.conf")
	if cur, err := os.ReadFile(name); err == nil && string(cur) == want {
		return
	}
	if err := a.pg.writeConf(ctx, a.standbyConf(e)); err == nil {
		_ = a.pg.ctl(ctx, "reload")
	}
}

// recreateSlot legt einen ungültigen Slot neu an (mit sofort reserviertem
// WAL) – pg_basebackup -S der Neukopie braucht einen gültigen Slot.
func (a *pgActor) recreateSlot(ctx context.Context, slot string) {
	if _, err := a.pg.query(ctx, fmt.Sprintf(
		"SELECT pg_drop_replication_slot('%[1]s') FROM pg_replication_slots WHERE slot_name = '%[1]s';\n"+
			"SELECT pg_create_physical_replication_slot('%[1]s', true)", slot)); err != nil {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Slot " + slot + " nicht neu angelegt: " + err.Error()})
	}
}

// recopy: Der aktive Server hat das WAL für diesen Standby nicht mehr
// (Slot ungültig) – Datenbank neu von ihm kopieren und wieder Standby werden.
func (a *pgActor) recopy(ctx context.Context) {
	e := a.env()
	if st := a.pg.status(ctx); st.State != "standby" && st.State != "stopped" {
		return // eigene Daten (kein Standby): Sache der Wiedereingliederung
	}
	a.ev.add(ha.Event{Kind: "alarm", Text: "Slot beim aktiven Server ungültig – Datenbank wird neu vom aktiven Server kopiert"})
	if err := a.stopServices(ctx); err != nil {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Neukopie: pdh bzw. Tunnel nicht gestoppt: " + err.Error()})
		return
	}
	if err := a.pg.ctl(ctx, "stop"); err != nil && a.pg.status(ctx).State != "stopped" {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Neukopie: PostgreSQL nicht gestoppt: " + err.Error()})
		return
	}
	err := a.fullCopy(ctx, e)
	if err == nil {
		err = a.becomeStandby(ctx, e)
	}
	if err != nil {
		a.ev.add(ha.Event{Kind: "alarm", Text: err.Error()})
	}
	if a.rewindUnsafe() {
		return // zurückgeholter Ordner aus einem abgebrochenen pg_rewind: bleibt gestoppt (R35)
	}
	if serr := a.pg.ctl(ctx, "start"); serr != nil {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Neukopie: PostgreSQL startet nicht: " + serr.Error()})
	}
}

// hostOfAddr: "10.0.0.1:7480" → "10.0.0.1".
func hostOfAddr(a string) string {
	if h, _, err := net.SplitHostPort(a); err == nil {
		return h
	}
	return a
}

// finalSyncError: der letzte Dateiabgleich vor der Übergabe ist gescheitert.
// Wie swapWithdrawnError setzt er keine Wartung (R29, R31.4).
type finalSyncError struct{ err error }

func (e *finalSyncError) Error() string {
	return "Letzter Abgleich der Dateien nicht fertig: " + e.err.Error()
}
func (e *finalSyncError) Unwrap() error { return e.err }
func (e *finalSyncError) noMaint()      {}

// swapTakenError: der Partner hat während der Abgabe übernommen; die Datenbank bleibt nur
// lesend. rejoin: die Marke ist gesetzt, die Wiederanbindung folgt von selbst (nur demote).
type swapTakenError struct{ rejoin bool }

func (e swapTakenError) Error() string {
	if e.rejoin {
		return "Der Partner hat inzwischen übernommen – nur lesend, Wiederanbindung folgt"
	}
	return "Der Partner hat inzwischen übernommen – nur lesend"
}
func (swapTakenError) noMaint() {}

// swapWithdrawnError: der Tausch wurde vor dem Stoppen zurückgenommen.
type swapWithdrawnError struct{}

func (swapWithdrawnError) Error() string { return "Rollentausch wurde inzwischen abgebrochen" }
func (swapWithdrawnError) noMaint()      {}
