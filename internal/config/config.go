package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentProduction  = "production"
)

// Config holds runtime settings for the voice gateway.
type Config struct {
	Environment        string
	DataDir            string
	StaticDir          string
	DatabaseFile       string
	AuthUsername          string
	AuthPassword          string
	AuthSessionTTL        int
	TokenEncryptionKey    string
	Impersonate           string
	SkipSSLVerify         bool
	SessionTTLSeconds     int
	MaxAccountAttempts    int
	DefaultUA             string
	ClientVersion         string
	ClientBuildNumber     string
	ListenAddr            string
	TLS                   bool
	TLSCertFile           string
	TLSKeyFile            string
	TLSCertDir            string
	LogFormat             string
	LogLevel              string
}

func env(name, def string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	return v
}

func envRaw(name, def string) string {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return def
	}
	return v
}

func envBool(name string, def bool) bool {
	raw := strings.ToLower(env(name, map[bool]string{true: "1", false: "0"}[def]))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func envInt(name string, def int) int {
	raw := env(name, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// Load reads configuration from environment variables.
func Load() Config {
	baseDir, err := os.Getwd()
	if err != nil {
		baseDir = "."
	}

	environment := strings.ToLower(env("VOICE_ENV", EnvironmentDevelopment))
	dataDir := env("VOICE_DATA_DIR", filepath.Join(baseDir, "data"))
	staticDir := env("VOICE_STATIC_DIR", filepath.Join(baseDir, "static"))
	databaseFile := env("VOICE_DATABASE_FILE", filepath.Join(dataDir, "voice.db"))
	skipSSLVerify := envBool("VOICE_SKIP_SSL_VERIFY", false)
	if environment == EnvironmentProduction {
		skipSSLVerify = false
	}

	maxAttempts := envInt("VOICE_MAX_ACCOUNT_ATTEMPTS", 4)
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	return Config{
		Environment:        environment,
		DataDir:            dataDir,
		StaticDir:          staticDir,
		DatabaseFile:       databaseFile,
		AuthUsername:       env("VOICE_AUTH_USERNAME", ""),
		AuthPassword:       envRaw("VOICE_AUTH_PASSWORD", ""),
		AuthSessionTTL:     envInt("VOICE_AUTH_SESSION_TTL_SECONDS", 12*60*60),
		TokenEncryptionKey: envRaw("VOICE_TOKEN_ENCRYPTION_KEY", ""),
		Impersonate:        env("VOICE_IMPERSONATE", "chrome136"),
		SkipSSLVerify:      skipSSLVerify,
		SessionTTLSeconds:  envInt("VOICE_SESSION_TTL_SECONDS", 6*60*60),
		MaxAccountAttempts: maxAttempts,
		DefaultUA: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) " +
			"Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0",
		ClientVersion:     env("VOICE_CLIENT_VERSION", "prod-fb4a8a2a751dfec391053cfd7b01c52699ccf78c"),
		ClientBuildNumber: env("VOICE_CLIENT_BUILD_NUMBER", "8370486"),
		// Bind all interfaces so Windows / VS Code port-forward can reach WSL.
		ListenAddr:  env("VOICE_LISTEN_ADDR", "0.0.0.0:8090"),
		TLS:         envBool("VOICE_TLS", false),
		TLSCertFile: env("VOICE_TLS_CERT", ""),
		TLSKeyFile:  env("VOICE_TLS_KEY", ""),
		TLSCertDir:  env("VOICE_TLS_CERT_DIR", filepath.Join(dataDir, "certs")),
		LogFormat:   env("VOICE_LOG_FORMAT", "json"),
		LogLevel:    env("VOICE_LOG_LEVEL", "info"),
	}
}

// Validate rejects configurations that would leave protected content exposed.
func (c Config) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.Environment)) {
	case EnvironmentDevelopment, EnvironmentProduction:
	default:
		return fmt.Errorf("VOICE_ENV must be development or production")
	}
	if strings.TrimSpace(c.AuthUsername) == "" {
		return fmt.Errorf("VOICE_AUTH_USERNAME is required")
	}
	if c.AuthPassword == "" {
		return fmt.Errorf("VOICE_AUTH_PASSWORD is required")
	}
	if strings.TrimSpace(c.TokenEncryptionKey) == "" {
		return fmt.Errorf("VOICE_TOKEN_ENCRYPTION_KEY is required (32-byte hex or base64 key for sealing access tokens)")
	}
	if c.AuthSessionTTL < 1 {
		return fmt.Errorf("VOICE_AUTH_SESSION_TTL_SECONDS must be greater than zero")
	}
	if strings.TrimSpace(c.DatabaseFile) == "" {
		return fmt.Errorf("VOICE_DATABASE_FILE is required")
	}
	format := strings.ToLower(strings.TrimSpace(c.LogFormat))
	if format != "json" && format != "text" {
		return fmt.Errorf("VOICE_LOG_FORMAT must be json or text")
	}
	level := strings.ToLower(strings.TrimSpace(c.LogLevel))
	if level != "debug" && level != "info" && level != "warn" && level != "error" {
		return fmt.Errorf("VOICE_LOG_LEVEL must be debug, info, warn, or error")
	}
	return nil
}
