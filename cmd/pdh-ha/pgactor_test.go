package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// svcLog: Dienst-Aufrufe landen als "svc <op>" im fakeRunner (eine Reihenfolge).
type svcLog struct {
	r      *fakeRunner
	active bool
}

func (s *svcLog) log(op string)                            { _, _ = s.r.Run(context.Background(), []string{"svc", op}, nil, nil) }
func (s *svcLog) Start(context.Context) error              { s.log("start"); s.active = true; return nil }
func (s *svcLog) Stop(context.Context) error               { s.log("stop"); s.active = false; return nil }
func (s *svcLog) Active(context.Context) bool              { return s.active }
func (s *svcLog) SetAutostart(context.Context, bool) error { return nil }

const (
	stPrimary = "f|0/100|1|off|pdh_ha_b|"
	stStandby = "t|0/100|1|off||streaming"
)

func testActor(t *testing.T, r *fakeRunner) (*pgActor, *svcLog) {
	t.Helper()
	pg := testPG(r)
	pg.c.DataDir, pg.c.ConfDir = t.TempDir(), t.TempDir()
	svc := &svcLog{r: r, active: true}
	ev := openEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 5<<20, 3)
	a := newPGActor(pg, svc, ev, func() pgEnv {
		return pgEnv{SelfID: "a", PartnerID: "b", PartnerAddrs: []string{"10.0.0.2"}, CAFile: "/ca.crt",
			Listen: []string{"10.0.0.1"}, SlotKeepMB: 20480, DataCurrent: true}
	})
	a.wait, a.poll = 100*time.Millisecond, 10*time.Millisecond
	return a, svc
}

// seq prüft, dass want in dieser Reihenfolge in got vorkommt (Präfixe).
func seq(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && strings.HasPrefix(g, want[i]) {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("Reihenfolge: fehlt ab %q\nbekommen:\n  %s", want[i], strings.Join(got, "\n  "))
	}
}

func has(got []string, prefix string) bool {
	for _, g := range got {
		if strings.HasPrefix(g, prefix) {
			return true
		}
	}
	return false
}

func confText(t *testing.T, a *pgActor) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "conf.d", "pdh-ha.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDemoteOrder(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "pg_is_in_recovery", stPrimary, nil).
		on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/100|0/100|sync", nil)
	a, _ := testActor(t, r)
	a.pushCfg = func(context.Context) error {
		_, _ = r.Run(context.Background(), []string{"push"}, nil, nil)
		return nil
	}
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActDemote, Term: 3, Manual: true}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "push", "svc stop", "chown", "pg_ctlcluster 18 main reload", "psql: SELECT pg_terminate_backend",
		"pg_ctlcluster 18 main stop -m fast", "chown", "chown", "pg_ctlcluster 18 main start")
	if _, err := os.Stat(filepath.Join(a.pg.c.DataDir, "standby.signal")); err != nil {
		t.Fatal("standby.signal fehlt")
	}
	c := confText(t, a)
	if !strings.Contains(c, "primary_conninfo = 'host=10.0.0.2 port=5432 user=pdh_replica application_name=pdh_ha_a") ||
		!strings.Contains(c, "primary_slot_name = 'pdh_ha_a'") || strings.Contains(c, "password") {
		t.Fatalf("Standby-Konfiguration:\n%s", c)
	}
}

func TestDemotePushFailsKeepsAll(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	a.pushCfg = func(context.Context) error { return errors.New("Partner weg") }
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActDemote}); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if l := r.list(); len(l) != 0 {
		t.Fatalf("nichts darf passieren: %v", l)
	}
}

func TestDemoteTimeoutRestartsPDH(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "pg_is_in_recovery", stPrimary, nil).
		on("runuser", "pg_stat_replication", "pdh_ha_b|streaming|0/100|0/80|sync", nil)
	a, svc := testActor(t, r)
	err := a.Do(context.Background(), ha.Action{Kind: ha.ActDemote})
	if err == nil || !strings.Contains(err.Error(), "Partner hat nicht aufgeholt") {
		t.Fatalf("Frist: %v", err)
	}
	if has(r.list(), "pg_ctlcluster 18 main stop") || !svc.active {
		t.Fatalf("nach Frist nichts gestoppt, pdh wieder an: %v", r.list())
	}
	if strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatal("nur-lesend nicht zurückgenommen")
	}
}

func TestPromoteOrder(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "(SELECT timeline_id FROM pg_control_checkpoint())", stStandby, nil).
		on("runuser", "SELECT pg_promote", "t", nil).
		on("runuser", "SELECT pg_is_in_recovery()", "f", nil)
	a, svc := testActor(t, r)
	svc.active = false
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 4}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "psql: SELECT pg_promote(true, 60)", "psql: SELECT pg_is_in_recovery()",
		"psql: SELECT pg_create_physical_replication_slot('pdh_ha_b')", "chown", "pg_ctlcluster 18 main reload", "svc start")
	if c := confText(t, a); !strings.Contains(c, "synchronous_standby_names = 'pdh_ha_b'") || strings.Contains(c, "primary_conninfo") {
		t.Fatalf("Konfiguration:\n%s", c)
	}
}

