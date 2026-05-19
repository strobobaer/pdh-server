package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Typen ────────────────────────────────────────────────────

type PlanType string
type Interval string
type TaskStatus string
type Priority string

const (
	PlanPreventive  PlanType = "preventive"  // Vorbeugend
	PlanInspection  PlanType = "inspection"  // Inspektion
	PlanCalibration PlanType = "calibration" // Kalibrierung
	PlanCleaning    PlanType = "cleaning"    // Reinigung

	IntervalDaily     Interval = "daily"
	IntervalWeekly    Interval = "weekly"
	IntervalMonthly   Interval = "monthly"
	IntervalQuarterly Interval = "quarterly"
	IntervalYearly    Interval = "yearly"

	TaskOpen       TaskStatus = "open"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskSkipped    TaskStatus = "skipped"

	PrioLow      Priority = "low"
	PrioMedium   Priority = "medium"
	PrioHigh     Priority = "high"
	PrioCritical Priority = "critical"
)

// ── Wartungsplan (wiederkehrend) ─────────────────────────────

type MaintenancePlan struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description,omitempty"`
	Type             PlanType   `json:"type"`
	InfrastructureID string     `json:"infrastructure_id"`
	Interval         Interval   `json:"interval"`
	IntervalDays     int        `json:"interval_days"`
	EstimatedMin     int        `json:"estimated_min"`
	Priority         Priority   `json:"priority"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	Active           bool       `json:"active"`
	LastExecutedAt   *time.Time `json:"last_executed_at,omitempty"`
	NextDueAt        time.Time  `json:"next_due_at"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`

	// Joined
	InfraName    string `json:"infra_name,omitempty"`
	AssigneeName string `json:"assignee_name,omitempty"`
}

// ── Wartungsauftrag (einmalig) ───────────────────────────────

type MaintenanceTask struct {
	ID               string     `json:"id"`
	PlanID           *string    `json:"plan_id,omitempty"`
	Title            string     `json:"title"`
	Description      string     `json:"description,omitempty"`
	Type             PlanType   `json:"type"`
	InfrastructureID string     `json:"infrastructure_id"`
	Priority         Priority   `json:"priority"`
	Status           TaskStatus `json:"status"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	DueDate          time.Time  `json:"due_date"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	DurationMin      *int       `json:"duration_min,omitempty"`
	Notes            string     `json:"notes,omitempty"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`

	// Joined
	InfraName    string `json:"infra_name,omitempty"`
	AssigneeName string `json:"assignee_name,omitempty"`
}

// ── Wartungsprüfpunkt ────────────────────────────────────────

type ChecklistItem struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
	Done        bool   `json:"done"`
	SortOrder   int    `json:"sort_order"`
}

// ── Inputs ───────────────────────────────────────────────────

type CreatePlanInput struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Type             PlanType `json:"type"`
	InfrastructureID string   `json:"infrastructure_id"`
	Interval         Interval `json:"interval"`
	IntervalDays     int      `json:"interval_days"`
	EstimatedMin     int      `json:"estimated_min"`
	Priority         Priority `json:"priority"`
	AssignedTo       *string  `json:"assigned_to,omitempty"`
	FirstDueAt       string   `json:"first_due_at"`
}

type CreateTaskInput struct {
	PlanID           *string  `json:"plan_id,omitempty"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Type             PlanType `json:"type"`
	InfrastructureID string   `json:"infrastructure_id"`
	Priority         Priority `json:"priority"`
	AssignedTo       *string  `json:"assigned_to,omitempty"`
	DueDate          string   `json:"due_date"`
}

type CompleteTaskInput struct {
	Notes       string `json:"notes"`
	DurationMin int    `json:"duration_min"`
}

type UpdateTaskInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    Priority `json:"priority"`
	DueDate     string   `json:"due_date"`
	Notes       string   `json:"notes"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) CreatePlan(ctx context.Context, p *MaintenancePlan) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO maintenance_plans
		  (id, name, description, type, infrastructure_id, interval_type, interval_days,
		   estimated_min, priority, assigned_to, active, next_due_at, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, true, $10, $11)
		RETURNING id, active, created_at`,
		p.Name, p.Description, p.Type, p.InfrastructureID,
		p.Interval, p.IntervalDays, p.EstimatedMin, p.Priority,
		p.AssignedTo, p.NextDueAt, p.CreatedBy,
	).Scan(&p.ID, &p.Active, &p.CreatedAt)
}

