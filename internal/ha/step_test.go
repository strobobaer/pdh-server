package ha

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const sec = time.Second

var testWall = time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

// base liefert einen Takt um t=100 s mit Amtszeit 1.
func base(mode Mode, role Role) Input {
	return Input{
		SelfID:        "self",
		Now:           100 * sec,
		Wall:          testWall,
		Mode:          mode,
		Version:       "0.75.0",
		Gateway:       true,
		GatewayLastOK: 100 * sec,
		DataCurrent:   true,
		DataAssumed:   true,
		Settings:      DefaultSettings(),
		Persist:       Persisted{Term: 1, Role: role},
	}
}

func seenPeer(kind string, role Role, term uint64, at time.Duration) *PeerObs {
	return &PeerObs{ID: kind + "1", Kind: kind, Seen: true, LastSeen: at, Role: role, Term: term}
}

func gonePeer(kind string, role Role, term uint64, last time.Duration) *PeerObs {
	return &PeerObs{ID: kind + "1", Kind: kind, LastSeen: last, Role: role, Term: term}
}

func acts(a ...any) []Action {
	var out []Action
	for i := 0; i < len(a); i += 2 {
		out = append(out, Action{Kind: a[i].(ActionKind), Term: uint64(a[i+1].(int))})
	}
	return out
}

