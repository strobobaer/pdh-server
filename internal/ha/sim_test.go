package ha

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// Simulation: zwei Step-Knoten (a, b) und im Trio ein Vote-Zeuge (w).
// Nachrichten werden je Takt zugestellt (Verzug 0 oder 1 Takt, Reihenfolge
// je Richtung bleibt wie bei TCP erhalten); Verbindungen fallen beidseitig aus.

const simVersion = "0.75.0"

var simWall = time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)

type simHB struct {
	role     Role
	term     uint64
	maint    bool
	maintSeq uint64
	votedFor string
	sees     map[string]bool
}

type simMsg struct {
	at       int // Zustelltakt
	from, to string
	hb       simHB
	handover uint64
	vote     uint64 // Stimmbitte an den Zeugen
	grant    uint64 // Zusage an den Knoten
	probe    bool   // nur Probe (Beobachtung): nichts speichern
}

type simSeen struct {
	last time.Duration // Empfang in der Uhr des Empfängers
	hb   simHB
}

type simNode struct {
	id, peer     string
	up           bool
	start        int // Starttakt, Grundlage der eigenen monotonen Uhr
	booting      bool
	gateway      bool
	gwLastOK     time.Duration
	p            Persisted
	carry        Output
	view         map[string]*simSeen
	granted      uint64
	demoteDone   uint64 // Amtszeit der zuletzt erledigten Abgabe
	probeGranted uint64
	stale        bool // Datenstand nicht aktuell (DataCurrent false)
	handover     uint64
	cmd          CommandKind
}

func (n *simNode) clock(tick int) time.Duration { return time.Duration(tick-n.start+1) * time.Second }

type simWitness struct {
	p      Persisted
	view   map[string]*simSeen
	votes  []VoteRequest
	probes []VoteRequest
}

type sim struct {
	t            testing.TB
	mode         Mode
	set          Settings
	tick         int
	nodes        []*simNode
	w            *simWitness
	down         map[string]int // "a>b" → Richtung getrennt bis Takt (ausschließlich)
	rng          *rand.Rand     // nil: kein Verzug
	queue        []simMsg
	lastAt       map[string]int
	dropHandover bool
	owner        map[uint64]string
	events       []string
	trace        []string
}

func newSim(t testing.TB, mode Mode, rng *rand.Rand) *sim {
	s := &sim{
		t: t, mode: mode, set: realSettings(), rng: rng,
		down: map[string]int{}, lastAt: map[string]int{}, owner: map[uint64]string{},
	}
	for _, id := range []string{"a", "b"} {
		n := &simNode{id: id, peer: "b", up: true, start: 1, gateway: true, view: map[string]*simSeen{},
			p: Persisted{Term: 1, Role: Passive, PartnerVersion: simVersion}}
		if id == "b" {
			n.peer = "a"
		} else {
			n.p.Role = Active
		}
		s.nodes = append(s.nodes, n)
	}
	if mode == Trio {
		s.w = &simWitness{p: Persisted{Role: Witness, Term: 1}, view: map[string]*simSeen{}}
	}
	return s
}

// linkUp: Richtung from → to verbunden.
func (s *sim) linkUp(from, to string) bool { return s.down[from+">"+to] <= s.tick }

// cutOne trennt nur die Richtung from → to für n Takte (n < 0: dauerhaft).
func (s *sim) cutOne(from, to string, n int) {
	if n < 0 {
		n = 1 << 30
	}
	s.down[from+">"+to] = s.tick + 1 + n
}

// cut trennt die Verbindung in beide Richtungen.
func (s *sim) cut(a, b string, n int) {
	s.cutOne(a, b, n)
	s.cutOne(b, a, n)
}

func (s *sim) heal(a, b string) {
	delete(s.down, a+">"+b)
	delete(s.down, b+">"+a)
}

func (s *sim) node(id string) *simNode {
	for _, n := range s.nodes {
		if n.id == id {
			return n
		}
	}
	s.t.Fatalf("unbekannter Knoten %s", id)
	return nil
}

