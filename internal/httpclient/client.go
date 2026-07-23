package httpclient

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
)

// Options controls upstream HTTP transport policy. Callers pass only the fields
// that affect TLS, environment mode, and browser impersonation — not the full
// gateway configuration.
type Options struct {
	Environment   string
	SkipSSLVerify bool
	Transport     string
	// TLSProfile selects a bogdanfinn/tls-client profile (e.g. chrome_146).
	TLSProfile string
	// Impersonate selects a curl-impersonate launcher profile (e.g. chrome136).
	Impersonate        string
	CurlImpersonateBin string
	DataDir            string
}

// FromConfig extracts upstream client options from gateway configuration.
func FromConfig(cfg config.Config) Options {
	return Options{
		Environment:        cfg.Environment,
		SkipSSLVerify:      cfg.SkipSSLVerify,
		Transport:          cfg.UpstreamTransport,
		TLSProfile:         cfg.TLSProfile,
		Impersonate:        cfg.Impersonate,
		CurlImpersonateBin: cfg.CurlImpersonateBin,
		DataDir:            cfg.DataDir,
	}
}

// New builds an HTTP client for upstream ChatGPT traffic.
//
// Proxy selection:
//  1. non-empty account proxy wins (per-account override)
//  2. otherwise use process proxy environment variables
//     (HTTP_PROXY / HTTPS_PROXY and NO_PROXY, including lowercase variants)
//
// Transport selection (aligned with ChatGPT2API-GO documented defaults):
//   - "tls-client" (default): bogdanfinn/tls-client browser TLS fingerprint
//   - "curl-impersonate": external curl binary with browser TLS fingerprint
//   - "go": stdlib crypto/tls fallback
//
// Local development may explicitly disable upstream TLS verification for the
// Go and tls-client transports; production always verifies certificates.
// curl-impersonate always verifies certificates.
func New(opts Options, accountProxy string) *http.Client {
	proxyURL := strings.TrimSpace(accountProxy)
	transportName := normalizeTransport(opts.Transport)

	skipSSLVerify := opts.SkipSSLVerify
	if strings.EqualFold(strings.TrimSpace(opts.Environment), config.EnvironmentProduction) {
		skipSSLVerify = false
	}

	switch transportName {
	case config.TransportCurlImpersonate:
		return &http.Client{
			Timeout: 120 * time.Second,
			Transport: &curlRoundTripper{
				bin:          strings.TrimSpace(opts.CurlImpersonateBin),
				impersonate:  strings.TrimSpace(opts.Impersonate),
				dataDir:      strings.TrimSpace(opts.DataDir),
				accountProxy: proxyURL,
			},
		}
	case config.TransportGo:
		return newStdlibClient(proxyURL, skipSSLVerify)
	default:
		// tls-client (default)
		profile := strings.TrimSpace(opts.TLSProfile)
		if profile == "" {
			profile = strings.TrimSpace(opts.Impersonate)
		}
		if profile == "" {
			profile = config.DefaultTLSProfile
		}
		return &http.Client{
			Timeout: 120 * time.Second,
			Transport: &tlsClientRoundTripper{
				profile:      profile,
				accountProxy: proxyURL,
				skipVerify:   skipSSLVerify,
				timeoutSec:   120,
			},
		}
	}
}

func newStdlibClient(accountProxy string, skipSSLVerify bool) *http.Client {
	transport := &http.Transport{
		Proxy: proxyFunc(accountProxy),
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

func normalizeTransport(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", config.TransportTLSClient, "tls", "tlsclient":
		return config.TransportTLSClient
	case config.TransportCurlImpersonate, "curl":
		return config.TransportCurlImpersonate
	case config.TransportGo, "stdlib", "nethttp", "net/http":
		return config.TransportGo
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// ResolveCurlBinary returns the curl-impersonate executable that would be used
// for the given options. Useful for startup logging and diagnostics.
func ResolveCurlBinary(opts Options) (string, error) {
	return resolveCurlBinary(opts.CurlImpersonateBin, opts.Impersonate, opts.DataDir)
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

// FormatTransport describes the active upstream transport for logs.
func FormatTransport(opts Options) string {
	name := normalizeTransport(opts.Transport)
	switch name {
	case config.TransportCurlImpersonate:
		profile := strings.TrimSpace(opts.Impersonate)
		if profile == "" {
			profile = "edge_101"
		}
		return fmt.Sprintf("curl-impersonate/%s", profile)
	case config.TransportGo:
		return "go"
	default:
		profile := strings.TrimSpace(opts.TLSProfile)
		if profile == "" {
			profile = strings.TrimSpace(opts.Impersonate)
		}
		if profile == "" {
			profile = config.DefaultTLSProfile
		}
		return fmt.Sprintf("tls-client/%s", profile)
	}
}
