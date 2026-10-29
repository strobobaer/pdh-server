package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"pdh/internal/ha"
)

// ohne Wiederholung, sonst warten die Fehlerfälle je 15 s
func init() { probeRetryMax = 0 }

// vipCheckCluster: Server a (CA-Halter) und b, Zeuge w, alle im Netz 10.77.1.0/24.
func vipCheckCluster() clusterConfig {
	return clusterConfig{ClusterID: "c1", Version: 3, CAHolder: "a", Settings: realSettings(), Nodes: []clusterNode{
		{ID: "a", Kind: "node", Addrs: []string{"10.77.1.5:7480"}},
		{ID: "b", Kind: "node", Addrs: []string{"10.77.1.6:7480"}},
		{ID: "w", Kind: "witness", Addrs: []string{"10.77.1.7:7480"}},
	}}
}

// fakeProbe spielt Partner und Zeugen: b hält die Prüfadresse über
// probe-hold, jeder Prüfende fragt, wer sie gerade hat (a über vipState).
type fakeProbe struct {
	mu    sync.Mutex
	a     *vipState
	bHeld bool
	calls []string
	fail  map[string]string // "Halter>Prüfender" → Fehlertext
}

func (f *fakeProbe) holder() string {
	f.a.mu.Lock()
	aHeld := f.a.held
	f.a.mu.Unlock()
	switch {
	case aHeld && f.bHeld:
		return "a+b"
	case aHeld:
		return "a"
	case f.bHeld:
		return "b"
	}
	return "niemand"
}

func (f *fakeProbe) check(checker, expect string) error {
	if msg, ok := f.fail[expect+">"+checker]; ok {
		return errors.New(msg)
	}
	if h := f.holder(); h != expect {
		return fmt.Errorf("Adresse gehört zu %s", h)
	}
	return nil
}

func (f *fakeProbe) post(_ context.Context, peer clusterNode, path string, in, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, peer.ID+" "+path)
	resp := out.(*probeResp)
	switch r := in.(type) {
	case probeHoldReq:
		if peer.ID != "b" || r.Addr != "10.77.1.50/24" || r.Seconds != probeHoldMax {
			return fmt.Errorf("unerwartet: %s %+v", peer.ID, r)
		}
		f.bHeld = true
		*resp = probeResp{OK: true, Iface: "ens18"}
	case probeReleaseReq:
		f.bHeld = false
		*resp = probeResp{OK: true}
	case probeAddrReq:
		if r.Addr != "10.77.1.50" {
			return fmt.Errorf("Adresse %q", r.Addr)
		}
		if err := f.check(peer.ID, r.Expect); err != nil {
			*resp = probeResp{Err: err.Error()}
		} else {
			*resp = probeResp{OK: true}
		}
	default:
		return fmt.Errorf("unbekannte Anfrage %T", in)
	}
	return nil
}

func (f *fakeProbe) count(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// checkDaemon: Knoten a (CA-Halter, aktiv) mit Fakes für Halten und Prüfen.
func checkDaemon(t *testing.T, dup bool) (*daemon, *vipState, *fakeProbe) {
	t.Helper()
	d := fakeDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, vipCheckCluster(), ha.Persisted{Role: ha.Active, Term: 1}, newFakeNet())
	d.ifAddrsFn = fakeCards("eth0=10.77.1.5/24")
	vs := &vipState{dup: dup}
	d.sys = vs
	fp := &fakeProbe{a: vs, fail: map[string]string{}}
	d.peerPostFn = fp.post
	d.probeAddrFn = func(_ context.Context, hostport, expect string) error {
		if hostport != "10.77.1.50:7480" {
			return fmt.Errorf("Ziel %q", hostport)
		}
		fp.mu.Lock()
		defer fp.mu.Unlock()
		fp.calls = append(fp.calls, "a lokal")
		return fp.check("a", expect)
	}
	t.Cleanup(d.stopProbeHold)
	return d, vs, fp
}

