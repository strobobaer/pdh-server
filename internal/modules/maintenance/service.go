package maintenance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/internal/core/addins"
	"pdh/internal/modules/inventory"
	"pdh/pkg/appsettings"
)

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// Repo: Zugriff auf das Repository (Web-Oberflaeche).
func (s *Service) Repo() *Repository { return s.repo }

var (
	eventBus   *addins.EventBus
	invService *inventory.Service
)

// SetEventBus verbindet das Modul mit dem Add-in-Ereignis-Bus (main.go).
func SetEventBus(b *addins.EventBus) { eventBus = b }

// SetInventoryService verbindet das Modul mit dem Lager (Reservieren/Buchen).
func SetInventoryService(inv *inventory.Service) { invService = inv }

// completedHooks laufen nach jedem erfolgreichen Abschluss – egal ueber
// welchen Weg (Assistent, Leitstand, API). Genutzt fuer Protokoll-PDF und
// Rueckmeldung.
var (
	completedHooksMu sync.RWMutex
	completedHooks   []func(ctx context.Context, taskID, userID string)
)

// OnTaskCompleted meldet eine Funktion fuer abgeschlossene Auftraege an.
func OnTaskCompleted(fn func(ctx context.Context, taskID, userID string)) {
	completedHooksMu.Lock()
	defer completedHooksMu.Unlock()
	completedHooks = append(completedHooks, fn)
}

func runCompletedHooks(ctx context.Context, taskID, userID string) {
	completedHooksMu.RLock()
	hooks := append([]func(context.Context, string, string){}, completedHooks...)
	completedHooksMu.RUnlock()
	for _, fn := range hooks {
		runCompletedHook(context.WithoutCancel(ctx), fn, taskID, userID)
	}
}

// runCompletedHook: ein fehlerhafter Hook (Protokoll, Rueckmeldung) darf den
// bereits gespeicherten Abschluss nicht mehr abbrechen.
func runCompletedHook(ctx context.Context, fn func(context.Context, string, string), taskID, userID string) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("task", taskID).Msg("wartung: abschluss-hook abgestuerzt")
		}
	}()
	fn(ctx, taskID, userID)
}

// IsInputError: Fehler stammt aus einer ungueltigen Eingabe (HTTP 400).
func IsInputError(err error) bool { return errors.Is(err, ErrInput) }

// InputMessage: Meldung eines Eingabefehlers ohne Praefix.
func InputMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInput.Error()+": ")
}

// ── Plaene ───────────────────────────────────────────────────

func (s *Service) ListPlans(ctx context.Context, infraID string) ([]*MaintenancePlan, error) {
	return s.repo.ListPlans(ctx, infraID, false)
}

func (s *Service) GetPlan(ctx context.Context, id string) (*MaintenancePlan, error) {
	return s.repo.GetPlan(ctx, id)
}

// CreatePlan legt den Plan an und sofort seinen ersten Auftrag.
func (s *Service) CreatePlan(ctx context.Context, in *PlanInput, userID string) (string, error) {
	id, err := s.repo.CreatePlan(ctx, in, userID)
	if err != nil {
		return "", err
	}
	_, err = s.repo.ensureTaskForPlan(ctx, id, userID)
	return id, err
}

func (s *Service) UpdatePlan(ctx context.Context, id string, in *PlanInput, userID string) error {
	if err := s.repo.UpdatePlan(ctx, id, in); err != nil {
		return err
	}
	_, err := s.repo.ensureTaskForPlan(ctx, id, userID)
	return err
}

func (s *Service) SetPlanActive(ctx context.Context, id string, active bool) error {
	return s.repo.SetPlanActive(ctx, id, active)
}

func (s *Service) DuplicatePlan(ctx context.Context, id, userID string) (string, error) {
	newID, err := s.repo.DuplicatePlan(ctx, id, userID)
	if err == nil {
		_, err = s.repo.ensureTaskForPlan(ctx, newID, userID)
	}
	return newID, err
}

// EnsurePlanTask: Auftrag zum Plan anlegen, falls keiner offen ist.
func (s *Service) EnsurePlanTask(ctx context.Context, planID, userID string) error {
	_, err := s.repo.ensureTaskForPlan(ctx, planID, userID)
	return err
}

// GenerateTasks: fehlende Auftraege fuer aktive Plaene anlegen (je Plan genau einer).
func (s *Service) GenerateTasks(ctx context.Context, userID string) (int, error) {
	return s.repo.EnsurePlanTasks(ctx)
}

// ── Auftraege ────────────────────────────────────────────────

func (s *Service) ListTasks(ctx context.Context, status TaskStatus, infraID string) ([]*MaintenanceTask, error) {
	return s.repo.ListTasks(ctx, status, infraID)
}

func (s *Service) GetTaskByID(ctx context.Context, id string) (*MaintenanceTask, error) {
	return s.repo.GetTaskByID(ctx, id)
}

func (s *Service) GetDueToday(ctx context.Context) ([]*MaintenanceTask, error) {
	return s.repo.GetDueToday(ctx)
}

