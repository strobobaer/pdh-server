package ha

import (
	"errors"
	"fmt"
	"time"
)

// Wartungsfenster (Spec 4): Einstellungen, Fälligkeit und nächster Start.
// Reine Rechnung ohne Uhr: alle Uhrzeiten und der Wochentag gelten in der
// Zeitzone von now (der Daemon übergibt die Ortszeit des Servers).

// Window sind die Einstellungen des Wartungsfensters.
type Window struct {
	Mode         string     `json:"mode"`          // "off" | "daily" | "weekly"
	Weekday      int        `json:"weekday"`       // 0=So … 6=Sa
	Begin        string     `json:"begin"`         // "before_files" | "after_files"
	Clock        string     `json:"clock"`         // "HH:MM", nur ohne Grundsicherung
	Latest       string     `json:"latest"`        // "HH:MM": spätestens beginnen bis
	Install      bool       `json:"install"`       // neue Version einspielen
	Kind         UpdateKind `json:"kind"`          // "rolling" | "sequential"
	Swap         bool       `json:"swap"`          // Rollentausch im Fenster
	CountdownMin int        `json:"countdown_min"` // 1–15
	LockMaxMin   int        `json:"lock_max_min"`  // 10–120
}

// Werte von Window.Mode und Window.Begin.
const (
	WindowOff    = "off"
	WindowDaily  = "daily"
	WindowWeekly = "weekly"

	WindowBeforeFiles = "before_files"
	WindowAfterFiles  = "after_files"
)

// DefaultWindow liefert die Standardwerte (Spec 4): Fenster aus.
func DefaultWindow() Window {
	return Window{
		Mode: WindowOff, Weekday: 0, Begin: WindowAfterFiles, Clock: "02:00", Latest: "05:00",
		Install: true, Kind: UpdateRolling, Swap: false, CountdownMin: 2, LockMaxMin: 30,
	}
}

// clockMinutes liest „HH:MM“ (zweistellig) als Minuten seit Mitternacht.
func clockMinutes(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Validate prüft die Einstellungen; Fehlertexte für Anwender.
func (w Window) Validate() error {
	switch w.Mode {
	case WindowOff, WindowDaily, WindowWeekly:
	default:
		return errors.New("Wartungsfenster muss „aus“, „täglich“ oder „wöchentlich“ sein")
	}
	if w.Weekday < 0 || w.Weekday > 6 {
		return errors.New("Wochentag muss zwischen 0 (Sonntag) und 6 (Samstag) liegen")
	}
	if w.Begin != WindowBeforeFiles && w.Begin != WindowAfterFiles {
		return errors.New("Beginn muss „vor“ oder „nach“ dem Dateiabgleich sein")
	}
	if _, ok := clockMinutes(w.Clock); !ok {
		return errors.New("Uhrzeit muss im Format HH:MM angegeben werden")
	}
	if _, ok := clockMinutes(w.Latest); !ok {
		return errors.New("„Spätestens beginnen bis“ muss im Format HH:MM angegeben werden")
	}
	if w.Kind != UpdateRolling && w.Kind != UpdateSequential {
		return errors.New("Update-Art muss „ohne Unterbrechung“ oder „alle nacheinander“ sein")
	}
	if w.CountdownMin < 1 || w.CountdownMin > 15 {
		return errors.New("Countdown muss zwischen 1 und 15 min liegen")
	}
	if w.LockMaxMin < 10 || w.LockMaxMin > 120 {
		return errors.New("Höchstdauer der Sperre muss zwischen 10 und 120 min liegen")
	}
	return nil
}

// WindowState: Stand der Sicherungen des Tages (aus dem Update-Agenten/PDH).
type WindowState struct {
	HasBackup             bool // Ausfallschutz-Sicherung eingerichtet
	BaseDoneToday         bool // Grundsicherung des Tages erfolgreich fertig
	FilesStartedAfterBase bool // Dateiabgleich nach der Grundsicherung hat begonnen
	FilesDoneAfterBase    bool // … und ist erfolgreich beendet
}

// minutesOf: Minuten seit Mitternacht in der Ortszeit von t.
func minutesOf(t time.Time) int { return t.Hour()*60 + t.Minute() }

// WindowDue sagt, ob das Fenster jetzt beginnen soll. skipReason ist
// gesetzt, wenn „spätestens bis“ überschritten ist, ohne dass gestartet
// wurde (das Fenster wird dann ausgelassen und gemeldet). lastRun ist der
// Beginn des letzten Fensters (Null = nie).
func WindowDue(w Window, st WindowState, lastRun, now time.Time) (due bool, skipReason string) {
	if w.Validate() != nil || w.Mode == WindowOff {
		return false, ""
	}
	if w.Mode == WindowWeekly && int(now.Weekday()) != w.Weekday {
		return false, ""
	}
	if !lastRun.IsZero() {
		ly, lm, ld := lastRun.In(now.Location()).Date()
		ny, nm, nd := now.Date()
		if ly == ny && lm == nm && ld == nd {
			return false, "" // heute schon gelaufen
		}
	}
	latest, _ := clockMinutes(w.Latest)
	clock, _ := clockMinutes(w.Clock)
	var ready bool
	var what string
	switch {
	case !st.HasBackup:
		ready, what = minutesOf(now) >= clock, "Uhrzeit nicht erreicht"
	case !st.BaseDoneToday:
		ready, what = false, "Grundsicherung nicht fertig"
	case w.Begin == WindowBeforeFiles:
		ready, what = !st.FilesStartedAfterBase, "Dateiabgleich schon gestartet"
	default:
		ready, what = st.FilesDoneAfterBase, "Dateiabgleich nicht fertig"
	}
	if minutesOf(now) > latest {
		reason := what
		if ready {
			reason = "Bedingungen erst nach der Frist erfüllt"
		}
		return false, fmt.Sprintf("Wartungsfenster ausgelassen: %s bis %s", reason, w.Latest)
	}
	return ready, ""
}

// NextWindow liefert den nächsten möglichen Beginn nach now (Null bei „aus“):
// mit Sicherung zur Stunde der Grundsicherung (baseHour, frühestens – der
// tatsächliche Start folgt WindowDue), sonst zur Uhrzeit Clock.
// Nicht existierende Ortszeiten (Umstellung auf Sommerzeit) rechnet Go
// auf die nächste gültige um.
func NextWindow(w Window, baseHour int, hasBackup bool, now time.Time) time.Time {
	if w.Validate() != nil || w.Mode == WindowOff {
		return time.Time{}
	}
	h, m := baseHour, 0
	if !hasBackup {
		c, _ := clockMinutes(w.Clock)
		h, m = c/60, c%60
	}
	y, mo, d := now.Date()
	for i := 0; i <= 8; i++ {
		c := time.Date(y, mo, d+i, h, m, 0, 0, now.Location())
		if w.Mode == WindowWeekly && int(c.Weekday()) != w.Weekday {
			continue
		}
		if c.After(now) {
			return c
		}
	}
	return time.Time{}
}
