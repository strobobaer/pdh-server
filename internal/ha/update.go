package ha

import (
	"fmt"
	"reflect"
	"time"
)

// Update im Verbund: reine Zustandsmaschine des Auftrags (Spec 5–7).
// Keine Uhr, keine Ein-/Ausgabe: alles kommt über UpdateObs, alles geht über
// den zurückgegebenen Auftrag und die Aktionen. Der Daemon des aktiven
// Servers ruft UpdateStep in jedem Takt, speichert den Auftrag
// (update-job.json), spiegelt ihn bei gestiegenem Seq an den Partner und
// führt die Aktionen aus. Prüfung „alles grün“: update_ready.go,
// Anzeige: update_view.go.

// UpdateKind ist die Art des Auftrags.
type UpdateKind string

const (
	UpdateRolling    UpdateKind = "rolling"    // ohne Unterbrechung (mit Rollentausch)
	UpdateSequential UpdateKind = "sequential" // alle nacheinander (Neustart des aktiven)
	UpdateSwap       UpdateKind = "swap"       // nur Rollentausch, keine neue Version
)

// UpdatePhase ist der Schritt des Auftrags; der Text steht so in JSON.
type UpdatePhase string

const (
	UpNone       UpdatePhase = ""
	UpPrepare    UpdatePhase = "vorbereiten"
	UpVerifyPrep UpdatePhase = "pruefen_vorher"
	UpCountdown  UpdatePhase = "countdown"
	UpLocked     UpdatePhase = "gesperrt"
	UpVerifyNew  UpdatePhase = "pruefen_neu"
	UpRelease    UpdatePhase = "freigabe"
	UpFollow     UpdatePhase = "nachziehen"
	UpRollback   UpdatePhase = "zuruecksetzen"
	UpDone       UpdatePhase = "fertig"
	UpFailed     UpdatePhase = "abgebrochen"
)

// UpdatePrepareWait: so lange wartet der Auftrag auf das Ergebnis eines
// Einspielens, Rückdrehens oder Neuaufsetzens (der Agent selbst wartet bis
// 20 min). Die Prüffrist (UpdateObs.Verify) zählt erst ab dem Ergebnis.
const UpdatePrepareWait = 30 * time.Minute

// UpdateJob ist der Auftrag. Old, First und Second setzt der Daemon beim
// Anlegen; Phase UpPrepare mit leerem PhaseAt ist der Eintritt.
type UpdateJob struct {
	ID     string
	Kind   UpdateKind
	Source string // "knopf" | "zeitplan"

	Target, TargetVersion, OldVersion string
	Old                               map[string]string // Knoten-ID → Alt-Commit
	First, Second                     string            // First führt X zuerst aus und ist danach aktiv (rolling: B, sequential: A)

	Phase                                                    UpdatePhase
	PhaseAt, StartedAt, CountdownUntil, LockedAt, ReleasedAt time.Time

	Attempts   int
	Result     string // Ergebnis bzw. Grund in Anwendersprache
	MarkFaulty bool   // X als fehlerhaft merken (der Daemon trägt es in update-faulty.json ein)
	AbortedBy  string
	Seq        uint64 // steigt bei jeder Änderung; höherer Seq gewinnt beim Spiegeln

	// Offener Auftrag an einen Knoten (node_op/local_op) dieser Phase.
	// Der Daemon schickt mit jeder Aktion den Seq des zurückgegebenen
	// Auftrags mit; das Ergebnis (UpdateAck) trägt ihn zurück. Ergebnisse mit
	// kleinerem Seq, anderer Op oder anderem Knoten gelten als veraltet.
	WaitOp, WaitNode string
	WaitSeq          uint64
	WaitOK           bool      // Ergebnis „ok“ ist da
	AckAt            time.Time // wann es kam (Beginn der Prüffrist im Nachziehen)

	// FreezeAcked (nur sequential): Second hat das Einfrieren bestätigt; erst
	// danach spielt der aktive Server X ein (zweiter Schritt der Sperre).
	FreezeAcked bool
	// RollbackSent: im Zurücksetzen ging der Auftrag an den Partner schon raus.
	RollbackSent bool
}

