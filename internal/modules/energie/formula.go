package energie

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// maxFormulaLen ist die größte erlaubte Formellänge in Zeichen.
const maxFormulaLen = 2000

// FormulaError beschreibt einen Fehler in der Schreibweise einer Formel.
// Pos zählt Zeichen ab 1.
type FormulaError struct {
	Pos int
	Msg string
}

func (e *FormulaError) Error() string {
	return fmt.Sprintf("Formel, Zeichen %d: %s", e.Pos, e.Msg)
}

// UnknownRefError: die Formel nennt eine Messstelle, die es nicht gibt.
type UnknownRefError struct{ Key string }

func (e *UnknownRefError) Error() string {
	return "unbekannte Messstelle [" + e.Key + "]"
}

// CycleError: berechnete Messstellen beziehen sich im Kreis aufeinander.
// Path beginnt und endet mit derselben ID.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	parts := make([]string, len(e.Path))
	for i, p := range e.Path {
		parts[i] = "[" + p + "]"
	}
	return "Zirkelbezug: " + strings.Join(parts, " → ")
}

type nodeKind int

const (
	nodeNum nodeKind = iota
	nodeRef
	nodeNeg
	nodeBin
)

type node struct {
	kind nodeKind
	num  float64
	ref  string
	op   rune
	l, r *node
}

// Formula ist eine geprüfte, auswertbare Formel.
type Formula struct{ root *node }

type parser struct {
	src []rune
	pos int
}

// ParseFormula liest eine Formel. Erlaubt sind Zahlen (Punkt oder Komma),
// + - * / (auch − × ÷), Klammern, einstelliges Minus und Bezüge [ID].
func ParseFormula(src string) (*Formula, error) {
	rs := []rune(src)
	if len(rs) > maxFormulaLen {
		return nil, &FormulaError{Pos: maxFormulaLen + 1, Msg: "Formel ist zu lang (höchstens 2000 Zeichen)"}
	}
	p := &parser{src: rs}
	p.skip()
	if p.pos >= len(rs) {
		return nil, &FormulaError{Pos: 1, Msg: "Formel ist leer"}
	}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos < len(rs) {
		return nil, p.errf("unerwartetes Zeichen „%c“", rs[p.pos])
	}
	return &Formula{root: n}, nil
}

func (p *parser) errf(format string, a ...any) *FormulaError {
	return &FormulaError{Pos: p.pos + 1, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) skip() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) expr() (*node, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		p.skip()
		if p.pos >= len(p.src) {
			return l, nil
		}
		c := p.src[p.pos]
		if c != '+' && c != '-' && c != '−' {
			return l, nil
		}
		op := '+'
		if c != '+' {
			op = '-'
		}
		p.pos++
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = &node{kind: nodeBin, op: op, l: l, r: r}
	}
}

func (p *parser) term() (*node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		p.skip()
		if p.pos >= len(p.src) {
			return l, nil
		}
		c := p.src[p.pos]
		if c != '*' && c != '×' && c != '/' && c != '÷' {
			return l, nil
		}
		op := '*'
		if c == '/' || c == '÷' {
			op = '/'
		}
		p.pos++
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = &node{kind: nodeBin, op: op, l: l, r: r}
	}
}

func (p *parser) unary() (*node, error) {
	p.skip()
	if p.pos < len(p.src) && (p.src[p.pos] == '-' || p.src[p.pos] == '−') {
		p.pos++
		n, err := p.primary()
		if err != nil {
			return nil, err
		}
		return &node{kind: nodeNeg, l: n}, nil
	}
	return p.primary()
}

