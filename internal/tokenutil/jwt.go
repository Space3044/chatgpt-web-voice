package tokenutil

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenExpiry is the locally decoded JWT expiration without signature verification.
type TokenExpiry struct {
	// Exp is the Unix expiration timestamp when present.
	Exp int64 `json:"exp,omitempty"`
	// ExpiresInSeconds is exp - now. Negative when already expired.
	ExpiresInSeconds int64 `json:"expires_in_seconds"`
	// Expired is true when Exp is in the past.
	Expired bool `json:"expired"`
	// HasExp reports whether the token payload contained a usable exp claim.
	HasExp bool `json:"has_exp"`
}

// ParseAccessTokenExpiry decodes a ChatGPT web access token JWT payload and
// extracts exp. Signature verification is intentionally skipped: this is only
// used for operator-facing expiry display and pre-checks.
func ParseAccessTokenExpiry(token string) (TokenExpiry, error) {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return TokenExpiry{}, fmt.Errorf("token is empty")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return TokenExpiry{}, fmt.Errorf("token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some tokens include padding; retry with standard URL encoding.
		payload, err = base64.URLEncoding.DecodeString(padBase64(parts[1]))
		if err != nil {
			return TokenExpiry{}, fmt.Errorf("decode JWT payload: %w", err)
		}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return TokenExpiry{}, fmt.Errorf("parse JWT payload: %w", err)
	}
	if strings.TrimSpace(claims.Exp.String()) == "" {
		return TokenExpiry{HasExp: false}, nil
	}
	exp, err := claims.Exp.Int64()
	if err != nil {
		return TokenExpiry{}, fmt.Errorf("parse JWT exp: %w", err)
	}
	now := time.Now().Unix()
	return TokenExpiry{
		Exp:              exp,
		ExpiresInSeconds: exp - now,
		Expired:          exp <= now,
		HasExp:           true,
	}, nil
}

func padBase64(value string) string {
	switch len(value) % 4 {
	case 2:
		return value + "=="
	case 3:
		return value + "="
	default:
		return value
	}
}
