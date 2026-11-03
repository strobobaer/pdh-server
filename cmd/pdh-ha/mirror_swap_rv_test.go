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

const stRO = "f|0/100|1|on|pdh_ha_b|"

func dryRuns(r *fakeRunner) int {
	n := 0
	for _, c := range r.list() {
		if strings.Contains(c, "pg_rewind") && strings.Contains(c, "--dry-run") {
			n++
		}
	}
	return n
}

// passiver Server, Datenbank laeuft als nur lesender Primary
func passiveRO(t *testing.T, mutate func(cl *clusterConfig), partner message, marked bool) (*mdt, *fakeRunner) {
	cl := duoCluster()
	if mutate != nil {
		mutate(&cl)
	}
	fn := &stopNet{fakeNet: newFakeNet()}
	partner.Ver, partner.Sees = "test", []string{"a"}
	fn.setView("b", seenView(partner))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: ha.Passive, Term: 2, PartnerVersion: "test"}, fn.fakeNet)
	d.net = fn
	r := &fakeRunner{}
	r.on("runuser", "pg_is_in_recovery", stRO, nil)
	a, _ := testActor(t, r)
	d.wireActor(a)
	d.act = a
	d.gwFn = func() (string, error) { return "10.0.0.254", nil }
	m := &mdt{t: t, d: d, fn: fn, ctx: t.Context()}
	d.undoRefused = marked
	return m, r
}

func settle(m *mdt) {
	for i := 0; i < 4; i++ {
		m.tick()
		time.Sleep(100 * time.Millisecond)
	}
	waitFor(m.t, 5*time.Second, "idle", m.d.actionsIdle)
}

// P1: Rolling-Update, Sperre, A eingefroren (Rueckfallstand); Neustart des Rechners
// startet PostgreSQL als nur lesenden Primary (Konfiguration aus dem Abschotten).
func TestRV_FrozenFallbackDuringLock(t *testing.T) {
	m, r := passiveRO(t, nil, message{Role: "aktiv", Term: 2, PG: "primary", Sync: false}, false)
	now := time.Now()
	m.d.lock()
	m.d.up.job = ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, First: "b", Second: "a", Target: "x", Phase: ha.UpVerifyNew, PhaseAt: now, StartedAt: now, LockedAt: now, FrozenSynced: true}
	locked := m.d.up.job.Locked()
	m.d.unlock()
	settle(m)
	if !locked {
		t.Fatal("Auftrag nicht gesperrt")
	}
	if dryRuns(r) > 0 {
		t.Errorf("RUECKFALLSTAND ANGEFASST: pg_rewind waehrend der Sperre; %v", r.list())
	}
}

// P2: Partner Rolle aktiv, seine Datenbank noch Standby (Hochstufen laeuft) bzw. gestoppt.
func TestRV_PartnerNotYetPrimary(t *testing.T) {
	for _, pg := range []string{"standby", "stopped"} {
		t.Run(pg, func(t *testing.T) {
			m, r := passiveRO(t, nil, message{Role: "aktiv", Term: 2, PG: pg}, true)
			settle(m)
			if dryRuns(r) > 0 {
				t.Errorf("pg_rewind gegen eine Quelle, die noch nicht Primary ist")
			}
		})
	}
}

// P3: Beobachtung.
func TestRV_Observe(t *testing.T) {
	m, r := passiveRO(t, func(cl *clusterConfig) { cl.Settings.AutoMode = ha.AutoObserve }, message{Role: "aktiv", Term: 2, PG: "primary"}, false)
	settle(m)
	m.d.lock()
	real := m.d.settingsLocked().Real()
	m.d.unlock()
	if real || dryRuns(r) > 0 {
		t.Errorf("Beobachtung ohne Marke: Real=%v, pg_rewind läuft (%d)", real, dryRuns(r))
	}
}

// P3b: Wartung (nur Feststellung, was passiert).
func TestRV_Maint(t *testing.T) {
	m, r := passiveRO(t, nil, message{Role: "aktiv", Term: 2, PG: "primary", Maint: true, MaintSeq: 7}, false)
	settle(m)
	if dryRuns(r) > 0 {
		t.Errorf("Wartung ohne Marke: pg_rewind läuft")
	}
}