// UpdateAck ist das Ergebnis eines node_op (vom Partner) oder local_op
// (eigener Agent, Node = eigene Kennung).
type UpdateAck struct {
	JobID, Op, Node string
	OK              bool
	Err             string
	Seq             uint64
}

// UpdateObs ist alles, was ein Schritt des Auftrags wissen darf.
type UpdateObs struct {
	Now        time.Time
	Self       string
	SelfRole   Role
	SelfCommit string
	PartnerID  string
	Partner    PeerObs

	SelfPDHOK, PartnerPDHOK bool // /health: erwartete Version, migrations_ok
	// MirrorSync: Standby des Partners verbunden (streaming) und Verzögerung
	// unter der Grenze – nicht „synchroner Modus“ (der ist während der Sperre
	// absichtlich aus, SyncIn.ForceAsync = job.Locked()).
	MirrorSync bool
	// SelfDBPending: Die eigene Datenbank ist (noch) nicht schreibbarer Primary –
	// etwa direkt nach dem Rücktausch, solange das Hochstufen bzw. der Start
	// aus dem eingefrorenen Stand läuft. Ohne Datenbank-Spiegel immer false.
	SelfDBPending bool
	Ack           *UpdateAck

	Countdown, LockMax, Verify time.Duration

	Abort    string // Name des Abbrechenden, "" = keiner
	Takeover bool   // echte Übernahme/Hand-Übernahme seit Start
}

// UpdateActionKind ist die Art einer Aktion des Auftrags.
type UpdateActionKind string

const (
	UpNodeOp  UpdateActionKind = "node_op"  // Partner soll Op ausführen (update_node)
	UpLocalOp UpdateActionKind = "local_op" // eigener Agent soll Op ausführen
	UpCommand UpdateActionKind = "command"  // Bedienbefehl an den eigenen Kern
	UpEvent   UpdateActionKind = "event"    // Meldung (alarm|info)
)

// Ops für node_op und local_op.
const (
	OpInstall           = "install"
	OpInstallNoRestart  = "install_norestart"
	OpRollback          = "rollback"
	OpRollbackNoRestart = "rollback_norestart" // nur Checkout zurück, laufendes Programm bleibt (sequential vor der Sperre)
	OpFreeze            = "freeze"
	OpThaw              = "thaw"
	OpReinit            = "reinit"
)

type UpdateAction struct {
	Kind             UpdateActionKind
	Node, Op, Commit string // Op: siehe Op…-Konstanten
	Command          CommandKind
	EventKind, Text  string
}

// Grund bei überschrittener Höchstdauer der Sperre.
const reasonLockMax = "Höchstdauer überschritten"

// Locked: Countdown vorbei und noch nicht freigegeben (LockedAt gesetzt,
// ReleasedAt leer). Ein beendeter Auftrag sperrt nie.
func (j UpdateJob) Locked() bool {
	return !j.LockedAt.IsZero() && j.ReleasedAt.IsZero() && !j.ended()
}

func (j UpdateJob) ended() bool {
	return j.Phase == UpNone || j.Phase == UpDone || j.Phase == UpFailed
}

// name: Bezeichnung in Meldungen („Update auf 0.77.9“ bzw. „Rollentausch“).
func (j UpdateJob) name() string {
	if j.Kind == UpdateSwap {
		return "Rollentausch"
	}
	v := j.TargetVersion
	if v == "" {
		v = j.Target
		if len(v) > 7 {
			v = v[:7]
		}
	}
	return "Update auf " + v
}

// upRun ist ein Schritt der Zustandsmaschine: Auftrag, Sicht, Aktionen.
type upRun struct {
	j    UpdateJob
	o    UpdateObs
	seq  uint64 // Seq des Auftrags nach diesem Schritt, falls er sich ändert
	acts []UpdateAction
}

// UpdateStep führt den Auftrag einen Schritt weiter. Jede Änderung am
// Auftrag erhöht Seq um eins; jede node_op/local_op geht je Phaseneintritt
// nur einmal raus. Nach dem Ende (Done/Failed) geht keine Op mehr raus.
func UpdateStep(job UpdateJob, o UpdateObs) (UpdateJob, []UpdateAction) {
	if job.ended() {
		return job, nil
	}
	r := &upRun{j: job, o: o, seq: job.Seq + 1}
	r.step()
	if !reflect.DeepEqual(r.j, job) {
		r.j.Seq = r.seq
	}
	return r.j, r.acts
}

