package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"pdh/internal/modules/energie"
)

// Energiedaten – Reiter „Einstellungen“ auf /energy/meters: Werte für alle
// Messstellen (Frische einer Quelle, Aufbewahrung der Minutenwerte) und
// Vorgaben für neue Efficio-Exporte. Gespeichert in app_settings (energy.*,
// energie.Store.SaveSettings). Ansehen mit energy.view, ändern mit energy.admin.

// settingsFromForm liest das Formular; ungültige Zahlen führen zu einer Meldung.
func settingsFromForm(r *http.Request) (energie.Settings, string) {
	var bad []string
	num := func(key, label string) int {
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(key)))
		if err != nil {
			bad = append(bad, "„"+label+"“ muss eine ganze Zahl sein.")
		}
		return n
	}
	s := energie.Settings{
		StaleMinutes:       num("stale_minutes", "Quellwert gilt als veraltet nach"),
		ValueRetentionDays: num("value_retention_days", "Minutenwerte aufbewahren"),
		RunMinutes:         num("run_minutes", "Datei ergänzen alle"),
		IntervalMinutes:    num("interval_minutes", "Zeilen im Abstand von"),
		FileDays:           num("file_days", "Max. Länge der Datei in Tagen"),
		RetentionDays:      num("retention_days", "Aufbewahrung in Tagen"),
		TimeFormat:         strings.TrimSpace(r.FormValue("time_format")),
		TimeBase:           strings.TrimSpace(r.FormValue("time_base")),
		Encoding:           strings.TrimSpace(r.FormValue("encoding")),
		Prefix:             strings.TrimSpace(r.FormValue("prefix")),
		DecimalComma:       r.FormValue("decimal_comma") != "",
		Header:             r.FormValue("header") != "",
	}
	if s.TimeBase == "" {
		s.TimeBase = energie.TimeBaseLocal
	}
	if len(bad) > 0 {
		return s, bad[0]
	}
	return s, ""
}

// EnergySettingsWeb: POST /energy/settings-web – nur Bereichsadministrator (energy.admin).
func (h *Handler) EnergySettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.energyCan(r, permEnergyAdmin, "") {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		http.Error(w, "keine Datenbank", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	back := "/energy/meters?tab=settings&"
	s, msg := settingsFromForm(r)
	if msg == "" {
		if err := s.Validate(); err != nil {
			msg = err.Error()
		}
	}
	if msg != "" {
		http.Redirect(w, r, back+"err="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	if err := st.SaveSettings(r.Context(), s); err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("einstellungen speichern")
		http.Redirect(w, r, back+"err="+url.QueryEscape("Die Einstellungen konnten nicht gespeichert werden."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"notice="+url.QueryEscape("Einstellungen gespeichert."), http.StatusSeeOther)
}
