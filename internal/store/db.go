package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// DB is the process-wide SQLite handle. Domain repositories share one
// connection and serialize access through the embedded mutex so account
// selection, invalidation, and conversation writes cannot interleave unsafely.
type DB struct {
	conn *sql.DB
	mu   sync.Mutex
}

// Open opens or creates a SQLite database and applies the gateway schema.
// Runtime code never reads accounts.json.
func Open(path string) (*DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if !strings.HasPrefix(path, "file:") && path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create database file: %w", err)
		}
		_ = file.Close()
		_ = os.Chmod(path, 0o600)
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the SQLite connection.
func (db *DB) Close() error {
	if db == nil || db.conn == nil {
		return nil
	}
	return db.conn.Close()
}

// Lock serializes repository access across domains that share this database.
func (db *DB) Lock() { db.mu.Lock() }

// Unlock releases the repository mutex.
func (db *DB) Unlock() { db.mu.Unlock() }

// Conn returns the underlying sql.DB. Callers that mutate shared state must
// hold Lock for the duration of the operation.
func (db *DB) Conn() *sql.DB { return db.conn }

// Scanner is satisfied by *sql.Row and *sql.Rows.
type Scanner interface {
	Scan(dest ...any) error
}
