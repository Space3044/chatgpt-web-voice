package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/callsessions"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/httpclient"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/tokenutil"
)

const (
	wmURL           = "https://chatgpt.com/realtime/wm?dcid=0"
	settingsUserURL = "https://chatgpt.com/backend-api/settings/user"
	// conversationURLPrefix is used to load chatgpt.com conversation metadata
	// (including the generated title) with the sticky account token.
	conversationURLPrefix = "https://chatgpt.com/backend-api/conversation/"
	// Probe is a quick liveness check. Keep it short so the accounts panel
	// does not sit on "checking…" while an unreachable upstream path dials chatgpt.com.
	probeTimeout     = 12 * time.Second
	probeDialTimeout = 8 * time.Second
	probeTLSTimeout  = 8 * time.Second
	probeBodyLimit   = 64 << 10
	titleFetchTimeout = 15 * time.Second
	titleBodyLimit    = 2 << 20
)

// AccountRepository is the account-pool surface required by the voice gateway.
// Concrete type is *accounts.Pool; the interface keeps this package free of
// storage-construction details.
type AccountRepository interface {
	Pick(preferredToken string, excluded map[string]struct{}) (string, accounts.Account, error)
	PickByID(id int64, excluded map[string]struct{}) (string, accounts.Account, error)
	MarkInvalid(token string) error
	Get(id int64) (accounts.Account, error)
}

// CallSessionStore persists gateway voice-session metadata (no chat content)
// for admin visibility and sticky account resume after restarts.
type CallSessionStore interface {
	Upsert(item callsessions.Session) error
	UpdateUpstream(owner, voiceSessionID string, accountID int64, upstreamConversationID, upstreamParentMessageID, upstreamVoiceSessionID string) (callsessions.Session, error)
	MarkReleased(owner, voiceSessionID string) error
	Get(owner, voiceSessionID string) (callsessions.Session, error)
}

// ServiceError is a typed gateway error with HTTP status.
type ServiceError struct {
	Message    string
	StatusCode int
	Detail     any
}

func (e *ServiceError) Error() string { return e.Message }

// UpstreamContext is the chatgpt.com conversation continuity state learned from
// DataChannel events or supplied by a reconnecting client.
type UpstreamContext struct {
	ConversationID          string `json:"upstream_conversation_id,omitempty"`
	ParentMessageID         string `json:"upstream_parent_message_id,omitempty"`
	UpstreamVoiceSessionID  string `json:"upstream_voice_session_id,omitempty"`
}

type sessionBinding struct {
	Owner                  string
	AccountID              int64
	AccessToken            string
	DeviceID               string
	Proxy                  string
	UpstreamVoiceSessionID string
	ConversationID         string
	ParentMessageID        string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Service is the ChatGPT web voice gateway.
type Service struct {
	cfg         config.Config
	httpOptions httpclient.Options
	pool        AccountRepository
	records     CallSessionStore
	logger      *slog.Logger

	// settingsUserURL overrides the account probe endpoint in tests.
	settingsUserURL string
	// conversationURLPrefix overrides the conversation metadata endpoint in tests.
	conversationURLPrefix string

	mu       sync.Mutex
	bindings map[string]*sessionBinding
}

// New creates a voice service.
func New(cfg config.Config, pool AccountRepository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:                   cfg,
		httpOptions:           httpclient.FromConfig(cfg),
		pool:                  pool,
		logger:                logger,
		settingsUserURL:       settingsUserURL,
		conversationURLPrefix: conversationURLPrefix,
		bindings:              make(map[string]*sessionBinding),
	}
}

// WithCallSessions attaches durable call-session metadata storage.
func (s *Service) WithCallSessions(records CallSessionStore) *Service {
	if s != nil {
		s.records = records
	}
	return s
}

// SessionResult is returned by CreateSession.
type SessionResult struct {
	AnswerSDP               string `json:"answer_sdp"`
	Voice                   string `json:"voice"`
	VoiceMode               string `json:"voice_mode"`
	LanguageCode            string `json:"language_code"`
	SessionID               string `json:"session_id"`
	VoiceSessionID          string `json:"voice_session_id"`
	AccountID               int64  `json:"account_id,omitempty"`
	UpstreamVoiceSessionID  string `json:"upstream_voice_session_id,omitempty"`
	UpstreamConversationID  string `json:"upstream_conversation_id,omitempty"`
	UpstreamParentMessageID string `json:"upstream_parent_message_id,omitempty"`
}

func normalizeVoice(voice string) string {
	clean := strings.ToLower(strings.TrimSpace(voice))
	if clean == "" {
		return defaultVoice
	}
	if alias, ok := realtimeVoiceAliases[clean]; ok {
		clean = alias
	}
	if _, ok := allowedRealtimeVoices[clean]; ok {
		return clean
	}
	return defaultVoice
}

