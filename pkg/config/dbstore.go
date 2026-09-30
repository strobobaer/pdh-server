package config

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Server-Einstellungen in der Datenbank (Tabelle server_settings).
//
// Rangfolge: echte Umgebungsvariable > Datenbank > Einstellungsdatei > Standard.
// In der Datei bleiben nur die Bootstrap-Werte, ohne die PDH die Datenbank
// gar nicht erreichen bzw. die Geheimnisse nicht entschluesseln kann.

// BootstrapKey: Werte, die zwingend in der Datei bleiben.
func BootstrapKey(key string) bool {
	return strings.HasPrefix(key, "PDH_DATABASE_") || key == "PDH_SETTINGS_KEY" || key == "PDH_ENV_FILE"
}

// SecretKey: Heuristik fuer geheime Werte (zusaetzlich zum Katalog).
func SecretKey(key string) bool {
	for _, s := range []string{"PASSWORD", "SECRET", "TOKEN", "_KEY", "ANTHROPICKEY"} {
		if strings.Contains(key, s) {
			return true
		}
	}
	return false
}

// DBExec ist das Minimum, das pgxpool.Pool und pgx.Tx bieten.
type DBExec interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
}

// EnsureSettingsKey erzeugt den Schluessel fuer verschluesselte Werte
// (einmalig, in der Einstellungsdatei).
func EnsureSettingsKey(path string) (bool, error) {
	snapshotEnv()
	if len(os.Getenv("PDH_SETTINGS_KEY")) >= 32 {
		return false, nil
	}
	if origEnv["PDH_SETTINGS_KEY"] {
		return false, errors.New("PDH_SETTINGS_KEY ist von außen gesetzt, aber kürzer als 32 Zeichen")
	}
	key := RandomSecret(32)
	if err := WriteEnvFile(path, map[string]string{"PDH_SETTINGS_KEY": key}); err != nil {
		return false, err
	}
	envMu.Lock()
	os.Setenv("PDH_SETTINGS_KEY", key)
	envMu.Unlock()
	return true, nil
}

func settingsAEAD() (cipher.AEAD, error) {
	k := os.Getenv("PDH_SETTINGS_KEY")
	if len(k) < 32 {
		return nil, errors.New("PDH_SETTINGS_KEY fehlt")
	}
	sum := sha256.Sum256([]byte("pdh-server-settings:" + k))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

const encPrefix = "enc:v1:"

func encryptSetting(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	a, err := settingsAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return encPrefix + base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func decryptSetting(v string) (string, error) {
	if !strings.HasPrefix(v, encPrefix) {
		return v, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, encPrefix))
	if err != nil {
		return "", err
	}
	a, err := settingsAEAD()
	if err != nil {
		return "", err
	}
	if len(raw) < a.NonceSize() {
		return "", errors.New("zu kurz")
	}
	out, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], nil)
	if err != nil {
		return "", errors.New("nicht entschlüsselbar (anderer PDH_SETTINGS_KEY?)")
	}
	return string(out), nil
}

// DBSetting ist ein gespeicherter Wert (entschluesselt).
type DBSetting struct {
	Key, Value, Source string
	Secret             bool
	UpdatedAt          time.Time
	DecryptErr         error
}

// LoadDBSettings liest alle Werte (Geheimnisse entschluesselt).
func LoadDBSettings(ctx context.Context, db DBExec) (map[string]DBSetting, error) {
	rows, err := db.Query(ctx, `SELECT key, value, secret, source, updated_at FROM server_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DBSetting{}
	for rows.Next() {
		var s DBSetting
		if err := rows.Scan(&s.Key, &s.Value, &s.Secret, &s.Source, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if v, err := decryptSetting(s.Value); err != nil {
			s.Value, s.DecryptErr = "", err
		} else {
			s.Value = v
		}
		out[s.Key] = s
	}
	return out, rows.Err()
}

// SaveDBSettings speichert Werte (leer = bewusst leer, ueberschreibt die Datei).
func SaveDBSettings(ctx context.Context, db DBExec, updates map[string]string, secret func(string) bool, source, userID string) error {
	for k, v := range updates {
		if BootstrapKey(k) {
			return fmt.Errorf("%s gehört in die Einstellungsdatei, nicht in die Datenbank", k)
		}
		isSecret := secret != nil && secret(k) || SecretKey(k)
		stored := v
		if isSecret {
			enc, err := encryptSetting(v)
			if err != nil {
				return err
			}
			stored = enc
		}
		var uid interface{}
		if userID != "" {
			uid = userID
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO server_settings (key, value, secret, source, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, secret = EXCLUDED.secret,
			       source = EXCLUDED.source, updated_by = EXCLUDED.updated_by, updated_at = NOW()`,
			k, stored, isSecret, source, uid); err != nil {
			return err
		}
	}
	markDBKeys(updates)
	return nil
}

