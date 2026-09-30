package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Die Server-Einstellungen stehen in einer .env-Datei (Standard ./.env,
// in Docker PDH_ENV_FILE=/app/config/pdh.env auf einem Volume). Sie wird
// beim Start in die Prozessumgebung uebernommen - echte Umgebungsvariablen
// (Docker compose, systemd EnvironmentFile) haben aber immer Vorrang und
// koennen in der Oberflaeche nicht ueberschrieben werden.

var (
	origOnce sync.Once
	origEnv  map[string]bool // Variablen, die schon vor dem Laden der Datei gesetzt waren
	envMu    sync.Mutex
)

// snapshotEnv merkt sich einmalig, welche Variablen "von aussen" kommen.
// Hat systemd (EnvironmentFile=) dieselbe Datei bereits geladen, stimmen
// Prozess- und Dateiwert ueberein - solche Werte gelten als aus der Datei
// stammend und bleiben in der Oberflaeche bearbeitbar.
func snapshotEnv() {
	origOnce.Do(func() {
		file, _ := ReadEnvFile(EnvFilePath())
		origEnv = map[string]bool{}
		for _, kv := range os.Environ() {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "PDH_ENV_FILE" {
				continue
			}
			if fv, inFile := file[k]; inFile && fv == v {
				continue
			}
			origEnv[k] = true
		}
	})
}

// EnvFilePath liefert den Pfad der Einstellungsdatei.
func EnvFilePath() string {
	if p := strings.TrimSpace(os.Getenv("PDH_ENV_FILE")); p != "" {
		return p
	}
	return ".env"
}

// FromProcessEnv: true, wenn die Variable von aussen (Docker/systemd/Shell)
// gesetzt wurde und daher Vorrang vor der Datei hat.
func FromProcessEnv(key string) bool {
	snapshotEnv()
	return origEnv[key]
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ReadEnvFile liest KEY=VALUE-Zeilen (Kommentare, "export", Anfuehrungszeichen).
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if k, v, ok := parseEnvLine(sc.Text()); ok {
			out[k] = v
		}
	}
	return out, sc.Err()
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), string(rune(0xFEFF))))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	k, v, ok := strings.Cut(line, "=")
	k = strings.TrimSpace(k)
	if !ok || !envKeyRe.MatchString(k) {
		return "", "", false
	}
	return k, unquoteEnvValue(strings.TrimSpace(v)), true
}

func unquoteEnvValue(v string) string {
	if len(v) >= 2 && v[0] == '"' {
		if end := strings.LastIndex(v, `"`); end > 0 {
			inner := v[1:end]
			var b strings.Builder
			for i := 0; i < len(inner); i++ {
				if inner[i] == '\\' && i+1 < len(inner) {
					i++
					switch inner[i] {
					case 'n':
						b.WriteByte('\n')
					default:
						b.WriteByte(inner[i])
					}
					continue
				}
				b.WriteByte(inner[i])
			}
			return b.String()
		}
	}
	if len(v) >= 2 && v[0] == '\'' {
		if end := strings.LastIndex(v, `'`); end > 0 {
			return v[1:end]
		}
	}
	// unquotiert: Kommentar am Zeilenende abschneiden
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// quoteEnvValue schreibt Werte so, dass Shell, systemd und docker compose sie lesen.
func quoteEnvValue(v string) string {
	if v == "" {
		return ""
	}
	if regexp.MustCompile(`^[A-Za-z0-9_./:@+,-]+$`).MatchString(v) {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

// ApplyEnvFile uebernimmt die Datei in die Prozessumgebung. Von aussen
// gesetzte Variablen bleiben unangetastet. Mehrfach aufrufbar (nach dem
// Speichern in der Oberflaeche).
func ApplyEnvFile(path string) error {
	snapshotEnv()
	vals, err := ReadEnvFile(path)
	if err != nil {
		return err
	}
	envMu.Lock()
	defer envMu.Unlock()
	for k, v := range vals {
		if origEnv[k] {
			continue
		}
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
	return nil
}

// WriteEnvFile setzt/aendert Werte in der Datei. Kommentare, Reihenfolge und
// unbekannte Zeilen bleiben erhalten; neue Schluessel werden angehaengt.
// Die Datei wird atomar ersetzt und ist nur fuer den Eigentuemer lesbar.
func WriteEnvFile(path string, updates map[string]string) error {
	for k := range updates {
		if !envKeyRe.MatchString(k) {
			return fmt.Errorf("ungültiger Name %q", k)
		}
	}
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	done := map[string]bool{}
	for i, line := range lines {
		k, _, ok := parseEnvLine(line)
		if !ok {
			continue
		}
		if v, upd := updates[k]; upd {
			if done[k] {
				lines[i] = "# " + line + "  (doppelt, ersetzt)"
				continue
			}
			lines[i] = k + "=" + quoteEnvValue(v)
			done[k] = true
		}
	}
	var missing []string
	for k := range updates {
		if !done[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		lines = append(lines, "", "# ── in der PDH-Oberfläche gesetzt ──")
		for _, k := range missing {
			lines = append(lines, k+"="+quoteEnvValue(updates[k]))
		}
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RandomSecret liefert einen zufaelligen Hex-String (2*n Zeichen).
func RandomSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// EnsureJWTSecret erzeugt bei der Erstinstallation automatisch ein sicheres
// JWT-Secret und speichert es in der Einstellungsdatei.
func EnsureJWTSecret(path string) (bool, error) {
	snapshotEnv()
	if s := os.Getenv("PDH_AUTH_JWTSECRET"); len(s) >= 32 && !strings.Contains(s, "AENDERN") {
		return false, nil
	}
	if origEnv["PDH_AUTH_JWTSECRET"] {
		return false, fmt.Errorf("PDH_AUTH_JWTSECRET ist von außen gesetzt, aber kürzer als 32 Zeichen")
	}
	secret := RandomSecret(32)
	if err := WriteEnvFile(path, map[string]string{"PDH_AUTH_JWTSECRET": secret}); err != nil {
		return false, err
	}
	envMu.Lock()
	os.Setenv("PDH_AUTH_JWTSECRET", secret)
	envMu.Unlock()
	return true, nil
}

// ExternalEnviron liefert nur die von aussen gesetzten Variablen - fuer
// einen Neustart des Prozesses, damit Werte aus der Datei danach wieder als
// "aus der Datei" gelten (und bearbeitbar bleiben).
func ExternalEnviron() []string {
	snapshotEnv()
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if origEnv[k] || k == "PDH_ENV_FILE" {
			out = append(out, kv)
		}
	}
	return out
}
