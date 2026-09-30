package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Init richtet die Protokollierung ein: Konsole (wie bisher) und zusaetzlich
// die Protokoll-Ablage (Tagesdateien + Speicherpuffer), die unter
// Verwaltung -> Server-Einstellungen -> Protokoll gefiltert werden kann.
// Mehrfach aufrufbar (z. B. nach dem Laden der Einstellungen aus der Datenbank).
func Init(env string) {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.SetGlobalLevel(ParseLevel(os.Getenv("PDH_LOG_LEVEL")))

	var console io.Writer = os.Stdout
	if env == "development" {
		console = zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}
	}
	s := Store()
	s.Configure(os.Getenv("PDH_LOG_DIR"), atoiDefault(os.Getenv("PDH_LOG_RETENTION_DAYS"), 14))
	log.Logger = zerolog.New(zerolog.MultiLevelWriter(console, s)).With().Timestamp().Logger()
}

func Get() zerolog.Logger {
	return log.Logger
}

// ParseLevel: debug | info | warn | error (Standard info).
func ParseLevel(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "trace":
		return zerolog.TraceLevel
	}
	return zerolog.InfoLevel
}

// SetLevel aendert die Protokollstufe sofort.
func SetLevel(s string) { zerolog.SetGlobalLevel(ParseLevel(s)) }

func atoiDefault(s string, def int) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return def
	}
	return n
}
