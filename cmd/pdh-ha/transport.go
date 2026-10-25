package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

// Transport: je Gegenstelle und Weg eine dauerhafte mTLS-Verbindung (TLS 1.3)
// auf Port 7480, darüber JSON-Zeilen. Der Port trennt per ALPN: "pdh-ha/1" ist
// der Heartbeat-Strom (Client-Zertifikat Pflicht), "http/1.1" nur der Beitritt.

const (
	alpnHA       = "pdh-ha/1"
	alpnJoin     = "http/1.1"
	maxLine      = 64 << 10
	joinCertDays = 730
	maxJoinAddrs = 8
	dedupKeep    = 64
	restartSlack = 64 // so weit darf hb-seq über mehrere Wege zurückspringen
	handshakeMax = 3 * time.Second
	handshakeIP  = 8 // gleichzeitige Handshakes je Quelladresse
)

var dialBackoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second}

// message ist eine Zeile auf dem Heartbeat-Strom.
type message struct {
	T           string    `json:"t"` // hb, vote_req, vote_resp, handover, cluster, renew_req, renew_resp, removed
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	Term        uint64    `json:"term"`
	Ver         string    `json:"ver"`
	Commit      string    `json:"commit"`
	DataCurrent bool      `json:"data_current"`
	DataAssumed bool      `json:"data_assumed"`
	Gateway     bool      `json:"gateway"`
	Maint       bool      `json:"maint"`
	PG          string    `json:"pg"`
	PDH         string    `json:"pdh"`
	Disk        string    `json:"disk"`
	Seq         uint64    `json:"seq"`
	Wall        time.Time `json:"wall"`
	HostID      string    `json:"hostid,omitempty"`
	MaintSeq    uint64    `json:"mseq"`
	ClusterVer  uint64    `json:"cver"`
	Sees        []string  `json:"sees,omitempty"`      // Gegenstellen, die der Absender gerade sieht
	VotedFor    string    `json:"voted_for,omitempty"` // nur Zeuge

	VoteTerm uint64         `json:"vote_term,omitempty"`
	Granted  bool           `json:"granted,omitempty"`
	Cluster  *clusterConfig `json:"cluster,omitempty"`
	CSR      string         `json:"csr,omitempty"`
	Cert     string         `json:"cert,omitempty"`
}

var deliverTypes = map[string]bool{
	"vote_req": true, "vote_resp": true, "handover": true, "cluster": true,
	"renew_req": true, "renew_resp": true, "removed": true,
}

// pathStat beschreibt einen Weg; LastRecv monoton, 0 = nie.
type pathStat struct {
	Addr     string        `json:"addr"`
	Alive    bool          `json:"alive"`
	LastRecv time.Duration `json:"last_recv"`
	Err      string        `json:"err,omitempty"`
}

type peerView struct {
	Last     message
	LastSeen time.Duration
	Paths    []pathStat
}

// seen: mindestens ein Weg lebendig.
func (v peerView) seen() bool {
	for _, p := range v.Paths {
		if p.Alive {
			return true
		}
	}
	return false
}

type inbound struct {
	From string
	Msg  message
}

type pathState struct {
	recv bool
	last time.Duration
	err  string
}

type peerState struct {
	last    message
	hasLast bool
	paths   map[string]*pathState
	dedup   []string
}

type peerConn struct {
	peer, path, serial string
	c                  net.Conn
	raw                net.Conn // TCP darunter: Schließen ohne close_notify blockiert nie
	out                chan []byte
	done               chan struct{}
	once               sync.Once
}

func (p *peerConn) close() {
	p.once.Do(func() {
		close(p.done)
		p.raw.Close()
	})
}

