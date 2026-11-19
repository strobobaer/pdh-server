package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"pdh/internal/core/listsort"
)

// Gemeinsame Bausteine der Karten „Maßnahmen“ (record_actions.go) und „Kommentare“
// (record_comments.go): Datensatz prüfen, Zeilen laden, Eingabe prüfen, Abweisung zeigen, ordnen.

// listEntry: eine Zeile einer sortierten Liste; Count ab 2 = zusammengefasste Gruppe
// („N× Text – zuletzt …“) mit den Einzeleinträgen in Rows (neueste zuerst).
type listEntry[T any] struct {
	Row   T
	Label string
	Count int
	Last  string
	Rows  []T
}

// arrangeRows ordnet rows (items[i] gehört zu rows[i]) nach der gewählten Art und fasst bei
// „häufigste“ und „gleiches zusammen“ gleiche Texte zu Gruppen zusammen.
func arrangeRows[T any](items []listsort.Item, rows []T, mode listsort.Mode) []listEntry[T] {
	var entries []listEntry[T]
	if mode.Grouped() {
		for _, g := range listsort.Groups(items, mode) {
			e := listEntry[T]{Row: rows[g.Idx[0]], Label: g.Label, Count: g.Count, Last: g.Last.Local().Format("02.01.2006")}
			if g.Count > 1 {
				for _, i := range g.Idx {
					e.Rows = append(e.Rows, rows[i])
				}
			}
			entries = append(entries, e)
		}
		return entries
	}
	for _, i := range listsort.Order(items, mode) {
		entries = append(entries, listEntry[T]{Row: rows[i], Count: 1})
	}
	return entries
}

// blockRow: eine geladene Zeile (Text, Zeitpunkt, Name der Person).
type blockRow struct {
	Text   string
	At     time.Time
	Person string
}

// arrangeBlockRows baut aus den Zeilen die Einträge der Karte; conv macht aus einer Zeile die Anzeigezeile.
func arrangeBlockRows[T any](rows []blockRow, mode listsort.Mode, conv func(blockRow) T) []listEntry[T] {
	items := make([]listsort.Item, len(rows))
	list := make([]T, len(rows))
	for i, r := range rows {
		items[i] = listsort.Item{At: r.At, Key: listsort.Key(r.Text), Label: r.Text, Person: r.Person}
		list[i] = conv(r)
	}
	return arrangeRows(items, list, mode)
}

// blockRecord prüft Anmeldung, Modul (known), Datensatz-ID und Vorhandensein des Datensatzes und liefert
// dessen Status. Nicht gefunden = 404, Datenbankfehler = 500 mit Eintrag im Protokoll (comp). Der
// Tabellenname kommt aus der festen Tabelle des Moduls, nie aus der Eingabe.
func (h *Handler) blockRecord(w http.ResponseWriter, r *http.Request, comp, key, id, table string, known bool) (status string, ok bool) {
	if getUser(r).ID == "" {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return "", false
	}
	if !known || h.db == nil {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return "", false
	}
	if len(id) != 36 || !uuidInPathRe.MatchString(id) {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return "", false
	}
	switch err := h.db.QueryRow(r.Context(), fmt.Sprintf(`SELECT status::text FROM %s WHERE id = $1::uuid`, table), id).Scan(&status); {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return "", false
	case err != nil:
		componentLog(comp).Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("datensatz prüfen")
		http.Error(w, "Der Datensatz konnte nicht geladen werden.", http.StatusInternalServerError)
		return "", false
	}
	return status, true
}

// loadBlockRows liest Text, Zeitpunkt und Person der Einträge eines Datensatzes (älteste zuerst, feste
// Reihenfolge bei Gleichstand). Tabellen- und Spaltennamen stammen aus den festen Tabellen der Module.
// Bei einem Fehler: Eintrag im Protokoll, 500 und false.
func (h *Handler) loadBlockRows(w http.ResponseWriter, r *http.Request, comp, key, id, table, fk, textCol, userCol string) ([]blockRow, bool) {
	fail := func(err error, msg string) ([]blockRow, bool) {
		componentLog(comp).Error().Err(err).Str("modul", key).Str("datensatz", id).Msg(msg)
		http.Error(w, "Die Einträge konnten nicht geladen werden.", http.StatusInternalServerError)
		return nil, false
	}
	rows, err := h.db.Query(r.Context(), fmt.Sprintf(`
		SELECT t.%s, t.created_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')
		FROM %s t LEFT JOIN users u ON u.id = t.%s
		WHERE t.%s = $1::uuid ORDER BY t.created_at, t.id`, textCol, table, userCol, fk), id)
	if err != nil {
		return fail(err, "einträge laden")
	}
	defer rows.Close()
	var out []blockRow
	for rows.Next() {
		var b blockRow
		if err := rows.Scan(&b.Text, &b.At, &b.Person); err != nil {
			return fail(err, "eintrag lesen")
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return fail(err, "einträge laden")
	}
	return out, true
}

// blockText liest und prüft den Text einer Eingabe (1 bis max Zeichen). Bei einer Abweisung steht in
// code/msg Status und deutscher Klartext; raw ist die unveränderte Eingabe (zum Wiederherstellen des Feldes).
func blockText(w http.ResponseWriter, r *http.Request, field string, max int, emptyMsg, longMsg string) (text, raw string, code int, msg string) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		return "", "", http.StatusBadRequest, "Die Eingabe ist ungültig oder zu groß."
	}
	raw = r.PostFormValue(field)
	text = strings.TrimSpace(raw)
	switch {
	case text == "":
		return "", "", http.StatusBadRequest, emptyMsg
	case utf8.RuneCountInString(text) > max:
		return "", raw, http.StatusBadRequest, fmt.Sprintf(longMsg, max)
	}
	return text, raw, 0, ""
}

// refuseBlock zeigt eine abgewiesene Eingabe: über htmx kommt die Karte mit dem Klartext zurück (render;
// htmx tauscht bei 4xx nicht, die Seite zeigte nur „HTTP 409“), sonst der echte Status mit Klartext.
func refuseBlock(w http.ResponseWriter, r *http.Request, code int, msg string, render func(msg string)) {
	if r.Header.Get("HX-Request") == "true" {
		render(msg)
		return
	}
	http.Error(w, msg, code)
}