func (r *Repository) ListPlans(ctx context.Context, infraID string) ([]*MaintenancePlan, error) {
	query := `
		SELECT mp.id, mp.name, COALESCE(mp.description,''), mp.type,
		       mp.infrastructure_id, mp.interval_type, mp.interval_days,
		       mp.estimated_min, mp.priority, mp.assigned_to, mp.active,
		       mp.last_executed_at, mp.next_due_at, mp.created_by, mp.created_at,
		       COALESCE(i.name,''), COALESCE(u.first_name||' '||u.last_name,'')
		FROM maintenance_plans mp
		LEFT JOIN infrastructure i ON mp.infrastructure_id = i.id
		LEFT JOIN users u ON mp.assigned_to = u.id
		WHERE mp.active=true`
	args := []interface{}{}
	if infraID != "" {
		query += " AND mp.infrastructure_id=$1"
		args = append(args, infraID)
	}
	query += " ORDER BY mp.next_due_at ASC"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []*MaintenancePlan
	for rows.Next() {
		p := &MaintenancePlan{}
		rows.Scan(&p.ID, &p.Name, &p.Description, &p.Type,
			&p.InfrastructureID, &p.Interval, &p.IntervalDays,
			&p.EstimatedMin, &p.Priority, &p.AssignedTo, &p.Active,
			&p.LastExecutedAt, &p.NextDueAt, &p.CreatedBy, &p.CreatedAt,
			&p.InfraName, &p.AssigneeName)
		plans = append(plans, p)
	}
	return plans, nil
}

func (r *Repository) CreateTask(ctx context.Context, t *MaintenanceTask) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO maintenance_tasks
		  (id, plan_id, title, description, type, infrastructure_id,
		   priority, status, assigned_to, due_date, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, 'open', $7, $8, $9)
		RETURNING id, status, created_at`,
		t.PlanID, t.Title, t.Description, t.Type, t.InfrastructureID,
		t.Priority, t.AssignedTo, t.DueDate, t.CreatedBy,
	).Scan(&t.ID, &t.Status, &t.CreatedAt)
}

func scanTask(row interface{ Scan(...interface{}) error }) (*MaintenanceTask, error) {
	t := &MaintenanceTask{}
	err := row.Scan(&t.ID, &t.PlanID, &t.Title, &t.Description, &t.Type,
		&t.InfrastructureID, &t.Priority, &t.Status, &t.AssignedTo,
		&t.DueDate, &t.StartedAt, &t.CompletedAt, &t.DurationMin,
		&t.Notes, &t.CreatedBy, &t.CreatedAt,
		&t.InfraName, &t.AssigneeName)
	return t, err
}

func (r *Repository) ListTasks(ctx context.Context, status TaskStatus, infraID string) ([]*MaintenanceTask, error) {
	query := `
		SELECT mt.id, mt.plan_id, mt.title, COALESCE(mt.description,''), mt.type,
		       mt.infrastructure_id, mt.priority, mt.status, mt.assigned_to,
		       mt.due_date, mt.started_at, mt.completed_at, mt.duration_min,
		       COALESCE(mt.notes,''), mt.created_by, mt.created_at,
		       COALESCE(i.name,''), COALESCE(u.first_name||' '||u.last_name,'')
		FROM maintenance_tasks mt
		LEFT JOIN infrastructure i ON mt.infrastructure_id = i.id
		LEFT JOIN users u ON mt.assigned_to = u.id
		WHERE 1=1`
	args := []interface{}{}
	n := 1
	if status != "" {
		query += fmt.Sprintf(" AND mt.status=$%d", n)
		args = append(args, status)
		n++
	}
	if infraID != "" {
		query += fmt.Sprintf(" AND mt.infrastructure_id=$%d", n)
		args = append(args, infraID)
	}
	query += " ORDER BY CASE mt.priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, mt.due_date ASC"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*MaintenanceTask
	for rows.Next() {
		t := &MaintenanceTask{}
		rows.Scan(&t.ID, &t.PlanID, &t.Title, &t.Description, &t.Type,
			&t.InfrastructureID, &t.Priority, &t.Status, &t.AssignedTo,
			&t.DueDate, &t.StartedAt, &t.CompletedAt, &t.DurationMin,
			&t.Notes, &t.CreatedBy, &t.CreatedAt,
			&t.InfraName, &t.AssigneeName)
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (r *Repository) GetTaskByID(ctx context.Context, id string) (*MaintenanceTask, error) {
	return scanTask(r.db.QueryRow(ctx, `
		SELECT mt.id, mt.plan_id, mt.title, COALESCE(mt.description,''), mt.type,
		       mt.infrastructure_id, mt.priority, mt.status, mt.assigned_to,
		       mt.due_date, mt.started_at, mt.completed_at, mt.duration_min,
		       COALESCE(mt.notes,''), mt.created_by, mt.created_at,
		       COALESCE(i.name,''), COALESCE(u.first_name||' '||u.last_name,'')
		FROM maintenance_tasks mt
		LEFT JOIN infrastructure i ON mt.infrastructure_id = i.id
		LEFT JOIN users u ON mt.assigned_to = u.id
		WHERE mt.id=$1`, id))
}

func (r *Repository) UpdateTask(ctx context.Context, id string, in *UpdateTaskInput) error {
	dueDate, err := time.Parse("2006-01-02", in.DueDate)
	if err != nil {
		_, err = r.db.Exec(ctx, `
			UPDATE maintenance_tasks
			SET title=$1, description=$2, priority=$3, notes=$4
			WHERE id=$5`,
			in.Title, in.Description, in.Priority, in.Notes, id)
		return err
	}
	_, err = r.db.Exec(ctx, `
		UPDATE maintenance_tasks
		SET title=$1, description=$2, priority=$3, due_date=$4, notes=$5
		WHERE id=$6`,
		in.Title, in.Description, in.Priority, dueDate, in.Notes, id)
	return err
}

func (r *Repository) StartTask(ctx context.Context, id, userID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE maintenance_tasks SET status='in_progress', started_at=NOW()
		 WHERE id=$1 AND (assigned_to=$2 OR assigned_to IS NULL)`, id, userID)
	return err
}

