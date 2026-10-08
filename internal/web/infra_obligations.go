package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Prüfpflichten und Gefährdungsbeurteilungen je Anlage (migrations/109):
// Reiter „Prüfungen & GBU“ der Anlage, Überblick auf der Infrastruktur-Seite
// und Erinnerung per PDH-System-Chat – remind_days vor der Fälligkeit und
// noch einmal, wenn sie überschritten ist.

type obligationLog struct {
	DoneOn, Result, Note, DoneBy string
	Defects                      bool
}

type obligationView struct {
	ID, InfraID, InfraName, Kind, KindLabel string
	Title, Basis, Inspector, Notes          string
	IntervalMonths, RemindDays              int
	LastDone, NextDue                       string // TT.MM.JJJJ
	LastDoneISO, NextDueISO                 string // JJJJ-MM-TT (Formulare)
	ResponsibleID, ResponsibleName          string
	State, StateLabel, StateClass           string // overdue | soon | ok | none
	DaysLeft                                int
	Log                                     []obligationLog
}

var obligationKinds = map[string]string{"inspection": "Prüfpflicht", "risk_assessment": "Gefährdungsbeurteilung"}

// obligationNextDue: nächste Fälligkeit nach einer Durchführung (0 Monate = einmalig).
func obligationNextDue(done time.Time, months int) *time.Time {
	if months <= 0 {
		return nil
	}
	n := done.AddDate(0, months, 0)
	return &n
}

// obligationState bewertet die Fälligkeit gegen heute.
func obligationState(next *time.Time, remindDays int, today time.Time) (state string, daysLeft int) {
	if next == nil {
		return "none", 0
	}
	d0 := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	n0 := time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, time.UTC)
	daysLeft = int(n0.Sub(d0).Hours() / 24)
	switch {
	case daysLeft < 0:
		return "overdue", daysLeft
	case daysLeft <= remindDays:
		return "soon", daysLeft
	}
	return "ok", daysLeft
}

func obligationStateLabel(state string, days int) (string, string) {
	switch state {
	case "overdue":
		return fmt.Sprintf("seit %d Tagen überfällig", -days), "b-red"
	case "soon":
		if days == 0 {
			return "heute fällig", "b-amber"
		}
		return fmt.Sprintf("in %d Tagen fällig", days), "b-amber"
	case "ok":
		return "erledigt", "b-green"
	}
	return "ohne Termin", "b-gray"
}

const obligationSelect = `SELECT o.id::text, o.infrastructure_id::text, COALESCE(i.name, ''), o.kind, o.title, o.basis, o.inspector, o.notes,
	o.interval_months, o.remind_days, o.last_done, o.next_due, COALESCE(o.responsible_to::text, ''),
	COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), '')
	FROM infra_obligations o
	JOIN infrastructure i ON i.id = o.infrastructure_id
	LEFT JOIN users u ON u.id = o.responsible_to`

func (h *Handler) scanObligations(ctx context.Context, where string, args ...any) []obligationView {
	rows, err := h.db.Query(ctx, obligationSelect+` WHERE `+where+` ORDER BY o.next_due NULLS LAST, o.title`, args...)
	if err != nil {
		componentLog("pruefpflichten").Warn().Err(err).Msg("laden")
		return nil
	}
	defer rows.Close()
	today := time.Now()
	var out []obligationView
	for rows.Next() {
		var o obligationView
		var last, next *time.Time
		if rows.Scan(&o.ID, &o.InfraID, &o.InfraName, &o.Kind, &o.Title, &o.Basis, &o.Inspector, &o.Notes,
			&o.IntervalMonths, &o.RemindDays, &last, &next, &o.ResponsibleID, &o.ResponsibleName) != nil {
			continue
		}
		o.KindLabel = obligationKinds[o.Kind]
		if last != nil {
			o.LastDone, o.LastDoneISO = last.Format("02.01.2006"), last.Format("2006-01-02")
		}
		if next != nil {
			o.NextDue, o.NextDueISO = next.Format("02.01.2006"), next.Format("2006-01-02")
		}
		o.State, o.DaysLeft = obligationState(next, o.RemindDays, today)
		o.StateLabel, o.StateClass = obligationStateLabel(o.State, o.DaysLeft)
		out = append(out, o)
	}
	return out
}

