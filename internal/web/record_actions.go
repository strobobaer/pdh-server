package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Maßnahmen an Störung, Ticket, Aufgabe und Wartung als ein gemeinsamer Baustein
// (Plan 2026-11-19-sortierung, Aufgabe 5; gemeinsame Teile in record_blocks.go):
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
	"maintenance_task": {Table: "maintenance_tasks", ActionsTable: "maintenance_task_actions", FK: "task_id",
		Input: func(string) bool { return false }},
}

var errNoService = fmt.Errorf("dienst nicht verfügbar")

// actionRow: eine Maßnahme.
type actionRow struct {
	Text, When, Person string
}

// actionsPage: Daten der Karte (Vorlage „record-actions“).
type actionsPage struct {
	Lang, Module string
	Bar          SortBar
	Entries      []listEntry[actionRow]
	CanAdd       bool
	PostURL      string
	Error, Draft string
	Focus        bool
}

func (p actionsPage) Language() string { return p.Lang }

// actionsContext prüft Anmeldung, Modul und Datensatz; liefert auch dessen Status.
func (h *Handler) actionsContext(w http.ResponseWriter, r *http.Request) (key string, am actionModule, id, status string, ok bool) {
	key, id = chi.URLParam(r, "module"), chi.URLParam(r, "id")
	am, known := actionModules[key]
	status, ok = h.blockRecord(w, r, "massnahmen", key, id, am.Table, known)
	return key, am, id, status, ok
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
	h.renderActions(w, r, key, am, id, status, "", "", false)
}

// RecordActionsAddWeb trägt eine Maßnahme über den Dienst des Moduls ein.
func (h *Handler) RecordActionsAddWeb(w http.ResponseWriter, r *http.Request) {
	key, am, id, status, ok := h.actionsContext(w, r)
	if !ok {
		return
	}
	refuse := func(code int, msg, draft string) {
		refuseBlock(w, r, code, msg, func(m string) { h.renderActions(w, r, key, am, id, status, m, draft, true) })
	}
	if !h.canWriteAction(r, key, id) {
		refuse(http.StatusForbidden, "Dafür fehlt die Berechtigung.", "")
		return
	}
	if am.Add == nil || !am.Input(status) {
		refuse(http.StatusConflict, "Hier können keine Maßnahmen eingetragen werden.", "")
		return
	}
	text, raw, code, msg := blockText(w, r, "description", actionMaxLen, "Bitte eine Maßnahme eingeben.", "Die Maßnahme darf höchstens %d Zeichen haben.")
	if msg != "" {
		refuse(code, msg, raw)
		return
	}
	if err := am.Add(h, r.Context(), id, text, getUser(r).ID); err != nil {
		componentLog("massnahmen").Error().Err(err).Str("modul", key).Str("datensatz", id).Msg("maßnahme eintragen")
		http.Error(w, "Die Maßnahme konnte nicht gespeichert werden.", http.StatusInternalServerError)
		return
	}
	h.renderActions(w, r, key, am, id, status, "", "", true)
}

// renderActions liest die Maßnahmen, sortiert und gruppiert sie und gibt die Karte aus.
func (h *Handler) renderActions(w http.ResponseWriter, r *http.Request, key string, am actionModule, id, status, errMsg, draft string, focus bool) {
	rows, ok := h.loadBlockRows(w, r, "massnahmen", key, id, am.ActionsTable, am.FK, "description", "created_by")
	if !ok {
		return
	}
	mode := h.sortMode(r, "actions")
	base := r.URL.Path + "?ls_actions=" + string(mode)
	h.renderFragment(w, "record-actions", actionsPage{
		Lang: h.requestLang(r), Module: key, PostURL: base, Error: errMsg, Draft: draft, Focus: focus,
		Entries: arrangeBlockRows(rows, mode, func(b blockRow) actionRow {
			return actionRow{Text: b.Text, When: b.At.Local().Format("02.01.2006 15:04"), Person: b.Person}
		}),
		CanAdd: am.Add != nil && am.Input(status) && h.canWriteAction(r, key, id),
		Bar:    h.sortBar(r, "actions", mode, base, "#rec-actions"),
	})
}
