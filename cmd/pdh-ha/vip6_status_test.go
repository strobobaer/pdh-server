package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// heldWorker6: Arbeiter, der IPv4 und die IPv6-Adresse hält.
func heldWorker6(t *testing.T) *accessWorker {
	t.Helper()
	w, _ := testWorker6(t, card6)
	w.set(true, ha.Active, 1)
	w.step(context.Background())
	if a, _ := w.held6(); !w.held() || a != vip6Want {
		t.Fatalf("held %v, held6 %q", w.held(), a)
	}
	return w
}

func withVIP6(cl clusterConfig) clusterConfig {
	cl = withVIP(cl)
	cl.Access.VIP6Suffix = "15"
	return cl
}

func TestHeartbeatVIP6Field(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test", VIP6Held: "2a02:aaaa:0:1::15"}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, withVIP6(duoCluster()), ha.Persisted{Role: ha.Active, Term: 1}, fn)
	hw := heldWorker6(t) // vor der Sperre: scheitert er, darf d.mu nicht gehalten sein
	d.mu.Lock()
	d.access = hw
	d.views = fn.peers(0)
	m := d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if m.VIP6Held != vip6Want {
		t.Fatalf("Heartbeat vip6_held %q", m.VIP6Held)
	}
	b, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(b), `"vip6_held":"`+vip6Want+`"`) {
		t.Fatalf("JSON %s (%v)", b, err)
	}
	st := d.statusNow()
	if st.Self.VIP6Held != vip6Want || len(st.Peers) != 1 || st.Peers[0].VIP6Held != "2a02:aaaa:0:1::15" {
		t.Fatalf("self %q, peers %+v", st.Self.VIP6Held, st.Peers)
	}
	// älterer Partner ohne das Feld: leer
	var old message
	if err := json.Unmarshal([]byte(`{"t":"hb","role":"passiv","vip":true}`), &old); err != nil || old.VIP6Held != "" {
		t.Fatalf("alter Heartbeat: %q (%v)", old.VIP6Held, err)
	}
	// ohne IPv6 bleibt das Feld weg
	w := heldWorker(t, true)
	d.mu.Lock()
	d.access = w
	m = d.heartbeatLocked(false, nil, time.Now())
	d.mu.Unlock()
	if b, _ := json.Marshal(m); strings.Contains(string(b), "vip6_held") {
		t.Fatalf("JSON ohne IPv6 enthält vip6_held: %s", b)
	}
}

func TestStatusAccessVIP6(t *testing.T) {
	fn := newFakeNet()
	fn.setView("b", seenView(message{Role: "passiv", Term: 1, Ver: "test"}))
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node"}, withVIP6(duoCluster()), ha.Persisted{Role: ha.Active, Term: 1}, fn)
	set := func(w *accessWorker) {
		d.mu.Lock()
		d.access = w
		d.views = fn.peers(0)
		d.mu.Unlock()
	}
	set(heldWorker6(t))
	st := d.statusNow()
	if st.Access.Holder != "a" || st.Access.VIP6 != vip6Want || st.Access.VIP6Note != "" {
		t.Fatalf("Zugang: %+v", st.Access)
	}
	// Partner hält: sein Wert
	set(heldWorker(t, false))
	fn.setView("b", seenView(message{Role: "aktiv", Term: 2, Ver: "test", VIP: true, VIP6Held: "2a02:aaaa:0:1::15"}))
	d.mu.Lock()
	d.views = fn.peers(0)
	d.mu.Unlock()
	if a := d.statusNow().Access; a.Holder != "b" || a.VIP6 != "2a02:aaaa:0:1::15" {
		t.Fatalf("Zugang: %+v", a)
	}
	// Hinweis dieses Servers
	w, _ := testWorker6(t, `2: eth0    inet6 fe80::1/64 scope link \       valid_lft forever preferred_lft forever`)
	w.set(false, ha.Passive, 2)
	w.step(context.Background())
	set(w)
	if a := d.statusNow().Access; a.VIP6Note != "Kein IPv6-Präfix an eth0 – die IPv6-Adresse ist nicht gesetzt" {
		t.Fatalf("Hinweis %q", a.VIP6Note)
	}
}

