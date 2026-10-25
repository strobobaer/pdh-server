package ha

import (
	"fmt"
	"time"
)

// voteRepeat: frühestens nach so vielen Takten wird eine Stimmbitte wiederholt.
const voteRepeat = 5

const dataAssumedNote = " (Datenstand angenommen, keine Replikation)"

// absent: Gegenstelle nicht gesehen und seit mindestens d still. nil zählt
// als weg – das gilt nur fürs Abschalten; Übernahmen verlangen einen Partner.
func absent(p *PeerObs, now, d time.Duration) bool {
	return p == nil || (!p.Seen && now-p.LastSeen >= d)
}

func seen(p *PeerObs) bool { return p != nil && p.Seen }

// takeoverAllowed ist die einzige Stelle, die über eine automatische
// Übernahme entscheidet. Die Bremsen (Wartung, Version, Wartezeit,
// Datenstand, Tageshöchstzahl) kommen hier hinzu.
func takeoverAllowed(in Input) bool { return true }

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
	if s.p.Role == "" {
		s.p.Role = Passive
	}
	s.decide()
	s.out.Persist = s.p
	return s.out
}

func (s *stepper) decide() {
	if s.in.Mode == Single {
		s.clearVote()
		s.become(Active, "")
		return
	}
	if s.adoptHigherTerm() {
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
		if s.in.Mode == Trio {
			s.passiveTrio()
		} else {
			s.passiveDuo()
		}
	}
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

func (s *stepper) active() {
	in := s.in
	if pa := in.Partner; seen(pa) && pa.Role == Active && pa.Term == s.p.Term {
		if !s.p.Maint {
			s.p.Maint = true
			s.p.MaintSeq++
			s.event("alarm", "Zwei aktive Server mit gleicher Amtszeit – Automatik aus")
		}
		return
	}
	lost := false
	switch in.Mode {
	case Trio:
		lost = absent(in.Partner, in.Now, in.Settings.FenceAfter) &&
			absent(in.Witness, in.Now, in.Settings.FenceAfter)
	case Duo:
		lost = absent(in.Partner, in.Now, in.Settings.FenceAfter) &&
			in.Now-in.GatewayLastOK >= in.Settings.FenceAfter
	}
	if lost {
		s.act(ActFence, s.p.Term)
		s.act(ActVIPRelease, s.p.Term)
		s.become(Fenced, "")
	}
}

func (s *stepper) fenced() {
	in := s.in
	back := seen(in.Partner)
	switch in.Mode {
	case Trio:
		back = back || seen(in.Witness)
	case Duo:
		back = back || in.Gateway
	}
	if back {
		s.act(ActUnfence, s.p.Term)
		s.act(ActVIPTake, s.p.Term)
		s.become(Active, "")
	}
}

func (s *stepper) passiveTrio() {
	in := s.in
	if g := s.out.VotePending; g > s.p.Term && in.VoteGranted == g {
		// Mit Stimme übernehmen: Partner gesehen und nicht aktiv, oder
		// für die volle Übernahmezeit weg.
		pa := in.Partner
		gone := pa != nil && (seen(pa) && pa.Role != Active ||
			absent(pa, in.Now, in.Settings.TakeoverTrio))
		if gone && takeoverAllowed(in) {
			s.takeOver(g)
		}
		return
	}
	if in.Partner == nil || !absent(in.Partner, in.Now, in.Settings.TakeoverTrio) || !takeoverAllowed(in) {
		return
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
		in.Gateway && takeoverAllowed(in) {
		s.takeOver(s.p.Term + 1)
	}
}

func (s *stepper) takeOver(term uint64) {
	s.p.Term = term
	s.clearVote()
	s.p.LastSwitch = s.in.Wall
	if day := s.in.Wall.Format("2006-01-02"); s.p.SwitchDay != day {
		s.p.SwitchDay, s.p.SwitchCount = day, 0
	}
	s.p.SwitchCount++
	s.act(ActPromote, term)
	s.act(ActVIPTake, term)
	note := ""
	if s.in.DataAssumed {
		note = dataAssumedNote
	}
	s.become(Active, note)
}

// votedForSelf: der Zeuge meldet Amtszeit g mit Stimme für diesen Knoten.
func (s *stepper) votedForSelf(g uint64) bool {
	w := s.in.Witness
	return w != nil && s.in.SelfID != "" && w.Term == g && w.VotedFor == s.in.SelfID
}

func (s *stepper) clearVote() { s.out.VotePending, s.out.VoteAskedAt = 0, 0 }

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
