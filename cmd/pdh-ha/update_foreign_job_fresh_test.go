package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- R65: eingefrorener Stand – Hinweis nur, wenn er stimmt ---

// foreignAlarm: der Alarm zum fremden Auftrag ("" = noch keiner).
func foreignAlarm(m *mdt) string {
	for _, s := range eventTexts(m.d) {
		if strings.Contains(s, "sudo pdh-ha update abort") {
			return s
		}
	}
	return ""
}

// tickUntilForeignAlarm taktet, bis der Alarm zum fremden Auftrag da ist (Abfragen laufen nebenher).
func tickUntilForeignAlarm(t *testing.T, m *mdt) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		m.tick()
		if s := foreignAlarm(m); s != "" {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("kein Alarm zum fremden Auftrag")
	return ""
}

// Ursache des zufälligen Scheiterns von TestAbortFrozenNamesWayOut: Der Alarm kam im ersten Takt mit
// dem Datenbankstand von davor (oder noch keinem), also ohne Hinweis auf den eingefrorenen Stand, und
// wird nur einmal geschrieben. Jetzt wartet er auf einen Stand, der nach Beginn abgefragt wurde.
func TestForeignAlarmWaitsForFreshPG(t *testing.T) {
	rg := frozenRig(t, false)
	m := rg.m
	gate := make(chan struct{})
	rg.r.onFn("pg_lsclusters", "", func(fakeCall) (string, error) {
		<-gate // die erste Abfrage hängt
		return stStoppedLs, nil
	})
	m.tick()
	m.tick()
	if s := foreignAlarm(m); s != "" {
		t.Fatalf("Alarm ohne frischen Datenbankstand: %q", s)
	}
	close(gate)
	alarm := tickUntilForeignAlarm(t, m)
	if !strings.Contains(alarm, "eingefroren") || !strings.Contains(alarm, "sudo pg_ctlcluster 18 main start") {
		t.Fatalf("Alarm nennt den eingefrorenen Stand nicht: %q", alarm)
	}
}

// abort nimmt einen frischen Stand: Der zuletzt abgefragte sagt noch „nur lesend“, die Datenbank ist
// inzwischen gestoppt – das Ergebnis nennt den eingefrorenen Stand.
func TestAbortUsesFreshPG(t *testing.T) {
	rg := frozenRig(t, false)
	rg.stage.Store("ro")
	m := rg.m
	tickUntilForeignAlarm(t, m)
	rg.stage.Store("stopped") // zwischen zwei Abfragen gestoppt
	msg, err := m.d.abortUpdateMsg("Befehlszeile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "eingefroren") {
		t.Fatalf("Ergebnis des Abbruchs mit altem Stand: %q", msg)
	}
}

// U9: Nach abgebrochenem pg_rewind ist der gestoppte Ordner unsicher, kein Rückfallstand: keine
// Warnung „eingefroren“ und kein Rat, ihn zu starten; der Weg ist rejoin (Neukopie).
func TestFrozenHintSilentWhenRewindUnsafe(t *testing.T) {
	rg := newStrandedRig(t, frozenLead, false)
	rg.stage.Store("stopped")
	m := rg.m
	if err := os.WriteFile(rg.a.rewindMarkerPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	settle(m)
	rg.shift.Add(int64(2 * alarmAfter))
	settle(m)
	for _, s := range append(eventTexts(m.d), m.d.statusNow().Warnings...) {
		if strings.Contains(s, "eingefroren") || strings.Contains(s, "pg_ctlcluster 18 main start") {
			t.Fatalf("unsicherer Ordner heißt eingefrorener Rückfallstand: %q", s)
		}
	}
	if !hasWarning(m.d, rewindUnsafeText) {
		t.Fatalf("Warnung zum unsicheren Ordner fehlt: %q", m.d.statusNow().Warnings)
	}
	if code, why := postRejoin(m.d); code != 200 {
		t.Fatalf("rejoin: %d %q", code, why)
	}
}

// U10: fence schreibt die nur lesende Konfiguration auch bei gestoppter Datenbank, damit „startet nur
// lesend“ stimmt; ein gestoppter Standby (standby.signal) bleibt unberührt.
func TestFenceStoppedWritesReadOnly(t *testing.T) {
	for _, standby := range []bool{false, true} {
		r := &fakeRunner{}
		r.on("runuser", "pg_is_in_recovery", "", errors.New("statement timeout"))
		r.on("pg_lsclusters", "", stStoppedLs, nil)
		a, _ := testActor(t, r)
		dd := a.pg.c.DataDir
		os.WriteFile(filepath.Join(dd, "PG_VERSION"), []byte("18\n"), 0o600)
		if standby {
			os.WriteFile(filepath.Join(dd, "standby.signal"), nil, 0o600)
		}
		if err := a.fence(context.Background()); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(a.pg.c.ConfDir, "conf.d", "pdh-ha.conf"))
		ro := strings.Contains(string(b), "default_transaction_read_only = on")
		if ro == standby {
			t.Fatalf("standby=%v: nur lesende Konfiguration geschrieben=%v: %q", standby, ro, b)
		}
		for _, c := range r.calls {
			if strings.Contains(c.String(), " reload") {
				t.Fatalf("reload auf gestoppter Datenbank: %s", c)
			}
		}
	}
}
