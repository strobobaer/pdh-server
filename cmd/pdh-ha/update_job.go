package main

// Update im Verbund im Dienst (Spec 3.2, 5, 7): Der aktive Server dirigiert
// den Auftrag – Zustandsmaschine ha.UpdateStep in jedem Takt, Auftrag
// speichern (update-job.json), bei gestiegenem Seq an den Partner spiegeln
// (update_job), Aktionen ausführen. Der Partner führt Aufträge an sich
// (update_node) aus und meldet das Ergebnis (update_ack). Wer aktiv ist,
// dirigiert; nach einem Rollentausch oder Neustart setzt der dann aktive
// Server den Auftrag fort.
//
// Vertrag mit ha.UpdateStep: jede node_op/local_op geht mit dem Seq des
// zurückgegebenen Auftrags raus (update_node trägt den ganzen Auftrag), das
// Ergebnis trägt ihn zurück.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

const (
	upVerifyDefault = 10 * time.Minute // Prüffrist (PDH_HA_UPDATE_VERIFY, nur Labor)
	upResendAfter   = 2 * time.Minute  // offenen Auftrag an einen Knoten ohne Ergebnis wiederholen
	upMirrorEvery   = 30 * time.Second // Auftrag auch ohne Änderung erneut spiegeln
	upMirrorKeep    = 10 * time.Minute // beendeten Auftrag so lange weiter spiegeln
	upHealthFresh   = 30 * time.Second // so lange gilt die letzte Antwort von /health
	upMaxAcks       = 8
	upMaxDone       = 16
)

// upState: Auftrag und Hilfszustand des Dienstes (alles unter mu, außer den
// atomaren Feldern).
type upState struct {
	job ha.UpdateJob

	acks         []ha.UpdateAck // Ergebnisse für die nächsten Schritte (eins je Schritt)
	abort        string         // Abbruch durch einen Administrator für den nächsten Schritt
	restartAbort string         // Kennung eines Auftrags, der beim Laden vor der Sperre stand
	takeover     bool           // Übernahme erkannt, gilt bis zum nächsten gespeicherten Schritt
	peerSeq      uint64         // höchster Seq, den der Partner je geschickt hat
	sentSeq      uint64         // zuletzt an den Partner gespiegelter Seq
	sentAt       time.Time      // letzte Spiegelung (Wanduhr)
	opSent       opMark         // offener Auftrag an einen Knoten: seit wann bekannt

	running map[string]bool         // laufende Ops (opKey)
	done    map[string]ha.UpdateAck // Ergebnisse erledigter Ops (Wiederholung ohne zweiten Lauf)

	agent      agentClient
	healthFn   func(context.Context) (pdhHealth, error)
	health     atomic.Pointer[healthNote]
	healthBusy atomic.Bool
}

type opMark struct {
	key string
	at  time.Time
}

type healthNote struct {
	h      pdhHealth
	err    error
	at     time.Time
	commit string // eigener Stand bei der Abfrage – wechselt er, gilt die Antwort nicht mehr
}

func newUpState() upState {
	return upState{running: map[string]bool{}, done: map[string]ha.UpdateAck{},
		agent: newAgentClient(), healthFn: getPDHHealth}
}

// upEnded: kein Auftrag oder beendet.
func upEnded(j ha.UpdateJob) bool {
	return j.Phase == ha.UpNone || j.Phase == ha.UpDone || j.Phase == ha.UpFailed
}

// beforeLock: Phasen vor der Sperre (Spec 7: Neustart → zurückdrehen, abbrechen).
func beforeLock(j ha.UpdateJob) bool {
	switch j.Phase {
	case ha.UpPrepare, ha.UpVerifyPrep, ha.UpCountdown:
		return true
	}
	return false
}

// cloneJob: Kopie mit eigener Old-Tabelle (Nachrichten werden erst nach dem
// gesperrten Teil kodiert).
func cloneJob(j ha.UpdateJob) ha.UpdateJob {
	if j.Old != nil {
		old := make(map[string]string, len(j.Old))
		for k, v := range j.Old {
			old[k] = v
		}
		j.Old = old
	}
	return j
}

// opKey kennzeichnet einen offenen Auftrag an einen Knoten.
func opKey(j ha.UpdateJob, op string) string {
	return j.ID + "|" + op + "|" + strconv.FormatUint(j.WaitSeq, 10)
}

