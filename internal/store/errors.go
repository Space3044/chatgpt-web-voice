package store

import "errors"

// Error is a domain validation or availability error safe to surface to API callers.
type Error struct {
	Message string
}

func (e *Error) Error() string { return e.Message }

var (
	// ErrNotFound indicates that a requested resource does not exist.
	ErrNotFound = errors.New("resource not found")
	// ErrConflict indicates a uniqueness or state conflict.
	ErrConflict = errors.New("resource conflict")
)