func newVoiceSessionID() string {
	return "vs_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

func (s *Service) cleanupBindingsLocked(now time.Time) {
	ttl := time.Duration(s.cfg.SessionTTLSeconds) * time.Second
	type expiredBinding struct {
		owner     string
		sessionID string
	}
	var expired []expiredBinding
	for id, item := range s.bindings {
		base := item.UpdatedAt
		if base.IsZero() {
			base = item.CreatedAt
		}
		if now.Sub(base) > ttl {
			expired = append(expired, expiredBinding{owner: item.Owner, sessionID: id})
			delete(s.bindings, id)
		}
	}
	// Persist expiry so admin call_sessions does not remain "active" after the
	// in-memory binding is reaped by TTL.
	if s.records == nil {
		return
	}
	for _, item := range expired {
		if err := s.records.MarkReleased(item.owner, item.sessionID); err != nil {
			s.logger.Warn("call_session_ttl_release_failed", "voice_session_id", item.sessionID, "error", err)
		}
	}
}

func (s *Service) bindVoiceSession(owner, sessionID, token string, account accounts.Account, upstream UpstreamContext) string {
	owner = normalizeSessionOwner(owner)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = newVoiceSessionID()
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupBindingsLocked(now)

	// Preserve continuity fields across SDP re-exchanges unless the caller
	// supplies newer values.
	prev := s.bindings[sessionID]
	createdAt := now
	merged := upstream
	if prev != nil && prev.Owner == owner {
		createdAt = prev.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		merged = mergeUpstreamContext(prev.upstreamContext(), upstream)
	}
	if strings.TrimSpace(merged.UpstreamVoiceSessionID) == "" {
		merged.UpstreamVoiceSessionID = newUpstreamVoiceSessionID()
	}

	s.bindings[sessionID] = &sessionBinding{
		Owner:                  owner,
		AccountID:              account.ID,
		AccessToken:            token,
		DeviceID:               account.DeviceID,
		Proxy:                  account.Proxy,
		UpstreamVoiceSessionID: merged.UpstreamVoiceSessionID,
		ConversationID:         merged.ConversationID,
		ParentMessageID:        merged.ParentMessageID,
		CreatedAt:              createdAt,
		UpdatedAt:              now,
	}
	return sessionID
}

func (b *sessionBinding) upstreamContext() UpstreamContext {
	if b == nil {
		return UpstreamContext{}
	}
	return UpstreamContext{
		ConversationID:         b.ConversationID,
		ParentMessageID:        b.ParentMessageID,
		UpstreamVoiceSessionID: b.UpstreamVoiceSessionID,
	}
}

func mergeUpstreamContext(base, patch UpstreamContext) UpstreamContext {
	out := base
	if v := strings.TrimSpace(patch.ConversationID); v != "" {
		out.ConversationID = v
	}
	if v := strings.TrimSpace(patch.ParentMessageID); v != "" {
		out.ParentMessageID = v
	}
	if v := strings.TrimSpace(patch.UpstreamVoiceSessionID); v != "" {
		out.UpstreamVoiceSessionID = v
	}
	return out
}

func newUpstreamVoiceSessionID() string {
	return strings.ToUpper(uuid.New().String())
}

// UpdateSessionContext records upstream conversation identifiers learned from
// the DataChannel after the media plane is connected. Only the binding owner
// may update the context. When the in-memory binding was released after hangup,
// durable call_sessions rows still accept continuity updates for the same owner.
func (s *Service) UpdateSessionContext(owner, voiceSessionID string, patch UpstreamContext) (UpstreamContext, error) {
	owner = normalizeSessionOwner(owner)
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return UpstreamContext{}, &ServiceError{Message: "voice session id is required", StatusCode: 400}
	}
	now := time.Now()
	s.mu.Lock()
	s.cleanupBindingsLocked(now)
	item := s.bindings[sessionID]
	if item != nil && item.Owner != owner {
		s.mu.Unlock()
		return UpstreamContext{}, &ServiceError{Message: "voice session does not belong to caller", StatusCode: 403}
	}
	if item != nil {
		merged := mergeUpstreamContext(item.upstreamContext(), patch)
		item.ConversationID = merged.ConversationID
		item.ParentMessageID = merged.ParentMessageID
		if strings.TrimSpace(merged.UpstreamVoiceSessionID) != "" {
			item.UpstreamVoiceSessionID = merged.UpstreamVoiceSessionID
		}
		item.UpdatedAt = now
		out := item.upstreamContext()
		accountID := item.AccountID
		s.mu.Unlock()
		if s.records != nil {
			if _, err := s.records.UpdateUpstream(owner, sessionID, accountID, out.ConversationID, out.ParentMessageID, out.UpstreamVoiceSessionID); err != nil {
				s.logger.Warn("call_session_upstream_persist_failed", "voice_session_id", sessionID, "error", err)
			}
		}
		return out, nil
	}
	s.mu.Unlock()

	// Memory binding gone (typical after hangup): update durable metadata only.
	if s.records == nil {
		return UpstreamContext{}, &ServiceError{Message: "voice session not found", StatusCode: 404}
	}
	row, err := s.records.Get(owner, sessionID)
	if err != nil {
		if errors.Is(err, callsessions.ErrNotFound) {
			return UpstreamContext{}, &ServiceError{Message: "voice session not found", StatusCode: 404}
		}
		return UpstreamContext{}, &ServiceError{Message: "voice session lookup failed", StatusCode: 500}
	}
	updated, err := s.records.UpdateUpstream(
		owner,
		sessionID,
		row.AccountID,
		strings.TrimSpace(patch.ConversationID),
		strings.TrimSpace(patch.ParentMessageID),
		strings.TrimSpace(patch.UpstreamVoiceSessionID),
	)
	if err != nil {
		if errors.Is(err, callsessions.ErrNotFound) {
			return UpstreamContext{}, &ServiceError{Message: "voice session not found", StatusCode: 404}
		}
		return UpstreamContext{}, &ServiceError{Message: "voice session update failed", StatusCode: 500}
	}
	return UpstreamContext{
		ConversationID:         updated.UpstreamConversationID,
		ParentMessageID:        updated.UpstreamParentMessageID,
		UpstreamVoiceSessionID: updated.UpstreamVoiceSessionID,
	}, nil
}

