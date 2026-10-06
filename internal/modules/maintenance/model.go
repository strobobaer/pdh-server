// Package maintenance – Wartungsmodul v2.
//
// Begriffe:
//   - Wartungsplan: was wie oft an welcher Anlage gemacht wird. Intervall
//     „alle N Tage/Wochen/Monate/Jahre“, ab Durchfuehrung oder fester Rhythmus,
//     Vorlauf in Tagen. Zu jedem aktiven Plan gibt es genau einen offenen Auftrag.
//   - Checkliste (Vorlage): wiederverwendbare Liste von Punkten (Abhaken,
//     Messwert mit Soll/Min/Max, Freitext, Bilder zur Darstellung).
//   - Plan-Checkliste: eine Vorlage am Plan mit eigenem Takt (immer oder alle
//     N Tage/Wochen/Monate/Jahre). Faellige Checklisten werden bei der
//     Durchfuehrung zu einer Liste zusammengefuehrt.
//   - Wartungsauftrag: ein Termin. Seine Punkte stehen als Schritte am Auftrag
//     (Momentaufnahme) und werden im Abschluss-Assistenten abgearbeitet.
package maintenance

import "time"

type PlanType string
type Interval string
type TaskStatus string
type Priority string

const (
	PlanPreventive  PlanType = "preventive"  // Vorbeugend
	PlanInspection  PlanType = "inspection"  // Inspektion
	PlanCalibration PlanType = "calibration" // Kalibrierung
	PlanCleaning    PlanType = "cleaning"    // Reinigung

	// Interval: Altfeld interval_type (wird fuer andere Programmteile mitgeschrieben)
	IntervalDaily     Interval = "daily"
	IntervalWeekly    Interval = "weekly"
	IntervalMonthly   Interval = "monthly"
	IntervalQuarterly Interval = "quarterly"
	IntervalYearly    Interval = "yearly"

	TaskOpen       TaskStatus = "open"
	TaskInProgress TaskStatus = "in_progress"
	TaskPending    TaskStatus = "pending" // wartet (z. B. auf Teile, Leitstand „Warten“)
	TaskDone       TaskStatus = "done"
	TaskSkipped    TaskStatus = "skipped"

	PrioLow      Priority = "low"
	PrioMedium   Priority = "medium"
	PrioHigh     Priority = "high"
	PrioCritical Priority = "critical"
)

// Einheiten fuer Plan-Intervall und Checklisten-Takt.
const (
	UnitDay      = "day"
	UnitWeek     = "week"
	UnitMonth    = "month"
	UnitYear     = "year"
	RhythmAlways = "always" // Checkliste bei jedem Termin
)

// Berechnung des naechsten Termins (maintenance_plans.schedule_mode).
const (
	ScheduleFromCompletion = "completion" // ab Durchfuehrung: Abschluss + Intervall
	ScheduleFixed          = "fixed"      // fester Rhythmus: Faelligkeit + Intervall
)

// Anhang-Arten fuer Checklisten-Bilder.
const (
	RefChecklistItemImage   = "maint_check_item"   // Bild zur Darstellung an einem Vorlagen-Punkt
	RefChecklistResultImage = "maint_check_result" // Dokumentationsfoto an einem Schritt (id = Schritt-id)
)

// OpenStatuses: Auftrag ist noch zu erledigen.
var OpenStatuses = []string{string(TaskOpen), string(TaskInProgress), string(TaskPending)}

