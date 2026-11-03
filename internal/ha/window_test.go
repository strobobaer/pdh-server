package ha

import (
	"testing"
	"time"
	_ "time/tzdata" // Europe/Berlin auch ohne Zeitzonen-Datenbank des Systems
)

func TestWindowValidate(t *testing.T) {
	if err := DefaultWindow().Validate(); err != nil {
		t.Fatalf("Standard ungültig: %v", err)
	}
	bad := func(name string, f func(*Window)) {
		t.Helper()
		w := DefaultWindow()
		f(&w)
		if w.Validate() == nil {
			t.Errorf("%s: Fehler erwartet", name)
		}
	}
	good := func(name string, f func(*Window)) {
		t.Helper()
		w := DefaultWindow()
		f(&w)
		if err := w.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad("Countdown 0", func(w *Window) { w.CountdownMin = 0 })
	bad("Countdown 16", func(w *Window) { w.CountdownMin = 16 })
	good("Countdown 1", func(w *Window) { w.CountdownMin = 1 })
	good("Countdown 15", func(w *Window) { w.CountdownMin = 15 })
	bad("Sperre 9", func(w *Window) { w.LockMaxMin = 9 })
	bad("Sperre 121", func(w *Window) { w.LockMaxMin = 121 })
	good("Sperre 10", func(w *Window) { w.LockMaxMin = 10 })
	good("Sperre 120", func(w *Window) { w.LockMaxMin = 120 })
	bad("Uhrzeit 25:00", func(w *Window) { w.Clock = "25:00" })
	bad("Uhrzeit 2:00", func(w *Window) { w.Clock = "2:00" })
	bad("Uhrzeit leer", func(w *Window) { w.Latest = "" })
	bad("Uhrzeit 05:60", func(w *Window) { w.Latest = "05:60" })
	bad("Modus", func(w *Window) { w.Mode = "stündlich" })
	bad("Wochentag 7", func(w *Window) { w.Weekday = 7 })
	bad("Wochentag -1", func(w *Window) { w.Weekday = -1 })
	bad("Beginn", func(w *Window) { w.Begin = "mittendrin" })
	bad("Art swap", func(w *Window) { w.Kind = UpdateSwap })
	good("Art sequential", func(w *Window) { w.Kind = UpdateSequential })
	good("wöchentlich", func(w *Window) { w.Mode = "weekly"; w.Weekday = 6 })
}

func TestWindowDefaults(t *testing.T) {
	w := DefaultWindow()
	if w.Mode != "off" || w.Begin != "after_files" || w.Clock != "02:00" || w.Latest != "05:00" || !w.Install ||
		w.Kind != UpdateRolling || w.Swap || w.CountdownMin != 2 || w.LockMaxMin != 30 {
		t.Fatalf("Standard weicht ab: %+v", w)
	}
}

func TestWindowDue(t *testing.T) {
	loc := time.UTC
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) } // 2026-11-02 ist ein Montag
	daily := DefaultWindow()
	daily.Mode = "daily"
	var never time.Time

	// vor dem Abgleich: sobald die Grundsicherung fertig ist und der Abgleich noch nicht lief
	before := daily
	before.Begin = "before_files"
	if due, _ := WindowDue(before, WindowState{HasBackup: true, BaseDoneToday: true}, never, at(2, 2, 30)); !due {
		t.Error("vor dem Abgleich: fällig erwartet")
	}
	if due, _ := WindowDue(before, WindowState{HasBackup: true}, never, at(2, 2, 30)); due {
		t.Error("ohne Grundsicherung nicht fällig")
	}
	if due, _ := WindowDue(before, WindowState{HasBackup: true, BaseDoneToday: true, FilesStartedAfterBase: true}, never, at(2, 3, 0)); due {
		t.Error("Abgleich schon gestartet: „vor“ nicht mehr möglich")
	}
	// nach dem Abgleich: erst wenn er fertig ist
	after := daily
	if due, _ := WindowDue(after, WindowState{HasBackup: true, BaseDoneToday: true, FilesStartedAfterBase: true}, never, at(2, 3, 0)); due {
		t.Error("Abgleich läuft noch: nicht fällig")
	}
	if due, skip := WindowDue(after, WindowState{HasBackup: true, BaseDoneToday: true, FilesStartedAfterBase: true, FilesDoneAfterBase: true}, never, at(2, 3, 20)); !due || skip != "" {
		t.Errorf("nach dem Abgleich: fällig erwartet (%v %q)", due, skip)
	}
	// spätestens überschritten → ausgelassen mit Grund
	due, skip := WindowDue(after, WindowState{HasBackup: true, BaseDoneToday: true, FilesStartedAfterBase: true, FilesDoneAfterBase: true}, never, at(2, 5, 40))
	if due || skip == "" {
		t.Errorf("zu spät: ausgelassen erwartet (%v %q)", due, skip)
	}
	due, skip = WindowDue(after, WindowState{HasBackup: true}, never, at(2, 5, 1))
	if due || skip == "" {
		t.Errorf("Grundsicherung fehlt bis spätestens: Grund erwartet (%v %q)", due, skip)
	}
	// vor „spätestens“ und nicht fällig: still warten
	if due, skip := WindowDue(after, WindowState{HasBackup: true}, never, at(2, 4, 0)); due || skip != "" {
		t.Errorf("noch Zeit: kein Grund erwartet (%v %q)", due, skip)
	}
	// heute schon gelaufen
	if due, skip := WindowDue(after, WindowState{HasBackup: true, BaseDoneToday: true, FilesDoneAfterBase: true}, at(2, 2, 45), at(2, 3, 20)); due || skip != "" {
		t.Errorf("heute gelaufen: nichts erwartet (%v %q)", due, skip)
	}
	// gestern gelaufen zählt nicht
	if due, _ := WindowDue(after, WindowState{HasBackup: true, BaseDoneToday: true, FilesDoneAfterBase: true}, at(1, 2, 45), at(2, 3, 20)); !due {
		t.Error("gestern gelaufen: heute wieder fällig")
	}
	// wöchentlich: anderer Tag
	weekly := after
	weekly.Mode = "weekly"
	weekly.Weekday = 3 // Mittwoch
	st := WindowState{HasBackup: true, BaseDoneToday: true, FilesDoneAfterBase: true}
	if due, skip := WindowDue(weekly, st, never, at(2, 3, 20)); due || skip != "" { // Montag
		t.Errorf("falscher Wochentag: nichts erwartet (%v %q)", due, skip)
	}
	if due, _ := WindowDue(weekly, st, never, at(4, 3, 20)); !due { // Mittwoch
		t.Error("richtiger Wochentag: fällig erwartet")
	}
	// aus
	if due, skip := WindowDue(DefaultWindow(), st, never, at(2, 3, 20)); due || skip != "" {
		t.Errorf("aus: nichts erwartet (%v %q)", due, skip)
	}
	// ohne Sicherung: ab Clock
	nb := daily
	if due, _ := WindowDue(nb, WindowState{}, never, at(2, 1, 59)); due {
		t.Error("vor 02:00 nicht fällig")
	}
	if due, _ := WindowDue(nb, WindowState{}, never, at(2, 2, 0)); !due {
		t.Error("ab 02:00 fällig")
	}
	if due, skip := WindowDue(nb, WindowState{}, never, at(2, 5, 30)); due || skip == "" {
		t.Errorf("ohne Sicherung nach spätestens: ausgelassen erwartet (%v %q)", due, skip)
	}
}

func TestNextWindowDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := DefaultWindow()
	w.Mode = "daily"
	// Nacht zum 25.10.2026: Umstellung 03:00 → 02:00
	now := time.Date(2026, 10, 25, 0, 30, 0, 0, loc)
	n1 := NextWindow(w, 1, false, now)
	if n1.Day() != 25 || n1.Hour() != 2 || n1.Minute() != 0 {
		t.Fatalf("erster Start: %v", n1)
	}
	n2 := NextWindow(w, 1, false, n1.Add(30*time.Minute))
	if n2.Day() != 26 || n2.Hour() != 2 || n2.Minute() != 0 {
		t.Fatalf("nächster Start nach Umstellung: %v", n2)
	}
	// 02:00 gibt es in dieser Nacht zweimal; Go wählt eine davon (Abstand 24 oder 25 h) –
	// wichtig ist: jeder Tag genau ein Start, kein doppelter, keiner übersprungen.
	if d := n2.Sub(n1); d != 24*time.Hour && d != 25*time.Hour {
		t.Fatalf("Abstand über die Umstellung: %v", d)
	}
	// mit Grundsicherung: Stunde der Grundsicherung
	if n := NextWindow(w, 1, true, now); n.Hour() != 1 || n.Day() != 25 {
		t.Fatalf("mit Sicherung: %v", n)
	}
	// Frühjahr: 02:30 gibt es am 29.03.2026 nicht – kein Absturz, Tag stimmt
	w.Clock = "02:30"
	if n := NextWindow(w, 1, false, time.Date(2026, 3, 29, 0, 10, 0, 0, loc)); n.Day() != 29 || n.Before(time.Date(2026, 3, 29, 0, 10, 0, 0, loc)) {
		t.Fatalf("Frühjahr: %v", n)
	}
	// wöchentlich: nächster Mittwoch (28.10.2026), aus: Null
	w.Mode, w.Weekday, w.Clock = "weekly", 3, "02:00"
	if n := NextWindow(w, 1, false, now); n.Day() != 28 || n.Month() != 10 || n.Weekday() != time.Wednesday {
		t.Fatalf("wöchentlich: %v", n)
	}
	w.Mode = "off"
	if !NextWindow(w, 1, false, now).IsZero() {
		t.Fatal("aus: Null erwartet")
	}
}
