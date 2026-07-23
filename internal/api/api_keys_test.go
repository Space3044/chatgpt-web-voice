package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/apikeys"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/store"
)

func TestAPIKeyManagementLifecycle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keyStore := apikeys.NewStore(db)
	mux := http.NewServeMux()
	New(Dependencies{APIKeys: keyStore}).Register(mux)

	createResp := performJSONRequest(t, mux, http.MethodPost, "/api/keys", map[string]any{"name": "Production client"})
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResp.Code, createResp.Body.String())
	}
	var created apikeys.CreatedKey
	if err := json.Unmarshal(createResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Key.ID < 1 || !strings.HasPrefix(created.Secret, "vgw_live_") {
		t.Fatalf("unexpected create response: %+v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	listResp := httptest.NewRecorder()
	mux.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResp.Code, listResp.Body.String())
	}
	if strings.Contains(listResp.Body.String(), created.Secret) || strings.Contains(listResp.Body.String(), "secret_hash") {
		t.Fatalf("list leaked API key material: %s", listResp.Body.String())
	}

	keyPath := "/api/keys/" + strconv.FormatInt(created.Key.ID, 10)
	disabled := false
	updateResp := performJSONRequest(t, mux, http.MethodPatch, keyPath, map[string]any{
		"name":    "Renamed client",
		"enabled": disabled,
	})
	if updateResp.Code != http.StatusOK || !strings.Contains(updateResp.Body.String(), "Renamed client") {
		t.Fatalf("update status=%d body=%s", updateResp.Code, updateResp.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, keyPath, nil)
	deleteResp := httptest.NewRecorder()
	mux.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
}
