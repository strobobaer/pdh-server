package energie

import (
	"errors"
	"testing"
)

// Zählerstand und Verbrauch bleiben beim alten Rechenumfang: Funktionen,
// Vergleiche, Potenz und Namen ohne Klammern würden die Zuwächse verfälschen.
// Momentanwerte dürfen alles, was der Rechenkern kann.
func TestFormulaScope(t *testing.T) {
	for _, kind := range []string{KindCounter, KindConsumption, KindInstant} {
		for _, src := range []string{"[A] + [B]", "-([A] / 3,5) × 2"} {
			if err := formulaScopeErr(src, kind); err != nil {
				t.Errorf("%s %q: %v", kind, src, err)
			}
		}
	}
	for _, src := range []string{"MIN([A]; [B])", "[A] > 1", "A + B", "[A] ^ 2"} {
		for _, kind := range []string{KindCounter, KindConsumption} {
			if err := formulaScopeErr(src, kind); !errors.Is(err, ErrFormulaScope) {
				t.Errorf("%s %q: erwartet ErrFormulaScope, bekam %v", kind, src, err)
			}
		}
		if err := formulaScopeErr(src, KindInstant); err != nil {
			t.Errorf("Momentanwert %q: %v", src, err)
		}
	}
	var fe *FormulaError
	if err := formulaScopeErr("[A] +", KindInstant); !errors.As(err, &fe) {
		t.Errorf("Schreibfehler bleibt Schreibfehler: %v", err)
	}
}
