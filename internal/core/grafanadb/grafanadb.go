// Package grafanadb richtet den Nur-Lese-Datenbankbenutzer für Grafana ein
// (Datenquelle „PDH“, grafana/provisioning/datasources/pdh.yaml).
//
// Läuft bei jedem Start nach den Migrationen, wenn PDH_GRAFANA_DB_PASSWORD
// gesetzt ist: Benutzer anlegen bzw. Passwort setzen, Lesezugriff auf alle
// Tabellen – außer Spalten mit Passwörtern, Schlüsseln, Tokens und Geheimnissen.
package grafanadb

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Role ist der Datenbankbenutzer, mit dem Grafana liest.
const Role = "pdh_grafana"

// sensitive: Spaltennamen, die Grafana nie lesen darf.
var sensitive = regexp.MustCompile(`(?i)(password|passwd|secret|token|hash|credential|private_key|api_key|_key_enc|encrypted|p256dh|auth_key|salt)`)

// Sensitive meldet, ob eine Spalte für Grafana gesperrt ist.
func Sensitive(column string) bool { return sensitive.MatchString(column) }

// EnsureReader legt den Benutzer an (oder setzt sein Passwort) und vergibt die Leserechte.
func EnsureReader(ctx context.Context, db *pgxpool.Pool, password string) error {
	if strings.TrimSpace(password) == "" {
		return nil
	}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)`, Role).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	var stmt string
	if err := db.QueryRow(ctx, `SELECT format('%s ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE', $1::text, $2::text, $3::text)`,
		verb, Role, password).Scan(&stmt); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, stmt); err != nil && !exists {
		// ohne CREATEROLE: Rolle als Postgres-Superuser anlegen (scripts/install-grafana.sh)
		return fmt.Errorf("grafana-datenbankbenutzer: %w", err)
	}
	var grant string
	if err := db.QueryRow(ctx, `SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), $1::text)`, Role).Scan(&grant); err != nil {
		return err
	}
	// Rollen-/Datenbankrechte sind optional (gibt ggf. der Superuser), Tabellenrechte nicht
	for _, q := range []string{grant, `GRANT USAGE ON SCHEMA public TO ` + Role, `ALTER ROLE ` + Role + ` SET default_transaction_read_only = on`} {
		_, _ = db.Exec(ctx, q)
	}
	for _, q := range []string{
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + Role,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO ` + Role,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			return fmt.Errorf("grafana-leserechte: %w", err)
		}
	}
	return restrictSensitive(ctx, db)
}

// restrictSensitive: Tabellen mit geheimen Spalten nur spaltenweise freigeben.
func restrictSensitive(ctx context.Context, db *pgxpool.Pool) error {
	rows, err := db.Query(ctx, `SELECT c.table_name, c.column_name
		FROM information_schema.columns c JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema = 'public' ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return err
	}
	cols := map[string][]string{}
	var order []string
	blocked := map[string]bool{}
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			rows.Close()
			return err
		}
		if _, ok := cols[table]; !ok {
			order = append(order, table)
		}
		if Sensitive(col) {
			blocked[table] = true
			cols[table] = append(cols[table], "")
			continue
		}
		cols[table] = append(cols[table], col)
	}
	rows.Close()
	for _, table := range order {
		if !blocked[table] {
			continue
		}
		var allowed []string
		for _, c := range cols[table] {
			if c != "" {
				allowed = append(allowed, c)
			}
		}
		var revoke string
		if err := db.QueryRow(ctx, `SELECT format('REVOKE SELECT ON %I FROM %I', $1::text, $2::text)`, table, Role).Scan(&revoke); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, revoke); err != nil {
			return err
		}
		if len(allowed) == 0 {
			continue
		}
		var grant string
		if err := db.QueryRow(ctx, `SELECT format('GRANT SELECT (%s) ON %I TO %I',
				(SELECT string_agg(format('%I', c), ', ') FROM unnest($1::text[]) c), $2::text, $3::text)`,
			allowed, table, Role).Scan(&grant); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, grant); err != nil {
			return err
		}
	}
	return nil
}
