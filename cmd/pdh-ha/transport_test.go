package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdh/internal/ha"
)

const testTick = 50 * time.Millisecond

type testCA struct {
	cert, key []byte
	pool      *x509.CertPool
}

func newTestCA(t *testing.T, clusterID string) testCA {
	t.Helper()
	c, k, err := newCA(clusterID)
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(c)
	return testCA{cert: c, key: k, pool: p}
}

func (c testCA) tlsCert(t *testing.T, id, kind string) (tls.Certificate, []byte) {
	t.Helper()
	certPEM, keyPEM := issueNode(t, c.cert, c.key, id, kind)
	tc, err := loadTLSCert(certPEM, keyPEM, c.cert)
	if err != nil {
		t.Fatal(err)
	}
	return tc, certPEM
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func testMono() func() time.Duration {
	start := time.Now()
	return func() time.Duration { return time.Since(start) + time.Millisecond }
}

func testEvents(t *testing.T) *eventLog {
	return openEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 5<<20, 3)
}

// waitFor fragt cond bis zur Frist ab (keine festen Wartezeiten).
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Zeitüberschreitung: %s", what)
}

func testSettings() ha.Settings {
	s := ha.DefaultSettings()
	s.Tick = testTick
	return s
}

// beat sendet bis ctx endet im Takt Heartbeats.
func beat(ctx context.Context, tr *transport, id string) {
	var seq uint64
	tk := time.NewTicker(testTick)
	defer tk.Stop()
	for {
		seq++
		tr.broadcast(message{T: "hb", ID: id, Role: "passiv", Seq: seq, Wall: time.Now()})
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

func pathAlive(v peerView, addr string) bool {
	for _, p := range v.Paths {
		if p.Addr == addr {
			return p.Alive
		}
	}
	return false
}

func TestShouldDial(t *testing.T) {
	cases := []struct {
		self nodeConfig
		peer clusterNode
		want bool
	}{
		{nodeConfig{ID: "a", Kind: "node"}, clusterNode{ID: "b", Kind: "node"}, true},
		{nodeConfig{ID: "b", Kind: "node"}, clusterNode{ID: "a", Kind: "node"}, false},
		{nodeConfig{ID: "z", Kind: "node"}, clusterNode{ID: "a", Kind: "witness"}, true},
		{nodeConfig{ID: "a", Kind: "witness"}, clusterNode{ID: "z", Kind: "node"}, false},
	}
	for _, c := range cases {
		if got := shouldDial(c.self, c.peer); got != c.want {
			t.Errorf("%s→%s: %v", c.self.ID, c.peer.ID, got)
		}
	}
}

func TestMessageJSONNames(t *testing.T) {
	b, _ := json.Marshal(message{T: "hb", ID: "n1", DataCurrent: true, MaintSeq: 3, ClusterVer: 2, HostID: "h", Sees: []string{"n2"}, VotedFor: "n2"})
	s := string(b)
	for _, k := range []string{`"t":"hb"`, `"id":"n1"`, `"role"`, `"term"`, `"ver"`, `"commit"`, `"data_current":true`, `"data_assumed"`,
		`"gateway"`, `"maint"`, `"pg"`, `"pdh"`, `"disk"`, `"seq"`, `"wall"`, `"hostid":"h"`, `"mseq":3`, `"cver":2`, `"sees":["n2"]`, `"voted_for":"n2"`} {
		if !strings.Contains(s, k) {
			t.Errorf("%s fehlt in %s", k, s)
		}
	}
}

func TestTransportHeartbeat(t *testing.T) {
	ca := newTestCA(t, "c1")
	certA, _ := ca.tlsCert(t, "a", "node")
	certB, _ := ca.tlsCert(t, "b", "node")
	lnA, lnB1, lnB2 := listenLocal(t), listenLocal(t), listenLocal(t)
	b1, b2 := lnB1.Addr().String(), lnB2.Addr().String()
	cl := clusterConfig{ClusterID: "c1", Version: 1, Settings: testSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{lnA.Addr().String()}},
		{ID: "b", Kind: "node", Addrs: []string{b1, b2}},
	}}
	ta := newTransport(nodeConfig{ID: "a", Kind: "node"}, cl, certA, ca.pool, testMono(), testEvents(t))
	tb := newTransport(nodeConfig{ID: "b", Kind: "node", Addrs: []string{b1, b2}}, cl, certB, ca.pool, testMono(), testEvents(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ta.serve(ctx, lnA, nil)
	go tb.serve(ctx, lnB1, nil)
	go tb.serve(ctx, lnB2, nil)
	go beat(ctx, ta, "a")
	go beat(ctx, tb, "b")

	waitFor(t, 5*time.Second, "beide Wege lebendig", func() bool {
		va, vb := ta.peers(testTick)["b"], tb.peers(testTick)["a"]
		return pathAlive(va, b1) && pathAlive(va, b2) && pathAlive(vb, b1) && pathAlive(vb, b2)
	})
	v := ta.peers(testTick)["b"]
	if v.Last.ID != "b" || v.Last.Role != "passiv" || v.LastSeen == 0 || !v.seen() {
		t.Fatalf("Ansicht: %+v", v)
	}

	// Nicht-hb-Nachrichten werden zugestellt, hb nicht
	ta.sendTo("b", message{T: "vote_req", ID: "a", VoteTerm: 5, Seq: 1 << 40})
	select {
	case in := <-tb.incoming():
		if in.From != "a" || in.Msg.T != "vote_req" || in.Msg.VoteTerm != 5 {
			t.Fatalf("eingang: %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("vote_req nicht angekommen")
	}

	lnB2.Close()
	waitFor(t, 5*time.Second, "Weg 2 tot, Weg 1 lebendig", func() bool {
		va, vb := ta.peers(testTick)["b"], tb.peers(testTick)["a"]
		return !pathAlive(va, b2) && pathAlive(va, b1) && !pathAlive(vb, b2) && pathAlive(vb, b1)
	})
	if !ta.peers(testTick)["b"].seen() {
		t.Fatal("Gegenstelle sollte weiter gesehen werden")
	}
}

// stalledPeer ist eine Gegenstelle "b", die einmal einen Heartbeat sendet und dann schweigt.
func TestTransportStalledPeer(t *testing.T) {
	ca := newTestCA(t, "c1")
	certA, _ := ca.tlsCert(t, "a", "node")
	certB, _ := ca.tlsCert(t, "b", "node")
	lnA, lnB := listenLocal(t), listenLocal(t)
	addrB := lnB.Addr().String()
	var accepted atomic.Int32
	closed := make(chan struct{}, 10)
	tl := tls.NewListener(lnB, &tls.Config{Certificates: []tls.Certificate{certB}, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: ca.pool, NextProtos: []string{"pdh-ha/1"}, MinVersion: tls.VersionTLS13})
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				b, _ := json.Marshal(message{T: "hb", ID: "b", Seq: 1})
				c.Write(append(b, '\n'))
				r := bufio.NewReader(c)
				for {
					if _, err := r.ReadByte(); err != nil {
						closed <- struct{}{}
						return
					}
				}
			}(c)
		}
	}()
	cl := clusterConfig{ClusterID: "c1", Settings: testSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{lnA.Addr().String()}},
		{ID: "b", Kind: "node", Addrs: []string{addrB}},
	}}
	ta := newTransport(nodeConfig{ID: "a", Kind: "node"}, cl, certA, ca.pool, testMono(), testEvents(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ta.serve(ctx, lnA, nil)
	go beat(ctx, ta, "a")

	waitFor(t, 5*time.Second, "Weg lebendig", func() bool { return pathAlive(ta.peers(testTick)["b"], addrB) })
	waitFor(t, 5*time.Second, "Weg tot", func() bool { return !pathAlive(ta.peers(testTick)["b"], addrB) })
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("schweigende Verbindung nicht geschlossen")
	}
	if p := ta.peers(testTick)["b"].Paths[0]; p.Err != "Keine Nachricht seit 3 Takten" {
		t.Errorf("Fehlertext: %+v", p)
	}
	waitFor(t, 8*time.Second, "Neuaufbau", func() bool { return accepted.Load() >= 2 })
}

