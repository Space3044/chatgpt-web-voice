package app

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/tlsutil"
)

func newTLSTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPrepareTLSDevelopmentGeneratesSelfSignedCertificate(t *testing.T) {
	certDir := t.TempDir()
	certFile, keyFile, scheme, err := prepareTLS(config.Config{
		Environment: config.EnvironmentDevelopment,
		TLS:         true,
		TLSCertDir:  certDir,
	}, newTLSTestLogger())
	if err != nil {
		t.Fatalf("development TLS setup failed: %v", err)
	}
	if scheme != "https" {
		t.Fatalf("unexpected scheme: %q", scheme)
	}
	for _, path := range []string{certFile, keyFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("generated TLS file %q is missing: %v", path, err)
		}
	}
}

func TestPrepareTLSProductionRequiresExplicitCertificate(t *testing.T) {
	certDir := t.TempDir()
	_, _, scheme, err := prepareTLS(config.Config{
		Environment: config.EnvironmentProduction,
		TLS:         true,
		TLSCertDir:  certDir,
	}, newTLSTestLogger())
	if err == nil {
		t.Fatal("expected production TLS without certificate paths to fail")
	}
	if !strings.Contains(err.Error(), "VOICE_TLS_CERT") {
		t.Fatalf("unexpected error: %v", err)
	}
	if scheme != "https" {
		t.Fatalf("unexpected scheme on error: %q", scheme)
	}
	entries, readErr := os.ReadDir(certDir)
	if readErr != nil {
		t.Fatalf("read certificate directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("production TLS unexpectedly generated files: %v", entries)
	}
}

func TestPrepareTLSProductionUsesExplicitCertificate(t *testing.T) {
	certDir := t.TempDir()
	certFile, keyFile, err := tlsutil.EnsureLocalCert(certDir)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}

	gotCert, gotKey, scheme, err := prepareTLS(config.Config{
		Environment: config.EnvironmentProduction,
		TLS:         true,
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
	}, newTLSTestLogger())
	if err != nil {
		t.Fatalf("production TLS setup failed: %v", err)
	}
	if gotCert != certFile || gotKey != keyFile {
		t.Fatalf("explicit TLS paths were changed: got %q, %q", gotCert, gotKey)
	}
	if scheme != "https" {
		t.Fatalf("unexpected scheme: %q", scheme)
	}
}

func TestPrepareTLSPartialCertificateConfigurationFails(t *testing.T) {
	certDir := t.TempDir()
	certFile, _, err := tlsutil.EnsureLocalCert(certDir)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}

	_, _, _, err = prepareTLS(config.Config{
		Environment: config.EnvironmentDevelopment,
		TLS:         true,
		TLSCertFile: certFile,
	}, newTLSTestLogger())
	if err == nil {
		t.Fatal("expected partial TLS configuration to fail")
	}
	if !strings.Contains(err.Error(), "provided together") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareTLSDisabledDoesNotRequireEnvironmentOrCertificates(t *testing.T) {
	certFile, keyFile, scheme, err := prepareTLS(config.Config{TLS: false}, newTLSTestLogger())
	if err != nil {
		t.Fatalf("HTTP setup failed: %v", err)
	}
	if certFile != "" || keyFile != "" || scheme != "http" {
		t.Fatalf("unexpected HTTP TLS result: %q, %q, %q", certFile, keyFile, scheme)
	}
}
