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
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
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

func TestConfigAndSessionOptionValidation(t *testing.T) {
	cfg := Config()
	if cfg.Version != "v1" || cfg.Defaults.Voice != "cove" || len(cfg.Voices) != 9 || len(cfg.Languages) < 60 {
		t.Fatalf("unexpected public config: %+v", cfg)
	}
	if cfg.WebRTC.DataChannel.Label != "oai-events" || !cfg.WebRTC.DataChannel.Negotiated || cfg.WebRTC.DataChannel.ID != 0 {
		t.Fatalf("unexpected WebRTC config: %+v", cfg.WebRTC)
	}

	options, err := normalizeSessionOptions("Arbor", "", "ZH-CN")
	if err != nil {
		t.Fatal(err)
	}
	if options.Voice != "fathom" || options.VoiceMode != "wingman" || options.LanguageCode != "zh-cn" {
		t.Fatalf("unexpected normalized options: %+v", options)
	}
	for _, tc := range []struct {
		voice    string
		mode     string
		language string
	}{
		{voice: "unknown"},
		{mode: "unknown"},
		{language: "xx-invalid"},
	} {
		if _, err := normalizeSessionOptions(tc.voice, tc.mode, tc.language); err == nil {
			t.Fatalf("expected validation error for %+v", tc)
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

func TestVoiceSessionOwnership(t *testing.T) {
	svc := New(config.Config{SessionTTLSeconds: 3600}, nil, nil)
	account := accounts.Account{ID: 7, DeviceID: "device", Proxy: "http://proxy.example:8080"}
	id := svc.bindVoiceSession("api_key:1", "", "token", account)
	if id == "" {
		t.Fatal("missing voice session ID")
	}
	owned, forbidden := svc.boundVoiceSession("api_key:1", id)
	if forbidden || owned == nil || owned.AccountID != account.ID {
		t.Fatalf("unexpected owned binding: binding=%+v forbidden=%v", owned, forbidden)
	}
	if binding, forbidden := svc.boundVoiceSession("api_key:2", id); binding != nil || !forbidden {
		t.Fatalf("cross-key binding was not rejected: binding=%+v forbidden=%v", binding, forbidden)
	}
	if svc.ReleaseSession("api_key:2", id) {
		t.Fatal("another key released the voice session")
	}
	if !svc.ReleaseSession("api_key:1", id) {
		t.Fatal("owner could not release the voice session")
	}
}

func TestCreateSessionRejectsUnknownCallerSuppliedSessionID(t *testing.T) {
	svc := New(config.Config{}, nil, nil)
	_, err := svc.CreateSession(CreateSessionRequest{
		Owner:          "api_key:1",
		OfferSDP:       "v=0\r\noffer\r\n",
		VoiceSessionID: "vs_missing",
	})
	serviceErr, ok := err.(*ServiceError)
	if !ok || serviceErr.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected error: %T %v", err, err)
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

func TestProbeNetworkDetailSuggestsProcessProxyOnTimeout(t *testing.T) {
	err := fmt.Errorf(`Get "https://chatgpt.com/backend-api/settings/user": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)
	got := probeNetworkDetail(err, "")
	if !strings.Contains(got, "HTTP_PROXY") || !strings.Contains(got, "NO_PROXY") {
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
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	pool.WithBox(box)
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func testJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}
