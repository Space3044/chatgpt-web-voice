package app

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/tlsutil"
)

func prepareTLS(cfg config.Config, logger *slog.Logger) (certFile, keyFile, scheme string, err error) {
	scheme = "http"
	if !cfg.TLS {
		return "", "", scheme, nil
	}
	scheme = "https"
	certFile, keyFile = strings.TrimSpace(cfg.TLSCertFile), strings.TrimSpace(cfg.TLSKeyFile)
	environment := strings.ToLower(strings.TrimSpace(cfg.Environment))
	selfSigned := false

	switch environment {
	case config.EnvironmentDevelopment:
		if certFile == "" && keyFile == "" {
			selfSigned = true
			certFile, keyFile, err = tlsutil.EnsureLocalCert(cfg.TLSCertDir)
			if err != nil {
				return "", "", scheme, err
			}
		}
	case config.EnvironmentProduction:
		if certFile == "" && keyFile == "" {
			return "", "", scheme, fmt.Errorf("production gateway TLS requires VOICE_TLS_CERT and VOICE_TLS_KEY")
		}
	default:
		return "", "", scheme, fmt.Errorf("VOICE_ENV must be development or production")
	}

	if certFile == "" || keyFile == "" {
		return "", "", scheme, fmt.Errorf("VOICE_TLS_CERT and VOICE_TLS_KEY must be provided together")
	}
	if err := validateTLSFile(certFile, "VOICE_TLS_CERT"); err != nil {
		return "", "", scheme, err
	}
	if err := validateTLSFile(keyFile, "VOICE_TLS_KEY"); err != nil {
		return "", "", scheme, err
	}
	logger.Info("tls_ready", "environment", environment, "certificate", certFile, "self_signed", selfSigned)
	return certFile, keyFile, scheme, nil
}

func validateTLSFile(path, envName string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s file %q is not accessible: %w", envName, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s file %q is a directory", envName, path)
	}
	return nil
}

func logListeningAddresses(addr, staticDir, scheme string, logger *slog.Logger) {
	port := normalizePort(addr)
	if _, err := os.Stat(filepath.Join(staticDir, "voice.html")); err != nil {
		logger.Warn("static_voice_page_missing", "static_dir", staticDir)
		return
	}
	logger.Info("server_listening", "address", addr, "url", scheme+"://127.0.0.1"+port+"/voice")
	for _, ip := range localIPv4s() {
		logger.Info("server_lan_url", "url", scheme+"://"+ip+port+"/voice", "microphone", scheme == "https")
	}
	if scheme == "http" {
		logger.Info("port_forward_hint", "port", strings.TrimPrefix(port, ":"), "url", "http://127.0.0.1"+port+"/voice")
	}
}

func normalizeListenAddr(addr string) string {
	if strings.HasPrefix(addr, ":") || strings.Contains(addr, ":") {
		return addr
	}
	return ":" + addr
}

// normalizePort returns ":port" form for log URLs.
func normalizePort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":8090"
}

func localIPv4s() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch value := a.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				out = append(out, v4.String())
			}
		}
	}
	return out
}
