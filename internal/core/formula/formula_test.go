package formula

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseEvalBase(t *testing.T) {
	get := func(k string) (float64, bool) {
		v, ok := map[string]float64{"G": 100, "A": 30, "B": 20}[k]
		return v, ok
	}
	for src, want := range map[string]float64{"[G] − [A] − [B]": 50, "([A]+[B]) × 0,5": 25, "-[A] + 2*3": -24, "[A] ÷ 4": 7.5} {
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if got, ok := f.Eval(get); !ok || got != want {
			t.Errorf("%q = %v (%v)", src, got, ok)
		}
	}
}

func TestOrderDeps(t *testing.T) {
	got, err := OrderDeps(map[string][]string{"c": {"b", "x"}, "b": {"a"}, "a": nil})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("%v %v", got, err)
	}
	var ce *CycleError
	if _, err := OrderDeps(map[string][]string{"a": {"b"}, "b": {"a"}}); !errors.As(err, &ce) || ce.Path[0] != ce.Path[len(ce.Path)-1] {
		t.Fatalf("Kreis: %v", err)
	}
}

func TestUnknownRefMessage(t *testing.T) {
	if err := Check(map[string]string{"V": "[X]"}, map[string]bool{"V": true}); err == nil || err.Error() != "unbekannter Bezug [X]" {
		t.Fatal(err)
	}
}