type transport struct {
	self nodeConfig
	cert tls.Certificate
	ca   *x509.CertPool
	mono func() time.Duration
	ev   *eventLog
	in   chan inbound

	mu       sync.Mutex
	cl       clusterConfig
	ps       map[string]*peerState
	conns    map[string]*peerConn // Schlüssel peer|weg
	dialers  map[string]context.CancelFunc
	runCtx   context.Context
	rejectAt map[string]time.Duration
	hsPerIP  map[string]int // laufende Handshakes je Quelladresse

	rejects atomic.Uint64 // Zähler abgelehnter Verbindungen (Tests)
}

// newTransport erwartet in cert die Kette Knoten + CA (loadTLSCert), damit
// beitretende Knoten den CA-Fingerabdruck prüfen können.
func newTransport(self nodeConfig, cl clusterConfig, cert tls.Certificate, ca *x509.CertPool, mono func() time.Duration, ev *eventLog) *transport {
	return &transport{
		self: self, cl: cl, cert: cert, ca: ca, mono: mono, ev: ev,
		in:       make(chan inbound, 256),
		ps:       map[string]*peerState{},
		conns:    map[string]*peerConn{},
		dialers:  map[string]context.CancelFunc{},
		rejectAt: map[string]time.Duration{},
		hsPerIP:  map[string]int{},
	}
}

// loadTLSCert baut das TLS-Zertifikat mit Kette Knoten + CA.
func loadTLSCert(certPEM, keyPEM, caPEM []byte) (tls.Certificate, error) {
	chain := append(append(append([]byte{}, certPEM...), '\n'), caPEM...)
	return tls.X509KeyPair(chain, keyPEM)
}

// shouldDial: die kleinere Kennung baut auf; der Zeuge nie, zum Zeugen immer.
func shouldDial(self nodeConfig, peer clusterNode) bool {
	if self.Kind == "witness" || self.ID == peer.ID {
		return false
	}
	if peer.Kind == "witness" {
		return true
	}
	return self.ID < peer.ID
}

func (t *transport) tickLocked() time.Duration {
	if t.cl.Settings.Tick > 0 {
		return t.cl.Settings.Tick
	}
	return time.Second
}

func (t *transport) tick() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tickLocked()
}

func (t *transport) nodeLocked(id string) (clusterNode, bool) {
	for _, n := range t.cl.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return clusterNode{}, false
}

// ownAddrsLocked: eigene Adressen laut Cluster, sonst laut node.json.
func (t *transport) ownAddrsLocked() []string {
	if n, ok := t.nodeLocked(t.self.ID); ok && len(n.Addrs) > 0 {
		return n.Addrs
	}
	return t.self.Addrs
}

// pathsLocked: Wege zu einer Gegenstelle – beim Aufbauenden deren Adressen,
// beim Annehmenden die eigenen.
func (t *transport) pathsLocked(n clusterNode) []string {
	if shouldDial(t.self, n) {
		return n.Addrs
	}
	return t.ownAddrsLocked()
}

func (t *transport) peerLocked(id string) *peerState {
	p := t.ps[id]
	if p == nil {
		p = &peerState{paths: map[string]*pathState{}}
		t.ps[id] = p
	}
	return p
}

func (t *transport) pathLocked(id, path string) *pathState {
	p := t.peerLocked(id)
	s := p.paths[path]
	if s == nil {
		s = &pathState{}
		p.paths[path] = s
	}
	return s
}

func (t *transport) pathErr(id, path, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.nodeLocked(id); ok {
		t.pathLocked(id, path).err = msg
	}
}

// setCluster übernimmt eine neue Knotenliste: entfernte oder gesperrte
// Gegenstellen werden getrennt, Verbindungsaufbau angepasst. Eine ältere
// Version als die aktuelle wird ignoriert (verspätete Zustellung).
func (t *transport) setCluster(cl clusterConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cl.Version < t.cl.Version {
		return
	}
	t.cl = cl
	for id := range t.ps {
		if _, ok := t.nodeLocked(id); !ok {
			delete(t.ps, id)
		}
	}
	for _, pc := range t.conns {
		if _, ok := t.nodeLocked(pc.peer); !ok || t.revokedLocked(pc.serial) {
			pc.close()
		}
	}
	t.reconcileLocked()
}

