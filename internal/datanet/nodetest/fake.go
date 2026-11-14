// Package nodetest liefert eine Attrappe des Datennetz-Knotens für Tests.
package nodetest

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
)

// Fake implementiert datanet.Node auf echtem Loopback.
type Fake struct {
	Addrs   []netip.Addr
	BlockUp bool              // Up blockiert bis zum Ende des Kontexts
	Routes  map[string]string // Datennetz-Adresse -> Loopback-Adresse
	Tags    map[string][]string
	PeerErr map[string]error // Adresse -> Fehler von PeerTags (Gerät nicht sichtbar)

	mu    sync.Mutex
	Ports map[int]string // angefragter Port -> tatsächliche Loopback-Adresse
}

func (f *Fake) Up(ctx context.Context) ([]netip.Addr, error) {
	if f.BlockUp {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.Addrs, nil
}

func (f *Fake) Listen(network, addr string) (net.Listener, error) {
	port := 0
	if _, p, err := net.SplitHostPort(addr); err == nil {
		port, _ = strconv.Atoi(p)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.Ports == nil {
		f.Ports = map[int]string{}
	}
	f.Ports[port] = l.Addr().String()
	f.mu.Unlock()
	return l, nil
}

func (f *Fake) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	to, ok := f.Routes[addr]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("nodetest: keine Route zu " + addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", to)
}

func (f *Fake) PeerTags(_ context.Context, remoteAddr string) ([]string, error) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if err := f.PeerErr[host]; err != nil {
		return nil, err
	}
	return f.Tags[host], nil
}

func (f *Fake) Close() error { return nil }

// Listening liefert die tatsächliche Loopback-Adresse eines belauschten Ports.
func (f *Fake) Listening(port int) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.Ports[port]
	return a, ok
}
