package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
)

type stubAPIKeyAuthenticator struct {
	secret string
	key    apikeys.Key
	err    error
}

func (s stubAPIKeyAuthenticator) Authenticate(secret string) (apikeys.Key, bool, error) {
	if s.err != nil {
		return apikeys.Key{}, false, s.err
	}
	return s.key, secret == s.secret, nil
}

func TestAPIKeyRequireAuthenticatesBearerAndSetsOwner(t *testing.T) {
	manager := NewAPIKeyManager(stubAPIKeyAuthenticator{
		secret: "vgw_live_secret",
		key:    apikeys.Key{ID: 42, Name: "downstream", Enabled: true},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := APIKey(r.Context())
		if !ok || key.ID != 42 || APIKeyOwner(r.Context()) != "api_key:42" {
			t.Fatalf("unexpected API key context: ok=%v key=%+v owner=%q", ok, key, APIKeyOwner(r.Context()))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer vgw_live_secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAPIKeyRequireRejectsMissingAndInvalidSecret(t *testing.T) {
	manager := NewAPIKeyManager(stubAPIKeyAuthenticator{
		secret: "vgw_live_secret",
		key:    apikeys.Key{ID: 1},
	}, nil)
	handler := manager.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler should not run")
	}))
	for _, header := range []string{"", "Basic abc", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized || !strings.Contains(resp.Body.String(), "invalid_api_key") {
			t.Fatalf("header=%q status=%d body=%s", header, resp.Code, resp.Body.String())
		}
	}
}

func TestAPIKeyRequireHandlesStoreFailure(t *testing.T) {
	manager := NewAPIKeyManager(stubAPIKeyAuthenticator{err: errors.New("database unavailable")}, nil)
	handler := manager.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler should not run")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer vgw_live_secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAPIKeyOwnerMissing(t *testing.T) {
	if got := APIKeyOwner(context.Background()); got != "" {
		t.Fatalf("owner=%q", got)
	}
}
