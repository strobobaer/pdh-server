package tiamqtt

// Lesen der Quelle eines Datenbausteins aus dem TIA Portal
// („Quelle aus Baustein generieren“, Datei *.db). Gelesen wird nur, was für
// den MQTT-Baustein gebraucht wird: Name, Variablenliste mit Typen und
// Startwerten, der BEGIN-Teil und ob remanente Variablen vorkommen.
// Mitexportierte PLC-Datentypen (TYPE … END_TYPE) werden aufgelöst.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

const (
	maxSourceSize  = 1 << 20 // 1 MiB
	maxNesting     = 8
	maxArrayElems  = 1000
	maxNameRunes   = 128
	defaultStrLen  = 254
	reasonBadName  = "Name enthält Zeichen, die im JSON-Pfad nicht gehen"
	reasonEmpty    = "Struktur ohne unterstützte Variablen"
	reasonArrStrct = "Feld von Strukturen wird nicht unterstützt"
	reasonArrArr   = "Feld von Feldern wird nicht unterstützt"
	msgNoDB        = "Die Datei enthält keinen Datenbaustein"
	msgManyDB      = "Bitte nur einen Datenbaustein exportieren"
	msgInstanceDB  = "Instanz- oder Typ-Datenbaustein ohne eigene Variablen – bitte einen Global-DB mit Variablenliste exportieren"
	msgNesting     = "Verschachtelung tiefer als 8 Ebenen"
)

// Kind ist der Datentyp einer Variablen.
type Kind int

const (
	KindBool Kind = iota
	KindInt
	KindDInt
	KindUInt
	KindUDInt
	KindWord
	KindDWord
	KindReal
	KindLReal
	KindString
	KindDTL
	KindDT
	KindStruct
	KindArray
)

// elemKinds ordnet die unterstützten Elementartypen (klein geschrieben) zu.
var elemKinds = map[string]Kind{
	"bool": KindBool, "int": KindInt, "dint": KindDInt, "uint": KindUInt,
	"udint": KindUDInt, "word": KindWord, "dword": KindDWord, "real": KindReal,
	"lreal": KindLReal, "string": KindString, "dtl": KindDTL,
	"date_and_time": KindDT, "dt": KindDT,
}

// Var ist eine Variable des Datenbausteins.
type Var struct {
	Name    string
	Kind    Kind
	Line    int
	StrLen  int    // String[n]; ohne Angabe 254
	Lo, Hi  int    // Array-Grenzen (eindimensional)
	Elem    *Var   // Array-Element (Name leer)
	Members []*Var // Struct und aufgelöster PLC-Datentyp
	Init    string // Startwert wie in der Quelle (roh, für den Ersatz-DB)
}

// Skipped ist eine Variable, die nicht übernommen wird, mit Grund.
type Skipped struct {
	Path   string // SCL-Schreibweise, z. B. Motor.Zeichen
	Line   int
	Reason string
}

// DBSource ist der gelesene Datenbaustein.
type DBSource struct {
	Name      string // ohne Anführungszeichen
	Vars      []*Var
	Skipped   []Skipped
	Begin     string // roher BEGIN-Teil
	HasRetain bool   // VAR RETAIN oder Retain-Attribute gefunden
}

// ParseError ist ein Fehler, wegen dem die Datei abgewiesen wird.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("Zeile %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

// ParseDBSource liest die Quelle eines Global-Datenbausteins.
func ParseDBSource(src []byte) (db *DBSource, err error) {
	if len(src) > maxSourceSize {
		return nil, &ParseError{Msg: "Die Datei ist größer als 1 MiB"}
	}
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})
	var text string
	if utf8.Valid(src) {
		text = string(src)
	} else {
		b, derr := charmap.Windows1252.NewDecoder().Bytes(src)
		if derr != nil {
			return nil, &ParseError{Msg: "Die Datei hat eine unbekannte Zeichenkodierung"}
		}
		text = string(b)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	defer func() {
		if r := recover(); r != nil {
			pe, ok := r.(*ParseError)
			if !ok {
				panic(r)
			}
			db, err = nil, pe
		}
	}()
	p := &parser{lx: &lexer{src: text, line: 1}, types: map[string]*typeExpr{}}
	p.advance()
	return p.parseFile(), nil
}