func TestPromoteRefusesPrimary(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Manual: true}); err == nil || err.Error() != "Datenbank ist kein Standby" {
		t.Fatalf("%v", err)
	}
	if has(r.list(), "psql: SELECT pg_promote") {
		t.Fatal("darf nicht hochstufen")
	}
}

func TestPromoteRefusesStaleUnlessManual(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "(SELECT timeline_id FROM pg_control_checkpoint())", stStandby, nil).
		on("runuser", "SELECT pg_promote", "t", nil).
		on("runuser", "SELECT pg_is_in_recovery()", "f", nil)
	a, _ := testActor(t, r)
	env := a.env
	a.env = func() pgEnv { e := env(); e.DataCurrent = false; return e }
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActPromote}); err == nil || err.Error() != "Datenstand nicht aktuell" {
		t.Fatalf("automatisch: %v", err)
	}
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Manual: true}); err != nil {
		t.Fatalf("erzwungen: %v", err)
	}
}

func TestFenceOrder(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, svc := testActor(t, r)
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActFence}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "svc stop", "chown", "pg_ctlcluster 18 main reload", "psql: SELECT pg_terminate_backend")
	if svc.active || !strings.Contains(confText(t, a), "default_transaction_read_only = on") {
		t.Fatal("nicht abgeschaltet")
	}
	for _, c := range r.calls {
		if strings.Contains(c.stdin, "pg_terminate_backend") && !strings.Contains(c.stdin, "backend_type = 'client backend'") {
			t.Fatalf("Replikation darf nicht beendet werden: %s", c.stdin)
		}
	}
}

func TestUnfence(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, svc := testActor(t, r)
	svc.active = false
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActUnfence}); err != nil {
		t.Fatal(err)
	}
	if !svc.active || strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatal("nicht zurückgenommen")
	}
	r2 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a2, _ := testActor(t, r2)
	if err := a2.Do(context.Background(), ha.Action{Kind: ha.ActUnfence}); err == nil {
		t.Fatal("Standby kann nicht wieder aktiv werden")
	}
}

func TestReconcilePassiveStopsPDH(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, svc := testActor(t, r)
	a.reconcile(context.Background(), ha.Passive)
	if svc.active {
		t.Fatal("pdh läuft auf passivem Knoten weiter")
	}
	a.reconcile(context.Background(), ha.Active)
	if svc.active {
		t.Fatal("Abgleich darf pdh nicht starten")
	}
}

func TestReconcileWritableNotActiveFences(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, _ := testActor(t, r)
	a.reconcile(context.Background(), ha.Passive)
	if !strings.Contains(confText(t, a), "default_transaction_read_only = on") {
		t.Fatal("schreibbare Datenbank nicht abgeschaltet")
	}
	found := false
	for _, e := range a.ev.since(0, 0) {
		found = found || e.Text == "Nicht aktiver Server mit schreibbarer Datenbank – abgeschaltet"
	}
	if !found {
		t.Fatalf("Alarm fehlt: %+v", a.ev.since(0, 0))
	}
}

func TestReconcileNeverPromotes(t *testing.T) {
	for _, role := range []ha.Role{ha.Passive, ha.Fenced, ha.Rejoin, ha.Active} {
		r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
		a, _ := testActor(t, r)
		a.reconcile(context.Background(), role)
		if has(r.list(), "psql: SELECT pg_promote") {
			t.Fatalf("%s: Abgleich hat hochgestuft", role)
		}
	}
}

func TestReconcileStandbyFollowsActive(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, _ := testActor(t, r)
	a.reconcile(context.Background(), ha.Passive)
	if !strings.Contains(confText(t, a), "host=10.0.0.2") || !has(r.list(), "pg_ctlcluster 18 main reload") {
		t.Fatalf("Standby nicht auf den aktiven Knoten gestellt: %v", r.list())
	}
	n := len(r.list())
	a.reconcile(context.Background(), ha.Passive)
	for _, l := range r.list()[n:] {
		if strings.HasPrefix(l, "pg_ctlcluster") || strings.HasPrefix(l, "chown") {
			t.Fatalf("unverändert: kein Schreiben/Neuladen: %v", r.list()[n:])
		}
	}
}

// Der Abgleich läuft in der Aktions-Warteschlange, nie neben einer Aktion.
func TestReconcileSkipsWhenBusy(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	release := make(chan struct{})
	ctx := context.Background()
	d.act = blockActor{release}
	d.enqueueActions(ctx, []ha.Action{{Kind: ha.ActFence}})
	waitFor(t, 2*time.Second, "Aktion läuft", d.busy.Load)
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	pa, _ := testActor(t, r)
	d.mu.Lock()
	d.act = pa
	d.maybeReconcileLocked(ctx, time.Minute)
	d.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if len(r.list()) != 0 {
		t.Fatalf("Abgleich lief neben der Aktion: %v", r.list())
	}
	close(release)
	waitFor(t, 2*time.Second, "Abgleich gelaufen", func() bool { return d.actionsIdle() && len(r.list()) > 0 })
}
