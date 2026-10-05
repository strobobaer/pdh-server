package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"pdh"
	"pdh/internal/core/infrastructure"
	"pdh/internal/core/mqttbroker"
	"pdh/internal/core/mqttimport"
	"pdh/internal/core/rbac"
	"pdh/internal/core/scheduler"
	"pdh/internal/core/shifts"
	"pdh/internal/core/storage"
	"pdh/internal/core/users"
	"pdh/internal/modules/checklists"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/inventory"
	"pdh/internal/modules/it"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/projects"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
	"pdh/internal/modules/timetracking"
	"pdh/pkg/appsettings"
	"pdh/pkg/config"
)

// ── Template-Daten ───────────────────────────────────────────

type BaseData struct {
	Title         string
	Page          string
	ContextTitle  string
	UserName      string
	UserFirstName string
	UserLastName  string
	FaultID       string

	// Auto-Logout / Systemnutzer-Override (siehe base.gohtml Script-Block)
	IsSystemUser           bool       // Session bleibt dauerhaft eingeloggt, kein Inaktivitäts-Timer
	IsOverrideSession      bool       // aktuell per Override auf einem Systemnutzer angemeldet
	IdleTimeoutMinutes     int        // globale Auto-Logout-Zeit (Minuten)
	OverrideTimeoutMinutes int        // Zeit bis zur automatischen Rückkehr zum Systemnutzer
	CanManageUsers         bool       // Benutzerverwaltung anzeigen
	CanManageRoles         bool       // Nav-Link "Rollen & Berechtigungen" anzeigen
	CanImport              bool       // Nav-Link "Import" anzeigen
	CanExport              bool       // Nav-Link "Export" anzeigen
	CanChat                bool       // Chat-Navigation, Ungelesen-Zaehler und "Im Chat teilen"
	CanBackup              bool       // Nav-Link "Datensicherung"
	CanPrinters            bool       // Nav-Link "Drucker" (printers.manage)
	CanPrint               bool       // direkt auf eingerichtete Drucker drucken (printers.use)
	CanCleanup             bool       // Nav-Link "Bereinigung"
	CanServerConfig        bool       // Nav-Link "Server-Einstellungen" (nur Admins)
	Brand                  Branding   // Name und Logos (Erscheinungsbild)
	CanEditInfra           bool       // Infrastruktur anlegen/bearbeiten (infrastructure.edit)
	CanTrainings           bool       // Schulungen & Qualifikationen verwalten (trainings.manage)
	TerminalInfraID        string     // Standort des Terminals: Infra-Picker klappt bis hierhin auf
	Look                   Appearance // Farbschema, Schrift und Groesse dieses Benutzers
	Lang                   string     // Sprache der Oberflaeche (i18n.go)
	Nav                    []navGroup // linke Navigation (nav.go)
}

// Language: Sprache fuer die Template-Funktion t (siehe bindLang).
func (b BaseData) Language() string { return b.Lang }

type DashboardData struct {
	BaseData
	Greeting       string
	DateStr        string
	WeekNumber     int
	WeekRange      string
	Stats          DashStats
	Faults         []FaultView
	MaintenanceDue []MaintenanceView
	OpenTickets    []TicketView
	WeekPlan       bool
	WeekDays       []ShiftDay
	GanttItems     []GanttItem
	Widgets        []DashboardWidgetView // persoenliche Widgets (dashboard_widgets.go)
	WidgetCatalog  []WidgetCatalogGroup
}

// GanttItem ist ein vereinheitlichter Zeitstrahl-Eintrag fürs Dashboard,
// zusammengeführt aus Aufgaben, Tickets, Wartungsaufträgen und Störungen.
type GanttItem struct {
	ID              string `json:"id"`
	RefType         string `json:"ref_type"` // "task" | "ticket" | "maintenance" | "fault"
	Title           string `json:"title"`
	StartISO        string `json:"start_iso"`      // YYYY-MM-DD
	EndISO          string `json:"end_iso"`        // YYYY-MM-DD (echt oder vorläufig)
	IsProvisional   bool   `json:"is_provisional"` // kein echtes Fälligkeitsdatum -> +30 Tage Platzhalter
	IsDone          bool   `json:"is_done"`
	IsRunning       bool   `json:"is_running"`    // in Bearbeitung
	IsUnassigned    bool   `json:"is_unassigned"` // weder Person noch Gruppe zugewiesen
	Color           string `json:"color"`
	DetailURL       string `json:"detail_url"`
	DueDateEndpoint string `json:"due_date_endpoint"` // API-Pfad zum Setzen des Fälligkeitsdatums per Drag
}

type DashStats struct {
	OpenTickets     int
	CriticalTickets int
	ActiveFaults    int
	AnalyzingFaults int
	MaintenanceDue  int
	InventoryValue  string
	LowStock        int
}

type FaultView struct {
	ID              string
	Title           string
	Description     string
	Status          string
	StatusLabel     string
	StatusClass     string
	Severity        string
	SeverityClass   string
	InfraID         string
	InfraName       string
	DetectedAgo     string
	Confidence      float64
	AssignedID      string
	ResponsibleID   string
	AssignedName    string
	ResponsibleName string
	RecordImageURL  string
	BlinkClass      string
}

type TicketView struct {
	ID               string
	Title            string
	Description      string
	InfraID          string
	InfraName        string
	Priority         string
	PriorityClass    string
	PriorityDot      string
	Status           string
	StatusLabel      string
	StatusClass      string
	CreatedAgo       string
	AssignedID       string
	ResponsibleID    string
	AssignedName     string
	ResponsibleName  string
	RecordImageURL   string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	BlinkClass       string
	DueDate          string
	DueDateISO       string
}

type UserOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type HistoryView struct {
	Action    string
	FieldName string
	OldValue  string
	NewValue  string
	Message   string
	UserName  string
	CreatedAt string
}

type RecordPeople struct {
	AssignedID      string
	ResponsibleID   string
	AssignedName    string
	ResponsibleName string
}

type MaintenanceView struct {
	ID            string
	Title         string
	InfraName     string
	EstimatedMin  int
	Priority      string
	PriorityClass string
	BlinkClass    string
}

type ShiftDay struct {
	Short string
	Label string
	Class string
}

type FaultsPageData struct {
	BaseData
	Tabs           []ListTab
	Total          int
	Open           int
	Filter         string
	Faults         []FaultView
	RecentAnalyses []AnalysisView
	Users          []UserOption
}

type AnalysisView struct {
	FaultTitle string
	Confidence float64
}

type TicketsPageData struct {
	BaseData
	Tabs            []ListTab
	Total           int
	Open            int
	Filter          string
	Tickets         []TicketView
	CriticalTickets []TicketView
	Users           []UserOption
	DefaultDueDays  int
}

type InventoryStats struct {
	Total      int
	LowStock   int
	Critical   int
	Empty      int
	TotalValue string
}

