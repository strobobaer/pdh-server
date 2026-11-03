package ha

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const sec = time.Second

var testWall = time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

// realSettings: Standard, aber automatische Entscheidungen wirken (Teil-2-Verhalten).
func realSettings() Settings {
	s := DefaultSettings()
	s.AutoMode = AutoReal
	return s
}

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
		Settings:      realSettings(),
		Persist:       Persisted{Term: 1, Role: role, PartnerVersion: "0.75.0"},
		LastMutual:    100 * sec,
	}
}

func seenPeer(kind string, role Role, term uint64, at time.Duration) *PeerObs {
	return &PeerObs{ID: kind + "1", Kind: kind, Seen: true, LastSeen: at, Role: role, Term: term,
		Version: "0.75.0", SeesSelf: true}
}

func gonePeer(kind string, role Role, term uint64, last time.Duration) *PeerObs {
	return &PeerObs{ID: kind + "1", Kind: kind, LastSeen: last, Role: role, Term: term, Version: "0.75.0"}
}

func acts(a ...any) []Action {
	var out []Action
	for i := 0; i < len(a); i += 2 {
		out = append(out, Action{Kind: a[i].(ActionKind), Term: uint64(a[i+1].(int))})
	}
	return out
}

type stepCase struct {
	name    string
	in      func() Input
	role    Role
	term    uint64
	actions []Action
	check   func(t *testing.T, out Output)
}

func runCases(t *testing.T, cases []stepCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in()
			out := Step(in)
			if out.Persist.Role != c.role || out.Persist.Term != c.term {
				t.Fatalf("Rolle/Amtszeit = %s/%d, erwartet %s/%d (Ereignisse %v)",
					out.Persist.Role, out.Persist.Term, c.role, c.term, out.Events)
			}
			if len(out.Actions) != 0 || len(c.actions) != 0 {
				// Manual prüfen eigene Tests; die Tabellen vergleichen Art und Amtszeit
				got := make([]Action, len(out.Actions))
				for i, a := range out.Actions {
					got[i] = Action{Kind: a.Kind, Term: a.Term}
				}
				if !reflect.DeepEqual(got, c.actions) {
					t.Fatalf("Aktionen = %v, erwartet %v", out.Actions, c.actions)
				}
			}
			if c.check != nil {
				c.check(t, out)
			}
		})
	}
}

func TestStepTable(t *testing.T) {
	runCases(t, []stepCase{
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
				in.LastMutual = 91 * sec
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
				in.LastMutual = 90 * sec
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
			in: func() Input {
				in := base(Trio, Active)
				in.LastMutual = 0
				return in
			},
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
				wantEvent(t, out, "alarm", "Aktiver Server abgelöst (neue Amtszeit 3)")
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
			check: func(t *testing.T, out Output) {
				for _, e := range out.Events {
					if e.Kind == "alarm" {
						t.Fatalf("abgeschaltet: kein Alarm erwartet, %v", out.Events)
					}
				}
			},
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
	})
}