// opCommit: zu welchem Stand die offene Op gehört (für Wiederholungen).
func opCommit(j ha.UpdateJob, op, node string) string {
	switch op {
	case ha.OpInstall, ha.OpInstallNoRestart, ha.OpReinit:
		return j.Target
	case ha.OpRollback, ha.OpRollbackNoRestart:
		return j.Old[node]
	}
	return ""
}

// updateDurationsLocked: Countdown und Höchstdauer der Sperre, Prüffrist.
func (d *daemon) updateDurationsLocked() (countdown, lockMax, verify time.Duration) {
	// Countdown und Höchstdauer kommen aus dem Wartungsfenster (ungültig oder
	// nie gesetzt: Standardwerte, Spec 4). Die Prüffrist nur im Labor verkürzen.
	w := d.windowLocked()
	countdown, lockMax, verify = time.Duration(w.CountdownMin)*time.Minute, time.Duration(w.LockMaxMin)*time.Minute, upVerifyDefault
	if v, err := time.ParseDuration(os.Getenv("PDH_HA_UPDATE_VERIFY")); err == nil && v > 0 {
		verify = v
	}
	return countdown, lockMax, verify
}

// loadUpdateLocked liest Auftrag und fehlerhafte Versionen beim Laden.
// Stand der Auftrag vor der Sperre, bricht der erste Schritt ihn ab (R9).
func (d *daemon) loadUpdateLocked() {
	j, err := d.st.loadJob()
	if err != nil {
		d.alarmOnce("update-job", "Update-Auftrag nicht lesbar: "+err.Error())
		j = ha.UpdateJob{}
	}
	d.migrateFaultyLocked()
	d.up.job, d.up.restartAbort = j, ""
	if j.ID != "" && beforeLock(j) {
		d.up.restartAbort = j.ID
	}
}

// startUpdateLocked legt einen neuen Auftrag an (Task 5: POST /v1/update/start).
// Der nächste Takt spiegelt ihn und führt den ersten Schritt aus.
func (d *daemon) startUpdateLocked(j ha.UpdateJob) error {
	cur := d.up.job
	if cur.ID != "" && !upEnded(cur) {
		return errors.New("Ein Update-Auftrag läuft bereits")
	}
	if j.ID == "" {
		return errors.New("Auftrag ohne Kennung")
	}
	j.Phase, j.PhaseAt = ha.UpPrepare, time.Time{}
	j.Seq = max(cur.Seq, d.up.peerSeq) + 1 // höherer Seq gewinnt beim Spiegeln
	if err := d.st.saveJob(j); err != nil {
		return fmt.Errorf("Update-Auftrag nicht gespeichert: %w", err)
	}
	d.setJobLocked(j)
	d.up.sentSeq = 0
	return nil
}

// setJobLocked übernimmt einen anderen Auftrag; Hilfszustand des alten verfällt.
func (d *daemon) setJobLocked(j ha.UpdateJob) {
	if j.ID != d.up.job.ID {
		d.up.acks, d.up.abort, d.up.opSent, d.up.takeover = nil, "", opMark{}, false
	}
	d.up.job, d.up.restartAbort = j, ""
}

// abortUpdate: Abbruch durch einen Administrator; wirkt im nächsten Schritt.
func (d *daemon) abortUpdate(by string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	j := d.up.job
	if j.ID == "" || upEnded(j) {
		return errors.New("Kein Update-Auftrag offen")
	}
	if d.p.Role != ha.Active {
		return errors.New("Abbrechen nur auf dem aktiven Server")
	}
	// I7: nicht still verwerfen – klar sagen, warum es nichts mehr abzubrechen gibt
	switch j.Phase {
	case ha.UpRollback:
		return errors.New("Zurücksetzen läuft bereits")
	case ha.UpRelease, ha.UpFollow:
		return errors.New("Update ist schon freigegeben – die neue Version ist geprüft")
	}
	if by == "" {
		by = "unbekannt"
	}
	d.up.abort = by
	return nil
}

// updateTakeover: echte Übernahme bzw. Hand-Übernahme seit dem letzten
// Takt – dieser Server wurde mit neuer Amtszeit aktiv, ohne dass es der
// Rollentausch des Auftrags selbst war (CmdSwapFreeze/CmdSwapAbandon/CmdSwap).
func updateTakeover(j ha.UpdateJob, termBefore, termAfter uint64, role ha.Role, swapped bool) bool {
	if j.ID == "" || upEnded(j) || role != ha.Active || termAfter <= termBefore {
		return false
	}
	return !(swapped && swapExpected(j))
}

