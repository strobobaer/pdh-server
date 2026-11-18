package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/ruesten"
	"pdh/pkg/appsettings"
)

// Rüsten an der Anlage: Easy-Modus (öffentlich, /e/{id}/setup/…) und Seite der
// Anlage für Angemeldete (/infrastructure/{id}/setup/…). Beide zeigen denselben
// Baustein (widgets/setup_buttons.gohtml). Dazu der Status „Rüsten“ bzw.
// „Störung durch Rüsten“ der Anlage und die Überwachung, die nach Höchstdauer
// plus Karenzzeit die Störmeldung anlegt (Monitor in internal/modules/ruesten).

// Eigene Grenze je Anlage, unabhängig von den Meldungen (easyLimit): höchstens
// 30 Rüst-Vorgänge in 10 Minuten.
const (
	setupLimitMax = 30
	setupLimitPer = 10 * time.Minute
)

var setupLimiter = &easyLimiter{hits: map[string][]time.Time{}}

// Zustand einer Anlage (Kennzeichen; angezeigt wird er über {{t}}): es gilt die
// neueste Rüstzeit der Anlage – läuft sie, ist es "running"; ist sie in eine noch
// offene Störmeldung übergegangen, "fault".
const (
	setupKindRunning = ruesten.StatusRunning
	setupKindFault   = ruesten.StatusFault
)

type setupBarButton struct {
	ID, Name string
	Current  bool // läuft gerade
}

// setupBarView ist der Baustein „Rüsten“ einer Anlage.
type setupBarView struct {
	Show    bool // Rüsttasten vorhanden oder etwas läuft/steht – sonst bleibt die Seite wie sie war
	Action  string
	Back    string
	Test    bool
	Buttons []setupBarButton
	Running bool
	Fault   bool
	Text    string
	Overrun bool
	Notice  string
}

// setupNotices: Hinweise nach einer Aktion (Kennung → Text). Der Text ist
// zugleich der Übersetzungsschlüssel.
func setupNotice(lang, code string) string {
	switch code {
	case "rate":
		return tr(lang, "Zu viele Vorgänge an dieser Anlage – bitte kurz warten.")
	case "same":
		return tr(lang, "Diese Rüstzeit läuft bereits.")
	case "none":
		return tr(lang, "Hier läuft gerade kein Rüsten.")
	case "gone":
		return tr(lang, "Diese Rüsttaste gibt es hier nicht (mehr).")
	case "err":
		return tr(lang, "Das hat nicht geklappt – bitte nochmal versuchen.")
	}
	return ""
}

// setupBarFor baut den Baustein. action ist der Pfad ohne /start bzw. /finish.
func (h *Handler) setupBarFor(ctx context.Context, lang, infraID, action, back string, test bool, notice string) setupBarView {
	v := setupBarView{Action: action, Back: back, Test: test, Notice: setupNotice(lang, notice)}
	st := h.setupStore()
	if st == nil {
		return v
	}
	buttons, err := st.InfraButtons(ctx, infraID)
	if err != nil {
		componentLog("ruesten").Warn().Err(err).Msg("rüsttasten laden")
	}
	run, err := st.Running(ctx, infraID)
	if err != nil {
		componentLog("ruesten").Warn().Err(err).Msg("laufende rüstzeit laden")
	}
	names := make([]string, 0, len(buttons))
	for _, b := range buttons {
		names = append(names, b.Name)
	}
	labels := h.setupButtonLabels(ctx, lang, names)
	for _, b := range buttons {
		v.Buttons = append(v.Buttons, setupBarButton{ID: b.ID, Name: labels[b.Name], Current: run != nil && run.ButtonID == b.ID})
	}
	kind := h.setupPlantState(ctx, infraID) // dieselbe Bestimmung wie Kopfzeile und Schnellübersicht
	if run != nil && kind == setupKindRunning {
		v.Running = true
		v.Text = tr(lang, "Rüsten: %s seit %s", setupLabel(labels, run.ButtonName, run.ReasonName), setupSince(run.StartedAt))
		v.Overrun = time.Now().After(run.StartedAt.Add(time.Duration(run.MaxMinutes) * time.Minute))
	} else if kind == setupKindFault {
		v.Fault = true
		v.Text = tr(lang, "Störung durch Rüsten – Instandhaltung ist informiert")
	}
	v.Show = len(v.Buttons) > 0 || v.Running || v.Fault
	return v
}

