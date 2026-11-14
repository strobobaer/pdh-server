// Package ruesten verwaltet die Vorgangsart „Rüstzeit“ (Datensatztyp setup):
// Rüstzeiten je Anlage mit Grund, Beginn, Ende und überwachter Höchstdauer.
// Rüsttasten (Katalog) werden Anlagen zugeordnet und über die Anlagen-
// hierarchie vererbt – wie die Meldetexte im Easy-Modus.
package ruesten

import (
	"errors"
	"fmt"
	"time"
)

// Status einer Rüstzeit und Herkunft des Eintrags.
const (
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFault   = "fault" // Höchstdauer überschritten, Störmeldung entstanden

	OriginEasy   = "easy"
	OriginManual = "manual"
)

// Schlüssel und Vorgaben der globalen Werte (Tabelle app_settings).
const (
	KeyMaxMinutes   = "setup_max_minutes"
	KeyGraceMinutes = "setup_grace_minutes"

	DefaultMaxMinutes   = 120
	DefaultGraceMinutes = 15
)

// globalSource: Quelle der Höchstdauer, wenn kein Anlagenwert gilt.
const globalSource = "globaler Stamm"

// tooSoon: dieselbe Taste zweimal innerhalb dieser Zeit zählt als Doppelklick.
const tooSoon = 10 * time.Second

var (
	ErrNotFound       = errors.New("rüstzeit nicht gefunden")
	ErrTooSoon        = errors.New("dieselbe Rüsttaste wurde eben erst gedrückt")
	ErrOverGlobalMax  = errors.New("die Höchstdauer der Anlage darf die globale Höchstdauer nicht überschreiten")
	ErrNotRunning     = errors.New("die Rüstzeit läuft nicht (mehr)")
	ErrAlreadyRunning = errors.New("an dieser Anlage läuft bereits eine Rüstzeit")
	ErrReadOnly       = errors.New("Beginn und Ende einer Rüstzeit mit Störmeldung sind nicht änderbar")
	ErrReasonInUse    = errors.New("der Grund wird noch verwendet")

	// ErrNoFaultCreator: der Überwachung fehlt der Dienst, der Störmeldungen anlegt.
	ErrNoFaultCreator = errors.New("kein Dienst für Störmeldungen angeschlossen")
)

// Reason ist ein Grund aus dem Katalog (z. B. Produktumstellung).
type Reason struct {
	ID, Key, Name, Group string
	Sort                 int
	Active               bool
}

// Button ist eine Rüsttaste aus dem Katalog.
type Button struct {
	ID, Name, ReasonID, ReasonName string
	Sort                           int
	Active                         bool
	InheritedFrom                  string // nur bei InfraButtons: Name der übergeordneten Anlage
}

// Entry ist eine Rüstzeit. MaxMinutes, GraceMinutes, Verantwortliche und
// ButtonName werden beim Anlegen festgehalten.
type Entry struct {
	ID                                         string
	Number                                     int
	InfraID, InfraName                         string
	StartedAt                                  time.Time
	EndedAt                                    *time.Time
	ReasonID, ReasonName, ButtonID, ButtonName string
	Origin, Status, Note                       string
	MaxMinutes, GraceMinutes                   int
	FaultID, ResponsibleID, ShiftLeaderID      *string
	ResponsibleName, ShiftLeaderName           string
	CreatedBy                                  string
	CreatedAt                                  time.Time
}

// ManualInput ist die Eingabe beim manuellen Anlegen und Korrigieren.
type ManualInput struct {
	InfraID, ReasonID, Note string
	StartedAt               time.Time
	EndedAt                 *time.Time
}

// Filter für List. From ist inklusive, To exklusiv (jeweils auf den Beginn).
type Filter struct {
	InfraID          string // mit Unteranlagen
	ReasonID, Status string
	From, To         *time.Time
	Limit, Offset    int
}

// Number formatiert die laufende Nummer, z. B. „RZ-0001“.
func Number(n int) string { return fmt.Sprintf("RZ-%04d", n) }