// swapExpected: in dieser Phase tauscht der Auftrag selbst die Rollen
// (Countdown → Sperre, Rücktausch im Zurücksetzen).
func swapExpected(j ha.UpdateJob) bool {
	switch j.Phase {
	case ha.UpCountdown, ha.UpLocked, ha.UpVerifyNew:
		return true
	case ha.UpRollback:
		return !j.LockedAt.IsZero()
	}
	return false
}

// upWantVersion: welche Version pdh im Auftrag melden muss. Im Zurücksetzen
// die alte; beim reinen Rollentausch die laufende; sonst die Zielversion –
// ist sie (noch) unbekannt, nichts (C1: nie die eigene alte Version als
// Ziel annehmen; der Auftrag übernimmt die Version von B nach dem Einspielen).
func upWantVersion(j ha.UpdateJob, own string) string {
	switch {
	case j.Phase == ha.UpRollback:
		if j.OldVersion != "" {
			return j.OldVersion
		}
		return own
	case j.Kind == ha.UpdateSwap:
		return own
	}
	return j.TargetVersion
}

// frozenCurrentLocked (R11, für „nacheinander“ korrigiert): Dieser Server
// ist der eingefrorene Server des Auftrags und hatte beim Einfrieren
// bestätigt den Gleichstand (FrozenSynced) – seine Daten sind der letzte
// Stand vor der Sperre, das Hochstufen beim Zurücksetzen darf nicht
// abgelehnt werden. Ohne bestätigten Gleichstand nie.
func (d *daemon) frozenCurrentLocked() bool {
	j := d.up.job
	if j.LockedAt.IsZero() || j.Second == "" || j.Second != d.node.ID || !j.FrozenSynced {
		return false
	}
	switch j.Phase {
	case ha.UpLocked, ha.UpVerifyNew, ha.UpRollback:
		return true
	}
	return false
}

// updateTickLocked: im Takt nach dem Entscheidungsschritt. Nur der aktive
// Server dirigiert; alle mit offenem Auftrag fragen das eigene pdh ab
// (Heartbeat-Feld pdh).
func (d *daemon) updateTickLocked(ctx context.Context, pl *plan, termBefore uint64, swapped bool, s ha.Settings, views map[string]peerView) {
	u := &d.up
	j := u.job
	if j.ID == "" {
		return
	}
	open := !upEnded(j)
	if open {
		d.probeHealthLocked(ctx)
	}
	partner, _ := d.peerIDsLocked()
	if partner == "" || d.p.Role != ha.Active || d.unsaved {
		return
	}
	wall := d.wall()
	if !open {
		// beendeten Auftrag noch eine Weile spiegeln (verlorene Nachricht)
		if wall.Sub(j.PhaseAt) < upMirrorKeep {
			d.mirrorJobLocked(pl, partner, wall, j.Seq > u.sentSeq)
		}
		return
	}
	countdown, lockMax, verify := d.updateDurationsLocked()
	pn, _ := d.clusterNodeLocked(partner)
	pv := views[partner]
	want := upWantVersion(j, d.version)
	o := ha.UpdateObs{
		Now: wall, Self: d.node.ID, SelfRole: d.p.Role, SelfCommit: d.commit,
		PartnerID: partner, Partner: obsFrom(pn, pv, d.node.ID),
		SelfPDHOK:      d.selfPDHOKLocked(want, wall),
		PartnerPDHOK:   want != "" && pv.seen() && pv.Last.PDH == "ok:"+want,
		SelfPDHVersion: d.selfPDHVersionLocked(wall),
		MirrorSync:     d.mirrorSyncLocked(s, partner, pv),
		SelfDBPending:  d.selfDBPendingLocked(),
		Handover:       d.carry.HandoverPending != 0,
		Abort:          u.abort,
		Countdown:      countdown, LockMax: lockMax, Verify: verify,
	}
	if pv.seen() {
		o.PartnerPDHVersion = pdhHBVersion(pv.Last.PDH)
	}
	if updateTakeover(j, termBefore, d.p.Term, d.p.Role, swapped) {
		u.takeover = true // bleibt gemerkt, falls das Speichern dieses Schritts scheitert
	}
	o.Takeover = u.takeover
	if u.restartAbort == j.ID {
		o.Abort = "Neustart" // Spec 7: Neustart vor der Sperre → zurückdrehen, abbrechen
	}
	if len(u.acks) > 0 {
		a := u.acks[0]
		o.Ack = &a
	}
	next, acts := ha.UpdateStep(j, o)
	if next.Seq != j.Seq {
		if err := d.st.saveJob(next); err != nil {
			d.saveAlarmLocked(pl, "Update-Auftrag nicht gespeichert, Schritt wird wiederholt: "+err.Error())
			return
		}
		u.job = next
	}
	if o.Ack != nil {
		u.acks = u.acks[1:]
	}
	u.abort, u.restartAbort, u.takeover = "", "", false
	if next.MarkFaulty {
		d.addFaultyLocked(pl, next.Target, true)
	}
	// Auftrag zuerst spiegeln: Der Partner kennt ihn, bevor er durch einen
	// Tausch-Befehl aktiv wird (der Befehl wirkt erst im nächsten Takt).
	d.mirrorJobLocked(pl, partner, wall, next.Seq > u.sentSeq)
	for _, a := range acts {
		switch a.Kind {
		case ha.UpNodeOp:
			d.sendNodeOpLocked(pl, a.Node, a.Op, a.Commit, wall)
		case ha.UpLocalOp:
			d.startLocalOpLocked(ctx, a.Op, a.Commit, wall)
		case ha.UpCommand:
			d.cmd = a.Command
		case ha.UpEvent:
			pl.event(a.EventKind, a.Text)
		}
	}
	if len(acts) == 0 {
		d.resendOpLocked(ctx, pl, partner, pv, wall)
	}
}