func (t *transport) revokedLocked(serial string) bool {
	for _, r := range t.cl.Revoked {
		if normHex(r) == serial {
			return true
		}
	}
	return false
}

// reconcileLocked startet und beendet die ausgehenden Verbindungen.
func (t *transport) reconcileLocked() {
	if t.runCtx == nil {
		return
	}
	want := map[string][2]string{}
	for _, n := range t.cl.Nodes {
		if !shouldDial(t.self, n) {
			continue
		}
		for _, a := range n.Addrs {
			want[n.ID+"|"+a] = [2]string{n.ID, a}
		}
	}
	for k, cancel := range t.dialers {
		if _, ok := want[k]; !ok {
			cancel()
			delete(t.dialers, k)
		}
	}
	for k, w := range want {
		if _, ok := t.dialers[k]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(t.runCtx)
		t.dialers[k] = cancel
		go t.dialLoop(ctx, w[0], w[1])
	}
}

func (t *transport) startDialers(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runCtx != nil {
		return
	}
	t.runCtx = ctx
	t.reconcileLocked()
	// Endet ctx, darf ein späterer serve-Aufruf neu starten.
	go func() {
		<-ctx.Done()
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.runCtx == ctx {
			t.runCtx = nil
			for k, cancel := range t.dialers {
				cancel()
				delete(t.dialers, k)
			}
		}
	}()
}

func (t *transport) dialLoop(ctx context.Context, id, addr string) {
	fails := 0
	for ctx.Err() == nil {
		if t.dialOnce(ctx, id, addr) {
			fails = 0
		}
		d := dialBackoff[min(fails, len(dialBackoff)-1)]
		fails++
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// dialOnce baut eine Verbindung auf und betreibt sie bis zum Abbruch.
// Ergebnis: mindestens eine Nachricht empfangen.
func (t *transport) dialOnce(ctx context.Context, id, addr string) bool {
	d := net.Dialer{Timeout: 5 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.pathErr(id, addr, "Verbindung fehlgeschlagen: "+err.Error())
		return false
	}
	c := tls.Client(raw, &tls.Config{
		Certificates: []tls.Certificate{t.cert},
		RootCAs:      t.ca,
		ServerName:   id,
		NextProtos:   []string{alpnHA},
		MinVersion:   tls.VersionTLS13,
	})
	hctx, cancel := context.WithTimeout(ctx, handshakeMax)
	err = c.HandshakeContext(hctx)
	cancel()
	if err != nil {
		c.Close()
		t.pathErr(id, addr, "TLS: "+err.Error())
		return false
	}
	cs := c.ConnectionState()
	if cs.NegotiatedProtocol != alpnHA {
		c.Close()
		t.pathErr(id, addr, "Gegenstelle spricht kein "+alpnHA)
		return false
	}
	pid, serial, err := t.identify(cs)
	if err == nil && pid != id {
		err = fmt.Errorf("Gegenstelle meldet sich als %q", pid)
	}
	if err != nil {
		c.Close()
		t.pathErr(id, addr, err.Error())
		return false
	}
	return t.runConn(ctx, id, addr, c, raw, serial)
}

// identify prüft Zertifikat, Sperrliste und Mitgliedschaft im Cluster.
func (t *transport) identify(cs tls.ConnectionState) (id, serial string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id, kind, err := peerIdentity(cs, t.cl.Revoked)
	if err != nil {
		return "", "", err
	}
	n, ok := t.nodeLocked(id)
	if !ok || id == t.self.ID {
		return "", "", fmt.Errorf("%q gehört nicht zum Cluster", id)
	}
	if n.Kind != kind {
		return "", "", fmt.Errorf("%q hat die falsche Art %q", id, kind)
	}
	return id, normHex(serialHex(cs.VerifiedChains[0][0])), nil
}

// runConn betreibt eine Verbindung: eigener Schreiber, Lesen mit Frist 3 Takte.
func (t *transport) runConn(ctx context.Context, id, path string, c, raw net.Conn, serial string) (got bool) {
	pc := &peerConn{peer: id, path: path, serial: serial, c: c, raw: raw, out: make(chan []byte, 32), done: make(chan struct{})}
	key := id + "|" + path
	t.mu.Lock()
	// erneut unter derselben Sperre prüfen: setCluster kann seit identify gelaufen sein
	if _, ok := t.nodeLocked(id); !ok || t.revokedLocked(serial) {
		t.mu.Unlock()
		pc.close()
		return false
	}
	if old := t.conns[key]; old != nil {
		old.close()
	}
	t.conns[key] = pc
	t.pathLocked(id, path).err = ""
	t.mu.Unlock()
	defer func() {
		pc.close()
		t.mu.Lock()
		if t.conns[key] == pc {
			delete(t.conns, key)
		}
		t.mu.Unlock()
	}()
	go func() {
		select {
		case <-ctx.Done():
			pc.close()
		case <-pc.done:
		}
	}()
	go t.writer(pc)

	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 4096), maxLine+1)
	for {
		c.SetReadDeadline(time.Now().Add(3 * t.tick()))
		if !sc.Scan() {
			t.mu.Lock()
			// nur der aktuelle Weg darf den Fehlertext setzen
			if ctx.Err() == nil && t.conns[key] == pc {
				if _, ok := t.nodeLocked(id); ok {
					t.pathLocked(id, path).err = readErrText(sc.Err())
				}
			}
			t.mu.Unlock()
			return got
		}
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		got = true
		t.receive(id, path, serial, m)
	}
}

