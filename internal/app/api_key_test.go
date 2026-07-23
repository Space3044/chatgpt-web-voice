package app

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/api"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func TestAPIKeyRoutesAreIsolatedFromAdministratorRoutes(t *testing.T) {
	staticDir := t.TempDir()
	for _, name := range []string{"login.html", "voice.html", "accounts.html", "keys.html"} {
		if err := os.WriteFile(filepath.Join(staticDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keyStore := apikeys.NewStore(db)
	created, err := keyStore.Create("integration client")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	adminAuth := auth.New("admin", "password", time.Hour, logger)
	keyAuth := auth.NewAPIKeyManager(keyStore, logger)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 13)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	apiServer := api.New(api.Dependencies{
		Accounts:      accounts.NewPoolFromDB(db).WithBox(box),
		Conversations: conversations.NewStore(db),
		APIKeys:       keyStore,
	})
	handler := newHandler(config.Config{StaticDir: staticDir}, adminAuth, keyAuth, apiServer, logger)

	configReq := httptest.NewRequest(http.MethodGet, "/v1/voice/config", nil)
	configReq.Header.Set("Authorization", "Bearer "+created.Secret)
	configResp := httptest.NewRecorder()
	handler.ServeHTTP(configResp, configReq)
	if configResp.Code != http.StatusOK {
		t.Fatalf("downstream config status=%d body=%s", configResp.Code, configResp.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	adminReq.Header.Set("Authorization", "Bearer "+created.Secret)
	adminResp := httptest.NewRecorder()
	handler.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusUnauthorized {
		t.Fatalf("API key accessed administrator route: status=%d body=%s", adminResp.Code, adminResp.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := httptest.NewRecorder()
	handler.ServeHTTP(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("admin login status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Result().Cookies() {
		if c.Name == "voice_gateway_session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("missing session cookie: %+v", loginResp.Result().Cookies())
	}

	keysPageReq := httptest.NewRequest(http.MethodGet, "/keys", nil)
	keysPageReq.AddCookie(sessionCookie)
	keysPageResp := httptest.NewRecorder()
	handler.ServeHTTP(keysPageResp, keysPageReq)
	if keysPageResp.Code != http.StatusOK {
		t.Fatalf("administrator keys page status=%d body=%s", keysPageResp.Code, keysPageResp.Body.String())
	}
}
