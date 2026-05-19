package config

import (
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Auth     AuthConfig
	Copilot  CopilotConfig
}

type ServerConfig struct {
	Host string
	Port int
	Env  string
}

type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

type AuthConfig struct {
	JWTSecret     string
	TokenDuration int
}

type CopilotConfig struct {
	Backend      string
	OllamaURL    string
	Model        string
	AnthropicKey string
}

func Load() (*Config, error) {
	// .env Datei laden
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.ReadInConfig()

	// config.yaml laden (überschreibt .env falls vorhanden)
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.MergeInConfig()

	// Umgebungsvariablen haben höchste Priorität
	// PDH_DATABASE_PASSWORD → database.password
	viper.SetEnvPrefix("PDH")
	viper.AutomaticEnv()

	// Mapping: ENV_VAR → config key
	viper.BindEnv("server.host", "PDH_SERVER_HOST")
	viper.BindEnv("server.port", "PDH_SERVER_PORT")
	viper.BindEnv("server.env", "PDH_SERVER_ENV")
	viper.BindEnv("database.host", "PDH_DATABASE_HOST")
	viper.BindEnv("database.port", "PDH_DATABASE_PORT")
	viper.BindEnv("database.user", "PDH_DATABASE_USER")
	viper.BindEnv("database.password", "PDH_DATABASE_PASSWORD")
	viper.BindEnv("database.name", "PDH_DATABASE_NAME")
	viper.BindEnv("database.sslmode", "PDH_DATABASE_SSLMODE")
	viper.BindEnv("auth.jwtsecret", "PDH_AUTH_JWTSECRET")
	viper.BindEnv("auth.tokenduration", "PDH_AUTH_TOKENDURATION")
	viper.BindEnv("copilot.backend", "PDH_COPILOT_BACKEND")
	viper.BindEnv("copilot.ollamaurl", "PDH_COPILOT_OLLAMAURL")
	viper.BindEnv("copilot.model", "PDH_COPILOT_MODEL")
	viper.BindEnv("copilot.anthropickey", "PDH_COPILOT_ANTHROPICKEY")

	// Standardwerte
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.port", 8090)
	viper.SetDefault("server.env", "development")
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", 5432)
	viper.SetDefault("database.sslmode", "disable")
	viper.SetDefault("auth.tokenduration", 24)
	viper.SetDefault("copilot.backend", "ollama")
	viper.SetDefault("copilot.ollamaurl", "http://localhost:11434")
	viper.SetDefault("copilot.model", "llama3.2")

	cfg := &Config{}
	cfg.Server.Host = viper.GetString("server.host")
	cfg.Server.Port = viper.GetInt("server.port")
	cfg.Server.Env = viper.GetString("server.env")
	cfg.Database.Host = viper.GetString("database.host")
	cfg.Database.Port = viper.GetInt("database.port")
	cfg.Database.User = viper.GetString("database.user")
	cfg.Database.Password = viper.GetString("database.password")
	cfg.Database.Name = viper.GetString("database.name")
	cfg.Database.SSLMode = viper.GetString("database.sslmode")
	cfg.Auth.JWTSecret = viper.GetString("auth.jwtsecret")
	cfg.Auth.TokenDuration = viper.GetInt("auth.tokenduration")
	cfg.Copilot.Backend = viper.GetString("copilot.backend")
	cfg.Copilot.OllamaURL = viper.GetString("copilot.ollamaurl")
	cfg.Copilot.Model = viper.GetString("copilot.model")
	cfg.Copilot.AnthropicKey = viper.GetString("copilot.anthropickey")

	if len(cfg.Auth.JWTSecret) < 32 {
		return nil, fmt.Errorf("auth.jwtsecret muss gesetzt und mindestens 32 zeichen lang sein")
	}

	return cfg, nil
}
