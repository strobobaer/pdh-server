package main

import (
	"context"
	"os"
	"time"

	"pdh/internal/hostnet"
)

// netSummaryEvery: so oft liest pdh-ha den eigenen Netzwerkzustand neu. Die
// Zusammenfassung reist im Lebenszeichen zum Partner und in den Status.
const netSummaryEvery = 30 * time.Second

// netSummaryTimeout: Frist für eine ganze Runde der Befehle.
const netSummaryTimeout = 15 * time.Second

// collectNet liest den Netzwerkzustand dieses Servers (läuft als root, also ohne nsenter).
func (d *daemon) collectNet(ctx context.Context) hostnet.Summary {
	if d.netCollect != nil {
		return d.netCollect(ctx)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return d.sys.Run(ctx, append([]string{name}, args...), nil, nil)
	}
	return hostnet.Collect(ctx, run, os.ReadFile)
}

// netLoop liest sofort und dann alle netSummaryEvery den eigenen Zustand und
// legt ihn für Lebenszeichen und Status ab. Befehle laufen nie unter d.mu:
// heartbeatLocked und statusLocked lesen nur ownNet. Der Zeuge hat keine
// eigene Adresse für Aktionen und liest nichts.
func (d *daemon) netLoop(ctx context.Context) {
	if d.node.Kind != "node" {
		return
	}
	read := func() {
		c, cancel := context.WithTimeout(ctx, netSummaryTimeout)
		defer cancel()
		s := d.collectNet(c)
		if ctx.Err() != nil {
			return
		}
		d.ownNet.Store(&s)
	}
	read()
	t := time.NewTicker(netSummaryEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			read()
		}
	}
}
