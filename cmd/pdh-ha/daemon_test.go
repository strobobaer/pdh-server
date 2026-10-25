package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// fakeNet ersetzt den Transport: feste Sicht, protokolliert Sendungen.
type fakeNet struct {
	mu    sync.Mutex
	views map[string]peerView
	sent  []sentMsg
	order *orderLog
	in    chan inbound
	cl    clusterConfig
	certs int
}

type orderLog struct {
	mu sync.Mutex
	l  []string
}

func (o *orderLog) add(s string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.l = append(o.l, s)
	o.mu.Unlock()
}

func (o *orderLog) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.l...)
}

func newFakeNet() *fakeNet {
	return &fakeNet{views: map[string]peerView{}, in: make(chan inbound, 16)}
}

func (f *fakeNet) peers(time.Duration) map[string]peerView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]peerView{}
	for k, v := range f.views {
		out[k] = v
	}
	return out
}

func (f *fakeNet) record(to string, m message) {
	f.mu.Lock()
	f.sent = append(f.sent, sentMsg{to, m})
	f.mu.Unlock()
	f.order.add(fmt.Sprintf("send %s term=%d vote=%d", m.T, m.Term, m.VoteTerm))
}

func (f *fakeNet) broadcast(m message)           { f.record("", m) }
func (f *fakeNet) sendTo(id string, m message)   { f.record(id, m) }
func (f *fakeNet) incoming() <-chan inbound      { return f.in }
func (f *fakeNet) setCluster(cl clusterConfig)   { f.mu.Lock(); f.cl = cl; f.mu.Unlock() }
func (f *fakeNet) setCert(tls.Certificate)       { f.mu.Lock(); f.certs++; f.mu.Unlock() }
func (f *fakeNet) setView(id string, v peerView) { f.mu.Lock(); f.views[id] = v; f.mu.Unlock() }
func (f *fakeNet) messages(typ string) []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentMsg
	for _, s := range f.sent {
		if s.m.T == typ {
			out = append(out, s)
		}
	}
	return out
}

func seenView(m message) peerView {
	m.T = "hb"
	return peerView{Last: m, LastSeen: time.Second, Paths: []pathStat{{Addr: "x", Alive: true, LastRecv: time.Second}}}
}

// recActor merkt sich Aktionen; fail bestimmt das Verhalten.
type recActor struct {
	mu   sync.Mutex
	got  []ha.Action
	fail func(ha.Action) error
}

func (r *recActor) Do(_ context.Context, a ha.Action) error {
	r.mu.Lock()
	r.got = append(r.got, a)
	r.mu.Unlock()
	if r.fail != nil {
		return r.fail(a)
	}
	return nil
}

func duoCluster() clusterConfig {
	return clusterConfig{ClusterID: "c1", Version: 2, CAHolder: "a", Settings: ha.DefaultSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{"127.0.0.1:7480"}},
		{ID: "b", Kind: "node", Addrs: []string{"127.0.0.2:7480"}},
	}}
}

func trioCluster() clusterConfig {
	cl := duoCluster()
	cl.Version = 3
	cl.Nodes = append(cl.Nodes, clusterNode{ID: "w", Kind: "witness", Addrs: []string{"127.0.0.3:7480"}})
	return cl
}

// fakeDaemon baut einen Daemon mit Ablage im Temp-Ordner und Fake-Transport.
func fakeDaemon(t *testing.T, self nodeConfig, cl clusterConfig, p ha.Persisted, fn *fakeNet) *daemon {
	t.Helper()
	st := store{etc: t.TempDir(), state: t.TempDir()}
	if err := st.saveNode(self); err != nil {
		t.Fatal(err)
	}
	if err := st.saveCluster(cl); err != nil {
		t.Fatal(err)
	}
	if err := st.saveState(p); err != nil {
		t.Fatal(err)
	}
	return loadFakeDaemon(t, st, fn)
}