func (s *sim) crash(id string) { s.node(id).up = false }

func (s *sim) restart(id string) {
	n := s.node(id)
	*n = simNode{id: n.id, peer: n.peer, up: true, start: s.tick + 1, booting: true, gateway: n.gateway,
		p: n.p, view: map[string]*simSeen{}}
}

func (s *sim) send(m simMsg) {
	if !s.linkUp(m.from, m.to) {
		return
	}
	m.at = s.tick + 1
	if s.rng != nil && s.rng.Intn(2) == 1 {
		m.at++
	}
	dir := m.from + ">" + m.to
	if m.at < s.lastAt[dir] {
		m.at = s.lastAt[dir]
	}
	s.lastAt[dir] = m.at
	s.queue = append(s.queue, m)
}

func (s *sim) run(n int) {
	for i := 0; i < n; i++ {
		s.step()
	}
}

func (s *sim) step() {
	s.tick++
	k := s.tick
	var rest []simMsg
	for _, m := range s.queue {
		if m.at > k {
			rest = append(rest, m)
			continue
		}
		s.deliver(m)
	}
	s.queue = rest
	if s.w != nil {
		s.witnessStep()
	}
	for _, n := range s.nodes {
		if n.up {
			s.nodeStep(n)
		}
	}
	s.record()
	s.check()
}

func (s *sim) deliver(m simMsg) {
	if m.to == "w" {
		s.w.view[m.from] = &simSeen{last: time.Duration(s.tick) * time.Second, hb: m.hb}
		if m.vote > 0 && m.probe {
			s.w.probes = append(s.w.probes, VoteRequest{From: m.from, Term: m.vote})
		} else if m.vote > 0 {
			s.w.votes = append(s.w.votes, VoteRequest{From: m.from, Term: m.vote})
		}
		return
	}
	n := s.node(m.to)
	if !n.up {
		return
	}
	n.view[m.from] = &simSeen{last: n.clock(s.tick), hb: m.hb}
	if m.handover > 0 {
		n.handover = m.handover
	}
	if m.grant > n.granted && !m.probe {
		n.granted = m.grant
	}
	if m.grant > n.probeGranted && m.probe {
		n.probeGranted = m.grant
	}
}

func (s *sim) obs(view map[string]*simSeen, now time.Duration, self, id, kind string) *PeerObs {
	p := &PeerObs{ID: id, Kind: kind}
	v := view[id]
	if v == nil {
		return p
	}
	p.Seen = now-v.last < 3*s.set.Tick
	p.LastSeen = v.last
	p.Role, p.Term = v.hb.role, v.hb.term
	p.Version, p.Gateway, p.DataCurrent = simVersion, true, true
	p.Maint, p.MaintSeq = v.hb.maint, v.hb.maintSeq
	p.VotedFor = v.hb.votedFor
	p.SeesSelf = v.hb.sees[self]
	return p
}

func (s *sim) witnessStep() {
	w := s.w
	now := time.Duration(s.tick) * time.Second
	var nodes []PeerObs
	for _, n := range s.nodes {
		o := s.obs(w.view, now, "w", n.id, "node")
		nodes = append(nodes, *o)
		w.p.Term = max(w.p.Term, o.Term)
	}
	for _, req := range w.votes {
		ok, next := Vote(w.p, req, nodes, now, s.set)
		w.p = next
		if ok {
			s.logf("w: Stimme %d für %s", req.Term, req.From)
			s.send(simMsg{from: "w", to: req.From, hb: s.witnessHB(nodes), grant: req.Term})
		}
	}
	w.votes = nil
	for _, req := range w.probes {
		if ok, _ := Vote(w.p, req, nodes, now, s.set); ok {
			s.send(simMsg{from: "w", to: req.From, hb: s.witnessHB(nodes), grant: req.Term, probe: true})
		}
	}
	w.probes = nil
	w.p.Term = max(w.p.Term, w.p.VotedTerm)
	hb := s.witnessHB(nodes)
	for _, n := range s.nodes {
		s.send(simMsg{from: "w", to: n.id, hb: hb})
	}
}

