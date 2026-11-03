package main

import (
	"context"
	"testing"
	"time"

	"pdh/internal/ha"
)

// blockRsync: rsync hängt, bis der Lauf abgebrochen wird – der Erstabgleich bleibt offen.
func blockRsync(m *mdt) {
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error { <-ctx.Done(); return ctx.Err() }
	m.rs.mu.Unlock()
}

const firstSyncMsg = "Erstabgleich der Dateien läuft – Rollentausch erst danach möglich"

// Im Erstabgleich ist der Handtausch gesperrt; Übernahme erzwingen nie.
func TestSwapRefusedDuringFirstSync(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	blockRsync(m)
	m.tick()
	m.ready()
	if err := m.d.command("swap"); err == nil || err.Error() != firstSyncMsg {
		t.Fatalf("swap im Erstabgleich: %v", err)
	}
	m.d.lock()
	cmd := m.d.cmd
	m.d.unlock()
	if cmd != ha.CmdNone {
		t.Fatalf("Befehl trotz Sperre vorgemerkt: %v", cmd)
	}

	// Übernahme erzwingen am nicht aktiven Server bleibt als Notweg offen.
	p := newMDT(t, ha.Passive, true, message{Role: "passiv"})
	blockRsync(p)
	p.tick()
	p.ready()
	if err := p.d.command("force_takeover"); err != nil {
		t.Fatalf("force_takeover gesperrt: %v", err)
	}
}

// Ist der Erstabgleich fertig, geht der Tausch; bei ausgeschalteter Spiegelung auch.
func TestSwapAllowedAfterFirstSyncOrMirrorOff(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Erstabgleich fertig", func() bool { return rt.worker.Load().snapshot().FirstDone })
	if err := m.d.command("swap"); err != nil {
		t.Fatalf("swap nach dem Erstabgleich: %v", err)
	}

	off := newMDT(t, ha.Active, false, message{Role: "passiv"})
	off.tick()
	if err := off.d.command("swap"); err != nil {
		t.Fatalf("swap ohne Spiegelung: %v", err)
	}
}

// „Alles grün“ bekommt die Spiegelung aus dem Zwischengespeicherten.
func TestReadyObsMirror(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	blockRsync(m)
	m.tick()
	m.ready()
	obs := func(d *daemon) ha.ReadyObs {
		d.lock()
		defer d.unlock()
		partner, witness := d.peerIDsLocked()
		o, _ := d.readyObsLocked(partner, witness, updateStartReq{})
		return o
	}
	if o := obs(m.d); !o.MirrorOn || o.MirrorFirstDone {
		t.Fatalf("im Erstabgleich: On=%v FirstDone=%v", o.MirrorOn, o.MirrorFirstDone)
	}
	m.setOn(false)
	if o := obs(m.d); o.MirrorOn {
		t.Fatalf("Spiegelung aus, aber On=%v", o.MirrorOn)
	}
}

// Beim Rollentausch ist die Rolle sofort passiv; der Arbeiter muss für den
// letzten Abgleich aber bis zur Übernahme des Partners aktiv bleiben.
func TestMirrorWorkerStaysActiveDuringHandover(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.d.act = &recActor{}
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Erstabgleich fertig", func() bool { return rt.worker.Load().snapshot().FirstDone })
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	waitFor(t, 2*time.Second, "demote erledigt", m.d.actionsIdle)
	m.tick()
	m.d.lock()
	role, pending := m.d.p.Role, m.d.carry.HandoverPending
	m.d.unlock()
	if role != ha.Passive || pending == 0 {
		t.Fatalf("Übergabe läuft nicht: Rolle %s, offen %d", role, pending)
	}
	wk := rt.worker.Load()
	wk.mu.Lock()
	active := wk.active()
	wk.mu.Unlock()
	if !active {
		t.Fatal("Arbeiter während der Übergabe nicht aktiv: der letzte Abgleich bräche ab")
	}
}

// mirrorFinalSync liest die Einstellungen erst beim Aufruf: aus = nichts zu tun,
// später eingeschaltet = es wird abgeglichen.
func TestMirrorFinalSyncReadsSettingsAtCall(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	m.tick()
	if err := m.d.mirrorFinalSync(context.Background()); err != nil {
		t.Fatalf("Spiegelung aus: %v", err)
	}
	m.setOn(true)
	m.tick()
	rt := m.ready()
	waitFor(t, 3*time.Second, "Erstabgleich fertig", func() bool { return rt.worker.Load().snapshot().FirstDone })
	if err := m.d.mirrorFinalSync(context.Background()); err != nil {
		t.Fatalf("letzter Abgleich: %v", err)
	}
}
