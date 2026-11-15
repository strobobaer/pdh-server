package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/energie"
)

// Energiedaten – die Seite „Messstellen“: Zuweisungstabelle und globale
// Übersicht in einem. Jede Messstelle hat eine Anlage, eine Quelle (ein
// importierter Wert) oder eine Formel, Einheit, Art und Faktor. Bearbeitet
// wird direkt in der Tabelle (energy_meter_table.gohtml); gespeichert und
// geprüft wird in energie.Store.

var meterUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// energyCan: Recht für Messstellen (id "" = irgendwo vorhanden). Für eine
// bestimmte Messstelle zählt die Abteilung ihrer Anlage (hasPermRec).
func (h *Handler) energyCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if id == "" {
		return h.hasPerm(r, perm)
	}
	return h.hasPermRec(r, perm, meterRef, id)
}

// energyCanInfra: Recht für eine Messstelle an dieser Anlage (Abteilung der Anlage).
func (h *Handler) energyCanInfra(r *http.Request, perm, infraID string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	return h.hasPermRec(r, perm, "infrastructure", infraID)
}

// energyDenied lehnt ab: Seiten leiten zur Startseite, Aktionen antworten 403.
func energyDenied(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	writeGlobalBoardError(w, http.StatusForbidden, "Dafür fehlt die Berechtigung.")
}

// energyBase: Seitenkopf; nur in Tests ohne Rechtedienst leer.
func (h *Handler) energyBase(r *http.Request) BaseData {
	if h.rbac == nil {
		var none BaseData
		return none
	}
	return h.baseData(r, "energy", "Messstellen", "Energiedaten")
}

// MeterRow ist eine Zeile der Tabelle.
type MeterRow struct {
	energie.Meter
	Calc                 bool // berechnet (Formel statt Quelle)
	KindText, FactorText string
	LastText, LastAtText string
	Stale                bool
	CanEdit, CanDelete   bool
	EditJSON             string // Vorbelegung des Bearbeiten-Formulars
}

// MeterFilterView: Filter so, wie das Formular sie wieder anzeigt.
type MeterFilterView struct {
	Infra, InfraLabel, Virtual, Export, Active string
	Unit, Kind, Conn, Q                        string
	Stale                                      bool
}

// MeterSourceOption: ein importierter Wert, der als Quelle gewählt werden kann.
type MeterSourceOption struct{ ID, Label string }

// MeterConnOption: eine Verbindung für den Filter.
type MeterConnOption struct{ ID, Name string }

// EnergyMetersData ist das Datenmodell der Seite.
type EnergyMetersData struct {
	BaseData
	Rows              []MeterRow
	Total             int
	CanCreate         bool
	Filter            MeterFilterView
	FilterOn          bool
	Units             []string
	Conns             []MeterConnOption
	Sources           []MeterSourceOption
	Sort              string
	Desc              bool
	SortURL, SortMark map[string]string
	Err               string
}

// meterSortURL: Adresse der Seite mit Sortierung nach col (Klick auf die
// Spaltenüberschrift wechselt auf/ab); alle Filter bleiben erhalten.
func meterSortURL(q url.Values, col string) string {
	nq := url.Values{}
	for k, v := range q {
		nq[k] = v
	}
	f := meterFilterFromQuery(q)
	nq.Set("sort", col)
	if f.Sort == col && !f.Desc {
		nq.Set("dir", "desc")
	} else {
		nq.Del("dir")
	}
	return "/energy/meters?" + nq.Encode()
}

// meterNumber: Zahl mit Dezimalkomma, höchstens vier Nachkommastellen, ohne Nullen am Ende.
func meterNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return strings.Replace(s, ".", ",", 1)
}

