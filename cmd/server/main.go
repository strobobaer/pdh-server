package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", tmplPath).Msg("templates laden fehlgeschlagen")
	}
	log.Info().Int("count", len(tmpl.Templates())).Msg("templates geladen")

	// Web Handler
	webHandler := web.NewHandler(tmpl, userSvc, shiftSvc, storageSvc, infraSvc, ticketSvc, faultSvc, maintSvc, invSvc, itSvc, timeSvc, cfg.Auth.JWTSecret)

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
		r.Mount("/it", itHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/inventory", invHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/attachments", attachHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/checklists", checkHandler.Routes(cfg.Auth.JWTSecret))
	})

	// Static uploads
	r.Handle("/uploads/*", http.StripPrefix("/uploads/", http.FileServer(http.Dir("uploads"))))

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
