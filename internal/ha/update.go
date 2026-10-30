package ha

import (
	"fmt"
	"reflect"
	"strings"
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

// UpdateSwapWait: eigene Frist für den Rollentausch in der Sperre; danach
// wird zurückgesetzt (der Tausch selbst ist kein Fehler von X).
const UpdateSwapWait = 3 * time.Minute

// updateSwapRepeat: so oft wird ein Tausch-Befehl höchstens wiederholt.
const updateSwapRepeat = 20 * time.Second

// Grund, wenn der Gleichstand vor dem Einfrieren (nacheinander) scheitert.
const reasonNoSync = "Spiegel nicht auf Gleichstand – Update abgebrochen, nichts geändert"

// InstallUnchangedMarker: So endet die Meldung von install_update.sh, wenn
// es das Ziel vor jeder Änderung verweigert (nicht im Update-Zweig, nicht
// neuer als der installierte Stand). Der Wortlaut im Skript muss gleich
// bleiben. Ein solcher Fehler liegt nicht an X – X nicht fehlerhaft merken.
const InstallUnchangedMarker = "nichts geaendert"

// InstallUnchanged: Meldet die Ausgabe eine Verweigerung vor jeder Änderung?
func InstallUnchanged(output string) bool {
	return strings.Contains(output, InstallUnchangedMarker)
}

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
	// FrozenSynced: Der eingefrorene Server hatte beim Einfrieren den
	// Gleichstand mit dem aktiven (rolling: demoteFreeze wartet darauf;
	// sequential: A war abgeschottet und B hatte aufgeholt). Nur dann darf
	// die eingefrorene Kopie beim Zurücksetzen hochgestuft werden.
	FrozenSynced bool
	// Sealed (nur sequential): A wurde für den Gleichstand abgeschottet
	// (pdh gestoppt, Datenbank nur lesend) und ist noch nicht wieder frei.
	Sealed bool
	// CmdAt: letzter Tausch-Befehl (Wiederholung, Frist UpdateSwapWait).
	CmdAt time.Time
	// Unprotected: Sperre ohne aufgeholten Spiegel aufgehoben (R6).
	Unprotected bool
	// RollbackSent: im Zurücksetzen ging der Auftrag an den Partner schon raus.
	RollbackSent bool
	// InstallSeen: Der Einspiel-Lauf auf First ist beendet (Ergebnis kam, auch
	// verspätet) oder First lief schon sichtbar auf X. Erst dann zählt im
	// Rückweg vor der Sperre „Partner auf Alt-Stand“ als Erfolg (N1) – vorher
	// meldet B während des Baus noch Alt-Stand und alte Version.
	InstallSeen bool
	// FreezeSent (sequential): der Auftrag zum Einfrieren ging schon raus –
	// B kann eingefroren sein und muss auf jedem Rückweg aufgetaut werden.
	FreezeSent bool
}

// UpdateAck ist das Ergebnis eines node_op (vom Partner) oder local_op
// (eigener Agent, Node = eigene Kennung).
type UpdateAck struct {
	JobID, Op, Node string
	OK              bool
	Err             string
	// NoFault: der Fehler liegt nicht an X (z. B. nur die Freigabe der
	// abgeschotteten Datenbank scheiterte) – X nicht als fehlerhaft merken.
	NoFault bool
	Seq     uint64
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
	// SelfPDHVersion, PartnerPDHVersion: Version, die pdh mit HTTP 200
	// meldet – auch als eingeschränkter Spiegel (migrations_ok=false); ""
	// = keine Antwort. Vor der Sperre genügt am Spiegel die Version.
	SelfPDHVersion, PartnerPDHVersion string
	// Handover: der eigene Rollentausch (Übergabe) läuft gerade.
	Handover bool
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
	// FreshStart: erster Schritt nach dem Laden des Auftrags (pdh-ha neu
	// gestartet). Beim Start gibt der Kern eine Abschottung wieder frei (N5).
	FreshStart bool
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
	// OpSeal (sequential, lokal am aktiven Server): abschotten wie beim
	// Abgeben (pdh gestoppt, nur lesend, Verbindungen getrennt) und warten,
	// bis der Spiegel alles eingespielt hat. Scheitert der Gleichstand, nimmt
	// die Op die Abschottung selbst zurück.
	OpSeal = "seal"
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
	r.adoptVersion()
	r.noteInstall()
	if r.j.Kind != UpdateSequential && r.j.Phase == UpLocked && r.selfFirstActive() {
		// Übernahme gelungen: der abgebende Server hat erst nach dem
		// Gleichstand abgegeben (demote/demoteFreeze warten darauf) – gilt
		// auch, wenn im selben Schritt ein Abbruch das Zurücksetzen auslöst.
		r.j.FrozenSynced = true
	}
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
			if a.NoFault {
				// A1: vor jeder Änderung verweigert (z. B. Ziel nicht neuer)
				r.failPrep(fmt.Sprintf("Neue Version auf %s nicht eingespielt: %s", a.Node, a.Err), false)
			} else {
				r.failPrep(fmt.Sprintf("Einspielen auf %s fehlgeschlagen: %s", a.Node, a.Err), true)
			}
		}
		return
	}
	// Startet pdh-ha auf B beim Einspielen neu, geht das Ergebnis verloren;
	// B auf X mit antwortendem pdh gilt dann als Erfolg.
	if j.Kind == UpdateRolling && r.partnerReadyPrep() {
		r.enter(UpVerifyPrep)
		return
	}
	if r.since(j.PhaseAt) > UpdatePrepareWait {
		r.failPrep("Einspielen nicht rechtzeitig abgeschlossen", true)
	}
}

