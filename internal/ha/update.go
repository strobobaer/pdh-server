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
// führt die Aktionen aus.

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
	WaitOK           bool // Ergebnis „ok“ ist da

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
	Ack        *UpdateAck

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
	OpInstall          = "install"
	OpInstallNoRestart = "install_norestart"
	OpRollback         = "rollback"
	OpFreeze           = "freeze"
	OpThaw             = "thaw"
	OpReinit           = "reinit"
)

type UpdateAction struct {
	Kind             UpdateActionKind
	Node, Op, Commit string // Op: "install" | "install_norestart" | "rollback" | "freeze" | "thaw" | "reinit"
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
// nur einmal raus.
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
		if o.MirrorSync {
			r.follow()
		}
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
			r.enter(UpVerifyPrep)
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
	if r.timedOut() {
		r.failPrep("Prüffrist überschritten")
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
			j.WaitOK = true // Neustart folgt, dann SelfCommit == X
		} else {
			r.rollback("Einspielen fehlgeschlagen: "+a.Err, true)
		}
	}
}

// verifyNew: neue Version prüfen (Frist Verify ab Phasenbeginn).
func (r *upRun) verifyNew() {
	j := &r.j
	if r.o.SelfPDHOK {
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

// follow: Freigabe (Sperre aus), dann zieht Second X nach.
func (r *upRun) follow() {
	r.enter(UpFollow)
	r.j.ReleasedAt = r.o.Now
	r.nodeOp(r.j.Second, OpInstall, r.j.Target)
}

// followStep: Second spielt X ein; ein zweiter Versuch, danach neu aufsetzen.
func (r *upRun) followStep() {
	j := &r.j
	failed := false
	if a := r.ack(); a != nil {
		if a.OK {
			j.WaitOK = true
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
	if !failed && !j.WaitOK && r.timedOut() {
		failed = true // kein Ergebnis innerhalb der Frist
	}
	if !failed {
		return
	}
	if j.Attempts == 0 {
		j.Attempts = 1
		j.PhaseAt = r.o.Now // neue Frist für den zweiten Versuch
		r.nodeOp(j.Second, OpInstall, j.Target)
		return
	}
	second := j.Second
	r.finish(UpDone, "abgeschlossen, "+second+" wird neu aufgesetzt")
	r.nodeOp(second, OpReinit, j.Target)
	r.event("alarm", fmt.Sprintf("%s: %s konnte die neue Version nicht einspielen und wird neu aufgesetzt", j.name(), second))
}

// rollback: Zurücksetzen beginnen (Spec 5.4). Läuft die neue Version auf
// diesem (aktiven) Server, gibt er zuerst zurück: CmdSwapAbandon (bzw.
// CmdSwap beim reinen Rollentausch). Der dann aktive alte Server dirigiert
// weiter (rollbackStep).
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

// rollbackStep: der alte Server ist aktiv → Partner zurückdrehen, auf das
// Ergebnis und den aufgeholten Spiegel warten, dann Sperre aus.
func (r *upRun) rollbackStep() {
	j, o := &r.j, r.o
	if o.SelfRole != Active || r.selfNewActive() {
		return // Rücktausch läuft noch
	}
	op, commit := r.rollbackOp()
	if !j.RollbackSent {
		j.RollbackSent = true
		if op != "" {
			r.nodeOp(o.PartnerID, op, commit)
		}
		return
	}
	if a := r.ack(); a != nil {
		if a.OK {
			j.WaitOK = true
		} else {
			// Zurücksetzen selbst gescheitert: Sperre bleibt bis zur Höchstdauer.
			j.WaitOp = ""
			r.event("alarm", fmt.Sprintf("Zurücksetzen auf %s fehlgeschlagen: %s", a.Node, a.Err))
			return
		}
	}
	if (op == "" || j.WaitOK) && o.MirrorSync {
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

// abort: Abbruch durch einen Administrator. Vor der Sperre: abbrechen (B
// zurück); in der Sperre: Zurücksetzen, X nicht fehlerhaft. Danach (Rollback,
// Freigabe, Nachziehen) zählt der Abbruch nicht mehr.
func (r *upRun) abort() bool {
	j, o := &r.j, r.o
	switch j.Phase {
	case UpPrepare, UpVerifyPrep, UpCountdown:
		sent := j.Phase != UpPrepare || !j.PhaseAt.IsZero()
		j.AbortedBy = o.Abort
		r.finish(UpFailed, "abgebrochen von "+o.Abort)
		if j.Kind == UpdateRolling && sent {
			r.nodeOp(j.First, OpRollback, j.Old[j.First])
		}
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
// Freigabe → Sperre trotzdem aufheben und nachziehen (die neue Version ist
// geprüft); Zurücksetzen → aufgeben, Sperre aus, dringender Alarm. Das
// Zurücksetzen bekommt dabei mindestens die Prüffrist.
func (r *upRun) overdue() bool {
	j, o := &r.j, r.o
	if j.LockedAt.IsZero() || o.Now.Sub(j.LockedAt) <= o.LockMax {
		return false
	}
	switch j.Phase {
	case UpLocked, UpVerifyNew:
		r.rollback(reasonLockMax, r.newStarted())
		return true
	case UpRelease:
		r.event("alarm", fmt.Sprintf("%s: %s, Spiegel nicht aufgeholt – Sperre aufgehoben", j.name(), reasonLockMax))
		r.follow()
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
// B auf Alt, X fehlerhaft, Meldung.
func (r *upRun) failPrep(reason string) {
	j := &r.j
	j.MarkFaulty = true
	r.finish(UpFailed, reason)
	if j.Kind == UpdateRolling {
		r.nodeOp(j.First, OpRollback, j.Old[j.First])
	}
	r.event("alarm", fmt.Sprintf("%s abgebrochen: %s; Version als fehlerhaft gemerkt", j.name(), reason))
}

// --- Hilfen ---

func (r *upRun) enter(p UpdatePhase) {
	r.j.Phase = p
	r.j.PhaseAt = r.o.Now
	r.j.WaitOp, r.j.WaitNode, r.j.WaitSeq, r.j.WaitOK = "", "", 0, false
}

func (r *upRun) enterCountdown() {
	r.enter(UpCountdown)
	r.j.CountdownUntil = r.o.Now.Add(r.o.Countdown)
}

// finish beendet den Auftrag; eine noch laufende Sperre endet mit ihm.
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
	r.j.WaitOp, r.j.WaitNode, r.j.WaitSeq, r.j.WaitOK = op, node, r.seq, false
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

func (r *upRun) timedOut() bool { return r.o.Now.Sub(r.j.PhaseAt) > r.o.Verify }

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

// --- Prüfung „alles grün“ (Spec 6) ---

// UpdateMinFree ist der Mindestplatz je Server.
const UpdateMinFree int64 = 2 << 30

// ReadyObs ist die Sicht für die Prüfung „alles grün“.
type ReadyObs struct {
	Mode        Mode
	SelfRole    Role
	PartnerSeen bool
	PartnerRole Role
	SameTerm    bool

	Maint, Busy, JobOpen bool

	SelfCommit, PartnerCommit string
	Resume                    bool // Fortsetzen eines Auftrags: Stand darf abweichen

	MirrorSync         bool
	LagBytes, LagLimit int64

	BackupConfigured, LastBaseOK, LastFilesOK bool

	FreeSelf, FreePartner int64

	WitnessExpected, WitnessSeen bool
}

// UpdateReady prüft „alles grün“. reason ist der erste Hinderungsgrund;
// hints enthält weitere Gründe und Hinweise, die nicht hindern (Zeuge).
// Einzelbetrieb: nur Platz und Grundsicherung.
func UpdateReady(o ReadyObs) (ok bool, reason string, hints []string) {
	var probs, notes []string
	add := func(bad bool, msg string) {
		if bad {
			probs = append(probs, msg)
		}
	}
	duo := o.Mode != Single
	if duo {
		add(!o.PartnerSeen, "Partner nicht erreichbar")
		if o.PartnerSeen {
			roles := (o.SelfRole == Active && o.PartnerRole == Passive) ||
				(o.SelfRole == Passive && o.PartnerRole == Active)
			add(!roles, fmt.Sprintf("Rollen nicht eindeutig (%s/%s)", o.SelfRole, o.PartnerRole))
			add(!o.SameTerm, "Amtszeit unterschiedlich")
		}
		add(!o.MirrorSync, "Spiegel nicht synchron")
		add(o.MirrorSync && o.LagLimit > 0 && o.LagBytes >= o.LagLimit, "Verzögerung der Spiegelung zu groß")
		add(o.Maint, "Wartung eingeschaltet")
		add(o.Busy, "Ein anderer Vorgang läuft")
		if !o.Resume {
			add(o.JobOpen, "Ein Auftrag läuft bereits")
			add(o.SelfCommit == "" || o.PartnerCommit == "", "Stand des Quelltexts unbekannt")
			add(o.SelfCommit != "" && o.PartnerCommit != "" && o.SelfCommit != o.PartnerCommit,
				"Server auf unterschiedlichem Stand")
		}
	}
	if o.BackupConfigured {
		add(!o.LastBaseOK, "Letzte Grundsicherung nicht erfolgreich")
		if duo {
			add(!o.LastFilesOK, "Letzter Dateiabgleich nicht erfolgreich")
		}
	}
	add(o.FreeSelf < UpdateMinFree, "Weniger als 2 GB frei auf diesem Server")
	if duo {
		add(o.FreePartner < UpdateMinFree, "Weniger als 2 GB frei auf dem Partner")
		if o.WitnessExpected && !o.WitnessSeen {
			notes = append(notes, "Zeuge nicht erreichbar")
		}
	}
	if len(probs) == 0 {
		return true, "", notes
	}
	hints = append(append(hints, probs[1:]...), notes...)
	return false, probs[0], hints
}

// --- Anzeige ---

// UpdateView ist der Auftrag für Anzeige und PDH (GET /v1/update).
type UpdateView struct {
	Active         bool      `json:"active"`
	Kind           string    `json:"kind"`
	Phase          string    `json:"phase"`
	Steps          []string  `json:"steps"`
	Step           int       `json:"step"`
	Percent        int       `json:"percent"`
	Text           string    `json:"text"`
	RemainingS     int       `json:"remaining_s"`
	Locked         bool      `json:"locked"`
	CountdownUntil time.Time `json:"countdown_until"`
	FromVersion    string    `json:"from_version"`
	ToVersion      string    `json:"to_version"`
	RollingBack    bool      `json:"rolling_back"`
	Result         string    `json:"result"`
	Faulty         []string  `json:"faulty"`
}

// View bereitet den Auftrag für die Anzeige auf. RemainingS ist die Restzeit
// bis zur Höchstdauer der Sperre (nur während der Sperre, nie negativ).
func (j UpdateJob) View(now time.Time, lockMax time.Duration, faulty []string) UpdateView {
	v := UpdateView{
		Active: !j.ended(), Kind: string(j.Kind), Phase: string(j.Phase),
		Locked: j.Locked(), CountdownUntil: j.CountdownUntil,
		FromVersion: j.OldVersion, ToVersion: j.TargetVersion,
		RollingBack: j.Phase == UpRollback, Result: j.Result,
		Faulty: append([]string{}, faulty...),
	}
	if j.Kind == "" {
		return v
	}
	swap := j.Kind == UpdateSwap
	switch {
	case swap:
		v.Steps = []string{"Abmelden", "Rollentausch", "Prüfen", "Freigabe"}
	case j.Kind == UpdateSequential:
		v.Steps = []string{"Abmelden", "Neustart", "Datenbank aktualisieren", "Prüfen", "Freigabe"}
	default:
		v.Steps = []string{"Abmelden", "Rollentausch", "Datenbank aktualisieren", "Prüfen", "Freigabe"}
	}
	v.Step, v.Percent, v.Text = j.viewStep()
	if swap && v.Step >= 2 {
		v.Step-- // ohne „Datenbank aktualisieren“
	}
	if v.Locked {
		rem := lockMax - now.Sub(j.LockedAt)
		if rem < 0 {
			rem = 0
		}
		v.RemainingS = int(rem / time.Second)
	}
	return v
}

// viewStep: Schritt (in der Fünferliste), Prozent und Text je Phase.
func (j UpdateJob) viewStep() (step, percent int, text string) {
	seq, swap := j.Kind == UpdateSequential, j.Kind == UpdateSwap
	what := "Update"
	if swap {
		what = "Rollentausch"
	}
	switch j.Phase {
	case UpPrepare:
		if seq {
			return 0, 0, "Neue Version wird im Hintergrund gebaut"
		}
		return 0, 0, "Neue Version wird auf dem Partner eingespielt"
	case UpVerifyPrep:
		if seq {
			return 0, 3, "Vorbereitung wird geprüft"
		}
		return 0, 3, "Partner wird geprüft"
	case UpCountdown:
		return 0, 5, "Abmeldung in Kürze – bitte Arbeit speichern"
	case UpLocked:
		if seq {
			return 1, 20, "Server startet mit der neuen Version neu"
		}
		return 1, 20, "Rollentausch läuft"
	case UpVerifyNew:
		if swap {
			return 3, 60, "Neuer aktiver Server wird geprüft"
		}
		return 3, 60, "Neue Version wird geprüft"
	case UpRelease:
		return 4, 90, "Freigabe – Spiegel holt auf"
	case UpFollow:
		return 4, 100, "Partner zieht die neue Version nach"
	case UpRollback:
		return 3, 60, what + " wird zurückgesetzt"
	case UpDone:
		return 4, 100, what + " abgeschlossen"
	case UpFailed:
		return 4, 100, what + " abgebrochen"
	}
	return 0, 0, ""
}