func readErrText(err error) string {
	var ne net.Error
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return "Verbindung geschlossen"
	case errors.Is(err, bufio.ErrTooLong):
		return "Zeile zu lang, Verbindung geschlossen"
	case errors.As(err, &ne) && ne.Timeout():
		return "Keine Nachricht seit 3 Takten"
	}
	return err.Error()
}

func (t *transport) writer(pc *peerConn) {
	for {
		select {
		case <-pc.done:
			return
		case b := <-pc.out:
			pc.c.SetWriteDeadline(time.Now().Add(3 * t.tick()))
			if _, err := pc.c.Write(b); err != nil {
				pc.close()
				return
			}
		}
	}
}

// receive verbucht eine Nachricht. hb wird nur gespeichert; andere bekannte
// Arten gehen an incoming() – über mehrere Wege doppelt angekommene
// (gleiche Art und seq > 0) nur einmal.
func (t *transport) receive(id, path, serial string, m message) {
	now := t.mono()
	t.mu.Lock()
	if _, ok := t.nodeLocked(id); !ok || t.revokedLocked(serial) {
		t.mu.Unlock()
		return
	}
	p := t.peerLocked(id)
	s := t.pathLocked(id, path)
	s.recv, s.last = true, now
	m.ID = id
	if m.T == "hb" {
		// Neustart der Gegenstelle (seq deutlich kleiner oder andere HostID):
		// alte Kennungen der Doppelerkennung verwerfen
		if p.hasLast && (m.Seq+restartSlack < p.last.Seq ||
			(m.HostID != "" && p.last.HostID != "" && m.HostID != p.last.HostID)) {
			p.dedup = nil
		}
		p.last, p.hasLast = m, true
		t.mu.Unlock()
		return
	}
	if !deliverTypes[m.T] {
		t.mu.Unlock()
		return
	}
	if m.Seq > 0 {
		k := m.T + ":" + strconv.FormatUint(m.Seq, 10)
		if slices.Contains(p.dedup, k) {
			t.mu.Unlock()
			return
		}
		p.dedup = append(p.dedup, k)
		if len(p.dedup) > dedupKeep {
			p.dedup = p.dedup[len(p.dedup)-dedupKeep:]
		}
	}
	t.mu.Unlock()
	select {
	case t.in <- inbound{From: id, Msg: m}:
	default: // Empfänger hängt: verwerfen, die Abläufe wiederholen sich im Takt
	}
}

