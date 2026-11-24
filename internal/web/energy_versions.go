package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/energie"
)

// Fassungen der Formel einer berechneten Messstelle (Paket 2b): ausklappbare
// Zeile unter der Messstelle in der Tabelle (widgets/energy_meter_versions.gohtml).
// Geplante Fassungen pflegt, wer die Messstelle bearbeiten darf (energy.edit);
// eine Fassung mit einem Datum bis heute rechnet die gespeicherten Werte neu
// und ist dem Bereichsadministrator vorbehalten (energy.admin).

// VersionRow: eine Fassung, aufbereitet für die Liste.
type VersionRow struct {
	energie.Version
	StatusText, StatusClass string
	ValidText, CreatedText  string
	ValidInput              string // JJJJ-MM-TT für das Formular
}

// RecalcRow: ein Lauf der Neurechnung, aufbereitet für die Liste.
type RecalcRow struct {
	energie.RecalcRun
	RangeText, StateText, StateClass, WhenText string
}

// EnergyVersionsData: Datenmodell des Fragments „Fassungen“.
type EnergyVersionsData struct {
	Meter                     energie.Meter
	Versions                  []VersionRow
	Runs                      []RecalcRow
	Running                   *RecalcRow // ein Lauf mit dieser Messstelle
	OtherRunning              bool       // ein Lauf mit anderen Messstellen
	CanEdit, CanAdmin         bool
	Today, Tomorrow, Earliest string // JJJJ-MM-TT
	EarliestText              string
	Notice, Err               string
}

// VersionPreviewView: Vorschau einer Neurechnung in Anwendersprache.
type VersionPreviewView struct {
	FromText, ToText, NextText, LastText string
	Days                                 int
	Meters                               []string
	Rebuild, Keep                        []energie.FileImpact
}

func dmy(t time.Time) string { return t.Format("02.01.2006") }

func versionStatus(s string) (text, class string) {
	switch s {
	case energie.StatusPlanned:
		return "geplant", "b-blue"
	case energie.StatusCurrent:
		return "gilt", "b-green"
	}
	return "abgelöst", "b-gray"
}

func recalcRow(r energie.RecalcRun) RecalcRow {
	row := RecalcRow{RecalcRun: r, RangeText: "ab " + dmy(r.From), WhenText: r.CreatedAt.Local().Format("02.01.2006 15:04")}
	switch r.State {
	case energie.RunRunning, energie.RunExporting:
		row.StateText, row.StateClass = "läuft ("+strconv.Itoa(r.Progress)+" %)", "b-amber"
	case energie.RunDone:
		row.StateText, row.StateClass = "fertig", "b-green"
	default:
		row.StateText, row.StateClass = "abgebrochen", "b-red"
	}
	return row
}

// energyRecalcNudge stößt die Neurechnung an (nach dem Anlegen eines Laufs).
func (h *Handler) energyRecalcNudge() {
	if rc := h.energyRecalc.Load(); rc != nil {
		rc.Nudge()
	}
}

// recalcFor: der laufende Recalculator oder – am Spiegel und in Tests – einer
// nur für die Vorschau (sie liest nur).
func (h *Handler) recalcFor(st *energie.Store) *energie.Recalculator {
	if rc := h.energyRecalc.Load(); rc != nil {
		return rc
	}
	return energie.NewRecalculator(st, &energie.Exporter{Store: st, Configs: h.efficioConfigs}, nil)
}

