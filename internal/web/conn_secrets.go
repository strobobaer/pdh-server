package web

import (
	"context"
	"errors"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// Kennwörter und Zugangsdaten der Import-/Export-Verbindungen liegen nicht im
// Klartext in der Spalte config, sondern verschlüsselt als "<feld>_enc"
// (AES-256-GCM, Format wie bei den Partner-Zugangsdaten). In den Browser
// gelangen sie nie; Treiber, MQTT und Exporte bekommen sie über openConnConfig.

// connSecretError: ein gespeichertes Kennwort lässt sich nicht entschlüsseln.
type connSecretError struct{ Field string }

func (e *connSecretError) Error() string {
	return "Kennwort der Verbindung kann nicht entschlüsselt werden (Feld „" + e.Field +
		"“) – der Schlüssel hat sich geändert. Bitte die Verbindung bearbeiten und das Kennwort neu eingeben."
}

// connKey: eigener Schlüssel über PDH_CREDENTIALS_KEY, sonst aus dem
// JWT-Secret abgeleitet. Ein Schlüsselwechsel macht gespeicherte Kennwörter
// unlesbar (sie müssen neu erfasst werden).
func (h *Handler) connKey() []byte {
	secret := h.mailCfg.CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-connections:" + secret))
	return sum[:]
}

func sortedSecretFields() []string {
	fs := make([]string, 0, len(connSecretFields))
	for f := range connSecretFields {
		fs = append(fs, f)
	}
	sort.Strings(fs)
	return fs
}

// sealConnConfig verschlüsselt alle nicht-leeren Geheimfelder: aus "password"
// wird "password_enc". Bereits vorhandene "_enc"-Werte bleiben, solange kein
// neuer Klartext angegeben ist.
func sealConnConfig(key []byte, cfg map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range sortedSecretFields() {
		plain := out[f]
		delete(out, f)
		if plain == "" {
			continue
		}
		enc, err := encryptSecret(key, plain)
		if err != nil {
			return nil, fmt.Errorf("Kennwort konnte nicht verschlüsselt werden: %w", err)
		}
		out[f+"_enc"] = enc
	}
	return out, nil
}

// openConnConfig ist die Umkehrung: "<feld>_enc" wird entschlüsselt und als
// "<feld>" geliefert. Ein altes Klartextfeld ohne "_enc" bleibt gültig.
func openConnConfig(key []byte, cfg map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range sortedSecretFields() {
		enc := out[f+"_enc"]
		delete(out, f+"_enc")
		if enc == "" {
			continue
		}
		plain, err := decryptSecret(key, enc)
		if err != nil {
			// kein Teil-Ergebnis: sonst würde mit leerem Kennwort verbunden
			return nil, &connSecretError{Field: f}
		}
		out[f] = plain
	}
	return out, nil
}

// publicConnConfig: Konfiguration ohne Geheimfelder und ohne "_enc"-Felder –
// für alles, was ins HTML oder in JSON-Antworten geht.
func publicConnConfig(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if connSecretFields[k] || (strings.HasSuffix(k, "_enc") && connSecretFields[strings.TrimSuffix(k, "_enc")]) {
			continue
		}
		out[k] = v
	}
	return out
}

// openConnJSON liest die gespeicherte Konfiguration (JSON) und entschlüsselt sie.
func (h *Handler) openConnJSON(raw []byte) (map[string]string, error) {
	cfg := map[string]string{}
	_ = json.Unmarshal(raw, &cfg)
	return openConnConfig(h.connKey(), cfg)
}

// EncryptConnectionSecrets verschlüsselt beim Start alle Verbindungen, die noch
// ein Kennwort im Klartext haben. Idempotent; liefert die Zahl der Zeilen.
func (h *Handler) EncryptConnectionSecrets(ctx context.Context) (int, error) {
	return h.encryptConnectionSecretsIn(ctx, nil)
}

// encryptConnectionSecretsIn: wie EncryptConnectionSecrets, bei ids != nil nur
// für diese Verbindungen (Tests dürfen fremde Zeilen nicht anfassen).
func (h *Handler) encryptConnectionSecretsIn(ctx context.Context, ids []string) (int, error) {
	q := `SELECT id::text, config FROM import_export_connections`
	args := []any{}
	if ids != nil {
		q += ` WHERE id::text = ANY($1)`
		args = append(args, ids)
	}
	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	type item struct {
		id  string
		cfg map[string]string
	}
	var todo []item
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		cfg := map[string]string{}
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		for f := range connSecretFields {
			if cfg[f] != "" {
				todo = append(todo, item{id, cfg})
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	key := h.connKey()
	for _, it := range todo {
		sealed, err := sealConnConfig(key, it.cfg)
		if err != nil {
			return n, err
		}
		b, err := json.Marshal(sealed)
		if err != nil {
			return n, err
		}
		if _, err := h.db.Exec(ctx, `UPDATE import_export_connections SET config=$1 WHERE id=$2`, b, it.id); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		log.Info().Int("anzahl", n).Msg("verbindungen: kennwörter verschlüsselt")
	}
	return n, nil
}

// keepStoredSecrets: Beim Bearbeiten bedeutet ein leeres Geheimfeld „unverändert
// lassen“. Der gespeicherte Wert (verschlüsselt oder noch Klartext) wird übernommen.
func keepStoredSecrets(cfg, stored map[string]string, fields []string) map[string]string {
	for _, f := range fields {
		if !connSecretFields[f] || cfg[f] != "" {
			continue
		}
		if enc := stored[f+"_enc"]; enc != "" {
			cfg[f+"_enc"] = enc
		} else if old := stored[f]; old != "" {
			cfg[f] = old
		}
	}
	return cfg
}

// isConnSecretErr: der Fehler stammt vom nicht entschlüsselbaren Kennwort.
// Aufrufer, die nur Metadaten (Typ, Status) brauchen, dürfen ihn ignorieren.
func isConnSecretErr(err error) bool {
	var se *connSecretError
	return errors.As(err, &se)
}
