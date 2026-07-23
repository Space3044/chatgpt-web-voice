package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/voice"
)

type downstreamVoiceStub struct {
	createRequest voice.CreateSessionRequest
	releaseOwner  string
	releaseID     string
	createError   error
}

func (s *downstreamVoiceStub) CreateSession(req voice.CreateSessionRequest) (*voice.SessionResult, error) {
	s.createRequest = req
	if s.createError != nil {
		return nil, s.createError
	}
	return &voice.SessionResult{
		AnswerSDP:      "v=0\r\nanswer\r\n",
		VoiceSessionID: "vs_test",
		AccountID:      9,
		Voice:          "cove",
		VoiceMode:      "wingman",
		LanguageCode:   "zh-cn",
	}, nil
}

func (s *downstreamVoiceStub) ReleaseSession(owner, id string) bool {
	s.releaseOwner = owner
	s.releaseID = id
	return owner == "api_key:1" && id == "vs_test"
}

func (s *downstreamVoiceStub) UpdateSessionContext(owner, id string, patch voice.UpstreamContext) (voice.UpstreamContext, error) {
	if owner != "api_key:1" || id != "vs_test" {
		return voice.UpstreamContext{}, &voice.ServiceError{Message: "voice session not found", StatusCode: http.StatusNotFound}
	}
	return patch, nil
}

func (s *downstreamVoiceStub) FetchUpstreamTitle(owner, id, conversationID string) (*voice.UpstreamTitleResult, error) {
	if owner != "api_key:1" || id != "vs_test" {
		return nil, &voice.ServiceError{Message: "voice session not found", StatusCode: http.StatusNotFound}
	}
	title := "Downstream title"
	if conversationID != "" {
		title = "Title for " + conversationID
	}
	return &voice.UpstreamTitleResult{
		VoiceSessionID:         id,
		UpstreamConversationID: orDefaultString(conversationID, "conv-bound"),
		Title:                  title,
		HasTitle:               true,
	}, nil
}

func (s *downstreamVoiceStub) ProbeAccountToken(int64) (*voice.ProbeResult, error) {
	return nil, nil
}

func orDefaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func newDownstreamTestHandler(t *testing.T, voiceService VoiceService) (http.Handler, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keyStore := apikeys.NewStore(db)
	created, err := keyStore.Create("test client")
	if err != nil {
		t.Fatal(err)
	}
	server := New(Dependencies{Voice: voiceService, APIKeys: keyStore})
	mux := http.NewServeMux()
	server.RegisterDownstream(mux)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return auth.NewAPIKeyManager(keyStore, logger).Require(mux), created.Secret
}

func performDownstreamRequest(t *testing.T, handler http.Handler, method, path, secret string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func TestDownstreamConfigAndSessionIsolation(t *testing.T) {
	voiceStub := &downstreamVoiceStub{}
	handler, secret := newDownstreamTestHandler(t, voiceStub)

	unauthorized := performDownstreamRequest(t, handler, http.MethodGet, "/v1/voice/config", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized config status=%d", unauthorized.Code)
	}

	configResp := performDownstreamRequest(t, handler, http.MethodGet, "/v1/voice/config", secret, nil)
	if configResp.Code != http.StatusOK || !strings.Contains(configResp.Body.String(), "oai-events") {
		t.Fatalf("config status=%d body=%s", configResp.Code, configResp.Body.String())
	}
	for _, forbidden := range []string{"access_token", "device_id", "proxy", "account_id"} {
		if strings.Contains(configResp.Body.String(), forbidden) {
			t.Fatalf("config leaked %q: %s", forbidden, configResp.Body.String())
		}
	}

	sessionResp := performDownstreamRequest(t, handler, http.MethodPost, "/v1/voice/sessions", secret, map[string]any{
		"offer_sdp":     "v=0\r\noffer\r\n",
		"voice":         "cove",
		"voice_mode":    "wingman",
		"language_code": "zh-cn",
	})
	if sessionResp.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", sessionResp.Code, sessionResp.Body.String())
	}
	if voiceStub.createRequest.Owner != "api_key:1" {
		t.Fatalf("session owner=%q", voiceStub.createRequest.Owner)
	}
	var sessionBody map[string]any
	if err := json.Unmarshal(sessionResp.Body.Bytes(), &sessionBody); err != nil {
		t.Fatal(err)
	}
	if _, exists := sessionBody["session_id"]; exists {
		t.Fatalf("downstream response exposed internal compatibility field: %s", sessionResp.Body.String())
	}
	// Downstream resume handle is only voice_session_id; pool account stays server-side.
	if _, exists := sessionBody["voice_session_id"]; !exists {
		t.Fatalf("expected voice_session_id in session response: %s", sessionResp.Body.String())
	}
	for _, forbidden := range []string{"account_id", "access_token", "device_id", "proxy", "upstream_conversation_id", "upstream_voice_session_id"} {
		if _, exists := sessionBody[forbidden]; exists {
			t.Fatalf("downstream response exposed %q: %s", forbidden, sessionResp.Body.String())
		}
	}

	contextResp := performDownstreamRequest(t, handler, http.MethodPost, "/v1/voice/sessions/vs_test/context", secret, map[string]any{
		"upstream_conversation_id":   "conv-1",
		"upstream_parent_message_id": "msg-1",
	})
	if contextResp.Code != http.StatusOK || !strings.Contains(contextResp.Body.String(), "conv-1") {
		t.Fatalf("context status=%d body=%s", contextResp.Code, contextResp.Body.String())
	}

	titleResp := performDownstreamRequest(t, handler, http.MethodGet, "/v1/voice/sessions/vs_test/title?upstream_conversation_id=conv-1", secret, nil)
	if titleResp.Code != http.StatusOK || !strings.Contains(titleResp.Body.String(), "Title for conv-1") {
		t.Fatalf("title status=%d body=%s", titleResp.Code, titleResp.Body.String())
	}
	for _, forbidden := range []string{"access_token", "token-secret", "account_id"} {
		if strings.Contains(titleResp.Body.String(), forbidden) {
			t.Fatalf("title response leaked %q: %s", forbidden, titleResp.Body.String())
		}
	}

	releaseResp := performDownstreamRequest(t, handler, http.MethodDelete, "/v1/voice/sessions/vs_test", secret, nil)
	if releaseResp.Code != http.StatusOK || voiceStub.releaseOwner != "api_key:1" || voiceStub.releaseID != "vs_test" {
		t.Fatalf("release status=%d owner=%q id=%q", releaseResp.Code, voiceStub.releaseOwner, voiceStub.releaseID)
	}
}

func TestDownstreamHidesUpstreamErrorDetail(t *testing.T) {
	voiceStub := &downstreamVoiceStub{createError: &voice.ServiceError{
		Message:    "realtime/wm failed status=403",
		StatusCode: http.StatusBadGateway,
		Detail:     "access_token=secret-account-token",
	}}
	handler, secret := newDownstreamTestHandler(t, voiceStub)
	resp := performDownstreamRequest(t, handler, http.MethodPost, "/v1/voice/sessions", secret, map[string]any{
		"offer_sdp": "v=0\r\noffer\r\n",
	})
	if resp.Code != http.StatusBadGateway || !strings.Contains(resp.Body.String(), "upstream_unavailable") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "secret-account-token") || strings.Contains(resp.Body.String(), "realtime/wm") {
		t.Fatalf("downstream error leaked upstream detail: %s", resp.Body.String())
	}
}