func encodeLine(m message) []byte {
	b, _ := json.Marshal(m)
	return append(b, '\n')
}

func (t *transport) send(m message, to string) {
	b := encodeLine(m)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pc := range t.conns {
		if to != "" && pc.peer != to {
			continue
		}
		select {
		case pc.out <- b:
		default: // Weg staut: Nachricht fällt weg
		}
	}
}

// broadcast schickt m über alle offenen Wege an alle Gegenstellen.
func (t *transport) broadcast(m message) { t.send(m, "") }

// sendTo schickt m über alle offenen Wege an eine Gegenstelle.
func (t *transport) sendTo(id string, m message) {
	if id != "" {
		t.send(m, id)
	}
}

func (t *transport) incoming() <-chan inbound { return t.in }

// peers liefert je Gegenstelle der Knotenliste den letzten Heartbeat und die Wege.
func (t *transport) peers(tick time.Duration) map[string]peerView {
	now := t.mono()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]peerView{}
	for _, n := range t.cl.Nodes {
		if n.ID == t.self.ID {
			continue
		}
		var v peerView
		p := t.ps[n.ID]
		addrs := append([]string(nil), t.pathsLocked(n)...)
		if p != nil {
			if p.hasLast {
				v.Last = p.last
			}
			var extra []string
			for a := range p.paths {
				if !slices.Contains(addrs, a) {
					extra = append(extra, a)
				}
			}
			sort.Strings(extra)
			addrs = append(addrs, extra...)
		}
		for _, a := range addrs {
			st := pathStat{Addr: a}
			if p != nil {
				if s := p.paths[a]; s != nil {
					st.Err = s.err
					if s.recv {
						st.LastRecv = s.last
						st.Alive = now-s.last <= 3*tick
						v.LastSeen = max(v.LastSeen, s.last)
					}
				}
			}
			v.Paths = append(v.Paths, st)
		}
		out[n.ID] = v
	}
	return out
}

// normAddr macht host:port vergleichbar (IPv4 in IPv6 → IPv4).
func normAddr(s string) string {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s
	}
	if ip := net.ParseIP(h); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		h = ip.String()
	}
	return net.JoinHostPort(h, p)
}

// localPath ordnet eine angenommene Verbindung einer eigenen Adresse zu.
func (t *transport) localPath(a net.Addr) string {
	s := normAddr(a.String())
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, own := range t.ownAddrsLocked() {
		if normAddr(own) == s {
			return own
		}
	}
	return s
}