func fail(line int, msg string) {
	panic(&ParseError{Line: line, Msg: msg})
}

// ---------------------------------------------------------------- Zerlegen

type tokKind int

const (
	tEOF   tokKind = iota
	tWord          // Bezeichner, Zahl, Literal wie T#5s oder 16#FF
	tQName         // "Name" (Text ohne Anführungszeichen)
	tStr           // 'Text' (roh mit Hochkommas)
	tSym           // Satzzeichen
)

type token struct {
	kind       tokKind
	text       string
	line       int
	start, end int // Bytepositionen in der Quelle
}

type lexer struct {
	src        string
	pos, line  int
	retainAttr bool // ein { … }-Attribut nennt Retain
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '#'
}

// next liefert das nächste Zeichen ohne Leerraum, Kommentare, Attribute und
// TITLE-Zeilen.
func (lx *lexer) next() token {
	for {
		lx.skipSpace()
		rest := lx.src[lx.pos:]
		switch {
		case rest == "":
			return token{kind: tEOF, line: lx.line, start: lx.pos, end: lx.pos}
		case strings.HasPrefix(rest, "//"):
			lx.skipToEOL()
		case strings.HasPrefix(rest, "(*"):
			end := strings.Index(rest[2:], "*)")
			if end < 0 {
				fail(lx.line, "Kommentar „(*“ wird nicht geschlossen")
			}
			lx.advanceBy(end + 4)
		case rest[0] == '{':
			lx.skipAttr()
		default:
			t := lx.token()
			if t.kind == tWord && strings.EqualFold(t.text, "TITLE") && lx.equalsFollows() {
				lx.skipToEOL()
				continue
			}
			return t
		}
	}
}

func (lx *lexer) skipSpace() {
	for lx.pos < len(lx.src) {
		switch lx.src[lx.pos] {
		case '\n':
			lx.line++
		case ' ', '\t', '\f', '\v':
		default:
			return
		}
		lx.pos++
	}
}

func (lx *lexer) skipToEOL() {
	if i := strings.IndexByte(lx.src[lx.pos:], '\n'); i >= 0 {
		lx.pos += i
	} else {
		lx.pos = len(lx.src)
	}
}

// advanceBy rückt um n Byte vor und zählt dabei die Zeilen.
func (lx *lexer) advanceBy(n int) {
	lx.line += strings.Count(lx.src[lx.pos:lx.pos+n], "\n")
	lx.pos += n
}

// equalsFollows meldet, ob auf derselben Zeile als Nächstes „=“ steht.
func (lx *lexer) equalsFollows() bool {
	for i := lx.pos; i < len(lx.src); i++ {
		switch lx.src[i] {
		case ' ', '\t':
			continue
		case '=':
			return true
		}
		return false
	}
	return false
}

// skipAttr überspringt { … } samt Verschachtelung und Zeichenketten.
func (lx *lexer) skipAttr() {
	startLine, start := lx.line, lx.pos
	depth := 0
	for i := lx.pos; i < len(lx.src); i++ {
		switch c := lx.src[i]; c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				attr := strings.ToUpper(lx.src[start : i+1])
				if strings.Contains(strings.ReplaceAll(attr, "NON_RETAIN", ""), "RETAIN") {
					lx.retainAttr = true
				}
				lx.advanceBy(i + 1 - lx.pos)
				return
			}
		case '\'', '"':
			if j := closingQuote(lx.src, i, c); j > 0 {
				i = j
			}
		}
	}
	fail(startLine, "Attribut „{“ wird nicht geschlossen")
}

// closingQuote sucht ab src[i] (Anführungszeichen q) das schließende
// Zeichen auf derselben Zeile; $ maskiert das folgende Zeichen. -1 = keins.
func closingQuote(src string, i int, q byte) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\n':
			return -1
		case '$':
			j++
		case q:
			return j
		}
	}
	return -1
}