func loadFakeDaemon(t *testing.T, st store, fn *fakeNet) *daemon {
	t.Helper()
	d := newDaemon(st, openEventLog(st.statePath("events.jsonl"), 5<<20, 3))
	d.version = "test"
	d.net = fn
	d.pingFn = func(context.Context, string) bool { return true }
	d.gwFn = func() (string, error) { return "", errors.New("kein Gateway") }
	d.ufwFn = func(context.Context, [][]string) error { return nil }
	if err := d.load(); err != nil {
		t.Fatal(err)
	}
	return d
}

func eventTexts(d *daemon) []string {
	var out []string
	for _, e := range d.ev.since(0, 0) {
		out = append(out, e.Kind+": "+e.Text)
	}
	return out
}

func hasEvent(d *daemon, sub string) bool {
	for _, s := range eventTexts(d) {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestDaemonPersistsBeforeSend(t *testing.T) {
	ol := &orderLog{}
	fn := newFakeNet()
	fn.order = ol
	fn.setView("b", seenView(message{Role: "aktiv", Term: 3, Ver: "test"}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.saveFn = func(p ha.Persisted) error {
		ol.add(fmt.Sprintf("save term=%d", p.Term))
		return d.st.saveState(p)
	}
	d.tick(context.Background())
	l := ol.list()
	if len(l) < 2 || l[0] != "save term=3" || !strings.HasPrefix(l[1], "send hb term=3") {
		t.Fatalf("Reihenfolge: %v", l)
	}
	p, _ := d.st.loadState()
	if p.Term != 3 {
		t.Fatalf("gespeichert: %+v", p)
	}
	hb := fn.messages("hb")[0].m
	if !hb.DataCurrent || !hb.DataAssumed || hb.ID != "a" || hb.Ver != "test" || hb.ClusterVer != 2 ||
		len(hb.Sees) != 1 || hb.Sees[0] != "b" || hb.Seq == 0 {
		t.Fatalf("Heartbeat: %+v", hb)
	}
}

func TestDaemonSaveFailureSendsNothing(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "aktiv", Term: 3, Ver: "test"}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.saveFn = func(ha.Persisted) error { return errors.New("Platte voll") }
	d.tick(context.Background())
	d.tick(context.Background())
	if n := len(fn.messages("hb")); n != 0 {
		t.Fatalf("%d Heartbeats trotz Speicherfehler", n)
	}
	if d.statusNow().Self.Term != 1 || !hasEvent(d, "Platte voll") {
		t.Fatalf("Zustand: %+v %v", d.statusNow().Self, eventTexts(d))
	}
	n := 0
	for _, e := range eventTexts(d) {
		if strings.Contains(e, "Platte voll") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Speicher-Alarm %d-mal statt einmal je Minute", n)
	}
}

func TestWitnessPersistsBeforeReply(t *testing.T) {
	ol := &orderLog{}
	fn := newFakeNet()
	fn.order = ol
	fn.setView("a", seenView(message{Role: "passiv", Term: 1}))
	fn.setView("b", peerView{Last: message{T: "hb", Role: "aktiv", Term: 1}, LastSeen: time.Second, Paths: []pathStat{{Addr: "x"}}})
	d := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, trioCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, fn)
	d.mono = func() time.Duration { return 100 * time.Second }
	d.saveFn = func(p ha.Persisted) error {
		ol.add(fmt.Sprintf("save voted=%d/%s", p.VotedTerm, p.VotedFor))
		return d.st.saveState(p)
	}
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "vote_req", Seq: 7, VoteTerm: 2}})
	l := ol.list()
	if len(l) != 2 || l[0] != "save voted=2/a" || l[1] != "send vote_resp term=0 vote=2" {
		t.Fatalf("Reihenfolge: %v", l)
	}
	r := fn.messages("vote_resp")[0]
	if r.to != "a" || !r.m.Granted || r.m.Seq == 0 {
		t.Fatalf("Antwort: %+v", r)
	}
	// Nur Knoten dürfen um eine Stimme bitten.
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "vote_req", Seq: 8, VoteTerm: 3}})
	if n := len(fn.messages("vote_resp")); n != 1 {
		t.Fatalf("%d Antworten", n)
	}
	// Heartbeat des Zeugen: Rolle zeuge, höchste Amtszeit, Stimme.
	d.tick(context.Background())
	hb := fn.messages("hb")[0].m
	if hb.Role != "zeuge" || hb.Term != 2 || hb.VotedFor != "a" {
		t.Fatalf("Zeugen-Heartbeat: %+v", hb)
	}
}