func TestTransportRejectsForeign(t *testing.T) {
	ca := newTestCA(t, "c1")
	other := newTestCA(t, "c2")
	certB, _ := ca.tlsCert(t, "b", "node")
	certA, pemA := ca.tlsCert(t, "a", "node")
	foreignA, _ := other.tlsCert(t, "a", "node")
	strangerX, _ := ca.tlsCert(t, "x", "node")
	lnB := listenLocal(t)
	cl := clusterConfig{ClusterID: "c1", Settings: testSettings(), Revoked: []string{serialHex(parseCert(t, pemA))}, Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{"127.0.0.1:1"}},
		{ID: "b", Kind: "node", Addrs: []string{lnB.Addr().String()}},
	}}
	ev := testEvents(t)
	tb := newTransport(nodeConfig{ID: "b", Kind: "node"}, cl, certB, ca.pool, testMono(), ev)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tb.serve(ctx, lnB, nil)

	try := func(cert *tls.Certificate) {
		cfg := &tls.Config{RootCAs: ca.pool, ServerName: "b", NextProtos: []string{"pdh-ha/1"}, MinVersion: tls.VersionTLS13}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		c, err := tls.Dial("tcp", lnB.Addr().String(), cfg)
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := json.Marshal(message{T: "vote_req", ID: "a", Seq: 7})
		hb, _ := json.Marshal(message{T: "hb", ID: "a", Seq: 8})
		c.Write(append(append(b, '\n'), append(hb, '\n')...))
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		bufio.NewReader(c).ReadByte() // wartet auf das Schließen durch b
	}
	try(&foreignA)  // fremde CA
	try(&certA)     // gesperrt
	try(nil)        // ohne Zertifikat
	try(&strangerX) // nicht im Cluster
	waitFor(t, 5*time.Second, "vier Ablehnungen", func() bool { return tb.rejects.Load() >= 4 })

	n := 0
	for _, e := range ev.since(0, 0) {
		if e.Text == "Verbindung abgelehnt: 127.0.0.1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Ereignis %d-mal statt einmal: %+v", n, ev.since(0, 0))
	}
	select {
	case in := <-tb.incoming():
		t.Fatalf("Nachricht trotz Ablehnung: %+v", in)
	default:
	}
	if v := tb.peers(testTick)["a"]; v.seen() || v.LastSeen != 0 {
		t.Fatalf("abgelehnte Gegenstelle gesehen: %+v", v)
	}
}

