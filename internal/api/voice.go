package api

import (
	"encoding/json"
	"net/http"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/voice"
)

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"endpoint": "/api/voice/session",
		"release":  "/api/voice/session/release",
		"wm":       "https://chatgpt.com/realtime/wm",
		"project":  "chatgpt-web-voice",
	})
}

type sessionRequest struct {
	OfferSDP       string `json:"offer_sdp"`
	Voice          string `json:"voice"`
	VoiceMode      string `json:"voice_mode"`
	LanguageCode   string `json:"language_code"`
	VoiceSessionID string `json:"voice_session_id"`
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "voice service unavailable"},
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body sessionRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": map[string]any{"error": "invalid json body"},
		})
		return
	}
	result, err := s.voice.CreateSession(voice.CreateSessionRequest{
		OfferSDP:       body.OfferSDP,
		Voice:          body.Voice,
		VoiceMode:      body.VoiceMode,
		LanguageCode:   body.LanguageCode,
		VoiceSessionID: body.VoiceSessionID,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type releaseRequest struct {
	VoiceSessionID string `json:"voice_session_id"`
	SessionID      string `json:"session_id"`
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "voice service unavailable"},
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body releaseRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	sessionID := body.VoiceSessionID
	if sessionID == "" {
		sessionID = body.SessionID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"released": s.voice.ReleaseSession(sessionID),
	})
}

func writeServiceError(w http.ResponseWriter, err error) {
	if se, ok := err.(*voice.ServiceError); ok {
		writeJSON(w, se.StatusCode, map[string]any{
			"detail": map[string]any{
				"error":  se.Message,
				"detail": se.Detail,
			},
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"detail": map[string]any{"error": truncate(err.Error(), 300)},
	})
}
