package faults

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"pdh/internal/core/addins"
	"pdh/internal/modules/inventory"
	"pdh/internal/core/synclink"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// Service
type Service struct {
	repo    *Repository
	copilot *Copilot
}

func NewService(repo *Repository, copilot *Copilot) *Service {
	globalFaultRepo = repo
	return &Service{repo: repo, copilot: copilot}
}

var eventBus *addins.EventBus

// SetEventBus verbindet dieses Modul mit dem Add-in-Ereignis-Bus (wird in main.go gesetzt).
func SetEventBus(b *addins.EventBus) { eventBus = b }

var faultLinker *synclink.Linker
var globalFaultRepo *Repository

// notifyFaultLinkerStatus ruft den synclink-Hook auf (falls gesetzt).
// Eigene Funktion, damit repository.go keinen Import von synclink braucht.
func notifyFaultLinkerStatus(ctx context.Context, faultID, status, userID string) {
	if faultLinker != nil {
		faultLinker.OnFaultStatusChanged(ctx, faultID, status, userID)
	}
}

// SetLinker verbindet das faults-Modul mit dem synclink-Paket (wird in
// main.go gesetzt), um Status und Massnahmen mit einem verknuepften
// Ticket zu synchronisieren.
func SetLinker(l *synclink.Linker) {
	faultLinker = l
	l.RegisterFault(
		func(ctx context.Context, faultID, status, userID string) error {
			return globalFaultRepo.SetStatusDirect(ctx, faultID, status, userID)
		},
		func(ctx context.Context, faultID, description, userID string) error {
			return globalFaultRepo.AddActionDirect(ctx, faultID, description, userID)
		},
		func(ctx context.Context, faultID string) (string, error) {
			return globalFaultRepo.GetLinkedTicketID(ctx, faultID)
		},
		func(ctx context.Context, faultID string) (string, error) {
			return globalFaultRepo.GetLinkedTaskID(ctx, faultID)
		},
	)
}

func (s *Service) Create(ctx context.Context, in *CreateFaultInput, userID string) (*Fault, error) {
	f := &Fault{
		Title:            in.Title,
		Description:      in.Description,
		Symptoms:         in.Symptoms,
		Severity:         in.Severity,
		InfrastructureID: in.InfrastructureID,
		AssignedTo:       in.AssignedTo,
		ResponsibleTo:    in.ResponsibleTo,
		CreatedBy:        userID,
		CostCenterID:     in.CostCenterID,
	}
	if err := s.repo.Create(ctx, f); err != nil {
		return f, err
	}
	s.syncFaultAsync(f)
	if eventBus != nil {
		eventBus.Publish("fault.created", map[string]interface{}{
			"id": f.ID, "title": f.Title, "severity": string(f.Severity), "created_by": f.CreatedBy,
		})
	}
	return f, nil
}

func (s *Service) GetByID(ctx context.Context, id string) (*Fault, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, status FaultStatus) ([]*Fault, error) {
	return s.repo.List(ctx, status)
}

func (s *Service) Analyze(ctx context.Context, faultID string) (*CopilotAnalysis, error) {
	fault, err := s.repo.GetByID(ctx, faultID)
	if err != nil {
		return nil, err
	}
	s.repo.UpdateStatus(ctx, faultID, StatusAnalyzing, "")
	analysis, err := s.copilot.Analyze(ctx, fault)
	if err != nil {
		s.repo.UpdateStatus(ctx, faultID, StatusDetected, "")
		return nil, err
	}
	s.repo.UpdateStatus(ctx, faultID, StatusInProgress, "")
	return analysis, nil
}

func (s *Service) GetAnalysis(ctx context.Context, faultID string) (*CopilotAnalysis, error) {
	return s.repo.GetAnalysis(ctx, faultID)
}

func (s *Service) Chat(ctx context.Context, faultID, userID, message string, history []anthropicMessage) (string, error) {
	fault, err := s.repo.GetByID(ctx, faultID)
	if err != nil {
		return "", err
	}
	return s.copilot.Chat(ctx, fault, history, message)
}

