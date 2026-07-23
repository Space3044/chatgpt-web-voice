package accounts

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"

	_ "modernc.org/sqlite"
)

func stringPointer(value string) *string { return &value }

func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	pool, err := NewPool(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	pool.WithBox(testBox(t))
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func TestPickRejectsPreferredOutsideDatabase(t *testing.T) {
	pool := newTestPool(t)
	_, _, err := pool.Pick("raw-token-xyz", nil)
	if err == nil {
		t.Fatal("expected unknown preferred token to be rejected")
	}
}

func TestPickSkipsDisabledAndRotatesAccounts(t *testing.T) {
	pool := newTestPool(t)
	for _, account := range []Account{
		{Email: "a@x.com", AccessToken: "t1", Status: "禁用"},
		{Email: "b@x.com", AccessToken: "t2", Status: "正常"},
		{Email: "c@x.com", AccessToken: "t3", Status: "正常"},
	} {
		if err := pool.Upsert(account); err != nil {
			t.Fatal(err)
		}
	}

	token, account, err := pool.Pick("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "t2" || account.Email != "b@x.com" {
		t.Fatalf("expected first enabled account, got %q %+v", token, account)
	}
	token, account, err = pool.Pick("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "t3" || account.Email != "c@x.com" {
		t.Fatalf("expected least recently used account, got %q %+v", token, account)
	}
}

func TestMarkInvalidPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.db")
	box := testBox(t)
	pool, err := NewPool(path)
	if err != nil {
		t.Fatal(err)
	}
	pool.WithBox(box)
	if err := pool.Upsert(Account{Email: "a@x.com", AccessToken: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.MarkInvalid("t1"); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}

	pool, err = NewPool(path)
	if err != nil {
		t.Fatal(err)
	}
	pool.WithBox(box)
	defer pool.Close()
	if _, _, err := pool.Pick("", nil); err == nil {
		t.Fatal("expected disabled account to remain unavailable after reopen")
	}
	items, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Disabled || items[0].Status != "禁用" || items[0].InvalidAt == 0 {
		t.Fatalf("invalid persisted state: %+v", items)
	}
}

func TestAccessTokensAreSealedAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.db")
	pool, err := NewPool(path)
	if err != nil {
		t.Fatal(err)
	}
	pool.WithBox(testBox(t))
	defer pool.Close()

	const token = "access-token-plaintext-value"
	if _, err := pool.Create(Account{Email: "a@x.com", AccessToken: token}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.DB().Conn().QueryRow("SELECT access_token FROM accounts LIMIT 1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || !secretbox.IsSealed(stored) {
		t.Fatalf("expected sealed access_token in sqlite, got %q", stored)
	}
	items, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AccessToken != token {
		t.Fatalf("domain layer should decrypt token: %+v", items)
	}
}

func TestSealStoredTokensMigratesLegacyPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.db")
	// Simulate a pre-encryption database row.
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`
		CREATE TABLE accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL DEFAULT '',
			access_token TEXT NOT NULL UNIQUE,
			device_id TEXT NOT NULL DEFAULT '',
			proxy TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '正常',
			disabled INTEGER NOT NULL DEFAULT 0,
			invalid_at REAL NOT NULL DEFAULT 0,
			last_used_at TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO accounts (email, access_token, status) VALUES ('a@x.com', 'legacy-plain-token', '正常');
	`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.Close()

	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pool := NewPoolFromDB(db).WithBox(testBox(t))
	rewritten, err := pool.SealStoredTokens()
	if err != nil {
		t.Fatal(err)
	}
	if rewritten != 1 {
		t.Fatalf("rewritten=%d want 1", rewritten)
	}
	var stored, hash string
	if err := db.Conn().QueryRow("SELECT access_token, token_hash FROM accounts WHERE id = 1").Scan(&stored, &hash); err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(stored) || strings.Contains(stored, "legacy-plain-token") {
		t.Fatalf("token still plaintext: %q", stored)
	}
	if hash == "" {
		t.Fatal("token_hash should be filled after sealing")
	}
	items, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AccessToken != "legacy-plain-token" {
		t.Fatalf("unexpected decrypted account: %+v", items)
	}
	token, _, err := pool.Pick("legacy-plain-token", nil)
	if err != nil || token != "legacy-plain-token" {
		t.Fatalf("preferred pick after seal: token=%q err=%v", token, err)
	}
}

func TestImportLegacyJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	content := `{
		"accounts": [
			{"email":"a@x.com","access_token":"t1","device_id":"d1","status":"正常"},
			{"email":"disabled@x.com","token":"t2","status":"禁用","invalid_at":123.5}
		]
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := ImportJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].AccessToken != "t1" || items[0].DeviceID != "d1" || !items[1].Disabled || items[1].InvalidAt != 123.5 {
		t.Fatalf("unexpected import: %+v", items)
	}
}

func TestAccountCRUDAndStats(t *testing.T) {
	pool := newTestPool(t)
	created, err := pool.Create(Account{
		Email:       "admin@example.com",
		AccessToken: "access-token-123456",
		DeviceID:    "device-1",
		Proxy:       "http://user:password@127.0.0.1:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("missing generated fields: %+v", created)
	}
	if _, err := pool.Create(Account{AccessToken: "access-token-123456"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected token conflict, got %v", err)
	}
	stats, err := pool.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Available != 1 || stats.Disabled != 0 {
		t.Fatalf("unexpected initial stats: %+v", stats)
	}

	updated, err := pool.Update(created.ID, AccountUpdate{
		Email:       "updated@example.com",
		AccessToken: nil,
		DeviceID:    "device-2",
		Proxy:       stringPointer(""),
		Status:      "禁用",
		Disabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != created.AccessToken || updated.Proxy != "" || !updated.Disabled || updated.Status != "禁用" {
		t.Fatalf("unexpected disabled update: %+v", updated)
	}
	stats, err = pool.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Available != 0 || stats.Disabled != 1 {
		t.Fatalf("unexpected disabled stats: %+v", stats)
	}

	replacement := "replacement-access-token"
	updated, err = pool.Update(created.ID, AccountUpdate{
		Email:       updated.Email,
		AccessToken: &replacement,
		DeviceID:    updated.DeviceID,
		Status:      "正常",
		Disabled:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != replacement || updated.Disabled || updated.Status != "正常" || updated.InvalidAt != 0 {
		t.Fatalf("unexpected enabled update: %+v", updated)
	}

	if err := pool.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Get(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted account to be missing, got %v", err)
	}
	if err := pool.Delete(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected second delete to be not found, got %v", err)
	}
}