func (s *sim) witnessHB(nodes []PeerObs) simHB {
	hb := simHB{role: Witness, term: s.w.p.Term, votedFor: s.w.p.VotedFor, sees: map[string]bool{}}
	for _, n := range nodes {
		hb.sees[n.ID] = n.Seen
	}
	return hb
}

func (s *sim) nodeStep(n *simNode) {
	now := n.clock(s.tick)
	if n.gateway {
		n.gwLastOK = now
	}
	in := Input{
		SelfID: n.id, Now: now, Wall: simWall.Add(time.Duration(s.tick) * time.Second),
		Mode: s.mode, Version: simVersion,
		Gateway: n.gateway, GatewayLastOK: n.gwLastOK,
		DataCurrent: !n.stale, DataAssumed: true, Booting: n.booting,
		Partner:     s.obs(n.view, now, n.id, n.peer, "node"),
		VoteGranted: n.granted, Handover: n.handover, Command: n.cmd,
		Settings: s.set, Persist: n.p,
		HoldUntil: n.carry.HoldUntil, VotePending: n.carry.VotePending, VoteAskedAt: n.carry.VoteAskedAt,
		HandoverPending: n.carry.HandoverPending, HandoverAt: n.carry.HandoverAt,
		HandoverSentAt: n.carry.HandoverSentAt,
		NoActiveSince:  n.carry.NoActiveSince, NoActiveAlarmed: n.carry.NoActiveAlarmed,
		VoteSince: n.carry.VoteSince, VoteAlarmed: n.carry.VoteAlarmed, LastMutual: n.carry.LastMutual,
		HandoverReadyAt: n.carry.HandoverReadyAt, Would: n.carry.Would, DemoteDone: n.demoteDone,
		ProbeGranted: n.probeGranted,
	}
	if s.mode == Trio {
		in.Witness = s.obs(n.view, now, n.id, "w", "witness")
	}
	out := Step(in)
	n.p, n.carry = out.Persist, out
	n.booting, n.handover, n.cmd = false, 0, CmdNone
	for _, e := range out.Events {
		s.events = append(s.events, fmt.Sprintf("%d %s %s: %s", s.tick, n.id, e.Kind, e.Text))
		s.logf("%s %s: %s", n.id, e.Kind, e.Text)
	}
	hb := simHB{role: n.p.Role, term: n.p.Term, maint: n.p.Maint, maintSeq: n.p.MaintSeq, sees: map[string]bool{
		n.peer: in.Partner.Seen,
	}}
	if in.Witness != nil {
		hb.sees["w"] = in.Witness.Seen
	}
	for _, a := range out.Actions {
		s.logf("%s Aktion %s{%d}", n.id, a.Kind, a.Term)
		if a.Kind == ActPromote && !a.Manual && n.stale {
			s.fail("%s ohne aktuellen Datenstand automatisch hochgestuft (Amtszeit %d)", n.id, a.Term)
		}
		switch a.Kind {
		case ActRequestVote:
			if s.w != nil {
				s.send(simMsg{from: n.id, to: "w", hb: hb, vote: a.Term})
			}
		case ActProbeVote:
			if s.w != nil {
				s.send(simMsg{from: n.id, to: "w", hb: hb, vote: a.Term, probe: true})
			}
		case ActDemote:
			// Abgabe der Datenbank gilt im nächsten Takt als erledigt
			n.demoteDone = a.Term
		case ActHandover:
			if !s.dropHandover && !(s.rng != nil && s.rng.Intn(4) == 0) {
				s.send(simMsg{from: n.id, to: n.peer, hb: hb, handover: a.Term})
			}
		}
	}
	s.send(simMsg{from: n.id, to: n.peer, hb: hb})
	if s.w != nil {
		s.send(simMsg{from: n.id, to: "w", hb: hb})
	}
}

func (s *sim) logf(format string, a ...any) {
	s.trace = append(s.trace, fmt.Sprintf("  %4d ", s.tick)+fmt.Sprintf(format, a...))
}

