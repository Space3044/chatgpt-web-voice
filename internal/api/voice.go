package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
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
	OfferSDP                string `json:"offer_sdp"`
	Voice                   string `json:"voice"`
	VoiceMode               string `json:"voice_mode"`
	LanguageCode            string `json:"language_code"`
	VoiceSessionID          string `json:"voice_session_id"`
	AccountID               int64  `json:"account_id"`
	UpstreamConversationID  string `json:"upstream_conversation_id"`
	UpstreamParentMessageID string `json:"upstream_parent_message_id"`
	UpstreamVoiceSessionID  string `json:"upstream_voice_session_id"`
}

type sessionContextRequest struct {
	VoiceSessionID          string `json:"voice_session_id"`
	UpstreamConversationID  string `json:"upstream_conversation_id"`
	UpstreamParentMessageID string `json:"upstream_parent_message_id"`
	UpstreamVoiceSessionID  string `json:"upstream_voice_session_id"`
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
		Owner:                   adminVoiceOwner(r),
		OfferSDP:                body.OfferSDP,
		Voice:                   body.Voice,
		VoiceMode:               body.VoiceMode,
		LanguageCode:            body.LanguageCode,
		VoiceSessionID:          body.VoiceSessionID,
		AccountID:               body.AccountID,
		UpstreamConversationID:  body.UpstreamConversationID,
		UpstreamParentMessageID: body.UpstreamParentMessageID,
		UpstreamVoiceSessionID:  body.UpstreamVoiceSessionID,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) sessionContext(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "voice service unavailable"},
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body sessionContextRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": map[string]any{"error": "invalid json body"},
		})
		return
	}
	updated, err := s.voice.UpdateSessionContext(adminVoiceOwner(r), body.VoiceSessionID, voice.UpstreamContext{
		ConversationID:         body.UpstreamConversationID,
		ParentMessageID:        body.UpstreamParentMessageID,
		UpstreamVoiceSessionID: body.UpstreamVoiceSessionID,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                         true,
		"voice_session_id":           body.VoiceSessionID,
		"upstream_conversation_id":   updated.ConversationID,
		"upstream_parent_message_id": updated.ParentMessageID,
		"upstream_voice_session_id":  updated.UpstreamVoiceSessionID,
	})
}

func (s *Server) sessionTitle(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "voice service unavailable"},
		})
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("voice_session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(r.URL.Query().Get("session_id"))
	}
	conversationID := strings.TrimSpace(r.URL.Query().Get("upstream_conversation_id"))
	if conversationID == "" {
		conversationID = strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	}
	result, err := s.voice.FetchUpstreamTitle(adminVoiceOwner(r), sessionID, conversationID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type imageUploadRequestBody struct {
	VoiceSessionID string `json:"voice_session_id"`
	FileName       string `json:"file_name"`
	FileSize       int64  `json:"file_size"`
	MimeType       string `json:"mime_type"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
}

type imageUploadCompleteBody struct {
	VoiceSessionID string `json:"voice_session_id"`
	FileID         string `json:"file_id"`
}

// sessionImageUpload issues a direct-upload credential for the sticky account
// on the live voice session. Image bytes never pass through the gateway.
func (s *Server) sessionImageUpload(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "voice service unavailable"},
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body imageUploadRequestBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": map[string]any{"error": "invalid json body"},
		})
		return
	}
	result, err := s.voice.CreateImageUploadCredential(adminVoiceOwner(r), body.VoiceSessionID, voice.ImageUploadRequest{
		FileName: body.FileName,
		FileSize: body.FileSize,
		MimeType: body.MimeType,
		Width:    body.Width,
		Height:   body.Height,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) sessionImageUploadComplete(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "voice service unavailable"},
		})
		return
	}
	fileID := strings.TrimSpace(r.PathValue("file_id"))
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body imageUploadCompleteBody
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": map[string]any{"error": "invalid json body"},
		})
		return
	}
	if fileID == "" {
		fileID = strings.TrimSpace(body.FileID)
	}
	result, err := s.voice.CompleteImageUpload(adminVoiceOwner(r), body.VoiceSessionID, fileID)
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
		"released": s.voice.ReleaseSession(adminVoiceOwner(r), sessionID),
	})
}

func adminVoiceOwner(r *http.Request) string {
	return "admin:" + auth.Username(r.Context())
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