// setupLabel: Anzeigename einer laufenden Rüstzeit (übersetzt, sonst der gespeicherte Name).
func setupLabel(labels map[string]string, name, reason string) string {
	if name == "" {
		name = reason
	}
	if l := labels[name]; l != "" {
		return l
	}
	return name
}

// setupButtonLabels übersetzt die Namen der Rüsttasten für die Anzeige; ohne
// Dienst oder bei Zeitüberschreitung bleibt der deutsche Name stehen.
func (h *Handler) setupButtonLabels(ctx context.Context, lang string, names []string) map[string]string {
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = n
	}
	if lang == defaultLang || len(names) == 0 || !h.liveTranslateAvailable() {
		return out
	}
	tctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	m, err := h.translateCached(tctx, lang, names)
	if err != nil && len(m) == 0 {
		componentLog("ruesten").Warn().Err(err).Msg("rüsttasten übersetzen")
		return out
	}
	for n, l := range m {
		if l = strings.TrimSpace(l); l != "" {
			out[n] = l
		}
	}
	return out
}

// setupSince: „10:42“, an einem anderen Tag mit Datum.
func setupSince(t time.Time) string {
	t = t.Local()
	if t.YearDay() == time.Now().YearDay() && t.Year() == time.Now().Year() {
		return t.Format("15:04")
	}
	return t.Format("02.01. 15:04")
}

