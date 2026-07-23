package store

import "fmt"

func (db *DB) migrate() error {
	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		`CREATE TABLE IF NOT EXISTS accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL DEFAULT '',
			access_token TEXT NOT NULL UNIQUE,
			refresh_token TEXT NOT NULL DEFAULT '',
			device_id TEXT NOT NULL DEFAULT '',
			proxy TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '正常',
			disabled INTEGER NOT NULL DEFAULT 0,
			invalid_at REAL NOT NULL DEFAULT 0,
			last_used_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		"CREATE INDEX IF NOT EXISTS idx_accounts_available ON accounts(disabled, status, last_used_at, id)",
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			preview TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		"CREATE INDEX IF NOT EXISTS idx_conversations_owner_updated ON conversations(owner, updated_at DESC, id DESC)",
		`CREATE TABLE IF NOT EXISTS conversation_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			client_id TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
			content TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(conversation_id, client_id)
		)`,
		"CREATE INDEX IF NOT EXISTS idx_conversation_messages_conversation ON conversation_messages(conversation_id, id)",
	} {
		if _, err := db.conn.Exec(statement); err != nil {
			return fmt.Errorf("initialize sqlite database: %w", err)
		}
	}
	return nil
}
