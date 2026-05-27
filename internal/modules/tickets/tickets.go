package tickets

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"pdh/internal/integrations/nextcloud"
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
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	Priority         Priority   `json:"priority"`
	Status           Status     `json:"status"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	ResponsibleTo    *string    `json:"responsible_to,omitempty"`
	CreatedBy        string     `json:"created_by"`
	InfrastructureID *string    `json:"infrastructure_id,omitempty"`
	RecordImageID    *string    `json:"record_image_attachment_id,omitempty"`
	Tags             []string   `json:"tags"`
	DueDate          *time.Time `json:"due_date,omitempty"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
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
	ResponsibleTo    *string    `json:"responsible_to,omitempty"`
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
	query := `
		INSERT INTO tickets (id, title, description, priority, status, assigned_to, responsible_to, created_by, infrastructure_id, due_date)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open', $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		t.Title, t.Description, t.Priority,
		t.AssignedTo, t.ResponsibleTo, t.CreatedBy, t.InfrastructureID, t.DueDate,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Ticket, error) {
	t := &Ticket{}
	query := `SELECT id, title, description, priority, status, assigned_to, responsible_to,
		created_by, infrastructure_id, record_image_attachment_id, due_date, resolved_at, archived_at, created_at, updated_at
		FROM tickets WHERE id = $1`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&t.ID, &t.Title, &t.Description, &t.Priority, &t.Status,
		&t.AssignedTo, &t.ResponsibleTo, &t.CreatedBy, &t.InfrastructureID,
		&t.RecordImageID, &t.DueDate, &t.ResolvedAt, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

func (r *Repository) List(ctx context.Context, status Status) ([]*Ticket, error) {
	query := `SELECT id, title, description, priority, status, assigned_to, responsible_to,
		created_by, infrastructure_id, due_date, archived_at, created_at, updated_at
		FROM tickets`
	args := []interface{}{}
	if status == Status("archive") {
		query += " WHERE archived_at IS NOT NULL"
	} else if status != "" {
		query += " WHERE status = $1 AND archived_at IS NULL"
		args = append(args, status)
	} else {
		query += " WHERE archived_at IS NULL"
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
		err := rows.Scan(
			&t.ID, &t.Title, &t.Description, &t.Priority, &t.Status,
			&t.AssignedTo, &t.ResponsibleTo, &t.CreatedBy, &t.InfrastructureID, &t.DueDate, &t.ArchivedAt,
			&t.CreatedAt, &t.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status Status, userID string) error {
	query := `UPDATE tickets SET status=$1, updated_at=NOW()`
	if status == StatusResolved || status == StatusClosed {
		query += ", resolved_at=COALESCE(resolved_at,NOW()), archived_at=COALESCE(archived_at,NOW()), archived_by=$3"
	}
	query += " WHERE id=$2"
	var err error
	if status == StatusResolved || status == StatusClosed {
		_, err = r.db.Exec(ctx, query, status, id, userID)
	} else {
		_, err = r.db.Exec(ctx, query, status, id)
	}
	if err == nil {
		_, _ = r.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
			VALUES ('ticket', $1, 'status', 'status', $2, $3, 'Status geändert')`, id, string(status), userID)
	}
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
		ResponsibleTo:    in.ResponsibleTo,
		CreatedBy:        createdBy,
		InfrastructureID: in.InfrastructureID,
		Tags:             in.Tags,
		DueDate:          in.DueDate,
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return t, err
	}

	s.syncTicketToDeckAsync(t)
	return t, nil
}

func (s *Service) syncTicketToDeckAsync(t *Ticket) {
	deck := nextcloud.DeckClientFromEnv()
	if !deck.Enabled() {
		log.Debug().Str("ticket_id", t.ID).Str("title", t.Title).Msg("nextcloud deck ticket-sync deaktiviert")
		return
	}

	due := ""
	if t.DueDate != nil {
		due = t.DueDate.Format(time.RFC3339)
	}

	input := nextcloud.DeckCardInput{
		RefType:     "ticket",
		RefID:       t.ID,
		Title:       "Ticket: " + t.Title,
		Description: t.Description,
		Priority:    string(t.Priority),
		DueDate:     due,
	}

	go func(ticketID, title string, cardInput nextcloud.DeckCardInput) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		card, err := deck.CreateTicketCard(ctx, cardInput)
		if err != nil {
			log.Error().Err(err).Str("ticket_id", ticketID).Str("title", title).Msg("nextcloud deck ticket-karte erstellen fehlgeschlagen")
			return
		}
		if card != nil {
			log.Info().Str("ticket_id", ticketID).Int("deck_card_id", card.ID).Msg("nextcloud deck ticket-karte erstellt")
		}
	}(t.ID, t.Title, input)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Ticket, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status Status) ([]*Ticket, error) {
	return s.repo.List(ctx, status)
}

func (s *Service) UpdateStatus(ctx context.Context, id string, status Status, userID string) error {
	return s.repo.UpdateStatus(ctx, id, status, userID)
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
	r.Put("/{id}/status", h.UpdateStatus)
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
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.UpdateStatus(r.Context(), id, in.Status, userID); err != nil {
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
