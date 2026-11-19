package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/core/listsort"
)

// Kommentare an Ticket und KVP-Vorschlag als ein gemeinsamer Baustein
// (Plan 2026-11-19-sortierung, Aufgabe 6):
//
//	GET  /records/comments/{modul}/{id}   Karte mit Sortier-Leiste, Liste und Eingabezeile
//	POST /records/comments/{modul}/{id}   trägt einen Kommentar ein und liefert die Karte neu
//
// Lesen wie die Detailseite: TicketDetail prüft kein Einzelrecht (jeder Angemeldete, die Abteilung
// sperrt DepartmentScopeMiddleware über scopePaths), KVPDetailPage ebenso und blendet nichts aus.
// Schreiben wie bisher (TicketAddComment, KVPCommentWeb – beide ohne Recht, siehe gateRules):
// wer die Seite sieht, darf kommentieren. Daher gibt es hier auch keine Regel in gateRules.

const commentMaxLen = 4000 // Zeichen je Kommentar

// commentModule: ein Modul mit Kommentaren.
type commentModule struct {
	Table, CommentsTable, FK string
	// Add legt den Kommentar an (Ticket über den Dienst des Moduls).
	Add func(h *Handler, ctx context.Context, id, uid, text string) error
}

var commentModules = map[string]commentModule{
	"ticket": {"tickets", "ticket_comments", "ticket_id",
		func(h *Handler, ctx context.Context, id, uid, text string) error {
			if h.tickets == nil {
				return errNoService
			}
			_, err := h.tickets.AddComment(ctx, id, uid, text)
			return err
		}},
	"kvp": {"kvp_ideas", "kvp_comments", "idea_id",
		func(h *Handler, ctx context.Context, id, uid, text string) error {
			_, err := h.db.Exec(ctx, `INSERT INTO kvp_comments (idea_id, user_id, text) VALUES ($1::uuid, $2, $3)`, id, nullID(uid), text)
			return err
		}},
}

// commentRow: ein Kommentar.
type commentRow struct {
	Text, When, Person, Initial string
}

// commentsPage: Daten der Karte (Vorlage „record-comments“).
type commentsPage struct {
	Lang, Module string
	Bar          SortBar
	Entries      []listEntry[commentRow]
	PostURL      string
	Error, Draft string
	Focus        bool
}

func (p commentsPage) Language() string { return p.Lang }

// commentInitial: Anfangsbuchstabe des Namens – über Runen, nicht Bytes („Özlem“ → „Ö“).
func commentInitial(name string) string {
	name = strings.TrimSpace(name)
	r, size := utf8.DecodeRuneInString(name)
	if size == 0 || r == utf8.RuneError {
		return "?"
	}
	return string(unicode.ToUpper(r))
}

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

