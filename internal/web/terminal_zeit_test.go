package web

import (
	"strings"
	"testing"
	"time"
)

func TestZeitAttendanceStateMachine(t *testing.T) {
	cases := []struct {
		from attState
		kind string
		to   attState
		bad  string // Teil der Meldung, "" = erlaubt
	}{
		{attAbwesend, attKommen, attAnwesend, ""},
		{attAnwesend, attPauseBegin, attPause, ""},
		{attPause, attPauseEnde, attAnwesend, ""},
		{attAnwesend, attGehen, attAbwesend, ""},
		{attAbwesend, attGehen, attAbwesend, "Gehen ohne Kommen"},
		{attAnwesend, attKommen, attAnwesend, "Kommen ist schon gebucht"},
		{attPause, attKommen, attPause, "Kommen ist schon gebucht"},
		{attAbwesend, attPauseBegin, attAbwesend, "nur während der Anwesenheit"},
		{attAbwesend, attPauseEnde, attAbwesend, "keine Pause"},
		{attAnwesend, attPauseEnde, attAnwesend, "keine Pause"},
		{attPause, attPauseBegin, attPause, "Pause läuft schon"},
		{attPause, attGehen, attPause, "nach der Pause"},
	}
	for _, c := range cases {
		to, msg := attNext(c.from, c.kind)
		if c.bad == "" && msg != "" {
			t.Errorf("%s nach %s: unerwartet abgewiesen: %s", c.kind, c.from, msg)
		}
		if c.bad != "" && !strings.Contains(msg, c.bad) {
			t.Errorf("%s nach %s: Meldung %q enthält nicht %q", c.kind, c.from, msg, c.bad)
		}
		if to != c.to {
			t.Errorf("%s nach %s: Zustand %s, erwartet %s", c.kind, c.from, to, c.to)
		}
	}
	if _, msg := attNext(attAbwesend, "quatsch"); msg == "" {
		t.Error("unbekannte Buchung nicht abgewiesen")
	}
}

func TestZeitStateStale(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	if attStateOf(attKommen, now.Add(-2*time.Hour), now) != attAnwesend {
		t.Error("frisches Kommen")
	}
	if attStateOf(attKommen, now.Add(-30*time.Hour), now) != attAbwesend {
		t.Error("vergessenes Kommen gilt als abwesend")
	}
	if attStateOf(attPauseBegin, now.Add(-time.Hour), now) != attPause || attStateOf(attGehen, now, now) != attAbwesend {
		t.Error("Pause/Gehen")
	}
}

func TestAttendanceValidateAndBlocks(t *testing.T) {
	d := func(h, m int) time.Time { return time.Date(2026, 3, 2, h, m, 0, 0, time.UTC) }
	good := []attEvent{{Kind: attKommen, At: d(7, 0)}, {Kind: attPauseBegin, At: d(9, 0)}, {Kind: attPauseEnde, At: d(9, 15)}, {Kind: attGehen, At: d(15, 30)}}
	if msg := attValidate(attAbwesend, good); msg != "" {
		t.Errorf("gültiger Ablauf abgewiesen: %s", msg)
	}
	if msg := attValidate(attAbwesend, []attEvent{{Kind: attGehen, At: d(7, 0)}}); !strings.Contains(msg, "Gehen ohne Kommen") {
		t.Errorf("Meldung: %q", msg)
	}
	bl := attBlocks(good)
	if len(bl) != 1 || bl[0].Open || bl[0].PauseMin != 15 || bl[0].Net() != 8*60+30-15 {
		t.Errorf("Blöcke: %+v", bl)
	}
	open := attBlocks([]attEvent{{Kind: attKommen, At: d(7, 0)}})
	if len(open) != 1 || !open[0].Open || open[0].Net() != 0 {
		t.Errorf("offener Block: %+v", open)
	}
}

func TestZeitCSVCellNeutralizesFormulas(t *testing.T) {
	if csvCell("=1+1") != "'=1+1" || csvCell("Anna") != "Anna" || csvCell("@x") != "'@x" {
		t.Error("csvCell")
	}
}

func TestZeitFunctionDefaultOff(t *testing.T) {
	d, ok := terminalFnDefByKey("zeiterfassung")
	if !ok || d.Def {
		t.Fatalf("Schalter zeiterfassung: %+v ok=%v", d, ok)
	}
	if defaultTerminalFns()["zeiterfassung"].Enabled {
		t.Error("Grundwert muss aus sein")
	}
}
