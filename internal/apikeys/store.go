package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

const (
	secretPrefix = "vgw_live_"
	selectFields = `id, name, key_prefix, enabled, COALESCE(last_used_at, ''), created_at, updated_at`
)

// Store persists downstream API keys in the process-wide SQLite database.
// Raw secrets are never stored; authentication uses their SHA-256 digest.
type Store struct {
	db *store.DB
}

// NewStore wraps an already-opened shared database.
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// List returns redacted key metadata ordered by newest first.
func (s *Store) List() ([]Key, error) {
	s.db.Lock()
	defer s.db.Unlock()
	rows, err := s.db.Conn().Query("SELECT " + selectFields + " FROM api_keys ORDER BY id DESC")
	if err != nil {
		return nil, fmt.Errorf("list API keys: %w", err)
	}
	defer rows.Close()
	items := make([]Key, 0)
	for rows.Next() {
		item, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("read API key: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read API keys: %w", err)
	}
	return items, nil
}

// Create generates a cryptographically random credential and stores only its
// digest. The caller must show CreatedKey.Secret once and then discard it.
func (s *Store) Create(name string) (CreatedKey, error) {
	name, err := normalizeName(name)
	if err != nil {
		return CreatedKey{}, err
	}
	secret, prefix, digest, err := generateSecret()
	if err != nil {
		return CreatedKey{}, fmt.Errorf("generate API key: %w", err)
	}
	s.db.Lock()
	defer s.db.Unlock()
	result, err := s.db.Conn().Exec(`
		INSERT INTO api_keys (name, key_prefix, secret_hash, enabled, updated_at)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)`, name, prefix, digest)
	if err != nil {
		return CreatedKey{}, fmt.Errorf("create API key: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return CreatedKey{}, fmt.Errorf("read created API key ID: %w", err)
	}
	key, err := s.getUnlocked(id)
	if err != nil {
		return CreatedKey{}, err
	}
	return CreatedKey{Key: key, Secret: secret}, nil
}

// Update changes an API key name or enabled state.
func (s *Store) Update(id int64, update Update) (Key, error) {
	s.db.Lock()
	defer s.db.Unlock()
	current, err := s.getUnlocked(id)
	if err != nil {
		return Key{}, err
	}
	if update.Name != nil {
		current.Name, err = normalizeName(*update.Name)
		if err != nil {
			return Key{}, err
		}
	}
	if update.Enabled != nil {
		current.Enabled = *update.Enabled
	}
	if _, err := s.db.Conn().Exec(`
		UPDATE api_keys SET name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, current.Name, boolInt(current.Enabled), id); err != nil {
		return Key{}, fmt.Errorf("update API key: %w", err)
	}
	return s.getUnlocked(id)
}

// Delete permanently revokes and removes an API key.
func (s *Store) Delete(id int64) error {
	s.db.Lock()
	defer s.db.Unlock()
	result, err := s.db.Conn().Exec("DELETE FROM api_keys WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete API key: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted API key: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// Authenticate validates a Bearer secret and records successful use. Invalid,
// disabled, or deleted credentials all return ok=false without revealing why.
func (s *Store) Authenticate(secret string) (key Key, ok bool, err error) {
	secret = strings.TrimSpace(secret)
	if !strings.HasPrefix(secret, secretPrefix) || len(secret) <= len(secretPrefix) {
		return Key{}, false, nil
	}
	digest := hashSecret(secret)
	s.db.Lock()
	defer s.db.Unlock()
	row := s.db.Conn().QueryRow(
		"SELECT "+selectFields+" FROM api_keys WHERE secret_hash = ? AND enabled = 1",
		digest,
	)
	key, err = scanKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, false, nil
	}
	if err != nil {
		return Key{}, false, fmt.Errorf("authenticate API key: %w", err)
	}
	if _, err := s.db.Conn().Exec(
		"UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?",
		key.ID,
	); err != nil {
		return Key{}, false, fmt.Errorf("mark API key used: %w", err)
	}
	key, err = s.getUnlocked(key.ID)
	if err != nil {
		return Key{}, false, err
	}
	return key, true, nil
}

// Stats returns total, enabled, and disabled key counts.
func (s *Store) Stats() (Stats, error) {
	s.db.Lock()
	defer s.db.Unlock()
	var stats Stats
	if err := s.db.Conn().QueryRow("SELECT COUNT(*), COALESCE(SUM(enabled), 0) FROM api_keys").Scan(&stats.Total, &stats.Enabled); err != nil {
		return Stats{}, fmt.Errorf("count API keys: %w", err)
	}
	stats.Disabled = stats.Total - stats.Enabled
	return stats, nil
}

// Get returns one API key metadata row by id.
func (s *Store) Get(id int64) (Key, error) {
	s.db.Lock()
	defer s.db.Unlock()
	return s.getUnlocked(id)
}

func (s *Store) getUnlocked(id int64) (Key, error) {
	key, err := scanKey(s.db.Conn().QueryRow("SELECT "+selectFields+" FROM api_keys WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	if err != nil {
		return Key{}, fmt.Errorf("get API key: %w", err)
	}
	return key, nil
}

func scanKey(row store.Scanner) (Key, error) {
	var key Key
	var enabled int
	if err := row.Scan(
		&key.ID, &key.Name, &key.Prefix, &enabled, &key.LastUsedAt,
		&key.CreatedAt, &key.UpdatedAt,
	); err != nil {
		return Key{}, err
	}
	key.Enabled = enabled != 0
	return key, nil
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", &Error{Message: "API key name is required"}
	}
	if !utf8.ValidString(name) || len([]rune(name)) > 120 {
		return "", &Error{Message: "API key name is too long"}
	}
	return name, nil
}

func generateSecret() (secret, prefix, digest string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	secret = secretPrefix + encoded
	prefix = secretPrefix + encoded[:8]
	digest = hashSecret(secret)
	return secret, prefix, digest, nil
}

func hashSecret(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
