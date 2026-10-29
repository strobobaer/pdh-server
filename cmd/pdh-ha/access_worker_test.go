package main

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// vipState: Fake-runner mit Zustand – ip addr add/del ändern, was
// ip addr show meldet; dup lässt die Duplikat-Prüfung anschlagen.
type vipState struct {
	mu    sync.Mutex
	held  bool
	dup   bool
	calls []string
}

func (f *vipState) Run(_ context.Context, argv []string, _ []string, _ []byte) ([]byte, error) {
	cmd := strings.Join(argv, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cmd)
	switch {
	case strings.HasPrefix(cmd, "ip -o -4 addr show"):
		if f.held {
			return []byte(ipShowHeld), nil
		}
		return []byte("2: eth0    inet 10.77.1.5/24 scope global eth0\n"), nil
	case strings.HasPrefix(cmd, "arping -D"):
		if f.dup {
			return []byte("Unicast reply from 10.77.1.50 [AA:BB:CC:DD:EE:FF]  1.2ms\n"), errors.New("exit status 1")
		}
	case strings.HasPrefix(cmd, "ip addr add"):
		f.held = true
	case strings.HasPrefix(cmd, "ip addr del"):
		if !f.held {
			return nil, errors.New("RTNETLINK answers: Cannot assign requested address")
		}
		f.held = false
	}
	return nil, nil
}

func (f *vipState) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *vipState) setHeld(b bool) {
	f.mu.Lock()
	f.held = b
	f.mu.Unlock()
}

const (
	vipAdd = "ip addr add 10.77.1.50/24 dev eth0"
	vipDel = "ip addr del 10.77.1.50/24 dev eth0"
)

func testWorker(t *testing.T, f runner) *accessWorker {
	t.Helper()
	p, err := parseVIP("10.77.1.50/24")
	if err != nil {
		t.Fatal(err)
	}
	v := newVIPCtl(f, "eth0", p)
	v.sleep = func(time.Duration) {}
	w := newAccessWorker(v, testEvents(t))
	w.every = time.Hour // nur Signale treiben den Abgleich
	return w
}

// heldWorker: Arbeiter, der die Adresse hält (nur beobachtet, ohne Soll).
func heldWorker(t *testing.T, held bool) *accessWorker {
	t.Helper()
	w := testWorker(t, &vipState{held: held})
	w.step(context.Background())
	if w.held() != held {
		t.Fatalf("held = %v", w.held())
	}
	return w
}

func workerEvents(w *accessWorker, kind, sub string) int {
	n := 0
	for _, e := range w.ev.since(0, 0) {
		if e.Kind == kind && strings.Contains(e.Text, sub) {
			n++
		}
	}
	return n
}

func TestAccessWant(t *testing.T) {
	on := accessConfig{VIP: "10.77.1.50/24", VIPOn: true}
	off := accessConfig{VIP: "10.77.1.50/24"}
	for _, c := range []struct {
		role ha.Role
		a    accessConfig
		want bool
	}{
		{ha.Active, on, true},
		{ha.Passive, on, false},
		{ha.Fenced, on, false},
		{ha.Rejoin, on, false},
		{ha.Witness, on, false},
		{ha.Active, off, false},
	} {
		if got := accessWant(c.role, 0, c.a); got != c.want {
			t.Errorf("accessWant(%s, 0, vip_on=%v) = %v", c.role, c.a.VIPOn, got)
		}
	}
}

func TestAccessWantDuringHandover(t *testing.T) {
	if accessWant(ha.Active, 5, accessConfig{VIP: "10.77.1.50/24", VIPOn: true}) {
		t.Fatal("während der eigenen Übergabe darf die Adresse nicht gewollt sein")
	}
}