func hostOf(a net.Addr) string {
	if a == nil {
		return "?"
	}
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

// reject protokolliert eine abgelehnte Verbindung, höchstens einmal je Minute und Adresse.
func (t *transport) reject(a net.Addr) {
	host := hostOf(a)
	now := t.mono()
	t.mu.Lock()
	last, ok := t.rejectAt[host]
	logIt := !ok || now-last >= time.Minute
	if logIt {
		if len(t.rejectAt) > 1000 {
			for h, at := range t.rejectAt {
				if now-at >= time.Minute {
					delete(t.rejectAt, h)
				}
			}
		}
		t.rejectAt[host] = now
	}
	t.mu.Unlock()
	if logIt && t.ev != nil {
		t.ev.add(ha.Event{Kind: "warn", Text: "Verbindung abgelehnt: " + host})
	}
	t.rejects.Add(1)
}

// chanListener reicht bereits verschlüsselte Beitritts-Verbindungen an http.Server.
type chanListener struct {
	addr net.Addr
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

func (l *chanListener) put(ctx context.Context, c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-ctx.Done():
		return false
	case <-l.done:
		return false
	}
}

// serve nimmt Verbindungen auf ln an und trennt per ALPN. Der erste Aufruf
// startet auch die ausgehenden Verbindungen (bis dessen ctx endet). Endet
// serve, schließen alle über ln angenommenen Verbindungen. join darf nil sein
// (Knoten ohne CA): Beitrittsversuche werden dann geschlossen.
func (t *transport) serve(ctx context.Context, ln net.Listener, join http.Handler) {
	t.startDialers(ctx)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		ln.Close()
	}()
	var jl *chanListener
	if join != nil {
		jl = &chanListener{addr: ln.Addr(), ch: make(chan net.Conn), done: make(chan struct{})}
		srv := &http.Server{
			Handler:           join,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       10 * time.Second,
			MaxHeaderBytes:    16 << 10,
			ErrorLog:          log.New(io.Discard, "", 0),
		}
		go srv.Serve(jl)
		defer srv.Close()
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{t.cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    t.ca,
		NextProtos:   []string{alpnHA, alpnJoin},
		MinVersion:   tls.VersionTLS13,
	}
	sem := make(chan struct{}, 64) // gleichzeitige Handshakes
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for {
		raw, err := ln.Accept()
		if err != nil {
			if sctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		host := hostOf(raw.RemoteAddr())
		t.mu.Lock()
		full := t.hsPerIP[host] >= handshakeIP
		if !full {
			t.hsPerIP[host]++
		}
		t.mu.Unlock()
		if full {
			raw.Close()
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			t.handshakeDone(host)
			raw.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.handleConn(sctx, raw, cfg, jl, func() { <-sem; t.handshakeDone(host) })
		}()
	}
}

func (t *transport) handshakeDone(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hsPerIP[host]--; t.hsPerIP[host] <= 0 {
		delete(t.hsPerIP, host)
	}
}

func (t *transport) handleConn(ctx context.Context, raw net.Conn, cfg *tls.Config, jl *chanListener, release func()) {
	c := tls.Server(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, handshakeMax)
	err := c.HandshakeContext(hctx)
	cancel()
	release()
	if err != nil {
		if ctx.Err() == nil {
			t.reject(raw.RemoteAddr())
		}
		c.Close()
		return
	}
	cs := c.ConnectionState()
	if cs.NegotiatedProtocol == alpnHA {
		id, serial, err := t.identify(cs)
		if err != nil {
			t.reject(raw.RemoteAddr())
			c.Close()
			return
		}
		t.runConn(ctx, id, t.localPath(c.LocalAddr()), c, raw, serial)
		return
	}
	if jl == nil || !jl.put(ctx, c) {
		c.Close()
	}
}

// --- Beitritt ---

type joinRequest struct {
	Secret string   `json:"secret"`
	ID     string   `json:"id"`
	CSR    string   `json:"csr"`
	Addrs  []string `json:"addrs"`
}

type joinResponse struct {
	Cert    string        `json:"cert"`
	CA      string        `json:"ca"`
	Cluster clusterConfig `json:"cluster"`
}

type joinErrorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// joinHandler bedient POST /v1/join (nur auf dem CA-Knoten). Der Code wird
// beim Einlösen verbraucht – scheitert danach die Prüfung, ist ein neuer nötig.
func joinHandler(st store, codes *joinCodes, onJoin func(clusterConfig), ev *eventLog) http.Handler {
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/join", func(w http.ResponseWriter, r *http.Request) {
		var req joinRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLine)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, joinErrorBody{"Anfrage ist unlesbar"})
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		role, err := codes.redeem(req.Secret, host, time.Now())
		if err != nil {
			status := http.StatusForbidden
			if strings.HasPrefix(err.Error(), "Zu viele") {
				status = http.StatusTooManyRequests
			}
			writeJSON(w, status, joinErrorBody{err.Error()})
			return
		}
		// onJoin unter derselben Sperre: Knotenlisten kommen in Reihenfolge an
		mu.Lock()
		resp, status, err := admit(st, role, req)
		if err == nil && onJoin != nil {
			onJoin(resp.Cluster)
		}
		mu.Unlock()
		if err != nil {
			msg := err.Error()
			if status >= http.StatusInternalServerError {
				if ev != nil {
					ev.add(ha.Event{Kind: "warn", Text: fmt.Sprintf("Beitritt von %s fehlgeschlagen: %v", req.ID, err)})
				}
				msg = "Interner Fehler beim Beitritt"
			}
			writeJSON(w, status, joinErrorBody{msg})
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	return mux
}

// admit prüft den Beitritt, signiert und trägt den Knoten ein.
func admit(st store, role string, req joinRequest) (joinResponse, int, error) {
	bad := http.StatusBadRequest
	if !validID(req.ID) {
		return joinResponse{}, bad, fmt.Errorf("Ungültige Kennung %q", req.ID)
	}
	if len(req.Addrs) == 0 || len(req.Addrs) > maxJoinAddrs {
		return joinResponse{}, bad, fmt.Errorf("1 bis %d Adressen angeben", maxJoinAddrs)
	}
	for _, a := range req.Addrs {
		if h, p, err := net.SplitHostPort(a); err != nil || h == "" || p == "" {
			return joinResponse{}, bad, fmt.Errorf("Ungültige Adresse %q", a)
		}
	}
	rb, _ := pem.Decode([]byte(req.CSR))
	if rb == nil {
		return joinResponse{}, bad, errors.New("Signieranforderung ist unlesbar")
	}
	csr, err := x509.ParseCertificateRequest(rb.Bytes)
	if err != nil {
		return joinResponse{}, bad, errors.New("Signieranforderung ist unlesbar")
	}
	if csr.Subject.CommonName != req.ID {
		return joinResponse{}, bad, errors.New("Kennung passt nicht zur Signieranforderung")
	}
	if pub, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() || csr.CheckSignature() != nil {
		return joinResponse{}, bad, errors.New("Signieranforderung muss ECDSA P-256 und gültig signiert sein")
	}
	// Fehler mit Status 500 gehen nur ins Ereignisprotokoll, nicht an die Gegenseite
	internal := http.StatusInternalServerError
	cl, err := st.loadCluster()
	if err != nil {
		return joinResponse{}, internal, fmt.Errorf("Cluster-Konfiguration nicht lesbar: %v", err)
	}
	nodes, witnesses := 0, 0
	for _, n := range cl.Nodes {
		if n.ID == req.ID {
			return joinResponse{}, http.StatusConflict, fmt.Errorf("Kennung %s ist bereits im Cluster", req.ID)
		}
		switch n.Kind {
		case "node":
			nodes++
		case "witness":
			witnesses++
		}
	}
	switch {
	case role == "witness" && nodes < 2:
		return joinResponse{}, http.StatusConflict, errors.New("Zuerst den zweiten Server hinzufügen")
	case role == "witness" && witnesses > 0:
		return joinResponse{}, http.StatusConflict, errors.New("Der Cluster hat bereits einen Zeugen")
	case role == "node" && nodes >= 2:
		return joinResponse{}, http.StatusConflict, errors.New("Der Cluster hat bereits zwei Server")
	}
	caCert, err1 := os.ReadFile(st.etcPath("ca.crt"))
	caKey, err2 := os.ReadFile(st.etcPath("ca.key"))
	if err := errors.Join(err1, err2); err != nil {
		return joinResponse{}, internal, fmt.Errorf("Zertifizierungsstelle nicht lesbar: %v", err)
	}
	certPEM, err := signCSR(caCert, caKey, []byte(req.CSR), role, joinCertDays)
	if err != nil {
		return joinResponse{}, internal, fmt.Errorf("Signieren: %v", err)
	}
	cb, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return joinResponse{}, internal, err
	}
	cl.Nodes = append(cl.Nodes, clusterNode{ID: req.ID, Kind: role, Addrs: req.Addrs, Serial: serialHex(cert)})
	cl.Version++
	if err := st.saveCluster(cl); err != nil {
		return joinResponse{}, internal, fmt.Errorf("Cluster-Konfiguration nicht gespeichert: %v", err)
	}
	return joinResponse{Cert: string(certPEM), CA: string(caCert), Cluster: cl}, http.StatusOK, nil
}