type PartView struct {
	ID               string
	PartNumber       string
	Name             string
	Manufacturer     string
	ManufacturerID   string
	ManufacturerName string
	Category         string
	Unit             string
	StockQty         string
	MinQty           string
	Price            string
	StorageLocation  string
	StoragePlace     string
	Status           string
	StatusLabel      string
	StatusClass      string
	StatusDot        string
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct {
	db               *pgxpool.Pool
	tmpl             *template.Template
	users            *users.Service
	shifts           *shifts.Service
	storage          *storage.Service
	infra            *infrastructure.Service
	tickets          *tickets.Service
	faults           *faults.Service
	maint            *maintenance.Service
	inv              *inventory.Service
	it               *it.Service
	time             *timetracking.Service
	checks           *checklists.Service
	tasks            *tasks.Service
	projects         *projects.Service
	rbac             *rbac.Service
	jwtSecret        string
	mailCfg          config.MailConfig
	chatOnce         sync.Once
	chatHubRef       *chatHub
	updateAgentURL   string
	updateAgentToken string
	buildCommit      string
	restartFn        func() // Neustart nach Aenderung der Server-Einstellungen (main.go)
	restartPending   []string
	microsoft        MicrosoftOAuthConfig
	microsoftSyncMu  sync.Mutex
	mqttBrokers      *mqttbroker.Manager
	mqttImport       *mqttimport.Manager
	exportCron       *scheduler.CronManager
	importPoll       *scheduler.IntervalManager
}

func NewHandler(
	db *pgxpool.Pool,
	tmpl *template.Template,
	u *users.Service,
	s *shifts.Service,
	st *storage.Service,
	i *infrastructure.Service,
	t *tickets.Service,
	f *faults.Service,
	m *maintenance.Service,
	inv *inventory.Service,
	itt *it.Service,
	tt *timetracking.Service,
	ch *checklists.Service,
	tk *tasks.Service,
	pj *projects.Service,
	rb *rbac.Service,
	jwtSecret string,
) *Handler {
	return &Handler{
		db:   db,
		tmpl: tmpl, users: u, shifts: s, storage: st, infra: i,
		tickets: t, faults: f, maint: m, inv: inv, it: itt, time: tt,
		checks:      ch,
		tasks:       tk,
		projects:    pj,
		rbac:        rb,
		jwtSecret:   jwtSecret,
		mqttBrokers: mqttbroker.NewManager(),
		mqttImport:  mqttimport.NewManager(),
		exportCron:  scheduler.NewCronManager(),
		importPoll:  scheduler.NewIntervalManager(),
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.authMiddleware)
	r.Use(h.LangMiddleware)
	r.Use(h.DepartmentScopeMiddleware)

	r.Get("/", h.Dashboard)
	r.Get("/assignments/new", h.AssignmentNewPage)
	r.Get("/account", h.AccountPage)
	r.Post("/account/appearance", h.AppearanceSaveWeb)
	r.Post("/account/nav-layout", h.NavLayoutSaveWeb)
	r.Post("/account/microsoft/connect", h.MicrosoftConnectStart)
	r.Post("/account/microsoft/connect-teams", h.MicrosoftTeamsConnectStart)
	r.Get("/account/microsoft/callback", h.MicrosoftConnectCallback)
	r.Post("/account/microsoft/disconnect", h.MicrosoftDisconnect)
	r.Post("/account/microsoft/calendar", h.SaveMicrosoftCalendarPreferences)
	r.Post("/account/microsoft/calendar/sync", h.SyncMicrosoftCalendarWeb)
	r.Get("/tickets", h.Tickets)
	r.Post("/tickets", h.CreateTicket)
	r.Get("/tickets/{id}", h.TicketDetail)
	r.Post("/tickets/{id}/status-web", h.TicketStatusWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/tickets/{id}/resolve-web", h.TicketResolve)
	r.Post("/tickets/{id}/infrastructure-web", h.TicketInfrastructureWeb) // war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/tickets/{id}/comment", h.TicketAddComment)
	r.Post("/tickets/{id}/time/start", h.TicketStartTime)
	r.Get("/faults", h.Faults)
	r.Post("/faults", h.CreateFault)
	r.Get("/faults/{id}", h.FaultDetail)
	r.Post("/faults/{id}/analyze", h.AnalyzeFault)
	r.Post("/faults/{id}/resolve", h.FaultResolve)
	r.Post("/faults/{id}/infrastructure-web", h.FaultInfrastructureWeb) // war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/faults/{id}/time/start", h.FaultStartTime)
	r.Get("/inventory", h.Inventory)
	r.Post("/inventory", h.CreatePart)
	r.Get("/inventory/labels", h.PartLabelsPage)
	r.Post("/inventory/labels/print", h.PartLabelsPrintWeb)
	// Druckerintegration (printers.go)
	r.Get("/admin/printers", h.PrintersPage)
	r.Post("/admin/printers", h.PrinterSaveWeb)
	r.Post("/admin/printers/{id}/delete", h.PrinterDeleteWeb)
	r.Post("/admin/printers/{id}/default", h.PrinterDefaultWeb)
	r.Post("/admin/printers/{id}/check", h.PrinterCheckWeb)
	r.Post("/admin/printers/{id}/client-check", h.PrinterClientCheckWeb)
	r.Post("/admin/printers/{id}/test", h.PrinterTestWeb)
	r.Get("/admin/printers/{id}/dymo-test", h.PrinterDymoTestWeb)
	r.Get("/inventory/{id}", h.PartDetailPage)
	r.Get("/inventory/{id}/image-search", h.PartImageSearchWeb)
	r.Post("/inventory/{id}/image", h.PartImageSetWeb)
	r.Get("/inventory/{id}/doc-search", h.PartDocSearchWeb)
	r.Post("/inventory/{id}/documents", h.PartDocTakeWeb)
	r.Post("/inventory/{id}/documents/{att}/refresh", h.PartDocRefreshWeb)
	r.Post("/inventory/{id}/master", h.PartMasterSaveWeb)
	r.Post("/inventory/{id}/book", h.PartBookWeb)
	r.Post("/inventory/book-web", h.InventoryBookWeb)
	r.Get("/maintenance", h.Maintenance)
	r.Get("/tasks", h.TasksPage)
	r.Get("/tasks/{id}", h.TaskDetail)
	r.Post("/tasks/{id}/time/start", h.TaskStartTime)
	r.Get("/projects", h.ProjectsPage)
	r.Get("/projects/{id}", h.ProjectDetail)
	// Wartungsplaene (maintenance_plans_web.go) – frueher ungeschuetzt in main.go
	r.Post("/maintenance/plans", h.MaintenancePlanCreateWeb)
	r.Post("/maintenance/plans/restore-all-web", h.MaintenancePlansRestoreAllWeb)
	r.Post("/maintenance/plans/{id}/edit-web", h.MaintenancePlanEditWeb)
	r.Delete("/maintenance/plans/{id}/delete-web", h.MaintenancePlanDeleteWeb)
	r.Post("/maintenance/plans/{id}/duplicate-web", h.MaintenancePlanDuplicateWeb)
	r.Post("/maintenance/generate", h.MaintenanceGenerate)
	r.Get("/maintenance/tasks/{id}", h.MaintenanceTaskDetail)
	r.Post("/maintenance/tasks/{id}/start-web", h.MaintenanceTaskStartWeb)
	r.Post("/maintenance/tasks/{id}/complete-web", h.MaintenanceTaskCompleteWeb)
	r.Post("/maintenance/tasks/{id}/edit-web", h.MaintenanceTaskEditWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Post("/maintenance/tasks/{id}/delete-web", h.MaintenanceTaskDeleteWeb)
	r.Post("/maintenance/tasks/{id}/time/start", h.MaintenanceTaskStartTime)
	r.Post("/maintenance/tasks/{id}/checklist", h.MaintenanceTaskChecklistWeb)
	r.Get("/shifts", h.Shifts)
	r.Get("/infrastructure", h.Infrastructure)
	r.Get("/infrastructure/{id}", h.InfraDetail)
	r.Post("/infrastructure/{id}/edit", h.InfraUpdate) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Get("/infrastructure/{id}/history", h.InfraHistoryWeb)
	r.Get("/infrastructure/{id}/history.csv", h.InfraHistoryCSV)
	r.Post("/infrastructure/{id}/comments", h.InfraCommentAddWeb)
	r.Post("/infrastructure/{id}/comments/{commentId}/edit", h.InfraCommentEditWeb)
	r.Post("/infrastructure/{id}/comments/{commentId}/pin", h.InfraCommentPinWeb)
	r.Post("/infrastructure/{id}/comments/{commentId}/delete", h.InfraCommentDeleteWeb)
	r.Post("/infrastructure", h.InfraCreate)
	r.Get("/it", h.ITPage)
	r.Post("/it", h.ITCreate)
	r.Get("/it/{id}", h.ITDetail)
	// QR-Infoseiten fuer Anlagen und IT-Assets (asset_info.go)
	r.Get("/a/labels", h.AssetLabelsPage)
	r.Post("/a/labels/print", h.AssetLabelsPrintWeb)
	r.Get("/a/{id}", h.AssetInfoPage)
	r.Post("/records/{refType}/{id}/it-asset", h.RecordITAssetWeb)
	r.Post("/it/{id}/edit-web", h.ITEditWeb)
	r.Post("/it/{id}/status-web", h.ITStatusWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Get("/storage", h.StoragePage)
	r.Get("/storage/{id}", h.StorageDetailPage)
	r.Post("/storage", h.StorageCreateRoot)
	r.Post("/storage/{id}/children-web", h.StorageAddChild)
	r.Get("/checklists", h.ChecklistsPage)
	r.Get("/shifts", h.Shifts)
	r.Post("/faults/{id}/chat-web", h.FaultChatWeb)
	r.Post("/faults/{id}/time/start", h.FaultStartTime)
	r.Post("/time/start-web", h.TimeStartWeb)
	r.Post("/time/manual-web", h.TimeManualWeb)
	r.Post("/time/{id}/stop-web", h.TimeStopWeb)
	r.Delete("/time/{id}/delete-web", h.TimeDeleteWeb)
	r.Post("/time/{id}/edit-web", h.TimeEditWeb)
	r.Post("/records/{refType}/{id}/people", h.RecordPeopleWeb) // war PUT, wird von Cloudflare/Nginx blockiert
	r.Get("/records/{refType}/{id}/group-options", h.RecordGroupOptionsWeb)
	r.Post("/records/{refType}/{id}/group", h.RecordGroupWeb) // war PUT, wird von Cloudflare/Nginx blockiert
	r.Get("/records/{refType}/{id}/parties", h.RecordPartiesWeb)
	r.Post("/records/{refType}/{id}/parties", h.RecordPartyAddWeb)
	r.Post("/records/{refType}/{id}/parties/{partyId}/delete", h.RecordPartyDeleteWeb)
	r.Post("/records/{refType}/{id}/archive", h.RecordArchiveWeb)
	r.Get("/directory", h.DirectoryPage)
	// Feldsaetze (gemeinsames System fuer alle Module)
	r.Get("/records/fields/{module}/{id}", h.RecordFieldsWeb)
	r.Get("/records/links/{module}/{id}", h.RecordLinksWeb)
	r.Get("/records/history/{module}/{id}", h.RecordHistoryWeb)
	r.Post("/records/fields/{module}/{id}", h.RecordFieldsSaveWeb)
	// Kopfleiste: Kategorien (#) + Stammdaten-Status (Sperren/Deaktivieren/Loeschvormerkung)
	r.Get("/records/meta/{module}/{id}", h.RecordMetaWeb)
	r.Post("/records/state/{module}/{id}", h.RecordStateWeb)
	r.Post("/records/categories/{module}/{id}", h.RecordCategoryToggleWeb)
	r.Get("/categories", h.CategoriesPage)
	r.Post("/categories", h.CategorySaveWeb)
	r.Post("/categories/{id}/toggle", h.CategoryToggleWeb)
	r.Get("/categories/filter/{module}", h.CategoryFilterWeb)
	// Loeschvormerkungen & Bereinigungslauf
	r.Get("/admin/cleanup", h.CleanupPage)
	r.Post("/admin/cleanup/run", h.CleanupRunWeb)
	r.Post("/admin/cleanup/{module}/{id}", h.CleanupActionWeb)
	// Datensicherung & Wiederherstellung
	// Server-Einstellungen (.env) - nur Administratoren
	r.Get("/admin/server-config", h.ServerConfigPage)
	r.Post("/admin/server-config/save", h.ServerConfigSaveWeb)
	r.Post("/admin/server-config/test", h.ServerConfigTestWeb)
	r.Post("/admin/server-config/restart", h.ServerRestartWeb)
	r.Post("/admin/server-config/import", h.ServerConfigImportWeb)
	r.Post("/admin/branding/settings", h.BrandingSettingsWeb)
	r.Post("/admin/branding/theme", h.BrandingThemeWeb)
	r.Post("/admin/branding/timeline", h.BrandingTimelineWeb)
	r.Post("/admin/branding/ha-import", h.BrandingHAImportWeb)
	r.Post("/admin/branding/ha/{id}/delete", h.BrandingHADeleteWeb)
	r.Post("/admin/branding/gallery/{id}", h.BrandingGalleryWeb)
	r.Post("/admin/branding/{slot}/upload", h.BrandingUploadWeb)
	r.Post("/admin/branding/{slot}/delete", h.BrandingDeleteWeb)
	r.Get("/admin/server-config/logs", h.ServerLogsPage)
	r.Get("/admin/server-config/logs/rows", h.ServerLogsRowsWeb)
	r.Get("/admin/server-config/logs/download", h.ServerLogsDownloadWeb)
	r.Get("/admin/backup", h.BackupPage)
	r.Post("/admin/backup/create", h.BackupCreateWeb)
	r.Post("/admin/backup/upload", h.BackupUploadWeb)
	r.Get("/admin/backup/files/{id}", h.BackupDownloadWeb)
	r.Post("/admin/backup/files/{id}/delete", h.BackupDeleteWeb)
	r.Post("/admin/backup/files/{id}/restore", h.BackupRestoreWeb)
	r.Post("/admin/backup/update-settings", h.BackupUpdateSettingsWeb)
	r.Post("/admin/backup/schedules", h.BackupScheduleSaveWeb)
	r.Post("/admin/backup/schedules/{id}/{action}", h.BackupScheduleActionWeb)
	r.Get("/core/fieldsets", h.FieldSetsAdminPage)
	r.Post("/core/fieldsets/sets", h.FieldSetSaveWeb)
	r.Post("/core/fieldsets/sets/{id}", h.FieldSetSaveWeb)
	r.Post("/core/fieldsets/sets/{id}/toggle", h.FieldSetToggleWeb)
	r.Post("/core/fieldsets/sets/{id}/fields", h.FieldDefSaveWeb)
	r.Post("/core/fieldsets/fields/{fieldId}", h.FieldDefSaveWeb)
	r.Post("/core/fieldsets/fields/{fieldId}/toggle", h.FieldDefToggleWeb)
	r.Post("/core/fieldsets/fields/{fieldId}/options", h.FieldOptionAddWeb)
	r.Post("/core/fieldsets/options/{optionId}/delete", h.FieldOptionDeleteWeb)
	// Chat & Teams
	r.Get("/help", h.HelpPage)
	r.Get("/chat", h.ChatPage)
	r.Get("/chat/stream", h.ChatStream)
	r.Get("/chat/files/{id}", h.ChatFileDownload)
	r.Get("/chat/api/bootstrap", h.ChatBootstrap)
	r.Get("/chat/api/unread", h.ChatUnread)
	r.Get("/chat/api/search", h.ChatSearch)
	r.Get("/chat/api/conversations/{id}/messages", h.ChatMessages)
	r.Post("/chat/api/conversations/{id}/messages", h.ChatSend)
	r.Get("/chat/api/conversations/{id}/files", h.ChatFilesList)
	r.Post("/chat/api/conversations/{id}/read", h.ChatRead)
	r.Post("/chat/api/conversations/{id}/typing", h.ChatTyping)
	r.Post("/chat/api/conversations/{id}/mute", h.ChatMute)
	r.Post("/chat/api/conversations/{id}/rename", h.ChatRename)
	r.Post("/chat/api/conversations/{id}/members", h.ChatAddMembers)
	r.Post("/chat/api/conversations/{id}/leave", h.ChatLeave)
	r.Post("/chat/api/channels/{id}/archive", h.ChatArchiveChannel)
	r.Post("/chat/api/messages/{id}/edit", h.ChatEdit)
	r.Post("/chat/api/messages/{id}/delete", h.ChatDelete)
	r.Post("/chat/api/messages/{id}/react", h.ChatReact)
	r.Post("/chat/api/direct", h.ChatDirect)
	r.Post("/chat/api/groups", h.ChatCreateGroup)
	r.Post("/chat/api/teams", h.ChatCreateTeam)
	r.Post("/chat/api/teams/{id}/members", h.ChatTeamAddMembers)
	r.Post("/chat/api/teams/{id}/members/{userId}/remove", h.ChatTeamRemoveMember)
	r.Post("/chat/api/teams/{id}/leave", h.ChatTeamLeave)
	r.Post("/chat/api/teams/{id}/channels", h.ChatCreateChannel)
	r.Get("/directory/purchase-report", h.PurchaseReportPage)
	r.Post("/directory/purchase-report/settings", h.PurchaseReportSettingsWeb)
	r.Post("/directory/purchase-report/send", h.PurchaseReportSendWeb)
	r.Post("/inventory/{id}/purchasing", h.PartPurchasingWeb)
	r.Get("/directory/new", h.PartnerNewPage)
	r.Post("/directory", h.PartnerCreateWeb)
	r.Get("/directory/{id}", h.PartnerDetail)
	r.Post("/directory/{id}/edit", h.PartnerUpdateWeb)
	r.Post("/directory/{id}/active", h.PartnerActiveWeb)
	r.Post("/directory/{id}/contacts", h.PartnerContactSaveWeb)
	r.Post("/directory/{id}/contacts/{contactId}/delete", h.PartnerContactDeleteWeb)
	r.Post("/directory/{id}/links", h.PartnerLinkSaveWeb)
	r.Post("/directory/{id}/links/{linkId}/delete", h.PartnerLinkDeleteWeb)
	r.Post("/directory/{id}/links/{linkId}/embed", h.PartnerLinkEmbedWeb)
	r.Get("/directory/{id}/links/{linkId}/password", h.PartnerLinkPasswordWeb)
	r.Post("/directory/{id}/assign", h.PartnerAssignWeb)
	r.Post("/directory/{id}/unassign", h.PartnerUnassignWeb)
	r.Post("/inventory/{id}/suppliers", h.PartSupplierAssignWeb)
	r.Post("/inventory/{id}/suppliers/{partnerId}/delete", h.PartSupplierRemoveWeb)
	r.Get("/users", h.Users)
	r.Get("/users/{id}", h.UserDetailPage)
	r.Post("/users/me/change-notifications", h.UserChangeNotificationsWeb)
	r.Post("/users/me/password", h.AccountPasswordWeb)
	r.Post("/users/{id}/master", h.UserMasterSaveWeb)
	r.Post("/users/{id}/private", h.UserPrivateSaveWeb)
	r.Post("/users/{id}/qualifications", h.UserQualificationSaveWeb)
	r.Post("/users/{id}/groups", h.UserGroupsSaveWeb)
	r.Get("/admin/org-units", h.OrgUnitsPage)
	r.Get("/trainings", h.TrainingsPage)
	r.Get("/suggest", h.SuggestWeb)
	r.Post("/copilot/ask", h.CopilotAskWeb)
	r.Post("/reservations/{kind}/{ref}/{id}/return", h.ReservationReturnWeb)
	r.Get("/create/options", h.CreateOptionsWeb)
	r.Get("/create/similar", h.CreateSimilarWeb)
	r.Post("/suggest/accept", h.SuggestAcceptWeb)
	r.Post("/trainings/topics", h.TrainingTopicSaveWeb)
	r.Post("/trainings/topics/{id}/delete", h.TrainingTopicDeleteWeb)
	r.Post("/trainings/sessions", h.TrainingSessionCreateWeb)
	r.Get("/trainings/sessions/{id}", h.TrainingSessionPage)
	r.Post("/trainings/sessions/{id}", h.TrainingSessionSaveWeb)
	r.Post("/trainings/sessions/{id}/participants", h.TrainingParticipantsAddWeb)
	r.Post("/trainings/sessions/{id}/participants/{uid}/remove", h.TrainingParticipantRemoveWeb)
	r.Post("/trainings/sessions/{id}/sign", h.TrainingSignWeb)
	r.Post("/trainings/sessions/{id}/archive", h.TrainingArchiveWeb)
	r.Post("/trainings/sessions/{id}/reuse", h.TrainingReuseWeb)
	r.Post("/trainings/sessions/{id}/delete", h.TrainingSessionDeleteWeb)
	r.Get("/trainings/user/{id}", h.TrainingUserFragment)
	r.Post("/admin/departments", h.DepartmentSaveWeb)
	r.Post("/admin/departments/{id}/delete", h.DepartmentDeleteWeb)
	r.Post("/admin/groups", h.GroupSaveWeb)
	r.Post("/admin/groups/{id}/delete", h.GroupDeleteWeb)
	r.Post("/users/{id}/qualifications/{qid}/delete", h.UserQualificationDeleteWeb)
	r.Post("/users/save-web", h.UserSaveWeb)
	r.Post("/users/{id}/role-web", h.UserRoleWeb) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
	r.Delete("/users/{id}/deactivate-web", h.UserDeactivateWeb)
	r.Get("/users/{id}/permissions", h.UserPermissionsWeb)
	r.Post("/users/{id}/permissions", h.UserPermissionSetWeb)
	r.Get("/time", h.TimeTracking)
	r.Get("/dashboard/w/{id}", h.DashboardWidgetWeb)
	r.Get("/complete/{type}/{id}", h.CompletionInfoWeb)
	r.Post("/complete/{type}/{id}", h.CompletionWeb)
	r.Post("/time/{id}/confirm", h.TimeConfirmWeb)
	r.Post("/dashboard/widgets", h.DashboardWidgetsSaveWeb)
	r.Post("/dashboard/w/{id}/note", h.DashboardNoteSaveWeb)

	// Rollen & Berechtigungen
	r.Get("/admin/roles", h.RolesPage)
	r.Get("/admin/orgchart", h.OrgChartPage)
	r.Get("/core/settings", h.CoreSettingsPage)
	r.Get("/core/settings/microsoft", h.MicrosoftAdminPage)
	r.Post("/core/settings/microsoft/sync", h.MicrosoftDirectorySyncWeb)
	r.Post("/core/settings/microsoft/assign", h.MicrosoftDirectoryAssignWeb)
	r.Post("/core/settings/microsoft/{id}/unassign", h.MicrosoftDirectoryUnassignWeb)
	r.Post("/core/settings", h.SaveCoreSettings)
	r.Post("/core/settings/due-dates", h.SaveDueDateSettings)
	r.Post("/core/settings/completion", h.CompletionSettingsWeb)
	r.Post("/core/settings/check-update", h.CheckUpdateWeb)
	r.Post("/core/settings/install-update", h.InstallUpdateWeb)
	r.Post("/admin/roles", h.RoleCreateWeb)
	r.Post("/admin/roles/{id}/delete-web", h.RoleDeleteWeb)
	r.Post("/admin/roles/{id}/level-web", h.RoleUpdateLevelWeb)
	r.Post("/admin/roles/{id}/department-web", h.RoleDepartmentWeb)
	r.Post("/admin/roles/matrix-web", h.RoleMatrixWeb)
	r.Post("/admin/settings", h.SettingsWeb)

	// Import & Export
	r.Get("/import", h.ImportPage)
	r.Post("/import/connections", h.ImportConnectionCreateWeb)
	r.Post("/import/connections/{id}/edit-web", h.ImportConnectionEditWeb)
	r.Post("/import/connections/{id}/delete-web", h.ImportConnectionDeleteWeb)
	r.Post("/import/connections/{id}/toggle-web", h.ImportConnectionToggleWeb)
	r.Get("/import/connections/{id}/sniffer", h.MqttSnifferPage)
	r.Get("/import/connections/{id}/sniffer/stream", h.MqttSnifferStream)
	r.Get("/import/connections/{id}/broker", h.MqttBrokerPage)
	r.Get("/import/connections/{id}/broker/stream", h.MqttBrokerStream)
	r.Post("/import/connections/{id}/mappings", h.ImportMappingCreateWeb)
	r.Post("/import/connections/{id}/mappings/{mappingId}/delete-web", h.ImportMappingDeleteWeb)
	r.Get("/import/connections/{id}/preview", h.ExcelPreviewPage)
	r.Post("/import/connections/{id}/preview/refresh", h.ExcelRefreshValuesWeb)
	r.Get("/import/connections/{id}/browse", h.SQLBrowsePage)
	r.Post("/import/connections/{id}/browse/refresh", h.SQLRefreshValuesWeb)
	r.Get("/import/connections/{id}/response", h.ImportResponsePage)
	r.Post("/import/connections/{id}/response/refresh", h.ImportResponseRefreshWeb)
	r.Get("/import/connections/{id}/read", h.ModbusReadPage)
	r.Post("/import/connections/{id}/read/refresh", h.ModbusRefreshValuesWeb)
	r.Get("/import/connections/{id}/nodes", h.OPCUABrowsePage)
	r.Post("/import/connections/{id}/nodes/refresh", h.OPCUARefreshValuesWeb)
	r.Get("/import/mappings", h.ImportMappingsOverviewPage)
	// Verbindungsseiten (connection_detail.go)
	r.Get("/import/connections/{id}", h.ImportConnectionPage)
	r.Get("/export/connections/{id}", h.ExportConnectionPage)
	r.Post("/import/connections/{id}/checks", h.ImportConnectionChecksWeb)
	r.Post("/export/connections/{id}/checks", h.ExportConnectionChecksWeb)
	r.Post("/import/connections/{id}/queries", h.ConnectionQuerySaveWeb)
	r.Post("/import/connections/{id}/queries/run-all", h.ConnectionQueriesRunAllWeb)
	r.Post("/import/connections/{id}/queries/{qid}/run", h.ConnectionQueryRunWeb)
	r.Post("/import/connections/{id}/queries/{qid}/delete", h.ConnectionQueryDeleteWeb)
	r.Post("/import/connections/{id}/templates/save", h.ImportConnectionTemplateSaveWeb)
	r.Post("/export/connections/{id}/templates/save", h.ExportConnectionTemplateSaveWeb)
	r.Post("/import/connections/{id}/templates/{tid}/apply", h.ImportConnectionTemplateApplyWeb)
	r.Post("/export/connections/{id}/templates/{tid}/apply", h.ExportConnectionTemplateApplyWeb)
	r.Post("/import/connections/{id}/templates/{tid}/delete", h.ImportConnectionTemplateDeleteWeb)
	r.Post("/export/connections/{id}/templates/{tid}/delete", h.ExportConnectionTemplateDeleteWeb)
	r.Get("/export", h.ExportPage)
	r.Post("/export/connections", h.ExportConnectionCreateWeb)
	r.Post("/export/connections/{id}/edit-web", h.ExportConnectionEditWeb)
	r.Post("/export/connections/{id}/delete-web", h.ExportConnectionDeleteWeb)
	r.Post("/export/connections/{id}/toggle-web", h.ExportConnectionToggleWeb)
	r.Get("/export/connections/{id}/preview", h.ExportPreviewPage)
	r.Post("/export/connections/{id}/preview/run", h.ExportRunWeb)
	r.Post("/export/connections/{id}/mappings", h.ExportMappingCreateWeb)
	r.Post("/export/connections/{id}/mappings/{mappingId}/delete-web", h.ExportMappingDeleteWeb)
	r.Get("/export/templates", h.ExportTemplatesPage)
	r.Post("/export/templates", h.ExportTemplateCreateWeb)
	r.Post("/export/templates/{id}/edit-web", h.ExportTemplateEditWeb)
	r.Post("/export/templates/{id}/delete-web", h.ExportTemplateDeleteWeb)
	r.Get("/export/mappings", h.ExportMappingsOverviewPage)

	// Override-Anmeldung an Systemnutzer-Terminals
	r.Post("/override-login", h.OverrideLoginWeb)
	r.Post("/override-login/rfid", h.OverrideLoginRFIDWeb)
	r.Post("/override-logout", h.OverrideLogoutWeb)

	// HTMX partials
	r.Get("/api/activity", h.ActivityFeed)
	r.Get("/api/search", h.Search)

	// Auth
	r.Get("/login", h.LoginPage)
	r.Post("/lang", h.LangSwitchWeb)
	r.Post("/login", h.LoginPost)
	r.Post("/login/rfid", h.LoginRFIDWeb)
	r.Get("/login/forgot", h.LoginForgotPage)
	r.Post("/login/forgot", h.LoginForgotPost)
	r.Get("/login/reset", h.LoginResetPage)
	r.Post("/login/reset", h.LoginResetPost)
	r.Get("/logout", h.Logout)

	return r
}

// ── Helpers ──────────────────────────────────────────────────

func (h *Handler) render(w http.ResponseWriter, tmpl string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	t = bindLang(t, langOf(data))
	if tmpl == "login" {
		lt, _ := t.ParseFiles("web/templates/login.gohtml")
		lt.ExecuteTemplate(w, "login.gohtml", data)
		return
	}
	if _, err2 := t.ParseFiles("web/templates/" + tmpl + ".gohtml"); err2 != nil {
		http.Error(w, "Parse: "+err2.Error(), 500)
		return
	}
	if err3 := t.ExecuteTemplate(w, "base.gohtml", data); err3 != nil {
		http.Error(w, "Template-Fehler: "+err3.Error(), 500)
	}
}

func esc(s string) string {
	return template.HTMLEscapeString(s)
}

func jsesc(s string) string {
	return template.JSEscapeString(s)
}

func greeting() string {
	h := time.Now().Hour()
	if h < 12 {
		return "Morgen"
	}
	if h < 18 {
		return "Tag"
	}
	return "Abend"
}
func timeAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "gerade eben"
	}
	if d < time.Hour {
		return fmt.Sprintf("vor %dmin", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("vor %dh", int(d.Hours()))
	}
	return fmt.Sprintf("vor %dd", int(d.Hours()/24))
}
func severityClass(s string) string {
	switch s {
	case "critical":
		return "b-red"
	case "high":
		return "b-red"
	case "medium":
		return "b-amber"
	default:
		return "b-gray"
	}
}

func priorityClass(p string) string {
	switch p {
	case "critical":
		return "b-red"
	case "high":
		return "b-red"
	case "medium":
		return "b-amber"
	default:
		return "b-blue"
	}
}

func priorityDot(p string) string {
	switch p {
	case "critical", "high":
		return "d-red"
	case "medium":
		return "d-amber"
	default:
		return "d-blue"
	}
}

func statusClass(s string) string {
	switch s {
	case "resolved", "closed", "done", "completed":
		return "b-green"
	case "in_progress", "active":
		return "b-blue"
	case "open", "detected":
		return "b-amber"
	default:
		return "b-gray"
	}
}

func statusLabel(s string) string {
	labels := map[string]string{
		"open": "Offen", "in_progress": "In Arbeit",
		"resolved": "Gelöst", "closed": "Geschlossen",
		"detected": "Erkannt", "analyzing": "Analysiert",
		"pending": "Ausstehend", "archive": "Archiv",
		"done": "Erledigt", "skipped": "Übersprungen",
		"planning": "Planung", "active": "Aktiv",
		"paused": "Pausiert", "completed": "Abgeschlossen",
	}
	if l, ok := labels[s]; ok {
		return l
	}
	return s
}

func faultBlinkClass(status string) string {
	if status == "detected" {
		return "blink-yellow"
	}
	return ""
}

func ticketBlinkClass(assignedTo *string, status string) string {
	if status == "open" && assignedTo == nil {
		return "blink-purple"
	}
	return ""
}

func maintenanceBlinkClass(dueDate time.Time) string {
	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	due := dueDate.Format("2006-01-02")
	if due <= today {
		return "blink-red"
	}
	if due == tomorrow {
		return "blink-yellow"
	}
	return ""
}

func getUser(r *http.Request) *users.User {
	if u, ok := r.Context().Value("user").(*users.User); ok {
		return u
	}
	return &users.User{FirstName: "Gast", LastName: "", Role: "viewer"}
}

// baseData baut die auf jeder Seite benoetigten Basisdaten - inklusive der
// Auto-Logout-/Override-Felder, die base.gohtml global fuer den
// Inaktivitaets-Timer und die Override-Anmeldung braucht.
func (h *Handler) baseData(r *http.Request, page, title, ctxTitle string) BaseData {
	u := getUser(r)
	isOverride := false
	if c, err := r.Cookie("pdh_return_token"); err == nil && c.Value != "" {
		isOverride = true
	}
	brand := h.branding()
	lang := h.requestLang(r)
	b := BaseData{
		Look:  h.appearance(r, brand),
		Lang:  lang,
		Title: tr(lang, title), Page: page,
		ContextTitle:  tr(lang, ctxTitle),
		UserName:      u.FirstName + " " + u.LastName,
		UserFirstName: u.FirstName,
		UserLastName:  u.LastName,

		IsSystemUser:           u.IsSystemUser,
		IsOverrideSession:      isOverride,
		IdleTimeoutMinutes:     h.rbac.IdleTimeoutMinutes(),
		OverrideTimeoutMinutes: h.rbac.OverrideTimeoutMinutes(),
		CanManageUsers:         h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_users"),
		CanManageRoles:         h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_roles"),
		CanImport:              h.rbac.HasPermissionForUser(u.ID, string(u.Role), "import.read"),
		CanExport:              h.rbac.HasPermissionForUser(u.ID, string(u.Role), "export.read"),
		CanChat:                u.ID != "" && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "chat.use"),
		Brand:                  brand,
		CanEditInfra:           h.rbac.HasPermissionForUser(u.ID, string(u.Role), "infrastructure.edit"),
		CanTrainings:           h.rbac.HasPermissionForUser(u.ID, string(u.Role), trainingsPerm),
		TerminalInfraID:        h.terminalInfraID(r),
		CanBackup:              h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.backup"),
		CanPrinters:            h.rbac.HasPermissionForUser(u.ID, string(u.Role), "printers.manage"),
		CanPrint:               h.rbac.HasPermissionForUser(u.ID, string(u.Role), "printers.use"),
		CanCleanup:             h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.cleanup"),
		CanServerConfig:        u.Role == users.RoleAdmin && h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.server_config"),
	}
	b.Nav = buildNav(&b, h.userNavLayout(r.Context(), u.ID))
	return b
}

func recordTable(refType string) (string, bool) {
	switch refType {
	case "ticket":
		return "tickets", true
	case "fault":
		return "faults", true
	case "maintenance_task":
		return "maintenance_tasks", true
	default:
		return "", false
	}
}

func nullID(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

// derefOr liefert *s, falls s nicht nil ist, sonst den Default-Wert.
func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

func optionalID(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (h *Handler) infraName(ctx context.Context, id *string) string {
	if id == nil || strings.TrimSpace(*id) == "" || h.infra == nil {
		return ""
	}
	node, err := h.infra.GetByID(ctx, *id)
	if err != nil || node == nil {
		return ""
	}
	return node.Name
}

func (h *Handler) userOptions(ctx context.Context) []UserOption {
	list, err := h.users.List(ctx)
	if err != nil {
		return nil
	}
	opts := make([]UserOption, 0, len(list))
	for _, u := range list {
		opts = append(opts, UserOption{ID: u.ID, Name: strings.TrimSpace(u.FirstName + " " + u.LastName)})
	}
	return opts
}

// peopleTable: Datensatztypen mit den Spalten assigned_to und responsible_to.
func peopleTable(refType string) (string, bool) {
	if refType == "project" {
		return "projects", true
	}
	return recordTable(refType)
}

func (h *Handler) recordPeople(ctx context.Context, refType, id string) RecordPeople {
	table, ok := peopleTable(refType)
	if !ok || h.db == nil {
		return RecordPeople{}
	}
	var assignedID, responsibleID *string
	var assignedName, responsibleName string
	query := fmt.Sprintf(`
		SELECT r.assigned_to::text, r.responsible_to::text,
		       COALESCE(au.first_name || ' ' || au.last_name, ''),
		       COALESCE(ru.first_name || ' ' || ru.last_name, '')
		FROM %s r
		LEFT JOIN users au ON r.assigned_to = au.id
		LEFT JOIN users ru ON r.responsible_to = ru.id
		WHERE r.id=$1`, table)
	if err := h.db.QueryRow(ctx, query, id).Scan(&assignedID, &responsibleID, &assignedName, &responsibleName); err != nil {
		return RecordPeople{}
	}
	result := RecordPeople{AssignedName: assignedName, ResponsibleName: responsibleName}
	if assignedID != nil {
		result.AssignedID = *assignedID
	}
	if responsibleID != nil {
		result.ResponsibleID = *responsibleID
	}
	return result
}

func (h *Handler) recordImageURL(ctx context.Context, refType, id string) string {
	table, ok := recordTable(refType)
	if !ok || h.db == nil {
		return ""
	}
	var path *string
	query := fmt.Sprintf(`
		SELECT a.filepath
		FROM %s r
		JOIN attachments a ON a.id = r.record_image_attachment_id
		WHERE r.id=$1`, table)
	if err := h.db.QueryRow(ctx, query, id).Scan(&path); err != nil || path == nil {
		return ""
	}
	return "/uploads/" + *path
}

func (h *Handler) updateInfrastructureWeb(w http.ResponseWriter, r *http.Request, refType string) {
	table, ok := recordTable(refType)
	if !ok || h.db == nil {
		http.Error(w, "ungültiger Datensatztyp", http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "id")
	r.ParseForm()
	infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
	query := fmt.Sprintf(`UPDATE %s SET infrastructure_id=NULLIF($1,'')::uuid, updated_at=NOW() WHERE id=$2`, table)
	if _, err := h.db.Exec(r.Context(), query, infraID, id); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	label := "Keine Infrastruktur"
	if infraID != "" && h.infra != nil {
		if node, err := h.infra.GetByID(r.Context(), infraID); err == nil && node != nil {
			label = node.Name
		}
	}
	u := getUser(r)
	_, _ = h.db.Exec(r.Context(), `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
		VALUES ($1, $2, 'update', 'infrastructure_id', $3, $4, 'Infrastruktur geändert')`,
		refType, id, infraID, u.ID)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px">Gespeichert: %s</div>`, esc(label))
}

func (h *Handler) recordHistory(ctx context.Context, refType, id string) []HistoryView {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT rh.action, COALESCE(rh.field_name,''), COALESCE(rh.old_value,''),
		       COALESCE(rh.new_value,''), COALESCE(rh.message,''),
		       COALESCE(u.first_name || ' ' || u.last_name, 'System'), rh.created_at
		FROM record_history rh
		LEFT JOIN users u ON rh.created_by = u.id
		WHERE rh.ref_type=$1 AND rh.ref_id=$2
		ORDER BY rh.created_at DESC
		LIMIT 30`, refType, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []HistoryView
	for rows.Next() {
		var item HistoryView
		var created time.Time
		if err := rows.Scan(&item.Action, &item.FieldName, &item.OldValue, &item.NewValue, &item.Message, &item.UserName, &created); err == nil {
			item.CreatedAt = created.Format("02.01.2006 15:04")
			list = append(list, item)
		}
	}
	return list
}

func (h *Handler) addHistory(ctx context.Context, refType, id, action, field, oldValue, newValue, message, userID string) {
	if h.db == nil {
		return
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		refType, id, action, field, oldValue, newValue, message, nullID(userID))
}

func (h *Handler) RecordPeopleWeb(w http.ResponseWriter, r *http.Request) {
	refType := chi.URLParam(r, "refType")
	id := chi.URLParam(r, "id")
	table, ok := peopleTable(refType)
	if !ok {
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	r.ParseForm()
	u := getUser(r)
	old := h.recordPeople(r.Context(), refType, id)
	assigned := r.FormValue("assigned_to")
	responsible := r.FormValue("responsible_to")
	_, err := h.db.Exec(r.Context(), fmt.Sprintf(`
		UPDATE %s SET assigned_to=$1, responsible_to=$2, updated_at=NOW() WHERE id=$3`, table),
		nullID(assigned), nullID(responsible), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if old.AssignedID != assigned {
		h.addHistory(r.Context(), refType, id, "people", "assigned_to", old.AssignedID, assigned, "Zugewiesener Mitarbeiter geändert", u.ID)
	}
	if old.ResponsibleID != responsible {
		h.addHistory(r.Context(), refType, id, "people", "responsible_to", old.ResponsibleID, responsible, "Verantwortlicher Mitarbeiter geändert", u.ID)
	}
	if err := h.setRecordGroup(r, refType, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Zuständigkeit gespeichert</span>`)
}

func (h *Handler) RecordArchiveWeb(w http.ResponseWriter, r *http.Request) {
	refType := chi.URLParam(r, "refType")
	id := chi.URLParam(r, "id")
	u := getUser(r)
	var err error
	switch refType {
	case "ticket":
		err = h.tickets.QuickResolve(r.Context(), id, "Archiviert", "", u.ID)
	case "fault":
		err = h.faults.QuickResolve(r.Context(), id, "Archiviert", "", u.ID)
	default:
		http.Error(w, "unbekannter datensatztyp", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.addHistory(r.Context(), refType, id, "archive", "archived_at", "", time.Now().Format(time.RFC3339), "Datensatz archiviert", u.ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<span style="color:var(--green);font-size:12px"><i class="ti ti-archive"></i> Archiviert</span>`)
}

// ── Seiten ───────────────────────────────────────────────────

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	now := time.Now()
	_, week := now.ISOWeek()

	data := DashboardData{
		BaseData:   h.baseData(r, "dashboard", "Dashboard", "Offene Tickets"),
		Greeting:   greeting(),
		DateStr:    longDate(h.requestLang(r), now),
		WeekNumber: week,
		WeekRange: fmt.Sprintf("%s–%s",
			now.AddDate(0, 0, -int(now.Weekday())+1).Format("02.01"),
			now.AddDate(0, 0, 7-int(now.Weekday())).Format("02.01")),
	}
	data.Widgets, data.WidgetCatalog, _ = h.dashboardWidgetViews(r)

	// Störungen
	if fl, err := h.faults.List(ctx, ""); err == nil {
		for _, f := range fl {
			if f.Status == "detected" || f.Status == "in_progress" || f.Status == "analyzing" {
				data.Stats.ActiveFaults++
				if f.Status == "analyzing" {
					data.Stats.AnalyzingFaults++
				}
			}
			if len(data.Faults) < 4 && (f.Status == "detected" || f.Status == "in_progress") {
				data.Faults = append(data.Faults, FaultView{
					ID: f.ID, Title: f.Title,
					Status: string(f.Status), StatusLabel: statusLabel(string(f.Status)),
					StatusClass: statusClass(string(f.Status)),
					Severity:    string(f.Severity), SeverityClass: severityClass(string(f.Severity)),
					DetectedAgo: timeAgo(f.DetectedAt),
					BlinkClass:  faultBlinkClass(string(f.Status)),
				})
			}
		}
	}

	// Tickets
	if tl, err := h.tickets.List(ctx, ""); err == nil {
		for _, t := range tl {
			if t.Status == "open" || t.Status == "in_progress" {
				data.Stats.OpenTickets++
				if t.Priority == "critical" {
					data.Stats.CriticalTickets++
				}
			}
			if len(data.OpenTickets) < 5 && (t.Status == "open") {
				data.OpenTickets = append(data.OpenTickets, TicketView{
					ID: t.ID, Title: t.Title,
					Priority: string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
					PriorityDot: priorityDot(string(t.Priority)),
					BlinkClass:  ticketBlinkClass(t.AssignedTo, string(t.Status)),
				})
			}
		}
	}

	// Wartung fällig
	if mt, err := h.maint.GetDueToday(ctx); err == nil {
		data.Stats.MaintenanceDue = len(mt)
		for _, m := range mt {
			if len(data.MaintenanceDue) < 3 {
				data.MaintenanceDue = append(data.MaintenanceDue, MaintenanceView{
					ID: m.ID, Title: m.Title, InfraName: m.InfraName,
					EstimatedMin: 0, Priority: string(m.Priority),
					PriorityClass: priorityClass(string(m.Priority)),
					BlinkClass:    maintenanceBlinkClass(m.DueDate),
				})
			}
		}
	}

	// Lager
	if stats, err := h.inv.GetStats(ctx); err == nil {
		if v, ok := stats["total_value"].(float64); ok {
			data.Stats.InventoryValue = fmt.Sprintf("%.0f", v)
		}
		if v, ok := stats["low_stock"].(int); ok {
			data.Stats.LowStock = v
		}
	}

	// Schichtplan
	monday := now.AddDate(0, 0, -int(now.Weekday())+1)
	if wp, err := h.shifts.GetWeekPlan(ctx, monday.Format("2006-01-02")); err == nil && wp != nil {
		days := []string{"Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"}
		shiftClasses := map[string]string{"F": "sf", "S": "ss", "N": "sn"}
		for i, u2 := range wp.Users {
			if i == 0 || u2.UserID == u.ID {
				data.WeekPlan = true
				for d := 0; d < 7; d++ {
					date := monday.AddDate(0, 0, d).Format("2006-01-02")
					label, class := "–", "se"
					if entry, ok := u2.Days[date]; ok && entry.ShortName != "" {
						label = entry.ShortName
						if c, ok := shiftClasses[entry.ShortName]; ok {
							class = c
						}
					}
					data.WeekDays = append(data.WeekDays, ShiftDay{Short: days[d], Label: label, Class: class})
				}
				break
			}
		}
	}

	// ── Kombinierter Zeitstrahl (Aufgaben, Tickets, Wartung, Störungen) ──
	data.GanttItems = h.scopeGantt(ctx, h.requestScope(r), h.buildDashboardGantt(ctx, now))

	h.render(w, "dashboard", data)
}

// buildDashboardGantt führt Aufgaben, Tickets, Wartungsaufträge und
// Störungen zu einer einheitlichen Zeitstrahl-Liste fürs Dashboard
// zusammen. Objekte ohne echtes Fälligkeitsdatum bekommen ein vorläufiges
// Enddatum (Erstellungsdatum + 30 Tage) und werden als "vorläufig"
// markiert, damit das Template die Balkenfarbe entsprechend ausblassen
// kann.
func (h *Handler) buildDashboardGantt(ctx context.Context, now time.Time) []GanttItem {
	const recentDoneWindow = 14 * 24 * time.Hour
	var items []GanttItem

	// Aufgaben
	tl, err := h.tasks.List(ctx, "", "", false)
	if err != nil {
		log.Error().Err(err).Msg("dashboard-gantt: aufgaben laden fehlgeschlagen")
	}
	if err == nil {
		for _, t := range tl {
			isDone := t.Status == tasks.StatusResolved || t.Status == tasks.StatusClosed
			start := t.CreatedAt
			if t.StartDate != nil {
				start = *t.StartDate
			}
			end := start.AddDate(0, 0, 30)
			provisional := true
			if t.DueDate != nil {
				end = *t.DueDate
				provisional = false
			}
			if isDone {
				if t.ResolvedAt != nil {
					if now.Sub(*t.ResolvedAt) > recentDoneWindow {
						continue
					}
					end = *t.ResolvedAt
				}
				provisional = false
			}
			color := t.Color
			if color == "" {
				color = "#9d7fc9" // Aufgabe (Standardfarbe) - synchron mit dem Leitstand-Zeitstrahl
			}
			items = append(items, GanttItem{
				ID: t.ID, RefType: "task", Title: t.Title,
				StartISO: start.Format("2006-01-02"), EndISO: end.Format("2006-01-02"),
				IsProvisional: provisional, IsDone: isDone, IsRunning: t.Status == "in_progress", Color: color,
				DetailURL:       "/tasks/" + t.ID,
				DueDateEndpoint: "/api/v1/tasks/" + t.ID + "/edit",
			})
		}
	}

	// Tickets
	if tl, err := h.tickets.List(ctx, ""); err == nil {
		for _, t := range tl {
			isDone := t.Status == "resolved" || t.Status == "closed"
			start := t.CreatedAt
			end := start.AddDate(0, 0, 30)
			provisional := true
			if t.DueDate != nil {
				end = *t.DueDate
				provisional = false
			}
			if isDone {
				if t.ResolvedAt != nil {
					if now.Sub(*t.ResolvedAt) > recentDoneWindow {
						continue
					}
					end = *t.ResolvedAt
				}
				provisional = false
			}
			items = append(items, GanttItem{
				ID: t.ID, RefType: "ticket", Title: t.Title,
				StartISO: start.Format("2006-01-02"), EndISO: end.Format("2006-01-02"),
				IsProvisional: provisional, IsDone: isDone, IsRunning: t.Status == "in_progress", Color: "#4b9fc4", // synchron mit dem Leitstand-Zeitstrahl
				DetailURL:       "/tickets/" + t.ID,
				DueDateEndpoint: "/api/v1/tickets/" + t.ID + "/due-date",
			})
		}
	}

	// Wartungsaufträge
	if ml, err := h.maint.ListTasks(ctx, "", ""); err == nil {
		for _, m := range ml {
			if m.Status == maintenance.TaskSkipped {
				continue
			}
			isDone := m.Status == maintenance.TaskDone
			start := m.CreatedAt
			end := m.DueDate // immer gesetzt, nie vorläufig
			if isDone {
				if m.CompletedAt != nil {
					if now.Sub(*m.CompletedAt) > recentDoneWindow {
						continue
					}
					end = *m.CompletedAt
				}
			}
			items = append(items, GanttItem{
				ID: m.ID, RefType: "maintenance", Title: m.Title,
				StartISO: start.Format("2006-01-02"), EndISO: end.Format("2006-01-02"),
				IsProvisional: false, IsDone: isDone, IsRunning: m.Status == maintenance.TaskInProgress, Color: "#c99a3c", // synchron mit dem Leitstand-Zeitstrahl
				DetailURL:       "/maintenance/tasks/" + m.ID,
				DueDateEndpoint: "/api/v1/maintenance/tasks/" + m.ID + "/due-date",
			})
		}
	}

	// Störungen
	if fl, err := h.faults.List(ctx, ""); err == nil {
		for _, f := range fl {
			isDone := f.Status == "resolved" || f.Status == "closed"
			start := f.DetectedAt
			end := start.AddDate(0, 0, 30)
			provisional := true
			if f.DueDate != nil {
				end = *f.DueDate
				provisional = false
			}
			if isDone {
				if f.ResolvedAt != nil {
					if now.Sub(*f.ResolvedAt) > recentDoneWindow {
						continue
					}
					end = *f.ResolvedAt
				}
				provisional = false
			}
			items = append(items, GanttItem{
				ID: f.ID, RefType: "fault", Title: f.Title,
				StartISO: start.Format("2006-01-02"), EndISO: end.Format("2006-01-02"),
				IsProvisional: provisional, IsDone: isDone, IsRunning: f.Status == "in_progress", Color: "#3fae86", // synchron mit dem Leitstand-Zeitstrahl
				DetailURL:       "/faults/" + f.ID,
				DueDateEndpoint: "/api/v1/faults/" + f.ID + "/due-date",
			})
		}
	}


	// nicht zugewiesen: weder Person noch Gruppe (Aufgaben: keine Beteiligten)
	if open := h.unassignedRecords(ctx); len(open) > 0 {
		for i := range items {
			if !items[i].IsDone && open[items[i].RefType+":"+items[i].ID] {
				items[i].IsUnassigned = true
			}
		}
	}
	return items
}

func (h *Handler) Faults(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	unassigned := r.URL.Query().Get("unassigned") == "1"

	data := FaultsPageData{
		BaseData: h.baseData(r, "faults", "Störungen", "Copilot-Analysen"),
		Filter:   filter,
		Users:    h.userOptions(ctx),
	}
	data.Tabs = h.statusTabs(ctx, "faults", "/faults", filter, []statusTabDef{
		{"detected", "Erkannt", "ti-alert-triangle"}, {"analyzing", "Analysiert", "ti-brain"},
		{"in_progress", "In Bearbeitung", "ti-tool"}, {"resolved", "Gelöst", "ti-check"}, {"closed", "Geschlossen", "ti-lock"},
	}, true, brokerInboxTab(unassigned))

	tagIDs := h.categoryFilterIDs(r, "fault")
	scopeIDs := h.scopeAllowedIDs(r, "fault")
	if fl, err := h.faults.List(ctx, faults.FaultStatus(filter)); err == nil {
		data.Total = len(fl)
		for _, f := range fl {
			if tagIDs != nil && !tagIDs[f.ID] || scopeIDs != nil && !scopeIDs[f.ID] {
				continue
			}
			if unassigned && (f.AssignedTo != nil || f.Status == "resolved" || f.Status == "closed") {
				continue
			}
			if f.Status == "detected" || f.Status == "in_progress" {
				data.Open++
			}
			fv := FaultView{
				ID: f.ID, Title: f.Title, Description: f.Description,
				Status: string(f.Status), StatusLabel: statusLabel(string(f.Status)),
				StatusClass: statusClass(string(f.Status)),
				Severity:    string(f.Severity), SeverityClass: severityClass(string(f.Severity)),
				DetectedAgo: timeAgo(f.DetectedAt),
				InfraName:   h.infraName(ctx, f.InfrastructureID),
			}
			if f.InfrastructureID != nil {
				fv.InfraID = *f.InfrastructureID
			}
			data.Faults = append(data.Faults, fv)
		}
	}
	h.render(w, "faults", data)
}

func (h *Handler) Tickets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	unassigned := r.URL.Query().Get("unassigned") == "1"

	data := TicketsPageData{
		BaseData:       h.baseData(r, "tickets", "Tickets", "Kritische Tickets"),
		Filter:         filter,
		Users:          h.userOptions(ctx),
		DefaultDueDays: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTicket, appsettings.DefaultDueDaysFallback),
	}
	data.Tabs = h.statusTabs(ctx, "tickets", "/tickets", filter, []statusTabDef{
		{"open", "Offen", "ti-circle"}, {"in_progress", "In Arbeit", "ti-tool"}, {"pending", "Ausstehend", "ti-hourglass"},
		{"resolved", "Gelöst", "ti-check"}, {"closed", "Geschlossen", "ti-lock"},
	}, true, brokerInboxTab(unassigned))

	tagIDs := h.categoryFilterIDs(r, "ticket")
	scopeIDs := h.scopeAllowedIDs(r, "ticket")
	if tl, err := h.tickets.List(ctx, tickets.Status(filter)); err == nil {
		data.Total = len(tl)
		for _, t := range tl {
			if tagIDs != nil && !tagIDs[t.ID] || scopeIDs != nil && !scopeIDs[t.ID] {
				continue
			}
			if unassigned && (t.AssignedTo != nil || t.Status == "resolved" || t.Status == "closed") {
				continue
			}
			if t.Status == "open" || t.Status == "in_progress" {
				data.Open++
			}
			tv := TicketView{
				ID: t.ID, Title: t.Title, Description: t.Description,
				InfraName: h.infraName(ctx, t.InfrastructureID),
				Priority:  string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
				PriorityDot: priorityDot(string(t.Priority)),
				Status:      string(t.Status), StatusLabel: statusLabel(string(t.Status)),
				StatusClass: statusClass(string(t.Status)),
				CreatedAgo:  timeAgo(t.CreatedAt),
			}
			if t.InfrastructureID != nil {
				tv.InfraID = *t.InfrastructureID
			}
			data.Tickets = append(data.Tickets, tv)
			if t.Priority == "critical" {
				data.CriticalTickets = append(data.CriticalTickets, tv)
			}
		}
	}
	h.render(w, "tickets", data)
}

func (h *Handler) CreateFault(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	symptomsRaw := r.FormValue("symptoms")
	var symptoms []string
	for _, s := range strings.Split(symptomsRaw, ",") {
		if s := strings.TrimSpace(s); s != "" {
			symptoms = append(symptoms, s)
		}
	}
	in := &faults.CreateFaultInput{
		Title:         r.FormValue("title"),
		Description:   r.FormValue("description"),
		Symptoms:      symptoms,
		Severity:      faults.Severity(r.FormValue("severity")),
		AssignedTo:    optionalID(r.FormValue("assigned_to")),
		ResponsibleTo: optionalID(r.FormValue("responsible_to")),
	}
	h.faults.Create(r.Context(), in, u.ID)
	h.Faults(w, r)
}

// AnalyzeFault: Copilot-Analyse ausfuehren und das Ergebnis bzw. den genauen
// Fehler anzeigen (frueher lief sie unsichtbar im Hintergrund, Fehler gingen verloren).
func (h *Handler) AnalyzeFault(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := context.WithTimeout(r.Context(), 290*time.Second)
	defer cancel()
	_, err := h.faults.Analyze(ctx, id)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		componentLog("copilot").Error().Err(err).Str("stoerung", id).Msg("analyse fehlgeschlagen")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;margin-top:8px;text-align:left"><i class="ti ti-alert-circle"></i> Analyse fehlgeschlagen: %s</div>`, esc(err.Error()))
		return
	}
	w.Header().Set("HX-Refresh", "true")
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px;margin-top:8px"><i class="ti ti-check"></i> Analyse fertig</div>`)
}

// CopilotAskWeb: POST /copilot/ask (q, fault) – Fragen aus der Copilot-Seitenleiste.
// Mit Stoerungs-ID im Kontext dieser Stoerung, sonst allgemein.
func (h *Handler) CopilotAskWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" || len([]rune(q)) > 2000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Bitte eine Frage eingeben (höchstens 2000 Zeichen)."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 290*time.Second)
	defer cancel()
	u := getUser(r)
	scope := h.requestScope(r)
	fid := strings.TrimSpace(r.FormValue("fault"))
	if fid != "" && (!uuidInPathRe.MatchString(fid) || (scope != nil && !h.recordInScope(ctx, scope, "fault", fid))) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Kein Zugriff auf diese Störung."})
		return
	}
	// Copilot mit PDH-Daten (copilot_data.go): liest mit den Rechten der fragenden Person
	d := &copilotData{h: h, userID: u.ID, scope: scope, can: func(p string) bool { return h.hasPerm(r, p) }}
	reply, sources, err := h.askCopilot(ctx, d, strings.TrimSpace(u.FirstName+" "+u.LastName), q, fid)
	if err != nil {
		componentLog("copilot").Error().Err(err).Msg("frage fehlgeschlagen")
		// bewusst 200: Reverse-Proxys (Cloudflare, Nginx) ersetzen 502-Antworten
		// durch eigene Fehlerseiten – dann ginge der eigentliche Grund verloren
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reply": reply, "sources": sources})
}

func (h *Handler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	in := &tickets.CreateInput{
		Title:         r.FormValue("title"),
		Description:   r.FormValue("description"),
		Priority:      tickets.Priority(r.FormValue("priority")),
		AssignedTo:    optionalID(r.FormValue("assigned_to")),
		ResponsibleTo: optionalID(r.FormValue("responsible_to")),
	}
	h.tickets.Create(r.Context(), in, u.ID)
	h.Tickets(w, r)
}

func (h *Handler) CreatePart(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	minQty, _ := strconv.ParseFloat(r.FormValue("min_qty"), 64)
	in := &inventory.CreatePartInput{
		PartNumber:   r.FormValue("part_number"),
		Name:         r.FormValue("name"),
		Manufacturer: r.FormValue("manufacturer"),
		Category:     r.FormValue("category"),
		MinQty:       minQty,
	}
	h.inv.Create(r.Context(), in, u.ID)
	h.Inventory(w, r)
}

func (h *Handler) ActivityFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
<div class="act-item"><div class="act-dot d-green"></div><div class="act-text">Server läuft</div><div class="act-time">jetzt</div></div>
<div class="act-item"><div class="act-dot d-blue"></div><div class="act-text">PDH v%s</div><div class="act-time">aktiv</div></div>
`, template.HTMLEscapeString(pdh.Version()))
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) < 2 {
		w.Write([]byte(""))
		return
	}
	w.Header().Set("Content-Type", "text/html")

	results, err := h.infra.Search(r.Context(), q)
	if err != nil || len(results) == 0 {
		fmt.Fprintf(w, `<div style="color:var(--muted);padding:4px 0;font-size:11px">Keine Ergebnisse</div>`)
		return
	}
	for _, item := range results {
		fmt.Fprintf(w, `<div class="act-item"><div class="act-dot d-blue"></div><div class="act-text">%s</div><div class="act-time" style="font-size:10px">%s</div></div>`, esc(item.Name), esc(string(item.Type)))
	}
}

// Platzhalter-Seiten

type TimePageData struct {
	BaseData
	TodayStr     string
	WeekStr      string
	TodayCount   int
	Entries      []TimeEntryView
	Running      *TimeEntryView
	IsAdmin      bool
	Users        []UserOption
	FilterUser   string
	FilterRange  string
	FilterRaster string
	RangeFromISO string
	RangeToISO   string
	// Von Kollegen eingetragene, noch zu bestaetigende eigene Zeiten
	// (Abschluss-Assistent, "wer war dabei") – unabhaengig vom Zeitraum
	PendingEntries []TimeEntryView
}

type TimeEntryView struct {
	ID           string
	Description  string
	RefTypeLabel string
	RefTypeDot   string
	StartedStr   string
	EndedStr     string
	StartedISO   string
	EndedISO     string
	DurationStr  string
	Running      bool
	InfraName    string
	UserName     string
	CanEdit      bool
	Pending      bool   // unbestaetigt (gelb)
	PendingFrom  string // eingetragen von
	Mine         bool   // eigener Eintrag (darf bestaetigt werden)
	userID       string
}

func timeRefLabel(t timetracking.RefType) string {
	switch t {
	case timetracking.RefFault:
		return "Störung"
	case timetracking.RefTicket:
		return "Ticket"
	case timetracking.RefMaintenance:
		return "Wartung"
	case timetracking.RefProduction:
		return "Sonstiges"
	default:
		return string(t)
	}
}

func timeRefDot(t timetracking.RefType) string {
	switch t {
	case timetracking.RefFault:
		return "d-red"
	case timetracking.RefTicket:
		return "d-blue"
	case timetracking.RefMaintenance:
		return "d-amber"
	default:
		return "d-green"
	}
}

func durationText(min int) string {
	h := min / 60
	m := min % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dmin", h, m)
	}
	return fmt.Sprintf("%dmin", m)
}

func timeEntryView(e *timetracking.TimeEntry) TimeEntryView {
	startedLocal := e.StartedAt.In(time.Local)
	v := TimeEntryView{
		ID:           e.ID,
		Description:  e.Description,
		RefTypeLabel: timeRefLabel(e.RefType),
		RefTypeDot:   timeRefDot(e.RefType),
		StartedStr:   startedLocal.Format("15:04 02.01."),
		StartedISO:   startedLocal.Format("2006-01-02T15:04"),
		Running:      e.EndedAt == nil,
		InfraName:    e.InfraName,
		UserName:     e.UserName,
		Pending:      e.Pending,
		PendingFrom:  e.PendingFrom,
		userID:       e.UserID,
	}
	if e.EndedAt != nil {
		endedLocal := e.EndedAt.In(time.Local)
		v.EndedStr = endedLocal.Format("15:04 02.01.")
		v.EndedISO = endedLocal.Format("2006-01-02T15:04")
	}
	if e.DurationMin != nil {
		v.DurationStr = durationText(*e.DurationMin)
	}
	return v
}

func (h *Handler) TimeTracking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	now := time.Now()
	weekStart := now.AddDate(0, 0, -int(now.Weekday())+1)
	if now.Weekday() == time.Sunday {
		weekStart = now.AddDate(0, 0, -6)
	}

	isFullAdmin := u.Role == users.RoleAdmin || u.Role == users.RoleManager
	subordinateIDs, _ := h.users.SubordinateIDs(ctx, u.ID)
	// Wer keine volle Admin/Manager-Rolle hat, aber eigene (direkte oder
	// indirekte) Unteruser fuehrt, darf trotzdem deren Zeiten zusammen mit
	// den eigenen auswerten - eingeschraenkt auf den eigenen Team-Umfang.
	isAdmin := isFullAdmin || len(subordinateIDs) > 0
	filterUser := r.URL.Query().Get("user")
	filterRange := r.URL.Query().Get("range")
	if filterRange == "" {
		filterRange = "today"
	}
	filterRaster := r.URL.Query().Get("raster")
	if filterRaster == "" {
		filterRaster = "day"
	}

	rangeFrom := now.Format("2006-01-02")
	rangeTo := now.Format("2006-01-02")
	switch filterRange {
	case "week":
		rangeFrom = weekStart.Format("2006-01-02")
		rangeTo = weekStart.AddDate(0, 0, 6).Format("2006-01-02")
	case "month":
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		monthEnd := monthStart.AddDate(0, 1, -1)
		rangeFrom = monthStart.Format("2006-01-02")
		rangeTo = monthEnd.Format("2006-01-02")
	}

	rangeFromDate, _ := time.Parse("2006-01-02", rangeFrom)
	rangeToDate, _ := time.Parse("2006-01-02", rangeTo)
	rangeToExclusive := rangeToDate.AddDate(0, 0, 1)

	data := TimePageData{
		BaseData:     h.baseData(r, "time", "Zeiterfassung", "Heute"),
		TodayStr:     now.Format("02.01.2006"),
		WeekStr:      weekStart.Format("02.01.") + " - " + weekStart.AddDate(0, 0, 6).Format("02.01."),
		IsAdmin:      isAdmin,
		FilterUser:   filterUser,
		FilterRange:  filterRange,
		FilterRaster: filterRaster,
		RangeFromISO: rangeFromDate.Format("2006-01-02T15:04:05"),
		RangeToISO:   rangeToExclusive.Format("2006-01-02T15:04:05"),
	}

	if isAdmin {
		scopeIDs := append([]string{u.ID}, subordinateIDs...)
		if isFullAdmin {
			data.Users = h.userOptions(ctx)
		} else {
			allOptions := h.userOptions(ctx)
			scopeSet := make(map[string]bool, len(scopeIDs))
			for _, id := range scopeIDs {
				scopeSet[id] = true
			}
			for _, opt := range allOptions {
				if scopeSet[opt.ID] {
					data.Users = append(data.Users, opt)
				}
			}
			// nicht autorisierter Filterversuch ausserhalb des eigenen
			// Teams: ignorieren und auf den eigenen Team-Umfang zurueckfallen.
			if filterUser != "" && !scopeSet[filterUser] {
				filterUser = ""
			}
		}
		var entries []*timetracking.TimeEntry
		var err error
		if filterUser != "" {
			entries, err = h.time.ListByUser(ctx, filterUser, rangeFrom, rangeTo)
		} else if isFullAdmin {
			entries, err = h.time.ListAll(ctx, rangeFrom, rangeTo)
		} else {
			entries, err = h.time.ListByUserIDs(ctx, scopeIDs, rangeFrom, rangeTo)
		}
		if err == nil {
			for _, e := range entries {
				view := timeEntryView(e)
				// Wer nur ueber Unteruser (nicht per Rolle) Zugriff hat,
				// darf fremde Zeiten auswerten, aber nicht bearbeiten -
				// nur eigene Eintraege bleiben editierbar (TimeEditWeb/
				// TimeDeleteWeb pruefen serverseitig ohnehin unabhaengig).
				view.CanEdit = isFullAdmin || e.UserID == u.ID
				data.Entries = append(data.Entries, view)
				if e.StartedAt.Format("2006-01-02") == now.Format("2006-01-02") {
					data.TodayCount++
				}
				if view.Running && e.UserID == u.ID && data.Running == nil {
					running := view
					data.Running = &running
				}
			}
		}
	} else {
		if entries, err := h.time.ListByUser(ctx, u.ID, rangeFrom, rangeTo); err == nil {
			data.TodayCount = len(entries)
			for _, e := range entries {
				view := timeEntryView(e)
				view.CanEdit = true
				data.Entries = append(data.Entries, view)
				if view.Running && data.Running == nil {
					running := view
					data.Running = &running
				}
			}
		}
	}

	for i := range data.Entries {
		data.Entries[i].Mine = data.Entries[i].userID == u.ID
	}
	if pending, err := h.time.ListPending(ctx, u.ID); err == nil {
		for _, e := range pending {
			v := timeEntryView(e)
			v.Mine = true
			data.PendingEntries = append(data.PendingEntries, v)
		}
	}

	if running, err := h.time.GetRunning(ctx, u.ID); err == nil && running != nil && data.Running == nil {
		view := timeEntryView(running)
		data.Running = &view
	}
	h.render(w, "timetracking", data)
}

func (h *Handler) simplePage(w http.ResponseWriter, r *http.Request, page, title, ctxTitle string) {
	h.render(w, "simple", struct{ BaseData }{h.baseData(r, page, title, ctxTitle)})
}

// ── Auth ──────────────────────────────────────────────────────

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	d := h.loginDataFor(r, "")
	if n := loginNext(r.URL.Query().Get("next")); n != "/" {
		d.Next = n
	}
	h.render(w, "login", d)
}

// loginNext: Ziel nach der Anmeldung – nur Pfade im PDH (kein "//host",
// kein "/\host"), sonst die Startseite.
func loginNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\r\n") || strings.HasPrefix(next, "/login") || len(next) > 500 {
		return "/"
	}
	return next
}

func (h *Handler) LoginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	token, user, err := h.users.Login(r.Context(), r.FormValue("email"), r.FormValue("password"))
	if err != nil {
		authLog(r, false, "passwort", r.FormValue("email"), "", "", err.Error())
		d := h.loginDataFor(r, "Ungültige Anmeldedaten")
		if n := loginNext(r.FormValue("next")); n != "/" {
			d.Next = n
		}
		h.render(w, "login", d)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: user.ID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	authLog(r, true, "passwort", r.FormValue("email"), user.ID, strings.TrimSpace(user.FirstName+" "+user.LastName), "")
	http.Redirect(w, r, loginNext(r.FormValue("next")), http.StatusFound)
}

// LoginRFIDWeb meldet per RFID-Karten-UID an (kein Passwort - der Besitz
// der Karte ist der Anmeldefaktor). Gängige RFID-Lesegeräte an
// Werkstatt-Terminals emulieren eine Tastatur und tippen die UID gefolgt
// von Enter, daher reicht ein einfaches Formularfeld im Frontend.
func (h *Handler) LoginRFIDWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := strings.TrimSpace(r.FormValue("uid"))
	if uid == "" {
		h.render(w, "login", h.loginDataFor(r, "Keine Karte erkannt"))
		return
	}
	token, user, err := h.users.LoginByRFID(r.Context(), uid)
	if err != nil {
		authLog(r, false, "rfid", "Karte "+maskUID(uid), "", "", err.Error())
		d := h.loginDataFor(r, "Unbekannte Karte")
		if n := loginNext(r.FormValue("next")); n != "/" {
			d.Next = n
		}
		h.render(w, "login", d)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: user.ID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	authLog(r, true, "rfid", "Karte "+maskUID(uid), user.ID, strings.TrimSpace(user.FirstName+" "+user.LastName), "")
	http.Redirect(w, r, loginNext(r.FormValue("next")), http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if uid := h.requestActor(r); uid != "" {
		log.Info().Str("bereich", "auth").Str("user", uid).Str("user_name", h.cachedUserName(r.Context(), uid)).Str("ip", r.RemoteAddr).Msg("abmeldung")
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/global/", http.StatusFound)
}

// sessionUserID liest die pdh_token-Cookie und validiert das JWT, ohne wie
// authMiddleware bei fehlender/ungueltiger Anmeldung umzuleiten - fuer
// Stellen (z.B. die oeffentliche Leitstand-Seite), die nur wissen muessen,
// ob bereits eine gueltige Sitzung besteht.
func (h *Handler) sessionUserID(r *http.Request) string {
	cookie, err := r.Cookie("pdh_token")
	if err != nil || cookie.Value == "" {
		return ""
	}
	token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unerwartete signaturmethode")
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil {
		log.Warn().Err(err).Str("path", r.URL.Path).Msg("sitzung: jwt ungueltig")
		return ""
	}
	if !token.Valid {
		log.Warn().Str("path", r.URL.Path).Msg("sitzung: jwt als ungueltig markiert")
		return ""
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		log.Warn().Str("path", r.URL.Path).Msg("sitzung: jwt-claims unlesbar")
		return ""
	}
	if scope, _ := claims["scope"].(string); scope != "" {
		// zweckgebundene Tokens (z. B. Fertigmeldung im Leitstand) sind keine Sitzung
		log.Warn().Str("path", r.URL.Path).Str("scope", scope).Msg("sitzung: zweckgebundenes token als cookie abgelehnt")
		return ""
	}
	userID, _ := claims["sub"].(string)
	if userID == "" {
		log.Warn().Str("path", r.URL.Path).Msg("sitzung: jwt ohne sub-claim")
	}
	return userID
}

// sessionUser validiert die Sitzung vollstaendig: gueltiges JWT UND ein
// dazu ladbarer, aktiver Benutzer. Ein syntaktisch gueltiges JWT reicht
// nicht - das Konto kann zwischenzeitlich deaktiviert oder geloescht
// worden sein (GetByID liefert dann keinen Treffer). Liefert nil, wenn
// keine gueltige Sitzung besteht.
func (h *Handler) sessionUser(r *http.Request) *users.User {
	userID := h.sessionUserID(r)
	if userID == "" {
		return nil
	}
	user, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		log.Warn().Err(err).Str("path", r.URL.Path).Str("user_id", userID).Msg("sitzung: benutzer nicht ladbar (deaktiviert/geloescht?)")
		return nil
	}
	return user
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Anmeldeseite samt RFID-Anmeldung und Passwort vergessen/zuruecksetzen sind oeffentlich
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/login/") || r.URL.Path == "/lang" {
			next.ServeHTTP(w, r)
			return
		}
		// Fertigmeldung aus dem Leitstand: RFID-Token nur fuer /complete/{type}/{id}
		// (global_dashboard_complete.go) – hat Vorrang vor einer evtl. Sitzung am Terminal
		if bu := h.boardCompleteUser(r); bu != nil {
			ctx := context.WithValue(r.Context(), "user", bu)
			ctx = context.WithValue(ctx, boardCompleteKey{}, true)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		user := h.sessionUser(r)
		if user == nil {
			// Cookie in jedem Fall loeschen - auch wenn das JWT selbst noch
			// gueltig war, aber der Benutzer nicht mehr geladen werden kann
			// (deaktiviert/geloescht). Sonst haengt der Browser dauerhaft an
			// einem Token fest, das nie wieder zu "/" durchkommt.
			http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: "", Path: "/", MaxAge: -1})
			http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: "", Path: "/", MaxAge: -1})
			http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: "", Path: "/", MaxAge: -1})
			// QR-Code gescannt: erst anmelden, dann zurueck auf die Infoseite
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/a/") {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			http.Redirect(w, r, "/global/", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), "user", user)
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}

// ── Fault Detail ─────────────────────────────────────────────

type FaultDetailData struct {
	BaseData
	Fault         FaultDetailView
	Analysis      *faults.CopilotAnalysis
	TimeEntries   []*timetracking.TimeEntry
	RunningTime   *timetracking.TimeEntry
	SimilarFaults []SimilarFaultView
	Users         []UserOption
	History       []HistoryView
}

type FaultDetailView struct {
	ID               string
	Title            string
	Description      string
	Symptoms         []string
	Status           string
	StatusLabel      string
	StatusClass      string
	Severity         string
	SeverityClass    string
	InfraID          string
	InfraName        string
	DetectedAgo      string
	Resolution       string
	RootCause        string
	AssignedID       string
	ResponsibleID    string
	AssignedName     string
	ResponsibleName  string
	RecordImageURL   string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	DueDate          string
	DueDateISO       string
}

type SimilarFaultView struct {
	ID         string
	Title      string
	Resolution string
	Percent    int // berechnete Aehnlichkeit
}

func (h *Handler) FaultDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	fault, err := h.faults.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/faults", http.StatusFound)
		return
	}

	u := getUser(r)
	people := h.recordPeople(ctx, "fault", id)
	data := FaultDetailData{
		BaseData: BaseData{
			Title: fault.Title, Page: "faults",
			ContextTitle:  "Ähnliche Störungen",
			UserName:      u.FirstName + " " + u.LastName,
			UserFirstName: u.FirstName, UserLastName: u.LastName,
			FaultID: id,
		},
		Users:   h.userOptions(ctx),
		History: h.recordHistory(ctx, "fault", id),
		Fault: FaultDetailView{
			ID: fault.ID, Title: fault.Title,
			Description:      fault.Description,
			Symptoms:         fault.Symptoms,
			Status:           string(fault.Status),
			StatusLabel:      statusLabel(string(fault.Status)),
			StatusClass:      statusClass(string(fault.Status)),
			Severity:         string(fault.Severity),
			SeverityClass:    severityClass(string(fault.Severity)),
			InfraName:        h.infraName(ctx, fault.InfrastructureID),
			DetectedAgo:      timeAgo(fault.DetectedAt),
			AssignedID:       people.AssignedID,
			ResponsibleID:    people.ResponsibleID,
			AssignedName:     people.AssignedName,
			ResponsibleName:  people.ResponsibleName,
			RecordImageURL:   h.recordImageURL(ctx, "fault", id),
			CostCenterNumber: fault.CostCenterNumber,
			CostCenterName:   fault.CostCenterName,
		},
	}
	if fault.InfrastructureID != nil {
		data.Fault.InfraID = *fault.InfrastructureID
	}
	if fault.CostCenterID != nil {
		data.Fault.CostCenterID = *fault.CostCenterID
	}
	if fault.Resolution != nil {
		data.Fault.Resolution = *fault.Resolution
	}
	if fault.RootCause != nil {
		data.Fault.RootCause = *fault.RootCause
	}
	if fault.DueDate != nil {
		data.Fault.DueDate = fault.DueDate.Format("02.01.2006")
		data.Fault.DueDateISO = fault.DueDate.Format("2006-01-02")
	}

	// Analyse laden
	var similar []faults.SimilarFault
	if a, err := h.faults.GetAnalysis(ctx, id); err == nil {
		data.Analysis = a
		similar = a.SimilarFaults
	}
	if len(similar) == 0 { // auch ohne Copilot-Analyse aehnliche geloeste Faelle zeigen
		similar, _ = h.faults.SimilarFaults(ctx, id, 5)
	}
	for _, s := range similar {
		data.SimilarFaults = append(data.SimilarFaults, SimilarFaultView{
			ID: s.ID, Title: s.Title, Resolution: s.Resolution, Percent: int(s.Similarity*100 + 0.5),
		})
	}

	// Zeiteinträge
	if entries, err := h.time.ListByRef(ctx, timetracking.RefFault, id); err == nil {
		data.TimeEntries = entries
	}

	// Läuft gerade?
	if running, err := h.time.GetRunning(ctx, u.ID); err == nil && running != nil {
		data.RunningTime = running
	}

	h.render(w, "fault_detail", data)
}

func (h *Handler) FaultResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	w.Header().Set("Content-Type", "text/html")
	if err := h.faults.Resolve(r.Context(), id, r.FormValue("resolution"), r.FormValue("root_cause"), u.ID, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);padding:10px;text-align:center;font-size:13px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);padding:12px;text-align:center"><i class="ti ti-check"></i> Störung gelöst! <a href="/faults" style="color:var(--accent)">Zurück zur Liste</a></div>`)
}

func (h *Handler) FaultInfrastructureWeb(w http.ResponseWriter, r *http.Request) {
	h.updateInfrastructureWeb(w, r, "fault")
}

func (h *Handler) FaultStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefFault,
		RefID:       id,
		Description: "Entstörung vor Ort",
	}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;margin-top:8px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;margin-top:8px"><i class="ti ti-check"></i> Zeit gestartet (ID: %s...)</div>`, esc(entry.ID[:8]))
}