// mirrorJobLocked schickt den Auftrag an den Partner – bei Änderung sofort,
// sonst alle upMirrorEvery (eine verlorene Nachricht holt sich so ein).
func (d *daemon) mirrorJobLocked(pl *plan, partner string, wall time.Time, force bool) {
	u := &d.up
	if !force && !u.sentAt.IsZero() && wall.Sub(u.sentAt) < upMirrorEvery {
		return
	}
	j := cloneJob(u.job)
	pl.send(partner, message{T: "update_job", ID: d.node.ID, Seq: d.nextSeq(), JobID: j.ID, Job: &j})
	u.sentSeq, u.sentAt = j.Seq, wall
}

// sendNodeOpLocked: Auftrag an einen Knoten (update_node) mit dem aktuellen Auftrag.
func (d *daemon) sendNodeOpLocked(pl *plan, node, op, commit string, wall time.Time) {
	j := cloneJob(d.up.job)
	pl.send(node, message{T: "update_node", ID: d.node.ID, Seq: d.nextSeq(), Op: op, Commit: commit, JobID: j.ID, Job: &j})
	d.up.opSent = opMark{key: opKey(j, op) + "|" + node, at: wall}
}

// startLocalOpLocked: eigener Agent; das Ergebnis kommt als lokales UpdateAck.
func (d *daemon) startLocalOpLocked(ctx context.Context, op, commit string, wall time.Time) {
	j := cloneJob(d.up.job)
	d.up.opSent = opMark{key: opKey(j, op) + "|" + d.node.ID, at: wall}
	d.runOpAsyncLocked(ctx, j, op, commit, func(a ha.UpdateAck) {
		d.mu.Lock()
		d.queueAckLocked(a)
		d.mu.Unlock()
	})
}

// resendOpLocked: Ein Ergebnis kann verloren gehen (pdh-ha startet beim
// Einspielen neu). Ohne Ergebnis nach upResendAfter geht der offene Auftrag
// erneut raus; der Empfänger führt ihn nicht doppelt aus, und das Einspielen
// desselben Stands ändert nichts.
func (d *daemon) resendOpLocked(ctx context.Context, pl *plan, partner string, pv peerView, wall time.Time) {
	u := &d.up
	j := u.job
	if j.WaitOp == "" || j.WaitOK {
		u.opSent = opMark{}
		return
	}
	key := opKey(j, j.WaitOp) + "|" + j.WaitNode
	if u.opSent.key != key {
		u.opSent = opMark{key: key, at: wall} // z. B. nach Neustart oder Tausch: Frist beginnt jetzt
		return
	}
	if wall.Sub(u.opSent.at) < upResendAfter {
		return
	}
	commit := opCommit(j, j.WaitOp, j.WaitNode)
	switch {
	case j.WaitNode == d.node.ID:
		d.startLocalOpLocked(ctx, j.WaitOp, commit, wall)
	case j.WaitNode == partner && pv.seen():
		d.sendNodeOpLocked(pl, partner, j.WaitOp, commit, wall)
	}
}

