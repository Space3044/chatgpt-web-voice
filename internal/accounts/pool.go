package accounts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

const accountSelectColumns = `
	id, email, access_token, token_hash, device_id, proxy, status, disabled, invalid_at,
	COALESCE(last_used_at, ''), created_at, updated_at`

// Pool is the SQLite-backed account repository. Selection and invalidation are
// serialized through the shared store mutex. Access tokens are sealed at rest
// when a secretbox.Box is configured.
type Pool struct {
	db     *store.DB
	box    *secretbox.Box
	ownsDB bool
}

// NewPool opens a SQLite database and returns an account pool. Prefer
// NewPoolFromDB when multiple domain repositories share one connection.
// Encryption is optional only for unit tests that call NewPool without a box;
// production always supplies a box via NewPoolFromDB.
func NewPool(path string) (*Pool, error) {
	db, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	return &Pool{db: db, ownsDB: true}, nil
}

// NewPoolFromDB wraps an already-opened store database. The caller owns the
// database lifetime and should close store.DB itself.
func NewPoolFromDB(db *store.DB) *Pool {
	return &Pool{db: db, ownsDB: false}
}

// WithBox configures at-rest sealing for access tokens. Call once during
// composition-root setup, then SealStoredTokens to migrate legacy plaintext.
func (p *Pool) WithBox(box *secretbox.Box) *Pool {
	if p != nil {
		p.box = box
	}
	return p
}

// Close releases the underlying SQLite connection when this pool owns it.
func (p *Pool) Close() error {
	if p == nil || p.db == nil || !p.ownsDB {
		return nil
	}
	return p.db.Close()
}

// DB exposes the shared store handle for composition roots that need additional
// repositories on the same connection.
func (p *Pool) DB() *store.DB {
	if p == nil {
		return nil
	}
	return p.db
}

// SealStoredTokens re-seals any legacy plaintext access_token rows and fills
// missing token_hash digests. Safe to call on every startup.
func (p *Pool) SealStoredTokens() (rewritten int, err error) {
	if p == nil || p.box == nil {
		return 0, nil
	}
	p.db.Lock()
	defer p.db.Unlock()
	rows, err := p.db.Conn().Query("SELECT id, access_token, token_hash FROM accounts")
	if err != nil {
		return 0, fmt.Errorf("list accounts for sealing: %w", err)
	}
	defer rows.Close()

	type row struct {
		id     int64
		stored string
		hash   string
	}
	var pending []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.stored, &item.hash); err != nil {
			return 0, fmt.Errorf("read account for sealing: %w", err)
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate accounts for sealing: %w", err)
	}

	for _, item := range pending {
		plain, err := p.box.Open(item.stored)
		if err != nil {
			return rewritten, fmt.Errorf("open account %d access token: %w", item.id, err)
		}
		hash := p.box.Hash(plain)
		sealed := item.stored
		needsSeal := !secretbox.IsSealed(item.stored)
		if needsSeal {
			sealed, err = p.box.Seal(plain)
			if err != nil {
				return rewritten, fmt.Errorf("seal account %d access token: %w", item.id, err)
			}
		}
		if !needsSeal && item.hash == hash {
			continue
		}
		if _, err := p.db.Conn().Exec(
			`UPDATE accounts SET access_token = ?, token_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			sealed, hash, item.id,
		); err != nil {
			return rewritten, fmt.Errorf("persist sealed account %d: %w", item.id, err)
		}
		rewritten++
	}
	return rewritten, nil
}

// List returns all accounts without exposing database handles to callers.
func (p *Pool) List() ([]Account, error) {
	p.db.Lock()
	defer p.db.Unlock()
	rows, err := p.db.Conn().Query("SELECT " + accountSelectColumns + " FROM accounts ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var result []Account
	for rows.Next() {
		account, err := p.scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("read account: %w", err)
		}
		result = append(result, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read accounts: %w", err)
	}
	return result, nil
}

// Get returns one account by database ID.
func (p *Pool) Get(id int64) (Account, error) {
	p.db.Lock()
	defer p.db.Unlock()
	return p.getUnlocked(id)
}

// Create inserts a new account and returns the stored row.
func (p *Pool) Create(account Account) (Account, error) {
	account = normalizeAccount(account)
	if account.AccessToken == "" {
		return Account{}, &Error{Message: "access_token is required"}
	}
	stored, hash, err := p.sealToken(account.AccessToken)
	if err != nil {
		return Account{}, err
	}
	p.db.Lock()
	defer p.db.Unlock()
	if exists, err := p.tokenHashExistsUnlocked(hash, 0); err != nil {
		return Account{}, err
	} else if exists {
		return Account{}, fmt.Errorf("%w: access_token already exists", ErrConflict)
	}
	result, err := p.db.Conn().Exec(`
		INSERT INTO accounts
			(email, access_token, token_hash, device_id, proxy, status, disabled, invalid_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		account.Email, stored, hash, account.DeviceID,
		account.Proxy, account.Status, boolInt(account.Disabled), account.InvalidAt)
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Account{}, fmt.Errorf("read created account ID: %w", err)
	}
	return p.getUnlocked(id)
}

