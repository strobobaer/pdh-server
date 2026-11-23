// Package formula ist der gemeinsame Rechenkern für Formeln: berechnete
// Messstellen der Energiedaten und berechnete Werte (virtuelle Messwerte).
package formula

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// MaxLen ist die größte erlaubte Formellänge in Zeichen.
const MaxLen = 2000

// maxArgs ist die größte Anzahl Angaben einer Funktion.
const maxArgs = 50

// SyntaxError beschreibt einen Fehler in der Schreibweise einer Formel.
// Pos zählt Zeichen ab 1.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("Formel, Zeichen %d: %s", e.Pos, e.Msg)
}

// UnknownRefError: die Formel nennt einen Bezug, den es nicht gibt.
type UnknownRefError struct{ Key string }

func (e *UnknownRefError) Error() string {
	return "unbekannter Bezug [" + e.Key + "]"
}

// CycleError: Formeln beziehen sich im Kreis aufeinander.
// Path beginnt und endet mit derselben ID.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	parts := make([]string, len(e.Path))
	for i, p := range e.Path {
		parts[i] = "[" + p + "]"
	}
	return "Zirkelbezug: " + strings.Join(parts, " → ")
}

// NoValueKind sagt, warum eine Formel keinen Wert liefert.
type NoValueKind int

const (
	// NoValueRef: ein Bezug hat keinen Wert (Ref nennt ihn).
	NoValueRef NoValueKind = iota + 1
	// NoValueDivZero: Teilen durch 0.
	NoValueDivZero
	// NoValueNotFinite: das Ergebnis ist keine endliche Zahl.
	NoValueNotFinite
)

// NoValue ist der Grund für „kein Wert“.
type NoValue struct {
	Kind NoValueKind
	Ref  string
}

type nodeKind int

const (
	nodeNum nodeKind = iota
	nodeRef
	nodeNeg
	nodeBin
	nodeCmp
	nodePow
	nodeFunc
)

type node struct {
	kind nodeKind
	num  float64
	ref  string
	bare bool   // Bezug ohne eckige Klammern
	op   rune   // nodeBin: + - * /
	cmp  string // nodeCmp: < <= > >= = <>
	fn   string // nodeFunc: deutscher Name
	l, r *node
	args []*node
}

// Formula ist eine geprüfte, auswertbare Formel.
type Formula struct{ root *node }

type parser struct {
	src []rune
	pos int
}

// Parse liest eine Formel. Erlaubt sind Zahlen (Punkt oder Komma),
// + - * / (auch − × ÷), ^, Vergleiche (< <= > >= = <>), Klammern,
// einstelliges Minus, Bezüge ([ID] oder ein Name) und Funktionen mit „;“
// zwischen den Angaben.
func Parse(src string) (*Formula, error) {
	rs := []rune(src)
	if len(rs) > MaxLen {
		return nil, &SyntaxError{Pos: MaxLen + 1, Msg: "Formel ist zu lang (höchstens 2000 Zeichen)"}
	}
	p := &parser{src: rs}
	p.skip()
	if p.pos >= len(rs) {
		return nil, &SyntaxError{Pos: 1, Msg: "Formel ist leer"}
	}
	n, err := p.compare()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos < len(rs) {
		return nil, p.errf("unerwartetes Zeichen „%c“", rs[p.pos])
	}
	return &Formula{root: n}, nil
}

func (p *parser) errf(format string, a ...any) *SyntaxError {
	return &SyntaxError{Pos: p.pos + 1, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) skip() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

// cmpOp liest einen Vergleich an der Schreibmarke (ohne sie zu bewegen).
func (p *parser) cmpOp() (op string, width int) {
	if p.pos >= len(p.src) {
		return "", 0
	}
	c := p.src[p.pos]
	next := rune(0)
	if p.pos+1 < len(p.src) {
		next = p.src[p.pos+1]
	}
	switch {
	case c == '<' && next == '=':
		return "<=", 2
	case c == '>' && next == '=':
		return ">=", 2
	case c == '<' && next == '>':
		return "<>", 2
	case c == '<':
		return "<", 1
	case c == '>':
		return ">", 1
	case c == '=':
		return "=", 1
	case c == '≤':
		return "<=", 1
	case c == '≥':
		return ">=", 1
	case c == '≠':
		return "<>", 1
	}
	return "", 0
}

func (p *parser) compare() (*node, error) {
	l, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skip()
	op, w := p.cmpOp()
	if w == 0 {
		return l, nil
	}
	p.pos += w
	r, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skip()
	if _, again := p.cmpOp(); again > 0 {
		return nil, p.errf("Vergleiche bitte klammern")
	}
	return &node{kind: nodeCmp, cmp: op, l: l, r: r}, nil
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
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &node{kind: nodeNeg, l: n}, nil
	}
	return p.power()
}