func (r *upRun) step() {
	o := r.o
	if o.Takeover {
		// Spec 7: Auftrag abbrechen, kein weiterer automatischer Schritt.
		r.event("alarm", "Auftrag abgebrochen: Übernahme")
		r.finish(UpFailed, "Übernahme")
		return
	}
	if o.Abort != "" && r.abort() {
		return
	}
	if r.overdue() {
		return
	}
	switch r.j.Phase {
	case UpPrepare:
		r.prepare()
	case UpVerifyPrep:
		r.verifyPrep()
	case UpCountdown:
		r.countdown()
	case UpLocked:
		r.locked()
	case UpVerifyNew:
		r.verifyNew()
	case UpRelease:
		r.release()
	case UpFollow:
		r.followStep()
	case UpRollback:
		r.rollbackStep()
	}
}

// prepare: X auf dem ersten Server vorbereiten (rolling: B spielt X ein;
// sequential: A baut X im Hintergrund). swap überspringt die Vorbereitung.
func (r *upRun) prepare() {
	j, o := &r.j, r.o
	if j.PhaseAt.IsZero() {
		j.PhaseAt = o.Now
		if j.StartedAt.IsZero() {
			j.StartedAt = o.Now
		}
		switch j.Kind {
		case UpdateSwap:
			r.enterCountdown()
		case UpdateSequential:
			r.localOp(OpInstallNoRestart, j.Target)
		default:
			r.nodeOp(j.First, OpInstall, j.Target)
		}
		return
	}
	if a := r.ack(); a != nil {
		if a.OK {
			r.enter(UpVerifyPrep) // Prüffrist läuft ab dem Ergebnis
		} else {
			r.failPrep(fmt.Sprintf("Einspielen auf %s fehlgeschlagen: %s", a.Node, a.Err))
		}
		return
	}
	// Startet pdh-ha auf B beim Einspielen neu, geht das Ergebnis verloren;
	// B auf X mit bereitem pdh gilt dann als Erfolg.
	if j.Kind == UpdateRolling && r.partnerOnTarget() {
		r.enter(UpVerifyPrep)
		return
	}
	if r.since(j.PhaseAt) > UpdatePrepareWait {
		r.failPrep("Einspielen nicht rechtzeitig abgeschlossen")
	}
}

// verifyPrep: rolling prüft B (Commit X, pdh bereit, Spiegel aufgeholt);
// sequential geht sofort weiter.
func (r *upRun) verifyPrep() {
	if r.j.Kind == UpdateSequential || (r.partnerOnTarget() && r.o.MirrorSync) {
		r.enterCountdown()
		return
	}
	if r.timedOut() {
		r.failPrep("Prüffrist überschritten")
	}
}

// countdown: nach Ablauf Sperre an. rolling/swap: der abgebende Server
// (Second, noch aktiv) gibt den Tausch-Befehl; ist First schon aktiv, lief
// der Tausch bereits (veralteter Auftrag nach Spiegelung) – kein zweiter.
// sequential: erst Second einfrieren, das Einspielen folgt nach dem Ack.
func (r *upRun) countdown() {
	j, o := &r.j, r.o
	if o.Now.Before(j.CountdownUntil) {
		return
	}
	r.enter(UpLocked)
	j.LockedAt = o.Now
	j.FreezeAcked = false
	switch j.Kind {
	case UpdateSequential:
		r.nodeOp(j.Second, OpFreeze, "")
	default:
		if o.Self == j.Second && o.SelfRole == Active {
			cmd := CmdSwapFreeze
			if j.Kind == UpdateSwap {
				cmd = CmdSwap
			}
			r.command(cmd)
		}
	}
}