func (lx *lexer) token() token {
	start, line := lx.pos, lx.line
	mk := func(k tokKind, text string, end int) token {
		lx.pos = end
		return token{kind: k, text: text, line: line, start: start, end: end}
	}
	rest := lx.src[start:]
	switch c := rest[0]; {
	case c == '"':
		end := strings.IndexAny(rest[1:], "\"\n")
		if end < 0 || rest[1+end] != '"' {
			fail(line, "Name „\"“ wird nicht geschlossen")
		}
		return mk(tQName, rest[1:1+end], start+end+2)
	case c == '\'':
		end := closingQuote(lx.src, start, '\'')
		if end < 0 {
			fail(line, "Zeichenkette „'“ wird nicht geschlossen")
		}
		return mk(tStr, lx.src[start:end+1], end+1)
	case strings.HasPrefix(rest, ":="):
		return mk(tSym, ":=", start+2)
	case strings.HasPrefix(rest, ".."):
		return mk(tSym, "..", start+2)
	}
	r, size := utf8.DecodeRuneInString(rest)
	if !isWordRune(r) {
		return mk(tSym, rest[:size], start+size)
	}
	i := size
	for i < len(rest) {
		r, size = utf8.DecodeRuneInString(rest[i:])
		if isWordRune(r) {
			i += size
			continue
		}
		if r == '.' && i+1 < len(rest) {
			if nr, _ := utf8.DecodeRuneInString(rest[i+1:]); nr != '.' && isWordRune(nr) {
				i++
				continue
			}
		}
		break
	}
	return mk(tWord, rest[:i], start+i)
}

// ---------------------------------------------------------------- Grammatik

type typeForm int

const (
	fElem typeForm = iota
	fUnsupported
	fStruct
	fArray
	fRef
)

type typeExpr struct {
	form    typeForm
	kind    Kind   // fElem
	strLen  int    // fElem String
	reason  string // fUnsupported, oder fArray, wenn das Feld selbst nicht geht
	ref     string // fRef: Name des PLC-Datentyps
	members []*decl
	lo, hi  int
	elem    *typeExpr
}

type decl struct {
	name string
	line int
	typ  *typeExpr
	init string
}

type parser struct {
	lx        *lexer
	cur, prev token
	types     map[string]*typeExpr // Schlüssel klein geschrieben
	hasRetain bool
}

func (p *parser) advance() {
	p.prev = p.cur
	p.cur = p.lx.next()
}

func (p *parser) isWord(w string) bool {
	return p.cur.kind == tWord && strings.EqualFold(p.cur.text, w)
}

func (p *parser) isSym(s string) bool {
	return p.cur.kind == tSym && p.cur.text == s
}

// failExpected meldet, dass etwas fehlt – in der Zeile, in der es stehen
// müsste (am Zeilenende also die vorherige Zeile).
func (p *parser) failExpected(what string) {
	line := p.cur.line
	if p.prev.line > 0 && (p.cur.kind == tEOF || p.cur.line > p.prev.line) {
		line = p.prev.line
	}
	fail(line, what+" erwartet")
}

func (p *parser) expectSym(s string) {
	if !p.isSym(s) {
		p.failExpected("„" + s + "“")
	}
	p.advance()
}

func (p *parser) expectWord(w string) {
	if !p.isWord(w) {
		p.failExpected("„" + w + "“")
	}
	p.advance()
}

func (p *parser) name() (string, int) {
	if p.cur.kind != tWord && p.cur.kind != tQName {
		p.failExpected("Name")
	}
	n, l := p.cur.text, p.cur.line
	p.advance()
	return n, l
}

type dbAST struct {
	name     string
	decls    []*decl
	hasVars  bool
	ref      string // DATA_BLOCK "X" "Y"
	refQuote bool
	refLine  int
	begin    string
}

