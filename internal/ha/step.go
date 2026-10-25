package ha

import (
	"fmt"
	"time"
)

// voteRepeat: frühestens nach so vielen Takten wird eine Stimmbitte wiederholt.
const voteRepeat = 5

// handoverRepeat: Abstand der handover-Wiederholungen in Takten.
// handoverQuiet: so viele Takte vor Ablauf des Rollentauschs wird nicht mehr
// wiederholt – eine späte Zustellung ist dann vor dem Zurückfallen sichtbar.
const (
	handoverRepeat = 5
	handoverQuiet  = 5
)

const dataAssumedNote = " (Datenstand angenommen, keine Replikation)"

// Gründe für eine gesperrte Übernahme, in Prüfreihenfolge.
const (
	reasonMaint    = "Wartung"
	reasonMaintSeq = "Wartungsstand beim Partner noch nicht angekommen"
	reasonVersion  = "Versionen unterschiedlich"
	reasonCooldown = "Wartezeit nach Umschaltung"
	reasonData     = "Datenstand nicht aktuell"
	reasonMaxDay   = "Tageshöchstzahl erreicht"
)

// absent: Gegenstelle nicht gesehen und seit mindestens d still. nil zählt
// als weg – das gilt nur fürs Abschalten; Übernahmen verlangen einen Partner.
func absent(p *PeerObs, now, d time.Duration) bool {
	return p == nil || (!p.Seen && now-p.LastSeen >= d)
}

func seen(p *PeerObs) bool { return p != nil && p.Seen }

// mutual: gesehen und die Gegenstelle meldet, dass sie uns sieht. Nur dann
// darf ein abgeschalteter Knoten zurück – sonst könnte er auf einen veralteten
// Stand hin aktiv werden, während der Zeuge gerade eine neue Amtszeit vergibt.
func mutual(p *PeerObs) bool { return seen(p) && p.SeesSelf }

// Allowed sagt, ob eine automatische Übernahme gerade erlaubt wäre, und
// sonst warum nicht (Anzeige „Übernahme gesperrt: …“).
func Allowed(in Input) (ok bool, reason string) {
	p := in.Persist
	switch {
	case p.Maint:
		return false, reasonMaint
	case !maintAgreed(in.Partner, p):
		return false, reasonMaintSeq
	case p.PartnerVersion != in.Version:
		return false, reasonVersion
	case in.Wall.Sub(p.LastSwitch) < in.Settings.Cooldown:
		return false, reasonCooldown
	case !in.DataCurrent:
		return false, reasonData
	case switchesToday(p, in.Wall) >= in.Settings.MaxPerDay:
		return false, reasonMaxDay
	}
	return true, ""
}

// maintAgreed: der Partner hatte zuletzt denselben Wartungsstand wie wir.
// Ohne das könnte eine nur hier geänderte Wartung die Automatik freigeben,
// während der Partner (noch) in Wartung nicht abschaltet.
func maintAgreed(pa *PeerObs, p Persisted) bool { return pa != nil && pa.MaintSeq == p.MaintSeq }

func switchesToday(p Persisted, wall time.Time) int {
	if p.SwitchDay != wall.Format("2006-01-02") {
		return 0
	}
	return p.SwitchCount
}

type stepper struct {
	in  Input
	p   Persisted
	out Output
}

// Step berechnet aus einem Takt die nächste Rolle, Aktionen und Ereignisse.
func Step(in Input) Output {
	s := &stepper{in: in, p: in.Persist}
	s.out.HoldUntil = in.HoldUntil
	s.out.VotePending, s.out.VoteAskedAt = in.VotePending, in.VoteAskedAt
	s.out.HandoverPending, s.out.HandoverAt, s.out.HandoverSentAt = in.HandoverPending, in.HandoverAt, in.HandoverSentAt
	s.out.NoActiveSince, s.out.NoActiveAlarmed = in.NoActiveSince, in.NoActiveAlarmed
	s.out.VoteSince, s.out.VoteAlarmed = in.VoteSince, in.VoteAlarmed
	s.out.LastMutual = in.LastMutual
	if mutual(in.Partner) || in.Mode == Trio && mutual(in.Witness) {
		s.out.LastMutual = in.Now
	}
	if s.p.Role == "" {
		s.p.Role = Passive
	}
	s.decide()
	if s.p.Role != Fenced {
		s.out.HoldUntil = 0
	}
	if s.p.Role != Passive {
		s.clearHandover()
	}
	s.noActive()
	s.out.Persist = s.p
	return s.out
}