// locked: warten, bis die neue Version auf dem dirigierenden Server läuft.
func (r *upRun) locked() {
	j, o := &r.j, r.o
	if j.Kind != UpdateSequential {
		if r.selfFirstActive() {
			r.enter(UpVerifyNew)
		}
		return
	}
	if !j.FreezeAcked {
		if a := r.ack(); a != nil {
			if a.OK {
				j.FreezeAcked = true
				r.localOp(OpInstall, j.Target)
			} else {
				r.rollback(fmt.Sprintf("Einfrieren von %s fehlgeschlagen: %s", j.Second, a.Err), false)
			}
		}
		return
	}
	if o.SelfCommit == j.Target {
		r.enter(UpVerifyNew)
		return
	}
	if a := r.ack(); a != nil {
		if a.OK {
			r.gotOK() // Neustart folgt, dann SelfCommit == X
		} else {
			r.rollback("Einspielen fehlgeschlagen: "+a.Err, true)
		}
	}
}

// verifyNew: neue Version prüfen (Frist Verify ab Phasenbeginn). Die eigene
// Datenbank muss dabei schon Primary sein – das Auftauen des Partners
// (Wiedereingliederung) braucht eine hochgestufte Quelle.
func (r *upRun) verifyNew() {
	j := &r.j
	if r.o.SelfPDHOK && !r.o.SelfDBPending {
		if j.Kind == UpdateSwap {
			r.event("info", "Rollentausch abgeschlossen")
			r.finish(UpDone, "Rollentausch abgeschlossen")
			return
		}
		r.enter(UpRelease)
		r.nodeOp(j.Second, OpThaw, "")
		return
	}
	if r.timedOut() {
		r.rollback("Prüfung der neuen Version fehlgeschlagen", r.newStarted())
	}
}

// release: Second taut auf und holt auf. Scheitert das Auftauen, wird die
// Sperre trotzdem aufgehoben (R6, unprotected) – zurückgesetzt wird hier
// nicht mehr, X ist geprüft.
func (r *upRun) release() {
	if a := r.ack(); a != nil {
		if !a.OK {
			r.unprotected(fmt.Sprintf("Auftauen von %s fehlgeschlagen: %s", a.Node, a.Err))
			return
		}
		r.gotOK()
	}
	if r.o.MirrorSync {
		r.follow()
	}
}

// unprotected: Sperre aufheben, obwohl der Spiegel nicht aufgeholt hat;
// dringender Alarm, dann nachziehen (dessen Fehlerweg endet im Neuaufsetzen).
func (r *upRun) unprotected(reason string) {
	r.event("alarm", fmt.Sprintf("Dringend: %s: Spiegel nicht aufgeholt – Verbund ungeschützt (%s)", r.j.name(), reason))
	r.follow()
}

// follow: Freigabe (Sperre aus), dann zieht Second X nach.
func (r *upRun) follow() {
	r.enter(UpFollow)
	r.j.ReleasedAt = r.o.Now
	r.nodeOp(r.j.Second, OpInstall, r.j.Target)
}

// followStep: Second spielt X ein. Frist: bis zum Ergebnis
// UpdatePrepareWait, danach Verify ab dem Ergebnis. Ein zweiter Versuch,
// danach Neuaufsetzen (reinitStep).
func (r *upRun) followStep() {
	j, o := &r.j, r.o
	if j.WaitOp == OpReinit {
		r.reinitStep()
		return
	}
	failed := false
	if a := r.ack(); a != nil {
		if a.OK {
			r.gotOK()
		} else {
			failed = true
		}
	}
	if !failed && r.partnerOnTarget() {
		text := j.name() + " abgeschlossen"
		r.event("info", text)
		r.finish(UpDone, text)
		return
	}
	if !failed {
		if j.WaitOK {
			failed = r.since(j.AckAt) > o.Verify
		} else {
			failed = r.since(j.PhaseAt) > UpdatePrepareWait
		}
	}
	if !failed {
		return
	}
	j.PhaseAt = o.Now // neue Frist
	if j.Attempts == 0 {
		j.Attempts = 1
		r.nodeOp(j.Second, OpInstall, j.Target)
		return
	}
	r.nodeOp(j.Second, OpReinit, j.Target)
	r.event("info", fmt.Sprintf("%s: %s konnte die neue Version nicht einspielen und wird neu aufgesetzt", j.name(), j.Second))
}