// MaintenancePlan: wiederkehrende Wartung an einer Anlage.
type MaintenancePlan struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Description      string     `json:"description,omitempty"`
	Type             PlanType   `json:"type"`
	InfrastructureID string     `json:"infrastructure_id"`
	IntervalUnit     string     `json:"interval_unit"`  // day | week | month | year
	IntervalCount    int        `json:"interval_count"` // alle N Einheiten
	ScheduleMode     string     `json:"schedule_mode"`  // completion | fixed
	LeadDays         int        `json:"lead_days"`      // Vorlauf: so viele Tage vor Faelligkeit „anstehend“
	EstimatedMin     int        `json:"estimated_min"`  // geplante Dauer
	Priority         Priority   `json:"priority"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	ResponsibleTo    *string    `json:"responsible_to,omitempty"`
	AssignedGroupID  *string    `json:"assigned_group_id,omitempty"`
	CostCenterID     *string    `json:"cost_center_id,omitempty"`
	Active           bool       `json:"active"`
	LastExecutedAt   *time.Time `json:"last_executed_at,omitempty"`
	NextDueAt        time.Time  `json:"next_due_at"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`

	// Altfelder (fuer andere Programmteile weiter gefuellt)
	Interval     Interval `json:"interval"`
	IntervalDays int      `json:"interval_days"`

	// Joined
	InfraName        string           `json:"infra_name,omitempty"`
	AssigneeName     string           `json:"assignee_name,omitempty"`
	ResponsibleName  string           `json:"responsible_name,omitempty"`
	GroupName        string           `json:"group_name,omitempty"`
	CostCenterNumber string           `json:"cost_center_number,omitempty"`
	CostCenterName   string           `json:"cost_center_name,omitempty"`
	OpenTaskID       string           `json:"open_task_id,omitempty"`
	Checklists       []*PlanChecklist `json:"checklists,omitempty"`
}

// MaintenanceTask: ein Wartungstermin (mit oder ohne Plan).
type MaintenanceTask struct {
	ID               string     `json:"id"`
	PlanID           *string    `json:"plan_id,omitempty"`
	Title            string     `json:"title"`
	Description      string     `json:"description,omitempty"`
	Type             PlanType   `json:"type"`
	InfrastructureID string     `json:"infrastructure_id"`
	Priority         Priority   `json:"priority"`
	Status           TaskStatus `json:"status"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	ResponsibleTo    *string    `json:"responsible_to,omitempty"`
	AssignedGroupID  *string    `json:"assigned_group_id,omitempty"`
	DueDate          time.Time  `json:"due_date"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	DurationMin      *int       `json:"duration_min,omitempty"`
	Notes            string     `json:"notes,omitempty"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	CostCenterID     *string    `json:"cost_center_id,omitempty"`

	// Joined
	InfraName        string `json:"infra_name,omitempty"`
	AssigneeName     string `json:"assignee_name,omitempty"`
	ResponsibleName  string `json:"responsible_name,omitempty"`
	GroupName        string `json:"group_name,omitempty"`
	PlanName         string `json:"plan_name,omitempty"`
	LeadDays         int    `json:"lead_days"`
	CostCenterNumber string `json:"cost_center_number,omitempty"`
	CostCenterName   string `json:"cost_center_name,omitempty"`
}

// IsOpen: noch zu erledigen (offen, in Arbeit oder wartet).
func (t *MaintenanceTask) IsOpen() bool {
	return t.Status == TaskOpen || t.Status == TaskInProgress || t.Status == TaskPending
}

// ChecklistImage: Bild an einem Vorlagen-Punkt bzw. Schritt.
type ChecklistImage struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
}

// ChecklistTemplate: wiederverwendbare Checkliste.
type ChecklistTemplate struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Active      bool                     `json:"active"`
	CreatedAt   time.Time                `json:"created_at"`
	ItemCount   int                      `json:"item_count"`
	PlanCount   int                      `json:"plan_count"`
	Items       []*ChecklistTemplateItem `json:"items,omitempty"`
}