func (s *stepper) decide() {
	if s.in.Mode == Single {
		s.clearVote()
		s.become(Active, "")
		return
	}
	s.observe()
	switch s.in.Command {
	case CmdMaintOn, CmdMaintOff:
		s.setMaint(s.in.Command == CmdMaintOn)
	case CmdForceTakeover:
		if s.forceTakeover() {
			return
		}
	}
	if s.adoptHigherTerm() {
		return
	}
	if s.in.Command == CmdSwap && s.swap() {
		return
	}
	if s.in.Booting && s.boot() {
		return
	}
	switch s.p.Role {
	case Rejoin:
		s.become(Passive, "")
	case Active:
		s.active()
	case Fenced:
		s.fenced()
	case Passive:
		if s.receiveHandover() || s.handover() {
			return
		}
		if s.in.Mode == Trio {
			s.passiveTrio()
		} else {
			s.passiveDuo()
		}
	}
}

// observe übernimmt die Version des gesehenen Partners und eine neuere
// Wartungs-Einstellung (höheres MaintSeq) von Partner oder Zeuge.
func (s *stepper) observe() {
	if pa := s.in.Partner; seen(pa) {
		s.p.PartnerVersion = pa.Version
	}
	for _, o := range []*PeerObs{s.in.Partner, s.in.Witness} {
		if !seen(o) {
			continue
		}
		if o.MaintSeq > s.p.MaintSeq {
			s.p.Maint, s.p.MaintSeq = o.Maint, o.MaintSeq
		} else if o.MaintSeq == s.p.MaintSeq && o.Maint {
			s.p.Maint = true // gleicher Stand, verschieden: Wartung gewinnt
		}
	}
}

func (s *stepper) setMaint(on bool) {
	s.p.Maint = on
	s.p.MaintSeq++
}

// takeoverAllowed ist die einzige Stelle, die über eine automatische
// Übernahme entscheidet. Sperrt allein die Tageshöchstzahl, geht die
// Automatik in Wartung.
func (s *stepper) takeoverAllowed() bool {
	in := s.in
	in.Persist = s.p
	ok, reason := Allowed(in)
	if reason == reasonMaxDay {
		s.setMaint(true)
		s.event("alarm", "Zu viele Umschaltungen heute – Automatik aus")
	}
	return ok
}

// adoptHigherTerm übernimmt eine höhere Amtszeit von Partner oder Zeuge.
// true = Rollenwechsel nach wiedereingliederung, der Schritt endet.
func (s *stepper) adoptHigherTerm() bool {
	pa, w := s.in.Partner, s.in.Witness
	t := s.p.Term
	if pa != nil && pa.Term > t {
		t = pa.Term
	}
	if w != nil && w.Term > t {
		t = w.Term
	}
	// Hat der Zeuge die erbetene Amtszeit uns gegeben, die Bitte nicht
	// verfallen lassen – außer der Partner führt sie aktiv.
	if g := s.out.VotePending; g > s.p.Term && t == g && s.votedForSelf(g) &&
		!(pa != nil && pa.Role == Active && pa.Term >= g) {
		t = g - 1
	}
	if t <= s.p.Term {
		return false
	}
	s.p.Term = t
	if s.out.VotePending <= t {
		s.clearVote()
	}
	if s.p.Role == Active || s.p.Role == Fenced {
		s.act(ActRejoin, t)
		s.become(Rejoin, "")
		return true
	}
	return false
}

// forceTakeover: Handbefehl, nur wenn nicht aktiv; ohne Stimme und Bremsen.
func (s *stepper) forceTakeover() bool {
	if s.p.Role == Active {
		return false
	}
	t := s.p.Term
	for _, o := range []*PeerObs{s.in.Partner, s.in.Witness} {
		if o != nil && o.Term > t {
			t = o.Term
		}
	}
	s.promote(t+1, false)
	s.p.LastSwitch = s.in.Wall // Wartezeit gilt, Tageszähler nicht
	s.event("alarm", "Übernahme erzwungen")
	return true
}

// swap: Handbefehl Rollentausch, nur aktiv mit gesehenem passivem Partner.
func (s *stepper) swap() bool {
	pa := s.in.Partner
	if s.p.Role != Active || !seen(pa) || pa.Role != Passive {
		return false
	}
	next := s.p.Term + 1
	s.act(ActDemote, s.p.Term)
	s.act(ActVIPRelease, s.p.Term)
	s.act(ActHandover, next)
	s.out.HandoverPending, s.out.HandoverAt, s.out.HandoverSentAt = next, s.in.Now, s.in.Now
	s.become(Passive, "")
	return true
}

