package tiamqtt

// Go-Gegenstück zum JSON-Aufbau im SCL-Baustein: Render schreibt eine
// Nachricht Byte für Byte so, wie es der erzeugte SCL-Code tut. Die Tests
// des SCL-Erzeugers vergleichen dagegen.

import (
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// LRealMaxJSON ist der größte LReal-Betrag, der noch als Zahl geschrieben
// wird. Darüber (bis zur größten LReal-Zahl) rundet das Format mit 15
// Stellen auf 1.79769313486232E+308 – das ist größer als jede LReal-Zahl
// und JSON-Leser (auch PDH) weisen die ganze Nachricht ab. Größere Beträge
// werden deshalb wie unendlich als null geschrieben.
const LRealMaxJSON = 1.79769313486231e308

// Render schreibt Teil p (von parts Teilen) mit Zeitstempel ts (wird nach
// UTC umgerechnet). get liefert den Wert zu Leaf.Path: bool, int64 für
// Ganzzahlen und Word/DWord, float32 für Real, float64 für LReal, string
// (Bytes wie in der Steuerung, Latin-1) für String, time.Time für DTL/DT.
// Fehlt ein Wert oder hat er den falschen Typ, steht null; ebenso bei NaN,
// ±unendlich und LReal-Beträgen über LRealMaxJSON.
func Render(p Part, parts int, ts time.Time, get func(path string) any) []byte {
	b := make([]byte, 0, p.MaxBytes)
	b = append(b, `{"ts":"`...)
	b = ts.UTC().AppendFormat(b, "2006-01-02T15:04:05Z")
	b = append(b, `","part":`...)
	b = strconv.AppendInt(b, int64(p.No), 10)
	b = append(b, `,"parts":`...)
	b = strconv.AppendInt(b, int64(parts), 10)
	b = append(b, `,"values":{`...)
	for _, op := range p.Ops {
		if op.Kind == OpRaw {
			b = append(b, op.Raw...)
			continue
		}
		b = appendValue(b, *op.Leaf, get(op.Leaf.Path))
	}
	return append(b, `}}`...)
}

func appendValue(b []byte, l Leaf, v any) []byte {
	switch l.Kind {
	case KindBool:
		if x, ok := v.(bool); ok {
			return strconv.AppendBool(b, x)
		}
	case KindInt, KindDInt, KindUInt, KindUDInt, KindWord, KindDWord:
		if x, ok := v.(int64); ok {
			return strconv.AppendInt(b, x, 10)
		}
	case KindReal:
		if x, ok := v.(float32); ok && !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0) {
			return strconv.AppendFloat(b, float64(x), 'E', 6, 32)
		}
	case KindLReal:
		if x, ok := v.(float64); ok && !math.IsNaN(x) && math.Abs(x) <= LRealMaxJSON {
			return strconv.AppendFloat(b, x, 'E', 14, 64)
		}
	case KindString:
		if x, ok := v.(string); ok {
			return appendString(b, x, l.StrLen)
		}
	case KindDTL, KindDT:
		if x, ok := v.(time.Time); ok {
			b = append(b, '"')
			b = x.AppendFormat(b, "2006-01-02T15:04:05")
			return append(b, '"')
		}
	}
	return append(b, "null"...)
}

// appendString schreibt die Bytes einer S7-Zeichenkette (höchstens strLen)
// als JSON-String: " und \ mit \, Steuerzeichen als Leerzeichen, Bytes ab
// 0x80 als Latin-1-Zeichen in UTF-8.
func appendString(b []byte, s string, strLen int) []byte {
	if len(s) > strLen {
		s = s[:strLen]
	}
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, ' ')
		case c < 0x80:
			b = append(b, c)
		default:
			b = utf8.AppendRune(b, rune(c))
		}
	}
	return append(b, '"')
}

// WorstCase liefert zu l den Wert mit der längsten JSON-Form.
func WorstCase(l Leaf) any {
	switch l.Kind {
	case KindBool:
		return false
	case KindInt:
		return int64(math.MinInt16)
	case KindDInt:
		return int64(math.MinInt32)
	case KindUInt, KindWord:
		return int64(math.MaxUint16)
	case KindUDInt, KindDWord:
		return int64(math.MaxUint32)
	case KindReal:
		return float32(-math.MaxFloat32)
	case KindLReal:
		return -LRealMaxJSON
	case KindString:
		s := make([]byte, l.StrLen)
		for i := range s {
			s[i] = 0xE4 // ä: zwei Byte in UTF-8
		}
		return string(s)
	case KindDTL, KindDT:
		return time.Date(2554, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	return nil
}