// ── Maintenance Page ─────────────────────────────────────────

type MaintenancePageData struct {
	BaseData
	Tabs         []ListTab
	TotalPlans   int
	OpenTasks    int
	Today        string
	Plans        []MaintPlanView
	Tasks        []MaintTaskView
	DueTasks     []MaintTaskView
	InfraOptions []InfraOption
	Filter       string
	Users        []UserOption
}

type InfraOption struct{ ID, Name string }

type MaintPlanView struct {
	ID              string
	Name            string
	InfraID         string
	InfraName       string
	TypeValue       string
	TypeLabel       string
	IntervalValue   string
	IntervalDays    int
	IntervalLabel   string
	NextDue         string
	NextDueISO      string
	Priority        string
	PriorityDot     string
	AssignedID      string
	AssigneeName    string
	ResponsibleID   string
	ResponsibleName string
}

type MaintTaskView struct {
	ID            string
	Title         string
	InfraName     string
	Status        string
	StatusLabel   string
	StatusClass   string
	Priority      string
	PriorityClass string
	PriorityDot   string
	DueDate       string
}

func maintTaskView(t *maintenance.MaintenanceTask) MaintTaskView {
	return MaintTaskView{
		ID: t.ID, Title: t.Title, InfraName: t.InfraName,
		Status: string(t.Status), StatusLabel: statusLabel(string(t.Status)),
		StatusClass: statusClass(string(t.Status)),
		Priority:    string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot: priorityDot(string(t.Priority)),
		DueDate:     t.DueDate.Format("02.01."),
	}
}