// queueAckLocked merkt ein Ergebnis für die nächsten Schritte.
func (d *daemon) queueAckLocked(a ha.UpdateAck) {
	if a.JobID == "" || a.JobID != d.up.job.ID {
		return
	}
	d.up.acks = append(d.up.acks, a)
	if n := len(d.up.acks); n > upMaxAcks {
		d.up.acks = d.up.acks[n-upMaxAcks:]
	}
}

// migrateFaultyLocked übernimmt die frühere knotenlokale Liste
// (update-faulty.json) einmalig in die Verbund-Einstellungen.
func (d *daemon) migrateFaultyLocked() {
	f, err := d.st.loadFaulty()
	if err != nil {
		d.alarmOnce("update-faulty", "Liste fehlerhafter Versionen nicht lesbar: "+err.Error())
		return
	}
	if _, err := os.Stat(d.st.statePath("update-faulty.json")); err != nil {
		return
	}
	cl := d.cl
	cl.Faulty = slices.Clone(d.cl.Faulty)
	changed := false
	for _, c := range f {
		if c != "" && !slices.Contains(cl.Faulty, c) {
			cl.Faulty = append(cl.Faulty, c)
			changed = true
		}
	}
	if changed {
		cl.Version++ // verteilt sich über den Abgleich der Knotenliste
		if err := d.st.saveCluster(cl); err != nil {
			d.alarmOnce("update-faulty", "Fehlerhafte Versionen nicht übernommen: "+err.Error())
			return
		}
		d.cl = cl
	}
	_ = os.Remove(d.st.statePath("update-faulty.json"))
}

// addFaultyLocked merkt einen Commit als fehlerhaft – im ganzen Verbund
// (clusterConfig.Faulty). Nur der dirigierende (aktive) Server erhöht die
// Version und verteilt (publish); der Partner trägt den Commit aus dem
// gespiegelten Auftrag nur lokal nach, ohne die Version zu erhöhen.
func (d *daemon) addFaultyLocked(pl *plan, commit string, publish bool) {
	if commit == "" || slices.Contains(d.cl.Faulty, commit) {
		return
	}
	cl := d.cl
	cl.Faulty = append(slices.Clone(d.cl.Faulty), commit)
	if publish {
		cl.Version++
	}
	if err := d.st.saveCluster(cl); err != nil {
		d.saveAlarmLocked(pl, "Fehlerhafte Version nicht gespeichert: "+err.Error())
		return
	}
	d.cl = cl
	if publish {
		d.net.setCluster(cl)
		pl.send("", message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq(), Cluster: &cl})
	}
}

// mirrorInLocked: gespiegelten Auftrag vom Partner übernehmen – nur mit
// höherem Seq. Erst speichern, dann übernehmen.
func (d *daemon) mirrorInLocked(pl *plan, j ha.UpdateJob) {
	u := &d.up
	if j.ID == u.job.ID && j.Seq >= u.job.Seq {
		u.restartAbort = "" // der dirigierende Server lebt
	}
	u.peerSeq = max(u.peerSeq, j.Seq)
	if !mirrorWins(u.job, j) {
		return
	}
	if err := d.st.saveJob(j); err != nil {
		d.saveAlarmLocked(pl, "Gespiegelter Update-Auftrag nicht gespeichert: "+err.Error())
		return
	}
	d.setJobLocked(j)
	if j.MarkFaulty {
		d.addFaultyLocked(pl, j.Target, false)
	}
}

// mirrorWins: Ersetzt der gespiegelte Auftrag in den eigenen? Derselbe
// Auftrag: höherer Seq gewinnt. Ein anderer, offener Auftrag ersetzt einen
// beendeten (bzw. fehlenden) unabhängig vom Seq – sonst behielte ein
// Partner, dessen Seq vorausliegt (lange weg, Zustand neu), den alten
// Auftrag und dirigierte nach dem Tausch nicht weiter.
func mirrorWins(cur, in ha.UpdateJob) bool {
	if in.ID == "" {
		return false
	}
	if in.ID != cur.ID && !upEnded(in) && (cur.ID == "" || upEnded(cur)) {
		return true
	}
	return in.Seq > cur.Seq
}

// updateNodeLocked: Auftrag des dirigierenden Partners an diesen Server.
// Der mitgeschickte Auftrag wird gespiegelt; das Ergebnis geht als
// update_ack an den Absender.
func (d *daemon) updateNodeLocked(ctx context.Context, pl *plan, from string, m message) {
	if m.Job == nil || m.Op == "" || m.Job.ID == "" {
		return
	}
	d.mirrorInLocked(pl, *m.Job)
	j := cloneJob(*m.Job)
	self := d.node.ID
	d.runOpAsyncLocked(ctx, j, m.Op, m.Commit, func(a ha.UpdateAck) {
		d.mu.Lock()
		nt := d.net
		d.mu.Unlock()
		if nt != nil {
			nt.sendTo(from, message{T: "update_ack", ID: self, Seq: d.nextSeq(), JobID: a.JobID, Ack: &a})
		}
	})
}