func (s *Service) Resolve(ctx context.Context, faultID, resolution, rootCause, userID string, noPartsNeeded bool) error {
	actionCount, err := s.repo.CountActions(ctx, faultID)
	if err != nil {
		return err
	}
	if actionCount == 0 {
		return fmt.Errorf("bitte mindestens eine durchgeführte maßnahme erfassen, bevor die störung gelöst werden kann")
	}

	pendingParts, err := s.repo.GetPendingParts(ctx, faultID)
	if err != nil {
		return err
	}
	if !noPartsNeeded && len(pendingParts) == 0 {
		return fmt.Errorf(`bitte verwendete ersatzteile erfassen oder "keine teile benötigt" bestätigen`)
	}

	// Vorgemerkte Ersatzteile jetzt tatsaechlich buchen (Warenausgang).
	// Erfolgreich gebuchte Eintraege werden sofort aus der Merkliste
	// entfernt, damit ein Wiederholungsversuch nach einem Fehler nicht
	// bereits gebuchte Teile doppelt bucht.
	if invService != nil {
		for _, pp := range pendingParts {
			_, bookErr := invService.Book(ctx, &inventory.BookMovementInput{
				PartID: pp.PartID, Type: inventory.MovementOut, Qty: pp.Qty,
				StorageNodeID: pp.StorageNodeID, Reference: "Störung " + faultID, FaultID: faultID,
			}, pp.CreatedBy)
			if bookErr != nil {
				return fmt.Errorf("buchung für ersatzteil \"%s\" fehlgeschlagen: %w", pp.PartName, bookErr)
			}
			_ = s.repo.DeletePendingPart(ctx, pp.ID)
		}
	}

	err = s.repo.Resolve(ctx, faultID, resolution, rootCause, userID, noPartsNeeded)
	if err == nil && eventBus != nil {
		eventBus.Publish("fault.resolved", map[string]interface{}{
			"id": faultID, "resolution": resolution, "root_cause": rootCause, "resolved_by": userID,
		})
	}
	if err == nil && faultLinker != nil {
		faultLinker.OnFaultStatusChanged(ctx, faultID, "resolved", userID)
	}
	return err
}

func (s *Service) UpdateCostCenter(ctx context.Context, id string, costCenterID *string) error {
	return s.repo.UpdateCostCenter(ctx, id, costCenterID)
}

// QuickResolve schließt eine Störung OHNE die Pflichtprüfung (mind. eine
// Maßnahme, Ersatzteile/"keine benötigt") ab. Gedacht für den generischen
// Archivieren-Weg (z.B. versehentlich angelegte Datensätze), der Tickets
// und Störungen gleich behandelt und keine detaillierte
// Störungsbehebung durchläuft.
func (s *Service) QuickResolve(ctx context.Context, faultID, resolution, rootCause, userID string) error {
	err := s.repo.Resolve(ctx, faultID, resolution, rootCause, userID, true)
	if err == nil && eventBus != nil {
		eventBus.Publish("fault.resolved", map[string]interface{}{
			"id": faultID, "resolution": resolution, "root_cause": rootCause, "resolved_by": userID,
		})
	}
	return err
}

// Handler
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.GetByID)
	r.Post("/{id}/analyze", h.Analyze)
	r.Get("/{id}/analysis", h.GetAnalysis)
	r.Post("/{id}/chat", h.Chat)
	r.Post("/{id}/resolve", h.Resolve)
	r.Post("/{id}/cost-center", h.UpdateCostCenter)
	r.Post("/{id}/actions", h.AddAction)
	r.Get("/{id}/actions", h.GetActions)
	r.Delete("/{id}/actions/{actionID}", h.DeleteAction)
	r.Get("/{id}/parts-usage", h.GetPartsUsage)
	r.Post("/{id}/pending-parts", h.AddPendingPart)
	r.Get("/{id}/pending-parts", h.GetPendingParts)
	r.Delete("/{id}/pending-parts/{partItemID}", h.DeletePendingPart)

	return r
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := FaultStatus(r.URL.Query().Get("status"))
	faults, err := h.svc.List(r.Context(), status)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, faults)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateFaultInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungueltige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	fault, err := h.svc.Create(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, fault)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	fault, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusNotFound, "stoerung nicht gefunden")
		return
	}
	response.JSON(w, http.StatusOK, fault)
}

func (h *Handler) Analyze(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	analysis, err := h.svc.Analyze(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "copilot analyse fehlgeschlagen: "+err.Error())
		return
	}
	response.JSON(w, http.StatusOK, analysis)
}

func (h *Handler) GetAnalysis(w http.ResponseWriter, r *http.Request) {
	analysis, err := h.svc.GetAnalysis(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, http.StatusNotFound, "keine analyse vorhanden")
		return
	}
	response.JSON(w, http.StatusOK, analysis)
}

func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Message string             `json:"message"`
		History []anthropicMessage `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungueltige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	reply, err := h.svc.Chat(r.Context(), id, userID, in.Message, in.History)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"reply": reply})
}

func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in ResolveInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungueltige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.Resolve(r.Context(), id, in.Resolution, in.RootCause, userID, in.NoPartsNeeded); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// UpdateCostCenter akzeptiert sowohl JSON als auch normale Formulardaten
// (fuer htmx-Formulare wie in fault_detail.gohtml).
func (h *Handler) UpdateCostCenter(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var costCenterID *string

	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var in struct {
			CostCenterID *string `json:"cost_center_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			response.Error(w, http.StatusBadRequest, "ungueltige eingabe")
			return
		}
		costCenterID = in.CostCenterID
	} else {
		if err := r.ParseForm(); err != nil {
			response.Error(w, http.StatusBadRequest, "ungueltige eingabe")
			return
		}
		if v := r.FormValue("cost_center_id"); v != "" {
			costCenterID = &v
		}
	}

	if err := h.svc.UpdateCostCenter(r.Context(), id, costCenterID); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "aktualisiert"})
}
