package tickets

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// Priorität und Status
type Priority string
type Status string

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"

	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusPending    Status = "pending"
	StatusResolved   Status = "resolved"
	StatusClosed     Status = "closed"
)

// Ticket - Hauptmodell
type Ticket struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	Priority       Priority  `json:"priority"`
	Status         Status    `json:"status"`
	AssignedTo     *string   `json:"assigned_to,omitempty"`
	CreatedBy      string    `json:"created_by"`
	InfrastructureID *string `json:"infrastructure_id,omitempty"`
	Tags           []string  `json:"tags"`
	DueDate        *time.Time `json:"due_date,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Kommentar zu einem Ticket
type Comment struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticket_id"`
	UserID    string    `json:"user_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateInput
type CreateInput struct {
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	Priority         Priority   `json:"priority"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	InfrastructureID *string    `json:"infrastructure_id,omitempty"`
	Tags             []string   `json:"tags"`
	DueDate          *time.Time `json:"due_date,omitempty"`
}

// Repository
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, t *Ticket) error {
	// FIX: Tags wurden vorher nicht gespeichert
	tags, _ := json.Marshal(t.Tags)
	query := `
		INSERT INTO tickets (id, title, description, priority, status, assigned_to, created_by, infrastructure_id, due_date, tags)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open', $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		t.Title, t.Description, t.Priority,
		t.AssignedTo, t.CreatedBy, t.InfrastructureID, t.DueDate, tags,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Ticket, error) {
	t := &Ticket{}
	var tags []byte
	// FIX: tags jetzt mit ausgelesen
	query := `SELECT id, title, description, priority, status, assigned_to,
		created_by, infrastructure_id, due_date, resolved_at, created_at, updated_at, tags
		FROM tickets WHERE id = $1`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&t.ID, &t.Title, &t.Description, &t.Priority, &t.Status,
		&t.AssignedTo, &t.CreatedBy, &t.InfrastructureID,
		&t.DueDate, &t.ResolvedAt, &t.CreatedAt, &t.UpdatedAt, &tags,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(tags, &t.Tags)
	return t, nil
}

func (r *Repository) List(ctx context.Context, status Status) ([]*Ticket, error) {
	// FIX: tags jetzt mit ausgelesen
	query := `SELECT id, title, description, priority, status, assigned_to,
		created_by, due_date, created_at, updated_at, tags
		FROM tickets`
	args := []interface{}{}
	if status != "" {
		query += " WHERE status = $1"
		args = append(args, status)
	}
	query += " ORDER BY CASE priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, created_at DESC"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []*Ticket
	for rows.Next() {
		t := &Ticket{}
		var tags []byte
		err := rows.Scan(
			&t.ID, &t.Title, &t.Description, &t.Priority, &t.Status,
			&t.AssignedTo, &t.CreatedBy, &t.DueDate,
			&t.CreatedAt, &t.UpdatedAt, &tags,
		)
		if err != nil {
			return nil, err
		}
		json.Unmarshal(tags, &t.Tags)
		tickets = append(tickets, t)
	}
	return tickets, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status Status) error {
	query := `UPDATE tickets SET status=$1, updated_at=NOW()`
	if status == StatusResolved {
		query += ", resolved_at=NOW()"
	}
	query += " WHERE id=$2"
	_, err := r.db.Exec(ctx, query, status, id)
	return err
}

func (r *Repository) AddComment(ctx context.Context, c *Comment) error {
	query := `INSERT INTO ticket_comments (id, ticket_id, user_id, text)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id, created_at`
	return r.db.QueryRow(ctx, query, c.TicketID, c.UserID, c.Text).
		Scan(&c.ID, &c.CreatedAt)
}

// Service
type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, in *CreateInput, createdBy string) (*Ticket, error) {
	t := &Ticket{
		Title:            in.Title,
		Description:      in.Description,
		Priority:         in.Priority,
		AssignedTo:       in.AssignedTo,
		CreatedBy:        createdBy,
		InfrastructureID: in.InfrastructureID,
		Tags:             in.Tags,
		DueDate:          in.DueDate,
	}
	return t, s.repo.Create(ctx, t)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Ticket, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status Status) ([]*Ticket, error) {
	return s.repo.List(ctx, status)
}

func (s *Service) UpdateStatus(ctx context.Context, id string, status Status) error {
	return s.repo.UpdateStatus(ctx, id, status)
}

func (s *Service) AddComment(ctx context.Context, ticketID, userID, text string) (*Comment, error) {
	c := &Comment{TicketID: ticketID, UserID: userID, Text: text}
	return c, s.repo.AddComment(ctx, c)
}

// Handler
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/status", h.UpdateStatus) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/{id}/comments", h.AddComment)

	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := Status(r.URL.Query().Get("status"))
	tickets, err := h.svc.List(r.Context(), status)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, tickets)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	ticket, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, ticket)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ticket, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "ticket nicht gefunden")
		return
	}
	response.JSON(w, http.StatusOK, ticket)
}

func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Status Status `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	if err := h.svc.UpdateStatus(r.Context(), id, in.Status); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": string(in.Status)})
}

func (h *Handler) AddComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	comment, err := h.svc.AddComment(r.Context(), id, userID, in.Text)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, comment)
}