func testAddr(t *testing.T) netip.Prefix {
	t.Helper()
	p, err := parseVIP("10.77.1.50/24")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunVIPCheckBothWays(t *testing.T) {
	d, vs, fp := checkDaemon(t, false)
	ver := d.cl.Version
	res, err := d.runVIPCheck(context.Background(), testAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []vipCheckResult{
		{Holder: "a", Checker: "b", OK: true},
		{Holder: "a", Checker: "w", OK: true},
		{Holder: "b", Checker: "a", OK: true},
		{Holder: "b", Checker: "w", OK: true},
	}
	if fmt.Sprint(res) != fmt.Sprint(want) {
		t.Fatalf("Ergebnisse:\n%+v\nstatt\n%+v", res, want)
	}
	if vs.count(vipAdd) != 1 || vs.count(vipDel) != 1 {
		t.Fatalf("eigene Prüfadresse: %v", vs.calls)
	}
	if fp.count("probe-hold") != 1 || fp.count("probe-release") != 1 || fp.bHeld {
		t.Fatalf("Partner: %v", fp.calls)
	}
	d.mu.Lock()
	cl := d.cl
	d.mu.Unlock()
	tt := cl.Access.Tested
	if tt == nil || tt.Net != "10.77.1.0/24" || tt.Ifaces["a"] != "eth0" || tt.Ifaces["b"] != "ens18" || len(tt.Ifaces) != 2 || tt.At.IsZero() {
		t.Fatalf("Tested: %+v", tt)
	}
	if cl.Version != ver+1 {
		t.Fatalf("Version %d statt %d", cl.Version, ver+1)
	}
	if saved, _ := d.st.loadCluster(); saved.Access.Tested == nil {
		t.Fatal("Test nicht gespeichert")
	}
	if len(d.net.(*fakeNet).messages("cluster")) == 0 {
		t.Fatal("Test nicht verteilt")
	}
	// gilt für jede Adresse im Netz auf denselben Karten
	if !tt.covers(netip.MustParsePrefix("10.77.1.202/24"), map[string]string{"a": "eth0"}) {
		t.Fatal("Test gilt nicht für das Netz")
	}
}

func TestRunVIPCheckBusy(t *testing.T) {
	d, vs, fp := checkDaemon(t, true)
	_, err := d.runVIPCheck(context.Background(), testAddr(t))
	if err == nil || !strings.Contains(err.Error(), "Prüfadresse ist belegt") {
		t.Fatalf("Fehler: %v", err)
	}
	if n := vs.count("ip addr add"); n != 0 {
		t.Fatalf("trotz Belegung angenommen: %v", vs.calls)
	}
	if len(fp.calls) != 0 {
		t.Fatalf("Partner gefragt: %v", fp.calls)
	}
	if d.cl.Access.Tested != nil {
		t.Fatal("Tested gesetzt")
	}
}

func TestRunVIPCheckOwnAddressBusy(t *testing.T) {
	d, vs, fp := checkDaemon(t, false)
	// die Adresse liegt schon auf einer eigenen Karte (z. B. die mitwandernde)
	d.ifAddrsFn = fakeCards("eth0=10.77.1.5/24", "eth0=10.77.1.50/24")
	_, err := d.runVIPCheck(context.Background(), testAddr(t))
	if err == nil || !strings.Contains(err.Error(), "Prüfadresse ist belegt") {
		t.Fatalf("Fehler: %v", err)
	}
	if vs.count("ip addr") != 0 || len(fp.calls) != 0 {
		t.Fatalf("trotzdem gearbeitet: %v %v", vs.calls, fp.calls)
	}
}

// Prüfadresse gleich der eingeschalteten mitwandernden Adresse → abgelehnt,
// am Auslöser wie am haltenden Partner, auch wenn sie gerade niemand hat.
func TestVIPCheckRefusesEnabledVIP(t *testing.T) {
	d, vs, fp := checkDaemon(t, false)
	d.lock()
	d.cl.Access = accessConfig{VIP: "10.77.1.50/24", VIPOn: true}
	d.unlock()
	if _, err := d.runVIPCheck(context.Background(), testAddr(t)); !errors.Is(err, errProbeIsVIP) {
		t.Fatalf("runVIPCheck: %v", err)
	}
	if _, err := d.probeHold(context.Background(), "10.77.1.50/24", 5); !errors.Is(err, errProbeIsVIP) {
		t.Fatalf("probeHold: %v", err)
	}
	if vs.count("ip addr") != 0 || len(fp.calls) != 0 {
		t.Fatalf("trotzdem gearbeitet: %v %v", vs.calls, fp.calls)
	}
	// ausgeschaltet → erlaubt
	d.lock()
	d.cl.Access.VIPOn = false
	d.unlock()
	if _, err := d.runVIPCheck(context.Background(), testAddr(t)); err != nil {
		t.Fatalf("ausgeschaltet: %v", err)
	}
}

func TestRunVIPCheckReleasesOnFailure(t *testing.T) {
	d, vs, fp := checkDaemon(t, false)
	fp.fail["a>w"] = "keine Verbindung"
	ver := d.cl.Version
	res, err := d.runVIPCheck(context.Background(), testAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 || res[1].OK || res[1].Holder != "a" || res[1].Checker != "w" || !strings.Contains(res[1].Err, "keine Verbindung") {
		t.Fatalf("Ergebnisse: %+v", res)
	}
	if !res[0].OK || !res[2].OK || !res[3].OK {
		t.Fatalf("übrige Richtungen: %+v", res)
	}
	if vs.count(vipDel) != 1 || vs.held {
		t.Fatalf("eigene Prüfadresse nicht abgegeben: %v", vs.calls)
	}
	if fp.count("probe-release") != 1 || fp.bHeld {
		t.Fatalf("Partner nicht abgegeben: %v", fp.calls)
	}
	if d.cl.Access.Tested != nil || d.cl.Version != ver {
		t.Fatalf("Tested %+v, Version %d", d.cl.Access.Tested, d.cl.Version)
	}
}

// Erreicht ein Prüfender die Adresse erst nach ein paar Versuchen (Windows
// fragt erst beim alten Halter nach), gilt die Richtung als bestanden.
func TestRunVIPCheckRetries(t *testing.T) {
	probeRetryMax, probeRetryPause = 5*time.Second, time.Millisecond
	t.Cleanup(func() { probeRetryMax, probeRetryPause = 0, time.Second })
	d, _, fp := checkDaemon(t, false)
	tries := 0
	d.peerPostFn = func(ctx context.Context, peer clusterNode, path string, in, out any) error {
		if r, ok := in.(probeAddrReq); ok && peer.ID == "w" && r.Expect == "b" {
			if tries++; tries < 3 {
				*out.(*probeResp) = probeResp{Err: "dial tcp 10.77.1.50:7480: i/o timeout"}
				return nil
			}
		}
		return fp.post(ctx, peer, path, in, out)
	}
	res, err := d.runVIPCheck(context.Background(), testAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.OK {
			t.Fatalf("Ergebnisse: %+v", res)
		}
	}
	if tries != 3 {
		t.Fatalf("%d Versuche statt 3", tries)
	}
	if d.cl.Access.Tested == nil {
		t.Fatal("Test nicht als bestanden gespeichert")
	}
}

func TestRunVIPCheckPartnerHoldFails(t *testing.T) {
	d, vs, fp := checkDaemon(t, false)
	d.peerPostFn = func(ctx context.Context, peer clusterNode, path string, in, out any) error {
		if _, ok := in.(probeHoldReq); ok {
			fp.mu.Lock()
			fp.calls = append(fp.calls, peer.ID+" "+path)
			fp.mu.Unlock()
			*out.(*probeResp) = probeResp{Err: "Adresse 10.77.1.50 wird noch von einem anderen Gerät benutzt"}
			return nil
		}
		return fp.post(ctx, peer, path, in, out)
	}
	res, err := d.runVIPCheck(context.Background(), testAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 || !res[0].OK || !res[1].OK || res[2].OK || res[3].OK || !strings.Contains(res[2].Err, "nicht angenommen") {
		t.Fatalf("Ergebnisse: %+v", res)
	}
	if fp.count("probe-release") != 1 {
		t.Fatalf("auch nach gescheitertem Annehmen abgeben: %v", fp.calls)
	}
	if vs.count(vipDel) != 1 || d.cl.Access.Tested != nil {
		t.Fatalf("%v %+v", vs.calls, d.cl.Access.Tested)
	}
}

func TestRunVIPCheckRights(t *testing.T) {
	d, _, _ := checkDaemon(t, false)
	d.lock()
	d.node.CAHolder = false
	d.p.Role = ha.Passive
	d.unlock()
	if _, err := d.runVIPCheck(context.Background(), testAddr(t)); err == nil || !strings.Contains(err.Error(), "aktiven Server") {
		t.Fatalf("passiver Server ohne CA: %v", err)
	}
	w := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, vipCheckCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, newFakeNet())
	if _, err := w.runVIPCheck(context.Background(), testAddr(t)); err == nil || !strings.Contains(err.Error(), "Zeugen") {
		t.Fatalf("Zeuge: %v", err)
	}
}

// probeServer: TLS-Gegenstelle mit Knotenzertifikat id (wie pdh-ha auf Port 7480).
func probeServer(t *testing.T, ca testCA, id string) string {
	t.Helper()
	cert, _ := ca.tlsCert(t, id, "node")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: ca.pool,
		NextProtos: []string{alpnHA, alpnJoin}, MinVersion: tls.VersionTLS13,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

func probeCall(t *testing.T, h http.HandlerFunc, cs *tls.ConnectionState, body string) (int, probeResp) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/probe-addr", strings.NewReader(body))
	r.TLS = cs
	rec := httptest.NewRecorder()
	h(rec, r)
	var resp probeResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Antwort %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, resp
}

func TestProbeAddrHandler(t *testing.T) {
	ca := newTestCA(t, "c1")
	port := probeServer(t, ca, "node2")
	cl := clusterConfig{ClusterID: "c1", Version: 3, CAHolder: "node1", Settings: realSettings(), Nodes: []clusterNode{
		{ID: "node1", Kind: "node", Addrs: []string{"127.0.0.9:" + port}},
		{ID: "node2", Kind: "node", Addrs: []string{"127.0.0.8:" + port}},
		{ID: "w", Kind: "witness", Addrs: []string{"127.0.0.7:7480"}},
	}}
	// geprüft wird am Zeugen
	d := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, cl, ha.Persisted{Role: ha.Witness, Term: 1}, newFakeNet())
	own, _ := ca.tlsCert(t, "w", "witness")
	d.tlsFn = func() (*tls.Certificate, *x509.CertPool, error) { return &own, ca.pool, nil }
	cs, ok := clientState(t, ca, ca, "node1", "node")
	if !ok {
		t.Fatal("Handshake node1")
	}
	code, resp := probeCall(t, d.probeAddrHTTP, &cs, `{"addr":"127.0.0.1","expect":"node2"}`)
	if code != http.StatusOK || !resp.OK || resp.Err != "" {
		t.Fatalf("node2: %d %+v", code, resp)
	}
	code, resp = probeCall(t, d.probeAddrHTTP, &cs, `{"addr":"127.0.0.1","expect":"node1"}`)
	if code != http.StatusOK || resp.OK || !strings.Contains(resp.Err, "gehört zu node2") {
		t.Fatalf("node1: %d %+v", code, resp)
	}
	// fremde CA an der Adresse → kein ok
	d.tlsFn = func() (*tls.Certificate, *x509.CertPool, error) { return &own, newTestCA(t, "c2").pool, nil }
	if _, resp = probeCall(t, d.probeAddrHTTP, &cs, `{"addr":"127.0.0.1","expect":"node2"}`); resp.OK {
		t.Fatal("Zertifikat einer fremden CA angenommen")
	}
	d.tlsFn = func() (*tls.Certificate, *x509.CertPool, error) { return &own, ca.pool, nil }
	// niemand antwortet → Fehler, keine Wartezeit über die Frist
	free := listenLocal(t)
	_, freePort, _ := net.SplitHostPort(free.Addr().String())
	free.Close()
	d.lock()
	d.cl.Nodes[1].Addrs = []string{"127.0.0.8:" + freePort}
	d.unlock()
	if _, resp = probeCall(t, d.probeAddrHTTP, &cs, `{"addr":"127.0.0.1","expect":"node2"}`); resp.OK || resp.Err == "" {
		t.Fatalf("ohne Gegenstelle: %+v", resp)
	}
	// unbekannter Knoten als Ziel
	if _, resp = probeCall(t, d.probeAddrHTTP, &cs, `{"addr":"127.0.0.1","expect":"x"}`); resp.OK {
		t.Fatal("unbekannter Knoten")
	}
}

func TestProbeEndpointsNeedMember(t *testing.T) {
	ca := newTestCA(t, "c1")
	d, _, _ := checkDaemon(t, false)
	body := `{"addr":"10.77.1.50","expect":"b"}`
	if code, _ := probeCall(t, d.probeAddrHTTP, nil, body); code != http.StatusForbidden {
		t.Fatalf("ohne Zertifikat: %d", code)
	}
	for _, c := range []struct{ id, kind string }{{"w", "witness"}, {"x", "node"}, {"a", "node"}} {
		cs, ok := clientState(t, ca, ca, c.id, c.kind)
		if !ok {
			t.Fatalf("Handshake %s", c.id)
		}
		for name, h := range map[string]http.HandlerFunc{"addr": d.probeAddrHTTP, "hold": d.probeHoldHTTP, "release": d.probeReleaseHTTP} {
			if code, _ := probeCall(t, h, &cs, body); code != http.StatusForbidden {
				t.Fatalf("%s/%s: %d", c.id, name, code)
			}
		}
	}
	d.lock()
	d.cl.Revoked = []string{"egal"}
	d.unlock()
	cs, _ := clientState(t, ca, ca, "b", "node")
	if code, _ := probeCall(t, d.probeReleaseHTTP, &cs, `{"addr":"10.77.1.50/24"}`); code != http.StatusOK {
		t.Fatalf("Partner: %d", code)
	}
}

func TestProbeHoldAutoRelease(t *testing.T) {
	d, vs, _ := checkDaemon(t, false)
	after := make(chan time.Time)
	var asked time.Duration
	d.holdAfter = func(dur time.Duration) <-chan time.Time { asked = dur; return after }
	iface, err := d.probeHold(context.Background(), "10.77.1.50/24", 99)
	if err != nil || iface != "eth0" {
		t.Fatalf("%q %v", iface, err)
	}
	if asked != probeHoldMax*time.Second {
		t.Fatalf("Frist %v statt höchstens %d s", asked, probeHoldMax)
	}
	if vs.count(vipAdd) != 1 {
		t.Fatalf("%v", vs.calls)
	}
	// zweiter Test während des ersten → abgelehnt
	if _, err := d.probeHold(context.Background(), "10.77.1.51/24", 5); err == nil {
		t.Fatal("zweite Prüfadresse angenommen")
	}
	after <- time.Now()
	waitFor(t, 2*time.Second, "Prüfadresse abgegeben", func() bool { return vs.count(vipDel) == 1 })
	waitFor(t, 2*time.Second, "Halter frei", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.probeHeld == nil
	})
	// danach wieder möglich; probe-release gibt sofort ab
	if _, err := d.probeHold(context.Background(), "10.77.1.50/24", 5); err != nil {
		t.Fatal(err)
	}
	if err := d.probeRelease(context.Background(), "10.77.1.50/24"); err != nil {
		t.Fatal(err)
	}
	if vs.count(vipDel) != 2 {
		t.Fatalf("%v", vs.calls)
	}
}

func TestProbeHoldRefusesOwnAddress(t *testing.T) {
	d, vs, _ := checkDaemon(t, false)
	for _, a := range []string{"10.77.1.5/24", "192.168.1.50/24", "10.77.1.50"} {
		if _, err := d.probeHold(context.Background(), a, 5); err == nil {
			t.Fatalf("%s angenommen", a)
		}
	}
	// Adresse eines anderen Mitglieds (Partner, Zeuge) → abgelehnt
	for _, a := range []string{"10.77.1.6/24", "10.77.1.7/24"} {
		if _, err := d.probeHold(context.Background(), a, 5); err == nil || !strings.Contains(err.Error(), "gehört schon zu Knoten") {
			t.Fatalf("Knotenadresse %s: %v", a, err)
		}
	}
	// Karte per vip_interface festgelegt: fremdes Netz trotzdem abgelehnt
	d.lock()
	d.node.VIPInterface = "eth0"
	d.unlock()
	for _, a := range []string{"192.168.1.50/24", "10.77.2.50/24"} {
		if _, err := d.probeHold(context.Background(), a, 5); err == nil {
			t.Fatalf("fremdes Netz %s mit vip_interface angenommen", a)
		}
	}
	d.ifAddrsFn = fakeCards("eth0=10.77.1.5/24", "eth1=192.168.1.5/24")
	if _, err := d.probeHold(context.Background(), "192.168.1.50/24", 5); err == nil {
		t.Fatal("Netz einer anderen Karte als vip_interface angenommen")
	}
	// gültiges Verbund-Netz, aber vip_interface zeigt auf eine Karte in einem anderen Netz
	d.lock()
	d.node.VIPInterface = "eth1"
	d.unlock()
	if _, err := d.probeHold(context.Background(), "10.77.1.50/24", 5); err == nil || !strings.Contains(err.Error(), "nicht im Netz der Netzwerkkarte eth1") {
		t.Fatalf("Karte im fremden Netz: %v", err)
	}
	d.lock()
	d.node.VIPInterface = ""
	d.unlock()
	d.ifAddrsFn = fakeCards("eth0=10.77.1.5/24")
	vs.setHeld(true) // schon gesetzt (z. B. mitwandernde Adresse) → nicht anfassen
	if _, err := d.probeHold(context.Background(), "10.77.1.50/24", 5); err == nil || !strings.Contains(err.Error(), "belegt") {
		t.Fatalf("gesetzte Adresse: %v", err)
	}
	if vs.count("ip addr") != 0 {
		t.Fatalf("%v", vs.calls)
	}
	w := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, vipCheckCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, newFakeNet())
	w.ifAddrsFn = fakeCards("eth0=10.77.1.7/24")
	w.sys = &vipState{}
	if _, err := w.probeHold(context.Background(), "10.77.1.50/24", 5); err == nil {
		t.Fatal("Zeuge hält eine Prüfadresse")
	}
}