// infraObligations: alle Einträge einer Anlage mit den letzten Nachweisen.
func (h *Handler) infraObligations(ctx context.Context, infraID string) []obligationView {
	list := h.scanObligations(ctx, `o.infrastructure_id = $1::uuid`, infraID)
	for i := range list {
		rows, err := h.db.Query(ctx, `SELECT l.done_on, l.result, l.note, COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), '')
			FROM infra_obligation_log l LEFT JOIN users u ON u.id = l.done_by
			WHERE l.obligation_id = $1::uuid ORDER BY l.done_on DESC, l.created_at DESC LIMIT 10`, list[i].ID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var l obligationLog
			var d time.Time
			if rows.Scan(&d, &l.Result, &l.Note, &l.DoneBy) == nil {
				l.DoneOn, l.Defects = d.Format("02.01.2006"), l.Result == "defects"
				list[i].Log = append(list[i].Log, l)
			}
		}
		rows.Close()
	}
	return list
}

// dueObligations: überfällig oder im Erinnerungsvorlauf – für den Überblick.
func (h *Handler) dueObligations(ctx context.Context) []obligationView {
	return h.scanObligations(ctx, `o.next_due IS NOT NULL AND o.next_due - o.remind_days <= CURRENT_DATE AND i.active`)
}

// ── Bearbeiten ───────────────────────────────────────────────

func obligationBack(infraID, msg string, err error) string {
	u := "/infrastructure/" + infraID + "?tab=checks"
	if err != nil {
		return u + "&err=" + url.QueryEscape(err.Error())
	}
	return u + "&msg=" + url.QueryEscape(msg)
}

func parseISODate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, errors.New("Ungültiges Datum")
	}
	return &t, nil
}

// obligationForm liest die Stammdaten aus dem Formular.
type obligationInput struct {
	kind, title, basis, inspector, notes string
	interval, remind                     int
	responsible                          string
	lastDone, nextDue                    *time.Time
}

func readObligationForm(r *http.Request) (obligationInput, error) {
	var in obligationInput
	in.kind = r.FormValue("kind")
	if obligationKinds[in.kind] == "" {
		return in, errors.New("Bitte die Art wählen")
	}
	in.title = strings.TrimSpace(r.FormValue("title"))
	if in.title == "" || len([]rune(in.title)) > 200 {
		return in, errors.New("Bitte eine Bezeichnung angeben (höchstens 200 Zeichen)")
	}
	in.basis, in.inspector, in.notes = strings.TrimSpace(r.FormValue("basis")), strings.TrimSpace(r.FormValue("inspector")), strings.TrimSpace(r.FormValue("notes"))
	var err error
	if in.interval, err = strconv.Atoi(r.FormValue("interval_months")); err != nil || in.interval < 0 || in.interval > 240 {
		return in, errors.New("Intervall: 0 bis 240 Monate (0 = einmalig)")
	}
	if in.remind, err = strconv.Atoi(r.FormValue("remind_days")); err != nil || in.remind < 0 || in.remind > 365 {
		return in, errors.New("Erinnerung: 0 bis 365 Tage vorher")
	}
	in.responsible = strings.TrimSpace(r.FormValue("responsible_to"))
	if in.lastDone, err = parseISODate(r.FormValue("last_done")); err != nil {
		return in, err
	}
	if in.nextDue, err = parseISODate(r.FormValue("next_due")); err != nil {
		return in, err
	}
	// ohne eigene Fälligkeit: aus „zuletzt erledigt“ + Intervall
	if in.nextDue == nil && in.lastDone != nil {
		in.nextDue = obligationNextDue(*in.lastDone, in.interval)
	}
	return in, nil
}

