package web

import (
	"context"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Kommentare an Ticket und KVP-Vorschlag als ein gemeinsamer Baustein
// (Plan 2026-11-19-sortierung, Aufgabe 6; gemeinsame Teile in record_blocks.go):
//
//	GET  /records/comments/{modul}/{id}   Karte mit Sortier-Leiste, Liste und Eingabezeile
//	POST /records/comments/{modul}/{id}   trägt einen Kommentar ein und liefert die Karte neu
//
// Lesen wie die Detailseite: TicketDetail prüft kein Einzelrecht (jeder Angemeldete, die Abteilung
// sperrt DepartmentScopeMiddleware über scopePaths), KVPDetailPage ebenso und blendet nichts aus.
// Schreiben wie bisher (die alten Wege /tickets/{id}/comment und /kvp/{id}/comment – beide ohne
// Recht, siehe gateRules): wer die Seite sieht, darf kommentieren. Daher gibt es hier auch keine
// Regel in gateRules.

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

// commentsContext prüft Anmeldung, Modul und Datensatz.
func (h *Handler) commentsContext(w http.ResponseWriter, r *http.Request) (key string, cm commentModule, id string, ok bool) {
	key, id = chi.URLParam(r, "module"), chi.URLParam(r, "id")
	cm, known := commentModules[key]
	if _, ok := h.blockRecord(w, r, "kommentare", key, id, cm.Table, known); !ok {
		return "", cm, "", false
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
	text, raw, code, msg := blockText(w, r, "text", commentMaxLen, "Bitte einen Kommentar eingeben.", "Der Kommentar darf höchstens %d Zeichen haben.")
	if msg != "" {
		refuseBlock(w, r, code, msg, func(m string) { h.renderComments(w, r, key, cm, id, m, raw, true) })
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
// Auch ohne htmx kommt das Kartenfragment zurück (keine Weiterleitung auf die Seite).
func (h *Handler) TicketCommentLegacy(w http.ResponseWriter, r *http.Request) {
	h.commentLegacy(w, r, "ticket")
}

// KVPCommentLegacy: der alte Weg POST /kvp/{id}/comment läuft über den Baustein.
// Auch ohne htmx kommt das Kartenfragment zurück (keine Weiterleitung auf die Seite).
func (h *Handler) KVPCommentLegacy(w http.ResponseWriter, r *http.Request) {
	h.commentLegacy(w, r, "kvp")
}

func (h *Handler) commentLegacy(w http.ResponseWriter, r *http.Request, module string) {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		rc.URLParams.Add("module", module)
	}
	h.RecordCommentsAddWeb(w, r)
}

// renderComments liest die Kommentare, sortiert und gruppiert sie und gibt die Karte aus.
func (h *Handler) renderComments(w http.ResponseWriter, r *http.Request, key string, cm commentModule, id, errMsg, draft string, focus bool) {
	rows, ok := h.loadBlockRows(w, r, "kommentare", key, id, cm.CommentsTable, cm.FK, "text", "user_id")
	if !ok {
		return
	}
	mode := h.sortMode(r, "comments")
	// feste Adresse des Bausteins (auch wenn der Aufruf über einen alten Weg kam)
	base := "/records/comments/" + key + "/" + id + "?ls_comments=" + string(mode)
	h.renderFragment(w, "record-comments", commentsPage{
		Lang: h.requestLang(r), Module: key, PostURL: base, Error: errMsg, Draft: draft, Focus: focus,
		Entries: arrangeBlockRows(rows, mode, func(b blockRow) commentRow {
			name := b.Person
			if name == "" {
				name = "Unbekannt"
			}
			return commentRow{Text: b.Text, When: timeAgo(b.At), Person: name, Initial: commentInitial(name)}
		}),
		Bar: h.sortBar(r, "comments", mode, base, "#rec-comments"),
	})
}
