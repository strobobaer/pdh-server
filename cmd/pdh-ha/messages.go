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
			if m.Probe {
				d.probeGranted = m.VoteTerm // Beobachtung: nur „würde zustimmen“
			} else {
				d.voteGranted = m.VoteTerm
			}
		}
	case "vote_probe":
		if witness && kind == "node" {
			d.probeLocked(&pl, in.From, m)
		}
	case "stale", "current":
		if witness && kind == "node" && m.Node != "" {
			d.witnessStaleLocked(&pl, in.From, m, m.T == "stale")
		}
	case "stale_ack":
		if !witness && kind == "witness" && m.VoteTerm == d.p.Term {
			d.staleAckTerm = m.VoteTerm
		}
	case "current_ack":
		if !witness && kind == "witness" {
			if d.persistLocked(&pl, ha.ClearStale(d.p, m.Node, m.VoteTerm)) {
				d.staleAckTerm = 0
				pl.event("info", fmt.Sprintf("Wieder synchron: %s ist aktuell", m.Node))
			}
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
		// nur der CA-Knoten darf entfernen (laut eigener Knotenliste)
		if kind == "node" && in.From == d.cl.CAHolder && !d.removed {
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
	if !next.Equal(d.p) {
		if err := d.saveFn(next); err != nil {
			d.saveAlarmLocked(pl, "Stimme nicht gespeichert, keine Antwort: "+err.Error())
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
		if from != d.cl.CAHolder {
			if !d.alarms["foreign-remove|"+from] {
				d.alarms["foreign-remove|"+from] = true
				pl.event("warn", fmt.Sprintf("Knotenliste von %s ohne diesen Server ignoriert (nur der erste Server darf entfernen)", from))
			}
			return
		}
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
	if err := d.renameRemoved(); err != nil {
		if d.node.CAHolder {
			pl.event("warn", "Alte Knotenliste nicht gesichert: "+err.Error())
		} else {
			// ohne Umbenennen gälte nach einem Neustart wieder die alte Liste
			d.renamePending = true
			pl.event("alarm", "Entfernen nicht dauerhaft gespeichert: "+err.Error())
		}
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

func (d *daemon) renameRemoved() error {
	return os.Rename(d.st.etcPath("cluster.json"), d.st.etcPath("cluster.json.removed"))
}

// removedHousekeepingLocked: Umbenennen wiederholen; nach „pdh-ha reset“
// (node.json fehlt) zurück in den uneingerichteten Zustand.
func (d *daemon) removedHousekeepingLocked(pl *plan) {
	if !d.removed {
		return
	}
	if d.renamePending {
		if err := d.renameRemoved(); err == nil || os.IsNotExist(err) {
			d.renamePending = false
			pl.event("info", "Entfernen dauerhaft gespeichert")
		}
	}
	if !configured(d.st) {
		if d.trCancel != nil {
			d.trCancel()
			d.trCancel = nil
		}
		if _, real := d.net.(*transport); real {
			d.net = nil
		}
		d.configured, d.removed, d.started, d.renamePending = false, false, false, false
		d.node, d.cl, d.p = nodeConfig{}, clusterConfig{}, ha.Persisted{}
		d.carry, d.last = ha.Output{}, ha.Input{}
		d.views = map[string]peerView{}
		pl.event("info", "Zurückgesetzt – bereit zum Einrichten oder Beitreten")
	}
}
