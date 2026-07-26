package projects

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusPlanning  Status = "planning"
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
)

type Project struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description,omitempty"`
	StartDate        *time.Time `json:"start_date,omitempty"`
	EndDate          *time.Time `json:"end_date,omitempty"`
	Status           Status     `json:"status"`
	ResponsibleTo    *string    `json:"responsible_to,omitempty"`
	InfrastructureID *string    `json:"infrastructure_id,omitempty"`
	CostCenterID     *string    `json:"cost_center_id,omitempty"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`

	// Joined
	ResponsibleName  string `json:"responsible_name,omitempty"`
	InfraName        string `json:"infra_name,omitempty"`
	CostCenterNumber string `json:"cost_center_number,omitempty"`
	CostCenterName   string `json:"cost_center_name,omitempty"`
	TaskCount        int    `json:"task_count,omitempty"`
}

type CreateProjectInput struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	StartDate        string  `json:"start_date"`
	EndDate          string  `json:"end_date"`
	ResponsibleTo    *string `json:"responsible_to,omitempty"`
	InfrastructureID *string `json:"infrastructure_id,omitempty"`
	CostCenterID     *string `json:"cost_center_id,omitempty"`
}

type UpdateProjectInput struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	StartDate        string  `json:"start_date"`
	EndDate          string  `json:"end_date"`
	Status           Status  `json:"status"`
	ResponsibleTo    *string `json:"responsible_to,omitempty"`
	InfrastructureID *string `json:"infrastructure_id,omitempty"`
	CostCenterID     *string `json:"cost_center_id,omitempty"`
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, p *Project) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO projects (id, name, description, start_date, end_date, responsible_to,
			infrastructure_id, cost_center_id, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, status, created_at, updated_at`,
		p.Name, p.Description, p.StartDate, p.EndDate, p.ResponsibleTo,
		p.InfrastructureID, p.CostCenterID, p.CreatedBy,
	).Scan(&p.ID, &p.Status, &p.CreatedAt, &p.UpdatedAt)
}

const selectProjectColumns = `
	p.id, p.name, COALESCE(p.description,''), p.start_date, p.end_date, p.status,
	p.responsible_to, p.infrastructure_id, p.cost_center_id, p.created_by, p.created_at, p.updated_at,
	COALESCE(u.first_name || ' ' || u.last_name, ''), COALESCE(i.name, ''),
	COALESCE(cc.number, ''), COALESCE(cc.name, ''),
	(SELECT COUNT(*) FROM tasks t WHERE t.project_id = p.id)`

const projectJoins = `
	FROM projects p
	LEFT JOIN users u ON p.responsible_to = u.id
	LEFT JOIN infrastructure i ON p.infrastructure_id = i.id
	LEFT JOIN cost_centers cc ON p.cost_center_id = cc.id`

func scanProject(row interface{ Scan(...interface{}) error }) (*Project, error) {
	p := &Project{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.StartDate, &p.EndDate, &p.Status,
		&p.ResponsibleTo, &p.InfrastructureID, &p.CostCenterID, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
		&p.ResponsibleName, &p.InfraName, &p.CostCenterNumber, &p.CostCenterName, &p.TaskCount)
	return p, err
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Project, error) {
	query := "SELECT " + selectProjectColumns + " " + projectJoins + " WHERE p.id=$1"
	return scanProject(r.db.QueryRow(ctx, query, id))
}

func (r *Repository) List(ctx context.Context, status Status) ([]*Project, error) {
	query := "SELECT " + selectProjectColumns + " " + projectJoins
	args := []interface{}{}
	if status != "" {
		query += " WHERE p.status=$1"
		args = append(args, status)
	}
	query += " ORDER BY p.start_date NULLS LAST, p.created_at DESC"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) Update(ctx context.Context, id string, in *UpdateProjectInput) error {
	_, err := r.db.Exec(ctx, `
		UPDATE projects SET name=$1, description=$2, start_date=$3, end_date=$4, status=$5,
			responsible_to=$6, infrastructure_id=$7, cost_center_id=$8, updated_at=NOW()
		WHERE id=$9`,
		in.Name, in.Description, nullDate(in.StartDate), nullDate(in.EndDate), in.Status,
		in.ResponsibleTo, in.InfrastructureID, in.CostCenterID, id)
	return err
}

func nullDate(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM projects WHERE id=$1`, id)
	return err
}