// TestStepPersistCopy: Output.Persist ist eine vollständige Kopie des Zustands.
func TestStepPersistCopy(t *testing.T) {
	in := base(Trio, Active)
	in.Persist.PartnerVersion = "0.75.0"
	in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-24", 2
	in.Witness = seenPeer("witness", Witness, 1, 100*sec)
	out := Step(in)
	if !out.Persist.Equal(in.Persist) {
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

// duoTakeover: Duo-Passiver, Partner seit 60 s weg, Gateway ok – übernimmt ohne Bremse.
func duoTakeover() Input {
	in := base(Duo, Passive)
	in.Now, in.GatewayLastOK = 160*sec, 160*sec
	in.Partner = gonePeer("node", Active, 1, 100*sec)
	return in
}

func noActions(t *testing.T, out Output) {
	t.Helper()
	if len(out.Actions) != 0 {
		t.Fatalf("keine Aktionen erwartet: %v", out.Actions)
	}
}

func TestStepBrakes(t *testing.T) {
	const tooMany = "Zu viele Umschaltungen heute – Automatik aus"
	runCases(t, []stepCase{
		{
			name: "ohne bremse",
			in:   duoTakeover,
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
		},
		{
			name: "wartung",
			in: func() Input {
				in := duoTakeover()
				in.Persist.Maint = true
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "version anders",
			in: func() Input {
				in := duoTakeover()
				in.Persist.PartnerVersion = "0.74.0"
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "wartezeit 14 min",
			in: func() Input {
				in := duoTakeover()
				in.Persist.LastSwitch = testWall.Add(-14 * time.Minute)
				in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 1
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "wartezeit 15 min",
			in: func() Input {
				in := duoTakeover()
				in.Persist.LastSwitch = testWall.Add(-15 * time.Minute)
				in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 1
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
			check: func(t *testing.T, out Output) {
				if out.Persist.SwitchCount != 2 || !out.Persist.LastSwitch.Equal(testWall) {
					t.Fatalf("Umschaltung nicht vermerkt: %+v", out.Persist)
				}
			},
		},
		{
			name: "datenstand nicht aktuell",
			in: func() Input {
				in := duoTakeover()
				in.DataCurrent = false
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "zähler 3",
			in: func() Input {
				in := duoTakeover()
				in.Persist.LastSwitch = testWall.Add(-time.Hour)
				in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 3
				in.Persist.MaintSeq, in.Partner.MaintSeq = 4, 4
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				noActions(t, out)
				if !out.Persist.Maint || out.Persist.MaintSeq != 5 {
					t.Fatalf("Wartung nicht gesetzt: %+v", out.Persist)
				}
				wantEvent(t, out, "alarm", tooMany)
			},
		},
		{
			name: "zähler 3, neuer tag",
			in: func() Input {
				in := duoTakeover()
				in.Persist.LastSwitch = testWall.Add(-20 * time.Hour)
				in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-24", 3
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
			check: func(t *testing.T, out Output) {
				if out.Persist.SwitchDay != "2026-10-25" || out.Persist.SwitchCount != 1 || out.Persist.Maint {
					t.Fatalf("Tageszähler = %+v", out.Persist)
				}
			},
		},
		{
			name: "zähler 3 und wartezeit: kein wartungsalarm",
			in: func() Input {
				in := duoTakeover()
				in.Persist.LastSwitch = testWall.Add(-time.Minute)
				in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 3
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.Persist.Maint || len(out.Events) != 0 {
					t.Fatalf("Wartung nur, wenn allein der Zähler sperrt: %+v %v", out.Persist, out.Events)
				}
			},
		},
		{
			name: "trio wartung verhindert abschaltung",
			in: func() Input {
				in := base(Trio, Active)
				in.Persist.Maint = true
				in.Partner = gonePeer("node", Passive, 1, 90*sec)
				in.Witness = gonePeer("witness", Witness, 1, 90*sec)
				return in
			},
			role: Active, term: 1,
		},
		{
			name: "duo wartung verhindert abschaltung",
			in: func() Input {
				in := base(Duo, Active)
				in.Persist.Maint = true
				in.Now, in.Gateway, in.GatewayLastOK = 110*sec, false, 100*sec
				in.Partner = gonePeer("node", Passive, 1, 100*sec)
				return in
			},
			role: Active, term: 1,
		},
		{
			name: "trio wartung verhindert stimmbitte",
			in: func() Input {
				in := base(Trio, Passive)
				in.Persist.Maint = true
				in.Partner = gonePeer("node", Active, 1, 70*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "trio zähler 3 bei stimmbitte",
			in: func() Input {
				in := base(Trio, Passive)
				in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 3
				in.Partner = gonePeer("node", Active, 1, 70*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				noActions(t, out)
				if !out.Persist.Maint {
					t.Fatal("Wartung nicht gesetzt")
				}
				wantEvent(t, out, "alarm", tooMany)
			},
		},
		{
			name: "trio version anders verhindert übernahme mit stimme",
			in: func() Input {
				in := base(Trio, Passive)
				in.Persist.PartnerVersion = "0.74.0"
				in.Partner = gonePeer("node", Active, 1, 60*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VotePending, in.VoteAskedAt, in.VoteGranted = 2, 99*sec, 2
				return in
			},
			role: Passive, term: 1,
		},
		{
			name: "gesehener partner aktualisiert version",
			in: func() Input {
				in := base(Trio, Passive)
				in.Persist.PartnerVersion = "0.74.0"
				in.Partner = seenPeer("node", Active, 1, 100*sec)
				in.Partner.Version = "0.76.0"
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.Persist.PartnerVersion != "0.76.0" {
					t.Fatalf("PartnerVersion = %q", out.Persist.PartnerVersion)
				}
			},
		},
		{
			name: "nicht gesehener partner ändert version nicht",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 90*sec)
				in.Partner.Version = "0.76.0"
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.Persist.PartnerVersion != "0.75.0" {
					t.Fatalf("PartnerVersion = %q", out.Persist.PartnerVersion)
				}
			},
		},
	})
}

func TestAllowed(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*Input)
		ok     bool
		reason string
	}{
		{"frei", func(in *Input) {}, true, ""},
		{"wartung vor wartungsstand", func(in *Input) {
			in.Persist.Maint, in.Persist.MaintSeq = true, 6
		}, false, "Wartung"},
		{"wartungsstand vor version", func(in *Input) {
			in.Persist.MaintSeq, in.Partner.MaintSeq = 6, 5
			in.Persist.PartnerVersion = "x"
		}, false, "Wartungsstand beim Partner noch nicht angekommen"},
		{"wartung vor version", func(in *Input) {
			in.Persist.Maint, in.Persist.PartnerVersion = true, "x"
		}, false, "Wartung"},
		{"version vor wartezeit", func(in *Input) {
			in.Persist.PartnerVersion, in.Persist.LastSwitch = "x", testWall
		}, false, "Versionen unterschiedlich"},
		{"wartezeit vor datenstand", func(in *Input) {
			in.Persist.LastSwitch, in.DataCurrent = testWall.Add(-time.Minute), false
		}, false, "Wartezeit nach Umschaltung"},
		{"datenstand vor zähler", func(in *Input) {
			in.DataCurrent = false
			in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 3
		}, false, "Datenstand nicht aktuell"},
		{"zähler", func(in *Input) {
			in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 3
		}, false, "Tageshöchstzahl erreicht"},
		{"zähler gestern", func(in *Input) {
			in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-24", 3
		}, true, ""},
		{"zähler 2", func(in *Input) {
			in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 2
		}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := duoTakeover()
			c.mod(&in)
			ok, reason := Allowed(in)
			if ok != c.ok || reason != c.reason {
				t.Fatalf("Allowed = %v %q, erwartet %v %q", ok, reason, c.ok, c.reason)
			}
		})
	}
}

func TestStepCommands(t *testing.T) {
	swapIn := func(maint bool) func() Input {
		return func() Input {
			in := base(Trio, Active)
			in.Persist.Term, in.Persist.Maint = 3, maint
			in.Partner = seenPeer("node", Passive, 3, 100*sec)
			in.Witness = seenPeer("witness", Witness, 3, 100*sec)
			in.Command = CmdSwap
			return in
		}
	}
	swapCheck := func(t *testing.T, out Output) {
		if out.HandoverPending != 4 || out.HandoverAt != 100*sec || out.HandoverSentAt != 0 || out.HandoverReadyAt != 0 {
			t.Fatalf("Rollentausch nicht vermerkt: %d %v %v", out.HandoverPending, out.HandoverAt, out.HandoverSentAt)
		}
		wantEvent(t, out, "role", "Rolle: aktiv → passiv (Amtszeit 3)")
	}
	receive := func(handover uint64) func() Input {
		return func() Input {
			in := base(Trio, Passive)
			in.Persist.Term = 3
			in.Persist.Maint = true
			in.Persist.LastSwitch = testWall.Add(-time.Minute)
			in.Persist.SwitchDay, in.Persist.SwitchCount = "2026-10-25", 3
			in.Partner = seenPeer("node", Passive, 3, 100*sec)
			in.Witness = seenPeer("witness", Witness, 3, 100*sec)
			in.Handover = handover
			return in
		}
	}
	pending := func(at, sentAt time.Duration) Input {
		in := base(Trio, Passive)
		in.Persist.Term = 3
		in.Partner = seenPeer("node", Passive, 3, 100*sec)
		in.Witness = seenPeer("witness", Witness, 3, 100*sec)
		in.HandoverPending, in.HandoverAt, in.HandoverSentAt = 4, at, sentAt
		// Abgabe der Datenbank erledigt, Übergabe läuft seit at
		in.DemoteDone, in.HandoverReadyAt = 3, at
		return in
	}
	runCases(t, []stepCase{
		{
			name: "swap",
			in:   swapIn(false),
			role: Passive, term: 3,
			actions: acts(ActDemote, 3, ActVIPRelease, 3),
			check:   swapCheck,
		},
		{
			name: "swap in wartung",
			in:   swapIn(true),
			role: Passive, term: 3,
			actions: acts(ActDemote, 3, ActVIPRelease, 3),
			check:   swapCheck,
		},
		{
			name: "swap ohne gesehenen partner",
			in: func() Input {
				in := swapIn(false)()
				in.Partner = gonePeer("node", Passive, 3, 90*sec)
				return in
			},
			role: Active, term: 3,
		},
		{
			name: "swap als passiv wird ignoriert",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = seenPeer("node", Active, 1, 100*sec)
				in.Command = CmdSwap
				return in
			},
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 0 || len(out.Events) != 0 {
					t.Fatalf("swap als passiv muss ignoriert werden: %d %v", out.HandoverPending, out.Events)
				}
			},
		},
		{
			name: "handover empfangen, auch in wartung und ohne zählung",
			in:   receive(4),
			role: Active, term: 4,
			actions: acts(ActPromote, 4, ActVIPTake, 4),
			check: func(t *testing.T, out Output) {
				if out.Persist.SwitchCount != 3 || !out.Persist.LastSwitch.Equal(testWall.Add(-time.Minute)) {
					t.Fatalf("Rollentausch darf nicht zählen: %+v", out.Persist)
				}
				wantEvent(t, out, "role", "Rolle: passiv → aktiv (Amtszeit 4)")
			},
		},
		{
			name: "handover mit falscher amtszeit",
			in:   receive(6),
			role: Passive, term: 3,
		},
		{
			name: "später handover nach rückfall des partners wird abgelehnt",
			in: func() Input {
				in := receive(4)()
				in.Partner.Role = Active
				return in
			},
			role: Passive, term: 3,
		},
		{
			name: "handover, partner mit anderer amtszeit",
			in: func() Input {
				in := receive(4)()
				in.Persist.Term = 3
				in.Partner.Term = 2
				return in
			},
			role: Passive, term: 3,
		},
		{
			name: "handover, partner nicht gesehen",
			in: func() Input {
				in := receive(4)()
				in.Partner = gonePeer("node", Passive, 3, 95*sec)
				return in
			},
			role: Passive, term: 3,
		},
		{
			name: "rollentausch: partner sieht uns nicht mehr",
			in: func() Input {
				in := pending(95*sec, 95*sec)
				in.Partner.SeesSelf = false
				return in
			},
			role: Passive, term: 4,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 0 {
					t.Fatalf("Rollentausch nicht beendet: %d", out.HandoverPending)
				}
				wantEvent(t, out, "alarm", "Rollentausch fehlgeschlagen")
			},
		},
		{
			name: "rollentausch: wiederholung nach 5 takten",
			in:   func() Input { return pending(95*sec, 95*sec) },
			role: Passive, term: 3,
			actions: acts(ActHandover, 4),
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 4 || out.HandoverAt != 95*sec || out.HandoverSentAt != 100*sec {
					t.Fatalf("Rollentausch: %d %v %v", out.HandoverPending, out.HandoverAt, out.HandoverSentAt)
				}
			},
		},
		{
			name: "rollentausch: keine wiederholung nach 3 takten",
			in:   func() Input { return pending(95*sec, 97*sec) },
			role: Passive, term: 3,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 4 || out.HandoverSentAt != 97*sec {
					t.Fatalf("Rollentausch: %d %v", out.HandoverPending, out.HandoverSentAt)
				}
			},
		},
		{
			name: "rollentausch: kurz vor ablauf keine wiederholung",
			in:   func() Input { return pending(74*sec, 95*sec) },
			role: Passive, term: 3,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 4 {
					t.Fatalf("Rollentausch muss weiterlaufen: %d", out.HandoverPending)
				}
			},
		},
		{
			name: "rollentausch: partner aus den augen verloren",
			in: func() Input {
				in := pending(95*sec, 95*sec)
				in.Partner = gonePeer("node", Passive, 3, 97*sec)
				return in
			},
			role: Passive, term: 4,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 0 {
					t.Fatalf("Rollentausch nicht beendet: %d", out.HandoverPending)
				}
				wantEvent(t, out, "alarm", "Rollentausch fehlgeschlagen")
			},
		},
		{
			name: "rollentausch: partner gesehen, aber nicht passiv – keine wiederholung",
			in: func() Input {
				in := pending(95*sec, 95*sec)
				in.Partner.Role = Rejoin
				return in
			},
			role: Passive, term: 3,
		},
		{
			name: "rollentausch gelungen",
			in: func() Input {
				in := pending(95*sec, 95*sec)
				in.Partner = seenPeer("node", Active, 4, 100*sec)
				return in
			},
			role: Passive, term: 4,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 0 || out.HandoverAt != 0 || out.HandoverSentAt != 0 {
					t.Fatalf("Rollentausch nicht beendet: %d", out.HandoverPending)
				}
			},
		},
		{
			name: "rollentausch fehlgeschlagen",
			in:   func() Input { return pending(70*sec, 95*sec) },
			role: Active, term: 3,
			actions: acts(ActUnfence, 3, ActVIPTake, 3),
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 0 {
					t.Fatalf("Rollentausch nicht beendet: %d", out.HandoverPending)
				}
				wantEvent(t, out, "alarm", "Rollentausch fehlgeschlagen")
				wantEvent(t, out, "role", "Rolle: passiv → aktiv (Amtszeit 3)")
			},
		},
		{
			name: "rollentausch abgelaufen, partner nicht gesehen: nicht zurück, amtszeit verbraucht",
			in: func() Input {
				in := pending(70*sec, 95*sec)
				in.Partner = gonePeer("node", Passive, 3, 95*sec)
				return in
			},
			role: Passive, term: 4,
			check: func(t *testing.T, out Output) {
				if out.HandoverPending != 0 {
					t.Fatalf("Rollentausch nicht beendet: %d", out.HandoverPending)
				}
				wantEvent(t, out, "alarm", "Rollentausch fehlgeschlagen")
			},
		},
		{
			name: "übernahme erzwingen",
			in: func() Input {
				in := base(Trio, Passive)
				in.Persist.Term, in.Persist.Maint = 7, true
				in.DataCurrent = false
				in.Partner = seenPeer("node", Passive, 6, 100*sec)
				in.Witness = seenPeer("witness", Witness, 7, 100*sec)
				in.Command = CmdForceTakeover
				return in
			},
			role: Active, term: 8,
			actions: acts(ActPromote, 8, ActVIPTake, 8),
			check: func(t *testing.T, out Output) {
				if !out.Persist.LastSwitch.Equal(testWall) || out.Persist.SwitchCount != 0 {
					t.Fatalf("erzwungen: Wartezeit ja, Zählung nein: %+v", out.Persist)
				}
				wantEvent(t, out, "alarm", "Übernahme erzwungen")
				wantEvent(t, out, "role", "Rolle: passiv → aktiv (Amtszeit 8)")
			},
		},
		{
			name: "übernahme erzwingen, zeuge kennt höhere amtszeit",
			in: func() Input {
				in := base(Trio, Fenced)
				in.Persist.Term = 2
				in.Partner = gonePeer("node", Active, 4, 50*sec)
				in.Witness = gonePeer("witness", Witness, 5, 50*sec)
				in.Command = CmdForceTakeover
				return in
			},
			role: Active, term: 6,
			actions: acts(ActPromote, 6, ActVIPTake, 6),
		},
		{
			name: "übernahme erzwingen als aktiv wird ignoriert",
			in: func() Input {
				in := base(Trio, Active)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				in.Command = CmdForceTakeover
				return in
			},
			role: Active, term: 1,
			check: func(t *testing.T, out Output) {
				if len(out.Events) != 0 {
					t.Fatalf("keine Ereignisse erwartet: %v", out.Events)
				}
			},
		},
		{
			name: "wartung ein",
			in: func() Input {
				in := base(Trio, Active)
				in.Persist.MaintSeq = 2
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				in.Command = CmdMaintOn
				return in
			},
			role: Active, term: 1,
			check: func(t *testing.T, out Output) {
				if !out.Persist.Maint || out.Persist.MaintSeq != 3 {
					t.Fatalf("Wartung: %+v", out.Persist)
				}
			},
		},
		{
			name: "wartung aus",
			in: func() Input {
				in := base(Trio, Active)
				in.Persist.Maint, in.Persist.MaintSeq = true, 3
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				in.Command = CmdMaintOff
				return in
			},
			role: Active, term: 1,
			check: func(t *testing.T, out Output) {
				if out.Persist.Maint || out.Persist.MaintSeq != 4 {
					t.Fatalf("Wartung: %+v", out.Persist)
				}
			},
		},
	})
}

