package conversations

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

const conversationSelectColumns = `id, owner, title, preview, created_at, updated_at`

// Conversation is one persisted voice/chat workspace owned by an authenticated
// gateway user.
type Conversation struct {
	ID        string    `json:"id"`
	Owner     string    `json:"-"`
	Title     string    `json:"title"`
	Preview   string    `json:"preview"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Messages  []Message `json:"messages,omitempty"`
}

// Message is an idempotently persisted message. ClientID remains stable while a
// streaming caption is updated.
type Message struct {
	ID        int64  `json:"id"`
	ClientID  string `json:"client_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Error is a domain validation error safe to surface to API callers.
type Error = store.Error

// ErrNotFound indicates that a conversation resource does not exist.
var ErrNotFound = store.ErrNotFound

// Store is the SQLite-backed conversation repository.
type Store struct {
	db *store.DB
}

// NewStore wraps an already-opened store database.
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// List returns conversation summaries ordered by most recent activity.
func (s *Store) List(owner string) ([]Conversation, error) {
	owner = normalizeOwner(owner)
	s.db.Lock()
	defer s.db.Unlock()
	rows, err := s.db.Conn().Query(
		"SELECT "+conversationSelectColumns+" FROM conversations WHERE owner = ? ORDER BY updated_at DESC, id DESC",
		owner,
	)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	items := make([]Conversation, 0)
	for rows.Next() {
		item, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("read conversation: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read conversations: %w", err)
	}
	return items, nil
}

// Create creates an empty persisted conversation.
func (s *Store) Create(owner, title string) (Conversation, error) {
	owner = normalizeOwner(owner)
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 120 {
		return Conversation{}, &Error{Message: "conversation title is too long"}
	}
	id := "cv_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	s.db.Lock()
	defer s.db.Unlock()
	if _, err := s.db.Conn().Exec(
		`INSERT INTO conversations (id, owner, title, preview, updated_at)
		 VALUES (?, ?, ?, '', CURRENT_TIMESTAMP)`,
		id, owner, title,
	); err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return s.getUnlocked(owner, id)
}

// UpdateTitle changes the user-visible title of one conversation.
func (s *Store) UpdateTitle(owner, id, title string) (Conversation, error) {
	owner = normalizeOwner(owner)
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	if title == "" {
		return Conversation{}, &Error{Message: "conversation title is required"}
	}
	if len([]rune(title)) > 120 {
		return Conversation{}, &Error{Message: "conversation title is too long"}
	}
	s.db.Lock()
	defer s.db.Unlock()
	result, err := s.db.Conn().Exec(
		`UPDATE conversations
		 SET title = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND owner = ?`,
		title, id, owner,
	)
	if err != nil {
		return Conversation{}, fmt.Errorf("update conversation title: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Conversation{}, fmt.Errorf("check updated conversation: %w", err)
	}
	if count == 0 {
		return Conversation{}, ErrNotFound
	}
	return s.getUnlocked(owner, id)
}

// Delete removes one conversation and its messages.
func (s *Store) Delete(owner, id string) error {
	owner = normalizeOwner(owner)
	id = strings.TrimSpace(id)
	s.db.Lock()
	defer s.db.Unlock()
	result, err := s.db.Conn().Exec(
		"DELETE FROM conversations WHERE id = ? AND owner = ?",
		id, owner,
	)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted conversation: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one conversation and all of its persisted messages.
func (s *Store) Get(owner, id string) (Conversation, error) {
	owner = normalizeOwner(owner)
	id = strings.TrimSpace(id)
	s.db.Lock()
	defer s.db.Unlock()
	conversation, err := s.getUnlocked(owner, id)
	if err != nil {
		return Conversation{}, err
	}
	rows, err := s.db.Conn().Query(`
		SELECT id, client_id, role, content, created_at, updated_at
		FROM conversation_messages
		WHERE conversation_id = ?
		ORDER BY id`, id)
	if err != nil {
		return Conversation{}, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	conversation.Messages = make([]Message, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return Conversation{}, fmt.Errorf("read conversation message: %w", err)
		}
		conversation.Messages = append(conversation.Messages, message)
	}
	if err := rows.Err(); err != nil {
		return Conversation{}, fmt.Errorf("read conversation messages: %w", err)
	}
	return conversation, nil
}

// UpsertMessage inserts a message or updates the same streaming message
// identified by client_id.
func (s *Store) UpsertMessage(owner, conversationID string, message Message) (Message, error) {
	owner = normalizeOwner(owner)
	conversationID = strings.TrimSpace(conversationID)
	message.ClientID = strings.TrimSpace(message.ClientID)
	message.Role = strings.TrimSpace(message.Role)
	message.Content = strings.TrimSpace(message.Content)
	if message.ClientID == "" {
		message.ClientID = "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if len(message.ClientID) > 160 {
		return Message{}, &Error{Message: "message client_id is too long"}
	}
	if message.Role != "user" && message.Role != "assistant" {
		return Message{}, &Error{Message: "message role must be user or assistant"}
	}
	if message.Content == "" {
		return Message{}, &Error{Message: "message content is required"}
	}
	if len(message.Content) > 64<<10 {
		return Message{}, &Error{Message: "message content is too long"}
	}
	s.db.Lock()
	defer s.db.Unlock()
	if _, err := s.getUnlocked(owner, conversationID); err != nil {
		return Message{}, err
	}
	if _, err := s.db.Conn().Exec(`
		INSERT INTO conversation_messages
			(conversation_id, client_id, role, content, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(conversation_id, client_id) DO UPDATE SET
			role = excluded.role,
			content = excluded.content,
			updated_at = CURRENT_TIMESTAMP`,
		conversationID, message.ClientID, message.Role, message.Content,
	); err != nil {
		return Message{}, fmt.Errorf("upsert conversation message: %w", err)
	}
	titleCandidate := truncateText(message.Content, 120)
	preview := truncateText(message.Content, 240)
	if _, err := s.db.Conn().Exec(`
		UPDATE conversations SET
			title = CASE WHEN title = '' THEN ? ELSE title END,
			preview = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND owner = ?`, titleCandidate, preview, conversationID, owner); err != nil {
		return Message{}, fmt.Errorf("update conversation summary: %w", err)
	}
	return s.getMessageUnlocked(conversationID, message.ClientID)
}

// CountMessages returns how many messages remain for a conversation ID. Used by
// cascade-delete tests against the shared store.
func (s *Store) CountMessages(conversationID string) (int, error) {
	s.db.Lock()
	defer s.db.Unlock()
	var count int
	if err := s.db.Conn().QueryRow(
		"SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ?",
		conversationID,
	).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// HasAttachmentSchema reports whether legacy attachment artifacts still exist.
func (s *Store) HasAttachmentSchema() (columnPresent, tablePresent bool, err error) {
	s.db.Lock()
	defer s.db.Unlock()
	var columnCount int
	if err := s.db.Conn().QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('conversation_messages')
		WHERE name = 'attachments_json'`).Scan(&columnCount); err != nil {
		return false, false, err
	}
	var tableCount int
	if err := s.db.Conn().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'conversation_attachments'`).Scan(&tableCount); err != nil {
		return false, false, err
	}
	return columnCount > 0, tableCount > 0, nil
}

func (s *Store) getUnlocked(owner, id string) (Conversation, error) {
	row := s.db.Conn().QueryRow(
		"SELECT "+conversationSelectColumns+" FROM conversations WHERE id = ? AND owner = ?",
		id, owner,
	)
	conversation, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return conversation, nil
}

func (s *Store) getMessageUnlocked(conversationID, clientID string) (Message, error) {
	row := s.db.Conn().QueryRow(`
		SELECT id, client_id, role, content, created_at, updated_at
		FROM conversation_messages
		WHERE conversation_id = ? AND client_id = ?`, conversationID, clientID)
	message, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, fmt.Errorf("get conversation message: %w", err)
	}
	return message, nil
}

func scanConversation(row store.Scanner) (Conversation, error) {
	var conversation Conversation
	if err := row.Scan(
		&conversation.ID, &conversation.Owner, &conversation.Title, &conversation.Preview,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	); err != nil {
		return Conversation{}, err
	}
	return conversation, nil
}

func scanMessage(row store.Scanner) (Message, error) {
	var message Message
	if err := row.Scan(
		&message.ID, &message.ClientID, &message.Role, &message.Content,
		&message.CreatedAt, &message.UpdatedAt,
	); err != nil {
		return Message{}, err
	}
	return message, nil
}

func normalizeOwner(owner string) string {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "default"
	}
	return truncateText(owner, 320)
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