func TestAccessWorkerTakesAndReleases(t *testing.T) {
	f := &vipState{}
	w := testWorker(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-w.done }()
	go w.run(ctx)
	w.set(true, ha.Active, 3)
	waitFor(t, 2*time.Second, "Adresse übernommen", w.held)
	if n := f.count(vipAdd); n != 1 {
		t.Fatalf("ip addr add %d-mal", n)
	}
	if n := workerEvents(w, "info", "Adresse 10.77.1.50 übernommen"); n != 1 {
		t.Fatalf("Ereignis übernommen %d-mal", n)
	}
	w.set(false, ha.Passive, 4)
	waitFor(t, 2*time.Second, "Adresse abgegeben", func() bool { return !w.held() })
	if n := f.count(vipDel); n != 1 {
		t.Fatalf("ip addr del %d-mal", n)
	}
	if n := workerEvents(w, "info", "Adresse 10.77.1.50 abgegeben"); n != 1 {
		t.Fatalf("Ereignis abgegeben %d-mal", n)
	}
	cancel()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Arbeiter endet nicht mit dem Kontext")
	}
	// Ende des Dienstes gibt die Adresse nicht ab
	if n := f.count(vipDel); n != 1 {
		t.Fatalf("ip addr del %d-mal", n)
	}
}

func TestAccessWorkerRepairs(t *testing.T) {
	f := &vipState{}
	w := testWorker(t, f)
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	w.step(ctx)
	f.setHeld(false) // z. B. netplan apply
	w.step(ctx)
	if n := f.count(vipAdd); n != 2 {
		t.Fatalf("ip addr add %d-mal statt zweimal", n)
	}
	if !w.held() {
		t.Fatal("Adresse nicht wieder gesetzt")
	}
	w.step(ctx) // im Soll: nichts tun
	if n := f.count(vipAdd); n != 2 {
		t.Fatalf("ip addr add %d-mal statt zweimal", n)
	}
}

func TestAccessWorkerNoActionBeforeSet(t *testing.T) {
	// Neustart von pdh-ha am aktiven Server: vor dem ersten Takt nichts abgeben
	f := &vipState{held: true}
	w := testWorker(t, f)
	w.step(context.Background())
	if n := f.count("ip addr"); n != 0 {
		t.Fatalf("ip addr vor dem ersten Soll: %d", n)
	}
	if !w.held() {
		t.Fatal("Ist-Zustand nicht erkannt")
	}
}

func TestAccessWorkerDuplicateAlarmOnce(t *testing.T) {
	f := &vipState{dup: true}
	w := testWorker(t, f)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	w.step(ctx)
	w.step(ctx)
	const txt = "Adresse 10.77.1.50 wird noch von einem anderen Gerät benutzt (MAC AA:BB:CC:DD:EE:FF)"
	if n := workerEvents(w, "alarm", txt); n != 1 {
		t.Fatalf("Duplikat-Alarm %d-mal statt einmal", n)
	}
	if n := f.count("ip addr add"); n != 0 || w.held() {
		t.Fatalf("trotz Duplikat übernommen (%d)", n)
	}
	now = now.Add(61 * time.Minute)
	w.step(ctx)
	if n := workerEvents(w, "alarm", txt); n != 2 {
		t.Fatalf("nach einer Stunde %d Alarme statt zwei", n)
	}
}

func TestAccessWorkerWaitHeld(t *testing.T) {
	f := &vipState{}
	w := testWorker(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-w.done }()
	if w.waitHeld(ctx, 10*time.Millisecond) {
		t.Fatal("waitHeld ohne Adresse true")
	}
	go w.run(ctx)
	w.set(true, ha.Active, 3)
	if !w.waitHeld(ctx, 2*time.Second) {
		t.Fatal("waitHeld: Adresse nicht übernommen")
	}
}

