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

	client := New(Options{Transport: config.TransportGo}, "")
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

	client := New(Options{Transport: config.TransportGo}, "http://account-proxy.example:8080")
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

	client := New(Options{Transport: config.TransportGo}, "ftp://proxy-user:proxy-secret@account-proxy.example:21")
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
		Transport:     config.TransportGo,
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
		Transport:     config.TransportGo,
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
		Environment:       config.EnvironmentDevelopment,
		SkipSSLVerify:     true,
		UpstreamTransport: config.TransportCurlImpersonate,
		TLSProfile:        config.DefaultTLSProfile,
		Impersonate:       "edge_101",
		DataDir:           "/tmp/data",
	})
	if opts.Environment != config.EnvironmentDevelopment || !opts.SkipSSLVerify {
		t.Fatalf("unexpected options: %+v", opts)
	}
	if opts.Transport != config.TransportCurlImpersonate || opts.Impersonate != "edge_101" || opts.DataDir != "/tmp/data" {
		t.Fatalf("unexpected transport options: %+v", opts)
	}
	if opts.TLSProfile != config.DefaultTLSProfile {
		t.Fatalf("TLS profile not copied: %+v", opts)
	}
}

func TestNewDefaultUsesTLSClient(t *testing.T) {
	client := New(Options{}, "")
	if _, ok := client.Transport.(*tlsClientRoundTripper); !ok {
		t.Fatalf("expected default tlsClientRoundTripper, got %T", client.Transport)
	}
}

func TestNewCurlTransportUsesRoundTripper(t *testing.T) {
	client := New(Options{
		Transport:   config.TransportCurlImpersonate,
		Impersonate: "edge_101",
	}, "")
	if _, ok := client.Transport.(*curlRoundTripper); !ok {
		t.Fatalf("expected curlRoundTripper, got %T", client.Transport)
	}
}

func TestResolveTLSProfileDefaultsAndAliases(t *testing.T) {
	def := resolveTLSProfile("")
	if def.GetClientHelloStr() == "" {
		t.Fatal("default profile should resolve")
	}
	// edge_101 maps to chrome_120 (ChatGPT2API-GO fallback when Edge profile missing).
	edge := resolveTLSProfile("edge_101")
	if edge.GetClientHelloStr() == "" {
		t.Fatal("edge_101 should resolve via chrome_120 fallback")
	}
	chrome133 := resolveTLSProfile("chrome_133")
	if chrome133.GetClientHelloStr() == "" {
		t.Fatal("chrome_133 should resolve")
	}
	_ = resolveTLSProfile("not-a-real-profile")
}

func TestCurlBinaryCandidatesPreferProfile(t *testing.T) {
	names := curlBinaryCandidates("edge_101")
	if len(names) == 0 || names[0] != "curl_edge101" {
		t.Fatalf("unexpected candidates: %v", names)
	}
	defaults := curlBinaryCandidates("")
	if len(defaults) == 0 || defaults[0] != "curl_edge101" {
		t.Fatalf("default candidates: %v", defaults)
	}
}

func TestHeaderKeysOrdersBrowserHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer x")
	h.Set("User-Agent", "ua")
	h.Set("Oai-Device-Id", "d")
	keys := headerKeys(h)
	if len(keys) < 3 {
		t.Fatalf("keys=%v", keys)
	}
	ua, auth := -1, -1
	for i, k := range keys {
		switch http.CanonicalHeaderKey(k) {
		case "User-Agent":
			ua = i
		case "Authorization":
			auth = i
		}
	}
	if ua < 0 || auth < 0 || ua > auth {
		t.Fatalf("unexpected order: %v", keys)
	}
}

func TestFormatTransport(t *testing.T) {
	if got := FormatTransport(Options{}); !strings.HasPrefix(got, "tls-client/") {
		t.Fatalf("FormatTransport(empty)=%q", got)
	}
	if got := FormatTransport(Options{Transport: config.TransportGo}); got != "go" {
		t.Fatalf("got %q", got)
	}
	if got := FormatTransport(Options{Transport: config.TransportCurlImpersonate, Impersonate: "edge_101"}); got != "curl-impersonate/edge_101" {
		t.Fatalf("got %q", got)
	}
}
