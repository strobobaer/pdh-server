package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/formula"
	"pdh/internal/modules/calc"
)

// Berechnete Werte (virtuelle Messwerte): Seite der eingebauten Verbindung
// „Berechnete Werte“ mit Liste, Formular (Formelgenerator, Prüfung, Probe)
// und Fassungen. Speicher und Rechenwerk: internal/modules/calc.
//
// Rechte: ansehen mit import.read, anlegen/ändern/löschen mit import.write an
// der Anlage des berechneten Werts (Abteilung zählt, beim Wechsel der Anlage
// an alter und neuer). Nicht in der Wartung.

type calcListRow struct {
	ID, Name, InfraPath, Value, Unit, Age, Formula, Since, Reason string
	Planned                                                       int  // weitere geplante Fassungen
	NoAgeCheck                                                    bool // Höchstalter leer
}

type calcFormInput struct {
	Short, MappingID, Label, Value, Age string
	Gone                                bool
}

type calcVersionRow struct {
	ID, Status, ValidFrom, Formula, By, At string
	Editable                               bool // geplant: ändern und löschen
}

type calcForm struct {
	ID, VersionID, Name, InfraID, InfraPath, Unit string
	Decimals                                      int
	MaxAge                                        string // Minuten, leer = Alter wird nicht geprüft
	Formula, ValidFrom, Today                     string // Datum als JJJJ-MM-TT
	Inputs                                        []calcFormInput
	Versions                                      []calcVersionRow
	Usage                                         calc.Usage
	InUse                                         string // Satz, warum sich der Wert nicht löschen lässt
}

// CalcPageData: Seite „Berechnete Werte“.
type CalcPageData struct {
	BaseData
	ConnectionID string
	CanWrite     bool
	Values       []calcListRow
	Form         *calcForm
	Functions    []formula.FuncInfo
	Notice, Err  string
}

// calcDisplayNumber zeigt einen gespeicherten Wert mit Dezimalkomma.
func calcDisplayNumber(s string) string { return strings.Replace(s, ".", ",", 1) }

