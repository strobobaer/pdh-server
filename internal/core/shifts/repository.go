package shifts

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ── Schichtmodelle ───────────────────────────────────────────

func (r *Repository) CreateModel(ctx context.Context, m *ShiftModel) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO shift_models (id, name, description, active)
		 VALUES (gen_random_uuid(), $1, $2, true)
		 RETURNING id, created_at`,
		m.Name, m.Description,
	).Scan(&m.ID, &m.CreatedAt)
}

func (r *Repository) ListModels(ctx context.Context) ([]*ShiftModel, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, name, description, active, created_at
		 FROM shift_models WHERE active = true ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []*ShiftModel
	for rows.Next() {
		m := &ShiftModel{}
		rows.Scan(&m.ID, &m.Name, &m.Description, &m.Active, &m.CreatedAt)
		models = append(models, m)
	}
	return models, nil
}

// ── Schichtdefinitionen ──────────────────────────────────────

func (r *Repository) CreateShift(ctx context.Context, s *ShiftDefinition) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO shift_definitions (id, model_id, name, short_name, start_time, end_time, color, is_night)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		s.ModelID, s.Name, s.ShortName, s.StartTime, s.EndTime, s.Color, s.IsNight,
	).Scan(&s.ID)
}

func (r *Repository) ListShifts(ctx context.Context, modelID string) ([]*ShiftDefinition, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, model_id, name, short_name, start_time, end_time, color, is_night
		 FROM shift_definitions WHERE model_id = $1 ORDER BY start_time`,
		modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shifts []*ShiftDefinition
	for rows.Next() {
		s := &ShiftDefinition{}
		rows.Scan(&s.ID, &s.ModelID, &s.Name, &s.ShortName, &s.StartTime, &s.EndTime, &s.Color, &s.IsNight)
		shifts = append(shifts, s)
	}
	return shifts, nil
}

// ── Zuweisungen ──────────────────────────────────────────────

func (r *Repository) Assign(ctx context.Context, a *ShiftAssignment) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO shift_assignments (id, user_id, shift_id, date, note, created_by)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, date) DO UPDATE
		 SET shift_id=$2, note=$4, created_by=$5
		 RETURNING id, created_at`,
		a.UserID, a.ShiftID, a.Date, a.Note, a.CreatedBy,
	).Scan(&a.ID, &a.CreatedAt)
}

func (r *Repository) GetWeekPlan(ctx context.Context, weekStart, weekEnd string) ([]*ShiftAssignment, error) {
	rows, err := r.db.Query(ctx,
		`SELECT sa.id, sa.user_id, sa.shift_id, sa.date::text, sa.note,
		        sd.name, sd.short_name, sd.start_time::text, sd.end_time::text, sd.color,
		        u.first_name || ' ' || u.last_name
		 FROM shift_assignments sa
		 JOIN shift_definitions sd ON sa.shift_id = sd.id
		 JOIN users u ON sa.user_id = u.id
		 WHERE sa.date BETWEEN $1 AND $2
		 ORDER BY sa.date, u.last_name`,
		weekStart, weekEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var assignments []*ShiftAssignment
	for rows.Next() {
		a := &ShiftAssignment{}
		rows.Scan(&a.ID, &a.UserID, &a.ShiftID, &a.Date, &a.Note,
			&a.ShiftName, &a.ShortName, &a.StartTime, &a.EndTime, &a.Color, &a.UserName)
		assignments = append(assignments, a)
	}
	return assignments, nil
}

func (r *Repository) GetUserShifts(ctx context.Context, userID, from, to string) ([]*ShiftAssignment, error) {
	rows, err := r.db.Query(ctx,
		`SELECT sa.id, sa.user_id, sa.shift_id, sa.date::text, sa.note,
		        sd.name, sd.short_name, sd.start_time::text, sd.end_time::text, sd.color
		 FROM shift_assignments sa
		 JOIN shift_definitions sd ON sa.shift_id = sd.id
		 WHERE sa.user_id = $1 AND sa.date BETWEEN $2 AND $3
		 ORDER BY sa.date`,
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var assignments []*ShiftAssignment
	for rows.Next() {
		a := &ShiftAssignment{}
		rows.Scan(&a.ID, &a.UserID, &a.ShiftID, &a.Date, &a.Note,
			&a.ShiftName, &a.ShortName, &a.StartTime, &a.EndTime, &a.Color)
		assignments = append(assignments, a)
	}
	return assignments, nil
}

func (r *Repository) DeleteAssignment(ctx context.Context, userID, date string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM shift_assignments WHERE user_id=$1 AND date=$2`,
		userID, date)
	return err
}

// ── Abwesenheiten ────────────────────────────────────────────

func (r *Repository) CreateAbsence(ctx context.Context, a *Absence) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO absences (id, user_id, type, status, start_date, end_date, days, note)
		 VALUES (gen_random_uuid(), $1, $2, 'pending', $3, $4, $5, $6)
		 RETURNING id, created_at`,
		a.UserID, a.Type, a.StartDate, a.EndDate, a.Days, a.Note,
	).Scan(&a.ID, &a.CreatedAt)
}

func (r *Repository) ListAbsences(ctx context.Context, userID string, status AbsenceStatus) ([]*Absence, error) {
	query := `SELECT a.id, a.user_id, a.type, a.status, a.start_date::text,
		a.end_date::text, a.days, a.note, a.approved_by, a.created_at,
		u.first_name || ' ' || u.last_name
		FROM absences a JOIN users u ON a.user_id = u.id WHERE 1=1`
	args := []interface{}{}
	i := 1
	if userID != "" {
		query += fmt.Sprintf(" AND a.user_id = $%d", i)
		args = append(args, userID)
		i++
	}
	if status != "" {
		query += fmt.Sprintf(" AND a.status = $%d", i)
		args = append(args, status)
	}
	query += " ORDER BY a.start_date DESC"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var absences []*Absence
	for rows.Next() {
		a := &Absence{}
		rows.Scan(&a.ID, &a.UserID, &a.Type, &a.Status, &a.StartDate,
			&a.EndDate, &a.Days, &a.Note, &a.ApprovedBy, &a.CreatedAt, &a.UserName)
		absences = append(absences, a)
	}
	return absences, nil
}

func (r *Repository) ApproveAbsence(ctx context.Context, id, approvedBy string, approved bool) error {
	status := AbsenceApproved
	if !approved {
		status = AbsenceRejected
	}
	_, err := r.db.Exec(ctx,
		`UPDATE absences SET status=$1, approved_by=$2 WHERE id=$3`,
		status, approvedBy, id)
	return err
}