// P5: normales Rolling-Update (swap_freeze): Die Statusabfrage nach dem Stopp braucht laenger als
// einen Takt, der letzte bekannte Stand ist noch der nur lesende Primary von vor dem Stopp.
func TestRV_StaleStatusAfterFreeze(t *testing.T) {
	m := newMDT(t, ha.Active, false, message{Role: "passiv"})
	var stopped atomic.Bool
	gateStop, gateStatus := make(chan struct{}), make(chan struct{})
	r := &fakeRunner{}
	a, _ := testActor(t, r)
	r.onFn("runuser", "pg_is_in_recovery", func(c fakeCall) (string, error) {
		if stopped.Load() {
			<-gateStatus
			return "", errors.New("connection refused")
		}
		b, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "conf.d", "pdh-ha.conf"))
		if strings.Contains(string(b), "default_transaction_read_only = on") {
			return stRO, nil
		}
		return stPrimary, nil
	})
	r.on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/100|0/100|sync", nil)
	r.onFn("pg_ctlcluster 18 main stop", "", func(fakeCall) (string, error) {
		<-gateStop
		stopped.Store(true)
		return "", nil
	})
	r.on("pg_lsclusters", "", "18 main 5432 down postgres /d /l", nil)
	m.d.wireActor(a)
	m.d.act = a
	m.d.gwFn = func() (string, error) { return "10.0.0.254", nil }
	m.tick()
	time.Sleep(100 * time.Millisecond)
	m.d.lock()
	m.d.cmd = ha.CmdSwapFreeze
	m.d.unlock()
	m.tick()
	waitFor(t, 5*time.Second, "lesend bekannt", func() bool {
		m.tick()
		time.Sleep(20 * time.Millisecond)
		st := m.d.lastPG()
		return st != nil && st.ReadOnly
	})
	close(gateStop)
	waitFor(t, 5*time.Second, "demote fertig", func() bool { return m.d.pendingDemotes.Load() == 0 })
	m.tick() // DemoteDone -> Uebergabe gesendet; startet die haengende Statusabfrage
	time.Sleep(50 * time.Millisecond)
	m.fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", Sees: []string{"a"}, PG: "primary", Sync: true}))
	m.tick() // ein Takt spaeter: Partner hat uebernommen
	st := m.d.lastPG()
	t.Logf("letzter bekannter Stand: %+v", *st)
	close(gateStatus)
	time.Sleep(200 * time.Millisecond)
	waitFor(t, 5*time.Second, "idle", m.d.actionsIdle)
	alarm := false
	for _, s := range eventTexts(m.d) {
		if strings.Contains(s, "nur lesender Datenbank") {
			alarm = true
		}
	}
	t.Logf("Alarm=%v Probelauf=%d", alarm, dryRuns(r))
	if dryRuns(r) > 0 {
		t.Errorf("EINGEFRORENER STAND ANGEFASST: pg_rewind nach normalem swap_freeze")
	}
}

// Mit Marke und offenem Update-Auftrag (Rückfallstand) bleibt der Server unberührt.
func TestRV_MarkAndOpenUpdate(t *testing.T) {
	m, r := passiveRO(t, nil, message{Role: "aktiv", Term: 2, PG: "primary"}, true)
	now := time.Now()
	m.d.lock()
	m.d.up.job = ha.UpdateJob{ID: "j1", Kind: ha.UpdateRolling, First: "b", Second: "a", Target: "x", Phase: ha.UpVerifyNew, PhaseAt: now, StartedAt: now, LockedAt: now, FrozenSynced: true}
	m.d.unlock()
	settle(m)
	if dryRuns(r) > 0 {
		t.Errorf("Rückfallstand angefasst: %v", r.list())
	}
}

// Marke gesetzt, Partner aktiv und Primary: genau eine Wiederanbindung, auch in Beobachtung
// und in Wartung; die Marke fällt danach weg (kein zweiter Versuch).
func TestRV_MarkRejoinsOnce(t *testing.T) {
	for name, c := range map[string]struct {
		mut func(cl *clusterConfig)
		msg message
	}{
		"normal":      {nil, message{Role: "aktiv", Term: 2, PG: "primary"}},
		"Beobachtung": {func(cl *clusterConfig) { cl.Settings.AutoMode = ha.AutoObserve }, message{Role: "aktiv", Term: 2, PG: "primary"}},
		"Wartung":     {nil, message{Role: "aktiv", Term: 2, PG: "primary", Maint: true, MaintSeq: 7}},
	} {
		t.Run(name, func(t *testing.T) {
			m, r := passiveRO(t, c.mut, c.msg, true)
			settle(m)
			settle(m)
			if n := dryRuns(r); n != 1 {
				t.Fatalf("%d Wiederanbindungen, erwartet genau 1", n)
			}
		})
	}
}

// Der Aktor prüft frisch: ist die Datenbank kein nur lesender Primary, geschieht nichts.
func TestRejoinStrandedChecksFreshStatus(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil) // schreibbar
	a, _ := testActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActRejoinStranded, Term: 2, Manual: true}); err != nil {
		t.Fatal(err)
	}
	if has(r.list(), "svc stop") || strings.Contains(strings.Join(r.list(), "\n"), "pg_rewind") {
		t.Fatalf("nichts darf geschehen: %v", r.list())
	}
}
