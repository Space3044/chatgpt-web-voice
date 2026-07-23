package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/voice"
)

func testAccountBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 11)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func newAPITestServer(t *testing.T) (*accounts.Pool, *http.ServeMux) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pool := accounts.NewPoolFromDB(db).WithBox(testAccountBox(t))
	mux := http.NewServeMux()
	New(Dependencies{
		Accounts:      pool,
		Conversations: conversations.NewStore(db),
	}).Register(mux)
	return pool, mux
}

func performJSONRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func TestAccountManagementAPI(t *testing.T) {
	pool, mux := newAPITestServer(t)
	createResp := performJSONRequest(t, mux, http.MethodPost, "/api/accounts", map[string]any{
		"email":        "admin@example.com",
		"access_token": "secret-access-token-123456",
		"device_id":    "device-1",
		"proxy":        "http://user:password@127.0.0.1:8080",
		"status":       "正常",
		"disabled":     false,
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResp.Code, createResp.Body.String())
	}
	if strings.Contains(createResp.Body.String(), "secret-access-token-123456") ||
		strings.Contains(createResp.Body.String(), "password") {
		t.Fatalf("create response leaked secrets: %s", createResp.Body.String())
	}
	var createBody struct {
		Account struct {
			ID int64 `json:"id"`
		} `json:"account"`
	}
	if err := json.Unmarshal(createResp.Body.Bytes(), &createBody); err != nil {
		t.Fatal(err)
	}
	if createBody.Account.ID == 0 {
		t.Fatal("missing created account ID")
	}

	listResp := performJSONRequest(t, mux, http.MethodGet, "/api/accounts", nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResp.Code, listResp.Body.String())
	}
	if strings.Contains(listResp.Body.String(), "secret-access-token-123456") ||
		strings.Contains(listResp.Body.String(), "password") {
		t.Fatalf("list response leaked secrets: %s", listResp.Body.String())
	}

	updateResp := performJSONRequest(t, mux, http.MethodPut, "/api/accounts/"+jsonNumber(createBody.Account.ID), map[string]any{
		"email":     "updated@example.com",
		"device_id": "device-2",
		"proxy":     "",
		"status":    "禁用",
		"disabled":  true,
	})
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateResp.Code, updateResp.Body.String())
	}
	stored, err := pool.Get(createBody.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "secret-access-token-123456" || stored.Proxy != "" || !stored.Disabled {
		t.Fatalf("unexpected stored update: %+v", stored)
	}

	deleteResp := performJSONRequest(t, mux, http.MethodDelete, "/api/accounts/"+jsonNumber(createBody.Account.ID), nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	listResp = performJSONRequest(t, mux, http.MethodGet, "/api/accounts", nil)
	if !strings.Contains(listResp.Body.String(), `"total":0`) {
		t.Fatalf("expected empty pool after delete: %s", listResp.Body.String())
	}
}

func TestAccountManagementAPIRejectsDuplicateToken(t *testing.T) {
	_, mux := newAPITestServer(t)
	payload := map[string]any{"access_token": "same-secret-token", "status": "正常"}
	first := performJSONRequest(t, mux, http.MethodPost, "/api/accounts", payload)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create failed: %d %s", first.Code, first.Body.String())
	}
	second := performJSONRequest(t, mux, http.MethodPost, "/api/accounts", payload)
	if second.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got %d %s", second.Code, second.Body.String())
	}
}

func TestAccountListIncludesJWTExpiry(t *testing.T) {
	_, mux := newAPITestServer(t)
	exp := time.Now().Add(3 * time.Hour).Unix()
	token := testJWT(map[string]any{"exp": exp, "sub": "user-1"})
	createResp := performJSONRequest(t, mux, http.MethodPost, "/api/accounts", map[string]any{
		"access_token": token,
		"status":       "正常",
	})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResp.Code, createResp.Body.String())
	}
	listResp := performJSONRequest(t, mux, http.MethodGet, "/api/accounts", nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResp.Code, listResp.Body.String())
	}
	var body struct {
		Accounts []struct {
			TokenHasExp      bool  `json:"token_has_exp"`
			TokenExp         int64 `json:"token_exp"`
			ExpiresInSeconds int64 `json:"expires_in_seconds"`
			TokenExpired     bool  `json:"token_expired"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(body.Accounts))
	}
	account := body.Accounts[0]
	if !account.TokenHasExp || account.TokenExp != exp || account.TokenExpired {
		t.Fatalf("unexpected expiry fields: %+v", account)
	}
	if account.ExpiresInSeconds < 2*3600 || account.ExpiresInSeconds > 4*3600 {
		t.Fatalf("expires_in_seconds out of range: %d", account.ExpiresInSeconds)
	}
}

func TestAccountCheckRouteRequiresExistingAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pool := accounts.NewPoolFromDB(db).WithBox(testAccountBox(t))
	svc := voice.New(config.Config{}, pool, nil)
	mux := http.NewServeMux()
	New(Dependencies{
		Voice:         svc,
		Accounts:      pool,
		Conversations: conversations.NewStore(db),
	}).Register(mux)
	missing := performJSONRequest(t, mux, http.MethodPost, "/api/accounts/999/check", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing account, got %d %s", missing.Code, missing.Body.String())
	}
}

func testJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}

func jsonNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