// verifyPrep: rolling prüft B (Commit X, pdh antwortet mit der Zielversion,
// Spiegel aufgeholt); sequential geht sofort weiter. B läuft hier als
// eingeschränkter Spiegel und wartet erwartungsgemäß auf die Migration –
// migrations_ok zählt erst nach der Sperre (verifyNew am neuen Primary).
func (r *upRun) verifyPrep() {
	if r.j.Kind == UpdateSequential || (r.partnerReadyPrep() && r.o.MirrorSync) {
		r.enterCountdown()
		return
	}
	if r.timedOut() {
		r.failPrep("Prüffrist überschritten", true)
	}
}

// countdown: nach Ablauf Sperre an. rolling/swap: der abgebende Server
// (Second, noch aktiv) gibt den Tausch-Befehl; ist First schon aktiv, lief
// der Tausch bereits (veralteter Auftrag nach Spiegelung) – kein zweiter.
// sequential: erst A abschotten und den Gleichstand abwarten (OpSeal), dann
// Second einfrieren; das Einspielen folgt nach dem Ack des Einfrierens.
func (r *upRun) countdown() {
	j, o := &r.j, r.o
	if o.Now.Before(j.CountdownUntil) {
		return
	}
	r.enter(UpLocked)
	j.LockedAt = o.Now
	j.FreezeAcked, j.FrozenSynced = false, false
	switch j.Kind {
	case UpdateSequential:
		j.Sealed = true
		r.localOp(OpSeal, "")
	default:
		if o.Self == j.Second && o.SelfRole == Active {
			r.swapCommand()
		}
	}
}

// swapCommand: Tausch-Befehl der Sperre (rolling: mit Einfrieren, swap: normal).
func (r *upRun) swapCommand() {
	cmd := CmdSwapFreeze
	if r.j.Kind == UpdateSwap {
		cmd = CmdSwap
	}
	r.j.CmdAt = r.o.Now
	r.command(cmd)
}