// SessionContext returns the stored upstream continuity fields for an owned binding.
func (s *Service) SessionContext(owner, voiceSessionID string) (UpstreamContext, error) {
	binding, ownedByOther := s.boundVoiceSession(owner, voiceSessionID)
	if ownedByOther {
		return UpstreamContext{}, &ServiceError{Message: "voice session does not belong to caller", StatusCode: 403}
	}
	if binding == nil {
		return UpstreamContext{}, &ServiceError{Message: "voice session not found", StatusCode: 404}
	}
	return binding.upstreamContext(), nil
}

// UpstreamTitleResult is a redacted conversation title fetched from chatgpt.com.
// It never includes the access token or full conversation mapping.
type UpstreamTitleResult struct {
	VoiceSessionID         string `json:"voice_session_id"`
	UpstreamConversationID string `json:"upstream_conversation_id"`
	Title                  string `json:"title"`
	HasTitle               bool   `json:"has_title"`
	StatusCode             int    `json:"status_code,omitempty"`
}

// FetchUpstreamTitle loads the chatgpt.com conversation title using the sticky
// account bound to voiceSessionID. conversationID may be empty when the binding
// or durable call_sessions row already stores UpstreamConversationID.
func (s *Service) FetchUpstreamTitle(owner, voiceSessionID, conversationID string) (*UpstreamTitleResult, error) {
	owner = normalizeSessionOwner(owner)
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return nil, &ServiceError{Message: "voice session id is required", StatusCode: 400}
	}
	binding, ownedByOther := s.boundVoiceSession(owner, sessionID)
	if ownedByOther {
		return nil, &ServiceError{Message: "voice session does not belong to caller", StatusCode: 403}
	}

	// Rehydrate sticky account from durable metadata when memory was released.
	var accessToken, device, proxy string
	var accountID int64
	var storedConversationID string
	if binding != nil {
		accessToken = binding.AccessToken
		device = binding.DeviceID
		proxy = binding.Proxy
		accountID = binding.AccountID
		storedConversationID = binding.ConversationID
	} else if s.records != nil {
		row, err := s.records.Get(owner, sessionID)
		if err != nil {
			if errors.Is(err, callsessions.ErrNotFound) {
				return nil, &ServiceError{Message: "voice session not found", StatusCode: 404}
			}
			return nil, &ServiceError{Message: "voice session lookup failed", StatusCode: 500}
		}
		if row.AccountID <= 0 {
			return nil, &ServiceError{Message: "voice session has no sticky account", StatusCode: 404}
		}
		token, account, err := s.pool.PickByID(row.AccountID, nil)
		if err != nil {
			if ae, ok := err.(*accounts.Error); ok {
				return nil, &ServiceError{Message: ae.Message, StatusCode: 503}
			}
			return nil, &ServiceError{Message: err.Error(), StatusCode: 503}
		}
		accessToken = token
		device = account.DeviceID
		proxy = account.Proxy
		accountID = account.ID
		storedConversationID = row.UpstreamConversationID
	} else {
		return nil, &ServiceError{Message: "voice session not found", StatusCode: 404}
	}

	upstreamID := strings.TrimSpace(conversationID)
	if upstreamID == "" {
		upstreamID = strings.TrimSpace(storedConversationID)
	}
	if upstreamID == "" {
		return nil, &ServiceError{Message: "upstream conversation id is required", StatusCode: 400}
	}
	if !validUpstreamConversationID(upstreamID) {
		return nil, &ServiceError{Message: "upstream conversation id is invalid", StatusCode: 400}
	}

	// Keep continuity in sync when the client supplies a newer id.
	if upstreamID != strings.TrimSpace(storedConversationID) {
		if _, err := s.UpdateSessionContext(owner, sessionID, UpstreamContext{ConversationID: upstreamID}); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(device) == "" {
		device = uuid.New().String()
	}
	status, contentType, body, err := s.getConversationOnce(accessToken, device, proxy, upstreamID)
	if err != nil {
		s.logger.Warn("upstream_title_fetch_failed", "voice_session_id", sessionID, "error", err)
		return nil, &ServiceError{
			Message:    "upstream conversation request failed",
			StatusCode: 502,
			Detail:     truncate(err.Error(), 300),
		}
	}

	result := &UpstreamTitleResult{
		VoiceSessionID:         sessionID,
		UpstreamConversationID: upstreamID,
		StatusCode:             status,
	}
	switch {
	case status == http.StatusUnauthorized:
		if markErr := s.pool.MarkInvalid(accessToken); markErr != nil {
			s.logger.Error("account_mark_invalid_failed", "account_id", accountID, "error", markErr)
		}
		return nil, &ServiceError{Message: "account token invalid", StatusCode: 401, Detail: truncate(body, 300)}
	case status == http.StatusNotFound:
		return nil, &ServiceError{Message: "upstream conversation not found", StatusCode: 404}
	case status == http.StatusTooManyRequests:
		return nil, &ServiceError{Message: "upstream conversation rate limited", StatusCode: 429}
	case status != http.StatusOK:
		kind := classifyProbeBody(contentType, body)
		detail := truncate(body, 300)
		if kind == "html" {
			detail = "upstream returned HTML challenge or block page"
		}
		return nil, &ServiceError{
			Message:    fmt.Sprintf("upstream conversation failed status=%d", status),
			StatusCode: 502,
			Detail:     detail,
		}
	}

	title, ok := extractConversationTitle(body)
	if !ok {
		// Conversation exists but title is not generated yet (common early in a call).
		result.HasTitle = false
		result.Title = ""
		s.logger.Info("upstream_title_empty", "voice_session_id", sessionID, "upstream_conversation_id", upstreamID)
		return result, nil
	}
	result.Title = title
	result.HasTitle = true
	s.logger.Info("upstream_title_fetched", "voice_session_id", sessionID, "upstream_conversation_id", upstreamID, "title_len", len(title))
	return result, nil
}

