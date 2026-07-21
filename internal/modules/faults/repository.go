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
			infrastructure_id, assigned_to, responsible_to, created_by, detected_at, cost_center_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'detected', $5, $6, $7, $8, NOW(), $9)
		RETURNING id, status, detected_at, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		f.Title, f.Description, symptoms, f.Severity,
		f.InfrastructureID, f.AssignedTo, f.ResponsibleTo, f.CreatedBy, f.CostCenterID,
	).Scan(&f.ID, &f.Status, &f.DetectedAt, &f.CreatedAt, &f.UpdatedAt)
}

func (r *Repository) CreateTicketFromFault(ctx context.Context, f *Fault, priority string) (string, error) {
	description := f.Description
	if len(f.Symptoms) > 0 {
		b, _ := json.MarshalIndent(f.Symptoms, "", "  ")
		description += "\n\nSymptome:\n" + string(b)
	}
	description += "\n\nQuelle: PDH-Störung " + f.ID

	var id string
	err := r.db.QueryRow(ctx, `
		INSERT INTO tickets (id, title, description, priority, status, assigned_to, responsible_to, created_by, infrastructure_id, cost_center_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open', $4, $5, $6, $7, $8)
		RETURNING id`,
		"Störung: "+f.Title, description, priority, f.AssignedTo, f.ResponsibleTo, f.CreatedBy, f.InfrastructureID, f.CostCenterID,
	).Scan(&id)
	if err == nil {
		_, _ = r.db.Exec(ctx, `
			INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
			VALUES ('fault', $1, 'ticket', 'ticket_id', $2, $3, 'Ticket aus Störung erstellt')`,
			f.ID, id, f.CreatedBy)
	}
	return id, err
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Fault, error) {
	f := &Fault{}
	var symptoms []byte
	query := `SELECT fl.id, fl.title, fl.description, fl.symptoms, fl.severity, fl.status,
		fl.infrastructure_id, fl.assigned_to, fl.responsible_to, fl.created_by, fl.record_image_attachment_id, fl.resolution, fl.root_cause,
		fl.detected_at, fl.resolved_at, fl.archived_at, fl.created_at, fl.updated_at,
		fl.cost_center_id, COALESCE(cc.number,''), COALESCE(cc.name,'')
		FROM faults fl
		LEFT JOIN cost_centers cc ON fl.cost_center_id = cc.id
		WHERE fl.id = $1`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&f.ID, &f.Title, &f.Description, &symptoms, &f.Severity, &f.Status,
		&f.InfrastructureID, &f.AssignedTo, &f.ResponsibleTo, &f.CreatedBy, &f.RecordImageID,
		&f.Resolution, &f.RootCause,
		&f.DetectedAt, &f.ResolvedAt, &f.ArchivedAt, &f.CreatedAt, &f.UpdatedAt,
		&f.CostCenterID, &f.CostCenterNumber, &f.CostCenterName,
	)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(symptoms, &f.Symptoms)
	return f, nil
}

func (r *Repository) List(ctx context.Context, status FaultStatus) ([]*Fault, error) {
	query := `SELECT fl.id, fl.title, fl.description, fl.symptoms, fl.severity, fl.status,
		fl.infrastructure_id, fl.assigned_to, fl.responsible_to, fl.created_by, fl.detected_at, fl.archived_at, fl.created_at, fl.updated_at,
		fl.cost_center_id, COALESCE(cc.number,''), COALESCE(cc.name,'')
		FROM faults fl
		LEFT JOIN cost_centers cc ON fl.cost_center_id = cc.id`
	args := []interface{}{}
	if status == FaultStatus("archive") {
		query += " WHERE fl.archived_at IS NOT NULL"
	} else if status != "" {
		query += " WHERE fl.status = $1 AND fl.archived_at IS NULL"
		args = append(args, status)
	} else {
		query += " WHERE fl.archived_at IS NULL"
	}
	query += " ORDER BY CASE fl.severity WHEN 'critical' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END, fl.detected_at DESC"

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
			&f.InfrastructureID, &f.AssignedTo, &f.ResponsibleTo, &f.CreatedBy,
			&f.DetectedAt, &f.ArchivedAt, &f.CreatedAt, &f.UpdatedAt,
			&f.CostCenterID, &f.CostCenterNumber, &f.CostCenterName,
		)
		if err != nil {
			return nil, err
		}
		json.Unmarshal(symptoms, &f.Symptoms)
		faults = append(faults, f)
	}
	return faults, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, id string, status FaultStatus, userID string) error {
	query := `UPDATE faults SET status=$1, updated_at=NOW()`
	if status == StatusResolved || status == StatusClosed {
		query += `, resolved_at=COALESCE(resolved_at,NOW()), archived_at=COALESCE(archived_at,NOW()), archived_by=$3`
	}
	query += ` WHERE id=$2`
	var err error
	if status == StatusResolved || status == StatusClosed {
		_, err = r.db.Exec(ctx, query, status, id, userID)
	} else {
		_, err = r.db.Exec(ctx, query, status, id)
	}
	if err == nil && userID != "" {
		_, _ = r.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
			VALUES ('fault', $1, 'status', 'status', $2, $3, 'Status geändert')`, id, string(status), userID)
	}
	return err
}

// UpdateCostCenter setzt/ändert die Kostenstelle einer bestehenden Störung.
func (r *Repository) UpdateCostCenter(ctx context.Context, id string, costCenterID *string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE faults SET cost_center_id=$1, updated_at=NOW() WHERE id=$2`,
		costCenterID, id)
	return err
}

func (r *Repository) Resolve(ctx context.Context, id, resolution, rootCause, userID string, noPartsNeeded bool) error {
	_, err := r.db.Exec(ctx,
		`UPDATE faults SET status='resolved', resolution=$1, root_cause=$2, no_parts_needed=$5,
		resolved_at=NOW(), archived_at=COALESCE(archived_at,NOW()), archived_by=$4, updated_at=NOW() WHERE id=$3`,
		resolution, rootCause, id, userID, noPartsNeeded)
	if err == nil {
		_, _ = r.db.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
			VALUES ('fault', $1, 'resolved', 'status', 'resolved', $2, 'Störung gelöst und archiviert')`, id, userID)
	}
	return err
}

// CountActions: Anzahl erfasster Maßnahmen (fault_actions) für eine Störung.
func (r *Repository) CountActions(ctx context.Context, faultID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM fault_actions WHERE fault_id=$1`, faultID).Scan(&n)
	return n, err
}

// CountPartsUsage: Anzahl mit dieser Störung verknüpfter Lagerbuchungen.
func (r *Repository) CountPartsUsage(ctx context.Context, faultID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM stock_movements WHERE fault_id=$1`, faultID).Scan(&n)
	return n, err
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