// receiveHandover: der Partner übergibt die nächste Amtszeit – zählt nicht
// zur Tageshöchstzahl und gilt auch in Wartung.
func (s *stepper) receiveHandover() bool {
	h, pa := s.in.Handover, s.in.Partner
	if h == 0 || h != s.p.Term+1 || s.out.HandoverPending != 0 ||
		!seen(pa) || pa.Role != Passive || pa.Term != h-1 {
		return false
	}
	s.promote(h, false)
	return true
}

// handover begleitet den eigenen Rollentausch: wiederholen, beenden oder
// nach der Übernahmezeit zurück nach aktiv. true = Schritt endet.
func (s *stepper) handover() bool {
	hp := s.out.HandoverPending
	if hp == 0 {
		return false
	}
	in, pa := s.in, s.in.Partner
	if s.p.Term >= hp || seen(pa) && pa.Term >= hp {
		s.clearHandover()
		return false
	}
	took := in.Settings.Takeover(in.Mode)
	since := in.Now - s.out.HandoverAt
	// Partner aus den Augen verloren: ob er übernommen hat, ist unklar, und
	// er könnte inzwischen um eine Stimme bitten. Die übergebene Amtszeit gilt
	// als vergeben, eine spätere Stimmbitte fragt nach der nächsten.
	if !mutual(pa) || (since >= took && pa.Role != Passive) {
		s.clearHandover()
		s.p.Term = hp
		s.event("alarm", "Rollentausch fehlgeschlagen")
		return false
	}
	// Partner die ganze Zeit gesehen und passiv geblieben: zurück nach aktiv.
	if since >= took {
		s.clearHandover()
		s.event("alarm", "Rollentausch fehlgeschlagen")
		s.unfence()
		return true
	}
	if pa.Role == Passive &&
		in.Now-s.out.HandoverSentAt >= handoverRepeat*in.Settings.Tick &&
		since < took-handoverQuiet*in.Settings.Tick {
		s.out.HandoverSentAt = in.Now
		s.act(ActHandover, hp)
	}
	return true
}

func (s *stepper) clearHandover() {
	s.out.HandoverPending, s.out.HandoverAt, s.out.HandoverSentAt = 0, 0, 0
}

// boot: Start nach Neustart (Spec 5.4). Ein bisher aktiver Knoten wartet
// abgeschaltet, bis er den Partner sieht oder die Übernahmezeit um ist.
func (s *stepper) boot() bool {
	if s.p.Role != Active && s.p.Role != Fenced {
		return false
	}
	if mutual(s.in.Partner) {
		s.unfence()
		return true
	}
	s.act(ActFence, s.p.Term)
	s.out.HoldUntil = s.in.Now + s.in.Settings.Takeover(s.in.Mode)
	s.become(Fenced, "")
	return true
}

func (s *stepper) active() {
	in := s.in
	if pa := in.Partner; seen(pa) && pa.Role == Active && pa.Term == s.p.Term {
		if !s.p.Maint {
			s.setMaint(true)
			s.event("alarm", "Zwei aktive Server mit gleicher Amtszeit – Automatik aus")
		}
		return
	}
	// Wartung hält nur, wenn der Partner sie mit demselben Stand kennt.
	if s.p.Maint && maintAgreed(in.Partner, s.p) {
		return
	}
	// Aktiv bleibt nur, wer gegenseitige Sicht hat – bloßes Empfangen reicht
	// nicht, die Gegenstelle könnte uns für weg halten.
	lost := in.Now-s.out.LastMutual >= in.Settings.FenceAfter
	if in.Mode == Duo {
		lost = lost && in.Now-in.GatewayLastOK >= in.Settings.FenceAfter
	}
	if lost {
		s.act(ActFence, s.p.Term)
		s.act(ActVIPRelease, s.p.Term)
		s.become(Fenced, "")
	}
}

func (s *stepper) fenced() {
	in := s.in
	if h := s.out.HoldUntil; h > 0 {
		// Nach Neustart: vor Ablauf nur der Partner (Amtszeit ≤ eigener,
		// höhere ist schon übernommen).
		if in.Now < h {
			if mutual(in.Partner) {
				s.unfence()
			}
			return
		}
		s.out.HoldUntil = 0
	}
	back := mutual(in.Partner)
	switch in.Mode {
	case Trio:
		back = back || mutual(in.Witness)
	case Duo:
		back = back || in.Gateway
	}
	if back {
		s.unfence()
	}
}

func (s *stepper) unfence() {
	s.act(ActUnfence, s.p.Term)
	s.act(ActVIPTake, s.p.Term)
	s.become(Active, "")
}