func (h *Handler) Maintenance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := maintenance.TaskStatus(r.URL.Query().Get("status"))

	data := MaintenancePageData{
		BaseData: h.baseData(r, "maintenance", "Wartungsplanung", "Fällige Aufträge"),
		Today:    time.Now().Format("2006-01-02"),
		Filter:   string(status),
		Users:    h.userOptions(ctx),
	}
	data.Tabs = h.statusTabs(ctx, "maintenance_tasks", "/maintenance", string(status), []statusTabDef{
		{"open", "Offen", "ti-circle"}, {"in_progress", "In Arbeit", "ti-tool"}, {"done", "Erledigt", "ti-check"}, {"skipped", "Übersprungen", "ti-player-skip-forward"},
	}, false)
	data.Tabs[0].Label = "Aktuell" // ohne Filter zeigt die Seite offene und laufende Auftraege
	data.Tabs[0].Count = data.Tabs[1].Count + data.Tabs[2].Count

	if plans, err := h.maint.ListPlans(ctx, ""); err == nil {
		data.TotalPlans = len(plans)
		intervalLabels := map[maintenance.Interval]string{
			"daily": "Täglich", "weekly": "Wöchentlich", "monthly": "Monatlich",
			"quarterly": "Quartalsweise", "yearly": "Jährlich",
		}
		typeLabels := map[maintenance.PlanType]string{
			"preventive": "Vorbeugend", "inspection": "Inspektion",
			"calibration": "Kalibrierung", "cleaning": "Reinigung",
		}
		planScope := h.scopeAllowedIDs(r, "maintenance_plan")
		for _, p := range plans {
			if planScope != nil && !planScope[p.ID] {
				continue
			}
			data.Plans = append(data.Plans, MaintPlanView{
				ID: p.ID, Name: p.Name, InfraID: p.InfrastructureID, InfraName: p.InfraName,
				TypeValue:       string(p.Type),
				TypeLabel:       typeLabels[p.Type],
				IntervalValue:   string(p.Interval),
				IntervalDays:    p.IntervalDays,
				IntervalLabel:   intervalLabels[p.Interval],
				NextDue:         p.NextDueAt.Format("02.01.2006"),
				NextDueISO:      p.NextDueAt.Format("2006-01-02"),
				Priority:        string(p.Priority),
				PriorityDot:     priorityDot(string(p.Priority)),
				AssignedID:      derefOr(p.AssignedTo, ""),
				AssigneeName:    p.AssigneeName,
				ResponsibleID:   derefOr(p.ResponsibleTo, ""),
				ResponsibleName: p.ResponsibleName,
			})
		}
	}

	tagIDs := h.categoryFilterIDs(r, "maintenance_task")
	scopeIDs := h.scopeAllowedIDs(r, "maintenance_task")
	if tasks, err := h.maint.ListTasks(ctx, status, ""); err == nil {
		for _, t := range tasks {
			if tagIDs != nil && !tagIDs[t.ID] || scopeIDs != nil && !scopeIDs[t.ID] {
				continue
			}
			if t.Status == "open" {
				data.OpenTasks++
			}
			if status == "" && t.Status != "open" && t.Status != "in_progress" {
				continue
			}
			data.Tasks = append(data.Tasks, maintTaskView(t))
		}
	}

	if due, err := h.maint.GetDueToday(ctx); err == nil {
		for _, t := range due {
			if tagIDs != nil && !tagIDs[t.ID] {
				continue
			}
			data.DueTasks = append(data.DueTasks, maintTaskView(t))
		}
	}

	if infra, err := h.infra.List(ctx, nil, ""); err == nil {
		for _, i := range infra {
			data.InfraOptions = append(data.InfraOptions, InfraOption{ID: i.ID, Name: i.Name})
		}
	}

	h.render(w, "maintenance", data)
}

func (h *Handler) MaintenancePlanDuplicateWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if _, err := h.maint.DuplicatePlan(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		http.Error(w, "Wartungsplan konnte nicht dupliziert werden: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) MaintenanceGenerate(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	count, err := h.maint.GenerateTasks(r.Context(), u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;padding:8px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px;background:rgba(16,185,129,.1);border-radius:8px;margin-bottom:12px"><i class="ti ti-check"></i> %d Aufträge generiert</div>`, count)
}

type MaintenanceTaskDetailData struct {
	BaseData
	Task           MaintenanceTaskDetailView
	TimeEntries    []*timetracking.TimeEntry
	Running        *timetracking.TimeEntry
	ChecklistItems []struct{}
	Users          []UserOption
	History        []HistoryView
}

type MaintenanceTaskDetailView struct {
	ID              string
	Title           string
	Description     string
	TypeLabel       string
	InfraID         string
	InfraName       string
	Priority        string
	PriorityClass   string
	PriorityDot     string
	Status          string
	StatusLabel     string
	StatusClass     string
	DueDate         string
	DueDateISO      string
	StartedAt       string
	CompletedAt     string
	DurationStr     string
	Notes           string
	AssignedID      string
	ResponsibleID   string
	AssigneeName    string
	ResponsibleName string
	RecordImageURL  string
	CreatedAt       string
	CanStart        bool
	CanComplete     bool
}

func maintenanceTypeLabel(t maintenance.PlanType) string {
	labels := map[maintenance.PlanType]string{
		"preventive": "Vorbeugend", "inspection": "Inspektion",
		"calibration": "Kalibrierung", "cleaning": "Reinigung",
	}
	if label, ok := labels[t]; ok {
		return label
	}
	return string(t)
}

func maintenanceTaskDetailView(t *maintenance.MaintenanceTask) MaintenanceTaskDetailView {
	v := MaintenanceTaskDetailView{
		ID:            t.ID,
		Title:         t.Title,
		Description:   t.Description,
		TypeLabel:     maintenanceTypeLabel(t.Type),
		InfraID:       t.InfrastructureID,
		InfraName:     t.InfraName,
		Priority:      string(t.Priority),
		PriorityClass: priorityClass(string(t.Priority)),
		PriorityDot:   priorityDot(string(t.Priority)),
		Status:        string(t.Status),
		StatusLabel:   statusLabel(string(t.Status)),
		StatusClass:   statusClass(string(t.Status)),
		DueDate:       t.DueDate.Format("02.01.2006"),
		DueDateISO:    t.DueDate.Format("2006-01-02"),
		Notes:         t.Notes,
		AssigneeName:  t.AssigneeName,
		CreatedAt:     t.CreatedAt.Format("02.01.2006 15:04"),
		CanStart:      t.Status == maintenance.TaskOpen,
		CanComplete:   t.Status == maintenance.TaskOpen || t.Status == maintenance.TaskInProgress,
	}
	if t.StartedAt != nil {
		v.StartedAt = t.StartedAt.Format("02.01.2006 15:04")
	}
	if t.CompletedAt != nil {
		v.CompletedAt = t.CompletedAt.Format("02.01.2006 15:04")
	}
	if t.DurationMin != nil {
		v.DurationStr = durationText(*t.DurationMin)
	}
	return v
}

func (h *Handler) MaintenanceTaskDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.maint.GetTaskByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/maintenance", http.StatusFound)
		return
	}

	u := getUser(r)
	data := MaintenanceTaskDetailData{
		BaseData: h.baseData(r, "maintenance", task.Title, "Auftrag"),
		Task:     maintenanceTaskDetailView(task),
		Users:    h.userOptions(r.Context()),
		History:  h.recordHistory(r.Context(), "maintenance_task", id),
	}
	people := h.recordPeople(r.Context(), "maintenance_task", id)
	data.Task.AssignedID = people.AssignedID
	data.Task.ResponsibleID = people.ResponsibleID
	data.Task.AssigneeName = people.AssignedName
	data.Task.ResponsibleName = people.ResponsibleName
	data.Task.RecordImageURL = h.recordImageURL(r.Context(), "maintenance_task", id)
	if entries, err := h.time.ListByRef(r.Context(), timetracking.RefMaintenance, id); err == nil {
		data.TimeEntries = entries
	}
	if running, err := h.time.GetRunning(r.Context(), u.ID); err == nil && running != nil &&
		running.RefType == timetracking.RefMaintenance && running.RefID == id {
		data.Running = running
	}
	h.render(w, "maintenance_detail", data)
}

func (h *Handler) MaintenanceTaskStartWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	if err := h.maint.StartTask(r.Context(), chi.URLParam(r, "id"), u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Gestartet</div>`)
}

func (h *Handler) MaintenanceTaskCompleteWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(32 << 20)
	u := getUser(r)
	duration, _ := strconv.Atoi(r.FormValue("duration_min"))
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	in := &maintenance.CompleteTaskInput{
		Notes:       r.FormValue("notes"),
		DurationMin: duration,
	}
	w.Header().Set("Content-Type", "text/html")
	if err := h.maint.CompleteTaskValidated(r.Context(), chi.URLParam(r, "id"), u.ID, in, noPartsNeeded); err != nil {
		log.Error().Err(err).Str("task_id", chi.URLParam(r, "id")).Msg("maintenance complete-web fehlgeschlagen")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Abgeschlossen</div>`)
}

func (h *Handler) MaintenanceTaskEditWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	in := &maintenance.UpdateTaskInput{
		Title:            r.FormValue("title"),
		Description:      r.FormValue("description"),
		InfrastructureID: strings.TrimSpace(r.FormValue("infrastructure_id")),
		Priority:         maintenance.Priority(r.FormValue("priority")),
		DueDate:          r.FormValue("due_date"),
		Notes:            r.FormValue("notes"),
		CostCenterID:     optionalID(r.FormValue("cost_center_id")),
	}
	if err := h.maint.UpdateTask(r.Context(), chi.URLParam(r, "id"), in); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<div style="color:var(--green);font-size:12px">Gespeichert</div>`)
}

func (h *Handler) MaintenanceTaskDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if err := h.maint.DeleteTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/maintenance", http.StatusFound)
}

func (h *Handler) MaintenanceTaskStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefMaintenance,
		RefID:       id,
		Description: "Wartungsauftrag bearbeiten",
	}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px">Zeit gestartet (%s)</div>`, entry.ID[:8])
}

func (h *Handler) MaintenanceTaskChecklistWeb(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/maintenance/tasks/"+chi.URLParam(r, "id"), http.StatusFound)
}

// ── Shifts Page ───────────────────────────────────────────────

type ShiftsPageData struct {
	BaseData
	WeekNumber           int
	WeekRange            string
	PrevWeek             string
	NextWeek             string
	Days                 []WeekDay
	Users                []shifts.UserWeekPlan
	ShiftDefs            []ShiftDefView
	Absences             []AbsenceView
	ShiftMap             []ShiftEntry
	LocksmithSlot1Phone  string
	LocksmithSlot2Phone  string
	LocksmithSlot1TeamID string
	LocksmithSlot2TeamID string
	LocksmithSlot1UserID string
	LocksmithSlot2UserID string
	UserOptions          []UserOption
}

type WeekDay struct {
	Short    string
	Date     string
	DateFull string
}

type ShiftDefView struct {
	ID        string
	Name      string
	ShortName string
	StartTime string
	EndTime   string
	Class     string
}

type AbsenceView struct {
	UserName    string
	TypeLabel   string
	TypeDot     string
	StartDate   string
	EndDate     string
	Days        int
	StatusLabel string
	StatusClass string
}

type ShiftEntry struct {
	UserID string
	Date   string
	Label  string
	Class  string
	Title  string
}

func (h *Handler) Shifts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	weekParam := r.URL.Query().Get("week")
	if weekParam == "" {
		weekParam = time.Now().Format("2006-01-02")
	}

	t, _ := time.Parse("2006-01-02", weekParam)
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := t.AddDate(0, 0, -(wd - 1))
	sunday := monday.AddDate(0, 0, 6)
	_, week := monday.ISOWeek()

	dayNames := []string{"Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"}
	data := ShiftsPageData{
		BaseData:   h.baseData(r, "shifts", "Schichtplanung", "Abwesenheiten"),
		WeekNumber: week,
		WeekRange:  monday.Format("02.01") + "–" + sunday.Format("02.01.2006"),
		PrevWeek:   monday.AddDate(0, 0, -7).Format("2006-01-02"),
		NextWeek:   monday.AddDate(0, 0, 7).Format("2006-01-02"),
	}

	for d := 0; d < 7; d++ {
		day := monday.AddDate(0, 0, d)
		data.Days = append(data.Days, WeekDay{
			Short:    dayNames[d],
			Date:     day.Format("02.01"),
			DateFull: day.Format("2006-01-02"),
		})
	}

	shiftClasses := map[string]string{"F": "sf", "S": "ss", "N": "sn"}

	if wp, err := h.shifts.GetWeekPlan(ctx, monday.Format("2006-01-02")); err == nil && wp != nil {
		data.Users = wp.Users
		for _, u := range wp.Users {
			for date, entry := range u.Days {
				class := shiftClasses[entry.ShortName]
				if class == "" {
					class = "se"
				}
				data.ShiftMap = append(data.ShiftMap, ShiftEntry{
					UserID: u.UserID, Date: date,
					Label: entry.ShortName, Class: class,
				})
			}
		}
	}

	data.UserOptions = h.userOptions(ctx)
	for _, u := range data.Users {
		if u.ShiftLocksmith1 {
			data.LocksmithSlot1UserID = u.UserID
		}
		if u.ShiftLocksmith2 {
			data.LocksmithSlot2UserID = u.UserID
		}
	}

	if assignments, err := h.shifts.GetLocksmithAssignments(ctx); err == nil {
		for _, a := range assignments {
			if a.Slot == 1 {
				data.LocksmithSlot1Phone = a.Phone
				if a.TeamID != nil {
					data.LocksmithSlot1TeamID = *a.TeamID
				}
			} else if a.Slot == 2 {
				data.LocksmithSlot2Phone = a.Phone
				if a.TeamID != nil {
					data.LocksmithSlot2TeamID = *a.TeamID
				}
			}
		}
	}

	if models, err := h.shifts.ListModels(ctx); err == nil && len(models) > 0 {
		if defs, err := h.shifts.ListShifts(ctx, models[0].ID); err == nil {
			for _, d := range defs {
				class := shiftClasses[d.ShortName]
				data.ShiftDefs = append(data.ShiftDefs, ShiftDefView{
					ID:   d.ID,
					Name: d.Name, ShortName: d.ShortName,
					StartTime: d.StartTime, EndTime: d.EndTime, Class: class,
				})
			}
		}
	}

	absTypeLabels := map[string]string{"vacation": "Urlaub", "sick": "Krank", "training": "Schulung", "other": "Sonstiges"}
	absTypeDots := map[string]string{"vacation": "d-blue", "sick": "d-red", "training": "d-green", "other": "d-amber"}
	if abs, err := h.shifts.ListAbsences(ctx, "", ""); err == nil {
		for _, a := range abs {
			data.Absences = append(data.Absences, AbsenceView{
				UserName:    a.UserName,
				TypeLabel:   absTypeLabels[string(a.Type)],
				TypeDot:     absTypeDots[string(a.Type)],
				StartDate:   a.StartDate,
				EndDate:     a.EndDate,
				Days:        a.Days,
				StatusLabel: statusLabel(string(a.Status)),
				StatusClass: statusClass(string(a.Status)),
			})
		}
	}
	if rows, err := h.db.Query(ctx, `
		SELECT user_id::text, starts_at, ends_at FROM microsoft_calendar_blocks
		WHERE starts_at < $2 AND ends_at > $1`, monday, sunday.AddDate(0, 0, 1)); err == nil {
		defer rows.Close()
		marked := make(map[string]struct{})
		for rows.Next() {
			var userID string
			var startsAt, endsAt time.Time
			if rows.Scan(&userID, &startsAt, &endsAt) != nil || !endsAt.After(startsAt) {
				continue
			}
			localStart := startsAt.In(time.Local)
			localEnd := endsAt.Add(-time.Nanosecond).In(time.Local)
			for day := time.Date(localStart.Year(), localStart.Month(), localStart.Day(), 0, 0, 0, 0, time.Local); !day.After(localEnd); day = day.AddDate(0, 0, 1) {
				date := day.Format("2006-01-02")
				key := userID + ":" + date
				if _, exists := marked[key]; exists {
					continue
				}
				marked[key] = struct{}{}
				data.ShiftMap = append(data.ShiftMap, ShiftEntry{UserID: userID, Date: date, Label: "O", Class: "outlook-busy", Title: "Outlook belegt"})
			}
		}
	}

	h.render(w, "shifts", data)
}