func TestStepTable(t *testing.T) {
	cases := []struct {
		name    string
		in      func() Input
		role    Role
		term    uint64
		actions []Action
		check   func(t *testing.T, out Output)
	}{
		{
			name: "einzel",
			in:   func() Input { return base(Single, Passive) },
			role: Active, term: 1,
			check: func(t *testing.T, out Output) {
				wantEvent(t, out, "role", "Rolle: passiv → aktiv (Amtszeit 1)")
			},
		},
		{
			name: "einzel bleibt aktiv ohne Ereignis",
			in:   func() Input { return base(Single, Active) },
			role: Active, term: 1,
			check: func(t *testing.T, out Output) {
				if len(out.Events) != 0 {
					t.Fatalf("keine Ereignisse erwartet: %v", out.Events)
				}
			},
		},
		{
			name: "trio aktiv sieht nur zeugen",
			in: func() Input {
				in := base(Trio, Active)
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				return in
			},
			role: Active, term: 1,
		},
		{
			name: "trio aktiv niemand 9 s",
			in: func() Input {
				in := base(Trio, Active)
				in.Partner = gonePeer("node", Passive, 1, 91*sec)
				in.Witness = gonePeer("witness", Witness, 1, 91*sec)
				return in
			},
			role: Active, term: 1,
		},
		{
			name: "trio aktiv niemand 10 s",
			in: func() Input {
				in := base(Trio, Active)
				in.Partner = gonePeer("node", Passive, 1, 90*sec)
				in.Witness = gonePeer("witness", Witness, 1, 90*sec)
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
			check: func(t *testing.T, out Output) {
				wantEvent(t, out, "role", "Rolle: aktiv → abgeschaltet (Amtszeit 1)")
			},
		},
		{
			name: "trio aktiv nie jemanden gesehen",
			in:   func() Input { return base(Trio, Active) },
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
		},
		{
			name: "trio passiv partner 29 s weg",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 71*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "trio passiv partner 30 s weg",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 70*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
			actions: acts(ActRequestVote, 2),
			check: func(t *testing.T, out Output) {
				if out.VotePending != 2 || out.VoteAskedAt != 100*sec {
					t.Fatalf("VotePending/VoteAskedAt = %d/%v", out.VotePending, out.VoteAskedAt)
				}
			},
		},
		{
			name: "trio passiv wiederholung nach 2 takten",
			in: func() Input {
				in := base(Trio, Passive)
				in.Now = 102 * sec
				in.Partner = gonePeer("node", Active, 1, 70*sec)
				in.VotePending, in.VoteAskedAt = 2, 100*sec
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.VotePending != 2 || out.VoteAskedAt != 100*sec {
					t.Fatalf("Bitte muss bestehen bleiben: %d/%v", out.VotePending, out.VoteAskedAt)
				}
			},
		},
		{
			name: "trio passiv wiederholung nach 5 takten",
			in: func() Input {
				in := base(Trio, Passive)
				in.Now = 105 * sec
				in.Partner = gonePeer("node", Active, 1, 70*sec)
				in.VotePending, in.VoteAskedAt = 2, 100*sec
				return in
			},
			role: Passive, term: 1,
			actions: acts(ActRequestVote, 2),
			check: func(t *testing.T, out Output) {
				if out.VoteAskedAt != 105*sec {
					t.Fatalf("VoteAskedAt = %v", out.VoteAskedAt)
				}
			},
		},
		{
			name: "trio passiv stimme erhalten",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 60*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VotePending, in.VoteAskedAt, in.VoteGranted = 2, 99*sec, 2
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
			check: func(t *testing.T, out Output) {
				if out.VotePending != 0 {
					t.Fatalf("VotePending muss zurückgesetzt sein: %d", out.VotePending)
				}
				if !out.Persist.LastSwitch.Equal(testWall) || out.Persist.SwitchCount != 1 ||
					out.Persist.SwitchDay != "2026-10-25" {
					t.Fatalf("Umschaltung nicht vermerkt: %+v", out.Persist)
				}
				wantEvent(t, out, "role",
					"Rolle: passiv → aktiv (Amtszeit 2) (Datenstand angenommen, keine Replikation)")
			},
		},
		{
			name: "trio passiv zeuge meldet erbetene amtszeit vor der zusage",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 60*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VotePending, in.VoteAskedAt = 2, 99*sec
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.VotePending != 2 {
					t.Fatalf("eigene Bitte darf nicht verfallen: %d", out.VotePending)
				}
			},
		},
		{
			name: "trio passiv stimme erhalten, alter aktiver bereits in wiedereingliederung",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = seenPeer("node", Rejoin, 2, 100*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VotePending, in.VoteAskedAt, in.VoteGranted = 2, 99*sec, 2
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
		},
		{
			name: "trio passiv stimme erhalten, partner noch aktiv gesehen",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = seenPeer("node", Active, 1, 100*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VotePending, in.VoteAskedAt, in.VoteGranted = 2, 99*sec, 2
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.VotePending != 2 {
					t.Fatalf("Bitte muss bestehen bleiben: %d", out.VotePending)
				}
			},
		},
		{
			name: "trio passiv zeuge kennt neuere amtszeit als erbeten",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 60*sec)
				in.Witness = seenPeer("witness", Witness, 3, 100*sec)
				in.VotePending, in.VoteAskedAt = 2, 99*sec
				return in
			},
			role: Passive, term: 3,
			actions: acts(ActRequestVote, 4),
			check: func(t *testing.T, out Output) {
				if out.VotePending != 4 {
					t.Fatalf("veraltete Bitte muss durch neue ersetzt sein: %d", out.VotePending)
				}
			},
		},
		{
			name: "trio passiv stimme erhalten, aktiver partner erst 3 s weg",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 97*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VotePending, in.VoteAskedAt, in.VoteGranted = 2, 99*sec, 2
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.VotePending != 2 {
					t.Fatalf("Bitte muss bestehen bleiben: %d", out.VotePending)
				}
			},
		},
		{
			name: "trio passiv zeuge hat erbetene amtszeit anderem knoten gegeben",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 60*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "other"
				in.VotePending, in.VoteAskedAt = 2, 90*sec
				return in
			},
			role: Passive, term: 2,
			actions: acts(ActRequestVote, 3),
			check: func(t *testing.T, out Output) {
				if out.VotePending != 3 {
					t.Fatalf("Bitte um 2 muss durch 3 ersetzt sein: %d", out.VotePending)
				}
			},
		},
		{
			name: "trio passiv partner unbekannt",
			in: func() Input {
				in := base(Trio, Passive)
				in.Now = 1 * sec
				in.Witness = seenPeer("witness", Witness, 1, 1*sec)
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "duo passiv partner unbekannt, gateway ok",
			in: func() Input {
				in := base(Duo, Passive)
				in.Now, in.GatewayLastOK = 1*sec, 1*sec
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "duo aktiv ohne partner, gateway ok, 120 s",
			in: func() Input {
				in := base(Duo, Active)
				in.Now, in.GatewayLastOK = 120*sec, 120*sec
				return in
			},
			role: Active, term: 1,
		},
		{
			name: "duo aktiv ohne gateway 9 s",
			in: func() Input {
				in := base(Duo, Active)
				in.Now, in.Gateway, in.GatewayLastOK = 110*sec, false, 101*sec
				in.Partner = gonePeer("node", Passive, 1, 100*sec)
				return in
			},
			role: Active, term: 1,
		},
		{
			name: "duo aktiv ohne gateway 10 s",
			in: func() Input {
				in := base(Duo, Active)
				in.Now, in.Gateway, in.GatewayLastOK = 110*sec, false, 100*sec
				in.Partner = gonePeer("node", Passive, 1, 100*sec)
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
		},
		{
			name: "duo passiv 59 s, gateway ok",
			in: func() Input {
				in := base(Duo, Passive)
				in.Now, in.GatewayLastOK = 159*sec, 159*sec
				in.Partner = gonePeer("node", Active, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "duo passiv 60 s, gateway ok",
			in: func() Input {
				in := base(Duo, Passive)
				in.Now, in.GatewayLastOK = 160*sec, 160*sec
				in.Partner = gonePeer("node", Active, 1, 100*sec)
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
			check: func(t *testing.T, out Output) {
				if out.Persist.SwitchCount != 1 {
					t.Fatalf("Zähler = %d", out.Persist.SwitchCount)
				}
				wantEvent(t, out, "role",
					"Rolle: passiv → aktiv (Amtszeit 2) (Datenstand angenommen, keine Replikation)")
			},
		},
		{
			name: "duo passiv 60 s, ohne gateway",
			in: func() Input {
				in := base(Duo, Passive)
				in.Now, in.Gateway, in.GatewayLastOK = 160*sec, false, 100*sec
				in.Partner = gonePeer("node", Active, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "aktiv sieht höhere amtszeit",
			in: func() Input {
				in := base(Trio, Active)
				in.Persist.Term = 2
				in.Partner = seenPeer("node", Active, 3, 100*sec)
				return in
			},
			role: Rejoin, term: 3,
			actions: acts(ActRejoin, 3),
			check: func(t *testing.T, out Output) {
				wantEvent(t, out, "role", "Rolle: aktiv → wiedereingliederung (Amtszeit 3)")
			},
		},
		{
			name: "wiedereingliederung wird passiv",
			in: func() Input {
				in := base(Trio, Rejoin)
				in.Persist.Term = 3
				in.Partner = seenPeer("node", Active, 3, 100*sec)
				return in
			},
			role: Passive, term: 3,
		},
		{
			name: "abgeschaltet sieht höhere amtszeit beim zeugen",
			in: func() Input {
				in := base(Trio, Fenced)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				return in
			},
			role: Rejoin, term: 2,
			actions: acts(ActRejoin, 2),
		},
		{
			name: "passiv übernimmt höhere amtszeit ohne rollenwechsel",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = seenPeer("node", Active, 4, 100*sec)
				return in
			},
			role: Passive, term: 4,
		},
		{
			name: "doppelt aktiv mit gleicher amtszeit",
			in: func() Input {
				in := base(Duo, Active)
				in.Persist.Term, in.Persist.MaintSeq = 2, 5
				in.Partner = seenPeer("node", Active, 2, 100*sec)
				return in
			},
			role: Active, term: 2,
			check: func(t *testing.T, out Output) {
				if !out.Persist.Maint || out.Persist.MaintSeq != 6 {
					t.Fatalf("Wartung nicht gesetzt: %+v", out.Persist)
				}
				wantEvent(t, out, "alarm", "Zwei aktive Server mit gleicher Amtszeit – Automatik aus")
			},
		},
		{
			name: "doppelt aktiv, wartung schon an",
			in: func() Input {
				in := base(Duo, Active)
				in.Persist.Term, in.Persist.Maint, in.Persist.MaintSeq = 2, true, 6
				in.Partner = seenPeer("node", Active, 2, 100*sec)
				return in
			},
			role: Active, term: 2,
			check: func(t *testing.T, out Output) {
				if out.Persist.MaintSeq != 6 || len(out.Events) != 0 {
					t.Fatalf("kein erneuter Alarm erwartet: %+v %v", out.Persist, out.Events)
				}
			},
		},
		{
			name: "trio abgeschaltet sieht zeugen wieder",
			in: func() Input {
				in := base(Trio, Fenced)
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				return in
			},
			role: Active, term: 1,
			actions: acts(ActUnfence, 1, ActVIPTake, 1),
			check: func(t *testing.T, out Output) {
				wantEvent(t, out, "role", "Rolle: abgeschaltet → aktiv (Amtszeit 1)")
			},
		},
		{
			name: "trio abgeschaltet sieht niemanden",
			in: func() Input {
				in := base(Trio, Fenced)
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Witness = gonePeer("witness", Witness, 1, 50*sec)
				return in
			},
			role: Fenced, term: 1,
		},
		{
			name: "duo abgeschaltet, gateway wieder da",
			in: func() Input {
				in := base(Duo, Fenced)
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				return in
			},
			role: Active, term: 1,
			actions: acts(ActUnfence, 1, ActVIPTake, 1),
		},
		{
			name: "duo abgeschaltet, kein gateway, kein partner",
			in: func() Input {
				in := base(Duo, Fenced)
				in.Gateway, in.GatewayLastOK = false, 50*sec
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				return in
			},
			role: Fenced, term: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in()
			out := Step(in)
			if out.Persist.Role != c.role || out.Persist.Term != c.term {
				t.Fatalf("Rolle/Amtszeit = %s/%d, erwartet %s/%d (Ereignisse %v)",
					out.Persist.Role, out.Persist.Term, c.role, c.term, out.Events)
			}
			if len(out.Actions) != 0 || len(c.actions) != 0 {
				if !reflect.DeepEqual(out.Actions, c.actions) {
					t.Fatalf("Aktionen = %v, erwartet %v", out.Actions, c.actions)
				}
			}
			if c.check != nil {
				c.check(t, out)
			}
		})
	}
}

// TestStepPersistCopy: Output.Persist ist eine vollständige Kopie des Zustands.
func TestStepPersistCopy(t *testing.T) {
	in := base(Trio, Active)
	in.Persist.PartnerVersion = "0.75.0"
	in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-24", 2
	in.Witness = seenPeer("witness", Witness, 1, 100*sec)
	out := Step(in)
	if out.Persist != in.Persist {
		t.Fatalf("Zustand verändert: %+v, erwartet %+v", out.Persist, in.Persist)
	}
}

// TestStepSwitchDay: neuer Tag beginnt den Zähler von vorn.
func TestStepSwitchDay(t *testing.T) {
	in := base(Duo, Passive)
	in.Now, in.GatewayLastOK = 160*sec, 160*sec
	in.Partner = gonePeer("node", Active, 1, 100*sec)
	in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-24", 2
	out := Step(in)
	if out.Persist.SwitchDay != "2026-10-25" || out.Persist.SwitchCount != 1 {
		t.Fatalf("Tageszähler = %s/%d", out.Persist.SwitchDay, out.Persist.SwitchCount)
	}
}

func wantEvent(t *testing.T, out Output, kind, text string) {
	t.Helper()
	for _, e := range out.Events {
		if e.Kind == kind && strings.Contains(e.Text, text) {
			return
		}
	}
	t.Fatalf("Ereignis %s %q fehlt: %v", kind, text, out.Events)
}