func TestPGActorIgnoresVIPActions(t *testing.T) {
	a, _ := testActor(t, &fakeRunner{})
	for _, k := range []ha.ActionKind{ha.ActVIPTake, ha.ActVIPRelease} {
		if err := a.Do(context.Background(), ha.Action{Kind: k, Term: 2}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if evs := a.ev.since(0, 0); len(evs) != 0 {
		t.Fatalf("Ereignisse: %+v", evs)
	}
}

func withVIP(cl clusterConfig) clusterConfig {
	cl.Access = accessConfig{VIP: "10.77.1.50/24", VIPOn: true}
	return cl
}

func TestHeartbeatVIPField(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", VIP: true}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, withVIP(duoCluster()), ha.Persisted{Role: ha.Active, Term: 1}, fn)
	d.mu.Lock()
	m := d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if m.VIP {
		t.Fatal("VIP ohne Arbeiter gemeldet")
	}
	d.mu.Lock()
	d.access = heldWorker(t, true)
	d.views = fn.peers(0)
	m = d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if !m.VIP {
		t.Fatal("Heartbeat ohne VIP, obwohl gehalten")
	}
	st := d.statusNow()
	if !st.Self.VIPHeld || len(st.Peers) != 1 || !st.Peers[0].VIPHeld {
		t.Fatalf("vip_held: self %v, peers %+v", st.Self.VIPHeld, st.Peers)
	}
}

func TestStatusAccessHolder(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
	cl := withVIP(duoCluster())
	cl.Access.Tested = &vipTested{Net: "10.77.1.0/24"}
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, cl, ha.Persisted{Role: ha.Active, Term: 1}, fn)
	set := func(w *accessWorker) {
		d.mu.Lock()
		d.access = w
		d.views = fn.peers(0)
		d.mu.Unlock()
	}
	set(heldWorker(t, true))
	st := d.statusNow()
	if st.Access.Holder != "a" || st.Access.VIP != "10.77.1.50/24" || !st.Access.VIPOn ||
		st.Access.Tested == nil || st.Access.Tested.Net != "10.77.1.0/24" {
		t.Fatalf("Zugang: %+v", st.Access)
	}
	set(heldWorker(t, false))
	if h := d.statusNow().Access.Holder; h != "" {
		t.Fatalf("Halter %q statt niemand", h)
	}
	fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", VIP: true}))
	d.mu.Lock()
	d.views = fn.peers(0)
	d.mu.Unlock()
	if h := d.statusNow().Access.Holder; h != "b" {
		t.Fatalf("Halter %q statt b", h)
	}
}

func fakeCards(addrs ...string) func() ([]ifAddr, error) {
	return func() ([]ifAddr, error) {
		var out []ifAddr
		for _, a := range addrs {
			name, pfx, _ := strings.Cut(a, "=")
			out = append(out, ifAddr{Iface: name, Prefix: netip.MustParsePrefix(pfx)})
		}
		return out, nil
	}
}

func TestWitnessNoAccess(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, withVIP(trioCluster()), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	d.ifAddrsFn = fakeCards("eth0=10.77.1.3/24")
	f := &vipState{}
	d.sys = f
	ctx := stopAccess(t, d)
	d.tick(ctx)
	d.mu.Lock()
	w := d.access
	d.mu.Unlock()
	if w != nil {
		t.Fatal("Zeuge hat einen Adress-Arbeiter")
	}
	if n := f.count("ip"); n != 0 {
		t.Fatalf("Zeuge ruft ip auf: %d", n)
	}
}

// stopAccess beendet beim Testende den Adress-Arbeiter und wartet auf ihn
// (sonst schreibt er noch ins Ereignisprotokoll, während der Temp-Ordner
// gelöscht wird).
func stopAccess(t *testing.T, d *daemon) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		d.mu.Lock()
		w, prev := d.access, d.accessPrev
		d.mu.Unlock()
		if w != nil {
			<-w.done
		}
		if prev != nil {
			<-prev
		}
	})
	return ctx
}

// accessDaemon: aktiver Knoten a, Partner b sieht a, Adresse eingeschaltet.
func accessDaemon(t *testing.T, cards func() ([]ifAddr, error)) (*daemon, *vipState) {
	t.Helper()
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Sees: []string{"a"}}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, withVIP(duoCluster()), ha.Persisted{Role: ha.Active, Term: 1}, fn)
	d.ifAddrsFn = cards
	f := &vipState{}
	d.sys = f
	return d, f
}

func TestDaemonAccessFollowsRole(t *testing.T) {
	d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24", "eth1=192.168.9.5/24"))
	ctx := stopAccess(t, d)
	d.tick(ctx)
	if r := d.statusNow().Self.Role; r != ha.Active {
		t.Fatalf("Rolle %s", r)
	}
	waitFor(t, 2*time.Second, "Adresse übernommen", func() bool { return f.count(vipAdd) == 1 })
	st := d.statusNow()
	if st.Access.Iface != "eth0" || st.Access.IfaceErr != "" {
		t.Fatalf("Zugang: %+v", st.Access)
	}
	waitFor(t, 2*time.Second, "Halter a", func() bool { return d.statusNow().Access.Holder == "a" })
	// Adresse ausgeschaltet: alter Arbeiter gibt sie ab, kein neuer Arbeiter
	d.lock()
	d.cl.Access.VIPOn = false
	d.unlock()
	d.tick(ctx)
	waitFor(t, 2*time.Second, "Adresse abgegeben", func() bool { return f.count(vipDel) == 1 })
	d.mu.Lock()
	w := d.access
	d.mu.Unlock()
	if w != nil {
		t.Fatal("Arbeiter läuft trotz vip_on=false")
	}
}