func (p *parser) parseFile() *DBSource {
	var db *dbAST
	for p.cur.kind != tEOF {
		switch {
		case p.isWord("TYPE"):
			p.parseTypeBlock()
		case p.isWord("DATA_BLOCK"):
			if db != nil {
				fail(p.cur.line, msgManyDB)
			}
			db = p.parseDataBlock()
		case p.isWord("FUNCTION_BLOCK"), p.isWord("FUNCTION"), p.isWord("ORGANIZATION_BLOCK"):
			p.skipBlock("END_" + strings.ToUpper(p.cur.text))
		default:
			fail(p.cur.line, "„"+p.cur.text+"“ an dieser Stelle unerwartet")
		}
	}
	if db == nil {
		fail(0, msgNoDB)
	}

	decls := db.decls
	if !db.hasVars && db.ref != "" {
		t := p.types[strings.ToLower(db.ref)]
		if !db.refQuote || t == nil || t.form != fStruct {
			fail(db.refLine, msgInstanceDB)
		}
		decls = t.members
	}
	r := &resolver{types: p.types}
	out := &DBSource{
		Name:      db.name,
		Vars:      r.decls(decls, "", 1),
		Begin:     db.begin,
		HasRetain: p.hasRetain || p.lx.retainAttr,
	}
	out.Skipped = r.skipped
	return out
}

// skipHeader überspringt VERSION, AUTHOR, FAMILY, NAME und Retain-Angaben
// im Kopf eines Bausteins (TITLE und { … } fallen schon beim Zerlegen weg).
func (p *parser) skipHeader() {
	for {
		switch {
		case p.isWord("VERSION"), p.isWord("AUTHOR"), p.isWord("FAMILY"), p.isWord("NAME"):
			p.advance()
			p.expectSym(":")
			if p.cur.kind == tEOF {
				p.failExpected("Wert")
			}
			p.advance()
		case p.isWord("RETAIN"):
			p.hasRetain = true
			p.advance()
		case p.isWord("NON_RETAIN"), p.isWord("UNLINKED"), p.isWord("READ_ONLY"), p.isWord("KNOW_HOW_PROTECT"):
			p.advance()
		default:
			return
		}
	}
}

// skipBlock überspringt einen nicht gebrauchten Baustein (FB, FC, OB).
func (p *parser) skipBlock(end string) {
	p.advance()
	p.skipBlockRest(end)
}

func (p *parser) parseTypeBlock() {
	p.advance()
	name, _ := p.name()
	p.skipHeader()
	if !p.isWord("STRUCT") {
		// Andere Typen (z. B. Aufzählungen) werden nicht gebraucht.
		p.skipBlockRest("END_TYPE")
		return
	}
	p.advance()
	members := p.parseDecls("END_STRUCT", 1)
	p.expectWord("END_STRUCT")
	if p.isSym(";") {
		p.advance()
	}
	p.expectWord("END_TYPE")
	p.types[strings.ToLower(name)] = &typeExpr{form: fStruct, members: members}
}

// skipBlockRest überspringt alles bis einschließlich end.
func (p *parser) skipBlockRest(end string) {
	for !p.isWord(end) {
		if p.cur.kind == tEOF {
			p.failExpected("„" + end + "“")
		}
		p.advance()
	}
	p.advance()
}

func (p *parser) parseDataBlock() *dbAST {
	p.advance()
	db := &dbAST{}
	db.name, _ = p.name()
	p.skipHeader()
	switch {
	case p.isWord("VAR"):
		for p.isWord("VAR") {
			p.advance()
			for p.isWord("RETAIN") || p.isWord("NON_RETAIN") || p.isWord("CONSTANT") {
				if p.isWord("RETAIN") {
					p.hasRetain = true
				}
				p.advance()
			}
			db.decls = append(db.decls, p.parseDecls("END_VAR", 1)...)
			p.expectWord("END_VAR")
		}
		db.hasVars = true
	case p.isWord("STRUCT"):
		p.advance()
		db.decls = p.parseDecls("END_STRUCT", 1)
		p.expectWord("END_STRUCT")
		if p.isSym(";") {
			p.advance()
		}
		db.hasVars = true
	case p.cur.kind == tQName || (p.cur.kind == tWord && !p.isWord("BEGIN") && !p.isWord("END_DATA_BLOCK")):
		db.ref, db.refQuote, db.refLine = p.cur.text, p.cur.kind == tQName, p.cur.line
		p.advance()
	}
	if p.isWord("BEGIN") {
		start := p.cur.end
		p.advance()
		for !p.isWord("END_DATA_BLOCK") {
			if p.cur.kind == tEOF {
				p.failExpected("„END_DATA_BLOCK“")
			}
			p.advance()
		}
		db.begin = strings.TrimSpace(p.lx.src[start:p.cur.start])
	}
	p.expectWord("END_DATA_BLOCK")
	return db
}

