package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentProduction  = "production"

	// Upstream transport modes.
	// Docker image defaults to curl-impersonate; local go run defaults to tls-client.
	TransportTLSClient       = "tls-client"
	TransportCurlImpersonate = "curl-impersonate"
	TransportGo              = "go"

	// Defaults mirror ChatGPT2API-GO account fingerprint + upstream headers
	// (Edge 143 browser persona + curl edge_101).
	//
	// tls-client MappedTLSClients has no edge_101; Chrome_120 is the same
	// fallback their upstreamTLSProfile() uses when edge_101 is missing.
	DefaultTLSProfile = "chrome_120"
	// defaultImpersonate is the curl-impersonate launcher (lwthiker v0.6.1).
	defaultImpersonate = "edge_101"

	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0"
	defaultSecCHUA   = `"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`
	// Full Client Hints match ChatGPT2API-GO headers() defaults.
	defaultSecCHUAArch            = `"x86"`
	defaultSecCHUABitness         = `"64"`
	defaultSecCHUAFullVersion     = `"143.0.3650.96"`
	defaultSecCHUAFullVersionList = `"Microsoft Edge";v="143.0.3650.96", "Chromium";v="143.0.7499.147", "Not A(Brand";v="24.0.0.0"`
	defaultSecCHUAMobile          = "?0"
	defaultSecCHUAModel           = `""`
	defaultSecCHUAPlatform        = `"Windows"`
	defaultSecCHUAPlatformVersion = `"19.0.0"`
	// ChatGPT web client build identifiers from ChatGPT2API-GO.
	defaultClientVersion     = "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad"
	defaultClientBuildNumber = "5955942"
)

