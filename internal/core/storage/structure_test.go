package storage

import (
	"strings"
	"testing"
)

func TestCheckStructure(t *testing.T) {
	platz := &Node{ID: "p1", Name: "Platz 1", Type: TypePlatz}
	fach := &Node{ID: "f1", Name: "Fach 1", Type: TypeFach, Children: []*Node{platz}}
	regal := &Node{ID: "r1", Name: "Regal A", Type: TypeRegal, Children: []*Node{fach}}
	lager := &Node{ID: "l1", Name: "Hauptlager", Type: TypeLagerort, Children: []*Node{regal}}
	lager2 := &Node{ID: "l2", Name: "Außenlager", Type: TypeLagerort}

	cases := []struct {
		name   string
		n      *Node
		t      NodeType
		parent *Node
		errHas string // "" = erlaubt
	}{
		{"Platz → Fach (keine Kinder)", platz, TypeFach, regal, ""},
		{"Fach → Regal unter Lagerort", fach, TypeRegal, lager, ""},
		{"Regal verschieben in anderen Lagerort", regal, TypeRegal, lager2, ""},
		{"Lagerort bleibt oben", lager, TypeLagerort, nil, ""},
		{"Fach → Platz, aber enthält Platz", fach, TypePlatz, regal, "enthält Platz"},
		{"Regal → Platz mit Fach darunter", regal, TypePlatz, lager, "enthält Fach"},
		{"Fach in eigenen Platz", fach, TypeFach, platz, "in sich selbst"},
		{"Regal in eigenes Fach", regal, TypeRegal, fach, "in sich selbst"},
		{"Fach unter Platz", &Node{ID: "x", Name: "X", Type: TypeFach}, TypeFach, platz, "kann nicht in Platz"},
		{"Fach ganz oben", fach, TypeFach, nil, "nur Lagerorte"},
		{"Lagerort unter Lagerort", lager2, TypeLagerort, lager, "ganz oben"},
		{"unbekannte Art", platz, "kiste", regal, "unbekannte Art"},
	}
	for _, c := range cases {
		err := checkStructure(c.n, c.t, c.parent)
		switch {
		case c.errHas == "" && err != nil:
			t.Errorf("%s: unerwarteter Fehler %v", c.name, err)
		case c.errHas != "" && (err == nil || !strings.Contains(err.Error(), c.errHas)):
			t.Errorf("%s: Fehler mit %q erwartet, bekommen %v", c.name, c.errHas, err)
		}
	}
}