var (
	errFingerprint = errors.New("Fingerabdruck der Zertifizierungsstelle stimmt nicht")
	errServerCert  = errors.New("Server-Zertifikat nicht von der Zertifizierungsstelle")
)

// verifyJoinServer: die mitgeschickte CA muss den Fingerabdruck aus dem Code
// haben und das Server-Zertifikat ausgestellt haben.
func verifyJoinServer(rawCerts [][]byte, fp string) error {
	if len(rawCerts) == 0 {
		return errFingerprint
	}
	var ca *x509.Certificate
	for _, raw := range rawCerts {
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) == fp {
			c, err := x509.ParseCertificate(raw)
			if err == nil && c.IsCA {
				ca = c
			}
			break
		}
	}
	if ca == nil {
		return errFingerprint
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return errServerCert
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errServerCert
	}
	// Beitritt nur bei einem Server, nie bei einem Zeugen
	if len(leaf.Subject.OrganizationalUnit) != 1 || leaf.Subject.OrganizationalUnit[0] != "node" {
		return errServerCert
	}
	return nil
}

// joinRemote tritt über die Adressen des Codes der Reihe nach bei. Antwortet
// ein Server mit einem Fehler, wird nicht weiter probiert (Code ist verbraucht).
func joinRemote(ctx context.Context, code joinCode, req joinRequest) (joinResponse, error) {
	if req.Secret == "" {
		req.Secret = code.Secret
	}
	body, err := json.Marshal(req)
	if err != nil {
		return joinResponse{}, err
	}
	fp := strings.ToLower(code.FP)
	var lastErr error
	fpFail := false
	for _, addr := range code.Addrs {
		resp, answered, err := joinOnce(ctx, addr, fp, body)
		if err == nil {
			if fingerprint([]byte(resp.CA)) != fp {
				return joinResponse{}, errFingerprint
			}
			if code.Cluster != "" && resp.Cluster.ClusterID != code.Cluster {
				return joinResponse{}, errors.New("Antwort gehört zu einem anderen Cluster")
			}
			return resp, nil
		}
		if answered {
			return joinResponse{}, err
		}
		if errors.Is(err, errFingerprint) {
			fpFail = true
		}
		lastErr = err
	}
	if fpFail {
		return joinResponse{}, errFingerprint
	}
	if lastErr == nil {
		lastErr = errors.New("Beitrittscode enthält keine Adresse")
	}
	return joinResponse{}, fmt.Errorf("Beitritt nicht möglich: %w", lastErr)
}

