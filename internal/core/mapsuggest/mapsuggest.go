// Package mapsuggest schlägt Zuordnungen vor, ohne Sprachmodell: Zu jedem
// Quellwert (MQTT-Thema, Spalte einer Abfrage, OPC-UA-Knoten …) sucht es die
// Anlage mit dem ähnlichsten Namen, leitet eine lesbare Bezeichnung ab und
// nennt eine vermutete Einheit. Für Exporte baut es einen Feldnamen.
package mapsuggest

import (
	"strings"
	"unicode"
)

// Source ist ein Quellwert: Ref wie in import_mappings.source_ref, Label der
// lesbare Name („halle2/presse3/strom“, „Abfrage › Spalte“), Sample ein
// zuletzt gesehener Wert.
type Source struct {
	Ref, Label, Sample string
}

// Asset ist eine Anlage mit Namen und Pfad („Werk › Halle 2 › Presse 3“).
type Asset struct {
	ID, Name, Path string
}

// Suggestion ist der Vorschlag zu einer Quelle; ohne passende Anlage bleiben
// AssetID und AssetPath leer und Score ist 0.
type Suggestion struct {
	Source
	AssetID, AssetPath, Name, Note string
	Score                          float64
}

const (
	minScore  = 0.6
	maxScore  = 1.5
	maxName   = 150
	maxField  = 150
	pathBonus = 0.1
)

var umlauts = map[rune]string{'ä': "ae", 'ö': "oe", 'ü': "ue", 'Ä': "Ae", 'Ö': "Oe", 'Ü': "Ue", 'ß': "ss"}

// Tokens zerlegt einen Namen in kleingeschriebene Wörter: Umlaute
// umgeschrieben, Trenner sind alle Zeichen außer Buchstaben und Ziffern, dazu
// Wechsel von klein auf groß (camelCase) und zwischen Buchstabe und Ziffer.
// Zahlen verlieren führende Nullen („03“ → „3“).
func Tokens(s string) []string {
	var out []string
	var cur strings.Builder
	var prev rune
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		w := strings.ToLower(cur.String())
		if isDigits(w) {
			if t := strings.TrimLeft(w, "0"); t != "" {
				w = t
			} else {
				w = "0"
			}
		}
		out = append(out, w)
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			if prev != 0 && (unicode.IsDigit(prev) || (unicode.IsLower(prev) && unicode.IsUpper(r))) {
				flush()
			}
			if u, ok := umlauts[r]; ok {
				cur.WriteString(u)
			} else {
				cur.WriteRune(r)
			}
		case unicode.IsDigit(r):
			if prev != 0 && unicode.IsLetter(prev) {
				flush()
			}
			cur.WriteRune(r)
		default:
			flush()
			r = 0
		}
		prev = r
	}
	flush()
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type assetWords struct {
	a         Asset
	name      []string
	nums      []string
	path      map[string]bool // Wörter des Pfads ohne die des Namens
	covered   map[string]bool // Name und Pfad zusammen
	depth     int
	pairs     [][2]string // Zahl des Namens mit dem Wort davor
	pathPairs [][2]string // dasselbe für die Ebenen darüber
}

const scoreEps = 1e-9

func same(a, b float64) bool { return a-b < scoreEps && b-a < scoreEps }

func prepare(assets []Asset) []assetWords {
	out := make([]assetWords, 0, len(assets))
	for _, a := range assets {
		raw := Tokens(a.Name)
		name := uniq(raw)
		if len(name) == 0 {
			continue
		}
		w := assetWords{a: a, name: name, path: map[string]bool{}, covered: map[string]bool{}, depth: strings.Count(a.Path, "›")}
		for _, t := range name {
			w.covered[t] = true
		}
		w.nums, w.pairs = numberPairs(raw)
		// Ebenen über der Anlage: Wörter zählen einzeln, Zahlen nur zusammen
		// mit dem Wort davor („Halle 2“ ≠ „halle3/presse2“)
		segs := strings.Split(a.Path, "›")
		for n, seg := range segs {
			ts := Tokens(seg)
			for _, t := range ts {
				w.covered[t] = true
			}
			if n == len(segs)-1 {
				break // die Anlage selbst steht in name
			}
			_, pairs := numberPairs(ts)
			w.pathPairs = append(w.pathPairs, pairs...)
			for _, t := range ts {
				if !isDigits(t) && !contains(name, t) {
					w.path[t] = true
				}
			}
		}
		out = append(out, w)
	}
	return out
}

// numberPairs: Zahlen eines Namens – mit dem Wort davor als Paar, ohne Wort
// davor einzeln.
func numberPairs(ts []string) (single []string, pairs [][2]string) {
	for i, t := range ts {
		if !isDigits(t) {
			continue
		}
		if i == 0 || isDigits(ts[i-1]) {
			single = append(single, t)
			continue
		}
		pairs = append(pairs, [2]string{ts[i-1], t})
	}
	return single, pairs
}

