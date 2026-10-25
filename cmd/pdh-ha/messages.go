package main

import (
	"context"
	"fmt"
	"os"

	"pdh/internal/ha"
)

// Nachrichten der Gegenstellen außer dem Heartbeat. Wer was senden darf,
// richtet sich nach der Art laut Knotenliste (nie nach der Nachricht selbst).

// handle verarbeitet eine Nachricht (nicht Heartbeat) einer Gegenstelle.
func (d *daemon) handle(ctx context.Context, in inbound) {
	d.lock()
	if !d.configured {
		d.unlock()
		return
	}
	pl := plan{net: d.net}
	kind := d.kindOfLocked(in.From)
	m := in.Msg
	witness := d.node.Kind == "witness"
	switch m.T {
	case "vote_req":
		if witness && kind == "node" {
			d.voteLocked(&pl, in.From, m)
		}
	case "vote_resp":
		if !witness && kind == "witness" && m.Granted && m.VoteTerm != 0 && m.VoteTerm == d.carry.VotePending {
			d.voteGranted = m.VoteTerm
		}
	case "handover":
		if !witness && kind == "node" {
			d.handoverIn = m.VoteTerm
		}
	case "cluster":
		if kind == "node" {
			d.clusterMsgLocked(&pl, in.From, m)
		}
	case "renew_req":
		if kind != "" && d.node.CAHolder {
			d.renewReqLocked(&pl, in.From, kind, m)
		}
	case "renew_resp":
		if kind == "node" {
			d.renewRespLocked(&pl, m)
		}
	case "removed":
		if kind == "node" && !d.removed {
			d.removedLocked(&pl)
		}
	}
	d.unlock()
	d.execute(ctx, pl)
}

// voteLocked: Stimme ERST speichern, DANN antworten.
func (d *daemon) voteLocked(pl *plan, from string, m message) {
	s := d.settingsLocked()
	nodes := d.nodeObsLocked(d.net.peers(s.Tick))
	granted, next := ha.Vote(d.p, ha.VoteRequest{From: from, Term: m.VoteTerm}, nodes, d.mono(), s)
	if next != d.p {
		if err := d.saveFn(next); err != nil {
			pl.event("alarm", "Stimme nicht gespeichert, keine Antwort: "+err.Error())
			return
		}
		d.p = next
		pl.event("info", fmt.Sprintf("Stimme für Amtszeit %d an %s gegeben", m.VoteTerm, from))
	}
	pl.send(from, message{T: "vote_resp", ID: d.node.ID, VoteTerm: m.VoteTerm, Granted: granted, Seq: d.nextSeq()})
}

func (d *daemon) clusterMsgLocked(pl *plan, from string, m message) {
	if m.Cluster == nil {
		cl := d.cl
		pl.send(from, message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq(), Cluster: &cl})
		return
	}
	c := *m.Cluster
	if c.ClusterID != d.cl.ClusterID || c.Version <= d.cl.Version {
		return
	}
	if _, ok := findNode(c, d.node.ID); !ok {
		d.removedLocked(pl)
		return
	}
	if err := d.st.saveCluster(c); err != nil {
		pl.event("alarm", "Knotenliste nicht gespeichert: "+err.Error())
		return
	}
	d.cl = c
	d.net.setCluster(c)
	pl.ufw = true
	pl.event("info", fmt.Sprintf("Knotenliste übernommen (Version %d)", c.Version))
}

func findNode(c clusterConfig, id string) (clusterNode, bool) {
	for _, n := range c.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return clusterNode{}, false
}

// removedLocked: aus dem Cluster entfernt → zurück auf Einzel.
func (d *daemon) removedLocked(pl *plan) {
	if err := os.Rename(d.st.etcPath("cluster.json"), d.st.etcPath("cluster.json.removed")); err != nil {
		pl.event("warn", "cluster.json nicht umbenannt: "+err.Error())
	}
	self := clusterNode{ID: d.node.ID, Kind: d.node.Kind, Addrs: d.ownAddrsLocked()}
	if n, ok := findNode(d.cl, d.node.ID); ok {
		self = n
	}
	cl := d.cl
	cl.Version++
	cl.Nodes = []clusterNode{self}
	if d.node.CAHolder {
		cl.Revoked = append([]string(nil), cl.Revoked...)
		for _, n := range d.cl.Nodes {
			if n.ID != self.ID && n.Serial != "" {
				cl.Revoked = append(cl.Revoked, n.Serial)
			}
		}
		if err := d.st.saveCluster(cl); err != nil {
			pl.event("alarm", "Knotenliste nicht gespeichert: "+err.Error())
		}
	} else {
		d.removed = true
	}
	d.cl = cl
	d.net.setCluster(cl)
	pl.ufw = true
	pl.event("info", "Aus dem Cluster entfernt")
}
