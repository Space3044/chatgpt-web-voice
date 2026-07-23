package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/auth"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/logging"
)

type createConversationRequest struct {
	Title string `json:"title"`
}

type updateConversationRequest struct {
	Title string `json:"title"`
}

type conversationMessageRequest struct {
	ClientID string `json:"client_id"`
	Role     string `json:"role"`
	Content  string `json:"content"`
}

func requestOwner(r *http.Request) string {
	owner := auth.Username(r.Context())
	if strings.TrimSpace(owner) == "" {
		// Direct handler tests do not install the outer auth middleware. Runtime
		// requests always carry the configured authenticated username.
		return "default"
	}
	return owner
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	items, err := s.conversations.List(requestOwner(r))
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": items})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	var request createConversationRequest
	if err := decodeConversationJSON(w, r, &request, 16<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	conversation, err := s.conversations.Create(requestOwner(r), request.Title)
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("conversation_created", "conversation_id", conversation.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": conversation})
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	id, err := conversationPathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	conversation, err := s.conversations.Get(requestOwner(r), id)
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conversation})
}

func (s *Server) updateConversation(w http.ResponseWriter, r *http.Request) {
	id, err := conversationPathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	var request updateConversationRequest
	if err := decodeConversationJSON(w, r, &request, 16<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	conversation, err := s.conversations.UpdateTitle(requestOwner(r), id, request.Title)
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("conversation_renamed", "conversation_id", conversation.ID)
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conversation})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	id, err := conversationPathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	if err := s.conversations.Delete(requestOwner(r), id); err != nil {
		writeConversationError(w, r, err)
		return
	}
	logging.FromContext(r.Context()).Info("conversation_deleted", "conversation_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) upsertConversationMessage(w http.ResponseWriter, r *http.Request) {
	id, err := conversationPathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	var request conversationMessageRequest
	if err := decodeConversationJSON(w, r, &request, 80<<10); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": err.Error()}})
		return
	}
	message, err := s.conversations.UpsertMessage(requestOwner(r), id, conversations.Message{
		ClientID: request.ClientID,
		Role:     request.Role,
		Content:  request.Content,
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": message})
}

func decodeConversationJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid conversation payload")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid conversation payload")
	}
	return nil
}

func conversationPathID(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("invalid conversation id")
	}
	return id, nil
}

func writeConversationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, conversations.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"error": "conversation resource not found"}})
	default:
		var validationError *conversations.Error
		if errors.As(err, &validationError) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"error": validationError.Message}})
			return
		}
		logging.FromContext(r.Context()).Error("conversation_operation_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": map[string]any{"error": "conversation operation failed"}})
	}
}