func TestDaemonAccessIfaceAmbiguous(t *testing.T) {
	d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24", "eth1=10.77.1.6/24"))
	ctx := stopAccess(t, d)
	d.tick(ctx)
	st := d.statusNow()
	if !strings.Contains(st.Access.IfaceErr, "nicht eindeutig") || st.Access.Iface != "" {
		t.Fatalf("Zugang: %+v", st.Access)
	}
	d.mu.Lock()
	w := d.access
	d.mu.Unlock()
	if w != nil || f.count("ip") != 0 {
		t.Fatal("Arbeiter trotz unklarer Netzwerkkarte")
	}
	// Karte per node.json festgelegt → Arbeiter startet
	d.lock()
	d.node.VIPInterface = "eth1"
	d.unlock()
	d.tick(ctx)
	waitFor(t, 2*time.Second, "Adresse auf eth1", func() bool { return f.count("ip addr add 10.77.1.50/24 dev eth1") == 1 })
	if st := d.statusNow(); st.Access.Iface != "eth1" || st.Access.IfaceErr != "" {
		t.Fatalf("Zugang: %+v", st.Access)
	}
}

func TestPrintStatusAccess(t *testing.T) {
	base := apiStatus{Configured: true, Mode: ha.Duo, Self: apiNode{ID: "node1", Kind: "node", Role: ha.Passive}}
	for _, c := range []struct {
		a    apiAccess
		want string
	}{
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, Holder: "node2"}, "Adresse 10.77.1.50: bei node2\n"},
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, Holder: "node1"}, "Adresse 10.77.1.50: hier\n"},
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true}, "Adresse 10.77.1.50: niemand\n"},
		{apiAccess{VIP: "10.77.1.50/24"}, "Adresse 10.77.1.50: niemand (aus)\n"},
	} {
		st := base
		st.Access = c.a
		var b strings.Builder
		printStatus(&b, st)
		if !strings.Contains(b.String(), c.want) {
			t.Errorf("Ausgabe ohne %q:\n%s", c.want, b.String())
		}
	}
	var b strings.Builder
	printStatus(&b, base)
	if strings.Contains(b.String(), "Adresse") {
		t.Errorf("Adresszeile ohne Adresse:\n%s", b.String())
	}
}

// Rollentausch: Der alte aktive Server gibt die Adresse ab, sobald seine
// Übergabe beginnt (demote läuft noch), und holt sie nicht zurück.
func TestDaemonAccessReleasedDuringSwap(t *testing.T) {
	d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24"))
	d.lock()
	d.p.PartnerVersion = "test"
	d.unlock()
	release := make(chan struct{})
	defer close(release)
	d.act = blockActor{release}
	ctx := stopAccess(t, d)
	d.tick(ctx)
	waitFor(t, 2*time.Second, "Adresse übernommen", func() bool { return f.count(vipAdd) == 1 })
	if err := d.command("swap"); err != nil {
		t.Fatal(err)
	}
	d.tick(ctx) // demote blockiert: Übergabe läuft
	d.mu.Lock()
	w := d.access
	d.mu.Unlock()
	waitFor(t, 2*time.Second, "Adresse abgegeben", func() bool { return f.count(vipDel) == 1 && !w.held() })
	shows := f.count("ip -o -4 addr show")
	for i := 0; i < 3; i++ {
		d.tick(ctx)
	}
	d.mu.Lock()
	pending := d.carry.HandoverPending
	d.mu.Unlock()
	if pending == 0 {
		t.Fatal("Übergabe nicht mehr offen – Test prüft nichts")
	}
	waitFor(t, 2*time.Second, "Abgleich nach den Takten", func() bool { return f.count("ip -o -4 addr show") > shows })
	if n := f.count(vipAdd); n != 1 || w.held() {
		t.Fatalf("Adresse während der Übergabe zurückgeholt (add %d, held %v)", n, w.held())
	}
}

