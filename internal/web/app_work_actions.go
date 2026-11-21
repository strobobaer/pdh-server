package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tickets"
)

// Schreibende App-Wege an einem Vorgang (alle POST, hinter AppAuth und AppWrite):
//
//	/api/app/v1/work/{type}/{id}/start             annehmen/beginnen (unzugewiesen: an sich nehmen)
//	/api/app/v1/work/{type}/{id}/actions           {"text"}                          Maßnahme
//	/api/app/v1/work/{type}/{id}/comments          {"text"}                          Kommentar (nur Ticket)
//	/api/app/v1/work/{type}/{id}/steps/{stepID}    {"value","done"}                  Checklistenpunkt (nur Wartung)
//	/api/app/v1/work/{type}/{id}/finish            {"text","no_parts","root_cause"}  abschließen
//
// Jeder Weg prüft sein can-Flag aus appWorkCan (Benutzer ∩ Gerät, wie das Web) und
// schreibt über dieselben Dienste wie das Web. Antwort ist immer die aktuelle
// Einzelansicht (wie GET). Bei einer bekannten Once (appIsReplay) wird nichts
// geschrieben, nur der Stand geliefert.

const (
	appTextMax = 4000     // Zeichen je Maßnahme, Kommentar, Abschlusstext
	appBodyMax = 32 << 10 // Bytes je Anfrage
)

// appInputErr: Eingabefehler mit Meldung für den Anwender (→ 422 app_invalid_input).
type appInputErr struct{ msg string }

func (e appInputErr) Error() string { return e.msg }

// appNotFoundErr: Teil des Vorgangs (z. B. Checklistenpunkt) gibt es nicht (→ 404 app_not_found).
type appNotFoundErr struct{}

func (appNotFoundErr) Error() string { return "app_not_found" }

// plainErrType: Typ von errors.New / fmt.Errorf ohne %w – so melden die Module
// Störung, Ticket und Aufgabe ihre Prüfungen („bitte mindestens eine … maßnahme …“).
var plainErrType = reflect.TypeOf(errors.New(""))

// appModuleInput: ist err eine Eingabe-/Regelprüfung des Moduls? Dann mit Meldung.
// Datenbank- und Verbindungsfehler sind getypt (pgconn, net) oder bekannte
// Wächter (pgx.ErrNoRows, context.*) und gelten als vorübergehend.
func appModuleInput(err error) (string, bool) {
	var ie appInputErr
	switch {
	case errors.As(err, &ie):
		return ie.msg, true
	case maintenance.IsInputError(err):
		return maintenance.InputMessage(err), true
	case errors.Is(err, tickets.ErrRootCauseRequired):
		return err.Error(), true
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, errNoService):
		return "", false
	case reflect.TypeOf(err) == plainErrType:
		return err.Error(), true
	}
	return "", false
}

// appText: Text prüfen (getrimmt, höchstens appTextMax Zeichen; leer nur mit empty != "").
func appText(s, empty, tooLong string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" && empty != "" {
		return "", appInputErr{empty}
	}
	if len([]rune(s)) > appTextMax {
		return "", appInputErr{fmt.Sprintf(tooLong, appTextMax)}
	}
	return s, nil
}

// appWorkWrite ist der gemeinsame Rahmen: Art (only), Sichtbarkeit, Replay,
// can-Flag, Eingabe, Schreiben, neuer Stand.
// openOnly: auf einem abgeschlossenen Vorgang 409 app_conflict statt 403 (Abschließen).
func (h *Handler) appWorkWrite(w http.ResponseWriter, r *http.Request, only string, openOnly bool, allowed func(appWorkPerms) bool,
	run func(ctx context.Context, k completionKind, id, uid string, body []byte) error) {
	kind, id := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	if only != "" && kind != only {
		appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
		return
	}
	p, err := h.appWorkCan(r, kind, id)
	if err != nil {
		appUnavailable(w, err, kind, id, "vorgang prüfen")
		return
	}
	if appWorkDenied(w, p) {
		return
	}
	if appIsReplay(r) { // schon ausgeführt: nur den Stand liefern
		h.appWorkRespond(w, r, kind, id, p)
		return
	}
	if openOnly && p.Closed {
		appJSONErr(w, http.StatusConflict, "app_conflict", "Der Vorgang ist bereits abgeschlossen.")
		return
	}
	if !allowed(p) {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, appBodyMax+1))
	if err != nil {
		appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", "ungültige Daten")
		return
	}
	if len(body) > appBodyMax {
		appJSONErr(w, http.StatusRequestEntityTooLarge, "app_too_large", "app_too_large")
		return
	}
	ctx := r.Context()
	if err := run(ctx, completionKinds[kind], id, getUser(r).ID, body); err != nil {
		if errors.As(err, new(appNotFoundErr)) {
			appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
			return
		}
		if msg, ok := appModuleInput(err); ok {
			appJSONErr(w, http.StatusUnprocessableEntity, "app_invalid_input", msg)
			return
		}
		appUnavailable(w, err, kind, id, "app: vorgang schreiben")
		return
	}
	appKeepOnce(r) // geschrieben: ein erneuter Versuch ist ab jetzt ein Replay
	if p, err = h.appWorkCan(r, kind, id); err != nil {
		appUnavailable(w, err, kind, id, "vorgang prüfen")
		return
	}
	if appWorkDenied(w, p) {
		return
	}
	h.appWorkRespond(w, r, kind, id, p)
}

