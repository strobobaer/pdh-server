package namerange

import (
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	for _, c := range []struct {
		prefix, from, to, suffix, want string
	}{
		{"Fach ", "1", "3", "", "Fach 1|Fach 2|Fach 3"},
		{"A", "01", "03", "", "A01|A02|A03"},
		{"A", "8", "10", "", "A8|A9|A10"},
		{"P", "098", "100", "-L", "P098-L|P099-L|P100-L"},
		{"Regal ", "A", "C", "", "Regal A|Regal B|Regal C"},
		{"", "x", "z", "", "x|y|z"},
		{"Platz ", "5", "", "", "Platz 5"},
		{"", "Mitte", "Mitte", "", "Mitte"},
	} {
		got, err := Expand(c.prefix, c.from, c.to, c.suffix)
		if err != nil || strings.Join(got, "|") != c.want {
			t.Errorf("%+v: %v %v", c, got, err)
		}
	}
	for _, bad := range [][2]string{{"5", "1"}, {"1", "B"}, {"A", "c"}, {"C", "A"}, {"1", "1000"}, {"", "3"}, {"ab", "cd"}} {
		if _, err := Expand("", bad[0], bad[1], ""); err == nil {
			t.Errorf("%v: Fehler erwartet", bad)
		}
	}
	if !(Spec{}).Empty() || (Spec{From: "1"}).Empty() {
		t.Error("Empty")
	}
}