func (s *sim) record() {
	var b strings.Builder
	for _, n := range s.nodes {
		st := "weg"
		if n.up {
			st = fmt.Sprintf("%s/%d", n.p.Role, n.p.Term)
		}
		fmt.Fprintf(&b, "%s=%s ", n.id, st)
	}
	if s.w != nil {
		fmt.Fprintf(&b, "w=%d/%s ", s.w.p.Term, s.w.p.VotedFor)
	}
	for _, l := range [][2]string{{"a", "b"}, {"a", "w"}, {"b", "w"}} {
		if s.w == nil && l[1] == "w" {
			continue
		}
		switch ab, ba := s.linkUp(l[0], l[1]), s.linkUp(l[1], l[0]); {
		case !ab && !ba:
			fmt.Fprintf(&b, "%s-%s:getrennt ", l[0], l[1])
		case !ab:
			fmt.Fprintf(&b, "%s>%s:getrennt ", l[0], l[1])
		case !ba:
			fmt.Fprintf(&b, "%s>%s:getrennt ", l[1], l[0])
		}
	}
	s.trace = append(s.trace, fmt.Sprintf("%4d %s", s.tick, b.String()))
	if len(s.trace) > 400 {
		s.trace = s.trace[len(s.trace)-200:]
	}
}

// check prüft die Invarianten nach jedem Takt.
func (s *sim) check() {
	var active []string
	for _, n := range s.nodes {
		if !n.up || n.p.Role != Active {
			continue
		}
		active = append(active, n.id)
		if o, ok := s.owner[n.p.Term]; ok && o != n.id {
			s.fail("Amtszeit %d zweimal aktiv geworden (%s, dann %s)", n.p.Term, o, n.id)
		}
		s.owner[n.p.Term] = n.id
	}
	if len(active) > 1 {
		s.fail("zwei aktive Server: %v", active)
	}
}

func (s *sim) fail(format string, a ...any) {
	s.t.Helper()
	tr := s.trace
	if len(tr) > 120 {
		tr = tr[len(tr)-120:]
	}
	s.t.Fatalf("Takt %d: %s\n%s", s.tick, fmt.Sprintf(format, a...), strings.Join(tr, "\n"))
}

func (s *sim) want(id string, role Role, term uint64) {
	s.t.Helper()
	n := s.node(id)
	if n.p.Role != role || n.p.Term != term {
		s.fail("%s = %s/%d, erwartet %s/%d", id, n.p.Role, n.p.Term, role, term)
	}
}

func (s *sim) wantEvent(text string) {
	s.t.Helper()
	for _, e := range s.events {
		if strings.Contains(e, text) {
			return
		}
	}
	s.fail("Ereignis %q fehlt: %v", text, s.events)
}

// simSeeds: ohne Verzug und mit einigen festen Verzugsfolgen.
var simSeeds = []int64{0, 1, 2, 3, 4, 5, 6, 7}

func forSeeds(t *testing.T, mode Mode, f func(s *sim)) {
	for _, seed := range simSeeds {
		t.Run(fmt.Sprintf("verzug-%d", seed), func(t *testing.T) {
			var rng *rand.Rand
			if seed != 0 {
				rng = rand.New(rand.NewSource(seed))
			}
			f(newSim(t, mode, rng))
		})
	}
}

