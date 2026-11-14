package datanet

import (
	"context"
	"net"
	"sync"
)

// Dialer wählt Verbindungen ins Datennetz (z. B. Node).
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Bridges stellt je Datennetz-Gegenstelle einen lokalen Loopback-Listener
// bereit, damit gewöhnliche Treiber (127.0.0.1:port) das Datennetz erreichen.
type Bridges struct {
	d      Dialer
	mu     sync.Mutex
	m      map[string]*bridge
	closed bool
}

type bridge struct {
	remote string
	host   string
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
}

func NewBridges(d Dialer) *Bridges {
	return &Bridges{d: d, m: map[string]*bridge{}}
}

// Addr liefert die lokale Adresse "127.0.0.1:<port>" für remote (host:port im
// Datennetz); je remote bleibt der Listener dauerhaft bestehen.
func (b *Bridges) Addr(remote string) (string, error) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if br, ok := b.m[remote]; ok {
		return br.ln.Addr().String(), nil
	}
	if b.closed {
		return "", net.ErrClosed
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	br := &bridge{remote: remote, host: host, ln: ln, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	b.m[remote] = br
	go br.serve(b.d)
	return ln.Addr().String(), nil
}

// CloseHost schließt alle Brücken zu dieser Datennetz-Adresse und trennt
// offene Verbindungen.
func (b *Bridges) CloseHost(ip string) {
	b.mu.Lock()
	var out []*bridge
	for k, br := range b.m {
		if br.host == ip {
			out = append(out, br)
			delete(b.m, k)
		}
	}
	b.mu.Unlock()
	for _, br := range out {
		br.close()
	}
}

// Close schließt alle Brücken.
func (b *Bridges) Close() {
	b.mu.Lock()
	b.closed = true
	all := b.m
	b.m = map[string]*bridge{}
	b.mu.Unlock()
	for _, br := range all {
		br.close()
	}
}

func (br *bridge) serve(d Dialer) {
	for {
		c, err := br.ln.Accept()
		if err != nil {
			return
		}
		if !br.track(c) {
			c.Close()
			return
		}
		go br.handle(d, c)
	}
}

func (br *bridge) track(c net.Conn) bool {
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.ctx.Err() != nil {
		return false
	}
	br.conns[c] = struct{}{}
	return true
}

func (br *bridge) untrack(c net.Conn) {
	br.mu.Lock()
	delete(br.conns, c)
	br.mu.Unlock()
	c.Close()
}

func (br *bridge) handle(d Dialer, c net.Conn) {
	defer br.untrack(c)
	dctx, cancel := context.WithTimeout(br.ctx, dialTimeout)
	t, err := d.DialContext(dctx, "tcp", br.remote)
	cancel()
	if err != nil {
		return
	}
	if !br.track(t) {
		t.Close()
		return
	}
	defer br.untrack(t)
	pipe(c, t)
}

func (br *bridge) close() {
	br.cancel()
	br.ln.Close()
	br.mu.Lock()
	for c := range br.conns {
		c.Close()
	}
	br.mu.Unlock()
}
