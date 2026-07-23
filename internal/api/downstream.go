package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/voice"
)

func (s *Server) downstreamHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": "v1"})
}

func (s *Server) voiceConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, voice.Config())
}

func (s *Server) downstreamSession(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeDownstreamError(w, http.StatusServiceUnavailable, "voice_unavailable", "voice service unavailable")
		return
	}
	owner := auth.APIKeyOwner(r.Context())
	if owner == "" {
		writeDownstreamError(w, http.StatusUnauthorized, "invalid_api_key", "valid Bearer API key required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body sessionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeDownstreamError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	// Downstream resume model: the caller only keeps voice_session_id.
	// Sticky pool account + upstream continuity are restored from call_sessions
	// on the gateway. Do not accept or return account_id / pool secrets.
	result, err := s.voice.CreateSession(voice.CreateSessionRequest{
		Owner:          owner,
		OfferSDP:       body.OfferSDP,
		Voice:          body.Voice,
		VoiceMode:      body.VoiceMode,
		LanguageCode:   body.LanguageCode,
		VoiceSessionID: body.VoiceSessionID,
	})
	if err != nil {
		writeDownstreamServiceError(w, err)
		return
	}
	key, _ := auth.APIKey(r.Context())
	logging.FromContext(r.Context()).Info(
		"downstream_voice_session_created",
		"api_key_id", key.ID,
		"voice_session_id", result.VoiceSessionID,
		"account_id", result.AccountID,
		"resume_conversation", result.UpstreamConversationID != "",
	)
	// Public contract: only signaling + session handle. Pool account and
	// upstream continuity stay server-side.
	writeJSON(w, http.StatusOK, map[string]any{
		"answer_sdp":       result.AnswerSDP,
		"voice_session_id": result.VoiceSessionID,
		"voice":            result.Voice,
		"voice_mode":       result.VoiceMode,
		"language_code":    result.LanguageCode,
	})
}

func (s *Server) downstreamSessionContext(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeDownstreamError(w, http.StatusServiceUnavailable, "voice_unavailable", "voice service unavailable")
		return
	}
	owner := auth.APIKeyOwner(r.Context())
	if owner == "" {
		writeDownstreamError(w, http.StatusUnauthorized, "invalid_api_key", "valid Bearer API key required")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if sessionID == "" {
		writeDownstreamError(w, http.StatusBadRequest, "invalid_request", "voice session id is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		UpstreamConversationID  string `json:"upstream_conversation_id"`
		UpstreamParentMessageID string `json:"upstream_parent_message_id"`
		UpstreamVoiceSessionID  string `json:"upstream_voice_session_id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeDownstreamError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	updated, err := s.voice.UpdateSessionContext(owner, sessionID, voice.UpstreamContext{
		ConversationID:         body.UpstreamConversationID,
		ParentMessageID:        body.UpstreamParentMessageID,
		UpstreamVoiceSessionID: body.UpstreamVoiceSessionID,
	})
	if err != nil {
		writeDownstreamServiceError(w, err)
		return
	}
	key, _ := auth.APIKey(r.Context())
	logging.FromContext(r.Context()).Info(
		"downstream_voice_session_context_updated",
		"api_key_id", key.ID,
		"voice_session_id", sessionID,
		"has_conversation", updated.ConversationID != "",
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                         true,
		"voice_session_id":           sessionID,
		"upstream_conversation_id":   updated.ConversationID,
		"upstream_parent_message_id": updated.ParentMessageID,
		"upstream_voice_session_id":  updated.UpstreamVoiceSessionID,
	})
}

func (s *Server) downstreamSessionTitle(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeDownstreamError(w, http.StatusServiceUnavailable, "voice_unavailable", "voice service unavailable")
		return
	}
	owner := auth.APIKeyOwner(r.Context())
	if owner == "" {
		writeDownstreamError(w, http.StatusUnauthorized, "invalid_api_key", "valid Bearer API key required")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if sessionID == "" {
		writeDownstreamError(w, http.StatusBadRequest, "invalid_request", "voice session id is required")
		return
	}
	conversationID := strings.TrimSpace(r.URL.Query().Get("upstream_conversation_id"))
	if conversationID == "" {
		conversationID = strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	}
	result, err := s.voice.FetchUpstreamTitle(owner, sessionID, conversationID)
	if err != nil {
		writeDownstreamServiceError(w, err)
		return
	}
	key, _ := auth.APIKey(r.Context())
	logging.FromContext(r.Context()).Info(
		"downstream_voice_session_title_fetched",
		"api_key_id", key.ID,
		"voice_session_id", sessionID,
		"has_title", result.HasTitle,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"voice_session_id":           result.VoiceSessionID,
		"upstream_conversation_id":   result.UpstreamConversationID,
		"title":                      result.Title,
		"has_title":                  result.HasTitle,
	})
}

func (s *Server) downstreamRelease(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeDownstreamError(w, http.StatusServiceUnavailable, "voice_unavailable", "voice service unavailable")
		return
	}
	owner := auth.APIKeyOwner(r.Context())
	if owner == "" {
		writeDownstreamError(w, http.StatusUnauthorized, "invalid_api_key", "valid Bearer API key required")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	if sessionID == "" || !s.voice.ReleaseSession(owner, sessionID) {
		writeDownstreamError(w, http.StatusNotFound, "voice_session_not_found", "voice session not found")
		return
	}
	key, _ := auth.APIKey(r.Context())
	logging.FromContext(r.Context()).Info(
		"downstream_voice_session_released",
		"api_key_id", key.ID,
		"voice_session_id", sessionID,
	)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "released": true})
}

func writeDownstreamServiceError(w http.ResponseWriter, err error) {
	se, ok := err.(*voice.ServiceError)
	if !ok {
		writeDownstreamError(w, http.StatusInternalServerError, "internal_error", "voice session could not be created")
		return
	}
	switch se.StatusCode {
	case http.StatusBadRequest:
		writeDownstreamError(w, http.StatusBadRequest, "invalid_request", se.Message)
	case http.StatusUnauthorized:
		writeDownstreamError(w, http.StatusUnauthorized, "upstream_unauthorized", "upstream account token invalid")
	case http.StatusForbidden:
		writeDownstreamError(w, http.StatusForbidden, "forbidden", "voice session does not belong to caller")
	case http.StatusNotFound:
		// Distinguish missing gateway binding vs missing upstream conversation when possible.
		if strings.Contains(strings.ToLower(se.Message), "upstream conversation") {
			writeDownstreamError(w, http.StatusNotFound, "upstream_conversation_not_found", "upstream conversation not found")
			return
		}
		writeDownstreamError(w, http.StatusNotFound, "voice_session_not_found", "voice session not found")
	case http.StatusTooManyRequests:
		writeDownstreamError(w, http.StatusTooManyRequests, "upstream_rate_limited", "upstream conversation rate limited")
	case http.StatusBadGateway:
		writeDownstreamError(w, http.StatusBadGateway, "upstream_unavailable", "upstream voice service unavailable")
	default:
		writeDownstreamError(w, http.StatusServiceUnavailable, "voice_unavailable", "voice service unavailable")
	}
}

func writeDownstreamError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
