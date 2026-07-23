package apikeys

import "github.com/dyhhhhhh/chatgpt-web-voice/internal/store"

// Key is the non-secret metadata stored for one downstream API credential.
type Key struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Enabled    bool   `json:"enabled"`
	LastUsedAt string `json:"last_used_at"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// CreatedKey contains the raw secret exactly once, immediately after creation.
type CreatedKey struct {
	Key    Key    `json:"key"`
	Secret string `json:"secret"`
}

// Update contains mutable key metadata. Nil fields preserve current values.
type Update struct {
	Name    *string
	Enabled *bool
}

// Stats summarizes API key state for the management panel.
type Stats struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
}

// Error is a validation error safe to return to an authenticated administrator.
type Error = store.Error

var (
	// ErrNotFound indicates that an API key ID does not exist.
	ErrNotFound = store.ErrNotFound
)
