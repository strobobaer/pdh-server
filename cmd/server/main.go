package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"pdh/internal/core/infrastructure"
	"pdh/internal/core/shifts"
	"pdh/internal/core/storage"
	"pdh/internal/core/users"
	"pdh/internal/modules/attachments"
	"pdh/internal/modules/checklists"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/inventory"
	"pdh/internal/modules/it"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tickets"
	"pdh/internal/modules/timetracking"
	"pdh/internal/web"
	"pdh/pkg/config"
	"pdh/pkg/database"
	"pdh/pkg/logger"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	logger.Init(cfg.Server.Env)
	log.Info().Str("env", cfg.Server.Env).Msg("PDH startet")

	db, err := database.New(&cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("datenbank")
	}
	defer db.Close()
	log.Info().Msg("datenbank verbunden")

	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer migrationCancel()
	if err := database.RunMigrations(migrationCtx, db.Pool, "migrations"); err != nil {
		log.Fatal().Err(err).Msg("migrationen")
	}
	log.Info().Msg("migrationen geprüft")

	// Services
	userRepo := users.NewRepository(db.Pool)
	userSvc := users.NewService(userRepo, cfg.Auth.JWTSecret, cfg.Auth.TokenDuration)
	userHandler := users.NewHandler(userSvc)

	shiftRepo := shifts.NewRepository(db.Pool)

	storageRepo := storage.NewRepository(db.Pool)
	storageSvc := storage.NewService(storageRepo)
	storageHandler := storage.NewHandler(storageSvc)
	shiftSvc := shifts.NewService(shiftRepo)
	shiftHandler := shifts.NewHandler(shiftSvc)

	infraRepo := infrastructure.NewRepository(db.Pool)
	infraSvc := infrastructure.NewService(infraRepo)
	infraHandler := infrastructure.NewHandler(infraSvc)

	ticketRepo := tickets.NewRepository(db.Pool)
	ticketSvc := tickets.NewService(ticketRepo)
	ticketHandler := tickets.NewHandler(ticketSvc)

	faultRepo := faults.NewRepository(db.Pool)
	copilot := faults.NewCopilot(cfg.Copilot.AnthropicKey, cfg.Copilot.OllamaURL, cfg.Copilot.Model, faultRepo)
	faultSvc := faults.NewService(faultRepo, copilot)
	faultHandler := faults.NewHandler(faultSvc)

	timeRepo := timetracking.NewRepository(db.Pool)
	timeSvc := timetracking.NewService(timeRepo)
	timeHandler := timetracking.NewHandler(timeSvc)

	maintRepo := maintenance.NewRepository(db.Pool)
	maintSvc := maintenance.NewService(maintRepo)
	maintHandler := maintenance.NewHandler(maintSvc)

	invRepo := inventory.NewRepository(db.Pool)

	itRepo := it.NewRepository(db.Pool)
	itSvc := it.NewService(itRepo)
	itHandler := it.NewHandler(itSvc)
	invSvc := inventory.NewService(invRepo)
	invHandler := inventory.NewHandler(invSvc)

	// Uploads-Verzeichnis
	os.MkdirAll("uploads", 0755)

	// Modul: Anhänge
	attachRepo := attachments.NewRepository(db.Pool)
	attachSvc := attachments.NewService(attachRepo)
	attachHandler := attachments.NewHandler(attachSvc)

	// Modul: Checklisten
	checkRepo := checklists.NewRepository(db.Pool)
	checkSvc := checklists.NewService(checkRepo)
	checkHandler := checklists.NewHandler(checkSvc)

	// Templates laden
	tmplPath := filepath.Join("web", "templates", "base.gohtml")
	tmpl, err := template.New(filepath.Base(tmplPath)).Funcs(web.TemplateFuncs()).ParseFiles(tmplPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", tmplPath).Msg("templates laden fehlgeschlagen")
	}
	if _, err := tmpl.ParseGlob(filepath.Join("web", "templates", "widgets", "*.gohtml")); err != nil {
		log.Fatal().Err(err).Msg("widget-templates laden fehlgeschlagen")
	}
	log.Info().Int("count", len(tmpl.Templates())).Msg("templates geladen")

	// Web Handler
	webHandler := web.NewHandler(db.Pool, tmpl, userSvc, shiftSvc, storageSvc, infraSvc, ticketSvc, faultSvc, maintSvc, invSvc, itSvc, timeSvc, checkSvc, cfg.Auth.JWTSecret)

	log.Info().Str("backend", cfg.Copilot.Backend).Str("model", cfg.Copilot.Model).Msg("copilot bereit")

	r := chi.NewRouter()
	r.Use(chimw.Recoverer, chimw.RequestID, middleware.Logger, middleware.CORS)

	// API
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			http.Error(w, "db error", 503)
			return
		}
		response.JSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": "pdh",
			"version": "0.8.0",
		})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Mount("/users", userHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/storage", storageHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/shifts", shiftHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/infrastructure", infraHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/tickets", ticketHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/faults", faultHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/time", timeHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/maintenance", maintHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/maintenance-checklists", maintHandler.ChecklistRoutes(cfg.Auth.JWTSecret))
		r.Mount("/it", itHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/inventory", invHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/attachments", attachHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/checklists", checkHandler.Routes(cfg.Auth.JWTSecret))
	})

	// Web-save fallback: speichert Checklisten-Zuordnung und Standarddauer zusammen mit dem Wartungsplan.
	// Diese Route liegt vor dem generischen Web-Mount und verhindert, dass ein fehlgeschlagener Browser-API-Call
	// die Wartungsplan-Stammdaten unvollständig lässt.
	r.Put("/maintenance/plans/{id}/edit-web", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
			return
		}
		planID := chi.URLParam(r, "id")
		name := strings.TrimSpace(r.FormValue("name"))
		infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
		if name == "" || infraID == "" {
			http.Error(w, "Name und Infrastruktur sind Pflicht", http.StatusBadRequest)
			return
		}
		intervalDays, _ := strconv.Atoi(r.FormValue("interval_days"))
		estimatedMin, _ := strconv.Atoi(r.FormValue("estimated_min"))
		defaultDurationMin, _ := strconv.Atoi(r.FormValue("default_duration_min"))
		if defaultDurationMin > 0 {
			estimatedMin = defaultDurationMin
		}
		_, err := db.Pool.Exec(r.Context(), `
			UPDATE maintenance_plans
			SET name=$1,
			    description=COALESCE(NULLIF($2,''), description),
			    type=$3::maintenance_type,
			    infrastructure_id=$4::uuid,
			    interval_type=$5::maintenance_interval,
			    interval_days=CASE WHEN $6 > 0 THEN $6 ELSE interval_days END,
			    estimated_min=CASE WHEN $7 > 0 THEN $7 ELSE estimated_min END,
			    default_duration_min=$8,
			    priority=$9::maintenance_priority,
			    next_due_at=CASE WHEN NULLIF($10,'') IS NULL THEN next_due_at ELSE $10::timestamptz END
			WHERE id=$11`,
			name,
			strings.TrimSpace(r.FormValue("description")),
			r.FormValue("type"),
			infraID,
			r.FormValue("interval"),
			intervalDays,
			estimatedMin,
			defaultDurationMin,
			r.FormValue("priority"),
			strings.TrimSpace(r.FormValue("next_due_at")),
			planID,
		)
		if err != nil {
			http.Error(w, "Wartungsplan konnte nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		templateIDs := r.Form["checklist_template_ids"]
		if len(templateIDs) == 0 {
			templateIDs = r.Form["checklist_template_id"]
		}
		if err := maintRepo.AssignChecklistTemplatesToPlan(r.Context(), planID, templateIDs, defaultDurationMin); err != nil {
			http.Error(w, "Checklisten konnten nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Gespeichert</span>`))
	})

	r.Delete("/maintenance/plans/{id}/delete-web", func(w http.ResponseWriter, r *http.Request) {
		if _, err := db.Pool.Exec(r.Context(), `UPDATE maintenance_plans SET active=false WHERE id=$1`, chi.URLParam(r, "id")); err != nil {
			http.Error(w, "Wartungsplan konnte nicht vorgemerkt werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Static uploads
	r.Handle("/uploads/*", http.StripPrefix("/uploads/", http.FileServer(http.Dir("uploads"))))

	// SSO routes must be mounted before the protected web UI.
	r.Get("/sso/nextcloud", web.NextcloudSSOHandler(db.Pool, cfg.Auth.JWTSecret))

	// Web UI
	r.Mount("/", webHandler.Routes())

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: r,
		ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Info().Str("addr", addr).Msg("server läuft")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server fehler")
		}
	}()
	<-quit
	log.Info().Msg("PDH wird gestoppt...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Info().Msg("PDH gestoppt")
}
