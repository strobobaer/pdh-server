// Package listsort enthält die gemeinsame Sortier- und Zusammenfass-Logik der
// Listen (Historie, Maßnahmen, Kommentare). Reine Funktionen, ohne Datenbank.
package listsort

import (
	"sort"
	"strings"
	"time"

	"pdh/internal/core/textmatch"
)

// Mode ist die Kennung einer Sortierart.
type Mode string

const (
	Newest   Mode = "new"    // Neueste zuerst
	Oldest   Mode = "old"    // Älteste zuerst
	Frequent Mode = "freq"   // Häufigste zuerst (zusammengefasst)
	Same     Mode = "same"   // Gleiches zusammen (zusammengefasst)
	Person   Mode = "person" // Nach Person
	Kind     Mode = "kind"   // Nach Art
	Due      Mode = "due"    // Nach Termin
)

// Grouped: wahr, wenn die Art Einträge zu Gruppen zusammenfasst.
func (m Mode) Grouped() bool { return m == Frequent || m == Same }

// Parse liefert s, wenn es in allowed steht, sonst def.
func Parse(s string, allowed []Mode, def Mode) Mode {
	for _, a := range allowed {
		if s != "" && Mode(s) == a {
			return a
		}
	}
	return def
}

// Key: gleicher Schlüssel heißt gleicher Text (Groß/Klein, Umlaute, Satz- und
// Leerzeichen egal). Ähnliche Texte werden nicht zusammengelegt.
func Key(text string) string { return strings.Join(textmatch.Words(text), " ") }

// Item ist ein Listeneintrag in der für die Sortierung nötigen Form.
type Item struct {
	At     time.Time
	Key    string
	Label  string
	Person string
	Kind   string
	Due    time.Time
}

// Order liefert die Reihenfolge als Indexliste für new, old, person, kind und
// due. Stabil: bei Gleichstand gilt der ursprüngliche Index. Andere Arten
// (und unbekannte) ergeben die Ursprungsreihenfolge.
func Order(items []Item, m Mode) []int {
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	newer := func(a, b int) (less, decided bool) {
		if !items[a].At.Equal(items[b].At) {
			return items[a].At.After(items[b].At), true
		}
		return false, false
	}
	var less func(a, b int) (bool, bool)
	switch m {
	case Newest:
		less = newer
	case Oldest:
		less = func(a, b int) (bool, bool) {
			if !items[a].At.Equal(items[b].At) {
				return items[a].At.Before(items[b].At), true
			}
			return false, false
		}
	case Person:
		less = func(a, b int) (bool, bool) {
			pa, pb := strings.TrimSpace(items[a].Person), strings.TrimSpace(items[b].Person)
			if (pa == "") != (pb == "") {
				return pb == "", true // ohne Name ans Ende
			}
			// nach dem Schlüssel (Umlaute und Groß/Klein gefaltet: „Özlem“ steht bei „O“, nicht hinter „Zoe“),
			// bei gleichem Schlüssel nach dem Namen ohne Groß/Klein
			if ka, kb := Key(pa), Key(pb); ka != kb {
				return ka < kb, true
			}
			if la, lb := strings.ToLower(pa), strings.ToLower(pb); la != lb {
				return la < lb, true
			}
			return newer(a, b)
		}
	case Kind:
		less = func(a, b int) (bool, bool) {
			if items[a].Kind != items[b].Kind {
				return items[a].Kind < items[b].Kind, true
			}
			return newer(a, b)
		}
	case Due:
		less = func(a, b int) (bool, bool) {
			da, db := items[a].Due, items[b].Due
			if da.IsZero() != db.IsZero() {
				return db.IsZero(), true // ohne Termin ans Ende
			}
			if !da.Equal(db) {
				return da.Before(db), true
			}
			return false, false
		}
	default:
		return idx
	}
	sort.SliceStable(idx, func(i, j int) bool {
		l, _ := less(idx[i], idx[j])
		return l
	})
	return idx
}

// Group fasst Einträge mit gleichem Schlüssel zusammen.
type Group struct {
	Key, Label string
	Count      int
	Last       time.Time
	Idx        []int // neueste zuerst
}

const noText = "(ohne Text)"

// Groups bildet Gruppen für freq (Anzahl absteigend, bei Gleichstand die
// jüngste Gruppe zuerst) und same (Schlüssel aufsteigend). Label ist das
// Label des jüngsten Eintrags; ein leerer Schlüssel ergibt eine Gruppe
// „(ohne Text)“.
func Groups(items []Item, m Mode) []Group {
	pos := map[string]int{}
	var gs []Group
	for _, i := range Order(items, Newest) { // neueste zuerst
		k := items[i].Key
		p, ok := pos[k]
		if !ok {
			label := items[i].Label
			if k == "" {
				label = noText
			}
			p = len(gs)
			pos[k] = p
			gs = append(gs, Group{Key: k, Label: label, Last: items[i].At})
		}
		gs[p].Count++
		gs[p].Idx = append(gs[p].Idx, i)
	}
	switch m {
	case Frequent:
		sort.SliceStable(gs, func(a, b int) bool {
			if gs[a].Count != gs[b].Count {
				return gs[a].Count > gs[b].Count
			}
			return gs[a].Last.After(gs[b].Last)
		})
	case Same:
		sort.SliceStable(gs, func(a, b int) bool { return gs[a].Key < gs[b].Key })
	}
	return gs
}
