package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdh/internal/ha"
)

// R47: Die Rollenvorgeschichte entscheidet nicht über --delete. Tausch bei ausgeschalteter
// Spiegelung, hin und zurück oder einfach: Der Empfänger hat keinen Beleg, das Einschalten
// löscht nichts am Partner, und der „Wechsel“ gibt keinen eigenen Alarm.
func TestMirrorSwapWhileOffNoDeleteNoPromotion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start ha.Role
		steps []ha.Role
	}{
		{"einfacher Tausch", ha.Passive, []ha.Role{ha.Active}},
		{"hin und zurück", ha.Active, []ha.Role{ha.Passive, ha.Active}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := offRig(t, tc.start)
			for _, r := range tc.steps {
				m.setRole(r)
			}
			if del, nodel := m.mirrorAfterOn(); del != 0 || nodel != 1 {
				t.Fatalf("uploads: mit --delete %d, ohne %d", del, nodel)
			}
			time.Sleep(100 * time.Millisecond)
			if hasEvent(m.d, "wurde aktiv") {
				t.Fatalf("Alarm zum Wechsel: %v", eventTexts(m.d))
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(m.w.statePath), mirrorInboxFile)); statErr == nil {
				rt := m.rt()
				if r := rt.inbox.Load().lastRole(); r == tc.start && tc.start == ha.Passive {
					t.Fatalf("Rolle bei ausgeschalteter Spiegelung gemerkt: %s", r)
				}
			}
		})
	}
}

// offRig: Spiegelung aus, Rolle start.
func offRig(t *testing.T, start ha.Role) *mdt {
	t.Helper()
	other := "passiv"
	if start == ha.Passive {
		other = "aktiv"
	}
	m := newMDT(t, start, false, message{Role: other})
	up := filepath.Join(m.dir, "repo", "uploads")
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(up, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.tick()
	m.ready()
	return m
}

func (m *mdt) setRole(r ha.Role) {
	m.t.Helper()
	other := "passiv"
	if r == ha.Passive {
		other = "aktiv"
	}
	m.fn.setView("b", seenView(message{Role: other, Term: 1, Ver: "test", Sees: []string{"a"}}))
	m.d.lock()
	m.d.p.Role = r
	m.d.unlock()
	m.tick()
	time.Sleep(50 * time.Millisecond) // der Abgleich der Spiegelung läuft eine Runde
}

func (m *mdt) mirrorAfterOn() (del, nodel int) {
	m.t.Helper()
	m.setOn(true)
	m.tick()
	waitFor(m.t, 5*time.Second, "erster Lauf", func() bool { d, n := uploadsCalls(m.rs); return d+n > 0 })
	return uploadsCalls(m.rs)
}

// R47: Ausschalten macht Belege und Bestätigung ungültig.
func TestMirrorOffDropsProofs(t *testing.T) {
	m, _, _, rt, _ := proofRig(t)
	ib := rt.inbox.Load()
	ib.noteSource("uploads")
	m.setOn(false)
	m.tick()
	waitFor(t, 5*time.Second, "Beleg weg", func() bool { ok, _ := ib.proofOf("uploads"); return !ok })
}
