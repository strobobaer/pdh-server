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
	Update   UpdateConfig
	Microsoft MicrosoftConfig
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
	Backend        string
	OllamaURL      string
	Model          string
	AnthropicKey   string
	AnthropicModel string // FIX: war hardcoded in copilot.go
}

type UpdateConfig struct {
	AgentURL   string
	AgentToken string
}

type MicrosoftConfig struct {
	ClientID          string
	ClientSecret      string
	RedirectURL       string
	TenantID          string
	TeamsSenderUserID string
	TeamsID           string
	TeamsChannelID    string
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
	viper.BindEnv("copilot.anthropicmodel", "PDH_COPILOT_ANTHROPICMODEL")
	viper.BindEnv("update.agenturl", "PDH_UPDATE_AGENT_URL")
	viper.BindEnv("update.agenttoken", "PDH_UPDATE_AGENT_TOKEN")
	viper.BindEnv("microsoft.clientid", "PDH_MICROSOFT_CLIENT_ID")
	viper.BindEnv("microsoft.clientsecret", "PDH_MICROSOFT_CLIENT_SECRET")
	viper.BindEnv("microsoft.redirecturl", "PDH_MICROSOFT_REDIRECT_URL")
	viper.BindEnv("microsoft.tenantid", "PDH_MICROSOFT_TENANT_ID")
	viper.BindEnv("microsoft.teamssenderuserid", "PDH_MICROSOFT_TEAMS_SENDER_USER_ID")
	viper.BindEnv("microsoft.teamsid", "PDH_MICROSOFT_TEAMS_ID")
	viper.BindEnv("microsoft.teamschannelid", "PDH_MICROSOFT_TEAMS_CHANNEL_ID")

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
	viper.SetDefault("copilot.anthropicmodel", "claude-sonnet-4-20250514")

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
	cfg.Copilot.AnthropicModel = viper.GetString("copilot.anthropicmodel")
	cfg.Update.AgentURL = viper.GetString("update.agenturl")
	cfg.Update.AgentToken = viper.GetString("update.agenttoken")
	cfg.Microsoft.ClientID = viper.GetString("microsoft.clientid")
	cfg.Microsoft.ClientSecret = viper.GetString("microsoft.clientsecret")
	cfg.Microsoft.RedirectURL = viper.GetString("microsoft.redirecturl")
	cfg.Microsoft.TenantID = viper.GetString("microsoft.tenantid")
	cfg.Microsoft.TeamsSenderUserID = viper.GetString("microsoft.teamssenderuserid")
	cfg.Microsoft.TeamsID = viper.GetString("microsoft.teamsid")
	cfg.Microsoft.TeamsChannelID = viper.GetString("microsoft.teamschannelid")

	if len(cfg.Auth.JWTSecret) < 32 {
		return nil, fmt.Errorf("auth.jwtsecret muss gesetzt und mindestens 32 zeichen lang sein")
	}

	return cfg, nil
}
