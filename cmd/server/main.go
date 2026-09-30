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

	"pdh/internal/core/addins"
	"pdh/internal/core/costcenters"
	"pdh/internal/core/directory"
	"pdh/internal/core/infrastructure"
	"pdh/internal/core/rbac"
	"pdh/internal/core/shifts"
	"pdh/internal/core/storage"
	"pdh/internal/core/synclink"
	"pdh/internal/core/users"
	"pdh/internal/modules/attachments"
	"pdh/internal/modules/checklists"
	"pdh/internal/modules/faults"
	"pdh/internal/modules/inventory"
	"pdh/internal/modules/it"
	"pdh/internal/modules/maintenance"
	"pdh/internal/modules/projects"
	"pdh/internal/modules/tasks"
	"pdh/internal/modules/tickets"
	"pdh/internal/modules/timetracking"
	"pdh/internal/setup"
	"pdh/internal/web"
	"pdh/pkg/config"
	"pdh/pkg/database"
	"pdh/pkg/logger"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

var buildCommit = "unknown"

func main() {
	// Erstinstallation: Einstellungsdatei laden und fehlendes JWT-Secret
	// automatisch erzeugen (statt mit Fehler abzubrechen).
	envPath := config.EnvFilePath()
	if err := config.ApplyEnvFile(envPath); err != nil {
		fmt.Fprintf(os.Stderr, "einstellungsdatei %s: %v\n", envPath, err)
		os.Exit(1)
	}
	jwtCreated, err := config.EnsureJWTSecret(envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	logger.Init(cfg.Server.Env)
	log.Info().Str("env", cfg.Server.Env).Str("einstellungen", envPath).Msg("PDH startet")
	if jwtCreated {
		log.Info().Str("datei", envPath).Msg("neues JWT-Secret erzeugt und gespeichert")
	}
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)

	db, err := openDatabase(&cfg.Database)
	if err != nil {
		// Datenbank fehlt/unerreichbar -> Einrichtungsassistent im Browser
		log.Warn().Err(err).Msg("datenbank nicht verfügbar – starte Einrichtungsassistent")
		if err := setup.Run(context.Background(), setup.Options{Addr: addr, NeedDB: true, DBError: err.Error()}); err != nil {
			log.Fatal().Err(err).Msg("einrichtung")
		}
		if cfg, err = config.Load(); err != nil {
			log.Fatal().Err(err).Msg("config")
		}
		if db, err = openDatabase(&cfg.Database); err != nil {
			log.Fatal().Err(err).Msg("datenbank")
		}
	}
	defer db.Close()
	log.Info().Msg("datenbank verbunden")

	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer migrationCancel()
	if err := database.RunMigrations(migrationCtx, db.Pool, "migrations"); err != nil {
		log.Fatal().Err(err).Msg("migrationen")
	}
	log.Info().Msg("migrationen geprüft")

	// Noch kein Benutzer (frische Datenbank, z. B. Docker): ersten
	// Administrator im Browser anlegen lassen.
	var humans int
	if err := db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users WHERE NOT is_bot`).Scan(&humans); err == nil && humans == 0 {
		log.Warn().Msg("noch kein benutzer vorhanden – starte Einrichtungsassistent")
		if err := setup.Run(context.Background(), setup.Options{Addr: addr, Pool: db.Pool}); err != nil {
			log.Fatal().Err(err).Msg("einrichtung")
		}
		if cfg, err = config.Load(); err != nil { // z. B. neue oeffentliche Adresse
			log.Fatal().Err(err).Msg("config")
		}
	}

	// Services
	userRepo := users.NewRepository(db.Pool)
	userSvc := users.NewService(userRepo, cfg.Auth.JWTSecret, cfg.Auth.TokenDuration)

	rbacRepo := rbac.NewRepository(db.Pool)
	rbacSvc := rbac.NewService(rbacRepo)
	if err := rbacSvc.Warm(context.Background()); err != nil {
		log.Warn().Err(err).Msg("rbac-cache konnte nicht initial geladen werden")
	}

	userHandler := users.NewHandler(userSvc, rbacSvc)

	shiftRepo := shifts.NewRepository(db.Pool)

	storageRepo := storage.NewRepository(db.Pool)
	storageSvc := storage.NewService(storageRepo)
	storageHandler := storage.NewHandler(storageSvc)
	shiftSvc := shifts.NewService(shiftRepo)
	shiftHandler := shifts.NewHandler(shiftSvc)

	infraRepo := infrastructure.NewRepository(db.Pool)
	infraSvc := infrastructure.NewService(infraRepo)
	infraHandler := infrastructure.NewHandler(infraSvc)

	costCenterRepo := costcenters.NewRepository(db.Pool)
	costCenterSvc := costcenters.NewService(costCenterRepo)
	costCenterHandler := costcenters.NewHandler(costCenterSvc)

	directoryRepo := directory.NewRepository(db.Pool)
	directorySvc := directory.NewService(directoryRepo)
	directoryHandler := directory.NewHandler(directorySvc)

	ticketRepo := tickets.NewRepository(db.Pool)
	ticketSvc := tickets.NewService(ticketRepo)
	ticketHandler := tickets.NewHandler(ticketSvc)

	taskRepo := tasks.NewRepository(db.Pool)
	taskSvc := tasks.NewService(taskRepo)
	taskHandler := tasks.NewHandler(taskSvc)

	projectRepo := projects.NewRepository(db.Pool)
	projectSvc := projects.NewService(projectRepo)
	projectHandler := projects.NewHandler(projectSvc)

	faultRepo := faults.NewRepository(db.Pool)
	// FIX: AnthropicModel wird jetzt aus der Config übergeben statt hardcoded (copilot.go)
	copilot := faults.NewCopilot(cfg.Copilot.AnthropicKey, cfg.Copilot.OllamaURL, cfg.Copilot.Model, cfg.Copilot.AnthropicModel, faultRepo)
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
	maintenance.SetInventoryService(invSvc)
	tickets.SetInventoryService(invSvc)
	faults.SetInventoryService(invSvc)
	tasks.SetInventoryService(invSvc)

	// Add-ins: Ereignis-Bus + Verwaltung (nur Admin, siehe internal/core/addins)
	addinsRepo := addins.NewRepository(db.Pool)
	addinsBus := addins.NewEventBus(addinsRepo)
	addinsHandler := addins.NewHandler(addinsRepo, addinsBus)
	tickets.SetEventBus(addinsBus)
	faults.SetEventBus(addinsBus)

	// Stoerung <-> Ticket Synchronisation (Status + Massnahmen)
	linker := synclink.New()
	faults.SetLinker(linker)
	tickets.SetLinker(linker)
	tasks.SetLinker(linker)
	inventory.SetEventBus(addinsBus)
	maintenance.SetEventBus(addinsBus)

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
	webHandler := web.NewHandler(db.Pool, tmpl, userSvc, shiftSvc, storageSvc, infraSvc, ticketSvc, faultSvc, maintSvc, invSvc, itSvc, timeSvc, checkSvc, taskSvc, projectSvc, rbacSvc, cfg.Auth.JWTSecret)
	webHandler.ConfigureUpdates(cfg.Update.AgentURL, cfg.Update.AgentToken, buildCommit)
	webHandler.ConfigureMicrosoft(web.MicrosoftOAuthConfig{
		ClientID:          cfg.Microsoft.ClientID,
		ClientSecret:      cfg.Microsoft.ClientSecret,
		RedirectURL:       cfg.Microsoft.RedirectURL,
		TenantID:          cfg.Microsoft.TenantID,
		TeamsSenderUserID: cfg.Microsoft.TeamsSenderUserID,
		TeamsID:           cfg.Microsoft.TeamsID,
		TeamsChannelID:    cfg.Microsoft.TeamsChannelID,
	})
	webHandler.StartUpdateChecker(context.Background())
	webHandler.StartEnabledMqttBrokers(context.Background())
	webHandler.StartEnabledMqttConsumers(context.Background())
	webHandler.StartEnabledExportSchedules(context.Background())
	webHandler.ConfigureMail(cfg.Mail)
	webHandler.StartPurchaseReportSchedule(context.Background())
	webHandler.StartBackupSystem(context.Background())
	webHandler.StartChangeNotifier(context.Background())
	webHandler.StartEnabledImportPolls(context.Background())

	log.Info().Str("backend", cfg.Copilot.Backend).Str("model", cfg.Copilot.Model).Msg("copilot bereit")

	r := chi.NewRouter()
	r.Use(chimw.Recoverer, chimw.RequestID, middleware.Logger, middleware.CORS)
	// Aenderungshinweise (Bearbeiter merken), Stammdaten-Sperre, DELETE -> Loeschvormerkung
	r.Use(webHandler.ChangeTrackingMiddleware)

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
		r.Mount("/costcenters", costCenterHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/directory", directoryHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/tickets", ticketHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/tasks", taskHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/projects", projectHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/faults", faultHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/time", timeHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/maintenance", maintHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/maintenance-checklists", maintHandler.ChecklistRoutes(cfg.Auth.JWTSecret))
		r.Mount("/it", itHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/inventory", invHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/attachments", attachHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/checklists", checkHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/addins", addinsHandler.Routes(cfg.Auth.JWTSecret))
	})

	// Web-save fallbacks: diese Routen liegen vor dem generischen Web-Mount.
	r.Post("/maintenance/plans", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil { // FIX: r.ParseForm() parst kein multipart/form-data (FormData+fetch)
			http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		infraID := strings.TrimSpace(r.FormValue("infrastructure_id"))
		log.Info().Str("name", name).Str("infrastructure_id", infraID).Str("type", r.FormValue("type")).Str("interval", r.FormValue("interval")).Str("priority", r.FormValue("priority")).Str("first_due_at", r.FormValue("first_due_at")).Msg("maintenance plan create form")
		if name == "" || infraID == "" {
			http.Error(w, "Name und Infrastruktur sind Pflicht", http.StatusBadRequest)
			return
		}
		interval := r.FormValue("interval")
		intervalDays := 30
		switch interval {
		case "daily":
			intervalDays = 1
		case "weekly":
			intervalDays = 7
		case "monthly":
			intervalDays = 30
		case "quarterly":
			intervalDays = 90
		case "yearly":
			intervalDays = 365
		}
		firstDue := strings.TrimSpace(r.FormValue("first_due_at"))
		if firstDue == "" {
			firstDue = time.Now().Format("2006-01-02")
		}
		var createdBy string
		if err := db.Pool.QueryRow(r.Context(), `SELECT id::text FROM users WHERE active=true ORDER BY created_at LIMIT 1`).Scan(&createdBy); err != nil || createdBy == "" {
			http.Error(w, "Kein aktiver Benutzer fuer created_by gefunden", http.StatusInternalServerError)
			return
		}
		cmd, err := db.Pool.Exec(r.Context(), `
			INSERT INTO maintenance_plans
			  (id, name, description, type, infrastructure_id, interval_type, interval_days,
			   estimated_min, priority, assigned_to, active, next_due_at, created_by, cost_center_id)
			VALUES (gen_random_uuid(), $1, $2, $3::maintenance_type, $4::uuid, $5::maintenance_interval, $6,
				0, $7::maintenance_priority, NULLIF($8,'')::uuid, true, $9::date, $10::uuid, NULLIF($11,'')::uuid)`,
			name,
			strings.TrimSpace(r.FormValue("description")),
			r.FormValue("type"),
			infraID,
			interval,
			intervalDays,
			r.FormValue("priority"),
			strings.TrimSpace(r.FormValue("assigned_to")),
			firstDue,
			createdBy,
			strings.TrimSpace(r.FormValue("cost_center_id")),
		)
		if err != nil {
			log.Error().Err(err).Msg("maintenance plan create failed")
			http.Error(w, "Wartungsplan konnte nicht angelegt werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info().Int64("rows", cmd.RowsAffected()).Msg("maintenance plan created")
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/maintenance")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
	})

	// FIX: war r.Put(...) - PUT wird von Cloudflare/Nginx blockiert, siehe gleiches
	// Problem bei users/tickets/shifts. Frontend muss ggf. auf POST umgestellt werden.
	r.Post("/maintenance/plans/{id}/edit-web", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
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
		templateIDs := r.Form["checklist_template_ids"]
		if len(templateIDs) == 0 {
			templateIDs = r.Form["checklist_template_id"]
		}
		log.Info().Str("plan_id", planID).Str("name", name).Str("infrastructure_id", infraID).Int("default_duration_min", defaultDurationMin).Strs("template_ids", templateIDs).Msg("maintenance plan edit form")
		cmd, err := db.Pool.Exec(r.Context(), `
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
			    next_due_at=CASE WHEN NULLIF($10,'') IS NULL THEN next_due_at ELSE $10::date END,
			    cost_center_id=NULLIF($11,'')::uuid,
			active=true
			WHERE id=$12`,
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
			strings.TrimSpace(r.FormValue("cost_center_id")),
			planID,
		)
		if err != nil {
			log.Error().Err(err).Str("plan_id", planID).Msg("maintenance plan edit failed")
			http.Error(w, "Wartungsplan konnte nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := maintRepo.AssignChecklistTemplatesToPlan(r.Context(), planID, templateIDs, defaultDurationMin); err != nil {
			log.Error().Err(err).Str("plan_id", planID).Strs("template_ids", templateIDs).Msg("maintenance checklist assignment failed")
			http.Error(w, "Checklisten konnten nicht gespeichert werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info().Int64("rows", cmd.RowsAffected()).Str("plan_id", planID).Msg("maintenance plan edited")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<span style="color:var(--green);font-size:12px"><i class="ti ti-check"></i> Gespeichert</span>`))
	})

	r.Post("/maintenance/plans/restore-all-web", func(w http.ResponseWriter, r *http.Request) {
		cmd, err := db.Pool.Exec(r.Context(), `UPDATE maintenance_plans SET active=true WHERE active=false`)
		if err != nil {
			log.Error().Err(err).Msg("maintenance plans restore failed")
			http.Error(w, "Wartungspläne konnten nicht wiederhergestellt werden: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info().Int64("rows", cmd.RowsAffected()).Msg("maintenance plans restored")
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/maintenance")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/maintenance", http.StatusSeeOther)
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

	// Public shop-floor dashboard; its write actions authenticate each operator by RFID.
	r.Mount("/global", webHandler.GlobalDashboardRoutes())

	// Web UI
	r.Mount("/", webHandler.Routes())

	srv := &http.Server{Addr: addr, Handler: r,
		ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// Neustart aus Verwaltung -> Server-Einstellungen
	restart := make(chan struct{}, 1)
	webHandler.SetRestartFunc(func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	})
	go func() {
		log.Info().Str("addr", addr).Msg("server läuft")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server fehler")
		}
	}()
	doRestart := false
	select {
	case <-quit:
	case <-restart:
		doRestart = true
	}
	log.Info().Bool("neustart", doRestart).Msg("PDH wird gestoppt...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	webHandler.StopMqttBrokers()
	webHandler.StopExportSchedules()
	webHandler.StopImportPolls()
	if doRestart {
		db.Close()
		log.Info().Msg("PDH startet neu")
		if err := execSelf(); err != nil {
			log.Error().Err(err).Msg("neustart fehlgeschlagen – bitte den Dienst manuell starten")
			os.Exit(1)
		}
	}
	log.Info().Msg("PDH gestoppt")
}

// openDatabase verbindet sich; fehlt nur die Datenbank selbst (Benutzer
// darf sich anmelden), wird sie automatisch angelegt.
func openDatabase(cfg *config.DatabaseConfig) (*database.DB, error) {
	if cfg.User == "" || cfg.Name == "" {
		return nil, fmt.Errorf("keine Datenbank-Zugangsdaten konfiguriert")
	}
	db, err := database.New(cfg)
	if err != nil && database.IsMissingDatabase(err) {
		log.Warn().Str("datenbank", cfg.Name).Msg("datenbank fehlt – versuche sie anzulegen")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cerr := database.CreateDatabaseAsOwner(ctx, cfg); cerr != nil {
			log.Warn().Err(cerr).Msg("datenbank konnte nicht automatisch angelegt werden")
			return nil, err
		}
		log.Info().Str("datenbank", cfg.Name).Msg("datenbank angelegt")
		db, err = database.New(cfg)
	}
	return db, err
}
