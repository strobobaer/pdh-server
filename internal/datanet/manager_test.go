package datanet_test

import (
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

func waitState(t *testing.T, m *datanet.Manager, want datanet.State, d time.Duration) datanet.Status {
	t.Helper()
	end := time.Now().Add(d)
	for {
		st := m.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(end) {
			t.Fatalf("Zustand %q statt %q (%s)", st.State, want, st.Err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestManagerStartNeverBlocks(t *testing.T) {
	m := datanet.NewManager(func(datanet.NodeConfig) (datanet.Node, error) {
		return &nodetest.Fake{BlockUp: true}, nil
	}, 200*time.Millisecond)
	t0 := time.Now()
	m.Start(datanet.NodeConfig{Dir: t.TempDir(), AuthKey: "k"}, func(datanet.Node) {})
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Fatalf("Start blockierte %s", d)
	}
	if st := m.Status().State; st != datanet.StateStarting {
		t.Fatalf("Zustand %q", st)
	}
	st := waitState(t, m, datanet.StateError, 3*time.Second)
	if !strings.Contains(st.Err, "Zeitgrenze") {
		t.Fatalf("Fehlertext %q", st.Err)
	}
	if _, ok := m.Node(); ok {
		t.Fatal("Node() nur bei running")
	}
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hängt")
	}
}

func TestManagerNeedsKey(t *testing.T) {
	var calls atomic.Int32
	m := datanet.NewManager(func(datanet.NodeConfig) (datanet.Node, error) {
		calls.Add(1)
		return &nodetest.Fake{}, nil
	}, time.Second)
	m.Start(datanet.NodeConfig{Dir: t.TempDir()}, func(datanet.Node) {})
	defer m.Stop()
	st := m.Status()
	if st.State != datanet.StateNeedsKey {
		t.Fatalf("Zustand %q", st.State)
	}
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("Fabrik darf nicht aufgerufen werden")
	}
}

func TestManagerRunning(t *testing.T) {
	addr := netip.MustParseAddr("100.64.0.1")
	fake := &nodetest.Fake{Addrs: []netip.Addr{addr}}
	var ups atomic.Int32
	got := make(chan datanet.Node, 2)
	m := datanet.NewManager(func(datanet.NodeConfig) (datanet.Node, error) { return fake, nil }, time.Second)
	m.Start(datanet.NodeConfig{Dir: t.TempDir(), AuthKey: "k"}, func(n datanet.Node) {
		ups.Add(1)
		got <- n
	})
	defer m.Stop()
	st := waitState(t, m, datanet.StateRunning, 2*time.Second)
	if len(st.Addrs) != 1 || st.Addrs[0] != addr {
		t.Fatalf("Adressen %v", st.Addrs)
	}
	select {
	case n := <-got:
		if n != datanet.Node(fake) {
			t.Fatal("falscher Knoten")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onUp nicht gerufen")
	}
	if n, ok := m.Node(); !ok || n != datanet.Node(fake) {
		t.Fatal("Node() liefert den Knoten nicht")
	}
	time.Sleep(50 * time.Millisecond)
	if ups.Load() != 1 {
		t.Fatalf("onUp %d-mal", ups.Load())
	}
}
