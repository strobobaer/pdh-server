package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"pdh"
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
	// Erstinstallation: Einstellungsdatei laden. Sie enthaelt mindestens den
	// Datenbank-Zugang und den Schluessel fuer verschluesselte Einstellungen
	// (wird automatisch erzeugt); alles andere liegt in der Datenbank.
	envPath := config.EnvFilePath()
	if err := config.ApplyEnvFile(envPath); err != nil {
		fmt.Fprintf(os.Stderr, "einstellungsdatei %s: %v\n", envPath, err)
		os.Exit(1)
	}
	keyCreated, err := config.EnsureSettingsKey(envPath)
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
	log.Info().Str("version", pdh.Version()).Str("commit", buildCommit).Str("env", cfg.Server.Env).Str("einstellungen", envPath).Msg("PDH startet")
	if keyCreated {
		log.Info().Str("datei", envPath).Msg("schlüssel für verschlüsselte server-einstellungen erzeugt (PDH_SETTINGS_KEY) – datei mitsichern!")
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

	// Neue Programmversion bzw. ausstehende Datenbank-Aenderungen: vorher
	// automatisch eine Vollsicherung des bisherigen Stands anlegen.
	backupCtx, backupCancel := context.WithTimeout(context.Background(), 2*time.Hour)
	if err := web.BackupBeforeUpdate(backupCtx, db.Pool, buildCommit, "migrations"); err != nil {
		if os.Getenv("PDH_SKIP_UPDATE_BACKUP") == "1" {
			log.Error().Err(err).Msg("vollsicherung vor dem update fehlgeschlagen – PDH_SKIP_UPDATE_BACKUP=1, update wird trotzdem angewendet")
		} else {
			backupCancel()
			log.Fatal().Err(err).Msg("vollsicherung vor dem update fehlgeschlagen – update wird NICHT angewendet (Speicherplatz/Sicherungsordner prüfen; im Notfall PDH_SKIP_UPDATE_BACKUP=1 setzen)")
		}
	}
	backupCancel()

	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer migrationCancel()
	if err := database.RunMigrations(migrationCtx, db.Pool, "migrations"); err != nil {
		log.Fatal().Err(err).Msg("migrationen")
	}
	log.Info().Msg("migrationen geprüft")
	cfg = loadServerSettings(db, envPath)

	// Noch kein Benutzer (frische Datenbank, z. B. Docker): ersten
	// Administrator im Browser anlegen lassen.
	var humans int
	if err := db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users WHERE NOT is_bot`).Scan(&humans); err == nil && humans == 0 {
		log.Warn().Msg("noch kein benutzer vorhanden – starte Einrichtungsassistent")
		if err := setup.Run(context.Background(), setup.Options{Addr: addr, Pool: db.Pool}); err != nil {
			log.Fatal().Err(err).Msg("einrichtung")
		}
		cfg = loadServerSettings(db, envPath) // z. B. neue oeffentliche Adresse
	}
	addr = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port) // Port/Host koennen aus der Datenbank kommen

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
	copilot.SetAnthropicWorkspace(cfg.Copilot.AnthropicWorkspace)
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
	webHandler.StartTrainingScheduler(context.Background())
	webHandler.StartEnabledImportPolls(context.Background())
	webHandler.StartEnabledQueryPolls(context.Background())

	log.Info().Str("backend", cfg.Copilot.Backend).Str("model", cfg.Copilot.Model).Msg("copilot bereit")

	r := chi.NewRouter()
	// RequestID vor dem Protokoll, damit jede Zeile ihre Request-ID traegt
	r.Use(chimw.RequestID, webHandler.RequestLogger, chimw.Recoverer, middleware.CORS)
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
			"version": pdh.Version(),
		})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(webHandler.APIDepartmentScope)
		r.Mount("/users", userHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/storage", storageHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/shifts", shiftHandler.Routes(cfg.Auth.JWTSecret))
		r.Mount("/infrastructure", infraHandler.Routes(cfg.Auth.JWTSecret, rbacSvc.RequirePermission("infrastructure.edit")))
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

	// App-Symbole (Favicon, Apple-Touch-Icon, Manifest) - oeffentlich
	icons := web.AppIconHandler()
	for _, name := range web.AppIconFiles {
		r.Handle("/"+name, icons)
	}

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

// loadServerSettings uebertraegt noch nicht uebernommene Werte aus der
// Einstellungsdatei in die Datenbank, laedt die Datenbank-Einstellungen,
// erzeugt ein fehlendes JWT-Secret und liefert die fertige Konfiguration.
func loadServerSettings(db *database.DB, envPath string) *config.Config {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	imported, err := config.ImportEnvFileToDB(ctx, db.Pool, envPath, web.IsSecretSetting)
	if err != nil {
		log.Error().Err(err).Msg("server-einstellungen: übernahme aus der einstellungsdatei")
	}
	if len(imported) > 0 {
		log.Info().Strs("werte", imported).Str("datei", envPath).Msg("server-einstellungen aus der einstellungsdatei in die datenbank übernommen")
	}
	bad, err := config.ApplyDBSettings(ctx, db.Pool)
	if err != nil {
		log.Fatal().Err(err).Msg("server-einstellungen laden")
	}
	if len(bad) > 0 {
		log.Warn().Strs("werte", bad).Msg("server-einstellungen nicht entschlüsselbar (anderer PDH_SETTINGS_KEY?) – bitte unter Server-Einstellungen neu eingeben")
	}
	if s := os.Getenv("PDH_AUTH_JWTSECRET"); len(s) < 32 || strings.Contains(s, "AENDERN") {
		if config.FromProcessEnv("PDH_AUTH_JWTSECRET") {
			log.Fatal().Msg("PDH_AUTH_JWTSECRET ist von außen gesetzt, aber ungültig (mind. 32 Zeichen)")
		}
		secret := config.RandomSecret(32)
		if err := config.SaveDBSettings(ctx, db.Pool, map[string]string{"PDH_AUTH_JWTSECRET": secret}, web.IsSecretSetting, "generated", ""); err != nil {
			log.Fatal().Err(err).Msg("jwt-secret speichern")
		}
		os.Setenv("PDH_AUTH_JWTSECRET", secret)
		log.Info().Msg("neues JWT-Secret erzeugt und in der datenbank gespeichert")
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config")
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal().Err(err).Msg("config")
	}
	logger.Init(cfg.Server.Env)
	return cfg
}