// runOpAsyncLocked führt op im Hintergrund aus, höchstens einmal je
// Auftrag/Op/Seq; eine Wiederholung bekommt das gemerkte Ergebnis.
func (d *daemon) runOpAsyncLocked(ctx context.Context, j ha.UpdateJob, op, commit string, done func(ha.UpdateAck)) {
	key := opKey(j, op)
	if d.up.running[key] {
		return // läuft noch, das Ergebnis folgt
	}
	if a, ok := d.up.done[key]; ok {
		a.Seq = max(a.Seq, j.Seq)
		go done(a)
		return
	}
	d.up.running[key] = true
	self := d.node.ID
	go func() {
		err := d.runUpdateOp(ctx, j, op, commit)
		if err != nil && ctx.Err() != nil {
			// Dienst endet (z. B. startet das eigene Einspielen pdh-ha neu):
			// kein Ergebnis – weder merken noch melden. Der dirigierende
			// Server wiederholt den Auftrag nach upResendAfter; „install X“
			// bei laufendem X gilt dann sofort als erledigt.
			d.mu.Lock()
			delete(d.up.running, key)
			d.mu.Unlock()
			return
		}
		a := ha.UpdateAck{JobID: j.ID, Op: op, Node: self, OK: err == nil, Seq: j.Seq}
		if err != nil {
			a.Err = err.Error()
			d.event("alarm", fmt.Sprintf("Update-Auftrag %s fehlgeschlagen: %v", op, err))
		}
		d.mu.Lock()
		delete(d.up.running, key)
		if len(d.up.done) >= upMaxDone {
			d.up.done = map[string]ha.UpdateAck{}
		}
		d.up.done[key] = a
		d.mu.Unlock()
		done(a)
	}()
}

// runUpdateOp führt eine Op des Auftrags an diesem Server aus. Datenbank-
// Schritte laufen über die Aktions-Warteschlange (nie neben einer Aktion),
// der Agent außerhalb – ein Update dauert Minuten, Umschaltungen dürfen
// solange nicht warten.
func (d *daemon) runUpdateOp(ctx context.Context, j ha.UpdateJob, op, commit string) error {
	d.mu.Lock()
	cur, role, term, pa, agent, self := d.commit, d.p.Role, d.p.Term, d.pgActorLocked(), d.up.agent, d.node.ID
	d.mu.Unlock()
	needPG := func() error {
		if pa == nil {
			return errors.New("Datenbank-Spiegel nicht eingerichtet")
		}
		if role == ha.Active {
			// Einfrieren, Auftauen, Neuaufsetzen nur am passiven Server
			// (z. B. verspätete Nachricht nach einem Tausch)
			return errors.New("Dieser Server ist aktiv – Datenbank wird nicht angefasst")
		}
		return nil
	}
	// unseal (sequential, A): Abschottung für den Gleichstand zurücknehmen –
	// vor dem Einspielen (die neue Version muss schreiben) und auf jedem
	// Rückweg. Nur am aktiven Server mit Datenbank-Spiegel.
	unseal := func() error {
		if !j.Sealed || j.First != self || pa == nil || role != ha.Active {
			return nil
		}
		if err := d.queueWait(ctx, pa.unfence); err != nil {
			return fmt.Errorf("Abschottung nicht zurückgenommen: %w", err)
		}
		return nil
	}
	switch op {
	case ha.OpSeal:
		if pa == nil {
			return nil // ohne Datenbank-Spiegel gibt es nichts abzugleichen
		}
		if role != ha.Active {
			return errors.New("Abschotten nur am aktiven Server")
		}
		return d.queueWait(ctx, pa.seal)
	case ha.OpInstall:
		if commit != "" && commit == cur {
			return nil // läuft schon mit diesem Stand
		}
		if j.Kind == ha.UpdateSequential && j.First == self {
			if err := unseal(); err != nil {
				return err
			}
			return agent.Finish(ctx) // in der Vorbereitung gebaut (install_norestart)
		}
		return agent.Install(ctx, commit, false)
	case ha.OpInstallNoRestart:
		return agent.Install(ctx, commit, true)
	case ha.OpRollback, ha.OpRollbackNoRestart:
		noRestart := op == ha.OpRollbackNoRestart
		if noRestart {
			if err := unseal(); err != nil {
				return err
			}
		} else if d.rolledBack(j, commit, cur, pa) {
			// C2: Wiederholung nach dem Neustart von pdh-ha, den das
			// Zurückdrehen selbst auslöst – schon erledigt, kein zweiter Lauf.
			return nil
		}
		if !noRestart && role != ha.Active && pa != nil {
			// eigene Datenbank zuerst wieder Standby des aktiven Servers
			// (abgezweigt, wenn hier die neue Version schrieb); folgt sie
			// schon, ändert rejoin nichts
			if err := d.queueWait(ctx, func(ctx context.Context) error { return pa.rejoin(ctx, term) }); err != nil {
				return fmt.Errorf("Wiedereingliederung fehlgeschlagen: %w", err)
			}
		}
		return agent.Rollback(ctx, commit, noRestart)
	case ha.OpFreeze:
		if err := needPG(); err != nil {
			return err
		}
		return d.queueWait(ctx, pa.freeze)
	case ha.OpThaw:
		if err := needPG(); err != nil {
			return err
		}
		return d.queueWait(ctx, func(ctx context.Context) error { return pa.rejoin(ctx, term) })
	case ha.OpReinit:
		if err := needPG(); err != nil {
			return err
		}
		if d.healthyStandby(j, commit, cur) {
			// Wiederholung: schon gesunder Standby des aktiven mit X – erledigt
			return nil
		}
		if err := d.queueWait(ctx, pa.reinit); err != nil {
			return fmt.Errorf("Neuaufsetzen fehlgeschlagen: %w", err)
		}
		if commit != "" && commit != cur {
			return agent.Install(ctx, commit, false)
		}
		return nil
	}
	return fmt.Errorf("unbekannter Update-Auftrag %q", op)
}

