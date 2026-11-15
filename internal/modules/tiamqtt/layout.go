package tiamqtt

// Aufteilen der Variablen eines Datenbausteins auf MQTT-Nachrichten
// („Teile“). Jede Variable wird zu Einzelwerten (Leaf) aufgelöst; die
// Einzelwerte werden in Quellreihenfolge so auf Teile verteilt, dass jede
// Nachricht auch mit den längsten möglichen Werten unter der Größengrenze
// bleibt.
//
// Eine Nachricht hat immer die Form
//
//	{"ts":"2026-11-15T10:01:00Z","part":1,"parts":2,"values":{ … }}
//
// Kopf ({"ts":…,"values":{) und Schluss (}}) gehören nicht zu Part.Ops; ihre
// größte Länge liefert HeaderWidth, geschrieben werden sie von Render bzw.
// vom SCL-Code um die Ops herum. Part.Ops ist genau der Inhalt zwischen
// "values":{ und }} und wird vom SCL-Erzeuger eins zu eins abgearbeitet:
// OpRaw sind feste Zeichen (Schlüssel, Kommas, Klammern; UTF-8), OpValue
// ist ein Einzelwert.

import (
	"errors"
	"strconv"
	"strings"
)

// Family ist die Steuerungsfamilie.
type Family string

const (
	FamilyS71500 Family = "s7-1500"
	FamilyS71200 Family = "s7-1200"
)

const (
	DefaultLimit = 4096  // Vorgabe der Größengrenze je Nachricht in Byte
	MinLimit     = 1024  // kleinste erlaubte Grenze
	MaxLimit     = 16384 // größte erlaubte Grenze
	MaxParts     = 50    // höchstens so viele Nachrichten je Auslösung
	MaxLeaves    = 2000  // höchstens so viele Einzelwerte
)

const (
	reasonTooBig  = "zu groß für eine Nachricht"
	reasonDT1200  = "Date_And_Time gibt es auf S7-1200 nicht – DTL verwenden"
	msgTooMany    = "Zu viele Werte: höchstens 50 Nachrichten bzw. 2000 Werte"
	msgNoValues   = "Keine sendbaren Variablen gefunden"
	msgBadLimit   = "Die Größengrenze muss zwischen 1024 und 16384 Byte liegen"
	msgBadFamily  = "Unbekannte Steuerungsfamilie – S7-1500 oder S7-1200 wählen"
	tsTextWidth   = len("2026-11-15T10:01:00Z")
	headerFixed   = len(`{"ts":"`) + tsTextWidth + len(`","part":`) + len(`,"parts":`) + len(`,"values":{`) + len(`}}`)
	dateTimeWidth = len(`"2026-11-15T10:01:00"`)
)

// Leaf ist ein Einzelwert, der in die Nachricht geschrieben wird.
type Leaf struct {
	Path     string // SCL: Motor.Drehzahl, Feld[3]
	JSONPath string // values.Motor.Drehzahl, values.Feld[2]
	Kind     Kind
	StrLen   int
	Width    int // größte Länge des JSON-Werts in Byte
}

// OpKind unterscheidet feste Zeichen und Einzelwerte.
type OpKind int

const (
	OpRaw   OpKind = iota // feste Zeichen, Kommas schon eingerechnet
	OpValue               // ein Einzelwert
)

// Op ist ein Schritt beim Schreiben des values-Inhalts einer Nachricht.
type Op struct {
	Kind OpKind
	Raw  string // bei OpRaw: die Zeichen in UTF-8
	Leaf *Leaf  // bei OpValue
}

// Part ist eine Nachricht.
type Part struct {
	No       int // ab 1
	Ops      []Op
	Leaves   []Leaf
	MaxBytes int // größte Länge der ganzen Nachricht samt Kopf
}

// Layout ist die Aufteilung eines Datenbausteins.
type Layout struct {
	Parts   []Part
	Leaves  []Leaf // alle gesendeten Einzelwerte in Quellreihenfolge
	Skipped []Skipped
	Limit   int
}

// HeaderWidth ist die größte Länge von Kopf und Schluss einer Nachricht bei
// parts Teilen (part und parts haben höchstens so viele Ziffern wie parts).
func HeaderWidth(parts int) int {
	return headerFixed + 2*len(strconv.Itoa(parts))
}

