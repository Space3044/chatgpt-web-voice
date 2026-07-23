package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
)

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	if s.apiKeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": map[string]any{"error": "API key service unavailable"}})
		return
	}
	keys, err := s.apiKeys.List()
	if err != nil {
		writeAPIKeyAdminError(w, r, err)
		return
	}
	stats, err := s.apiKeys.Stats()
	if err != nil {
		writeAPIKeyAdminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "stats": stats})
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.apiKeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": map[string]any{"error": "API key service unavailable"}})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var request struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": "invalid API key payload"}})
		return
	}
	created, err := s.apiKeys.Create(request.Name)
	if err != nil {
		writeAPIKeyAdminError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("api_key_created", "api_key_id", created.Key.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.apiKeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": map[string]any{"error": "API key service unavailable"}})
		return
	}
	id, err := pathAPIKeyID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var request struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || (request.Name == nil && request.Enabled == nil) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": "invalid API key payload"}})
		return
	}
	updated, err := s.apiKeys.Update(id, apikeys.Update{Name: request.Name, Enabled: request.Enabled})
	if err != nil {
		writeAPIKeyAdminError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("api_key_updated", "api_key_id", id, "enabled", updated.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"key": updated})
}

func (s *Server) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if s.apiKeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": map[string]any{"error": "API key service unavailable"}})
		return
	}
	id, err := pathAPIKeyID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	if err := s.apiKeys.Delete(id); err != nil {
		writeAPIKeyAdminError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("api_key_deleted", "api_key_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func pathAPIKeyID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("invalid API key id")
	}
	return id, nil
}

func writeAPIKeyAdminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, apikeys.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"error": "API key not found"}})
	default:
		var validationError *apikeys.Error
		if errors.As(err, &validationError) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": validationError.Message}})
			return
		}
		logging.FromContext(r.Context()).Error("api_key_operation_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": map[string]any{"error": "API key operation failed"}})
	}
}
