package energie

import (
	"errors"
	"strings"
	"time"
)

// Art einer Messstelle.
const (
	KindCounter     = "counter"     // Zählerstand (wächst)
	KindConsumption = "consumption" // Verbrauch je Lesung
	KindInstant     = "instant"     // Momentanwert
)

const (
	// StaleAfter: ältere Lesungen einer Quelle gelten nicht mehr als frisch.
	StaleAfter = 15 * time.Minute
	// ValueRetentionDays: so lange werden Minutenwerte aufbewahrt.
	ValueRetentionDays = 400
)

var (
	ErrNotFound = errors.New("messstelle nicht gefunden")
	ErrBadKey   = errors.New("die Messstellen-ID muss 1 bis 64 Zeichen lang sein und darf kein [, ], ;, \" und keinen Zeilenumbruch enthalten")
	ErrKeyTaken = errors.New("diese Messstellen-ID ist bereits vergeben")
	ErrBadKind  = errors.New("unbekannte Art der Messstelle")
	ErrKindMix  = errors.New("Momentanwerte lassen sich nur mit Momentanwerten verrechnen, Zählerstände und Verbräuche nur untereinander")
	ErrNoInfra  = errors.New("bitte eine Anlage wählen")
	// ErrFormulaScope: Messstellen rechnen bei Zählern auf Zuwächsen – Funktionen,
	// Vergleiche und Potenz würden sie verfälschen (Rechenkern: Formula.Extended).
	// ErrCalcConsumption: ein berechneter Wert wird zusammengefasst geschrieben;
	// eine Verbrauchs-Messstelle addiert aber jede Lesung.
	ErrCalcConsumption = errors.New("ein berechneter Wert kann nicht Quelle einer Messstelle der Art Verbrauch sein – er wird zusammengefasst geschrieben, die Summe wäre falsch. Bitte Momentanwert oder Zählerstand wählen")
	ErrFormulaScope    = errors.New("bei Zählerständen und Verbräuchen sind in der Formel nur + − × ÷, Klammern und [Messstellen-ID] erlaubt – Funktionen und Vergleiche gehen bei Momentanwerten; sonst einen berechneten Wert anlegen und als Quelle wählen")

	// Fassungen der Formel und Neurechnung.
	ErrPastDate     = errors.New("ein Datum bis heute geht nur mit „Neu rechnen“ – ab sofort ohne Neurechnung ändert der Stift in der Zeile die Formel")
	ErrVersionFixed = errors.New("nur eine geplante Fassung lässt sich ändern oder löschen")
	ErrNotCalc      = errors.New("nur berechnete Messstellen haben Fassungen")
	ErrRecalcBusy   = errors.New("es läuft gerade eine Neurechnung – bitte warten, bis sie fertig ist")
)

// PlannedExistsError: für den Tag gibt es schon eine geplante Fassung.
type PlannedExistsError struct{ Date time.Time }

func (e *PlannedExistsError) Error() string {
	return "für den " + e.Date.Format("02.01.2006") + " ist schon eine Fassung geplant"
}

// TooEarlyError: so weit zurück gibt es keine Werte der Messstelle.
type TooEarlyError struct{ First time.Time }

func (e *TooEarlyError) Error() string {
	return "Werte dieser Messstelle gibt es erst ab dem " + e.First.Format("02.01.2006")
}

// InUseError: die Messstelle steht in der Formel anderer Messstellen.
type InUseError struct{ Users []string }

func (e *InUseError) Error() string {
	parts := make([]string, len(e.Users))
	for i, u := range e.Users {
		parts[i] = "[" + u + "]"
	}
	return "wird verwendet in " + strings.Join(parts, ", ")
}

// Meter ist eine Messstelle. Sie hat entweder eine Quelle (Import-Zuordnung)
// oder eine Formel über andere Messstellen.
type Meter struct {
	ID, Key, Name, InfraID, InfraPath, SourceID, SourceLabel string
	ConnectionID, ConnectionName, Formula, Unit, Kind        string
	Factor                                                   float64
	Export, Active                                           bool
	LastValue                                                *float64
	LastAt                                                   *time.Time
}

// Virtual: die Messstelle wird aus einer Formel berechnet.
func (m Meter) Virtual() bool { return m.Formula != "" }

// Filter für List. Leere Felder filtern nicht.
type Filter struct {
	InfraID                 string // mit Unteranlagen
	Virtual, Export, Active *bool
	Unit, Kind              string
	ConnectionID, Query     string
	Stale                   bool   // LastAt leer oder älter als StaleAfter
	Alarm                   bool   // mindestens ein Schwellwert im Alarm
	Sort                    string // key|name|infra|unit|kind|last
	Desc                    bool
	AllowedIDs              map[string]bool // nil = alle
}