// ApplyDBSettings uebernimmt die Datenbank-Werte in die Prozessumgebung
// (von aussen gesetzte Variablen bleiben unangetastet). Rueckgabe: Schluessel,
// die nicht entschluesselt werden konnten.
func ApplyDBSettings(ctx context.Context, db DBExec) ([]string, error) {
	snapshotEnv()
	vals, err := LoadDBSettings(ctx, db)
	if err != nil {
		return nil, err
	}
	var bad []string
	envMu.Lock()
	defer envMu.Unlock()
	for k, s := range vals {
		if BootstrapKey(k) {
			continue
		}
		if s.DecryptErr == nil {
			dbKeys[k] = true
		}
		if origEnv[k] {
			continue
		}
		if s.DecryptErr != nil {
			bad = append(bad, k)
			continue
		}
		if s.Value == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, s.Value)
		}
	}
	sort.Strings(bad)
	return bad, nil
}

// ImportEnvFileToDB uebertraegt Werte aus der Einstellungsdatei, die in der
// Datenbank noch fehlen (einmalig je Schluessel), und kommentiert sie in der
// Datei aus - dort wirken sie ohnehin nicht mehr. Vorher wird eine Kopie
// der Datei angelegt. Von aussen gesetzte und Bootstrap-Werte bleiben.
func ImportEnvFileToDB(ctx context.Context, db DBExec, path string, secret func(string) bool) ([]string, error) {
	snapshotEnv()
	file, err := ReadEnvFile(path)
	if err != nil {
		return nil, err
	}
	have, err := LoadDBSettings(ctx, db)
	if err != nil {
		return nil, err
	}
	updates := map[string]string{}
	for k, v := range file {
		if !strings.HasPrefix(k, "PDH_") || BootstrapKey(k) || origEnv[k] {
			continue
		}
		if _, exists := have[k]; exists {
			continue
		}
		updates[k] = v
	}
	if len(updates) == 0 {
		return nil, nil
	}
	if err := SaveDBSettings(ctx, db, updates, secret, "env_import", ""); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if err := commentOutKeys(path, keys); err != nil {
		return keys, fmt.Errorf("übernommen, aber Datei nicht bereinigt: %w", err)
	}
	return keys, nil
}

// commentOutKeys markiert uebernommene Zeilen in der Datei (mit Sicherungskopie).
func commentOutKeys(path string, keys []string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".bak-"+time.Now().Format("20060102-150405"), b, 0o600); err != nil {
		return err
	}
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if k, _, ok := parseEnvLine(line); ok && set[k] {
			// Wert nicht im Klartext stehen lassen, wenn er geheim ist.
			if SecretKey(k) {
				lines[i] = "# " + k + "=…  → in die Datenbank übernommen (Verwaltung → Server-Einstellungen)"
			} else {
				lines[i] = "# " + strings.TrimSpace(line) + "  → in die Datenbank übernommen"
			}
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// markDBKeys merkt sich gespeicherte Schluessel (Vorrang vor der Datei).
func markDBKeys(keys map[string]string) {
	envMu.Lock()
	defer envMu.Unlock()
	for k := range keys {
		dbKeys[k] = true
	}
}

// ManagedInDB: Wert wird in der Datenbank verwaltet.
func ManagedInDB(key string) bool {
	envMu.Lock()
	defer envMu.Unlock()
	return dbKeys[key]
}