// renderVersions baut das Fragment einer Messstelle und schreibt es.
func (h *Handler) renderVersions(w http.ResponseWriter, r *http.Request, st *energie.Store, meterID, notice, errMsg string) {
	ctx := r.Context()
	m, err := st.Get(ctx, meterID)
	if err != nil {
		status, msg := http.StatusInternalServerError, "Die Fassungen konnten nicht geladen werden."
		if errors.Is(err, energie.ErrNotFound) {
			status, msg = http.StatusNotFound, meterSaveMessage(err)
		}
		writeGlobalBoardError(w, status, msg)
		return
	}
	vs, err := st.Versions(ctx, meterID)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("fassungen laden")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Die Fassungen konnten nicht geladen werden.")
		return
	}
	runs, err := st.RecalcRuns(ctx, meterID, 5)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("neurechnungen laden")
	}
	running, err := st.RunningRecalc(ctx)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("laufende neurechnung laden")
	}
	today := st.Today()
	d := EnergyVersionsData{
		Meter: *m, Notice: notice, Err: errMsg,
		CanEdit:  h.energyCan(r, permEnergyEdit, meterID),
		CanAdmin: h.energyCan(r, permEnergyAdmin, meterID),
		Today:    today.Format("2006-01-02"), Tomorrow: today.AddDate(0, 0, 1).Format("2006-01-02"),
	}
	earliest, ok, err := st.EarliestDay(ctx, meterID)
	if err != nil || !ok {
		earliest = today
	}
	d.Earliest, d.EarliestText = earliest.Format("2006-01-02"), dmy(earliest)
	for _, v := range vs {
		row := VersionRow{Version: v, ValidText: dmy(v.ValidFrom), ValidInput: v.ValidFrom.Format("2006-01-02")}
		row.StatusText, row.StatusClass = versionStatus(v.Status)
		row.CreatedText = v.CreatedAt.Local().Format("02.01.2006 15:04")
		if v.CreatedBy != "" {
			row.CreatedText = v.CreatedBy + " · " + row.CreatedText
		}
		d.Versions = append(d.Versions, row)
	}
	for _, run := range runs {
		d.Runs = append(d.Runs, recalcRow(run))
	}
	if running != nil {
		if slicesContains(running.MeterKeys, m.Key) {
			row := recalcRow(*running)
			d.Running = &row
		} else {
			d.OtherRunning = true
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "energy-meter-versions", d)
	}
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("fassungen darstellen")
		http.Error(w, "Die Fassungen konnten nicht dargestellt werden.", http.StatusInternalServerError)
	}
}

func slicesContains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// EnergyMeterVersionsWeb: GET /energy/meters/{id}/versions – Fragment „Fassungen“.
func (h *Handler) EnergyMeterVersionsWeb(w http.ResponseWriter, r *http.Request) {
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
	h.renderVersions(w, r, st, id, r.URL.Query().Get("notice"), "")
}

// versionSaveInput: Eingabe von Vorschau und Speichern (JSON).
type versionSaveInput struct {
	ValidFrom string `json:"valid_from"`
	Formula   string `json:"formula"`
	VersionID string `json:"version_id"`
	Recalc    bool   `json:"recalc"`
}

// readVersionInput liest Pfad und JSON. false: die Antwort ist schon geschrieben.
func readVersionInput(w http.ResponseWriter, r *http.Request) (string, energie.VersionInput, bool) {
	id, ok := thresholdMeterID(w, r)
	if !ok {
		return "", energie.VersionInput{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in versionSaveInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe.")
		return "", energie.VersionInput{}, false
	}
	day, err := time.Parse("2006-01-02", strings.TrimSpace(in.ValidFrom))
	if err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte ein gültiges Datum wählen.")
		return "", energie.VersionInput{}, false
	}
	vid := strings.TrimSpace(in.VersionID)
	if vid != "" && !meterUUIDRe.MatchString(vid) {
		writeGlobalBoardError(w, http.StatusNotFound, "Die Fassung wurde nicht gefunden.")
		return "", energie.VersionInput{}, false
	}
	return id, energie.VersionInput{MeterID: id, VersionID: vid, Formula: strings.TrimSpace(in.Formula), ValidFrom: day, Recalc: in.Recalc}, true
}

// errRecalcMaint: in der Wartung startet keine Neurechnung.
const errRecalcMaint = "Wartung läuft – bitte nach der Wartung neu rechnen."