// ── Ticket Detail ─────────────────────────────────────────────

type TicketDetailData struct {
	BaseData
	Ticket         TicketView
	Comments       []CommentView
	CommentCount   int
	TimeEntries    []*timetracking.TimeEntry
	RunningTime    *timetracking.TimeEntry
	StatusOptions  []StatusOption
	RelatedTickets []TicketView
	Users          []UserOption
	History        []HistoryView
}

type CommentView struct {
	ID         string
	UserName   string
	Text       string
	CreatedAgo string
}

type StatusOption struct {
	Value string
	Label string
}

func (h *Handler) TicketDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u := getUser(r)

	t, err := h.tickets.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/tickets", http.StatusFound)
		return
	}

	data := TicketDetailData{
		BaseData: h.baseData(r, "tickets", t.Title, "Ähnliche Tickets"),
		Users:    h.userOptions(ctx),
		History:  h.recordHistory(ctx, "ticket", id),
		Ticket: TicketView{
			ID: t.ID, Title: t.Title, Description: t.Description,
			InfraName: h.infraName(ctx, t.InfrastructureID),
			Priority:  string(t.Priority), PriorityClass: priorityClass(string(t.Priority)),
			PriorityDot: priorityDot(string(t.Priority)),
			Status:      string(t.Status), StatusLabel: statusLabel(string(t.Status)),
			StatusClass:      statusClass(string(t.Status)),
			CreatedAgo:       timeAgo(t.CreatedAt),
			CostCenterNumber: t.CostCenterNumber,
			CostCenterName:   t.CostCenterName,
		},
		StatusOptions: []StatusOption{
			{"open", "Offen"}, {"in_progress", "In Arbeit"},
			{"resolved", "Gelöst"}, {"closed", "Geschlossen"},
		},
	}
	if t.InfrastructureID != nil {
		data.Ticket.InfraID = *t.InfrastructureID
	}
	if t.CostCenterID != nil {
		data.Ticket.CostCenterID = *t.CostCenterID
	}
	if t.DueDate != nil {
		data.Ticket.DueDate = t.DueDate.Format("02.01.2006")
		data.Ticket.DueDateISO = t.DueDate.Format("2006-01-02")
	}
	people := h.recordPeople(ctx, "ticket", id)
	data.Ticket.AssignedID = people.AssignedID
	data.Ticket.ResponsibleID = people.ResponsibleID
	data.Ticket.AssignedName = people.AssignedName
	data.Ticket.ResponsibleName = people.ResponsibleName
	data.Ticket.RecordImageURL = h.recordImageURL(ctx, "ticket", id)

	if running, err := h.time.GetRunning(ctx, u.ID); err == nil {
		data.RunningTime = running
	}
	if entries, err := h.time.ListByRef(ctx, timetracking.RefTicket, id); err == nil {
		data.TimeEntries = entries
	}
	if tl, err := h.tickets.List(ctx, "open"); err == nil {
		for _, t2 := range tl {
			if t2.ID != id && len(data.RelatedTickets) < 5 {
				data.RelatedTickets = append(data.RelatedTickets, TicketView{
					ID: t2.ID, Title: t2.Title,
					PriorityDot: priorityDot(string(t2.Priority)),
					StatusLabel: statusLabel(string(t2.Status)),
					StatusClass: statusClass(string(t2.Status)),
				})
			}
		}
	}
	h.render(w, "ticket_detail", data)
}

func (h *Handler) TicketAddComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	r.ParseForm()
	c, err := h.tickets.AddComment(r.Context(), id, u.ID, r.FormValue("text"))
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler</div>`)
		return
	}
	initial := "?"
	if r := []rune(u.FirstName); len(r) > 0 {
		initial = string(r[:1])
	}
	fmt.Fprintf(w, `<div style="display:flex;gap:10px;padding:10px 0;border-bottom:1px solid var(--border)">
		<div style="width:30px;height:30px;background:var(--accent);border-radius:50%%;display:flex;align-items:center;justify-content:center;font-size:12px;font-weight:600;flex-shrink:0;color:#fff">%s</div>
		<div><div style="font-size:12px;font-weight:500">%s · <span style="color:var(--muted);font-weight:400">gerade eben</span></div>
		<div style="font-size:13px;margin-top:4px">%s</div></div></div>`,
		esc(initial), esc(u.FirstName+" "+u.LastName), esc(c.Text))
}

func (h *Handler) TicketStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := tickets.Status(r.FormValue("status"))
	u := getUser(r)
	w.Header().Set("Content-Type", "text/html")
	if err := h.tickets.UpdateStatus(r.Context(), id, status, u.ID); err != nil {
		fmt.Fprintf(w, `<span class="badge b-red" title="%s">Nicht möglich</span>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClass(string(status)), statusLabel(string(status)))
}

func (h *Handler) TicketResolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	noPartsNeeded := r.FormValue("no_parts_needed") == "on"
	w.Header().Set("Content-Type", "text/html")
	if err := h.tickets.Resolve(r.Context(), id, r.FormValue("resolution"), r.FormValue("root_cause"), u.ID, noPartsNeeded); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);padding:10px;text-align:center;font-size:13px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);padding:12px;text-align:center"><i class="ti ti-check"></i> Ticket gelöst! <a href="/tickets" style="color:var(--accent)">Zurück zur Liste</a></div>`)
}

func (h *Handler) TicketInfrastructureWeb(w http.ResponseWriter, r *http.Request) {
	h.updateInfrastructureWeb(w, r, "ticket")
}

func (h *Handler) TicketStartTime(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	in := &timetracking.CreateEntryInput{RefType: timetracking.RefTicket, RefID: id, Description: "Ticket bearbeiten"}
	entry, err := h.time.Start(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;margin-top:8px">⏱ Zeit gestartet (%s)</div>`, entry.ID[:8])
}

// ── Inventory Detail ──────────────────────────────────────────

func (h *Handler) InventoryBookWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	qty, _ := strconv.ParseFloat(r.FormValue("qty"), 64)
	in := &inventory.BookMovementInput{
		PartID:        r.FormValue("part_id"),
		Type:          inventory.MovementType(r.FormValue("type")),
		Qty:           qty,
		StorageNodeID: r.FormValue("storage_node_id"),
		Reference:     r.FormValue("reference"),
		Notes:         r.FormValue("notes"),
	}
	mv, err := h.inv.Book(r.Context(), in, u.ID)
	err = triggerError(err)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	typeLabel := map[inventory.MovementType]string{"in": "Zugang", "out": "Abgang", "correction": "Korrektur", "inventory": "Inventur"}
	fmt.Fprintf(w, `<div style="display:flex;align-items:center;gap:12px;padding:8px 0;border-bottom:1px solid var(--border);font-size:13px">
		<div style="font-weight:500;flex:1">%s · Bestand: %.2f</div>
		<div style="font-weight:600;color:var(--green)">%.2f</div></div>`,
		typeLabel[mv.Type], mv.QtyAfter, mv.Qty)
}

// ── Infrastructure Page ───────────────────────────────────────

type InfraPageData struct {
	BaseData
	Tree     []InfraNodeView
	AllNodes []InfraNodeView
	Stats    map[string]int
}

type InfraNodeView struct {
	ID               string
	Name             string
	TypeLabel        string
	TypeIcon         string
	TypeBg           string
	Location         string
	Manufacturer     string
	ManufacturerID   string
	ManufacturerName string
	SerialNo         string
	CostCenterID     string
	CostCenterNumber string
	CostCenterName   string
	Children         []InfraNodeView
}

func infraNodeView(i *infrastructure.Infrastructure) InfraNodeView {
	icons := map[infrastructure.InfraType]string{
		"building": "🏭", "line": "🔄", "plant": "⚙️", "device": "🔌",
	}
	labels := map[infrastructure.InfraType]string{
		"building": "Gebäude", "line": "Linie", "plant": "Anlage", "device": "Gerät",
	}
	bgs := map[infrastructure.InfraType]string{
		"building": "rgba(99,102,241,.2)", "line": "rgba(79,110,247,.2)",
		"plant": "rgba(16,185,129,.2)", "device": "rgba(245,158,11,.2)",
	}
	v := InfraNodeView{
		ID: i.ID, Name: i.Name,
		TypeLabel: labels[i.Type], TypeIcon: icons[i.Type],
		TypeBg:   bgs[i.Type],
		Location: i.Location, Manufacturer: i.Manufacturer, ManufacturerName: i.ManufacturerName,
		SerialNo:         i.SerialNo,
		CostCenterNumber: i.CostCenterNumber, CostCenterName: i.CostCenterName,
	}
	if i.CostCenterID != nil {
		v.CostCenterID = *i.CostCenterID
	}
	if i.ManufacturerID != nil {
		v.ManufacturerID = *i.ManufacturerID
	}
	for _, c := range i.Children {
		v.Children = append(v.Children, infraNodeView(c))
	}
	return v
}

func flattenNodes(nodes []InfraNodeView) []InfraNodeView {
	var flat []InfraNodeView
	for _, n := range nodes {
		flat = append(flat, InfraNodeView{ID: n.ID, Name: n.Name, TypeIcon: n.TypeIcon, TypeLabel: n.TypeLabel})
		flat = append(flat, flattenNodes(n.Children)...)
	}
	return flat
}

func (h *Handler) Infrastructure(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := InfraPageData{
		BaseData: h.baseData(r, "infrastructure", "Infrastruktur", "Schnellzugriff"),
	}

	if tree, err := h.infra.GetTree(ctx); err == nil {
		for _, i := range tree {
			data.Tree = append(data.Tree, infraNodeView(i))
		}
		data.AllNodes = flattenNodes(data.Tree)
		if tagIDs := h.categoryFilterIDs(r, "infrastructure"); tagIDs != nil {
			data.Tree = pruneInfraTree(data.Tree, tagIDs) // Pfad zu Treffern bleibt sichtbar
		}
	}

	if stats, err := h.infra.GetStats(ctx); err == nil {
		data.Stats = stats
	}

	h.render(w, "infrastructure", data)
}

func (h *Handler) InfraCreate(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	r.ParseForm()
	parentID := r.FormValue("parent_id")
	in := &infrastructure.CreateInput{
		Name:           r.FormValue("name"),
		Type:           infrastructure.InfraType(r.FormValue("type")),
		Location:       r.FormValue("location"),
		Manufacturer:   r.FormValue("manufacturer"),
		ManufacturerID: optionalID(r.FormValue("manufacturer_id")),
		SerialNo:       r.FormValue("serial_no"),
		Description:    r.FormValue("description"),
		CostCenterID:   optionalID(r.FormValue("cost_center_id")),
	}
	if parentID != "" {
		in.ParentID = &parentID
	}
	h.infra.Create(r.Context(), in)
	h.Infrastructure(w, r)
}

// ── Storage Page ──────────────────────────────────────────────
// Generischer Lagerort-Baum (lagerort -> regal -> fach -> platz), ersetzt die
// frühere feste 3-Ebenen-Struktur (Warehouse -> Location -> Place).

type StorageNodeView struct {
	ID             string
	Name           string
	Type           string
	TypeLabel      string
	TypeIcon       string
	Description    string
	Location       string
	Capacity       string
	CurrentParts   int
	ChildType      string
	ChildTypeLabel string
	Children       []StorageNodeView
}

type StoragePageData struct {
	BaseData
	Tree  []StorageNodeView
	Stats map[string]int
}

var storageTypeIcons = map[storage.NodeType]string{
	storage.TypeLagerort: "🏭",
	storage.TypeRegal:    "🗄️",
	storage.TypeFach:     "📁",
	storage.TypePlatz:    "📦",
}

var storageTypeLabels = map[storage.NodeType]string{
	storage.TypeLagerort: "Lagerort",
	storage.TypeRegal:    "Regal",
	storage.TypeFach:     "Fach",
	storage.TypePlatz:    "Platz",
}

// storageChildTypes: welcher Typ darf als Nächstes unter diesem Typ angelegt
// werden (steuert das "+"-Button-Label im Template). Platz ist die unterste
// Ebene und hat bewusst keinen Eintrag.
var storageChildTypes = map[storage.NodeType]storage.NodeType{
	storage.TypeLagerort: storage.TypeRegal,
	storage.TypeRegal:    storage.TypeFach,
	storage.TypeFach:     storage.TypePlatz,
}

func storageNodeView(n *storage.Node) StorageNodeView {
	v := StorageNodeView{
		ID:           n.ID,
		Name:         n.Name,
		Type:         string(n.Type),
		TypeLabel:    storageTypeLabels[n.Type],
		TypeIcon:     storageTypeIcons[n.Type],
		Description:  n.Description,
		Location:     n.Location,
		Capacity:     n.Capacity,
		CurrentParts: n.CurrentParts,
	}
	if childType, ok := storageChildTypes[n.Type]; ok {
		v.ChildType = string(childType)
		v.ChildTypeLabel = storageTypeLabels[childType]
	}
	for _, c := range n.Children {
		v.Children = append(v.Children, storageNodeView(c))
	}
	return v
}

func (h *Handler) StoragePage(w http.ResponseWriter, r *http.Request) {
	data := StoragePageData{
		BaseData: h.baseData(r, "storage", "Lagerverwaltung", "Übersicht"),
		Stats:    map[string]int{},
	}
	if tree, err := h.storage.GetTree(r.Context()); err == nil {
		for _, n := range tree {
			data.Tree = append(data.Tree, storageNodeView(n))
		}
		if tagIDs := h.categoryFilterIDs(r, "storage"); tagIDs != nil {
			data.Tree = pruneStorageTree(data.Tree, tagIDs)
		}
	}
	if stats, err := h.storage.GetStats(r.Context()); err == nil {
		data.Stats = stats
	}
	h.render(w, "storage", data)
}

