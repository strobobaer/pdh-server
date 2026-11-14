package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/ruesten"
)

// Rüstzeiten – Aktionen: anlegen, bearbeiten, beenden, löschen. Die Rechte
// prüft zusätzlich die Rechte-Schranke (perm_gate.go, gateRules); gespeichert
// wird über ruesten.Store. Nach jeder Aktion geht es zur Seite zurück
// (Post/Redirect/Get), Meldungen stehen als notice bzw. err in der Adresse.

// Meldungen (deutscher Text = Übersetzungsschlüssel, siehe uiError).
var (
	errSetupInfraReason = uiError("Bitte Anlage und Grund wählen.")
	errSetupNeedStart   = uiError("Bitte den Beginn angeben.")
	errSetupStartFuture = uiError("Der Beginn darf nicht in der Zukunft liegen.")
	errSetupEndFuture   = uiError("Das Ende darf nicht in der Zukunft liegen.")
	errSetupEndBefore   = uiError("Das Ende muss nach dem Beginn liegen.")
	errSetupDuration    = uiError("Die Dauer muss eine ganze Zahl von Minuten über 0 sein.")
	errSetupRunning     = uiError("An dieser Anlage läuft bereits eine Rüstzeit.")
	errSetupReadOnly    = uiError("Beginn und Ende einer Rüstzeit mit Störmeldung sind nicht änderbar.")
	errSetupNotRunning  = uiError("Die Rüstzeit läuft nicht (mehr).")
	errSetupNotFound    = uiError("Rüstzeit nicht gefunden.")
	errSetupSave        = uiError("Die Rüstzeit konnte nicht gespeichert werden.")
)

var (
	noticeSetupCreated  = uiError("Rüstzeit angelegt.").Error()
	noticeSetupSaved    = uiError("Gespeichert.").Error()
	noticeSetupFinished = uiError("Rüstzeit beendet.").Error()
	noticeSetupDeleted  = uiError("Rüstzeit gelöscht.").Error()
)

// setupFutureSlack: so weit darf ein Zeitpunkt vorausliegen (Uhren gehen verschieden).
const setupFutureSlack = 5 * time.Minute

// setupBack: Adresse der Detailseite mit Meldung.
func setupBack(id, notice string, err error) string {
	u := "/setup/" + id
	if err != nil {
		return u + "?err=" + url.QueryEscape(err.Error())
	}
	if notice != "" {
		return u + "?notice=" + url.QueryEscape(notice)
	}
	return u
}

// setupFormError: zurück zum Anlegen mit den Eingaben und der Meldung.
func setupFormError(r *http.Request, err error) string {
	q := url.Values{}
	for form, param := range map[string]string{"infrastructure_id": "infra", "reason_id": "reason", "started_at": "started_at",
		"ended_at": "ended_at", "duration_min": "duration_min", "note": "note"} {
		if v := strings.TrimSpace(r.FormValue(form)); v != "" {
			q.Set(param, v)
		}
	}
	q.Set("err", err.Error())
	return "/setup/new?" + q.Encode()
}

// setupStoreError: Meldung zu einem Fehler des Speichers; Unerwartetes steht im Protokoll.
func setupStoreError(r *http.Request, what string, err error) error {
	switch {
	case errors.Is(err, ruesten.ErrAlreadyRunning):
		return errSetupRunning
	case errors.Is(err, ruesten.ErrReadOnly):
		return errSetupReadOnly
	case errors.Is(err, ruesten.ErrNotRunning):
		return errSetupNotRunning
	case errors.Is(err, ruesten.ErrNotFound):
		return errSetupNotFound
	}
	componentLog("rüstzeiten").Error().Err(err).Str("aktion", what).Str("benutzer", getUser(r).ID).Msg("rüstzeit nicht gespeichert")
	return errSetupSave
}

