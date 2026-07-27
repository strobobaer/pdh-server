package shifts

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Typen ────────────────────────────────────────────────────

type ShiftPlanStatus string

const (
	PlanDraft     ShiftPlanStatus = "draft"
	PlanPublished ShiftPlanStatus = "published"
)

// ShiftPlan: ein konkreter Plan fuer einen Zeitraum + Team. Solange
// Status="draft" ist, sind die zugehoerigen Zuweisungen nur fuer
// Admin/Manager sichtbar (siehe GetWeekPlanFiltered/GetUserShiftsFiltered).
type ShiftPlan struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	ModelID     string          `json:"model_id"`
	ModelName   string          `json:"model_name,omitempty"`
	StartDate   string          `json:"start_date"`
	EndDate     string          `json:"end_date"`
	Status      ShiftPlanStatus `json:"status"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	PublishedBy *string         `json:"published_by,omitempty"`
	PublishedAt *time.Time      `json:"published_at,omitempty"`
}

// RotationPattern: ein wiederkehrender Zyklus (z.B. "4 Tage: Fruh, Fruh,
// Spat, Frei"), aus dem sich konkrete Plaene generieren lassen.
type RotationPattern struct {
	ID          string               `json:"id"`
	ModelID     string               `json:"model_id"`
	Name        string               `json:"name"`
	CycleLength int                  `json:"cycle_length"`
	Active      bool                 `json:"active"`
	CreatedBy   string               `json:"created_by"`
	CreatedAt   time.Time            `json:"created_at"`
	Days        []RotationPatternDay `json:"days,omitempty"`
}

// RotationPatternDay: ein Tag im Zyklus. ShiftID = nil bedeutet "frei".
type RotationPatternDay struct {
	DayIndex  int     `json:"day_index"`
	ShiftID   *string `json:"shift_id,omitempty"`
	ShiftName string  `json:"shift_name,omitempty"`
	ShortName string  `json:"short_name,omitempty"`
	Color     string  `json:"color,omitempty"`
}

type CreatePlanInput struct {
	Name      string `json:"name"`
	ModelID   string `json:"model_id"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

type CreateRotationPatternInput struct {
	ModelID     string `json:"model_id"`
	Name        string `json:"name"`
	CycleLength int    `json:"cycle_length"`
}

type SetPatternDayInput struct {
	DayIndex int     `json:"day_index"`
	ShiftID  *string `json:"shift_id"` // nil = frei an diesem Zyklustag
}

// GeneratePlanUser: ein Mitarbeiter mit individuellem Rotations-Versatz
// (z.B. damit nicht alle am selben Tag mit "Fruhschicht" starten).
type GeneratePlanUser struct {
	UserID string `json:"user_id"`
	Offset int    `json:"offset"`
}

type GeneratePlanInput struct {
	PatternID string             `json:"pattern_id"`
	Name      string             `json:"name"`
	StartDate string             `json:"start_date"`
	EndDate   string             `json:"end_date"`
	Users     []GeneratePlanUser `json:"users"`
}

// ── Repository: Schichtplaene ────────────────────────────────

func (r *Repository) CreatePlan(ctx context.Context, p *ShiftPlan) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO shift_plans (id, name, model_id, start_date, end_date, status, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'draft', $5)
		RETURNING id, status, created_at`,
		p.Name, p.ModelID, p.StartDate, p.EndDate, p.CreatedBy,
	).Scan(&p.ID, &p.Status, &p.CreatedAt)
}

func (r *Repository) ListPlans(ctx context.Context, includeDrafts bool) ([]*ShiftPlan, error) {
	query := `
		SELECT sp.id, sp.name, sp.model_id, sm.name, sp.start_date::text, sp.end_date::text,
			sp.status, sp.created_by, sp.created_at, sp.published_by, sp.published_at
		FROM shift_plans sp
		JOIN shift_models sm ON sp.model_id = sm.id`
	if !includeDrafts {
		query += ` WHERE sp.status = 'published'`
	}
	query += ` ORDER BY sp.start_date DESC`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []*ShiftPlan
	for rows.Next() {
		p := &ShiftPlan{}
		if err := rows.Scan(&p.ID, &p.Name, &p.ModelID, &p.ModelName, &p.StartDate, &p.EndDate,
			&p.Status, &p.CreatedBy, &p.CreatedAt, &p.PublishedBy, &p.PublishedAt); err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func (r *Repository) GetPlan(ctx context.Context, id string) (*ShiftPlan, error) {
	p := &ShiftPlan{}
	err := r.db.QueryRow(ctx, `
		SELECT sp.id, sp.name, sp.model_id, sm.name, sp.start_date::text, sp.end_date::text,
			sp.status, sp.created_by, sp.created_at, sp.published_by, sp.published_at
		FROM shift_plans sp JOIN shift_models sm ON sp.model_id = sm.id
		WHERE sp.id=$1`, id,
	).Scan(&p.ID, &p.Name, &p.ModelID, &p.ModelName, &p.StartDate, &p.EndDate,
		&p.Status, &p.CreatedBy, &p.CreatedAt, &p.PublishedBy, &p.PublishedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *Repository) PublishPlan(ctx context.Context, id, publishedBy string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE shift_plans SET status='published', published_by=$1, published_at=NOW()
		WHERE id=$2 AND status='draft'`, publishedBy, id)
	return err
}

