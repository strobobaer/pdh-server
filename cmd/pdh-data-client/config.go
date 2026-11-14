package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"pdh/internal/datanet"
)

// Config ist die Konfiguration des Daten-Clients. Sie enthält Geheimnisse
// (Einmal-Schlüssel, Client-Token) und darf nur der Dienst lesen.
type Config struct {
	ClientID string           `json:"client_id"`
	Token    string           `json:"token"`
	AuthKey  string           `json:"auth_key,omitempty"`
	Hostname string           `json:"hostname"`
	PDH      []string         `json:"pdh"`
	Targets  []datanet.Target `json:"targets"`
	StateDir string           `json:"state_dir"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalizePDH ergänzt bei Adressen ohne Port den Meldeport.
func normalizePDH(list []string) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(a); err != nil {
			a = net.JoinHostPort(strings.Trim(a, "[]"), strconv.Itoa(datanet.ReportPort))
		}
		out = append(out, a)
	}
	return out
}

// validPDHAddr prüft eine PDH-Adresse der Form Host:Port.
func validPDHAddr(a string) bool {
	host, port, err := net.SplitHostPort(a)
	if err != nil || host == "" {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// Validate prüft die Konfiguration auf Vollständigkeit.
func (c Config) Validate() error {
	if !uuidRe.MatchString(c.ClientID) {
		return errors.New("client_id fehlt oder ist keine UUID")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("token fehlt")
	}
	pdh := normalizePDH(c.PDH)
	if len(pdh) == 0 {
		return errors.New("mindestens eine PDH-Adresse nötig")
	}
	for _, a := range pdh {
		if !validPDHAddr(a) {
			return fmt.Errorf("ungültige PDH-Adresse %q", a)
		}
	}
	if err := datanet.ValidateTargets(c.Targets); err != nil {
		return err
	}
	if strings.TrimSpace(c.Hostname) == "" {
		return errors.New("hostname fehlt")
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return errors.New("state_dir fehlt")
	}
	return nil
}

// LoadConfig liest die Konfiguration; PDH-Adressen ohne Port bekommen :8443.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("Konfiguration %s: %w", path, err)
	}
	c.PDH = normalizePDH(c.PDH)
	return c, nil
}

// SaveConfig schreibt die Konfiguration atomar mit Rechten 0600 (unter Windows
// gilt die ACL des Ordners, siehe service_windows.go).
func SaveConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // schlägt nach erfolgreichem Rename harmlos fehl
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