// appDecode liest JSON; ein leerer Körper ist erlaubt (alle Felder leer).
func appDecode(body []byte, v any) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return appInputErr{"ungültige Daten"}
	}
	return nil
}

// AppWorkStartAPI: POST …/start – Vorgang annehmen: ist niemand zugewiesen (keine
// Person, keine Gruppe), nimmt der Benutzer ihn an sich (wie „übernehmen“ in
// completionInvolved); Status „in Bearbeitung“ und Historie „work“ wie der Assistent.
func (h *Handler) AppWorkStartAPI(w http.ResponseWriter, r *http.Request) {
	h.appWorkWrite(w, r, "", false, func(p appWorkPerms) bool { return p.Start },
		func(ctx context.Context, k completionKind, id, uid string, _ []byte) error {
			q := fmt.Sprintf(`UPDATE %s SET assigned_to = $1::uuid, updated_at = NOW() WHERE id = $2::uuid AND %s`, k.Table, unassignedSQL(false))
			if k.Type == "task" {
				q = `INSERT INTO task_assignees (task_id, user_id) SELECT id, $1::uuid FROM tasks WHERE id = $2::uuid AND ` + unassignedSQL(true)
			}
			tag, err := h.db.Exec(ctx, q, uid, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				if k.Type == "task" { // task_assignees ändert die Aufgabe selbst nicht
					if _, err := h.db.Exec(ctx, `UPDATE tasks SET updated_at = NOW() WHERE id = $1::uuid`, id); err != nil {
						return err
					}
				}
				h.addHistory(ctx, historyRef(k.Type), id, "people", "assigned_to", "", uid, "In der App übernommen", uid)
			}
			// Historie „work“ nur, wenn der Vorgang dadurch begonnen wurde (nicht bei jedem Tippen)
			if changed, err := h.completionInProgress(ctx, k, id); err != nil || !changed {
				return err
			}
			h.addHistory(ctx, historyRef(k.Type), id, "work", "", "", "", "Arbeit in der App begonnen", uid)
			return nil
		})
}

// AppWorkActionAPI: POST …/actions {"text"} – Maßnahme über den Dienst des Moduls.
func (h *Handler) AppWorkActionAPI(w http.ResponseWriter, r *http.Request) {
	h.appWorkWrite(w, r, "", false, func(p appWorkPerms) bool { return p.Action },
		func(ctx context.Context, k completionKind, id, uid string, body []byte) error {
			var in struct {
				Text string `json:"text"`
			}
			if err := appDecode(body, &in); err != nil {
				return err
			}
			text, err := appText(in.Text, "Bitte eine Maßnahme eingeben.", "Die Maßnahme darf höchstens %d Zeichen haben.")
			if err != nil {
				return err
			}
			am, ok := actionModules[historyRef(k.Type)]
			if !ok || am.Add == nil {
				return errNoService
			}
			return am.Add(h, ctx, id, text, uid)
		})
}

// AppWorkCommentAPI: POST …/comments {"text"} – Kommentar (nur Ticket).
func (h *Handler) AppWorkCommentAPI(w http.ResponseWriter, r *http.Request) {
	h.appWorkWrite(w, r, "ticket", false, func(p appWorkPerms) bool { return p.Comment },
		func(ctx context.Context, _ completionKind, id, uid string, body []byte) error {
			var in struct {
				Text string `json:"text"`
			}
			if err := appDecode(body, &in); err != nil {
				return err
			}
			text, err := appText(in.Text, "Bitte einen Kommentar eingeben.", "Der Kommentar darf höchstens %d Zeichen haben.")
			if err != nil {
				return err
			}
			return commentModules["ticket"].Add(h, ctx, id, uid, text)
		})
}