func (h *Handler) StorageCreateRoot(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	_, err := h.storage.Create(r.Context(), nil, r.FormValue("name"), "lagerort", r.FormValue("description"), r.FormValue("location"), "", u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // FIX: sonst haelt htmx den Fehler fuer einen Erfolg
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) StorageAddChild(w http.ResponseWriter, r *http.Request) {
	parentID := chi.URLParam(r, "id")
	r.ParseForm()
	u := getUser(r)
	_, err := h.storage.Create(r.Context(), &parentID, r.FormValue("name"), r.FormValue("type"), r.FormValue("description"), "", r.FormValue("capacity"), u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest) // FIX: sonst haelt htmx den Fehler fuer einen Erfolg
		fmt.Fprintf(w, `<div style="color:var(--red)">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Users Page ────────────────────────────────────────────────

type ITPageData struct {
	BaseData
	Assets []ITAssetView
	Stats  map[string]int
	Filter string
}

type ITAssetView struct {
	ID           string
	Name         string
	TypeLabel    string
	TypeIcon     string
	StatusLabel  string
	StatusClass  string
	IPAddress    string
	Hostname     string
	Manufacturer string
	Model        string
	SerialNo     string
	Location     string
	InfraName    string
	Notes        string
}

type ITDetailData struct {
	BaseData
	Supplier   *PartnerLink
	SupplierID string
	Users      []UserOption
	Asset      ITAssetDetailView
}

type ITAssetDetailView struct {
	ID               string
	Name             string
	Type             string
	TypeLabel        string
	TypeIcon         string
	Status           string
	StatusLabel      string
	StatusClass      string
	Hostname         string
	IPAddress        string
	MACAddress       string
	Manufacturer     string
	ManufacturerID   string
	ManufacturerName string
	Model            string
	SerialNo         string
	Location         string
	OS               string
	PurchasedAt      string
	WarrantyUntil    string
	AssignedID       string
	AssigneeName     string
	ResponsibleID    string
	ResponsibleName  string
	InfrastructureID string
	InfraName        string
	Notes            string
	CreatedAgo       string
}

type UsersPageData struct {
	BaseData
	Users           []UserView
	TotalUsers      int
	ActiveUsers     int
	Filter          string
	RoleStats       []RoleStat
	Roles           []*rbac.Role
	CanPromoteAdmin bool
	ScopedToOwnTeam bool         // Betrachter hat kein system.manage_users, sieht/bearbeitet nur eigene (indirekte) Unteruser
	ManagerOptions  []UserOption // fuer die "Vorgesetzter"-Auswahl im Bearbeiten-Formular
	Departments     []string     // Auswahl fuer das Feld Abteilung
	DeptFilter      string       // ?dept=
	GroupFilter     string       // ?group=
	GroupOptions    []UserOption // Gruppen fuer den Filter
}

type UserView struct {
	ID              string
	Username        string
	Email           string
	NextcloudUserID string
	FirstName       string
	LastName        string
	FullName        string
	Initials        string
	AvatarBg        string
	RoleValue       string
	RoleLabel       string
	RoleClass       string
	IsSystemUser    bool
	RFIDUID         string
	Department      string
	Phone           string
	Active          bool
	CanManage       bool // ob der angemeldete Benutzer diesen Nutzer laut Rollenhierarchie verwalten darf (inkl. Rolle/Deaktivieren)
	CanEditProfile  bool // CanManage ODER dieser Nutzer ist ein (indirekter) Unteruser - erlaubt nur die Profil-Bearbeitung, keine Rollenaenderung
	ManagerID       string
	ManagerName     string

	OnCallDuty      bool
	ShiftLocksmith1 bool
	ShiftLocksmith2 bool
	Sharpening      bool
	HeatingFill     bool
	ShiftLeader     bool
}

type RoleStat struct {
	Icon  string
	Label string
	Count int
}

// roleLabelMap liefert key->label für alle Rollen (inkl. selbst angelegter),
// fuer die Anzeige in UserViews.
func (h *Handler) roleLabelMap(ctx context.Context) map[string]string {
	roles, _ := h.rbac.ListRoles(ctx)
	m := map[string]string{}
	for _, ro := range roles {
		m[ro.Key] = ro.Label
	}
	return m
}

// avatarFor liefert Initialen und eine stabile (namensabhaengige)
// Hintergrundfarbe fuer die runden Avatar-Kreise - gemeinsam genutzt von
// Benutzerverwaltung und Organigramm, damit dieselbe Person ueberall
// gleich aussieht.
func avatarFor(firstName, lastName string) (initials, bg string) {
	avatarBgs := []string{"#6366f1", "#10b981", "#f59e0b", "#ef4444", "#3b82f6", "#8b5cf6", "#ec4899"}
	if len(firstName) > 0 {
		initials += string([]rune(firstName)[:1])
	}
	if len(lastName) > 0 {
		initials += string([]rune(lastName)[:1])
	}
	bg = avatarBgs[(len(firstName)+len(lastName))%len(avatarBgs)]
	return initials, bg
}

func (h *Handler) userView(actorRoleKey string, u *users.User, roleLabels, userNames map[string]string, subordinateIDs map[string]bool) UserView {
	roleClasses := map[users.Role]string{
		"admin": "b-red", "manager": "b-blue", "technician": "b-green",
		"worker": "b-gray", "viewer": "b-gray",
	}
	initials, bg := avatarFor(u.FirstName, u.LastName)

	label := roleLabels[string(u.Role)]
	if label == "" {
		label = string(u.Role)
	}
	canManage := h.rbac.Outranks(actorRoleKey, string(u.Role))
	managerID := derefOr(u.ManagerID, "")

	return UserView{
		ID: u.ID, Username: u.Username, Email: u.Email,
		NextcloudUserID: u.NextcloudUserID,
		FirstName:       u.FirstName, LastName: u.LastName,
		FullName: u.FirstName + " " + u.LastName,
		Initials: initials, AvatarBg: bg,
		RoleValue:    string(u.Role),
		RoleLabel:    label,
		RoleClass:    roleClasses[u.Role],
		IsSystemUser: u.IsSystemUser,
		RFIDUID:      derefOr(u.RFIDUID, ""),
		Department:   u.Department, Phone: u.Phone, Active: u.Active,
		CanManage:      canManage,
		CanEditProfile: canManage || subordinateIDs[u.ID],
		ManagerID:      managerID,
		ManagerName:    userNames[managerID],
		OnCallDuty:     u.OnCallDuty, ShiftLocksmith1: u.ShiftLocksmith1, ShiftLocksmith2: u.ShiftLocksmith2,
		Sharpening: u.Sharpening, HeatingFill: u.HeatingFill, ShiftLeader: u.ShiftLeader,
	}
}

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := getUser(r)
	actorRoleKey := string(actor.Role)
	canFullyManage := h.canManageUsers(r)
	subordinateList, _ := h.users.SubordinateIDs(ctx, actor.ID)
	subordinateSet := make(map[string]bool, len(subordinateList))
	for _, id := range subordinateList {
		subordinateSet[id] = true
	}
	if !canFullyManage && len(subordinateSet) == 0 {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if canFullyManage {
		// Benutzerverwaltung darf das eigene Konto immer bearbeiten
		subordinateSet[actor.ID] = true
	}

	filter := r.URL.Query().Get("role")
	deptFilter := r.URL.Query().Get("dept")
	groupFilter := r.URL.Query().Get("group")
	var groupMembers map[string]bool
	if groupFilter != "" {
		groupMembers = map[string]bool{}
		for _, g := range h.loadGroups(ctx) {
			if g.ID == groupFilter {
				groupMembers = g.MemberIDs
			}
		}
	}
	data := UsersPageData{
		BaseData:        h.baseData(r, "users", "Benutzerverwaltung", "Rollen"),
		Filter:          filter,
		CanPromoteAdmin: h.actorIsAdmin(r) && h.rbac.Outranks(actorRoleKey, "admin"),
		ScopedToOwnTeam: !canFullyManage,
		ManagerOptions:  h.userOptions(ctx),
	}

	roles, _ := h.rbac.ListRoles(ctx)
	for _, ro := range roles {
		if h.rbac.Outranks(actorRoleKey, ro.Key) {
			data.Roles = append(data.Roles, ro)
		}
	}
	roleLabelByKey := h.roleLabelMap(ctx)

	allUsers, err := h.users.List(ctx)
	if err == nil {
		userNames := make(map[string]string, len(allUsers))
		for _, u := range allUsers {
			userNames[u.ID] = strings.TrimSpace(u.FirstName + " " + u.LastName)
		}
		roleCounts := map[string]int{}
		scopedTotal := 0
		for _, u := range allUsers {
			if !canFullyManage && !subordinateSet[u.ID] {
				continue
			}
			scopedTotal++
			if !u.Active {
				continue
			}
			data.ActiveUsers++
			roleCounts[string(u.Role)]++
			if (filter == "" || string(u.Role) == filter) &&
				(deptFilter == "" || strings.EqualFold(strings.TrimSpace(u.Department), deptFilter)) &&
				(groupMembers == nil || groupMembers[u.ID]) {
				data.Users = append(data.Users, h.userView(actorRoleKey, u, roleLabelByKey, userNames, subordinateSet))
			}
		}
		if canFullyManage {
			data.TotalUsers = len(allUsers)
		} else {
			data.TotalUsers = scopedTotal
		}
		roleIcons := map[string]string{"admin": "👑", "manager": "📋", "technician": "🔧", "worker": "👷", "viewer": "👁️"}
		for _, ro := range roles {
			if roleCounts[ro.Key] > 0 {
				icon := roleIcons[ro.Key]
				if icon == "" {
					icon = "🔑"
				}
				data.RoleStats = append(data.RoleStats, RoleStat{Icon: icon, Label: ro.Label, Count: roleCounts[ro.Key]})
			}
		}
	}
	data.Departments = h.departmentNames(r.Context())
	data.DeptFilter, data.GroupFilter = deptFilter, groupFilter
	for _, g := range h.loadGroups(ctx) {
		data.GroupOptions = append(data.GroupOptions, UserOption{ID: g.ID, Name: g.Name})
	}
	h.render(w, "users", data)
}

func (h *Handler) UserSaveWeb(w http.ResponseWriter, r *http.Request) {
	// Das Formular sendet multipart/form-data (new FormData(userForm) im
	// Browser) - r.ParseForm() liest bei diesem Content-Type den Body
	// NICHT (siehe net/http-Doku: nur application/x-www-form-urlencoded),
	// wodurch jedes Feld leer ankaeme. ParseMultipartForm deckt beides ab.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	actor := getUser(r)
	userID := strings.TrimSpace(r.FormValue("user_id"))
	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	firstName := strings.TrimSpace(r.FormValue("first_name"))
	lastName := strings.TrimSpace(r.FormValue("last_name"))
	password := r.FormValue("password")
	if username == "" || email == "" || firstName == "" || lastName == "" {
		http.Error(w, "Vorname, Nachname, Benutzername und E-Mail sind Pflichtfelder", http.StatusBadRequest)
		return
	}
	roleValue := strings.TrimSpace(r.FormValue("role"))
	canFullyManage := h.canManageUsers(r)

	// common setzt die Profilfelder, die sowohl volle Verwalter als auch
	// Vorgesetzte (fuer ihre Unteruser) aendern duerfen. Rolle und
	// Benutzername sind bewusst NICHT enthalten: die Rolle wird separat
	// ueber die Rang-Hierarchie freigegeben (siehe unten), der
	// Benutzername ist nach dem Anlegen nicht mehr aenderbar.
	common := func(u *users.User, managerID *string) {
		u.Email = email
		u.NextcloudUserID = strings.TrimSpace(r.FormValue("nextcloud_user_id"))
		u.FirstName = firstName
		u.LastName = lastName
		u.Department = strings.TrimSpace(r.FormValue("department"))
		u.Phone = strings.TrimSpace(r.FormValue("phone"))
		u.IsSystemUser = r.FormValue("is_system_user") == "on"
		u.RFIDUID = optionalID(r.FormValue("rfid_uid"))
		u.ManagerID = managerID
		u.OnCallDuty = r.FormValue("on_call_duty") == "on"
		u.ShiftLocksmith1 = r.FormValue("shift_locksmith_1") == "on"
		u.ShiftLocksmith2 = r.FormValue("shift_locksmith_2") == "on"
		u.Sharpening = r.FormValue("sharpening") == "on"
		u.HeatingFill = r.FormValue("heating_fill") == "on"
		u.ShiftLeader = r.FormValue("shift_leader") == "on"
	}

	if userID == "" {
		// Neuanlage bleibt Admins/Managern mit system.manage_users
		// vorbehalten - Unteruser-Zugriff gilt nur fuers Bearbeiten
		// bestehender Personen.
		if !canFullyManage {
			http.Error(w, "keine berechtigung", http.StatusForbidden)
			return
		}
		if roleValue == "" {
			http.Error(w, "Bitte eine Rolle auswählen", http.StatusBadRequest)
			return
		}
		role := users.Role(roleValue)
		if !h.outranksRole(r, string(role)) {
			http.Error(w, "keine berechtigung für diese Rolle", http.StatusForbidden)
			return
		}
		if role == users.RoleAdmin && !h.actorIsAdmin(r) {
			http.Error(w, "nur Administratoren dürfen die Admin-Rolle vergeben", http.StatusForbidden)
			return
		}
		if len(password) < 8 {
			http.Error(w, "Neues Passwort muss mindestens 8 Zeichen lang sein", http.StatusBadRequest)
			return
		}
		managerID, err := h.validatedManagerID(ctx, "", r.FormValue("manager_id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in := &users.CreateUserInput{
			Username: username, Email: email, Password: password,
			NextcloudUserID: strings.TrimSpace(r.FormValue("nextcloud_user_id")),
			FirstName:       firstName, LastName: lastName, Role: role,
			Department:   strings.TrimSpace(r.FormValue("department")),
			Phone:        strings.TrimSpace(r.FormValue("phone")),
			IsSystemUser: r.FormValue("is_system_user") == "on", RFIDUID: optionalID(r.FormValue("rfid_uid")),
			ManagerID:  managerID,
			OnCallDuty: r.FormValue("on_call_duty") == "on", Sharpening: r.FormValue("sharpening") == "on",
			HeatingFill: r.FormValue("heating_fill") == "on", ShiftLeader: r.FormValue("shift_leader") == "on",
			ShiftLocksmith1: r.FormValue("shift_locksmith_1") == "on", ShiftLocksmith2: r.FormValue("shift_locksmith_2") == "on",
		}
		if _, err := h.users.Register(ctx, in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	currentUser, err := h.users.GetByID(ctx, userID)
	if err != nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	// Hierarchie: eine Rolle darf nur Benutzer und Rollen mit echt
	// niedrigerem Rang verwalten (Ausnahme: die ranghoechste Rolle darf
	// auch Gleichrangige verwalten) - siehe rbac.Service.Outranks.
	// Wer die Benutzerverwaltung hat, darf auch das eigene Konto pflegen - auch
	// wenn die eigene Rolle nicht als ranghoechste eingetragen ist.
	canManageThisUser := canFullyManage && (h.outranksRole(r, string(currentUser.Role)) || currentUser.ID == actor.ID)
	if !canManageThisUser {
		// Zweite, unabhaengige Zugriffsart: jeder darf seine direkten und
		// indirekten Unteruser bearbeiten (Profilfelder), unabhaengig von
		// system.manage_users und der Rang-Hierarchie - aber ohne die
		// Rolle aendern zu duerfen (siehe unten).
		isSubordinate, subErr := h.users.IsSubordinate(ctx, actor.ID, userID)
		if subErr != nil || !isSubordinate {
			http.Error(w, "keine berechtigung, diesen Benutzer zu bearbeiten", http.StatusForbidden)
			return
		}
	}

	role := currentUser.Role
	if canManageThisUser && roleValue != "" {
		newRole := users.Role(roleValue)
		if newRole != currentUser.Role {
			if !h.outranksRole(r, string(newRole)) {
				http.Error(w, "keine berechtigung für diese Rolle", http.StatusForbidden)
				return
			}
			if newRole == users.RoleAdmin && !h.actorIsAdmin(r) {
				http.Error(w, "nur Administratoren dürfen die Admin-Rolle vergeben", http.StatusForbidden)
				return
			}
			role = newRole
		}
	}

	managerID, err := h.validatedManagerID(ctx, userID, r.FormValue("manager_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	common(currentUser, managerID)
	currentUser.Role = role
	if err := h.users.UpdateWithPassword(ctx, currentUser, password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validatedManagerID liest den gewaehlten Vorgesetzten aus dem Formular
// und verhindert Zyklen (niemand darf sein eigener oder ein indirekter
// eigener Vorgesetzter werden).
func (h *Handler) validatedManagerID(ctx context.Context, userID, raw string) (*string, error) {
	managerID := optionalID(raw)
	if managerID == nil {
		return nil, nil
	}
	if *managerID == userID {
		return nil, fmt.Errorf("Ein Benutzer kann nicht sein eigener Vorgesetzter sein")
	}
	if userID != "" {
		cyclic, err := h.users.IsSubordinate(ctx, userID, *managerID)
		if err != nil {
			return nil, fmt.Errorf("Vorgesetzten-Zuordnung konnte nicht geprüft werden")
		}
		if cyclic {
			return nil, fmt.Errorf("Der gewählte Vorgesetzte ist bereits ein Unteruser dieser Person")
		}
	}
	return managerID, nil
}

func (h *Handler) UserDeactivateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	target, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Benutzer nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.outranksRole(r, string(target.Role)) {
		http.Error(w, "keine berechtigung, diesen Benutzer zu deaktivieren", http.StatusForbidden)
		return
	}
	h.users.Deactivate(r.Context(), id)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<tr id="user-row-%s" style="opacity:.5">
		<td colspan="5" style="color:var(--muted);font-size:12px;padding:12px">Benutzer deaktiviert</td>
		<td></td></tr>`, id)
}

func (h *Handler) UserRoleWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageUsers(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	r.ParseForm()
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, "nicht gefunden", 404)
		return
	}
	newRole := users.Role(r.FormValue("role"))
	if !h.outranksRole(r, string(u.Role)) || !h.outranksRole(r, string(newRole)) {
		http.Error(w, "keine berechtigung für diese Rolle", http.StatusForbidden)
		return
	}
	if newRole == users.RoleAdmin && !h.actorIsAdmin(r) {
		http.Error(w, "nur Administratoren dürfen die Admin-Rolle vergeben", http.StatusForbidden)
		return
	}
	u.Role = newRole
	h.users.Update(r.Context(), u)
	h.Users(w, r)
}

// ── Rollen- & Berechtigungsverwaltung ────────────────────────────

type RolesPageData struct {
	BaseData
	Roles                  []RoleColumn
	PermissionGroups       []PermissionGroup
	Matrix                 map[string]map[string]bool // roleID -> permissionID -> granted
	IdleTimeoutMinutes     int
	OverrideTimeoutMinutes int
	NextRoleLevel          int
	Departments            []deptView
	RoleDepartments        map[string]string   // roleID -> departmentID
	RoleTrainings          map[string][]string // roleKey -> Pflichtschulungen
}

// RoleColumn ist eine Rollen-Spalte der Berechtigungs-Matrix, angereichert
// um CanManage - ob der angemeldete Benutzer diese Rolle laut Hierarchie
// loeschen bzw. ihre Berechtigungen bearbeiten darf.
type RoleColumn struct {
	*rbac.Role
	CanManage bool
}

type PermissionGroup struct {
	Category    string
	Permissions []*rbac.Permission
}

func (h *Handler) canManageRoles(r *http.Request) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_roles")
}

// actorIsAdmin: angemeldete Person hat die Rolle "admin" (nur Admins duerfen
// die Admin-Rolle vergeben).
func (h *Handler) actorIsAdmin(r *http.Request) bool {
	return getUser(r).Role == users.RoleAdmin
}

func (h *Handler) canManageUsers(r *http.Request) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_users")
}

// outranksRole prueft, ob der angemeldete Benutzer die uebergebene Rolle
// in der Hierarchie verwalten darf (siehe rbac.Service.Outranks) - die
// zusaetzliche Schranke zu canManageUsers/canManageRoles, damit z.B. ein
// Manager mit "Benutzer verwalten" keine Admins bearbeiten oder befoerdern
// kann.
func (h *Handler) outranksRole(r *http.Request, roleKey string) bool {
	return h.rbac.Outranks(string(getUser(r).Role), roleKey)
}

func (h *Handler) RolesPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	roles, _ := h.rbac.ListRoles(ctx)
	perms, _ := h.rbac.ListPermissions(ctx)
	matrix, _ := h.rbac.MatrixEntries(ctx)

	var groups []PermissionGroup
	currentCat := ""
	for _, p := range perms {
		if p.Category != currentCat || len(groups) == 0 {
			groups = append(groups, PermissionGroup{Category: p.Category})
			currentCat = p.Category
		}
		last := &groups[len(groups)-1]
		last.Permissions = append(last.Permissions, p)
	}

	actorRoleKey := string(getUser(r).Role)
	roleColumns := make([]RoleColumn, 0, len(roles))
	for _, ro := range roles {
		roleColumns = append(roleColumns, RoleColumn{Role: ro, CanManage: h.rbac.Outranks(actorRoleKey, ro.Key)})
	}
	nextRoleLevel := h.rbac.RoleLevel(actorRoleKey) - 1
	if nextRoleLevel < 0 {
		nextRoleLevel = 0
	}

	data := RolesPageData{
		BaseData:               h.baseData(r, "roles", "Rollen & Berechtigungen", "Auto-Logout"),
		Roles:                  roleColumns,
		PermissionGroups:       groups,
		Matrix:                 matrix,
		IdleTimeoutMinutes:     h.rbac.IdleTimeoutMinutes(),
		OverrideTimeoutMinutes: h.rbac.OverrideTimeoutMinutes(),
		NextRoleLevel:          nextRoleLevel,
		Departments:            h.loadDepartments(ctx),
		RoleDepartments:        h.roleDepartments(ctx),
		RoleTrainings:          h.roleTrainings(ctx),
	}
	h.render(w, "roles", data)
}

// clampRoleLevel verhindert Rang-Eskalation: niemand darf eine Rolle auf
// einen Rang ueber der eigenen Rangstufe anlegen/setzen; unterhalb der
// ranghoechsten Rolle im System ist zusaetzlich nur ein echt niedrigerer
// Rang erlaubt (nicht gleichrangig).
func (h *Handler) clampRoleLevel(r *http.Request, level int) int {
	actorRoleKey := string(getUser(r).Role)
	actorLevel := h.rbac.RoleLevel(actorRoleKey)
	maxLevel := h.rbac.MaxRoleLevel()
	if level > actorLevel {
		level = actorLevel
	}
	if actorLevel < maxLevel && level >= actorLevel {
		level = actorLevel - 1
	}
	if level < 0 {
		level = 0
	}
	return level
}

func (h *Handler) RoleCreateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	key := strings.TrimSpace(r.FormValue("key"))
	label := strings.TrimSpace(r.FormValue("label"))
	level, err := strconv.Atoi(strings.TrimSpace(r.FormValue("level")))
	if err != nil {
		level = 0
	}
	level = h.clampRoleLevel(r, level)
	if key != "" && label != "" {
		h.rbac.CreateRole(r.Context(), key, label, level)
	}
	http.Redirect(w, r, "/admin/roles", http.StatusFound)
}

// RoleUpdateLevelWeb aendert die Rangstufe einer bestehenden Rolle
// (eingebaut oder benutzerdefiniert - nur das Loeschen ist auf
// benutzerdefinierte Rollen beschraenkt). Instant-Save-Feld auf der
// Rollen-Seite.
func (h *Handler) RoleUpdateLevelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	role, err := h.rbac.GetRoleByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Rolle nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.outranksRole(r, role.Key) {
		http.Error(w, "keine berechtigung, diese Rolle zu bearbeiten", http.StatusForbidden)
		return
	}
	r.ParseForm()
	level, err := strconv.Atoi(strings.TrimSpace(r.FormValue("level")))
	if err != nil {
		http.Error(w, "ungültiger Rang", http.StatusBadRequest)
		return
	}
	level = h.clampRoleLevel(r, level)
	if err := h.rbac.UpdateRoleLevel(r.Context(), id, level); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) RoleDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	role, err := h.rbac.GetRoleByID(r.Context(), id)
	if err != nil {
		http.Error(w, "Rolle nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.outranksRole(r, role.Key) {
		http.Error(w, "keine berechtigung, diese Rolle zu löschen", http.StatusForbidden)
		return
	}
	h.rbac.DeleteRole(r.Context(), id)
	http.Redirect(w, r, "/admin/roles", http.StatusFound)
}

// RoleMatrixWeb schaltet eine einzelne Berechtigung fuer eine Rolle per
// htmx-Checkbox um, ohne die Seite neu zu laden.
func (h *Handler) RoleMatrixWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	roleID := r.FormValue("role_id")
	permID := r.FormValue("permission_id")
	granted := r.FormValue("granted") == "true"
	role, err := h.rbac.GetRoleByID(r.Context(), roleID)
	if err != nil {
		http.Error(w, "Rolle nicht gefunden", http.StatusNotFound)
		return
	}
	if !h.outranksRole(r, role.Key) {
		http.Error(w, "keine berechtigung, diese Rolle zu bearbeiten", http.StatusForbidden)
		return
	}
	if err := h.rbac.SetRolePermission(r.Context(), roleID, permID, granted); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) SettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	if v, err := strconv.Atoi(r.FormValue("idle_timeout_minutes")); err == nil {
		h.rbac.SetIdleTimeoutMinutes(r.Context(), v)
	}
	if v, err := strconv.Atoi(r.FormValue("override_timeout_minutes")); err == nil {
		h.rbac.SetOverrideTimeoutMinutes(r.Context(), v)
	}
	http.Redirect(w, r, "/admin/roles", http.StatusFound)
}

// ── Override-Anmeldung an Systemnutzer-Terminals ─────────────────
//
// Ablauf: An einem als "Systemnutzer" markierten, dauerhaft eingeloggten
// Terminal meldet sich jemand mit "system.override"-Berechtigung kurz mit
// den eigenen Zugangsdaten an, erledigt eine Aufgabe unter eigenem Namen,
// und kehrt danach (manuell per Button oder automatisch nach
// Inaktivitaet) wieder zum Systemnutzer zurueck. Der urspruengliche
// Systemnutzer-Token wird dafuer im Cookie "pdh_return_token"
// zwischengespeichert - er wird niemals ueberschrieben, solange eine
// Override-Sitzung bereits laeuft, damit sich Override-Anmeldungen nicht
// verschachteln koennen.

