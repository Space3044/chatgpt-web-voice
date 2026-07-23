package httpclient

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
)

// Options controls upstream HTTP transport policy. Callers pass only the fields
// that affect TLS and environment mode, not the full gateway configuration.
type Options struct {
	Environment   string
	SkipSSLVerify bool
}

// FromConfig extracts upstream client options from gateway configuration.
func FromConfig(cfg config.Config) Options {
	return Options{
		Environment:   cfg.Environment,
		SkipSSLVerify: cfg.SkipSSLVerify,
	}
}

// New builds an HTTP client with an optional account-specific proxy. Local
// development may explicitly disable upstream TLS verification; production
// always verifies certificates. An empty proxy always means a direct
// connection; process-wide proxy environment variables are intentionally
// ignored.
// Note: Go cannot fully replicate curl_cffi Chrome TLS impersonation;
// headers still match the browser client used by ChatGPT web.
func New(opts Options, accountProxy string) *http.Client {
	proxyURL := strings.TrimSpace(accountProxy)
	skipSSLVerify := opts.SkipSSLVerify
	if strings.EqualFold(strings.TrimSpace(opts.Environment), config.EnvironmentProduction) {
		skipSSLVerify = false
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipSSLVerify, //nolint:gosec // production always forces certificate verification
		},
	}

	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}

	return &http.Client{
		Timeout:   120 * time.Second,
		Transport: transport,
	}
}
