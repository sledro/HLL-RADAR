package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// HostedConfig holds configuration for hosted multi-tenant mode
type HostedConfig struct {
	JWTSecret           string `mapstructure:"jwt_secret"`
	JWTAccessTTLMinutes int    `mapstructure:"jwt_access_ttl_minutes"`
	JWTRefreshTTLDays   int    `mapstructure:"jwt_refresh_ttl_days"`
	BaseURL             string `mapstructure:"base_url"`
}

// TurnstileConfig holds Cloudflare Turnstile configuration
type TurnstileConfig struct {
	SiteKey   string `mapstructure:"site_key"`
	SecretKey string `mapstructure:"secret_key"`
}

// SMTPConfig holds SMTP configuration for sending invite emails
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`
}

// Load loads the configuration from config.toml, with environment variable overrides.
// Environment variables use the prefix HLL_ and double underscores for nesting:
//
//	HLL_GLOBAL__MODE=hosted
//	HLL_DATABASE__HOST=postgres.railway.internal
//	HLL_DATABASE__PORT=5432
//	HLL_HOSTED__JWT_SECRET=...
//	HLL_SMTP__HOST=smtp.example.com
//	HLL_WEBSERVER__PORT=8080
//
// If DATABASE_URL is set (e.g. by Railway), it is parsed and used for database config.
func Load() error {
	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath(".")    // Project root (local dev) or /app (Docker)

	// Register ALL config keys with defaults so env var overrides work.
	// Viper's AutomaticEnv only matches env vars to keys it already knows about.

	// [global]
	viper.SetDefault("global.mode", "standalone")
	viper.SetDefault("global.log_level", "info")
	viper.SetDefault("global.log_file", false)

	// [database]
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", 5432)
	viper.SetDefault("database.user", "")
	viper.SetDefault("database.password", "")
	viper.SetDefault("database.dbname", "")
	viper.SetDefault("database.sslmode", "disable")

	// [tracker]
	viper.SetDefault("tracker.enabled", true)
	viper.SetDefault("tracker.log_level", "info")
	viper.SetDefault("tracker.check_interval_seconds", 5)

	// [webserver]
	viper.SetDefault("webserver.enabled", true)
	viper.SetDefault("webserver.port", 8080)
	viper.SetDefault("webserver.log_level", "info")
	viper.SetDefault("webserver.cors_origins", []string{"*"})
	viper.SetDefault("webserver.sp_editor", false)

	// [crcon] (standalone mode only)
	viper.SetDefault("crcon.enabled", false)
	viper.SetDefault("crcon.url", "")
	viper.SetDefault("crcon.cache_ttl_seconds", 60)

	// [hosted] (hosted mode only)
	viper.SetDefault("hosted.jwt_secret", "")
	viper.SetDefault("hosted.jwt_access_ttl_minutes", 15)
	viper.SetDefault("hosted.jwt_refresh_ttl_days", 30)
	viper.SetDefault("hosted.base_url", "")

	// [smtp] (hosted mode only)
	viper.SetDefault("smtp.host", "")
	viper.SetDefault("smtp.port", 0)
	viper.SetDefault("smtp.username", "")
	viper.SetDefault("smtp.password", "")
	viper.SetDefault("smtp.from", "")

	// [turnstile] (optional, hosted mode)
	viper.SetDefault("turnstile.site_key", "")
	viper.SetDefault("turnstile.secret_key", "")

	// Enable environment variable overrides with HLL_ prefix.
	// Uses "__" (double underscore) as the key delimiter for nested keys:
	//   HLL_DATABASE__HOST  →  database.host
	//   HLL_HOSTED__JWT_SECRET  →  hosted.jwt_secret
	viper.SetEnvPrefix("HLL")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	viper.AutomaticEnv()

	// Try to read config file — not required if env vars provide all config
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("error reading config file: %w", err)
		}
		// Config file not found is OK — env vars may provide everything
	}

	// If DATABASE_URL is set (common in Railway/Heroku), parse it
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		parseDatabaseURL(dbURL)
	}

	return nil
}

// parseDatabaseURL parses a postgres:// URL and sets individual viper keys.
func parseDatabaseURL(rawURL string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	viper.Set("database.host", u.Hostname())
	if p := u.Port(); p != "" {
		port, _ := strconv.Atoi(p)
		viper.Set("database.port", port)
	}
	if u.User != nil {
		viper.Set("database.user", u.User.Username())
		if pw, ok := u.User.Password(); ok {
			viper.Set("database.password", pw)
		}
	}
	if len(u.Path) > 1 {
		viper.Set("database.dbname", u.Path[1:]) // strip leading /
	}
	q := u.Query()
	if sslmode := q.Get("sslmode"); sslmode != "" {
		viper.Set("database.sslmode", sslmode)
	} else {
		viper.Set("database.sslmode", "disable")
	}
}

// StartWatching enables config file hot reloading
func StartWatching(logger *slog.Logger) {
	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		logger.Info("Config file changed, reloading", "file", e.Name, "operation", e.Op.String())

		// Re-validate config after reload
		if err := Validate(); err != nil {
			logger.Error("Config validation failed after reload", "error", err)
		} else {
			logger.Info("Config reloaded successfully")
		}
	})
}

// GetMode returns the application mode ("standalone" or "hosted")
func GetMode() string {
	mode := viper.GetString("global.mode")
	if mode == "" {
		return "standalone"
	}
	return mode
}

// IsHostedMode returns true if the application is running in hosted multi-tenant mode
func IsHostedMode() bool {
	return GetMode() == "hosted"
}

// GetHostedConfig returns the hosted mode configuration.
// Uses viper.Get* instead of UnmarshalKey so env var overrides work.
func GetHostedConfig() HostedConfig {
	cfg := HostedConfig{
		JWTSecret:           viper.GetString("hosted.jwt_secret"),
		JWTAccessTTLMinutes: viper.GetInt("hosted.jwt_access_ttl_minutes"),
		JWTRefreshTTLDays:   viper.GetInt("hosted.jwt_refresh_ttl_days"),
		BaseURL:             viper.GetString("hosted.base_url"),
	}
	if cfg.JWTAccessTTLMinutes == 0 {
		cfg.JWTAccessTTLMinutes = 15
	}
	if cfg.JWTRefreshTTLDays == 0 {
		cfg.JWTRefreshTTLDays = 30
	}
	return cfg
}

// GetTurnstileConfig returns the Turnstile configuration.
func GetTurnstileConfig() TurnstileConfig {
	return TurnstileConfig{
		SiteKey:   viper.GetString("turnstile.site_key"),
		SecretKey: viper.GetString("turnstile.secret_key"),
	}
}

// IsTurnstileEnabled returns true if Turnstile is configured.
func IsTurnstileEnabled() bool {
	cfg := GetTurnstileConfig()
	return cfg.SiteKey != "" && cfg.SecretKey != ""
}

// GetSMTPConfig returns the SMTP configuration.
// Uses viper.Get* instead of UnmarshalKey so env var overrides work.
func GetSMTPConfig() SMTPConfig {
	return SMTPConfig{
		Host:     viper.GetString("smtp.host"),
		Port:     viper.GetInt("smtp.port"),
		Username: viper.GetString("smtp.username"),
		Password: viper.GetString("smtp.password"),
		From:     viper.GetString("smtp.from"),
	}
}

// ServerConfig represents a single RCON server configuration
type ServerConfig struct {
	Name        string `mapstructure:"name"`
	DisplayName string `mapstructure:"display_name"`
	Host        string `mapstructure:"host"`
	Port        int    `mapstructure:"port"`
	Password    string `mapstructure:"password"`
	Enabled     bool   `mapstructure:"enabled"`
}

// GetServers returns all configured servers
func GetServers() ([]ServerConfig, error) {
	var servers []ServerConfig
	if err := viper.UnmarshalKey("servers", &servers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal servers: %w", err)
	}
	return servers, nil
}

// Validate validates that all required configuration values are present
func Validate() error {
	if IsHostedMode() {
		return validateHostedMode()
	}
	return validateStandaloneMode()
}

func validateStandaloneMode() error {
	servers, err := GetServers()
	if err != nil {
		return fmt.Errorf("failed to get servers: %w", err)
	}

	if len(servers) == 0 {
		return fmt.Errorf("no servers configured - at least one server is required")
	}

	for i, server := range servers {
		if server.Name == "" {
			return fmt.Errorf("server %d: name is required", i)
		}
		if server.DisplayName == "" {
			return fmt.Errorf("server %s: display_name is required", server.Name)
		}
		if server.Host == "" {
			return fmt.Errorf("server %s: host is required", server.Name)
		}
		if server.Port == 0 {
			return fmt.Errorf("server %s: port is required", server.Name)
		}
		if server.Password == "" {
			return fmt.Errorf("server %s: password is required", server.Name)
		}
	}

	return nil
}

func validateHostedMode() error {
	cfg := GetHostedConfig()
	if cfg.JWTSecret == "" || cfg.JWTSecret == "change-me-to-a-random-64-char-string" {
		return fmt.Errorf("hosted.jwt_secret must be set to a secure random string")
	}

	smtp := GetSMTPConfig()
	if smtp.Host == "" {
		return fmt.Errorf("smtp.host is required in hosted mode")
	}
	if smtp.Port == 0 {
		return fmt.Errorf("smtp.port is required in hosted mode")
	}
	if smtp.From == "" {
		return fmt.Errorf("smtp.from is required in hosted mode")
	}

	return nil
}