// newMeterRow bereitet eine Messstelle für die Tabelle auf (ohne Rechte).
func newMeterRow(m energie.Meter, now time.Time) MeterRow {
	row := MeterRow{Meter: m, Calc: m.Virtual(), KindText: meterKindLabel(m.Kind), FactorText: meterNumber(m.Factor)}
	if m.LastValue != nil {
		row.LastText = meterNumber(*m.LastValue)
		if m.Unit != "" {
			row.LastText += " " + m.Unit
		}
	}
	if m.LastAt != nil {
		row.LastAtText = m.LastAt.Local().Format("02.01.2006 15:04")
	}
	row.Stale = m.LastAt == nil || now.Sub(*m.LastAt) > energie.StaleAfter
	edit, _ := json.Marshal(map[string]any{
		"id": m.ID, "key": m.Key, "name": m.Name, "infrastructure_id": m.InfraID, "infra_label": m.InfraPath,
		"source_id": m.SourceID, "formula": m.Formula, "unit": m.Unit, "kind": m.Kind, "factor": m.Factor,
		"export": m.Export, "active": m.Active,
	})
	row.EditJSON = string(edit)
	return row
}

// EnergyMetersPage: GET /energy/meters.
func (h *Handler) EnergyMetersPage(w http.ResponseWriter, r *http.Request) {
	if !h.energyCan(r, permEnergyView, "") {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		http.Error(w, "keine Datenbank", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	f := meterFilterFromQuery(q)
	f.AllowedIDs = h.scopeAllowedIDs(r, meterRef)

	d := EnergyMetersData{Sort: f.Sort, Desc: f.Desc, SortURL: map[string]string{}, SortMark: map[string]string{}}
	for _, col := range []string{"key", "name", "infra", "unit", "kind", "last"} {
		d.SortURL[col] = meterSortURL(q, col)
	}
	if f.Desc {
		d.SortMark[f.Sort] = "▼"
	} else {
		d.SortMark[f.Sort] = "▲"
	}
	d.Filter = MeterFilterView{Infra: f.InfraID, Unit: f.Unit, Kind: f.Kind, Conn: f.ConnectionID, Q: f.Query, Stale: f.Stale}
	for k, p := range map[string]*string{"virtual": &d.Filter.Virtual, "export": &d.Filter.Export, "active": &d.Filter.Active} {
		if v := q.Get(k); v == "0" || v == "1" {
			*p = v
		}
	}
	d.FilterOn = f.InfraID != "" || f.ConnectionID != "" || f.Unit != "" || f.Kind != "" || f.Query != "" || f.Stale ||
		f.Virtual != nil || f.Export != nil || f.Active != nil
	if f.InfraID != "" && h.db != nil {
		_ = h.db.QueryRow(ctx, `SELECT name FROM infrastructure WHERE id = $1::uuid`, f.InfraID).Scan(&d.Filter.InfraLabel)
	}

	list, err := st.List(ctx, f)
	if err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("messstellen laden")
		d.Err = "Die Messstellen konnten nicht geladen werden."
	}
	if d.Units, err = st.Units(ctx); err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("einheiten laden")
	}
	if sources, err := h.availableExportSources(ctx); err != nil {
		componentLog("energiedaten").Error().Err(err).Msg("quellen laden")
	} else {
		seen := map[string]bool{}
		for _, s := range sources {
			label := s.Name
			if s.ConnectionName != "" {
				label = s.ConnectionName + " · " + s.Name
			}
			if s.InfrastructureName != "" {
				label += " (" + s.InfrastructureName + ")"
			}
			d.Sources = append(d.Sources, MeterSourceOption{ID: s.ID, Label: label})
			if s.ConnectionID != "" && !seen[s.ConnectionID] {
				seen[s.ConnectionID] = true
				d.Conns = append(d.Conns, MeterConnOption{ID: s.ConnectionID, Name: s.ConnectionName})
			}
		}
	}

	// Rechte je Anlage einmal prüfen: die Abteilung hängt an der Anlage.
	canEdit, canDel := map[string]bool{}, map[string]bool{}
	now := time.Now()
	for _, m := range list {
		row := newMeterRow(m, now)
		if _, ok := canEdit[m.InfraID]; !ok {
			canEdit[m.InfraID] = h.energyCanInfra(r, permEnergyEdit, m.InfraID)
			canDel[m.InfraID] = h.energyCanInfra(r, permEnergyAdmin, m.InfraID)
		}
		row.CanEdit, row.CanDelete = canEdit[m.InfraID], canDel[m.InfraID]
		d.Rows = append(d.Rows, row)
	}
	d.Total = len(d.Rows)
	d.CanCreate = h.energyCan(r, permEnergyEdit, "")
	d.BaseData = h.energyBase(r)
	h.render(w, "energy_meters", d)
}