// errRecalcAdmin: eine Fassung bis heute ist dem Bereichsadministrator vorbehalten.
const errRecalcAdmin = "Rückwirkend neu rechnen darf nur der Bereichsadministrator der Energiedaten."

// EnergyMeterVersionPreviewWeb: POST /energy/meters/{id}/versions/preview –
// was „Neu rechnen“ ab dem Datum bewirkt (HTML-Fragment). Nur Bereichsadministrator.
func (h *Handler) EnergyMeterVersionPreviewWeb(w http.ResponseWriter, r *http.Request) {
	id, in, ok := readVersionInput(w, r)
	if !ok {
		return
	}
	if !h.energyCan(r, permEnergyAdmin, id) {
		writeGlobalBoardError(w, http.StatusForbidden, errRecalcAdmin)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	if h.MaintLocked() {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, errRecalcMaint)
		return
	}
	p, err := h.recalcFor(st).Preview(r.Context(), in)
	if err != nil {
		writeGlobalBoardError(w, meterSaveStatus(err), meterSaveMessage(err))
		return
	}
	v := VersionPreviewView{FromText: dmy(p.From), ToText: dmy(p.To), Days: p.Days, Meters: p.Meters, Rebuild: p.Rebuild, Keep: p.Keep}
	if p.NextFrom != nil {
		v.NextText, v.LastText = dmy(*p.NextFrom), dmy(p.NextFrom.AddDate(0, 0, -1))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "energy-version-preview", v)
	}
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("vorschau darstellen")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Die Vorschau konnte nicht dargestellt werden.")
	}
}

// EnergyMeterVersionSaveWeb: POST /energy/meters/{id}/versions/save-web (JSON
// {valid_from, formula, version_id, recalc}) → {status, run_id}.
func (h *Handler) EnergyMeterVersionSaveWeb(w http.ResponseWriter, r *http.Request) {
	id, in, ok := readVersionInput(w, r)
	if !ok {
		return
	}
	if !h.energyCan(r, permEnergyEdit, id) {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	if !in.ValidFrom.After(st.Today()) {
		if !h.energyCan(r, permEnergyAdmin, id) {
			writeGlobalBoardError(w, http.StatusForbidden, errRecalcAdmin)
			return
		}
		if in.Recalc && h.MaintLocked() {
			writeGlobalBoardError(w, http.StatusServiceUnavailable, errRecalcMaint)
			return
		}
	}
	res, err := st.SaveVersion(r.Context(), in, getUser(r).ID)
	if err != nil {
		writeGlobalBoardError(w, meterSaveStatus(err), meterSaveMessage(err))
		return
	}
	status := "ok"
	if res.Unchanged {
		status = "unchanged"
	}
	if res.RunID != "" {
		h.energyRecalcNudge()
	}
	meterJSON(w, map[string]string{"status": status, "run_id": res.RunID})
}

// EnergyMeterVersionDeleteWeb: POST /energy/meters/{id}/versions/{vid}/delete-web –
// eine geplante Fassung löschen (wie anlegen: energy.edit an der Messstelle).
func (h *Handler) EnergyMeterVersionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := thresholdMeterID(w, r)
	if !ok {
		return
	}
	vid := chi.URLParam(r, "vid")
	if !meterUUIDRe.MatchString(vid) {
		writeGlobalBoardError(w, http.StatusNotFound, "Die Fassung wurde nicht gefunden.")
		return
	}
	if !h.energyCan(r, permEnergyEdit, id) {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	if err := st.DeleteVersion(r.Context(), id, vid); err != nil {
		if errors.Is(err, energie.ErrNotFound) {
			writeGlobalBoardError(w, http.StatusNotFound, "Die Fassung wurde nicht gefunden.")
			return
		}
		writeGlobalBoardError(w, meterSaveStatus(err), meterSaveMessage(err))
		return
	}
	meterJSON(w, map[string]string{"status": "ok"})
}