// power: Potenz bindet stärker als das Vorzeichen davor und ist rechts
// gebunden (2 ^ 3 ^ 2 = 512, -2 ^ 2 = -4).
func (p *parser) power() (*node, error) {
	base, err := p.primary()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos >= len(p.src) || p.src[p.pos] != '^' {
		return base, nil
	}
	p.pos++
	exp, err := p.unary()
	if err != nil {
		return nil, err
	}
	return &node{kind: nodePow, l: base, r: exp}, nil
}

func identStart(c rune) bool { return unicode.IsLetter(c) }
func identPart(c rune) bool  { return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' }

func (p *parser) primary() (*node, error) {
	p.skip()
	if p.pos >= len(p.src) {
		return nil, p.errf("Wert fehlt")
	}
	c := p.src[p.pos]
	switch {
	case c == '(':
		p.pos++
		n, err := p.compare()
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
			if c := p.src[p.pos]; c == '[' || c == ';' || c == '"' || c == '\n' || c == '\r' {
				return nil, p.errf("Bezug nicht erlaubt")
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
			return nil, p.errf("Bezug fehlt")
		}
		if len([]rune(key)) > 64 {
			return nil, p.errf("Bezug ist zu lang")
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
				return nil, p.errf("Ziffer nach dem Komma fehlt – Angaben einer Funktion trennt „;“")
			}
		}
		txt := strings.Replace(string(p.src[start:p.pos]), ",", ".", 1)
		v, err := strconv.ParseFloat(txt, 64)
		if err != nil {
			p.pos = start
			return nil, p.errf("ungültige Zahl")
		}
		return &node{kind: nodeNum, num: v}, nil
	case identStart(c):
		start := p.pos
		for p.pos < len(p.src) && identPart(p.src[p.pos]) {
			p.pos++
		}
		name := string(p.src[start:p.pos])
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != '(' {
			return &node{kind: nodeRef, ref: name, bare: true}, nil
		}
		return p.call(name, start)
	}
	return nil, p.errf("unerwartetes Zeichen „%c“", c)
}

// call liest die Angaben einer Funktion; die Schreibmarke steht auf „(“.
func (p *parser) call(name string, start int) (*node, error) {
	fail := func(msg string) (*node, error) {
		return nil, &SyntaxError{Pos: start + 1, Msg: msg}
	}
	def := lookupFunc(name)
	if def == nil {
		return fail("unbekannte Funktion „" + name + "“")
	}
	p.pos++ // (
	var args []*node
	p.skip()
	if p.pos < len(p.src) && p.src[p.pos] == ')' {
		p.pos++
	} else {
		for {
			a, err := p.compare()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			p.skip()
			if p.pos < len(p.src) && p.src[p.pos] == ';' {
				p.pos++
				continue
			}
			if p.pos < len(p.src) && p.src[p.pos] == ')' {
				p.pos++
				break
			}
			if p.pos >= len(p.src) {
				return nil, p.errf("Klammer fehlt")
			}
			return nil, p.errf("unerwartetes Zeichen „%c“", p.src[p.pos])
		}
	}
	if msg := def.arity(len(args)); msg != "" {
		return fail(msg)
	}
	if def.name == "RUNDEN" && len(args) == 2 {
		d := args[1]
		if d.kind != nodeNum || d.num != math.Trunc(d.num) || d.num < 0 || d.num > 6 {
			return fail("RUNDEN: Stellen als Zahl von 0 bis 6 angeben")
		}
	}
	return &node{kind: nodeFunc, fn: def.name, args: args}, nil
}

func walk(n *node, visit func(*node)) {
	if n == nil {
		return
	}
	visit(n)
	walk(n.l, visit)
	walk(n.r, visit)
	for _, a := range n.args {
		walk(a, visit)
	}
}

