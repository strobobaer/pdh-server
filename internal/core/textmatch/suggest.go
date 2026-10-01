package textmatch

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Suggester lernt aus vorhandenen Texten einer Feldart (z. B. alle
// Massnahmen) und schlaegt waehrend der Eingabe ganze Formulierungen und das
// naechste Wort vor. Uebernommene Vorschlaege (Feedback) zaehlen mehr.
type Suggester struct {
	phrases map[string]*phrase            // normalisiert -> Formulierung
	next1   map[string]map[string]float64 // letztes Wort -> naechstes Wort
	next2   map[string]map[string]float64 // letzte zwei Woerter -> naechstes Wort
	words   map[string]*phrase            // Wortschatz fuer Wortvervollstaendigung
	now     time.Time
}

type phrase struct {
	Text     string
	Count    float64
	Accepted float64
	Last     time.Time
	forms    map[string]int // Schreibweisen (Gross/Klein) -> Haeufigkeit
}

// Sample: ein gespeicherter Text.
type Sample struct {
	Text string
	At   time.Time
}

const maxPhraseLen = 160

// NewSuggester baut den Index. lines=true: jede Zeile ist eine eigene
// Formulierung (Stichpunkte); sonst werden laengere Texte in Saetze geteilt.
func NewSuggester(samples []Sample, lines bool, now time.Time) *Suggester {
	s := &Suggester{phrases: map[string]*phrase{}, next1: map[string]map[string]float64{},
		next2: map[string]map[string]float64{}, words: map[string]*phrase{}, now: now}
	for _, smp := range samples {
		for _, part := range splitPhrases(smp.Text, lines) {
			s.addPhrase(part, smp.At)
		}
	}
	return s
}

func splitPhrases(text string, lines bool) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var parts []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-•*·"))
		if l == "" {
			continue
		}
		if lines {
			parts = append(parts, l)
			continue
		}
		// Fliesstext in Saetze teilen
		start := 0
		rs := []rune(l)
		for i, r := range rs {
			if (r == '.' || r == '!' || r == '?' || r == ';') && (i+1 == len(rs) || rs[i+1] == ' ') {
				if p := strings.TrimSpace(string(rs[start : i+1])); p != "" {
					parts = append(parts, p)
				}
				start = i + 1
			}
		}
		if p := strings.TrimSpace(string(rs[start:])); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// rawWords: Woerter in Originalschreibweise.
func rawWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '/' && r != ','
	})
}

func (s *Suggester) addPhrase(text string, at time.Time) {
	if r := []rune(text); len(r) > maxPhraseLen {
		text = string(r[:maxPhraseLen])
	}
	key := Normalize(text)
	p := s.phrases[key]
	if p == nil {
		p = &phrase{Text: text, forms: map[string]int{}}
		s.phrases[key] = p
	}
	p.Count++
	p.forms[text]++
	if p.forms[text] > p.forms[p.Text] {
		p.Text = text
	}
	if at.After(p.Last) {
		p.Last = at
	}
	ws := rawWords(text)
	for i, w := range ws {
		w = strings.TrimRight(w, ",")
		lw := Normalize(w)
		if len([]rune(lw)) >= 3 {
			wp := s.words[lw]
			if wp == nil {
				wp = &phrase{Text: w, forms: map[string]int{}}
				s.words[lw] = wp
			}
			wp.Count++
			wp.forms[w]++
			if wp.forms[w] > wp.forms[wp.Text] {
				wp.Text = w
			}
		}
		if i >= 1 {
			addNext(s.next1, Normalize(strings.TrimRight(ws[i-1], ",")), w)
		}
		if i >= 2 {
			addNext(s.next2, Normalize(strings.TrimRight(ws[i-2], ","))+" "+Normalize(strings.TrimRight(ws[i-1], ",")), w)
		}
	}
}

func addNext(m map[string]map[string]float64, ctx, w string) {
	w = strings.TrimRight(w, ",")
	if m[ctx] == nil {
		m[ctx] = map[string]float64{}
	}
	m[ctx][w]++
}

