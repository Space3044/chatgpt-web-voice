package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPMiddlewareAddsRequestIDAndAccessLog(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := HTTPMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	req.Header.Set("X-Request-ID", "request-123")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.Code)
	}
	if resp.Header().Get("X-Request-ID") != "request-123" {
		t.Fatalf("unexpected request ID: %q", resp.Header().Get("X-Request-ID"))
	}
	logLine := output.String()
	for _, expected := range []string{`"msg":"http_request"`, `"status":201`, `"response_bytes":2`, `"request_id":"request-123"`} {
		if !strings.Contains(logLine, expected) {
			t.Fatalf("missing %s in access log: %s", expected, logLine)
		}
	}
}

func TestHTTPMiddlewareRecoversPanic(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := HTTPMiddleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.Code)
	}
	if !strings.Contains(output.String(), `"msg":"http_panic"`) {
		t.Fatalf("panic was not logged: %s", output.String())
	}
}