// ValueWidth ist die größte Länge des JSON-Werts eines Typs in Byte.
func ValueWidth(k Kind, strLen int) int {
	switch k {
	case KindBool:
		return 5 // false
	case KindInt:
		return 6 // -32768
	case KindDInt:
		return 11 // -2147483648
	case KindUInt, KindWord:
		return 5 // 65535
	case KindUDInt, KindDWord:
		return 10 // 4294967295
	case KindReal:
		return 15 // -3.402823E+38
	case KindLReal:
		return 25 // -1.79769313486232E+308
	case KindString:
		// je Byte höchstens 2 (\" \\ oder Latin-1 als UTF-8), dazu die
		// Anführungszeichen; mindestens so breit wie null
		return max(2*strLen+2, len("null"))
	case KindDTL, KindDT:
		return dateTimeWidth
	}
	return 0
}

// unit ist, was zusammen in einer Nachricht stehen muss: ein Einzelwert oder
// ein ganzes Feld.
type unit struct {
	structs []string // Namen der umgebenden Strukturen
	name    string
	array   bool
	leaves  []Leaf
}

// Plan verteilt die Variablen von db auf Nachrichten bis zur Grenze limit
// (Byte, 0 = DefaultLimit).
func Plan(db *DBSource, family Family, limit int) (*Layout, error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < MinLimit || limit > MaxLimit {
		return nil, errors.New(msgBadLimit)
	}
	if family != FamilyS71500 && family != FamilyS71200 {
		return nil, errors.New(msgBadFamily)
	}
	f := &flattener{family: family, skipped: append([]Skipped(nil), db.Skipped...)}
	f.vars(db.Vars, nil, "")
	n := 0
	for _, u := range f.units {
		n += len(u.leaves)
	}
	if n > MaxLeaves {
		return nil, errors.New(msgTooMany)
	}

	// Die Teilezahl bestimmt die Kopfbreite: so lange neu verteilen, bis
	// sie sich nicht mehr ändert. Mehr Teile machen den Kopf nie kürzer,
	// die Zahl steigt also nur und die Schleife endet.
	guess := 1
	for {
		parts, skipped, err := split(f.units, f.unitPaths, guess, limit)
		if err != nil {
			return nil, err
		}
		if len(parts) > guess {
			guess = len(parts)
			continue
		}
		l := &Layout{Parts: parts, Skipped: append(f.skipped, skipped...), Limit: limit}
		for i := range parts {
			parts[i].MaxBytes += HeaderWidth(len(parts))
			l.Leaves = append(l.Leaves, parts[i].Leaves...)
		}
		if len(l.Leaves) == 0 {
			return nil, errors.New(msgNoValues)
		}
		return l, nil
	}
}

// split verteilt gierig bei angenommener Teilezahl guess.
func split(units []unit, paths []skipRef, guess, limit int) ([]Part, []Skipped, error) {
	budget := limit - HeaderWidth(guess)
	var (
		parts   []Part
		skipped []Skipped
		cur     opsBuilder
	)
	flush := func() error {
		if len(cur.leaves) == 0 {
			return nil
		}
		if len(parts) == MaxParts {
			return errors.New(msgTooMany)
		}
		parts = append(parts, cur.finish(len(parts)+1))
		cur = opsBuilder{}
		return nil
	}
	for i, u := range units {
		var alone opsBuilder
		if alone.cost(u) > budget {
			skipped = append(skipped, Skipped{Path: paths[i].path, Line: paths[i].line, Reason: reasonTooBig})
			continue
		}
		if cur.cost(u) > budget {
			if err := flush(); err != nil {
				return nil, nil, err
			}
		}
		cur.add(u)
	}
	if err := flush(); err != nil {
		return nil, nil, err
	}
	return parts, skipped, nil
}

// ---------------------------------------------------------------- Auflösen

type skipRef struct {
	path string
	line int
}

type flattener struct {
	family    Family
	units     []unit
	unitPaths []skipRef
	skipped   []Skipped
}

func (f *flattener) vars(vs []*Var, structs []string, prefix string) {
	for _, v := range vs {
		path := sclPath(prefix, v.Name)
		switch v.Kind {
		case KindStruct:
			inner := append(append([]string(nil), structs...), v.Name)
			f.vars(v.Members, inner, path)
		case KindArray:
			if f.dtMissing(v.Elem.Kind) {
				f.skipped = append(f.skipped, Skipped{Path: path, Line: v.Line, Reason: reasonDT1200})
				continue
			}
			u := unit{structs: structs, name: v.Name, array: true}
			for i := v.Lo; i <= v.Hi; i++ {
				u.leaves = append(u.leaves, newLeaf(
					path+"["+strconv.Itoa(i)+"]",
					jsonPath(structs, v.Name)+"["+strconv.Itoa(i-v.Lo)+"]",
					v.Elem.Kind, v.Elem.StrLen))
			}
			f.addUnit(u, path, v.Line)
		default:
			if f.dtMissing(v.Kind) {
				f.skipped = append(f.skipped, Skipped{Path: path, Line: v.Line, Reason: reasonDT1200})
				continue
			}
			u := unit{structs: structs, name: v.Name,
				leaves: []Leaf{newLeaf(path, jsonPath(structs, v.Name), v.Kind, v.StrLen)}}
			f.addUnit(u, path, v.Line)
		}
	}
}