// AppWorkStepAPI: POST …/steps/{stepID} {"value","done"} – Checklistenpunkt einer
// Wartung (Prüfungen des Moduls: Zahl, Länge; Abhaken bei „checkbox“).
func (h *Handler) AppWorkStepAPI(w http.ResponseWriter, r *http.Request) {
	stepID := chi.URLParam(r, "stepID")
	if !bareUUIDRe.MatchString(stepID) {
		appJSONErr(w, http.StatusNotFound, "app_not_found", "app_not_found")
		return
	}
	h.appWorkWrite(w, r, "maintenance", false, func(p appWorkPerms) bool { return p.Step },
		func(ctx context.Context, _ completionKind, id, uid string, body []byte) error {
			var in struct {
				Value string `json:"value"`
				Done  bool   `json:"done"`
			}
			if err := appDecode(body, &in); err != nil {
				return err
			}
			if h.maint == nil {
				return errNoService
			}
			var known bool
			if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance_task_steps WHERE id = $1::uuid AND task_id = $2::uuid)`, stepID, id).Scan(&known); err != nil {
				return err
			}
			if !known {
				return appNotFoundErr{}
			}
			_, err := h.maint.SaveStep(ctx, id, stepID, in.Value, in.Done, uid)
			return err
		})
}

// AppWorkFinishAPI: POST …/finish {"text","no_parts","root_cause"} – abschließen wie
// der Abschluss-Assistent: ein nicht leerer Text wird zuerst als Maßnahme eingetragen
// (completionAddAction), dann completionFinish (ohne Zeitbuchung). Die Prüfungen des
// Moduls (Maßnahme, Material, Ursache, Pflichtpunkte) kommen als 422 mit ihrer Meldung.
// Scheitert der Abschluss, bleibt die Maßnahme stehen (wie im Assistenten) und die
// Once wird freigegeben; ein erneuter Versuch trägt denselben Text nicht noch einmal
// ein (appSameActionRecent).
func (h *Handler) AppWorkFinishAPI(w http.ResponseWriter, r *http.Request) {
	h.appWorkWrite(w, r, "", true, func(p appWorkPerms) bool { return p.Finish },
		func(ctx context.Context, k completionKind, id, uid string, body []byte) error {
			var in struct {
				Text      string `json:"text"`
				NoParts   bool   `json:"no_parts"`
				RootCause string `json:"root_cause"`
			}
			if err := appDecode(body, &in); err != nil {
				return err
			}
			text, err := appText(in.Text, "", "Der Text darf höchstens %d Zeichen haben.")
			if err != nil {
				return err
			}
			cause, err := appText(in.RootCause, "", "Die Ursache darf höchstens %d Zeichen haben.")
			if err != nil {
				return err
			}
			if !h.completionService(k.Type) {
				return errNoService
			}
			if text != "" {
				dup, err := h.appSameActionRecent(ctx, k, id, uid, text)
				if err != nil {
					return err
				}
				if !dup {
					if err := h.completionAddAction(ctx, k, id, text, uid); err != nil {
						return err
					}
				}
			}
			return h.completionFinish(ctx, k, id, &completionRequest{Comment: text, NoParts: in.NoParts, RootCause: cause}, uid, 0)
		})
}

// completionService: ist der Dienst der Vorgangsart da (completionFinish braucht ihn)?
func (h *Handler) completionService(kind string) bool {
	switch kind {
	case "ticket":
		return h.tickets != nil
	case "fault":
		return h.faults != nil
	case "task":
		return h.tasks != nil
	case "maintenance":
		return h.maint != nil
	}
	return false
}

// appSameActionRecent: hat uid an diesem Vorgang in den letzten 10 Minuten schon eine
// Maßnahme mit genau diesem Text eingetragen? (Wiederholung eines gescheiterten
// Abschlusses aus der App – derselbe Text soll nicht zweimal stehen.)
func (h *Handler) appSameActionRecent(ctx context.Context, k completionKind, id, uid, text string) (bool, error) {
	am, ok := actionModules[historyRef(k.Type)]
	if !ok {
		return false, errNoService
	}
	var yes bool
	err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+am.ActionsTable+` WHERE `+am.FK+` = $1::uuid
		AND created_by = $2::uuid AND description = $3 AND created_at > NOW() - INTERVAL '10 minutes')`, id, uid, text).Scan(&yes)
	return yes, err
}
