package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/pkg/config"
	"pdh/pkg/database"
	"pdh/pkg/logger"
)

// schemaBehind: Die Datenbank ist eingerichtet, aber der Spiegel hinkt dem
// Programmstand bei den Migrationen hinterher (Anmeldung dann eingeschränkt).
func schemaBehind(pending []string, initialized bool) bool {
	return initialized && len(pending) > 0
}

// loadServerSettingsReadOnly lädt die Konfiguration im Spiegelmodus: ohne
// Übernahme aus der Einstellungsdatei und ohne Erzeugen eines JWT-Secrets
// (beides würde in die Datenbank schreiben).
func loadServerSettingsReadOnly(db *database.DB) *config.Config {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bad, err := config.ApplyDBSettings(ctx, db.Pool)
	if err != nil {
		log.Fatal().Err(err).Msg("server-einstellungen laden")
	}
	if len(bad) > 0 {
		log.Warn().Strs("werte", bad).Msg("server-einstellungen nicht entschlüsselbar (anderer PDH_SETTINGS_KEY?)")
	}
	if s := os.Getenv("PDH_AUTH_JWTSECRET"); len(s) < 32 || strings.Contains(s, "AENDERN") {
		log.Fatal().Msg("PDH_AUTH_JWTSECRET fehlt oder ist ungültig – im Spiegel nicht erzeugbar (Wert muss vom Hauptsystem repliziert sein)")
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
