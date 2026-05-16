package faults

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, f *Fault) error {
	symptoms, _ := json.Marshal(f.Symptoms)
	query := `
		INSERT INTO faults (id, title, description, symptoms, severity, status,
			infrastructure_id, assigned_to, created_by, detected_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'detected', $5, $6, $7, NOW())
		RETURNING id, status, detected_at, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		f.Title, f.Description, symptoms, f.Severity,
		f.InfrastructureID, f.AssignedTo, f.CreatedBy,
	).Scan(&f.ID, &f.Status, &f.DetectedAt, &f.CreatedAt, &f.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Fault, error) {
	f := &Fault{}
	var symptoms []byte
	query := `SELECT id, title, description, symptoms, severity, status,
		infrastructure_id, assigned_to, created_by, resolution, root_cause,
		detected_at, resolved_at, created_at, updated_at
		FROM faults WHERE id = $1`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&f.ID, &f.Title, &f.Description, &symptoms, &f.Severity, &f.Status,
		&f.InfrastructureID, &f.AssignedTo, &f.CreatedBy,
		&f.Resolution, &f.RootCause,
		&f.DetectedAt, &f.ResolvedAt, &f.CreatedAt, &f.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(symptoms, &f.Symptoms)
	return f, nil
}

func (r *Repository) List(ctx context.Context, status FaultStatus) ([]*Fault, error) {
	query := `SELECT id, title, description, symptoms, severity, status,
		infrastructure_id, assigned_to, created_by, detected_at, created_at, updated_at
		FROM faults`
	args := []interface{}{}
	if status != "" {
		query += " WHERE status = $1"
		args = append(args, status)
	}
	query += " ORDER BY CASE severity WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, detected_at DESC"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var faults []*Fault
	for rows.Next() {
		f := &Fault{}
		var symptoms []byte
		err := rows.Scan(
			&f.ID, &f.Title, &f.Description, &symptoms, &f.Severity, &f.Status,
			&f.InfrastructureID, &f.AssignedTo, &f.CreatedBy,
			&f.DetectedAt, &f.CreatedAt, &f.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		json.Unmarshal(symptoms, &f.Symptoms)
		faults = append(faults, f)
	}
	return faults, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status FaultStatus) error {
	_, err := r.db.Exec(ctx,
		`UPDATE faults SET status=$1, updated_at=NOW() WHERE id=$2`, status, id)
	return err
}

func (r *Repository) Resolve(ctx context.Context, id, resolution, rootCause string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE faults SET status='resolved', resolution=$1, root_cause=$2,
		resolved_at=NOW(), updated_at=NOW() WHERE id=$3`,
		resolution, rootCause, id)
	return err
}

func (r *Repository) SaveAnalysis(ctx context.Context, a *CopilotAnalysis) error {
	steps, _ := json.Marshal(a.Steps)
	causes, _ := json.Marshal(a.PossibleCauses)
	similar, _ := json.Marshal(a.SimilarFaults)
	query := `
		INSERT INTO fault_analyses (id, fault_id, summary, possible_causes, steps, similar_faults, confidence)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
		ON CONFLICT (fault_id) DO UPDATE
		SET summary=$2, possible_causes=$3, steps=$4, similar_faults=$5,
			confidence=$6, created_at=NOW()
		RETURNING id, created_at`
	return r.db.QueryRow(ctx, query,
		a.FaultID, a.Summary, causes, steps, similar, a.Confidence,
	).Scan(&a.ID, &a.CreatedAt)
}

func (r *Repository) GetAnalysis(ctx context.Context, faultID string) (*CopilotAnalysis, error) {
	a := &CopilotAnalysis{}
	var steps, causes, similar []byte
	query := `SELECT id, fault_id, summary, possible_causes, steps,
		similar_faults, confidence, created_at
		FROM fault_analyses WHERE fault_id = $1`
	err := r.db.QueryRow(ctx, query, faultID).Scan(
		&a.ID, &a.FaultID, &a.Summary, &causes, &steps, &similar,
		&a.Confidence, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(steps, &a.Steps)
	json.Unmarshal(causes, &a.PossibleCauses)
	json.Unmarshal(similar, &a.SimilarFaults)
	return a, nil
}

func (r *Repository) GetResolvedSimilar(ctx context.Context, keywords []string) ([]*Fault, error) {
	query := `SELECT id, title, resolution, symptoms FROM faults
		WHERE status = 'resolved' AND resolution IS NOT NULL
		ORDER BY resolved_at DESC LIMIT 10`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var faults []*Fault
	for rows.Next() {
		f := &Fault{}
		var symptoms []byte
		rows.Scan(&f.ID, &f.Title, &f.Resolution, &symptoms)
		json.Unmarshal(symptoms, &f.Symptoms)
		faults = append(faults, f)
	}
	return faults, nil
}