// parseDecls liest Deklarationen bis zum Schlüsselwort end (nicht verbraucht).
func (p *parser) parseDecls(end string, depth int) []*decl {
	var out []*decl
	for !p.isWord(end) {
		if p.cur.kind == tEOF {
			p.failExpected("„" + end + "“")
		}
		if depth > maxNesting {
			fail(p.cur.line, msgNesting)
		}
		out = append(out, p.parseDecl(depth))
	}
	return out
}

func (p *parser) parseDecl(depth int) *decl {
	d := &decl{}
	if p.cur.kind != tWord && p.cur.kind != tQName {
		fail(p.cur.line, "Variablenname erwartet")
	}
	d.name, d.line = p.name()
	p.expectSym(":")
	d.typ = p.parseType(depth)
	if p.isSym(":=") {
		start := p.cur.end
		p.advance()
		nest := 0
		for nest > 0 || !p.isSym(";") {
			switch {
			case p.cur.kind == tEOF:
				p.failExpected("„;“")
			case p.isSym("("), p.isSym("["):
				nest++
			case p.isSym(")"), p.isSym("]"):
				nest--
			}
			p.advance()
		}
		d.init = strings.TrimSpace(p.lx.src[start:p.cur.start])
	}
	p.expectSym(";")
	return d
}

func (p *parser) parseType(depth int) *typeExpr {
	switch {
	case p.isWord("STRUCT"):
		p.advance()
		members := p.parseDecls("END_STRUCT", depth+1)
		p.expectWord("END_STRUCT")
		return &typeExpr{form: fStruct, members: members}
	case p.isWord("ARRAY"):
		p.advance()
		p.expectSym("[")
		bounds := p.bracket()
		p.expectWord("OF")
		t := &typeExpr{form: fArray, elem: p.parseType(depth + 1)}
		t.lo, t.hi, t.reason = arrayBounds(bounds)
		return t
	case p.cur.kind == tQName:
		ref := p.cur.text
		p.advance()
		return &typeExpr{form: fRef, ref: ref}
	case p.cur.kind == tWord:
		name := p.cur.text
		p.advance()
		var arg []token
		if p.isSym("[") {
			p.advance()
			arg = p.bracket()
		}
		kind, ok := elemKinds[strings.ToLower(name)]
		if !ok {
			return &typeExpr{form: fUnsupported, reason: "Typ " + name + " wird nicht unterstützt"}
		}
		t := &typeExpr{form: fElem, kind: kind}
		if kind == KindString {
			t.strLen = defaultStrLen
			if arg != nil {
				n, err := 0, error(nil)
				if len(arg) == 1 && arg[0].kind == tWord {
					n, err = strconv.Atoi(arg[0].text)
				}
				if len(arg) != 1 || err != nil || n < 1 || n > defaultStrLen {
					return &typeExpr{form: fUnsupported, reason: "Länge der Zeichenkette muss eine Zahl von 1 bis 254 sein"}
				}
				t.strLen = n
			}
		}
		return t
	}
	p.failExpected("Datentyp")
	return nil
}

// bracket liest die Zeichen bis „]“ (verbraucht) nach einem schon
// verbrauchten „[“.
func (p *parser) bracket() []token {
	var out []token
	for !p.isSym("]") {
		if p.cur.kind == tEOF || p.isSym(";") {
			p.failExpected("„]“")
		}
		out = append(out, p.cur)
		p.advance()
	}
	p.advance()
	return out
}