// OverrideLoginRFIDWeb: wie OverrideLoginWeb, aber per RFID-Karten-UID
// statt E-Mail+Passwort - der praktische Regelfall am Terminal ("kurz
// Karte dranhalten"), Formular mit E-Mail+Passwort bleibt als Fallback.
func (h *Handler) OverrideLoginRFIDWeb(w http.ResponseWriter, r *http.Request) {
	systemCookie, err := r.Cookie("pdh_token")
	if err != nil || systemCookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	current := getUser(r)
	if !current.IsSystemUser {
		http.Redirect(w, r, "/?override_error=nur_am_systemnutzer", http.StatusFound)
		return
	}

	r.ParseForm()
	uid := strings.TrimSpace(r.FormValue("uid"))
	overrideUser, err := h.users.GetByRFID(r.Context(), uid)
	if err != nil {
		http.Redirect(w, r, "/?override_error=unbekannte_karte", http.StatusFound)
		return
	}
	if !h.rbac.HasPermissionForUser(overrideUser.ID, string(overrideUser.Role), "system.override") {
		http.Redirect(w, r, "/?override_error=keine_berechtigung", http.StatusFound)
		return
	}

	if _, err := r.Cookie("pdh_return_token"); err != nil {
		http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: systemCookie.Value, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	}

	overrideTTL := time.Duration(h.rbac.OverrideTimeoutMinutes()) * time.Minute
	tokenStr, err := h.users.IssueToken(overrideUser, overrideTTL, map[string]interface{}{"override": true})
	if err != nil {
		http.Redirect(w, r, "/?override_error=token", http.StatusFound)
		return
	}
	maxAge := int(overrideTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: tokenStr, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: overrideUser.ID, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handler) OverrideLoginWeb(w http.ResponseWriter, r *http.Request) {
	systemCookie, err := r.Cookie("pdh_token")
	if err != nil || systemCookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	current := getUser(r)
	if !current.IsSystemUser {
		http.Redirect(w, r, "/?override_error=nur_am_systemnutzer", http.StatusFound)
		return
	}

	r.ParseForm()
	overrideUser, err := h.users.Authenticate(r.Context(), r.FormValue("email"), r.FormValue("password"))
	if err != nil {
		http.Redirect(w, r, "/?override_error=zugangsdaten", http.StatusFound)
		return
	}
	if !h.rbac.HasPermissionForUser(overrideUser.ID, string(overrideUser.Role), "system.override") {
		http.Redirect(w, r, "/?override_error=keine_berechtigung", http.StatusFound)
		return
	}

	// Nur beim allerersten Override-Login den Rueckkehr-Token setzen, damit
	// verschachtelte Override-Anmeldungen nicht den eigentlichen
	// Systemnutzer "verlieren".
	if _, err := r.Cookie("pdh_return_token"); err != nil {
		http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: systemCookie.Value, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	}

	overrideTTL := time.Duration(h.rbac.OverrideTimeoutMinutes()) * time.Minute
	tokenStr, err := h.users.IssueToken(overrideUser, overrideTTL, map[string]interface{}{"override": true})
	if err != nil {
		http.Redirect(w, r, "/?override_error=token", http.StatusFound)
		return
	}
	maxAge := int(overrideTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: tokenStr, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: overrideUser.ID, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// OverrideLogoutWeb kehrt zur Systemnutzer-Sitzung zurueck (Button "Zurück
// zum Systemnutzer" oder automatisch per Inaktivitaets-Timer im Frontend).
// Ohne aktive Override-Sitzung faellt das Verhalten auf einen normalen
// Logout zurueck.
func (h *Handler) OverrideLogoutWeb(w http.ResponseWriter, r *http.Request) {
	returnCookie, err := r.Cookie("pdh_return_token")
	if err != nil || returnCookie.Value == "" {
		h.Logout(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: "", Path: "/", MaxAge: -1})

	token, parseErr := jwt.Parse(returnCookie.Value, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unerwartete signaturmethode")
		}
		return []byte(h.jwtSecret), nil
	})
	if parseErr != nil || !token.Valid {
		h.Logout(w, r)
		return
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	userID, _ := claims["sub"].(string)
	if !ok || userID == "" {
		h.Logout(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: returnCookie.Value, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: userID, Path: "/", MaxAge: 86400, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// ── Wiederhergestellte Handler ────────────────────────────────

func (h *Handler) FaultChatWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	message := r.FormValue("message")
	if message == "" {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "")
		return
	}
	reply, err := h.faults.Chat(r.Context(), id, getUser(r).ID, message, nil)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		componentLog("copilot").Error().Err(err).Str("stoerung", id).Msg("chat fehlgeschlagen")
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px;margin-bottom:10px"><i class="ti ti-alert-circle"></i> Copilot: %s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="margin-bottom:10px"><div style="font-size:10px;color:var(--muted);margin-bottom:3px">Copilot</div><div style="color:var(--text);line-height:1.5;font-size:12px;white-space:pre-wrap">%s</div></div>`, esc(reply))
}

func (h *Handler) TimeStopWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := getUser(r)
	h.time.Stop(r.Context(), id, u.ID, time.Time{})
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px 0"><i class="ti ti-check"></i> Zeit gestoppt</div>`)
}

func parseDateTimeLocal(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

func (h *Handler) TimeStartWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(32 << 20) // FIX: r.ParseForm() parst kein multipart/form-data (FormData+fetch)
	u := getUser(r)
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefType(r.FormValue("ref_type")),
		RefID:       r.FormValue("ref_id"),
		Description: r.FormValue("description"),
	}
	if infraID := r.FormValue("infrastructure_id"); infraID != "" {
		in.InfrastructureID = &infraID
	}
	if in.RefType == "" || in.RefID == "" {
		http.Error(w, "Typ und Bezug fehlen", http.StatusBadRequest)
		return
	}
	if in.Description == "" {
		in.Description = timeRefLabel(in.RefType)
	}
	if _, err := h.time.Start(r.Context(), in, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TimeManualWeb(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(32 << 20) // FIX: r.ParseForm() parst kein multipart/form-data (FormData+fetch)
	u := getUser(r)
	startedAt, err := parseDateTimeLocal(r.FormValue("started_at"))
	if err != nil || startedAt.IsZero() {
		http.Error(w, "Startzeit ist ungültig", http.StatusBadRequest)
		return
	}
	endedAt, err := parseDateTimeLocal(r.FormValue("ended_at"))
	if err != nil || endedAt.IsZero() || !endedAt.After(startedAt) {
		http.Error(w, "Endzeit ist ungültig", http.StatusBadRequest)
		return
	}
	in := &timetracking.CreateEntryInput{
		RefType:     timetracking.RefType(r.FormValue("ref_type")),
		RefID:       r.FormValue("ref_id"),
		Description: r.FormValue("description"),
		StartedAt:   startedAt,
		EndedAt:     &endedAt,
	}
	if infraID := r.FormValue("infrastructure_id"); infraID != "" {
		in.InfrastructureID = &infraID
	}
	if in.RefType == "" || in.RefID == "" {
		http.Error(w, "Typ und Bezug fehlen", http.StatusBadRequest)
		return
	}
	if in.Description == "" {
		in.Description = timeRefLabel(in.RefType)
	}
	if _, err := h.time.Start(r.Context(), in, u.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TimeDeleteWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	isAdmin := u.Role == users.RoleAdmin || u.Role == users.RoleManager
	if err := h.time.Delete(r.Context(), chi.URLParam(r, "id"), u.ID, isAdmin); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TimeEditWeb(w http.ResponseWriter, r *http.Request) {
	u := getUser(r)
	isAdmin := u.Role == users.RoleAdmin || u.Role == users.RoleManager
	r.ParseMultipartForm(32 << 20)
	started, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("started_at"), time.Local)
	if err != nil {
		http.Error(w, "ungueltiges startdatum", http.StatusBadRequest)
		return
	}
	in := &timetracking.UpdateEntryInput{
		Description: r.FormValue("description"),
		StartedAt:   started,
	}
	if endedStr := r.FormValue("ended_at"); endedStr != "" {
		if ended, err := time.ParseInLocation("2006-01-02T15:04", endedStr, time.Local); err == nil {
			in.EndedAt = &ended
		}
	}
	w.Header().Set("Content-Type", "text/html")
	if err := h.time.Update(r.Context(), chi.URLParam(r, "id"), u.ID, isAdmin, in); err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">%s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px">Gespeichert</div>`)
}

func (h *Handler) ChecklistsPage(w http.ResponseWriter, r *http.Request) {
	type checklistView struct {
		ID          string
		Name        string
		Description string
		Category    string
	}
	data := struct {
		BaseData
		Total      int
		Checklists []checklistView
	}{BaseData: h.baseData(r, "checklists", "Checklisten-Vorlagen", "Feldtypen")}
	if h.checks != nil {
		if list, err := h.checks.List(r.Context(), r.URL.Query().Get("category")); err == nil {
			data.Total = len(list)
			for _, c := range list {
				data.Checklists = append(data.Checklists, checklistView{
					ID: c.ID, Name: c.Name, Description: c.Description, Category: c.Category,
				})
			}
		}
	}
	h.render(w, "checklist_builder", data)
}

func (h *Handler) InfraDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	type InfraDetailData struct {
		BaseData
		Node           InfraNodeView
		Children       []InfraNodeView
		Parent         *InfraNodeView
		HistoryModules []infraHistoryModule
		CommentRefs    []InfraCommentRefOption
		PartnerLinks   []PartnerLink
		SupplierID     string
		ServiceID      string
		Departments    []deptView
		DepartmentID   string // eigene Abteilung der Anlage
		EffectiveDept  string // wirksame Abteilung (ggf. geerbt)
		DeptInherited  bool
	}
	node, err := h.infra.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/infrastructure", http.StatusFound)
		return
	}
	data := InfraDetailData{
		BaseData:       h.baseData(r, "infrastructure", node.Name, "Untergeordnete Anlagen"),
		Node:           infraNodeView(node),
		HistoryModules: infraHistoryModules,
		CommentRefs:    h.infraCommentRefOptions(ctx, id),
	}
	data.PartnerLinks, data.SupplierID, data.ServiceID = h.infraPartnerLinks(ctx, id)
	data.Departments = h.loadDepartments(ctx)
	var effID string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE((SELECT department_id::text FROM infrastructure WHERE id = $1::uuid), ''),
		COALESCE((SELECT d.id::text FROM infrastructure_department x JOIN departments d ON d.id = x.department_id WHERE x.infrastructure_id = $1::uuid), ''),
		COALESCE((SELECT d.name FROM infrastructure_department x JOIN departments d ON d.id = x.department_id WHERE x.infrastructure_id = $1::uuid), '')`, id).
		Scan(&data.DepartmentID, &effID, &data.EffectiveDept)
	data.DeptInherited = effID != "" && effID != data.DepartmentID
	if children, err := h.infra.List(ctx, &id, ""); err == nil {
		for _, c := range children {
			data.Children = append(data.Children, infraNodeView(c))
		}
	}
	if node.ParentID != nil {
		if parent, err := h.infra.GetByID(ctx, *node.ParentID); err == nil {
			pv := infraNodeView(parent)
			data.Parent = &pv
		}
	}
	t, _ := h.tmpl.Clone()
	t = bindLang(t, data.Lang)
	t.ParseFiles("web/templates/infra_detail.gohtml")
	t.ExecuteTemplate(w, "base.gohtml", data)
}

func (h *Handler) InfraUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.canEditInfra(r) {
		http.Error(w, "keine berechtigung (Infrastruktur bearbeiten)", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	r.ParseForm()
	in := &infrastructure.UpdateInput{
		Name: r.FormValue("name"), Location: r.FormValue("location"),
		Manufacturer: r.FormValue("manufacturer"), ManufacturerID: optionalID(r.FormValue("manufacturer_id")),
		SerialNo: r.FormValue("serial_no"),
		Model:    r.FormValue("model"), CostCenterID: optionalID(r.FormValue("cost_center_id")),
	}
	err := h.infra.Update(r.Context(), id, in)
	if err == nil {
		err = h.saveInfraPartnerRoles(r.Context(), r, id)
	}
	if _, ok := r.Form["department_id"]; ok && err == nil {
		_, err = h.db.Exec(r.Context(), `UPDATE infrastructure SET department_id = $1 WHERE id = $2::uuid`, nullID(r.FormValue("department_id")), id)
	}
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Fehler: %s</div>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<div style="color:var(--green);font-size:12px;padding:8px 0"><i class="ti ti-check"></i> Gespeichert</div>`)
}

func (h *Handler) ITPage(w http.ResponseWriter, r *http.Request) {
	data := ITPageData{
		BaseData: h.baseData(r, "it", "IT-Infrastruktur", "Übersicht"),
		Filter:   r.URL.Query().Get("type"), Stats: map[string]int{},
	}
	typeLabels := map[string]string{"server": "Server", "network": "Netzwerk", "workstation": "Workstation", "printer": "Drucker", "phone": "Telefon", "tablet": "Tablet", "other": "Sonstiges"}
	typeIcons := map[string]string{"server": "🖥️", "network": "🌐", "workstation": "💻", "printer": "🖨️", "phone": "📱", "tablet": "📟", "other": "📦"}
	statusLabels := map[string]string{"active": "Aktiv", "inactive": "Inaktiv", "maintenance": "Wartung", "retired": "Außer Dienst"}
	statusClasses := map[string]string{"active": "b-green", "inactive": "b-gray", "maintenance": "b-amber", "retired": "b-red"}
	if list, err := h.it.List(r.Context(), it.AssetType(data.Filter), ""); err == nil {
		scopeIDs := h.scopeAllowedIDs(r, "it_asset")
		for _, a := range list {
			if scopeIDs != nil && !scopeIDs[a.ID] {
				continue
			}
			data.Assets = append(data.Assets, ITAssetView{
				ID: a.ID, Name: a.Name,
				TypeLabel: typeLabels[string(a.Type)], TypeIcon: typeIcons[string(a.Type)],
				StatusLabel: statusLabels[string(a.Status)], StatusClass: statusClasses[string(a.Status)],
				IPAddress: a.IPAddress, Hostname: a.Hostname,
				Manufacturer: a.Manufacturer, Model: a.Model, SerialNo: a.SerialNo, Location: a.Location,
				InfraName: a.InfraName, Notes: a.Notes,
			})
		}
	}
	if stats, err := h.it.GetStats(r.Context()); err == nil {
		data.Stats = stats
	}
	h.render(w, "it", data)
}

func (h *Handler) ITCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	u := getUser(r)
	in := &it.CreateAssetInput{
		Name: r.FormValue("name"), Type: it.AssetType(r.FormValue("type")),
		Hostname: r.FormValue("hostname"), IPAddress: r.FormValue("ip_address"),
		Manufacturer: r.FormValue("manufacturer"), ManufacturerID: optionalID(r.FormValue("manufacturer_id")), Model: r.FormValue("model"),
		SerialNo: r.FormValue("serial_no"), Location: r.FormValue("location"),
		OS: r.FormValue("os"), InfrastructureID: optionalID(r.FormValue("infrastructure_id")), Notes: r.FormValue("notes"),
	}
	a, err := h.it.Create(r.Context(), in, u.ID)
	w.Header().Set("Content-Type", "text/html")
	if err != nil {
		fmt.Fprintf(w, "<tr><td>Fehler: %s</td></tr>", err.Error())
		return
	}
	icons := map[string]string{"server": "🖥️", "network": "🌐", "workstation": "💻", "printer": "🖨️", "phone": "📱", "tablet": "📟", "other": "📦"}
	labels := map[string]string{"server": "Server", "network": "Netzwerk", "workstation": "Workstation", "printer": "Drucker", "phone": "Telefon", "tablet": "Tablet", "other": "Sonstiges"}
	t := string(a.Type)
	fmt.Fprintf(w, "<tr><td><b>%s</b><div style=\"font-size:11px;color:var(--muted)\">%s</div><div style=\"font-size:11px;color:var(--muted)\">%s</div></td><td>%s %s</td><td>%s</td><td>%s</td><td><span class=\"badge b-green\">Aktiv</span></td><td></td></tr>",
		esc(a.Name), esc(a.InfraName), esc(a.Notes), esc(icons[t]), esc(labels[t]), esc(a.IPAddress), esc(a.Location))
}

func (h *Handler) ITStatusWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()
	status := it.AssetStatus(r.FormValue("status"))
	h.it.UpdateStatus(r.Context(), id, status)
	w.Header().Set("Content-Type", "text/html")
	statusClasses := map[string]string{"active": "b-green", "inactive": "b-gray", "maintenance": "b-amber", "retired": "b-red"}
	statusLabels := map[string]string{"active": "Aktiv", "inactive": "Inaktiv", "maintenance": "Wartung", "retired": "Außer Dienst"}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, statusClasses[string(status)], statusLabels[string(status)])
}

func itAssetDetailView(a *it.Asset) ITAssetDetailView {
	typeLabels := map[string]string{"server": "Server", "network": "Netzwerk", "workstation": "Workstation", "printer": "Drucker", "phone": "Telefon", "tablet": "Tablet", "other": "Sonstiges"}
	typeIcons := map[string]string{"server": "🖥️", "network": "🌐", "workstation": "💻", "printer": "🖨️", "phone": "📱", "tablet": "📟", "other": "📦"}
	statusLabels := map[string]string{"active": "Aktiv", "inactive": "Inaktiv", "maintenance": "Wartung", "retired": "Außer Dienst"}
	statusClasses := map[string]string{"active": "b-green", "inactive": "b-gray", "maintenance": "b-amber", "retired": "b-red"}
	v := ITAssetDetailView{
		ID: a.ID, Name: a.Name, Type: string(a.Type),
		TypeLabel: typeLabels[string(a.Type)], TypeIcon: typeIcons[string(a.Type)],
		Status: string(a.Status), StatusLabel: statusLabels[string(a.Status)], StatusClass: statusClasses[string(a.Status)],
		Hostname: a.Hostname, IPAddress: a.IPAddress, MACAddress: a.MACAddress,
		Manufacturer: a.Manufacturer, ManufacturerName: a.ManufacturerName, Model: a.Model, SerialNo: a.SerialNo,
		Location: a.Location, OS: a.OS,
		AssigneeName: a.AssigneeName, InfraName: a.InfraName,
		Notes: a.Notes, CreatedAgo: timeAgo(a.CreatedAt),
	}
	if a.PurchasedAt != nil {
		v.PurchasedAt = *a.PurchasedAt
	}
	if a.WarrantyUntil != nil {
		v.WarrantyUntil = *a.WarrantyUntil
	}
	if a.AssignedTo != nil {
		v.AssignedID = *a.AssignedTo
	}
	v.ResponsibleName = a.ResponsibleName
	if a.ResponsibleTo != nil {
		v.ResponsibleID = *a.ResponsibleTo
	}
	if a.InfrastructureID != nil {
		v.InfrastructureID = *a.InfrastructureID
	}
	if a.ManufacturerID != nil {
		v.ManufacturerID = *a.ManufacturerID
	}
	return v
}

// ITDetail zeigt die Detailseite eines IT-Assets. Die Route dafuer fehlte
// bisher komplett - jeder Klick auf ein Asset in der Liste fuehrte zu
// einem 404, weil nur "/it" (Liste) und "/it/{id}/status-web" existierten.
func (h *Handler) ITDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	a, err := h.it.GetByID(ctx, id)
	if err != nil {
		http.Redirect(w, r, "/it", http.StatusFound)
		return
	}

	data := ITDetailData{
		BaseData: h.baseData(r, "it", a.Name, "IT-Infrastruktur"),
		Users:    h.userOptions(ctx),
		Asset:    itAssetDetailView(a),
	}
	data.Supplier = h.itSupplierLink(ctx, id)
	if data.Supplier != nil {
		data.SupplierID = data.Supplier.ID
	}
	h.render(w, "it_detail", data)
}

// ITEditWeb speichert die auf der Detailseite bearbeitbaren Felder. POST
// statt PUT, weil Cloudflare/Nginx PUT-Requests blockiert (siehe andere
// "-web" Endpunkte in dieser Datei).
func (h *Handler) ITEditWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.ParseForm()

	in := &it.UpdateDetailsInput{
		Name: r.FormValue("name"), Type: it.AssetType(r.FormValue("type")),
		Hostname: r.FormValue("hostname"), IPAddress: r.FormValue("ip_address"),
		MACAddress:     r.FormValue("mac_address"),
		Manufacturer:   r.FormValue("manufacturer"),
		ManufacturerID: optionalID(r.FormValue("manufacturer_id")),
		Model:          r.FormValue("model"),
		SerialNo:       r.FormValue("serial_no"), Location: r.FormValue("location"),
		OS:               r.FormValue("os"),
		PurchasedAt:      optionalID(r.FormValue("purchased_at")),
		WarrantyUntil:    optionalID(r.FormValue("warranty_until")),
		AssignedTo:       optionalID(r.FormValue("assigned_to")),
		ResponsibleTo:    optionalID(r.FormValue("responsible_to")),
		InfrastructureID: optionalID(r.FormValue("infrastructure_id")),
		Notes:            r.FormValue("notes"),
	}
	if err := h.saveITSupplier(r.Context(), r, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.it.UpdateDetails(r.Context(), id, in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/it/"+id, http.StatusFound)
}

// unassignedRecords: offene Vorgaenge ohne Zuweisung als "art:id" (Arten wie
// GanttItem.RefType). Fehler ergeben eine leere Menge.
func (h *Handler) unassignedRecords(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `
		SELECT 'ticket:' || id FROM tickets WHERE assigned_to IS NULL AND assigned_group_id IS NULL AND status NOT IN ('resolved', 'closed')
		UNION ALL SELECT 'fault:' || id FROM faults WHERE assigned_to IS NULL AND assigned_group_id IS NULL AND status NOT IN ('resolved', 'closed')
		UNION ALL SELECT 'maintenance:' || id FROM maintenance_tasks WHERE assigned_to IS NULL AND assigned_group_id IS NULL AND status IN ('open', 'in_progress')
		UNION ALL SELECT 'task:' || t.id FROM tasks t WHERE t.assigned_group_id IS NULL AND t.status NOT IN ('resolved', 'closed')
			AND NOT EXISTS (SELECT 1 FROM task_assignees a WHERE a.task_id = t.id)`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}
