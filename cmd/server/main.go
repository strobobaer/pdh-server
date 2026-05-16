package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"pdh/internal/core/infrastructure"
	"pdh/internal/core/shifts"
	"pdh/internal/core/users"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/tickets"
	"pdh/internal/modules/timetracking"
	"pdh/pkg/config"
	"pdh/pkg/database"
	"pdh/pkg/logger"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

func main() {
	cfg, err := config.Load()
	if err != nil { fmt.Fprintf(os.Stderr, "config fehler: %v\n", err); os.Exit(1) }

	logger.Init(cfg.Server.Env)
	log.Info().Str("env", cfg.Server.Env).Msg("PDH startet")

	db, err := database.New(&cfg.Database)
	if err != nil { log.Fatal().Err(err).Msg("datenbank fehler") }
	defer db.Close()
	log.Info().Msg("datenbank verbunden")

	userRepo    := users.NewRepository(db.Pool)
	userSvc     := users.NewService(userRepo, cfg.Auth.JWTSecret, cfg.Auth.TokenDuration)
	userHandler := users.NewHandler(userSvc)

	shiftRepo    := shifts.NewRepository(db.Pool)
	shiftSvc     := shifts.NewService(shiftRepo)
	shiftHandler := shifts.NewHandler(shiftSvc)

	infraRepo    := infrastructure.NewRepository(db.Pool)
	infraSvc     := infrastructure.NewService(infraRepo)
	infraHandler := infrastructure.NewHandler(infraSvc)

	ticketRepo    := tickets.NewRepository(db.Pool)
	ticketSvc     := tickets.NewService(ticketRepo)
	ticketHandler := tickets.NewHandler(ticketSvc)

	faultRepo    := faults.NewRepository(db.Pool)
	copilot      := faults.NewCopilot(cfg.Copilot.AnthropicKey, cfg.Copilot.OllamaURL, cfg.Copilot.Model, faultRepo)
	faultSvc     := faults.NewService(faultRepo, copilot)
	faultHandler := faults.NewHandler(faultSvc)

	timeRepo    := timetracking.NewRepository(db.Pool)
	timeSvc     := timetracking.NewService(timeRepo)
	timeHandler := timetracking.NewHandler(timeSvc)

	maintRepo    := maintenance.NewRepository(db.Pool)
	maintSvc     := maintenance.NewService(maintRepo)
	maintHandler := maintenance.NewHandler(maintSvc)

	log.Info().Str("backend", cfg.Copilot.Backend).Str("model", cfg.Copilot.Model).Msg("copilot bereit")

	r := chi.NewRouter()
	r.Use(chimw.Recoverer, chimw.RequestID, middleware.Logger, middleware.CORS)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]interface{}{
			"service": "PDH – Prozess Data Hub",
			"version": "0.6.0",
			"modules": []string{"users","shifts","infrastructure","tickets","faults","timetracking","maintenance"},
			"copilot": copilot.Info(),
		})
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil { http.Error(w, "db error", 503); return }
		w.Write([]byte(`{"status":"ok","service":"pdh"}`))
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Mount("/users",          userHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/shifts",         shiftHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/infrastructure", infraHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/tickets",        ticketHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/faults",         faultHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/time",           timeHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/maintenance",    maintHandler.Routes(cfg.Auth.JWTSecret))
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: r,
		ReadTimeout: 15*time.Second, WriteTimeout: 120*time.Second, IdleTimeout: 60*time.Second}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Info().Str("addr", addr).Msg("server läuft")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server fehler")
		}
	}()
	<-quit
	log.Info().Msg("server wird gestoppt...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Info().Msg("server gestoppt")
}