// InfraObligationSaveWeb: POST /infrastructure/{id}/obligations[/{oid}] – anlegen bzw. ändern.
func (h *Handler) InfraObligationSaveWeb(w http.ResponseWriter, r *http.Request) {
	infraID, oid := chi.URLParam(r, "id"), chi.URLParam(r, "oid")
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	in, err := readObligationForm(r)
	if err != nil {
		http.Redirect(w, r, obligationBack(infraID, "", err), http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	if oid == "" {
		_, err = h.db.Exec(ctx, `INSERT INTO infra_obligations (infrastructure_id, kind, title, basis, inspector, interval_months, last_done, next_due, remind_days, responsible_to, notes, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11, $12::uuid)`,
			infraID, in.kind, in.title, in.basis, in.inspector, in.interval, in.lastDone, in.nextDue, in.remind, nullID(in.responsible), in.notes, nullID(getUser(r).ID))
	} else {
		_, err = h.db.Exec(ctx, `UPDATE infra_obligations SET kind = $3, title = $4, basis = $5, inspector = $6, interval_months = $7, last_done = $8, next_due = $9,
			remind_days = $10, responsible_to = $11::uuid, notes = $12, updated_at = NOW() WHERE id = $1::uuid AND infrastructure_id = $2::uuid`,
			oid, infraID, in.kind, in.title, in.basis, in.inspector, in.interval, in.lastDone, in.nextDue, in.remind, nullID(in.responsible), in.notes)
	}
	if err != nil {
		componentLog("pruefpflichten").Error().Err(err).Msg("speichern")
		http.Redirect(w, r, obligationBack(infraID, "", errors.New(friendlyDBError(err))), http.StatusSeeOther)
		return
	}
	h.addHistory(ctx, "infrastructure", infraID, "update", obligationKinds[in.kind], "", in.title, obligationKinds[in.kind]+" gespeichert", getUser(r).ID)
	http.Redirect(w, r, obligationBack(infraID, obligationKinds[in.kind]+" gespeichert", nil), http.StatusSeeOther)
}

// InfraObligationDoneWeb: POST /infrastructure/{id}/obligations/{oid}/done – Durchführung
// erfassen (Nachweis) und nächste Fälligkeit setzen.
func (h *Handler) InfraObligationDoneWeb(w http.ResponseWriter, r *http.Request) {
	infraID, oid := chi.URLParam(r, "id"), chi.URLParam(r, "oid")
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	done, err := parseISODate(r.FormValue("done_on"))
	if err == nil && done == nil {
		err = errors.New("Bitte das Datum der Durchführung angeben")
	}
	if err == nil && done.After(time.Now()) {
		err = errors.New("Die Durchführung kann nicht in der Zukunft liegen")
	}
	result := r.FormValue("result")
	if result != "defects" {
		result = "ok"
	}
	ctx := r.Context()
	var months int
	var title, kind string
	if err == nil {
		err = h.db.QueryRow(ctx, `SELECT interval_months, title, kind FROM infra_obligations WHERE id = $1::uuid AND infrastructure_id = $2::uuid`, oid, infraID).Scan(&months, &title, &kind)
	}
	if err == nil {
		_, err = h.db.Exec(ctx, `INSERT INTO infra_obligation_log (obligation_id, done_on, result, note, done_by) VALUES ($1::uuid, $2, $3, $4, $5::uuid)`,
			oid, *done, result, strings.TrimSpace(r.FormValue("note")), nullID(getUser(r).ID))
	}
	if err == nil {
		_, err = h.db.Exec(ctx, `UPDATE infra_obligations SET last_done = $2, next_due = $3, reminded_due = NULL, overdue_reminded_due = NULL, updated_at = NOW() WHERE id = $1::uuid`,
			oid, *done, obligationNextDue(*done, months))
	}
	if err != nil {
		http.Redirect(w, r, obligationBack(infraID, "", err), http.StatusSeeOther)
		return
	}
	res := "ohne Mängel"
	if result == "defects" {
		res = "mit Mängeln"
	}
	h.addHistory(ctx, "infrastructure", infraID, "update", obligationKinds[kind], "", title+" – "+done.Format("02.01.2006")+" "+res, obligationKinds[kind]+" durchgeführt", getUser(r).ID)
	http.Redirect(w, r, obligationBack(infraID, "Durchführung erfasst ("+res+")", nil), http.StatusSeeOther)
}

// InfraObligationDeleteWeb: POST /infrastructure/{id}/obligations/{oid}/delete
func (h *Handler) InfraObligationDeleteWeb(w http.ResponseWriter, r *http.Request) {
	infraID, oid := chi.URLParam(r, "id"), chi.URLParam(r, "oid")
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	var title, kind string
	err := h.db.QueryRow(r.Context(), `DELETE FROM infra_obligations WHERE id = $1::uuid AND infrastructure_id = $2::uuid RETURNING title, kind`, oid, infraID).Scan(&title, &kind)
	if err != nil {
		http.Redirect(w, r, obligationBack(infraID, "", errors.New("Eintrag nicht gefunden")), http.StatusSeeOther)
		return
	}
	h.addHistory(r.Context(), "infrastructure", infraID, "update", obligationKinds[kind], title, "", obligationKinds[kind]+" gelöscht", getUser(r).ID)
	http.Redirect(w, r, obligationBack(infraID, obligationKinds[kind]+" gelöscht", nil), http.StatusSeeOther)
}

// ── Erinnerung ───────────────────────────────────────────────

// StartObligationReminders prüft alle 6 Stunden die Fälligkeiten.
func (h *Handler) StartObligationReminders(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				if n, err := h.runObligationReminders(runCtx); err != nil {
					componentLog("pruefpflichten").Error().Err(err).Msg("erinnerungen")
				} else if n > 0 {
					componentLog("pruefpflichten").Info().Int("erinnert", n).Msg("erinnerungen verschickt")
				}
				cancel()
				timer.Reset(6 * time.Hour)
			}
		}
	}()
}