func TestObserveActorOnly(t *testing.T) {
	ev := testEvents(t)
	a := observeActor{ev: ev}
	kinds := []ha.ActionKind{ha.ActPromote, ha.ActDemote, ha.ActFence, ha.ActUnfence, ha.ActVIPTake,
		ha.ActVIPRelease, ha.ActRequestVote, ha.ActHandover, ha.ActRejoin}
	for _, k := range kinds {
		if err := a.Do(context.Background(), ha.Action{Kind: k, Term: 4}); err != nil {
			t.Fatal(err)
		}
	}
	got := ev.since(0, 0)
	if len(got) != len(kinds) {
		t.Fatalf("%d Ereignisse", len(got))
	}
	for i, e := range got {
		if e.Kind != "action" || e.Text != "würde jetzt: "+string(kinds[i])+" (Amtszeit 4)" {
			t.Errorf("%+v", e)
		}
	}
}

func TestDaemonActorPanic(t *testing.T) {
	for name, fail := range map[string]func(ha.Action) error{
		"panic":  func(ha.Action) error { panic("kaputt") },
		"fehler": func(ha.Action) error { return errors.New("kaputt") },
	} {
		t.Run(name, func(t *testing.T) {
			fn := newFakeNet()
			fn.setView("b", peerView{Paths: []pathStat{{Addr: "x"}}})
			d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1, MaintSeq: 4}, fn)
			ra := &recActor{fail: fail}
			d.act = ra
			d.tick(context.Background()) // Start: aktiv ohne Partner → fence
			if len(ra.got) == 0 || ra.got[0].Kind != ha.ActFence {
				t.Fatalf("Aktionen: %+v", ra.got)
			}
			p, _ := d.st.loadState()
			if !p.Maint || p.MaintSeq != 5 {
				t.Fatalf("Wartung nicht an: %+v", p)
			}
			if !hasEvent(d, "alarm: Aktion fence fehlgeschlagen") || !hasEvent(d, "(Beobachtung)") {
				t.Fatalf("Alarm fehlt: %v", eventTexts(d))
			}
		})
	}
}