// locked: warten, bis die neue Version auf dem dirigierenden Server läuft.
// rolling/swap: Der Tausch-Befehl wird wiederholt, solange der abgebende
// Server noch aktiv ist und keine Übergabe läuft (verlorener oder
// gescheiterter Tausch); nach UpdateSwapWait wird zurückgesetzt.
func (r *upRun) locked() {
	j, o := &r.j, r.o
	if j.Kind != UpdateSequential {
		if r.selfFirstActive() {
			r.enter(UpVerifyNew) // FrozenSynced setzt step()
			return
		}
		if o.Self != j.Second || o.SelfRole != Active || o.Handover {
			return
		}
		if r.since(j.PhaseAt) > UpdateSwapWait {
			r.rollback("Rollentausch nicht gelungen", false)
			return
		}
		if r.since(j.CmdAt) >= updateSwapRepeat {
			r.swapCommand()
		}
		return
	}
	if !j.FreezeAcked {
		if o.FreshStart && j.Sealed {
			// N5: pdh-ha startete neu, der Kern gab A beim Start wieder frei –
			// der Gleichstand ist verloren. Erneut abschotten und warten; war
			// B womöglich schon eingefroren, zuerst auftauen.
			switch j.WaitOp {
			case OpSeal:
				r.localOp(OpSeal, "")
				return
			case OpFreeze:
				r.nodeOp(j.Second, OpThaw, "")
				return
			}
		}
		a := r.ack()
		if a == nil {
			return
		}
		switch a.Op {
		case OpSeal:
			if a.OK {
				j.FreezeSent = true
				r.nodeOp(j.Second, OpFreeze, "")
			} else {
				r.sealFailed(a.Err)
			}
		case OpThaw:
			if a.OK {
				r.localOp(OpSeal, "") // B folgt wieder: neu abschotten, Gleichstand abwarten
			} else {
				r.rollback(fmt.Sprintf("Auftauen von %s fehlgeschlagen: %s", j.Second, a.Err), false)
			}
		case OpFreeze:
			if a.OK {
				// A war abgeschottet und B hatte aufgeholt: Gleichstand beim Einfrieren.
				j.FreezeAcked, j.FrozenSynced = true, true
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
			r.rollback("Einspielen fehlgeschlagen: "+a.Err, !a.NoFault) // N4: Freigabe gescheitert ≠ X fehlerhaft
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
	r.j.Unprotected = true
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
		text, kind := j.name()+" abgeschlossen", "info"
		if j.Unprotected {
			// Auftauen scheiterte, A hat X trotzdem eingespielt: Spiegel prüfen
			text, kind = text+" – Spiegel ungeschützt, bitte prüfen", "alarm"
		}
		r.event(kind, text)
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
	if r.selfNewActive() && j.Kind != UpdateSwap && !j.FrozenSynced {
		// Verteidigung (C3): Die eingefrorene Kopie hatte beim Einfrieren
		// keinen bestätigten Gleichstand – sie hochzustufen verlöre Daten.
		// Kein Rücktausch; die neue Version bleibt aktiv, Sperre aus.
		if faulty {
			j.MarkFaulty = true
		}
		text := fmt.Sprintf("Dringend: %s: %s – eingefrorener Stand ohne bestätigten Gleichstand, kein Zurücksetzen; Sperre aufgehoben, bitte von Hand prüfen", j.name(), reason)
		r.finish(UpFailed, reason+"; kein Zurücksetzen (Gleichstand nicht bestätigt)")
		r.event("alarm", text)
		return
	}
	r.enter(UpRollback)
	j.Result = reason
	j.RollbackSent = false
	if faulty {
		j.MarkFaulty = true
	}
	if r.selfNewActive() {
		r.swapBack()
	}
	r.event("alarm", fmt.Sprintf("%s wird zurückgesetzt: %s", j.name(), reason))
}

// swapBack: Rücktausch im Zurücksetzen (CmdSwapAbandon bzw. CmdSwap beim
// reinen Rollentausch).
func (r *upRun) swapBack() {
	cmd := CmdSwapAbandon
	if r.j.Kind == UpdateSwap {
		cmd = CmdSwap
	}
	r.j.CmdAt = r.o.Now
	r.command(cmd)
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
	if o.SelfRole != Active {
		return
	}
	if r.selfNewActive() {
		// Rücktausch läuft noch; verlorenen bzw. gescheiterten Befehl wiederholen
		if !o.Handover && r.since(j.CmdAt) >= updateSwapRepeat {
			r.swapBack()
		}
		return
	}
	if r.unsealFirst() {
		// sequential vor dem Einspielen: A war abgeschottet – erst wieder
		// freigeben (Checkout zurück), dann B auftauen.
		if j.WaitOp != OpRollbackNoRestart {
			r.localOp(OpRollbackNoRestart, j.Old[o.Self])
			return
		}
		a := r.ack()
		if a == nil {
			return
		}
		if !a.OK {
			r.event("alarm", fmt.Sprintf("Abschottung von %s nicht zurückgenommen: %s", o.Self, a.Err))
		}
		j.Sealed = false
		r.clearWait()
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
	} else if op == OpRollback && j.WaitOp == OpRollback && !j.WaitOK && r.partnerOnOld() {
		// Ergebnis verloren (pdh-ha startet beim Zurückdrehen neu), der
		// Partner läuft aber schon auf dem Alt-Stand mit der alten Version.
		r.gotOK()
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
	} else if node != "" && node == r.o.PartnerID && j.WaitOp == OpRollback && j.InstallSeen && r.partnerOnOld() {
		// Ergebnis verloren (pdh-ha auf dem Partner startet beim Zurückdrehen
		// neu), der Partner läuft aber schon auf dem Alt-Stand: erledigt.
		r.finish(UpFailed, j.Result)
		return
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
// zurückdrehen, Meldung; faulty: X fehlerhaft merken. Ohne faulty (A1:
// install_update.sh hat vor jeder Änderung verweigert) bleibt X unmarkiert.
func (r *upRun) failPrep(reason string, faulty bool) {
	r.j.MarkFaulty = faulty
	r.undoPrep(reason, true)
	if faulty {
		r.event("alarm", fmt.Sprintf("%s abgebrochen: %s; Version als fehlerhaft gemerkt", r.j.name(), reason))
		return
	}
	r.event("alarm", fmt.Sprintf("%s abgebrochen: %s; Version nicht als fehlerhaft gemerkt", r.j.name(), reason))
}

// sealFailed (sequential, C3): Der Spiegel hat nach dem Abschotten von A
// nicht aufgeholt – B wird nicht eingefroren. Die Op hat die Abschottung
// zurückgenommen; die Sperre gilt als nie begonnen (nichts geändert), nur
// der Checkout von A geht zurück (Rückweg vor der Sperre). X nicht fehlerhaft.
func (r *upRun) sealFailed(errText string) {
	j := &r.j
	if j.FreezeSent {
		// B kann schon eingefroren sein (Neustart, N5): über das Zurücksetzen
		// – A freigeben, B auftauen –, X nicht fehlerhaft.
		r.rollback(reasonNoSync, false)
		r.event("alarm", fmt.Sprintf("%s: %s (%s)", j.name(), reasonNoSync, errText))
		return
	}
	j.LockedAt = time.Time{}
	r.undoPrep(reasonNoSync, true)
	r.event("alarm", fmt.Sprintf("%s: %s (%s)", j.name(), reasonNoSync, errText))
}

// noteInstall (N1): First auf X gesehen bzw. Einspielen gescheitert. Auch
// ein verspätetes Ergebnis (der Auftrag wartet schon auf etwas anderes)
// zählt – es zeigt, dass der Lauf vorbei ist.
func (r *upRun) noteInstall() {
	j, o := &r.j, r.o
	if j.InstallSeen || j.Target == "" {
		return
	}
	if j.First == o.PartnerID && o.Partner.Commit == j.Target {
		j.InstallSeen = true
		return
	}
	// Ein gescheitertes Einspielen hat B nicht umgestellt: Alt-Stand ist echt.
	// Ein gelungenes nicht – dann erst B auf X sehen (sonst könnte ein noch
	// alter Heartbeat „Alt-Stand“ vortäuschen).
	if a := o.Ack; a != nil && !a.OK && a.JobID == j.ID && a.Node == j.First &&
		(a.Op == OpInstall || a.Op == OpInstallNoRestart) {
		j.InstallSeen = true
	}
}

// adoptVersion (C1): Ohne Zielversion übernimmt der Auftrag die Version,
// die pdh nach dem Einspielen von X meldet (B bzw. bei „nacheinander“ A) –
// dann zeigen Bildschirm und Meldungen die echte Version.
func (r *upRun) adoptVersion() {
	j, o := &r.j, r.o
	if j.TargetVersion != "" || j.Kind == UpdateSwap || j.Target == "" {
		return
	}
	switch {
	case o.Partner.Commit == j.Target && o.PartnerPDHVersion != "":
		j.TargetVersion = o.PartnerPDHVersion
	case o.SelfCommit == j.Target && o.SelfPDHVersion != "":
		j.TargetVersion = o.SelfPDHVersion
	}
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

// partnerOnTarget (nach der Sperre): Partner auf X, pdh ganz bereit
// (Version und migrations_ok).
func (r *upRun) partnerOnTarget() bool {
	return r.o.Partner.Commit == r.j.Target && r.o.PartnerPDHOK
}

// partnerReadyPrep (vor der Sperre): Partner auf X, pdh antwortet mit der
// Zielversion – auch als eingeschränkter Spiegel (C1).
func (r *upRun) partnerReadyPrep() bool {
	j := r.j
	return r.o.Partner.Commit == j.Target && j.TargetVersion != "" && r.o.PartnerPDHVersion == j.TargetVersion
}

// partnerOnOld (Zurückdrehen): Partner auf seinem Alt-Stand, pdh bereit mit
// der alten Version (der Daemon prüft im Zurücksetzen gegen OldVersion).
func (r *upRun) partnerOnOld() bool {
	old := r.j.Old[r.o.PartnerID]
	return old != "" && r.o.Partner.Commit == old && r.o.PartnerPDHOK
}

// unsealFirst: sequential, dieser Server ist A, hat X noch nicht ausgeführt
// und ist noch abgeschottet – im Zurücksetzen zuerst freigeben.
func (r *upRun) unsealFirst() bool {
	j := r.j
	return j.Kind == UpdateSequential && r.o.Self == j.First && !j.FreezeAcked && j.Sealed
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