func (s *Service) getConversationOnce(token, device, proxy, conversationID string) (status int, contentType, text string, err error) {
	client := httpclient.New(s.httpOptions, proxy)
	client.Timeout = titleFetchTimeout
	prefix := s.conversationURLPrefix
	if prefix == "" {
		prefix = conversationURLPrefix
	}
	endpoint := strings.TrimRight(prefix, "/") + "/" + conversationID
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header = s.authHeaders(token, device, map[string]string{
		"accept": "application/json, text/plain, */*",
	})
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, titleBodyLimit))
	return resp.StatusCode, resp.Header.Get("content-type"), string(raw), nil
}

func validUpstreamConversationID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 160 {
		return false
	}
	// Conversation ids are opaque strings from chatgpt.com; reject path traversal
	// and whitespace so they can be safely appended to the URL path.
	if strings.ContainsAny(id, "/\\?# \t\r\n") {
		return false
	}
	return true
}

// extractConversationTitle pulls the user-visible title from a conversation JSON
// payload. Empty / placeholder titles are treated as not ready.
func extractConversationTitle(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	title := strings.TrimSpace(fmt.Sprint(payload["title"]))
	if title == "" || title == "<nil>" {
		return "", false
	}
	if isPlaceholderConversationTitle(title) {
		return "", false
	}
	// Cap length for UI / local storage consumers.
	runes := []rune(title)
	if len(runes) > 120 {
		title = string(runes[:120])
	}
	return title, true
}

func isPlaceholderConversationTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", "new chat", "new conversation", "untitled", "untitled conversation",
		"新聊天", "新对话", "新会话", "未命名会话", "未命名对话":
		return true
	default:
		return false
	}
}

// ReleaseSession unbinds a voice_session_id.
func (s *Service) ReleaseSession(owner, voiceSessionID string) bool {
	owner = normalizeSessionOwner(owner)
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	item, ok := s.bindings[sessionID]
	released := false
	if ok && item.Owner == owner {
		delete(s.bindings, sessionID)
		released = true
	}
	s.mu.Unlock()
	if s.records != nil {
		if err := s.records.MarkReleased(owner, sessionID); err != nil {
			s.logger.Warn("call_session_release_persist_failed", "voice_session_id", sessionID, "error", err)
		}
	}
	return released
}

func (s *Service) boundVoiceSession(owner, voiceSessionID string) (binding *sessionBinding, ownedByOther bool) {
	owner = normalizeSessionOwner(owner)
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return nil, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupBindingsLocked(now)
	item := s.bindings[sessionID]
	if item == nil {
		return nil, false
	}
	if item.Owner != owner {
		return nil, true
	}
	item.UpdatedAt = now
	cp := *item
	return &cp, false
}

func normalizeSessionOwner(owner string) string {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "internal"
	}
	return owner
}

func encodeMultipart(fields [][2]string) (body []byte, contentType string) {
	boundary := "----WebKitFormBoundary" + strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	var buf bytes.Buffer
	for _, kv := range fields {
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Disposition: form-data; name=\"%s\"\r\n\r\n", kv[0])
		buf.WriteString(kv[1])
		buf.WriteString("\r\n")
	}
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes(), "multipart/form-data; boundary=" + boundary
}

