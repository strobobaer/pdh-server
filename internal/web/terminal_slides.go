package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Verwaltungsseite „Infobildschirm“ (globales Terminal): die Folien pflegen, die
// Terminals mit dem Schalter „Globales Terminal“ abspielen (Store: terminal_slides_store.go).
//
//	GET  /terminal-slides                  Liste
//	GET  /terminal-slides/new?kind=…       Formular für eine neue Folie (image, text, vorgaenge, stats_faults, stats_tickets, stats_setup, grafana)
//	GET  /terminal-slides/{id}             Formular zum Ändern
//	POST /terminal-slides                  anlegen (multipart wegen des Bildes)
//	POST /terminal-slides/{id}             ändern
//	POST /terminal-slides/{id}/delete      löschen
//	POST /terminal-slides/{id}/move?d=-1|1 eine Stelle nach oben bzw. unten
//	GET  /terminal-slides/{id}/image       Bild der Folie (Vorschau)
//
// Alles nur mit dem Recht terminal.slides.edit. Schreib-Routen stehen zusätzlich in gateRules.
// Das Formular ist ein normales POST wie die anderen Seiten (Schutz vor Fremdaufrufen: Sitzungs-Cookie SameSite=Lax).
// Jede Änderung meldet den globalen Terminals „slides“ (notifySlideTerminals im Store).

const (
	permTerminalSlides = "terminal.slides.edit"
	slideMaxInfra      = 200
	// Obergrenze der Anfrage: Bild plus Formularfelder.
	slideMaxRequest = slideMaxImageBytes + 1<<20
)

var slideKindLabels = map[string]string{
	slideKindImage: "Bild", slideKindText: "Text", slideKindVorgaenge: "Vorgänge",
	slideKindStatsFaults: "Statistik Störungen", slideKindStatsTickets: "Statistik Tickets",
	slideKindStatsSetup: "Statistik Rüstzeiten", slideKindStatsDown: "Statistik Stillstände", slideKindGrafana: "Grafana-Dashboard",
	slideKindWebsite:    "Website",
	slideKindPlanShifts: "Schichtplan", slideKindPlanAbsences: "Abwesenheiten",
	slideKindPlanDuties: "Dienste und Bereitschaft", slideKindPlanMaint: "Wartungsplanung",
	slideKindTasks: "Aufgaben", slideKindProjectsYear: "Projekte im Jahresband",
}

var slideNotices = map[string]string{
	"created": "Folie angelegt.", "saved": "Folie gespeichert.", "deleted": "Folie gelöscht.",
}

// terminalSlideView: eine Zeile der Liste.
type terminalSlideView struct {
	terminalSlide
	KindLabel string
	HasImage  bool
	Summary   string // Kurzinfo: Text-Anfang bzw. gewählte Anlagen
	First     bool
	Last      bool
}

type TerminalSlidesPageData struct {
	BaseData
	NoStatsUsers []string // Infobildschirm-Benutzer ohne Recht „Statistik ansehen“ (Grafana-Folien bleiben leer)
	HasGrafana   bool
	Notice       string
	Slides       []terminalSlideView
}

type terminalSlideFormData struct {
	BaseData
	IsNew        bool
	Slide        terminalSlide
	HasImage     bool
	Err          string
	InfraCSV     string // gewählte Anlagen (IDs, kommagetrennt)
	InfraLabels  string // JSON: ID → Pfad, für die Anzeige im Formular
	MaxBody      int
	MaxTitle     int
	MaxMB        int
	NoStatsUsers []string
	Dashboards   []grafanaDashboard // vorhandene Grafana-Dashboards (Statistik-Kacheln) als Vorschläge
	Departments  []slideDeptOption  // Abteilungen zur Auswahl (Planungsfolien)
	ShiftPlans   []shiftPlanChoice  // Pläne zur Auswahl (Folie Schichtplan)
}

// slideDeptOption: eine Abteilung in der Auswahl einer Planungsfolie.
type slideDeptOption struct {
	ID, Name string
	On       bool
}

func (h *Handler) slidesAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !h.hasPerm(r, permTerminalSlides) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return false
	}
	return true
}