func (r *Repository) CompleteTask(ctx context.Context, id, userID, notes string, durationMin int) error {
	_, err := r.db.Exec(ctx,
		`UPDATE maintenance_tasks SET status='done', completed_at=NOW(),
		 notes=$1, duration_min=$2 WHERE id=$3`,
		notes, durationMin, id)
	if err != nil {
		return err
	}
	// Plan aktualisieren falls vorhanden
	_, err = r.db.Exec(ctx, `
		UPDATE maintenance_plans mp
		SET last_executed_at=NOW(),
		    next_due_at=NOW() + (interval_days || ' days')::interval
		FROM maintenance_tasks mt
		WHERE mt.id=$1 AND mt.plan_id=mp.id`, id)
	return err
}

func (r *Repository) GetDueToday(ctx context.Context) ([]*MaintenanceTask, error) {
	rows, err := r.db.Query(ctx, `
		SELECT mt.id, mt.plan_id, mt.title, COALESCE(mt.description,''), mt.type,
		       mt.infrastructure_id, mt.priority, mt.status, mt.assigned_to,
		       mt.due_date, mt.started_at, mt.completed_at, mt.duration_min,
		       COALESCE(mt.notes,''), mt.created_by, mt.created_at,
		       COALESCE(i.name,''), COALESCE(u.first_name||' '||u.last_name,'')
		FROM maintenance_tasks mt
		LEFT JOIN infrastructure i ON mt.infrastructure_id = i.id
		LEFT JOIN users u ON mt.assigned_to = u.id
		WHERE mt.due_date::date <= NOW()::date AND mt.status IN ('open','in_progress')
		ORDER BY mt.priority, mt.due_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*MaintenanceTask
	for rows.Next() {
		t := &MaintenanceTask{}
		rows.Scan(&t.ID, &t.PlanID, &t.Title, &t.Description, &t.Type,
			&t.InfrastructureID, &t.Priority, &t.Status, &t.AssignedTo,
			&t.DueDate, &t.StartedAt, &t.CompletedAt, &t.DurationMin,
			&t.Notes, &t.CreatedBy, &t.CreatedAt,
			&t.InfraName, &t.AssigneeName)
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (r *Repository) DeleteTask(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM maintenance_tasks WHERE id=$1`, id)
	return err
}

