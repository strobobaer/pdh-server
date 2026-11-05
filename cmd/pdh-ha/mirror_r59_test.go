package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

// --- R59: gescheitertes ActUnfence wird wiederholt; Adresse und Skripte folgen erst in Takt N+1 ---

// unfenceFailRig: Tausch scheitert, Takt N nimmt zurück (Rolle aktiv, Merker); die Datenbank
// antwortet nicht, solange fail gesetzt ist.
func unfenceFailRig(t *testing.T) (*mdt, *pgActor, *svcLog, *atomic.Bool) {
	t.Helper()
	m, a, svc, r, inFinal := stuckRig(t)
	var fail atomic.Bool
	r.onFn("runuser", "pg_is_in_recovery", func(c fakeCall) (string, error) {
		if fail.Load() {
			return "", errors.New("Verbindung abgelehnt")
		}
		if !strings.Contains(c.stdin, "pg_last_wal") {
			return "f", nil
		}
		b, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "conf.d", "pdh-ha.conf"))
		if strings.Contains(string(b), "default_transaction_read_only = on") {
			return "f|0/100|1|on|pdh_ha_b|", nil
		}
		return stPrimary, nil
	})
	release := make(chan struct{})
	m.rs.mu.Lock()
	m.rs.fn = func(ctx context.Context, _ []string) error {
		inFinal <- struct{}{}
		<-release
		return errors.New("rsync 23")
	}
	m.rs.mu.Unlock()
	m.noWatchCover()
	if err := m.d.command("swap"); err != nil {
		t.Fatal(err)
	}
	m.tick()
	select {
	case <-inFinal:
	case <-time.After(10 * time.Second):
		t.Fatal("letzter Abgleich nicht gestartet")
	}
	m.d.lock()
	m.d.carry.HandoverAt -= time.Hour
	m.d.unlock()
	m.tick() // Takt N: Rolle aktiv, Merker
	close(release)
	waitFor(t, 10*time.Second, "demote zu Ende", m.d.actionsIdle)
	return m, a, svc, &fail
}

func pendingOf(m *mdt) (ha.Role, bool) {
	m.d.lock()
	defer m.d.unlock()
	return m.d.p.Role, m.d.p.UnfencePending
}

// T1: In Takt N+1 antwortet die Datenbank einmal nicht. Der Merker bleibt, der nächste Takt
// wiederholt, danach ist der Server schreibbar mit Diensten; der Grund wird einmal gemeldet.
func TestUnfenceFailsOnceThenRetried(t *testing.T) {
	m, a, svc, fail := unfenceFailRig(t)
	fail.Store(true)
	m.tick() // Takt N+1: ActUnfence scheitert
	waitFor(t, 10*time.Second, "unfence", m.d.actionsIdle)
	if role, pending := pendingOf(m); role != ha.Active || !pending {
		t.Fatalf("nach gescheitertem ActUnfence: Rolle %s, Merker %v (muss stehen bleiben)", role, pending)
	}
	if svc.active || !strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatal("trotz Fehler eingeschaltet")
	}
	m.tick() // wiederholt, scheitert noch einmal mit demselben Grund
	waitFor(t, 10*time.Second, "unfence 2", m.d.actionsIdle)
	fail.Store(false)
	m.tick() // Datenbank wieder gesund: Rücknahme gelingt
	waitFor(t, 10*time.Second, "unfence 3", m.d.actionsIdle)
	if !svc.active || !tunOf(a).active || strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatalf("nach der Wiederholung: svc=%v tun=%v", svc.active, tunOf(a).active)
	}
	if role, pending := pendingOf(m); role != ha.Active || pending {
		t.Fatalf("nach Erfolg: Rolle %s, Merker %v", role, pending)
	}
	n := 0
	for _, s := range eventTexts(m.d) {
		if strings.Contains(s, "Aktion unfence fehlgeschlagen") {
			n++
			if strings.Contains(s, "Übernahme erzwingen") {
				t.Fatalf("Alarm nennt einen Ausweg, den es nicht gibt: %s", s)
			}
		}
	}
	if n != 1 {
		t.Fatalf("Alarm %d-mal: %v", n, eventTexts(m.d))
	}
}

