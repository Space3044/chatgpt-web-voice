package secretbox

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := hex.DecodeString(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestParseKeyAcceptsHexAndBase64(t *testing.T) {
	want := testKey(t)
	hexKey := hex.EncodeToString(want)
	got, err := ParseKey("  " + hexKey + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("hex parse mismatch")
	}
	b64 := base64.StdEncoding.EncodeToString(want)
	key, err := ParseKey(b64)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != string(want) {
		t.Fatalf("base64 parse mismatch")
	}
}

func TestParseKeyRejectsWrongSize(t *testing.T) {
	if _, err := ParseKey("deadbeef"); err == nil {
		t.Fatal("expected short key to fail")
	}
	if _, err := ParseKey(""); err == nil {
		t.Fatal("expected empty key to fail")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	const token = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig"
	sealed, err := box.Seal(token)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) || sealed == token {
		t.Fatalf("expected sealed ciphertext, got %q", sealed)
	}
	// Random nonce => different ciphertext each seal.
	again, err := box.Seal(token)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == again {
		t.Fatal("expected distinct ciphertext for identical plaintext")
	}
	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if opened != token {
		t.Fatalf("opened=%q want %q", opened, token)
	}
	// Hash is stable for lookups.
	if box.Hash(token) == "" || box.Hash(token) != box.Hash(token) {
		t.Fatal("hash should be stable and non-empty")
	}
	if box.Hash(token) == box.Hash(token+"x") {
		t.Fatal("different tokens must not share a hash")
	}
}

func TestOpenLegacyPlaintext(t *testing.T) {
	box, err := New(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Open("legacy-plaintext-token")
	if err != nil || got != "legacy-plaintext-token" {
		t.Fatalf("legacy open: %q %v", got, err)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	box, err := New(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("secret-token")
	if err != nil {
		t.Fatal(err)
	}
	otherKey := make([]byte, 32)
	for i := range otherKey {
		otherKey[i] = byte(i)
	}
	other, err := New(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("expected wrong key to fail decryption")
	}
}
