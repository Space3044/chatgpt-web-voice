package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/callsessions"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/secretbox"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func TestCallSessionsAdminAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}

	keyStore := apikeys.NewStore(db)
	created, err := keyStore.Create("billing bot")
	if err != nil {
		t.Fatal(err)
	}
	accountPool := accounts.NewPoolFromDB(db).WithBox(box)
	adminAccount, err := accountPool.Create(accounts.Account{Email: "admin-pool@example.com", AccessToken: "token-admin"})
	if err != nil {
		t.Fatal(err)
	}
	keyAccount, err := accountPool.Create(accounts.Account{Email: "key-pool@example.com", AccessToken: "token-key"})
	if err != nil {
		t.Fatal(err)
	}
	records := callsessions.NewStore(db)
	if err := records.Upsert(callsessions.Session{
		VoiceSessionID:         "vs_admin_row",
		Owner:                  "admin:root",
		CallerKind:             callsessions.CallerAdmin,
		CallerLabel:            "admin:root",
		AccountID:              adminAccount.ID,
		UpstreamConversationID: "conv-admin",
		Status:                 callsessions.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := records.Upsert(callsessions.Session{
		VoiceSessionID: "vs_key_row",
		Owner:          "api_key:" + strconv.FormatInt(created.Key.ID, 10),
		CallerKind:     callsessions.CallerAPIKey,
		APIKeyID:       created.Key.ID,
		AccountID:      keyAccount.ID,
		Status:         callsessions.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(Dependencies{Accounts: accountPool, APIKeys: keyStore, CallSessions: records}).Register(mux)

	list := performJSONRequest(t, mux, http.MethodGet, "/api/call-sessions", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var body struct {
		Sessions []map[string]any   `json:"sessions"`
		Stats    callsessions.Stats `json:"stats"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Stats.Total != 2 || len(body.Sessions) != 2 {
		t.Fatalf("unexpected list payload: %+v", body)
	}
	// Admin rows are labeled "admin"; api key rows show the key name.
	foundAdmin, foundKey := false, false
	for _, row := range body.Sessions {
		if row["voice_session_id"] == "vs_admin_row" {
			foundAdmin = true
			if row["caller_label"] != "admin" {
				t.Fatalf("admin label=%v", row["caller_label"])
			}
			if row["account_email"] != "admin-pool@example.com" {
				t.Fatalf("admin account email=%v", row["account_email"])
			}
		}
		if row["voice_session_id"] == "vs_key_row" {
			foundKey = true
			if row["caller_label"] != "billing bot" {
				t.Fatalf("key label=%v", row["caller_label"])
			}
			if row["api_key_prefix"] == nil || row["api_key_prefix"] == "" {
				t.Fatalf("missing api key prefix enrichment: %+v", row)
			}
			if row["account_email"] != "key-pool@example.com" {
				t.Fatalf("key account email=%v", row["account_email"])
			}
		}
		// Must never expose chat content fields.
		for _, forbidden := range []string{"messages", "content", "transcript", "access_token"} {
			if _, ok := row[forbidden]; ok {
				t.Fatalf("leaked %q: %+v", forbidden, row)
			}
		}
	}
	if !foundAdmin || !foundKey {
		t.Fatalf("missing expected rows: %s", list.Body.String())
	}

	del := performJSONRequest(t, mux, http.MethodDelete, "/api/call-sessions/vs_admin_row", nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", del.Code, del.Body.String())
	}
	missing := performJSONRequest(t, mux, http.MethodDelete, "/api/call-sessions/vs_admin_row", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d %s", missing.Code, missing.Body.String())
	}

	filtered := performJSONRequest(t, mux, http.MethodGet, "/api/call-sessions?caller=api_key", nil)
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), "vs_key_row") || strings.Contains(filtered.Body.String(), "vs_admin_row") {
		t.Fatalf("filter failed: %s", filtered.Body.String())
	}
}
