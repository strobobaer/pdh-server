package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func Init(env string) {
	zerolog.TimeFieldFormat = time.RFC3339

	if env == "development" {
		log.Logger = log.Output(zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "15:04:05",
		})
	} else {
		log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
}

func Get() zerolog.Logger {
	return log.Logger
}
