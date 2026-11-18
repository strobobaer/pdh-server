package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/energie"
)

// Schwellwerte an Messstellen (energy_thresholds.gohtml): Liste und Formular je
// Messstelle als htmx-Fragment, eingeblendet unter der Zeile der Übersicht.
// Gespeichert und geprüft wird in energie.Store (thresholds.go); der Wächter,
// der die Alarme auslöst, steht in internal/modules/energie/alarms.go.

// ThresholdRow ist eine Zeile der Schwellwert-Liste.
type ThresholdRow struct {
	energie.Threshold
	CondText, LimitText, WindowText, ResetText string
	PriorityText, ResponsibleText              string
	StateText                                  string
	Alarm, Clearing                            bool
}

// ThresholdForm: Eingaben des Formulars, so wie das Formular sie wieder anzeigt.
type ThresholdForm struct {
	ID, Name, Condition, Limit, Window, Reset, Tag, Priority string
	Responsible                                              string // "", "user:<id>" oder "group:<id>"
	Active                                                   bool
}

// ThresholdChoice: eine Auswahl für Benutzer, Gruppe oder Priorität.
type ThresholdChoice struct{ Value, Label string }

// ThresholdsData ist das Datenmodell des Fragments.
type ThresholdsData struct {
	MeterID, MeterName, Unit string
	Rows                     []ThresholdRow
	CanSave, CanDelete       bool
	ShowForm                 bool
	Form                     ThresholdForm
	Err, Notice              string
	CountText                string // nur bei Speichern/Löschen: frische Beschriftung des Zählerknopfs (hx-swap-oob)
	CountAlarms              int
	Users, Groups            []ThresholdChoice
	Tags                     []string
	Priorities               []ThresholdChoice
}

var thresholdPriorities = []ThresholdChoice{
	{"low", "Niedrig"}, {"medium", "Mittel"}, {"high", "Hoch"}, {"critical", "Kritisch"},
}

func thresholdPriorityLabel(p string) string {
	for _, c := range thresholdPriorities {
		if c.Value == p {
			return c.Label
		}
	}
	return p
}

// thresholdTime: „TT.MM. hh:mm“.
func thresholdTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("02.01. 15:04")
}

// thresholdStateText: „normal“, „Alarm seit …“ oder „zurückschaltend seit …“.
// Ein Alarm mit gesetztem clear_since schaltet gerade zurück (Rückschaltzeit läuft).
func thresholdStateText(t energie.Threshold) string {
	if t.State != "alarm" {
		return "normal"
	}
	if t.ClearSince != nil {
		return "zurückschaltend seit " + thresholdTime(t.ClearSince)
	}
	return "Alarm seit " + thresholdTime(t.AlarmSince)
}

// thresholdFormFromThreshold füllt das Formular zum Bearbeiten.
func thresholdFormFromThreshold(t energie.Threshold) ThresholdForm {
	f := ThresholdForm{
		ID: t.ID, Name: t.Name, Condition: t.Condition, Limit: meterNumber(t.Limit),
		Window: strconv.Itoa(t.WindowMinutes), Reset: strconv.Itoa(t.ResetMinutes),
		Tag: t.Tag, Priority: t.Priority, Active: t.Active,
	}
	if t.ResponsibleUserID != nil {
		f.Responsible = "user:" + *t.ResponsibleUserID
	} else if t.ResponsibleGroupID != nil {
		f.Responsible = "group:" + *t.ResponsibleGroupID
	}
	return f
}

// newThresholdForm: Vorgaben für einen neuen Schwellwert.
func newThresholdForm() ThresholdForm {
	return ThresholdForm{Condition: energie.CondAbove, Window: "15", Reset: "15", Priority: "medium", Active: true}
}