func TestStepMaintSpread(t *testing.T) {
	cases := []struct {
		name     string
		mod      func(*Input)
		maint    bool
		maintSeq uint64
	}{
		{"partner höher, wartung an", func(in *Input) {
			in.Partner.Maint, in.Partner.MaintSeq = true, 5
		}, true, 5},
		{"partner höher, wartung aus", func(in *Input) {
			in.Persist.Maint = true
			in.Partner.Maint, in.Partner.MaintSeq = false, 5
		}, false, 5},
		{"partner niedriger", func(in *Input) {
			in.Partner.Maint, in.Partner.MaintSeq = true, 2
		}, false, 3},
		{"partner gleich, wartung an gewinnt", func(in *Input) {
			in.Partner.Maint, in.Partner.MaintSeq = true, 3
		}, true, 3},
		{"partner gleich, eigene wartung bleibt", func(in *Input) {
			in.Persist.Maint = true
			in.Partner.Maint, in.Partner.MaintSeq = false, 3
		}, true, 3},
		{"partner nicht gesehen", func(in *Input) {
			in.Partner = gonePeer("node", Active, 1, 90*sec)
			in.Partner.Maint, in.Partner.MaintSeq = true, 9
		}, false, 3},
		{"zeuge höher", func(in *Input) {
			in.Witness = seenPeer("witness", Witness, 1, 100*sec)
			in.Witness.Maint, in.Witness.MaintSeq = true, 7
		}, true, 7},
		{"übernommen, dann befehl", func(in *Input) {
			in.Partner.Maint, in.Partner.MaintSeq = true, 5
			in.Command = CmdMaintOff
		}, false, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base(Trio, Passive)
			in.Persist.MaintSeq = 3
			in.Partner = seenPeer("node", Active, 1, 100*sec)
			c.mod(&in)
			out := Step(in)
			if out.Persist.Maint != c.maint || out.Persist.MaintSeq != c.maintSeq {
				t.Fatalf("Wartung = %v/%d, erwartet %v/%d", out.Persist.Maint, out.Persist.MaintSeq, c.maint, c.maintSeq)
			}
		})
	}
}

