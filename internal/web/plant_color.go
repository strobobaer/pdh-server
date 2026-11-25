package web

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// Farben der Anlagen in Diagrammen (Rüstzeiten-Auswertung und Statistikfolie stats_setup). Jede Anlage hat eine
// feste Farbe, abgeleitet aus ihrer ID (nicht aus dem Rang), damit sie auf Seite und Folie und von Tag zu Tag
// gleich bleibt. Die Palette ist in hellem und dunklem Theme lesbar (mittlere Helligkeit, weiße Beschriftung mit
// dunklem Rand im Kuchen). Innerhalb eines Diagramms bekommt jede Anlage eine eigene Farbe.

// plantPalette: gut unterscheidbare Farben (Blau, Orange, Grün, Rot, Violett, Cyan, Rosa, Limone, Bernstein,
// Türkis, Indigo, Braun, Magenta, Weinrot).
var plantPalette = []string{
	"#3b82f6", "#f97316", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#ec4899",
	"#84cc16", "#f59e0b", "#14b8a6", "#6366f1", "#a16207", "#d946ef", "#be123c",
}

// plantColorIndex: Wunschplatz einer Anlage in der Palette (FNV-1a über die kleingeschriebene ID).
func plantColorIndex(id string) int {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(id)))
	return int(h.Sum32() % uint32(len(plantPalette)))
}

// plantColor: die feste Wunschfarbe einer Anlage. Gilt für sich allein; in einem Diagramm mit mehreren Anlagen
// löst plantColors Kollisionen auf.
func plantColor(id string) string { return plantPalette[plantColorIndex(id)] }

// plantColors ordnet den Anlagen ids je eine eigene Farbe zu. Die IDs werden sortiert abgearbeitet (die Reihenfolge der
// Eingabe ändert nichts): Wunschfarbe aus plantColor, ist sie schon vergeben, die nächste freie Palettenfarbe;
// sind alle Palettenfarben vergeben, eine Farbe im Farbkreis mit dem Goldenen Winkel.
func plantColors(ids []string) map[string]string {
	uniq := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	sort.Strings(uniq)
	used := map[string]bool{}
	out := make(map[string]string, len(uniq))
	overflow := 0
	for _, id := range uniq {
		start := plantColorIndex(id)
		color := ""
		for k := 0; k < len(plantPalette); k++ {
			c := plantPalette[(start+k)%len(plantPalette)]
			if !used[c] {
				color = c
				break
			}
		}
		for color == "" {
			hue := math.Mod(float64(overflow)*137.50776405003785, 360)
			overflow++
			if c := hslToHex(hue, 0.62, 0.50); !used[c] {
				color = c
			}
		}
		used[color] = true
		out[id] = color
	}
	return out
}