// thresholdPeople liefert Benutzer und Gruppen für die Verantwortlichen-Auswahl.
func (h *Handler) thresholdPeople(ctx context.Context) (usersOut, groupsOut []ThresholdChoice) {
	if h.db == nil {
		return nil, nil
	}
	if rows, err := h.db.Query(ctx, `SELECT id::text, trim(first_name || ' ' || last_name) FROM users
		WHERE active AND NOT is_system_user ORDER BY lower(last_name), lower(first_name)`); err == nil {
		for rows.Next() {
			var c ThresholdChoice
			if rows.Scan(&c.Value, &c.Label) == nil {
				usersOut = append(usersOut, c)
			}
		}
		rows.Close()
	}
	if rows, err := h.db.Query(ctx, `SELECT id::text, name FROM user_groups WHERE active ORDER BY lower(name)`); err == nil {
		for rows.Next() {
			var c ThresholdChoice
			if rows.Scan(&c.Value, &c.Label) == nil {
				groupsOut = append(groupsOut, c)
			}
		}
		rows.Close()
	}
	return usersOut, groupsOut
}

// thresholdTags: vorhandene Schlagworte der Tickets als Vorschläge (frei wählbar).
func (h *Handler) thresholdTags(ctx context.Context) []string {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT tag FROM (
			SELECT jsonb_array_elements_text(tags) AS tag FROM tickets WHERE jsonb_typeof(tags) = 'array'
			UNION SELECT tag FROM energy_thresholds WHERE tag <> '') x
		WHERE tag <> '' ORDER BY tag LIMIT 200`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// renderThresholds baut das Fragment einer Messstelle und schreibt es.
// edit/new steuern das Formular; form (falls gesetzt) ersetzt die Vorbelegung.
func (h *Handler) renderThresholds(w http.ResponseWriter, r *http.Request, st *energie.Store, meterID string, form *ThresholdForm, notice, errMsg string) {
	ctx := r.Context()
	m, err := st.Get(ctx, meterID)
	if err != nil {
		status, msg := http.StatusInternalServerError, "Die Schwellwerte konnten nicht geladen werden."
		if errors.Is(err, energie.ErrNotFound) {
			status, msg = http.StatusNotFound, meterSaveMessage(err)
		}
		writeGlobalBoardError(w, status, msg)
		return
	}
	list, err := st.Thresholds(ctx, meterID)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("schwellwerte laden")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Die Schwellwerte konnten nicht geladen werden.")
		return
	}
	d := ThresholdsData{
		MeterID: meterID, MeterName: m.Name, Unit: m.Unit, Notice: notice, Err: errMsg,
		CanSave:    h.energyCan(r, permEnergyThresholds, meterID),
		CanDelete:  h.energyCan(r, permEnergyAdmin, meterID),
		Priorities: thresholdPriorities,
	}
	if d.MeterName == "" {
		d.MeterName = m.Key
	}
	d.Users, d.Groups = h.thresholdPeople(ctx)
	names := map[string]string{}
	inactive := func(kind, id string) string { // Name auch bei inaktiven Benutzern/Gruppen
		var n string
		q := `SELECT trim(first_name || ' ' || last_name) FROM users WHERE id = $1::uuid`
		if kind == "group" {
			q = `SELECT name || ' (Gruppe)' FROM user_groups WHERE id = $1::uuid`
		}
		if h.db == nil || h.db.QueryRow(ctx, q, id).Scan(&n) != nil || n == "" {
			return "unbekannt"
		}
		return n + " (inaktiv)"
	}
	for _, c := range d.Users {
		names["user:"+c.Value] = c.Label
	}
	for _, c := range d.Groups {
		names["group:"+c.Value] = c.Label + " (Gruppe)"
	}
	for _, t := range list {
		row := ThresholdRow{Threshold: t, LimitText: meterNumber(t.Limit) + unitSuffix(m.Unit),
			WindowText: strconv.Itoa(t.WindowMinutes) + " min", ResetText: strconv.Itoa(t.ResetMinutes) + " min",
			PriorityText: thresholdPriorityLabel(t.Priority), StateText: thresholdStateText(t),
			Alarm: t.State == "alarm", Clearing: t.State == "alarm" && t.ClearSince != nil}
		row.CondText = "über"
		if t.Condition == energie.CondBelow {
			row.CondText = "unter"
		}
		row.ResponsibleText = "Verantwortlicher der Anlage"
		if t.ResponsibleUserID != nil {
			row.ResponsibleText = names["user:"+*t.ResponsibleUserID]
			if row.ResponsibleText == "" {
				row.ResponsibleText = inactive("user", *t.ResponsibleUserID)
			}
		} else if t.ResponsibleGroupID != nil {
			row.ResponsibleText = names["group:"+*t.ResponsibleGroupID]
			if row.ResponsibleText == "" {
				row.ResponsibleText = inactive("group", *t.ResponsibleGroupID)
			}
		}
		d.Rows = append(d.Rows, row)
	}
	if notice != "" { // gespeichert/gelöscht: Zählerknopf in der Tabelle mit auffrischen
		cnt := energie.ThresholdCount{Total: len(list)}
		for _, t := range list {
			if t.State == "alarm" {
				cnt.Alarms++
			}
		}
		d.CountText, d.CountAlarms = thresholdCountText(cnt)
	}
	switch {
	case form != nil:
		d.ShowForm, d.Form = true, *form
	case r.URL.Query().Get("new") == "1":
		d.ShowForm, d.Form = true, newThresholdForm()
	case r.URL.Query().Get("edit") != "":
		for _, t := range list {
			if t.ID == r.URL.Query().Get("edit") {
				d.ShowForm, d.Form = true, thresholdFormFromThreshold(t)
			}
		}
	}
	if !d.CanSave {
		d.ShowForm = false
	}
	d.Tags = h.thresholdTags(ctx)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "energy-thresholds", d)
	}
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("schwellwerte darstellen")
		http.Error(w, "Die Schwellwerte konnten nicht dargestellt werden.", http.StatusInternalServerError)
	}
}

func unitSuffix(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

// thresholdMeterID liest die Messstellen-ID aus dem Pfad; ungültig: 404.
func thresholdMeterID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		writeGlobalBoardError(w, http.StatusNotFound, meterSaveMessage(energie.ErrNotFound))
		return "", false
	}
	return id, true
}

// EnergyThresholdsWeb: GET /energy/meters/{id}/thresholds – Fragment mit Liste
// (und mit ?new=1 bzw. ?edit=<id> dem Formular).
func (h *Handler) EnergyThresholdsWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := thresholdMeterID(w, r)
	if !ok {
		return
	}
	if !h.energyCan(r, permEnergyView, id) {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	h.renderThresholds(w, r, st, id, nil, "", "")
}

// thresholdFromForm liest das Formular. Fehler sind Anwendertexte.
func (h *Handler) thresholdFromForm(r *http.Request, meterID string) (*energie.Threshold, ThresholdForm, string) {
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	f := ThresholdForm{
		ID: v("tid"), Name: v("name"), Condition: v("condition"), Limit: v("limit"), Window: v("window_minutes"),
		Reset: v("reset_minutes"), Tag: v("tag"), Priority: v("priority"), Responsible: v("responsible"),
		Active: r.PostFormValue("active") != "",
	}
	t := &energie.Threshold{ID: f.ID, MeterID: meterID, Name: f.Name, Condition: f.Condition, Tag: f.Tag,
		Priority: f.Priority, Active: f.Active}
	if f.ID != "" && !meterUUIDRe.MatchString(f.ID) {
		return nil, f, "Der Schwellwert wurde nicht gefunden."
	}
	lim, err := strconv.ParseFloat(strings.ReplaceAll(f.Limit, ",", "."), 64)
	if err != nil {
		return nil, f, "Der Grenzwert muss eine Zahl sein."
	}
	t.Limit = lim
	// Minuten: eine ungültige Eingabe wird 0 und scheitert an der Prüfung im Speicher.
	t.WindowMinutes, _ = strconv.Atoi(f.Window)
	t.ResetMinutes, _ = strconv.Atoi(f.Reset)
	if f.Responsible != "" {
		kind, id, _ := strings.Cut(f.Responsible, ":")
		if !meterUUIDRe.MatchString(id) || (kind != "user" && kind != "group") {
			return nil, f, "Die gewählte Person oder Gruppe gibt es nicht."
		}
		table := "users"
		if kind == "group" {
			table = "user_groups"
		}
		var exists bool
		if h.db != nil {
			_ = h.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1::uuid)`, id).Scan(&exists)
		}
		if !exists {
			return nil, f, "Die gewählte Person oder Gruppe gibt es nicht."
		}
		if kind == "user" {
			t.ResponsibleUserID = &id
		} else {
			t.ResponsibleGroupID = &id
		}
	}
	return t, f, ""
}