// Config holds runtime settings for the voice gateway.
type Config struct {
	Environment         string
	DataDir             string
	StaticDir           string
	DatabaseFile        string
	AuthUsername        string
	AuthPassword        string
	AuthSessionTTL      int
	LoginMaxFailures    int
	LoginLockoutSeconds int
	LoginWindowSeconds  int
	TokenEncryptionKey  string
	// UpstreamTransport selects the HTTP stack for chatgpt.com:
	// "tls-client" (local default), "curl-impersonate" (Docker default), or "go".
	UpstreamTransport string
	// TLSProfile is the bogdanfinn/tls-client profile key (e.g. chrome_120).
	TLSProfile string
	// Impersonate is the curl-impersonate browser profile (e.g. edge_101).
	Impersonate string
	// CurlImpersonateBin is an optional absolute path to a curl-impersonate binary.
	CurlImpersonateBin string
	// DeviceID / SessionID are process-global oai-* fingerprint values.
	DeviceID               string
	SessionID              string
	SkipSSLVerify          bool
	SessionTTLSeconds      int
	MaxAccountAttempts     int
	DefaultUA              string
	SecCHUA                string
	SecCHUAArch            string
	SecCHUABitness         string
	SecCHUAFullVersion     string
	SecCHUAFullVersionList string
	SecCHUAMobile          string
	SecCHUAModel           string
	SecCHUAPlatform        string
	SecCHUAPlatformVersion string
	ClientVersion          string
	ClientBuildNumber      string
	ListenAddr             string
	TLS                    bool
	TLSCertFile            string
	TLSKeyFile             string
	TLSCertDir             string
	LogFormat              string
	LogLevel               string
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

func normalizeUpstreamTransport(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", TransportTLSClient, "tls", "tlsclient":
		return TransportTLSClient
	case TransportCurlImpersonate, "curl":
		return TransportCurlImpersonate
	case TransportGo, "stdlib", "nethttp", "net/http":
		return TransportGo
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
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

	transport := normalizeUpstreamTransport(env("VOICE_UPSTREAM_TRANSPORT", TransportTLSClient))

	deviceID := env("VOICE_DEVICE_ID", "")
	if deviceID == "" {
		deviceID = uuid.New().String()
	}
	sessionID := env("VOICE_SESSION_ID", "")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	return Config{
		Environment:            environment,
		DataDir:                dataDir,
		StaticDir:              staticDir,
		DatabaseFile:           databaseFile,
		AuthUsername:           env("VOICE_AUTH_USERNAME", ""),
		AuthPassword:           envRaw("VOICE_AUTH_PASSWORD", ""),
		AuthSessionTTL:         envInt("VOICE_AUTH_SESSION_TTL_SECONDS", 12*60*60),
		LoginMaxFailures:       envInt("VOICE_LOGIN_MAX_FAILURES", 8),
		LoginWindowSeconds:     envInt("VOICE_LOGIN_WINDOW_SECONDS", 15*60),
		LoginLockoutSeconds:    envInt("VOICE_LOGIN_LOCKOUT_SECONDS", 15*60),
		TokenEncryptionKey:     envRaw("VOICE_TOKEN_ENCRYPTION_KEY", ""),
		UpstreamTransport:      transport,
		TLSProfile:             env("VOICE_TLS_PROFILE", DefaultTLSProfile),
		Impersonate:            env("VOICE_IMPERSONATE", defaultImpersonate),
		CurlImpersonateBin:     env("VOICE_CURL_IMPERSONATE_BIN", ""),
		DeviceID:               deviceID,
		SessionID:              sessionID,
		SkipSSLVerify:          skipSSLVerify,
		SessionTTLSeconds:      envInt("VOICE_SESSION_TTL_SECONDS", 3*60),
		MaxAccountAttempts:     maxAttempts,
		DefaultUA:              env("VOICE_USER_AGENT", defaultUserAgent),
		SecCHUA:                env("VOICE_SEC_CH_UA", defaultSecCHUA),
		SecCHUAArch:            env("VOICE_SEC_CH_UA_ARCH", defaultSecCHUAArch),
		SecCHUABitness:         env("VOICE_SEC_CH_UA_BITNESS", defaultSecCHUABitness),
		SecCHUAFullVersion:     env("VOICE_SEC_CH_UA_FULL_VERSION", defaultSecCHUAFullVersion),
		SecCHUAFullVersionList: env("VOICE_SEC_CH_UA_FULL_VERSION_LIST", defaultSecCHUAFullVersionList),
		SecCHUAMobile:          env("VOICE_SEC_CH_UA_MOBILE", defaultSecCHUAMobile),
		SecCHUAModel:           env("VOICE_SEC_CH_UA_MODEL", defaultSecCHUAModel),
		SecCHUAPlatform:        env("VOICE_SEC_CH_UA_PLATFORM", defaultSecCHUAPlatform),
		SecCHUAPlatformVersion: env("VOICE_SEC_CH_UA_PLATFORM_VERSION", defaultSecCHUAPlatformVersion),
		ClientVersion:          env("VOICE_CLIENT_VERSION", defaultClientVersion),
		ClientBuildNumber:      env("VOICE_CLIENT_BUILD_NUMBER", defaultClientBuildNumber),
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
	if c.LoginMaxFailures < 1 {
		return fmt.Errorf("VOICE_LOGIN_MAX_FAILURES must be greater than zero")
	}
	if c.LoginWindowSeconds < 1 {
		return fmt.Errorf("VOICE_LOGIN_WINDOW_SECONDS must be greater than zero")
	}
	if c.LoginLockoutSeconds < 1 {
		return fmt.Errorf("VOICE_LOGIN_LOCKOUT_SECONDS must be greater than zero")
	}
	if strings.TrimSpace(c.DatabaseFile) == "" {
		return fmt.Errorf("VOICE_DATABASE_FILE is required")
	}
	transport := normalizeUpstreamTransport(c.UpstreamTransport)
	switch transport {
	case TransportTLSClient, TransportCurlImpersonate, TransportGo:
	default:
		return fmt.Errorf("VOICE_UPSTREAM_TRANSPORT must be tls-client, curl-impersonate, or go")
	}
	if transport == TransportCurlImpersonate && strings.TrimSpace(c.Impersonate) == "" {
		return fmt.Errorf("VOICE_IMPERSONATE is required when using curl-impersonate transport")
	}
	if transport == TransportTLSClient && strings.TrimSpace(c.TLSProfile) == "" && strings.TrimSpace(c.Impersonate) == "" {
		return fmt.Errorf("VOICE_TLS_PROFILE is required when using tls-client transport")
	}
	if strings.TrimSpace(c.DeviceID) == "" {
		return fmt.Errorf("VOICE_DEVICE_ID must not be empty")
	}
	if strings.TrimSpace(c.SessionID) == "" {
		return fmt.Errorf("VOICE_SESSION_ID must not be empty")
	}
	if strings.EqualFold(strings.TrimSpace(c.Environment), EnvironmentProduction) && c.SkipSSLVerify {
		return fmt.Errorf("production forbids VOICE_SKIP_SSL_VERIFY=true")
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
