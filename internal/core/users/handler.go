package users

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"pdh/internal/core/rbac"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

type Handler struct {
	svc  *Service
	rbac *rbac.Service
}

func NewHandler(svc *Service, rb *rbac.Service) *Handler {
	return &Handler{svc: svc, rbac: rb}
}

// Routes - registriert alle User-Routen
func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()

	// Öffentliche Routen
	r.Post("/login", h.Login)
	// /register ist NICHT mehr oeffentlich: den ersten Administrator legt der
	// Einrichtungsassistent an, weitere Benutzer die Benutzerverwaltung.

	// Geschützte Routen
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(jwtSecret))
		r.Get("/", h.List)
		r.Get("/{id}", h.GetByID)
		r.Post("/{id}", h.Update) // FIX: war PUT, wird von Cloudflare/Nginx blockiert
		r.Post("/set-locksmith/{slot}", h.SetLocksmithSlot)

		// Nur mit "system.manage_users"-Berechtigung (frueher hart auf die
		// Rolle "admin" verdrahtet - jetzt ueber die Rollen-/Berechtigungs-
		// verwaltung konfigurierbar).
		r.Group(func(r chi.Router) {
			r.Use(h.rbac.RequirePermission("system.manage_users"))
			r.Delete("/{id}", h.Deactivate)
			r.Post("/register", h.Register)
		})
	})

	return r
}

func (h *Handler) SetLocksmithSlot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	slot, err := strconv.Atoi(chi.URLParam(r, "slot"))
	if err != nil || (slot != 1 && slot != 2) {
		response.Error(w, http.StatusBadRequest, "ungültiger slot (nur 1 oder 2)")
		return
	}
	if err := h.svc.SetLocksmithSlot(r.Context(), slot, in.UserID); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "gespeichert"})
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}

	token, user, err := h.svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		log.Warn().Str("bereich", "auth").Str("verfahren", "api").Str("login", in.Email).Str("ip", r.RemoteAddr).Str("grund", err.Error()).Msg("anmeldung fehlgeschlagen")
		response.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !user.IsSystemUser && h.svc.PasswordState(r.Context(), user.ID).Required() {
		// Pflichtwechsel (Erstanmeldung, vom Administrator gesetzt, abgelaufen): erst im Browser aendern
		log.Warn().Str("bereich", "auth").Str("verfahren", "api").Str("login", in.Email).Str("user", user.ID).Str("ip", r.RemoteAddr).Msg("anmeldung abgelehnt: passwortwechsel noetig")
		response.Error(w, http.StatusForbidden, "passwort muss geändert werden – bitte zuerst im browser anmelden")
		return
	}
	log.Info().Str("bereich", "auth").Str("verfahren", "api").Str("login", in.Email).Str("user", user.ID).Str("ip", r.RemoteAddr).Msg("anmeldung erfolgreich")

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"user":  user,
	})
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var in CreateUserInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	// Die Admin-Rolle darf nur ein Administrator vergeben.
	if actorRole, _ := r.Context().Value(middleware.RoleKey).(string); in.Role == RoleAdmin && actorRole != string(RoleAdmin) {
		response.Error(w, http.StatusForbidden, "nur administratoren dürfen die admin-rolle vergeben")
		return
	}

	user, err := h.svc.Register(r.Context(), &in)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	response.JSON(w, http.StatusCreated, user)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, users)
}

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "benutzer nicht gefunden")
		return
	}
	response.JSON(w, http.StatusOK, user)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		response.Error(w, http.StatusBadRequest, "ungültige eingabe")
		return
	}
	u.ID = id
	if err := h.svc.Update(r.Context(), &u); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, u)
}

func (h *Handler) Deactivate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Deactivate(r.Context(), id); err != nil {
		response.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "deaktiviert"})
}