// EnergyThresholdSaveWeb: POST /energy/meters/{id}/thresholds/save-web (Formular).
// Ohne tid wird angelegt, mit tid geändert. Antwort: das Fragment, bei Fehlern
// mit Meldung und den eingegebenen Werten (Status 200, damit htmx es einsetzt).
func (h *Handler) EnergyThresholdSaveWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := thresholdMeterID(w, r)
	if !ok {
		return
	}
	if !h.energyCan(r, permEnergyThresholds, id) {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe.")
		return
	}
	t, form, msg := h.thresholdFromForm(r, id)
	if msg == "" {
		if t.ID != "" {
			// nur Schwellwerte dieser Messstelle ändern
			if old, err := st.Threshold(r.Context(), t.ID); err != nil || old.MeterID != id {
				writeGlobalBoardError(w, http.StatusNotFound, "Der Schwellwert wurde nicht gefunden.")
				return
			}
		}
		if err := t.Validate(); err != nil {
			msg = err.Error() // deutscher Text der Prüfung
		} else if err := st.SaveThreshold(r.Context(), t); err != nil {
			if errors.Is(err, energie.ErrNotFound) {
				writeGlobalBoardError(w, http.StatusNotFound, "Der Schwellwert wurde nicht gefunden.")
				return
			}
			componentLog("energiedaten").Error().Err(err).Msg("schwellwert speichern")
			msg = "Der Schwellwert konnte nicht gespeichert werden."
		}
	}
	if msg != "" {
		h.renderThresholds(w, r, st, id, &form, "", msg)
		return
	}
	h.renderThresholds(w, r, st, id, nil, "Schwellwert gespeichert.", "")
}

