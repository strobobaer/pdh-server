package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/listsort"
)

// Maßnahmen an Störung, Ticket, Aufgabe und Wartung als ein gemeinsamer Baustein
// (Plan 2026-11-19-sortierung, Aufgabe 5):
//
//	GET  /records/actions/{modul}/{id}   Karte mit Sortier-Leiste, Liste, ggf. Eingabezeile
//	POST /records/actions/{modul}/{id}   trägt eine Maßnahme ein und liefert die Karte neu
//
// Lesen wie die Detailseite des Moduls: sie prüft kein Einzelrecht, jeder Angemeldete darf
// (die Abteilung sperrt DepartmentScopeMiddleware über scopePaths). Schreiben wie die
// Schnittstelle /api/v1/…/actions (gateRules in perm_gate.go): Recht „bearbeiten“ des
// Moduls in der Abteilung des Datensatzes, Beteiligte auch ohne das Recht.

const actionMaxLen = 4000 // Zeichen je Maßnahme

// actionModule: ein Modul mit Maßnahmen-Verlauf.
type actionModule struct {
	Table, ActionsTable, FK string
	// Input: steht die Eingabezeile (und gilt der POST) bei diesem Status? So wie die Seiten
	// sie bisher zeigten: Störung immer, Ticket nicht gelöst/geschlossen, Aufgabe nur offen,
	// Wartung nie (dort wird beim Abschließen eingetragen).
	Input func(status string) bool
	// Add trägt über den Dienst des Moduls ein (mit seinen Nebenwirkungen, z. B. Verknüpfung).
	Add func(h *Handler, ctx context.Context, id, text, uid string) error
}

var actionModules = map[string]actionModule{
	"fault": {"faults", "fault_actions", "fault_id",
		func(string) bool { return true },
		func(h *Handler, ctx context.Context, id, text, uid string) error {
			if h.faults == nil {
				return errNoService
			}
			_, err := h.faults.AddAction(ctx, id, text, uid)
			return err
		}},
	"ticket": {"tickets", "ticket_actions", "ticket_id",
		func(status string) bool { return status != "resolved" && status != "closed" },
		func(h *Handler, ctx context.Context, id, text, uid string) error {
			if h.tickets == nil {
				return errNoService
			}
			_, err := h.tickets.AddAction(ctx, id, text, uid)
			return err
		}},
	"task": {"tasks", "task_actions", "task_id",
		func(status string) bool { return status == "open" || status == "in_progress" || status == "pending" },
		func(h *Handler, ctx context.Context, id, text, uid string) error {
			if h.tasks == nil {
				return errNoService
			}
			_, err := h.tasks.AddAction(ctx, id, text, uid)
			return err
		}},
	"maintenance_task": {Table: "maintenance_tasks", ActionsTable: "maintenance_task_actions", FK: "task_id"},
}

var errNoService = fmt.Errorf("dienst nicht verfügbar")

// actionRow: eine Maßnahme.
type actionRow struct {
	Text, When, Person string
}

// actionEntry: eine Zeile der Liste; Count ab 2 = zusammengefasste Gruppe
// („N× Text – zuletzt …“) mit den Einzeleinträgen in Rows (neueste zuerst).
type actionEntry struct {
	Row   actionRow
	Label string
	Count int
	Last  string
	Rows  []actionRow
}

// actionsPage: Daten der Karte (Vorlage „record-actions“).
type actionsPage struct {
	Lang, Module string
	Bar          SortBar
	Entries      []actionEntry
	CanAdd       bool
	PostURL      string
}

func (p actionsPage) Language() string { return p.Lang }

