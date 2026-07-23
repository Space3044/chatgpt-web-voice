package httpclient

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// tlsClientRoundTripper adapts bogdanfinn/tls-client (fhttp) to net/http.RoundTripper
// so the rest of the gateway can keep using the standard library request types.
type tlsClientRoundTripper struct {
	profile      string
	accountProxy string
	skipVerify   bool
	timeoutSec   int

	once   sync.Once
	client tls_client.HttpClient
	err    error
}

func (t *tlsClientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("tls-client: nil request")
	}
	client, err := t.httpClient()
	if err != nil {
		return nil, err
	}

	body, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	fReq, err := fhttp.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	for _, key := range headerKeys(req.Header) {
		for _, value := range req.Header.Values(key) {
			if value == "" {
				continue
			}
			fReq.Header.Add(key, value)
		}
	}

	fResp, err := client.Do(fReq)
	if err != nil {
		return nil, err
	}
	return convertFHTTPResponse(fResp, req), nil
}

func (t *tlsClientRoundTripper) httpClient() (tls_client.HttpClient, error) {
	t.once.Do(func() {
		timeout := t.timeoutSec
		if timeout <= 0 {
			timeout = 120
		}
		options := []tls_client.HttpClientOption{
			tls_client.WithTimeoutSeconds(timeout),
			tls_client.WithClientProfile(resolveTLSProfile(t.profile)),
			tls_client.WithRandomTLSExtensionOrder(),
			tls_client.WithNotFollowRedirects(),
		}
		if proxy := strings.TrimSpace(t.accountProxy); proxy != "" {
			options = append(options, tls_client.WithProxyUrl(proxy))
		} else if envProxy := processProxyURL(); envProxy != "" {
			// When no per-account proxy is set, honor process proxy env once at client build.
			// Per-request NO_PROXY is not re-evaluated by tls-client the same way as net/http.
			options = append(options, tls_client.WithProxyUrl(envProxy))
		}
		if t.skipVerify {
			options = append(options, tls_client.WithInsecureSkipVerify())
		}
		t.client, t.err = tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	})
	return t.client, t.err
}

func resolveTLSProfile(name string) profiles.ClientProfile {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, "-", "_")
	// ChatGPT2API-GO prefers edge_101; tls-client has no Edge mapped profile, so
	// fall through to chrome_120 (same fallback as their upstreamTLSProfile).
	switch key {
	case "", "edge_101", "edge101":
		key = "chrome_120"
	case "chrome136", "chrome_136":
		key = "chrome_133"
	}
	if profile, ok := profiles.MappedTLSClients[key]; ok {
		return profile
	}
	// Fall back to library default rather than panicking on a typo.
	return profiles.DefaultClientProfile
}

// processProxyURL returns the proxy URL that net/http would use for a generic
// HTTPS request when only environment variables are configured. Empty means direct.
func processProxyURL() string {
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	if err != nil {
		return ""
	}
	proxy, err := http.ProxyFromEnvironment(req)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.String()
}

func convertFHTTPResponse(fResp *fhttp.Response, original *http.Request) *http.Response {
	if fResp == nil {
		return nil
	}
	header := make(http.Header, len(fResp.Header))
	for k, values := range fResp.Header {
		for _, v := range values {
			header.Add(k, v)
		}
	}
	return &http.Response{
		Status:     fResp.Status,
		StatusCode: fResp.StatusCode,
		Proto:      fResp.Proto,
		ProtoMajor: fResp.ProtoMajor,
		ProtoMinor: fResp.ProtoMinor,
		Header:     header,
		Body:       fResp.Body,
		Request:    original,
	}
}