// Feedback: uebernommene Formulierung staerker gewichten (und lernen, falls neu).
func (s *Suggester) Feedback(text string, accepted int) {
	key := Normalize(strings.TrimSpace(text))
	if key == "" {
		return
	}
	p := s.phrases[key]
	if p == nil {
		s.addPhrase(strings.TrimSpace(text), s.now)
		p = s.phrases[key]
	}
	p.Accepted += float64(accepted)
}

func (s *Suggester) score(p *phrase) float64 {
	sc := math.Log1p(p.Count) + 1.5*math.Log1p(p.Accepted)
	if !p.Last.IsZero() {
		if days := s.now.Sub(p.Last).Hours() / 24; days < 90 {
			sc += 0.6 * (1 - days/90)
		}
	}
	return sc
}

// Result: Ergaenzung der aktuellen Zeile (Ghost) und ganze Formulierungen.
type Result struct {
	Ghost string   `json:"ghost"` // Text, der an die Eingabe angehaengt wuerde
	Items []string `json:"items"` // vollstaendige Formulierungen fuer die Zeile
}

// Suggest liefert Vorschlaege fuer die aktuelle Zeile.
func (s *Suggester) Suggest(line string, limit int) Result {
	res := Result{Items: []string{}}
	trimmed := strings.TrimLeft(line, " \t-•*·")
	if len([]rune(strings.TrimSpace(trimmed))) < 2 {
		return res
	}
	key := Normalize(trimmed)
	type cand struct {
		text  string
		score float64
	}
	var prefix, contains []cand
	qWords := Words(trimmed)
	for k, p := range s.phrases {
		if k == key {
			continue
		}
		if strings.HasPrefix(k, key) {
			prefix = append(prefix, cand{p.Text, s.score(p)})
			continue
		}
		if len(qWords) > 0 && len([]rune(key)) >= 3 && containsAllPrefixes(k, qWords) {
			contains = append(contains, cand{p.Text, s.score(p) - 1})
		}
	}
	sortC := func(c []cand) {
		sort.Slice(c, func(a, b int) bool {
			if c[a].score != c[b].score {
				return c[a].score > c[b].score
			}
			return c[a].text < c[b].text
		})
	}
	sortC(prefix)
	sortC(contains)
	for _, c := range append(prefix, contains...) {
		if len(res.Items) >= limit {
			break
		}
		res.Items = append(res.Items, c.text)
	}
	// Ghost: Rest der besten Formulierung, die genau so beginnt …
	if len(prefix) > 0 {
		rs := []rune(prefix[0].text)
		res.Ghost = string(rs[len([]rune(trimmed)):])
		return res
	}
	// … sonst naechstes Wort bzw. Wortende
	ws := rawWords(trimmed)
	if len(ws) == 0 {
		return res
	}
	if strings.HasSuffix(line, " ") {
		var m map[string]float64
		if len(ws) >= 2 {
			m = s.next2[Normalize(ws[len(ws)-2])+" "+Normalize(ws[len(ws)-1])]
		}
		if len(m) == 0 {
			m = s.next1[Normalize(ws[len(ws)-1])]
		}
		res.Ghost = best(m)
		return res
	}
	part := Normalize(ws[len(ws)-1])
	if len([]rune(part)) < 2 {
		return res
	}
	var bestW *phrase
	for k, wp := range s.words {
		if len(k) > len(part) && strings.HasPrefix(k, part) && (bestW == nil || wp.Count > bestW.Count || wp.Count == bestW.Count && wp.Text < bestW.Text) {
			bestW = wp
		}
	}
	if bestW != nil {
		lastRaw := []rune(ws[len(ws)-1])
		if br := []rune(bestW.Text); len(br) > len(lastRaw) {
			res.Ghost = string(br[len(lastRaw):])
		}
	}
	return res
}

func containsAllPrefixes(key string, qWords []string) bool {
	kw := Words(key)
	for _, q := range qWords {
		found := false
		for _, w := range kw {
			if strings.HasPrefix(w, q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func best(m map[string]float64) string {
	var bw string
	var bc float64
	for w, c := range m {
		if c > bc || c == bc && w < bw {
			bw, bc = w, c
		}
	}
	return bw
}

// Size: Anzahl gelernter Formulierungen (fuer Tests/Diagnose).
func (s *Suggester) Size() int { return len(s.phrases) }
