package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func testManager() *Manager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWithLimits("admin", "correct horse battery staple", time.Hour, Limits{
		MaxFailures:  8,
		Window:       15 * time.Minute,
		Lockout:      15 * time.Minute,
		FailureDelay: 0,
	}, logger)
}

func TestRequireRedirectsBrowserAndRejectsAPI(t *testing.T) {
	manager := testManager()
	protected := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	browserReq := httptest.NewRequest(http.MethodGet, "/voice", nil)
	browserReq.Header.Set("Accept", "text/html")
	browserResp := httptest.NewRecorder()
	protected.ServeHTTP(browserResp, browserReq)
	if browserResp.Code != http.StatusSeeOther || browserResp.Header().Get("Location") != "/login" {
		t.Fatalf("unexpected browser response: %d %q", browserResp.Code, browserResp.Header().Get("Location"))
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/api/voice/health", nil)
	apiResp := httptest.NewRecorder()
	protected.ServeHTTP(apiResp, apiReq)
	if apiResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected API 401, got %d", apiResp.Code)
	}
	if apiResp.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("management API must not advertise Basic auth")
	}
}

func TestLoginCreatesSessionCookie(t *testing.T) {
	manager := testManager()
	body := bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple","next":"/accounts"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	manager.HandleLogin(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d: %s", resp.Code, resp.Body.String())
	}
	cookies := resp.Result().Cookies()
	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range cookies {
		switch c.Name {
		case sessionCookieName:
			sessionCookie = c
		case csrfCookieName:
			csrfCookie = c
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly {
		t.Fatalf("missing session cookie: %+v", cookies)
	}
	if csrfCookie == nil || csrfCookie.HttpOnly {
		t.Fatalf("missing readable csrf cookie: %+v", cookies)
	}
	if sessionCookie.MaxAge != 0 || !sessionCookie.Expires.IsZero() {
		t.Fatalf("unchecked login should use a session cookie: %+v", sessionCookie)
	}
	var loginBody struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &loginBody); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginBody.Redirect != "/voice" {
		t.Fatalf("login redirect = %q, want /voice", loginBody.Redirect)
	}

	protected := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Username(r.Context()) != "admin" {
			t.Fatalf("unexpected context username: %q", Username(r.Context()))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	protectedReq := httptest.NewRequest(http.MethodGet, "/api/voice/health", nil)
	protectedReq.AddCookie(sessionCookie)
	protectedResp := httptest.NewRecorder()
	protected.ServeHTTP(protectedResp, protectedReq)
	if protectedResp.Code != http.StatusNoContent {
		t.Fatalf("expected authenticated request, got %d", protectedResp.Code)
	}
}

func TestRememberedLoginCreatesPersistentCookie(t *testing.T) {
	manager := testManager()
	body := bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple","remember":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	manager.HandleLogin(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d: %s", resp.Code, resp.Body.String())
	}
	cookies := resp.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.MaxAge <= 0 || sessionCookie.Expires.IsZero() {
		t.Fatalf("remembered login should create a persistent cookie: %+v", cookies)
	}
	if remaining := time.Until(sessionCookie.Expires); remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("unexpected persistent cookie lifetime: %s", remaining)
	}
}

func TestAuthenticatedLoginPageRedirectsToVoice(t *testing.T) {
	manager := testManager()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	manager.HandleLogin(loginResp, loginReq)
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("unexpected login cookies: %+v", loginResp.Result().Cookies())
	}

	req := httptest.NewRequest(http.MethodGet, "/login?next=/accounts", nil)
	req.AddCookie(sessionCookie)
	resp := httptest.NewRecorder()
	manager.LoginPage(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(resp, req)
	if resp.Code != http.StatusSeeOther || resp.Header().Get("Location") != "/voice" {
		t.Fatalf("unexpected authenticated login redirect: %d %q", resp.Code, resp.Header().Get("Location"))
	}
}