func (f *flattener) dtMissing(k Kind) bool {
	return k == KindDT && f.family == FamilyS71200
}

func (f *flattener) addUnit(u unit, path string, line int) {
	f.units = append(f.units, u)
	f.unitPaths = append(f.unitPaths, skipRef{path: path, line: line})
}

func newLeaf(path, jp string, k Kind, strLen int) Leaf {
	if k != KindString {
		strLen = 0
	}
	return Leaf{Path: path, JSONPath: jp, Kind: k, StrLen: strLen, Width: ValueWidth(k, strLen)}
}

func jsonPath(structs []string, name string) string {
	return "values." + strings.Join(append(append([]string(nil), structs...), name), ".")
}

// ---------------------------------------------------------------- Ops bauen

// opsBuilder schreibt die Ops eines Teils und zählt die größte Länge mit.
type opsBuilder struct {
	ops     []Op
	leaves  []Leaf
	stack   []string // offene Strukturen
	hasElem []bool   // je Ebene (0 = values): schon ein Eintrag geschrieben
	size    int      // feste Zeichen + Wertbreiten, ohne schließende Klammern
	dry     bool
}

// cost ist die Länge des values-Inhalts samt schließender Klammern, wenn u
// noch hinzukommt.
func (b *opsBuilder) cost(u unit) int {
	t := opsBuilder{
		stack:   append([]string(nil), b.stack...),
		hasElem: append([]bool(nil), b.hasElem...),
		size:    b.size,
		dry:     true,
	}
	t.add(u)
	return t.size + len(t.stack)
}

func (b *opsBuilder) raw(s string) {
	b.size += len(s)
	if b.dry {
		return
	}
	if n := len(b.ops); n > 0 && b.ops[n-1].Kind == OpRaw {
		b.ops[n-1].Raw += s
		return
	}
	b.ops = append(b.ops, Op{Kind: OpRaw, Raw: s})
}

func (b *opsBuilder) value(l Leaf) {
	b.size += l.Width
	if b.dry {
		return
	}
	lf := l
	b.ops = append(b.ops, Op{Kind: OpValue, Leaf: &lf})
	b.leaves = append(b.leaves, l)
}

// entry beginnt einen Eintrag im innersten offenen Objekt (Komma davor,
// wenn es nicht der erste ist).
func (b *opsBuilder) entry(name string) {
	top := len(b.hasElem) - 1
	if b.hasElem[top] {
		b.raw(",")
	}
	b.hasElem[top] = true
	b.raw(jsonKey(name))
}

func (b *opsBuilder) add(u unit) {
	if len(b.hasElem) == 0 {
		b.hasElem = []bool{false}
	}
	c := 0
	for c < len(b.stack) && c < len(u.structs) && b.stack[c] == u.structs[c] {
		c++
	}
	for len(b.stack) > c {
		b.raw("}")
		b.stack = b.stack[:len(b.stack)-1]
		b.hasElem = b.hasElem[:len(b.hasElem)-1]
	}
	for _, s := range u.structs[c:] {
		b.entry(s)
		b.raw("{")
		b.stack = append(b.stack, s)
		b.hasElem = append(b.hasElem, false)
	}
	b.entry(u.name)
	if !u.array {
		b.value(u.leaves[0])
		return
	}
	b.raw("[")
	for i, l := range u.leaves {
		if i > 0 {
			b.raw(",")
		}
		b.value(l)
	}
	b.raw("]")
}

func (b *opsBuilder) finish(no int) Part {
	if len(b.stack) > 0 {
		b.raw(strings.Repeat("}", len(b.stack)))
	}
	return Part{No: no, Ops: b.ops, Leaves: b.leaves, MaxBytes: b.size}
}

// jsonKey schreibt "Name": – Namen sind schon geprüft (validName), Sonder-
// zeichen werden trotzdem nach JSON übersetzt.
func jsonKey(name string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range name {
		switch {
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r < 0x20:
			sb.WriteString(`\u00`)
			sb.WriteString(strconv.FormatInt(int64(r)>>4, 16))
			sb.WriteString(strconv.FormatInt(int64(r)&0xF, 16))
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteString(`":`)
	return sb.String()
}