type joinEnv struct {
	st    store
	ca    testCA
	codes *joinCodes
	ta    *transport
	addr  string
	mu    sync.Mutex
	got   []clusterConfig
}

func newJoinEnv(t *testing.T) *joinEnv {
	t.Helper()
	e := &joinEnv{st: store{etc: t.TempDir(), state: t.TempDir()}, ca: newTestCA(t, "c1"), codes: &joinCodes{}}
	if err := e.st.writeEtc("ca.crt", e.ca.cert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.st.writeEtc("ca.key", e.ca.key, 0o600); err != nil {
		t.Fatal(err)
	}
	ln := listenLocal(t)
	e.addr = ln.Addr().String()
	cl := clusterConfig{ClusterID: "c1", Version: 1, Settings: testSettings(), Nodes: []clusterNode{{ID: "a", Kind: "node", Addrs: []string{e.addr}}}}
	if err := e.st.saveCluster(cl); err != nil {
		t.Fatal(err)
	}
	certA, _ := e.ca.tlsCert(t, "a", "node")
	e.ta = newTransport(nodeConfig{ID: "a", Kind: "node", Addrs: []string{e.addr}, CAHolder: true}, cl, certA, e.ca.pool, testMono(), testEvents(t))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.ta.serve(ctx, ln, joinHandler(e.st, e.codes, func(c clusterConfig) {
		e.mu.Lock()
		e.got = append(e.got, c)
		e.mu.Unlock()
		e.ta.setCluster(c)
	}))
	return e
}

func (e *joinEnv) code(t *testing.T, role string) joinCode {
	t.Helper()
	sec, err := e.codes.issue(role, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return joinCode{Addrs: []string{e.addr}, FP: fingerprint(e.ca.cert), Secret: sec, Role: role, Cluster: "c1"}
}

func TestJoinFlow(t *testing.T) {
	e := newJoinEnv(t)
	ctx := context.Background()
	code := e.code(t, "node")
	keyB, csrB, _ := newKeyCSR("b", "node")
	lnB := listenLocal(t)
	req := joinRequest{ID: "b", CSR: string(csrB), Addrs: []string{lnB.Addr().String()}}

	bad := code
	bad.FP = strings.Repeat("0", 64)
	if _, err := joinRemote(ctx, bad, req); err == nil || err.Error() != "Fingerabdruck der Zertifizierungsstelle stimmt nicht" {
		t.Fatalf("falscher Fingerabdruck: %v", err)
	}

	resp, err := joinRemote(ctx, code, req) // Code blieb unbenutzt
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, []byte(resp.Cert))
	if cert.Subject.CommonName != "b" || cert.Subject.OrganizationalUnit[0] != "node" || resp.CA != string(e.ca.cert) {
		t.Fatalf("Antwort: %v", cert.Subject)
	}
	if resp.Cluster.Version != 2 || len(resp.Cluster.Nodes) != 2 || resp.Cluster.Nodes[1].ID != "b" ||
		resp.Cluster.Nodes[1].Serial != serialHex(cert) || resp.Cluster.Nodes[1].Addrs[0] != lnB.Addr().String() {
		t.Fatalf("Cluster: %+v", resp.Cluster)
	}
	stored, _ := e.st.loadCluster()
	if stored.Version != 2 || len(stored.Nodes) != 2 {
		t.Fatalf("gespeichert: %+v", stored)
	}
	e.mu.Lock()
	if len(e.got) != 1 || e.got[0].Version != 2 {
		t.Fatalf("onJoin: %+v", e.got)
	}
	e.mu.Unlock()

	if _, err := joinRemote(ctx, code, req); err == nil || err.Error() != "Beitrittscode bereits benutzt" {
		t.Fatalf("zweites Mal: %v", err)
	}
	dup := e.code(t, "node")
	_, csrA, _ := newKeyCSR("a", "node")
	if _, err := joinRemote(ctx, dup, joinRequest{ID: "a", CSR: string(csrA), Addrs: []string{"127.0.0.1:2"}}); err == nil || err.Error() != "Kennung a ist bereits im Cluster" {
		t.Fatalf("doppelt: %v", err)
	}

	// Der neue Knoten nimmt mit dem erhaltenen Zertifikat am Heartbeat teil.
	certB, err := loadTLSCert([]byte(resp.Cert), keyB, []byte(resp.CA))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(resp.CA))
	tb := newTransport(nodeConfig{ID: "b", Kind: "node"}, resp.Cluster, certB, pool, testMono(), testEvents(t))
	bctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tb.serve(bctx, lnB, nil)
	go beat(bctx, tb, "b")
	go beat(bctx, e.ta, "a")
	waitFor(t, 5*time.Second, "Heartbeat nach Beitritt", func() bool {
		return e.ta.peers(testTick)["b"].seen() && tb.peers(testTick)["a"].seen()
	})
}

