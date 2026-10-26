package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Aus der Sicht des Transports werden PeerObs für den Entscheidungskern,
// Zustandsalarme und die Statusanzeige gebaut.

// obsFrom: Sicht auf eine Gegenstelle aus ihrem letzten Heartbeat. Auch eine
// nie gesehene Gegenstelle bekommt ein Objekt (LastSeen 0).
func obsFrom(n clusterNode, v peerView, selfID string) ha.PeerObs {
	o := ha.PeerObs{ID: n.ID, Kind: n.Kind, Seen: v.seen(), LastSeen: v.LastSeen}
	if m := v.Last; m.T == "hb" {
		o.Role, o.Term, o.Version = ha.Role(m.Role), m.Term, m.Ver
		o.Gateway, o.DataCurrent = m.Gateway, m.DataCurrent
		o.Maint, o.MaintSeq = m.Maint, m.MaintSeq
		o.VotedFor = m.VotedFor
		o.PG, o.Sync, o.Stale, o.LSN, o.LagBytes, o.Timeline = m.PG, m.Sync, m.Stale, m.LSN, m.Lag, m.TL
		o.SeesSelf = slices.Contains(m.Sees, selfID)
	}
	return o
}

func (d *daemon) inputLocked(now time.Duration, wall time.Time, s ha.Settings, mode ha.Mode, views map[string]peerView) ha.Input {
	c := d.carry
	in := ha.Input{
		SelfID: d.node.ID, Now: now, Wall: wall, Mode: mode, Version: d.version,
		DataCurrent: true, DataAssumed: true, Settings: s, Persist: d.p,
		HoldUntil: c.HoldUntil, VotePending: c.VotePending, VoteAskedAt: c.VoteAskedAt,
		HandoverPending: c.HandoverPending, HandoverAt: c.HandoverAt, HandoverSentAt: c.HandoverSentAt,
		NoActiveSince: c.NoActiveSince, NoActiveAlarmed: c.NoActiveAlarmed,
		VoteSince: c.VoteSince, VoteAlarmed: c.VoteAlarmed, LastMutual: c.LastMutual,
		HandoverReadyAt: c.HandoverReadyAt, Would: c.Would, DemoteDone: d.demoteDone,
		DemoteFailed: d.demoteFailed, ProbeGranted: d.probeGranted,
	}
	if gl := time.Duration(d.gwOK.Load()); gl > 0 {
		in.GatewayLastOK = gl
		in.Gateway = now-gl <= 3*s.Tick
	}
	if mode == ha.Single {
		return in
	}
	for _, n := range d.cl.Nodes {
		if n.ID == d.node.ID {
			continue
		}
		o := obsFrom(n, views[n.ID], d.node.ID)
		switch {
		case n.Kind == "witness":
			in.Witness = &o
		case in.Partner == nil:
			in.Partner = &o
		}
	}
	return in
}

// nodeObsLocked: Sicht des Zeugen auf alle Knoten.
func (d *daemon) nodeObsLocked(views map[string]peerView) []ha.PeerObs {
	var out []ha.PeerObs
	for _, n := range d.cl.Nodes {
		if n.Kind == "node" && n.ID != d.node.ID {
			out = append(out, obsFrom(n, views[n.ID], d.node.ID))
		}
	}
	return out
}

// clockSkew: Abweichung der Wanduhr der Gegenstelle (Empfangszeit geschätzt
// aus dem letzten Empfang).
func clockSkew(v peerView, now time.Duration, wall time.Time) time.Duration {
	if v.Last.Wall.IsZero() || v.LastSeen == 0 {
		return 0
	}
	recv := wall.Add(-(now - v.LastSeen))
	return v.Last.Wall.Sub(recv)
}

func absDur(x time.Duration) time.Duration {
	if x < 0 {
		return -x
	}
	return x
}