func TestPrintStatusVIP6(t *testing.T) {
	base := apiStatus{Configured: true, Mode: ha.Duo, Self: apiNode{ID: "node1", Kind: "node", Role: ha.Passive}}
	for _, c := range []struct {
		a       apiAccess
		want    string
		without string
	}{
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, VIP6Suffix: "15", Holder: "node2", VIP6: vip6Want}, "Adresse " + vip6Want + ": bei node2\n", ""},
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, VIP6Suffix: "15", Holder: "node1", VIP6: vip6Want}, "Adresse " + vip6Want + ": hier\n", ""},
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, VIP6Suffix: "15", Holder: "node1"}, "IPv6-Adresse (Ende 15): niemand\n", ""},
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, VIP6Suffix: "15", VIP6Note: "Kein IPv6-Präfix an eth0 – die IPv6-Adresse ist nicht gesetzt"},
			"Hinweis: Kein IPv6-Präfix an eth0 – die IPv6-Adresse ist nicht gesetzt\n", ""},
		{apiAccess{VIP: "10.77.1.50/24", VIPOn: true, Holder: "node1"}, "Adresse 10.77.1.50: hier\n", "IPv6"},
	} {
		st := base
		st.Access = c.a
		var b strings.Builder
		printStatus(&b, st)
		if !strings.Contains(b.String(), c.want) {
			t.Errorf("Ausgabe ohne %q:\n%s", c.want, b.String())
		}
		if c.without != "" && strings.Contains(b.String(), c.without) {
			t.Errorf("Ausgabe mit %q:\n%s", c.without, b.String())
		}
	}
}

// Verdrahtung im Dienst: Ende gesetzt → IPv6 folgt; Ende geändert oder
// geleert → alte Adresse weg, IPv4 bleibt dabei unberührt.
func TestDaemonAccessVIP6(t *testing.T) {
	d, _ := accessDaemon(t, fakeCards("eth0=10.77.1.5/24"))
	f := newDualFake(card6)
	d.lock()
	d.sys = f
	d.na6 = nil // keine echte Ankündigung im Test
	d.cl.Access.VIP6Suffix = "15"
	d.unlock()
	ctx := stopAccess(t, d)
	d.tick(ctx)
	d.tick(ctx) // R59: zweiter Takt nach dem Start
	waitFor(t, 2*time.Second, "IPv6 übernommen", func() bool { return f.count(vip6Add) == 1 })
	waitFor(t, 2*time.Second, "Status nennt IPv6", func() bool { return d.statusNow().Access.VIP6 == vip6Want })
	d.mu.Lock()
	w := d.access
	d.mu.Unlock()
	// Ende geändert
	d.lock()
	d.cl.Access.VIP6Suffix = "16"
	d.unlock()
	d.tick(ctx)
	waitFor(t, 2*time.Second, "neues Ende", func() bool {
		return f.count("ip -6 addr add 2a02:f90:e198:1700::16/128 dev eth0") == 1 && f.count(vip6Del) == 1
	})
	// Ende geleert
	d.lock()
	d.cl.Access.VIP6Suffix = ""
	d.unlock()
	d.tick(ctx)
	waitFor(t, 2*time.Second, "Ende geleert", func() bool {
		return f.count("ip -6 addr del 2a02:f90:e198:1700::16/128 dev eth0") == 1
	})
	d.mu.Lock()
	same := d.access == w
	d.mu.Unlock()
	if !same || f.count(vipDel) != 0 || f.count(vipAdd) != 1 || !w.held() {
		t.Fatalf("IPv4 berührt: gleicher Arbeiter %v, %q", same, f.calls)
	}
	n := f.count("ip -6")
	d.tick(ctx)
	time.Sleep(50 * time.Millisecond)
	if m := f.count("ip -6"); m != n {
		t.Fatalf("nach dem Leeren noch ip -6 (%d → %d)", n, m)
	}
}