func TestJoinWitnessNeedsTwoNodes(t *testing.T) {
	e := newJoinEnv(t)
	_, csr, _ := newKeyCSR("zeuge", "witness")
	_, err := joinRemote(context.Background(), e.code(t, "witness"), joinRequest{ID: "zeuge", CSR: string(csr), Addrs: []string{"127.0.0.1:3"}})
	if err == nil || err.Error() != "Zuerst den zweiten Server hinzufügen" {
		t.Fatalf("Zeuge: %v", err)
	}
	if c, _ := e.st.loadCluster(); c.Version != 1 || len(c.Nodes) != 1 {
		t.Fatalf("Cluster verändert: %+v", c)
	}
}

func TestTransportLongLine(t *testing.T) {
	ca := newTestCA(t, "c1")
	certA, _ := ca.tlsCert(t, "a", "node")
	certB, _ := ca.tlsCert(t, "b", "node")
	lnB := listenLocal(t)
	closed := make(chan struct{}, 10)
	tl := tls.NewListener(lnB, &tls.Config{Certificates: []tls.Certificate{certB}, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: ca.pool, NextProtos: []string{"pdh-ha/1"}, MinVersion: tls.VersionTLS13})
	go func() {
		c, err := tl.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte(`{"t":"hb","pg":"` + strings.Repeat("x", 70<<10) + "\"}\n"))
		bufio.NewReader(c).ReadByte()
		closed <- struct{}{}
	}()
	cl := clusterConfig{ClusterID: "c1", Settings: testSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node"}, {ID: "b", Kind: "node", Addrs: []string{lnB.Addr().String()}},
	}}
	ta := newTransport(nodeConfig{ID: "a", Kind: "node"}, cl, certA, ca.pool, testMono(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ta.serve(ctx, listenLocal(t), nil)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Verbindung nicht geschlossen")
	}
	v := ta.peers(testTick)["b"]
	if v.Paths[0].Err != "Zeile zu lang, Verbindung geschlossen" || v.LastSeen != 0 {
		t.Fatalf("%+v", v)
	}
}