func TestSimTrio(t *testing.T) {
	scenarios := []struct {
		name string
		run  func(s *sim)
	}{
		{"aktiv weg und rückkehr", func(s *sim) {
			s.run(5)
			s.crash("a")
			s.run(45)
			s.want("b", Active, 2)
			s.restart("a")
			s.run(40)
			s.want("a", Passive, 2)
			s.want("b", Active, 2)
		}},
		{"direktleitung weg", func(s *sim) {
			s.run(5)
			s.cut("a", "b", -1)
			s.run(120)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
			s.heal("a", "b")
			s.run(10)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
		}},
		{"zeuge weg", func(s *sim) {
			s.run(5)
			s.cut("a", "w", -1)
			s.cut("b", "w", -1)
			s.run(120)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
		}},
		{"aktiver isoliert und rückkehr", func(s *sim) {
			s.run(5)
			s.cut("a", "b", -1)
			s.cut("a", "w", -1)
			s.run(15)
			s.want("a", Fenced, 1)
			s.run(35)
			s.want("b", Active, 2)
			s.heal("a", "b")
			s.heal("a", "w")
			s.run(10)
			s.want("a", Passive, 2)
			s.want("b", Active, 2)
		}},
		{"passiver isoliert", func(s *sim) {
			s.run(5)
			s.cut("a", "b", -1)
			s.cut("b", "w", -1)
			s.run(120)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
			s.heal("a", "b")
			s.heal("b", "w")
			s.run(10)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
		}},
		{"neustart des aktiven ohne ausfall", func(s *sim) {
			s.run(5)
			s.crash("a")
			s.run(3)
			s.restart("a")
			s.run(10)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
		}},
		{"einseitig: zeuge sieht den abgeschalteten, der ihn nicht", func(s *sim) {
			s.run(5)
			s.cut("a", "b", -1)
			s.cutOne("w", "a", -1)
			s.run(150)
			s.want("a", Fenced, 1)
			s.want("b", Passive, 1)
			s.wantEvent("Übernahme verweigert")
		}},
		{"einseitig: aktiver hört partner, partner ihn nicht", func(s *sim) {
			s.run(5)
			s.cutOne("a", "b", -1)
			s.cut("a", "w", -1)
			s.run(60)
			s.want("b", Active, 2)
			s.wantEvent("a role: Rolle: aktiv → abgeschaltet")
		}},
		{"wartung, aktiver isoliert, wartung aus beim passiven", func(s *sim) {
			s.run(3)
			s.node("a").cmd = CmdMaintOn
			s.run(5)
			s.cut("a", "b", -1)
			s.cut("a", "w", -1)
			s.run(5)
			s.node("b").cmd = CmdMaintOff
			s.run(100)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
		}},
		{"rollentausch", func(s *sim) {
			s.run(5)
			s.node("a").cmd = CmdSwap
			s.run(15) // ein Takt mehr: Übergabe erst nach der Abgabe
			s.want("a", Passive, 2)
			s.want("b", Active, 2)
		}},
		{"rollentausch ohne zustellung", func(s *sim) {
			s.run(5)
			s.dropHandover = true
			s.node("a").cmd = CmdSwap
			s.run(40)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
			s.wantEvent("Rollentausch fehlgeschlagen")
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) { forSeeds(t, Trio, sc.run) })
	}
}

func TestSimDuo(t *testing.T) {
	t.Run("aktiv weg und rückkehr", func(t *testing.T) {
		forSeeds(t, Duo, func(s *sim) {
			s.run(5)
			s.crash("a")
			s.run(70)
			s.want("b", Active, 2)
			s.restart("a")
			s.run(10)
			s.want("a", Passive, 2)
			s.want("b", Active, 2)
		})
	})
	t.Run("gateway-ausfall beim aktiven", func(t *testing.T) {
		forSeeds(t, Duo, func(s *sim) {
			s.run(5)
			s.node("a").gateway = false
			s.run(120)
			s.want("a", Active, 1)
			s.want("b", Passive, 1)
		})
	})
	t.Run("aktiver isoliert", func(t *testing.T) {
		forSeeds(t, Duo, func(s *sim) {
			s.run(5)
			s.node("a").gateway = false
			s.cut("a", "b", -1)
			s.run(15)
			s.want("a", Fenced, 1)
			s.run(60)
			s.want("b", Active, 2)
			s.heal("a", "b")
			s.run(10)
			s.want("a", Passive, 2)
		})
	})
}

// simRandom: zufällige Ausfälle einzelner Verbindungen (1–120 Takte),
// seltene Neustarts und Rollentausche (handover geht mitunter verloren);
// Verzug ≤ 1 Takt. Liefert die Zahl der Rollenwechsel nach aktiv.
func simRandom(t testing.TB, seed int64, ticks int) int {
	return simRandomOpts(t, seed, ticks, false)
}