// Refs liefert die genannten Bezüge, sortiert und ohne Doppelte – auch die
// aus Zweigen, die beim Rechnen nicht angefasst werden.
func (f *Formula) Refs() []string {
	set := map[string]bool{}
	walk(f.root, func(n *node) {
		if n.kind == nodeRef {
			set[n.ref] = true
		}
	})
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Extended: die Formel nutzt mehr als + − × ÷, Klammern und [ID] – einen
// Namen ohne Klammern, einen Vergleich, eine Potenz oder eine Funktion.
func (f *Formula) Extended() bool {
	ext := false
	walk(f.root, func(n *node) {
		switch {
		case n.kind == nodeRef && n.bare, n.kind == nodeCmp, n.kind == nodePow, n.kind == nodeFunc:
			ext = true
		}
	})
	return ext
}

// Eval rechnet die Formel aus. false, wenn ein Bezug keinen Wert hat, durch
// 0 geteilt wird oder das Ergebnis keine endliche Zahl ist.
func (f *Formula) Eval(get func(key string) (float64, bool)) (float64, bool) {
	v, why := f.EvalWhy(get)
	return v, why == nil
}

// EvalWhy rechnet die Formel aus und nennt den Grund, wenn es keinen Wert
// gibt (nil = Wert da).
func (f *Formula) EvalWhy(get func(key string) (float64, bool)) (float64, *NoValue) {
	v, why := evalNode(f.root, get)
	if why != nil {
		return 0, why
	}
	if !finite(v) {
		return 0, &NoValue{Kind: NoValueNotFinite}
	}
	return v, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func truth(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func evalNode(n *node, get func(string) (float64, bool)) (float64, *NoValue) {
	switch n.kind {
	case nodeNum:
		return n.num, nil
	case nodeRef:
		v, ok := get(n.ref)
		if !ok || !finite(v) {
			return 0, &NoValue{Kind: NoValueRef, Ref: n.ref}
		}
		return v, nil
	case nodeNeg:
		v, why := evalNode(n.l, get)
		return -v, why
	case nodeFunc:
		return evalFunc(n, get)
	}
	a, why := evalNode(n.l, get)
	if why != nil {
		return 0, why
	}
	b, why := evalNode(n.r, get)
	if why != nil {
		return 0, why
	}
	var v float64
	switch n.kind {
	case nodeCmp:
		switch n.cmp {
		case "<":
			return truth(a < b), nil
		case "<=":
			return truth(a <= b), nil
		case ">":
			return truth(a > b), nil
		case ">=":
			return truth(a >= b), nil
		case "=":
			return truth(a == b), nil
		}
		return truth(a != b), nil
	case nodePow:
		v = math.Pow(a, b)
	default:
		switch n.op {
		case '+':
			v = a + b
		case '-':
			v = a - b
		case '*':
			v = a * b
		default:
			if b == 0 {
				return 0, &NoValue{Kind: NoValueDivZero}
			}
			v = a / b
		}
	}
	if !finite(v) {
		return 0, &NoValue{Kind: NoValueNotFinite}
	}
	return v, nil
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
		f, err := Parse(formulas[k])
		if err != nil {
			return nil, err
		}
		out[k] = f
	}
	return out, nil
}

// Check prüft alle Formeln (Schlüssel = Kennung des berechneten
// Werts): Schreibweise, unbekannte Bezüge und Zirkelbezüge.
// Bezüge auf andere Formeln sind erlaubt.
func Check(formulas map[string]string, known map[string]bool) error {
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

// Order liefert die Schlüssel der Formeln so sortiert, dass Bezüge vor
// ihren Nutzern stehen. Bei Zirkelbezug ein *CycleError.
func Order(formulas map[string]string) ([]string, error) {
	parsed, err := parseAll(formulas)
	if err != nil {
		return nil, err
	}
	return order(parsed)
}

func order(parsed map[string]*Formula) ([]string, error) {
	deps := make(map[string][]string, len(parsed))
	for k, f := range parsed {
		deps[k] = f.Refs()
	}
	return OrderDeps(deps)
}

// OrderDeps sortiert die Schlüssel so, dass genutzte Schlüssel vor ihren
// Nutzern stehen (deps: Schlüssel → genutzte Schlüssel). Ziele, die selbst
// kein Schlüssel sind, werden übergangen. Bei Zirkelbezug ein *CycleError.
func OrderDeps(deps map[string][]string) ([]string, error) {
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
		refs := append([]string{}, deps[k]...)
		sort.Strings(refs)
		for _, ref := range refs {
			if _, ok := deps[ref]; !ok {
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
	keys := make([]string, 0, len(deps))
	for k := range deps {
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
