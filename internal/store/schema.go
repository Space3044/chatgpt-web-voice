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
			access_token TEXT NOT NULL,
			token_hash TEXT NOT NULL DEFAULT '',
			proxy TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '正常',
			disabled INTEGER NOT NULL DEFAULT 0,
			invalid_at REAL NOT NULL DEFAULT 0,
			last_used_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		"CREATE INDEX IF NOT EXISTS idx_accounts_available ON accounts(disabled, status, last_used_at, id)",
		`CREATE TABLE IF NOT EXISTS api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			secret_hash TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_used_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		"CREATE INDEX IF NOT EXISTS idx_api_keys_enabled ON api_keys(enabled, id)",
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			title_locked INTEGER NOT NULL DEFAULT 0,
			preview TEXT NOT NULL DEFAULT '',
			account_id INTEGER NOT NULL DEFAULT 0,
			upstream_conversation_id TEXT NOT NULL DEFAULT '',
			upstream_parent_message_id TEXT NOT NULL DEFAULT '',
			upstream_voice_session_id TEXT NOT NULL DEFAULT '',
			gateway_voice_session_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		"CREATE INDEX IF NOT EXISTS idx_conversations_owner_updated ON conversations(owner, updated_at DESC, id DESC)",
		// call_sessions: gateway voice-session metadata only (no chat content).
		// Used for admin visibility and sticky account resume after restarts.
		`CREATE TABLE IF NOT EXISTS call_sessions (
			voice_session_id TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			caller_kind TEXT NOT NULL DEFAULT '',
			caller_label TEXT NOT NULL DEFAULT '',
			api_key_id INTEGER NOT NULL DEFAULT 0,
			account_id INTEGER NOT NULL DEFAULT 0,
			upstream_conversation_id TEXT NOT NULL DEFAULT '',
			upstream_parent_message_id TEXT NOT NULL DEFAULT '',
			upstream_voice_session_id TEXT NOT NULL DEFAULT '',
			voice TEXT NOT NULL DEFAULT '',
			voice_mode TEXT NOT NULL DEFAULT '',
			language_code TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			released_at TEXT NOT NULL DEFAULT ''
		)`,
		"CREATE INDEX IF NOT EXISTS idx_call_sessions_updated ON call_sessions(updated_at DESC, voice_session_id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_call_sessions_owner ON call_sessions(owner, updated_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_call_sessions_account ON call_sessions(account_id, updated_at DESC)",
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
		// Browser login sessions survive process restarts (token hash only).
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			token_hash TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		"CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at)",
	} {
		if _, err := db.conn.Exec(statement); err != nil {
			return fmt.Errorf("initialize sqlite database: %w", err)
		}
	}
	// Existing deployments may still have refresh_token from earlier schema.
	// The gateway never used it; drop the column when present.
	if err := db.dropColumnIfExists("accounts", "refresh_token"); err != nil {
		return err
	}
	// device_id is process-global fingerprint now; drop per-account column.
	if err := db.dropColumnIfExists("accounts", "device_id"); err != nil {
		return err
	}
	// access_token is now sealed ciphertext; uniqueness is enforced on token_hash.
	if err := db.ensureAccountsTokenHash(); err != nil {
		return err
	}
	if err := db.ensureConversationUpstreamColumns(); err != nil {
		return err
	}
	return nil
}

// ensureConversationUpstreamColumns adds optional chatgpt.com continuity
// fields to conversations for existing deployments created before resume support.
func (db *DB) ensureConversationUpstreamColumns() error {
	for _, column := range []string{
		"upstream_conversation_id",
		"upstream_parent_message_id",
		"upstream_voice_session_id",
		"gateway_voice_session_id",
	} {
		has, err := db.hasColumn("conversations", column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		statement := fmt.Sprintf(
			`ALTER TABLE conversations ADD COLUMN %s TEXT NOT NULL DEFAULT ''`,
			column,
		)
		if _, err := db.conn.Exec(statement); err != nil {
			return fmt.Errorf("add conversations.%s: %w", column, err)
		}
	}
	// Sticky pool account for upstream resume. 0 means "not bound yet".
	hasAccount, err := db.hasColumn("conversations", "account_id")
	if err != nil {
		return err
	}
	if !hasAccount {
		if _, err := db.conn.Exec(
			`ALTER TABLE conversations ADD COLUMN account_id INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("add conversations.account_id: %w", err)
		}
	}
	// title_locked: user manually renamed; hangup must not overwrite with upstream title.
	hasTitleLocked, err := db.hasColumn("conversations", "title_locked")
	if err != nil {
		return err
	}
	if !hasTitleLocked {
		if _, err := db.conn.Exec(
			`ALTER TABLE conversations ADD COLUMN title_locked INTEGER NOT NULL DEFAULT 0`,
		); err != nil {
			return fmt.Errorf("add conversations.title_locked: %w", err)
		}
	}
	return nil
}

