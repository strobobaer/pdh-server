package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/internal/mirror"
	"pdh/pkg/config"
	"pdh/pkg/database"
	"pdh/pkg/logger"
)

// haStat prüft Dateien für haConfigured (in Tests ersetzbar).
var haStat = os.Stat

// haConfigured: Dieser Rechner gehört zu einem Ausfallschutz-Verbund – pdh-ha
// ist eingerichtet (node.json unter PDH_HA_ETC, sonst /etc/pdh/ha, wie pdh-ha
// selbst). Der Ordner ist 0700 root; der Dienstbenutzer pdh bekommt dann
// „keine Berechtigung“ – das zählt als eingerichtet (pdh-ha ist installiert,
// und das geschieht erst nach der Einrichtung des PDH).
func haConfigured() bool {
	dir := "/etc/pdh/ha"
	if v := os.Getenv("PDH_HA_ETC"); v != "" {
		dir = v
	}
	_, err := haStat(filepath.Join(dir, "node.json"))
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// detectWithRetry: Modus ermitteln, bei Fehler bis zu tries Versuche im Abstand wait.
func detectWithRetry(tries int, wait time.Duration, detect func() (mirror.Mode, error)) (mirror.Mode, error) {
	var (
		m   mirror.Mode
		err error
	)
	for i := 0; i < tries; i++ {
		if i > 0 {
			time.Sleep(wait)
		}
		if m, err = detect(); err == nil {
			return m, nil
		}
	}
	return m, err
}

// watchSchemaCatchUp: Im eingeschränkten Spiegel alle every prüfen, ob die
// Datenbank den Programmstand erreicht hat (keine offenen Migrationen mehr,
// d. h. der aktive Server ist aktualisiert und das Schema repliziert). Dann
// genau einmal done rufen und zurückkehren. Fehler: weiter warten. Jede Prüfung
// ist auf every begrenzt; every <= 0 wird auf 1 s gesetzt.
func watchSchemaCatchUp(ctx context.Context, every time.Duration, pending func(context.Context) (int, error), done func()) {
	if every <= 0 {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, every)
		n, err := pending(pctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil && n == 0 {
			done()
			return
		}
	}
}

// schemaBehind: Die Datenbank ist eingerichtet, aber der Spiegel hinkt dem
// Programmstand bei den Migrationen hinterher (Anmeldung dann eingeschränkt).
func schemaBehind(pending []string, initialized bool) bool {
	return initialized && len(pending) > 0
}

// schemaAhead: Die Datenbank kennt Migrationen, die dieses Programm nicht
// kennt – das Programm ist zu alt für die Datenbank (Spiegel sperrt sich).
func schemaAhead(applied, known []int) bool {
	k := make(map[int]bool, len(known))
	for _, v := range known {
		k[v] = true
	}
	for _, v := range applied {
		if !k[v] {
			return true
		}
	}
	return false
}

// skipUpdateBackup: Der Update-Agent hat für genau diesen Programmstand
// bereits eine Rückfallkopie angelegt (app_settings update.backup_skip_commit).
// Ein leerer oder unbekannter Stand lässt die Sicherung nie entfallen.
func skipUpdateBackup(get func(string) string, commit string) bool {
	if commit == "" || commit == "unknown" {
		return false
	}
	return get("update.backup_skip_commit") == commit
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