// ChecklistTemplateItem: ein Punkt einer Checkliste.
type ChecklistTemplateItem struct {
	ID          string           `json:"id"`
	TemplateID  string           `json:"template_id"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	ItemType    string           `json:"item_type"` // checkbox | number | text
	Required    bool             `json:"required"`
	SortOrder   int              `json:"sort_order"`
	Unit        string           `json:"unit"`
	TargetValue *float64         `json:"target_value"`
	MinValue    *float64         `json:"min_value"`
	MaxValue    *float64         `json:"max_value"`
	Images      []ChecklistImage `json:"images"`
}

// PlanChecklist: Checkliste am Plan mit eigenem Takt.
type PlanChecklist struct {
	ID           string     `json:"id"`
	PlanID       string     `json:"plan_id"`
	TemplateID   string     `json:"template_id"`
	TemplateName string     `json:"template_name"`
	SortOrder    int        `json:"sort_order"`
	RhythmUnit   string     `json:"rhythm_unit"` // always | day | week | month | year
	RhythmCount  int        `json:"rhythm_count"`
	LastDoneAt   *time.Time `json:"last_done_at,omitempty"`
	NextFrom     *time.Time `json:"next_from,omitempty"` // ab wann wieder faellig (nil = immer)
	ItemCount    int        `json:"item_count"`
}

// TaskStep: ein Punkt am Auftrag (Momentaufnahme aus der Checkliste) samt Ergebnis.
type TaskStep struct {
	ID              string           `json:"id"`
	TaskID          string           `json:"task_id"`
	PlanChecklistID string           `json:"plan_checklist_id,omitempty"`
	TemplateID      string           `json:"template_id,omitempty"`
	TemplateItemID  string           `json:"template_item_id,omitempty"`
	ChecklistName   string           `json:"checklist_name"`
	SortOrder       int              `json:"sort_order"`
	Label           string           `json:"label"`
	Description     string           `json:"description"`
	ItemType        string           `json:"item_type"`
	Required        bool             `json:"required"`
	Unit            string           `json:"unit"`
	TargetValue     *float64         `json:"target_value"`
	MinValue        *float64         `json:"min_value"`
	MaxValue        *float64         `json:"max_value"`
	Value           string           `json:"value"`
	Done            bool             `json:"done"`
	InRange         *bool            `json:"in_range"`
	CheckedAt       *time.Time       `json:"checked_at,omitempty"`
	CheckedBy       string           `json:"checked_by,omitempty"` // Name
	RefImages       []ChecklistImage `json:"ref_images"`
	DocImages       []ChecklistImage `json:"doc_images"`
}

// Filled: Schritt ist erfasst (abgehakt bzw. Wert eingetragen).
func (s *TaskStep) Filled() bool {
	if s.ItemType == "checkbox" {
		return s.Done
	}
	return s.Value != ""
}

// TaskAction: durchgefuehrte Massnahme (Verlauf).
type TaskAction struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	Description   string    `json:"description"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// PendingPart: vorgemerktes/reserviertes Ersatzteil (Buchung beim Abschluss).
type PendingPart struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	PartID        string    `json:"part_id"`
	PartName      string    `json:"part_name"`
	PartNumber    string    `json:"part_number"`
	StorageNodeID string    `json:"storage_node_id"`
	StorageName   string    `json:"storage_name"`
	Qty           float64   `json:"qty"`
	Reserved      bool      `json:"reserved"` // bereits vom Lager abgebucht
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

type AddPendingPartInput struct {
	PartID        string  `json:"part_id"`
	StorageNodeID string  `json:"storage_node_id"`
	Qty           float64 `json:"qty"`
}

// PartUsage: tatsaechlich fuer den Auftrag gebuchtes Ersatzteil.
type PartUsage struct {
	ID            string    `json:"id"`
	PartID        string    `json:"part_id"`
	PartName      string    `json:"part_name"`
	PartNumber    string    `json:"part_number"`
	Qty           float64   `json:"qty"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

// ── Eingaben ─────────────────────────────────────────────────

// PlanInput: Plan anlegen oder aendern (alle Felder; nil = keine Person/Gruppe).
type PlanInput struct {
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	Type             PlanType             `json:"type"`
	InfrastructureID string               `json:"infrastructure_id"`
	IntervalUnit     string               `json:"interval_unit"`
	IntervalCount    int                  `json:"interval_count"`
	ScheduleMode     string               `json:"schedule_mode"`
	LeadDays         int                  `json:"lead_days"`
	EstimatedMin     int                  `json:"estimated_min"`
	Priority         Priority             `json:"priority"`
	AssignedTo       *string              `json:"assigned_to,omitempty"`
	ResponsibleTo    *string              `json:"responsible_to,omitempty"`
	AssignedGroupID  *string              `json:"assigned_group_id,omitempty"`
	CostCenterID     *string              `json:"cost_center_id,omitempty"`
	NextDueAt        string               `json:"next_due_at"` // JJJJ-MM-TT
	Checklists       []PlanChecklistInput `json:"checklists"`
	// Altform der API: interval (daily…yearly) statt Einheit/Anzahl
	Interval   Interval `json:"interval,omitempty"`
	FirstDueAt string   `json:"first_due_at,omitempty"`
}

// PlanChecklistInput: Vorlage am Plan mit Takt.
type PlanChecklistInput struct {
	TemplateID  string `json:"template_id"`
	RhythmUnit  string `json:"rhythm_unit"`
	RhythmCount int    `json:"rhythm_count"`
}

// CreatePlanInput: Name der bisherigen API (gleiche Felder).
type CreatePlanInput = PlanInput

type CreateTaskInput struct {
	PlanID           *string  `json:"plan_id,omitempty"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Type             PlanType `json:"type"`
	InfrastructureID string   `json:"infrastructure_id"`
	Priority         Priority `json:"priority"`
	AssignedTo       *string  `json:"assigned_to,omitempty"`
	DueDate          string   `json:"due_date"`
	CostCenterID     *string  `json:"cost_center_id,omitempty"`
	TemplateIDs      []string `json:"template_ids,omitempty"` // Checklisten fuer Auftraege ohne Plan
}

type CompleteTaskInput struct {
	Notes         string `json:"notes"`
	DurationMin   int    `json:"duration_min"`
	NoPartsNeeded bool   `json:"no_parts_needed"`
}

type UpdateTaskInput struct {
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	InfrastructureID string   `json:"infrastructure_id"`
	Priority         Priority `json:"priority"`
	DueDate          string   `json:"due_date"`
	Notes            string   `json:"notes"`
	CostCenterID     *string  `json:"cost_center_id,omitempty"`
}

// ChecklistItemInput: Punkt einer Vorlage anlegen/aendern.
type ChecklistItemInput struct {
	Label       string   `json:"label"`
	Description string   `json:"description"`
	ItemType    string   `json:"item_type"`
	Required    bool     `json:"required"`
	SortOrder   int      `json:"sort_order"`
	Unit        string   `json:"unit"`
	TargetValue *float64 `json:"target_value"`
	MinValue    *float64 `json:"min_value"`
	MaxValue    *float64 `json:"max_value"`
}
