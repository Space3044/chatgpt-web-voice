package config

import (
	"strings"
	"testing"
)

func TestValidateRequiresUsernameAndPassword(t *testing.T) {
	cfg := Config{
		Environment:         EnvironmentDevelopment,
		DatabaseFile:        "voice.db",
		AuthSessionTTL:      60,
		LoginMaxFailures:    8,
		LoginWindowSeconds:  900,
		LoginLockoutSeconds: 900,
		UpstreamTransport:   TransportTLSClient,
		TLSProfile:          DefaultTLSProfile,
		DeviceID:            "device-test",
		SessionID:           "session-test",
		LogFormat:           "json",
		LogLevel:            "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing username to fail validation")
	}
	cfg.AuthUsername = "admin"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing password to fail validation")
	}
	cfg.AuthPassword = "secret"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing token encryption key to fail validation")
	}
	cfg.TokenEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid auth configuration: %v", err)
	}
}

func TestLoadDefaultsToDevelopmentEnvironment(t *testing.T) {
	t.Setenv("VOICE_ENV", "")
	cfg := Load()
	if cfg.Environment != EnvironmentDevelopment {
		t.Fatalf("unexpected default environment: %q", cfg.Environment)
	}
}

func TestLoadDefaultsTo8090(t *testing.T) {
	t.Setenv("VOICE_LISTEN_ADDR", "")
	cfg := Load()
	if cfg.ListenAddr != "0.0.0.0:8090" {
		t.Fatalf("unexpected default listen address: %q", cfg.ListenAddr)
	}
}

func TestLoadNormalizesEnvironment(t *testing.T) {
	t.Setenv("VOICE_ENV", " PRODUCTION ")
	cfg := Load()
	if cfg.Environment != EnvironmentProduction {
		t.Fatalf("unexpected normalized environment: %q", cfg.Environment)
	}
}

func TestLoadForcesTLSVerificationInProduction(t *testing.T) {
	t.Setenv("VOICE_ENV", "production")
	t.Setenv("VOICE_SKIP_SSL_VERIFY", "true")
	cfg := Load()
	if cfg.SkipSSLVerify {
		t.Fatal("production must not allow upstream TLS verification to be disabled")
	}
}

func TestLoadAllowsSkipTLSVerificationInDevelopment(t *testing.T) {
	t.Setenv("VOICE_ENV", "development")
	t.Setenv("VOICE_SKIP_SSL_VERIFY", "true")
	cfg := Load()
	if !cfg.SkipSSLVerify {
		t.Fatal("development skip-verify setting was not loaded")
	}
}

func TestLoadVerifiesTLSByDefaultInDevelopment(t *testing.T) {
	t.Setenv("VOICE_ENV", "development")
	t.Setenv("VOICE_SKIP_SSL_VERIFY", "")
	cfg := Load()
	if cfg.SkipSSLVerify {
		t.Fatal("upstream TLS verification must be enabled by default")
	}
}

func TestValidateRejectsUnknownEnvironment(t *testing.T) {
	cfg := Config{
		Environment:         "staging",
		AuthUsername:        "admin",
		AuthPassword:        "secret",
		TokenEncryptionKey:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DatabaseFile:        "voice.db",
		AuthSessionTTL:      60,
		LoginMaxFailures:    8,
		LoginWindowSeconds:  900,
		LoginLockoutSeconds: 900,
		UpstreamTransport:   TransportTLSClient,
		TLSProfile:          DefaultTLSProfile,
		DeviceID:            "device-test",
		SessionID:           "session-test",
		LogFormat:           "json",
		LogLevel:            "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown environment to fail validation")
	}
}

func TestLoadGeneratesGlobalFingerprint(t *testing.T) {
	t.Setenv("VOICE_DEVICE_ID", "")
	t.Setenv("VOICE_SESSION_ID", "")
	cfg := Load()
	if cfg.DeviceID == "" || cfg.SessionID == "" {
		t.Fatalf("expected auto-generated fingerprint, got device=%q session=%q", cfg.DeviceID, cfg.SessionID)
	}
	if cfg.UpstreamTransport != TransportTLSClient {
		t.Fatalf("default transport=%q", cfg.UpstreamTransport)
	}
	if cfg.TLSProfile != DefaultTLSProfile {
		t.Fatalf("default TLS profile=%q", cfg.TLSProfile)
	}
	if cfg.Impersonate != "edge_101" {
		t.Fatalf("default impersonate=%q", cfg.Impersonate)
	}
	if !strings.Contains(cfg.DefaultUA, "Edg/143") {
		t.Fatalf("expected Edge 143 UA, got %q", cfg.DefaultUA)
	}
	if !strings.Contains(cfg.SecCHUA, "Microsoft Edge") {
		t.Fatalf("expected Edge sec-ch-ua, got %q", cfg.SecCHUA)
	}
	if cfg.ClientVersion != "prod-be885abbfcfe7b1f511e88b3003d9ee44757fbad" {
		t.Fatalf("client version=%q", cfg.ClientVersion)
	}
	if cfg.ClientBuildNumber != "5955942" {
		t.Fatalf("client build=%q", cfg.ClientBuildNumber)
	}
}

func TestValidateRejectsUnknownTransport(t *testing.T) {
	cfg := Config{
		Environment:         EnvironmentDevelopment,
		AuthUsername:        "admin",
		AuthPassword:        "secret",
		TokenEncryptionKey:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DatabaseFile:        "voice.db",
		AuthSessionTTL:      60,
		LoginMaxFailures:    8,
		LoginWindowSeconds:  900,
		LoginLockoutSeconds: 900,
		UpstreamTransport:   "browser",
		TLSProfile:          DefaultTLSProfile,
		DeviceID:            "d",
		SessionID:           "s",
		LogFormat:           "json",
		LogLevel:            "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown transport to fail")
	}
}

func TestLoadPreservesPasswordWhitespace(t *testing.T) {
	t.Setenv("VOICE_AUTH_USERNAME", " admin ")
	t.Setenv("VOICE_AUTH_PASSWORD", " password with spaces ")
	t.Setenv("VOICE_DATABASE_FILE", "./test.db")
	t.Setenv("VOICE_LOG_FORMAT", "json")
	t.Setenv("VOICE_LOG_LEVEL", "info")
	cfg := Load()
	if cfg.AuthUsername != "admin" {
		t.Fatalf("unexpected username: %q", cfg.AuthUsername)
	}
	if cfg.AuthPassword != " password with spaces " {
		t.Fatalf("password whitespace was changed: %q", cfg.AuthPassword)
	}
}


func TestValidateProductionForbidsSkipSSLVerify(t *testing.T) {
	cfg := Config{
		Environment:         EnvironmentProduction,
		UpstreamTransport:   TransportTLSClient,
		TLSProfile:          DefaultTLSProfile,
		DeviceID:            "device-test",
		SessionID:           "session-test",
		AuthUsername:        "admin",
		AuthPassword:        "secret",
		TokenEncryptionKey:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DatabaseFile:        "voice.db",
		AuthSessionTTL:      60,
		LoginMaxFailures:    8,
		LoginWindowSeconds:  900,
		LoginLockoutSeconds: 900,
		SkipSSLVerify:       true,
		LogFormat:           "json",
		LogLevel:            "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected production skip ssl verify to fail")
	}
	cfg.SkipSSLVerify = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected production config without skip-verify to pass: %v", err)
	}
}
