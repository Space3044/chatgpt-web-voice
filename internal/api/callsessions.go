package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/callsessions"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
)

// CallSessionStore is the admin-facing call-session metadata surface.
type CallSessionStore interface {
	List(filter callsessions.ListFilter) ([]callsessions.Session, error)
	Stats() (callsessions.Stats, error)
	GetByID(voiceSessionID string) (callsessions.Session, error)
	Delete(voiceSessionID string) error
}

func (s *Server) listCallSessions(w http.ResponseWriter, r *http.Request) {
	if s.callSessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "call session store unavailable"},
		})
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := s.callSessions.List(callsessions.ListFilter{
		Query:      r.URL.Query().Get("q"),
		CallerKind: r.URL.Query().Get("caller"),
		Status:     r.URL.Query().Get("status"),
		Limit:      limit,
	})
	if err != nil {
		logging.FromContext(r.Context()).Error("call_sessions_list_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"detail": map[string]any{"error": "list call sessions failed"},
		})
		return
	}
	// Enrich rows with current API key name and account email when still present.
	enriched := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := map[string]any{
			"voice_session_id":           item.VoiceSessionID,
			"owner":                      item.Owner,
			"caller_kind":                item.CallerKind,
			"caller_label":               item.CallerLabel,
			"api_key_id":                 item.APIKeyID,
			"account_id":                 item.AccountID,
			"account_email":              "",
			"upstream_conversation_id":   item.UpstreamConversationID,
			"upstream_parent_message_id": item.UpstreamParentMessageID,
			"upstream_voice_session_id":  item.UpstreamVoiceSessionID,
			"voice":                      item.Voice,
			"voice_mode":                 item.VoiceMode,
			"language_code":              item.LanguageCode,
			"status":                     item.Status,
			"created_at":                 item.CreatedAt,
			"updated_at":                 item.UpdatedAt,
			"last_seen_at":               item.LastSeenAt,
			"released_at":                item.ReleasedAt,
		}
		if item.CallerKind == callsessions.CallerAPIKey && item.APIKeyID > 0 && s.apiKeys != nil {
			if key, err := s.lookupAPIKey(item.APIKeyID); err == nil {
				row["api_key_name"] = key.Name
				row["api_key_prefix"] = key.Prefix
				if key.Name != "" {
					row["caller_label"] = key.Name
				}
			}
		}
		if item.CallerKind == callsessions.CallerAdmin {
			row["caller_label"] = "admin"
		}
		if item.AccountID > 0 && s.accounts != nil {
			if account, err := s.accounts.Get(item.AccountID); err == nil {
				row["account_email"] = account.Email
			}
		}
		enriched = append(enriched, row)
	}
	stats, err := s.callSessions.Stats()
	if err != nil {
		logging.FromContext(r.Context()).Error("call_sessions_stats_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"detail": map[string]any{"error": "call session stats failed"},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": enriched,
		"stats":    stats,
	})
}

func (s *Server) deleteCallSession(w http.ResponseWriter, r *http.Request) {
	if s.callSessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": map[string]any{"error": "call session store unavailable"},
		})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 160 || strings.ContainsAny(id, "/\\") {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": map[string]any{"error": "invalid voice session id"},
		})
		return
	}
	if err := s.callSessions.Delete(id); err != nil {
		if errors.Is(err, callsessions.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": map[string]any{"error": "call session not found"},
			})
			return
		}
		logging.FromContext(r.Context()).Error("call_session_delete_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"detail": map[string]any{"error": "delete call session failed"},
		})
		return
	}
	logging.FromContext(r.Context()).Info("call_session_deleted", "voice_session_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) lookupAPIKey(id int64) (struct {
	Name   string
	Prefix string
}, error) {
	type keyMeta struct {
		Name   string
		Prefix string
	}
	if s.apiKeys == nil {
		return keyMeta{}, errors.New("api keys unavailable")
	}
	item, err := s.apiKeys.Get(id)
	if err != nil {
		return keyMeta{}, err
	}
	return keyMeta{Name: item.Name, Prefix: item.Prefix}, nil
}
