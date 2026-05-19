package shifts

import "time"

// ── Schichtmodelle ───────────────────────────────────────────

type ShiftModel struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

type ShiftDefinition struct {
	ID        string `json:"id"`
	ModelID   string `json:"model_id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Color     string `json:"color"`
	IsNight   bool   `json:"is_night"`
}

// ── Schichtzuweisung ─────────────────────────────────────────

type ShiftAssignment struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ShiftID   string    `json:"shift_id"`
	Date      string    `json:"date"`
	Note      string    `json:"note,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`

	// Joined fields
	ShiftName string `json:"shift_name,omitempty"`
	ShortName string `json:"short_name,omitempty"`
	StartTime string `json:"start_time,omitempty"`
	EndTime   string `json:"end_time,omitempty"`
	Color     string `json:"color,omitempty"`
	UserName  string `json:"user_name,omitempty"`
}

// ── Abwesenheit ──────────────────────────────────────────────

type AbsenceType string
type AbsenceStatus string

const (
	AbsenceVacation AbsenceType = "vacation"
	AbsenceSick     AbsenceType = "sick"
	AbsenceTraining AbsenceType = "training"
	AbsenceOther    AbsenceType = "other"

	AbsencePending  AbsenceStatus = "pending"
	AbsenceApproved AbsenceStatus = "approved"
	AbsenceRejected AbsenceStatus = "rejected"
)

type Absence struct {
	ID         string        `json:"id"`
	UserID     string        `json:"user_id"`
	Type       AbsenceType   `json:"type"`
	Status     AbsenceStatus `json:"status"`
	StartDate  string        `json:"start_date"`
	EndDate    string        `json:"end_date"`
	Days       int           `json:"days"`
	Note       string        `json:"note,omitempty"`
	ApprovedBy *string       `json:"approved_by,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`

	UserName string `json:"user_name,omitempty"`
}

// ── Schichtplan (Wochenübersicht) ────────────────────────────

type WeekPlan struct {
	WeekStart string         `json:"week_start"`
	WeekEnd   string         `json:"week_end"`
	Users     []UserWeekPlan `json:"users"`
}

type UserWeekPlan struct {
	UserID   string              `json:"user_id"`
	UserName string              `json:"user_name"`
	Days     map[string]DayEntry `json:"days"`
}

type DayEntry struct {
	ShiftName string `json:"shift_name,omitempty"`
	ShortName string `json:"short_name,omitempty"`
	Color     string `json:"color,omitempty"`
	StartTime string `json:"start_time,omitempty"`
	EndTime   string `json:"end_time,omitempty"`
	Absence   string `json:"absence,omitempty"`
}

// ── Inputs ───────────────────────────────────────────────────

type CreateModelInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CreateShiftInput struct {
	ModelID   string `json:"model_id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Color     string `json:"color"`
	IsNight   bool   `json:"is_night"`
}

type AssignShiftInput struct {
	UserID  string `json:"user_id"`
	ShiftID string `json:"shift_id"`
	Date    string `json:"date"`
	Note    string `json:"note"`
}

type BulkAssignInput struct {
	Assignments []AssignShiftInput `json:"assignments"`
}

type CreateAbsenceInput struct {
	UserID    string      `json:"user_id"`
	Type      AbsenceType `json:"type"`
	StartDate string      `json:"start_date"`
	EndDate   string      `json:"end_date"`
	Note      string      `json:"note"`
}
