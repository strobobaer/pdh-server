package listsort

import (
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

func day(n int) time.Time { return t0.AddDate(0, 0, n) }

func TestParse(t *testing.T) {
	allowed := []Mode{Newest, Oldest, Frequent}
	cases := []struct {
		in   string
		want Mode
	}{
		{"old", Oldest},
		{"freq", Frequent},
		{"person", Newest}, // nicht erlaubt
		{"", Newest},
		{"müll; DROP TABLE", Newest},
	}
	for _, c := range cases {
		if got := Parse(c.in, allowed, Newest); got != c.want {
			t.Errorf("Parse(%q) = %q, erwartet %q", c.in, got, c.want)
		}
	}
}

func TestKey(t *testing.T) {
	if Key("Filter gereinigt.") != Key("filter  GEREINIGT") {
		t.Error("Groß/Klein und Leerzeichen müssen egal sein")
	}
	if Key("Öl") != "oel" {
		t.Errorf("Key(Öl) = %q, erwartet oel", Key("Öl"))
	}
	if Key("…") != "" {
		t.Errorf("Key(…) = %q, erwartet leer", Key("…"))
	}
}

func TestGrouped(t *testing.T) {
	if !Frequent.Grouped() || !Same.Grouped() || Newest.Grouped() || Person.Grouped() {
		t.Error("Grouped nur für freq und same")
	}
}

func TestOrder(t *testing.T) {
	items := []Item{
		{At: day(0), Person: "Zora", Kind: "b", Due: day(5)},
		{At: day(1), Person: "", Kind: "a"},
		{At: day(2), Person: "anna", Kind: "b", Due: day(2)},
		{At: day(3), Person: "Zora", Kind: "a", Due: day(9)},
	}
	cases := []struct {
		m    Mode
		want []int
	}{
		{Newest, []int{3, 2, 1, 0}},
		{Oldest, []int{0, 1, 2, 3}},
		{Person, []int{2, 3, 0, 1}}, // anna, Zora (neueste zuerst), ohne Name am Ende
		{Kind, []int{3, 1, 2, 0}},   // a: neueste zuerst, dann b
		{Due, []int{2, 0, 3, 1}},    // ohne Termin am Ende
	}
	for _, c := range cases {
		if got := Order(items, c.m); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Order(%s) = %v, erwartet %v", c.m, got, c.want)
		}
	}
}

func TestOrderStable(t *testing.T) {
	items := []Item{{At: t0}, {At: t0}, {At: t0}}
	for _, m := range []Mode{Newest, Oldest, Person, Kind, Due} {
		a, b := Order(items, m), Order(items, m)
		if !reflect.DeepEqual(a, []int{0, 1, 2}) || !reflect.DeepEqual(a, b) {
			t.Errorf("Order(%s) nicht stabil: %v %v", m, a, b)
		}
	}
}

func TestGroupsFrequent(t *testing.T) {
	items := []Item{
		{At: day(0), Key: "a", Label: "A alt"},
		{At: day(1), Key: "b", Label: "B"},
		{At: day(2), Key: "a", Label: "A neu"},
		{At: day(3), Key: "c", Label: "C"},
		{At: day(4), Key: "a", Label: "A neuest"},
		{At: day(5), Key: "b", Label: "B neu"},
	}
	g := Groups(items, Frequent)
	if len(g) != 3 {
		t.Fatalf("%d Gruppen, erwartet 3", len(g))
	}
	if g[0].Key != "a" || g[0].Count != 3 || g[1].Key != "b" || g[1].Count != 2 || g[2].Key != "c" || g[2].Count != 1 {
		t.Errorf("Reihenfolge falsch: %+v", g)
	}
	if !g[0].Last.Equal(day(4)) || g[0].Label != "A neuest" {
		t.Errorf("Last/Label falsch: %+v", g[0])
	}
	if !reflect.DeepEqual(g[0].Idx, []int{4, 2, 0}) {
		t.Errorf("Idx = %v, erwartet neueste zuerst", g[0].Idx)
	}
}

func TestGroupsFrequentTie(t *testing.T) {
	items := []Item{
		{At: day(0), Key: "a"}, {At: day(1), Key: "a"},
		{At: day(2), Key: "b"}, {At: day(3), Key: "b"},
	}
	g := Groups(items, Frequent)
	if g[0].Key != "b" || g[1].Key != "a" {
		t.Errorf("jüngere Gruppe muss zuerst kommen: %+v", g)
	}
}

func TestGroupsSame(t *testing.T) {
	items := []Item{{At: day(0), Key: "zeta"}, {At: day(1), Key: "alpha"}, {At: day(2), Key: "zeta"}}
	g := Groups(items, Same)
	if len(g) != 2 || g[0].Key != "alpha" || g[1].Key != "zeta" || g[1].Count != 2 {
		t.Errorf("alphabetisch nach Schlüssel erwartet: %+v", g)
	}
}

func TestGroupsEmptyKey(t *testing.T) {
	items := []Item{{At: day(0), Key: "", Label: "x"}, {At: day(1), Key: ""}}
	g := Groups(items, Same)
	if len(g) != 1 || g[0].Count != 2 || g[0].Label != "(ohne Text)" {
		t.Errorf("eine Gruppe „(ohne Text)“ erwartet: %+v", g)
	}
}

// „Nach Person“ sortiert nach dem gefalteten Namen: „Özlem“ steht vor „Zoe“, „anna“ und „Anna“ gelten als gleich
// (dann entscheidet die Zeit), ohne Namen am Ende.
func TestOrderPersonFoldsUmlauts(t *testing.T) {
	d := func(i int) time.Time { return time.Date(2026, 3, 1+i, 8, 0, 0, 0, time.UTC) }
	items := []Item{
		{At: d(0), Person: "Zoe"},
		{At: d(1), Person: "Özlem"},
		{At: d(2), Person: "anna"},
		{At: d(3), Person: ""},
		{At: d(4), Person: "Anna"},
	}
	got := Order(items, Person)
	want := []int{4, 2, 1, 0, 3} // Anna (neuer), anna, Özlem, Zoe, ohne Namen
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Reihenfolge %v, erwartet %v", got, want)
		}
	}
}
