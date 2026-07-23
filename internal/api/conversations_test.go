package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
)

func TestConversationManagementAPI(t *testing.T) {
	_, mux := newAPITestServer(t)
	create := performJSONRequest(t, mux, http.MethodPost, "/api/conversations", map[string]any{"title": "Dinner"})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var createBody struct {
		Conversation conversations.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &createBody); err != nil {
		t.Fatal(err)
	}
	if createBody.Conversation.ID == "" || createBody.Conversation.Title != "Dinner" {
		t.Fatalf("unexpected created conversation: %+v", createBody.Conversation)
	}

	list := performJSONRequest(t, mux, http.MethodGet, "/api/conversations", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), createBody.Conversation.ID) {
		t.Fatalf("list failed: %d %s", list.Code, list.Body.String())
	}

	message := performJSONRequest(t, mux, http.MethodPost, "/api/conversations/"+createBody.Conversation.ID+"/messages", map[string]any{
		"client_id": "client-1",
		"role":      "user",
		"content":   "Plan dinner",
	})
	if message.Code != http.StatusOK || !strings.Contains(message.Body.String(), "Plan dinner") {
		t.Fatalf("message failed: %d %s", message.Code, message.Body.String())
	}

	update := performJSONRequest(t, mux, http.MethodPost, "/api/conversations/"+createBody.Conversation.ID+"/messages", map[string]any{
		"client_id": "client-1",
		"role":      "user",
		"content":   "Plan a quick dinner",
	})
	if update.Code != http.StatusOK {
		t.Fatalf("message update failed: %d %s", update.Code, update.Body.String())
	}

	detail := performJSONRequest(t, mux, http.MethodGet, "/api/conversations/"+createBody.Conversation.ID, nil)
	if detail.Code != http.StatusOK || strings.Count(detail.Body.String(), `"client_id":"client-1"`) != 1 || !strings.Contains(detail.Body.String(), "Plan a quick dinner") {
		t.Fatalf("detail failed: %d %s", detail.Code, detail.Body.String())
	}

	rename := performJSONRequest(t, mux, http.MethodPatch, "/api/conversations/"+createBody.Conversation.ID, map[string]any{"title": "Dinner renamed"})
	if rename.Code != http.StatusOK || !strings.Contains(rename.Body.String(), "Dinner renamed") {
		t.Fatalf("rename failed: %d %s", rename.Code, rename.Body.String())
	}
	bindAccount := performJSONRequest(t, mux, http.MethodPatch, "/api/conversations/"+createBody.Conversation.ID, map[string]any{
		"account_id":               int64(7),
		"upstream_conversation_id": "conv-sticky",
		"gateway_voice_session_id": "vs_sticky",
	})
	if bindAccount.Code != http.StatusOK || !strings.Contains(bindAccount.Body.String(), `"account_id":7`) {
		t.Fatalf("sticky account bind failed: %d %s", bindAccount.Code, bindAccount.Body.String())
	}
	deleteResp := performJSONRequest(t, mux, http.MethodDelete, "/api/conversations/"+createBody.Conversation.ID, nil)
	if deleteResp.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	missing := performJSONRequest(t, mux, http.MethodGet, "/api/conversations/"+createBody.Conversation.ID, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected deleted conversation to be missing, got %d %s", missing.Code, missing.Body.String())
	}
}

func TestConversationManagementAPIRejectsUnknownFields(t *testing.T) {
	_, mux := newAPITestServer(t)
	response := performJSONRequest(t, mux, http.MethodPost, "/api/conversations", map[string]any{"unexpected": true})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d %s", response.Code, response.Body.String())
	}
}

func TestConversationManagementAPIRejectsInvalidRename(t *testing.T) {
	_, mux := newAPITestServer(t)
	create := performJSONRequest(t, mux, http.MethodPost, "/api/conversations", map[string]any{"title": "Dinner"})
	var createBody struct {
		Conversation conversations.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &createBody); err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, mux, http.MethodPatch, "/api/conversations/"+createBody.Conversation.ID, map[string]any{"title": "  "})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected empty title to be rejected, got %d %s", response.Code, response.Body.String())
	}
}

func TestConversationManagementAPIRejectsTrailingJSON(t *testing.T) {
	_, mux := newAPITestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/conversations", bytes.NewBufferString(`{"title":"Dinner"}{"title":"Second"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d %s", response.Code, response.Body.String())
	}
}

func TestConversationManagementAPIRejectsAttachments(t *testing.T) {
	_, mux := newAPITestServer(t)
	create := performJSONRequest(t, mux, http.MethodPost, "/api/conversations", map[string]any{"title": ""})
	var createBody struct {
		Conversation conversations.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &createBody); err != nil {
		t.Fatal(err)
	}
	response := performJSONRequest(t, mux, http.MethodPost, "/api/conversations/"+createBody.Conversation.ID+"/messages", map[string]any{
		"client_id":   "message-with-attachment",
		"role":        "user",
		"content":     "text only",
		"attachments": []any{map[string]any{"name": "removed.png"}},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected attachments to be rejected, got %d %s", response.Code, response.Body.String())
	}
}

func TestImageAndAttachmentRoutesAreRemoved(t *testing.T) {
	_, mux := newAPITestServer(t)
	for _, path := range []string{
		"/api/voice/upload-image",
		"/api/conversations/conversation-id/attachments/attachment-id",
	} {
		response := performJSONRequest(t, mux, http.MethodPost, path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected removed route %s to return 404, got %d", path, response.Code)
		}
	}
}