// reinitStep: auf das Ergebnis des Neuaufsetzens warten; der Auftrag ist
// danach in jedem Fall fertig (die neue Version läuft auf dem aktiven).
func (r *upRun) reinitStep() {
	j := &r.j
	name, second := j.name(), j.Second
	var errText string
	if a := r.ack(); a != nil {
		if a.OK {
			r.finish(UpDone, name+" abgeschlossen, "+second+" wurde neu aufgesetzt")
			r.event("alarm", fmt.Sprintf("%s abgeschlossen: %s wurde neu aufgesetzt", name, second))
			return
		}
		errText = a.Err
	} else if r.since(j.PhaseAt) > UpdatePrepareWait {
		errText = "keine Rückmeldung"
	} else {
		return
	}
	r.finish(UpDone, name+" abgeschlossen, "+second+" konnte nicht neu aufgesetzt werden")
	r.event("alarm", fmt.Sprintf("Dringend: %s: %s konnte nicht neu aufgesetzt werden (%s) – Verbund ungeschützt, bitte von Hand prüfen", name, second, errText))
}

// rollback: Zurücksetzen in der Sperre beginnen (Spec 5.4). Läuft die neue
// Version auf diesem (aktiven) Server, gibt er zuerst zurück: CmdSwapAbandon
// (bzw. CmdSwap beim reinen Rollentausch). Der dann aktive alte Server
// dirigiert weiter (rollbackStep).
func (r *upRun) rollback(reason string, faulty bool) {
	j := &r.j
	r.enter(UpRollback)
	j.Result = reason
	j.RollbackSent = false
	if faulty {
		j.MarkFaulty = true
	}
	if r.selfNewActive() {
		cmd := CmdSwapAbandon
		if j.Kind == UpdateSwap {
			cmd = CmdSwap
		}
		r.command(cmd)
	}
	r.event("alarm", fmt.Sprintf("%s wird zurückgesetzt: %s", j.name(), reason))
}

// rollbackStep: ohne Sperre (vor dem Countdown begonnen) nur auf das
// Rückdrehen warten. In der Sperre: der alte Server ist aktiv → Partner
// zurückdrehen; fertig, wenn das Ergebnis ok ist, der Spiegel aufgeholt hat
// und das eigene pdh antwortet (Spec 5.4 Schritt 4), dann Sperre aus.
func (r *upRun) rollbackStep() {
	j, o := &r.j, r.o
	if j.LockedAt.IsZero() {
		r.undoStep()
		return
	}
	if o.SelfRole != Active || r.selfNewActive() {
		return // Rücktausch läuft noch
	}
	op, commit := r.rollbackOp()
	if !j.RollbackSent {
		if o.SelfDBPending && op != "" {
			// Erst wenn die eigene Datenbank Primary ist: Die Wiedereingliederung
			// des Partners (pg_rewind) braucht eine hochgestufte Quelle. Gegen den
			// noch laufenden Standby sähe pg_rewind dieselbe Zeitleiste, hielte
			// nichts für zurückzuspulen, und der Partner bliebe mit seinem
			// neueren Stand (Datenbank-Änderungen von X) hängen.
			return
		}
		j.RollbackSent = true
		if op != "" {
			r.nodeOp(o.PartnerID, op, commit)
		}
		return
	}
	if a := r.ack(); a != nil {
		if a.OK {
			r.gotOK()
		} else {
			// Zurücksetzen selbst gescheitert: Sperre bleibt bis zur Höchstdauer.
			j.WaitOp = ""
			r.event("alarm", fmt.Sprintf("Zurücksetzen auf %s fehlgeschlagen: %s", a.Node, a.Err))
			return
		}
	}
	if (op == "" || j.WaitOK) && o.MirrorSync && o.SelfPDHOK {
		r.event("alarm", fmt.Sprintf("%s zurückgesetzt: %s", j.name(), j.Result))
		r.finish(UpFailed, j.Result)
	}
}

// rollbackOp: was der Partner im Zurücksetzen tun muss. swap: nichts (der
// alte Aktive hat seine Datenbank als Standby behalten). sequential, solange
// A X noch nicht ausgeführt hat: B nur auftauen. Sonst Partner per
// Wiedereingliederung zurück und auf seinen Alt-Commit.
func (r *upRun) rollbackOp() (op, commit string) {
	switch r.j.Kind {
	case UpdateSwap:
		return "", ""
	case UpdateSequential:
		if r.o.Self == r.j.First {
			return OpThaw, ""
		}
	}
	return OpRollback, r.j.Old[r.o.PartnerID]
}

