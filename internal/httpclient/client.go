package httpclient

import (
	"crypto/tls"
	"errors"
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

// New builds an HTTP client for upstream ChatGPT traffic.
//
// Proxy selection:
//  1. non-empty account proxy wins (per-account override)
//  2. otherwise use process proxy environment variables
//     (HTTP_PROXY / HTTPS_PROXY and NO_PROXY, including lowercase variants)
//
// Local development may explicitly disable upstream TLS verification;
// production always verifies certificates.
// Note: Go cannot fully replicate curl_cffi Chrome TLS impersonation;
// headers still match the browser client used by ChatGPT web.
func New(opts Options, accountProxy string) *http.Client {
	proxyURL := strings.TrimSpace(accountProxy)
	skipSSLVerify := opts.SkipSSLVerify
	if strings.EqualFold(strings.TrimSpace(opts.Environment), config.EnvironmentProduction) {
		skipSSLVerify = false
	}

	transport := &http.Transport{
		Proxy: proxyFunc(proxyURL),
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

	return &http.Client{
		Timeout:   120 * time.Second,
		Transport: transport,
	}
}

// proxyFunc returns the transport Proxy callback. Account proxy overrides
// environment proxies; empty account proxy falls back to ProxyFromEnvironment.
func proxyFunc(accountProxy string) func(*http.Request) (*url.URL, error) {
	if accountProxy == "" {
		return http.ProxyFromEnvironment
	}
	u, err := url.Parse(accountProxy)
	if err != nil || u.Hostname() == "" || !supportedProxyScheme(u.Scheme) {
		// Do not include the raw URL because it may contain proxy credentials.
		return func(*http.Request) (*url.URL, error) {
			return nil, errInvalidAccountProxy
		}
	}
	return http.ProxyURL(u)
}

var errInvalidAccountProxy = errors.New("invalid account proxy URL")

func supportedProxyScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}
