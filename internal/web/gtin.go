package web

import "strings"

// normGTIN: EAN/GTIN in Ziffernform (8, 12, 13 oder 14 Stellen) mit gültiger
// Prüfziffer, sonst "". Leerzeichen und Bindestriche werden entfernt.
func normGTIN(s string) string {
	s = strings.NewReplacer(" ", "", "-", "", ".", "").Replace(strings.TrimSpace(s))
	switch len(s) {
	case 8, 12, 13, 14:
	default:
		return ""
	}
	sum := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return ""
		}
		if i == len(s)-1 {
			break
		}
		d := int(c - '0')
		// von rechts (ohne Prüfziffer) abwechselnd ×3 und ×1
		if (len(s)-1-i)%2 == 1 {
			d *= 3
		}
		sum += d
	}
	if (10-sum%10)%10 != int(s[len(s)-1]-'0') {
		return ""
	}
	return s
}