// EnergyThresholdDeleteWeb: POST /energy/thresholds/{tid}/delete-web – nur
// Bereichsadministrator der Messstelle. Antwort: das Fragment der Messstelle.
func (h *Handler) EnergyThresholdDeleteWeb(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	if !meterUUIDRe.MatchString(tid) {
		writeGlobalBoardError(w, http.StatusNotFound, "Der Schwellwert wurde nicht gefunden.")
		return
	}
	if !h.energyCan(r, permEnergyAdmin, "") {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	t, err := st.Threshold(r.Context(), tid)
	if err != nil {
		if errors.Is(err, energie.ErrNotFound) {
			writeGlobalBoardError(w, http.StatusNotFound, "Der Schwellwert wurde nicht gefunden.")
			return
		}
		componentLog("energiedaten").Error().Err(err).Msg("schwellwert laden")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Das hat nicht geklappt.")
		return
	}
	if !h.energyCan(r, permEnergyAdmin, t.MeterID) {
		energyDenied(w, r)
		return
	}
	if err := st.DeleteThreshold(r.Context(), tid); err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("schwellwert löschen")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Der Schwellwert konnte nicht gelöscht werden.")
		return
	}
	h.renderThresholds(w, r, st, t.MeterID, nil, "Schwellwert gelöscht.", "")
}

// thresholdCountText: Beschriftung der Spalte „Schwellwerte“ („keine“, „2“, „2 · 1 Alarm“).
func thresholdCountText(c energie.ThresholdCount) (string, int) {
	switch {
	case c.Total == 0:
		return "keine", 0
	case c.Alarms == 1:
		return strconv.Itoa(c.Total) + " · 1 Alarm", 1
	case c.Alarms > 1:
		return strconv.Itoa(c.Total) + " · " + strconv.Itoa(c.Alarms) + " Alarme", c.Alarms
	}
	return strconv.Itoa(c.Total), 0
}
