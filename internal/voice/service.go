package voice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/httpclient"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/tokenutil"
)

const (
	wmURL           = "https://chatgpt.com/realtime/wm?dcid=0"
	settingsUserURL = "https://chatgpt.com/backend-api/settings/user"
	// Probe is a quick liveness check. Keep it short so the accounts panel
	// does not sit on "checking…" while an unreachable upstream path dials chatgpt.com.
	probeTimeout     = 12 * time.Second
	probeDialTimeout = 8 * time.Second
	probeTLSTimeout  = 8 * time.Second
	probeBodyLimit   = 64 << 10
)

var allowedRealtimeVoices = map[string]struct{}{
	"breeze": {}, "cove": {}, "ember": {}, "fathom": {}, "glimmer": {},
	"juniper": {}, "maple": {}, "orbit": {}, "vale": {},
}

var realtimeVoiceAliases = map[string]string{
	"arbor":  "fathom",
	"sol":    "glimmer",
	"spruce": "orbit",
}

// AccountRepository is the account-pool surface required by the voice gateway.
// Concrete type is *accounts.Pool; the interface keeps this package free of
// storage-construction details.
type AccountRepository interface {
	Pick(preferredToken string, excluded map[string]struct{}) (string, accounts.Account, error)
	MarkInvalid(token string) error
	Get(id int64) (accounts.Account, error)
}

// ServiceError is a typed gateway error with HTTP status.
type ServiceError struct {
	Message    string
	StatusCode int
	Detail     any
}

func (e *ServiceError) Error() string { return e.Message }

type sessionBinding struct {
	AccountID   int64
	AccessToken string
	DeviceID    string
	Proxy       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Service is the ChatGPT web voice gateway.
type Service struct {
	cfg         config.Config
	httpOptions httpclient.Options
	pool        AccountRepository
	logger      *slog.Logger

	// settingsUserURL overrides the account probe endpoint in tests.
	settingsUserURL string

	mu       sync.Mutex
	bindings map[string]*sessionBinding
}

// New creates a voice service.
func New(cfg config.Config, pool AccountRepository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:             cfg,
		httpOptions:     httpclient.FromConfig(cfg),
		pool:            pool,
		logger:          logger,
		settingsUserURL: settingsUserURL,
		bindings:        make(map[string]*sessionBinding),
	}
}

// SessionResult is returned by CreateSession.
type SessionResult struct {
	AnswerSDP      string `json:"answer_sdp"`
	Voice          string `json:"voice"`
	VoiceMode      string `json:"voice_mode"`
	SessionID      string `json:"session_id"`
	VoiceSessionID string `json:"voice_session_id"`
}

func normalizeVoice(voice string) string {
	clean := strings.ToLower(strings.TrimSpace(voice))
	if alias, ok := realtimeVoiceAliases[clean]; ok {
		clean = alias
	}
	if _, ok := allowedRealtimeVoices[clean]; ok {
		return clean
	}
	return "cove"
}

func newVoiceSessionID() string {
	return "vs_" + strings.ReplaceAll(uuid.New().String(), "-", "")
}

func (s *Service) cleanupBindingsLocked(now time.Time) {
	ttl := time.Duration(s.cfg.SessionTTLSeconds) * time.Second
	for id, item := range s.bindings {
		base := item.UpdatedAt
		if base.IsZero() {
			base = item.CreatedAt
		}
		if now.Sub(base) > ttl {
			delete(s.bindings, id)
		}
	}
}

func (s *Service) bindVoiceSession(sessionID, token string, account accounts.Account) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = newVoiceSessionID()
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupBindingsLocked(now)
	s.bindings[sessionID] = &sessionBinding{
		AccountID:   account.ID,
		AccessToken: token,
		DeviceID:    account.DeviceID,
		Proxy:       account.Proxy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	return sessionID
}

// ReleaseSession unbinds a voice_session_id.
func (s *Service) ReleaseSession(voiceSessionID string) bool {
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.bindings[sessionID]
	if ok {
		delete(s.bindings, sessionID)
	}
	return ok
}

func (s *Service) boundVoiceSession(voiceSessionID string) *sessionBinding {
	sessionID := strings.TrimSpace(voiceSessionID)
	if sessionID == "" {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupBindingsLocked(now)
	item := s.bindings[sessionID]
	if item == nil {
		return nil
	}
	item.UpdatedAt = now
	cp := *item
	return &cp
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

func buildSessionJSON(voice, voiceMode, languageCode string) string {
	sid := strings.ToUpper(uuid.New().String())
	if languageCode == "" {
		languageCode = "auto"
	}
	if voiceMode == "" {
		voiceMode = "wingman"
	}
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
	OfferSDP       string
	Voice          string
	VoiceMode      string
	LanguageCode   string
	VoiceSessionID string
}

// CreateSession POSTs offer SDP to /realtime/wm and returns answer SDP.
func (s *Service) CreateSession(req CreateSessionRequest) (*SessionResult, error) {
	offerSDP, err := normalizeSDP(req.OfferSDP)
	if err != nil {
		return nil, err
	}
	voice := normalizeVoice(req.Voice)
	bound := s.boundVoiceSession(req.VoiceSessionID)
	preferred := ""
	if bound != nil && bound.AccessToken != "" {
		preferred = bound.AccessToken
	}
	sessionJSON := buildSessionJSON(voice, req.VoiceMode, req.LanguageCode)
	excluded := map[string]struct{}{}
	var lastError string
	var lastDetail any
	lastStatus := 0

	for attempt := 1; attempt <= s.cfg.MaxAccountAttempts; attempt++ {
		pickPreferred := preferred
		if attempt > 1 {
			pickPreferred = ""
		}
		token, account, err := s.pool.Pick(pickPreferred, excluded)
		if err != nil && pickPreferred != "" {
			s.ReleaseSession(req.VoiceSessionID)
			preferred = ""
			token, account, err = s.pool.Pick("", excluded)
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
				s.ReleaseSession(req.VoiceSessionID)
			}
			preferred = ""
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

		voiceSessionID := s.bindVoiceSession(req.VoiceSessionID, token, account)
		s.logger.Info("voice_session_created", "voice_session_id", voiceSessionID, "account_id", account.ID, "voice", voice, "proxy_source", proxySource, "attempt", attempt)
		return &SessionResult{
			AnswerSDP:      text,
			SessionID:      voiceSessionID,
			VoiceSessionID: voiceSessionID,
			Voice:          voice,
			VoiceMode:      orDefault(req.VoiceMode, "wingman"),
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