// calcAgeText: wie alt ein Wert ist („vor 3 s“), nil = „noch nie“.
func calcAgeText(at *time.Time, now time.Time) string {
	if at == nil {
		return "noch nie"
	}
	d := now.Sub(*at)
	switch {
	case d < 0:
		d = 0
		fallthrough
	case d < time.Minute:
		return fmt.Sprintf("vor %d s", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("vor %d min", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("vor %d h", int(d/time.Hour))
	}
	return fmt.Sprintf("vor %d Tagen", int(d/(24*time.Hour)))
}

// calcListRows baut die Zeilen der Liste: geltende Formel (ohne geltende die
// erste geplante), Wert oder Grund, Zahl der weiteren geplanten Fassungen.
func calcListRows(values []calc.Value, now time.Time) []calcListRow {
	rows := make([]calcListRow, 0, len(values))
	for i := range values {
		v := &values[i]
		row := calcListRow{ID: v.ID, Name: v.Name, InfraPath: v.InfraPath, Unit: v.Unit, Reason: v.Reason, NoAgeCheck: v.MaxAgeMinutes == nil}
		for _, ver := range v.Versions {
			if ver.Status == calc.StatusPlanned {
				row.Planned++
			}
		}
		if cur := v.Current(); cur != nil {
			row.Formula, row.Since = cur.Formula, "gilt seit "+cur.ValidFrom.Format("02.01.2006")
		} else if n := len(v.Versions); n > 0 {
			first := v.Versions[n-1]
			row.Formula, row.Since = first.Formula, "gilt ab "+first.ValidFrom.Format("02.01.2006")
			row.Planned--
		}
		if v.LastValue != "" && v.Reason == "" {
			row.Value, row.Age = calcDisplayNumber(v.LastValue), calcAgeText(v.LastAt, now)
		}
		rows = append(rows, row)
	}
	return rows
}

// calcFormFor füllt das Formular: mit versionID die geplante Fassung (zum
// Ändern), sonst die geltende (ohne geltende die erste geplante).
func calcFormFor(v *calc.Value, versionID string, today, now time.Time) *calcForm {
	f := &calcForm{ID: v.ID, Name: v.Name, InfraID: v.InfraID, InfraPath: v.InfraPath, Unit: v.Unit, Decimals: v.Decimals,
		ValidFrom: today.Format("2006-01-02"), Today: today.Format("2006-01-02")}
	if v.MaxAgeMinutes != nil {
		f.MaxAge = strconv.Itoa(*v.MaxAgeMinutes)
	}
	var shown *calc.Version
	for i := range v.Versions {
		ver := &v.Versions[i]
		row := calcVersionRow{ID: ver.ID, Status: ver.Status, ValidFrom: ver.ValidFrom.Format("02.01.2006"), Formula: ver.Formula,
			By: ver.CreatedBy, At: ver.CreatedAt.Local().Format("02.01.2006 15:04"), Editable: ver.Status == calc.StatusPlanned}
		f.Versions = append(f.Versions, row)
		if versionID != "" && strings.EqualFold(ver.ID, versionID) && ver.Status == calc.StatusPlanned {
			shown = ver
			f.VersionID, f.ValidFrom = ver.ID, ver.ValidFrom.Format("2006-01-02")
		}
	}
	if shown == nil {
		shown = v.Current()
	}
	if shown == nil && len(v.Versions) > 0 { // nur geplant: die erste zum Ändern
		shown = &v.Versions[len(v.Versions)-1]
		f.VersionID, f.ValidFrom = shown.ID, shown.ValidFrom.Format("2006-01-02")
	}
	if shown != nil {
		f.Formula = shown.Formula
		for _, in := range shown.Inputs {
			row := calcFormInput{Short: in.Short, MappingID: in.MappingID, Label: in.Label, Gone: in.Gone}
			if !in.Gone && !in.NoValue {
				row.Value = in.Value
				if _, ok := calc.ParseInput(in.Value); ok {
					row.Value = calcDisplayNumber(in.Value)
				}
			}
			if !in.Gone {
				row.Age = calcAgeText(in.ReceivedAt, now)
			}
			f.Inputs = append(f.Inputs, row)
		}
	}
	return f
}

// calcBackPath: zurück auf die Seite, mit Meldung.
func calcBackPath(connID, editID, notice, errMsg string) string {
	q := url.Values{}
	if editID != "" {
		q.Set("edit", editID)
	}
	if notice != "" {
		q.Set("notice", notice)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	path := "/import/connections/" + connID
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return path
}

// calcPage zeigt die Seite der eingebauten Verbindung (aufgerufen aus
// connectionDetailPage für die Art „calc“).
func (h *Handler) calcPage(w http.ResponseWriter, r *http.Request, connID string) {
	st := h.calcStore()
	if st == nil {
		http.Error(w, "Datenbank nicht verfügbar", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	values, err := st.List(ctx)
	if err != nil {
		componentLog("berechnete-werte").Error().Err(err).Msg("liste lesen")
		http.Error(w, "Die berechneten Werte konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	q := r.URL.Query()
	d := CalcPageData{
		BaseData:     h.baseData(r, "import", "Berechnete Werte", "Verbindung"),
		ConnectionID: connID, CanWrite: h.canConnectionWrite(r, "import"),
		Values: calcListRows(values, now), Functions: formula.Functions(),
		Notice: q.Get("notice"), Err: q.Get("err"),
	}
	// Copilot-Seitenleiste kennt die Seite (eigene Werkzeuge, calc_copilot.go); keine Internetsuche
	d.CopilotConn, d.CopilotNoWeb = copilotConnRef(h.faults.Copilot(), "import", connID), true
	today := localDate(now)
	switch edit := q.Get("edit"); {
	case edit != "":
		for i := range values {
			if strings.EqualFold(values[i].ID, edit) {
				d.Form = calcFormFor(&values[i], q.Get("version"), today, now)
				if u, err := st.Usage(ctx, values[i].ID); err == nil {
					d.Form.Usage = u
					if len(u.Values) > 0 {
						d.Form.InUse = "Löschen nicht möglich: Der Wert " + (&calc.InUseError{Users: u.Values}).Error() + ". Bitte zuerst dort die Formel ändern."
					}
				}
			}
		}
		if d.Form == nil && d.Err == "" {
			d.Err = "Den berechneten Wert gibt es nicht mehr"
		}
	case q.Get("new") == "1" && d.CanWrite:
		d.Form = &calcForm{Decimals: 2, MaxAge: "10", ValidFrom: today.Format("2006-01-02"), Today: today.Format("2006-01-02")}
	}
	h.render(w, "calc_values", d)
}

// CalcRedirectWeb: GET /import/calc → Seite der eingebauten Verbindung.
func (h *Handler) CalcRedirectWeb(w http.ResponseWriter, r *http.Request) {
	st := h.calcStore()
	if !h.canConnectionRead(r, "import") || st == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id, err := st.ConnectionID(r.Context())
	if err != nil {
		http.Error(w, "Die Verbindung „Berechnete Werte“ fehlt", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/import/connections/"+id, http.StatusFound)
}

type calcCandidateView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Connection string `json:"connection"`
	Value      string `json:"value"`
	Age        string `json:"age"`
	Calc       bool   `json:"calc"`
}

// CalcInputsWeb: GET /import/calc/inputs?q=&exclude= → wählbare Eingangswerte.
func (h *Handler) CalcInputsWeb(w http.ResponseWriter, r *http.Request) {
	st := h.calcStore()
	if !h.canConnectionRead(r, "import") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Datenbank nicht verfügbar")
		return
	}
	list, err := st.Candidates(r.Context(), r.URL.Query().Get("q"), 50, r.URL.Query().Get("exclude"))
	if err != nil {
		componentLog("berechnete-werte").Error().Err(err).Msg("eingangswerte suchen")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Die Werte konnten nicht gelesen werden")
		return
	}
	now := time.Now()
	out := make([]calcCandidateView, 0, len(list))
	for _, c := range list {
		v := calcCandidateView{ID: c.ID, Name: c.Name, Path: c.InfraPath, Connection: c.Connection, Value: c.Value, Age: calcAgeText(c.ReceivedAt, now), Calc: c.Calc}
		if _, ok := calc.ParseInput(c.Value); ok {
			v.Value = calcDisplayNumber(c.Value)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

type calcCheckBody struct {
	ID            string          `json:"id"`
	Formula       string          `json:"formula"`
	Inputs        []calc.InputRef `json:"inputs"`
	Decimals      int             `json:"decimals"`
	MaxAgeMinutes *int            `json:"max_age_minutes"`
}

type calcCheckResult struct {
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
	Pos         int      `json:"pos,omitempty"`
	Hints       []string `json:"hints"`
	Probe       string   `json:"probe,omitempty"`
	ProbeReason string   `json:"probe_reason,omitempty"`
}

// CalcCheckWeb: POST /import/calc/check – Prüfung beim Tippen und Probe mit
// den Werten von jetzt. Speichert nichts.
func (h *Handler) CalcCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canConnectionRead(r, "import") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	st := h.calcStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Datenbank nicht verfügbar")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in calcCheckBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	ctx := r.Context()
	res := calcCheckResult{Hints: []string{}}
	if err := st.CheckFormula(ctx, strings.TrimSpace(in.ID), in.Formula, in.Inputs); err != nil {
		if !calc.UserError(err) {
			componentLog("berechnete-werte").Error().Err(err).Msg("formel prüfen")
			writeGlobalBoardError(w, http.StatusInternalServerError, "Die Formel konnte nicht geprüft werden")
			return
		}
		res.Error = upperFirst(err.Error())
		var se *formula.SyntaxError
		if errors.As(err, &se) {
			res.Pos = se.Pos
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.OK = true
	if f, err := formula.Parse(strings.TrimSpace(in.Formula)); err == nil {
		used := map[string]bool{}
		for _, ref := range f.Refs() {
			used[strings.ToLower(ref)] = true
		}
		for _, x := range in.Inputs {
			if !used[strings.ToLower(x.Short)] {
				res.Hints = append(res.Hints, x.Short+" wird in der Formel nicht benutzt")
			}
		}
	}
	if in.Decimals < 0 || in.Decimals > 6 {
		in.Decimals = 2
	}
	value, reason, err := st.Probe(ctx, in.Formula, in.Inputs, in.Decimals, in.MaxAgeMinutes)
	if err == nil {
		res.Probe, res.ProbeReason = calcDisplayNumber(value), reason
	}
	writeJSON(w, http.StatusOK, res)
}

type calcSaveBody struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	InfrastructureID string          `json:"infrastructure_id"`
	Unit             string          `json:"unit"`
	Decimals         int             `json:"decimals"`
	MaxAgeMinutes    *int            `json:"max_age_minutes"` // null = Alter wird nicht geprüft
	Formula          string          `json:"formula"`
	Inputs           []calc.InputRef `json:"inputs"`
	ValidFrom        string          `json:"valid_from"` // JJJJ-MM-TT, leer = heute
	VersionID        string          `json:"version_id"`
}

// calcWriteGuard: Recht und Wartung für die schreibenden Routen. false → beantwortet.
func (h *Handler) calcWriteGuard(w http.ResponseWriter, r *http.Request) bool {
	if !h.canConnectionWrite(r, "import") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return false
	}
	if h.MaintLocked() {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, errMaintApply)
		return false
	}
	return true
}

// calcErrStatus: HTTP-Status und Text zu einem Fehler des Speichers.
func calcErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, calc.ErrNotFound):
		return http.StatusNotFound, upperFirst(err.Error())
	case calc.UserError(err):
		return http.StatusBadRequest, upperFirst(err.Error())
	}
	componentLog("berechnete-werte").Error().Err(err).Msg("speichern")
	return http.StatusInternalServerError, "Der berechnete Wert konnte nicht gespeichert werden"
}

// CalcSaveWeb: POST /import/calc/values – anlegen oder ändern (JSON).
func (h *Handler) CalcSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.calcWriteGuard(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in calcSaveBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	save := calc.SaveInput{ID: strings.TrimSpace(in.ID), Name: in.Name, InfraID: strings.TrimSpace(in.InfrastructureID), Unit: in.Unit,
		Decimals: in.Decimals, MaxAgeMinutes: in.MaxAgeMinutes, Formula: in.Formula, Inputs: in.Inputs, VersionID: strings.TrimSpace(in.VersionID)}
	if d := strings.TrimSpace(in.ValidFrom); d != "" {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			writeGlobalBoardError(w, http.StatusBadRequest, "„Gültig ab“ ist kein Datum")
			return
		}
		save.ValidFrom = t
	}
	st := h.calcStore()
	if st == nil {
		writeGlobalBoardError(w, http.StatusServiceUnavailable, "Datenbank nicht verfügbar")
		return
	}
	ctx := r.Context()
	allow := h.assetGuardFor(r, "import.write")
	if save.ID != "" { // vorhandener Wert: die bisherige Anlage zählt
		v, err := st.Get(ctx, save.ID)
		if err != nil {
			status, msg := calcErrStatus(err)
			writeGlobalBoardError(w, status, msg)
			return
		}
		if !allow(v.InfraID) {
			writeGlobalBoardError(w, http.StatusForbidden, errAssetDenied)
			return
		}
	}
	if uuidInPathRe.MatchString(save.InfraID) && !allow(save.InfraID) { // die neue Anlage auch
		writeGlobalBoardError(w, http.StatusForbidden, errAssetDenied)
		return
	}
	res, err := st.Save(ctx, save, getUser(r).ID)
	if err != nil {
		status, msg := calcErrStatus(err)
		writeGlobalBoardError(w, status, msg)
		return
	}
	h.calcNudge()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": res.ID, "version": res.Version})
}

// calcGuardedValue: Recht, Wartung und Anlage für Löschrouten. nil → beantwortet.
func (h *Handler) calcGuardedValue(w http.ResponseWriter, r *http.Request, st *calc.Store) *calc.Value {
	if !h.canConnectionWrite(r, "import") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return nil
	}
	if h.MaintLocked() {
		http.Error(w, errMaintApply, http.StatusServiceUnavailable)
		return nil
	}
	if st == nil {
		http.Error(w, "Datenbank nicht verfügbar", http.StatusServiceUnavailable)
		return nil
	}
	v, err := st.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		status, msg := calcErrStatus(err)
		http.Error(w, msg, status)
		return nil
	}
	if !h.assetGuardFor(r, "import.write")(v.InfraID) {
		http.Error(w, errAssetDenied, http.StatusForbidden)
		return nil
	}
	return v
}

// CalcDeleteWeb: POST /import/calc/values/{id}/delete.
func (h *Handler) CalcDeleteWeb(w http.ResponseWriter, r *http.Request) {
	st := h.calcStore()
	v := h.calcGuardedValue(w, r, st)
	if v == nil {
		return
	}
	conn, _ := st.ConnectionID(r.Context())
	if err := st.Delete(r.Context(), v.ID); err != nil {
		_, msg := calcErrStatus(err)
		var inUse *calc.InUseError
		if errors.As(err, &inUse) {
			msg = "Löschen nicht möglich: Der Wert " + inUse.Error() + ". Bitte zuerst dort die Formel ändern."
		}
		http.Redirect(w, r, calcBackPath(conn, v.ID, "", msg), http.StatusSeeOther)
		return
	}
	h.calcNudge()
	http.Redirect(w, r, calcBackPath(conn, "", "Berechneter Wert gelöscht", ""), http.StatusSeeOther)
}

// CalcVersionDeleteWeb: POST /import/calc/values/{id}/versions/{vid}/delete –
// nur geplante Fassungen.
func (h *Handler) CalcVersionDeleteWeb(w http.ResponseWriter, r *http.Request) {
	st := h.calcStore()
	v := h.calcGuardedValue(w, r, st)
	if v == nil {
		return
	}
	conn, _ := st.ConnectionID(r.Context())
	if err := st.DeleteVersion(r.Context(), v.ID, chi.URLParam(r, "vid")); err != nil {
		_, msg := calcErrStatus(err)
		http.Redirect(w, r, calcBackPath(conn, v.ID, "", msg), http.StatusSeeOther)
		return
	}
	h.calcNudge()
	http.Redirect(w, r, calcBackPath(conn, v.ID, "Geplante Fassung gelöscht", ""), http.StatusSeeOther)
}
