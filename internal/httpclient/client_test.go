package httpclient

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
)

func TestNewIgnoresProcessProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example:8080")
	t.Setenv("ALL_PROXY", "http://env-proxy.example:8080")

	client := New(Options{}, "")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("empty account proxy must use a direct connection")
	}
}

func TestNewUsesAccountProxyOnly(t *testing.T) {
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
