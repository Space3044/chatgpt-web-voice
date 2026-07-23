package logging

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
)

type contextKey struct{}

// New creates the process logger used by both application and access logs.
func New(format, level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: slogLevel}
	if strings.EqualFold(strings.TrimSpace(format), "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// FromContext returns the request-scoped logger when available.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *responseRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// HTTPMiddleware adds request IDs, panic recovery, and structured access logs.
func HTTPMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := requestID(r)
		w.Header().Set("X-Request-ID", requestID)

		requestLogger := logger.With(
			"request_id", requestID,
			"method", r.Method,
			"path", truncate(r.URL.Path, 2048),
		)
		r = r.WithContext(context.WithValue(r.Context(), contextKey{}, requestLogger))
		recorder := &responseRecorder{ResponseWriter: w}

		defer func() {
			if recovered := recover(); recovered != nil {
				requestLogger.Error("http_panic",
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				if recorder.status == 0 {
					http.Error(recorder, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}

			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []any{
				"status", status,
				"response_bytes", recorder.bytes,
				"duration_ms", float64(time.Since(started).Microseconds()) / 1000,
				"remote_ip", remoteIP(r.RemoteAddr),
				"user_agent", truncate(r.UserAgent(), 512),
			}
			switch {
			case status >= 500:
				requestLogger.Error("http_request", attrs...)
			case status >= 400:
				requestLogger.Warn("http_request", attrs...)
			default:
				requestLogger.Info("http_request", attrs...)
			}
		}()

		next.ServeHTTP(recorder, r)
	})
}

func requestID(r *http.Request) string {
	incoming := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if incoming != "" && len(incoming) <= 128 && !strings.ContainsAny(incoming, "\r\n\t") {
		return incoming
	}
	return uuid.NewString()
}

func remoteIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
