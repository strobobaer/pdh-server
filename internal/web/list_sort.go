package web

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"

	"pdh/internal/core/listsort"
)

// sortList: welche Sortierarten eine Listenart kennt und womit sie beginnt.
type sortList struct {
	Modes   []listsort.Mode
	Default listsort.Mode
}

// sortLists: Tabelle „Listenarten“ des Plans 2026-11-19-sortierung. Die
// Reihenfolge in Modes ist die Reihenfolge der Knöpfe.
var sortLists = map[string]sortList{
	"history":     {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Frequent, listsort.Same, listsort.Person, listsort.Kind}, listsort.Newest},
	"timeline":    {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Frequent, listsort.Same, listsort.Person, listsort.Kind}, listsort.Newest},
	"actions":     {[]listsort.Mode{listsort.Oldest, listsort.Newest, listsort.Frequent, listsort.Same, listsort.Person}, listsort.Oldest},
	"kvp_actions": {[]listsort.Mode{listsort.Due, listsort.Newest, listsort.Oldest, listsort.Frequent, listsort.Same, listsort.Person}, listsort.Due},
	"comments":    {[]listsort.Mode{listsort.Oldest, listsort.Newest, listsort.Frequent, listsort.Same, listsort.Person}, listsort.Oldest},
	"moves":       {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"obligations": {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"vpn":         {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"builds":      {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"backups":     {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"cleanup":     {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"sync":        {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Kind}, listsort.Newest},
	"export":      {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Kind}, listsort.Newest},
	"hmi":         {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
	"serverlog":   {[]listsort.Mode{listsort.Newest, listsort.Oldest, listsort.Person, listsort.Kind}, listsort.Newest},
}

// storedListSort: gemerkte Wahl des Benutzers (Vorrat aus dem Kontext, sonst eine Abfrage).
func (h *Handler) storedListSort(r *http.Request, uid string) map[string]string {
	if row := userRowFrom(r.Context(), uid); row != nil {
		return row.ListSort
	}
	if h.db == nil {
		return nil
	}
	var raw []byte
	if err := h.db.QueryRow(r.Context(), `SELECT list_sort FROM users WHERE id = $1::uuid`, uid).Scan(&raw); err != nil || len(raw) == 0 {
		return nil
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// sortMode: Sortierart einer Liste. Der Abfrageparameter ls_<list> gilt, wenn er
// für die Liste erlaubt ist, und wird für den angemeldeten Benutzer gemerkt
// (am Spiegel nicht – dort gilt die Wahl nur für den Aufruf). Sonst die
// gemerkte Wahl, sonst der Standard der Liste.
func (h *Handler) sortMode(r *http.Request, list string) listsort.Mode {
	sl, ok := sortLists[list]
	if !ok {
		return listsort.Newest
	}
	uid := getUser(r).ID
	var stored map[string]string
	if uid != "" {
		stored = h.storedListSort(r, uid)
	}
	if m := listsort.Parse(r.URL.Query().Get("ls_"+list), sl.Modes, ""); m != "" {
		if uid != "" && h.db != nil && !h.Mirror().On && stored[list] != string(m) {
			if _, err := h.db.Exec(r.Context(), `UPDATE users SET list_sort = COALESCE(list_sort,'{}'::jsonb) || jsonb_build_object($1::text,$2::text) WHERE id=$3::uuid`,
				list, string(m), uid); err != nil {
				log.Printf("sortMode: Wahl %s=%s nicht gespeichert: %v", list, m, err)
			}
		}
		return m
	}
	return listsort.Parse(stored[list], sl.Modes, sl.Default)
}

// SortOption: ein Knopf der Sortier-Leiste.
type SortOption struct {
	Mode  listsort.Mode
	Label string
	On    bool
}

// SortBar: Daten für das Template „sort-bar“. Target leer = Seitenmodus
// (gewöhnliche Links), sonst htmx-Austausch von Target.
type SortBar struct {
	List        string
	Options     []SortOption
	URL, Target string
	Htmx        bool
	Lang        string
}

// Language: Sprache für die Vorlagenfunktion t beim Ausgeben.
func (b SortBar) Language() string { return b.Lang }

// Href: Adresse einer Option – alle übrigen Abfrageparameter bleiben, ls_<list> wird gesetzt.
func (b SortBar) Href(m listsort.Mode) string {
	u, err := url.Parse(b.URL)
	if err != nil {
		return b.URL
	}
	q := u.Query()
	q.Set("ls_"+b.List, string(m))
	u.RawQuery = q.Encode()
	return u.String()
}

// sortBar baut die Leiste für eine Liste; cur ist die gewählte Art.
func (h *Handler) sortBar(r *http.Request, list string, cur listsort.Mode, url, target string) SortBar {
	lang := h.requestLang(r)
	b := SortBar{List: list, URL: url, Target: target, Htmx: target != "", Lang: lang}
	for _, m := range sortLists[list].Modes {
		b.Options = append(b.Options, SortOption{Mode: m, Label: sortLabel(lang, m), On: m == cur})
	}
	return b
}

// logOrderSQL: ORDER BY-Text für Protokoll-Listen aus festen Bausteinen je Kennung.
// at, person und kind sind Spaltenausdrücke aus dem Programm, nie Anfragedaten.
// person/kind leer oder eine andere Kennung: wie „new“. Der Aufrufer hängt bei Bedarf
// selbst einen festen Gleichstand (z. B. die Zeilenkennung) an.
func logOrderSQL(m listsort.Mode, at, person, kind string) string {
	switch {
	case m == listsort.Oldest:
		return at + " ASC"
	case m == listsort.Person && person != "":
		return "lower(" + person + ") ASC NULLS LAST, " + at + " DESC"
	case m == listsort.Kind && kind != "":
		return kind + " ASC, " + at + " DESC"
	}
	return at + " DESC"
}

// sortLabel: Beschriftung einer Sortierart in der Sprache des Benutzers.
func sortLabel(lang string, m listsort.Mode) string {
	switch m {
	case listsort.Newest:
		return tr(lang, "Neueste zuerst")
	case listsort.Oldest:
		return tr(lang, "Älteste zuerst")
	case listsort.Frequent:
		return tr(lang, "Häufigste zuerst")
	case listsort.Same:
		return tr(lang, "Gleiches zusammen")
	case listsort.Person:
		return tr(lang, "Nach Person")
	case listsort.Kind:
		return tr(lang, "Nach Art")
	case listsort.Due:
		return tr(lang, "Nach Termin")
	}
	return string(m)
}
