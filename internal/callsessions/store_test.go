package callsessions

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func TestCallSessionUpsertUpdateAndList(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Session{
		VoiceSessionID: "vs_admin_1",
		Owner:          "admin:root",
		CallerKind:     CallerAdmin,
		CallerLabel:    "admin",
		AccountID:      3,
		Voice:          "cove",
		VoiceMode:      "wingman",
		LanguageCode:   "zh-cn",
		Status:         StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Session{
		VoiceSessionID:         "vs_key_1",
		Owner:                  "api_key:9",
		CallerKind:             CallerAPIKey,
		APIKeyID:               9,
		AccountID:              5,
		UpstreamConversationID: "conv-1",
		Status:                 StatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := s.UpdateUpstream("api_key:9", "vs_key_1", 5, "conv-1", "msg-2", "AAAA-BBBB")
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpstreamParentMessageID != "msg-2" || updated.UpstreamVoiceSessionID != "AAAA-BBBB" {
		t.Fatalf("unexpected upstream update: %+v", updated)
	}

	// Owner isolation on Get.
	if _, err := s.Get("api_key:1", "vs_key_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected owner isolation, got %v", err)
	}
	got, err := s.Get("api_key:9", "vs_key_1")
	if err != nil || got.AccountID != 5 {
		t.Fatalf("get sticky session failed: %+v %v", got, err)
	}

	if err := s.MarkReleased("api_key:9", "vs_key_1"); err != nil {
		t.Fatal(err)
	}
	released, err := s.GetByID("vs_key_1")
	if err != nil || released.Status != StatusReleased || released.ReleasedAt == "" {
		t.Fatalf("expected released session: %+v %v", released, err)
	}

	items, err := s.List(ListFilter{CallerKind: CallerAdmin, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].VoiceSessionID != "vs_admin_1" {
		t.Fatalf("admin filter unexpected: %+v", items)
	}

	stats, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 2 || stats.Admin != 1 || stats.APIKey != 1 || stats.Released != 1 || stats.Active != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	if err := s.Delete("vs_admin_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByID("vs_admin_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted session missing, got %v", err)
	}
}


func TestMarkAllActiveReleasedOnStartup(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Session{
		VoiceSessionID: "vs_alive",
		Owner:          "admin:root",
		CallerKind:     CallerAdmin,
		Status:         StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Session{
		VoiceSessionID: "vs_done",
		Owner:          "admin:root",
		CallerKind:     CallerAdmin,
		Status:         StatusReleased,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.MarkAllActiveReleased()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 orphan released, got %d", n)
	}
	alive, err := s.GetByID("vs_alive")
	if err != nil || alive.Status != StatusReleased || alive.ReleasedAt == "" {
		t.Fatalf("expected alive session released: %+v %v", alive, err)
	}
	done, err := s.GetByID("vs_done")
	if err != nil || done.Status != StatusReleased {
		t.Fatalf("expected already released session unchanged: %+v %v", done, err)
	}
	n, err = s.MarkAllActiveReleased()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected second pass to release 0, got %d", n)
	}
}
