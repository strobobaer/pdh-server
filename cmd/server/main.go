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
	"pdh/internal/core/grafanadb"
	"pdh/internal/core/infrastructure"
	"pdh/internal/core/rbac"
	"pdh/internal/core/shifts"
	"pdh/internal/core/storage"
	"pdh/internal/core/synclink"
	"pdh/internal/core/users"
	"pdh/internal/mirror"
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
		// Im Ausfallschutz-Verbund nie den Assistenten öffnen (z. B. auf dem
		// passiven Server, solange PostgreSQL noch startet): beenden, systemd
		// bzw. pdh-ha starten neu.
		if haConfigured() {
			log.Fatal().Err(err).Msg("datenbank nicht verfügbar – Ausfallschutz eingerichtet, kein Einrichtungsassistent; neuer Versuch durch systemd/pdh-ha")
		}
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

	// Spiegelmodus: PostgreSQL-Standby bzw. schreibgeschützte Datenbank. Dann
	// startet das PDH nur lesend, ohne Sicherung, Migration oder Hintergrunddienste.
	mode, merr := detectWithRetry(3, time.Second, func() (mirror.Mode, error) {
		detectCtx, detectCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer detectCancel()
		return mirror.Detect(detectCtx, db.Pool)
	})
	if merr != nil {
		if haConfigured() { // im Verbund nicht raten: keine Vollsicherung/Migration auf einem Spiegel
			log.Fatal().Err(merr).Msg("spiegelmodus nicht ermittelbar – Ausfallschutz eingerichtet, neuer Versuch durch systemd/pdh-ha")
		}
		log.Warn().Err(merr).Msg("spiegelmodus nicht ermittelbar – starte wie gewohnt")
	}
	mirrorOn := mode == mirror.ReadOnly
	restricted := false
	mirrorReason := ""
	if mirrorOn {
		pctx, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
		pending, initialized, perr := database.PendingMigrations(pctx, db.Pool, "migrations")
		pcancel()
		if perr != nil {
			log.Warn().Err(perr).Msg("spiegel: migrationsstand nicht ermittelbar – anmeldung eingeschränkt")
			restricted = true // im Zweifel eingeschränkt
		} else {
			restricted = schemaBehind(pending, initialized)
		}
		// Die Datenbank ist neuer als das Programm (Update lief auf dem aktiven
		// Server, dieser Spiegel hat noch das alte Programm): ebenfalls eingeschränkt.
		vctx, vcancel := context.WithTimeout(context.Background(), 30*time.Second)
		applied, known, verr := database.MigrationVersions(vctx, db.Pool, "migrations")
		vcancel()
		if verr != nil {
			log.Warn().Err(verr).Msg("spiegel: migrationsnummern nicht lesbar – prüfung „programm zu alt“ übersprungen")
		} else if schemaAhead(applied, known) {
			restricted = true
			mirrorReason = "Programm zu alt für diese Datenbank"
		}
		log.Warn().Bool("schema_veraltet", restricted).Msg("spiegelmodus: PDH startet nur lesend")
	}

	if mirrorOn {
		cfg = loadServerSettingsReadOnly(db)
		var humans int
		if err := db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users WHERE NOT is_bot`).Scan(&humans); err != nil {
			log.Fatal().Err(err).Msg("spiegel: benutzer konnten nicht gezählt werden")
		} else if humans == 0 {
			log.Fatal().Msg("spiegel: keine benutzer in der datenbank – einrichtung im Spiegel nicht möglich")
		}
	} else {
		// Neue Programmversion bzw. ausstehende Datenbank-Aenderungen: vorher
		// automatisch eine Vollsicherung des bisherigen Stands anlegen.
		backupCtx, backupCancel := context.WithTimeout(context.Background(), 2*time.Hour)
		if skipUpdateBackup(func(key string) string { return startupAppSetting(db, key) }, buildCommit) {
			// Der Update-Agent hat vor dem Einspielen schon eine Rückfallkopie angelegt.
			log.Info().Str("commit", buildCommit).Msg("Sicherung vor Update ausgelassen – Rückfallkopie vorhanden")
		} else if err := web.BackupBeforeUpdate(backupCtx, db.Pool, buildCommit, "migrations"); err != nil {
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
		// Nur-Lese-Benutzer fuer Grafana (Statistik); Fehler bremsen den Start nicht
		if pw := os.Getenv("PDH_GRAFANA_DB_PASSWORD"); pw != "" {
			if err := grafanadb.EnsureReader(migrationCtx, db.Pool, pw); err != nil {
				log.Warn().Err(err).Msg("grafana: nur-lese-benutzer nicht eingerichtet")
			} else {
				log.Info().Str("benutzer", grafanadb.Role).Msg("grafana: nur-lese-benutzer eingerichtet")
			}
		}

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
	shiftHandler.SetPermChecker(func(r *http.Request, perm string) bool {
		uid, _ := r.Context().Value(middleware.UserIDKey).(string)
		role, _ := r.Context().Value(middleware.RoleKey).(string)
		return rbacSvc.HasPermissionForUser(uid, role, perm)
	})

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
	timeHandler.SetPermChecker(func(r *http.Request, perm string) bool {
		uid, _ := r.Context().Value(middleware.UserIDKey).(string)
		role, _ := r.Context().Value(middleware.RoleKey).(string)
		return rbacSvc.HasPermissionForUser(uid, role, perm)
	})

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
	webHandler.SetMirror(web.MirrorInfo{On: mirrorOn, Restricted: restricted, Reason: mirrorReason}) // vor Routes(), danach unveränderlich
	webHandler.RegisterMaintenanceProtocol() // Wartungsprotokoll (PDF) an der Anlage
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
	webHandler.ConfigureMail(cfg.Mail)
	webHandler.LoadIconOverrides(context.Background())
	if !mirrorOn { // Spiegel: keine Hintergrunddienste, die schreiben oder nach außen wirken
		webHandler.StartUpdateChecker(context.Background())
		webHandler.StartEnabledMqttBrokers(context.Background())
		webHandler.StartEnabledMqttConsumers(context.Background())
		webHandler.StartEnabledExportSchedules(context.Background())
		webHandler.StartPurchaseReportSchedule(context.Background())
		webHandler.StartBackupSystem(context.Background())
		webHandler.StartChangeNotifier(context.Background())
		maintSvc.StartScheduler(context.Background()) // je Wartungsplan genau ein offener Auftrag (stuendlich)
		webHandler.StartTrainingScheduler(context.Background())
		webHandler.StartObligationReminders(context.Background())
		webHandler.StartNextcloudAdminSync(context.Background()) // Dienstkonto pdhNCadmin: PDH-Passwort hat Vorrang
		webHandler.StartPushNotifier(context.Background()) // Push aufs Handy bei neuen Störungen/Tickets
		faultSvc.StartPendingTicketWatcher(context.Background()) // Ticket aus Störung erst nach Zuweisung
		webHandler.StartKVPScheduler(context.Background())
		webHandler.StartDriveMonitor(context.Background())
		webHandler.StartEnabledImportPolls(context.Background())
		webHandler.StartEnabledQueryPolls(context.Background())
		webHandler.VPNStartup(context.Background()) // WireGuard: Konfiguration für den Dienst „wireguard“ schreiben
	}
	// Ausfallschutz: Tunnel-Token an den lokalen pdh-ha weiterreichen – auf beiden
	// Servern (aktiv und Spiegel); liest nur aus der Datenbank.
	if haConfigured() {
		go webHandler.RunTunnelSync(context.Background())
	}

	// Modus überwachen (auch im Normalmodus: fängt eine spätere Abschottung ab).
	// Im Spiegel zusätzlich: Datenbank 60 s weg -> Neustart; im Normalmodus
	// beenden nur Moduswechsel den Prozess (Einzelserver bleiben wie bisher).
	watchEvery, watchLost := 5*time.Second, time.Duration(0)
	if mirrorOn {
		watchLost = 60 * time.Second
	}
	go mirror.Watch(context.Background(), db.Pool, mode, watchEvery, watchLost, func(reason string) {
		log.Error().Str("grund", reason).Msg("spiegelmodus gewechselt – neustart")
		os.Exit(3)
	})
	// Eingeschränkter Spiegel: sobald das Schema nachgezogen ist (aktiver
	// Server aktualisiert, Migrationen repliziert), neu starten – dann ohne Einschränkung.
	if restricted {
		go watchSchemaCatchUp(context.Background(), 30*time.Second, func(ctx context.Context) (int, error) {
			pending, initialized, err := database.PendingMigrations(ctx, db.Pool, "migrations")
			if err != nil || !schemaBehind(pending, initialized) {
				return 0, err
			}
			return len(pending), nil
		}, func() {
			log.Warn().Msg("spiegel: datenbank auf programmstand – neustart ohne einschränkung")
			os.Exit(3)
		})
	}

	copilotModel := cfg.Copilot.Model // Ollama
	if cfg.Copilot.Backend == "anthropic" {
		copilotModel = cfg.Copilot.AnthropicModel
		if copilotModel == "" {
			copilotModel = faults.DefaultAnthropicModel
		}
	}
	log.Info().Str("backend", cfg.Copilot.Backend).Str("model", copilotModel).Msg("copilot bereit")

	// Wartungssperre: Sitzungs-Epoche laden und Update-Auftrag abfragen, bevor
	// Anfragen angenommen werden – läuft eine Sperre, startet das PDH gesperrt.
	webHandler.LoadMaintLock(context.Background())

	r := chi.NewRouter()
	// RequestID vor dem Protokoll, damit jede Zeile ihre Request-ID traegt
	r.Use(chimw.RequestID, webHandler.RequestLogger, chimw.Recoverer, middleware.CORS)
	// Wartungssperre während eines Updates: vor allen Routen und Schnittstellen (maint_lock.go)
	r.Use(webHandler.MaintLockMiddleware)
	// Dauerverbindungen (EventSource) enden beim Beenden sofort (streams.go)
	r.Use(webHandler.StreamsMiddleware)
	// Spiegelmodus: Schreibsperre fuer Web und API (vor allem, was schreiben koennte)
	r.Use(webHandler.MirrorMiddleware)
	// Aenderungshinweise (Bearbeiter merken), Stammdaten-Sperre, DELETE -> Loeschvormerkung
	// Löschen nur mit Recht (Bereichsadministrator), vor der Löschvormerkung
	r.Use(webHandler.PermissionGateMiddleware)
	r.Use(webHandler.ChangeTrackingMiddleware)

	// API
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			http.Error(w, "db error", 503)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "pdh",
			"version": pdh.Version(),
			"mode":    mode.String(),
			"commit":  buildCommit,
			// false: Spiegel mit offenen Migrationen bzw. Programm zu alt für die Datenbank
			"migrations_ok": !restricted,
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
	// Symbol-Bibliothek (Tabler Icons) lokal statt aus dem Internet
	r.Handle("/vendor/tabler-icons/*", icons)

	// Static uploads
	r.Handle("/uploads/*", http.StripPrefix("/uploads/", http.FileServer(http.Dir("uploads"))))

	// Zertifizierungsstelle für Nextcloud: CA-Zertifikat und Sperrliste öffentlich
	// (Geräte installieren die CA, nginx holt die Sperrliste; pki_web.go)
	r.Get("/pki/ca.crt", webHandler.PKICACertWeb)
	r.Get("/pki/crl.pem", webHandler.PKICRLWeb)

	// n8n und pgAdmin unter /n8n/ bzw. /pgadmin/ (admin_tools_proxy.go) – vor der
	// PDH-Anmeldung, weil n8n-Webhooks ohne Sitzung ankommen müssen; der Proxy
	// prüft alles andere selbst (nur Administratoren).
	for _, tool := range []string{"n8n", "pgadmin"} {
		r.Handle("/"+tool, webHandler.AdminToolProxy(tool))
		r.Handle("/"+tool+"/*", webHandler.AdminToolProxy(tool))
	}

	// SSO routes must be mounted before the protected web UI.
	r.Get("/sso/nextcloud", web.NextcloudSSOHandler(db.Pool, cfg.Auth.JWTSecret))

	// Public shop-floor dashboard; its write actions authenticate each operator by RFID.
	r.Mount("/global", webHandler.GlobalDashboardRoutes())

	// Web UI
	r.Mount("/", webHandler.Routes())

	srv := &http.Server{Addr: addr, Handler: r,
		ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second}
	// Shutdown wartet nicht auf offene Browser-Tabs (Chat-Stream), sonst bis 30 s
	srv.RegisterOnShutdown(webHandler.CloseStreams)

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

// startupAppSetting liest einen Wert aus app_settings, bevor der Handler existiert ("" = fehlt).
func startupAppSetting(db *database.DB, key string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var v string
	if err := db.Pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}
