package tasks

import (
	"context"
	"encoding/json"
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

// Assignee ist ein einzelner Eintrag der Mehrfach-Zuweisung einer
// Aufgabe (task_assignees) - anders als ResponsibleTo (genau eine
// Person, die Verantwortung traegt) koennen einer Aufgabe beliebig
// viele Assignees zugewiesen sein (z.B. ein Team arbeitet gemeinsam
// daran).
type Assignee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Task struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Description    string     `json:"description,omitempty"`
	Status         Status     `json:"status"`
	Priority       Priority   `json:"priority"`
	ResponsibleTo  *string    `json:"responsible_to,omitempty"`
	DueDate        *time.Time `json:"due_date,omitempty"`
	StartDate      *time.Time `json:"start_date,omitempty"`
	ProjectID      *string    `json:"project_id,omitempty"`
	Color          string     `json:"color,omitempty"`
	LinkedFaultID  *string    `json:"linked_fault_id,omitempty"`
	LinkedTicketID *string    `json:"linked_ticket_id,omitempty"`
	Resolution     string     `json:"resolution,omitempty"`
	RootCause      string     `json:"root_cause,omitempty"`
	NoPartsNeeded  bool       `json:"no_parts_needed"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// AssignedToIDs ist reines Schreib-Eingabefeld fuer Create/SetAssignees
	// (vom Aufrufer gesetzt), Assignees das beim Lesen befuellte Ergebnis.
	AssignedToIDs []string `json:"assigned_to_ids,omitempty"`

	// Joined
	Assignees       []Assignee `json:"assignees,omitempty"`
	ResponsibleName string     `json:"responsible_name,omitempty"`
	ProjectName     string     `json:"project_name,omitempty"`
}

type CreateTaskInput struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Priority      Priority `json:"priority"`
	AssignedToIDs []string `json:"assigned_to_ids,omitempty"`
	ResponsibleTo *string  `json:"responsible_to,omitempty"`
	DueDate       string   `json:"due_date"`
	StartDate     string   `json:"start_date"`
	ProjectID     *string  `json:"project_id,omitempty"`
	Color         string   `json:"color,omitempty"`
}

type UpdateTaskInput struct {
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Priority     Priority `json:"priority"`
	DueDate      string   `json:"due_date"`
	StartDate    string   `json:"start_date"`
	ProjectID    *string  `json:"project_id,omitempty"`
	ClearProject bool     `json:"clear_project,omitempty"`
	Color        string   `json:"color,omitempty"`
	// AssignedToIDs bleibt nil, wenn im Formular/JSON nicht mitgeschickt
	// (Zuweisung unveraendert) - ein leeres, aber nicht-nil Array leert
	// die Zuweisung bewusst.
	AssignedToIDs []string `json:"assigned_to_ids"`
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, t *Task) error {
	if err := r.db.QueryRow(ctx, `
		INSERT INTO tasks (id, title, description, priority, responsible_to,
			due_date, start_date, project_id, created_by, color)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9,''))
		RETURNING id, status, created_at, updated_at`,
		t.Title, t.Description, t.Priority, t.ResponsibleTo,
		t.DueDate, t.StartDate, t.ProjectID, t.CreatedBy, t.Color,
	).Scan(&t.ID, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	return r.SetAssignees(ctx, t.ID, t.AssignedToIDs)
}

// SetAssignees ersetzt die komplette Mehrfach-Zuweisung einer Aufgabe -
// aufgerufen bei Create (initiale Zuweisung) sowie bei Update, wenn das
// Formular/JSON explizit ein (auch leeres) assigned_to_ids-Array mitschickt.
func (r *Repository) SetAssignees(ctx context.Context, taskID string, userIDs []string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM task_assignees WHERE task_id=$1`, taskID); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	_, err := r.db.Exec(ctx, `INSERT INTO task_assignees (task_id, user_id) SELECT $1, unnest($2::uuid[])`, taskID, userIDs)
	return err
}

func scanTask(row interface{ Scan(...interface{}) error }) (*Task, error) {
	t := &Task{}
	var assigneesJSON []byte
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority,
		&t.ResponsibleTo, &t.DueDate, &t.StartDate, &t.ProjectID,
		&t.LinkedFaultID, &t.LinkedTicketID, &t.Resolution, &t.RootCause, &t.NoPartsNeeded,
		&t.ResolvedAt, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt,
		&t.ResponsibleName, &t.ProjectName, &t.Color, &assigneesJSON)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(assigneesJSON, &t.Assignees)
	return t, nil
}

const selectTaskColumns = `
	t.id, t.title, COALESCE(t.description,''), t.status, t.priority,
	t.responsible_to, t.due_date, t.start_date, t.project_id,
	t.linked_fault_id, t.linked_ticket_id, COALESCE(t.resolution,''), COALESCE(t.root_cause,''), t.no_parts_needed,
	t.resolved_at, t.created_by, t.created_at, t.updated_at,
	COALESCE(ur.first_name || ' ' || ur.last_name, ''),
	COALESCE(p.name, ''), COALESCE(t.color, ''),
	COALESCE((
		SELECT json_agg(json_build_object('id', au.id::text, 'name', au.first_name || ' ' || au.last_name) ORDER BY au.last_name, au.first_name)
		FROM task_assignees ta JOIN users au ON au.id = ta.user_id WHERE ta.task_id = t.id
	), '[]'::json)`

const taskJoins = `
	FROM tasks t
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
		UPDATE tasks SET
			title=COALESCE(NULLIF($1,''), title),
			description=CASE WHEN $1 = '' THEN description ELSE $2 END,
			priority=COALESCE(NULLIF($3,''), priority),
			due_date=COALESCE($4, due_date),
			start_date=COALESCE($5, start_date),
			project_id=CASE WHEN $6::uuid IS NOT NULL OR $8 THEN $6 ELSE project_id END,
			color=COALESCE(NULLIF($9,''), color),
			updated_at=NOW()
		WHERE id=$7`,
		in.Title, in.Description, in.Priority, nullDate(in.DueDate), nullDate(in.StartDate), in.ProjectID, id, in.ClearProject, in.Color)
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