// setupParseTimes liest Beginn und Ende (Ortszeit) aus dem Formular. Ohne Ende
// kann eine Dauer in Minuten (duration_min) das Ende ergeben. Beginn und Ende
// dürfen nicht in der Zukunft liegen, das Ende muss nach dem Beginn liegen.
// Entspricht ein Wert dem Wert des Formulars vor der Bearbeitung (auf die
// Minute), bleibt der gespeicherte Zeitpunkt samt Sekunden erhalten.
func setupParseTimes(r *http.Request, old *ruesten.Entry, now time.Time) (start time.Time, end *time.Time, err error) {
	sv := strings.TrimSpace(r.FormValue("started_at"))
	if sv == "" {
		return start, nil, errSetupNeedStart
	}
	if old != nil && sv == old.StartedAt.Local().Format(setupFormLayout) {
		start = old.StartedAt
	} else if start, err = setupFormTime(sv); err != nil {
		return start, nil, errSetupNeedStart
	}
	if start.After(now.Add(setupFutureSlack)) {
		return start, nil, errSetupStartFuture
	}
	if ev := strings.TrimSpace(r.FormValue("ended_at")); ev != "" {
		var e time.Time
		if old != nil && old.EndedAt != nil && ev == old.EndedAt.Local().Format(setupFormLayout) {
			e = *old.EndedAt
		} else if e, err = setupFormTime(ev); err != nil {
			return start, nil, errSetupEndBefore
		}
		end = &e
	} else if mins, ok := setupMinutes(r.FormValue("duration_min")); !ok {
		return start, nil, errSetupDuration
	} else if mins > 0 {
		e := start.Add(time.Duration(mins) * time.Minute)
		end = &e
	}
	if end != nil {
		if !end.After(start) {
			return start, nil, errSetupEndBefore
		}
		if end.After(now.Add(setupFutureSlack)) {
			return start, nil, errSetupEndFuture
		}
	}
	return start, end, nil
}

// SetupCreateWeb: POST /setup – Rüstzeit von Hand anlegen (setup.edit an der Anlage).
func (h *Handler) SetupCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.setupCan(r, permSetupEdit, "") {
		setupDenied(w, r)
		return
	}
	_ = r.ParseForm()
	infraID, reasonID := strings.TrimSpace(r.FormValue("infrastructure_id")), strings.TrimSpace(r.FormValue("reason_id"))
	if !uuidInPathRe.MatchString(infraID) || !uuidInPathRe.MatchString(reasonID) {
		http.Redirect(w, r, setupFormError(r, errSetupInfraReason), http.StatusSeeOther)
		return
	}
	if !h.setupCanInfra(r, permSetupEdit, infraID) {
		setupDenied(w, r)
		return
	}
	start, end, err := setupParseTimes(r, nil, time.Now())
	if err != nil {
		http.Redirect(w, r, setupFormError(r, err), http.StatusSeeOther)
		return
	}
	e, err := h.setupStore().Create(r.Context(), ruesten.ManualInput{InfraID: infraID, ReasonID: reasonID,
		Note: r.FormValue("note"), StartedAt: start, EndedAt: end}, getUser(r).ID)
	if err != nil {
		http.Redirect(w, r, setupFormError(r, setupStoreError(r, "anlegen", err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, setupBack(e.ID, noticeSetupCreated, nil), http.StatusSeeOther)
}

// setupEntry prüft ID und Recht und lädt die Rüstzeit; sonst ist die Antwort schon gesendet.
func (h *Handler) setupEntry(w http.ResponseWriter, r *http.Request, perm string) (*ruesten.Entry, bool) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return nil, false
	}
	if !h.setupCan(r, perm, id) {
		setupDenied(w, r)
		return nil, false
	}
	e, err := h.setupStore().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, ruesten.ErrNotFound) {
			http.Error(w, "Rüstzeit nicht gefunden", http.StatusNotFound)
		} else {
			http.Error(w, "Rüstzeit konnte nicht geladen werden", http.StatusInternalServerError)
		}
		return nil, false
	}
	return e, true
}