func (r *Repository) UnpublishPlan(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE shift_plans SET status='draft', published_by=NULL, published_at=NULL
		WHERE id=$1`, id)
	return err
}

func (r *Repository) DeletePlan(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM shift_plans WHERE id=$1 AND status='draft'`, id)
	return err
}

func (r *Repository) GetPlanAssignments(ctx context.Context, planID string) ([]*ShiftAssignment, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sa.id, sa.user_id, sa.shift_id, sa.date::text, sa.note,
			sd.name, sd.short_name, sd.start_time::text, sd.end_time::text, sd.color,
			u.first_name || ' ' || u.last_name
		FROM shift_assignments sa
		JOIN shift_definitions sd ON sa.shift_id = sd.id
		JOIN users u ON sa.user_id = u.id
		WHERE sa.plan_id = $1
		ORDER BY sa.date, u.last_name`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []*ShiftAssignment
	for rows.Next() {
		a := &ShiftAssignment{}
		if err := rows.Scan(&a.ID, &a.UserID, &a.ShiftID, &a.Date, &a.Note,
			&a.ShiftName, &a.ShortName, &a.StartTime, &a.EndTime, &a.Color, &a.UserName); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

// AssignWithPlan bucht/aktualisiert eine Zuweisung und verknuepft sie mit
// einem Plan (fuer manuelles Nachjustieren innerhalb eines Entwurfsplans).
func (r *Repository) AssignWithPlan(ctx context.Context, a *ShiftAssignment, planID string) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO shift_assignments (id, user_id, shift_id, date, note, created_by, plan_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, date) DO UPDATE
		SET shift_id=$2, note=$4, created_by=$5, plan_id=$6
		RETURNING id, created_at`,
		a.UserID, a.ShiftID, a.Date, a.Note, a.CreatedBy, planID,
	).Scan(&a.ID, &a.CreatedAt)
}

// ── Repository: Rotationsmuster ──────────────────────────────

func (r *Repository) CreateRotationPattern(ctx context.Context, p *RotationPattern) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO shift_rotation_patterns (id, model_id, name, cycle_length, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4)
		RETURNING id, active, created_at`,
		p.ModelID, p.Name, p.CycleLength, p.CreatedBy,
	).Scan(&p.ID, &p.Active, &p.CreatedAt)
}

func (r *Repository) ListRotationPatterns(ctx context.Context, modelID string) ([]*RotationPattern, error) {
	query := `SELECT id, model_id, name, cycle_length, active, created_at FROM shift_rotation_patterns WHERE active=true`
	args := []interface{}{}
	if modelID != "" {
		query += " AND model_id=$1"
		args = append(args, modelID)
	}
	query += " ORDER BY name"
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var patterns []*RotationPattern
	for rows.Next() {
		p := &RotationPattern{}
		if err := rows.Scan(&p.ID, &p.ModelID, &p.Name, &p.CycleLength, &p.Active, &p.CreatedAt); err != nil {
			return nil, err
		}
		patterns = append(patterns, p)
	}
	return patterns, rows.Err()
}

func (r *Repository) SetPatternDay(ctx context.Context, patternID string, dayIndex int, shiftID *string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO shift_rotation_pattern_days (id, pattern_id, day_index, shift_id)
		VALUES (gen_random_uuid(), $1, $2, $3)
		ON CONFLICT (pattern_id, day_index) DO UPDATE SET shift_id=$3`,
		patternID, dayIndex, shiftID)
	return err
}

