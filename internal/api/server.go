package api

import (
	"encoding/json"
	"net/http"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/conversations"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/voice"
)

// AccountStore is the account-management surface required by HTTP handlers.
type AccountStore interface {
	List() ([]accounts.Account, error)
	Get(id int64) (accounts.Account, error)
	Create(account accounts.Account) (accounts.Account, error)
	Update(id int64, update accounts.AccountUpdate) (accounts.Account, error)
	Delete(id int64) error
	Stats() (accounts.PoolStats, error)
}

// ConversationStore is the conversation persistence surface required by handlers.
type ConversationStore interface {
	List(owner string) ([]conversations.Conversation, error)
	Create(owner, title string) (conversations.Conversation, error)
	Get(owner, id string) (conversations.Conversation, error)
	UpdateTitle(owner, id, title string) (conversations.Conversation, error)
	Delete(owner, id string) error
	UpsertMessage(owner, conversationID string, message conversations.Message) (conversations.Message, error)
}

// VoiceService is the voice-session surface required by handlers.
type VoiceService interface {
	CreateSession(req voice.CreateSessionRequest) (*voice.SessionResult, error)
	ReleaseSession(voiceSessionID string) bool
	ProbeAccountToken(accountID int64) (*voice.ProbeResult, error)
}

// Dependencies wires domain services into the HTTP layer without leaking
// construction details or concrete storage types.
type Dependencies struct {
	Voice         VoiceService
	Accounts      AccountStore
	Conversations ConversationStore
}

// Server holds HTTP handlers for the voice gateway.
type Server struct {
	voice         VoiceService
	accounts      AccountStore
	conversations ConversationStore
}

// New creates an API server from domain dependencies.
func New(deps Dependencies) *Server {
	return &Server{
		voice:         deps.Voice,
		accounts:      deps.Accounts,
		conversations: deps.Conversations,
	}
}

// Register mounts routes on mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/voice/health", s.health)
	mux.HandleFunc("POST /api/voice/session", s.session)
	mux.HandleFunc("POST /api/voice/session/release", s.release)
	mux.HandleFunc("GET /api/accounts", s.listAccounts)
	mux.HandleFunc("POST /api/accounts", s.createAccount)
	mux.HandleFunc("PUT /api/accounts/{id}", s.updateAccount)
	mux.HandleFunc("DELETE /api/accounts/{id}", s.deleteAccount)
	mux.HandleFunc("POST /api/accounts/{id}/check", s.checkAccount)
	mux.HandleFunc("GET /api/conversations", s.listConversations)
	mux.HandleFunc("POST /api/conversations", s.createConversation)
	mux.HandleFunc("GET /api/conversations/{id}", s.getConversation)
	mux.HandleFunc("PATCH /api/conversations/{id}", s.updateConversation)
	mux.HandleFunc("DELETE /api/conversations/{id}", s.deleteConversation)
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.upsertConversationMessage)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