// ensureAccountsTokenHash adds the lookup digest column and unique index used
// after access tokens are sealed at rest. Existing rows keep an empty hash
// until the account pool re-seals them on first open with an encryption key.
func (db *DB) ensureAccountsTokenHash() error {
	hasColumn, err := db.hasColumn("accounts", "token_hash")
	if err != nil {
		return err
	}
	if !hasColumn {
		if _, err := db.conn.Exec(`ALTER TABLE accounts ADD COLUMN token_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add accounts.token_hash: %w", err)
		}
	}
	if _, err := db.conn.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_token_hash ON accounts(token_hash) WHERE token_hash <> ''`); err != nil {
		return fmt.Errorf("create accounts.token_hash index: %w", err)
	}
	// Legacy databases enforced UNIQUE on the plaintext access_token column.
	// Ciphertext is non-deterministic, so that constraint must be removed.
	if err := db.dropAccountsAccessTokenUniqueIfPresent(); err != nil {
		return err
	}
	return nil
}

func (db *DB) dropAccountsAccessTokenUniqueIfPresent() error {
	// SQLite names auto-indexes like sqlite_autoindex_accounts_1 for UNIQUE columns.
	// Rebuild the table only when a UNIQUE constraint still covers access_token.
	rows, err := db.conn.Query(`PRAGMA index_list(accounts)`)
	if err != nil {
		return fmt.Errorf("list accounts indexes: %w", err)
	}
	defer rows.Close()

	type indexInfo struct {
		name   string
		unique int
		origin string
	}
	var indexes []indexInfo
	for rows.Next() {
		var seq, partial int
		var info indexInfo
		if err := rows.Scan(&seq, &info.name, &info.unique, &info.origin, &partial); err != nil {
			return fmt.Errorf("read accounts index: %w", err)
		}
		indexes = append(indexes, info)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate accounts indexes: %w", err)
	}

	needsRebuild := false
	for _, info := range indexes {
		if info.unique == 0 {
			continue
		}
		cols, err := db.indexColumns(info.name)
		if err != nil {
			return err
		}
		if len(cols) == 1 && cols[0] == "access_token" {
			needsRebuild = true
			break
		}
	}
	if !needsRebuild {
		return nil
	}
	return db.rebuildAccountsWithoutAccessTokenUnique()
}

func (db *DB) indexColumns(indexName string) ([]string, error) {
	rows, err := db.conn.Query(fmt.Sprintf("PRAGMA index_info(%s)", quoteIdent(indexName)))
	if err != nil {
		return nil, fmt.Errorf("inspect index %s: %w", indexName, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return nil, fmt.Errorf("read index column for %s: %w", indexName, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate index columns for %s: %w", indexName, err)
	}
	return cols, nil
}

func (db *DB) rebuildAccountsWithoutAccessTokenUnique() error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin accounts rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	statements := []string{
		`CREATE TABLE accounts_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL DEFAULT '',
			access_token TEXT NOT NULL,
			token_hash TEXT NOT NULL DEFAULT '',
			proxy TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '正常',
			disabled INTEGER NOT NULL DEFAULT 0,
			invalid_at REAL NOT NULL DEFAULT 0,
			last_used_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO accounts_new (
			id, email, access_token, token_hash, proxy, status, disabled,
			invalid_at, last_used_at, created_at, updated_at
		)
		SELECT
			id, email, access_token, COALESCE(token_hash, ''), proxy, status, disabled,
			invalid_at, last_used_at, created_at, updated_at
		FROM accounts`,
		`DROP TABLE accounts`,
		`ALTER TABLE accounts_new RENAME TO accounts`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_available ON accounts(disabled, status, last_used_at, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_token_hash ON accounts(token_hash) WHERE token_hash <> ''`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("rebuild accounts table: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit accounts rebuild: %w", err)
	}
	return nil
}

func (db *DB) dropColumnIfExists(table, column string) error {
	found, err := db.hasColumn(table, column)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err := db.conn.Exec(fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)); err != nil {
		return fmt.Errorf("drop column %s.%s: %w", table, column, err)
	}
	return nil
}

func (db *DB) hasColumn(table, column string) (bool, error) {
	rows, err := db.conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("read table info for %s: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate table info for %s: %w", table, err)
	}
	return false, nil
}

// quoteIdent wraps a SQLite identifier that is already known to come from
// PRAGMA output, not from user input.
func quoteIdent(name string) string {
	return `"` + name + `"`
}
