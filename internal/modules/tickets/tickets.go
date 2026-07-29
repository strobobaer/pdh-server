package tickets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"pdh/internal/core/addins"
	"pdh/internal/core/synclink"
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

	// Kostenstelle (eigenständig, unabhängig von der Infrastruktur)
	CostCenterID     *string `json:"cost_center_id,omitempty"`
	CostCenterNumber string  `json:"cost_center_number,omitempty"`
	CostCenterName   string  `json:"cost_center_name,omitempty"`

	// Pflichtangaben beim Schließen: Maßnahmen-Verlauf + Ersatzteilverwendung
	Resolution    *string `json:"resolution,omitempty"`
	RootCause     *string `json:"root_cause,omitempty"`
	NoPartsNeeded bool    `json:"no_parts_needed,omitempty"`
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
	CostCenterID     *string    `json:"cost_center_id,omitempty"`
}

// Repository
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, t *Ticket) error {
	tags, _ := json.Marshal(t.Tags)
	query := `
		INSERT INTO tickets (id, title, description, priority, status, assigned_to, responsible_to, created_by, infrastructure_id, due_date, tags, cost_center_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open', $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		t.Title, t.Description, t.Priority,
		t.AssignedTo, t.ResponsibleTo, t.CreatedBy, t.InfrastructureID, t.DueDate, tags, t.CostCenterID,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Ticket, error) {
	t := &Ticket{}
	var tags []byte
	query := `SELECT t.id, t.title, t.description, t.priority, t.status, t.assigned_to, t.responsible_to,
		t.created_by, t.infrastructure_id, t.record_image_attachment_id, t.due_date, t.resolved_at, t.archived_at, t.created_at, t.updated_at, t.tags,
		t.cost_center_id, COALESCE(cc.number,''), COALESCE(cc.name,'')
		FROM tickets t
		LEFT JOIN cost_centers cc ON t.cost_center_id = cc.id
		WHERE t.id = $1`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&t.ID, &t.Title, &t.Description, &t.Priority, &t.Status,
		&t.AssignedTo, &t.ResponsibleTo, &t.CreatedBy, &t.InfrastructureID,
		&t.RecordImageID, &t.DueDate, &t.ResolvedAt, &t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt, &tags,
		&t.CostCenterID, &t.CostCenterNumber, &t.CostCenterName,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(tags, &t.Tags)
	return t, nil
}

func (r *Repository) List(ctx context.Context, status Status) ([]*Ticket, error) {
	query := `SELECT t.id, t.title, t.description, t.priority, t.status, t.assigned_to, t.responsible_to,
		t.created_by, t.infrastructure_id, t.due_date, t.archived_at, t.created_at, t.updated_at, t.tags,
		t.cost_center_id, COALESCE(cc.number,''), COALESCE(cc.name,'')
		FROM tickets t
		LEFT JOIN cost_centers cc ON t.cost_center_id = cc.id`
	args := []interface{}{}
	if status == Status("archive") {
		query += " WHERE t.archived_at IS NOT NULL"
	} else if status != "" {
		query += " WHERE t.status = $1 AND t.archived_at IS NULL"
		args = append(args, status)
	} else {
		query += " WHERE t.archived_at IS NULL"
	}
	query += " ORDER BY CASE t.priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, t.created_at DESC"

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
			&t.AssignedTo, &t.ResponsibleTo, &t.CreatedBy, &t.InfrastructureID, &t.DueDate, &t.ArchivedAt,
			&t.CreatedAt, &t.UpdatedAt, &tags,
			&t.CostCenterID, &t.CostCenterNumber, &t.CostCenterName,
		)
		if err != nil {
			return nil, err
		}
		json.Unmarshal(tags, &t.Tags)
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
	if err == nil && userID != "" {
		notifyTicketLinkerStatus(ctx, id, string(status), userID)
	}
	return err
}

// GetLinkedFaultID liefert die ID der mit diesem Ticket verknuepften
// Stoerung (leer, falls keine verknuepft ist).
func (r *Repository) GetLinkedFaultID(ctx context.Context, ticketID string) (string, error) {
	var id *string
	err := r.db.QueryRow(ctx, `SELECT linked_fault_id::text FROM tickets WHERE id=$1`, ticketID).Scan(&id)
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}

// GetLinkedTaskID liefert die ID der mit diesem Ticket verknuepften
// Aufgabe (leer, falls keine verknuepft ist).
func (r *Repository) GetLinkedTaskID(ctx context.Context, ticketID string) (string, error) {
	var id *string
	err := r.db.QueryRow(ctx, `SELECT linked_task_id::text FROM tickets WHERE id=$1`, ticketID).Scan(&id)
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}

// SetStatusDirect setzt den Status ohne Pflichtpruefung und OHNE die
// Synchronisation zur verknuepften Stoerung erneut auszuloesen (wird nur
// vom synclink-Paket aufgerufen, um Endlosschleifen zu vermeiden).
func (r *Repository) SetStatusDirect(ctx context.Context, ticketID, status, userID string) error {
	query := `UPDATE tickets SET status=$1, updated_at=NOW()`
	if status == "resolved" || status == "closed" {
		query += `, resolved_at=COALESCE(resolved_at,NOW()), archived_at=COALESCE(archived_at,NOW())`
	}
	query += ` WHERE id=$2`
	_, err := r.db.Exec(ctx, query, status, ticketID)
	return err
}

// AddActionDirect erfasst eine Massnahme (wird vom synclink-Paket
// aufgerufen, um eine bei der Stoerung erfasste Massnahme auch beim
// verknuepften Ticket einzutragen).
func (r *Repository) AddActionDirect(ctx context.Context, ticketID, description, userID string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO ticket_actions (id, ticket_id, description, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)`,
		ticketID, description, userID)
	return err
}