// obligationReminderText: Chat-Hinweis an die verantwortliche Person.
func obligationReminderText(o obligationView, base string) string {
	when := "ist fällig am " + o.NextDue
	switch {
	case o.State == "overdue":
		when = "ist seit " + o.NextDue + " überfällig"
	case o.DaysLeft > 0:
		when = fmt.Sprintf("ist in %d Tagen fällig (%s)", o.DaysLeft, o.NextDue)
	}
	text := "🔔 " + o.KindLabel + " „" + o.Title + "“ an " + o.InfraName + " " + when + "."
	if o.Basis != "" {
		text += "\nGrundlage: " + o.Basis
	}
	if o.Inspector != "" {
		text += "\nPrüfer: " + o.Inspector
	}
	return text + "\n" + base + "/infrastructure/" + o.InfraID + "?tab=checks"
}

// runObligationReminders: einmal je Fälligkeit zu Beginn des Vorlaufs, einmal bei Überfälligkeit.
func (h *Handler) runObligationReminders(ctx context.Context) (int, error) {
	list := h.dueObligations(ctx)
	base := strings.TrimRight(strings.TrimSpace(h.mailCfg.PublicURL), "/") // ohne Eintrag: relativer Link
	sent := 0
	for _, o := range list {
		col := "reminded_due"
		if o.State == "overdue" {
			col = "overdue_reminded_due"
		}
		// nur einmal je Stufe und Fälligkeit (atomar, auch bei mehreren Instanzen)
		tag, err := h.db.Exec(ctx, `UPDATE infra_obligations SET `+col+` = next_due
			WHERE id = $1::uuid AND next_due IS NOT NULL AND `+col+` IS DISTINCT FROM next_due`, o.ID)
		if err != nil {
			return sent, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		to := []string{o.ResponsibleID}
		if o.ResponsibleID == "" {
			to = h.adminIDs(ctx)
		}
		h.systemNotify(ctx, to, obligationReminderText(o, base))
		sent++
	}
	return sent, nil
}
