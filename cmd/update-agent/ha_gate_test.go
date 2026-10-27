package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Teil 3: Die Sperre ist ohne Testhook scharf.
func TestDRGateEnforced(t *testing.T) {
	if !haGateEnforced {
		t.Fatal("Sperre der Netzlaufwerk-Sicherung muss ab Teil 3 scharf sein")
	}
}

// gateAgent: Agent mit Ziel und eigenem Zwischenspeicher (Temp-Ordner).
func gateAgent(t *testing.T) (*updateAgent, string, string) {
	t.Helper()
	t.Setenv("PDH_DR_STATE_DIR", t.TempDir())
	target, spool := t.TempDir(), t.TempDir()
	old := drSpoolDir
	drSpoolDir = spool
	t.Cleanup(func() { drSpoolDir = old })
	a := &updateAgent{mode: "docker", drSysID: drTestSysID} // Pfade ohne /proc/1/root
	a.dr.Config = drDefaults()
	a.dr.Config.Target = target
	haInstalled(t)
	return a, target, spool
}

// Rolle aktiv, Datenbank aber Standby (z. B. mitten im Tausch): nicht sichern.
func TestDRGateStandbyPG(t *testing.T) {
	a, _, _ := gateAgent(t)
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"standby"}}`)
	if a.drCheckActive(context.Background()) {
		t.Fatal("Standby-Datenbank darf nicht sichern")
	}
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"primary","timeline":1}}`)
	if !a.drCheckActive(context.Background()) {
		t.Fatal("aktiv mit schreibbarer Datenbank muss sichern")
	}
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"unknown"}}`)
	if !a.drCheckActive(context.Background()) {
		t.Fatal("ohne Datenbank-Spiegel (unknown) wie bisher sichern")
	}
	if a.drStatus().LastNode != "a" {
		t.Fatalf("sichernder Server nicht vermerkt: %q", a.drStatus().LastNode)
	}
}

// Passiv: keine neue Sicherung, aber vorhandene WAL-Dateien gehen aufs Laufwerk.
func TestDRSpoolAlwaysMoved(t *testing.T) {
	a, target, spool := gateAgent(t)
	wal := "000000010000000000000007"
	if err := os.WriteFile(filepath.Join(spool, wal), []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"passiv","pg":"standby"}}`)
	a.drTick(context.Background(), time.Now())
	if _, err := os.Stat(filepath.Join(target, "wal", wal+".gz")); err != nil {
		t.Fatal("WAL aus dem Zwischenspeicher nicht verschoben")
	}
	if _, err := os.Stat(filepath.Join(spool, wal)); !os.IsNotExist(err) {
		t.Fatal("Zwischenspeicher nicht geleert")
	}
	if st := a.drStatus(); st.Paused != "passiver Knoten" || st.Running != "" {
		t.Fatalf("passiv: %+v", st)
	}
}

// Nach einer Hochstufung (neue Timeline) steht sofort eine Grundsicherung an.
func TestDRBaseBackupOnTimelineChange(t *testing.T) {
	a, _, _ := gateAgent(t)
	ctx := context.Background()
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"primary","timeline":3}}`)
	a.drCheckActive(ctx)
	a.drCheckActive(ctx)
	if a.dr.BaseNeeded {
		t.Fatal("erste oder gleiche Timeline löst keine Grundsicherung aus")
	}
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"b","role":"aktiv","pg":"primary","timeline":4}}`)
	a.drCheckActive(ctx)
	if !a.dr.BaseNeeded || a.dr.LastTimeline != 4 || a.dr.LastNode != "b" {
		t.Fatalf("neue Timeline: needed=%v tl=%d node=%q", a.dr.BaseNeeded, a.dr.LastTimeline, a.dr.LastNode)
	}
	// Ein passiver Knoten verändert den Vermerk nicht.
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"passiv","pg":"standby","timeline":4}}`)
	a.drCheckActive(ctx)
	if a.dr.LastNode != "b" {
		t.Fatal("passiver Knoten darf nicht als sichernder gelten")
	}
}

// pdh-ha länger als 15 min unerreichbar: zusätzlicher Fehler (Alarm).
func TestDRPauseAlarmAfter15Min(t *testing.T) {
	a, _, _ := gateAgent(t)
	t.Setenv("PDH_HA_SOCKET", filepath.Join(t.TempDir(), "fehlt.sock"))
	a.drCheckActive(context.Background())
	if strings.Contains(a.drStatus().Error, "15 Minuten") {
		t.Fatal("Alarm zu früh")
	}
	a.dr.mu.Lock()
	a.dr.UnreachSince = time.Now().Add(-16 * time.Minute)
	a.dr.mu.Unlock()
	a.drCheckActive(context.Background())
	if !strings.Contains(a.drStatus().Error, "Netzlaufwerk-Sicherung seit über 15 Minuten pausiert – pdh-ha prüfen") {
		t.Fatalf("Alarm fehlt: %q", a.drStatus().Error)
	}
	haFakeSocket(t, `{"ok":true,"configured":true,"self":{"id":"a","role":"aktiv","pg":"primary"}}`)
	a.drCheckActive(context.Background())
	if strings.Contains(a.drStatus().Error, "pausiert") {
		t.Fatalf("nach Rückkehr nicht gelöscht: %q", a.drStatus().Error)
	}
}
