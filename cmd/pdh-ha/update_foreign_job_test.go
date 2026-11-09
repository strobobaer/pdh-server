package main

import (
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// --- R60: Handweg beim eingefrorenen Stand nennt den echten Ausweg, die Sackgasse eine Warnung ---

var frozenLead = message{Role: "aktiv", Term: 2, PG: "primary", Sync: true, UpLead: "-", Ver: "test", Sees: []string{"a"}}

// frozenRig: passiver Server mit offenem, hier eingefrorenem Auftrag (Datenbank gestoppt, kein
// standby.signal), der aktive Partner führt den Auftrag nicht.
func frozenRig(t *testing.T, marked bool) *strandedRig {
	t.Helper()
	old := upForeignHold
	upForeignHold = 0
	t.Cleanup(func() { upForeignHold = old })
	rg := newStrandedRig(t, frozenLead, marked)
	rg.stage.Store("stopped")
	rg.m.d.lock()
	rg.m.d.up.job = ha.UpdateJob{ID: "alt", Phase: ha.UpLocked, LockedAt: time.Now()}
	rg.m.d.p.Term = 2
	rg.m.d.unlock()
	rg.m.fn.setView("b", seenView(frozenLead))
	return rg
}

func hasWarning(d *daemon, sub string) bool {
	for _, w := range d.statusNow().Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// T8: Alarm, Ergebnis des Abbruchs, Statuswarnung und rejoin nennen den eingefrorenen Stand und
// den Weg heraus (PostgreSQL starten, dann anbinden); nichts wird von selbst angefasst.
func TestAbortFrozenNamesWayOut(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "ohneMarke", true: "mitMarke"}[marked], func(t *testing.T) {
			rg := frozenRig(t, marked)
			m := rg.m
			// R65: Der Alarm wartet auf einen frisch abgefragten Datenbankstand (zwei Abfragen nach
			// Beginn), zwei Takte reichen dafür nicht immer; der Inhalt wird unverändert geprüft.
			alarm := tickUntilForeignAlarm(t, m)
			const start = "sudo pg_ctlcluster 18 main start"
			if !strings.Contains(alarm, "eingefroren") || !strings.Contains(alarm, start) {
				t.Fatalf("Alarm nennt den eingefrorenen Stand nicht: %q", alarm)
			}
			msg, err := m.d.abortUpdateMsg("Befehlszeile")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, "eingefroren") || !strings.Contains(msg, start) {
				t.Fatalf("Ergebnis des Abbruchs: %q", msg)
			}
			for i := 0; i < 4; i++ {
				settle(m)
			}
			rg.shift.Add(int64(2 * alarmAfter)) // die Warnung kommt erst, wenn der Stand länger gestoppt bleibt
			settle(m)
			if dryRuns(rg.r) != 0 {
				t.Fatal("pg_rewind-Probelauf trotz gestoppter Datenbank")
			}
			if !hasWarning(m.d, "eingefroren") || !hasWarning(m.d, start) {
				t.Fatalf("keine dauerhafte Warnung: %q", m.d.statusNow().Warnings)
			}
			code, why := postRejoin(m.d)
			if code == 200 || !strings.Contains(why, start) || strings.Contains(why, "läuft schon automatisch") {
				t.Fatalf("rejoin: %d %q", code, why)
			}
		})
	}
}

// Ohne eingefrorenen Stand (nur lesender Primary) bleibt alles wie bisher: kein Hinweis auf PostgreSQL.
func TestAbortNotFrozenUnchanged(t *testing.T) {
	rg := frozenRig(t, false)
	rg.stage.Store("ro")
	m := rg.m
	m.tick()
	m.tick()
	msg, err := m.d.abortUpdateMsg("Befehlszeile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "eingefroren") {
		t.Fatalf("Hinweis ohne eingefrorenen Stand: %q", msg)
	}
	settle(m)
	if hasWarning(m.d, "eingefroren") {
		t.Fatalf("Warnung ohne eingefrorenen Stand: %q", m.d.statusNow().Warnings)
	}
}
