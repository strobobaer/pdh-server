package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
	"pdh/internal/hostnet"
)

func TestHeartbeatNetField(t *testing.T) {
	own := &hostnet.Summary{Iface: "eth0", Addr: "192.0.2.17/24", Mode: hostnet.ModeStatic, Gateway: "192.0.2.1",
		DNS: []string{"192.0.2.1"}, Netplan: true}
	peer := &hostnet.Summary{Iface: "eth0", Addr: "192.0.2.14/24", Mode: hostnet.ModeDHCP}
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", Net: peer}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1}, fn)
	d.ownNet.Store(own)
	d.mu.Lock()
	d.views = fn.peers(0)
	m := d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if !reflect.DeepEqual(m.Net, own) {
		t.Fatalf("Heartbeat net %+v", m.Net)
	}
	b, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(b), `"net":{`) {
		t.Fatalf("JSON %s (%v)", b, err)
	}
	st := d.statusNow()
	if st.Self.Net == nil || st.Self.Net.Addr != "192.0.2.17/24" || len(st.Peers) != 1 || st.Peers[0].Net == nil || st.Peers[0].Net.Addr != "192.0.2.14/24" {
		t.Fatalf("self %+v, peers %+v", st.Self.Net, st.Peers)
	}
	if sb, _ := json.Marshal(st); !strings.Contains(string(sb), `"net":{`) {
		t.Fatalf("Status-JSON ohne net: %s", sb)
	}
	// älterer Partner ohne das Feld: nil
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
	d.mu.Lock()
	d.views = fn.peers(0)
	d.mu.Unlock()
	if st := d.statusNow(); len(st.Peers) != 1 || st.Peers[0].Net != nil {
		t.Fatalf("Partner ohne Feld: %+v", st.Peers)
	}
	var old message
	if err := json.Unmarshal([]byte(`{"t":"hb","role":"passiv","vip":true}`), &old); err != nil || old.Net != nil {
		t.Fatalf("alter Heartbeat: %+v (%v)", old.Net, err)
	}
	// noch nichts gelesen: das Feld bleibt weg
	d.ownNet.Store(nil)
	d.mu.Lock()
	m = d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if b, _ := json.Marshal(m); strings.Contains(string(b), `"net"`) {
		t.Fatalf("JSON ohne Messwert enthält net: %s", b)
	}
}

func TestNetLoopCollects(t *testing.T) {
	var calls atomic.Int32
	collect := func(context.Context) hostnet.Summary {
		calls.Add(1)
		return hostnet.Summary{Addr: "10.0.0.5/24", Mode: hostnet.ModeStatic}
	}
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	d.netCollect = collect
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.netLoop(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for d.ownNet.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := d.ownNet.Load(); s == nil || s.Addr != "10.0.0.5/24" || calls.Load() < 1 {
		t.Fatalf("ownNet %+v, Aufrufe %d", s, calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("netLoop endet nicht mit dem Kontext")
	}

	// Zeuge: nie gerufen
	calls.Store(0)
	w := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, duoCluster(), ha.Persisted{Term: 1}, newFakeNet())
	w.netCollect = collect
	wctx, wcancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer wcancel()
	w.netLoop(wctx)
	if calls.Load() != 0 || w.ownNet.Load() != nil {
		t.Fatalf("Zeuge hat gelesen: %d", calls.Load())
	}
}

// Start ohne Einrichtung, Einrichtung später im laufenden Prozess: die
// Schleife darf nicht enden und muss danach lesen (auch unter -race ohne Wettlauf).
func TestNetLoopAfterLateSetup(t *testing.T) {
	old := netWaitEvery
	netWaitEvery = 10 * time.Millisecond
	t.Cleanup(func() { netWaitEvery = old })
	st := store{etc: t.TempDir(), state: t.TempDir()}
	d := loadFakeDaemon(t, st, newFakeNet())
	var calls atomic.Int32
	d.netCollect = func(context.Context) hostnet.Summary {
		calls.Add(1)
		return hostnet.Summary{Addr: "10.0.0.5/24", Mode: hostnet.ModeStatic}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.netLoop(ctx); close(done) }()
	time.Sleep(60 * time.Millisecond)
	if calls.Load() != 0 || d.ownNet.Load() != nil {
		t.Fatalf("vor der Einrichtung gelesen: %d", calls.Load())
	}
	if err := st.saveNode(nodeConfig{ID: "a", Kind: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := st.saveCluster(duoCluster()); err != nil {
		t.Fatal(err)
	}
	if err := st.saveState(ha.Persisted{Role: ha.Active, Term: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.load(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for d.ownNet.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := d.ownNet.Load(); s == nil || s.Addr != "10.0.0.5/24" {
		t.Fatalf("nach der Einrichtung nicht gelesen: %+v", s)
	}
	cancel()
	<-done
}

// Die gemeinsame Adresse darf nie als eigene erscheinen, auch nicht im
// alten Zustand „gemeinsame Adresse mit Netzpräfix vorn, echte Adresse secondary“.
func TestNetLoopExcludesSharedAddr(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, withVIP(duoCluster()), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	d.netCollect = nil
	f := &fakeRunner{}
	f.on("ip -4 route show default", "", "default via 10.77.1.1 dev eth0 proto static\n", nil)
	f.on("ip -o addr show dev eth0", "", ipShowOldFront, nil)
	d.sys = f
	d.mu.Lock()
	ex := d.netExcludeLocked()
	d.mu.Unlock()
	s := d.collectNet(context.Background(), ex...)
	if s.Addr != "10.77.1.7/24" {
		t.Fatalf("eigene Adresse %q (gemeinsame 10.77.1.50 darf es nicht sein)", s.Addr)
	}
}

// Ohne netCollect läuft hostnet.Collect über d.sys (kein echter Befehl).
func TestCollectNetUsesSys(t *testing.T) {
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, duoCluster(), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	d.netCollect = nil
	f := &fakeRunner{}
	f.on("ip -4 route show default", "", "default via 192.0.2.1 dev eth0 proto dhcp\n", nil)
	d.sys = f
	s := d.collectNet(context.Background())
	if s.Gateway != "192.0.2.1" || s.Iface != "eth0" {
		t.Fatalf("%+v", s)
	}
}