// CreateTask: Auftrag (meist ohne Plan) anlegen, optional mit Checklisten.
func (s *Service) CreateTask(ctx context.Context, in *CreateTaskInput, userID string) (*MaintenanceTask, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, inputErr("Bitte einen Titel angeben")
	}
	if strings.TrimSpace(in.InfrastructureID) == "" {
		return nil, inputErr("Bitte eine Anlage wählen")
	}
	if in.Type == "" {
		in.Type = PlanPreventive
	}
	if in.Priority == "" {
		in.Priority = PrioMedium
	}
	due, err := time.ParseInLocation("2006-01-02", in.DueDate, time.Local)
	if err != nil {
		days := appsettings.GetInt(ctx, s.repo.db, appsettings.KeyDefaultDueDaysMaintenance, appsettings.DefaultDueDaysFallback)
		due = dayStart(time.Now()).AddDate(0, 0, days)
	}
	t := &MaintenanceTask{PlanID: nullID(in.PlanID), Title: in.Title, Description: strings.TrimSpace(in.Description),
		Type: in.Type, InfrastructureID: in.InfrastructureID, Priority: in.Priority, AssignedTo: nullID(in.AssignedTo),
		DueDate: due, CreatedBy: userID, CostCenterID: nullID(in.CostCenterID)}
	if err := s.repo.CreateTask(ctx, t); err != nil {
		return nil, err
	}
	for _, tpl := range in.TemplateIDs {
		if strings.TrimSpace(tpl) != "" {
			if _, err := s.repo.AddTemplateSteps(ctx, t.ID, tpl); err != nil && !IsInputError(err) {
				return t, err
			}
		}
	}
	return t, nil
}

func (s *Service) UpdateTask(ctx context.Context, id string, in *UpdateTaskInput) error {
	if in.Priority == "" {
		in.Priority = PrioMedium
	}
	return s.repo.UpdateTask(ctx, id, in)
}

func (s *Service) UpdateDueDate(ctx context.Context, id string, due time.Time) error {
	return s.repo.UpdateDueDate(ctx, id, due)
}

func (s *Service) DeleteTask(ctx context.Context, id string) error {
	return s.repo.DeleteTask(ctx, id)
}

// StartTask: Auftrag beginnen (in Arbeit).
func (s *Service) StartTask(ctx context.Context, id, userID string) error {
	return s.repo.SetStatus(ctx, id, TaskInProgress)
}

// Accept: Leitstand „Annehmen“ – zuweisen und starten.
func (s *Service) Accept(ctx context.Context, id, assigneeID string) error {
	return s.repo.Assign(ctx, id, assigneeID)
}

// Wait: Wiedervorlage – Termin verschieben, Status „wartet“.
func (s *Service) Wait(ctx context.Context, id string, until time.Time) error {
	if err := s.repo.UpdateDueDate(ctx, id, until); err != nil {
		return err
	}
	return s.repo.SetStatus(ctx, id, TaskPending)
}

