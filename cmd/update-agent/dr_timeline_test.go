package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDRWALTimeline(t *testing.T) {
	for name, want := range map[string]uint32{
		"000000020000000000000005":                 2,
		"000000020000000000000005.gz":              2,
		"00000002.history":                         2,
		"00000002.history.gz":                      2,
		"0000000A0000000000000001.partial.gz":      10,
		"000000010000000000000004.00000028.backup": 1,
		"pdh-restore.sh":                           0,
		"00000002.history.gz.part":                 0,
	} {
		got, ok := drWALTimeline(name)
		if got != want || ok != (want > 0) {
			t.Errorf("%s: %d %v, will %d", name, got, ok, want)
		}
	}
}

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// I1: Nach einem Zurücksetzen (A auf Zeitlinie 1, B war schon auf 2) legt der
// aktive Server die verworfene Zeitlinie auf dem Laufwerk beiseite und stößt
// eine neue Grundsicherung an; die eigene Zeitlinie bleibt unangetastet.
func TestDRQuarantineDiscardedTimeline(t *testing.T) {
	a, target, spool := gateAgent(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"primary","timeline":1}}`)
	a.drTLI = func(context.Context) uint32 { return 1 }
	a.drPartner = func(context.Context) bool { return true }
	wal := filepath.Join(target, "wal")
	writeFiles(t, wal, "000000010000000000000004.gz", "00000002.history.gz", "000000020000000000000004.gz")
	writeFiles(t, spool, "000000020000000000000005") // noch nicht verschickt
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	a.drTick(context.Background(), now)
	q := filepath.Join(target, "wal-quarantaene", "20261030T030000Z")
	for _, n := range []string{"00000002.history.gz", "000000020000000000000004.gz"} {
		if _, err := os.Stat(filepath.Join(q, n)); err != nil {
			t.Errorf("%s nicht beiseitegelegt", n)
		}
		if _, err := os.Stat(filepath.Join(wal, n)); err == nil {
			t.Errorf("%s liegt noch im Archiv", n)
		}
	}
	if _, err := os.Stat(filepath.Join(wal, "000000010000000000000004.gz")); err != nil {
		t.Error("eigene Zeitlinie angefasst")
	}
	if _, err := os.Stat(filepath.Join(spool, "quarantaene", "000000020000000000000005")); err != nil {
		t.Error("Zwischenspeicher: verworfene Zeitlinie nicht beiseitegelegt")
	}
	if _, err := os.Stat(filepath.Join(wal, "000000020000000000000005.gz")); err == nil {
		t.Error("verworfene Zeitlinie aus dem Zwischenspeicher aufs Laufwerk gebracht")
	}
	if !a.dr.BaseNeeded {
		t.Error("keine neue Grundsicherung angestoßen")
	}
}

// Ohne passiv sichtbaren Partner (etwa mitten in einer Übernahme) und ohne
// bekannte Zeitlinie bleibt das Laufwerk unangetastet.
func TestDRQuarantineNeedsPassivePartner(t *testing.T) {
	for _, c := range []struct {
		name    string
		tli     uint32
		partner bool
	}{{"Partner nicht passiv", 1, false}, {"Zeitlinie unbekannt", 0, true}} {
		t.Run(c.name, func(t *testing.T) {
			a, target, _ := gateAgent(t)
			haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"primary","timeline":1}}`)
			a.drTLI = func(context.Context) uint32 { return c.tli }
			a.drPartner = func(context.Context) bool { return c.partner }
			wal := filepath.Join(target, "wal")
			writeFiles(t, wal, "00000002.history.gz")
			a.drTick(context.Background(), time.Now())
			if _, err := os.Stat(filepath.Join(wal, "00000002.history.gz")); err != nil {
				t.Fatal("Archiv angefasst")
			}
			if a.dr.BaseNeeded {
				t.Fatal("Grundsicherung ohne Grund")
			}
		})
	}
}

// Passiver Server (nach dem Zurücksetzen Standby auf Zeitlinie 1): Dateien
// seiner verworfenen Zeitlinie 2 gehen nicht aufs Laufwerk.
func TestDRPassiveSpoolQuarantine(t *testing.T) {
	a, target, spool := gateAgent(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv","pg":"standby"}}`)
	a.drTLI = func(context.Context) uint32 { return 1 }
	writeFiles(t, spool, "00000002.history", "000000010000000000000009")
	a.drTick(context.Background(), time.Now())
	if _, err := os.Stat(filepath.Join(target, "wal", "000000010000000000000009.gz")); err != nil {
		t.Error("eigene Zeitlinie nicht verschickt")
	}
	if _, err := os.Stat(filepath.Join(target, "wal", "00000002.history.gz")); err == nil {
		t.Error("verworfene Zeitlinie verschickt")
	}
	if _, err := os.Stat(filepath.Join(spool, "quarantaene", "00000002.history")); err != nil {
		t.Error("nicht beiseitegelegt")
	}
}
