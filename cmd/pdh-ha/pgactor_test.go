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

// svcLog: Dienst-Aufrufe landen als "<name> <op>" (svc = pdh, tun = Tunnel)
// im fakeRunner (eine Reihenfolge).
type svcLog struct {
	r        *fakeRunner
	name     string
	active   bool
	startErr error // Start scheitert (pdh dauerhaft „failed“)
}

func (s *svcLog) log(op string)                            { _, _ = s.r.Run(context.Background(), []string{s.name, op}, nil, nil) }
func (s *svcLog) Start(context.Context) error {
	s.log("start")
	s.active = s.startErr == nil
	return s.startErr
}
func (s *svcLog) Stop(context.Context) error               { s.log("stop"); s.active = false; return nil }
func (s *svcLog) Restart(context.Context) error            { s.log("restart"); s.active = true; return nil }
func (s *svcLog) Active(context.Context) bool              { return s.active }
func (s *svcLog) SetAutostart(context.Context, bool) error { return nil }

// tunOf: der Tunnel-Fake eines testActor.
func tunOf(a *pgActor) *svcLog { return a.tun.(*svcLog) }

const (
	stPrimary = "f|0/100|1|off|pdh_ha_b|"
	stStandby = "t|0/100|1|off||streaming"
)

func testActor(t *testing.T, r *fakeRunner) (*pgActor, *svcLog) {
	t.Helper()
	pg := testPG(r)
	pg.c.DataDir, pg.c.ConfDir = t.TempDir(), t.TempDir()
	svc := &svcLog{r: r, name: "svc", active: true}
	tun := &svcLog{r: r, name: "tun", active: true}
	ev := openEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 5<<20, 3)
	a := newPGActor(pg, svc, tun, ev, func() pgEnv {
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
		"psql: SELECT pg_create_physical_replication_slot('pdh_ha_b')", "chown", "pg_ctlcluster 18 main reload", "svc restart", "tun start")
	if c := confText(t, a); !strings.Contains(c, "synchronous_standby_names = 'pdh_ha_b'") || strings.Contains(c, "primary_conninfo") {
		t.Fatalf("Konfiguration:\n%s", c)
	}
}

// Nach dem Hochstufen pdh neu starten (lief als Spiegel, nicht auf den
// Wächter warten), danach erst den Tunnel.
func TestPromoteRestartsPDH(t *testing.T) {
	r := (&fakeRunner{}).
		on("runuser", "(SELECT timeline_id FROM pg_control_checkpoint())", stStandby, nil).
		on("runuser", "SELECT pg_promote", "t", nil).
		on("runuser", "SELECT pg_is_in_recovery()", "f", nil)
	a, _ := testActor(t, r)
	tunOf(a).active = false
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActPromote, Term: 4}); err != nil {
		t.Fatal(err)
	}
	var svc []string
	for _, l := range r.list() {
		if strings.HasPrefix(l, "svc ") || strings.HasPrefix(l, "tun ") {
			svc = append(svc, l)
		}
	}
	if strings.Join(svc, ",") != "svc restart,tun start" {
		t.Fatalf("Dienste: %v", svc)
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
	seq(t, r.list(), "tun stop", "svc stop", "chown", "pg_ctlcluster 18 main reload", "psql: SELECT pg_terminate_backend")
	if svc.active || tunOf(a).active || !strings.Contains(confText(t, a), "default_transaction_read_only = on") {
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
	svc.active, tunOf(a).active = false, false
	if err := a.Do(context.Background(), ha.Action{Kind: ha.ActUnfence}); err != nil {
		t.Fatal(err)
	}
	seq(t, r.list(), "svc start", "tun start")
	if !svc.active || !tunOf(a).active || strings.Contains(confText(t, a), "default_transaction_read_only") {
		t.Fatal("nicht zurückgenommen")
	}
	r2 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a2, _ := testActor(t, r2)
	if err := a2.Do(context.Background(), ha.Action{Kind: ha.ActUnfence}); err == nil {
		t.Fatal("Standby kann nicht wieder aktiv werden")
	}
}

// Passiv mit Standby: pdh läuft als Spiegel, der Tunnel nie.
func TestReconcilePassiveStandbyStartsPDH(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, svc := testActor(t, r)
	svc.active = false
	a.reconcile(context.Background(), ha.Passive)
	if !svc.active || tunOf(a).active || !has(r.list(), "svc start") || !has(r.list(), "tun stop") || has(r.list(), "tun start") {
		t.Fatalf("passiv + Standby: pdh an, Tunnel aus: %v", r.list())
	}
	a.reconcile(context.Background(), ha.Passive)
	n := 0
	for _, e := range a.ev.since(0, 0) {
		if e.Text == "PDH als Spiegel gestartet" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Ereignis nur beim Wechsel von aus nach an: %d", n)
	}
	// aktiv, aber Datenbank Standby (noch nicht hochgestuft): nichts starten
	r2 := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a2, svc2 := testActor(t, r2)
	svc2.active, tunOf(a2).active = false, false
	a2.reconcile(context.Background(), ha.Active)
	if svc2.active || tunOf(a2).active {
		t.Fatal("Abgleich darf am aktiven Knoten ohne schreibbare Datenbank nichts starten")
	}
}

// countEvents: Ereignisse mit Text-Präfix.
func countEvents(a *pgActor, prefix string) int {
	n := 0
	for _, e := range a.ev.since(0, 0) {
		if strings.HasPrefix(e.Text, prefix) {
			n++
		}
	}
	return n
}

// Ein pdh, das nach jedem Start gleich wieder aus ist (oder gar nicht
// startet), darf das Protokoll nicht alle 30 s füllen: gleicher Ausgang nur
// einmal, nach einer Stunde erneut, bei Änderung sofort.
func TestReconcileMirrorStartNoFlood(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stStandby, nil)
	a, svc := testActor(t, r)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		svc.active = false // gleich wieder abgestürzt
		a.reconcile(ctx, ha.Passive)
		now = now.Add(30 * time.Second)
	}
	if n := countEvents(a, "PDH als Spiegel gestartet"); n != 1 || countPrefix(r.list(), "svc start") != 3 {
		t.Fatalf("Start ok mehrfach: %d Ereignisse, Aufrufe %v", n, r.list())
	}
	svc.startErr = errors.New("start request repeated too quickly")
	for i := 0; i < 5; i++ {
		svc.active = false
		a.reconcile(ctx, ha.Passive)
		now = now.Add(30 * time.Second)
	}
	if n := countEvents(a, "PDH als Spiegel nicht gestartet"); n != 1 {
		t.Fatalf("gleicher Fehler: genau ein Alarm, nicht %d", n)
	}
	now = now.Add(time.Hour)
	a.reconcile(ctx, ha.Passive)
	if n := countEvents(a, "PDH als Spiegel nicht gestartet"); n != 2 {
		t.Fatalf("nach einer Stunde erneut melden: %d", n)
	}
	svc.startErr = nil
	a.reconcile(ctx, ha.Passive)
	if n := countEvents(a, "PDH als Spiegel gestartet"); n != 2 {
		t.Fatalf("wieder gestartet: melden (%d)", n)
	}
	// bewusst gestoppt (Rückführung) und danach wieder Spiegel: erneut melden
	if err := a.stopServices(ctx); err != nil {
		t.Fatal(err)
	}
	a.reconcile(ctx, ha.Passive)
	if n := countEvents(a, "PDH als Spiegel gestartet"); n != 3 {
		t.Fatalf("nach bewusstem Stopp erneut melden: %d", n)
	}
}

