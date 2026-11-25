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

// plantPalette: zwölf gut unterscheidbare Farben (Blau, Orange, Grün, Rot, Violett, Cyan, Rosa, Bernstein,
// Limone, Braun, Schiefer, Dunkeltürkis); ähnliche Töne (Smaragd/Türkis, Indigo/Violett) kommen nicht doppelt vor.
var plantPalette = []string{
	"#2563eb", "#ea580c", "#16a34a", "#dc2626", "#9333ea", "#0891b2",
	"#db2777", "#ca8a04", "#65a30d", "#7c2d12", "#475569", "#0f766e",
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

// plantColors ordnet den Anlagen ids je eine eigene Farbe zu. Für Diagramme immer die Liste ALLER Anlagen übergeben
// (globalInfra.plantColors), damit die Farbe einer Anlage nicht vom gezeigten Ausschnitt abhängt. Die IDs werden sortiert abgearbeitet (die Reihenfolge der
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

// plantColors: die feste Farbzuordnung über ALLE Anlagen des Systems. Die IDs sind UUIDv7, die sortierte Reihenfolge ist
// die Anlagenreihenfolge der Anlage: Eine neu angelegte Anlage kommt hinten und ändert keine vorhandene Farbe. Seite und
// Folie nutzen dieselbe Zuordnung, unabhängig von Zeitraum, Rechten und Auswahl.
func (gi *globalInfra) plantColors() map[string]string {
	ids := make([]string, 0, len(gi.nodes))
	for id := range gi.nodes {
		ids = append(ids, id)
	}
	return plantColors(ids)
}

// colorOf: Farbe aus der globalen Zuordnung; fehlt die Anlage dort (nur in Tests oder bei Datenlücken), die Wunschfarbe.
func colorOf(colors map[string]string, id string) string {
	if c, ok := colors[id]; ok {
		return c
	}
	return plantColor(id)
}