// actionsContext prüft Anmeldung, Modul und Datensatz; liefert auch dessen Status.
func (h *Handler) actionsContext(w http.ResponseWriter, r *http.Request) (key string, am actionModule, id, status string, ok bool) {
	if getUser(r).ID == "" {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	key, id = chi.URLParam(r, "module"), chi.URLParam(r, "id")
	am, found := actionModules[key]
	if !found || h.db == nil {
		http.Error(w, "unbekanntes Modul", http.StatusNotFound)
		return
	}
	// Tabellenname stammt aus der festen Tabelle oben, nie aus der Eingabe
	if len(id) != 36 || !uuidInPathRe.MatchString(id) ||
		h.db.QueryRow(r.Context(), fmt.Sprintf(`SELECT status::text FROM %s WHERE id = $1::uuid`, am.Table), id).Scan(&status) != nil {
		http.Error(w, "Datensatz nicht gefunden", http.StatusNotFound)
		return
	}
	return key, am, id, status, true
}

// canWriteAction: wer Maßnahmen eintragen darf – wie die Schnittstelle (gateRules, involved):
// das Bearbeiten-Recht des Moduls für den Datensatz in seiner Abteilung, sonst Beteiligte.
func (h *Handler) canWriteAction(r *http.Request, key, id string) bool {
	if h.hasPermRec(r, recordPerms[key][0], key, id) {
		return true
	}
	return h.recordInvolved(r.Context(), getUser(r).ID, key, id)
}

// RecordActionsWeb liefert die Karte „Durchgeführte Maßnahmen“.
func (h *Handler) RecordActionsWeb(w http.ResponseWriter, r *http.Request) {
	key, am, id, status, ok := h.actionsContext(w, r)
	if !ok {
		return
	}
	h.renderActions(w, r, key, am, id, status)
}

// RecordActionsAddWeb trägt eine Maßnahme über den Dienst des Moduls ein.
func (h *Handler) RecordActionsAddWeb(w http.ResponseWriter, r *http.Request) {
	key, am, id, status, ok := h.actionsContext(w, r)
	if !ok {
		return
	}
	if !h.canWriteAction(r, key, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if am.Add == nil || !am.Input(status) {
		http.Error(w, "Hier können keine Maßnahmen eingetragen werden.", http.StatusConflict)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "ungültige Eingabe", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(r.PostFormValue("description"))
	if text == "" {
		http.Error(w, "Bitte eine Maßnahme eingeben.", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(text) > actionMaxLen {
		http.Error(w, fmt.Sprintf("Die Maßnahme darf höchstens %d Zeichen haben.", actionMaxLen), http.StatusBadRequest)
		return
	}
	if err := am.Add(h, r.Context(), id, text, getUser(r).ID); err != nil {
		componentLog("massnahmen").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("maßnahme eintragen")
		http.Error(w, "Die Maßnahme konnte nicht gespeichert werden.", http.StatusInternalServerError)
		return
	}
	h.renderActions(w, r, key, am, id, status)
}

// renderActions liest die Maßnahmen, sortiert und gruppiert sie und gibt die Karte aus.
func (h *Handler) renderActions(w http.ResponseWriter, r *http.Request, key string, am actionModule, id, status string) {
	rows, err := h.db.Query(r.Context(), fmt.Sprintf(`
		SELECT a.description, a.created_at, COALESCE(TRIM(u.first_name || ' ' || u.last_name), '')
		FROM %s a LEFT JOIN users u ON u.id = a.created_by
		WHERE a.%s = $1::uuid ORDER BY a.created_at, a.id`, am.ActionsTable, am.FK), id)
	if err != nil {
		componentLog("massnahmen").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("maßnahmen laden")
		http.Error(w, "Die Maßnahmen konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var items []listsort.Item
	var list []actionRow
	for rows.Next() {
		var it listsort.Item
		var text string
		if err := rows.Scan(&text, &it.At, &it.Person); err != nil {
			componentLog("massnahmen").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("maßnahme lesen")
			http.Error(w, "Die Maßnahmen konnten nicht geladen werden.", http.StatusInternalServerError)
			return
		}
		it.Key, it.Label = listsort.Key(text), text
		items = append(items, it)
		list = append(list, actionRow{Text: text, When: it.At.Local().Format("02.01.2006 15:04"), Person: it.Person})
	}
	if err := rows.Err(); err != nil {
		componentLog("massnahmen").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("maßnahmen laden")
		http.Error(w, "Die Maßnahmen konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	mode := h.sortMode(r, "actions")
	var entries []actionEntry
	if mode.Grouped() {
		for _, g := range listsort.Groups(items, mode) {
			e := actionEntry{Row: list[g.Idx[0]], Label: g.Label, Count: g.Count, Last: g.Last.Local().Format("02.01.2006")}
			if g.Count > 1 {
				for _, i := range g.Idx {
					e.Rows = append(e.Rows, list[i])
				}
			}
			entries = append(entries, e)
		}
	} else {
		for _, i := range listsort.Order(items, mode) {
			entries = append(entries, actionEntry{Row: list[i], Count: 1})
		}
	}
	base := r.URL.Path + "?ls_actions=" + string(mode)
	p := actionsPage{
		Lang: h.requestLang(r), Module: key, Entries: entries, PostURL: base,
		CanAdd: am.Add != nil && am.Input(status) && h.canWriteAction(r, key, id),
		Bar:    h.sortBar(r, "actions", mode, base, "#rec-actions"),
	}
	h.renderFragment(w, "record-actions", p)
}