func TestDaemonClusterUpdate(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "aktiv", Term: 1, ClusterVer: 5}))
	fn.setView("w", seenView(message{Role: "zeuge", Term: 1, ClusterVer: 9}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, trioCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.tick(context.Background())
	req := fn.messages("cluster")
	if len(req) != 1 || req[0].to != "b" || req[0].m.Cluster != nil {
		t.Fatalf("Anforderung: %+v", req)
	}
	newer := trioCluster()
	newer.Version = 5
	newer.GatewayTargets = []string{"10.0.0.254"}
	// vom Zeugen nie annehmen
	d.handle(context.Background(), inbound{From: "w", Msg: message{T: "cluster", Seq: 1, Cluster: &newer}})
	if c, _ := d.st.loadCluster(); c.Version != 3 {
		t.Fatalf("vom Zeugen übernommen: %d", c.Version)
	}
	d.handle(context.Background(), inbound{From: "b", Msg: message{T: "cluster", Seq: 2, Cluster: &newer}})
	c, _ := d.st.loadCluster()
	if c.Version != 5 || fn.cl.Version != 5 || len(d.statusNow().GatewayTargets) != 1 {
		t.Fatalf("nicht übernommen: %d %d", c.Version, fn.cl.Version)
	}
	// Anfrage eines Knotens beantworten
	d.handle(context.Background(), inbound{From: "b", Msg: message{T: "cluster", Seq: 3}})
	resp := fn.messages("cluster")
	if last := resp[len(resp)-1]; last.to != "b" || last.m.Cluster == nil || last.m.Cluster.Version != 5 {
		t.Fatalf("Antwort: %+v", last)
	}
}

func TestDaemonRemoved(t *testing.T) {
	fn := newFakeNet()
	fn.setView("a", seenView(message{Role: "aktiv", Term: 1}))
	d := fakeDaemon(t, nodeConfig{ID: "b", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.handle(context.Background(), inbound{From: "a", Msg: message{T: "removed", Seq: 1}})
	if _, err := os.Stat(d.st.etcPath("cluster.json.removed")); err != nil {
		t.Fatal(err)
	}
	s := d.statusNow()
	if s.Mode != ha.Single || !hasEvent(d, "Aus dem Cluster entfernt") || !strings.Contains(strings.Join(s.Warnings, "|"), "Aus dem Cluster entfernt") {
		t.Fatalf("%+v %v", s, eventTexts(d))
	}
	n := len(fn.messages("hb"))
	d.tick(context.Background())
	if len(fn.messages("hb")) != n {
		t.Fatal("Heartbeat nach Entfernen")
	}
}

func TestDaemonWarnings(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "aktiv", Term: 1, Ver: "0.0.0", HostID: "h1", Wall: time.Now().Add(-8 * time.Second)}))
	fn.setView("w", seenView(message{Role: "zeuge", Term: 1, Ver: "test", HostID: "h1", Wall: time.Now()}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, trioCluster(), ha.Persisted{Role: ha.Passive, Term: 1}, fn)
	d.mono = func() time.Duration { return time.Second }
	d.versionOverride = true
	d.tick(context.Background())
	w := strings.Join(d.statusNow().Warnings, "|")
	for _, s := range []string{"Zeuge und b laufen auf derselben Hardware", "Uhrzeit von b weicht um 8 s ab", "Versionen unterschiedlich", "Version überschrieben (Test)"} {
		if !strings.Contains(w, s) {
			t.Errorf("Warnung %q fehlt: %s", s, w)
		}
	}
	if strings.Contains(w, "kein Zeuge") {
		t.Errorf("Trio ohne Zeugen-Warnung erwartet: %s", w)
	}
}

func TestCertRenewal(t *testing.T) {
	stA := store{etc: t.TempDir(), state: t.TempDir()}
	if err := setupFiles(stA, setupOpts{ID: "a", Addrs: []string{"127.0.0.1:7480"}}); err != nil {
		t.Fatal(err)
	}
	caCert, _ := os.ReadFile(stA.etcPath("ca.crt"))
	caKey, _ := os.ReadFile(stA.etcPath("ca.key"))
	keyB, csrB, _ := newKeyCSR("b", "node")
	certB, err := signCSR(caCert, caKey, csrB, "node", 10)
	if err != nil {
		t.Fatal(err)
	}
	cl, _ := stA.loadCluster()
	cl.Nodes = append(cl.Nodes, clusterNode{ID: "b", Kind: "node", Addrs: []string{"127.0.0.2:7480"}, Serial: serialHex(parseCert(t, certB))})
	cl.Version++
	stA.saveCluster(cl)
	stB := store{etc: t.TempDir(), state: t.TempDir()}
	stB.writeEtc("ca.crt", caCert, 0o644)
	stB.writeEtc("node.crt", certB, 0o644)
	stB.writeEtc("node.key", keyB, 0o600)
	stB.saveCluster(cl)
	stB.saveNode(nodeConfig{ID: "b", Kind: "node", Addrs: []string{"127.0.0.2:7480"}})
	stB.saveState(ha.Persisted{Role: ha.Passive, Term: 1})

	fa, fb := newFakeNet(), newFakeNet()
	da := loadFakeDaemon(t, stA, fa)
	db := loadFakeDaemon(t, stB, fb)
	db.checkRenew(time.Now())
	req := fb.messages("renew_req")
	if len(req) != 1 || req[0].to != "a" || req[0].m.CSR == "" {
		t.Fatalf("Anforderung: %+v", req)
	}
	da.handle(context.Background(), inbound{From: "b", Msg: req[0].m})
	resp := fa.messages("renew_resp")
	if len(resp) != 1 || resp[0].to != "b" || resp[0].m.Cert == "" {
		t.Fatalf("Antwort: %+v", resp)
	}
	db.handle(context.Background(), inbound{From: "a", Msg: resp[0].m})
	newCert, _ := os.ReadFile(stB.etcPath("node.crt"))
	c := parseCert(t, newCert)
	if time.Until(c.NotAfter) < 700*24*time.Hour || fb.certs != 1 {
		t.Fatalf("nicht erneuert: %v", c.NotAfter)
	}
	ca, _ := stA.loadCluster()
	if ca.Version != cl.Version+1 || ca.Nodes[1].Serial != serialHex(c) {
		t.Fatalf("Seriennummer nicht nachgetragen: %+v", ca.Nodes[1])
	}
	db.checkRenew(time.Now())
	if len(fb.messages("renew_req")) != 1 {
		t.Fatal("erneut angefordert")
	}
}

// --- drei Daemons im Prozess ---

func scaledSettings() *ha.Settings {
	return &ha.Settings{Tick: testTick, FenceAfter: 500 * time.Millisecond, TakeoverTrio: 1500 * time.Millisecond,
		TakeoverDuo: 3 * time.Second, Cooldown: 0, MaxPerDay: 20}
}

type liveDaemon struct {
	d      *daemon
	cancel context.CancelFunc
	done   chan struct{}
}

func startLive(t *testing.T, st store, ln net.Listener) *liveDaemon {
	t.Helper()
	d := newDaemon(st, openEventLog(st.statePath("events.jsonl"), 5<<20, 3))
	d.version = "test"
	d.fixed = scaledSettings()
	d.ln = ln
	d.pingFn = func(context.Context, string) bool { return true }
	d.gwFn = func() (string, error) { return "127.0.0.1", nil }
	d.ufwFn = func(context.Context, [][]string) error { return nil }
	if err := d.load(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &liveDaemon{d: d, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		d.run(ctx)
	}()
	t.Cleanup(func() { l.stop() })
	return l
}

func (l *liveDaemon) stop() {
	l.cancel()
	<-l.done
}

func TestThreeDaemons(t *testing.T) {
	lns := make([]net.Listener, 3)
	stores := make([]store, 3)
	for i := range lns {
		lns[i] = listenLocal(t)
		dir := t.TempDir()
		stores[i] = store{etc: filepath.Join(dir, "etc"), state: filepath.Join(dir, "state")}
	}
	addr := func(i int) []string { return []string{lns[i].Addr().String()} }
	if err := setupFiles(stores[0], setupOpts{ID: "n1", Addrs: addr(0)}); err != nil {
		t.Fatal(err)
	}
	if err := setupFiles(stores[0], setupOpts{ID: "n1", Addrs: addr(0)}); err == nil {
		t.Fatal("zweites Einrichten nicht abgelehnt")
	}
	d1 := startLive(t, stores[0], lns[0])
	if s := d1.d.statusNow(); s.Mode != ha.Single || s.Self.Role != ha.Active || !s.Maint || !s.CAHolder {
		t.Fatalf("nach Einrichten: %+v", s)
	}
	ctx := context.Background()
	code, _, err := d1.d.issueJoinCode("witness")
	if err == nil || err.Error() != "Zuerst den zweiten Server hinzufügen" {
		t.Fatalf("Zeuge vor zweitem Server: %v", err)
	}
	code, _, err = d1.d.issueJoinCode("node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := joinFiles(ctx, stores[1], code, joinOpts{ID: "n2", Kind: "node", Addrs: addr(1)}); err != nil {
		t.Fatal(err)
	}
	d2 := startLive(t, stores[1], lns[1])
	code, _, err = d1.d.issueJoinCode("witness")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := joinFiles(ctx, stores[2], code, joinOpts{ID: "w", Kind: "witness", Addrs: addr(2)}); err != nil {
		t.Fatal(err)
	}
	w := startLive(t, stores[2], lns[2])

	allSeen := func(l *liveDaemon) bool {
		s := l.d.statusNow()
		if s.Mode != ha.Trio || len(s.Peers) != 2 {
			return false
		}
		for _, p := range s.Peers {
			if !p.Seen {
				return false
			}
		}
		return true
	}
	waitFor(t, 20*time.Second, "Trio", func() bool { return allSeen(d1) && allSeen(d2) && allSeen(w) })
	if err := d1.d.setMaintenance(false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "Wartung aus und frei", func() bool {
		s1, s2 := d1.d.statusNow(), d2.d.statusNow()
		return !s1.Maint && !s2.Maint && s2.Blocked == "" && s1.Self.Role == ha.Active && s2.Self.Role == ha.Passive && s2.Self.Term == 1
	})
	d1.stop()
	waitFor(t, 20*time.Second, "Übernahme durch n2", func() bool {
		s := d2.d.statusNow()
		return s.Self.Role == ha.Active && s.Self.Term == 2
	})
	waitFor(t, 2*time.Second, "Ereignis würde jetzt: promote", func() bool { return hasEvent(d2.d, "würde jetzt: promote") })
	if s := w.d.statusNow(); s.Self.Role != ha.Witness {
		t.Fatalf("Zeuge: %+v", s.Self)
	}
	p, _ := stores[2].loadState()
	if p.VotedTerm != 2 || p.VotedFor != "n2" {
		t.Fatalf("Stimme nicht gespeichert: %+v", p)
	}
}

// Vor dem ersten Takt meldet ein Server im Verbund nie „aktiv“ (I1).
func TestDaemonBootingReportsFenced(t *testing.T) {
	fn := newFakeNet()
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 2}, fn)
	if r := d.statusNow().Self.Role; r != ha.Fenced {
		t.Fatalf("vor dem ersten Takt: %s", r)
	}
	single := clusterConfig{ClusterID: "c1", Version: 1, CAHolder: "a", Settings: ha.DefaultSettings(),
		Nodes: []clusterNode{{ID: "a", Kind: "node", Addrs: []string{"127.0.0.1:7480"}}}}
	d1 := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, single, ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	if r := d1.statusNow().Self.Role; r != ha.Active {
		t.Fatalf("Einzel vor dem ersten Takt: %s", r)
	}
}