// Ende des Dienstes (SIGTERM): Automatik „echt“ gibt die gehaltene Adresse
// ab, damit der Partner sie bekommt; im Beobachtungsmodus bleibt sie liegen.
func TestDaemonAccessReleasedOnExit(t *testing.T) {
	for _, c := range []struct {
		mode string
		del  int
	}{{ha.AutoReal, 1}, {ha.AutoObserve, 0}} {
		t.Run(c.mode, func(t *testing.T) {
			d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24"))
			d.lock()
			d.cl.Settings.AutoMode = c.mode
			d.unlock()
			ctx, cancel := context.WithCancel(context.Background())
			d.tick(ctx)
			waitFor(t, 2*time.Second, "Adresse übernommen", func() bool {
				d.mu.Lock()
				w := d.access
				d.mu.Unlock()
				return w != nil && w.held()
			})
			cancel()
			d.stopAccessOnExit()
			if n := f.count(vipDel); n != c.del {
				t.Fatalf("ip addr del %d-mal statt %d", n, c.del)
			}
			f.mu.Lock()
			held := f.held
			f.mu.Unlock()
			if held != (c.del == 0) {
				t.Fatalf("Adresse liegt noch: %v", held)
			}
			if got := hasEvent(d, "Adresse 10.77.1.50 abgegeben"); got != (c.del == 1) {
				t.Fatalf("Ereignis abgegeben: %v (%v)", got, eventTexts(d))
			}
		})
	}
}

// Kein Arbeiter (Adresse aus): Dienstende tut nichts und hängt nicht.
func TestDaemonAccessExitWithoutWorker(t *testing.T) {
	d, f := accessDaemon(t, fakeCards("eth0=10.77.1.5/24"))
	d.stopAccessOnExit()
	if n := f.count("ip"); n != 0 {
		t.Fatalf("ip aufgerufen: %v", f.calls)
	}
}

// Reparatur in Dauerschleife (ein Netzwerkdienst entfernt die Adresse immer
// wieder): Meldungen höchstens einmal je Stunde, gesetzt wird trotzdem jedes Mal.
func TestAccessWorkerRepairDebounced(t *testing.T) {
	f := &vipState{}
	w := testWorker(t, f)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	ctx := context.Background()
	w.set(true, ha.Active, 3)
	w.step(ctx)
	for i := 0; i < 5; i++ {
		f.setHeld(false)
		w.step(ctx)
	}
	if n := f.count(vipAdd); n != 6 {
		t.Fatalf("ip addr add %d-mal statt sechsmal", n)
	}
	if n := workerEvents(w, "warn", "war nicht mehr gesetzt"); n != 1 {
		t.Fatalf("Reparaturmeldung %d-mal statt einmal", n)
	}
	if n := workerEvents(w, "info", "Adresse 10.77.1.50 übernommen"); n != 2 {
		t.Fatalf("übernommen %d-mal statt zweimal (Übernahme + eine Reparatur)", n)
	}
	now = now.Add(61 * time.Minute)
	f.setHeld(false)
	w.step(ctx)
	if n := workerEvents(w, "warn", "war nicht mehr gesetzt"); n != 2 {
		t.Fatalf("nach einer Stunde %d Reparaturmeldungen statt zwei", n)
	}
}

// Wechselnder Erfolg und Fehlschlag: derselbe Alarm trotzdem höchstens einmal je Stunde.
func TestAccessWorkerAlarmNotResetBySuccess(t *testing.T) {
	f := &vipState{dup: true}
	w := testWorker(t, f)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	ctx := context.Background()
	setDup := func(b bool) {
		f.mu.Lock()
		f.dup = b
		f.mu.Unlock()
	}
	const txt = "wird noch von einem anderen Gerät benutzt"
	for i := 0; i < 3; i++ {
		setDup(true)
		w.set(true, ha.Active, 3)
		w.step(ctx) // Duplikat → Alarm
		setDup(false)
		w.step(ctx) // gelingt
		if !w.held() {
			t.Fatalf("Runde %d: nicht übernommen", i)
		}
		w.set(false, ha.Passive, 4)
		w.step(ctx) // abgegeben
		now = now.Add(time.Minute)
	}
	if n := workerEvents(w, "alarm", txt); n != 1 {
		t.Fatalf("Duplikat-Alarm %d-mal statt einmal", n)
	}
	if n := workerEvents(w, "info", "übernommen"); n != 3 {
		t.Fatalf("übernommen %d-mal statt dreimal (echte Übernahmen bleiben sichtbar)", n)
	}
}