// Passiv ohne Standby (abgeschottet, gestoppt, Fehler): pdh aus.
func TestReconcilePassiveFencedStopsPDH(t *testing.T) {
	cases := map[string]*fakeRunner{
		"nur lesend": (&fakeRunner{}).on("runuser", "pg_is_in_recovery", "f|0/100|1|on|pdh_ha_b|", nil),
		"gestoppt": (&fakeRunner{}).
			on("runuser -u postgres -- psql", "", "", errors.New("läuft nicht")).
			on("pg_lsclusters", "", stStoppedLs, nil),
		"Fehler": (&fakeRunner{}).
			on("runuser -u postgres -- psql", "", "", errors.New("kaputt")).
			on("pg_lsclusters", "", "18 main 5432 online postgres /d /l", nil),
	}
	for name, r := range cases {
		a, svc := testActor(t, r)
		a.reconcile(context.Background(), ha.Passive)
		if svc.active || tunOf(a).active || has(r.list(), "svc start") {
			t.Fatalf("%s: pdh und Tunnel aus: %v", name, r.list())
		}
		if has(r.list(), "psql: SELECT pg_terminate_backend") {
			t.Fatalf("%s: nicht erneut abschotten: %v", name, r.list())
		}
	}
}

// Nach einem Neustart des aktiven Servers (Beobachtung: Rolle bleibt, kein
// unfence) startet der Abgleich pdh und Tunnel – sonst blieben sie aus.
func TestReconcileActiveStartsServices(t *testing.T) {
	r := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", stPrimary, nil)
	a, svc := testActor(t, r)
	svc.active, tunOf(a).active = false, false
	a.reconcile(context.Background(), ha.Active)
	if !svc.active || !tunOf(a).active {
		t.Fatal("aktiv mit schreibbarer Datenbank: pdh bzw. Tunnel nicht gestartet")
	}
	ro := (&fakeRunner{}).on("runuser", "pg_is_in_recovery", "f|0/100|1|on|pdh_ha_b|", nil)
	a2, svc2 := testActor(t, ro)
	svc2.active = false
	a2.reconcile(context.Background(), ha.Active)
	if svc2.active {
		t.Fatal("Datenbank nur lesend (abgeschaltet): pdh darf nicht starten")
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

// TestKeepAutostartOff: wieder eingeschalteter Autostart (Tunnel-Skript aus
// dem PDH) wird abgeschaltet und gemeldet, ein abgeschalteter bleibt in Ruhe.
func TestKeepAutostartOff(t *testing.T) {
	r := &fakeRunner{}
	r.on("systemctl list-unit-files", "", "cloudflared.service enabled enabled\n", nil)
	r.on("systemctl is-enabled pdh", "", "disabled\n", nil)
	r.on("systemctl is-enabled cloudflared", "", "enabled\n", nil)
	a, _ := testActor(t, r)
	a.svc = systemdService{r: r, unit: "pdh"}
	a.tun = tunnelService{s: systemdService{r: r, unit: tunnelUnit}}
	a.keepAutostartOff(context.Background())
	if !has(r.list(), "systemctl disable cloudflared") || has(r.list(), "systemctl disable pdh") {
		t.Fatalf("Aufrufe: %v", r.list())
	}
	if countEvents(a, "Autostart von Tunnel wieder abgeschaltet") != 1 {
		t.Fatal("Abschalten nicht gemeldet")
	}
	r2 := &fakeRunner{}
	r2.on("systemctl is-enabled", "", "disabled\n", nil)
	a.svc = systemdService{r: r2, unit: "pdh"}
	a.tun = tunnelService{s: systemdService{r: r2, unit: tunnelUnit}}
	a.keepAutostartOff(context.Background())
	if has(r2.list(), "systemctl disable") {
		t.Fatalf("abgeschalteter Autostart erneut abgeschaltet: %v", r2.list())
	}
}
