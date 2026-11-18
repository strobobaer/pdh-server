package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/ruesten"
)

// Rüstzeiten als Vorgang – Seiten: der Arbeitsbereich unter /setup (gemeinsam
// mit Tickets, Störungen, Aufgaben und Wartung, work_board.go), die Seite zum
// Anlegen und die Detailseite. Die Seiten zeigen an und sammeln Eingaben; die
// Aktionen stehen in setup_actions.go.

// setupCan: Recht für eine Rüstzeit (id "" = irgendwo vorhanden). Für eine
// bestimmte Rüstzeit zählt die Abteilung ihrer Anlage (hasPermRec).
func (h *Handler) setupCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if id == "" {
		return h.hasPerm(r, perm)
	}
	return h.hasPermRec(r, perm, setupRef, id)
}

// setupCanInfra: Recht für eine neue Rüstzeit an dieser Anlage (Abteilung der Anlage).
func (h *Handler) setupCanInfra(r *http.Request, perm, infraID string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	return h.hasPermRec(r, perm, "infrastructure", infraID)
}

// setupBase: Seitenkopf; nur in Tests ohne Rechtedienst leer.
func (h *Handler) setupBase(r *http.Request, title string) BaseData {
	if h.rbac == nil {
		var none BaseData
		return none
	}
	return h.baseData(r, "setup", title, "Rüstzeiten")
}

// setupDenied lehnt ab: Seiten leiten zur Startseite, Aktionen antworten 403.
func setupDenied(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Error(w, "keine berechtigung", http.StatusForbidden)
}

// SetupPage: GET /setup – Arbeitsbereich der Rüstzeiten.
func (h *Handler) SetupPage(w http.ResponseWriter, r *http.Request) {
	if !h.setupCan(r, permSetupView, "") {
		setupDenied(w, r)
		return
	}
	h.WorkBoardPage(w, r, setupRef)
}

// SetupDetailData ist das Datenmodell der Detailseite und der Seite zum Anlegen.
type SetupDetailData struct {
	BaseData
	IsNew                           bool
	ID                              string
	E                               *ruesten.Entry
	Code, StatusText, StatusClass   string
	FaultURL, Duration              string
	InfraID, InfraName              string
	ReasonID, Note                  string
	StartValue, EndValue, DurationM string // datetime-local bzw. Minuten
	MaxMinutes                      int
	Overdue, TimesLocked            bool
	Reasons                         []ruesten.Reason
	CanEdit, CanDelete              bool
	Notice, Err                     string
}

const setupFormLayout = "2006-01-02T15:04"

// newSetupDetail bereitet eine Rüstzeit für die Detailseite auf (ohne Rechte und Katalog).
func newSetupDetail(e *ruesten.Entry, now time.Time) SetupDetailData {
	d := SetupDetailData{
		ID: e.ID, E: e, Code: ruesten.Number(e.Number), StatusText: setupStatusText(e.Status),
		InfraID: e.InfraID, InfraName: e.InfraName, ReasonID: e.ReasonID, Note: e.Note, MaxMinutes: e.MaxMinutes,
		StartValue: e.StartedAt.Local().Format(setupFormLayout), TimesLocked: e.Status == ruesten.StatusFault,
	}
	switch e.Status {
	case ruesten.StatusRunning:
		d.StatusClass = "b-blue"
		d.Overdue = now.After(e.StartedAt.Add(time.Duration(e.MaxMinutes) * time.Minute))
		d.Duration = setupDuration(e.StartedAt, now)
	case ruesten.StatusDone:
		d.StatusClass = "b-green"
	default:
		d.StatusClass = "b-red"
	}
	if e.EndedAt != nil {
		d.EndValue = e.EndedAt.Local().Format(setupFormLayout)
		d.Duration = setupDuration(e.StartedAt, *e.EndedAt)
	}
	if e.FaultID != nil {
		d.FaultURL = "/faults/" + *e.FaultID
	}
	return d
}

// setupReasonOptions: aktive Gründe, dazu der bisherige Grund der Rüstzeit (auch wenn inzwischen inaktiv).
func (h *Handler) setupReasonOptions(r *http.Request, current string) []ruesten.Reason {
	st := h.setupStore()
	if st == nil {
		return nil
	}
	all, err := st.Reasons(r.Context())
	if err != nil {
		componentLog("rüstzeiten").Error().Err(err).Msg("gründe laden")
		return nil
	}
	var out []ruesten.Reason
	for _, x := range all {
		if x.Active || x.ID == current {
			out = append(out, x)
		}
	}
	return out
}

// SetupNewPage: GET /setup/new – Rüstzeit von Hand anlegen. Nach einem Fehler
// kommen die Eingaben über die Adresse zurück (infra, reason, started_at,
// ended_at, duration_min, note, err); ?infra= belegt die Anlage vor.
func (h *Handler) SetupNewPage(w http.ResponseWriter, r *http.Request) {
	if !h.setupCan(r, permSetupEdit, "") {
		setupDenied(w, r)
		return
	}
	q := r.URL.Query()
	d := SetupDetailData{IsNew: true, CanEdit: true, Err: q.Get("err"),
		ReasonID: q.Get("reason"), Note: q.Get("note"), EndValue: q.Get("ended_at"), DurationM: q.Get("duration_min"),
		StartValue: q.Get("started_at")}
	if d.StartValue == "" {
		d.StartValue = time.Now().Format(setupFormLayout)
	}
	if id := q.Get("infra"); uuidInPathRe.MatchString(id) && h.db != nil {
		if err := h.db.QueryRow(r.Context(), `SELECT id::text, name FROM infrastructure WHERE id = $1::uuid`, id).Scan(&d.InfraID, &d.InfraName); err != nil {
			d.InfraID, d.InfraName = "", ""
		}
	}
	d.Reasons = h.setupReasonOptions(r, "")
	d.BaseData = h.setupBase(r, "Rüstzeit anlegen")
	h.render(w, "setup_detail", d)
}

// SetupDetailPage: GET /setup/{id} – Detailseite einer Rüstzeit.
func (h *Handler) SetupDetailPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if !h.setupCan(r, permSetupView, id) {
		setupDenied(w, r)
		return
	}
	e, err := h.setupStore().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, ruesten.ErrNotFound) {
			http.Error(w, "Rüstzeit nicht gefunden", http.StatusNotFound)
		} else {
			http.Error(w, "Rüstzeit konnte nicht geladen werden", http.StatusInternalServerError)
		}
		return
	}
	d := newSetupDetail(e, time.Now())
	d.Reasons = h.setupReasonOptions(r, e.ReasonID)
	d.CanEdit = h.setupCan(r, permSetupEdit, id)
	d.CanDelete = h.setupCan(r, permSetupAdmin, id)
	d.Notice, d.Err = r.URL.Query().Get("notice"), r.URL.Query().Get("err")
	d.BaseData = h.setupBase(r, d.Code+" · "+setupName(e))
	h.render(w, "setup_detail", d)
}

// setupName: Name der Rüstzeit – Rüsttaste, bei Handeingabe der Grund.
func setupName(e *ruesten.Entry) string {
	if e.ButtonName != "" {
		return e.ButtonName
	}
	return e.ReasonName
}

// setupMinutes liest eine positive ganze Minutenzahl; leer = 0.
func setupMinutes(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n > 0 && n <= 7*24*60
}