func joinOnce(ctx context.Context, addr, fp string, body []byte) (resp joinResponse, answered bool, err error) {
	cfg := &tls.Config{
		InsecureSkipVerify: true, // geprüft wird der CA-Fingerabdruck
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyJoinServer(raw, fp)
		},
		NextProtos: []string{alpnJoin},
		MinVersion: tls.VersionTLS13,
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true},
	}
	defer client.CloseIdleConnections()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/v1/join", strings.NewReader(string(body)))
	if err != nil {
		return resp, false, err
	}
	hr.Header.Set("Content-Type", "application/json")
	r, err := client.Do(hr)
	if err != nil {
		for _, e := range []error{errFingerprint, errServerCert} {
			if errors.Is(err, e) {
				return resp, false, e
			}
		}
		return resp, false, err
	}
	defer r.Body.Close()
	lr := io.LimitReader(r.Body, 1<<20)
	if r.StatusCode != http.StatusOK {
		var eb joinErrorBody
		if json.NewDecoder(lr).Decode(&eb) != nil || eb.Error == "" {
			eb.Error = "Beitritt abgelehnt (" + r.Status + ")"
		}
		return resp, true, errors.New(eb.Error)
	}
	if err := json.NewDecoder(lr).Decode(&resp); err != nil {
		return resp, true, errors.New("Antwort des Servers ist unlesbar")
	}
	return resp, true, nil
}
