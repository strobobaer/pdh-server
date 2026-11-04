package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdh/internal/ha"
)

// offRig: Spiegelung aus, Rolle start; die Rolle ist gemerkt.
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
	waitFor(t, 5*time.Second, "Rolle gemerkt", func() bool {
		rt := m.rt()
		if rt == nil {
			return false
		}
		ib := rt.inbox.Load()
		return ib != nil && ib.lastRole() == start
	})
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

// R44 (B3, B8): Einfacher Tausch bei ausgeschalteter Spiegelung: Der Server war vorher passiv
// und hat vielleicht nicht alles. Das Einschalten löscht nichts am Partner und meldet es.
func TestMirrorSwapWhileOffSimpleKeepsProtection(t *testing.T) {
	m := offRig(t, ha.Passive)
	m.setRole(ha.Active)
	if del, nodel := m.mirrorAfterOn(); del != 0 || nodel != 1 {
		t.Fatalf("uploads: mit --delete %d, ohne %d", del, nodel)
	}
	waitFor(t, 5*time.Second, "Alarm", func() bool { return hasEvent(m.d, "Quelle uploads: Dieser Server wurde aktiv") })
}

// R44: Hin und zurück bei ausgeschalteter Spiegelung (F2): Der Server war vorher aktiv, kein
// Wechsel: normaler Erstabgleich, kein Fehlalarm.
func TestMirrorSwapWhileOffRoundTripNoAlarm(t *testing.T) {
	m := offRig(t, ha.Active)
	m.setRole(ha.Passive)
	m.setRole(ha.Active)
	if del, nodel := m.mirrorAfterOn(); del != 1 || nodel != 0 {
		t.Fatalf("uploads: mit --delete %d, ohne %d", del, nodel)
	}
	time.Sleep(100 * time.Millisecond)
	if hasEvent(m.d, "wurde aktiv") {
		t.Fatalf("Fehlalarm: %v", eventTexts(m.d))
	}
}