func uniq(ts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func contains(ts []string, t string) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

// score: Anteil der Namenswörter in der Quelle plus 0,1 je Wort der Ebenen
// darüber. Eine Zahl des Anlagennamens zählt nur direkt hinter ihrem Wort
// („Presse 3“ ≠ „presse30“, ≠ „linie3/presse30“); fehlt sie, ist es kein
// Treffer.
func (w assetWords) score(src map[string]bool, next map[[2]string]bool) float64 {
	for _, n := range w.nums {
		if !src[n] {
			return 0
		}
	}
	for _, p := range w.pairs {
		if !next[p] {
			return 0
		}
	}
	hit := 0
	for _, t := range w.name {
		if src[t] {
			hit++
		}
	}
	s := float64(hit) / float64(len(w.name))
	for t := range w.path {
		if src[t] {
			s += pathBonus
		}
	}
	for _, p := range w.pathPairs {
		if next[p] {
			s += pathBonus
		}
	}
	if s > maxScore {
		s = maxScore
	}
	return s
}

// Suggest liefert je Quelle (in Eingabereihenfolge) die beste Anlage über der
// Schwelle, eine Bezeichnung und eine vermutete Einheit.
func Suggest(sources []Source, assets []Asset) []Suggestion {
	prepared := prepare(assets)
	out := make([]Suggestion, 0, len(sources))
	for _, s := range sources {
		words := map[string]bool{}
		next := map[[2]string]bool{} // Wortpaare, die direkt hintereinander stehen
		ts := Tokens(s.Label)
		for i, t := range ts {
			words[t] = true
			if i > 0 {
				next[[2]string{ts[i-1], t}] = true
			}
		}
		best, bestScore, tie := -1, 0.0, false
		for i, w := range prepared {
			sc := w.score(words, next)
			if sc < minScore {
				continue
			}
			switch {
			case best < 0 || sc > bestScore+scoreEps || (same(sc, bestScore) && w.depth > prepared[best].depth):
				best, bestScore, tie = i, sc, false
			case same(sc, bestScore) && w.depth == prepared[best].depth:
				tie = true // zwei gleich gute Anlagen: lieber keinen Vorschlag
			}
		}
		if tie {
			best, bestScore = -1, 0
		}
		sg := Suggestion{Source: s}
		var covered map[string]bool
		if best >= 0 {
			w := prepared[best]
			sg.AssetID, sg.AssetPath, sg.Score = w.a.ID, w.a.Path, bestScore
			covered = w.covered
		}
		sg.Name = readableName(s.Label, covered)
		sg.Note = UnitHint(s.Label, s.Sample)
		out = append(out, sg)
	}
	return out
}

// readableName: der Teil nach dem letzten „›“, zerlegt an „/“, „.“, „#“; Teile,
// die nur aus Wörtern der gefundenen Anlage bestehen, fallen weg.
func readableName(label string, covered map[string]bool) string {
	base := label
	if i := strings.LastIndex(base, "›"); i >= 0 {
		base = base[i+len("›"):]
	}
	parts := strings.FieldsFunc(base, func(r rune) bool { return r == '/' || r == '.' || r == '#' })
	var keep []string
	for _, p := range parts {
		ts := Tokens(p)
		if len(ts) == 0 {
			continue
		}
		all := covered != nil
		for _, t := range ts {
			if !covered[t] {
				all = false
				break
			}
		}
		if !all {
			keep = append(keep, humanize(p))
		}
	}
	name := strings.Join(keep, " ")
	if len([]rune(name)) < 2 && len(parts) > 0 {
		name = strings.TrimSpace(parts[len(parts)-1])
	}
	if len([]rune(name)) < 2 {
		name = strings.TrimSpace(label)
	}
	if rs := []rune(name); len(rs) > maxName {
		name = string(rs[:maxName])
	}
	return name
}

// humanize: „oel_temp“ → „Oel Temp“, „motorStromL1“ → „Motor Strom L1“.
func humanize(s string) string {
	var b strings.Builder
	var prev rune
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			b.WriteRune(' ')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
		prev = r
	}
	words := strings.Fields(b.String())
	for i, w := range words {
		rs := []rune(w)
		rs[0] = unicode.ToUpper(rs[0])
		words[i] = string(rs)
	}
	return strings.Join(words, " ")
}

// unitRules: Reihenfolge zählt (Energie vor Leistung). Kurze Stämme müssen ein
// ganzes Wort sein, längere dürfen in zusammengesetzten Wörtern stecken.
var unitRules = []struct {
	unit  string
	stems []string
}{
	{"kWh", []string{"energie", "energy", "kwh", "zaehler"}},
	{"kW", []string{"leistung", "power", "kw"}},
	{"A", []string{"strom", "current", "ampere"}},
	{"V", []string{"spannung", "voltage", "volt"}},
	{"°C", []string{"temp"}},
	{"bar", []string{"druck", "pressure", "bar"}},
	{"1/min", []string{"drehzahl", "rpm"}},
	{"%", []string{"feuchte", "humidity"}},
	{"Hz", []string{"frequenz", "frequency", "hz"}},
}

// UnitHint vermutet die Einheit aus dem Namen, sonst die Art aus dem Wert.
func UnitHint(label, sample string) string {
	ts := Tokens(label)
	for _, rule := range unitRules {
		for _, stem := range rule.stems {
			for _, t := range ts {
				if t == stem || (len(stem) > 3 && strings.Contains(t, stem)) {
					return "Einheit " + rule.unit
				}
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(sample)) {
	case "0", "1":
		return "Zustand (0/1)"
	case "true", "false":
		return "Zustand (ein/aus)"
	}
	return ""
}

// ExportFieldName baut einen Feldnamen „Anlage_Name“ aus Buchstaben, Ziffern
// und „_“ (Umlaute umgeschrieben), höchstens 150 Zeichen, sonst „Wert“.
func ExportFieldName(assetName, name string) string {
	var b strings.Builder
	for _, r := range assetName + "_" + name {
		switch {
		case umlauts[r] != "":
			b.WriteString(umlauts[r])
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "_")
	if len(s) > maxField {
		s = strings.TrimRight(s[:maxField], "_")
	}
	if len(s) < 2 {
		return "Wert"
	}
	return s
}
