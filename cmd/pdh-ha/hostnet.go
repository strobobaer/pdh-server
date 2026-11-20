package main

import (
	"context"
	"net/netip"
	"os"
	"time"

	"pdh/internal/hostnet"
)

// netSummaryEvery: so oft liest pdh-ha den eigenen Netzwerkzustand neu. Die
// Zusammenfassung reist im Lebenszeichen zum Partner und in den Status.
const netSummaryEvery = 30 * time.Second

// netWaitEvery: Wartezeit, solange dieser Server noch nicht eingerichtet ist (Variable für Tests).
var netWaitEvery = 5 * time.Second

// netSummaryTimeout: Frist für eine ganze Runde der Befehle.
const netSummaryTimeout = 15 * time.Second

// collectNet liest den Netzwerkzustand dieses Servers (läuft als root, also ohne nsenter).
// exclude: die gemeinsame Adresse, die nie als eigene gilt.
func (d *daemon) collectNet(ctx context.Context, exclude ...netip.Addr) hostnet.Summary {
	if d.netCollect != nil {
		return d.netCollect(ctx)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return d.sys.Run(ctx, append([]string{name}, args...), nil, nil)
	}
	return hostnet.Collect(ctx, run, os.ReadFile, exclude...)
}

// netExcludeLocked: die gemeinsame IPv4-Adresse (Einstellung) und die gerade
// gehaltene gemeinsame IPv6-Adresse; beide zählen nie als Adresse des Servers.
func (d *daemon) netExcludeLocked() []netip.Addr {
	var ex []netip.Addr
	if p, err := netip.ParsePrefix(d.cl.Access.VIP); err == nil {
		ex = append(ex, p.Addr())
	}
	if d.access != nil {
		if a, _ := d.access.held6(); a != "" {
			if ad, err := netip.ParseAddr(a); err == nil {
				ex = append(ex, ad)
			}
		}
	}
	return ex
}

// netLoop liest sofort und dann alle netSummaryEvery den eigenen Zustand und
// legt ihn für Lebenszeichen und Status ab. Befehle laufen nie unter d.mu:
// heartbeatLocked und statusLocked lesen nur ownNet. Die Schleife läuft immer
// (die Einrichtung kann später im laufenden Prozess kommen); jede Runde prüft
// unter der Sperre, ob dies ein eingerichteter Server (nicht Zeuge) ist.
func (d *daemon) netLoop(ctx context.Context) {
	read := func() bool {
		d.lock()
		ok := d.configured && d.node.Kind == "node"
		var ex []netip.Addr
		if ok {
			ex = d.netExcludeLocked()
		}
		d.unlock()
		if !ok {
			return false
		}
		c, cancel := context.WithTimeout(ctx, netSummaryTimeout)
		defer cancel()
		s := d.collectNet(c, ex...)
		if c.Err() != nil {
			return true // Frist um: lieber den letzten guten Stand behalten als einen halben
		}
		d.ownNet.Store(&s)
		return true
	}
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = netSummaryEvery
		if !read() {
			wait = netWaitEvery
		}
	}
}
