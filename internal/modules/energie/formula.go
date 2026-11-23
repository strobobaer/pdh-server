package energie

import (
	"errors"

	"pdh/internal/core/formula"
)

// Die Formeln der berechneten Messstellen rechnet der gemeinsame Rechenkern
// (internal/core/formula). Hier steht nur die Hülle mit den Namen und
// Meldungen der Energiedaten.

type (
	// Formula ist eine geprüfte, auswertbare Formel.
	Formula = formula.Formula
	// FormulaError beschreibt einen Fehler in der Schreibweise einer Formel.
	FormulaError = formula.SyntaxError
	// CycleError: berechnete Messstellen beziehen sich im Kreis aufeinander.
	CycleError = formula.CycleError
)

// UnknownRefError: die Formel nennt eine Messstelle, die es nicht gibt.
type UnknownRefError struct{ Key string }

func (e *UnknownRefError) Error() string {
	return "unbekannte Messstelle [" + e.Key + "]"
}

// ParseFormula liest eine Formel. Erlaubt sind Zahlen (Punkt oder Komma),
// + - * / (auch − × ÷), Klammern, einstelliges Minus und Bezüge [ID].
func ParseFormula(src string) (*Formula, error) { return formula.Parse(src) }

// CheckFormulas prüft alle Formeln (Schlüssel = ID der berechneten
// Messstelle): Schreibweise, unbekannte Bezüge und Zirkelbezüge.
func CheckFormulas(formulas map[string]string, known map[string]bool) error {
	err := formula.Check(formulas, known)
	var ue *formula.UnknownRefError
	if errors.As(err, &ue) {
		return &UnknownRefError{Key: ue.Key}
	}
	return err
}

// EvalOrder liefert die berechneten Messstellen so sortiert, dass Bezüge vor
// ihren Nutzern stehen. Bei Zirkelbezug ein *CycleError.
func EvalOrder(formulas map[string]string) ([]string, error) { return formula.Order(formulas) }
