package web

import (
	"fmt"
	"regexp"
	"testing"
)

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func TestPlantColorStableAndInPalette(t *testing.T) {
	if len(plantPalette) < 12 {
		t.Fatalf("Palette hat nur %d Farben", len(plantPalette))
	}
	seen := map[string]bool{}
	for _, c := range plantPalette {
		if !hexColor.MatchString(c) || seen[c] {
			t.Errorf("Palettenfarbe %q ungültig oder doppelt", c)
		}
		seen[c] = true
	}
	id := "01a1f566-b4d2-783f-b0f7-e6e42ee3deae"
	a, b := plantColor(id), plantColor(id)
	if a != b || !seen[a] {
		t.Errorf("plantColor nicht stabil oder nicht aus der Palette: %q %q", a, b)
	}
	if plantColor(id) != plantColor("01A1F566-B4D2-783F-B0F7-E6E42EE3DEAE") {
		t.Error("Groß-/Kleinschreibung der ID darf die Farbe nicht ändern")
	}
	// über viele IDs werden alle Palettenfarben genutzt (Streuung)
	used := map[string]bool{}
	for i := 0; i < 400; i++ {
		used[plantColor(fmt.Sprintf("0190a000-0000-7000-8000-%012d", i))] = true
	}
	if len(used) != len(plantPalette) {
		t.Errorf("nur %d von %d Farben genutzt", len(used), len(plantPalette))
	}
}

func TestPlantColorsCollisionAndOverflow(t *testing.T) {
	// ohne Kollision entspricht die Farbe plantColor
	one := plantColors([]string{"a"})
	if one["a"] != plantColor("a") {
		t.Errorf("einzelne Anlage: %q statt %q", one["a"], plantColor("a"))
	}
	// Anlagen mit gleicher Wunschfarbe bekommen verschiedene Farben
	var same []string
	want := plantColor("x0")
	for i := 0; len(same) < 3; i++ {
		id := fmt.Sprintf("x%d", i)
		if plantColor(id) == want {
			same = append(same, id)
		}
	}
	m := plantColors(same)
	got := map[string]bool{}
	for _, id := range same {
		if !hexColor.MatchString(m[id]) || got[m[id]] {
			t.Fatalf("Kollision nicht aufgelöst: %v", m)
		}
		got[m[id]] = true
	}
	// Reihenfolge der Eingabe ändert die Zuordnung nicht
	rev := []string{same[2], same[1], same[0]}
	m2 := plantColors(rev)
	for _, id := range same {
		if m[id] != m2[id] {
			t.Errorf("%s: Farbe hängt von der Reihenfolge ab (%s/%s)", id, m[id], m2[id])
		}
	}
	// mehr Anlagen als Palettenfarben: alle verschieden, gültig, deterministisch
	var ids []string
	for i := 0; i < 60; i++ {
		ids = append(ids, fmt.Sprintf("0190a000-0000-7000-8000-%012d", i))
	}
	c1, c2 := plantColors(ids), plantColors(ids)
	seen := map[string]bool{}
	for _, id := range ids {
		c := c1[id]
		if !hexColor.MatchString(c) {
			t.Fatalf("%s: Farbe %q", id, c)
		}
		if seen[c] {
			t.Errorf("Farbe %s doppelt", c)
		}
		seen[c] = true
		if c != c2[id] {
			t.Errorf("%s: nicht deterministisch", id)
		}
	}
	// doppelte IDs in der Liste zählen einmal
	if d := plantColors([]string{"a", "a", "b"}); len(d) != 2 || d["a"] == d["b"] {
		t.Errorf("doppelte IDs: %v", d)
	}
}