// arrayBounds wertet „lo..hi“ aus; reason ist gesetzt, wenn das Feld nicht
// übernommen werden kann.
func arrayBounds(ts []token) (lo, hi int, reason string) {
	for _, t := range ts {
		if t.kind == tSym && t.text == "," {
			return 0, 0, "Mehrdimensionale Felder werden nicht unterstützt"
		}
	}
	num := func(ts []token) (int, bool) {
		s := ""
		for _, t := range ts {
			s += t.text
		}
		n, err := strconv.Atoi(s)
		return n, err == nil && len(ts) > 0 && len(ts) <= 2
	}
	sep := -1
	for i, t := range ts {
		if t.kind == tSym && t.text == ".." {
			sep = i
		}
	}
	var ok1, ok2 bool
	if sep >= 0 {
		lo, ok1 = num(ts[:sep])
		hi, ok2 = num(ts[sep+1:])
	}
	switch {
	case !ok1 || !ok2:
		return 0, 0, "Feldgrenzen müssen feste Zahlen sein"
	case hi < lo:
		return 0, 0, "Feldgrenzen sind ungültig"
	case hi-lo+1 > maxArrayElems:
		return 0, 0, "Feld mit mehr als 1000 Elementen wird nicht unterstützt"
	}
	return lo, hi, ""
}

// ---------------------------------------------------------------- Auflösen

type resolver struct {
	types   map[string]*typeExpr
	skipped []Skipped
}

func (r *resolver) decls(ds []*decl, prefix string, depth int) []*Var {
	var out []*Var
	for _, d := range ds {
		if v := r.decl(d, prefix, depth); v != nil {
			out = append(out, v)
		}
	}
	return out
}

func (r *resolver) decl(d *decl, prefix string, depth int) *Var {
	path := sclPath(prefix, d.name)
	if !validName(d.name) {
		r.skipped = append(r.skipped, Skipped{Path: path, Line: d.line, Reason: reasonBadName})
		return nil
	}
	v, reason := r.typ(d.typ, path, d.line, depth)
	if v == nil {
		r.skipped = append(r.skipped, Skipped{Path: path, Line: d.line, Reason: reason})
		return nil
	}
	v.Name, v.Line, v.Init = d.name, d.line, d.init
	return v
}

func missingUDT(name string) string {
	return `PLC-Datentyp "` + name + `" fehlt – Quelle mit abhängigen Bausteinen exportieren`
}

// typ baut die Variable zum Typ; nil mit Grund, wenn sie wegfällt.
func (r *resolver) typ(t *typeExpr, path string, line, depth int) (*Var, string) {
	if depth > maxNesting {
		fail(line, msgNesting)
	}
	switch t.form {
	case fElem:
		return &Var{Kind: t.kind, StrLen: t.strLen}, ""
	case fStruct:
		members := r.decls(t.members, path, depth+1)
		if len(members) == 0 {
			return nil, reasonEmpty
		}
		return &Var{Kind: KindStruct, Members: members}, ""
	case fRef:
		ut := r.types[strings.ToLower(t.ref)]
		if ut == nil {
			return nil, missingUDT(t.ref)
		}
		return r.typ(ut, path, line, depth)
	case fArray:
		if t.reason != "" {
			return nil, t.reason
		}
		switch e := t.elem; e.form {
		case fElem:
			return &Var{Kind: KindArray, Lo: t.lo, Hi: t.hi, Elem: &Var{Kind: e.kind, StrLen: e.strLen}}, ""
		case fRef:
			if r.types[strings.ToLower(e.ref)] == nil {
				return nil, missingUDT(e.ref)
			}
			return nil, reasonArrStrct
		case fStruct:
			return nil, reasonArrStrct
		case fArray:
			return nil, reasonArrArr
		default:
			return nil, e.reason
		}
	}
	return nil, t.reason
}

// validName prüft, ob der Name als Schlüssel im JSON-Pfad taugt.
func validName(n string) bool {
	if n == "" || utf8.RuneCountInString(n) > maxNameRunes {
		return false
	}
	for _, r := range n {
		if strings.ContainsRune(".[]\"\\#", r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// sclPath hängt name in SCL-Schreibweise an; Namen mit Sonderzeichen
// stehen in Anführungszeichen.
func sclPath(prefix, name string) string {
	seg := name
	plain := name != ""
	for i, r := range name {
		if !(unicode.IsLetter(r) || r == '_' || (i > 0 && unicode.IsDigit(r))) {
			plain = false
			break
		}
	}
	if !plain {
		seg = `"` + name + `"`
	}
	if prefix == "" {
		return seg
	}
	return prefix + "." + seg
}
