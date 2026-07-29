package faults

import "time"

type Severity string
type FaultStatus string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"

	StatusDetected   FaultStatus = "detected"
	StatusAnalyzing  FaultStatus = "analyzing"
	StatusInProgress FaultStatus = "in_progress"
	StatusResolved   FaultStatus = "resolved"
	StatusClosed     FaultStatus = "closed"
)

// Störung
type Fault struct {
	ID               string      `json:"id"`
	Title            string      `json:"title"`
	Description      string      `json:"description"`
	Symptoms         []string    `json:"symptoms"`
	Severity         Severity    `json:"severity"`
	Status           FaultStatus `json:"status"`
	InfrastructureID *string     `json:"infrastructure_id,omitempty"`
	AssignedTo       *string     `json:"assigned_to,omitempty"`
	ResponsibleTo    *string     `json:"responsible_to,omitempty"`
	CreatedBy        string      `json:"created_by"`
	RecordImageID    *string     `json:"record_image_attachment_id,omitempty"`
	Resolution       *string     `json:"resolution,omitempty"`
	RootCause        *string     `json:"root_cause,omitempty"`
	DetectedAt       time.Time   `json:"detected_at"`
	DueDate          *time.Time  `json:"due_date,omitempty"`
	ResolvedAt       *time.Time  `json:"resolved_at,omitempty"`
	ArchivedAt       *time.Time  `json:"archived_at,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`

	// Kostenstelle (eigenständig, unabhängig von der Infrastruktur)
	CostCenterID     *string `json:"cost_center_id,omitempty"`
	CostCenterNumber string  `json:"cost_center_number,omitempty"`
	CostCenterName   string  `json:"cost_center_name,omitempty"`

	// Pflichtangaben beim Lösen: Maßnahmen-Verlauf + Ersatzteilverwendung
	NoPartsNeeded bool `json:"no_parts_needed,omitempty"`
}

// Copilot-Analyse
type CopilotAnalysis struct {
	ID             string             `json:"id"`
	FaultID        string             `json:"fault_id"`
	Summary        string             `json:"summary"`
	PossibleCauses []string           `json:"possible_causes"`
	Steps          []TroubleshootStep `json:"steps"`
	SimilarFaults  []SimilarFault     `json:"similar_faults"`
	Confidence     float64            `json:"confidence"`
	CreatedAt      time.Time          `json:"created_at"`
}

type TroubleshootStep struct {
	Order       int    `json:"order"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	Done        bool   `json:"done"`
}

type SimilarFault struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Resolution string  `json:"resolution"`
	Similarity float64 `json:"similarity"`
}

// Wissenseintrag
type KnowledgeEntry struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Symptoms   []string  `json:"symptoms"`
	Solution   string    `json:"solution"`
	Category   string    `json:"category"`
	Tags       []string  `json:"tags"`
	CreatedBy  string    `json:"created_by"`
	UsageCount int       `json:"usage_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// Eingaben
type CreateFaultInput struct {
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Symptoms         []string `json:"symptoms"`
	Severity         Severity `json:"severity"`
	InfrastructureID *string  `json:"infrastructure_id,omitempty"`
	AssignedTo       *string  `json:"assigned_to,omitempty"`
	ResponsibleTo    *string  `json:"responsible_to,omitempty"`
	CostCenterID     *string  `json:"cost_center_id,omitempty"`
}

type ResolveInput struct {
	Resolution    string `json:"resolution"`
	RootCause     string `json:"root_cause"`
	NoPartsNeeded bool   `json:"no_parts_needed"`
}