func TestStepNoActive(t *testing.T) {
	const text = "Kein aktiver Server – bitte Übernahme erzwingen"
	twoPassive := func(mode Mode, since time.Duration, alarmed bool) func() Input {
		return func() Input {
			in := base(mode, Passive)
			in.Partner = seenPeer("node", Passive, 1, 100*sec)
			if mode == Trio {
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
			}
			in.NoActiveSince, in.NoActiveAlarmed = since, alarmed
			return in
		}
	}
	state := func(since time.Duration, alarmed bool, alarm bool) func(t *testing.T, out Output) {
		return func(t *testing.T, out Output) {
			if out.NoActiveSince != since || out.NoActiveAlarmed != alarmed {
				t.Fatalf("NoActive = %v/%v, erwartet %v/%v", out.NoActiveSince, out.NoActiveAlarmed, since, alarmed)
			}
			if alarm {
				wantEvent(t, out, "alarm", text)
			} else if len(out.Events) != 0 {
				t.Fatalf("kein Ereignis erwartet: %v", out.Events)
			}
		}
	}
	runCases(t, []stepCase{
		{name: "beginn", in: twoPassive(Trio, 0, false), role: Passive, term: 1, check: state(100*sec, false, false)},
		{name: "trio 29 s", in: twoPassive(Trio, 71*sec, false), role: Passive, term: 1, check: state(71*sec, false, false)},
		{name: "trio 30 s", in: twoPassive(Trio, 70*sec, false), role: Passive, term: 1, check: state(70*sec, true, true)},
		{name: "nur einmal", in: twoPassive(Trio, 10*sec, true), role: Passive, term: 1, check: state(10*sec, true, false)},
		{name: "duo 59 s", in: twoPassive(Duo, 41*sec, false), role: Passive, term: 1, check: state(41*sec, false, false)},
		{name: "duo 60 s", in: twoPassive(Duo, 40*sec, false), role: Passive, term: 1, check: state(40*sec, true, true)},
		{
			name: "partner aktiv beendet die phase",
			in: func() Input {
				in := twoPassive(Trio, 10*sec, true)()
				in.Partner.Role = Active
				return in
			},
			role: Passive, term: 1, check: state(0, false, false),
		},
		{
			name: "partner nicht gesehen beendet die phase",
			in: func() Input {
				in := twoPassive(Trio, 10*sec, true)()
				in.Partner = gonePeer("node", Passive, 1, 95*sec)
				return in
			},
			role: Passive, term: 1, check: state(0, false, false),
		},
		{
			name: "laufender rollentausch",
			in: func() Input {
				in := twoPassive(Trio, 10*sec, false)()
				in.Persist.Term = 3
				in.Partner.Term = 3
				in.HandoverPending, in.HandoverAt, in.HandoverSentAt = 4, 98*sec, 98*sec
				return in
			},
			role: Passive, term: 3, check: state(0, false, false),
		},
	})
}