func (r *Repository) GetPatternDays(ctx context.Context, patternID string) ([]RotationPatternDay, error) {
	rows, err := r.db.Query(ctx, `
		SELECT pd.day_index, pd.shift_id, COALESCE(sd.name,''), COALESCE(sd.short_name,''), COALESCE(sd.color,'')
		FROM shift_rotation_pattern_days pd
		LEFT JOIN shift_definitions sd ON pd.shift_id = sd.id
		WHERE pd.pattern_id=$1
		ORDER BY pd.day_index`, patternID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []RotationPatternDay
	for rows.Next() {
		d := RotationPatternDay{}
		if err := rows.Scan(&d.DayIndex, &d.ShiftID, &d.ShiftName, &d.ShortName, &d.Color); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	return days, rows.Err()
}

func (r *Repository) GetPattern(ctx context.Context, id string) (*RotationPattern, error) {
	p := &RotationPattern{}
	err := r.db.QueryRow(ctx, `
		SELECT id, model_id, name, cycle_length, active, created_at
		FROM shift_rotation_patterns WHERE id=$1`, id,
	).Scan(&p.ID, &p.ModelID, &p.Name, &p.CycleLength, &p.Active, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	days, err := r.GetPatternDays(ctx, id)
	if err != nil {
		return nil, err
	}
	p.Days = days
	return p, nil
}

// GeneratePlanFromPattern erzeugt einen neuen Entwurfsplan aus einem
// Rotationsmuster: fuer jeden Nutzer und jeden Tag im Zeitraum wird anhand
// von (Tag-Index + individueller Versatz) % Zykluslaenge die passende
// Schicht aus dem Muster uebernommen (kein Eintrag im Muster = frei).
func (r *Repository) GeneratePlanFromPattern(ctx context.Context, plan *ShiftPlan, patternID string, users []GeneratePlanUser) error {
	pattern, err := r.GetPattern(ctx, patternID)
	if err != nil {
		return fmt.Errorf("muster nicht gefunden: %w", err)
	}
	if pattern.CycleLength <= 0 {
		return fmt.Errorf("muster hat keine gültige zykluslänge")
	}

	dayShift := make(map[int]*string)
	for _, d := range pattern.Days {
		dayShift[d.DayIndex] = d.ShiftID
	}

	start, err := time.Parse("2006-01-02", plan.StartDate)
	if err != nil {
		return fmt.Errorf("ungültiges startdatum")
	}
	end, err := time.Parse("2006-01-02", plan.EndDate)
	if err != nil {
		return fmt.Errorf("ungültiges enddatum")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := tx.QueryRow(ctx, `
		INSERT INTO shift_plans (id, name, model_id, start_date, end_date, status, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'draft', $5)
		RETURNING id, status, created_at`,
		plan.Name, plan.ModelID, plan.StartDate, plan.EndDate, plan.CreatedBy,
	).Scan(&plan.ID, &plan.Status, &plan.CreatedAt); err != nil {
		return err
	}

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dayOffset := int(d.Sub(start).Hours() / 24)
		for _, u := range users {
			cycleDay := ((dayOffset+u.Offset)%pattern.CycleLength + pattern.CycleLength) % pattern.CycleLength
			shiftID, ok := dayShift[cycleDay]
			if !ok || shiftID == nil {
				continue // frei an diesem Tag - keine Zuweisung anlegen
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO shift_assignments (id, user_id, shift_id, date, created_by, plan_id)
				VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
				ON CONFLICT (user_id, date) DO UPDATE
				SET shift_id=$2, created_by=$4, plan_id=$5`,
				u.UserID, *shiftID, d.Format("2006-01-02"), plan.CreatedBy, plan.ID); err != nil {
				return err
			}
		}
	}

	return tx.Commit(ctx)
}

// UserQualInfo: Qualifikations-Kennzeichen + Telefonnummer eines Mitarbeiters.
type UserQualInfo struct {
	UserName        string
	Phone           string
	OnCallDuty      bool
	ShiftLocksmith1 bool
	ShiftLocksmith2 bool
	Sharpening      bool
	HeatingFill     bool
	ShiftLeader     bool
}

// GetUserQualifications: Kennzeichen aller aktiven Mitarbeiter, indiziert
// per User-ID.
func (r *Repository) GetUserQualifications(ctx context.Context) (map[string]*UserQualInfo, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id::text, first_name || ' ' || last_name, COALESCE(phone,''), on_call_duty, shift_locksmith_1, shift_locksmith_2,
			sharpening, heating_fill, shift_leader
		FROM users WHERE active=true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*UserQualInfo{}
	for rows.Next() {
		var id string
		q := &UserQualInfo{}
		if err := rows.Scan(&id, &q.UserName, &q.Phone, &q.OnCallDuty, &q.ShiftLocksmith1, &q.ShiftLocksmith2,
			&q.Sharpening, &q.HeatingFill, &q.ShiftLeader); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}

// ── Repository: sichtbarkeits-gefilterte Abfragen ────────────
// Fuer normale Mitarbeiter: Zuweisungen ohne Plan (plan_id IS NULL, z.B.
// manuelle Alt-Eintraege) bleiben sichtbar; Zuweisungen mit Plan nur wenn
// der Plan veroeffentlicht ist.

func (r *Repository) GetWeekPlanFiltered(ctx context.Context, weekStart, weekEnd string) ([]*ShiftAssignment, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sa.id, sa.user_id, sa.shift_id, sa.date::text, sa.note,
			sd.name, sd.short_name, sd.start_time::text, sd.end_time::text, sd.color,
			u.first_name || ' ' || u.last_name
		FROM shift_assignments sa
		JOIN shift_definitions sd ON sa.shift_id = sd.id
		JOIN users u ON sa.user_id = u.id
		LEFT JOIN shift_plans sp ON sa.plan_id = sp.id
		WHERE sa.date BETWEEN $1 AND $2
		  AND (sa.plan_id IS NULL OR sp.status = 'published')
		ORDER BY sa.date, u.last_name`,
		weekStart, weekEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []*ShiftAssignment
	for rows.Next() {
		a := &ShiftAssignment{}
		if err := rows.Scan(&a.ID, &a.UserID, &a.ShiftID, &a.Date, &a.Note,
			&a.ShiftName, &a.ShortName, &a.StartTime, &a.EndTime, &a.Color, &a.UserName); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

func (r *Repository) GetUserShiftsFiltered(ctx context.Context, userID, from, to string) ([]*ShiftAssignment, error) {
	rows, err := r.db.Query(ctx, `
		SELECT sa.id, sa.user_id, sa.shift_id, sa.date::text, sa.note,
			sd.name, sd.short_name, sd.start_time::text, sd.end_time::text, sd.color
		FROM shift_assignments sa
		JOIN shift_definitions sd ON sa.shift_id = sd.id
		LEFT JOIN shift_plans sp ON sa.plan_id = sp.id
		WHERE sa.user_id = $1 AND sa.date BETWEEN $2 AND $3
		  AND (sa.plan_id IS NULL OR sp.status = 'published')
		ORDER BY sa.date`,
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []*ShiftAssignment
	for rows.Next() {
		a := &ShiftAssignment{}
		if err := rows.Scan(&a.ID, &a.UserID, &a.ShiftID, &a.Date, &a.Note,
			&a.ShiftName, &a.ShortName, &a.StartTime, &a.EndTime, &a.Color); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

// ── Service ──────────────────────────────────────────────────

func (s *Service) CreatePlan(ctx context.Context, in *CreatePlanInput, userID string) (*ShiftPlan, error) {
	p := &ShiftPlan{Name: in.Name, ModelID: in.ModelID, StartDate: in.StartDate, EndDate: in.EndDate, CreatedBy: userID}
	return p, s.repo.CreatePlan(ctx, p)
}
func (s *Service) ListPlans(ctx context.Context, includeDrafts bool) ([]*ShiftPlan, error) {
	return s.repo.ListPlans(ctx, includeDrafts)
}
func (s *Service) GetPlan(ctx context.Context, id string) (*ShiftPlan, error) { return s.repo.GetPlan(ctx, id) }
func (s *Service) GetPlanAssignments(ctx context.Context, id string) ([]*ShiftAssignment, error) {
	return s.repo.GetPlanAssignments(ctx, id)
}
func (s *Service) PublishPlan(ctx context.Context, id, userID string) error {
	return s.repo.PublishPlan(ctx, id, userID)
}
func (s *Service) UnpublishPlan(ctx context.Context, id string) error { return s.repo.UnpublishPlan(ctx, id) }
func (s *Service) DeletePlan(ctx context.Context, id string) error    { return s.repo.DeletePlan(ctx, id) }
func (s *Service) AssignInPlan(ctx context.Context, in *AssignShiftInput, planID, createdBy string) (*ShiftAssignment, error) {
	a := &ShiftAssignment{UserID: in.UserID, ShiftID: in.ShiftID, Date: in.Date, Note: in.Note, CreatedBy: createdBy}
	return a, s.repo.AssignWithPlan(ctx, a, planID)
}

func (s *Service) CreateRotationPattern(ctx context.Context, in *CreateRotationPatternInput, userID string) (*RotationPattern, error) {
	if in.CycleLength <= 0 {
		return nil, fmt.Errorf("zykluslänge muss größer als 0 sein")
	}
	p := &RotationPattern{ModelID: in.ModelID, Name: in.Name, CycleLength: in.CycleLength, CreatedBy: userID}
	return p, s.repo.CreateRotationPattern(ctx, p)
}
func (s *Service) ListRotationPatterns(ctx context.Context, modelID string) ([]*RotationPattern, error) {
	return s.repo.ListRotationPatterns(ctx, modelID)
}
func (s *Service) GetPattern(ctx context.Context, id string) (*RotationPattern, error) {
	return s.repo.GetPattern(ctx, id)
}
func (s *Service) SetPatternDay(ctx context.Context, patternID string, in *SetPatternDayInput) error {
	return s.repo.SetPatternDay(ctx, patternID, in.DayIndex, in.ShiftID)
}

func (s *Service) GeneratePlan(ctx context.Context, in *GeneratePlanInput, userID string) (*ShiftPlan, error) {
	if len(in.Users) == 0 {
		return nil, fmt.Errorf("mindestens ein mitarbeiter ist pflicht")
	}
	pattern, err := s.repo.GetPattern(ctx, in.PatternID)
	if err != nil {
		return nil, fmt.Errorf("muster nicht gefunden")
	}
	plan := &ShiftPlan{Name: in.Name, ModelID: pattern.ModelID, StartDate: in.StartDate, EndDate: in.EndDate, CreatedBy: userID}
	if err := s.repo.GeneratePlanFromPattern(ctx, plan, in.PatternID, in.Users); err != nil {
		return nil, err
	}
	return plan, nil
}

// GetWeekPlanForRole liefert die Wochenuebersicht - Planer/Admin sehen auch
// Entwuerfe, alle anderen nur veroeffentlichte Plaene (bzw. planlose
// Alt-Eintraege).
func (s *Service) GetWeekPlanForRole(ctx context.Context, weekStart string, isPlanner bool) (*WeekPlan, error) {
	t, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return nil, err
	}
	weekDay := int(t.Weekday())
	if weekDay == 0 {
		weekDay = 7
	}
	monday := t.AddDate(0, 0, -(weekDay - 1))
	sunday := monday.AddDate(0, 0, 6)

	var assignments []*ShiftAssignment
	if isPlanner {
		assignments, err = s.repo.GetWeekPlan(ctx, monday.Format("2006-01-02"), sunday.Format("2006-01-02"))
	} else {
		assignments, err = s.repo.GetWeekPlanFiltered(ctx, monday.Format("2006-01-02"), sunday.Format("2006-01-02"))
	}
	if err != nil {
		return nil, err
	}

	quals, _ := s.repo.GetUserQualifications(ctx)
	locksmithAssignments, _ := s.repo.GetLocksmithAssignments(ctx)
	var slot1Phone, slot2Phone string
	for _, la := range locksmithAssignments {
		if la.Slot == 1 {
			slot1Phone = la.Phone
		}
		if la.Slot == 2 {
			slot2Phone = la.Phone
		}
	}

	userMap := make(map[string]*UserWeekPlan)
	for _, a := range assignments {
		if _, ok := userMap[a.UserID]; !ok {
			uwp := &UserWeekPlan{UserID: a.UserID, UserName: a.UserName, Days: make(map[string]DayEntry), TeamSortOrder: 3}
			if q, ok := quals[a.UserID]; ok {
				uwp.OnCallDuty = q.OnCallDuty
				uwp.OnCallPhone = q.Phone
				uwp.ShiftLocksmith1 = q.ShiftLocksmith1
				uwp.ShiftLocksmith2 = q.ShiftLocksmith2
				uwp.Sharpening = q.Sharpening
				uwp.HeatingFill = q.HeatingFill
				uwp.ShiftLeader = q.ShiftLeader
				if q.ShiftLocksmith1 {
					uwp.LocksmithPhone = slot1Phone
					uwp.TeamSortOrder = 1
				} else if q.ShiftLocksmith2 {
					uwp.LocksmithPhone = slot2Phone
					uwp.TeamSortOrder = 2
				}
			}
			userMap[a.UserID] = uwp
		}
		userMap[a.UserID].Days[a.Date] = DayEntry{
			ShiftName: a.ShiftName, ShortName: a.ShortName, Color: a.Color, StartTime: a.StartTime, EndTime: a.EndTime,
		}
	}
	// Alle aktiven Mitarbeiter ergaenzen, die noch keine Zuweisung diese
	// Woche haben, damit sie in der Tabelle sichtbar bleiben (z.B. um sie
	// als Schichtschlosser zuzuweisen, auch ohne bereits eine Schicht zu haben).
	for id, q := range quals {
		if _, ok := userMap[id]; ok {
			continue
		}
		uwp := &UserWeekPlan{UserID: id, UserName: q.UserName, Days: make(map[string]DayEntry), TeamSortOrder: 3}
		uwp.OnCallDuty = q.OnCallDuty
		uwp.OnCallPhone = q.Phone
		uwp.ShiftLocksmith1 = q.ShiftLocksmith1
		uwp.ShiftLocksmith2 = q.ShiftLocksmith2
		uwp.Sharpening = q.Sharpening
		uwp.HeatingFill = q.HeatingFill
		uwp.ShiftLeader = q.ShiftLeader
		if q.ShiftLocksmith1 {
			uwp.LocksmithPhone = slot1Phone
			uwp.TeamSortOrder = 1
		} else if q.ShiftLocksmith2 {
			uwp.LocksmithPhone = slot2Phone
			uwp.TeamSortOrder = 2
		}
		userMap[id] = uwp
	}

	var users []UserWeekPlan
	for _, u := range userMap {
		users = append(users, *u)
	}
	sort.SliceStable(users, func(i, j int) bool {
		if users[i].TeamSortOrder != users[j].TeamSortOrder {
			return users[i].TeamSortOrder < users[j].TeamSortOrder
		}
		return users[i].UserName < users[j].UserName
	})
	return &WeekPlan{WeekStart: monday.Format("2006-01-02"), WeekEnd: sunday.Format("2006-01-02"), Users: users}, nil
}

// ── Handler ──────────────────────────────────────────────────

func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var in CreatePlanInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	p, err := h.svc.CreatePlan(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	role, _ := r.Context().Value(middleware.RoleKey).(string)
	isPlanner := role == "admin" || role == "manager"
	plans, err := h.svc.ListPlans(r.Context(), isPlanner)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, plans)
}

func (h *Handler) GetPlan(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPlan(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	assignments, err := h.svc.GetPlanAssignments(r.Context(), p.ID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]interface{}{"plan": p, "assignments": assignments})
}

func (h *Handler) PublishPlan(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.PublishPlan(r.Context(), chi.URLParam(r, "id"), userID); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "veröffentlicht"})
}

func (h *Handler) UnpublishPlan(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.UnpublishPlan(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "zurückgezogen"})
}

func (h *Handler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeletePlan(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) AssignInPlan(w http.ResponseWriter, r *http.Request) {
	var in AssignShiftInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	a, err := h.svc.AssignInPlan(r.Context(), &in, chi.URLParam(r, "id"), userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, a)
}

func (h *Handler) CreateRotationPattern(w http.ResponseWriter, r *http.Request) {
	var in CreateRotationPatternInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	p, err := h.svc.CreateRotationPattern(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}

func (h *Handler) ListRotationPatterns(w http.ResponseWriter, r *http.Request) {
	patterns, err := h.svc.ListRotationPatterns(r.Context(), r.URL.Query().Get("model_id"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, patterns)
}

func (h *Handler) GetRotationPattern(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.GetPattern(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, 404, "nicht gefunden")
		return
	}
	response.JSON(w, 200, p)
}

func (h *Handler) SetPatternDay(w http.ResponseWriter, r *http.Request) {
	var in SetPatternDayInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.SetPatternDay(r.Context(), chi.URLParam(r, "id"), &in); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) GeneratePlan(w http.ResponseWriter, r *http.Request) {
	var in GeneratePlanInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	p, err := h.svc.GeneratePlan(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, p)
}
