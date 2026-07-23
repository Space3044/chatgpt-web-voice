package auth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

// SessionStore persists browser login sessions across process restarts.
// Only the SHA-256 digest of the cookie token is stored.
type SessionStore struct {
	db *store.DB
}

// NewSessionStore wraps the shared SQLite database.
func NewSessionStore(db *store.DB) *SessionStore {
	return &SessionStore{db: db}
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Put inserts or refreshes one session row.
func (s *SessionStore) Put(token, username string, expiresAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	token = strings.TrimSpace(token)
	username = strings.TrimSpace(username)
	if token == "" || username == "" {
		return fmt.Errorf("session token and username are required")
	}
	s.db.Lock()
	defer s.db.Unlock()
	_, err := s.db.Conn().Exec(`
		INSERT INTO auth_sessions (token_hash, username, expires_at, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(token_hash) DO UPDATE SET
			username = excluded.username,
			expires_at = excluded.expires_at`,
		hashSessionToken(token),
		username,
		expiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("persist auth session: %w", err)
	}
	return nil
}

// Get returns a non-expired session for the raw cookie token.
func (s *SessionStore) Get(token string) (username string, expiresAt time.Time, ok bool, err error) {
	if s == nil || s.db == nil {
		return "", time.Time{}, false, nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", time.Time{}, false, nil
	}
	s.db.Lock()
	defer s.db.Unlock()
	var expiresText string
	err = s.db.Conn().QueryRow(
		`SELECT username, expires_at FROM auth_sessions WHERE token_hash = ?`,
		hashSessionToken(token),
	).Scan(&username, &expiresText)
	if err == sql.ErrNoRows {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("load auth session: %w", err)
	}
	expiresAt, err = time.Parse(time.RFC3339, strings.TrimSpace(expiresText))
	if err != nil {
		// Best-effort fallback for SQLite CURRENT_TIMESTAMP style values.
		expiresAt, err = time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(expiresText), time.UTC)
		if err != nil {
			return "", time.Time{}, false, fmt.Errorf("parse auth session expiry: %w", err)
		}
	}
	if !time.Now().Before(expiresAt) {
		_, _ = s.db.Conn().Exec(`DELETE FROM auth_sessions WHERE token_hash = ?`, hashSessionToken(token))
		return "", time.Time{}, false, nil
	}
	return username, expiresAt, true, nil
}

// Delete removes one session by raw cookie token.
func (s *SessionStore) Delete(token string) error {
	if s == nil || s.db == nil {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	s.db.Lock()
	defer s.db.Unlock()
	_, err := s.db.Conn().Exec(`DELETE FROM auth_sessions WHERE token_hash = ?`, hashSessionToken(token))
	if err != nil {
		return fmt.Errorf("delete auth session: %w", err)
	}
	return nil
}

// DeleteExpired purges stale rows.
func (s *SessionStore) DeleteExpired(now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.db.Lock()
	defer s.db.Unlock()
	_, err := s.db.Conn().Exec(
		`DELETE FROM auth_sessions WHERE expires_at <= ?`,
		now.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("purge auth sessions: %w", err)
	}
	return nil
}
