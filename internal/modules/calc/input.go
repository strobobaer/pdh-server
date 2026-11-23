package calc

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"pdh/internal/core/formula"
)

var numberRe = regexp.MustCompile(`^[+-]?\d+([.,]\d+)?$`)

var truthWords = map[string]float64{
	"true": 1, "on": 1, "ein": 1, "ja": 1,
	"false": 0, "off": 0, "aus": 0, "nein": 0,
}

// ParseInput liest einen Eingangswert als Zahl: genau eine Zahl mit Punkt
// oder Komma (ohne Tausendertrennung, ohne Einheit, ohne Exponent) oder ein
// Wahrheitswort (true/false, on/off, ein/aus, ja/nein → 1/0). Alles andere
// ist keine Zahl – nie 0.
func ParseInput(raw string) (float64, bool) {
	t := strings.TrimSpace(raw)
	if v, ok := truthWords[strings.ToLower(t)]; ok {
		return v, true
	}
	if !numberRe.MatchString(t) {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Replace(t, ",", ".", 1), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// FormatValue schreibt das Ergebnis so, wie es an der Zuordnung steht:
// kaufmännisch gerundet, mit Dezimalpunkt, ohne Exponent.
func FormatValue(v float64, decimals int) string {
	s := strconv.FormatFloat(formula.Round(v, decimals), 'f', decimals, 64)
	if strings.HasPrefix(s, "-") && strings.Trim(s, "-0.") == "" {
		s = s[1:] // „-0.00“ ist 0
	}
	return s
}
