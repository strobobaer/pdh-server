package formula

import "testing"

// Schlussprüfung Minor 6: Das Formelfeld ist mehrzeilig; Zeilenumbruch und
// geschütztes Leerzeichen (aus Excel eingefügt) sind Leerraum.
func TestWhitespaceVariants(t *testing.T) {
	for _, src := range []string{"A +\nB", "A +\r\nB", "A + B", "SUMME(A;\n B)"} {
		f, err := Parse(src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if v, why := f.EvalWhy(extGet); why != nil || v != 50 {
			t.Errorf("%q = %v %+v", src, v, why)
		}
	}
}
