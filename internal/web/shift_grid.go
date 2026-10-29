package web

import (
	"context"
	"net/http"
	"net/url"

	"pdh/internal/core/users"
)

// Schichtplan-Raster als Widget (Dashboard) und Baustein (Leitstand): sieht
// aus wie der Schichtplan im Planer – Schichten × Tage mit den Namen, darunter
// die Sonderdienste (mit Telefonnummer, wenn bei der Dienstart eingestellt).
// Oben Woche, Schichtmodell und Feldhöhe wählen; nur lesend – eingeteilt wird
// im Planer.

// ShiftGridView: Daten für widgets/shift_grid.gohtml.
type ShiftGridView struct {
	Base                                          string // Adresse für Woche/Modell (ohne Parameter dahinter: ? oder &)
	Week, PrevWeek, NextWeek, ThisWeek, WeekLabel string
	Days                                          []plannerDay
	Rows                                          []plannerRow
	DutyRows                                      []plannerDutyRow
	Models                                        []plannerModel
	ModelID                                       string
	Embed                                         bool // im Leitstand: kein Link in den Planer
}

// Sep: Trenner für weitere Parameter an Base.
func (v ShiftGridView) Sep() string {
	for _, c := range v.Base {
		if c == '?' {
			return "&"
		}
	}
	return "?"
}

// shiftGridView: Woche aus ?week= und ?model=; dept/group vom Leitstand.
func (h *Handler) shiftGridView(r *http.Request, base string, board *leitstand) ShiftGridView {
	q := r.URL.Query()
	if board != nil {
		q.Set("dept", board.DepartmentID)
		q.Set("group", board.GroupID)
	}
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	pd := PlannerData{CanViewAll: h.canAbsenceViewAll(r)}
	h.fillShiftWeek(r2, &pd)
	return ShiftGridView{Base: base, Week: pd.Week, PrevWeek: pd.PrevWeek, NextWeek: pd.NextWeek, ThisWeek: pd.ThisWeek,
		WeekLabel: pd.WeekLabel, Days: pd.Days, Rows: pd.Rows, DutyRows: pd.DutyRows, Models: pd.Models, ModelID: pd.ModelID,
		Embed: board != nil}
}

// ShiftGridWeb: GET /planner/grid-view – Raster für das Dashboard-Widget.
func (h *Handler) ShiftGridWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canPlannerView(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderFragment(w, "shift-grid", h.shiftGridView(r, "/planner/grid-view", nil))
}

// ShiftGridBoardWeb: GET /global/shiftplan?b=<leitstand> – Baustein „Schichtplan“
// im Leitstand (auch ohne Anmeldung lesbar, wie der Leitstand selbst).
func (h *Handler) ShiftGridBoardWeb(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	board := h.resolveLeitstand(r)
	if b := h.boardBySlug(r, r.URL.Query().Get("b")); b != nil {
		board = b
	}
	if u := h.sessionUser(r); u != nil {
		r = r.WithContext(context.WithValue(r.Context(), "user", u))
	} else {
		r = r.WithContext(context.WithValue(r.Context(), "user", &users.User{}))
	}
	h.renderFragment(w, "shift-grid", h.shiftGridView(r, "/global/shiftplan?b="+url.QueryEscape(board.Slug), board))
}
