package textmatch

import (
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	got := strings.Join(Tokens("Die Lagerung der Hydraulikpumpe ist undicht, Öl tritt aus!"), " ")
	if got != "lager hydraulikpump undicht oel tritt" {
		t.Errorf("Tokens: %q", got)
	}
	if strings.Join(Tokens("Lager Lagers Lagern"), " ") != "lager lager lager" {
		t.Error("Wortformen sollen zusammenfallen")
	}
}

func TestSimilarityRanking(t *testing.T) {
	docs := []string{
		"Hydraulikpumpe undicht Öl tritt aus",
		"Förderband steht, Motorschutz ausgelöst",
		"Pumpe verliert Hydrauliköl, Dichtung defekt",
		"Druckluft zu niedrig an Presse 3",
	}
	var toks [][]string
	for _, d := range docs {
		toks = append(toks, Tokens(d))
	}
	c := NewCorpus(toks)
	q := c.Vector(Tokens("Hydraulikpumpe verliert Öl"))
	var scores []float64
	for _, d := range toks {
		scores = append(scores, Cosine(q, c.Vector(d)))
	}
	r := Rank(scores, 0.1, 2)
	if len(r) == 0 || r[0].Index != 0 {
		t.Fatalf("bester Treffer falsch: %+v (%v)", r, scores)
	}
	if scores[1] > 0.05 {
		t.Errorf("Förderband sollte nicht ähnlich sein: %.2f", scores[1])
	}
	if Cosine(nil, q) != 0 {
		t.Error("leerer Vektor")
	}
}