func (r *Repository) GenerateTasksFromPlans(ctx context.Context, createdBy string) (int, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, type, infrastructure_id, priority, assigned_to, interval_days, next_due_at
		FROM maintenance_plans
		WHERE active=true AND next_due_at::date <= NOW()::date`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var planID, name, infraID, priority string
		var assigned *string
		var intervalDays int
		var nextDue time.Time
		var planType PlanType
		rows.Scan(&planID, &name, &planType, &infraID, &priority, &assigned, &intervalDays, &nextDue)

		t := &MaintenanceTask{
			PlanID: &planID, Title: name, Type: planType,
			InfrastructureID: infraID, Priority: Priority(priority),
			AssignedTo: assigned, DueDate: nextDue, CreatedBy: createdBy,
		}
		if err := r.CreateTask(ctx, t); err == nil {
			count++
		}
	}
	return count, nil
}

// ── Service ──────────────────────────────────────────────────

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) CreatePlan(ctx context.Context, in *CreatePlanInput, userID string) (*MaintenancePlan, error) {
	nextDue, err := time.Parse("2006-01-02", in.FirstDueAt)
	if err != nil {
		nextDue = time.Now().AddDate(0, 0, in.IntervalDays)
	}

	intervalDays := in.IntervalDays
	if intervalDays == 0 {
		switch in.Interval {
		case IntervalDaily:
			intervalDays = 1
		case IntervalWeekly:
			intervalDays = 7
		case IntervalMonthly:
			intervalDays = 30
		case IntervalQuarterly:
			intervalDays = 90
		case IntervalYearly:
			intervalDays = 365
		}
	}

	p := &MaintenancePlan{
		Name: in.Name, Description: in.Description, Type: in.Type,
		InfrastructureID: in.InfrastructureID, Interval: in.Interval,
		IntervalDays: intervalDays, EstimatedMin: in.EstimatedMin,
		Priority: in.Priority, AssignedTo: in.AssignedTo,
		Active: true, NextDueAt: nextDue, CreatedBy: userID,
	}
	return p, s.repo.CreatePlan(ctx, p)
}

func (s *Service) ListPlans(ctx context.Context, infraID string) ([]*MaintenancePlan, error) {
	return s.repo.ListPlans(ctx, infraID)
}

func (s *Service) CreateTask(ctx context.Context, in *CreateTaskInput, userID string) (*MaintenanceTask, error) {
	dueDate, _ := time.Parse("2006-01-02", in.DueDate)
	if dueDate.IsZero() {
		dueDate = time.Now().AddDate(0, 0, 7)
	}

	t := &MaintenanceTask{
		PlanID: in.PlanID, Title: in.Title, Description: in.Description,
		Type: in.Type, InfrastructureID: in.InfrastructureID,
		Priority: in.Priority, AssignedTo: in.AssignedTo,
		DueDate: dueDate, CreatedBy: userID,
	}
	return t, s.repo.CreateTask(ctx, t)
}

func (s *Service) ListTasks(ctx context.Context, status TaskStatus, infraID string) ([]*MaintenanceTask, error) {
	return s.repo.ListTasks(ctx, status, infraID)
}

func (s *Service) GetTaskByID(ctx context.Context, id string) (*MaintenanceTask, error) {
	return s.repo.GetTaskByID(ctx, id)
}

func (s *Service) UpdateTask(ctx context.Context, id string, in *UpdateTaskInput) error {
	if in.Priority == "" {
		in.Priority = PrioMedium
	}
	return s.repo.UpdateTask(ctx, id, in)
}

func (s *Service) StartTask(ctx context.Context, id, userID string) error {
	return s.repo.StartTask(ctx, id, userID)
}

func (s *Service) CompleteTask(ctx context.Context, id, userID string, in *CompleteTaskInput) error {
	return s.repo.CompleteTask(ctx, id, userID, in.Notes, in.DurationMin)
}

func (s *Service) GetDueToday(ctx context.Context) ([]*MaintenanceTask, error) {
	return s.repo.GetDueToday(ctx)
}

func (s *Service) DeleteTask(ctx context.Context, id string) error {
	return s.repo.DeleteTask(ctx, id)
}

func (s *Service) GenerateTasks(ctx context.Context, userID string) (int, error) {
	return s.repo.GenerateTasksFromPlans(ctx, userID)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	// Pläne
	r.Get("/plans", h.ListPlans)
	r.Post("/plans", h.CreatePlan)

	// Aufträge
	r.Get("/tasks", h.ListTasks)
	r.Post("/tasks", h.CreateTask)
	r.Get("/tasks/due", h.GetDueToday)
	r.Post("/tasks/generate", h.GenerateTasks)
	r.Post("/tasks/{id}/start", h.StartTask)
	r.Post("/tasks/{id}/complete", h.CompleteTask)

	return r
}

func decode(r *http.Request, v interface{}) error { return json.NewDecoder(r.Body).Decode(v) }
func uid(r *http.Request) string                  { v, _ := r.Context().Value(middleware.UserIDKey).(string); return v }

func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.svc.ListPlans(r.Context(), r.URL.Query().Get("infrastructure_id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, plans)
}

func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var in CreatePlanInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	p, err := h.svc.CreatePlan(r.Context(), &in, uid(r))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.svc.ListTasks(r.Context(),
		TaskStatus(r.URL.Query().Get("status")),
		r.URL.Query().Get("infrastructure_id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, tasks)
}

func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var in CreateTaskInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	t, err := h.svc.CreateTask(r.Context(), &in, uid(r))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, t)
}

func (h *Handler) GetDueToday(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.svc.GetDueToday(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, tasks)
}

func (h *Handler) GenerateTasks(w http.ResponseWriter, r *http.Request) {
	count, err := h.svc.GenerateTasks(r.Context(), uid(r))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]int{"generated": count})
}

func (h *Handler) StartTask(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.StartTask(r.Context(), chi.URLParam(r, "id"), uid(r)); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "in_progress"})
}

func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	var in CompleteTaskInput
	decode(r, &in)
	if err := h.svc.CompleteTask(r.Context(), chi.URLParam(r, "id"), uid(r), &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "done"})
}
