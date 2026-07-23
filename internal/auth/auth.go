package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
)

const sessionCookieName = "voice_gateway_session"

type userContextKey struct{}

type session struct {
	Username  string
	ExpiresAt time.Time
}

// Manager validates configured credentials and owns browser login sessions.
type Manager struct {
	username string
	password string
	ttl      time.Duration
	logger   *slog.Logger

	mu       sync.Mutex
	sessions map[string]session
}

// New creates an authentication manager. Configuration validation is handled
// before this constructor is called.
func New(username, password string, ttl time.Duration, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		username: strings.TrimSpace(username),
		password: password,
		ttl:      ttl,
		logger:   logger,
		sessions: make(map[string]session),
	}
}

// Username returns the authenticated username stored in the request context.
func Username(ctx context.Context) string {
	username, _ := ctx.Value(userContextKey{}).(string)
	return username
}

// Require protects pages, static content, and APIs. Browser navigation is
// redirected to the login page; API clients receive a JSON 401 response.
func (m *Manager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, method, ok := m.authenticate(r)
		if ok {
			ctx := context.WithValue(r.Context(), userContextKey{}, username)
			logging.FromContext(ctx).Debug("authentication_succeeded", "auth_method", method, "username", username)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		logging.FromContext(r.Context()).Warn("authentication_denied", "remote_addr", r.RemoteAddr)
		if isBrowserNavigation(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("WWW-Authenticate", `Basic realm="chatgpt-web-voice", charset="UTF-8"`)
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"detail": map[string]any{"error": "authentication required"},
		})
	})
}

// LoginPage redirects an already authenticated browser to the voice page.
func (m *Manager) LoginPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := m.authenticate(r); ok {
			http.Redirect(w, r, "/voice", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HandleLogin validates a JSON or form username/password and sets an HttpOnly
// session cookie. The optional remember flag controls whether the browser
// retains the cookie after it closes. Credentials are never written to logs.
func (m *Manager) HandleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}

	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid login request")
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid login request")
			return
		}
		input.Username = r.FormValue("username")
		input.Password = r.FormValue("password")
		input.Remember = formBool(r.FormValue("remember"))
	}

	if !m.validCredentials(input.Username, input.Password) {
		logging.FromContext(r.Context()).Warn("login_failed", "username", logUsername(input.Username), "remote_addr", r.RemoteAddr)
		writeAuthError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, err := newSessionToken()
	if err != nil {
		m.logger.Error("session_token_generation_failed", "error", err)
		writeAuthError(w, http.StatusInternalServerError, "failed to create login session")
		return
	}

	expiresAt := time.Now().Add(m.ttl)
	m.mu.Lock()
	m.cleanupLocked(time.Now())
	m.sessions[token] = session{Username: m.username, ExpiresAt: expiresAt}
	m.mu.Unlock()

	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	}
	if input.Remember {
		cookie.Expires = expiresAt
		cookie.MaxAge = int(m.ttl.Seconds())
	}
	http.SetCookie(w, cookie)
	logging.FromContext(r.Context()).Info("login_succeeded", "username", m.username, "remote_addr", r.RemoteAddr)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"username": m.username,
		"redirect": "/voice",
	})
}

// HandleLogout revokes the current browser session and expires its cookie.
func (m *Manager) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		m.mu.Lock()
		delete(m.sessions, cookie.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	logging.FromContext(r.Context()).Info("logout_succeeded", "username", Username(r.Context()))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// HandleSession returns the authenticated browser identity without exposing
// configured credentials.
func (m *Manager) HandleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true,
		"username":      Username(r.Context()),
	})
}

func (m *Manager) authenticate(r *http.Request) (username, method string, ok bool) {
	if username, password, hasBasic := r.BasicAuth(); hasBasic && m.validCredentials(username, password) {
		return m.username, "basic", true
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", "", false
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	item, found := m.sessions[cookie.Value]
	if !found || !now.Before(item.ExpiresAt) {
		delete(m.sessions, cookie.Value)
		return "", "", false
	}
	return item.Username, "session", true
}

func (m *Manager) validCredentials(username, password string) bool {
	wantUsername := sha256.Sum256([]byte(m.username))
	gotUsername := sha256.Sum256([]byte(strings.TrimSpace(username)))
	wantPassword := sha256.Sum256([]byte(m.password))
	gotPassword := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(wantUsername[:], gotUsername[:]) == 1 &&
		subtle.ConstantTimeCompare(wantPassword[:], gotPassword[:]) == 1
}

func (m *Manager) cleanupLocked(now time.Time) {
	for token, item := range m.sessions {
		if !now.Before(item.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
}

func newSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func isBrowserNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return !strings.HasPrefix(r.URL.Path, "/api/") && strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func formBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func logUsername(username string) string {
	username = strings.TrimSpace(username)
	if len(username) > 128 {
		return username[:128]
	}
	return username
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"detail": map[string]any{"error": message},
	})
}
