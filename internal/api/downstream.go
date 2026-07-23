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
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"answer_sdp":       result.AnswerSDP,
		"voice_session_id": result.VoiceSessionID,
		"voice":            result.Voice,
		"voice_mode":       result.VoiceMode,
		"language_code":    result.LanguageCode,
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
	case http.StatusForbidden:
		writeDownstreamError(w, http.StatusForbidden, "forbidden", "voice session does not belong to caller")
	case http.StatusNotFound:
		writeDownstreamError(w, http.StatusNotFound, "voice_session_not_found", "voice session not found")
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
