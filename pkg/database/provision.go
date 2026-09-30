package database

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pdh/pkg/config"
)

// Erstinstallation: fehlende Datenbank automatisch anlegen.

// dsn baut den Verbindungs-String (Werte werden korrekt maskiert).
func dsn(cfg *config.DatabaseConfig, dbName string) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
		quoteDSN(cfg.Host), cfg.Port, quoteDSN(cfg.User), quoteDSN(cfg.Password), quoteDSN(dbName), quoteDSN(cfg.SSLMode))
}

func quoteDSN(v string) string {
	if v == "" {
		return "''"
	}
	out := []byte{'\''}
	for i := 0; i < len(v); i++ {
		if v[i] == '\'' || v[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, v[i])
	}
	return string(append(out, '\''))
}

// IsMissingDatabase: Server erreichbar, Anmeldung ok, aber Datenbank fehlt.
func IsMissingDatabase(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "3D000"
}

// IsAuthError: Benutzer/Passwort falsch.
func IsAuthError(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "28P01" || pe.Code == "28000")
}

// TestConnection prueft die Zugangsdaten (ohne Pool).
func TestConnection(ctx context.Context, cfg *config.DatabaseConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn(cfg, cfg.Name))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.Ping(ctx)
}

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidIdent: erlaubte Namen fuer automatisch angelegte Rollen/Datenbanken.
func ValidIdent(s string) bool { return identRe.MatchString(s) }

// CreateDatabaseAsOwner legt die konfigurierte Datenbank mit den eigenen
// Zugangsdaten an (Benutzer braucht das Recht CREATEDB).
func CreateDatabaseAsOwner(ctx context.Context, cfg *config.DatabaseConfig) error {
	if !ValidIdent(cfg.Name) {
		return fmt.Errorf("Datenbankname %q: nur Kleinbuchstaben, Ziffern und _ erlaubt", cfg.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn(cfg, "postgres"))
	if err != nil {
		return fmt.Errorf("Verbindung zur Verwaltungsdatenbank 'postgres': %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{cfg.Name}.Sanitize()); err != nil {
		return fmt.Errorf("Datenbank %s anlegen: %w", cfg.Name, err)
	}
	return nil
}

// Provision legt mit Administrator-Zugang (z. B. "postgres") eine eigene
// Rolle und Datenbank fuer PDH an. Existiert die Rolle schon, wird ihr
// Passwort gesetzt; existiert die Datenbank schon, wird sie weiterverwendet.
// Rueckgabe: Zugangsdaten, mit denen PDH kuenftig arbeitet.
func Provision(ctx context.Context, admin *config.DatabaseConfig, role, password, dbName string) (*config.DatabaseConfig, []string, error) {
	if !ValidIdent(role) || !ValidIdent(dbName) {
		return nil, nil, errors.New("Benutzer- und Datenbankname: nur Kleinbuchstaben, Ziffern und _ (max. 63 Zeichen)")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn(admin, "postgres"))
	if err != nil {
		return nil, nil, fmt.Errorf("Anmeldung als %s fehlgeschlagen: %w", admin.User, err)
	}
	defer conn.Close(ctx)
	var notes []string
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return nil, nil, err
	}
	// Passwort als Literal (DDL kennt keine Parameter) - sicher maskiert.
	pw := "'" + regexp.MustCompile(`'`).ReplaceAllString(password, "''") + "'"
	if exists {
		if _, err := conn.Exec(ctx, `ALTER ROLE `+pgx.Identifier{role}.Sanitize()+` WITH LOGIN PASSWORD `+pw); err != nil {
			return nil, nil, fmt.Errorf("Rolle %s aktualisieren: %w", role, err)
		}
		notes = append(notes, "Datenbank-Benutzer "+role+" war vorhanden – Passwort neu gesetzt")
	} else {
		if _, err := conn.Exec(ctx, `CREATE ROLE `+pgx.Identifier{role}.Sanitize()+` WITH LOGIN PASSWORD `+pw); err != nil {
			return nil, nil, fmt.Errorf("Rolle %s anlegen: %w", role, err)
		}
		notes = append(notes, "Datenbank-Benutzer "+role+" angelegt")
	}
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists); err != nil {
		return nil, nil, err
	}
	if exists {
		if _, err := conn.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{dbName}.Sanitize()+` OWNER TO `+pgx.Identifier{role}.Sanitize()); err != nil {
			return nil, nil, fmt.Errorf("Datenbank %s übernehmen: %w", dbName, err)
		}
		notes = append(notes, "Datenbank "+dbName+" war vorhanden – wird weiterverwendet")
	} else {
		if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{dbName}.Sanitize()+` OWNER `+pgx.Identifier{role}.Sanitize()+` ENCODING 'UTF8' TEMPLATE template0`); err != nil {
			return nil, nil, fmt.Errorf("Datenbank %s anlegen: %w", dbName, err)
		}
		notes = append(notes, "Datenbank "+dbName+" angelegt")
	}
	// Erweiterung pgcrypto braucht ggf. Administratorrechte - gleich hier anlegen.
	target := *admin
	target.Name = dbName
	if c2, err := pgx.Connect(ctx, dsn(&target, dbName)); err == nil {
		_, _ = c2.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`)
		_, _ = c2.Exec(ctx, `GRANT ALL ON SCHEMA public TO `+pgx.Identifier{role}.Sanitize())
		_, _ = c2.Exec(ctx, `ALTER SCHEMA public OWNER TO `+pgx.Identifier{role}.Sanitize())
		c2.Close(ctx)
	}
	out := &config.DatabaseConfig{Host: admin.Host, Port: admin.Port, User: role, Password: password, Name: dbName, SSLMode: admin.SSLMode}
	if err := TestConnection(ctx, out); err != nil {
		return nil, notes, fmt.Errorf("Test mit neuem Benutzer fehlgeschlagen: %w", err)
	}
	return out, notes, nil
}
