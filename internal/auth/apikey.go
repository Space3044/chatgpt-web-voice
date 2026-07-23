package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
)

type apiKeyContextKey struct{}

// APIKeyAuthenticator is the storage surface required by Bearer middleware.
type APIKeyAuthenticator interface {
	Authenticate(secret string) (apikeys.Key, bool, error)
}

// APIKeyManager authenticates downstream callers without granting access to
// browser or administrator routes.
type APIKeyManager struct {
	keys   APIKeyAuthenticator
	logger *slog.Logger
}

// NewAPIKeyManager creates downstream Bearer authentication middleware.
func NewAPIKeyManager(keys APIKeyAuthenticator, logger *slog.Logger) *APIKeyManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &APIKeyManager{keys: keys, logger: logger}
}

// APIKey returns the authenticated downstream credential metadata.
func APIKey(ctx context.Context) (apikeys.Key, bool) {
	key, ok := ctx.Value(apiKeyContextKey{}).(apikeys.Key)
	return key, ok
}

// APIKeyOwner returns the opaque owner namespace used for voice-session
// isolation. It never contains the raw secret.
func APIKeyOwner(ctx context.Context) string {
	key, ok := APIKey(ctx)
	if !ok || key.ID < 1 {
		return ""
	}
	return "api_key:" + strconv.FormatInt(key.ID, 10)
}

// Require protects downstream /v1 routes with Authorization: Bearer.
func (m *APIKeyManager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := bearerSecret(r.Header.Get("Authorization"))
		if !ok {
			writeAPIKeyError(w, http.StatusUnauthorized, "invalid_api_key", "valid Bearer API key required")
			return
		}
		key, valid, err := m.keys.Authenticate(secret)
		if err != nil {
			m.logger.Error("api_key_authentication_failed", "error", err)
			writeAPIKeyError(w, http.StatusServiceUnavailable, "authentication_unavailable", "API key authentication unavailable")
			return
		}
		if !valid {
			logging.FromContext(r.Context()).Warn("api_key_denied", "remote_addr", r.RemoteAddr)
			writeAPIKeyError(w, http.StatusUnauthorized, "invalid_api_key", "valid Bearer API key required")
			return
		}
		ctx := context.WithValue(r.Context(), apiKeyContextKey{}, key)
		logging.FromContext(ctx).Debug("api_key_authenticated", "api_key_id", key.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerSecret(header string) (string, bool) {
	parts := strings.Fields(strings.TrimSpace(header))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeAPIKeyError(w http.ResponseWriter, status int, code, message string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="chatgpt-web-voice"`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
