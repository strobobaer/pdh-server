// Package namerange erzeugt Namensreihen für das Sammelanlegen
// (Lager, Infrastruktur): Präfix + Bereich „von–bis“ + Suffix.
//
//	Expand("Fach ", "1", "3", "")  → Fach 1, Fach 2, Fach 3
//	Expand("A", "01", "12", "")    → A01 … A12 (führende Null aus „von“)
//	Expand("Regal ", "A", "D", "") → Regal A … Regal D
package namerange

import (
	"fmt"
	"strconv"
	"strings"
)

// Max begrenzt eine einzelne Reihe (Schutz vor Tippfehlern wie 1–10000).
const Max = 500

// Spec beschreibt eine Reihe; leeres From = keine Reihe (Ebene auslassen).
type Spec struct {
	Prefix, From, To, Suffix string
}

func (s Spec) Empty() bool { return strings.TrimSpace(s.From) == "" && strings.TrimSpace(s.To) == "" }

func (s Spec) Expand() ([]string, error) { return Expand(s.Prefix, s.From, s.To, s.Suffix) }

// Expand liefert die Namen der Reihe. Ist „bis“ leer, entsteht genau ein Name.
func Expand(prefix, from, to, suffix string) ([]string, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" {
		return nil, fmt.Errorf("„von“ fehlt")
	}
	if to == "" {
		to = from
	}
	if a, errA := strconv.Atoi(from); errA == nil {
		b, errB := strconv.Atoi(to)
		if errB != nil {
			return nil, fmt.Errorf("„von“ ist eine Zahl, „bis“ (%s) nicht", to)
		}
		if b < a {
			return nil, fmt.Errorf("„bis“ (%d) ist kleiner als „von“ (%d)", b, a)
		}
		if b-a+1 > Max {
			return nil, fmt.Errorf("höchstens %d auf einmal (%d–%d sind %d)", Max, a, b, b-a+1)
		}
		width := 0
		if len(from) > 1 && from[0] == '0' || len(to) > 1 && to[0] == '0' {
			width = max(len(from), len(to)) // führende Nullen beibehalten: 01…12
		}
		out := make([]string, 0, b-a+1)
		for i := a; i <= b; i++ {
			out = append(out, fmt.Sprintf("%s%0*d%s", prefix, width, i, suffix))
		}
		return out, nil
	}
	if isLetter(from) && isLetter(to) {
		a, b := from[0], to[0]
		if (a >= 'a') != (b >= 'a') {
			return nil, fmt.Errorf("„von“ und „bis“ bitte beide groß oder beide klein schreiben")
		}
		if b < a {
			return nil, fmt.Errorf("„bis“ (%s) liegt vor „von“ (%s)", to, from)
		}
		out := make([]string, 0, b-a+1)
		for c := a; c <= b; c++ {
			out = append(out, prefix+string(c)+suffix)
		}
		return out, nil
	}
	if from == to {
		return []string{prefix + from + suffix}, nil
	}
	return nil, fmt.Errorf("„von“ und „bis“ müssen Zahlen (1–20, 01–20) oder einzelne Buchstaben (A–F) sein")
}

func isLetter(s string) bool {
	return len(s) == 1 && (s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z')
}