func TestStepBoot(t *testing.T) {
	boot := func(mode Mode, role Role) Input {
		in := base(mode, role)
		in.Now, in.GatewayLastOK, in.Booting = 0, 0, true
		return in
	}
	held := func(mode Mode, now time.Duration) Input {
		in := base(mode, Fenced)
		in.Now, in.GatewayLastOK = now, now
		in.HoldUntil = DefaultSettings().Takeover(mode)
		in.Partner = gonePeer("node", Passive, 1, 0)
		return in
	}
	holdIs := func(want time.Duration) func(t *testing.T, out Output) {
		return func(t *testing.T, out Output) {
			t.Helper()
			if out.HoldUntil != want {
				t.Fatalf("HoldUntil = %v, erwartet %v", out.HoldUntil, want)
			}
		}
	}
	runCases(t, []stepCase{
		{
			name: "passiv bleibt passiv",
			in: func() Input {
				in := boot(Trio, Passive)
				in.Partner = gonePeer("node", Passive, 1, 0)
				in.Witness = gonePeer("witness", Witness, 1, 0)
				return in
			},
			role: Passive, term: 1, check: holdIs(0),
		},
		{
			name: "aktiv, partner mit gleicher amtszeit",
			in: func() Input {
				in := boot(Trio, Active)
				in.Partner = seenPeer("node", Passive, 1, 0)
				return in
			},
			role: Active, term: 1,
			actions: acts(ActUnfence, 1, ActVIPTake, 1),
			check:   holdIs(0),
		},
		{
			name: "aktiv, partner mit höherer amtszeit",
			in: func() Input {
				in := boot(Trio, Active)
				in.Partner = seenPeer("node", Active, 2, 0)
				return in
			},
			role: Rejoin, term: 2,
			actions: acts(ActRejoin, 2),
		},
		{
			name: "trio aktiv allein",
			in: func() Input {
				in := boot(Trio, Active)
				in.Partner = gonePeer("node", Passive, 1, 0)
				in.Witness = seenPeer("witness", Witness, 1, 0)
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1),
			check: func(t *testing.T, out Output) {
				holdIs(30*sec)(t, out)
				wantEvent(t, out, "role", "Rolle: aktiv → abgeschaltet (Amtszeit 1)")
			},
		},
		{
			name: "trio abgeschaltet beim start wartet ebenfalls",
			in: func() Input {
				in := boot(Trio, Fenced)
				in.Witness = seenPeer("witness", Witness, 1, 0)
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1),
			check:   holdIs(30 * sec),
		},
		{
			name: "trio 29 s mit zeuge",
			in: func() Input {
				in := held(Trio, 29*sec)
				in.Witness = seenPeer("witness", Witness, 1, 29*sec)
				return in
			},
			role: Fenced, term: 1, check: holdIs(30 * sec),
		},
		{
			name: "trio 29 s mit partner",
			in: func() Input {
				in := held(Trio, 29*sec)
				in.Partner = seenPeer("node", Passive, 1, 29*sec)
				return in
			},
			role: Active, term: 1,
			actions: acts(ActUnfence, 1, ActVIPTake, 1),
			check:   holdIs(0),
		},
		{
			name: "trio 30 s mit zeuge",
			in: func() Input {
				in := held(Trio, 30*sec)
				in.Witness = seenPeer("witness", Witness, 1, 30*sec)
				return in
			},
			role: Active, term: 1,
			actions: acts(ActUnfence, 1, ActVIPTake, 1),
			check:   holdIs(0),
		},
		{
			name: "trio 30 s ohne zeuge",
			in: func() Input {
				in := held(Trio, 30*sec)
				in.Witness = gonePeer("witness", Witness, 1, 0)
				return in
			},
			role: Fenced, term: 1,
		},
		{
			name: "duo 59 s mit gateway",
			in:   func() Input { return held(Duo, 59*sec) },
			role: Fenced, term: 1, check: holdIs(60 * sec),
		},
		{
			name: "duo 60 s mit gateway",
			in:   func() Input { return held(Duo, 60*sec) },
			role: Active, term: 1,
			actions: acts(ActUnfence, 1, ActVIPTake, 1),
			check:   holdIs(0),
		},
		{
			name: "duo 60 s ohne gateway",
			in: func() Input {
				in := held(Duo, 60*sec)
				in.Gateway, in.GatewayLastOK = false, 0
				return in
			},
			role: Fenced, term: 1,
		},
		{
			name: "einzel sofort aktiv",
			in:   func() Input { return boot(Single, Active) },
			role: Active, term: 1,
			check: func(t *testing.T, out Output) {
				holdIs(0)(t, out)
				if len(out.Events) != 0 {
					t.Fatalf("keine Ereignisse erwartet: %v", out.Events)
				}
			},
		},
	})
}

