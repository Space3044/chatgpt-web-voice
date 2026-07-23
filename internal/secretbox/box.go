// Package secretbox seals account access tokens for at-rest storage.
// Ciphertext uses AES-256-GCM with a random nonce. Lookups use HMAC-SHA256
// digests so the database never needs the plaintext token as a query key.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// CipherPrefix marks sealed values in SQLite. Anything without this prefix
	// is treated as legacy plaintext during migration.
	CipherPrefix = "enc1."

	keySize   = 32
	nonceSize = 12
)

// ErrInvalidKey is returned when the configured encryption key is unusable.
var ErrInvalidKey = errors.New("invalid token encryption key")

// Box seals and opens access tokens with a process-wide 32-byte key.
type Box struct {
	aead cipher.AEAD
	key  []byte
}

// ParseKey accepts a 32-byte key encoded as hex (64 chars) or standard /
// raw URL-safe base64. Whitespace around the value is ignored.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: empty VOICE_TOKEN_ENCRYPTION_KEY", ErrInvalidKey)
	}
	if key, err := hex.DecodeString(raw); err == nil && len(key) == keySize {
		return key, nil
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if key, err := encoding.DecodeString(raw); err == nil && len(key) == keySize {
			return key, nil
		}
	}
	return nil, fmt.Errorf("%w: expected 32 bytes as hex or base64", ErrInvalidKey)
}

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("%w: key must be %d bytes", ErrInvalidKey, keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	// Copy so callers can zero their buffer without affecting the box.
	owned := make([]byte, keySize)
	copy(owned, key)
	return &Box{aead: aead, key: owned}, nil
}

// Hash returns a keyed digest used for uniqueness checks and preferred-token
// lookups. It never appears in API responses.
func (b *Box) Hash(plaintext string) string {
	mac := hmac.New(sha256.New, b.key)
	_, _ = mac.Write([]byte(plaintext))
	return hex.EncodeToString(mac.Sum(nil))
}

// Seal encrypts a plaintext access token for SQLite storage.
func (b *Box) Seal(plaintext string) (string, error) {
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return "", errors.New("access token is empty")
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return CipherPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a sealed token. Legacy plaintext rows (no enc1. prefix) are
// returned as-is so startup migration can re-seal them.
func (b *Box) Open(stored string) (string, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", errors.New("stored access token is empty")
	}
	if !strings.HasPrefix(stored, CipherPrefix) {
		return stored, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(stored, CipherPrefix))
	if err != nil {
		return "", fmt.Errorf("decode sealed access token: %w", err)
	}
	if len(raw) < nonceSize {
		return "", errors.New("sealed access token is truncated")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plain, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt access token: %w", err)
	}
	return string(plain), nil
}

// IsSealed reports whether the stored value already uses the current format.
func IsSealed(stored string) bool {
	return strings.HasPrefix(strings.TrimSpace(stored), CipherPrefix)
}
