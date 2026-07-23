package voice

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
)

func TestNormalizeVoice(t *testing.T) {
	cases := map[string]string{
		"":        "cove",
		"COVE":    "cove",
		"arbor":   "fathom",
		"sol":     "glimmer",
		"spruce":  "orbit",
		"unknown": "cove",
		"ember":   "ember",
	}
	for in, want := range cases {
		if got := normalizeVoice(in); got != want {
			t.Errorf("normalizeVoice(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNormalizeSDP(t *testing.T) {
	_, err := normalizeSDP("not-sdp")
	if err == nil {
		t.Fatal("expected error for invalid sdp")
	}
	out, err := normalizeSDP("v=0\no=- 0 0 IN IP4 0.0.0.0\n")
	if err != nil {
		t.Fatal(err)
	}
	if out != "v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\n" {
		t.Fatalf("unexpected sdp: %q", out)
	}
}

func TestProbeAccountTokenAlive(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Fatal("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"beta_settings":{}}`))
	}))
	t.Cleanup(upstream.Close)

	pool := testPool(t)
	token := testJWT(map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	account, err := pool.Create(accounts.Account{AccessToken: token, Status: "正常", DeviceID: "device-1"})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(config.Config{}, pool, nil)
	svc.settingsUserURL = upstream.URL

	result, err := svc.ProbeAccountToken(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Alive || result.Status != "alive" || result.StatusCode != http.StatusOK || result.MarkedInvalid {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	stored, err := pool.Get(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Disabled {
		t.Fatal("alive probe should not disable account")
	}
}

func TestProbeAccountTokenUnauthorizedMarksInvalid(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"message":"Unauthorized"}}`))
	}))
	t.Cleanup(upstream.Close)

	pool := testPool(t)
	token := testJWT(map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	account, err := pool.Create(accounts.Account{AccessToken: token, Status: "正常"})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(config.Config{}, pool, nil)
	svc.settingsUserURL = upstream.URL

	result, err := svc.ProbeAccountToken(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Alive || result.Status != "unauthorized" || !result.MarkedInvalid {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	stored, err := pool.Get(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Disabled || stored.Status != "禁用" {
		t.Fatalf("expected disabled account: %+v", stored)
	}
}

func TestProbeAccountTokenHTMLChallengeIsUnknown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Just a moment...</body></html>`))
	}))
	t.Cleanup(upstream.Close)

	pool := testPool(t)
	token := testJWT(map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	account, err := pool.Create(accounts.Account{AccessToken: token, Status: "正常"})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(config.Config{}, pool, nil)
	svc.settingsUserURL = upstream.URL

	result, err := svc.ProbeAccountToken(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Alive || result.Status != "unknown" || result.MarkedInvalid || result.ContentKind != "html" {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	stored, err := pool.Get(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Disabled {
		t.Fatal("html challenge must not disable account")
	}
}

func TestClassifyProbeBody(t *testing.T) {
	if got := classifyProbeBody("application/json", `{"ok":true}`); got != "json" {
		t.Fatalf("got %q", got)
	}
	if got := classifyProbeBody("text/html", `<html>challenge</html>`); got != "html" {
		t.Fatalf("got %q", got)
	}
}

func TestProbeNetworkDetailSuggestsProxyOnDirectTimeout(t *testing.T) {
	err := fmt.Errorf(`Get "https://chatgpt.com/backend-api/settings/user": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)
	got := probeNetworkDetail(err, "")
	if !strings.Contains(got, "direct connection") || !strings.Contains(got, "proxy") {
		t.Fatalf("unexpected detail: %q", got)
	}
	got = probeNetworkDetail(err, "http://127.0.0.1:7890")
	if !strings.Contains(got, "account proxy") {
		t.Fatalf("unexpected proxy detail: %q", got)
	}
}

func testPool(t *testing.T) *accounts.Pool {
	t.Helper()
	pool, err := accounts.NewPool(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func testJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}