// simRandomOpts: oneWay = Verbindungen fallen auch nur in eine Richtung aus,
// dazu zufällige Wartungsbefehle.
func simRandomOpts(t testing.TB, seed int64, ticks int, oneWay bool) int {
	rng := rand.New(rand.NewSource(seed))
	s := newSim(t, Trio, rng)
	s.set.Cooldown, s.set.MaxPerDay = 0, 20
	links := [][2]string{{"a", "b"}, {"a", "w"}, {"b", "w"}}
	cutRate := 10 + rng.Intn(200)
	restartAt := map[string]int{}
	for i := 0; i < ticks; i++ {
		for _, l := range links {
			if !oneWay {
				if s.linkUp(l[0], l[1]) && rng.Intn(cutRate) == 0 {
					s.cut(l[0], l[1], 1+rng.Intn(120))
				}
				continue
			}
			if s.linkUp(l[0], l[1]) && s.linkUp(l[1], l[0]) && rng.Intn(cutRate) == 0 {
				switch d := 1 + rng.Intn(120); rng.Intn(3) {
				case 0:
					s.cut(l[0], l[1], d)
				case 1:
					s.cutOne(l[0], l[1], d)
				default:
					s.cutOne(l[1], l[0], d)
				}
			}
		}
		for _, n := range s.nodes {
			switch {
			case !n.up && restartAt[n.id] <= s.tick:
				s.restart(n.id)
			case n.up && rng.Intn(1500) == 0:
				s.crash(n.id)
				restartAt[n.id] = s.tick + 1 + rng.Intn(120)
			case n.up && n.p.Role == Active && rng.Intn(400) == 0:
				n.cmd = CmdSwap
			case n.up && rng.Intn(300) == 0:
				n.stale = !n.stale
			case oneWay && n.up && rng.Intn(500) == 0:
				n.cmd = CmdMaintOn
				if rng.Intn(2) == 0 {
					n.cmd = CmdMaintOff
				}
			}
		}
		s.step()
	}
	promoted := 0
	for _, e := range s.events {
		if strings.Contains(e, "→ aktiv") {
			promoted++
		}
	}
	return promoted
}

func TestSimTrioRandom(t *testing.T) {
	n := 300
	if testing.Short() {
		n = 30
	}
	promoted := 0
	for seed := int64(1); seed <= int64(n); seed++ {
		promoted += simRandom(t, seed, 2000)
		promoted += simRandomOpts(t, seed, 2000, true)
	}
	n *= 2
	t.Logf("%d Wechsel nach aktiv in %d Läufen", promoted, n)
	// Die Simulation muss tatsächlich umschalten, sonst prüft sie nichts.
	if promoted < n {
		t.Fatalf("nur %d Wechsel nach aktiv in %d Läufen", promoted, n)
	}
}

func FuzzSimTrio(f *testing.F) {
	for _, seed := range []int64{1, 7, 42, 1234, 20261025} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) { simRandom(t, seed, 2000) })
}

// FuzzSimOneWay: wie FuzzSimTrio, Ausfälle auch nur in eine Richtung,
// dazu Wartungsbefehle.
func FuzzSimOneWay(f *testing.F) {
	for _, seed := range []int64{1, 7, 42, 1234, 20261025} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) { simRandomOpts(t, seed, 2000, true) })
}

// Beobachtung: Fällt der aktive Knoten aus, ändern sich in 10 Minuten weder
// Rollen noch Amtszeiten – es wird nur gemeldet.
func TestSimObserveNoSwitch(t *testing.T) {
	for _, mode := range []Mode{Trio, Duo} {
		t.Run(string(mode), func(t *testing.T) {
			s := newSim(t, mode, nil)
			s.set.AutoMode = AutoObserve
			s.run(5)
			s.crash("a")
			s.run(600)
			s.want("b", Passive, 1)
			s.wantEvent("würde jetzt: promote (Beobachtung)")
		})
	}
}