// TestStepFenceReturn: ein abgeschalteter Knoten kehrt nur bei gegenseitiger
// Sicht zurück; mit Zusage wird nicht neben einem abgeschalteten Partner übernommen.
func TestStepFenceReturn(t *testing.T) {
	runCases(t, []stepCase{
		{
			name: "trio abgeschaltet sieht zeugen, zeuge sieht ihn nicht",
			in: func() Input {
				in := base(Trio, Fenced)
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				in.Witness.SeesSelf = false
				return in
			},
			role: Fenced, term: 1,
		},
		{
			name: "trio abgeschaltet sieht partner, partner sieht ihn nicht",
			in: func() Input {
				in := base(Trio, Fenced)
				in.Partner = seenPeer("node", Passive, 1, 100*sec)
				in.Partner.SeesSelf = false
				in.Witness = gonePeer("witness", Witness, 1, 50*sec)
				return in
			},
			role: Fenced, term: 1,
		},
		{
			name: "neustart aktiv, partner sieht ihn noch nicht",
			in: func() Input {
				in := base(Trio, Active)
				in.Now, in.GatewayLastOK, in.Booting = 0, 0, true
				in.Partner = seenPeer("node", Passive, 1, 0)
				in.Partner.SeesSelf = false
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1),
		},
		{
			name: "stimme erhalten, partner abgeschaltet gesehen",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = seenPeer("node", Fenced, 1, 100*sec)
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
	})
}

// TestStepMutual (R8): aktiv bleibt nur bei gegenseitiger Sicht.
func TestStepMutual(t *testing.T) {
	lastMutual := func(want time.Duration) func(t *testing.T, out Output) {
		return func(t *testing.T, out Output) {
			t.Helper()
			if out.LastMutual != want {
				t.Fatalf("LastMutual = %v, erwartet %v", out.LastMutual, want)
			}
		}
	}
	oneSided := func(last time.Duration) func() Input {
		return func() Input {
			in := base(Trio, Active)
			in.Partner = gonePeer("node", Passive, 1, 50*sec)
			in.Witness = seenPeer("witness", Witness, 1, 100*sec)
			in.Witness.SeesSelf = false
			in.LastMutual = last
			return in
		}
	}
	runCases(t, []stepCase{
		{
			name: "trio zeuge gegenseitig",
			in: func() Input {
				in := base(Trio, Active)
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Witness = seenPeer("witness", Witness, 1, 100*sec)
				in.LastMutual = 60 * sec
				return in
			},
			role: Active, term: 1, check: lastMutual(100 * sec),
		},
		{
			name: "trio zeuge einseitig 9 s",
			in:   oneSided(91 * sec),
			role: Active, term: 1, check: lastMutual(91 * sec),
		},
		{
			name: "trio zeuge einseitig 10 s",
			in:   oneSided(90 * sec),
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
		},
		{
			name: "trio partner einseitig 10 s",
			in: func() Input {
				in := base(Trio, Active)
				in.Partner = seenPeer("node", Passive, 1, 100*sec)
				in.Partner.SeesSelf = false
				in.Witness = gonePeer("witness", Witness, 1, 50*sec)
				in.LastMutual = 90 * sec
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
		},
		{
			name: "duo partner einseitig, gateway 10 s weg",
			in: func() Input {
				in := base(Duo, Active)
				in.Gateway, in.GatewayLastOK = false, 90*sec
				in.Partner = seenPeer("node", Passive, 1, 100*sec)
				in.Partner.SeesSelf = false
				in.LastMutual = 90 * sec
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
		},
		{
			name: "stimme erhalten, zeuge sieht uns nicht mehr",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 60*sec)
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor, in.Witness.SeesSelf = "self", false
				in.VotePending, in.VoteAskedAt, in.VoteGranted = 2, 99*sec, 2
				return in
			},
			role: Passive, term: 1,
		},
	})
}

// TestStepVoteRefused (R9): Stimmbitte bleibt 2 × Übernahmezeit ohne Zusage → einmal Alarm.
func TestStepVoteRefused(t *testing.T) {
	const text = "Übernahme verweigert – Zeuge sieht den anderen Server noch; prüfen und ggf. Übernahme erzwingen"
	asking := func(since time.Duration, alarmed bool) func() Input {
		return func() Input {
			in := base(Trio, Passive)
			in.Partner = gonePeer("node", Active, 1, 10*sec)
			in.Witness = seenPeer("witness", Witness, 1, 100*sec)
			in.VotePending, in.VoteAskedAt, in.VoteSince, in.VoteAlarmed = 2, 98*sec, since, alarmed
			return in
		}
	}
	state := func(since time.Duration, alarmed, alarm bool) func(t *testing.T, out Output) {
		return func(t *testing.T, out Output) {
			t.Helper()
			if out.VoteSince != since || out.VoteAlarmed != alarmed {
				t.Fatalf("VoteSince/VoteAlarmed = %v/%v, erwartet %v/%v", out.VoteSince, out.VoteAlarmed, since, alarmed)
			}
			found := false
			for _, e := range out.Events {
				found = found || e.Kind == "alarm" && e.Text == text
			}
			if found != alarm {
				t.Fatalf("Alarm = %v, erwartet %v: %v", found, alarm, out.Events)
			}
		}
	}
	runCases(t, []stepCase{
		{
			name: "erste bitte merkt beginn",
			in: func() Input {
				in := base(Trio, Passive)
				in.Partner = gonePeer("node", Active, 1, 70*sec)
				return in
			},
			role: Passive, term: 1,
			actions: acts(ActRequestVote, 2),
			check:   state(100*sec, false, false),
		},
		{name: "59 s", in: asking(41*sec, false), role: Passive, term: 1, check: state(41*sec, false, false)},
		{name: "60 s", in: asking(40*sec, false), role: Passive, term: 1, check: state(40*sec, true, true)},
		{name: "nur einmal", in: asking(10*sec, true), role: Passive, term: 1, check: state(10*sec, true, false)},
		{
			name: "partner wieder gesehen beendet die phase",
			in: func() Input {
				in := asking(10*sec, true)()
				in.Partner = seenPeer("node", Active, 1, 100*sec)
				return in
			},
			role: Passive, term: 1,
			check: state(0, false, false),
		},
		{
			name: "zusage beendet die phase",
			in: func() Input {
				in := asking(10*sec, true)()
				in.Witness = seenPeer("witness", Witness, 2, 100*sec)
				in.Witness.VotedFor = "self"
				in.VoteGranted = 2
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
			check:   state(0, false, false),
		},
	})
}

// TestStepMaintStand (R10): eine Übernahme verlangt denselben Wartungsstand
// wie zuletzt beim Partner gesehen; Abschalten in Wartung nur bei gleichem Stand.
func TestStepMaintStand(t *testing.T) {
	// b hat während der Trennung „Wartung aus“ bekommen, a war in Wartung (Stand 5).
	bAfterOff := func() Input {
		in := duoTakeover()
		in.Persist.Maint, in.Persist.MaintSeq = false, 6
		in.Partner.Maint, in.Partner.MaintSeq = true, 5
		return in
	}
	runCases(t, []stepCase{
		{
			name: "keine übernahme mit anderem wartungsstand",
			in:   bAfterOff,
			role: Passive, term: 1,
			check: func(t *testing.T, out Output) {
				in := bAfterOff()
				if ok, reason := Allowed(in); ok || reason != "Wartungsstand beim Partner noch nicht angekommen" {
					t.Fatalf("Allowed = %v %q", ok, reason)
				}
			},
		},
		{
			name: "übernahme erzwingen geht trotzdem",
			in: func() Input {
				in := bAfterOff()
				in.Command = CmdForceTakeover
				return in
			},
			role: Active, term: 2,
			actions: acts(ActPromote, 2, ActVIPTake, 2),
		},
		{
			name: "wartung nur hier eingeschaltet: abschalten wie sonst",
			in: func() Input {
				in := base(Trio, Active)
				in.Persist.Maint, in.Persist.MaintSeq = true, 6
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Partner.MaintSeq = 5
				in.Witness = gonePeer("witness", Witness, 1, 50*sec)
				in.LastMutual = 50 * sec
				return in
			},
			role: Fenced, term: 1,
			actions: acts(ActFence, 1, ActVIPRelease, 1),
		},
		{
			name: "gemeinsame wartung: kein abschalten",
			in: func() Input {
				in := base(Trio, Active)
				in.Persist.Maint, in.Persist.MaintSeq = true, 6
				in.Partner = gonePeer("node", Passive, 1, 50*sec)
				in.Partner.Maint, in.Partner.MaintSeq = true, 6
				in.Witness = gonePeer("witness", Witness, 1, 50*sec)
				in.LastMutual = 50 * sec
				return in
			},
			role: Active, term: 1,
		},
	})
}

// observeSettings: Standard mit Beobachtung (automatische Entscheidungen nur gemeldet).
func observeSettings() Settings {
	s := DefaultSettings()
	s.AutoMode = AutoObserve
	return s
}

func hasKind(out Output, k ActionKind) bool {
	for _, a := range out.Actions {
		if a.Kind == k {
			return true
		}
	}
	return false
}

func countEvents(out Output, text string) int {
	n := 0
	for _, e := range out.Events {
		if strings.Contains(e.Text, text) {
			n++
		}
	}
	return n
}

// trioPartnerGone: passiver Knoten, aktiver Partner seit der Übernahmezeit weg.
func trioPartnerGone(set Settings) Input {
	in := base(Trio, Passive)
	in.Settings = set
	in.Partner = gonePeer("node", Active, 1, 100*sec-set.TakeoverTrio)
	in.Witness = seenPeer("witness", Witness, 1, 100*sec)
	in.LastMutual = 100 * sec
	return in
}

func TestObserveNoRoleChange(t *testing.T) {
	in := trioPartnerGone(observeSettings())
	in.VotePending, in.VoteAskedAt = 2, 99*sec
	in.ProbeGranted = 2
	out := Step(in)
	if out.Persist.Role != Passive || out.Persist.Term != 1 || hasKind(out, ActPromote) {
		t.Fatalf("Beobachtung darf nicht übernehmen: %s/%d %v", out.Persist.Role, out.Persist.Term, out.Actions)
	}
	if countEvents(out, "würde jetzt: promote (Beobachtung)") != 1 || out.Would != ActPromote {
		t.Fatalf("Ereignis fehlt: %v, Would %q", out.Events, out.Would)
	}
	in.Would = out.Would
	in.Now += sec
	in.Witness.LastSeen = in.Now
	out = Step(in)
	if countEvents(out, "würde jetzt") != 0 || out.Would != ActPromote {
		t.Fatalf("Ereignis nur einmal: %v", out.Events)
	}
}

func TestObserveProbeVote(t *testing.T) {
	out := Step(trioPartnerGone(observeSettings()))
	if !hasKind(out, ActProbeVote) || hasKind(out, ActRequestVote) {
		t.Fatalf("Probestimme erwartet: %v", out.Actions)
	}
	if out.Persist.Role != Passive || out.Persist.Term != 1 {
		t.Fatalf("Rolle/Amtszeit verändert: %s/%d", out.Persist.Role, out.Persist.Term)
	}
}

func TestObserveDuoNoTakeover(t *testing.T) {
	in := base(Duo, Passive)
	in.Settings = observeSettings()
	in.Partner = gonePeer("node", Active, 1, 100*sec-in.Settings.TakeoverDuo)
	out := Step(in)
	if out.Persist.Role != Passive || out.Persist.Term != 1 || hasKind(out, ActPromote) {
		t.Fatalf("Duo-Beobachtung übernimmt: %s/%d %v", out.Persist.Role, out.Persist.Term, out.Actions)
	}
	if countEvents(out, "würde jetzt: promote (Beobachtung)") != 1 {
		t.Fatalf("Ereignis fehlt: %v", out.Events)
	}
}

func TestObserveActiveNoFence(t *testing.T) {
	in := base(Trio, Active)
	in.Settings = observeSettings()
	in.LastMutual = 100*sec - in.Settings.FenceAfter
	in.Partner = gonePeer("node", Passive, 1, 0)
	in.Witness = gonePeer("witness", Witness, 1, 0)
	out := Step(in)
	if out.Persist.Role != Active || hasKind(out, ActFence) {
		t.Fatalf("Beobachtung darf nicht abschalten: %s %v", out.Persist.Role, out.Actions)
	}
	if countEvents(out, "würde jetzt: fence (Beobachtung)") != 1 {
		t.Fatalf("Ereignis fehlt: %v", out.Events)
	}
}

func TestObserveBootStaysActive(t *testing.T) {
	in := base(Trio, Active)
	in.Settings = observeSettings()
	in.Booting = true
	in.LastMutual = 0
	out := Step(in)
	if out.Persist.Role != Active || hasKind(out, ActFence) {
		t.Fatalf("Start in Beobachtung: %s %v", out.Persist.Role, out.Actions)
	}
	if countEvents(out, "würde jetzt: fence (Beobachtung)") != 1 {
		t.Fatalf("Ereignis fehlt: %v", out.Events)
	}
}

func TestObserveManualReal(t *testing.T) {
	in := base(Trio, Passive)
	in.Settings = observeSettings()
	in.Command = CmdForceTakeover
	out := Step(in)
	if out.Persist.Role != Active || out.Persist.Term != 2 {
		t.Fatalf("erzwungene Übernahme: %s/%d", out.Persist.Role, out.Persist.Term)
	}
	for _, a := range out.Actions {
		if a.Kind == ActPromote && !a.Manual {
			t.Fatalf("promote ohne Manual: %v", out.Actions)
		}
	}
	if !hasKind(out, ActPromote) {
		t.Fatalf("promote fehlt: %v", out.Actions)
	}
}

// swapped: aktiver Knoten hat gerade den Rollentausch zu Amtszeit 2 begonnen.
func swapped(t *testing.T) Input {
	t.Helper()
	in := base(Trio, Active)
	in.Partner = seenPeer("node", Passive, 1, 100*sec)
	in.Witness = seenPeer("witness", Witness, 1, 100*sec)
	in.Command = CmdSwap
	out := Step(in)
	if out.Persist.Role != Passive || out.HandoverPending != 2 || !hasKind(out, ActDemote) {
		t.Fatalf("Tausch nicht begonnen: %s %d %v", out.Persist.Role, out.HandoverPending, out.Actions)
	}
	if hasKind(out, ActHandover) {
		t.Fatalf("handover vor erledigtem demote: %v", out.Actions)
	}
	for _, a := range out.Actions {
		if !a.Manual {
			t.Fatalf("Tausch-Aktion ohne Manual: %v", out.Actions)
		}
	}
	in.Command = CmdNone
	in.Persist = out.Persist
	in.HandoverPending, in.HandoverAt, in.HandoverSentAt = out.HandoverPending, out.HandoverAt, out.HandoverSentAt
	in.HandoverReadyAt = out.HandoverReadyAt
	return in
}

func TestHandoverWaitsForDemote(t *testing.T) {
	in := swapped(t)
	for i := 0; i < 10; i++ {
		in.Now += sec
		in.Partner.LastSeen, in.Witness.LastSeen = in.Now, in.Now
		out := Step(in)
		if hasKind(out, ActHandover) || out.Persist.Role != Passive {
			t.Fatalf("Takt %d: handover vor demote: %v (%s)", i, out.Actions, out.Persist.Role)
		}
		in.HandoverPending, in.HandoverAt, in.HandoverSentAt = out.HandoverPending, out.HandoverAt, out.HandoverSentAt
		in.HandoverReadyAt = out.HandoverReadyAt
	}
	in.Now += sec
	in.Partner.LastSeen, in.Witness.LastSeen = in.Now, in.Now
	in.DemoteDone = 1
	out := Step(in)
	if !hasKind(out, ActHandover) || out.HandoverReadyAt != in.Now {
		t.Fatalf("handover nach demote erwartet: %v, bereit %v", out.Actions, out.HandoverReadyAt)
	}
}

func TestHandoverDemoteFailed(t *testing.T) {
	in := swapped(t)
	in.Now += sec
	in.Partner.LastSeen, in.Witness.LastSeen = in.Now, in.Now
	in.DemoteFailed = 1
	out := Step(in)
	if out.Persist.Role != Active || !hasKind(out, ActUnfence) || hasKind(out, ActHandover) {
		t.Fatalf("Abbruch erwartet: %s %v", out.Persist.Role, out.Actions)
	}
	if countEvents(out, "Rollentausch abgebrochen") != 1 || out.HandoverPending != 0 {
		t.Fatalf("Ereignis/Zustand: %v %d", out.Events, out.HandoverPending)
	}
}

func TestHandoverDemoteTimeout(t *testing.T) {
	in := swapped(t)
	in.Now = in.HandoverAt + in.Settings.TakeoverTrio + 90*sec
	in.Partner.LastSeen, in.Witness.LastSeen = in.Now, in.Now
	out := Step(in)
	if out.Persist.Role != Active || countEvents(out, "Rollentausch abgebrochen") != 1 {
		t.Fatalf("Zeitüberschreitung beim demote: %s %v", out.Persist.Role, out.Events)
	}
}

// swapFreezeIn: aktiver Knoten, Partner gesehen und passiv, Tauschbefehl cmd.
func swapFreezeIn(cmd CommandKind) Input {
	in := base(Trio, Active)
	in.Persist.Term = 3
	in.Partner = seenPeer("node", Passive, 3, 100*sec)
	in.Witness = seenPeer("witness", Witness, 3, 100*sec)
	in.Command = cmd
	return in
}

func actionKinds(out Output) map[ActionKind]bool {
	m := map[ActionKind]bool{}
	for _, a := range out.Actions {
		m[a.Kind] = true
	}
	return m
}

func TestSwapFreezeEmitsDemoteFreeze(t *testing.T) {
	out := Step(swapFreezeIn(CmdSwapFreeze))
	k := actionKinds(out)
	if !k[ActDemoteFreeze] || !k[ActVIPRelease] || k[ActDemote] || k[ActAbandon] {
		t.Fatalf("Aktionen falsch: %+v", out.Actions)
	}
	if out.Persist.Role != Passive || out.HandoverPending != out.Persist.Term+1 {
		t.Fatalf("Rolle %v, HandoverPending %d", out.Persist.Role, out.HandoverPending)
	}
}

func TestSwapAbandonEmitsAbandon(t *testing.T) {
	out := Step(swapFreezeIn(CmdSwapAbandon))
	k := actionKinds(out)
	if !k[ActAbandon] || !k[ActVIPRelease] || k[ActDemote] || k[ActDemoteFreeze] {
		t.Fatalf("Aktionen falsch: %+v", out.Actions)
	}
	if out.Persist.Role != Passive || out.HandoverPending != out.Persist.Term+1 {
		t.Fatalf("Rolle %v, HandoverPending %d", out.Persist.Role, out.HandoverPending)
	}
}