// meterSaveInput: Eingabe von POST /energy/meters/save-web.
type meterSaveInput struct {
	ID       string  `json:"id"`
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	InfraID  string  `json:"infrastructure_id"`
	SourceID string  `json:"source_id"`
	Formula  string  `json:"formula"`
	Unit     string  `json:"unit"`
	Kind     string  `json:"kind"`
	Factor   float64 `json:"factor"`
	Export   bool    `json:"export"`
	Active   bool    `json:"active"`
}

func meterJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// EnergyMeterSaveWeb: POST /energy/meters/save-web (JSON). Ohne id legt die
// Zeile eine Messstelle an (Recht an der gewählten Anlage), mit id ändert sie
// diese (Recht am Datensatz und an der gewählten Anlage).
func (h *Handler) EnergyMeterSaveWeb(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var in meterSaveInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe.")
		return
	}
	in.ID, in.Key, in.Name = strings.TrimSpace(in.ID), strings.TrimSpace(in.Key), strings.TrimSpace(in.Name)
	in.InfraID, in.SourceID = strings.TrimSpace(in.InfraID), strings.TrimSpace(in.SourceID)
	in.Formula, in.Unit = strings.TrimSpace(in.Formula), strings.TrimSpace(in.Unit)
	if !meterUUIDRe.MatchString(in.InfraID) {
		writeGlobalBoardError(w, http.StatusBadRequest, meterSaveMessage(energie.ErrNoInfra))
		return
	}
	if in.SourceID != "" && !meterUUIDRe.MatchString(in.SourceID) {
		writeGlobalBoardError(w, http.StatusBadRequest, "Die gewählte Quelle gibt es nicht.")
		return
	}
	if in.ID != "" && !meterUUIDRe.MatchString(in.ID) {
		writeGlobalBoardError(w, http.StatusNotFound, meterSaveMessage(energie.ErrNotFound))
		return
	}
	if !h.energyCanInfra(r, permEnergyEdit, in.InfraID) || (in.ID != "" && !h.energyCan(r, permEnergyEdit, in.ID)) {
		energyDenied(w, r)
		return
	}
	if utf8.RuneCountInString(in.Name) > 150 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Die Bezeichnung darf höchstens 150 Zeichen lang sein.")
		return
	}
	if utf8.RuneCountInString(in.Unit) > 20 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Die Einheit darf höchstens 20 Zeichen lang sein.")
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	m := &energie.Meter{ID: in.ID, Key: in.Key, Name: in.Name, InfraID: in.InfraID, SourceID: in.SourceID,
		Formula: in.Formula, Unit: in.Unit, Kind: in.Kind, Factor: in.Factor, Export: in.Export, Active: in.Active}
	created, err := st.Save(r.Context(), m, getUser(r).ID)
	if err != nil {
		writeGlobalBoardError(w, meterSaveStatus(err), meterSaveMessage(err))
		return
	}
	meterJSON(w, map[string]any{"status": "ok", "id": m.ID, "created": created})
}

// EnergyMeterDeleteWeb: POST /energy/meters/{id}/delete-web – nur Bereichsadministrator.
func (h *Handler) EnergyMeterDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !meterUUIDRe.MatchString(id) {
		writeGlobalBoardError(w, http.StatusNotFound, meterSaveMessage(energie.ErrNotFound))
		return
	}
	if !h.energyCan(r, permEnergyAdmin, id) {
		energyDenied(w, r)
		return
	}
	st := h.energyStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Keine Datenbank.")
		return
	}
	if err := st.Delete(r.Context(), id); err != nil {
		writeGlobalBoardError(w, meterSaveStatus(err), meterSaveMessage(err))
		return
	}
	meterJSON(w, map[string]string{"status": "ok"})
}
