package tasks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Priority string
type Status string

const (
	PrioLow      Priority = "low"
	PrioMedium   Priority = "medium"
	PrioHigh     Priority = "high"
	PrioCritical Priority = "critical"

	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusResolved   Status = "resolved"
	StatusClosed     Status = "closed"
)

type Task struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	Status         Status     `json:"status"`
	Priority       Priority   `json:"priority"`
	AssignedTo     *string    `json:"assigned_to,omitempty"`
	ResponsibleTo  *string    `json:"responsible_to,omitempty"`
	DueDate        *time.Time `json:"due_date,omitempty"`
	StartDate      *time.Time `json:"start_date,omitempty"`
	ProjectID      *string    `json:"project_id,omitempty"`
	LinkedFaultID  *string    `json:"linked_fault_id,omitempty"`
	LinkedTicketID *string    `json:"linked_ticket_id,omitempty"`
	Resolution     string     `json:"resolution,omitempty"`
	RootCause      string     `json:"root_cause,omitempty"`
	NoPartsNeeded  bool       `json:"no_parts_needed"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// Joined
	AssigneeName    string `json:"assignee_name,omitempty"`
	ResponsibleName string `json:"responsible_name,omitempty"`
	ProjectName     string `json:"project_name,omitempty"`
}

type CreateTaskInput struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Priority      Priority `json:"priority"`
	AssignedTo    *string  `json:"assigned_to,omitempty"`
	ResponsibleTo *string  `json:"responsible_to,omitempty"`
	DueDate       string   `json:"due_date"`
	StartDate     string   `json:"start_date"`
	ProjectID     *string  `json:"project_id,omitempty"`
}

type UpdateTaskInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    Priority `json:"priority"`
	DueDate     string   `json:"due_date"`
	StartDate   string   `json:"start_date"`
	ProjectID   *string  `json:"project_id,omitempty"`
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, t *Task) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO tasks (id, title, description, priority, assigned_to, responsible_to,
			due_date, start_date, project_id, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, status, created_at, updated_at`,
		t.Title, t.Description, t.Priority, t.AssignedTo, t.ResponsibleTo,
		t.DueDate, t.StartDate, t.ProjectID, t.CreatedBy,
	).Scan(&t.ID, &t.Status, &t.CreatedAt, &t.UpdatedAt)
}

func scanTask(row interface{ Scan(...interface{}) error }) (*Task, error) {
	t := &Task{}
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority,
		&t.AssignedTo, &t.ResponsibleTo, &t.DueDate, &t.StartDate, &t.ProjectID,
		&t.LinkedFaultID, &t.LinkedTicketID, &t.Resolution, &t.RootCause, &t.NoPartsNeeded,
		&t.ResolvedAt, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		&t.AssigneeName, &t.ResponsibleName, &t.ProjectName)
	return t, err
}

const selectTaskColumns = `
	t.id, t.title, COALESCE(t.description,''), t.status, t.priority,
	t.assigned_to, t.responsible_to, t.due_date, t.start_date, t.project_id,
	t.linked_fault_id, t.linked_ticket_id, COALESCE(t.resolution,''), COALESCE(t.root_cause,''), t.no_parts_needed,
	t.resolved_at, t.created_by, t.created_at, t.updated_at,
	COALESCE(ua.first_name || ' ' || ua.last_name, ''), COALESCE(ur.first_name || ' ' || ur.last_name, ''),
	COALESCE(p.name, '')`

const taskJoins = `
	FROM tasks t
	LEFT JOIN users ua ON t.assigned_to = ua.id
	LEFT JOIN users ur ON t.responsible_to = ur.id
	LEFT JOIN projects p ON t.project_id = p.id`

func (r *Repository) GetByID(ctx context.Context, id string) (*Task, error) {
	query := "SELECT " + selectTaskColumns + " " + taskJoins + " WHERE t.id=$1"
	return scanTask(r.db.QueryRow(ctx, query, id))
}

func (r *Repository) List(ctx context.Context, status Status, projectID string, unassignedOnly bool) ([]*Task, error) {
	query := "SELECT " + selectTaskColumns + " " + taskJoins + " WHERE 1=1"
	args := []interface{}{}
	n := 1
	if status != "" {
		query += fmt_sprintf_status(n)
		args = append(args, status)
		n++
	}
	if projectID != "" {
		query += fmt_sprintf_project(n)
		args = append(args, projectID)
		n++
	}
	if unassignedOnly {
		query += " AND t.project_id IS NULL"
	}
	query += " ORDER BY CASE t.priority WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, t.due_date NULLS LAST"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func fmt_sprintf_status(n int) string {
	return " AND t.status=$" + itoa(n)
}
func fmt_sprintf_project(n int) string {
	return " AND t.project_id=$" + itoa(n)
}
func itoa(n int) string {
	digits := "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return itoa(n/10) + string(digits[n%10])
}

func (r *Repository) Update(ctx context.Context, id string, in *UpdateTaskInput) error {
	_, err := r.db.Exec(ctx, `
		UPDATE tasks SET title=$1, description=$2, priority=$3, due_date=$4, start_date=$5,
			project_id=$6, updated_at=NOW()
		WHERE id=$7`,
		in.Title, in.Description, in.Priority, nullDate(in.DueDate), nullDate(in.StartDate), in.ProjectID, id)
	return err
}

func nullDate(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, id)
	return err
}
