package main

// pgActor führt die Aktionen des Entscheidungskerns an PostgreSQL und am
// Dienst pdh wirklich aus (Teil 3). Virtuelle IP bleibt Beobachtung (Teil 5).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	DataCurrent               bool // für promote ohne Handbefehl
}

type pgActor struct {
	pg       *pgCtl
	svc      serviceCtl
	ev       *eventLog
	env      func() pgEnv
	pushCfg  func(ctx context.Context) error // Konfiguration an den Partner (nil = keine)
	wait     time.Duration                   // Frist für den Gleichstand beim Abgeben
	poll     time.Duration
	now      func() time.Time
	passfile string // Pfad der passfile (Tests: Temp-Ordner)

	mu        sync.Mutex
	syncNames string // gewünschte synchronous_standby_names (Replikation, Task 8)
}

func newPGActor(pg *pgCtl, svc serviceCtl, ev *eventLog, env func() pgEnv) *pgActor {
	return &pgActor{pg: pg, svc: svc, ev: ev, env: env, wait: 60 * time.Second, poll: 500 * time.Millisecond, now: time.Now, passfile: pgPassfile}
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
		return a.demote(ctx)
	case ha.ActFence:
		return a.fence(ctx)
	case ha.ActUnfence:
		return a.unfence(ctx)
	case ha.ActRejoin:
		return a.rejoin(ctx, act.Term)
	}
	a.ev.add(ha.Event{Kind: "action", Text: fmt.Sprintf("würde jetzt: %s (Amtszeit %d)", act.Kind, act.Term)})
	return nil
}

// demote: Rollentausch am aktiven Knoten. Erst nur lesend (auch andere
// Programme im selben PostgreSQL schreiben dann nicht mehr), dann auf den
// Gleichstand warten – bis dahin bleibt alles umkehrbar. Erst danach wird
// PostgreSQL gestoppt und Standby des Partners.
func (a *pgActor) demote(ctx context.Context) error {
	e := a.env()
	if a.pushCfg != nil {
		if err := a.pushCfg(ctx); err != nil {
			return fmt.Errorf("Konfiguration nicht übertragen: %w", err)
		}
	}
	if err := a.fence(ctx); err != nil {
		return err
	}
	if err := a.waitCaughtUp(ctx, slotName(e.PartnerID)); err != nil {
		if uerr := a.unfence(ctx); uerr != nil {
			err = fmt.Errorf("%w; Rücknahme fehlgeschlagen: %v", err, uerr)
		}
		return err
	}
	if err := a.pg.ctl(ctx, "stop"); err != nil {
		return err
	}
	if err := a.becomeStandby(ctx, e); err != nil {
		return err
	}
	return a.pg.ctl(ctx, "start")
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

// becomeStandby: standby.signal und Konfiguration für den Partner als Quelle.
func (a *pgActor) becomeStandby(ctx context.Context, e pgEnv) error {
	if err := a.pg.writeOwned(ctx, filepath.Join(filepath.FromSlash(a.pg.c.DataDir), "standby.signal"), nil); err != nil {
		return err
	}
	return a.pg.writeConf(ctx, a.standbyConf(e))
}

func (a *pgActor) promote(ctx context.Context, act ha.Action) error {
	e := a.env()
	if st := a.pg.status(ctx); st.State != "standby" {
		return errors.New("Datenbank ist kein Standby")
	}
	if !act.Manual && !e.DataCurrent {
		return errors.New("Datenstand nicht aktuell")
	}
	if out, err := a.pg.query(ctx, "SELECT pg_promote(true, 60)"); err != nil || out != "t" {
		return fmt.Errorf("pg_promote fehlgeschlagen: %v %s", err, out)
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
	return a.svc.Start(ctx)
}

// fence: pdh aus, Datenbank nur lesend, offene Sitzungen beenden. Die
// Replikation (walsender) läuft weiter.
func (a *pgActor) fence(ctx context.Context) error {
	e := a.env()
	if err := a.svc.Stop(ctx); err != nil {
		return fmt.Errorf("pdh nicht gestoppt: %w", err)
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

func (a *pgActor) unfence(ctx context.Context) error {
	e := a.env()
	if st := a.pg.status(ctx); st.State != "primary" {
		return fmt.Errorf("Datenbank ist nicht schreibbar (%s) – Übernahme erzwingen nötig", st.State)
	}
	if err := a.pg.writeConf(ctx, a.primaryConf(e, false)); err != nil {
		return err
	}
	if err := a.pg.ctl(ctx, "reload"); err != nil {
		return err
	}
	return a.svc.Start(ctx)
}

// reconcile gleicht Dienst und Datenbank mit der Rolle ab – nur in die
// sichere Richtung: nie hochstufen, nie pdh auf einem nicht aktiven Knoten.
// Am aktiven Knoten mit schreibbarer Datenbank startet er pdh und Tunnel
// (nach einem Neustart bei Beobachtung gibt es kein unfence, das sie startet);
// systemctl start lässt laufende Einheiten unberührt.
func (a *pgActor) reconcile(ctx context.Context, role ha.Role) {
	if role == ha.Active {
		if st := a.pg.status(ctx); st.State == "primary" && !st.ReadOnly {
			if err := a.svc.Start(ctx); err != nil {
				a.ev.add(ha.Event{Kind: "alarm", Text: "pdh bzw. Tunnel auf dem aktiven Server nicht gestartet: " + err.Error()})
			}
		}
		return
	}
	if a.svc.Active(ctx) {
		if err := a.svc.Stop(ctx); err == nil {
			a.ev.add(ha.Event{Kind: "info", Text: "pdh auf nicht aktivem Server gestoppt"})
		} else {
			a.ev.add(ha.Event{Kind: "alarm", Text: "pdh bzw. Tunnel auf nicht aktivem Server nicht gestoppt: " + err.Error()})
		}
	}
	e := a.env()
	switch st := a.pg.status(ctx); st.State {
	case "primary":
		if !st.ReadOnly {
			if err := a.fence(ctx); err == nil {
				a.ev.add(ha.Event{Kind: "alarm", Text: "Nicht aktiver Server mit schreibbarer Datenbank – abgeschaltet"})
			} else {
				a.ev.add(ha.Event{Kind: "alarm", Text: "Nicht aktiver Server mit schreibbarer Datenbank – Abschalten fehlgeschlagen: " + err.Error()})
			}
		}
	case "standby":
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
	if err := a.svc.Stop(ctx); err != nil {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Neukopie: pdh nicht gestoppt: " + err.Error()})
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