// commentsContext prüft Anmeldung, Modul und Datensatz.
func (h *Handler) commentsContext(w http.ResponseWriter, r *http.Request) (key string, cm commentModule, id string, ok bool) {
	if getUser(r).ID == "" {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	key, id = chi.URLParam(r, "module"), chi.URLParam(r, "id")
	cm, found := commentModules[key]
	if !found || h.db == nil {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return
	}
	if len(id) != 36 || !uuidInPathRe.MatchString(id) {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return
	}
	// Tabellenname stammt aus der festen Tabelle oben, nie aus der Eingabe
	var one int
	switch err := h.db.QueryRow(r.Context(), fmt.Sprintf(`SELECT 1 FROM %s WHERE id = $1::uuid`, cm.Table), id).Scan(&one); {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return
	case err != nil:
		componentLog("kommentare").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("datensatz prüfen")
		http.Error(w, "Die Kommentare konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	return key, cm, id, true
}

// RecordCommentsWeb liefert die Karte „Kommentare“.
func (h *Handler) RecordCommentsWeb(w http.ResponseWriter, r *http.Request) {
	key, cm, id, ok := h.commentsContext(w, r)
	if !ok {
		return
	}
	h.renderComments(w, r, key, cm, id, "", "", false)
}

// RecordCommentsAddWeb trägt einen Kommentar ein.
func (h *Handler) RecordCommentsAddWeb(w http.ResponseWriter, r *http.Request) {
	key, cm, id, ok := h.commentsContext(w, r)
	if !ok {
		return
	}
	// Eine abgewiesene Eingabe: über htmx kommt die Karte mit dem Klartext zurück (htmx tauscht
	// bei 4xx nicht und die Seite zeigte nur „HTTP 400“), sonst Status und Klartext.
	refuse := func(code int, msg, draft string) {
		if r.Header.Get("HX-Request") == "true" {
			h.renderComments(w, r, key, cm, id, msg, draft, true)
			return
		}
		http.Error(w, msg, code)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		refuse(http.StatusBadRequest, "Die Eingabe ist ungültig oder zu groß.", "")
		return
	}
	raw := r.PostFormValue("text")
	text := strings.TrimSpace(raw)
	if text == "" {
		refuse(http.StatusBadRequest, "Bitte einen Kommentar eingeben.", "")
		return
	}
	if utf8.RuneCountInString(text) > commentMaxLen {
		refuse(http.StatusBadRequest, fmt.Sprintf("Der Kommentar darf höchstens %d Zeichen haben.", commentMaxLen), raw)
		return
	}
	if err := cm.Add(h, r.Context(), id, getUser(r).ID, text); err != nil {
		componentLog("kommentare").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("kommentar eintragen")
		http.Error(w, "Der Kommentar konnte nicht gespeichert werden.", http.StatusInternalServerError)
		return
	}
	h.renderComments(w, r, key, cm, id, "", "", true)
}

// TicketCommentLegacy: der alte Weg POST /tickets/{id}/comment läuft über den Baustein.
func (h *Handler) TicketCommentLegacy(w http.ResponseWriter, r *http.Request) {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		rc.URLParams.Add("module", "ticket")
	}
	h.RecordCommentsAddWeb(w, r)
}

// renderComments liest die Kommentare, sortiert und gruppiert sie und gibt die Karte aus.
func (h *Handler) renderComments(w http.ResponseWriter, r *http.Request, key string, cm commentModule, id, errMsg, draft string, focus bool) {
	fail := func(err error, msg string) {
		componentLog("kommentare").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg(msg)
		http.Error(w, "Die Kommentare konnten nicht geladen werden.", http.StatusInternalServerError)
	}
	rows, err := h.db.Query(r.Context(), fmt.Sprintf(`
		SELECT c.text, c.created_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')
		FROM %s c LEFT JOIN users u ON u.id = c.user_id
		WHERE c.%s = $1::uuid ORDER BY c.created_at, c.id`, cm.CommentsTable, cm.FK), id)
	if err != nil {
		fail(err, "kommentare laden")
		return
	}
	defer rows.Close()
	var items []listsort.Item
	var list []commentRow
	for rows.Next() {
		var it listsort.Item
		var text string
		if err := rows.Scan(&text, &it.At, &it.Person); err != nil {
			fail(err, "kommentar lesen")
			return
		}
		it.Key, it.Label = listsort.Key(text), text
		items = append(items, it)
		name := it.Person
		if name == "" {
			name = "Unbekannt"
		}
		list = append(list, commentRow{Text: text, When: timeAgo(it.At), Person: name, Initial: commentInitial(name)})
	}
	if err := rows.Err(); err != nil {
		fail(err, "kommentare laden")
		return
	}
	mode := h.sortMode(r, "comments")
	// feste Adresse des Bausteins (auch wenn der Aufruf über den alten Ticket-Weg kam)
	base := "/records/comments/" + key + "/" + id + "?ls_comments=" + string(mode)
	h.renderFragment(w, "record-comments", commentsPage{
		Lang: h.requestLang(r), Module: key, Entries: arrangeRows(items, list, mode), PostURL: base,
		Error: errMsg, Draft: draft, Focus: focus,
		Bar: h.sortBar(r, "comments", mode, base, "#rec-comments"),
	})
}