func accessCheckDaemon(t *testing.T, self nodeConfig, role ha.Role) *daemon {
	t.Helper()
	d := fakeDaemon(t, self, vipCheckCluster(), ha.Persisted{Role: role, Term: 1}, newFakeNet())
	d.ifAddrsFn = fakeCards("eth0=10.77.1.5/24")
	return d
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

func TestAccessPutNeedsTest(t *testing.T) {
	d := accessCheckDaemon(t, nodeConfig{ID: "a", Kind: "node", CAHolder: true}, ha.Active)
	on := accessUpdate{VIP: strp("10.77.1.202/24"), VIPOn: boolp(true)}
	if err := d.updateAccess(on); err == nil || !strings.Contains(err.Error(), "Bitte zuerst testen") {
		t.Fatalf("ohne Test: %v", err)
	}
	// Test auf anderer Karte zählt nicht
	d.lock()
	d.cl.Access.Tested = &vipTested{Net: "10.77.1.0/24", Ifaces: map[string]string{"a": "eth1", "b": "ens18"}, At: time.Now()}
	d.unlock()
	if err := d.updateAccess(on); err == nil || !strings.Contains(err.Error(), "Bitte zuerst testen") {
		t.Fatalf("andere Karte: %v", err)
	}
	// anderes Netz zählt nicht
	d.lock()
	d.cl.Access.Tested = &vipTested{Net: "10.77.2.0/24", Ifaces: map[string]string{"a": "eth0", "b": "ens18"}, At: time.Now()}
	d.unlock()
	if err := d.updateAccess(on); err == nil || !strings.Contains(err.Error(), "Bitte zuerst testen") {
		t.Fatalf("anderes Netz: %v", err)
	}
	tested := &vipTested{Net: "10.77.1.0/24", Ifaces: map[string]string{"a": "eth0", "b": "ens18"}, At: time.Now()}
	d.lock()
	d.cl.Access.Tested = tested
	ver := d.cl.Version
	d.unlock()
	if err := d.updateAccess(on); err != nil {
		t.Fatal(err)
	}
	a := d.cl.Access
	if a.VIP != "10.77.1.202/24" || !a.VIPOn || a.HooksOn || a.Tested == nil || a.Tested.Net != "10.77.1.0/24" || d.cl.Version != ver+1 {
		t.Fatalf("Zugang: %+v, Version %d", a, d.cl.Version)
	}
	// nur Skripte schalten: übrige Felder bleiben
	if err := d.updateAccess(accessUpdate{HooksOn: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if a := d.cl.Access; a.VIP != "10.77.1.202/24" || !a.VIPOn || !a.HooksOn {
		t.Fatalf("Zugang: %+v", a)
	}
	// ungültige Adresse → validateAccess
	if err := d.updateAccess(accessUpdate{VIP: strp("10.77.1.5/24")}); err == nil {
		t.Fatal("Adresse eines Knotens angenommen")
	}
	// ausschalten geht immer
	if err := d.updateAccess(accessUpdate{VIPOn: boolp(false)}); err != nil || d.cl.Access.VIPOn {
		t.Fatalf("aus: %v", err)
	}
}

func TestAccessPutWitnessRefused(t *testing.T) {
	w := fakeDaemon(t, nodeConfig{ID: "w", Kind: "witness"}, vipCheckCluster(), ha.Persisted{Role: ha.Witness, Term: 1}, newFakeNet())
	err := w.updateAccess(accessUpdate{HooksOn: boolp(true)})
	if err == nil || err.Error() != w.clusterEditable().Error() {
		t.Fatalf("Zeuge: %v", err)
	}
	d := accessCheckDaemon(t, nodeConfig{ID: "b", Kind: "node"}, ha.Passive)
	if err := d.updateAccess(accessUpdate{HooksOn: boolp(true)}); err == nil || !strings.Contains(err.Error(), "aktiven Server") {
		t.Fatalf("passiv ohne CA: %v", err)
	}
}

func TestAccessAPIAndCLI(t *testing.T) {
	d, _, sock := apiDaemon(t)
	t.Setenv("PDH_HA_SOCKET", sock)
	d.lock()
	d.p.Role = ha.Active
	d.unlock()
	var out, errb strings.Builder
	if rc := runCLI([]string{"access", "--hooks-on"}, &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	if !d.statusNow().Access.HooksOn {
		t.Fatal("hooks_on nicht gesetzt")
	}
	errb.Reset()
	if rc := runCLI([]string{"access", "--vip-on", "--vip-off"}, &out, &errb); rc != 2 {
		t.Fatalf("widersprüchlich: rc %d", rc)
	}
	errb.Reset()
	if rc := runCLI([]string{"vip", "test"}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "Adresse") {
		t.Fatalf("ohne Adresse: rc %d %q", rc, errb.String())
	}
}

func TestPrintVIPCheck(t *testing.T) {
	var b strings.Builder
	bad := printVIPCheck(&b, []vipCheckResult{
		{Holder: "node1", Checker: "node2", OK: true},
		{Holder: "node2", Checker: "node1", Err: "Adresse 10.77.1.50 gehört zu node1 statt zu node2"},
	})
	want := "node1 hält, node2 prüft: ok\nnode2 hält, node1 prüft: Fehler – Adresse 10.77.1.50 gehört zu node1 statt zu node2\n"
	if !bad || b.String() != want {
		t.Fatalf("%v %q", bad, b.String())
	}
}