// stateAlarmsLocked: Alarme bei Zustandswechsel (Weg > 1 min weg, Zeuge weg,
// Versionen, Uhr) – je Zustand einmal, bei Rückkehr ein Hinweis.
func (d *daemon) stateAlarmsLocked(pl *plan, now time.Duration, wall time.Time, s ha.Settings, views map[string]peerView) {
	set := func(key string, on bool, alarm, back string) {
		switch {
		case on && !d.alarms[key]:
			d.alarms[key] = true
			pl.event("alarm", alarm)
		case !on && d.alarms[key]:
			delete(d.alarms, key)
			if back != "" {
				pl.event("info", back)
			}
		}
	}
	down := func(key string, alive bool) bool {
		if alive {
			delete(d.downSince, key)
			return false
		}
		if _, ok := d.downSince[key]; !ok {
			d.downSince[key] = now
		}
		return now-d.downSince[key] >= alarmAfter
	}
	versionDiff := ""
	for _, n := range d.cl.Nodes {
		if n.ID == d.node.ID {
			continue
		}
		v := views[n.ID]
		for _, p := range v.Paths {
			k := "path|" + n.ID + "|" + p.Addr
			set(k, down(k, p.Alive), fmt.Sprintf("Weg %s zu %s seit über 1 min ausgefallen", p.Addr, n.ID),
				fmt.Sprintf("Weg %s zu %s wieder da", p.Addr, n.ID))
		}
		if n.Kind == "witness" {
			k := "witness|" + n.ID
			set(k, down(k, v.seen()), fmt.Sprintf("Zeuge %s nicht erreichbar", n.ID), fmt.Sprintf("Zeuge %s wieder erreichbar", n.ID))
		}
		if v.seen() {
			sk := absDur(clockSkew(v, now, wall))
			set("clock|"+n.ID, sk > clockWarn, fmt.Sprintf("Uhrzeit von %s weicht um %.0f s ab", n.ID, sk.Seconds()), "")
			if v.Last.Ver != "" && v.Last.Ver != d.version && versionDiff == "" {
				versionDiff = fmt.Sprintf("Versionen unterschiedlich (hier %s, %s %s)", d.version, n.ID, v.Last.Ver)
			}
		}
	}
	set("version", versionDiff != "", versionDiff, "Versionen wieder gleich")
}

// statusNow liefert die Anzeige für GET /v1/status.
func (d *daemon) statusNow() apiStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statusLocked()
}

func (d *daemon) statusLocked() apiStatus {
	st := apiStatus{
		OK: true, Configured: d.configured, Mode: d.modeLocked(), Observe: true,
		Peers: []apiNode{}, Warnings: []string{}, GatewayTargets: []string{},
		Self:     apiNode{Kind: "node", Role: ha.Active, Version: d.version, Seen: true, Paths: []apiPath{}},
		Settings: ha.DefaultSettings(),
	}
	if d.versionOverride {
		st.Warnings = append(st.Warnings, "Version überschrieben (Test)")
	}
	// Uneingerichtet: fest aktiv (wie ohne pdh-ha)
	if !d.configured {
		st.Error = d.loadErr
		return st
	}
	now, wall := d.mono(), d.wall()
	st.Settings = d.settingsLocked()
	st.Maint, st.CAHolder = d.p.Maint, d.node.CAHolder
	st.GatewayTargets = append(st.GatewayTargets, d.cl.GatewayTargets...)
	st.Self = apiNode{
		ID: d.node.ID, Kind: d.node.Kind, Role: d.p.Role, Term: d.p.Term, Version: d.version, Commit: d.commit,
		Gateway: d.last.Gateway, Seen: true, Paths: []apiPath{},
	}
	st.AutoMode, st.Busy = st.Settings.AutoMode, d.busy.Load()
	if d.node.Kind == "node" {
		var hb message
		d.hbPGLocked(&hb)
		st.Self.PG, st.Self.LSN, st.Self.LagBytes, st.Self.Sync, st.Self.Stale, st.Self.Timeline, st.Self.Diverged =
			hb.PG, hb.LSN, hb.Lag, hb.Sync, hb.Stale, hb.TL, hb.Diverged
		st.Self.DataCurrent = d.dataCurrent || hb.PG == "primary"
		if st.Self.PG == "" {
			st.Self.PG = "unknown"
		}
	}
	// Vor dem ersten Takt (Neustart-Regel noch nicht angewandt) und nach einem
	// Speicherfehler nie „aktiv“ melden – sonst sichert z. B. der Agent zu früh.
	if d.node.Kind == "node" && (d.booting && st.Mode != ha.Single || d.unsaved && d.p.Role == ha.Active) {
		st.Self.Role = ha.Fenced
	}
	if d.node.Kind == "witness" {
		st.Self.Term = max(d.p.VotedTerm, d.p.Term)
	} else if st.Mode != ha.Single && d.last.SelfID != "" {
		_, st.Blocked = ha.Allowed(d.last)
	}
	ago := func(t time.Duration) float64 {
		if t == 0 {
			return -1
		}
		return (now - t).Seconds()
	}
	hosts := map[string]string{d.node.ID: d.hostID}
	witness := ""
	versionDiff := false
	for _, n := range d.cl.Nodes {
		if n.ID == d.node.ID {
			if n.Kind == "witness" {
				witness = n.ID
			}
			continue
		}
		if n.Kind == "witness" {
			witness = n.ID
		}
		v := d.views[n.ID]
		an := apiNode{ID: n.ID, Kind: n.Kind, Seen: v.seen(), LastSeenAgo: ago(v.LastSeen), Paths: []apiPath{}}
		if m := v.Last; m.T == "hb" {
			an.Role, an.Term, an.Version, an.Commit, an.Gateway = ha.Role(m.Role), m.Term, m.Ver, m.Commit, m.Gateway
			an.PG, an.LSN, an.LagBytes, an.Sync, an.Stale, an.Timeline, an.DataCurrent, an.Diverged =
				m.PG, m.LSN, m.Lag, m.Sync, m.Stale, m.TL, m.DataCurrent, m.Diverged
			hosts[n.ID] = m.HostID
		}
		for _, p := range v.Paths {
			an.Paths = append(an.Paths, apiPath{Addr: p.Addr, Alive: p.Alive, LastRecvAgo: ago(p.LastRecv), Err: p.Err})
		}
		if an.Seen {
			sk := clockSkew(v, now, wall)
			an.ClockSkew = math.Round(sk.Seconds()*10) / 10
			if absDur(sk) > clockWarn {
				st.Warnings = append(st.Warnings, fmt.Sprintf("Uhrzeit von %s weicht um %.0f s ab", n.ID, absDur(sk).Seconds()))
			}
			if an.Version != "" && an.Version != d.version {
				versionDiff = true
			}
		}
		st.Peers = append(st.Peers, an)
	}
	if d.removed {
		st.Warnings = append(st.Warnings, "Aus dem Cluster entfernt")
	}
	if st.Mode == ha.Duo {
		st.Warnings = append(st.Warnings, "eingeschränkter Ausfallschutz (kein Zeuge)")
	}
	if wh := hosts[witness]; wh != "" {
		for _, n := range d.cl.Nodes {
			if n.Kind == "node" && hosts[n.ID] == wh {
				st.Warnings = append(st.Warnings, fmt.Sprintf("Zeuge und %s laufen auf derselben Hardware", n.ID))
			}
		}
	}
	if versionDiff {
		st.Warnings = append(st.Warnings, "Versionen unterschiedlich")
	}
	return st
}