// undoPrep: Rückweg vor der Sperre (R8/R10). rolling: B zurück auf Alt;
// sequential: A dreht nur den Checkout zurück (das laufende Programm ist
// noch alt). Ist nichts zurückzudrehen (swap, Eintritt noch nicht gelaufen),
// endet der Auftrag sofort; sonst läuft UpRollback ohne Sperre bis zum
// Ergebnis (undoStep).
func (r *upRun) undoPrep(result string, sent bool) {
	j := &r.j
	if !sent || j.Kind == UpdateSwap {
		r.finish(UpFailed, result)
		return
	}
	r.enter(UpRollback)
	j.Result = result
	j.RollbackSent = true
	if j.Kind == UpdateSequential {
		r.localOp(OpRollbackNoRestart, j.Old[r.o.Self])
	} else {
		r.nodeOp(j.First, OpRollback, j.Old[j.First])
	}
}

// undoStep: Ergebnis des Rückdrehens vor der Sperre abwarten.
func (r *upRun) undoStep() {
	j := &r.j
	node := j.WaitNode
	var errText string
	if a := r.ack(); a != nil {
		if a.OK {
			r.finish(UpFailed, j.Result)
			return
		}
		errText = a.Err
	} else if r.since(j.PhaseAt) > UpdatePrepareWait {
		errText = "keine Rückmeldung"
	} else {
		return
	}
	r.finish(UpFailed, j.Result)
	r.event("alarm", fmt.Sprintf("%s braucht Hilfe: Rückdrehen fehlgeschlagen (%s)", node, errText))
}

// abort: Abbruch durch einen Administrator. Vor der Sperre: abbrechen und
// zurückdrehen; in der Sperre: Zurücksetzen, X nicht fehlerhaft.
// Freigabe (R9), Nachziehen und Zurücksetzen: der Abbruch wird übergangen –
// die neue Version ist geprüft bzw. der Rückweg läuft schon.
func (r *upRun) abort() bool {
	j, o := &r.j, r.o
	switch j.Phase {
	case UpPrepare, UpVerifyPrep, UpCountdown:
		sent := j.Phase != UpPrepare || !j.PhaseAt.IsZero()
		j.AbortedBy = o.Abort
		r.undoPrep("abgebrochen von "+o.Abort, sent)
		r.event("info", fmt.Sprintf("%s abgebrochen von %s", j.name(), o.Abort))
		return true
	case UpLocked, UpVerifyNew:
		j.AbortedBy = o.Abort
		r.rollback("abgebrochen von "+o.Abort, false)
		return true
	}
	return false
}

// overdue: Höchstdauer der Sperre. Locked/VerifyNew → Zurücksetzen;
// Freigabe → Sperre trotzdem aufheben und nachziehen (R6); Zurücksetzen →
// aufgeben, Sperre aus, dringender Alarm. Das Zurücksetzen bekommt dabei
// mindestens die Prüffrist.
func (r *upRun) overdue() bool {
	j, o := &r.j, r.o
	if j.LockedAt.IsZero() || !j.ReleasedAt.IsZero() || o.Now.Sub(j.LockedAt) <= o.LockMax {
		return false
	}
	switch j.Phase {
	case UpLocked, UpVerifyNew:
		r.rollback(reasonLockMax, r.newStarted())
		return true
	case UpRelease:
		r.unprotected(reasonLockMax)
		return true
	case UpRollback:
		if !r.timedOut() {
			return false
		}
		r.event("alarm", fmt.Sprintf("Dringend: %s – %s, Zurücksetzen nicht abgeschlossen; Sperre aufgehoben, bitte von Hand prüfen", j.name(), reasonLockMax))
		r.finish(UpFailed, reasonLockMax+", Zurücksetzen nicht abgeschlossen")
		return true
	}
	return false
}

