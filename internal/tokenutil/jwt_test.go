package tokenutil

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestParseAccessTokenExpiry(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour).Unix()
	token := fakeJWT(map[string]any{"exp": exp, "sub": "user"})
	got, err := ParseAccessTokenExpiry(token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasExp || got.Exp != exp || got.Expired {
		t.Fatalf("unexpected expiry: %+v", got)
	}
	if got.ExpiresInSeconds < 7000 || got.ExpiresInSeconds > 7300 {
		t.Fatalf("expires_in_seconds out of range: %d", got.ExpiresInSeconds)
	}
}

func TestParseAccessTokenExpiryExpired(t *testing.T) {
	exp := time.Now().Add(-time.Minute).Unix()
	token := fakeJWT(map[string]any{"exp": exp})
	got, err := ParseAccessTokenExpiry("Bearer " + token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasExp || !got.Expired || got.ExpiresInSeconds >= 0 {
		t.Fatalf("expected expired token: %+v", got)
	}
}

func TestParseAccessTokenExpiryRejectsNonJWT(t *testing.T) {
	if _, err := ParseAccessTokenExpiry("not-a-jwt"); err == nil {
		t.Fatal("expected non-JWT error")
	}
}

func fakeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}
