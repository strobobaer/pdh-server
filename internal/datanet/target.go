package datanet

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// MaxTargets ist die höchste Zahl freigegebener Ziele je Daten-Client.
const MaxTargets = 16

// Target ist ein im Datennetz freigegebener Port und sein lokales Ziel.
type Target struct {
	Port int    `json:"port"`
	Addr string `json:"addr"` // host:port lokal
}

// ParseTarget liest "1433" (→ 127.0.0.1:1433) oder "1433=10.0.0.5:1433".
func ParseTarget(s string) (Target, error) {
	s = strings.TrimSpace(s)
	portStr, addr, hasAddr := strings.Cut(s, "=")
	port, err := parsePort(strings.TrimSpace(portStr))
	if err != nil {
		return Target{}, err
	}
	if !hasAddr {
		return Target{Port: port, Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}, nil
	}
	addr = strings.TrimSpace(addr)
	if err := validateAddr(addr); err != nil {
		return Target{}, err
	}
	return Target{Port: port, Addr: addr}, nil
}

// validateAddr prüft ein lokales Ziel host:port: Host nicht leer, ohne "=" und
// keine Platzhalteradresse (0.0.0.0, ::), Port 1–65535.
func validateAddr(addr string) error {
	host, p, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.Contains(host, "=") {
		return fmt.Errorf("ungültiges Ziel %q: erwartet Host:Port", addr)
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		return fmt.Errorf("ungültiges Ziel %q: Platzhalteradresse nicht erlaubt", addr)
	}
	_, err = parsePort(p)
	return err
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("ungültiger Port %q (1–65535)", s)
	}
	return n, nil
}

// ValidateTargets prüft Anzahl, Ports und Doppelte.
func ValidateTargets(ts []Target) error {
	if len(ts) == 0 {
		return errors.New("mindestens ein Ziel nötig")
	}
	if len(ts) > MaxTargets {
		return fmt.Errorf("höchstens %d Ziele erlaubt", MaxTargets)
	}
	seen := map[int]bool{}
	for _, t := range ts {
		if t.Port < 1 || t.Port > 65535 {
			return fmt.Errorf("ungültiger Port %d", t.Port)
		}
		if t.Port == ReportPort {
			return fmt.Errorf("Port %d ist für Meldungen reserviert", ReportPort)
		}
		if err := validateAddr(t.Addr); err != nil {
			return fmt.Errorf("Port %d: %w", t.Port, err)
		}
		if seen[t.Port] {
			return fmt.Errorf("Port %d doppelt", t.Port)
		}
		seen[t.Port] = true
	}
	return nil
}