func (s *stepper) passiveTrio() {
	in := s.in
	if g := s.out.VotePending; g > s.p.Term && in.VoteGranted == g {
		// Mit Stimme übernehmen: Partner gesehen und weder aktiv noch
		// abgeschaltet (der kann gerade zurückkehren), oder für die volle
		// Übernahmezeit weg. Nur solange der Zeuge uns sieht – sein Schutz
		// der Stimme reicht nur bis Übernahmezeit nach dem letzten Kontakt.
		pa := in.Partner
		gone := pa != nil && (seen(pa) && pa.Role != Active && pa.Role != Fenced ||
			absent(pa, in.Now, in.Settings.TakeoverTrio))
		if gone && mutual(in.Witness) && s.takeoverAllowed() {
			s.promote(g, true)
		}
		return
	}
	if in.Partner == nil || !absent(in.Partner, in.Now, in.Settings.TakeoverTrio) {
		s.out.VoteSince, s.out.VoteAlarmed = 0, false
		return
	}
	if !s.takeoverAllowed() {
		return
	}
	if s.out.VoteSince == 0 {
		s.out.VoteSince = in.Now
	}
	if !s.out.VoteAlarmed && in.Now-s.out.VoteSince >= 2*in.Settings.TakeoverTrio {
		s.out.VoteAlarmed = true
		s.event("alarm", "Übernahme verweigert – Zeuge sieht den anderen Server noch; prüfen und ggf. Übernahme erzwingen")
	}
	if s.out.VotePending <= s.p.Term {
		s.out.VotePending = s.p.Term + 1
	} else if in.Now-s.out.VoteAskedAt < voteRepeat*in.Settings.Tick {
		return
	}
	s.out.VoteAskedAt = in.Now
	s.act(ActRequestVote, s.out.VotePending)
}

func (s *stepper) passiveDuo() {
	in := s.in
	// Partner unbekannt (nil) → keine Übernahme; nil zählt nur beim Abschalten als weg.
	if in.Partner != nil && absent(in.Partner, in.Now, in.Settings.TakeoverDuo) &&
		in.Gateway && s.takeoverAllowed() {
		s.promote(s.p.Term+1, true)
	}
}

// promote macht diesen Knoten mit Amtszeit term aktiv; counted = automatische
// Übernahme (Wartezeit und Tageszähler).
func (s *stepper) promote(term uint64, counted bool) {
	s.p.Term = term
	s.clearVote()
	if counted {
		s.p.LastSwitch = s.in.Wall
		if day := s.in.Wall.Format("2006-01-02"); s.p.SwitchDay != day {
			s.p.SwitchDay, s.p.SwitchCount = day, 0
		}
		s.p.SwitchCount++
	}
	s.act(ActPromote, term)
	s.act(ActVIPTake, term)
	note := ""
	if s.in.DataAssumed {
		note = dataAssumedNote
	}
	s.become(Active, note)
}

// noActive: zwei passive Knoten sehen sich, keiner aktiv, kein Rollentausch –
// nach der Übernahmezeit einmal je Phase ein Alarm, keine automatische Wahl.
func (s *stepper) noActive() {
	in, pa := s.in, s.in.Partner
	if in.Mode == Single || s.p.Role != Passive || !seen(pa) || pa.Role != Passive ||
		s.out.HandoverPending != 0 {
		s.out.NoActiveSince, s.out.NoActiveAlarmed = 0, false
		return
	}
	if s.out.NoActiveSince == 0 {
		s.out.NoActiveSince = in.Now
	}
	if !s.out.NoActiveAlarmed && in.Now-s.out.NoActiveSince >= in.Settings.Takeover(in.Mode) {
		s.out.NoActiveAlarmed = true
		s.event("alarm", "Kein aktiver Server – bitte Übernahme erzwingen")
	}
}

// votedForSelf: der Zeuge meldet Amtszeit g mit Stimme für diesen Knoten.
func (s *stepper) votedForSelf(g uint64) bool {
	w := s.in.Witness
	return w != nil && s.in.SelfID != "" && w.Term == g && w.VotedFor == s.in.SelfID
}

func (s *stepper) clearVote() {
	s.out.VotePending, s.out.VoteAskedAt = 0, 0
	s.out.VoteSince, s.out.VoteAlarmed = 0, false
}

func (s *stepper) act(k ActionKind, term uint64) {
	s.out.Actions = append(s.out.Actions, Action{Kind: k, Term: term})
}

func (s *stepper) event(kind, text string) {
	s.out.Events = append(s.out.Events, Event{Kind: kind, Text: text})
}

// become wechselt die Rolle und protokolliert den Wechsel.
func (s *stepper) become(r Role, note string) {
	if s.p.Role == r {
		return
	}
	s.event("role", fmt.Sprintf("Rolle: %s → %s (Amtszeit %d)%s", s.p.Role, r, s.p.Term, note))
	s.p.Role = r
}