// SetupEditWeb: POST /setup/{id}/edit-web – Grund, Beginn, Ende und Vermerk
// korrigieren. Bei einer Rüstzeit mit Störmeldung bleiben Beginn und Ende gesperrt.
func (h *Handler) SetupEditWeb(w http.ResponseWriter, r *http.Request) {
	e, ok := h.setupEntry(w, r, permSetupEdit)
	if !ok {
		return
	}
	_ = r.ParseForm()
	reasonID := strings.TrimSpace(r.FormValue("reason_id"))
	if !uuidInPathRe.MatchString(reasonID) {
		http.Redirect(w, r, setupBack(e.ID, "", errSetupInfraReason), http.StatusSeeOther)
		return
	}
	in := ruesten.ManualInput{InfraID: e.InfraID, ReasonID: reasonID, Note: r.FormValue("note"), StartedAt: e.StartedAt, EndedAt: e.EndedAt}
	if e.Status != ruesten.StatusFault {
		start, end, err := setupParseTimes(r, e, time.Now())
		if err != nil {
			http.Redirect(w, r, setupBack(e.ID, "", err), http.StatusSeeOther)
			return
		}
		in.StartedAt, in.EndedAt = start, end
	}
	if err := h.setupStore().Update(r.Context(), e.ID, in, getUser(r).ID); err != nil {
		http.Redirect(w, r, setupBack(e.ID, "", setupStoreError(r, "bearbeiten", err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, setupBack(e.ID, noticeSetupSaved, nil), http.StatusSeeOther)
}

// SetupFinishWeb: POST /setup/{id}/finish-web – laufende Rüstzeit jetzt beenden
// (optional mit Vermerk).
func (h *Handler) SetupFinishWeb(w http.ResponseWriter, r *http.Request) {
	e, ok := h.setupEntry(w, r, permSetupEdit)
	if !ok {
		return
	}
	_ = r.ParseForm()
	if err := h.setupStore().Finish(r.Context(), e.ID, getUser(r).ID, r.FormValue("note")); err != nil {
		http.Redirect(w, r, setupBack(e.ID, "", setupStoreError(r, "beenden", err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, setupBack(e.ID, noticeSetupFinished, nil), http.StatusSeeOther)
}

// SetupDeleteWeb: POST /setup/{id}/delete-web – endgültig löschen (nur
// Bereichsadministrator). Verlauf, Feldwerte und Kategorien gehen mit.
func (h *Handler) SetupDeleteWeb(w http.ResponseWriter, r *http.Request) {
	e, ok := h.setupEntry(w, r, permSetupAdmin)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := h.setupStore().Delete(ctx, e.ID); err != nil {
		if errors.Is(err, ruesten.ErrNotFound) {
			http.Error(w, "Rüstzeit nicht gefunden", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, setupBack(e.ID, "", setupStoreError(r, "löschen", err)), http.StatusSeeOther)
		return
	}
	for _, q := range []string{
		`DELETE FROM record_history WHERE ref_type = 'setup' AND ref_id = $1::uuid`,
		`DELETE FROM record_field_values WHERE module = 'setup' AND record_id = $1::uuid`,
		`DELETE FROM record_field_sets WHERE module = 'setup' AND record_id = $1::uuid`,
		`DELETE FROM record_categories WHERE module = 'setup' AND record_id = $1::uuid`,
	} {
		if _, err := h.db.Exec(ctx, q, e.ID); err != nil {
			componentLog("rüstzeiten").Warn().Err(err).Str("rüstzeit", e.ID).Msg("zubehör einer gelöschten rüstzeit nicht entfernt")
		}
	}
	componentLog("rüstzeiten").Info().Str("rüstzeit", ruesten.Number(e.Number)).Str("benutzer", getUser(r).ID).Msg("rüstzeit gelöscht")
	http.Redirect(w, r, "/setup?notice="+url.QueryEscape(noticeSetupDeleted), http.StatusSeeOther)
}
