package energie

import (
	"errors"
	"testing"
)

// Messstellen bleiben beim alten Rechenumfang: Funktionen, Vergleiche, Potenz
// und Namen ohne Klammern würden bei Zählern die Zuwächse verfälschen.
func TestFormulaScope(t *testing.T) {
	for _, src := range []string{"[A] + [B]", "-([A] / 3,5) × 2"} {
		if err := formulaScopeErr(src); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
	for _, src := range []string{"MIN([A]; [B])", "[A] > 1", "A + B", "[A] ^ 2"} {
		if err := formulaScopeErr(src); !errors.Is(err, ErrFormulaScope) {
			t.Errorf("%q: erwartet ErrFormulaScope, bekam %v", src, err)
		}
	}
	var fe *FormulaError
	if err := formulaScopeErr("[A] +"); !errors.As(err, &fe) {
		t.Errorf("Schreibfehler bleibt Schreibfehler: %v", err)
	}
}
