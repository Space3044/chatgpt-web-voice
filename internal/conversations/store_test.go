package conversations

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func TestConversationPersistenceAndOwnerIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	conversations := NewStore(db)

	conversation, err := conversations.Create("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.ID == "" || conversation.Title != "" {
		t.Fatalf("unexpected conversation: %+v", conversation)
	}

	message, err := conversations.UpsertMessage("alice", conversation.ID, Message{
		ClientID: "client-1",
		Role:     "user",
		Content:  "hello from sqlite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.ID == 0 || message.ClientID != "client-1" {
		t.Fatalf("unexpected message: %+v", message)
	}
	if _, err := conversations.UpsertMessage("alice", conversation.ID, Message{
		ClientID: "client-1",
		Role:     "user",
		Content:  "updated streaming text",
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := conversations.Get("alice", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "hello from sqlite" || loaded.Preview != "updated streaming text" || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "updated streaming text" {
		t.Fatalf("unexpected loaded conversation: %+v", loaded)
	}
	if _, err := conversations.Get("bob", conversation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected owner isolation, got %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conversations = NewStore(db)
	reopened, err := conversations.Get("alice", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Messages) != 1 || reopened.Messages[0].Content != "updated streaming text" {
		t.Fatalf("conversation did not persist across reopen: %+v", reopened)
	}
}

func TestConversationTitleTruncationPreservesUTF8(t *testing.T) {
	conversations := newTestStore(t)

	conversation, err := conversations.Create("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("会", 160)
	if _, err := conversations.UpsertMessage("alice", conversation.ID, Message{
		ClientID: "client-unicode",
		Role:     "user",
		Content:  content,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := conversations.Get("alice", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(loaded.Title) || utf8.RuneCountInString(loaded.Title) != 120 {
		t.Fatalf("unexpected UTF-8 title: valid=%v runes=%d", utf8.ValidString(loaded.Title), utf8.RuneCountInString(loaded.Title))
	}
}

func TestConversationRenameAndDeleteCascade(t *testing.T) {
	conversations := newTestStore(t)

	conversation, err := conversations.Create("alice", "Original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversations.UpsertMessage("alice", conversation.ID, Message{
		ClientID: "delete-me",
		Role:     "assistant",
		Content:  "this should be deleted",
	}); err != nil {
		t.Fatal(err)
	}

	renamed, err := conversations.UpdateTitle("alice", conversation.ID, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Renamed" || renamed.Preview != "this should be deleted" {
		t.Fatalf("unexpected renamed conversation: %+v", renamed)
	}
	if _, err := conversations.UpdateTitle("alice", conversation.ID, " "); err == nil {
		t.Fatal("expected empty title to be rejected")
	}

	if err := conversations.Delete("bob", conversation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected owner isolation on delete, got %v", err)
	}
	if err := conversations.Delete("alice", conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := conversations.Get("alice", conversation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted conversation to be unavailable, got %v", err)
	}
	messageCount, err := conversations.CountMessages(conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 {
		t.Fatalf("conversation messages were not cascaded: %d", messageCount)
	}
}

func TestConversationSchemaExcludesAttachments(t *testing.T) {
	conversations := newTestStore(t)
	columnPresent, tablePresent, err := conversations.HasAttachmentSchema()
	if err != nil {
		t.Fatal(err)
	}
	if columnPresent || tablePresent {
		t.Fatalf("attachment schema still exists: column=%v table=%v", columnPresent, tablePresent)
	}
}

func TestConversationStoreSupportsLegacyAttachmentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE conversations (
			id TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			preview TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE conversation_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			client_id TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
			content TEXT NOT NULL,
			attachments_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(conversation_id, client_id)
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	storeDB, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeDB.Close()
	conversations := NewStore(storeDB)
	conversation, err := conversations.Create("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversations.UpsertMessage("alice", conversation.ID, Message{
		ClientID: "legacy-schema-message",
		Role:     "user",
		Content:  "text remains writable",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := conversations.Get("alice", conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "text remains writable" {
		t.Fatalf("legacy schema compatibility failed: %+v", loaded)
	}
}