func normalizeSDP(offerSDP string) (string, error) {
	text := strings.TrimSpace(offerSDP)
	if !strings.HasPrefix(text, "v=0") {
		return "", &ServiceError{Message: "offer_sdp invalid; must be WebRTC offer SDP text", StatusCode: 400}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	if !strings.HasSuffix(text, "\r\n") {
		text += "\r\n"
	}
	return text, nil
}

func buildSessionJSON(voice, voiceMode, languageCode string, upstream UpstreamContext) string {
	sid := strings.TrimSpace(upstream.UpstreamVoiceSessionID)
	if sid == "" {
		sid = newUpstreamVoiceSessionID()
	}
	// ChatGPT web uses an uppercase UUID for voice_session_id in the wm form.
	sid = strings.ToUpper(sid)
	payload := map[string]any{
		"backend_reasoning_effort":      "instant",
		"language_code":                 languageCode,
		"requested_default_model":       "",
		"voice":                         normalizeVoice(voice),
		"voice_session_id":              sid,
		"voice_status_request_id":       sid,
		"timezone_offset_min":           -480,
		"timezone":                      "Etc/GMT-8",
		"voice_mode":                    voiceMode,
		"model_slug":                    "",
		"model_slug_advanced":           "",
		"client_tools":                  []any{},
		"history_and_training_disabled": false,
		"conversation_mode":             map[string]any{"kind": "primary_assistant"},
		"enable_message_streaming":      true,
	}
	// When present, these fields ask the upstream voice path to continue the
	// existing chatgpt.com conversation instead of starting a blank thread.
	// Field names follow the web conversation protocol; if upstream ignores an
	// unknown field the call still works as a fresh session.
	if conversationID := strings.TrimSpace(upstream.ConversationID); conversationID != "" {
		payload["conversation_id"] = conversationID
	}
	if parentID := strings.TrimSpace(upstream.ParentMessageID); parentID != "" {
		payload["parent_message_id"] = parentID
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func (s *Service) authHeaders(token, deviceID string, extra map[string]string) http.Header {
	h := http.Header{}
	h.Set("accept", "*/*")
	h.Set("origin", "https://chatgpt.com")
	h.Set("referer", "https://chatgpt.com/")
	h.Set("user-agent", s.cfg.DefaultUA)
	h.Set("oai-device-id", deviceID)
	h.Set("oai-language", "zh-CN")
	h.Set("oai-client-version", s.cfg.ClientVersion)
	h.Set("oai-client-build-number", s.cfg.ClientBuildNumber)
	h.Set("authorization", "Bearer "+token)
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

func (s *Service) postWMOnce(token, offerSDP, sessionJSON, device, proxy string) (status int, contentType, text string, err error) {
	client := httpclient.New(s.httpOptions, proxy)
	body, ct := encodeMultipart([][2]string{
		{"sdp", offerSDP},
		{"session", sessionJSON},
	})
	req, err := http.NewRequest(http.MethodPost, wmURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", "", err
	}
	req.Header = s.authHeaders(token, device, map[string]string{"content-type": ct})

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return resp.StatusCode, resp.Header.Get("content-type"), string(raw), nil
}

// ProbeResult is the outcome of a lightweight account liveness check.
type ProbeResult struct {
	AccountID        int64  `json:"account_id"`
	Alive            bool   `json:"alive"`
	Status           string `json:"status"`
	StatusCode       int    `json:"status_code,omitempty"`
	Detail           string `json:"detail,omitempty"`
	ContentKind      string `json:"content_kind,omitempty"`
	MarkedInvalid    bool   `json:"marked_invalid,omitempty"`
	TokenHasExp      bool   `json:"token_has_exp"`
	TokenExp         int64  `json:"token_exp,omitempty"`
	ExpiresInSeconds *int64 `json:"expires_in_seconds,omitempty"`
	TokenExpired     bool   `json:"token_expired,omitempty"`
}

// ProbeAccountToken GETs backend-api/settings/user to check whether the stored
// access token is still accepted. A JSON 401 marks the account invalid; Cloudflare
// HTML / network errors are reported as unknown and do not disable the account.
func (s *Service) ProbeAccountToken(accountID int64) (*ProbeResult, error) {
	account, err := s.pool.Get(accountID)
	if err != nil {
		return nil, err
	}
	result := &ProbeResult{
		AccountID: account.ID,
		Status:    "unknown",
	}
	if exp, expErr := tokenutil.ParseAccessTokenExpiry(account.AccessToken); expErr == nil {
		result.TokenHasExp = exp.HasExp
		result.TokenExp = exp.Exp
		result.TokenExpired = exp.Expired
		if exp.HasExp {
			seconds := exp.ExpiresInSeconds
			result.ExpiresInSeconds = &seconds
		}
	}

	device := strings.TrimSpace(account.DeviceID)
	if device == "" {
		device = uuid.New().String()
	}
	status, contentType, body, err := s.getSettingsUserOnce(account.AccessToken, device, account.Proxy)
	if err != nil {
		result.Detail = probeNetworkDetail(err, account.Proxy)
		s.logger.Warn("account_probe_failed", "account_id", account.ID, "proxy", account.Proxy != "", "error", err)
		return result, nil
	}
	result.StatusCode = status
	result.ContentKind = classifyProbeBody(contentType, body)

	switch {
	case status == http.StatusUnauthorized:
		result.Status = "unauthorized"
		result.Alive = false
		result.Detail = truncate(probeDetail(body), 300)
		if markErr := s.pool.MarkInvalid(account.AccessToken); markErr != nil {
			s.logger.Error("account_mark_invalid_failed", "account_id", account.ID, "error", markErr)
			result.Detail = truncate(markErr.Error(), 300)
		} else {
			result.MarkedInvalid = true
		}
		s.logger.Warn("account_probe_unauthorized", "account_id", account.ID, "status", status)
	case status == http.StatusOK && result.ContentKind == "json":
		result.Status = "alive"
		result.Alive = true
		result.Detail = "settings/user accepted token"
		s.logger.Info("account_probe_alive", "account_id", account.ID)
	default:
		result.Status = "unknown"
		result.Alive = false
		if result.ContentKind == "html" {
			result.Detail = "upstream returned HTML challenge or block page"
		} else {
			result.Detail = truncate(probeDetail(body), 300)
		}
		s.logger.Warn("account_probe_unknown", "account_id", account.ID, "status", status, "content_kind", result.ContentKind)
	}
	return result, nil
}

func (s *Service) getSettingsUserOnce(token, device, proxy string) (status int, contentType, text string, err error) {
	client := httpclient.New(s.httpOptions, proxy)
	client.Timeout = probeTimeout
	if transport, ok := client.Transport.(*http.Transport); ok {
		// Clone so session clients keep their longer dial budget.
		transport = transport.Clone()
		transport.DialContext = (&net.Dialer{
			Timeout:   probeDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext
		transport.TLSHandshakeTimeout = probeTLSTimeout
		transport.ResponseHeaderTimeout = probeTimeout
		client.Transport = transport
	}
	endpoint := s.settingsUserURL
	if endpoint == "" {
		endpoint = settingsUserURL
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header = s.authHeaders(token, device, map[string]string{
		"accept": "application/json, text/plain, */*",
	})
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	return resp.StatusCode, resp.Header.Get("content-type"), string(raw), nil
}

func probeNetworkDetail(err error, accountProxy string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	timedOut := strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "i/o timeout")
	if timedOut {
		if strings.TrimSpace(accountProxy) != "" {
			return "upstream timeout via account proxy; check proxy reachability"
		}
		return "upstream timeout; check HTTP_PROXY/HTTPS_PROXY/NO_PROXY or set this account's proxy if chatgpt.com is blocked"
	}
	return truncate(msg, 300)
}

func classifyProbeBody(contentType, body string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	trimmed := strings.TrimSpace(body)
	lower := strings.ToLower(trimmed)
	if strings.Contains(ct, "text/html") ||
		strings.Contains(lower, "cf-mitigated") ||
		strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "just a moment") ||
		strings.HasPrefix(lower, "<!doctype html") ||
		strings.HasPrefix(lower, "<html") {
		return "html"
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return "json"
	}
	if trimmed == "" {
		return "empty"
	}
	return "other"
}

func probeDetail(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			if detail, ok := payload["detail"]; ok {
				switch value := detail.(type) {
				case string:
					return value
				case map[string]any:
					if message, ok := value["message"]; ok {
						return fmt.Sprint(message)
					}
				}
				return fmt.Sprint(detail)
			}
			if errObj, ok := payload["error"].(map[string]any); ok {
				if message, ok := errObj["message"]; ok {
					return fmt.Sprint(message)
				}
			}
			if message, ok := payload["message"]; ok {
				return fmt.Sprint(message)
			}
		}
	}
	return trimmed
}

// CreateSessionRequest is the input for CreateSession.
type CreateSessionRequest struct {
	Owner          string
	OfferSDP       string
	Voice          string
	VoiceMode      string
	LanguageCode   string
	VoiceSessionID string
	// AccountID pins the pool account for this conversation. Prefer this over
	// LRU pick when resuming an upstream thread after a gateway restart.
	AccountID int64
	// Optional continuity fields. When VoiceSessionID already has a binding,
	// values stored on the binding win unless the request supplies non-empty
	// replacements (for example after DataChannel updates persisted client-side).
	UpstreamConversationID  string
	UpstreamParentMessageID string
	UpstreamVoiceSessionID  string
}

// pickAccountForSession prefers a live binding token, then a sticky account id
// from SQLite, then falls back to LRU pool selection.
func (s *Service) pickAccountForSession(preferredToken string, preferredAccountID int64, excluded map[string]struct{}) (string, accounts.Account, error) {
	if token := strings.TrimSpace(preferredToken); token != "" {
		token, account, err := s.pool.Pick(token, excluded)
		if err == nil {
			return token, account, nil
		}
		// Preferred token failed; try sticky id before giving up when both set.
	}
	if preferredAccountID > 0 {
		return s.pool.PickByID(preferredAccountID, excluded)
	}
	if token := strings.TrimSpace(preferredToken); token != "" {
		// Surface the preferred-token failure rather than silently rotating.
		return s.pool.Pick(token, excluded)
	}
	return s.pool.Pick("", excluded)
}

// CreateSession POSTs offer SDP to /realtime/wm and returns answer SDP.
func (s *Service) CreateSession(req CreateSessionRequest) (*SessionResult, error) {
	offerSDP, err := normalizeSDP(req.OfferSDP)
	if err != nil {
		return nil, err
	}
	options, err := normalizeSessionOptions(req.Voice, req.VoiceMode, req.LanguageCode)
	if err != nil {
		return nil, err
	}
	voice := options.Voice
	owner := normalizeSessionOwner(req.Owner)
	bound, ownedByOther := s.boundVoiceSession(owner, req.VoiceSessionID)
	if ownedByOther {
		return nil, &ServiceError{Message: "voice session does not belong to caller", StatusCode: 403}
	}

	// After a gateway restart the memory map is empty. Rebuild sticky preference
	// from durable call_sessions metadata when the client still holds vs_...
	var durable *callsessions.Session
	if bound == nil && strings.TrimSpace(req.VoiceSessionID) != "" && s.records != nil {
		if row, err := s.records.Get(owner, req.VoiceSessionID); err == nil {
			durable = &row
		} else if !errors.Is(err, callsessions.ErrNotFound) {
			s.logger.Warn("call_session_lookup_failed", "voice_session_id", req.VoiceSessionID, "error", err)
		}
	}
	// Unknown voice_session_id with neither memory nor durable row: reject so
	// clients cannot invent session ids (except first create with empty id).
	if strings.TrimSpace(req.VoiceSessionID) != "" && bound == nil && durable == nil {
		return nil, &ServiceError{Message: "voice session not found", StatusCode: 404}
	}

	// Sticky account preference, strongest first:
	// 1) live memory binding token
	// 2) client-supplied account_id (from SQLite conversation / downstream sticky)
	// 3) durable call_sessions.account_id
	// 4) LRU pool pick
	preferredToken := ""
	preferredAccountID := req.AccountID
	if bound != nil {
		if bound.AccessToken != "" {
			preferredToken = bound.AccessToken
		}
		if preferredAccountID <= 0 && bound.AccountID > 0 {
			preferredAccountID = bound.AccountID
		}
	}
	if preferredAccountID <= 0 && durable != nil && durable.AccountID > 0 {
		preferredAccountID = durable.AccountID
	}
	// When a sticky account is known, never silently switch: the chatgpt.com
	// conversation_id is only valid for the account that created it.
	// Legacy rows with upstream ids but no account_id keep best-effort LRU pick.
	requireStickyAccount := preferredAccountID > 0 || preferredToken != ""

	requestUpstream := UpstreamContext{
		ConversationID:         strings.TrimSpace(req.UpstreamConversationID),
		ParentMessageID:        strings.TrimSpace(req.UpstreamParentMessageID),
		UpstreamVoiceSessionID: strings.TrimSpace(req.UpstreamVoiceSessionID),
	}
	upstreamForWM := requestUpstream
	if durable != nil {
		// Fill gaps from durable metadata before applying the live binding.
		upstreamForWM = mergeUpstreamContext(UpstreamContext{
			ConversationID:         durable.UpstreamConversationID,
			ParentMessageID:        durable.UpstreamParentMessageID,
			UpstreamVoiceSessionID: durable.UpstreamVoiceSessionID,
		}, upstreamForWM)
	}
	if bound != nil {
		// Prefer sticky upstream voice session id from the binding so SDP
		// re-exchanges resume the same upstream realtime session when possible.
		upstreamForWM = mergeUpstreamContext(bound.upstreamContext(), requestUpstream)
		if durable != nil {
			// Keep any durable-only fields the live binding has not learned yet.
			upstreamForWM = mergeUpstreamContext(UpstreamContext{
				ConversationID:         durable.UpstreamConversationID,
				ParentMessageID:        durable.UpstreamParentMessageID,
				UpstreamVoiceSessionID: durable.UpstreamVoiceSessionID,
			}, upstreamForWM)
			upstreamForWM = mergeUpstreamContext(upstreamForWM, requestUpstream)
		}
	}
	if strings.TrimSpace(upstreamForWM.UpstreamVoiceSessionID) == "" {
		upstreamForWM.UpstreamVoiceSessionID = newUpstreamVoiceSessionID()
	}

	sessionJSON := buildSessionJSON(voice, options.VoiceMode, options.LanguageCode, upstreamForWM)
	excluded := map[string]struct{}{}
	var lastError string
	var lastDetail any
	lastStatus := 0

	for attempt := 1; attempt <= s.cfg.MaxAccountAttempts; attempt++ {
		token, account, err := s.pickAccountForSession(preferredToken, preferredAccountID, excluded)
		if err != nil {
			if preferredToken != "" || preferredAccountID > 0 {
				s.ReleaseSession(owner, req.VoiceSessionID)
				// Sticky account gone: cannot honestly resume this upstream thread.
				if requireStickyAccount {
					if ae, ok := err.(*accounts.Error); ok {
						return nil, &ServiceError{Message: ae.Message, StatusCode: 503}
					}
					return nil, &ServiceError{Message: err.Error(), StatusCode: 503}
				}
				preferredToken = ""
				preferredAccountID = 0
				upstreamForWM.UpstreamVoiceSessionID = newUpstreamVoiceSessionID()
				// Drop upstream conversation resume fields if we no longer own the account.
				upstreamForWM.ConversationID = ""
				upstreamForWM.ParentMessageID = ""
				sessionJSON = buildSessionJSON(voice, options.VoiceMode, options.LanguageCode, upstreamForWM)
				token, account, err = s.pool.Pick("", excluded)
			}
		}
		if err != nil {
			if ae, ok := err.(*accounts.Error); ok {
				return nil, &ServiceError{Message: ae.Message, StatusCode: 503}
			}
			return nil, &ServiceError{Message: err.Error(), StatusCode: 503}
		}
		excluded[token] = struct{}{}

		device := account.DeviceID
		if device == "" && bound != nil {
			device = bound.DeviceID
		}
		if device == "" {
			device = uuid.New().String()
		}

		explicitProxy := account.Proxy
		if explicitProxy == "" && bound != nil {
			explicitProxy = bound.Proxy
		}
		proxySource := "process_environment_or_direct"
		if strings.TrimSpace(explicitProxy) != "" {
			proxySource = "account"
		}

		status, _, text, err := s.postWMOnce(token, offerSDP, sessionJSON, device, explicitProxy)
		if err != nil {
			s.logger.Error("upstream_realtime_request_failed", "account_id", account.ID, "attempt", attempt, "error", err)
			return nil, &ServiceError{
				Message:    "realtime/wm network failed",
				StatusCode: 502,
				Detail:     truncate(err.Error(), 300),
			}
		}

		if status == 401 {
			lastStatus = status
			lastError = "account token invalid"
			lastDetail = truncate(text, 300)
			if markErr := s.pool.MarkInvalid(token); markErr != nil {
				s.logger.Error("account_mark_invalid_failed", "account_id", account.ID, "error", markErr)
			}
			s.logger.Warn("upstream_account_rejected", "account_id", account.ID, "upstream_status", status, "attempt", attempt)
			if bound != nil && token == bound.AccessToken {
				s.ReleaseSession(owner, req.VoiceSessionID)
			}
			// Bound account died. Upstream conversation ids for that account are unusable.
			if requireStickyAccount {
				return nil, &ServiceError{
					Message:    "bound account token invalid",
					StatusCode: 401,
					Detail:     lastDetail,
				}
			}
			preferredToken = ""
			preferredAccountID = 0
			upstreamForWM.UpstreamVoiceSessionID = newUpstreamVoiceSessionID()
			upstreamForWM.ConversationID = ""
			upstreamForWM.ParentMessageID = ""
			sessionJSON = buildSessionJSON(voice, options.VoiceMode, options.LanguageCode, upstreamForWM)
			continue
		}

		if (status != 200 && status != 201) || !strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "v=0") {
			s.logger.Warn("upstream_realtime_rejected", "account_id", account.ID, "upstream_status", status, "attempt", attempt)
			return nil, &ServiceError{
				Message:    fmt.Sprintf("realtime/wm failed status=%d", status),
				StatusCode: 502,
				Detail:     truncate(text, 500),
			}
		}

		voiceSessionID := s.bindVoiceSession(owner, req.VoiceSessionID, token, account, upstreamForWM)
		// Re-read binding so response fields match what was actually stored.
		stored, _ := s.boundVoiceSession(owner, voiceSessionID)
		upstreamOut := upstreamForWM
		if stored != nil {
			upstreamOut = stored.upstreamContext()
		}
		s.persistCallSession(owner, voiceSessionID, account.ID, voice, options.VoiceMode, options.LanguageCode, upstreamOut)
		s.logger.Info(
			"voice_session_created",
			"voice_session_id", voiceSessionID,
			"account_id", account.ID,
			"voice", voice,
			"proxy_source", proxySource,
			"attempt", attempt,
			"resume_conversation", upstreamOut.ConversationID != "",
			"upstream_voice_session_id", upstreamOut.UpstreamVoiceSessionID,
		)
		return &SessionResult{
			AnswerSDP:               text,
			SessionID:               voiceSessionID,
			VoiceSessionID:          voiceSessionID,
			AccountID:               account.ID,
			Voice:                   voice,
			VoiceMode:               options.VoiceMode,
			LanguageCode:            options.LanguageCode,
			UpstreamVoiceSessionID:  upstreamOut.UpstreamVoiceSessionID,
			UpstreamConversationID:  upstreamOut.ConversationID,
			UpstreamParentMessageID: upstreamOut.ParentMessageID,
		}, nil
	}

	code := 503
	if lastStatus == 401 {
		code = 401
	}
	return nil, &ServiceError{
		Message:    orDefault(lastError, "no available web access_token"),
		StatusCode: code,
		Detail:     lastDetail,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// persistCallSession writes metadata-only call session state for admin listing
// and sticky resume. Chat content is never stored here.
func (s *Service) persistCallSession(owner, voiceSessionID string, accountID int64, voiceName, voiceMode, languageCode string, upstream UpstreamContext) {
	if s == nil || s.records == nil {
		return
	}
	owner = normalizeSessionOwner(owner)
	kind, label, apiKeyID := classifyCaller(owner)
	item := callsessions.Session{
		VoiceSessionID:          strings.TrimSpace(voiceSessionID),
		Owner:                   owner,
		CallerKind:              kind,
		CallerLabel:             label,
		APIKeyID:                apiKeyID,
		AccountID:               accountID,
		UpstreamConversationID:  upstream.ConversationID,
		UpstreamParentMessageID: upstream.ParentMessageID,
		UpstreamVoiceSessionID:  upstream.UpstreamVoiceSessionID,
		Voice:                   voiceName,
		VoiceMode:               voiceMode,
		LanguageCode:            languageCode,
		Status:                  callsessions.StatusActive,
	}
	if err := s.records.Upsert(item); err != nil {
		s.logger.Warn("call_session_persist_failed", "voice_session_id", voiceSessionID, "error", err)
	}
}

func classifyCaller(owner string) (kind, label string, apiKeyID int64) {
	owner = strings.TrimSpace(owner)
	if strings.HasPrefix(owner, "api_key:") {
		idText := strings.TrimPrefix(owner, "api_key:")
		id, _ := strconv.ParseInt(idText, 10, 64)
		return callsessions.CallerAPIKey, "api_key:" + idText, id
	}
	// Built-in voice page / Basic Auth automation: admin:<username> or internal.
	label = callsessions.CallerAdmin
	if strings.HasPrefix(owner, "admin:") {
		user := strings.TrimSpace(strings.TrimPrefix(owner, "admin:"))
		if user != "" {
			label = "admin:" + user
		}
	}
	return callsessions.CallerAdmin, label, 0
}