// Skip: Termin ueberspringen – der Plan bekommt seinen naechsten Auftrag.
func (s *Service) Skip(ctx context.Context, id, userID string) (*time.Time, error) {
	if err := s.repo.markSkipped(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.ScheduleNext(ctx, id, userID, false)
}

// TaskSkipped: Folgetermin fuer einen bereits uebersprungenen Auftrag planen.
func (s *Service) TaskSkipped(ctx context.Context, taskID, userID string) (*time.Time, error) {
	return s.repo.ScheduleNext(ctx, taskID, userID, false)
}

func (s *Service) NextDueForTask(ctx context.Context, taskID string) *time.Time {
	return s.repo.NextDueForTask(ctx, taskID)
}

// ── Schritte ─────────────────────────────────────────────────

// Steps: Schritte eines Auftrags (bei offenen Plan-Auftraegen zuerst aufbauen).
func (s *Service) Steps(ctx context.Context, taskID string) ([]*TaskStep, error) {
	if _, err := s.repo.BuildSteps(ctx, taskID, true); err != nil {
		return nil, err
	}
	return s.repo.Steps(ctx, taskID)
}

func (s *Service) SaveStep(ctx context.Context, taskID, stepID, value string, done bool, userID string) (*TaskStep, error) {
	return s.repo.SaveStep(ctx, taskID, stepID, value, done, userID)
}

// ── Abschluss ────────────────────────────────────────────────

// Complete schliesst einen Auftrag ab: Pflichtpunkte, mindestens eine
// Massnahme, Ersatzteile oder „kein Material“. Reservierte Teile werden zum
// Verbrauch, vorgemerkte gebucht. Danach: Checklisten-Takt fortschreiben,
// Folgetermin planen, Protokoll und Rueckmeldung (Hooks), Add-in-Ereignis.
func (s *Service) Complete(ctx context.Context, taskID, userID string, in *CompleteTaskInput) error {
	t, err := s.repo.GetTaskByID(ctx, taskID)
	if err != nil {
		return inputErr("Auftrag nicht gefunden")
	}
	if !t.IsOpen() {
		return inputErr("Der Auftrag ist bereits abgeschlossen")
	}
	if _, err := s.repo.BuildSteps(ctx, taskID, false); err != nil {
		return err
	}
	missing, err := s.repo.MissingRequired(ctx, taskID)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return inputErr("Bitte zuerst die Checkliste ausfüllen: " + strings.Join(missing, ", "))
	}
	if n, err := s.repo.CountActions(ctx, taskID); err != nil {
		return err
	} else if n == 0 {
		return inputErr("Bitte kurz beschreiben, was gemacht wurde (Maßnahme)")
	}
	parts, err := s.repo.GetPendingParts(ctx, taskID)
	if err != nil {
		return err
	}
	if !in.NoPartsNeeded && len(parts) == 0 {
		return inputErr("Bitte verwendetes Material erfassen oder „Kein Material verwendet“ bestätigen")
	}
	if invService != nil {
		// Reservierungen sind bereits abgebucht und werden zum Verbrauch
		if err := invService.ConsumeReservations(ctx, "maintenance_task", taskID); err != nil {
			return inputErr("Reservierungen konnten nicht verbucht werden: " + err.Error())
		}
		for _, pp := range parts {
			if pp.Reserved {
				continue
			}
			if _, err := invService.Book(ctx, &inventory.BookMovementInput{
				PartID: pp.PartID, Type: inventory.MovementOut, Qty: pp.Qty, StorageNodeID: pp.StorageNodeID,
				Reference: "Wartungsauftrag " + taskID, MaintenanceTaskID: taskID,
			}, pp.CreatedBy); err != nil {
				return inputErr("Buchung für „" + pp.PartName + "“ fehlgeschlagen: " + err.Error())
			}
			_ = s.repo.DeletePendingPart(ctx, pp.ID)
		}
	}
	if err := s.repo.markDone(ctx, taskID, strings.TrimSpace(in.Notes), in.DurationMin, in.NoPartsNeeded); err != nil {
		return err
	}
	// Takt der enthaltenen Plan-Checklisten fortschreiben
	_, _ = s.repo.db.Exec(ctx, `UPDATE maintenance_plan_checklists SET last_done_at = NOW()
		WHERE id IN (SELECT DISTINCT plan_checklist_id FROM maintenance_task_steps WHERE task_id=$1::uuid AND plan_checklist_id IS NOT NULL)`, taskID)
	s.repo.scheduleNextLogged(ctx, taskID, userID, true)
	runCompletedHooks(ctx, taskID, userID)
	if eventBus != nil {
		eventBus.Publish("maintenance.task_completed", map[string]interface{}{
			"id": taskID, "completed_by": userID, "notes": in.Notes, "duration_min": in.DurationMin,
		})
	}
	return nil
}

// CompleteTaskValidated: bisheriger Name (Leitstand, API).
func (s *Service) CompleteTaskValidated(ctx context.Context, taskID, userID string, in *CompleteTaskInput, noPartsNeeded bool) error {
	in.NoPartsNeeded = noPartsNeeded
	return s.Complete(ctx, taskID, userID, in)
}

// ── Massnahmen & Ersatzteile ─────────────────────────────────

func (s *Service) AddAction(ctx context.Context, taskID, description, userID string) (*TaskAction, error) {
	if strings.TrimSpace(description) == "" {
		return nil, inputErr("Beschreibung ist Pflicht")
	}
	return s.repo.AddAction(ctx, taskID, strings.TrimSpace(description), userID)
}

func (s *Service) GetActions(ctx context.Context, taskID string) ([]*TaskAction, error) {
	return s.repo.GetActions(ctx, taskID)
}

func (s *Service) DeleteAction(ctx context.Context, id string) error {
	return s.repo.DeleteAction(ctx, id)
}

// AddPendingPart: Ersatzteil reservieren (Lager bucht sofort ab) bzw. vormerken.
func (s *Service) AddPendingPart(ctx context.Context, taskID string, in *AddPendingPartInput, userID string) (*PendingPart, error) {
	if in.PartID == "" || in.StorageNodeID == "" {
		return nil, inputErr("Ersatzteil und Lagerort sind Pflicht")
	}
	if in.Qty <= 0 {
		return nil, inputErr("Menge muss größer als 0 sein")
	}
	if invService != nil {
		id, err := invService.Reserve(ctx, "maintenance_task", taskID, in.PartID, in.StorageNodeID, in.Qty, userID)
		if err != nil {
			return nil, err
		}
		return &PendingPart{ID: id, TaskID: taskID, PartID: in.PartID, StorageNodeID: in.StorageNodeID, Qty: in.Qty, Reserved: true, CreatedBy: userID}, nil
	}
	return s.repo.AddPendingPart(ctx, taskID, in.PartID, in.StorageNodeID, in.Qty, userID)
}

func (s *Service) GetPendingParts(ctx context.Context, taskID string) ([]*PendingPart, error) {
	return s.repo.GetPendingParts(ctx, taskID)
}

func (s *Service) DeletePendingPart(ctx context.Context, id string) error {
	return s.repo.DeletePendingPart(ctx, id)
}

func (s *Service) GetPartsUsage(ctx context.Context, taskID string) ([]*PartUsage, error) {
	return s.repo.GetPartsUsage(ctx, taskID)
}