// Update changes an account without requiring secrets to be returned to the
// caller. Nil secret fields preserve their existing values.
func (p *Pool) Update(id int64, update AccountUpdate) (Account, error) {
	p.db.Lock()
	defer p.db.Unlock()
	current, err := p.getUnlocked(id)
	if err != nil {
		return Account{}, err
	}
	if update.AccessToken != nil {
		current.AccessToken = strings.TrimSpace(*update.AccessToken)
	}
	if current.AccessToken == "" {
		return Account{}, &Error{Message: "access_token is required"}
	}
	stored, hash, err := p.sealToken(current.AccessToken)
	if err != nil {
		return Account{}, err
	}
	if exists, err := p.tokenHashExistsUnlocked(hash, id); err != nil {
		return Account{}, err
	} else if exists {
		return Account{}, fmt.Errorf("%w: access_token already exists", ErrConflict)
	}
	current.Email = strings.TrimSpace(update.Email)
	current.DeviceID = strings.TrimSpace(update.DeviceID)
	if update.Proxy != nil {
		current.Proxy = strings.TrimSpace(*update.Proxy)
	}
	current.Status = strings.TrimSpace(update.Status)
	if current.Status == "" {
		current.Status = "正常"
	}
	current.Disabled = update.Disabled || current.Status == "禁用"
	if current.Disabled {
		current.Status = "禁用"
	}
	invalidAt := current.InvalidAt
	if !current.Disabled {
		invalidAt = 0
	}
	_, err = p.db.Conn().Exec(`
		UPDATE accounts SET
			email = ?, access_token = ?, token_hash = ?, device_id = ?, proxy = ?,
			status = ?, disabled = ?, invalid_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		current.Email, stored, hash, current.DeviceID,
		current.Proxy, current.Status, boolInt(current.Disabled), invalidAt, id)
	if err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	return p.getUnlocked(id)
}

// Delete removes an account by database ID.
func (p *Pool) Delete(id int64) error {
	p.db.Lock()
	defer p.db.Unlock()
	result, err := p.db.Conn().Exec("DELETE FROM accounts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted account: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// Stats returns total, available, and disabled account counts.
func (p *Pool) Stats() (PoolStats, error) {
	p.db.Lock()
	defer p.db.Unlock()
	var stats PoolStats
	if err := p.db.Conn().QueryRow("SELECT COUNT(*) FROM accounts").Scan(&stats.Total); err != nil {
		return PoolStats{}, fmt.Errorf("count accounts: %w", err)
	}
	if err := p.db.Conn().QueryRow("SELECT COUNT(*) FROM accounts WHERE disabled = 0 AND status <> '禁用'").Scan(&stats.Available); err != nil {
		return PoolStats{}, fmt.Errorf("count available accounts: %w", err)
	}
	stats.Disabled = stats.Total - stats.Available
	return stats, nil
}

// Upsert inserts or replaces an account keyed by access_token. It is used by
// the explicit legacy migration command.
func (p *Pool) Upsert(account Account) error {
	account = normalizeAccount(account)
	if account.AccessToken == "" {
		return &Error{Message: "access_token is required"}
	}
	stored, hash, err := p.sealToken(account.AccessToken)
	if err != nil {
		return err
	}
	p.db.Lock()
	defer p.db.Unlock()
	var existingID int64
	err = p.db.Conn().QueryRow(
		"SELECT id FROM accounts WHERE token_hash = ?",
		hash,
	).Scan(&existingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = p.db.Conn().Exec(`
			INSERT INTO accounts
				(email, access_token, token_hash, device_id, proxy, status, disabled, invalid_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
			account.Email, stored, hash,
			account.DeviceID, account.Proxy, account.Status, boolInt(account.Disabled), account.InvalidAt)
		if err != nil {
			return fmt.Errorf("upsert account: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("lookup account for upsert: %w", err)
	default:
		_, err = p.db.Conn().Exec(`
			UPDATE accounts SET
				email = ?, access_token = ?, token_hash = ?, device_id = ?, proxy = ?,
				status = ?, disabled = ?, invalid_at = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			account.Email, stored, hash, account.DeviceID, account.Proxy,
			account.Status, boolInt(account.Disabled), account.InvalidAt, existingID)
		if err != nil {
			return fmt.Errorf("upsert account: %w", err)
		}
		return nil
	}
}

// PickByID selects one enabled account by database id. Used to resume a local
// conversation against the same pool account that created the upstream thread.
// Returns Error when the account is missing, disabled, or in the excluded set.
func (p *Pool) PickByID(id int64, excluded map[string]struct{}) (string, Account, error) {
	if id <= 0 {
		return "", Account{}, &Error{Message: "account id is required"}
	}
	p.db.Lock()
	defer p.db.Unlock()
	if excluded == nil {
		excluded = map[string]struct{}{}
	}
	account, err := p.getUnlocked(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", Account{}, &Error{Message: "preferred account is unavailable"}
		}
		return "", Account{}, err
	}
	if account.Disabled || account.Status == "禁用" {
		return "", Account{}, &Error{Message: "preferred account is unavailable"}
	}
	if _, skip := excluded[account.AccessToken]; skip {
		return "", Account{}, &Error{Message: "preferred account is unavailable"}
	}
	if _, err := p.db.Conn().Exec(
		`UPDATE accounts SET last_used_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		account.ID,
	); err != nil {
		return "", Account{}, fmt.Errorf("mark account used: %w", err)
	}
	return account.AccessToken, account, nil
}

// Pick selects an enabled account, preferring the least recently used one.
// A preferred token is only accepted when it already exists in this database;
// callers cannot inject arbitrary upstream credentials.
func (p *Pool) Pick(preferredToken string, excluded map[string]struct{}) (string, Account, error) {
	p.db.Lock()
	defer p.db.Unlock()
	if excluded == nil {
		excluded = map[string]struct{}{}
	}

	var rows *sql.Rows
	var err error
	if preferred := strings.TrimSpace(preferredToken); preferred != "" {
		hash := p.tokenHash(preferred)
		rows, err = p.db.Conn().Query(
			"SELECT "+accountSelectColumns+" FROM accounts WHERE token_hash = ? AND disabled = 0 AND status <> '禁用'",
			hash,
		)
	} else {
		rows, err = p.db.Conn().Query(
			"SELECT " + accountSelectColumns + ` FROM accounts
			WHERE disabled = 0 AND status <> '禁用'
			ORDER BY CASE WHEN last_used_at IS NULL THEN 0 ELSE 1 END, last_used_at, id`,
		)
	}
	if err != nil {
		return "", Account{}, fmt.Errorf("select account: %w", err)
	}
	var selected Account
	found := false
	for rows.Next() {
		account, scanErr := p.scanAccount(rows)
		if scanErr != nil {
			_ = rows.Close()
			return "", Account{}, fmt.Errorf("read selected account: %w", scanErr)
		}
		if _, skip := excluded[account.AccessToken]; skip {
			continue
		}
		selected = account
		found = true
		break
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return "", Account{}, fmt.Errorf("iterate accounts: %w", rowsErr)
	}
	if found {
		if _, err := p.db.Conn().Exec(`UPDATE accounts SET last_used_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, selected.ID); err != nil {
			return "", Account{}, fmt.Errorf("mark account used: %w", err)
		}
		return selected.AccessToken, selected, nil
	}
	if strings.TrimSpace(preferredToken) != "" {
		return "", Account{}, &Error{Message: "preferred account is unavailable"}
	}
	return "", Account{}, &Error{Message: "no available web access_token in sqlite database"}
}

// MarkInvalid disables a token after an upstream 401 and persists the state.
func (p *Pool) MarkInvalid(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	hash := p.tokenHash(token)
	p.db.Lock()
	defer p.db.Unlock()
	_, err := p.db.Conn().Exec(`
		UPDATE accounts
		SET disabled = 1, status = '禁用', invalid_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE token_hash = ?`, float64(time.Now().Unix()), hash)
	if err != nil {
		return fmt.Errorf("mark account invalid: %w", err)
	}
	return nil
}

// AvailableCount returns the number of accounts currently eligible for use.
func (p *Pool) AvailableCount() (int, error) {
	stats, err := p.Stats()
	if err != nil {
		return 0, err
	}
	return stats.Available, nil
}

func (p *Pool) scanAccount(row store.Scanner) (Account, error) {
	var account Account
	var disabled int
	var storedToken, tokenHash string
	if err := row.Scan(
		&account.ID, &account.Email, &storedToken, &tokenHash,
		&account.DeviceID, &account.Proxy, &account.Status, &disabled, &account.InvalidAt,
		&account.LastUsedAt, &account.CreatedAt, &account.UpdatedAt,
	); err != nil {
		return Account{}, err
	}
	plain, err := p.openToken(storedToken)
	if err != nil {
		return Account{}, fmt.Errorf("decrypt account %d access token: %w", account.ID, err)
	}
	account.AccessToken = plain
	account.Disabled = disabled != 0
	return account, nil
}

func (p *Pool) getUnlocked(id int64) (Account, error) {
	row := p.db.Conn().QueryRow("SELECT "+accountSelectColumns+" FROM accounts WHERE id = ?", id)
	account, err := p.scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("get account: %w", err)
	}
	return account, nil
}

func (p *Pool) tokenHashExistsUnlocked(hash string, exceptID int64) (bool, error) {
	var count int
	if err := p.db.Conn().QueryRow(
		"SELECT COUNT(*) FROM accounts WHERE token_hash = ? AND id <> ?",
		hash, exceptID,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("check account token: %w", err)
	}
	return count > 0, nil
}

func (p *Pool) sealToken(plaintext string) (stored, hash string, err error) {
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return "", "", &Error{Message: "access_token is required"}
	}
	if p.box == nil {
		return "", "", errors.New("token encryption key is not configured")
	}
	stored, err = p.box.Seal(plaintext)
	if err != nil {
		return "", "", fmt.Errorf("seal access token: %w", err)
	}
	return stored, p.box.Hash(plaintext), nil
}

func (p *Pool) openToken(stored string) (string, error) {
	if p.box == nil {
		return "", errors.New("token encryption key is not configured")
	}
	return p.box.Open(stored)
}

func (p *Pool) tokenHash(plaintext string) string {
	return p.box.Hash(strings.TrimSpace(plaintext))
}

func normalizeAccount(account Account) Account {
	account.Email = strings.TrimSpace(account.Email)
	account.AccessToken = strings.TrimSpace(account.AccessToken)
	account.DeviceID = strings.TrimSpace(account.DeviceID)
	account.Proxy = strings.TrimSpace(account.Proxy)
	account.Status = strings.TrimSpace(account.Status)
	if account.Status == "" {
		account.Status = "正常"
	}
	account.Disabled = account.Disabled || account.Status == "禁用"
	if account.Disabled {
		account.Status = "禁用"
	}
	if !account.Disabled {
		account.InvalidAt = 0
	}
	return account
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