// Höhere Amtszeit zwischen den Wiederholungen: keine weitere Rücknahme, der Merker fällt.
func TestUnfenceRetryStopsAtHigherTerm(t *testing.T) {
	m, a, svc, fail := unfenceFailRig(t)
	fail.Store(true)
	m.tick()
	waitFor(t, 10*time.Second, "unfence", m.d.actionsIdle)
	fail.Store(false)
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}}))
	m.tick()
	waitFor(t, 10*time.Second, "Takt", m.d.actionsIdle)
	if role, pending := pendingOf(m); role == ha.Active || pending {
		t.Fatalf("höhere Amtszeit: Rolle %s, Merker %v", role, pending)
	}
	if svc.active || tunOf(a).active {
		t.Fatal("eingeschaltet trotz höherer Amtszeit")
	}
}

// Nur ein Fehler an der Rolle (unfenceRoleError) wird nicht wiederholt.
func TestRetryUnfenceDecision(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	m.tick() // Takt N+1 nach dem Start: der Merker des Starts ist weg
	waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	a := ha.Action{Kind: ha.ActUnfence, Term: 1}
	if m.d.retryUnfence(a, unfenceRoleError{}) {
		t.Fatal("Rollenfehler wiederholt")
	}
	if _, pending := pendingOf(m); pending {
		t.Fatal("Merker nach Rollenfehler gesetzt")
	}
	if m.d.retryUnfence(a, errors.New("Datenbank ist nicht schreibbar (stopped)")) {
		t.Fatal("erster Fehler gilt als Wiederholung")
	}
	if _, pending := pendingOf(m); !pending {
		t.Fatal("Merker nach Datenbankfehler nicht gesetzt")
	}
	if !m.d.retryUnfence(a, errors.New("Datenbank ist nicht schreibbar (stopped)")) {
		t.Fatal("derselbe Grund gilt nicht als Wiederholung")
	}
}

// T2: Die Adresse folgt erst in Takt N+1, nicht schon mit der Rolle in Takt N.
func TestVIPWaitsForSecondTick(t *testing.T) {
	d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24"))
	d.lock()
	d.p.Role = ha.Fenced
	d.p.PartnerVersion = "test"
	d.unlock()
	ctx := stopAccess(t, d)
	d.tick(ctx) // Takt N
	d.mu.Lock()
	role, pending := d.p.Role, d.p.UnfencePending
	d.mu.Unlock()
	if role != ha.Active || !pending {
		t.Fatalf("Takt N: Rolle %s, Merker %v", role, pending)
	}
	time.Sleep(300 * time.Millisecond)
	if n := f.count(vipAdd); n != 0 {
		t.Fatalf("Adresse schon in Takt N gesetzt (%d)", n)
	}
	d.tick(ctx) // Takt N+1
	waitFor(t, 2*time.Second, "Adresse übernommen", func() bool { return f.count(vipAdd) == 1 })
}

// T2: Die Rollenskripte laufen für „aktiv“ erst in Takt N+1 (hookRole liefert in Takt N keine Rolle).
func TestHookRoleWaitsForSecondTick(t *testing.T) {
	if hookRole(ha.Active, true) != "" {
		t.Fatal("aktiv mit Merker gilt für die Skripte schon als aktiv")
	}
	if hookRole(ha.Active, false) != ha.Active || hookRole(ha.Passive, false) != ha.Passive || hookRole(ha.Fenced, false) != ha.Fenced {
		t.Fatal("Rolle ohne Merker verändert")
	}
}

// Daemon: Takt N startet kein Skript, Takt N+1 genau einmal „aktiv“ (Skripte brauchen sh: nur Linux).
func TestHooksWaitForSecondTick(t *testing.T) {
	skipWindows(t)
	d, out := hookDaemon(t, "node", true)
	d.lock()
	d.p.Role = ha.Fenced
	d.unlock()
	ctx := context.Background()
	d.tick(ctx) // Takt N (zugleich erster Takt nach dem Start): Rolle aktiv mit Merker, kein Skript
	waitIdle(t, d.hooks)
	if got := hookRuns(out); got != "" {
		t.Fatalf("Skript in Takt N gelaufen: %q", got)
	}
	d.tick(ctx) // Takt N+1
	waitIdle(t, d.hooks)
	if got := hookRuns(out); got != "aktiv\n" {
		t.Fatalf("Läufe: %q", got)
	}
}
