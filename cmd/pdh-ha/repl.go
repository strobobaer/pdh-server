package main

// Replikation im Takt: Zustand von PostgreSQL abfragen (im Hintergrund),
// synchron/asynchron schalten (asynchron im Trio nur nach Bestätigung des
// Zeugen), den Datenstand des passiven Knotens bestimmen und im Heartbeat melden.

import (
	"context"
	"fmt"
	"time"

	"pdh/internal/ha"
)

// pgActorLocked: der echte Ausführer, falls der Datenbank-Spiegel eingerichtet ist.
func (d *daemon) pgActorLocked() *pgActor {
	pa, _ := d.act.(*pgActor)
	return pa
}

// peerIDsLocked: Kennungen von Partner und Zeuge laut Knotenliste.
func (d *daemon) peerIDsLocked() (partner, witness string) {
	for _, n := range d.cl.Nodes {
		switch {
		case n.ID == d.node.ID:
		case n.Kind == "witness":
			witness = n.ID
		case partner == "":
			partner = n.ID
		}
	}
	return partner, witness
}

// pgRound holt den Zustand von PostgreSQL im Hintergrund (höchstens eine
// Abfrage zur Zeit); der Takt nutzt den zuletzt bekannten Stand.
func (d *daemon) pgRound(ctx context.Context, pa *pgActor) {
	if !d.pgQuerying.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer d.pgQuerying.Store(false)
		st := pa.pg.status(ctx)
		d.pgSt.Store(&st)
	}()
}

// lastPG: zuletzt bekannter Zustand; nil = noch keiner oder kein Spiegel.
func (d *daemon) lastPG() *pgStatus { return d.pgSt.Load() }

// partnerStandby: Eintrag des Partners in pg_stat_replication.
func partnerStandby(st *pgStatus, partner string) (standbyStat, bool) {
	if st == nil {
		return standbyStat{}, false
	}
	for _, s := range st.Standbys {
		if s.App == slotName(partner) {
			return s, true
		}
	}
	return standbyStat{}, false
}

// replInputLocked setzt vor dem Entscheidungsschritt den echten Datenstand.
func (d *daemon) replInputLocked(ctx context.Context, in *ha.Input, s ha.Settings) {
	pa := d.pgActorLocked()
	if pa == nil {
		return
	}
	d.pgRound(ctx, pa)
	st := d.lastPG()
	standby := st != nil && st.State == "standby" && st.Streaming
	var lag int64
	if in.Partner != nil {
		lag = in.Partner.LagBytes
	}
	// Aktuell wird der Standby nur, wenn er streamt und synchron geführt wird.
	// Danach verliert er das nur durch Meldungen des aktiven Knotens (nicht
	// mehr synchron oder „nicht aktuell“): Solange der synchron führt, liegt
	// jede bestätigte Speicherung schon hier, auch wenn die Verbindung gerade
	// abreißt. Ist der Partner nicht zu sehen, gilt der zuletzt gemeldete Wert –
	// sonst wäre der Standby genau dann „nicht aktuell“, wenn er übernehmen soll.
	// Nach einem Neustart ohne Partner bleibt es beim Anfangswert false.
	switch pt := in.Partner; {
	case pt == nil:
		d.dataCurrent = false
	case !pt.Seen:
	case d.dataCurrent:
		d.dataCurrent = pt.PG == "primary" && pt.Sync && !pt.Stale
	default:
		d.dataCurrent = ha.DataCurrent(standby, lag, s.LagLimit, pt)
	}
	in.DataCurrent, in.DataAssumed = d.dataCurrent, false
}

