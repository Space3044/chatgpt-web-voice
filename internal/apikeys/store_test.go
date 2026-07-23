package apikeys

import (
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func TestStoreLifecycleAndHashedSecret(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/voice.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keys := NewStore(db)

	created, err := keys.Create("Production client")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "vgw_live_") || created.Key.Prefix == "" {
		t.Fatalf("unexpected created key: %+v", created)
	}
	var storedHash string
	if err := db.Conn().QueryRow("SELECT secret_hash FROM api_keys WHERE id = ?", created.Key.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == created.Secret || strings.Contains(storedHash, created.Key.Prefix) {
		t.Fatal("database stored raw API key material")
	}

	authenticated, ok, err := keys.Authenticate(created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || authenticated.ID != created.Key.ID || authenticated.LastUsedAt == "" {
		t.Fatalf("unexpected authentication result: ok=%v key=%+v", ok, authenticated)
	}
	if _, ok, err := keys.Authenticate("vgw_live_invalid"); err != nil || ok {
		t.Fatalf("invalid key authentication: ok=%v err=%v", ok, err)
	}

	name := "Renamed client"
	disabled := false
	updated, err := keys.Update(created.Key.ID, Update{Name: &name, Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Enabled {
		t.Fatalf("unexpected update: %+v", updated)
	}
	if _, ok, err := keys.Authenticate(created.Secret); err != nil || ok {
		t.Fatalf("disabled key authentication: ok=%v err=%v", ok, err)
	}

	stats, err := keys.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Enabled != 0 || stats.Disabled != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if err := keys.Delete(created.Key.ID); err != nil {
		t.Fatal(err)
	}
	if err := keys.Delete(created.Key.ID); err != ErrNotFound {
		t.Fatalf("second delete error=%v", err)
	}
}

func TestStoreRejectsInvalidName(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/voice.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keys := NewStore(db)
	if _, err := keys.Create("   "); err == nil {
		t.Fatal("expected empty name validation error")
	}
}