func TestFormRememberFlagCreatesPersistentCookie(t *testing.T) {
	manager := testManager()
	form := url.Values{
		"username": {"admin"},
		"password": {"correct horse battery staple"},
		"remember": {"on"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := httptest.NewRecorder()
	manager.HandleLogin(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected form login 200, got %d: %s", resp.Code, resp.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range resp.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.MaxAge <= 0 || sessionCookie.Expires.IsZero() {
		t.Fatalf("form remember flag was not honored: %+v", resp.Result().Cookies())
	}
}

func TestBasicAuthIsRejected(t *testing.T) {
	manager := testManager()
	protected := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/voice/health", nil)
	req.SetBasicAuth("admin", "correct horse battery staple")
	resp := httptest.NewRecorder()
	protected.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected Basic auth to be rejected, got %d", resp.Code)
	}
}


func TestLoginRejectsInvalidCredentials(t *testing.T) {
	manager := testManager()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	manager.HandleLogin(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected login 401, got %d", resp.Code)
	}
	if len(resp.Result().Cookies()) != 0 {
		t.Fatal("invalid login must not set a session cookie")
	}
}


func TestLoginLockoutAfterFailures(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewWithLimits("admin", "correct horse battery staple", time.Hour, Limits{
		MaxFailures:  3,
		Window:       time.Minute,
		Lockout:      time.Minute,
		FailureDelay: 0,
	}, logger)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.10:1234"
		resp := httptest.NewRecorder()
		manager.HandleLogin(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, resp.Code)
		}
	}

	// third failure trips lockout
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.10:1234"
	resp := httptest.NewRecorder()
	manager.HandleLogin(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("expected lockout 429, got %d body=%s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}

	// even correct password is blocked while locked
	okReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`))
	okReq.Header.Set("Content-Type", "application/json")
	okReq.RemoteAddr = "203.0.113.10:1234"
	okResp := httptest.NewRecorder()
	manager.HandleLogin(okResp, okReq)
	if okResp.Code != http.StatusTooManyRequests {
		t.Fatalf("expected locked correct password to stay 429, got %d", okResp.Code)
	}

	// different IP is unaffected
	other := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`))
	other.Header.Set("Content-Type", "application/json")
	other.RemoteAddr = "203.0.113.20:9999"
	otherResp := httptest.NewRecorder()
	manager.HandleLogin(otherResp, other)
	if otherResp.Code != http.StatusOK {
		t.Fatalf("expected other IP to login, got %d %s", otherResp.Code, otherResp.Body.String())
	}
}


func TestCSRFRequiredForCookieSessionWrites(t *testing.T) {
	manager := testManager()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	manager.HandleLogin(loginResp, loginReq)
	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range loginResp.Result().Cookies() {
		switch c.Name {
		case sessionCookieName:
			sessionCookie = c
		case csrfCookieName:
			csrfCookie = c
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login cookies incomplete: %+v", loginResp.Result().Cookies())
	}

	protected := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	// POST without CSRF must fail.
	bad := httptest.NewRequest(http.MethodPost, "/api/accounts", nil)
	bad.AddCookie(sessionCookie)
	badResp := httptest.NewRecorder()
	protected.ServeHTTP(badResp, bad)
	if badResp.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d", badResp.Code)
	}

	// POST with matching header succeeds.
	ok := httptest.NewRequest(http.MethodPost, "/api/accounts", nil)
	ok.AddCookie(sessionCookie)
	ok.AddCookie(csrfCookie)
	ok.Header.Set(csrfHeaderName, csrfCookie.Value)
	okResp := httptest.NewRecorder()
	protected.ServeHTTP(okResp, ok)
	if okResp.Code != http.StatusNoContent {
		t.Fatalf("expected CSRF pass, got %d %s", okResp.Code, okResp.Body.String())
	}

	// GET does not require CSRF.
	get := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	get.AddCookie(sessionCookie)
	getResp := httptest.NewRecorder()
	protected.ServeHTTP(getResp, get)
	if getResp.Code != http.StatusNoContent {
		t.Fatalf("GET should not require CSRF, got %d", getResp.Code)
	}
}

func TestDurableSessionSurvivesMemoryLoss(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewWithLimits("admin", "correct horse battery staple", time.Hour, Limits{FailureDelay: 0}, logger).
		WithSessionStore(NewSessionStore(db))

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple","remember":true}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	manager.HandleLogin(loginResp, loginReq)
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("missing session cookie")
	}

	// Simulate process restart: drop in-memory map.
	manager.mu.Lock()
	manager.sessions = map[string]session{}
	manager.mu.Unlock()

	protected := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Username(r.Context()) != "admin" {
			t.Fatalf("username=%q", Username(r.Context()))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/voice/health", nil)
	req.AddCookie(sessionCookie)
	resp := httptest.NewRecorder()
	protected.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected durable session auth, got %d", resp.Code)
	}
}
