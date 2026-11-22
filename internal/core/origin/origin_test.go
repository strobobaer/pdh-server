package origin

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	for _, c := range []struct{ in, label, wantO, wantL string }{
		{Terminal, "  Presse 3 ", Terminal, "Presse 3"},
		{Alarm, "Energie\ndaten", Alarm, "Energiedaten"},
		{"erfunden", "x", "", ""},
		{"", "x", "", ""},
		{Web, "", Web, ""},
	} {
		if o, l := Clean(c.in, c.label); o != c.wantO || l != c.wantL {
			t.Errorf("Clean(%q, %q) = %q, %q", c.in, c.label, o, l)
		}
	}
	if _, l := Clean(Sync, strings.Repeat("ä", 300)); len([]rune(l)) != maxLabel {
		t.Errorf("Zusatz nicht gekürzt: %d", len([]rune(l)))
	}
}
