package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func testManager() *Manager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New("admin", "correct horse battery staple", time.Hour, logger)
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
	if apiResp.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("expected Basic auth challenge")
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
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("unexpected cookies: %+v", cookies)
	}
	if cookies[0].MaxAge != 0 || !cookies[0].Expires.IsZero() {
		t.Fatalf("unchecked login should use a session cookie: %+v", cookies[0])
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
	protectedReq.AddCookie(cookies[0])
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
	if len(cookies) != 1 || cookies[0].MaxAge <= 0 || cookies[0].Expires.IsZero() {
		t.Fatalf("remembered login should create a persistent cookie: %+v", cookies)
	}
	if remaining := time.Until(cookies[0].Expires); remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("unexpected persistent cookie lifetime: %s", remaining)
	}
}

func TestAuthenticatedLoginPageRedirectsToVoice(t *testing.T) {
	manager := testManager()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct horse battery staple"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	manager.HandleLogin(loginResp, loginReq)
	cookies := loginResp.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("unexpected login cookies: %+v", cookies)
	}

	req := httptest.NewRequest(http.MethodGet, "/login?next=/accounts", nil)
	req.AddCookie(cookies[0])
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
	cookies := resp.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge <= 0 || cookies[0].Expires.IsZero() {
		t.Fatalf("form remember flag was not honored: %+v", cookies)
	}
}

func TestBasicAuthAndLogout(t *testing.T) {
	manager := testManager()
	protected := manager.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/voice/health", nil)
	req.SetBasicAuth("admin", "correct horse battery staple")
	resp := httptest.NewRecorder()
	protected.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected Basic auth success, got %d", resp.Code)
	}

	badReq := httptest.NewRequest(http.MethodGet, "/api/voice/health", nil)
	badReq.SetBasicAuth("admin", "wrong")
	badResp := httptest.NewRecorder()
	protected.ServeHTTP(badResp, badReq)
	if badResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected bad Basic auth to fail, got %d", badResp.Code)
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
