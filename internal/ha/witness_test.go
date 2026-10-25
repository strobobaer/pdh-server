package ha

import (
	"testing"
	"time"
)

func TestVote(t *testing.T) {
	s := DefaultSettings()
	// Knoten a war aktiv und ist seit 40 s weg, b bittet um Amtszeit 2.
	nodes := func(aLast time.Duration, aSeen bool) []PeerObs {
		return []PeerObs{
			{ID: "a", Kind: "node", Seen: aSeen, LastSeen: aLast, Role: Active, Term: 1},
			{ID: "b", Kind: "node", Seen: true, LastSeen: 100 * sec, Role: Passive, Term: 1},
		}
	}
	cases := []struct {
		name    string
		p       Persisted
		req     VoteRequest
		nodes   []PeerObs
		now     time.Duration
		granted bool
	}{
		{"gewährt", Persisted{Role: Witness, Term: 1, VotedTerm: 1, VotedFor: "a"},
			VoteRequest{From: "b", Term: 2}, nodes(60*sec, false), 100 * sec, true},
		{"zweite bitte gleiche amtszeit anderer knoten",
			Persisted{Role: Witness, Term: 2, VotedTerm: 2, VotedFor: "b"},
			VoteRequest{From: "a", Term: 2}, nodes(60*sec, false), 100 * sec, false},
		{"wiederholte bitte desselben knotens",
			Persisted{Role: Witness, Term: 2, VotedTerm: 2, VotedFor: "b"},
			VoteRequest{From: "b", Term: 2}, nodes(60*sec, false), 100 * sec, true},
		{"aktiver knoten vor 5 s gesehen", Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2}, nodes(95*sec, false), 100 * sec, false},
		{"aktiver knoten vor 29 s gesehen", Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2}, nodes(71*sec, false), 100 * sec, false},
		{"aktiver knoten vor 30 s gesehen", Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2}, nodes(70*sec, false), 100 * sec, true},
		{"aktiver knoten gerade gesehen", Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2}, nodes(100*sec, true), 100 * sec, false},
		{"niedrigere amtszeit", Persisted{Role: Witness, Term: 3, VotedTerm: 3, VotedFor: "a"},
			VoteRequest{From: "b", Term: 2}, nodes(60*sec, false), 100 * sec, false},
		{"zeuge kennt höhere amtszeit als erbeten", Persisted{Role: Witness, Term: 3, VotedTerm: 1},
			VoteRequest{From: "b", Term: 3}, nodes(60*sec, false), 100 * sec, false},
		{"zeuge kennt höhere amtszeit, wiederholung", Persisted{Role: Witness, Term: 3, VotedTerm: 2, VotedFor: "b"},
			VoteRequest{From: "b", Term: 2}, nodes(60*sec, false), 100 * sec, false},
		{"knoten kennt höhere amtszeit", Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2},
			[]PeerObs{{ID: "a", Kind: "node", LastSeen: 60 * sec, Role: Passive, Term: 3}},
			100 * sec, false},
		{"zeuge gerade gestartet, anderer knoten noch nie gesehen",
			Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2},
			[]PeerObs{{ID: "a", Kind: "node"}}, 10 * sec, false},
		{"zeuge läuft lange, anderer knoten nie gesehen",
			Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{From: "b", Term: 2},
			[]PeerObs{{ID: "a", Kind: "node"}}, 30 * sec, true},
		{"ohne absender", Persisted{Role: Witness, Term: 1, VotedTerm: 1},
			VoteRequest{Term: 2}, nodes(60*sec, false), 100 * sec, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, next := Vote(c.p, c.req, c.nodes, c.now, s)
			if ok != c.granted {
				t.Fatalf("gewährt = %v, erwartet %v", ok, c.granted)
			}
			if !ok {
				if next != c.p {
					t.Fatalf("abgelehnt darf den Zustand nicht ändern: %+v", next)
				}
				return
			}
			if next.VotedTerm != c.req.Term || next.VotedFor != c.req.From {
				t.Fatalf("Stimme nicht vermerkt: %+v", next)
			}
			if next.Term < c.req.Term {
				t.Fatalf("Amtszeit des Zeugen nicht nachgezogen: %+v", next)
			}
		})
	}
}