// failPrep: Fehler vor der Sperre (Spec 7 „B scheitert vor der Sperre“):
// zurückdrehen, X fehlerhaft, Meldung.
func (r *upRun) failPrep(reason string) {
	r.j.MarkFaulty = true
	r.undoPrep(reason, true)
	r.event("alarm", fmt.Sprintf("%s abgebrochen: %s; Version als fehlerhaft gemerkt", r.j.name(), reason))
}

// --- Hilfen ---

func (r *upRun) enter(p UpdatePhase) {
	r.j.Phase = p
	r.j.PhaseAt = r.o.Now
	r.clearWait()
}

func (r *upRun) clearWait() {
	r.j.WaitOp, r.j.WaitNode, r.j.WaitSeq, r.j.WaitOK, r.j.AckAt = "", "", 0, false, time.Time{}
}

func (r *upRun) enterCountdown() {
	r.enter(UpCountdown)
	r.j.CountdownUntil = r.o.Now.Add(r.o.Countdown)
}

// finish beendet den Auftrag; eine noch laufende Sperre endet mit ihm.
// Danach dürfen nur noch Meldungen folgen, keine Ops.
func (r *upRun) finish(p UpdatePhase, result string) {
	r.enter(p)
	r.j.Result = result
	if !r.j.LockedAt.IsZero() && r.j.ReleasedAt.IsZero() {
		r.j.ReleasedAt = r.o.Now
	}
}

func (r *upRun) nodeOp(node, op, commit string) {
	r.acts = append(r.acts, UpdateAction{Kind: UpNodeOp, Node: node, Op: op, Commit: commit})
	r.wait(op, node)
}

// localOp: eigener Agent; das Ergebnis kommt als UpdateAck mit Node = Self.
func (r *upRun) localOp(op, commit string) {
	r.acts = append(r.acts, UpdateAction{Kind: UpLocalOp, Node: r.o.Self, Op: op, Commit: commit})
	r.wait(op, r.o.Self)
}

func (r *upRun) wait(op, node string) {
	r.clearWait()
	r.j.WaitOp, r.j.WaitNode, r.j.WaitSeq = op, node, r.seq
}

// gotOK vermerkt ein passendes Ergebnis „ok“; weitere Zustellungen zählen nicht.
func (r *upRun) gotOK() {
	r.j.WaitOK = true
	r.j.AckAt = r.o.Now
}

func (r *upRun) command(c CommandKind) {
	r.acts = append(r.acts, UpdateAction{Kind: UpCommand, Command: c})
}

func (r *upRun) event(kind, text string) {
	r.acts = append(r.acts, UpdateAction{Kind: UpEvent, EventKind: kind, Text: text})
}

// ack liefert das Ergebnis, wenn es zum offenen Auftrag dieser Phase passt.
func (r *upRun) ack() *UpdateAck {
	a, j := r.o.Ack, r.j
	if a == nil || j.WaitOp == "" || j.WaitOK || a.JobID != j.ID ||
		a.Op != j.WaitOp || a.Node != j.WaitNode || a.Seq < j.WaitSeq {
		return nil
	}
	return a
}

func (r *upRun) since(t time.Time) time.Duration { return r.o.Now.Sub(t) }

func (r *upRun) timedOut() bool { return r.since(r.j.PhaseAt) > r.o.Verify }

func (r *upRun) partnerOnTarget() bool {
	return r.o.Partner.Commit == r.j.Target && r.o.PartnerPDHOK
}

func (r *upRun) selfFirstActive() bool { return r.o.Self == r.j.First && r.o.SelfRole == Active }

// selfNewActive: dieser Server ist aktiv und führt (bzw. führte) X aus –
// er muss im Zurücksetzen abgeben.
func (r *upRun) selfNewActive() bool {
	return r.selfFirstActive() && (r.j.Kind != UpdateSequential || r.j.FreezeAcked)
}

// newStarted: die neue Version wurde gestartet; ein Fehler ab hier geht zu
// Lasten von X (Spec 7). Scheitert nur der Tausch, ist X nicht fehlerhaft.
func (r *upRun) newStarted() bool {
	switch r.j.Kind {
	case UpdateSwap:
		return false
	case UpdateSequential:
		return r.j.FreezeAcked
	}
	return r.j.Phase == UpVerifyNew || r.selfFirstActive()
}
