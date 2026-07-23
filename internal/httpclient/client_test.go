package httpclient

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
)

func TestNewUsesProcessProxyEnvironmentWhenAccountProxyEmpty(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example:8080")
	t.Setenv("NO_PROXY", "chatgpt.com")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("no_proxy", "")

	client := New(Options{}, "")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("empty account proxy should still honor process proxy environment")
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := url.Parse("http://env-proxy.example:8080")
	if proxy == nil || proxy.String() != expected.String() {
		t.Fatalf("unexpected environment proxy: %v", proxy)
	}

	bypassedReq, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err = transport.Proxy(bypassedReq)
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil {
		t.Fatalf("NO_PROXY should bypass the environment proxy, got %v", proxy)
	}
}

func TestNewAccountProxyOverridesEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example:8080")

	client := New(Options{}, "http://account-proxy.example:8080")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	if transport.Proxy == nil {
		t.Fatal("account proxy was not configured")
	}

	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := url.Parse("http://account-proxy.example:8080")
	if proxy == nil || proxy.String() != expected.String() {
		t.Fatalf("unexpected account proxy: %v", proxy)
	}
}

func TestNewRejectsInvalidAccountProxyWithoutFallingBackOrLeakingCredentials(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example:8080")

	client := New(Options{}, "ftp://proxy-user:proxy-secret@account-proxy.example:21")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := transport.Proxy(req)
	if !errors.Is(err, errInvalidAccountProxy) {
		t.Fatalf("expected invalid account proxy error, got proxy=%v err=%v", proxy, err)
	}
	if strings.Contains(err.Error(), "proxy-user") || strings.Contains(err.Error(), "proxy-secret") {
		t.Fatalf("proxy error leaked credentials: %q", err)
	}
}

func TestNewForcesTLSVerificationInProduction(t *testing.T) {
	client := New(Options{
		Environment:   config.EnvironmentProduction,
		SkipSSLVerify: true,
	}, "")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("production HTTP client must verify upstream TLS certificates")
	}
}

func TestNewAllowsSkipTLSVerificationInDevelopment(t *testing.T) {
	client := New(Options{
		Environment:   config.EnvironmentDevelopment,
		SkipSSLVerify: true,
	}, "")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("development HTTP client did not apply skip-verify setting")
	}
}

func TestFromConfig(t *testing.T) {
	opts := FromConfig(config.Config{
		Environment:   config.EnvironmentDevelopment,
		SkipSSLVerify: true,
	})
	if opts.Environment != config.EnvironmentDevelopment || !opts.SkipSSLVerify {
		t.Fatalf("unexpected options: %+v", opts)
	}
}
