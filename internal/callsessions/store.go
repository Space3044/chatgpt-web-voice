package callsessions

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

const (
	// CallerAdmin is the built-in voice page / administrator automation path.
	CallerAdmin = "admin"
	// CallerAPIKey is a downstream Bearer API key path.
	CallerAPIKey = "api_key"

	StatusActive   = "active"
	StatusReleased = "released"
)

const sessionSelectColumns = `
	voice_session_id, owner, caller_kind, caller_label, api_key_id, account_id,
	COALESCE(upstream_conversation_id, ''),
	COALESCE(upstream_parent_message_id, ''),
	COALESCE(upstream_voice_session_id, ''),
	COALESCE(voice, ''), COALESCE(voice_mode, ''), COALESCE(language_code, ''),
	status, created_at, updated_at, last_seen_at, COALESCE(released_at, '')`

// Session is gateway voice-session metadata. It never stores chat content.
type Session struct {
	VoiceSessionID          string `json:"voice_session_id"`
	Owner                   string `json:"owner"`
	CallerKind              string `json:"caller_kind"`
	CallerLabel             string `json:"caller_label"`
	APIKeyID                int64  `json:"api_key_id,omitempty"`
	AccountID               int64  `json:"account_id,omitempty"`
	UpstreamConversationID  string `json:"upstream_conversation_id,omitempty"`
	UpstreamParentMessageID string `json:"upstream_parent_message_id,omitempty"`
	UpstreamVoiceSessionID  string `json:"upstream_voice_session_id,omitempty"`
	Voice                   string `json:"voice,omitempty"`
	VoiceMode               string `json:"voice_mode,omitempty"`
	LanguageCode            string `json:"language_code,omitempty"`
	Status                  string `json:"status"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
	LastSeenAt              string `json:"last_seen_at"`
	ReleasedAt              string `json:"released_at,omitempty"`
}

// ListFilter narrows admin list queries.
type ListFilter struct {
	Query      string
	CallerKind string
	Status     string
	Limit      int
}

// Stats summarizes call-session inventory for the admin page.
type Stats struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Released int `json:"released"`
	Admin    int `json:"admin"`
	APIKey   int `json:"api_key"`
}

// Error is a validation error safe for API callers.
type Error = store.Error

// ErrNotFound indicates a missing call session row.
var ErrNotFound = store.ErrNotFound

// Store is the SQLite-backed call-session metadata repository.
type Store struct {
	db *store.DB
}

// NewStore wraps an already-opened store database.
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// Upsert inserts or refreshes one call-session row after a successful SDP exchange.
func (s *Store) Upsert(item Session) error {
	item = normalizeSession(item)
	if item.VoiceSessionID == "" {
		return &Error{Message: "voice_session_id is required"}
	}
	if item.Owner == "" {
		return &Error{Message: "owner is required"}
	}
	if item.Status == "" {
		item.Status = StatusActive
	}
	s.db.Lock()
	defer s.db.Unlock()
	_, err := s.db.Conn().Exec(`
		INSERT INTO call_sessions (
			voice_session_id, owner, caller_kind, caller_label, api_key_id, account_id,
			upstream_conversation_id, upstream_parent_message_id, upstream_voice_session_id,
			voice, voice_mode, language_code, status,
			created_at, updated_at, last_seen_at, released_at
		) VALUES (
			?, ?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ''
		)
		ON CONFLICT(voice_session_id) DO UPDATE SET
			owner = excluded.owner,
			caller_kind = excluded.caller_kind,
			caller_label = CASE
				WHEN excluded.caller_label <> '' THEN excluded.caller_label
				ELSE call_sessions.caller_label
			END,
			api_key_id = CASE
				WHEN excluded.api_key_id > 0 THEN excluded.api_key_id
				ELSE call_sessions.api_key_id
			END,
			account_id = CASE
				WHEN excluded.account_id > 0 THEN excluded.account_id
				ELSE call_sessions.account_id
			END,
			upstream_conversation_id = CASE
				WHEN excluded.upstream_conversation_id <> '' THEN excluded.upstream_conversation_id
				ELSE call_sessions.upstream_conversation_id
			END,
			upstream_parent_message_id = CASE
				WHEN excluded.upstream_parent_message_id <> '' THEN excluded.upstream_parent_message_id
				ELSE call_sessions.upstream_parent_message_id
			END,
			upstream_voice_session_id = CASE
				WHEN excluded.upstream_voice_session_id <> '' THEN excluded.upstream_voice_session_id
				ELSE call_sessions.upstream_voice_session_id
			END,
			voice = CASE WHEN excluded.voice <> '' THEN excluded.voice ELSE call_sessions.voice END,
			voice_mode = CASE WHEN excluded.voice_mode <> '' THEN excluded.voice_mode ELSE call_sessions.voice_mode END,
			language_code = CASE WHEN excluded.language_code <> '' THEN excluded.language_code ELSE call_sessions.language_code END,
			status = excluded.status,
			updated_at = CURRENT_TIMESTAMP,
			last_seen_at = CURRENT_TIMESTAMP,
			released_at = CASE
				WHEN excluded.status = 'released' THEN CURRENT_TIMESTAMP
				ELSE ''
			END`,
		item.VoiceSessionID, item.Owner, item.CallerKind, item.CallerLabel, item.APIKeyID, item.AccountID,
		item.UpstreamConversationID, item.UpstreamParentMessageID, item.UpstreamVoiceSessionID,
		item.Voice, item.VoiceMode, item.LanguageCode, item.Status,
	)
	if err != nil {
		return fmt.Errorf("upsert call session: %w", err)
	}
	return nil
}

// UpdateUpstream merges continuity fields for an existing session owned by owner.
func (s *Store) UpdateUpstream(owner, voiceSessionID string, accountID int64, upstreamConversationID, upstreamParentMessageID, upstreamVoiceSessionID string) (Session, error) {
	owner = strings.TrimSpace(owner)
	voiceSessionID = strings.TrimSpace(voiceSessionID)
	if voiceSessionID == "" {
		return Session{}, &Error{Message: "voice_session_id is required"}
	}
	s.db.Lock()
	defer s.db.Unlock()
	current, err := s.getUnlocked(owner, voiceSessionID)
	if err != nil {
		return Session{}, err
	}
	if accountID > 0 {
		current.AccountID = accountID
	}
	if v := strings.TrimSpace(upstreamConversationID); v != "" {
		current.UpstreamConversationID = truncate(v, 160)
	}
	if v := strings.TrimSpace(upstreamParentMessageID); v != "" {
		current.UpstreamParentMessageID = truncate(v, 160)
	}
	if v := strings.TrimSpace(upstreamVoiceSessionID); v != "" {
		current.UpstreamVoiceSessionID = truncate(v, 160)
	}
	if current.Status == StatusReleased {
		current.Status = StatusActive
		current.ReleasedAt = ""
	}
	if _, err := s.db.Conn().Exec(`
		UPDATE call_sessions SET
			account_id = ?,
			upstream_conversation_id = ?,
			upstream_parent_message_id = ?,
			upstream_voice_session_id = ?,
			status = ?,
			released_at = ?,
			updated_at = CURRENT_TIMESTAMP,
			last_seen_at = CURRENT_TIMESTAMP
		WHERE voice_session_id = ? AND owner = ?`,
		current.AccountID,
		current.UpstreamConversationID,
		current.UpstreamParentMessageID,
		current.UpstreamVoiceSessionID,
		current.Status,
		current.ReleasedAt,
		voiceSessionID, owner,
	); err != nil {
		return Session{}, fmt.Errorf("update call session upstream: %w", err)
	}
	return s.getUnlocked(owner, voiceSessionID)
}

// MarkAllActiveReleased marks every active call session as released.
// Use on gateway startup: in-memory bindings never survive process restart, so leftover
// active rows would otherwise look live in the admin UI forever.
func (s *Store) MarkAllActiveReleased() (int64, error) {
	s.db.Lock()
	defer s.db.Unlock()
	result, err := s.db.Conn().Exec(`
		UPDATE call_sessions SET
			status = ?,
			released_at = CASE
				WHEN released_at IS NULL OR released_at = '' THEN CURRENT_TIMESTAMP
				ELSE released_at
			END,
			updated_at = CURRENT_TIMESTAMP
		WHERE status = ?`,
		StatusReleased, StatusActive,
	)
	if err != nil {
		return 0, fmt.Errorf("release active call sessions: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count released call sessions: %w", err)
	}
	return n, nil
}

// MarkReleased sets status=released for an owned session. Missing rows are ignored.
func (s *Store) MarkReleased(owner, voiceSessionID string) error {
	owner = strings.TrimSpace(owner)
	voiceSessionID = strings.TrimSpace(voiceSessionID)
	if voiceSessionID == "" {
		return nil
	}
	s.db.Lock()
	defer s.db.Unlock()
	_, err := s.db.Conn().Exec(`
		UPDATE call_sessions SET
			status = ?,
			released_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE voice_session_id = ? AND owner = ? AND status <> ?`,
		StatusReleased, voiceSessionID, owner, StatusReleased,
	)
	if err != nil {
		return fmt.Errorf("mark call session released: %w", err)
	}
	return nil
}

// Get returns one session by owner + voice_session_id.
func (s *Store) Get(owner, voiceSessionID string) (Session, error) {
	s.db.Lock()
	defer s.db.Unlock()
	return s.getUnlocked(strings.TrimSpace(owner), strings.TrimSpace(voiceSessionID))
}

// GetByID returns one session by voice_session_id (admin listing detail).
func (s *Store) GetByID(voiceSessionID string) (Session, error) {
	voiceSessionID = strings.TrimSpace(voiceSessionID)
	s.db.Lock()
	defer s.db.Unlock()
	row := s.db.Conn().QueryRow(
		"SELECT "+sessionSelectColumns+" FROM call_sessions WHERE voice_session_id = ?",
		voiceSessionID,
	)
	item, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get call session: %w", err)
	}
	return item, nil
}

// List returns recent sessions for the administrator page.
func (s *Store) List(filter ListFilter) ([]Session, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	args := make([]any, 0, 4)
	clauses := make([]string, 0, 3)
	if kind := strings.TrimSpace(filter.CallerKind); kind != "" && kind != "all" {
		clauses = append(clauses, "caller_kind = ?")
		args = append(args, kind)
	}
	if status := strings.TrimSpace(filter.Status); status != "" && status != "all" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + q + "%"
		clauses = append(clauses, `(
			voice_session_id LIKE ? OR caller_label LIKE ? OR owner LIKE ? OR
			upstream_conversation_id LIKE ? OR CAST(account_id AS TEXT) LIKE ? OR CAST(api_key_id AS TEXT) LIKE ?
		)`)
		args = append(args, like, like, like, like, like, like)
	}
	query := "SELECT " + sessionSelectColumns + " FROM call_sessions"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY updated_at DESC, voice_session_id DESC LIMIT ?"
	args = append(args, limit)

	s.db.Lock()
	defer s.db.Unlock()
	rows, err := s.db.Conn().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list call sessions: %w", err)
	}
	defer rows.Close()
	items := make([]Session, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("read call session: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate call sessions: %w", err)
	}
	return items, nil
}

// Stats returns inventory counts.
func (s *Store) Stats() (Stats, error) {
	s.db.Lock()
	defer s.db.Unlock()
	var stats Stats
	if err := s.db.Conn().QueryRow(`SELECT COUNT(*) FROM call_sessions`).Scan(&stats.Total); err != nil {
		return Stats{}, fmt.Errorf("count call sessions: %w", err)
	}
	if err := s.db.Conn().QueryRow(`SELECT COUNT(*) FROM call_sessions WHERE status = ?`, StatusActive).Scan(&stats.Active); err != nil {
		return Stats{}, fmt.Errorf("count active call sessions: %w", err)
	}
	if err := s.db.Conn().QueryRow(`SELECT COUNT(*) FROM call_sessions WHERE status = ?`, StatusReleased).Scan(&stats.Released); err != nil {
		return Stats{}, fmt.Errorf("count released call sessions: %w", err)
	}
	if err := s.db.Conn().QueryRow(`SELECT COUNT(*) FROM call_sessions WHERE caller_kind = ?`, CallerAdmin).Scan(&stats.Admin); err != nil {
		return Stats{}, fmt.Errorf("count admin call sessions: %w", err)
	}
	if err := s.db.Conn().QueryRow(`SELECT COUNT(*) FROM call_sessions WHERE caller_kind = ?`, CallerAPIKey).Scan(&stats.APIKey); err != nil {
		return Stats{}, fmt.Errorf("count api_key call sessions: %w", err)
	}
	return stats, nil
}

// Delete permanently removes one metadata row.
func (s *Store) Delete(voiceSessionID string) error {
	voiceSessionID = strings.TrimSpace(voiceSessionID)
	if voiceSessionID == "" {
		return &Error{Message: "voice_session_id is required"}
	}
	s.db.Lock()
	defer s.db.Unlock()
	result, err := s.db.Conn().Exec(`DELETE FROM call_sessions WHERE voice_session_id = ?`, voiceSessionID)
	if err != nil {
		return fmt.Errorf("delete call session: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted call session: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) getUnlocked(owner, voiceSessionID string) (Session, error) {
	if voiceSessionID == "" {
		return Session{}, ErrNotFound
	}
	row := s.db.Conn().QueryRow(
		"SELECT "+sessionSelectColumns+" FROM call_sessions WHERE voice_session_id = ? AND owner = ?",
		voiceSessionID, owner,
	)
	item, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get call session: %w", err)
	}
	return item, nil
}

func scanSession(row store.Scanner) (Session, error) {
	var item Session
	if err := row.Scan(
		&item.VoiceSessionID, &item.Owner, &item.CallerKind, &item.CallerLabel,
		&item.APIKeyID, &item.AccountID,
		&item.UpstreamConversationID, &item.UpstreamParentMessageID, &item.UpstreamVoiceSessionID,
		&item.Voice, &item.VoiceMode, &item.LanguageCode,
		&item.Status, &item.CreatedAt, &item.UpdatedAt, &item.LastSeenAt, &item.ReleasedAt,
	); err != nil {
		return Session{}, err
	}
	return item, nil
}

func normalizeSession(item Session) Session {
	item.VoiceSessionID = strings.TrimSpace(item.VoiceSessionID)
	item.Owner = strings.TrimSpace(item.Owner)
	item.CallerKind = strings.TrimSpace(item.CallerKind)
	item.CallerLabel = truncate(strings.TrimSpace(item.CallerLabel), 120)
	item.UpstreamConversationID = truncate(strings.TrimSpace(item.UpstreamConversationID), 160)
	item.UpstreamParentMessageID = truncate(strings.TrimSpace(item.UpstreamParentMessageID), 160)
	item.UpstreamVoiceSessionID = truncate(strings.TrimSpace(item.UpstreamVoiceSessionID), 160)
	item.Voice = truncate(strings.TrimSpace(item.Voice), 40)
	item.VoiceMode = truncate(strings.TrimSpace(item.VoiceMode), 40)
	item.LanguageCode = truncate(strings.TrimSpace(item.LanguageCode), 40)
	item.Status = strings.TrimSpace(item.Status)
	if item.CallerKind == "" {
		if strings.HasPrefix(item.Owner, "api_key:") {
			item.CallerKind = CallerAPIKey
		} else {
			item.CallerKind = CallerAdmin
		}
	}
	if item.CallerLabel == "" {
		if item.CallerKind == CallerAdmin {
			item.CallerLabel = CallerAdmin
		} else if item.APIKeyID > 0 {
			item.CallerLabel = fmt.Sprintf("api_key:%d", item.APIKeyID)
		}
	}
	return item
}

func truncate(value string, limit int) string {
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
