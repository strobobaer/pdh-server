// Package textmatch: einfache deutsche Textaufbereitung und Aehnlichkeit
// (TF-IDF, Kosinus) fuer die Copilot-Suche nach aehnlichen Stoerungen und
// die lernende Autovervollstaendigung. Bewusst ohne externe Abhaengigkeiten.
package textmatch

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

var fold = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")

// Fuellwoerter, die fuer die Aehnlichkeit nichts aussagen.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`der die das den dem des ein eine einer eines einem einen und oder aber
		ist sind war waren wird werden wurde wurden hat haben hatte bei mit von vom zu zum zur im in an am auf aus
		fuer ueber unter nach vor nicht kein keine keinen noch nur auch sehr mehr wie was wo wenn dann dass da
		es er sie wir ich man sich sein seine ihr ihre als bis durch gegen ohne um so schon wieder immer mal
		bitte ca etc usw the and of to is`) {
		stopwords[w] = true
	}
}

// Normalize: Kleinschreibung, Umlaute ausgeschrieben.
func Normalize(s string) string { return fold.Replace(strings.ToLower(s)) }

// Words zerlegt in normalisierte Woerter (ohne Stoppwort-Filter, fuer Vorhersagen).
func Words(s string) []string {
	return strings.FieldsFunc(Normalize(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// stem: grobes Abschneiden haeufiger deutscher Endungen ("Lager", "Lagers",
// "Lagerung" -> "lager"), damit Wortformen zusammenpassen.
func stem(w string) string {
	if len(w) <= 4 {
		return w
	}
	for _, suf := range []string{"ungen", "ung", "heit", "keit", "chen", "en", "er", "es", "em", "e", "s", "n"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// Tokens: Woerter ohne Stoppwoerter, gestemmt; Zahlen mit Einheit bleiben erhalten.
func Tokens(s string) []string {
	var out []string
	for _, w := range Words(s) {
		if len(w) < 2 || stopwords[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

// Corpus berechnet TF-IDF-Vektoren ueber eine Dokumentmenge.
type Corpus struct {
	df   map[string]int
	docs int
}

func NewCorpus(docs [][]string) *Corpus {
	c := &Corpus{df: map[string]int{}, docs: len(docs)}
	for _, d := range docs {
		seen := map[string]bool{}
		for _, t := range d {
			if !seen[t] {
				seen[t] = true
				c.df[t]++
			}
		}
	}
	return c
}

// Vector: gewichteter Vektor eines Dokuments (seltene Begriffe zaehlen mehr).
func (c *Corpus) Vector(tokens []string) map[string]float64 {
	v := map[string]float64{}
	for _, t := range tokens {
		v[t]++
	}
	for t, tf := range v {
		idf := math.Log(1 + float64(c.docs+1)/float64(c.df[t]+1))
		v[t] = (1 + math.Log(tf)) * idf
	}
	return v
}

// Cosine: Aehnlichkeit 0..1.
func Cosine(a, b map[string]float64) float64 {
	var dot, na, nb float64
	for t, x := range a {
		na += x * x
		if y, ok := b[t]; ok {
			dot += x * y
		}
	}
	for _, y := range b {
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// Scored: Ergebnis einer Rangfolge.
type Scored struct {
	Index int
	Score float64
}

// Rank liefert die besten Treffer (Score >= min), absteigend.
func Rank(scores []float64, min float64, limit int) []Scored {
	var out []Scored
	for i, s := range scores {
		if s >= min {
			out = append(out, Scored{i, s})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