// rolledBack (C2): Das Zurückdrehen auf commit ist schon erledigt – das
// laufende Programm hat diesen Stand, pdh antwortet bereit mit der alten
// Version und die Datenbank folgt (mit Spiegel) als Standby. So startet die
// Wiederholung nach dem Neustart von pdh-ha, den das Zurückdrehen selbst
// auslöst (install-pdh-ha.sh), keinen zweiten Lauf.
func (d *daemon) rolledBack(j ha.UpdateJob, commit, cur string, pa *pgActor) bool {
	if commit == "" || commit != cur {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	want := j.OldVersion
	if want == "" {
		want = d.version
	}
	if !d.selfPDHOKLocked(want, d.wall()) {
		return false
	}
	if pa != nil {
		if st := d.lastPG(); st == nil || st.State != "standby" {
			return false
		}
	}
	return true
}

// healthyStandby (C2): Neuaufsetzen schon erledigt – Standby, der vom aktiven
// Server empfängt, das laufende Programm auf commit und pdh bereit mit der
// Zielversion.
func (d *daemon) healthyStandby(j ha.UpdateJob, commit, cur string) bool {
	if commit != "" && commit != cur {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.lastPG()
	return st != nil && st.State == "standby" && st.Streaming && d.selfPDHOKLocked(upWantVersion(j, d.version), d.wall())
}

// queueWait reiht fn in die Aktions-Warteschlange ein und wartet auf das Ende.
func (d *daemon) queueWait(ctx context.Context, fn func(context.Context) error) error {
	d.actOnce.Do(func() { go d.actionWorker(ctx) })
	res := make(chan error, 1)
	d.pendingActs.Add(1)
	select {
	case d.actQ <- queuedAction{fn: func(ctx context.Context) { res <- fn(ctx) }}:
	case <-ctx.Done():
		d.pendingActs.Add(-1)
		return ctx.Err()
	}
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reinit: Datenbank neu vom aktiven Server kopieren und Standby werden
// (Neuaufsetzen im Nachziehen, Spec 5.1 Schritt 10). Der alte Datenordner
// bleibt bis zum Erfolg erhalten (fullCopy).
func (a *pgActor) reinit(ctx context.Context) error {
	e := a.env()
	if len(e.PartnerAddrs) == 0 {
		return errors.New("Kein aktiver Partner bekannt")
	}
	if err := a.stopServices(ctx); err != nil {
		return fmt.Errorf("pdh bzw. Tunnel nicht gestoppt: %w", err)
	}
	if err := a.stopPG(ctx); err != nil {
		return err
	}
	if err := a.fullCopy(ctx, e); err != nil {
		return err
	}
	if err := a.becomeStandby(ctx, e); err != nil {
		return err
	}
	return a.pg.ctl(ctx, "start")
}

// probeHealthLocked fragt das eigene pdh im Hintergrund ab (höchstens eine
// Abfrage zur Zeit).
func (d *daemon) probeHealthLocked(ctx context.Context) {
	if d.up.healthFn == nil || !d.up.healthBusy.CompareAndSwap(false, true) {
		return
	}
	fn, at, commit := d.up.healthFn, d.wall(), d.commit
	go func() {
		defer d.up.healthBusy.Store(false)
		h, err := fn(ctx)
		d.up.health.Store(&healthNote{h: h, err: err, at: at, commit: commit})
	}()
}

// healthLocked: letzte Antwort von /health, wenn sie frisch ist und zum
// laufenden Stand gehört. Nach einem Wechsel des Stands (Einspielen) zählt
// eine ältere Antwort nicht – sonst übernähme der Auftrag die Version des
// noch alten pdh als Zielversion.
func (d *daemon) healthLocked(wall time.Time) *healthNote {
	n := d.up.health.Load()
	if n == nil || wall.Sub(n.at) > upHealthFresh || n.commit != d.commit {
		return nil
	}
	return n
}

// freshHealthLocked: letzte Antwort von /health, wenn sie frisch ist.
func (d *daemon) freshHealthLocked(wall time.Time) (pdhHealth, bool) {
	n := d.healthLocked(wall)
	if n == nil {
		return pdhHealth{}, false
	}
	return n.h, n.err == nil
}

func (d *daemon) selfPDHOKLocked(want string, wall time.Time) bool {
	h, ok := d.freshHealthLocked(wall)
	return ok && h.ok(want)
}

// pdhHBLocked: Heartbeat-Feld pdh – "ok:<Version>" (bereit),
// "mirror:<Version>" (antwortet mit 200, aber migrations_ok=false: z. B.
// eingeschränkter Spiegel, der auf die Migration am Primary wartet), "down"
// oder "unknown".
func (d *daemon) pdhHBLocked() string {
	n := d.healthLocked(d.wall())
	if n == nil {
		return "unknown"
	}
	switch {
	case n.err == nil && n.h.up():
		return "ok:" + n.h.Version
	case n.err == nil && n.h.Status == http.StatusOK && n.h.Version != "":
		return "mirror:" + n.h.Version
	}
	return "down"
}

// pdhHBVersion: Version aus dem Heartbeat-Feld pdh ("ok:" oder "mirror:"),
// sonst "".
func pdhHBVersion(f string) string {
	for _, p := range []string{"ok:", "mirror:"} {
		if v, ok := strings.CutPrefix(f, p); ok {
			return v
		}
	}
	return ""
}

// selfPDHVersionLocked: Version, die das eigene pdh mit 200 meldet ("" = keine Antwort).
func (d *daemon) selfPDHVersionLocked(wall time.Time) string {
	n := d.healthLocked(wall)
	if n == nil || n.err != nil || n.h.Status != http.StatusOK {
		return ""
	}
	return n.h.Version
}

// selfDBPendingLocked: Datenbank-Spiegel eingerichtet, die eigene Datenbank
// laut letzter Abfrage aber (noch) nicht schreibbarer Primary – z. B. direkt
// nach dem Rücktausch, solange das Hochstufen läuft.
func (d *daemon) selfDBPendingLocked() bool {
	if d.pgActorLocked() == nil {
		return false
	}
	st := d.lastPG()
	return st == nil || st.State != "primary" || st.ReadOnly
}

// mirrorSyncLocked (R1): Standby des Partners verbunden (streaming) und
// Verzögerung unter LagLimit – nicht „synchroner Modus“. Ohne
// Datenbank-Spiegel gibt es nichts aufzuholen.
func (d *daemon) mirrorSyncLocked(s ha.Settings, partner string, pv peerView) bool {
	if d.pgActorLocked() == nil {
		return pv.seen()
	}
	st := d.lastPG()
	if st == nil || st.State != "primary" {
		return false
	}
	sb, ok := partnerStandby(st, partner)
	if !ok || sb.State != "streaming" {
		return false
	}
	var lag int64
	if st.LSN > sb.FlushLSN {
		lag = int64(st.LSN - sb.FlushLSN)
	}
	return s.LagLimit <= 0 || lag < s.LagLimit
}