// setupPlantStates: Zustand der Anlagen als Kennzeichen (setupKindRunning,
// setupKindFault oder fehlend). Die neueste Rüstzeit der Anlage (nach Beginn)
// entscheidet: läuft sie, gilt "running"; ist sie "fault" und die Störmeldung noch
// offen, gilt "fault"; sonst nichts.
func (h *Handler) setupPlantStates(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if h.db == nil || len(ids) == 0 {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT ON (s.infrastructure_id) s.infrastructure_id::text,
			CASE WHEN s.status = 'running' THEN 'running'
			     WHEN s.status = 'fault' AND f.id IS NOT NULL AND f.archived_at IS NULL
			          AND f.status::text IN ('detected','analyzing','in_progress','pending') THEN 'fault'
			     ELSE '' END
		FROM setup_times s LEFT JOIN faults f ON f.id = s.fault_id
		WHERE s.infrastructure_id = ANY($1::uuid[]) AND s.archived_at IS NULL
		ORDER BY s.infrastructure_id, s.started_at DESC, s.number DESC`, ids)
	if err != nil {
		componentLog("ruesten").Warn().Err(err).Msg("anlagenstatus")
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind string
		if rows.Scan(&id, &kind) == nil && kind != "" {
			out[id] = kind
		}
	}
	return out
}

// setupPlantState: Kennzeichen der Anlage ("running", "fault" oder "").
func (h *Handler) setupPlantState(ctx context.Context, infraID string) string {
	return h.setupPlantStates(ctx, []string{infraID})[infraID]
}

// setupCanPlant: darf die angemeldete Person die Anlage sehen (und damit rüsten)?
func (h *Handler) setupCanPlant(r *http.Request, infraID string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, "infrastructure.view")
	}
	return h.hasPermRec(r, "infrastructure.view", "infrastructure", infraID) ||
		h.hasPermRec(r, "infrastructure.edit", "infrastructure", infraID)
}

// setupBackPath: nur eigene Pfade der Anlagenseiten als Rücksprung zulassen.
func setupBackPath(back, fallback string) string {
	if (strings.HasPrefix(back, "/a/") || strings.HasPrefix(back, "/infrastructure/")) &&
		!strings.ContainsAny(back, "\\\r\n") && !strings.HasPrefix(back, "//") {
		return back
	}
	return fallback
}

// setupRedirect schickt zurück zur Seite; notice ist die Kennung eines Hinweises.
func setupRedirect(w http.ResponseWriter, r *http.Request, back, notice string) {
	if notice != "" {
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		back += sep + "setup=" + url.QueryEscape(notice)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// setupStartFor / setupFinishFor tun die Arbeit für beide Wege und liefern die
// Kennung eines Hinweises ("" = gelungen).
func (h *Handler) setupStartFor(ctx context.Context, infraID, buttonID, userID, origin string) string {
	st := h.setupStore()
	if st == nil {
		return "err"
	}
	allowed := false
	buttons, err := st.InfraButtons(ctx, infraID)
	if err != nil {
		return "err"
	}
	for _, b := range buttons {
		allowed = allowed || b.ID == buttonID
	}
	if !allowed {
		return "gone"
	}
	if !setupLimiter.allow("asset:"+infraID, setupLimitMax, setupLimitPer, time.Now()) {
		return "rate"
	}
	e, err := st.Start(ctx, infraID, buttonID, userID, origin)
	switch {
	case errors.Is(err, ruesten.ErrSameButton):
		return "same"
	case errors.Is(err, ruesten.ErrNotFound):
		return "gone"
	case err != nil:
		componentLog("ruesten").Error().Err(err).Str("anlage", infraID).Msg("rüsten starten")
		return "err"
	}
	componentLog("ruesten").Info().Str("anlage", infraID).Str("nr", ruesten.Number(e.Number)).Str("herkunft", origin).Msg("rüsten gestartet")
	return ""
}

func (h *Handler) setupFinishFor(ctx context.Context, infraID, note, userID string) string {
	st := h.setupStore()
	if st == nil {
		return "err"
	}
	if !setupLimiter.allow("asset:"+infraID, setupLimitMax, setupLimitPer, time.Now()) {
		return "rate"
	}
	run, err := st.Running(ctx, infraID)
	if err != nil {
		return "err"
	}
	if run == nil {
		return "none"
	}
	switch err := st.Finish(ctx, run.ID, userID, note); {
	case errors.Is(err, ruesten.ErrNotRunning):
		return "none"
	case err != nil:
		componentLog("ruesten").Error().Err(err).Str("anlage", infraID).Msg("rüsten beenden")
		return "err"
	}
	return ""
}

// easySetupGate prüft Kennung und Easy-Modus; false = Antwort ist schon gegeben.
func (h *Handler) easySetupGate(w http.ResponseWriter, r *http.Request) (id string, test, ok bool) {
	id = chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) || len(id) != 36 {
		http.NotFound(w, r)
		return "", false, false
	}
	_ = r.ParseForm()
	test = r.FormValue("test") == "1" && h.easyAdmin(r)
	if !h.easyModeEnabled(r.Context()) && !test {
		http.NotFound(w, r)
		return "", false, false
	}
	if name, _, _ := h.easyAsset(r.Context(), id); name == "" {
		http.NotFound(w, r)
		return "", false, false
	}
	return id, test, true
}

func easyBack(id string, test bool) string {
	if test {
		return "/e/" + id + "?test=1"
	}
	return "/e/" + id
}

// EasySetupStartWeb: POST /e/{id}/setup/start (button_id) – öffentlich wie die
// Meldungen; Ersteller ist „Service“.
func (h *Handler) EasySetupStartWeb(w http.ResponseWriter, r *http.Request) {
	id, test, ok := h.easySetupGate(w, r)
	if !ok {
		return
	}
	setupRedirect(w, r, easyBack(id, test), h.setupStartFor(r.Context(), id, r.FormValue("button_id"), pdhSystemUserID, ruesten.OriginEasy))
}

// EasySetupFinishWeb: POST /e/{id}/setup/finish (note) – beendet die laufende Rüstzeit.
func (h *Handler) EasySetupFinishWeb(w http.ResponseWriter, r *http.Request) {
	id, test, ok := h.easySetupGate(w, r)
	if !ok {
		return
	}
	setupRedirect(w, r, easyBack(id, test), h.setupFinishFor(r.Context(), id, r.FormValue("note"), pdhSystemUserID))
}

// infraSetupGate prüft Kennung und das Recht „Anlage sehen“.
func (h *Handler) infraSetupGate(w http.ResponseWriter, r *http.Request) (id, back, uid string, ok bool) {
	id = chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) || len(id) != 36 {
		http.NotFound(w, r)
		return "", "", "", false
	}
	if !h.setupCanPlant(r, id) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return "", "", "", false
	}
	_ = r.ParseForm()
	u := getUser(r)
	if u != nil {
		uid = u.ID
	}
	return id, setupBackPath(r.FormValue("back"), "/a/"+id), uid, true
}

// InfraSetupStartWeb: POST /infrastructure/{id}/setup/start (button_id).
func (h *Handler) InfraSetupStartWeb(w http.ResponseWriter, r *http.Request) {
	id, back, uid, ok := h.infraSetupGate(w, r)
	if !ok {
		return
	}
	setupRedirect(w, r, back, h.setupStartFor(r.Context(), id, r.FormValue("button_id"), uid, ruesten.OriginManual))
}

// InfraSetupFinishWeb: POST /infrastructure/{id}/setup/finish (note).
func (h *Handler) InfraSetupFinishWeb(w http.ResponseWriter, r *http.Request) {
	id, back, uid, ok := h.infraSetupGate(w, r)
	if !ok {
		return
	}
	setupRedirect(w, r, back, h.setupFinishFor(r.Context(), id, r.FormValue("note"), uid))
}

// ── Störmeldung bei Überschreitung ───────────────────────────────────────

// setupFaultCategory: Kategorie, die die Migration anlegt.
const setupFaultCategory = "Störung durch Rüsten"

// setupFaultMarker: Zeile in der Beschreibung, an der die Störmeldung zur
// Rüstzeit wiedergefunden wird – damit ein zweiter Versuch (z. B. nach einem
// Fehler hinter dem Anlegen) die vorhandene Störmeldung nutzt statt eine
// zweite anzulegen.
func setupFaultMarker(id string) string { return "/setup/" + id }

// setupFaultKeyField: Feldname des Verlaufseintrags, der die Störmeldung fest mit der
// Rüstzeit verbindet (new_value = Kennung der Rüstzeit). Anders als die Beschreibung
// lässt er sich in der Oberfläche nicht ändern.
const setupFaultKeyField = "setup_time"

// findSetupFault liefert die Störmeldung zur Rüstzeit ("" = gibt es noch nicht):
// zuerst über den Schlüssel im Verlauf, als Rückfall über die Markerzeile der Beschreibung.
func (h *Handler) findSetupFault(ctx context.Context, e ruesten.Entry) (string, error) {
	var id string
	err := h.db.QueryRow(ctx, `SELECT h.ref_id::text FROM record_history h JOIN faults f ON f.id = h.ref_id
		WHERE h.ref_type = 'fault' AND h.field_name = $1 AND h.new_value = $2 ORDER BY h.created_at LIMIT 1`, setupFaultKeyField, e.ID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = h.db.QueryRow(ctx, `SELECT id::text FROM faults WHERE infrastructure_id = $1::uuid
		AND position($2 in description) > 0 ORDER BY created_at LIMIT 1`, e.InfraID, setupFaultMarker(e.ID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// setupFaultText: Titel und Beschreibung der Störmeldung.
func (h *Handler) setupFaultText(ctx context.Context, e ruesten.Entry) (title, desc string) {
	name := e.ButtonName
	if name == "" {
		name = e.ReasonName
	}
	who := "Service (Easy-Modus)"
	if e.CreatedBy != "" && e.CreatedBy != pdhSystemUserID {
		var n string
		if h.db.QueryRow(ctx, `SELECT TRIM(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid`, e.CreatedBy).Scan(&n) == nil && n != "" {
			who = n
		}
	}
	title = "Störung durch Rüsten: " + name
	if r := []rune(title); len(r) > 200 {
		title = string(r[:199]) + "…"
	}
	desc = fmt.Sprintf("Die Anlage „%s“ steht noch im Rüsten, obwohl die erlaubte Zeit abgelaufen ist.\n\n"+
		"Rüsten: %s\nBeginn: %s\nHöchstdauer: %d Minuten\nKarenzzeit: %d Minuten\nGestartet von: %s\n\n"+
		"Rüstzeit %s: %s",
		e.InfraName, name, e.StartedAt.Local().Format("02.01.2006 15:04"), e.MaxMinutes, e.GraceMinutes, who,
		ruesten.Number(e.Number), setupFaultMarker(e.ID))
	return title, desc
}

// createSetupFault legt die Störmeldung „Störung durch Rüsten“ an (Schweregrad
// hoch, „Anlage steht“, Kategorie, Verlauf) und liefert deren Kennung. Der Aufruf
// ist wiederholbar: Gibt es zur Rüstzeit schon eine Störmeldung, wird sie
// wiederverwendet und nur ergänzt, was noch fehlt.
//
// Der Monitor hält beim Aufruf eine Zeile von setup_times gesperrt und eine
// Pool-Verbindung; hier wird deshalb nie in setup_times geschrieben. Das Anlegen
// braucht selbst eine Transaktion und für die Vorgabe-Fälligkeit eine weitere
// Verbindung (vorab ermittelt) – der Ablauf kommt mit zwei gleichzeitigen
// Verbindungen aus: Transaktion des Monitors und Anlege-Transaktion.
func (h *Handler) createSetupFault(ctx context.Context, e ruesten.Entry) (string, error) {
	if h.faults == nil || h.db == nil {
		return "", errors.New("kein Dienst für Störmeldungen")
	}
	msg := fmt.Sprintf("Angelegt, weil die Rüstzeit %s die Höchstdauer (%d min) plus Karenzzeit (%d min) überschritten hat", ruesten.Number(e.Number), e.MaxMinutes, e.GraceMinutes)
	id, err := h.findSetupFault(ctx, e)
	if err != nil {
		return "", err
	}
	if id == "" {
		// Störmeldung und Schlüssel (Verlaufseintrag mit der Kennung der Rüstzeit) in
		// einer Transaktion: gibt es die Störmeldung, gibt es auch den Schlüssel.
		title, desc := h.setupFaultText(ctx, e)
		infra := e.InfraID
		// Fälligkeit vorab: in der Transaktion braucht das Anlegen keine weitere Verbindung.
		due := time.Now().AddDate(0, 0, appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysFault, appsettings.DefaultDueDaysFallback))
		tx, err := h.db.Begin(ctx)
		if err != nil {
			return "", err
		}
		defer tx.Rollback(ctx)
		f, err := h.faults.CreateInTx(ctx, tx, &faults.CreateFaultInput{Title: title, Description: desc, Severity: "high", InfrastructureID: &infra, DueDate: &due}, pdhSystemUserID)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
			VALUES ('fault', $1::uuid, 'create', $2, '', $3, $4, $5::uuid)`, f.ID, setupFaultKeyField, e.ID, msg, pdhSystemUserID); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		h.faults.AfterCreate(f)
		id = f.ID
	}
	var stopped bool
	if err := h.db.QueryRow(ctx, `SELECT plant_stopped FROM faults WHERE id = $1::uuid`, id).Scan(&stopped); err == nil && !stopped {
		h.markPlantStopped(ctx, "fault", id, pdhSystemUserID)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO record_categories (module, record_id, category_id, created_by)
		SELECT 'fault', $1::uuid, c.id, $2::uuid FROM categories c WHERE lower(c.name) = lower($3)
		ON CONFLICT DO NOTHING`, id, pdhSystemUserID, setupFaultCategory); err != nil {
		return "", fmt.Errorf("kategorie: %w", err)
	}
	// Nach einem früheren Fehler: Verlaufseintrag nachholen, falls er fehlt.
	_, _ = h.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
		SELECT 'fault', $1::uuid, 'create', $2, '', $3, $4, $5::uuid
		WHERE NOT EXISTS (SELECT 1 FROM record_history WHERE ref_type = 'fault' AND ref_id = $1::uuid AND message = $4)`,
		id, setupFaultKeyField, e.ID, msg, pdhSystemUserID)
	return id, nil
}

// StartSetupMonitor startet die Überwachung der Rüstzeiten (nur am aktiven
// Server; ruht in der Wartungssperre).
func (h *Handler) StartSetupMonitor(ctx context.Context) {
	st := h.setupStore()
	if st == nil || h.faults == nil {
		log.Warn().Msg("rüstzeiten: überwachung nicht gestartet (keine Datenbank oder kein Störungsdienst)")
		return
	}
	m := &ruesten.Monitor{Store: st, CreateFault: h.createSetupFault, Skip: h.MaintLocked, SystemUser: pdhSystemUserID}
	go m.Start(ctx)
}
