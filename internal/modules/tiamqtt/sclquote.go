package tiamqtt

// Sicheres Einsetzen von Anwendertexten in SCL-Quellen. Alles, was vom
// Anwender kommt (Topic, Client-ID, Benutzer, Kennwort, Variablennamen,
// Schlüssel im JSON), geht nur über diese Funktionen in eine Vorlage.

import (
	"fmt"
	"strings"
	"unicode"
)

// PasswordPlaceholder steht in der Konfiguration, wenn das Kennwort nicht
// mitgeliefert wird.
const PasswordPlaceholder = "PASSWORT_EINTRAGEN"

// SCLString schreibt s als SCL-Zeichenkette '…'. $ und ' werden mit $
// maskiert, Bytes unter 0x20 und ab 0x80 (also auch jedes Byte eines
// UTF-8-Zeichens) als $hh. Die Steuerung bekommt so genau die Bytes von s.
// Länger als max Byte → Fehler.
func SCLString(s string, max int) (string, error) {
	if len(s) > max {
		return "", fmt.Errorf("Text ist %d Byte lang, erlaubt sind höchstens %d", len(s), max)
	}
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '$' || c == '\'':
			b.WriteByte('$')
			b.WriteByte(c)
		case c < 0x20 || c >= 0x7F:
			fmt.Fprintf(&b, "$%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String(), nil
}

// SCLName schreibt einen Bausteinnamen "…": nur A–Z, a–z, 0–9 und _, jedes
// andere Zeichen wird zu _.
func SCLName(s string) string {
	return `"` + plainName(s) + `"`
}

// plainName ist s mit nur A–Z, a–z, 0–9 und _ (auch für Dateinamen).
func plainName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && (r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// sclIdent schreibt einen Variablen- oder DB-Namen aus der Quelle des
// Anwenders als "…". Umlaute bleiben (die Quelle ist UTF-8), " und
// Steuerzeichen werden zu _ (der Leser lässt sie ohnehin nicht zu).
func sclIdent(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || unicode.IsControl(r) || r == unicode.ReplacementChar {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sclComment macht aus s einen Text für eine //-Kommentarzeile.
func sclComment(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// sclPathSegments zerlegt einen SCL-Pfad (Motor.Drehzahl, "Zähler".Wert,
// Feld[3]) in Namen und Indizes. Namen enthalten kein . [ ] " (siehe
// validName), deshalb ist das eindeutig. ok = false bei unbekannter Form.
type pathSeg struct {
	name  string
	index string // leer oder die Zahl im [ ]
}

func sclPathSegments(p string) ([]pathSeg, bool) {
	var out []pathSeg
	p = strings.TrimSpace(p)
	for p != "" {
		var seg pathSeg
		if p[0] == '"' {
			end := strings.IndexByte(p[1:], '"')
			if end < 0 {
				return nil, false
			}
			seg.name = p[1 : 1+end]
			p = p[2+end:]
		} else {
			end := strings.IndexAny(p, ".[ \t")
			if end < 0 {
				end = len(p)
			}
			seg.name = p[:end]
			p = p[end:]
		}
		if seg.name == "" {
			return nil, false
		}
		p = strings.TrimLeft(p, " \t")
		if strings.HasPrefix(p, "[") {
			end := strings.IndexByte(p, ']')
			if end < 0 {
				return nil, false
			}
			seg.index = strings.ReplaceAll(strings.TrimSpace(p[1:end]), " ", "")
			if !isInteger(seg.index) {
				return nil, false
			}
			p = strings.TrimLeft(p[end+1:], " \t")
		}
		out = append(out, seg)
		if p == "" {
			break
		}
		if p[0] != '.' {
			return nil, false
		}
		p = strings.TrimLeft(p[1:], " \t")
		if p == "" {
			return nil, false
		}
	}
	return out, len(out) > 0
}

func isInteger(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// sclRef schreibt einen Pfad mit jedem Namen in "…" (Feld[3] → "Feld"[3]).
func sclRef(segs []pathSeg) string {
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(sclIdent(s.name))
		if s.index != "" {
			b.WriteString("[" + s.index + "]")
		}
	}
	return b.String()
}

// pathKey ist ein Vergleichsschlüssel ohne Groß-/Kleinschreibung.
func pathKey(segs []pathSeg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(strings.ToLower(s.name))
		b.WriteString("[" + s.index + "]\x00")
	}
	return b.String()
}

// safeValue prüft einen Startwert aus der Quelle des Anwenders, bevor er
// in den Datentyp bzw. den BEGIN-Teil übernommen wird. Kommentare fallen
// weg; erlaubt sind nur Zahlen, Literale wie 16#FF, T#5s, DTL#…, TRUE,
// Wiederholungen 2(0), Felder [1, 2] und SCL-Zeichenketten '…'. Damit
// kann ein Startwert die Anweisung nicht verlassen.
func safeValue(v string) (string, bool) {
	var b strings.Builder
	space := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
			b.WriteByte(' ')
		}
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\'':
			j := closingQuote(v, i, '\'')
			if j < 0 {
				return "", false
			}
			b.WriteString(v[i : j+1])
			i = j
		case strings.HasPrefix(v[i:], "//"):
			end := strings.IndexByte(v[i:], '\n')
			if end < 0 {
				i = len(v)
			} else {
				i += end
			}
			space()
		case strings.HasPrefix(v[i:], "(*"):
			end := strings.Index(v[i+2:], "*)")
			if end < 0 {
				return "", false
			}
			i += end + 3
			space()
		case c == '\n' || c == '\r' || c == '\t' || c == ' ':
			space()
		case c < 0x80 && (c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte("_#.+-:,()[]", c) >= 0):
			b.WriteByte(c)
		default:
			return "", false
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || len(out) > 1000 {
		return "", false
	}
	return out, true
}