// pingRound prüft Standard-Gateway und Zusatzziele im Hintergrund.
func (d *daemon) pingRound(ctx context.Context, tick time.Duration) {
	if !d.pinging.CompareAndSwap(false, true) {
		return
	}
	targets := append([]string(nil), d.cl.GatewayTargets...)
	gwFn, pingFn := d.gwFn, d.pingFn
	go func() {
		defer d.pinging.Store(false)
		if gw, err := gwFn(); err == nil {
			targets = append([]string{gw}, targets...)
		}
		// alle Ziele gleichzeitig; der erste Erfolg zählt, die übrigen werden abgebrochen
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second+tick)
		defer cancel()
		var wg sync.WaitGroup
		for _, t := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if pingFn(pctx, t) {
					d.gwOK.Store(int64(d.mono()))
					cancel()
				}
			}()
		}
		wg.Wait()
	}()
}

func (d *daemon) heartbeatLocked(gw bool, views map[string]peerView, wall time.Time) message {
	m := message{
		T: "hb", ID: d.node.ID, Role: string(d.p.Role), Term: d.p.Term, Ver: d.version, Commit: d.commit,
		DataCurrent: true, DataAssumed: true, Gateway: gw, Maint: d.p.Maint, MaintSeq: d.p.MaintSeq,
		PG: "unknown", PDH: "unknown", Disk: "unknown", Seq: d.nextSeq(), Wall: wall.UTC(),
		HostID: d.hostID, ClusterVer: d.cl.Version,
	}
	for _, n := range d.cl.Nodes {
		if v, ok := views[n.ID]; ok && n.ID != d.node.ID && v.seen() {
			m.Sees = append(m.Sees, n.ID)
		}
	}
	if d.node.Kind == "witness" {
		m.Role = string(ha.Witness)
		m.Term = max(d.p.VotedTerm, d.p.Term)
		m.VotedFor = d.p.VotedFor
	}
	if d.node.Kind == "node" {
		d.hbPGLocked(&m)
	}
	return m
}

// clusterRequestsLocked: meldet ein Knoten eine neuere Knotenliste, anfordern.
func (d *daemon) clusterRequestsLocked(pl *plan, now time.Duration, s ha.Settings, views map[string]peerView) {
	for _, n := range d.cl.Nodes {
		v, ok := views[n.ID]
		if n.Kind != "node" || n.ID == d.node.ID || !ok || v.Last.ClusterVer <= d.cl.Version {
			continue
		}
		if at, ok := d.clReqAt[n.ID]; ok && now-at < clusterReqWait*s.Tick {
			continue
		}
		d.clReqAt[n.ID] = now
		pl.send(n.ID, message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq()})
	}
}
