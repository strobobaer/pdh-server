// Package appsettings liest Werte aus der systemweiten app_settings-
// Tabelle (key/value, siehe migrations/045_rbac_and_system_users.up.sql)
// - schreibender Zugriff bleibt bei internal/web/core_settings.go (dort
// wird die Einstellungsseite verwaltet), dieses Paket ist der schlanke
// lesende Zugriff fuer Fachmodule (tasks/tickets/faults/maintenance), die
// internal/web nicht importieren koennen.
package appsettings

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Schluessel der konfigurierbaren Standard-Fristen (Tage nach Anlage),
// die greifen, wenn beim Anlegen kein eigener Termin angegeben wird -
// siehe internal/web/core_settings.go fuer die Verwaltungsseite.
const (
	KeyDefaultDueDaysFault       = "default_due_days_fault"
	KeyDefaultDueDaysTicket      = "default_due_days_ticket"
	KeyDefaultDueDaysTask        = "default_due_days_task"
	KeyDefaultDueDaysMaintenance = "default_due_days_maintenance"

	DefaultDueDaysFallback = 14
)

// GetInt liest den Ganzzahlwert der Einstellung key - fehlt der
// Schluessel oder ist der Wert nicht als Zahl lesbar, wird fallback
// zurueckgegeben.
func GetInt(ctx context.Context, db *pgxpool.Pool, key string, fallback int) int {
	var value string
	if err := db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&value); err != nil {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}
