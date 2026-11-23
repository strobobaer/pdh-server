// Package calc hält die berechneten Werte (virtuelle Messwerte): ein
// berechneter Wert ist eine Zuordnung an der eingebauten Import-Verbindung
// „Berechnete Werte“, gerechnet aus anderen Zuordnungen nach einer Formel.
// Formel und Eingangswerte liegen in Fassungen mit „gültig ab“.
package calc

import (
	"errors"
	"strings"
	"time"

	"pdh/internal/core/formula"
)

// Kind ist die Art der eingebauten Verbindung (import_export_connections.kind).
const Kind = "calc"

// MaxInputs ist die größte Zahl Eingangswerte je Formel.
const MaxInputs = 50

// Status einer Fassung.
const (
	StatusPlanned    = "geplant"
	StatusCurrent    = "gilt"
	StatusSuperseded = "abgelöst"
)

// InputRef: Kurzname in der Formel → Zuordnung.
type InputRef struct {
	Short     string `json:"short"`
	MappingID string `json:"mapping_id"`
}

// SaveInput: Angaben zum Anlegen oder Ändern eines berechneten Werts.
type SaveInput struct {
	ID            string // Zuordnung des berechneten Werts; leer = neu
	Name          string
	InfraID       string
	Unit          string
	Decimals      int
	MaxAgeMinutes *int // nil = Alter der Eingangswerte wird nicht geprüft
	Formula       string
	Inputs        []InputRef
	ValidFrom     time.Time // Kalendertag (Mitternacht UTC); Null = heute
	VersionID     string    // diese geplante Fassung ändern
}

// SaveResult: was Save getan hat.
type SaveResult struct {
	ID        string
	VersionID string
	Created   bool   // der berechnete Wert ist neu
	Version   string // "created", "updated" oder "unchanged"
}

// Input ist ein Eingangswert einer Fassung mit seinem Stand.
type Input struct {
	Short      string
	MappingID  string // leer, wenn es die Zuordnung nicht mehr gibt
	Label      string // „Anlagenpfad › Bezeichnung“ beim Speichern
	Value      string
	ReceivedAt *time.Time
	Gone       bool // die Zuordnung wurde gelöscht
	Calc       bool // der Eingangswert ist selbst ein berechneter Wert
	NoValue    bool // an der Zuordnung steht kein Wert (NULL)
}

// Version ist eine Fassung der Formel.
type Version struct {
	ID        string
	ValidFrom time.Time
	Formula   string
	Inputs    []Input
	CreatedBy string
	CreatedAt time.Time
	Status    string
}

// Value ist ein berechneter Wert mit seinen Fassungen (neueste zuerst).
type Value struct {
	ID, Name, InfraID, InfraPath, Unit string
	Decimals                           int
	MaxAgeMinutes                      *int
	LastValue                          string
	LastAt                             *time.Time
	Reason                             string
	Versions                           []Version
}

// Current liefert die geltende Fassung (nil, wenn noch keine gilt).
func (v *Value) Current() *Version {
	for i := range v.Versions {
		if v.Versions[i].Status == StatusCurrent {
			return &v.Versions[i]
		}
	}
	return nil
}

// Candidate ist eine Zuordnung, die als Eingangswert wählbar ist.
type Candidate struct {
	ID, Name, InfraPath, Connection, Value string
	ReceivedAt                             *time.Time
	Calc                                   bool
}

// Usage: wo ein berechneter Wert gebraucht wird.
type Usage struct {
	Values       []string // berechnete Werte, die ihn als Eingangswert haben
	ExportFields []string // „Verbindung › Feld“
	Meters       []string // Energie-Messstellen mit ihm als Quelle
}

var (
	ErrNotFound      = errors.New("den berechneten Wert gibt es nicht")
	ErrBadName       = errors.New("Bezeichnung muss 2 bis 150 Zeichen lang sein")
	ErrNoInfra       = errors.New("Bitte eine Anlage wählen")
	ErrBadUnit       = errors.New("Einheit: höchstens 20 Zeichen")
	ErrBadDecimals   = errors.New("Nachkommastellen: 0 bis 6")
	ErrBadMaxAge     = errors.New("Höchstalter: 1 bis 44640 Minuten oder leer")
	ErrPastDate      = errors.New("„Gültig ab“ muss heute oder später sein – berechnete Werte haben keinen Verlauf, rückwirkend gäbe es nichts neu zu rechnen")
	ErrVersionFixed  = errors.New("Nur geplante Fassungen lassen sich ändern oder löschen")
	ErrLastVersion   = errors.New("Die einzige Fassung lässt sich nicht löschen – bitte den berechneten Wert löschen")
	ErrTooManyInputs = errors.New("Höchstens 50 Eingangswerte je Formel")
)

// PlannedExistsError: für das Datum gibt es schon eine geplante Fassung.
type PlannedExistsError struct{ Date time.Time }

func (e *PlannedExistsError) Error() string {
	return "Für den " + e.Date.Format("02.01.2006") + " gibt es schon eine geplante Fassung – bitte diese ändern"
}

// ShortNameError: der Kurzname eines Eingangswerts ist nicht erlaubt.
type ShortNameError struct{ Short string }

func (e *ShortNameError) Error() string {
	return "Kurzname „" + e.Short + "“: Buchstabe, dann Buchstaben, Ziffern oder _, höchstens 20 Zeichen, kein Funktionsname, je Formel nur einmal"
}

// UnknownInputError: die Formel nennt einen Namen, der kein Eingangswert ist.
type UnknownInputError struct{ Short string }

func (e *UnknownInputError) Error() string {
	return "Die Formel nennt „" + e.Short + "“ – das ist kein Eingangswert"
}

// InputGoneError: die Zuordnung eines Eingangswerts gibt es nicht (mehr).
type InputGoneError struct{ Short string }

func (e *InputGoneError) Error() string {
	return "Den Eingangswert „" + e.Short + "“ gibt es nicht mehr"
}

// CycleError: berechnete Werte beziehen sich im Kreis aufeinander. Names
// sind Bezeichnungen, beginnend und endend mit demselben Wert.
type CycleError struct{ Names []string }

func (e *CycleError) Error() string {
	return "Kreisbezug: " + strings.Join(e.Names, " → ")
}

// InUseError: der Wert wird von anderen berechneten Werten gebraucht.
type InUseError struct{ Users []string }

func (e *InUseError) Error() string {
	parts := make([]string, len(e.Users))
	for i, u := range e.Users {
		parts[i] = "„" + u + "“"
	}
	return "wird als Eingangswert gebraucht von " + strings.Join(parts, ", ")
}

// UserError: der Text des Fehlers ist für die Person gedacht.
func UserError(err error) bool {
	var (
		pe *PlannedExistsError
		se *ShortNameError
		ue *UnknownInputError
		ge *InputGoneError
		ce *CycleError
		ie *InUseError
		fe *formula.SyntaxError
	)
	if errors.As(err, &pe) || errors.As(err, &se) || errors.As(err, &ue) || errors.As(err, &ge) ||
		errors.As(err, &ce) || errors.As(err, &ie) || errors.As(err, &fe) {
		return true
	}
	for _, e := range []error{ErrNotFound, ErrBadName, ErrNoInfra, ErrBadUnit, ErrBadDecimals, ErrBadMaxAge,
		ErrPastDate, ErrVersionFixed, ErrLastVersion, ErrTooManyInputs} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