// replSyncLocked: am aktiven Knoten synchron/asynchron schalten und den
// Zeugen informieren. Zustandsänderungen werden vor dem Senden gespeichert.
func (d *daemon) replSyncLocked(ctx context.Context, pl *plan, now time.Duration, s ha.Settings, mode ha.Mode) {
	pa := d.pgActorLocked()
	st := d.lastPG()
	if pa == nil || st == nil || st.State != "primary" || d.p.Role != ha.Active {
		return
	}
	partner, witness := d.peerIDsLocked()
	if partner == "" {
		return
	}
	sb, ok := partnerStandby(st, partner)
	streaming := ok && sb.State == "streaming"
	// „zuletzt streaming“ gilt nur in dieser Amtszeit: ein Stempel von früher
	// meldete den Partner nach einem Tausch sofort als veraltet
	if streaming || d.lastStreaming == 0 || d.lastStreamingTerm != d.p.Term {
		d.lastStreaming, d.lastStreamingTerm = now, d.p.Term
	}
	var lag int64
	if ok && st.LSN > sb.FlushLSN {
		lag = int64(st.LSN - sb.FlushLSN)
	}
	_, partnerStale := d.p.Stale[partner]
	staleAcked := d.staleAckTerm != 0 && d.staleAckTerm == d.p.Term
	out := ha.SyncStep(ha.SyncIn{
		Mode: mode, Now: now, Settings: s, Sync: pa.syncTarget() != "", Streaming: streaming,
		LagBytes: lag, LastStreaming: d.lastStreaming,
		// Hat der Zeuge den Vermerk bestätigt, steht er dort – auch wenn der
		// Partner inzwischen wieder streamt; dann „wieder aktuell“ melden
		StaleAcked: staleAcked, PartnerStale: partnerStale || staleAcked,
	})
	switch {
	case out.MarkStale:
		if !d.persistLocked(pl, ha.MarkStale(d.p, partner, d.p.Term)) {
			return
		}
		pl.event("alarm", fmt.Sprintf("Asynchron: %s ist nicht erreichbar – Speicherungen laufen ohne Spiegel, %s gilt als nicht aktuell", partner, partner))
	case out.ClearStale:
		if !d.persistLocked(pl, ha.ClearStale(d.p, partner, d.p.Term)) {
			return
		}
		pl.event("info", fmt.Sprintf("Wieder synchron: %s ist aktuell", partner))
	}
	if want := out.WantSync; want != (pa.syncTarget() != "") {
		target := ""
		if want {
			target = slotName(partner)
		}
		pa.setSyncTarget(target)
		d.enqueueFnLocked(ctx, func(ctx context.Context) { pa.applySync(ctx) })
		if want {
			pl.event("info", "Synchron mit "+partner)
		}
	}
	if witness == "" {
		return
	}
	if out.SendStale {
		pl.send(witness, message{T: "stale", ID: d.node.ID, Node: partner, VoteTerm: d.p.Term, LSN: st.LSN, Seq: d.nextSeq()})
	}
	if out.SendCurrent {
		pl.send(witness, message{T: "current", ID: d.node.ID, Node: partner, VoteTerm: d.p.Term, Seq: d.nextSeq()})
	}
}

// persistLocked speichert einen geänderten Zustand; false = Speicherfehler (Alarm).
func (d *daemon) persistLocked(pl *plan, next ha.Persisted) bool {
	if next.Equal(d.p) {
		return true
	}
	if err := d.saveFn(next); err != nil {
		d.saveAlarmLocked(pl, "Zustand nicht gespeichert: "+err.Error())
		return false
	}
	d.p = next
	return true
}

// enqueueFnLocked reiht interne Arbeit in die Aktions-Warteschlange ein.
func (d *daemon) enqueueFnLocked(ctx context.Context, fn func(context.Context)) {
	d.actOnce.Do(func() { go d.actionWorker(ctx) })
	d.pendingActs.Add(1)
	go func() {
		select {
		case d.actQ <- queuedAction{fn: fn}:
		case <-ctx.Done():
			d.pendingActs.Add(-1)
		}
	}()
}

// applySync schreibt synchronous_standby_names (nur am schreibbaren Knoten).
func (a *pgActor) applySync(ctx context.Context) {
	st := a.pg.status(ctx)
	if st.State != "primary" {
		return
	}
	if err := a.pg.writeConf(ctx, a.primaryConf(a.env(), st.ReadOnly)); err != nil {
		a.ev.add(ha.Event{Kind: "alarm", Text: "Spiegel-Einstellung nicht geschrieben: " + err.Error()})
		return
	}
	_ = a.pg.ctl(ctx, "reload")
}

// hbPGLocked ergänzt den Heartbeat um den Datenbank-Zustand.
func (d *daemon) hbPGLocked(m *message) {
	pa := d.pgActorLocked()
	st := d.lastPG()
	if pa == nil || st == nil {
		return
	}
	m.PG, m.LSN, m.TL = st.State, st.LSN, st.Timeline
	if st.State == "primary" {
		partner, _ := d.peerIDsLocked()
		m.Sync = pa.syncTarget() != ""
		_, m.Stale = d.p.Stale[partner]
		if sb, ok := partnerStandby(st, partner); ok && st.LSN > sb.FlushLSN {
			m.Lag = int64(st.LSN - sb.FlushLSN)
		}
	}
	if list, err := listDiverged(divergedDir()); err == nil {
		m.Diverged = list
	}
}

// witnessStaleLocked: Zeuge vermerkt bzw. löscht „nicht aktuell“ – erst
// speichern, dann bestätigen.
func (d *daemon) witnessStaleLocked(pl *plan, from string, m message, mark bool) {
	next := ha.ClearStale(d.p, m.Node, m.VoteTerm)
	reply := "current_ack"
	if mark {
		next, reply = ha.MarkStale(d.p, m.Node, m.VoteTerm), "stale_ack"
	}
	if !d.persistLocked(pl, next) {
		return
	}
	pl.send(from, message{T: reply, ID: d.node.ID, Node: m.Node, VoteTerm: m.VoteTerm, Seq: d.nextSeq()})
}

// probeLocked: Zeuge rechnet eine Probestimme, ohne etwas zu speichern.
func (d *daemon) probeLocked(pl *plan, from string, m message) {
	s := d.settingsLocked()
	granted, _ := ha.Vote(d.p, ha.VoteRequest{From: from, Term: m.VoteTerm}, d.nodeObsLocked(d.net.peers(s.Tick)), d.mono(), s)
	pl.send(from, message{T: "vote_resp", ID: d.node.ID, VoteTerm: m.VoteTerm, Granted: granted, Probe: true, Seq: d.nextSeq()})
}
