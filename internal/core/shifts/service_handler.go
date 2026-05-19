package shifts

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Service ──────────────────────────────────────────────────

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateModel(ctx context.Context, in *CreateModelInput) (*ShiftModel, error) {
	m := &ShiftModel{Name: in.Name, Description: in.Description}
	return m, s.repo.CreateModel(ctx, m)
}

func (s *Service) ListModels(ctx context.Context) ([]*ShiftModel, error) {
	return s.repo.ListModels(ctx)
}

func (s *Service) CreateShift(ctx context.Context, in *CreateShiftInput) (*ShiftDefinition, error) {
	if in.Color == "" {
		in.Color = "#3B82F6"
	}
	sd := &ShiftDefinition{
		ModelID: in.ModelID, Name: in.Name, ShortName: in.ShortName,
		StartTime: in.StartTime, EndTime: in.EndTime, Color: in.Color, IsNight: in.IsNight,
	}
	return sd, s.repo.CreateShift(ctx, sd)
}

func (s *Service) ListShifts(ctx context.Context, modelID string) ([]*ShiftDefinition, error) {
	return s.repo.ListShifts(ctx, modelID)
}

func (s *Service) Assign(ctx context.Context, in *AssignShiftInput, createdBy string) (*ShiftAssignment, error) {
	a := &ShiftAssignment{UserID: in.UserID, ShiftID: in.ShiftID, Date: in.Date, Note: in.Note, CreatedBy: createdBy}
	return a, s.repo.Assign(ctx, a)
}

func (s *Service) BulkAssign(ctx context.Context, in *BulkAssignInput, createdBy string) (int, error) {
	count := 0
	for _, item := range in.Assignments {
		a := &ShiftAssignment{UserID: item.UserID, ShiftID: item.ShiftID, Date: item.Date, Note: item.Note, CreatedBy: createdBy}
		if err := s.repo.Assign(ctx, a); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Service) GetWeekPlan(ctx context.Context, weekStart string) (*WeekPlan, error) {
	t, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return nil, err
	}

	weekDay := int(t.Weekday())
	if weekDay == 0 {
		weekDay = 7
	}
	monday := t.AddDate(0, 0, -(weekDay - 1))
	sunday := monday.AddDate(0, 0, 6)

	assignments, err := s.repo.GetWeekPlan(ctx, monday.Format("2006-01-02"), sunday.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}

	// Strukturieren
	userMap := make(map[string]*UserWeekPlan)
	for _, a := range assignments {
		if _, ok := userMap[a.UserID]; !ok {
			userMap[a.UserID] = &UserWeekPlan{
				UserID: a.UserID, UserName: a.UserName,
				Days: make(map[string]DayEntry),
			}
		}
		userMap[a.UserID].Days[a.Date] = DayEntry{
			ShiftName: a.ShiftName, ShortName: a.ShortName,
			Color: a.Color, StartTime: a.StartTime, EndTime: a.EndTime,
		}
	}

	var users []UserWeekPlan
	for _, u := range userMap {
		users = append(users, *u)
	}

	return &WeekPlan{
		WeekStart: monday.Format("2006-01-02"),
		WeekEnd:   sunday.Format("2006-01-02"),
		Users:     users,
	}, nil
}

func (s *Service) CreateAbsence(ctx context.Context, in *CreateAbsenceInput) (*Absence, error) {
	start, _ := time.Parse("2006-01-02", in.StartDate)
	end, _ := time.Parse("2006-01-02", in.EndDate)
	days := int(end.Sub(start).Hours()/24) + 1

	a := &Absence{UserID: in.UserID, Type: in.Type, StartDate: in.StartDate,
		EndDate: in.EndDate, Days: days, Note: in.Note}
	return a, s.repo.CreateAbsence(ctx, a)
}

func (s *Service) ListAbsences(ctx context.Context, userID string, status AbsenceStatus) ([]*Absence, error) {
	return s.repo.ListAbsences(ctx, userID, status)
}

func (s *Service) ApproveAbsence(ctx context.Context, id, approvedBy string, approved bool) error {
	return s.repo.ApproveAbsence(ctx, id, approvedBy, approved)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	// Schichtmodelle
	r.Get("/models", h.ListModels)
	r.Post("/models", h.CreateModel)
	r.Get("/models/{id}/shifts", h.ListShifts)
	r.Post("/models/{id}/shifts", h.CreateShift)

	// Zuweisungen
	r.Post("/assign", h.Assign)
	r.Post("/assign/bulk", h.BulkAssign)
	r.Delete("/assign/{userID}/{date}", h.DeleteAssignment)
	r.Get("/week/{date}", h.GetWeekPlan)
	r.Get("/user/{id}", h.GetUserShifts)

	// Abwesenheiten
	r.Get("/absences", h.ListAbsences)
	r.Post("/absences", h.CreateAbsence)
	r.Put("/absences/{id}/approve", h.ApproveAbsence)

	return r
}

func decode(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.svc.ListModels(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, models)
}

func (h *Handler) CreateModel(w http.ResponseWriter, r *http.Request) {
	var in CreateModelInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	m, err := h.svc.CreateModel(r.Context(), &in)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, m)
}

func (h *Handler) ListShifts(w http.ResponseWriter, r *http.Request) {
	shifts, err := h.svc.ListShifts(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, shifts)
}

func (h *Handler) CreateShift(w http.ResponseWriter, r *http.Request) {
	var in CreateShiftInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	in.ModelID = chi.URLParam(r, "id")
	s, err := h.svc.CreateShift(r.Context(), &in)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, s)
}

func (h *Handler) Assign(w http.ResponseWriter, r *http.Request) {
	var in AssignShiftInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	a, err := h.svc.Assign(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, a)
}

func (h *Handler) BulkAssign(w http.ResponseWriter, r *http.Request) {
	var in BulkAssignInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	count, err := h.svc.BulkAssign(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, map[string]int{"assigned": count})
}

func (h *Handler) DeleteAssignment(w http.ResponseWriter, r *http.Request) {
	err := h.svc.repo.DeleteAssignment(r.Context(), chi.URLParam(r, "userID"), chi.URLParam(r, "date"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) GetWeekPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := h.svc.GetWeekPlan(r.Context(), chi.URLParam(r, "date"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, plan)
}

func (h *Handler) GetUserShifts(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().AddDate(0, 1, 0).Format("2006-01-02")
	}
	shifts, err := h.svc.repo.GetUserShifts(r.Context(), chi.URLParam(r, "id"), from, to)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, shifts)
}

func (h *Handler) ListAbsences(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	status := AbsenceStatus(r.URL.Query().Get("status"))
	absences, err := h.svc.ListAbsences(r.Context(), userID, status)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, absences)
}

func (h *Handler) CreateAbsence(w http.ResponseWriter, r *http.Request) {
	var in CreateAbsenceInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	a, err := h.svc.CreateAbsence(r.Context(), &in)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, a)
}

func (h *Handler) ApproveAbsence(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Approved bool `json:"approved"`
	}
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	approverID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.ApproveAbsence(r.Context(), chi.URLParam(r, "id"), approverID, in.Approved); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]bool{"approved": in.Approved})
}