// UpdateCostCenter setzt/ändert die Kostenstelle eines bestehenden Tickets.
func (r *Repository) UpdateCostCenter(ctx context.Context, id string, costCenterID *string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE tickets SET cost_center_id=$1, updated_at=NOW() WHERE id=$2`,
		costCenterID, id)
	return err
}

// UpdateDueDate setzt/löscht ausschließlich das Fälligkeitsdatum eines
// Tickets (z.B. per Drag im Dashboard-Zeitstrahl).
func (r *Repository) UpdateDueDate(ctx context.Context, id string, dueDate *time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE tickets SET due_date=$1, updated_at=NOW() WHERE id=$2`,
		dueDate, id)
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
	globalTicketRepo = repo
	return &Service{repo: repo}
}

var eventBus *addins.EventBus

// SetEventBus verbindet dieses Modul mit dem Add-in-Ereignis-Bus (wird in main.go gesetzt).
func SetEventBus(b *addins.EventBus) { eventBus = b }

var ticketLinker *synclink.Linker
var globalTicketRepo *Repository

// SetLinker verbindet das tickets-Modul mit dem synclink-Paket (wird in
// main.go gesetzt), um Status und Massnahmen mit einer verknuepften
// Stoerung zu synchronisieren.
func SetLinker(l *synclink.Linker) {
	ticketLinker = l
	l.RegisterTicket(
		func(ctx context.Context, ticketID, status, userID string) error {
			return globalTicketRepo.SetStatusDirect(ctx, ticketID, status, userID)
		},
		func(ctx context.Context, ticketID, description, userID string) error {
			return globalTicketRepo.AddActionDirect(ctx, ticketID, description, userID)
		},
		func(ctx context.Context, ticketID string) (string, error) {
			return globalTicketRepo.GetLinkedFaultID(ctx, ticketID)
		},
		func(ctx context.Context, ticketID string) (string, error) {
			return globalTicketRepo.GetLinkedTaskID(ctx, ticketID)
		},
	)
}

// notifyTicketLinkerStatus ruft den synclink-Hook auf (falls gesetzt).
func notifyTicketLinkerStatus(ctx context.Context, ticketID, status, userID string) {
	if ticketLinker != nil {
		ticketLinker.OnTicketStatusChanged(ctx, ticketID, status, userID)
	}
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
		CostCenterID:     in.CostCenterID,
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return t, err
	}
	if eventBus != nil {
		eventBus.Publish("ticket.created", map[string]interface{}{
			"id": t.ID, "title": t.Title, "priority": string(t.Priority), "created_by": t.CreatedBy,
		})
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
	if status == StatusResolved || status == StatusClosed {
		return fmt.Errorf(`bitte das Ticket über "Ticket lösen" abschließen, nicht über die Status-Auswahl`)
	}
	return s.repo.UpdateStatus(ctx, id, status, userID)
}

func (s *Service) UpdateCostCenter(ctx context.Context, id string, costCenterID *string) error {
	return s.repo.UpdateCostCenter(ctx, id, costCenterID)
}

// UpdateDueDate setzt/löscht das Fälligkeitsdatum eines Tickets (z.B. per
// Drag im Dashboard-Zeitstrahl). dueDate == nil löscht es wieder.
func (s *Service) UpdateDueDate(ctx context.Context, id string, dueDate *time.Time) error {
	return s.repo.UpdateDueDate(ctx, id, dueDate)
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
	r.Post("/{id}/status", h.UpdateStatus)
	r.Post("/{id}/cost-center", h.UpdateCostCenter)
	r.Post("/{id}/due-date", h.UpdateDueDate)
	r.Post("/{id}/comments", h.AddComment)
	r.Post("/{id}/resolve", h.Resolve)
	r.Post("/{id}/actions", h.AddAction)
	r.Get("/{id}/actions", h.GetActions)
	r.Delete("/{id}/actions/{actionID}", h.DeleteAction)
	r.Get("/{id}/parts-usage", h.GetPartsUsage)
	r.Post("/{id}/pending-parts", h.AddPendingPart)
	r.Get("/{id}/pending-parts", h.GetPendingParts)
	r.Delete("/{id}/pending-parts/{partItemID}", h.DeletePendingPart)

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

// UpdateCostCenter akzeptiert sowohl JSON (für die JS-Fetch-Formulare in
// tickets.gohtml) als auch normale Formulardaten (für htmx-Formulare wie in
// ticket_detail.gohtml).
func (h *Handler) UpdateDueDate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		DueDate string `json:"due_date"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	var dueDate *time.Time
	if in.DueDate != "" {
		parsed, err := time.Parse("2006-01-02", in.DueDate)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "ungültiges datum")
			return
		}
		dueDate = &parsed
	}
	if err := h.svc.UpdateDueDate(r.Context(), id, dueDate); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "gespeichert"})
}

func (h *Handler) UpdateCostCenter(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var costCenterID *string

	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var in struct {
			CostCenterID *string `json:"cost_center_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			response.Error(w, http.StatusBadRequest, "ungültige eingabe")
			return
		}
		costCenterID = in.CostCenterID
	} else {
		if err := r.ParseForm(); err != nil {
			response.Error(w, http.StatusBadRequest, "ungültige eingabe")
			return
		}
		if v := r.FormValue("cost_center_id"); v != "" {
			costCenterID = &v
		}
	}

	if err := h.svc.UpdateCostCenter(r.Context(), id, costCenterID); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "aktualisiert"})
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
