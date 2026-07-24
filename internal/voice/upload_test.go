package voice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/accounts"
	"github.com/dyhhhhhh/chatgpt-web-voice/internal/config"
)

func TestNormalizeImageUploadRequest(t *testing.T) {
	ok, err := normalizeImageUploadRequest(ImageUploadRequest{
		FileName: `..\evil\photo.JPG`,
		FileSize: 1024,
		MimeType: "image/jpeg",
		Width:    100,
		Height:   80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok.FileName != "photo.JPG" || ok.MimeType != "image/jpeg" {
		t.Fatalf("unexpected normalized meta: %+v", ok)
	}

	for _, tc := range []ImageUploadRequest{
		{FileName: "a.png", FileSize: 0, MimeType: "image/png"},
		{FileName: "a.png", FileSize: maxImageUploadBytes + 1, MimeType: "image/png"},
		{FileName: "a.bin", FileSize: 10, MimeType: "application/octet-stream"},
		{FileName: "a.png", FileSize: 10, MimeType: "image/png", Width: -1},
	} {
		if _, err := normalizeImageUploadRequest(tc); err == nil {
			t.Fatalf("expected validation error for %+v", tc)
		}
	}
}

func TestCreateImageUploadCredentialUsesBoundSessionAccount(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/files" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"file_id":"file-abc123",
			"upload_url":"https://blob.example/sas?sig=1"
		}`))
	}))
	t.Cleanup(upstream.Close)

	svc := New(config.Config{SessionTTLSeconds: 3600}, nil, nil)
	svc.filesAPIURL = upstream.URL + "/backend-api/files"
	sessionID := svc.bindVoiceSession("api_key:1", "", "token-secret", accounts.Account{ID: 42}, UpstreamContext{})

	cred, err := svc.CreateImageUploadCredential("api_key:1", sessionID, ImageUploadRequest{
		FileName: "shot.png",
		FileSize: 2048,
		MimeType: "image/png",
		Width:    640,
		Height:   480,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer token-secret" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotBody["file_name"] != "shot.png" || gotBody["use_case"] != "multimodal" {
		t.Fatalf("unexpected files payload: %+v", gotBody)
	}
	if cred.FileID != "file-abc123" ||
		cred.UploadURL != "https://blob.example/sas?sig=1" ||
		cred.UploadMethod != http.MethodPut ||
		cred.AssetPointer != "sediment://file-abc123" ||
		cred.VoiceSessionID != sessionID ||
		cred.RequiredHeaders["x-ms-blob-type"] != "BlockBlob" {
		t.Fatalf("unexpected credential: %+v", cred)
	}

	if _, err := svc.CreateImageUploadCredential("api_key:2", sessionID, ImageUploadRequest{
		FileName: "shot.png", FileSize: 10, MimeType: "image/png",
	}); err == nil {
		t.Fatal("expected foreign owner rejection")
	}
	if _, err := svc.CreateImageUploadCredential("api_key:1", "vs_missing", ImageUploadRequest{
		FileName: "shot.png", FileSize: 10, MimeType: "image/png",
	}); err == nil {
		t.Fatal("expected missing session rejection")
	}
}

func TestCreateImageUploadCredentialRequiresLiveBinding(t *testing.T) {
	svc := New(config.Config{SessionTTLSeconds: 3600}, nil, nil)
	sessionID := svc.bindVoiceSession("api_key:1", "", "token", accounts.Account{ID: 1}, UpstreamContext{})
	if !svc.ReleaseSession("api_key:1", sessionID) {
		t.Fatal("release failed")
	}
	_, err := svc.CreateImageUploadCredential("api_key:1", sessionID, ImageUploadRequest{
		FileName: "a.png", FileSize: 10, MimeType: "image/png",
	})
	se, ok := err.(*ServiceError)
	if !ok || se.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after release, got %T %v", err, err)
	}
}

func TestCompleteImageUploadUsesBoundSessionAccount(t *testing.T) {
	var paths []string
	var auths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		if strings.HasSuffix(r.URL.Path, "/uploaded") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	svc := New(config.Config{SessionTTLSeconds: 3600}, nil, nil)
	svc.filesAPIURL = upstream.URL + "/backend-api/files"
	sessionID := svc.bindVoiceSession("api_key:1", "", "token-secret", accounts.Account{ID: 7}, UpstreamContext{})

	result, err := svc.CompleteImageUpload("api_key:1", sessionID, "file-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || result.FileID != "file-xyz" || result.AssetPointer != "sediment://file-xyz" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/file-xyz/uploaded") {
		t.Fatalf("paths=%v", paths)
	}
	if len(auths) != 1 || auths[0] != "Bearer token-secret" {
		t.Fatalf("auths=%v", auths)
	}
	if _, err := svc.CompleteImageUpload("api_key:2", sessionID, "file-xyz"); err == nil {
		t.Fatal("expected foreign owner rejection")
	}
}

func TestCompleteImageUploadFallsBackToProcessStream(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/uploaded") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"missing"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/process_upload_stream") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	svc := New(config.Config{SessionTTLSeconds: 3600}, nil, nil)
	svc.filesAPIURL = upstream.URL + "/backend-api/files"
	sessionID := svc.bindVoiceSession("api_key:1", "", "token", accounts.Account{ID: 1}, UpstreamContext{})
	result, err := svc.CompleteImageUpload("api_key:1", sessionID, "file-fb")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed {
		t.Fatalf("expected completed: %+v", result)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%v", paths)
	}
}