func (p *parser) primary() (*node, error) {
	p.skip()
	if p.pos >= len(p.src) {
		return nil, p.errf("Wert fehlt")
	}
	c := p.src[p.pos]
	switch {
	case c == '(':
		p.pos++
		n, err := p.expr()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return nil, p.errf("Klammer fehlt")
		}
		p.pos++
		return n, nil
	case c == '[':
		start := p.pos
		p.pos++
		var id []rune
		for p.pos < len(p.src) && p.src[p.pos] != ']' {
			if p.src[p.pos] == '[' || p.src[p.pos] == '\n' || p.src[p.pos] == '\r' {
				return nil, p.errf("Messstellen-ID nicht erlaubt")
			}
			id = append(id, p.src[p.pos])
			p.pos++
		}
		if p.pos >= len(p.src) {
			p.pos = start
			return nil, p.errf("eckige Klammer fehlt")
		}
		key := strings.TrimSpace(string(id))
		if key == "" {
			return nil, p.errf("Messstellen-ID fehlt")
		}
		if len([]rune(key)) > 64 {
			return nil, p.errf("Messstellen-ID ist zu lang")
		}
		p.pos++
		return &node{kind: nodeRef, ref: key}, nil
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		if p.pos < len(p.src) && (p.src[p.pos] == '.' || p.src[p.pos] == ',') {
			p.pos++
			d := p.pos
			for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
				p.pos++
			}
			if p.pos == d {
				return nil, p.errf("Ziffer nach dem Komma fehlt")
			}
		}
		txt := strings.Replace(string(p.src[start:p.pos]), ",", ".", 1)
		v, err := strconv.ParseFloat(txt, 64)
		if err != nil {
			p.pos = start
			return nil, p.errf("ungültige Zahl")
		}
		return &node{kind: nodeNum, num: v}, nil
	}
	return nil, p.errf("unerwartetes Zeichen „%c“", c)
}

// Refs liefert die genannten Messstellen-IDs, sortiert und ohne Doppelte.
func (f *Formula) Refs() []string {
	set := map[string]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.kind == nodeRef {
			set[n.ref] = true
		}
		walk(n.l)
		walk(n.r)
	}
	walk(f.root)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Eval rechnet die Formel aus. false, wenn ein Bezug keinen Wert hat, durch
// 0 geteilt wird oder das Ergebnis keine endliche Zahl ist.
func (f *Formula) Eval(get func(key string) (float64, bool)) (float64, bool) {
	v, ok := evalNode(f.root, get)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func evalNode(n *node, get func(string) (float64, bool)) (float64, bool) {
	switch n.kind {
	case nodeNum:
		return n.num, true
	case nodeRef:
		v, ok := get(n.ref)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case nodeNeg:
		v, ok := evalNode(n.l, get)
		return -v, ok
	}
	a, ok := evalNode(n.l, get)
	if !ok {
		return 0, false
	}
	b, ok := evalNode(n.r, get)
	if !ok {
		return 0, false
	}
	var v float64
	switch n.op {
	case '+':
		v = a + b
	case '-':
		v = a - b
	case '*':
		v = a * b
	default:
		if b == 0 {
			return 0, false
		}
		v = a / b
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parseAll(formulas map[string]string) (map[string]*Formula, error) {
	out := make(map[string]*Formula, len(formulas))
	for _, k := range sortedKeys(formulas) {
		f, err := ParseFormula(formulas[k])
		if err != nil {
			return nil, err
		}
		out[k] = f
	}
	return out, nil
}

// CheckFormulas prüft alle Formeln (Schlüssel = ID der berechneten
// Messstelle): Schreibweise, unbekannte Bezüge und Zirkelbezüge.
// Bezüge auf andere berechnete Messstellen sind erlaubt.
func CheckFormulas(formulas map[string]string, known map[string]bool) error {
	parsed, err := parseAll(formulas)
	if err != nil {
		return err
	}
	for _, k := range sortedKeys(formulas) {
		for _, ref := range parsed[k].Refs() {
			if _, isFormula := formulas[ref]; !known[ref] && !isFormula {
				return &UnknownRefError{Key: ref}
			}
		}
	}
	_, err = order(parsed)
	return err
}

// EvalOrder liefert die berechneten Messstellen so sortiert, dass Bezüge vor
// ihren Nutzern stehen. Bei Zirkelbezug ein *CycleError.
func EvalOrder(formulas map[string]string) ([]string, error) {
	parsed, err := parseAll(formulas)
	if err != nil {
		return nil, err
	}
	return order(parsed)
}

func order(parsed map[string]*Formula) ([]string, error) {
	const (
		white = iota
		grey
		black
	)
	state := map[string]int{}
	var out, stack []string
	var visit func(k string) error
	visit = func(k string) error {
		state[k] = grey
		stack = append(stack, k)
		for _, ref := range parsed[k].Refs() {
			if _, ok := parsed[ref]; !ok {
				continue
			}
			switch state[ref] {
			case grey:
				i := len(stack) - 1
				for stack[i] != ref {
					i--
				}
				path := append(append([]string{}, stack[i:]...), ref)
				return &CycleError{Path: path}
			case white:
				if err := visit(ref); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[k] = black
		out = append(out, k)
		return nil
	}
	keys := make([]string, 0, len(parsed))
	for k := range parsed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if state[k] == white {
			if err := visit(k); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