func slidesBack(w http.ResponseWriter, r *http.Request, notice string) {
	target := "/terminal-slides"
	if notice != "" {
		target += "?" + url.Values{"n": {notice}}.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// TerminalSlidesPage: GET /terminal-slides.
func (h *Handler) TerminalSlidesPage(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	list, err := h.listTerminalSlides(r.Context(), false)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("folien lesen")
		http.Error(w, "Die Folien konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	d := TerminalSlidesPageData{
		BaseData: h.baseData(r, "terminal-slides", "Infobildschirm", "Terminal"),
		Notice:   slideNotices[r.URL.Query().Get("n")],
	}
	for _, s := range list {
		d.HasGrafana = d.HasGrafana || s.Kind == slideKindGrafana
	}
	if d.HasGrafana {
		d.NoStatsUsers = h.globalTerminalsWithoutStatistics(r.Context())
	}
	for i, s := range list {
		v := terminalSlideView{terminalSlide: s, KindLabel: slideKindLabels[s.Kind],
			HasImage: s.Kind == slideKindImage && s.ImageType != "", First: i == 0, Last: i == len(list)-1}
		switch s.Kind {
		case slideKindText:
			v.Summary = slideExcerpt(s.Body, 80)
		case slideKindGrafana:
			v.Summary = s.URL
		case slideKindWebsite:
			v.Summary = s.URL
			if s.ReloadMinutes > 0 {
				v.Summary += fmt.Sprintf(" · Neuladen alle %d Min.", s.ReloadMinutes)
			}
		case slideKindStatsFaults, slideKindStatsTickets, slideKindStatsSetup, slideKindStatsDown:
			v.Summary = fmt.Sprintf("%s · %s", statsPeriodLabel(s.StatsDays), slideInfraSummary(len(s.InfraIDs)))
		case slideKindPlanShifts, slideKindPlanAbsences, slideKindPlanDuties, slideKindPlanMaint:
			v.Summary = planRangeLabel(s.PlanRange, s.PlanDays) + " · "
			switch n := len(s.DepartmentIDs); n {
			case 0:
				v.Summary += "alle Abteilungen"
			case 1:
				v.Summary += "1 Abteilung"
			default:
				v.Summary += fmt.Sprintf("%d Abteilungen", n)
			}
			if s.Kind == slideKindPlanShifts && s.ShiftPlanID != "" {
				var pn string
				if h.db != nil && h.db.QueryRow(r.Context(), "SELECT name FROM shift_plans WHERE id = $1::uuid", s.ShiftPlanID).Scan(&pn) == nil {
					v.Summary += " · Plan " + pn
				} else {
					v.Summary += " · ein Plan"
				}
			}
		case slideKindTasks:
			v.Summary = taskRangeLabel(time.Now(), s.TaskRange, s.TaskUntil) + " · " + slideDeptSummary(len(s.DepartmentIDs))
		case slideKindProjectsYear:
			v.Summary = yearModeLabel(time.Now(), s.YearMode) + " · " + slideDeptSummary(len(s.DepartmentIDs))
			if s.OnlyActive {
				v.Summary += " · nur aktive"
			}
		case slideKindVorgaenge:
			switch n := len(s.InfraIDs); {
			case n == 0:
				v.Summary = "alle Anlagen"
			case n == 1:
				v.Summary = "1 Anlage"
			default:
				v.Summary = fmt.Sprintf("%d Anlagen", n)
			}
			var what []string
			if s.ShowFaults {
				what = append(what, "Störungen")
			}
			if s.ShowTickets {
				what = append(what, "Tickets")
			}
			if len(what) == 0 {
				what = append(what, "nichts")
			}
			v.Summary += " · " + strings.Join(what, " und ")
		}
		d.Slides = append(d.Slides, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	h.render(w, "terminal_slides", d)
}

// statsPeriodLabel: Zeitraum einer Statistikfolie in Worten.
func statsPeriodLabel(days int) string {
	if days == 30 {
		return "letzte 5 Wochen"
	}
	return "7 Tage"
}

// globalTerminalsWithoutStatistics: Benutzernamen der Infobildschirme, die Grafana-Folien nicht sehen dürfen.
// Das Recht statistics.view gilt überall (Grafana ist nicht nach Anlagen getrennt); die Rolle terminal
// hat es bewusst nicht.
func (h *Handler) globalTerminalsWithoutStatistics(ctx context.Context) []string {
	if h.db == nil || h.rbac == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT u.id::text, u.username, u.role::text FROM terminal_settings ts
		JOIN users u ON u.id = ts.user_id WHERE ts.global_screen ORDER BY u.username`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, name, role string
		if rows.Scan(&id, &name, &role) == nil && !h.rbac.HasPermissionForUser(id, role, "statistics.view") {
			out = append(out, name)
		}
	}
	return out
}

// slideDeptOptions: alle aktiven Abteilungen, die gewählten angehakt (eine gewählte, inzwischen gelöschte fällt weg).
func (h *Handler) slideDeptOptions(ctx context.Context, selected []string) []slideDeptOption {
	rows, err := h.db.Query(ctx, `SELECT id::text, name FROM departments WHERE active ORDER BY lower(name)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []slideDeptOption
	for rows.Next() {
		var o slideDeptOption
		if rows.Scan(&o.ID, &o.Name) == nil {
			o.On = slices.Contains(selected, o.ID)
			out = append(out, o)
		}
	}
	return out
}

// slideDeptSummary: Kurzinfo zur Abteilungsauswahl.
func slideDeptSummary(n int) string {
	switch n {
	case 0:
		return "alle Abteilungen"
	case 1:
		return "1 Abteilung"
	}
	return fmt.Sprintf("%d Abteilungen", n)
}

// slideDeptsFromForm: die gewählten Abteilungen (mehrere Felder, je Feld auch kommagetrennt), ohne Doppelte.
func slideDeptsFromForm(r *http.Request) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range r.Form["department_ids"] {
		for _, id := range strings.FieldsFunc(raw, func(c rune) bool { return c == ',' || c == ';' || c == ' ' || c == '\n' || c == '\t' }) {
			if id = strings.ToLower(id); !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// parseFormDate: Datum aus dem Formular (JJJJ-MM-TT vom Datumsfeld oder TT.MM.JJJJ) als JJJJ-MM-TT; sonst der Text
// unverändert (die Prüfung meldet ihn).
func parseFormDate(v string) string {
	v = strings.TrimSpace(v)
	if d, err := time.ParseInLocation("02.01.2006", v, time.Local); err == nil {
		return d.Format(dayISO)
	}
	return v
}

// slideInfraSummary: Kurzinfo zur Anlagenauswahl.
func slideInfraSummary(n int) string {
	switch n {
	case 0:
		return "alle Anlagen"
	case 1:
		return "1 Anlage"
	}
	return fmt.Sprintf("%d Anlagen", n)
}

// slideInfraFromForm liest die Anlagenauswahl (infra_ids, kommagetrennt, ohne Doppelte).
func slideInfraFromForm(r *http.Request, msg string) ([]string, string) {
	var out []string
	seen := map[string]bool{}
	for _, id := range strings.FieldsFunc(r.FormValue("infra_ids"), func(c rune) bool { return c == 44 || c == 59 || c == 32 || c == 10 || c == 9 }) {
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > slideMaxInfra && msg == "" {
		msg = fmt.Sprintf("Bitte höchstens %d Anlagen wählen", slideMaxInfra)
	}
	return out, msg
}

// slideExcerpt: Anfang eines Textes in einer Zeile (für die Liste).
func slideExcerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// slideFormPage rendert das Formular (status 200, bei Eingabefehlern 422).
func (h *Handler) slideFormPage(w http.ResponseWriter, r *http.Request, s terminalSlide, isNew bool, hasImage bool, errMsg string, status int) {
	title := "Neue Folie"
	if !isNew {
		title = "Folie bearbeiten"
	}
	d := terminalSlideFormData{
		BaseData: h.baseData(r, "terminal-slides", title, "Infobildschirm"),
		IsNew:    isNew, Slide: s, HasImage: hasImage, Err: errMsg,
		InfraCSV: strings.Join(s.InfraIDs, ","),
		MaxBody:  slideMaxBody, MaxTitle: slideMaxTitle, MaxMB: slideMaxImageBytes >> 20,
	}
	labels := map[string]string{}
	if len(s.InfraIDs) > 0 {
		labels = h.infraPaths(r.Context(), s.InfraIDs)
	}
	b, _ := json.Marshal(labels)
	d.InfraLabels = string(b)
	if s.Kind == slideKindGrafana {
		d.NoStatsUsers = h.globalTerminalsWithoutStatistics(r.Context())
	}
	if (slideIsPlan(s.Kind) || slideIsWork(s.Kind)) && h.db != nil {
		d.Departments = h.slideDeptOptions(r.Context(), s.DepartmentIDs)
	}
	if h.db != nil {
		d.ShiftPlans = h.shiftPlanOptions(r.Context(), s.ShiftPlanID)
	}
	if base, _ := h.grafanaBase(r); h.db != nil {
		d.Dashboards, _ = h.grafanaDashboards(r.Context(), base, false)
		for i := range d.Dashboards { // Vorschlag als Adresse, die die Folie annimmt (Pfad unter /grafana/ oder https)
			u := d.Dashboards[i].URL
			if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, grafanaProxyPath+"/") {
				u = grafanaProxyPath + u
			}
			if validateGrafanaSlideURL(u) != nil {
				u = ""
			}
			d.Dashboards[i].URL = u
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
	}
	h.render(w, "terminal_slide_form", d)
}

// TerminalSlideNewPage: GET /terminal-slides/new?kind=…
func (h *Handler) TerminalSlideNewPage(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if _, ok := slideKindLabels[kind]; !ok {
		kind = slideKindImage
	}
	h.slideFormPage(w, r, terminalSlide{Kind: kind, DurationSeconds: slideDefaultSeconds, ShowFaults: true, ShowTickets: true, Active: true, StatsDays: 7,
		PlanRange: planRangeToday, PlanDays: planDefaultDays, TaskRange: taskRangeWeek, YearMode: yearModeCurrent, OnlyActive: true},
		true, false, "", http.StatusOK)
}

// TerminalSlideEditPage: GET /terminal-slides/{id}.
func (h *Handler) TerminalSlideEditPage(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	s, err := h.getTerminalSlide(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("folie lesen")
		http.Error(w, "Die Folie konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	h.slideFormPage(w, r, s, false, s.ImageType != "", "", http.StatusOK)
}

// slideFromRequest liest das Formular (multipart oder urlencoded). Der zweite Rückgabewert
// ist eine Meldung für den Benutzer, wenn die Eingabe nicht zu lesen ist.
func slideFromRequest(w http.ResponseWriter, r *http.Request) (terminalSlide, string) {
	var s terminalSlide
	r.Body = http.MaxBytesReader(w, r.Body, slideMaxRequest)
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return s, "Das Bild darf höchstens 5 MB groß sein"
		}
		return s, "Das Formular konnte nicht gelesen werden"
	}
	// Alle Felder bleiben erhalten, damit das Formular bei einem Fehler wieder gefüllt wird.
	s.Kind = r.FormValue("kind")
	s.Title = strings.TrimSpace(r.FormValue("title"))
	s.Active = r.FormValue("active") != ""
	s.ShowFaults, s.ShowTickets = true, true
	var msg string
	if v := strings.TrimSpace(r.FormValue("duration")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			msg = fmt.Sprintf("Die Dauer muss eine ganze Zahl zwischen %d und %d Sekunden sein", slideMinSeconds, slideMaxSeconds)
			n = slideDefaultSeconds
		}
		s.DurationSeconds = n
	}
	switch s.Kind {
	case slideKindText:
		s.Body = strings.TrimSpace(strings.ReplaceAll(r.FormValue("body"), "\r\n", "\n"))
	case slideKindGrafana:
		s.URL = strings.TrimSpace(r.FormValue("url"))
	case slideKindWebsite:
		s.URL = strings.TrimSpace(r.FormValue("web_url")) // eigener Feldname: das Grafana-Feld „url“ steht im selben Formular
		if v := strings.TrimSpace(r.FormValue("reload_minutes")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > slideMaxReloadMinutes {
				if msg == "" {
					msg = fmt.Sprintf("Das Neuladen alle n Minuten ist eine ganze Zahl von 0 (nie) bis %d", slideMaxReloadMinutes)
				}
				n = 0
			}
			s.ReloadMinutes = n
		}
	case slideKindStatsFaults, slideKindStatsTickets, slideKindStatsSetup, slideKindStatsDown:
		s.StatsDays, _ = strconv.Atoi(r.FormValue("stats_days"))
		s.InfraIDs, msg = slideInfraFromForm(r, msg)
	case slideKindTasks, slideKindProjectsYear:
		s.TaskRange = strings.TrimSpace(r.FormValue("task_range"))
		s.TaskUntil = parseFormDate(r.FormValue("task_until"))
		s.YearMode = strings.TrimSpace(r.FormValue("year_mode"))
		s.OnlyActive = r.FormValue("only_active") != ""
		s.DepartmentIDs = slideDeptsFromForm(r)
		if len(s.DepartmentIDs) > planMaxDepts && msg == "" {
			msg = fmt.Sprintf("Bitte höchstens %d Abteilungen wählen", planMaxDepts)
		}
	case slideKindPlanShifts, slideKindPlanAbsences, slideKindPlanDuties, slideKindPlanMaint:
		s.PlanRange = strings.TrimSpace(r.FormValue("plan_range"))
		s.PlanDays = planDefaultDays
		if v := strings.TrimSpace(r.FormValue("plan_days")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < planMinDays || n > planMaxDays {
				if msg == "" && s.PlanRange == planRangeDays {
					msg = fmt.Sprintf("Die Anzahl der Tage ist eine ganze Zahl von %d bis %d", planMinDays, planMaxDays)
				}
				n = planDefaultDays
			}
			s.PlanDays = n
		}
		s.DepartmentIDs = slideDeptsFromForm(r)
		if len(s.DepartmentIDs) > planMaxDepts && msg == "" {
			msg = fmt.Sprintf("Bitte höchstens %d Abteilungen wählen", planMaxDepts)
		}
		if s.Kind == slideKindPlanShifts {
			s.ShiftPlanID = strings.TrimSpace(r.FormValue("shift_plan_id"))
		}
	case slideKindVorgaenge:
		s.ShowFaults = r.FormValue("show_faults") != ""
		s.ShowTickets = r.FormValue("show_tickets") != ""
		seen := map[string]bool{}
		for _, id := range strings.FieldsFunc(r.FormValue("infra_ids"), func(c rune) bool { return c == ',' || c == ';' || c == ' ' || c == '\n' || c == '\t' }) {
			id = strings.ToLower(id)
			if !seen[id] {
				seen[id] = true
				s.InfraIDs = append(s.InfraIDs, id)
			}
		}
		if len(s.InfraIDs) > slideMaxInfra && msg == "" {
			msg = fmt.Sprintf("Bitte höchstens %d Anlagen wählen", slideMaxInfra)
		}
	case slideKindImage:
		f, _, err := r.FormFile("image")
		if err == nil {
			defer f.Close()
			data, rerr := io.ReadAll(io.LimitReader(f, slideMaxImageBytes+1))
			if rerr != nil && msg == "" {
				msg = "Das Bild konnte nicht gelesen werden"
			}
			s.Image = data
		} else if !errors.Is(err, http.ErrMissingFile) && msg == "" {
			msg = "Das Bild konnte nicht gelesen werden"
		}
	}
	return s, msg
}

// TerminalSlideSaveWeb: POST /terminal-slides (anlegen) und POST /terminal-slides/{id} (ändern).
func (h *Handler) TerminalSlideSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	isNew := id == ""
	var old terminalSlide
	if !isNew {
		var err error
		if old, err = h.getTerminalSlide(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			componentLog("terminal").Error().Err(err).Msg("folie lesen")
			http.Error(w, "Die Folie konnte nicht gelesen werden", http.StatusInternalServerError)
			return
		}
	}
	s, msg := slideFromRequest(w, r)
	s.ID = id
	hasImage := old.ImageType != "" // das vorhandene Bild (bei neuer Folie keines)
	if msg == "" {
		// Eingaben prüfen, ohne zu speichern (validateTerminalSlide trägt Dauer und Bildart ein).
		chk := s
		if err := validateTerminalSlide(&chk); err != nil {
			msg = err.Error()
		} else if chk.Kind == slideKindImage && len(chk.Image) == 0 && !hasImage {
			msg = errSlideNeedsImage.Error()
		} else if chk.Kind == slideKindWebsite && websiteIsOwnHost(chk.URL, r.Host) {
			msg = "Die eigene PDH-Adresse lässt sich nicht als Website einbetten (für Grafana gibt es die Grafana-Folie)"
		}
	}
	if msg != "" {
		s.Image = nil
		if s.DurationSeconds == 0 {
			s.DurationSeconds = slideDefaultSeconds
		}
		s.ImageType, s.UpdatedAt = old.ImageType, old.UpdatedAt
		h.slideFormPage(w, r, s, isNew, hasImage && s.Kind == slideKindImage, msg, http.StatusUnprocessableEntity)
		return
	}
	if err := h.saveTerminalSlide(r.Context(), &s); err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			http.NotFound(w, r)
		case errors.Is(err, errSlideNeedsImage):
			s.Image = nil
			h.slideFormPage(w, r, s, isNew, false, err.Error(), http.StatusUnprocessableEntity)
		case errors.Is(err, errShiftPlanGone):
			s.Image = nil
			s.ShiftPlanID = ""
			h.slideFormPage(w, r, s, isNew, false, err.Error(), http.StatusUnprocessableEntity)
		default:
			componentLog("terminal").Error().Err(err).Msg("folie speichern")
			http.Error(w, "Die Folie konnte nicht gespeichert werden", http.StatusInternalServerError)
		}
		return
	}
	if isNew {
		slidesBack(w, r, "created")
		return
	}
	slidesBack(w, r, "saved")
}

// TerminalSlideDeleteWeb: POST /terminal-slides/{id}/delete.
func (h *Handler) TerminalSlideDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	if err := h.deleteTerminalSlide(r.Context(), chi.URLParam(r, "id")); err != nil {
		componentLog("terminal").Error().Err(err).Msg("folie löschen")
		http.Error(w, "Die Folie konnte nicht gelöscht werden", http.StatusInternalServerError)
		return
	}
	slidesBack(w, r, "deleted")
}

// TerminalSlideMoveWeb: POST /terminal-slides/{id}/move?d=-1|1.
func (h *Handler) TerminalSlideMoveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	var delta int
	switch r.URL.Query().Get("d") {
	case "-1":
		delta = -1
	case "1":
		delta = 1
	default:
		http.Error(w, "ungültige Richtung", http.StatusBadRequest)
		return
	}
	if err := h.moveTerminalSlide(r.Context(), chi.URLParam(r, "id"), delta); err != nil {
		componentLog("terminal").Error().Err(err).Msg("folie verschieben")
		http.Error(w, "Die Folie konnte nicht verschoben werden", http.StatusInternalServerError)
		return
	}
	slidesBack(w, r, "")
}

// TerminalSlideImageWeb: GET /terminal-slides/{id}/image – das Bild für die Vorschau.
func (h *Handler) TerminalSlideImageWeb(w http.ResponseWriter, r *http.Request) {
	if !h.slidesAllowed(w, r) {
		return
	}
	data, typ, err := h.terminalSlideImage(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("folienbild lesen")
		http.Error(w, "Das Bild konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// websiteIsOwnHost: zeigt die Adresse auf denselben Rechner wie die Anfrage (PDH selbst)? Ein eingebettetes PDH wäre
// im Rahmen nicht von der Folienschau getrennt (gleicher Ursprung); der Player lehnt es ebenfalls ab.
func websiteIsOwnHost(raw, reqHost string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(reqHost)
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	return h != "" && strings.EqualFold(u.Hostname(), h)
}

// slideUpdatedKey: Zusatz für die Bildadresse, damit die Vorschau nach einer Änderung neu geladen wird.
func (s terminalSlide) UpdatedKey() int64 { return s.UpdatedAt.Truncate(time.Second).Unix() }
