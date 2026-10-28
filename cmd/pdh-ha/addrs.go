package main

// Adressen eines Knotens ändern (pdh-ha addrs set): nötig, wenn eine bisherige
// Server-Adresse zur mitwandernden Adresse wird und der Server eine neue
// eigene Adresse bekommt. Heartbeat-Wege, Firewall, pg_hba, listen_addresses
// und primary_conninfo hängen an den Adressen der Knotenliste.
//
// Ablauf: Der CA-Halter prüft und verteilt die neue Knotenliste. Jeder Knoten
// mit Datenbank-Spiegel zieht danach PostgreSQL nach (reapplyAddrs in der
// Aktions-Warteschlange). listen_addresses verlangt einen Neustart: am
// Standby immer, am aktiven Knoten nur auf ausdrücklichen Wunsch (--neustart),
// sonst ein Hinweis. Der Wunsch reist in der Nachricht „cluster“ (RestartPG =
// Kennung des Knotens) mit; geht er verloren (Knotenliste später per
// Heartbeat nachgefordert), bleibt es beim Hinweis, und derselbe Befehl mit
// --neustart holt den Neustart nach.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"pdh/internal/ha"
)

// cleanNodeAddrs prüft neue Adressen wie beim Beitritt (fehlender Port: der
// bisherige des Knotens, sonst 7480) – nur IP-Adressen, ohne Doppelte.
func cleanNodeAddrs(cl clusterConfig, n clusterNode, addrs []string) ([]string, error) {
	port := portOf(n.Addrs)
	var in []string
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if h := strings.Trim(a, "[]"); net.ParseIP(h) != nil {
			a = net.JoinHostPort(h, port)
		} else if !strings.Contains(a, ":") {
			a = net.JoinHostPort(a, port)
		}
		in = append(in, a)
	}
	norm, err := normAddrs(in)
	if err != nil {
		return nil, err
	}
	var vip netip.Addr
	if cl.Access.VIP != "" {
		if p, err := parseVIP(cl.Access.VIP); err == nil {
			vip = p.Addr()
		}
	}
	var out []string
	for _, a := range norm {
		a = normAddr(a)
		ip, err := netip.ParseAddr(hostOfAddr(a))
		if err != nil {
			return nil, fmt.Errorf("Ungültige Adresse %q – bitte eine IP-Adresse angeben", a)
		}
		ip = ip.Unmap()
		if vip.IsValid() && ip == vip {
			return nil, errors.New("Die mitwandernde Adresse darf kein Verbindungsweg sein")
		}
		for _, o := range cl.Nodes {
			if o.ID == n.ID {
				continue
			}
			for _, oa := range o.Addrs {
				if oip, err := netip.ParseAddr(hostOfAddr(oa)); err == nil && oip.Unmap() == ip {
					return nil, fmt.Errorf("Adresse %s gehört schon zu %s", ip, o.ID)
				}
			}
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// addrsChanged: Knoten id steht in beiden Listen, mit anderen Adressen.
// Beitritt und Entfernen zählen nicht.
func addrsChanged(old, new clusterConfig, id string) bool {
	o, ok1 := findNode(old, id)
	n, ok2 := findNode(new, id)
	return ok1 && ok2 && !slices.Equal(o.Addrs, n.Addrs)
}

// setNodeAddrs ändert die Adressen eines Mitglieds (nur am CA-Halter) und
// verteilt die Knotenliste. restartPG erlaubt dem betroffenen Knoten, ein
// aktives PostgreSQL für die neuen Adressen neu zu starten; mit unveränderten
// Adressen holt er nur diesen Neustart nach.
func (d *daemon) setNodeAddrs(id string, addrs []string, restartPG bool) error {
	d.lock()
	if err := d.clusterEditable(); err != nil {
		d.unlock()
		return err
	}
	if !d.node.CAHolder {
		d.unlock()
		return errors.New("Adressen ändern nur auf dem Server, der den Ausfallschutz eingerichtet hat")
	}
	idx := slices.IndexFunc(d.cl.Nodes, func(n clusterNode) bool { return n.ID == id })
	if idx < 0 {
		d.unlock()
		return fmt.Errorf("Unbekannter Server %q", id)
	}
	clean, err := cleanNodeAddrs(d.cl, d.cl.Nodes[idx], addrs)
	if err != nil {
		d.unlock()
		return err
	}
	if slices.Equal(clean, d.cl.Nodes[idx].Addrs) && !restartPG {
		d.unlock()
		return nil
	}
	old := d.cl
	cl := d.cl
	cl.Nodes = slices.Clone(d.cl.Nodes)
	cl.Nodes[idx].Addrs = clean
	// die mitwandernde Adresse muss weiter im Netz beider Server liegen
	if err := validateAccess(cl, cl.Access); err != nil {
		d.unlock()
		return err
	}
	cl.Version++
	if err := d.st.saveCluster(cl); err != nil {
		d.unlock()
		return err
	}
	d.cl = cl
	pl := plan{net: d.net, ufw: true}
	m := message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq(), Cluster: &cl}
	if restartPG {
		m.RestartPG = id
	}
	pl.send("", m)
	d.followAddrsLocked(&pl, old, cl, restartPG && id == d.node.ID)
	nt, tick := d.net, d.settingsLocked().Tick
	d.unlock()
	pl.event("info", fmt.Sprintf("Adressen von %s geändert: %s", id, strings.Join(clean, ", ")))
	d.execute(context.Background(), pl)
	// erst senden, dann die Wege wechseln: sonst ginge die Nachricht über
	// einen gerade geschlossenen Weg verloren (wie beim Entfernen)
	time.AfterFunc(2*tick, func() { nt.setCluster(cl) })
	return nil
}

// followAddrsLocked zieht nach einer neuen Knotenliste die eigenen Adressen in
// node.json und PostgreSQL nach. restartReq: Neustart-Wunsch für diesen Knoten.
func (d *daemon) followAddrsLocked(pl *plan, old, cl clusterConfig, restartReq bool) {
	self := d.node.ID
	own := addrsChanged(old, cl, self)
	if own {
		if n, ok := findNode(cl, self); ok {
			node := d.node
			node.Addrs = slices.Clone(n.Addrs)
			if err := d.st.saveNode(node); err != nil {
				pl.event("warn", "Eigene Adressen nicht in node.json gespeichert: "+err.Error())
			} else {
				d.node = node
			}
		}
	}
	partner := false
	for _, n := range cl.Nodes {
		if n.Kind == "node" && n.ID != self && addrsChanged(old, cl, n.ID) {
			partner = true
		}
	}
	pa := d.pgActorLocked()
	if pa == nil || !own && !partner && !restartReq {
		return
	}
	listen := own || restartReq
	d.enqueueFnLocked(context.Background(), func(ctx context.Context) {
		if err := pa.reapplyAddrs(ctx, listen, restartReq); err != nil {
			pa.ev.add(ha.Event{Kind: "alarm", Text: "PostgreSQL nicht auf die neuen Adressen umgestellt: " + err.Error()})
		}
	})
}

// reapplyAddrs stellt PostgreSQL auf geänderte Adressen um: Zugang für den
// Partner (pg_hba, passfile) immer; am Standby Konfiguration neu und Neustart;
// am aktiven Knoten neu laden und – bei geänderten eigenen Adressen – nur mit
// restartPrimary neu starten, sonst Hinweis. Gestoppt: nur der Zugang.
func (a *pgActor) reapplyAddrs(ctx context.Context, ownListenChanged, restartPrimary bool) error {
	e := a.env()
	if err := a.writeAccess(ctx, e); err != nil {
		return fmt.Errorf("Zugang für den Partner nicht geschrieben: %w", err)
	}
	st := a.pg.status(ctx)
	switch st.State {
	case "standby":
		if len(e.PartnerAddrs) > 0 {
			if err := a.pg.writeConf(ctx, a.standbyConf(e)); err != nil {
				return err
			}
		}
		if err := a.pg.ctl(ctx, "restart"); err != nil {
			return fmt.Errorf("PostgreSQL (Spiegel) nicht neu gestartet: %w", err)
		}
		a.ev.add(ha.Event{Kind: "info", Text: "PostgreSQL (Spiegel) für neue Adressen neu gestartet"})
		return nil
	case "primary":
		if ownListenChanged {
			// nach einem Neustart von pdh-ha ist das gemerkte Ziel leer – die
			// laufende Einstellung übernehmen, sonst würde der Spiegel asynchron
			if a.syncTarget() == "" && st.SyncNames != "" {
				a.setSyncTarget(st.SyncNames)
			}
			if err := a.pg.writeConf(ctx, a.primaryConf(e, st.ReadOnly)); err != nil {
				return err
			}
		}
		if err := a.pg.ctl(ctx, "reload"); err != nil {
			return err
		}
		if !ownListenChanged {
			return nil
		}
		if !restartPrimary {
			a.ev.add(ha.Event{Kind: "warn", Text: fmt.Sprintf("Neustart von PostgreSQL nötig: sudo pdh-ha addrs set %s %s --neustart",
				e.SelfID, strings.Join(e.Listen, ","))})
			return nil
		}
		a.ev.add(ha.Event{Kind: "info", Text: "PostgreSQL wird für neue Adressen neu gestartet (wenige Sekunden)"})
		if err := a.pg.ctl(ctx, "restart"); err != nil {
			return fmt.Errorf("PostgreSQL nicht neu gestartet: %w", err)
		}
		return nil
	}
	return nil
}
