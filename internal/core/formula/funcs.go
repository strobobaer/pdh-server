package formula

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// funcDef beschreibt eine Funktion: deutscher Name und erlaubte Anzahl Angaben.
type funcDef struct {
	name     string
	min, max int
}

func (d *funcDef) arity(n int) string {
	switch {
	case n >= d.min && n <= d.max:
		return ""
	case d.name == "WENN":
		return "WENN braucht 3 Angaben (Bedingung; dann; sonst)"
	case d.min == 1 && d.max == 1:
		return d.name + " braucht 1 Angabe"
	case d.max == 2:
		return d.name + " braucht 1 oder 2 Angaben"
	case n < d.min:
		return d.name + " braucht mindestens eine Angabe"
	}
	return fmt.Sprintf("%s: höchstens %d Angaben", d.name, d.max)
}

var funcDefs = []*funcDef{
	{"MIN", 1, maxArgs}, {"MAX", 1, maxArgs}, {"SUMME", 1, maxArgs}, {"MITTELWERT", 1, maxArgs},
	{"BETRAG", 1, 1}, {"RUNDEN", 1, 2}, {"WURZEL", 1, 1}, {"WENN", 3, 3},
	{"UND", 1, maxArgs}, {"ODER", 1, maxArgs}, {"NICHT", 1, 1},
}

// Englische Namen gelten als gleichwertig (für Excel-Gewohnte und den Copilot).
var funcAliases = map[string]string{
	"SUM": "SUMME", "AVG": "MITTELWERT", "AVERAGE": "MITTELWERT", "ABS": "BETRAG", "ROUND": "RUNDEN",
	"SQRT": "WURZEL", "IF": "WENN", "AND": "UND", "OR": "ODER", "NOT": "NICHT",
}

func lookupFunc(name string) *funcDef {
	up := strings.ToUpper(name)
	if a, ok := funcAliases[up]; ok {
		up = a
	}
	for _, d := range funcDefs {
		if d.name == up {
			return d
		}
	}
	return nil
}

// IsFunction: der Name ist ein Funktionsname (deutsch oder englisch, ohne
// Rücksicht auf Groß- und Kleinschreibung).
func IsFunction(name string) bool { return lookupFunc(name) != nil }

// ValidIdent: gültiger Name für einen Bezug ohne Klammern – Buchstabe, dann
// Buchstaben, Ziffern oder _, höchstens 20 Zeichen, kein Funktionsname.
func ValidIdent(s string) bool {
	if n := utf8.RuneCountInString(s); n < 1 || n > 20 {
		return false
	}
	for i, c := range []rune(s) {
		if i == 0 && !identStart(c) || i > 0 && !identPart(c) {
			return false
		}
	}
	return !IsFunction(s)
}

// FuncInfo beschreibt eine Funktion für Knöpfe und Handbuch.
type FuncInfo struct {
	Name   string // deutscher Name
	Insert string // Text, den der Knopf in die Formel setzt
	Help   string // ein Satz mit Beispiel
}

// Functions liefert die Funktionen in der Reihenfolge der Knöpfe.
func Functions() []FuncInfo {
	return []FuncInfo{
		{"MIN", "MIN(; )", "Kleinster Wert, z. B. MIN(A; B)"},
		{"MAX", "MAX(; )", "Größter Wert, z. B. MAX(A; B; C)"},
		{"SUMME", "SUMME(; )", "Summe, z. B. SUMME(A; B; C)"},
		{"MITTELWERT", "MITTELWERT(; )", "Mittelwert, z. B. MITTELWERT(A; B)"},
		{"BETRAG", "BETRAG()", "Wert ohne Vorzeichen, z. B. BETRAG(A - B)"},
		{"RUNDEN", "RUNDEN(; 1)", "Kaufmännisch runden auf 0 bis 6 Stellen, z. B. RUNDEN(A / B; 2)"},
		{"WURZEL", "WURZEL()", "Quadratwurzel, z. B. WURZEL(A)"},
		{"WENN", "WENN(; ; )", "Gilt die Bedingung, zählt der erste Wert, sonst der zweite, z. B. WENN(B > 0; A / B; 0)"},
		{"UND", "UND(; )", "1, wenn alle Bedingungen gelten, z. B. UND(A > 5; B > 5)"},
		{"ODER", "ODER(; )", "1, wenn mindestens eine Bedingung gilt, z. B. ODER(A > 5; B > 5)"},
		{"NICHT", "NICHT()", "Kehrt eine Bedingung um, z. B. NICHT(A > 5)"},
	}
}

// evalFunc rechnet eine Funktion. WENN rechnet nur den gewählten Zweig – so
// lässt sich Teilen durch 0 abfangen: WENN(B > 0; A / B; 0).
func evalFunc(n *node, get func(string) (float64, bool)) (float64, *NoValue) {
	if n.fn == "WENN" {
		c, why := evalNode(n.args[0], get)
		if why != nil {
			return 0, why
		}
		if c != 0 {
			return evalNode(n.args[1], get)
		}
		return evalNode(n.args[2], get)
	}
	vals := make([]float64, len(n.args))
	for i, a := range n.args {
		v, why := evalNode(a, get)
		if why != nil {
			return 0, why
		}
		vals[i] = v
	}
	var out float64
	switch n.fn {
	case "MIN":
		out = vals[0]
		for _, v := range vals[1:] {
			out = math.Min(out, v)
		}
	case "MAX":
		out = vals[0]
		for _, v := range vals[1:] {
			out = math.Max(out, v)
		}
	case "SUMME", "MITTELWERT":
		for _, v := range vals {
			out += v
		}
		if n.fn == "MITTELWERT" {
			out /= float64(len(vals))
		}
	case "BETRAG":
		out = math.Abs(vals[0])
	case "RUNDEN":
		digits := 0
		if len(vals) == 2 {
			digits = int(vals[1])
		}
		out = Round(vals[0], digits)
	case "WURZEL":
		if vals[0] < 0 {
			return 0, &NoValue{Kind: NoValueNotFinite}
		}
		out = math.Sqrt(vals[0])
	case "UND":
		out = 1
		for _, v := range vals {
			if v == 0 {
				out = 0
			}
		}
	case "ODER":
		for _, v := range vals {
			if v != 0 {
				out = 1
			}
		}
	case "NICHT":
		out = truth(vals[0] == 0)
	}
	if !finite(out) {
		return 0, &NoValue{Kind: NoValueNotFinite}
	}
	return out, nil
}

// Round rundet kaufmännisch (von der Null weg) auf die Zahl der Stellen.
func Round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	r := math.Round(v*p) / p
	if !finite(r) {
		return v
	}
	return r
}