// Speicherfehler: der Server gilt bis zum nächsten gelungenen Speichern als
// nicht aktiv – im Status und als Eingabe für den nächsten Schritt (I2).
func TestDaemonSaveFailureNotActive(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 2, Ver: "test"}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 2}, fn)
	d.mu.Lock()
	d.booting = false // Neustart-Regel hier nicht prüfen
	d.mu.Unlock()
	fn.setView("b", seenView(message{Role: "aktiv", Term: 3, Ver: "test"}))
	d.saveFn = func(ha.Persisted) error { return errors.New("Platte voll") }
	d.tick(context.Background())
	if st := d.statusNow(); st.Self.Role != ha.Fenced || st.Self.Term != 2 {
		t.Fatalf("nach Speicherfehler: %s/%d", st.Self.Role, st.Self.Term)
	}
	if !hasEvent(d, "Platte voll") {
		t.Fatalf("Alarm fehlt: %v", eventTexts(d))
	}
	// Partner wieder passiv: mit „aktiv“ als Eingabe bliebe der Schritt aktiv
	// und würde gar nicht speichern – als nicht aktiv wird erneut gespeichert.
	fn.setView("b", seenView(message{Role: "passiv", Term: 2, Ver: "test"}))
	var saved []ha.Persisted
	d.saveFn = func(p ha.Persisted) error { saved = append(saved, p); return d.st.saveState(p) }
	d.tick(context.Background())
	if len(saved) == 0 || saved[0].Role == ha.Active {
		t.Fatalf("gespeichert: %+v", saved)
	}
	if r := d.statusNow().Self.Role; r == ha.Active {
		t.Fatalf("Status nach gelungenem Speichern noch aktiv: %s", r)
	}
	d.mu.Lock()
	unsaved := d.unsaved
	d.mu.Unlock()
	if unsaved {
		t.Fatal("Merker bleibt nach gelungenem Speichern gesetzt")
	}
}
