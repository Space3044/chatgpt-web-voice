package accounts

import "github.com/dyhhhhhh/chatgpt-web-voice/internal/store"

// Error is returned when no usable account is available or validation fails.
type Error = store.Error

var (
	// ErrNotFound indicates that an account ID does not exist.
	ErrNotFound = store.ErrNotFound
	// ErrConflict indicates that an access token is already assigned.
	ErrConflict = store.ErrConflict
)
